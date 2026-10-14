"""Managed OWLib DataTool jobs; the upstream executable owns all extraction.

Command contracts were checked against OWLib's README, ExtractHeroUnlocks,
ExtractFlags, ListHeroes, ListHeroUnlocks, ListFlags and JSONTool. Skin extraction
includes the gameplay entities and their animations; emotes, victory poses and
highlight intros are separate unlock categories, not an extract-animations mode.
The released executable v2.24.1.0+1149+df522f0c was run with --help; hidden
flags/categories were also checked against its corresponding df522f0c source.
This is verification provenance, not a pinned runtime version or release date.
Map listing/extraction also follows ListMaps, MapHeader and ExtractMaps; see
MAP_SOURCE_REFERENCE.md for the exact selection and structured catalog contract.
https://github.com/overtools/OWLib#how-to-use
https://github.com/overtools/OWLib/tree/develop/DataTool
"""
from __future__ import annotations

import copy
import ctypes
from ctypes import wintypes
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import threading
import uuid


CATEGORIES = [
    {"id": "skin", "label": "Models + gameplay animations",
     "description": "Extract a skin's entities, models and gameplay animation clips, including Gameplay1P and Gameplay3P when available."},
    {"id": "emote", "label": "Emotes", "description": "Extract hero emote animation assets."},
    {"id": "victorypose", "label": "Victory poses", "description": "Extract hero victory-pose animation assets."},
    {"id": "highlightintro", "label": "Highlight intros", "description": "Extract hero highlight-intro animation assets."},
]
_CATEGORY_IDS = {entry["id"] for entry in CATEGORIES}
_ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
_UNSUPPORTED = re.compile(r"does not support this version|doesn't (?:properly )?support|unsupportedbuildversion", re.I)
_BAD_QUERY = re.compile(r"found nothing matching|error parsing query|unknown (?:type|tag|mode)|invalid for flag|cannot interpret your command|positional .* is required|flag .* is required", re.I)
_BAD_RUNTIME = re.compile(r"you must install or update \.net|framework.*not found|failed to load.*(?:dll|hostfxr)|dllnotfound|badimageformat|not a valid win32", re.I)
_FATAL = re.compile(r"unhandled exception|error initializing casc|error reading game files|not a valid overwatch|invalid archive directory|access.*denied|permission denied|failed to determine tact product", re.I)


class DataToolError(RuntimeError):
    pass


class _Cancelled(Exception):
    pass


def _now():
    return datetime.now(timezone.utc).isoformat()


def _path(value, label):
    text = str(value or "").strip()
    if not text:
        raise ValueError(f"Choose {label}.")
    path = Path(text).expanduser()
    if not path.is_absolute():
        raise ValueError(f"Choose an absolute path for {label}.")
    return path.resolve()


def validate_paths(install, output=None):
    """Read the game directory; probe output permissions without touching the game.

    Battle.net installs use .build.info; Steam's static container uses
    data/.build.config (TACTLib ClientHandler). A selected _retail_ folder with
    .flavor.info is normalized to its installation parent as TACTLib does.
    """
    errors = {}
    game = destination = None
    try:
        game = _path(install, "the Overwatch installation folder")
        if (game / ".flavor.info").is_file():
            game = game.parent
        if not game.is_dir():
            raise ValueError("Overwatch Install does not exist. Select the installed game's folder.")
        marker = game / ".build.info"
        static = game / "data" / ".build.config"
        if not marker.is_file() and not static.is_file():
            raise ValueError("Select the Overwatch install root containing .build.info or data/.build.config, not an exported model folder.")
        if not (game / "data").is_dir():
            raise ValueError("This installation has no data folder. Finish installing Overwatch, then select its install root.")
        with (marker if marker.is_file() else static).open("rb") as handle:
            marker_bytes = handle.read(16384)
        if marker.is_file():
            rows = marker_bytes.decode("utf-8-sig", errors="replace").splitlines()
            columns = rows[0].split("|") if rows else []
            product_column = next((index for index, name in enumerate(columns) if name.split("!")[0] == "Product"), None)
            if product_column is not None:
                products = [row.split("|")[product_column].strip() for row in rows[1:] if len(row.split("|")) > product_column]
                if products and not any(product in ("pro", "prot") for product in products):
                    raise ValueError("This is a different Battle.net game installation. Select the Overwatch install root.")
    except (OSError, ValueError) as exc:
        errors["overwatchInstall"] = str(exc)
    if output is not None:
        try:
            destination = _path(output, "the Output Directory")
            if game is not None and destination.is_relative_to(game):
                raise ValueError("Choose an output folder outside the Overwatch installation. Avolition never exports into game files.")
            if destination.exists() and not destination.is_dir():
                raise ValueError("Output Directory is a file. Choose a folder instead.")
            # Probe the nearest existing ancestor, so validation need not create
            # the user's selected folder before an export actually begins.
            parent = destination
            while not parent.exists() and parent != parent.parent:
                parent = parent.parent
            if not parent.is_dir():
                raise ValueError("Output Directory is unavailable. Connect its drive or choose another folder.")
            with tempfile.NamedTemporaryFile(prefix=".avolition-write-check-", dir=parent):
                pass
        except PermissionError:
            errors["outputDirectory"] = "Avolition cannot write to Output Directory. Choose a writable folder or grant this account write access."
        except (OSError, ValueError) as exc:
            errors["outputDirectory"] = str(exc)
    return {"ok": not errors, "errors": errors,
            "overwatchInstall": str(game) if game else str(install or ""),
            "outputDirectory": str(destination) if destination else str(output or "")}


