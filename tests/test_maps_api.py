"""Maps/settings/name wiring: real assets and publication, mocked native C4D save."""
from copy import deepcopy
from pathlib import Path
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI
from owpreview.formats import load_asset
from test_c4d_api import MOCK_C4D_BYTES, MockBridgeClient
import test_export_queue_api as queue_fixtures
from test_map_formats import write_fixture


class MapBridge(MockBridgeClient):
    def status(self):
        return dict(super().status(), mapInstances=True)

    def export(self, *args, progress=None, **kwargs):
        return super().export(*args, **kwargs)


class MapsAndNamesAPITests(unittest.TestCase):
    write = queue_fixtures.ExportQueueAPITests.write

    def setUp(self):
        queue_fixtures.ExportQueueAPITests.setUp(self)

    def ok(self, result):
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_map_settings_defaults_and_custom_paths_are_separate_from_animations(self):
        fresh = BrowserAPI(self.base / 'Fresh profile', app_dir=self.base / 'Application')
        settings = self.ok(fresh.bootstrap())['maps']
        self.assertEqual(settings, {'root': str(self.base / 'Application' / 'library'),
                                   'outputFolder': str(self.base / 'Application' / 'library' / 'Map exports'),
                                   'bakeTextures': True})
        before = self.ok(self.api.bootstrap())
        custom = {'root': str(self.base / 'Maps 日本語'), 'outputFolder': str(self.base / 'Map output café'),
                  'bakeTextures': False}
        self.assertEqual(self.ok(self.api.maps_settings(custom)), custom)
        self.ok(self.api.workflow_settings({'activeTab': 'maps', 'toolchainTab': 'maps'}))
        restarted = BrowserAPI(self.profile)
        after = self.ok(restarted.bootstrap())
        self.assertEqual(after['maps'], custom)
        self.assertEqual(after['workflow']['activeTab'], 'maps')
        self.assertEqual(after['workflow']['toolchainTab'], 'maps')
        for key in ('lastRoot', 'outputFolder', 'catalog', 'exportQueue', 'libraryOptions'):
            self.assertEqual(after[key], before[key], key)
        self.ok(restarted.workflow_settings({'activeTab': 'animations', 'toolchainTab': 'heroes'}))
        self.assertEqual(self.ok(BrowserAPI(self.profile).bootstrap())['maps'], custom)
        self.assertFalse(Path(custom['outputFolder']).exists())

    def test_invalid_map_settings_and_failed_save_preserve_existing_settings(self):
        self.ok(self.api.maps_settings({'root': str(self.root), 'bakeTextures': False}))
        before = self.ok(self.api.maps_settings())
        catalog_path = self.profile / 'catalog.json'
        stored = catalog_path.read_bytes()
        for changes in ([], '', {'root': ''}, {'root': 'relative'}, {'outputFolder': ''},
                        {'bakeTextures': 'false'}, {'bakeTextures': 0}, {'other': True}):
            with self.subTest(changes=changes):
                self.assertFalse(self.api.maps_settings(changes)['ok'])
                self.assertEqual(self.ok(self.api.maps_settings()), before)
                self.assertEqual(catalog_path.read_bytes(), stored)
        with patch.object(self.api, '_save', side_effect=OSError('Disk full')):
            self.assertFalse(self.api.maps_settings({'outputFolder': str(self.base / 'New output')})['ok'])
        self.assertEqual(self.ok(self.api.maps_settings()), before)
        self.assertEqual(catalog_path.read_bytes(), stored)

    def test_scan_maps_uses_real_folder_and_leaves_animation_selection_untouched(self):
        map_root = self.base / 'Map sources 日本語'
        (map_root / 'Nested').mkdir(parents=True)
        (map_root / 'Generated').mkdir()
        # Scan indexes paths only; full map validity is checked when loading.
        for relative in ('B map.owmap', 'Nested/A 日本語.OWMAP', 'Generated/Hidden.owmap', 'Nested/not.txt'):
            (map_root / relative).write_bytes(b'map index fixture')
        self.api._catalog.setdefault('managedOutputDirectories', []).append(str(map_root / 'Generated'))
        before = self.ok(self.api.bootstrap())
        current_root = self.api._root
        result = self.ok(self.api.scan_maps(str(map_root)))
        self.assertEqual([entry['relativePath'] for entry in result['maps']],
                         ['B map.owmap', str(Path('Nested') / 'A 日本語.OWMAP')])
        self.assertEqual(result['warnings'], [])
        for entry in result['maps']:
            self.assertTrue(Path(entry['path']).is_absolute())
            self.assertTrue(Path(entry['path']).is_file())
            self.assertGreater(entry['size'], 0)
        after = self.ok(self.api.bootstrap())
        self.assertEqual(self.api._root, current_root)
        self.assertEqual(after['lastRoot'], before['lastRoot'])
        self.assertEqual(after['outputFolder'], before['outputFolder'])
        self.assertEqual(after['maps']['root'], str(map_root))
        for invalid in ('', 'relative', str(self.base / 'Not present')):
            self.assertFalse(self.api.scan_maps(invalid)['ok'], invalid)
        self.assertEqual(self.ok(self.api.maps_settings())['root'], str(map_root))

    def test_map_api_allowlist_and_open_output_use_separate_map_destination(self):
        for name in ('maps_settings', 'scan_maps', 'load_map', 'export_map', 'browse_map',
                     'open_map_output', 'object_names', 'save_object_name'):
            self.assertIn(name, API_METHODS)
        map_output = self.base / 'Map output'
        self.ok(self.api.maps_settings({'outputFolder': str(map_output)}))
        self.assertFalse(self.api.open_map_output()['ok'])
        map_output.mkdir()
        self.assertEqual(self.ok(self.api.open_map_output()), str(map_output))
        self.assertEqual(self.ok(self.api.bootstrap())['outputFolder'], str(self.output))

    def test_saved_object_names_survive_restart_and_failed_save_rolls_back(self):
        sources = {path: path.read_bytes() for path in self.root.iterdir()}
        asset = self.ok(self.api.load_entity(str(self.entity)))
        original_models = deepcopy(asset['models'])
        names = {'scene': 'Reload / scène', 'model:model-0': 'Arms 日本語', 'mesh:model-1:0': 'Weapon shell'}
        for node, name in names.items():
            self.ok(self.api.save_object_name(str(self.entity), node, name))
        restarted = BrowserAPI(self.profile)
        loaded = self.ok(restarted.load_entity(str(self.entity)))
        self.assertEqual(loaded['objectNames'], names)
        self.assertEqual(loaded['models'], original_models)
        self.assertEqual(self.ok(restarted.object_names(str(self.entity))), names)
        self.assertEqual(sources, {path: path.read_bytes() for path in self.root.iterdir()})
        stored = (self.profile / 'catalog.json').read_bytes()
        for node, name in (('bone:bone_0001', 'Rename'), ('model:missing', 'Rename'),
                           ('mesh:model-0:99', 'Rename'), ('model:model-0', ''),
                           ('model:model-0', 'Bad\nname')):
            self.assertFalse(restarted.save_object_name(str(self.entity), node, name)['ok'])
        with patch.object(restarted, '_save', side_effect=OSError('Disk full')):
            self.assertFalse(restarted.save_object_name(str(self.entity), 'model:model-0', 'Unsaved')['ok'])
        self.assertEqual(self.ok(restarted.object_names(str(self.entity))), names)
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), stored)

    def test_stale_name_keys_are_ignored_in_preview_without_erasing_saved_labels(self):
        names = {'model:model-0': 'Arms', 'mesh:model-1:0': 'Shell',
                 'model:removed': 'Old model', 'mesh:model-0:999': 'Removed mesh'}
        self.api._catalog['objectNames'] = {self.api._key(self.entity): names}
        self.api._save()
        stored = (self.profile / 'catalog.json').read_bytes()
        asset = self.ok(self.api.load_entity(str(self.entity)))
        self.assertEqual(asset['objectNames'], {'model:model-0': 'Arms', 'mesh:model-1:0': 'Shell'})
        self.assertEqual(self.ok(self.api.object_names(str(self.entity))), names)
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), stored)

    def test_queue_uses_names_captured_at_add_even_after_preview_is_renamed(self):
        self.ok(self.api.load_entity(str(self.entity)))
        names = {'scene': 'Reload setup', 'model:model-0': 'Arms at add', 'mesh:model-1:0': 'Gun shell at add'}
        for node, name in names.items():
            self.ok(self.api.save_object_name(str(self.entity), node, name))
        saved = self.ok(self.api.export_queue_save(self.entry))['entry']
        self.assertEqual(saved['objectNames'], names)
        self.ok(self.api.save_object_name(str(self.entity), 'model:model-0', 'Arms after add'))
        result = self.ok(self.api.export_queue_run(saved['id'], options=self.options))
        self.assertEqual(Path(result['path']).read_bytes(), MOCK_C4D_BYTES)
        models = self.bridge.calls[-1]['payload']['asset']['models']
        self.assertEqual(models[0]['name'], 'Arms at add')
        self.assertEqual(models[1]['meshes'][0]['name'], 'Gun shell at add')
        self.assertEqual(models[0]['entityName'], 'Reload setup')
        original = load_asset(self.entity, root_path=self.root)
        self.assertEqual([m['bones'] for m in models], [m['bones'] for m in original['models']])
        self.assertEqual([m['id'] for m in models], [m['id'] for m in original['models']])
        self.assertEqual(self.ok(self.api.bootstrap())['exportQueue'][0]['objectNames'], names)
        self.assertEqual(self.ok(self.api.object_names(str(self.entity)))['model:model-0'], 'Arms after add')

    def test_map_load_and_export_preserve_compact_instances_and_saved_names(self):
        map_root = self.base / 'Maps café'
        map_path = write_fixture(map_root)
        self.ok(self.api.maps_settings({'root': str(map_root), 'outputFolder': str(self.base / 'Map export')}))
        before = self.ok(self.api.bootstrap())
        sources = {p: p.read_bytes() for p in map_root.rglob('*') if p.is_file()}
        asset = self.ok(self.api.load_map(str(map_path)))
        self.assertEqual(len(asset['models']), 5)
        self.assertEqual(len(asset['meshPrototypes']), 3)
        model_id = asset['models'][0]['id']
        names = {'scene': 'Test map', 'model:' + model_id: 'First prop', 'mesh:' + model_id + ':0': 'Unique shell'}
        for node, name in names.items():
            self.ok(self.api.save_object_name(str(map_path), node, name))
        self.assertEqual(self.ok(BrowserAPI(self.profile).load_map(str(map_path)))['objectNames'], names)
        bridge = MapBridge()
        self.api._c4d_bridge = bridge
        result = self.ok(self.api.export_map(str(map_path), 'Map 日本語', bake_textures=False))
        self.assertEqual(Path(result['path']).read_bytes(), MOCK_C4D_BYTES)
        self.assertEqual(Path(result['path']).parent, self.base / 'Map export')
        payload = bridge.calls[-1]['payload']
        self.assertTrue(payload['mapInstances'])
        self.assertEqual(payload['animation']['tracks'], [])
        self.assertEqual(payload['asset']['name'], 'Test map')
        self.assertEqual(payload['asset']['models'][0]['name'], 'First prop')
        self.assertEqual(payload['asset']['models'][0]['meshNames'], {'0': 'Unique shell'})
        self.assertNotIn('meshNames', payload['asset']['models'][1])
        self.assertEqual(len(payload['asset']['meshPrototypes']), 3)
        self.assertEqual(sources, {p: p.read_bytes() for p in map_root.rglob('*') if p.is_file()})
        after = self.ok(self.api.bootstrap())
        self.assertEqual(after['lastRoot'], before['lastRoot'])
        self.assertEqual(after['outputFolder'], before['outputFolder'])
        self.assertEqual(after['exportQueue'], before['exportQueue'])
        self.assertIn(self.api._key(result['path']), self.api._catalog['exportedFiles'])
        self.assertIn(self.api._key(Path(result['path']).parent), self.api._catalog['managedOutputDirectories'])
        Path(result['path']).write_bytes(b'User edited map scene')
        collision = self.ok(self.api.export_map(str(map_path), 'Map 日本語', bake_textures=False))
        self.assertNotEqual(result['path'], collision['path'])
        self.assertEqual(Path(result['path']).read_bytes(), b'User edited map scene')
        self.assertEqual(Path(collision['path']).read_bytes(), MOCK_C4D_BYTES)

    def test_failed_map_save_and_old_bridge_preserve_existing_exports(self):
        map_root = self.base / 'Maps'
        map_path = write_fixture(map_root)
        output = self.base / 'Map output'
        output.mkdir()
        existing = output / 'Existing.c4d'
        existing.write_bytes(b'user scene')
        self.ok(self.api.maps_settings({'root': str(map_root), 'outputFolder': str(output)}))
        stored = (self.profile / 'catalog.json').read_bytes()
        self.api._c4d_bridge = MapBridge(fail=True)
        result = self.api.export_map(str(map_path), 'Failed', bake_textures=False)
        self.assertFalse(result['ok'])
        self.assertIn('Mock native writer failed', result['error'])
        self.assertEqual(list(output.iterdir()), [existing])
        self.assertEqual(existing.read_bytes(), b'user scene')
        self.assertEqual((self.profile / 'catalog.json').read_bytes(), stored)
        self.api._c4d_bridge = MockBridgeClient()
        old = self.api.export_map(str(map_path), 'Old bridge', bake_textures=False)
        self.assertFalse(old['ok'])
        self.assertIn('Bridge', old['error'])
        self.assertFalse(self.api._c4d_bridge.calls)
        self.assertEqual(list(output.iterdir()), [existing])
        self.api._c4d_bridge = MapBridge()
        self.ok(self.api.export_map(str(map_path), 'Recovered', bake_textures=False))

    def test_stale_map_names_are_ignored_at_export_without_discarding_saved_names(self):
        map_root = self.base / 'Changed maps'
        map_path = write_fixture(map_root)
        self.ok(self.api.maps_settings({'root': str(map_root), 'outputFolder': str(self.base / 'Map output')}))
        asset = self.ok(self.api.load_map(str(map_path)))
        model_id = asset['models'][0]['id']
        valid = {'scene': 'Saved scene', 'model:' + model_id: 'Surviving prop',
                 'mesh:' + model_id + ':0': 'Surviving mesh'}
        stored = dict(valid, **{'model:removed-model': 'Old placement',
                                'mesh:' + model_id + ':999': 'Old mesh'})
        self.api._catalog['objectNames'] = {self.api._key(map_path): stored}
        self.api._save()
        self.assertEqual(self.ok(self.api.load_map(str(map_path)))['objectNames'], valid)
        bridge = MapBridge()
        self.api._c4d_bridge = bridge
        result = self.ok(self.api.export_map(str(map_path), 'Changed map', bake_textures=False))
        self.assertEqual(Path(result['path']).read_bytes(), MOCK_C4D_BYTES)
        exported = bridge.calls[-1]['payload']['asset']
        self.assertEqual(exported['name'], valid['scene'])
        self.assertEqual(exported['models'][0]['name'], valid['model:' + model_id])
        self.assertEqual(exported['models'][0]['meshNames'], {'0': valid['mesh:' + model_id + ':0']})
        self.assertEqual(self.ok(self.api.object_names(str(map_path))), stored)
        self.assertEqual(self.ok(BrowserAPI(self.profile).object_names(str(map_path))), stored)


if __name__ == '__main__':
    unittest.main()
