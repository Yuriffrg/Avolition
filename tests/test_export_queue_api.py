"""Persistent named export setups; real parsing/publication, mocked native save."""
from copy import deepcopy
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI
from test_c4d_api import MockBridgeClient, MOCK_C4D_BYTES
from test_formats import clip_bytes, entity_bytes, model_bytes


class ExportQueueAPITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.root = self.base / 'Export sources 日本語'
        self.root.mkdir()
        for model in ('Body', 'Weapon', 'Charm'):
            self.write(model + '.owmdl', model_bytes(name=model + '.00C'))
        self.write('Charm.owentity', entity_bytes(model='Charm.owmdl', relative='.'))
        self.write('Weapon.owentity', entity_bytes(model='Weapon.owmdl', relative='.',
                                                  children=[('Charm.owentity', 'bone_0002')]))
        self.entity = self.write('Main.owentity', entity_bytes(model='Body.owmdl', relative='.',
                                                              children=[('Weapon.owentity', 'bone_0002')]))
        self.arms = self.write('Arms.owanimclip', clip_bytes([('bone_0001', [(0, [(0, (0, 0, 0)),
                                                                                             (2, (1, 0, 0))])])], duration=3))
        self.gun = self.write('Gun.owanimclip', clip_bytes([('bone_0002', [(0, [(0, (0, 2, 0)),
                                                                                           (2, (0, 4, 0))])])], duration=3))
        self.profile = self.base / 'Profile'
        self.api = BrowserAPI(self.profile)
        self.api.scan_root(str(self.root))
        self.bridge = MockBridgeClient()
        self.api._c4d_bridge = self.bridge
        self.output = self.base / 'Common output'
        self.api.set_output_folder(str(self.output))
        self.entry = {'name': 'Reload 日本語', 'entityPath': str(self.entity), 'rootPath': str(self.root),
                      'assignments': [{'modelId': 'model-0', 'clipPath': str(self.arms)},
                                      {'modelId': 'model-1', 'clipPath': str(self.gun)}]}
        self.options = {'includeMesh': True, 'bakeTextures': False, 'rootMotion': True,
                        'clipEnabled': True, 'enabled': True, 'c4dEnabled': False}

    def write(self, name, data):
        path = self.root / name
        path.write_bytes(data)
        return path

    def save(self, entry=None):
        result = self.api.export_queue_save(self.entry if entry is None else entry)
        self.assertTrue(result['ok'], result)
        return result['data']['entry']

    def test_add_persists_snapshot_without_export_or_cinema_connection(self):
        original_sources = {path: path.read_bytes() for path in self.root.iterdir()}
        with patch.object(self.bridge, 'status', side_effect=AssertionError('Adding must not contact Cinema')):
            saved = self.save()
        self.assertRegex(saved['id'], r'^[0-9a-f]{32}$')
        self.assertFalse(self.output.exists())
        self.assertFalse(self.bridge.calls)
        self.assertEqual(saved['assignments'], self.entry['assignments'])
        self.assertNotIn('checked', saved)
        self.entry['assignments'].clear()
        saved['name'] = 'Mutated response'
        restarted = BrowserAPI(self.profile)
        queue = restarted.bootstrap()['data']['exportQueue']
        self.assertEqual(queue[0]['name'], 'Reload 日本語')
        self.assertEqual(len(queue[0]['assignments']), 2)
        self.assertEqual(original_sources, {path: path.read_bytes() for path in self.root.iterdir()})

    def test_duplicate_names_add_entries_and_only_explicit_id_updates(self):
        first, second = self.save(), self.save()
        self.assertNotEqual(first['id'], second['id'])
        changed = self.save(dict(first, name='New name', assignments=first['assignments'][:1]))
        queue = self.api.bootstrap()['data']['exportQueue']
        self.assertEqual(len(queue), 2)
        self.assertEqual(queue[0], changed)
        self.assertEqual(queue[1], second)
        self.assertEqual(changed['id'], first['id'])
        invalid = self.api.export_queue_save(dict(self.entry, id='a' * 32))
        self.assertFalse(invalid['ok'])
        self.assertIn('no longer exists', invalid['error'])

    def test_remove_only_removes_selected_entry_without_deleting_source_files(self):
        first, second = self.save(), self.save()
        result = self.api.export_queue_remove(first['id'])
        self.assertEqual(result, {'ok': True, 'data': {'entries': [second]}})
        self.assertTrue(self.arms.exists())
        self.assertTrue(self.gun.exists())
        self.assertFalse(self.output.exists())
        self.assertEqual(BrowserAPI(self.profile).bootstrap()['data']['exportQueue'], [second])
        self.assertFalse(self.api.export_queue_remove(first['id'])['ok'])

    def test_explicit_run_exports_full_entity_even_with_one_assigned_clip(self):
        saved = self.save(dict(self.entry, assignments=self.entry['assignments'][:1]))
        before = self.api.bootstrap()['data']['exportQueue']
        result = self.api.export_queue_run(saved['id'], options=self.options)
        self.assertTrue(result['ok'], result)
        data = result['data']
        self.assertEqual(Path(data['path']), self.output / 'Reload 日本語.c4d')
        self.assertEqual(Path(data['path']).read_bytes(), MOCK_C4D_BYTES)
        self.assertEqual(list(self.output.glob('*.owanimclip')), [])
        self.assertEqual(list(self.output.glob('*.fbx')), [])
        payload = self.bridge.calls[-1]['payload']
        self.assertEqual(len(payload['asset']['models']), 3)
        self.assertEqual(sum(len(model['bones']) for model in payload['asset']['models']), 6)
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'model-0'})
        self.assertEqual(self.api.bootstrap()['data']['exportQueue'], before, 'Export must not clear or auto-check queue entries.')

    def test_stored_paths_override_new_preview_root_and_caller_root(self):
        saved = self.save()
        elsewhere = self.base / 'Other library'
        elsewhere.mkdir()
        self.assertTrue(self.api.scan_root(str(elsewhere))['ok'])
        self.assertEqual(self.api.bootstrap()['data']['outputFolder'], str(self.output))
        restarted = BrowserAPI(self.profile)
        self.assertEqual(restarted.bootstrap()['data']['outputFolder'], str(self.output))
        result = self.api.export_queue_run(saved['id'], options=dict(self.options, rootPath=str(elsewhere)))
        self.assertTrue(result['ok'], result)
        payload = self.bridge.calls[-1]['payload']
        self.assertEqual(payload['asset']['path'], str(self.entity))
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'model-0', 'model-1'})
        self.assertEqual(Path(result['data']['path']).parent, self.output)

    def test_missing_files_or_failed_native_save_keep_queue_and_existing_outputs(self):
        saved = self.save()
        queue = self.api.bootstrap()['data']['exportQueue']
        original = self.gun.read_bytes()
        self.gun.unlink()
        result = self.api.export_queue_run(saved['id'], options=self.options)
        self.assertFalse(result['ok'])
        self.assertFalse(self.output.exists())
        self.assertEqual(self.api.bootstrap()['data']['exportQueue'], queue)
        self.gun.write_bytes(original)
        self.output.mkdir()
        existing = self.output / 'User project.c4d'
        existing.write_bytes(b'user project')
        self.bridge.fail = True
        before = (self.profile / 'catalog.json').read_bytes()
        result = self.api.export_queue_run(saved['id'], options=self.options)
        self.assertFalse(result['ok'])
        self.assertEqual(list(self.output.iterdir()), [existing])
        self.assertEqual(existing.read_bytes(), b'user project')
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), before)
        self.assertEqual(self.api.bootstrap()['data']['exportQueue'], queue)

    def test_concurrent_duplicate_run_is_rejected_without_replacing_captured_entry(self):
        saved = self.save()
        started, release = threading.Event(), threading.Event()
        captured, results = [], []

        def export(api, entity, assignments, name, output, options):
            captured.append((entity, deepcopy(assignments), name, output, options))
            started.set()
            self.assertTrue(release.wait(5))
            return {'path': 'fixture.c4d', 'name': name + '.c4d'}

        with patch.object(BrowserAPI.export_animation_set, '__wrapped__', export):
            thread = threading.Thread(target=lambda: results.append(self.api.export_queue_run(saved['id'])))
            thread.start()
            try:
                self.assertTrue(started.wait(5))
                duplicate = self.api.export_queue_run(saved['id'])
                self.assertFalse(duplicate['ok'])
                self.assertIn('already exporting', duplicate['error'])
                self.save(dict(saved, name='Updated while exporting'))
                self.assertEqual(captured[0][2], 'Reload 日本語')
            finally:
                release.set()
                thread.join(5)
            self.assertFalse(thread.is_alive())
        self.assertTrue(results[0]['ok'], results)

    def test_catalog_failure_rolls_back_queue_edits_in_memory(self):
        saved = self.save()
        before = (self.profile / 'catalog.json').read_bytes()
        queue = self.api.bootstrap()['data']['exportQueue']
        with patch.object(self.api, '_save', side_effect=OSError('Disk full')):
            for method, args in ((self.api.export_queue_save, (dict(self.entry, name='New'),)),
                                 (self.api.export_queue_save, (dict(saved, name='Changed'),)),
                                 (self.api.export_queue_remove, (saved['id'],))):
                result = method(*args)
                self.assertFalse(result['ok'])
                self.assertIn('Disk full', result['error'])
                self.assertEqual(self.api.bootstrap()['data']['exportQueue'], queue)
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), before)

    def test_bad_input_and_queue_cap_do_not_alter_saved_entries(self):
        saved = self.save()
        before = (self.profile / 'catalog.json').read_bytes()
        bad = [None, [], dict(self.entry, name=''), dict(self.entry, name=42),
               dict(self.entry, name='../outside'), dict(self.entry, name='CON'),
               dict(self.entry, name='a' * 181), dict(self.entry, entityPath='relative.owentity'),
               dict(self.entry, entityPath=str(self.root / 'missing.owentity')),
               dict(self.entry, rootPath='relative'), dict(self.entry, rootPath=str(self.arms)),
               dict(self.entry, assignments=[]), dict(self.entry, assignments={}),
               dict(self.entry, assignments=[self.entry['assignments'][0]] * 513),
               dict(self.entry, assignments=[self.entry['assignments'][0]] * 2),
               dict(self.entry, assignments=[{'modelId': 'model-missing', 'clipPath': str(self.arms)}]),
               dict(self.entry, assignments=[{'modelId': 'model-0', 'clipPath': str(self.root / 'missing.owanimclip')}]),
               dict(self.entry, checked=True)]
        for value in bad:
            with self.subTest(value=value):
                result = self.api.export_queue_save(value)
                self.assertFalse(result['ok'], result)
                self.assertEqual((self.profile / 'catalog.json').read_bytes(), before)
        with patch('app.MAX_EXPORT_QUEUE', 1):
            self.assertFalse(self.api.export_queue_save(self.entry)['ok'])
            self.assertTrue(self.api.export_queue_save(dict(saved, name='Update existing'))['ok'])
        for ident in (None, 42, '', '../outside', 'unknown'):
            self.assertFalse(self.api.export_queue_remove(ident)['ok'])
            self.assertFalse(self.api.export_queue_run(ident)['ok'])
        self.assertFalse(self.output.exists())

    def test_open_output_returns_only_saved_existing_common_directory(self):
        for method in ('export_queue_save', 'export_queue_remove', 'export_queue_run', 'open_export_output'):
            self.assertIn(method, API_METHODS)
        self.assertFalse(self.api.open_export_output()['ok'])
        self.output.mkdir()
        self.assertEqual(self.api.open_export_output(), {'ok': True, 'data': str(self.output)})

    def test_old_default_sort_migrates_once_and_new_choice_survives_restart(self):
        catalog = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        catalog.pop('librarySortRevision', None)
        catalog['libraryOptions'] = {'sort': 'tracks-desc', 'hideDuplicates': False}
        catalog['annotations'] = {'source': {'name': 'Keep nickname'}}
        (self.profile / 'catalog.json').write_text(json.dumps(catalog), encoding='utf-8')
        migrated = BrowserAPI(self.profile)
        self.assertEqual(migrated.library_settings()['data'], {'sort': 'object-match', 'hideDuplicates': False, 'hideOriginPoses': True})
        saved = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        self.assertEqual(saved['librarySortRevision'], 1)
        self.assertEqual(saved['annotations'], catalog['annotations'])
        self.assertTrue(migrated.library_settings({'sort': 'tracks-desc'})['ok'])
        self.assertEqual(BrowserAPI(self.profile).library_settings()['data']['sort'], 'tracks-desc')

    def test_malformed_saved_queue_does_not_hide_or_overwrite_existing_labels(self):
        catalog = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        catalog.update(exportQueue={'wrong': 'shape'}, annotations={'source': {'name': 'Keep nickname'}})
        (self.profile / 'catalog.json').write_text(json.dumps(catalog), encoding='utf-8')
        before = (self.profile / 'catalog.json').read_bytes()
        api = BrowserAPI(self.profile)
        bootstrap = api.bootstrap()
        self.assertTrue(bootstrap['ok'])
        self.assertEqual(bootstrap['data']['catalog'], catalog['annotations'])
        self.assertEqual(bootstrap['data']['exportQueue'], [])
        self.assertIn('export list', bootstrap['data']['warning'])
        self.assertFalse(api.export_queue_save(self.entry)['ok'])
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), before)

    def test_malformed_saved_entry_cannot_crash_restore_or_be_silently_discarded(self):
        valid = self.save()
        catalog_path = self.profile / 'catalog.json'
        original = json.loads(catalog_path.read_text(encoding='utf-8'))
        candidate = dict(valid, id='a' * 32)
        malformed = [None, [], {'id': candidate['id']}, dict(candidate, id='invalid'),
                     dict(candidate, id=valid['id']), dict(candidate, name=None),
                     dict(candidate, name=[]), dict(candidate, name=''),
                     dict(candidate, entityPath=None), dict(candidate, rootPath=[]),
                     dict(candidate, assignments=None), dict(candidate, assignments={}),
                     dict(candidate, assignments=[]), dict(candidate, assignments=[None]),
                     dict(candidate, assignments=[{}]),
                     dict(candidate, assignments=[{'modelId': None, 'clipPath': str(self.arms)}]),
                     dict(candidate, assignments=[{'modelId': '', 'clipPath': str(self.arms)}]),
                     dict(candidate, assignments=[{'modelId': 'model-0', 'clipPath': []}]),
                     dict(candidate, assignments=[candidate['assignments'][0]] * 2),
                     dict(candidate, assignments=[candidate['assignments'][0]] * 513)]
        for damaged in malformed:
            with self.subTest(entry=damaged):
                catalog = dict(original, exportQueue=[valid, damaged],
                               annotations={'source': {'name': 'Keep nickname'}})
                catalog_path.write_text(json.dumps(catalog), encoding='utf-8')
                before = catalog_path.read_bytes()
                api = BrowserAPI(self.profile)
                bootstrap = api.bootstrap()
                self.assertTrue(bootstrap['ok'], bootstrap)
                self.assertEqual(bootstrap['data']['exportQueue'], [])
                self.assertEqual(bootstrap['data']['catalog'], catalog['annotations'])
                self.assertIn('export list', bootstrap['data']['warning'])
                self.assertFalse(api.export_queue_save(self.entry)['ok'])
                self.assertFalse(api.export_queue_remove(valid['id'])['ok'])
                self.assertFalse(api.export_queue_run(valid['id'])['ok'])
                self.assertEqual(catalog_path.read_bytes(), before, 'A malformed list must remain recoverable on disk.')
                self.assertEqual(api._catalog['exportQueue'], catalog['exportQueue'])
        self.assertFalse(self.output.exists())

    def test_structurally_valid_entries_remain_visible_but_are_revalidated_before_export(self):
        saved = self.save()
        missing_root = self.base / 'Offline sources 日本語'
        missing_sources = dict(saved, rootPath=str(missing_root), entityPath=str(missing_root / 'Main.owentity'),
                               assignments=[{'modelId': 'model-0', 'clipPath': str(missing_root / 'Arms.owanimclip')}])
        catalog_path = self.profile / 'catalog.json'
        catalog = json.loads(catalog_path.read_text(encoding='utf-8'))
        for entry in (missing_sources, dict(saved, name='../outside')):
            with self.subTest(entry=entry):
                catalog['exportQueue'] = [entry]
                catalog_path.write_text(json.dumps(catalog), encoding='utf-8')
                before = catalog_path.read_bytes()
                api = BrowserAPI(self.profile)
                bootstrap = api.bootstrap()
                self.assertTrue(bootstrap['ok'], bootstrap)
                self.assertEqual(bootstrap['data']['exportQueue'], [entry])
                self.assertFalse(bootstrap['data']['warning'])
                self.assertFalse(api.export_queue_run(entry['id'])['ok'], 'Export must revalidate sources and filenames before writing.')
                self.assertEqual(catalog_path.read_bytes(), before)
                self.assertTrue(api.export_queue_remove(entry['id'])['ok'])
                self.assertEqual(api.bootstrap()['data']['exportQueue'], [])
        self.assertFalse(missing_root.exists())
        self.assertFalse(self.output.exists())
        self.assertFalse((self.base / 'outside.c4d').exists())


if __name__ == '__main__':
    unittest.main()