def _filter_name(value, label):
    value = str(value or "").strip()
    if not value:
        raise ValueError(f"Select {label}.")
    # This is DataTool query-language validation, not shell escaping. OWLib has
    # no escaping for these name characters. Reject rather than change meaning.
    if any(char in value for char in '\x00\r\n|=,()!"”') or ("*" in value and value != "*"):
        raise ValueError(f"DataTool cannot represent this {label} name in its filter syntax. Choose another asset or the whole category.")
    return value


def build_extract_args(install, output, selection):
    category = str(selection.get("category", "skin"))
    if category not in _CATEGORY_IDS:
        raise ValueError("Choose a supported model or animation category.")
    hero = _filter_name(selection.get("hero"), "hero")
    asset = _filter_name(selection.get("asset"), "asset")
    return [str(install), "extract-unlocks", str(output), f"{hero}|{category}={asset}", *_extract_options(selection)]


def _extract_options(selection):
    args = ["--online=false", "--convert-textures-type=png", "--convert-animations-type=owanimclip"]
    for option, flag in (("includeAnimations", "skip-animations"), ("includeTextures", "skip-textures")):
        args.append(f"--{flag}={'false' if selection.get(option, True) else 'true'}")
    args.append(f"--skip-sound={'false' if selection.get('includeSound', False) else 'true'}")
    args.append(f"--all-lods={'true' if selection.get('allLods', False) else 'false'}")
    return args


def build_map_extract_args(install, output, query, selection):
    """The resolved map query remains one literal argv entry, including spaces."""
    return [str(install), "extract-maps", str(output), _filter_name(query, "map"), *_extract_options(selection)]


def build_list_args(install, kind, destination):
    if kind not in ("heroes", "unlocks", "maps"):
        raise ValueError("Unknown DataTool catalog request.")
    args = [str(install), f"list-{kind}", "--json", f"--out={destination}", "--online=false"]
    if kind == "unlocks":
        args.append("--flatten")
    return args


def parse_catalog(heroes, unlocks, install):
    if not isinstance(heroes, dict) or not isinstance(unlocks, dict):
        raise DataToolError("DataTool returned an unsupported catalog format. Check for a compatible Toolchain update.")
    entries = []
    for ident, row in heroes.items():
        if not isinstance(row, dict) or not row.get("Name") or row.get("IsHero") is False:
            continue
        entries.append({"id": str(row.get("GUID") or ident), "name": str(row["Name"])})
    entries.sort(key=lambda row: row["name"].casefold())
    choices = {}
    for hero, rows in unlocks.items():
        if not isinstance(rows, list):
            raise DataToolError("DataTool did not return flattened unlock data. Check for a compatible Toolchain update.")
        seen = set()
        choices[hero] = []
        for row in rows:
            if not isinstance(row, dict):
                continue
            category = str(row.get("Type", "")).lower()
            name = row.get("Name") or row.get("GUID")
            if category not in _CATEGORY_IDS or not name:
                continue
            key = (category, str(name))
            if key in seen:
                continue
            seen.add(key)
            choices[hero].append({"id": str(row.get("GUID", name)), "name": str(name), "category": category})
        choices[hero].sort(key=lambda row: (row["category"], row["name"].casefold()))
    if not entries:
        raise DataToolError("DataTool found no heroes in this installation. Check its version and the log.")
    return {"heroes": entries, "unlocks": choices, "install": str(install), "loadedAt": _now()}


