"""Background map exports keep serial desktop requests responsive.

Map parsing and catalog persistence use real fixtures. The native export is
held/simulated so these tests make no claim about Cinema 4D or Octane.
"""
import copy
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI, run_stdio
from owpreview.map_formats import load_map
from test_map_formats import write_fixture


class MapJobTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.map_root = self.root / 'Maps 日本語'
        self.map_path = write_fixture(self.map_root)
        self.output = self.root / 'Output café'
        self.api = BrowserAPI(self.root / 'Profile', desktop_mode=True, app_dir=self.root / 'App')
        self.addCleanup(self.api.shutdown)
        self.ok(self.api.maps_settings({'root': str(self.map_root), 'outputFolder': str(self.output)}))
        self.captured = []

    def ok(self, result):
        self.assertTrue(result['ok'], result)
        return result['data']

    def wait_finished(self, job_id=None):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            snapshot = self.ok(self.api.export_map_status(job_id))
            if not snapshot['busy']:
                return snapshot
            time.sleep(.005)
        self.fail('The test map export did not finish.')

    def fake_export(self, asset, folder, name, *, bake_textures=True, bridge=None, progress=None):
        self.captured.append({'asset': copy.deepcopy(asset), 'folder': folder, 'name': name,
                              'bakeTextures': bake_textures, 'bridge': bridge})
        if progress:
            progress('Saving test scene…', 1, 1)
        folder.mkdir(parents=True, exist_ok=True)
        destination = folder / (name + '.c4d')
        destination.write_bytes(b'FAKE NATIVE FILE; BACKEND TEST ONLY')
        return {'path': str(destination), 'c4dPath': str(destination), 'files': [str(destination)],
                'warnings': ['Test native export only']}

    def held_loader(self, entered, release):
        def held(path, root_path=None):
            entered.set()
            if not release.wait(10):
                raise RuntimeError('Test loader was not released.')
            return load_map(path, root_path=root_path)
        return held

    def test_serial_stdio_settings_status_and_bootstrap_respond_while_loading(self):
        entered, release, completed = threading.Event(), threading.Event(), threading.Event()
        self.addCleanup(release.set)
        requests = [
            {'id': 'start', 'method': 'export_map_start',
             'args': [str(self.map_path), 'Serial map', None, None, False]},
            {'id': 'status', 'method': 'export_map_status'},
            {'id': 'settings', 'method': 'maps_settings', 'args': [{'bakeTextures': False}]},
            {'id': 'workflow', 'method': 'workflow_settings', 'args': [{'activeTab': 'maps'}]},
            {'id': 'restore', 'method': 'bootstrap'},
        ]
        incoming = io.StringIO('\n'.join(json.dumps(item) for item in requests) + '\n')
        outgoing = io.StringIO()

        def exchange():
            try:
                run_stdio(self.api, incoming, outgoing)
            finally:
                completed.set()

        with patch('owpreview.map_formats.load_map', side_effect=self.held_loader(entered, release)), \
                patch('owpreview.map_export.export_map', side_effect=self.fake_export):
            transport = threading.Thread(target=exchange, daemon=True)
            transport.start()
            try:
                self.assertTrue(entered.wait(2))
                self.assertTrue(completed.wait(2), 'A held map worker must not block the serial request loop.')
                self.assertFalse(release.is_set())
                replies = {reply['id']: self.ok(reply['result'])
                           for reply in map(json.loads, outgoing.getvalue().splitlines())}
                self.assertEqual(set(replies), {'start', 'status', 'settings', 'workflow', 'restore'})
                self.assertEqual(replies['start']['state'], 'Running')
                self.assertEqual(replies['status']['id'], replies['start']['id'])
                self.assertTrue(replies['status']['busy'])
                self.assertEqual(replies['status']['message'], 'Loading map…')
                self.assertFalse(replies['settings']['bakeTextures'])
                self.assertEqual(replies['workflow']['activeTab'], 'maps')
                self.assertEqual(replies['restore']['mapExport']['id'], replies['start']['id'])
                self.assertTrue(replies['restore']['mapExport']['busy'])
            finally:
                release.set()
                transport.join(2)
                final = self.wait_finished()
            self.assertEqual(final['state'], 'Completed')
        self.assertIn('export_map_start', API_METHODS)
        self.assertIn('export_map_status', API_METHODS)

    def test_choices_names_progress_and_result_are_captured_and_snapshots_are_owned(self):
        original = self.ok(self.api.load_map(str(self.map_path)))
        model_id = original['models'][0]['id']
        self.ok(self.api.save_object_name(str(self.map_path), 'scene', 'Scene at start'))
        self.ok(self.api.save_object_name(str(self.map_path), 'model:' + model_id, 'Object at start'))
        entered, release = threading.Event(), threading.Event()
        exporting, finish = threading.Event(), threading.Event()
        self.addCleanup(release.set)
        self.addCleanup(finish.set)
        captured_root = []
        real_held_loader = self.held_loader(entered, release)

        def held_loader(path, root_path=None):
            captured_root.append(root_path)
            return real_held_loader(path, root_path)

        def held_export(*args, **kwargs):
            kwargs['progress']('Building Cinema 4D scene…', 3, 12)
            exporting.set()
            if not finish.wait(10):
                raise RuntimeError('Test export was not released.')
            return self.fake_export(*args, **kwargs)

        with patch('owpreview.map_formats.load_map', side_effect=held_loader), \
                patch('owpreview.map_export.export_map', side_effect=held_export):
            started = self.ok(self.api.export_map_start(str(self.map_path), 'Captured name', bake_textures=False))
            try:
                self.assertTrue(entered.wait(2))
                other_root, other_output = self.root / 'Other source', self.root / 'Other output'
                other_root.mkdir()
                self.ok(self.api.maps_settings({'root': str(other_root), 'outputFolder': str(other_output),
                                                'bakeTextures': True}))
                self.ok(self.api.save_object_name(str(self.map_path), 'scene', 'Scene after start'))
                self.ok(self.api.save_object_name(str(self.map_path), 'model:' + model_id, 'Object after start'))
                release.set()
                self.assertTrue(exporting.wait(2))
                running = self.ok(self.api.export_map_status(started['id']))
                self.assertEqual((running['message'], running['completed'], running['total']),
                                 ('Building Cinema 4D scene…', 3, 12))
                self.assertIsNone(running['result'])
                self.assertIsNone(running['error'])
                self.assertGreaterEqual(running['elapsedSeconds'], 0)
                self.assertFalse(self.api.export_map_status('unknown')['ok'])
            finally:
                release.set()
                finish.set()
                final = self.wait_finished(started['id'])
        self.assertEqual(final['state'], 'Completed')
        captured = self.captured[0]
        self.assertEqual(captured_root, [str(self.map_root)])
        self.assertEqual(captured['folder'], self.output)
        self.assertEqual(captured['name'], 'Captured name')
        self.assertFalse(captured['bakeTextures'])
        self.assertEqual(captured['asset']['name'], 'Scene at start')
        self.assertEqual(captured['asset']['models'][0]['name'], 'Object at start')
        self.assertEqual(Path(final['result']['path']).parent, self.output)
        self.assertIn(self.api._key(final['result']['path']), self.api._catalog['exportedFiles'])
        self.assertEqual(self.ok(self.api.bootstrap())['mapExport'], final)
        final['result']['warnings'].append('Caller mutation')
        self.assertNotIn('Caller mutation', self.ok(self.api.export_map_status())['result']['warnings'])

    def test_async_and_legacy_sync_exports_share_duplicate_guard(self):
        for legacy_first in (False, True):
            with self.subTest(legacy_first=legacy_first):
                entered, release = threading.Event(), threading.Event()
                self.addCleanup(release.set)
                with patch('owpreview.map_formats.load_map', side_effect=self.held_loader(entered, release)), \
                        patch('owpreview.map_export.export_map', side_effect=self.fake_export):
                    responses = []
                    if legacy_first:
                        caller = threading.Thread(target=lambda: responses.append(
                            self.api.export_map(str(self.map_path), 'Legacy', bake_textures=False)), daemon=True)
                        caller.start()
                    else:
                        self.ok(self.api.export_map_start(str(self.map_path), 'Background', bake_textures=False))
                    try:
                        self.assertTrue(entered.wait(2))
                        for method in (self.api.export_map_start, self.api.export_map):
                            refused = method(str(self.map_path), 'Duplicate', bake_textures=False)
                            self.assertFalse(refused['ok'])
                            self.assertIn('already running', refused['error'])
                    finally:
                        release.set()
                        self.assertEqual(self.wait_finished()['state'], 'Completed')
                        if legacy_first:
                            caller.join(2)
                    if legacy_first:
                        self.assertTrue(responses[0]['ok'], responses)

    def test_failed_job_is_observable_and_a_later_export_can_complete(self):
        idle = self.ok(self.api.export_map_status())
        self.assertEqual((idle['id'], idle['state'], idle['busy'], idle['elapsedSeconds']),
                         (None, 'Idle', False, 0))
        with patch('owpreview.map_formats.load_map', side_effect=ValueError('Broken map fixture')):
            started = self.ok(self.api.export_map_start(str(self.map_path), 'Failed map'))
            failed = self.wait_finished(started['id'])
        self.assertEqual(failed['state'], 'Failed')
        self.assertEqual(failed['error'], 'Broken map fixture')
        self.assertIsNone(failed['result'])
        self.assertFalse(self.output.exists())
        with patch('owpreview.map_export.export_map', side_effect=self.fake_export):
            recovered = self.ok(self.api.export_map_start(str(self.map_path), 'Recovered map', bake_textures=False))
            final = self.wait_finished(recovered['id'])
        self.assertNotEqual(started['id'], recovered['id'])
        self.assertEqual(final['state'], 'Completed')
        self.assertIsNone(final['error'])
        self.assertTrue(Path(final['result']['path']).is_file())

    def test_shutdown_does_not_join_a_held_native_worker(self):
        entered, release, closed = threading.Event(), threading.Event(), threading.Event()
        self.addCleanup(release.set)
        with patch('owpreview.map_formats.load_map', side_effect=self.held_loader(entered, release)), \
                patch('owpreview.map_export.export_map', side_effect=self.fake_export):
            started = self.ok(self.api.export_map_start(str(self.map_path), 'Shutdown test', bake_textures=False))
            try:
                self.assertTrue(entered.wait(2))
                closer = threading.Thread(target=lambda: (self.api.shutdown(), closed.set()), daemon=True)
                closer.start()
                self.assertTrue(closed.wait(2), 'Shutdown must not wait for a held native operation.')
                self.assertFalse(release.is_set())
                self.assertTrue(self.api._map_exports._thread.daemon)
                self.assertIn('closing', self.api.export_map_start(str(self.map_path), 'Too late')['error'])
            finally:
                release.set()
                self.assertEqual(self.wait_finished(started['id'])['state'], 'Completed')


if __name__ == '__main__':
    unittest.main()
