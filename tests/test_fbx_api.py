"""Actual FBX endpoint, paired output, persisted settings and source protection."""

import json
from pathlib import Path
import tempfile
import unittest

from app import BrowserAPI
from create_material_demo import generate
from test_formats import model_bytes


MAGIC = b'Kaydara FBX Binary  \x00\x1a\x00'


class FBXAPITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.root = generate(self.base/'Exports')
        self.profile = self.base/'Profile'
        self.api = BrowserAPI(self.profile)
        self.assertTrue(self.api.scan_root(str(self.root))['ok'])
        self.entity = self.root/'Entities/MaterialRobot/MaterialRobot.owentity'
        self.source = self.root/'Animations/001_Wave.owanimclip'
        self.output = self.base/'Output'
        self.options = {'clipEnabled': True, 'c4dEnabled': False, 'enabled': True, 'upAxis': 'Y', 'includeMesh': True, 'bakeTextures': True,
                        'rootMotion': True, 'entityPath': str(self.entity), 'rootPath': str(self.root), 'rigId': 'auto'}

    def export(self, nickname='Wave', options=None, source=None):
        result = self.api.export_animation(str(source or self.source), nickname, str(self.output),
                                           options if options is not None else self.options)
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_material_fixture_exports_real_fbx_matching_clip_and_portable_pngs(self):
        original = {str(path): path.read_bytes() for path in self.root.rglob('*') if path.is_file()}
        result = self.export()
        self.assertEqual(Path(result['path']).stem, Path(result['fbxPath']).stem)
        self.assertEqual(Path(result['path']).read_bytes(), self.source.read_bytes())
        data = Path(result['fbxPath']).read_bytes()
        self.assertTrue(data.startswith(MAGIC))
        self.assertTrue(b'RelativeFilename' in data, 'Portable texture references are missing')
        self.assertFalse(str(self.root).encode() in data, 'FBX leaked an absolute source path')
        self.assertGreater(result['textureCount'], 0)
        self.assertEqual(Path(result['textureFolder']), self.output/'tex')
        for path in (self.output/'tex').glob('*.png'):
            self.assertTrue(path.read_bytes().startswith(b'\x89PNG\r\n\x1a\n'))
        self.assertEqual(original, {str(path): path.read_bytes() for path in self.root.rglob('*') if path.is_file()})

    def test_repeat_reuses_pair_and_axis_change_numbers_both_files(self):
        first = self.export()
        stamps = {path: Path(path).stat().st_mtime_ns for path in first['files']}
        repeated = self.export()
        self.assertTrue(repeated['reused'])
        self.assertEqual(repeated['files'], first['files'])
        self.assertEqual(stamps, {path: Path(path).stat().st_mtime_ns for path in first['files']})
        different = self.export(options=dict(self.options, upAxis='Z'))
        self.assertEqual(Path(different['path']).name, 'Wave (2).owanimclip')
        self.assertEqual(Path(different['fbxPath']).name, 'Wave (2).fbx')
        self.assertNotEqual(Path(first['fbxPath']).read_bytes(), Path(different['fbxPath']).read_bytes())

    def test_skeleton_only_option_emits_no_texture_folder(self):
        result = self.export(options=dict(self.options, includeMesh=False))
        self.assertTrue(Path(result['fbxPath']).read_bytes().startswith(MAGIC))
        self.assertEqual(result['textureCount'], 0)
        self.assertIsNone(result['textureFolder'])
        self.assertFalse((self.output/'tex').exists())

    def test_texture_checkbox_off_emits_fbx_without_texture_references(self):
        result = self.export(options=dict(self.options, bakeTextures=False))
        self.assertEqual(result['textureCount'], 0)
        self.assertFalse(b'RelativeFilename' in Path(result['fbxPath']).read_bytes(), 'Disabled textures still have FBX file references')

    def test_settings_persist_and_invalid_update_keeps_previous_settings(self):
        settings = {'enabled': True, 'upAxis': 'Z', 'includeMesh': False, 'bakeTextures': False, 'rootMotion': False}
        saved = self.api.set_export_settings(settings)
        self.assertTrue(saved['ok'], saved)
        self.assertEqual(saved['data'], dict(settings, clipEnabled=False, c4dEnabled=True))
        self.assertFalse(self.api.set_export_settings({'upAxis': 'X'})['ok'])
        restored = BrowserAPI(self.profile).bootstrap()
        self.assertTrue(restored['ok'], restored)
        self.assertEqual(restored['data']['fbxOptions'], dict(settings, clipEnabled=False, c4dEnabled=True))

    def test_invalid_catalog_fbx_settings_recover_without_losing_annotations(self):
        catalog_path = self.profile/'catalog.json'
        catalog = json.loads(catalog_path.read_text(encoding='utf-8'))
        catalog['fbxOptions'] = {'enabled': 'invalid', 'upAxis': 'Q'}
        catalog['annotations'] = {'preserved': {'name': 'Keep this nickname', 'favorite': True}}
        catalog_path.write_text(json.dumps(catalog), encoding='utf-8')
        result = BrowserAPI(self.profile).bootstrap()
        self.assertTrue(result['ok'], result)
        self.assertFalse(result['data']['fbxOptions']['enabled'])
        self.assertEqual(result['data']['fbxOptions']['upAxis'], 'Y')
        self.assertEqual(result['data']['catalog'], catalog['annotations'])
        self.assertTrue(result['data']['warning'])

    def test_legacy_format_defaults_migrate_once_preserving_catalog_and_nonformat_options(self):
        catalog_path = self.profile/'catalog.json'
        original = json.loads(catalog_path.read_text(encoding='utf-8'))
        original['fbxOptions'] = {'enabled': True, 'c4dEnabled': False, 'upAxis': 'Z',
                                 'includeMesh': False, 'bakeTextures': False, 'rootMotion': False}
        original['annotations'] = {'preserved': {'name': 'Keep this nickname', 'favorite': True}}
        original['exportedFiles'] = ['existing clip path']
        original['unrelatedFutureSetting'] = {'keep': [1, 2, 3]}
        catalog_path.write_text(json.dumps(original), encoding='utf-8')
        migrated_api = BrowserAPI(self.profile)
        migrated = migrated_api.bootstrap()['data']['fbxOptions']
        self.assertEqual(migrated, dict(original['fbxOptions'], clipEnabled=False, enabled=False, c4dEnabled=True))
        persisted = json.loads(catalog_path.read_text(encoding='utf-8'))
        self.assertEqual(persisted['fbxOptions'], migrated)
        self.assertEqual({k: v for k, v in persisted.items() if k != 'fbxOptions'},
                         {k: v for k, v in original.items() if k != 'fbxOptions'})
        chosen = dict(migrated, clipEnabled=True, enabled=True, c4dEnabled=False)
        self.assertTrue(migrated_api.set_export_settings(chosen)['ok'])
        restored = BrowserAPI(self.profile).bootstrap()['data']['fbxOptions']
        self.assertEqual(restored, chosen, 'Startup reapplied defaults over explicit saved choices')

    def test_fresh_profile_defaults_to_c4d_without_a_clip_copy(self):
        result = self.api.bootstrap()['data']['fbxOptions']
        self.assertFalse(result['clipEnabled'])
        self.assertFalse(result['enabled'])
        self.assertTrue(result['c4dEnabled'])

    def test_numbered_collision_never_reuses_original_source(self):
        self.output.mkdir()
        original = self.output/'Wave (2).owanimclip'
        original.write_bytes(self.source.read_bytes())
        (self.output/'Wave.owanimclip').write_bytes(b'Existing unrelated clip')
        before = original.read_bytes()
        for enabled, expected in [(False, 'Wave (3).owanimclip'), (True, 'Wave (3).owanimclip')]:
            with self.subTest(fbx=enabled):
                result = self.export(options=dict(self.options, enabled=enabled), source=original)
                self.assertEqual(Path(result['path']).name, expected)
                self.assertNotEqual(Path(result['path']), original)
                self.assertEqual(original.read_bytes(), before)
                self.assertNotIn(self.api._key(original), self.api._catalog['exportedFiles'])

    def test_export_uses_captured_entity_even_after_viewport_loads_another(self):
        other = self.root/'Other.owmdl'
        other.write_bytes(model_bytes(name='Other preview model'))
        self.assertTrue(self.api.load_entity(str(other))['ok'])
        result = self.export()
        data = Path(result['fbxPath']).read_bytes()
        self.assertTrue(b'Material Robot' in data, 'Captured entity is missing from FBX')
        self.assertFalse(b'Other preview model' in data, 'Current viewport replaced the captured export entity')
        self.assertGreater(result['textureCount'], 0)

    def test_missing_entity_failure_does_not_publish_clip_copy(self):
        result = self.api.export_animation(str(self.source), 'Failed', str(self.output),
                                           dict(self.options, entityPath=''))
        self.assertFalse(result['ok'])
        self.assertIn('entity', result['error'].lower())
        self.assertFalse((self.output/'Failed.owanimclip').exists())
        self.assertFalse((self.output/'Failed.fbx').exists())


if __name__ == '__main__':
    unittest.main()