def parse_map_catalog(payload, install):
    """ListMaps JSON keys are header GUIDs; MapGUID identifies the actual map.

    Match MapHeader.GetUniqueName(): VariantName ?? Name ?? "Title Screen",
    followed by the low 32-bit map index in hex. This distinguishes map variants.
    """
    if not isinstance(payload, dict):
        raise DataToolError("DataTool returned an unsupported map catalog format. Check for a compatible Toolchain update.")
    entries, seen = [], set()
    for key, row in payload.items():
        if not isinstance(row, dict):
            continue
        ident = row.get("GUID") or key
        map_guid = row.get("MapGUID")
        if not isinstance(ident, str) or not re.fullmatch(r"[0-9a-fA-F]{12}\.09[Ff]", ident) \
                or not isinstance(map_guid, str) or not re.fullmatch(r"[0-9a-fA-F]{12}\.[0-9a-fA-F]{3}", map_guid):
            continue
        if ident.upper() in seen:
            continue
        index = int(map_guid.split('.')[0], 16) & 0xffffffff
        if not index:
            continue
        name = row.get("VariantName") if row.get("VariantName") is not None else row.get("Name")
        if name is not None and not isinstance(name, str):
            continue
        name = name if name is not None else "Title Screen"
        query = f"{name}:{index:X}"
        try:
            _filter_name(query, "map")
        except ValueError:
            # The extractor also matches the numeric map index, so a localized
            # name containing query-language punctuation still has an exact ID.
            query = f"{index:X}"
        entries.append({"id": ident, "name": name, "query": query, "mapGuid": map_guid,
                        "variantName": row.get("VariantName"), "mapType": row.get("MapType"),
                        "description": row.get("Description"), "stateA": row.get("StateA"), "stateB": row.get("StateB"),
                        "gameModes": [str(mode["Name"]) for mode in row.get("GameModes") or []
                                      if isinstance(mode, dict) and mode.get("Name")]})
        seen.add(ident.upper())
    if not entries:
        raise DataToolError("DataTool found no usable maps in this installation. Check its version and the log.")
    entries.sort(key=lambda row: (row["name"].casefold(), row["mapGuid"], row["id"]))
    return {"maps": entries, "install": str(install), "loadedAt": _now()}


class _WindowsJob:
    """Own child processes even after DataTool exits; close kills the job tree."""
    def __init__(self, process):
        self.handle = None
        if os.name != "nt":
            return
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        self.kernel = kernel
        class BASIC(ctypes.Structure):
            _fields_ = [("PerProcessUserTimeLimit", ctypes.c_longlong), ("PerJobUserTimeLimit", ctypes.c_longlong),
                        ("LimitFlags", wintypes.DWORD), ("MinimumWorkingSetSize", ctypes.c_size_t),
                        ("MaximumWorkingSetSize", ctypes.c_size_t), ("ActiveProcessLimit", wintypes.DWORD),
                        ("Affinity", ctypes.c_size_t), ("PriorityClass", wintypes.DWORD), ("SchedulingClass", wintypes.DWORD)]
        class IO(ctypes.Structure):
            _fields_ = [(name, ctypes.c_ulonglong) for name in ("ReadOperationCount", "WriteOperationCount", "OtherOperationCount", "ReadTransferCount", "WriteTransferCount", "OtherTransferCount")]
        class EXTENDED(ctypes.Structure):
            _fields_ = [("BasicLimitInformation", BASIC), ("IoInfo", IO), ("ProcessMemoryLimit", ctypes.c_size_t),
                        ("JobMemoryLimit", ctypes.c_size_t), ("PeakProcessMemoryUsed", ctypes.c_size_t), ("PeakJobMemoryUsed", ctypes.c_size_t)]
        kernel.CreateJobObjectW.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
        kernel.CreateJobObjectW.restype = wintypes.HANDLE
        kernel.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
        kernel.SetInformationJobObject.restype = wintypes.BOOL
        kernel.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        kernel.AssignProcessToJobObject.restype = wintypes.BOOL
        kernel.TerminateJobObject.argtypes = [wintypes.HANDLE, wintypes.UINT]
        kernel.TerminateJobObject.restype = wintypes.BOOL
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel.CloseHandle.restype = wintypes.BOOL
        handle = kernel.CreateJobObjectW(None, None)
        if not handle:
            raise ctypes.WinError(ctypes.get_last_error())
        limits = EXTENDED()
        limits.BasicLimitInformation.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if not kernel.SetInformationJobObject(handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)) or not kernel.AssignProcessToJobObject(handle, wintypes.HANDLE(int(process._handle))):
            error = ctypes.get_last_error()
            kernel.CloseHandle(handle)
            raise ctypes.WinError(error)
        self.handle = handle

    def terminate(self):
        return bool(self.handle and self.kernel.TerminateJobObject(self.handle, 1))

    def close(self):
        if self.handle:
            if not self.kernel.CloseHandle(self.handle):
                raise ctypes.WinError(ctypes.get_last_error())
            self.handle = None


