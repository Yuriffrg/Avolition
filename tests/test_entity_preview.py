"""Model browser previews cannot alter the scene loaded for animation/export."""
import copy
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI
from owpreview.formats import load_asset
from create_material_demo import generate


class EntityPreviewTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.library = self.root/'Library'
        active_root = generate(self.library/'Active')
        preview_root = generate(self.library/'Candidates')
        self.active = active_root/'Entities/MaterialRobot/MaterialRobot.owentity'
        self.candidate = preview_root/'Entities/MaterialRobot/MaterialRobot.owentity'
        self.profile = self.root/'Profile'
        self.api = BrowserAPI(self.profile, app_dir=self.root/'App')
        self.addCleanup(self.api.shutdown)
        self.ok(self.api.scan_root(str(self.library)))
        self.active_asset = self.ok(self.api.load_entity(str(self.active)))

    def ok(self, result):
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_many_previews_apply_saved_names_without_evicting_active_editable_names(self):
        self.ok(self.api.load_entity(str(self.candidate)))
        self.ok(self.api.save_object_name(str(self.candidate), 'scene', 'Saved preview 日本語'))
        self.ok(self.api.save_object_name(str(self.candidate), 'model:model-0', 'Saved preview body'))
        self.ok(self.api.load_entity(str(self.active)))
        candidates = []
        for index in range(10):
            path = self.candidate.with_name(f'Candidate {index}.owentity')
            path.write_bytes(self.candidate.read_bytes())
            candidates.append(path)
        source_files = {path: path.read_bytes() for path in self.library.rglob('*') if path.is_file()}
        before_catalog = copy.deepcopy(self.api._catalog)
        before_file = (self.profile/'catalog.json').read_bytes()
        before_names = copy.deepcopy(self.api._naming_assets)
        before_root = self.api._root
        texture_id = next(iter(self.active_asset['textures']))
        active_texture = self.ok(self.api.load_texture(texture_id))
        with patch.object(self.api, '_register_names', side_effect=AssertionError('Preview registered editable names')):
            preview = self.ok(self.api.preview_entity(str(self.candidate)))
            self.assertEqual(preview['name'], 'Saved preview 日本語')
            self.assertEqual(preview['models'][0]['name'], 'Saved preview body')
            self.assertEqual(preview['objectNames']['scene'], 'Saved preview 日本語')
            for candidate in candidates:
                self.ok(self.api.preview_entity(str(candidate)))
        self.assertEqual(self.api._catalog, before_catalog)
        self.assertEqual((self.profile/'catalog.json').read_bytes(), before_file)
        self.assertEqual(self.api._naming_assets, before_names)
        self.assertEqual(self.api._root, before_root)
        self.assertIs(self.ok(self.api.load_texture(texture_id)), active_texture)
        self.assertEqual(source_files, {path: path.read_bytes() for path in self.library.rglob('*') if path.is_file()})
        # The original object still has the editable metadata required by this API.
        self.ok(self.api.save_object_name(str(self.active), 'model:model-0', 'Still editable'))

    def test_preview_uses_captured_root_and_releases_only_its_texture_owner(self):
        animation_id = next(iter(self.active_asset['textures']))
        map_spec = next(iter(self.active_asset['textures'].values()))
        self.api._textures.register({'map-only': map_spec}, owner='maps')
        animation_texture = self.ok(self.api.load_texture(animation_id))
        map_texture = self.ok(self.api.load_texture('map-only'))
        with patch('app.load_asset', wraps=load_asset) as loader:
            preview = self.ok(self.api.preview_entity(str(self.candidate)))
            loader.assert_called_once_with(str(self.candidate.resolve()), root_path=str(self.library))
        preview_id = next(iter(preview['textures']))
        self.assertNotEqual(preview_id, animation_id)
        self.ok(self.api.load_texture(preview_id))
        self.assertEqual(self.ok(self.api.release_entity_preview()), {'released': True})
        self.assertFalse(self.api.load_texture(preview_id)['ok'])
        self.assertIs(self.ok(self.api.load_texture(animation_id)), animation_texture)
        self.assertIs(self.ok(self.api.load_texture('map-only')), map_texture)
        self.assertEqual(set(self.api._textures._owners), {'animations', 'maps'})

    def test_close_or_replacement_prevents_inflight_preview_from_registering_stale_textures(self):
        for replace in (False, True):
            with self.subTest(replace=replace):
                entered, release = threading.Event(), threading.Event()
                responses = []
                self.addCleanup(release.set)

                def held(path, root_path=None):
                    if path == str(self.candidate):
                        entered.set()
                        if not release.wait(5):
                            raise RuntimeError('Test preview was not released.')
                    return load_asset(path, root_path=root_path)

                with patch('app.load_asset', side_effect=held):
                    worker = threading.Thread(target=lambda: responses.append(
                        self.api.preview_entity(str(self.candidate))), daemon=True)
                    worker.start()
                    try:
                        self.assertTrue(entered.wait(2))
                        if replace:
                            replacement = self.ok(self.api.preview_entity(str(self.active)))
                        else:
                            self.ok(self.api.release_entity_preview())
                    finally:
                        release.set()
                        worker.join(5)
                    self.assertFalse(worker.is_alive())
                self.assertFalse(responses[0]['ok'])
                self.assertIn('closed or replaced', responses[0]['error'])
                if replace:
                    self.assertEqual(self.api._textures._owners['model-browser'], replacement['textures'])
                else:
                    self.assertNotIn('model-browser', self.api._textures._owners)
                self.ok(self.api.release_entity_preview())
                self.ok(self.api.load_texture(next(iter(self.active_asset['textures']))))

    def test_failed_preview_does_not_change_scene_or_catalog(self):
        before_names = copy.deepcopy(self.api._naming_assets)
        before_catalog = copy.deepcopy(self.api._catalog)
        self.assertFalse(self.api.preview_entity(str(self.root/'Missing.owentity'))['ok'])
        self.assertFalse(self.api.preview_entity('relative.owentity')['ok'])
        self.assertEqual(self.api._naming_assets, before_names)
        self.assertEqual(self.api._catalog, before_catalog)
        self.ok(self.api.load_texture(next(iter(self.active_asset['textures']))))

    def test_desktop_allowlists_expose_preview_and_release(self):
        project = Path(__file__).resolve().parents[1]
        for name in ('preview_entity', 'release_entity_preview'):
            self.assertIn(name, API_METHODS)
            for script in ('desktop/main.cjs', 'desktop/preload.cjs'):
                self.assertIn("'" + name + "'", (project/script).read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
