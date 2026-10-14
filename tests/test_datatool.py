"""Mocked DataTool contracts plus real harmless child-process lifecycle tests.

These tests do not extract Overwatch assets. The subprocess is a Python fixture
which exercises Windows argument transport, stdout/stderr and child cancellation.
"""
import ctypes
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

from owpreview.datatool import (CATEGORIES, DataToolError, DataToolJobs,
                               build_extract_args, build_list_args, parse_catalog,
                               validate_paths)
from owpreview import datatool


class DataToolTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.game = self.root / "Overwatch 安装 with spaces"
        (self.game / "data").mkdir(parents=True)
        (self.game / ".build.info").write_text("Product!STRING:0|Version!STRING:0\npro|2.0\n")
        self.output = self.root / "Export 输出 files"
        self.output.mkdir()
        self.exe = self.root / "runtime complete" / "DataTool.exe"
        self.exe.parent.mkdir()
        self.exe.write_bytes(b"fixture placeholder; never executed")
        self.script = self.exe.parent / "fake_datatool.py"
        self.script.write_text('''
import json, os, pathlib, subprocess, sys, time
args = sys.argv[1:]
mode = args[1]
print(json.dumps({"argv":args,"cwd":os.getcwd(),"override":os.environ.get("DATATOOL_OVERWATCH_DIR")},ensure_ascii=False),flush=True)
if mode.startswith("list-"):
    destination = next(a[6:] for a in args if a.startswith("--out="))
    if mode == "list-heroes":
        value = {"001.075":{"Name":"Lúcio","IsHero":True}}
    else:
        value = {"Lúcio":[{"Name":"Overwatch Classic","Type":"Skin","GUID":"002.0A5"},{"Name":"Dance","Type":"Emote","GUID":"003.0A5"}]}
    pathlib.Path(destination).write_text(json.dumps(value),encoding="utf-8")
elif "FailCode" in args[3]:
    print("fixture extraction error",file=sys.stderr,flush=True)
    sys.exit(7)
elif "FailZero" in args[3]:
    print('[Query] Found nothing matching your query of "bad"',file=sys.stderr,flush=True)
elif "Unsupported" in args[3]:
    print('[CASC] This version of DataTool does not support this version of Overwatch.',file=sys.stderr,flush=True)
    sys.exit(1)
elif "NoMarker" in args[3]:
    sys.exit(0)
elif "ParentExit" in args[3]:
    child=subprocess.Popen([sys.executable,"-c","import time;time.sleep(60)"])
    print("CHILD="+str(child.pid),flush=True)
elif "Wait" in args[3]:
    child=subprocess.Popen([sys.executable,"-c","import time;time.sleep(60)"])
    print("CHILD="+str(child.pid),flush=True)
    time.sleep(60)
else:
    pathlib.Path(args[2],"fixture.owanimclip").write_text("fixture only")
print("[Core] Execution finished in 00:00:00.01",flush=True)
''', encoding="utf-8")
        self.calls = []
        self.processes = []
        def factory(args, **kwargs):
            self.calls.append((args, kwargs.copy()))
            env = kwargs["env"].copy()
            env["PYTHONIOENCODING"] = "utf-8"
            kwargs["env"] = env
            process = subprocess.Popen([sys.executable, "-u", str(self.script), *args[1:]], **kwargs)
            self.processes.append(process)
            return process
        self.shared_lock = threading.Lock()
        self.manager = DataToolJobs(self.root / "job state", self.shared_lock, process_factory=factory)
        self.addCleanup(self.manager.close)

    def wait(self, manager=None):
        manager = manager or self.manager
        deadline = time.monotonic() + 12
        while manager.snapshot()["busy"] and time.monotonic() < deadline:
            time.sleep(.02)
        result = manager.snapshot()
        self.assertFalse(result["busy"], result)
        return result

    def seed(self, names=("Overwatch Classic",)):
        self.manager._catalog = {"install": str(self.game), "heroes": [{"id": "001", "name": "Lúcio"}],
                                 "unlocks": {"Lúcio": [{"id": str(i), "name": name, "category": "skin"} for i, name in enumerate(names)]}}

    def export(self, asset="Overwatch Classic", **options):
        self.seed([asset])
        return self.manager.start_export(self.exe, self.game, self.output,
                                         {"hero": "Lúcio", "asset": asset, "category": "skin", **options})

    def wait_for_child(self):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            for line in self.manager.snapshot()["logs"]:
                if line["text"].startswith("CHILD="):
                    return int(line["text"].split("=")[1])
            time.sleep(.02)
        self.fail(f"No child launched: {self.manager.snapshot()}")

    def assert_process_ended(self, pid):
        if os.name != "nt":
            return
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = [ctypes.c_ulong, ctypes.c_int, ctypes.c_ulong]
        kernel.OpenProcess.restype = ctypes.c_void_p
        kernel.GetExitCodeProcess.argtypes = [ctypes.c_void_p, ctypes.POINTER(ctypes.c_ulong)]
        kernel.CloseHandle.argtypes = [ctypes.c_void_p]
        handle = kernel.OpenProcess(0x1000, 0, pid)
        if handle:
            exit_code = ctypes.c_ulong()
            kernel.GetExitCodeProcess(handle, ctypes.byref(exit_code))
            kernel.CloseHandle(handle)
            self.assertNotEqual(exit_code.value, 259, "Managed process is still alive")

    def test_verified_commands_keep_literal_filter_in_single_argument(self):
        args = build_extract_args(self.game, self.output, {"hero": "Lúcio", "asset": "Overwatch Classic", "category": "skin"})
        self.assertEqual(args[:4], [str(self.game), "extract-unlocks", str(self.output), "Lúcio|skin=Overwatch Classic"])
        self.assertIn("--online=false", args)
        self.assertIn("--convert-animations-type=owanimclip", args)
        self.assertIn("--convert-textures-type=png", args)
        self.assertIn("--skip-animations=false", args)
        self.assertIn("--skip-sound=true", args)
        for category in CATEGORIES:
            args = build_extract_args(self.game, self.output, {"hero": "*", "asset": "*", "category": category["id"]})
            self.assertEqual(args[1], "extract-unlocks")
            self.assertEqual(args[3], f"*|{category['id']}=*")

    def test_explicit_options_and_query_metacharacter_rejection(self):
        args = build_extract_args(self.game, self.output, {"hero": "Lúcio", "asset": "*", "category": "skin", "includeAnimations": False, "includeTextures": False, "includeSound": True, "allLods": True})
        for argument in ("--skip-animations=true", "--skip-textures=true", "--skip-sound=false", "--all-lods=true"):
            self.assertIn(argument, args)
        for invalid in ("bad|emote=*", "bad,name", 'bad"name', "!skin", "prefix*", ""):
            with self.assertRaises(ValueError):
                build_extract_args(self.game, self.output, {"hero": "Lúcio", "asset": invalid})
        with self.assertRaises(ValueError):
            build_extract_args(self.game, self.output, {"hero": "*", "asset": "*", "category": "invented"})

    def test_list_json_file_arguments(self):
        dest = self.root / "目录 with spaces" / "unlocks.json"
        args = build_list_args(self.game, "unlocks", dest)
        self.assertEqual(args[1], "list-unlocks")
        self.assertIn(f"--out={dest}", args)
        self.assertIn("--flatten", args)
        self.assertIn("--online=false", args)

    def test_install_validation_accepts_battlenet_and_steam_without_writing_game(self):
        before = {p.relative_to(self.game): p.read_bytes() for p in self.game.rglob("*") if p.is_file()}
        self.assertTrue(validate_paths(self.game, self.output)["ok"])
        after = {p.relative_to(self.game): p.read_bytes() for p in self.game.rglob("*") if p.is_file()}
        self.assertEqual(before, after)
        (self.game / ".build.info").unlink()
        (self.game / "data" / ".build.config").write_text("build-name = fixture")
        self.assertTrue(validate_paths(self.game)["ok"])

    def test_validation_errors_and_retail_normalization(self):
        for bad in ("", "relative", self.root / "missing", self.output):
            self.assertIn("overwatchInstall", validate_paths(bad)["errors"])
        self.assertIn("outputDirectory", validate_paths(self.game, self.game / "Exports")["errors"])
        output_file = self.output / "file.txt"
        output_file.write_text("existing")
        self.assertIn("outputDirectory", validate_paths(self.game, output_file)["errors"])
        retail = self.game / "_retail_"
        retail.mkdir()
        (retail / ".flavor.info").write_text("fixture")
        self.assertEqual(validate_paths(retail)["overwatchInstall"], str(self.game))
        with patch("owpreview.datatool.tempfile.NamedTemporaryFile", side_effect=PermissionError("denied")):
            self.assertIn("write", validate_paths(self.game, self.output)["errors"]["outputDirectory"])
        (self.game / ".build.info").write_text("Product!STRING:0|Version!STRING:0\nothergame|1\n")
        self.assertIn("different Battle.net game", validate_paths(self.game)["errors"]["overwatchInstall"])

    def test_parse_catalog_filters_unsupported_types_and_deduplicates_names(self):
        result = parse_catalog({"a": {"Name": "Lúcio", "IsHero": True}, "b": {"Name": "NPC", "IsHero": False}},
                               {"Lúcio": [{"Name": "Dance", "Type": "Emote", "GUID": "1"}, {"Name": "Dance", "Type": "Emote", "GUID": "2"}, {"Name": "Icon", "Type": "Icon"}]}, self.game)
        self.assertEqual([row["name"] for row in result["heroes"]], ["Lúcio"])
        self.assertEqual(result["unlocks"]["Lúcio"], [{"name": "Dance", "category": "emote", "id": "1"}])
        with self.assertRaises(DataToolError):
            parse_catalog({}, {}, self.game)
        with self.assertRaises(DataToolError):
            parse_catalog({"a": {"Name": "Lúcio"}}, {"Lúcio": {}}, self.game)

    def test_catalog_executes_real_subprocesses_and_reads_json(self):
        self.manager.start_catalog(self.exe, self.game)
        result = self.wait()
        self.assertEqual(result["state"], "Completed", result)
        self.assertEqual(result["catalog"]["heroes"][0]["name"], "Lúcio")
        self.assertEqual([call[0][2] for call in self.calls], ["list-heroes", "list-unlocks"])
        self.assertFalse(list((self.root / "job state").glob("catalog-*")))

    def test_success_real_argument_transport_no_shell_and_dependency_cwd(self):
        with patch.dict(os.environ, {"DATATOOL_OVERWATCH_DIR": "wrong game"}):
            self.export()
            result = self.wait()
        self.assertEqual(result["state"], "Completed", result)
        self.assertEqual(result["exitCode"], 0)
        self.assertTrue((self.output / "fixture.owanimclip").is_file())
        payload = next(json.loads(line["text"]) for line in result["logs"] if line["text"].startswith('{"argv"'))
        self.assertEqual(payload["argv"][3], "Lúcio|skin=Overwatch Classic")
        self.assertEqual(payload["argv"][2], str(self.output))
        self.assertEqual(payload["cwd"], str(self.exe.parent))
        self.assertIsNone(payload["override"])
        self.assertFalse(self.calls[0][1]["shell"])
        if os.name == "nt":
            self.assertTrue(self.calls[0][1]["creationflags"] & 0x00000004, "Process must start suspended")
        self.assertIsNone(result["progress"])

    def test_failed_exit_zero_query_errors_and_missing_completion(self):
        for asset, expected in (("FailCode", "exit code 7"), ("FailZero", "could not match"), ("Unsupported", "does not support"), ("NoMarker", "without confirming completion")):
            self.export(asset)
            result = self.wait()
            self.assertEqual(result["state"], "Failed", result)
            self.assertIn(expected, result["error"])
            self.assertTrue(self.shared_lock.acquire(blocking=False))
            self.shared_lock.release()

    def test_installer_lock_and_duplicate_jobs(self):
        self.shared_lock.acquire()
        try:
            with self.assertRaisesRegex(DataToolError, "busy"):
                self.export()
        finally:
            self.shared_lock.release()
        self.export("Wait")
        with self.assertRaisesRegex(DataToolError, "already running"):
            self.export("Wait")
        self.manager.cancel()
        self.assertEqual(self.wait()["state"], "Cancelled")

    def test_cancel_terminates_real_child_process_and_streams_logs(self):
        self.export("Wait")
        child = self.wait_for_child()
        start = time.monotonic()
        self.manager.cancel()
        self.assertLess(time.monotonic() - start, .5)
        result = self.wait()
        self.assertEqual(result["state"], "Cancelled", result)
        self.assert_process_ended(child)

    def test_immediate_parent_exit_cannot_leave_child_alive(self):
        self.export("ParentExit")
        child = self.wait_for_child()
        result = self.wait()
        self.assertEqual(result["state"], "Completed", result)
        self.assert_process_ended(child)

    def test_backend_close_cancels_process_and_child(self):
        self.export("Wait")
        child = self.wait_for_child()
        self.manager.close()
        self.assertEqual(self.manager.snapshot()["state"], "Cancelled")
        self.assertFalse(self.manager.snapshot()["busy"])
        self.assert_process_ended(child)

    @unittest.skipUnless(os.name == "nt", "Windows process containment")
    def test_containment_failure_never_resumes_child(self):
        with patch.object(datatool, "_WindowsJob", side_effect=OSError("fixture containment failure")), patch.object(datatool, "_resume_windows_primary_thread") as resume:
            self.export()
            result = self.wait()
            resume.assert_not_called()
        self.assertEqual(result["state"], "Failed")
        self.assertIn("safely contain", result["error"])
        self.assertFalse((self.output / "fixture.owanimclip").exists())
        self.assert_process_ended(self.processes[0].pid)

    @unittest.skipUnless(os.name == "nt", "Windows process containment")
    def test_failed_native_termination_and_taskkill_timeout_still_close_job_tree(self):
        self.export("Wait")
        child = self.wait_for_child()
        with patch.object(datatool._WindowsJob, "terminate", return_value=False), patch.object(datatool.subprocess, "run", side_effect=subprocess.TimeoutExpired("taskkill", 5)) as taskkill:
            self.manager.cancel()
            result = self.wait()
            self.assertTrue(taskkill.called)
        self.assertEqual(result["state"], "Cancelled", result)
        self.assert_process_ended(child)

    def test_job_rejects_unknown_catalog_items_and_missing_executable(self):
        with self.assertRaisesRegex(DataToolError, "Load game assets"):
            self.manager.start_export(self.exe, self.game, self.output, {"hero": "Lúcio", "asset": "Dance"})
        self.seed()
        with self.assertRaisesRegex(DataToolError, "asset is not"):
            self.manager.start_export(self.exe, self.game, self.output, {"hero": "Lúcio", "asset": "invented"})
        self.exe.unlink()
        self.export()
        self.assertIn("DataTool is missing", self.wait()["error"])


if __name__ == "__main__":
    unittest.main()