def _resume_windows_primary_thread(process):
    """Resume only after containment, using documented Win32 APIs.

    Python Popen closes CreateProcess's primary-thread handle. CREATE_SUSPENDED
    leaves that thread unable to execute, so find its ID with Toolhelp and reopen
    it with THREAD_SUSPEND_RESUME. No process code can spawn a child before the
    Job Object has been assigned. A fresh suspended process has one thread.
    https://learn.microsoft.com/windows/win32/procthread/process-creation-flags
    https://learn.microsoft.com/windows/win32/api/processthreadsapi/nf-processthreadsapi-resumethread
    """
    class THREADENTRY32(ctypes.Structure):
        _fields_ = [("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD),
                    ("th32ThreadID", wintypes.DWORD), ("th32OwnerProcessID", wintypes.DWORD),
                    ("tpBasePri", wintypes.LONG), ("tpDeltaPri", wintypes.LONG), ("dwFlags", wintypes.DWORD)]
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateToolhelp32Snapshot.argtypes = [wintypes.DWORD, wintypes.DWORD]
    kernel.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    kernel.Thread32First.argtypes = [wintypes.HANDLE, ctypes.POINTER(THREADENTRY32)]
    kernel.Thread32First.restype = wintypes.BOOL
    kernel.Thread32Next.argtypes = [wintypes.HANDLE, ctypes.POINTER(THREADENTRY32)]
    kernel.Thread32Next.restype = wintypes.BOOL
    kernel.OpenThread.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel.OpenThread.restype = wintypes.HANDLE
    kernel.ResumeThread.argtypes = [wintypes.HANDLE]
    kernel.ResumeThread.restype = wintypes.DWORD
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.CloseHandle.restype = wintypes.BOOL
    snapshot = kernel.CreateToolhelp32Snapshot(0x00000004, 0)  # TH32CS_SNAPTHREAD
    if snapshot == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    entry = THREADENTRY32()
    entry.dwSize = ctypes.sizeof(entry)
    thread_ids = []
    try:
        available = kernel.Thread32First(snapshot, ctypes.byref(entry))
        while available:
            if entry.th32OwnerProcessID == process.pid:
                thread_ids.append(entry.th32ThreadID)
            entry.dwSize = ctypes.sizeof(entry)
            available = kernel.Thread32Next(snapshot, ctypes.byref(entry))
    finally:
        kernel.CloseHandle(snapshot)
    if len(thread_ids) != 1:
        raise OSError("Could not identify DataTool's suspended primary thread.")
    thread = kernel.OpenThread(0x0002, False, thread_ids[0])  # THREAD_SUSPEND_RESUME
    if not thread:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        previous_count = kernel.ResumeThread(thread)
        if previous_count == 0xFFFFFFFF:
            raise ctypes.WinError(ctypes.get_last_error())
        if previous_count != 1:
            raise OSError("DataTool's primary thread was not in its expected suspended state.")
    finally:
        kernel.CloseHandle(thread)


def _kill_tree(process, job):
    if job is not None and job.handle and job.terminate():
        return
    if os.name == "nt":
        # Only a numeric PID is passed; neither a shell nor a user command is run.
        # Closing a contained job also kills descendants if taskkill cannot find
        # a parent which has already exited. Check the native termination result
        # rather than silently assuming that an attempted cancellation succeeded.
        try:
            result = subprocess.run([str(Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "taskkill.exe"),
                                     "/PID", str(process.pid), "/T", "/F"],
                                    capture_output=True, shell=False, creationflags=subprocess.CREATE_NO_WINDOW, timeout=5)
            if result.returncode and process.poll() is None:
                process.kill()
        except (OSError, subprocess.TimeoutExpired):
            if process.poll() is None:
                process.kill()
        finally:
            if job is not None:
                job.close()
    else:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def _stop_process(process, job):
    """Bound cancellation/cleanup waits; never spin forever after a failed kill."""
    try:
        _kill_tree(process, job)
    finally:
        if job is not None:
            job.close()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired as exc:
            raise DataToolError("Windows did not finish stopping DataTool. Check the process before starting another export.") from exc


class DataToolJobs:
    def __init__(self, state_dir, operation_lock=None, *, process_factory=None):
        self.state_dir = Path(state_dir).resolve()
        self._activity = operation_lock or threading.Lock()
        self._lock = threading.RLock()
        self._cancel = threading.Event()
        self._process_factory = process_factory or subprocess.Popen
        self._thread = None
        self._process = None
        self._job = None
        self._sequence = 0
        self._catalog = None
        self._map_catalog = None
        self._state = {"state": "Ready", "busy": False, "kind": None, "jobId": None, "logs": [],
                       "error": None, "message": "Ready", "result": None, "exitCode": None,
                       "progress": None, "startedAt": None, "finishedAt": None}

    def snapshot(self):
        with self._lock:
            return {**copy.deepcopy(self._state), "catalog": copy.deepcopy(self._catalog),
                    "mapCatalog": copy.deepcopy(self._map_catalog)}

    def _log(self, stream, text):
        text = _ANSI.sub("", text).strip("\r\n")
        if not text:
            return
        with self._lock:
            self._sequence += 1
            self._state["logs"].append({"seq": self._sequence, "stream": stream, "text": text[:12000]})
            self._state["logs"] = self._state["logs"][-800:]

    def _start(self, kind, run):
        with self._lock:
            if self._state["busy"]:
                raise DataToolError("A DataTool job is already running. Wait for it or choose Cancel.")
            if not self._activity.acquire(blocking=False):
                raise DataToolError("Toolchain is busy installing, updating or exporting. Wait for the current operation.")
            self._cancel.clear()
            self._sequence = 0
            self._state.update(state="Loading" if kind == "catalog" else "Exporting", busy=True, kind=kind,
                               jobId=uuid.uuid4().hex, logs=[], error=None, result=None, exitCode=None,
                               progress=None, startedAt=_now(), finishedAt=None,
                               message="Reading installed game assets…" if kind == "catalog" else "Exporting…")
            self._thread = threading.Thread(target=self._worker, args=(run,), daemon=True, name="avolition-datatool")
            try:
                self._thread.start()
            except BaseException:
                self._activity.release()
                self._state["busy"] = False
                raise
        return self.snapshot()

    def _worker(self, run):
        try:
            result = run()
            if self._cancel.is_set():
                raise _Cancelled()
            with self._lock:
                self._state.update(state="Completed", result=result, message="Export completed." if self._state["kind"] == "export" else "Game assets loaded.")
        except _Cancelled:
            with self._lock:
                self._state.update(state="Cancelled", message="Cancelled. Partial output files may remain.")
        except Exception as exc:
            with self._lock:
                self._state.update(state="Failed", error=str(exc), message=str(exc))
            self._log("avolition", str(exc))
        finally:
            # Release before publishing idle; a UI can start the next job as soon
            # as it sees busy=false without racing the shared installation lock.
            self._activity.release()
            with self._lock:
                self._state.update(busy=False, finishedAt=_now())

    @staticmethod
    def _validate(install, output=None):
        checked = validate_paths(install, output)
        if not checked["ok"]:
            raise DataToolError(" ".join(checked["errors"].values()))
        return checked

    def start_catalog(self, executable, install, workflow="heroes"):
        if workflow not in ("heroes", "maps"):
            raise DataToolError("Choose the Animations or Maps Toolchain workflow.")
        checked = self._validate(install)
        game = checked["overwatchInstall"]
        def run():
            self.state_dir.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(prefix="catalog-", dir=self.state_dir) as directory:
                payloads = []
                for kind in (("maps",) if workflow == "maps" else ("heroes", "unlocks")):
                    output = Path(directory) / f"{kind}.json"
                    self._run_process(executable, build_list_args(game, kind, output))
                    if not output.is_file():
                        raise DataToolError(f"DataTool did not produce {kind} JSON. Check its version and the log.")
                    try:
                        payloads.append(json.loads(output.read_text(encoding="utf-8-sig")))
                    except (ValueError, OSError) as exc:
                        raise DataToolError(f"DataTool returned unreadable {kind} JSON: {exc}") from exc
                catalog = parse_map_catalog(payloads[0], game) if workflow == "maps" else parse_catalog(*payloads, game)
                with self._lock:
                    if workflow == "maps":
                        self._map_catalog = catalog
                    else:
                        self._catalog = catalog
                return {"mapCount" if workflow == "maps" else "heroCount": len(catalog["maps" if workflow == "maps" else "heroes"]),
                        "install": game, "workflow": workflow}
        return self._start("catalog", run)

    def start_export(self, executable, install, output, selection):
        if not isinstance(selection, dict):
            raise DataToolError("Invalid Toolchain export selection.")
        workflow = selection.get("workflow", "heroes")
        if workflow not in ("heroes", "maps"):
            raise DataToolError("Choose the Animations or Maps Toolchain workflow.")
        checked = self._validate(install, output)
        game, destination = checked["overwatchInstall"], checked["outputDirectory"]
        selection = dict(selection)
        if workflow == "maps":
            with self._lock:
                catalog = copy.deepcopy(self._map_catalog)
            if not catalog or catalog["install"] != game:
                raise DataToolError("Load maps for this Overwatch installation before selecting an export.")
            map_id = selection.get("mapId")
            chosen = next((row for row in catalog["maps"] if row["id"] == map_id), None)
            if map_id != "*" and chosen is None:
                raise DataToolError("This map is not in the loaded catalog. Refresh maps and select it again.")
            query = chosen["query"] if chosen else "*"
            args = build_map_extract_args(game, destination, query, selection)
            def run_map():
                Path(destination).mkdir(parents=True, exist_ok=True)
                self._run_process(executable, args)
                return {"outputDirectory": destination, "workflow": "maps", "mapId": map_id,
                        "mapName": chosen["name"] if chosen else "All maps", "query": query}
            return self._start("export", run_map)
        args = build_extract_args(game, destination, selection)
        hero, asset, category = str(selection["hero"]), str(selection["asset"]), str(selection.get("category", "skin"))
        if hero != "*":
            with self._lock:
                catalog = self._catalog
                if not catalog or catalog["install"] != game:
                    raise DataToolError("Load game assets for this Overwatch installation before selecting an export.")
                if not any(row["name"] == hero for row in catalog["heroes"]):
                    raise DataToolError("This hero is not in the loaded game catalog. Refresh game assets.")
                if asset != "*" and not any(row["name"] == asset and row["category"] == category for row in catalog["unlocks"].get(hero, [])):
                    raise DataToolError("This asset is not in the loaded game catalog. Refresh game assets.")
        elif asset != "*":
            raise DataToolError("Select a hero for a specific asset, or choose all assets in the category.")
        def run():
            Path(destination).mkdir(parents=True, exist_ok=True)
            self._run_process(executable, args)
            return {"outputDirectory": destination, "hero": hero, "asset": asset, "category": category}
        return self._start("export", run)

    def _run_process(self, executable, args):
        if self._cancel.is_set():
            raise _Cancelled()
        executable = Path(executable).resolve()
        if not executable.is_file():
            raise DataToolError("DataTool is missing. Install or repair Toolchain, then try again.")
        # The upstream executable supports an environment override of its install
        # argument. Remove it so the visible shared directory remains authoritative.
        environment = os.environ.copy()
        environment.pop("DATATOOL_OVERWATCH_DIR", None)
        kwargs = {"cwd": str(executable.parent), "shell": False, "stdin": subprocess.DEVNULL,
                  "stdout": subprocess.PIPE, "stderr": subprocess.PIPE, "env": environment,
                  "text": True, "encoding": "utf-8", "errors": "replace", "bufsize": 1}
        if os.name == "nt":
            kwargs["creationflags"] = subprocess.CREATE_NO_WINDOW | subprocess.CREATE_NEW_PROCESS_GROUP | 0x00000004  # CREATE_SUSPENDED
        else:
            kwargs["start_new_session"] = True
        self._log("avolition", "Running " + args[1] + "…")
        try:
            process = self._process_factory([str(executable), *args], **kwargs)
        except OSError as exc:
            raise DataToolError(f"Could not start DataTool. Verify that Toolchain and its runtime dependencies are installed: {exc}") from exc
        job = None
        try:
            job = _WindowsJob(process)
            if os.name == "nt":
                _resume_windows_primary_thread(process)
        except OSError as exc:
            # A Windows process is still suspended if containment failed. Do not
            # run uncontained code and hope to discover its children afterwards.
            try:
                _stop_process(process, job)
            finally:
                process.stdout.close()
                process.stderr.close()
            raise DataToolError(f"Could not safely contain and start DataTool's process tree: {exc}") from exc
        with self._lock:
            self._process, self._job = process, job
        failures = []
        stderr_lines = []
        completed = threading.Event()
        def read(stream, label):
            try:
                # readline also splits carriage-return progress lines in text mode.
                for line in iter(stream.readline, ""):
                    line = _ANSI.sub("", line).strip()
                    self._log(label, line)
                    if "Execution finished in" in line:
                        completed.set()
                    if _UNSUPPORTED.search(line):
                        failures.append("This Toolchain does not support the installed Overwatch build. Check for a Toolchain update; upstream support may not be available yet.")
                    elif _BAD_RUNTIME.search(line):
                        failures.append("DataTool's runtime dependencies are missing or incompatible. Reinstall the complete Toolchain package and follow the runtime message in the log.")
                    elif _BAD_QUERY.search(line):
                        failures.append("DataTool could not match or interpret this asset selection. Refresh game assets and select a supported item. " + line)
                    elif _FATAL.search(line):
                        failures.append(line)
                    if label == "stderr" and line and "Failed to detect" not in line:
                        stderr_lines.append(line)
                        del stderr_lines[:-12]
            finally:
                stream.close()
        readers = [threading.Thread(target=read, args=(stream, label), daemon=True) for stream, label in ((process.stdout, "stdout"), (process.stderr, "stderr"))]
        for reader in readers:
            reader.start()
        try:
            while process.poll() is None:
                if self._cancel.wait(0.08):
                    _stop_process(process, job)
                    break
            code = process.wait(timeout=5)
            # Close the job before joining streams: an abandoned child must not
            # keep stdout/stderr open after the main executable has exited.
            if job is not None:
                job.close()
            if os.name != "nt":
                _kill_tree(process, None)
            for reader in readers:
                reader.join(timeout=5)
            with self._lock:
                self._state["exitCode"] = code
            if self._cancel.is_set():
                raise _Cancelled()
            if failures:
                raise DataToolError(failures[0])
            if code != 0:
                detail = stderr_lines[-1] if stderr_lines else "Open the log for details."
                raise DataToolError(f"DataTool failed with exit code {code}. {detail}")
            if stderr_lines:
                raise DataToolError("DataTool reported errors; the output may be incomplete. " + stderr_lines[0])
            if not completed.is_set():
                raise DataToolError("DataTool exited without confirming completion. Check the log for invalid arguments or a missing runtime.")
        finally:
            if process.poll() is None:
                _stop_process(process, job)
            if job is not None:
                job.close()
            with self._lock:
                self._process = self._job = None

    def cancel(self):
        with self._lock:
            if self._state["busy"]:
                self._cancel.set()
                self._state["message"] = "Cancelling…"
        return self.snapshot()

    def close(self):
        self.cancel()
        thread = self._thread
        if thread is not None and thread.is_alive():
            thread.join(timeout=20)
