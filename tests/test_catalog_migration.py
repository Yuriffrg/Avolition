"""Avolition catalog migration never accesses the user's real profile in these tests."""

import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI


class CatalogMigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name).resolve()
        self.local = self.workspace / 'LocalAppData'
        self.local.mkdir()
        self.environment = patch.dict(os.environ, {'LOCALAPPDATA': str(self.local)}, clear=False)
        self.environment.start()
        self.addCleanup(self.environment.stop)
        os.environ.pop('AVOLITION_DATA_DIR', None)
        os.environ.pop('WHITEHOUND_DATA_DIR', None)
        os.environ.pop('OWIO_DATA_DIR', None)
        os.environ.pop('OWPREVIEW_DATA_DIR', None)
        self.legacy = self.local / 'OWAnimationBrowser' / 'catalog.json'
        self.current = self.local / 'Avolition' / 'catalog.json'
        self.root = self.workspace / 'Exported Hero'
        self.root.mkdir()
        self.clip = self.root / '00001.owanimclip'
        self.output = self.workspace / 'Renamed 日本語'
        self.annotation = {'name': 'Salut café 日本語', 'notes': 'Keep these notes\nПривет', 'favorite': True}
        self.catalog = {
            'lastRoot': str(self.root),
            'annotations': {self.key(self.clip): self.annotation},
            'outputFolder': str(self.output),
            'outputFolders': {self.key(self.root): str(self.output)},
            'exportDirectories': [self.key(self.output)],
            'exportedFiles': [self.key(self.output / 'Saved.owanimclip')],
            'managedOutputDirectories': [self.key(self.output)],
        }

    @staticmethod
    def key(path):
        return os.path.normcase(str(Path(path).resolve()))

    def write(self, path, value):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf-8')
        return path.read_bytes()

    def data(self, api):
        result = api.bootstrap()
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_default_migration_preserves_labels_notes_settings_and_original(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        legacy_mtime = self.legacy.stat().st_mtime_ns
        api = BrowserAPI(desktop_mode=True)
        restored = self.data(api)
        self.assertEqual(restored['lastRoot'], str(self.root))
        self.assertEqual(restored['catalog'][self.key(self.clip)], self.annotation)
        self.assertEqual(restored['outputFolder'], str(self.output))
        self.assertFalse(restored['browserMode'])
        self.assertIsNone(restored['warning'])
        self.assertEqual(self.current.read_bytes(), legacy_bytes)
        self.assertEqual(json.loads(self.current.read_text(encoding='utf-8')), self.catalog)
        updated = {'name': 'New nickname', 'notes': 'New notes', 'favorite': False}
        self.assertTrue(api.save_annotation(str(self.clip), updated)['ok'])
        restarted = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restarted['catalog'][self.key(self.clip)], updated)
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)
        self.assertEqual(self.legacy.stat().st_mtime_ns, legacy_mtime)

    def test_existing_avolition_catalog_wins_over_legacy_catalog(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        newer = {**self.catalog, 'annotations': {self.key(self.clip): {
            'name': 'Newer OWIO label', 'notes': 'Do not overwrite', 'favorite': False}}}
        current_bytes = self.write(self.current, newer)
        current_mtime = self.current.stat().st_mtime_ns
        restored = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restored['catalog'], newer['annotations'])
        self.assertEqual(self.current.read_bytes(), current_bytes)
        self.assertEqual(self.current.stat().st_mtime_ns, current_mtime)
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)

    def test_explicit_data_directory_skips_legacy_migration(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        state_dir = self.workspace / 'Isolated State'
        restored = self.data(BrowserAPI(state_dir, desktop_mode=True))
        self.assertEqual(restored['catalog'], {})
        self.assertEqual(restored['lastRoot'], '')
        self.assertFalse((state_dir / 'catalog.json').exists())
        self.assertFalse(self.current.exists())
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)

    def test_environment_data_directories_skip_migration(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        for variable in ('AVOLITION_DATA_DIR', 'WHITEHOUND_DATA_DIR', 'OWIO_DATA_DIR', 'OWPREVIEW_DATA_DIR'):
            with self.subTest(variable=variable):
                isolated = self.workspace / variable
                with patch.dict(os.environ, {variable: str(isolated)}, clear=False):
                    api = BrowserAPI(desktop_mode=True)
                    self.assertEqual(self.data(api)['catalog'], {})
                    self.assertTrue(api.save_annotation(str(self.clip), {'name': variable})['ok'])
                self.assertTrue((isolated / 'catalog.json').is_file())
                self.assertFalse(self.current.exists())
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)

    def test_owio_catalog_is_preferred_and_preserved_when_rebranding(self):
        oldest = self.write(self.legacy, self.catalog)
        owio = self.local/'OWIO/catalog.json'
        newer = {**self.catalog, 'annotations': {self.key(self.clip): {
            'name': 'Latest nickname', 'notes': 'Retain legacy notes', 'favorite': True}}}
        previous = self.write(owio, newer)
        restored = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restored['catalog'], newer['annotations'])
        self.assertEqual(self.current.read_bytes(), previous)
        self.assertEqual(owio.read_bytes(), previous)
        self.assertEqual(self.legacy.read_bytes(), oldest)

    def test_whitehound_environment_override_precedes_previous_names(self):
        selected = self.workspace/'whitehound-env'
        previous = self.workspace/'owio-env'
        with patch.dict(os.environ, {'WHITEHOUND_DATA_DIR': str(selected), 'OWIO_DATA_DIR': str(previous)}):
            api = BrowserAPI(desktop_mode=True)
            self.assertTrue(api.save_annotation(str(self.clip), self.annotation)['ok'])
        self.assertTrue((selected/'catalog.json').is_file())
        self.assertFalse(previous.exists())

    def test_avolition_environment_override_precedes_all_legacy_names(self):
        selected = self.workspace / 'avolition-env'
        previous = self.workspace / 'whitehound-env'
        with patch.dict(os.environ, {'AVOLITION_DATA_DIR': str(selected), 'WHITEHOUND_DATA_DIR': str(previous)}):
            api = BrowserAPI(desktop_mode=True)
            self.assertTrue(api.save_annotation(str(self.clip), self.annotation)['ok'])
        self.assertTrue((selected / 'catalog.json').is_file())
        self.assertFalse(previous.exists())

    def test_explicit_argument_wins_over_environment_data_directories(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        explicit = self.workspace / 'argument'
        newer_env = self.workspace / 'owio-env'
        legacy_env = self.workspace / 'preview-env'
        with patch.dict(os.environ, {'AVOLITION_DATA_DIR': str(newer_env), 'WHITEHOUND_DATA_DIR': str(legacy_env),
                                   'OWIO_DATA_DIR': str(newer_env), 'OWPREVIEW_DATA_DIR': str(legacy_env)}):
            api = BrowserAPI(explicit, desktop_mode=True)
            self.assertTrue(api.save_annotation(str(self.clip), self.annotation)['ok'])
        self.assertTrue((explicit / 'catalog.json').is_file())
        self.assertFalse(newer_env.exists())
        self.assertFalse(legacy_env.exists())
        self.assertFalse(self.current.exists())
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)

    def test_owio_environment_override_wins_over_legacy_override(self):
        newer_env = self.workspace / 'owio-env'
        legacy_env = self.workspace / 'preview-env'
        with patch.dict(os.environ, {'OWIO_DATA_DIR': str(newer_env), 'OWPREVIEW_DATA_DIR': str(legacy_env)}):
            api = BrowserAPI(desktop_mode=True)
            self.assertTrue(api.save_annotation(str(self.clip), self.annotation)['ok'])
        self.assertTrue((newer_env / 'catalog.json').is_file())
        self.assertFalse(legacy_env.exists())

    def test_invalid_legacy_catalog_does_not_replace_or_delete_original(self):
        self.legacy.parent.mkdir(parents=True)
        invalid = b'{"annotations": not valid JSON'
        self.legacy.write_bytes(invalid)
        restored = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restored['catalog'], {})
        self.assertTrue(restored['warning'])
        self.assertFalse(self.current.exists())
        self.assertEqual(self.legacy.read_bytes(), invalid)

    def test_invalid_existing_avolition_catalog_is_not_overwritten_with_legacy(self):
        legacy_bytes = self.write(self.legacy, self.catalog)
        self.current.parent.mkdir(parents=True)
        broken = b'{"incomplete":'
        self.current.write_bytes(broken)
        restored = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restored['catalog'], {})
        self.assertTrue(restored['warning'])
        self.assertEqual(self.current.read_bytes(), broken)
        self.assertEqual(self.legacy.read_bytes(), legacy_bytes)

    def test_no_legacy_catalog_starts_clean_in_avolition_directory(self):
        api = BrowserAPI(desktop_mode=True)
        restored = self.data(api)
        self.assertEqual(restored['catalog'], {})
        self.assertFalse(restored['browserMode'])
        self.assertEqual(restored['appName'], 'Avolition')
        self.assertEqual(restored['version'], '0.8.35')
        self.assertTrue(api.save_annotation(str(self.clip), self.annotation)['ok'])
        self.assertTrue(self.current.is_file())
        self.assertFalse(self.legacy.exists())

    def test_whitehound_is_first_migration_source_and_keeps_settings_queue_and_custom_paths(self):
        whitehound = self.local / 'Whitehound' / 'catalog.json'
        older = self.write(self.legacy, self.catalog)
        owio = self.write(self.local / 'OWIO' / 'catalog.json', self.catalog)
        custom_library = self.workspace / 'Shared assets 日本語'
        install = self.workspace / 'Overwatch Install'
        toolchain_output = self.workspace / 'Toolchain exports café'
        queue = [{'id': 'a' * 32, 'name': 'Gameplay1P_reload 日本語',
                  'entityPath': str(self.root / 'Gameplay1P.owentity'), 'rootPath': str(self.root),
                  'assignments': [{'modelId': 'model-0', 'clipPath': str(self.clip)}]}]
        previous = dict(self.catalog, libraryDirectory=str(custom_library), exportQueue=queue,
                        libraryOptions={'sort': 'tracks-desc', 'hideDuplicates': False}, librarySortRevision=1,
                        workflow={'overwatchInstall': str(install), 'outputDirectory': str(toolchain_output),
                                  'activeTab': 'toolchain'},
                        fbxOptions={'clipEnabled': False, 'enabled': False, 'c4dEnabled': True,
                                    'includeMesh': False, 'bakeTextures': False, 'rootMotion': False})
        previous['annotations'] = {self.key(self.clip): dict(self.annotation, name='Newest Whitehound nickname')}
        original = self.write(whitehound, previous)
        original_mtime = whitehound.stat().st_mtime_ns
        api = BrowserAPI(desktop_mode=True)
        restored = self.data(api)
        self.assertEqual(restored['catalog'], previous['annotations'])
        self.assertEqual(restored['outputFolder'], str(self.output))
        self.assertEqual(restored['exportQueue'], queue)
        self.assertEqual(restored['libraryOptions'], {**previous['libraryOptions'], 'hideOriginPoses': True})
        self.assertEqual(restored['workflow']['libraryDirectory'], str(custom_library))
        self.assertEqual(restored['workflow']['outputDirectory'], str(toolchain_output))
        self.assertEqual(restored['workflow']['overwatchInstall'], str(install))
        self.assertEqual(restored['workflow']['activeTab'], 'toolchain')
        self.assertFalse(restored['fbxOptions']['rootMotion'])
        self.assertFalse(restored['fbxOptions']['includeMesh'])
        self.assertFalse(restored['fbxOptions']['bakeTextures'])
        self.assertEqual(self.current.read_bytes(), original)
        self.assertEqual(api._c4d_bridge.root, self.local / 'Whitehound' / 'c4d-bridge')
        self.assertTrue(api.save_annotation(str(self.clip), {'name': 'Changed in Avolition'})['ok'])
        restarted = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restarted['catalog'][self.key(self.clip)]['name'], 'Changed in Avolition')
        self.assertEqual(restarted['exportQueue'], queue)
        self.assertEqual(restarted['outputFolder'], str(self.output))
        self.assertEqual(whitehound.read_bytes(), original)
        self.assertEqual(whitehound.stat().st_mtime_ns, original_mtime)
        self.assertEqual(self.legacy.read_bytes(), older)
        self.assertEqual((self.local / 'OWIO' / 'catalog.json').read_bytes(), owio)

    def test_existing_avolition_never_recopies_newer_whitehound_metadata(self):
        whitehound = self.local / 'Whitehound' / 'catalog.json'
        previous = self.write(whitehound, dict(self.catalog, annotations={'old': {'name': 'Whitehound'}}))
        current = self.write(self.current, dict(self.catalog, annotations={'new': {'name': 'Avolition'}}))
        restored = self.data(BrowserAPI(desktop_mode=True))
        self.assertEqual(restored['catalog'], {'new': {'name': 'Avolition'}})
        self.assertEqual(self.current.read_bytes(), current)
        self.assertEqual(whitehound.read_bytes(), previous)

    def test_explicit_profile_does_not_copy_whitehound_or_use_its_bridge_queue(self):
        whitehound = self.local / 'Whitehound' / 'catalog.json'
        previous = self.write(whitehound, self.catalog)
        profile = self.workspace / 'Explicit isolated profile'
        api = BrowserAPI(profile, desktop_mode=True)
        restored = self.data(api)
        self.assertEqual(restored['catalog'], {})
        self.assertEqual(restored['exportQueue'], [])
        self.assertEqual(api._c4d_bridge.root, profile / 'c4d-bridge')
        self.assertFalse((profile / 'catalog.json').exists())
        self.assertFalse(self.current.exists())
        self.assertEqual(whitehound.read_bytes(), previous)


if __name__ == '__main__':
    unittest.main()
