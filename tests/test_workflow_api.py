"""Workflow storage/API integration with isolated folders and mocked job starts."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI
from owpreview.workflow_paths import WorkflowPaths, application_directory


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.app_dir = self.root / 'Whitehound 日本語'
        self.app_dir.mkdir()
        self.api = BrowserAPI(self.root / 'settings', app_dir=self.app_dir)
        self.addCleanup(self.api.shutdown)

    def test_portable_defaults_ignore_current_working_directory(self):
        settings = self.api.bootstrap()['data']['workflow']
        self.assertEqual(settings['outputDirectory'], str(self.app_dir/'library'))
        self.assertEqual(settings['toolchainDirectory'], str(self.app_dir/'toolchain'))
        self.assertEqual(settings['activeTab'], 'animations')
        self.assertNotEqual(application_directory(), Path.cwd() / 'library')
        self.assertFalse((self.app_dir/'library').exists())

    def test_custom_settings_and_clip_destination_survive_restart(self):
        output = str(self.root/'Exports with spaces 日本語')
        clips = str(self.root/'Named clips')
        self.api.set_output_folder(clips)
        self.assertTrue(self.api.workflow_settings({'outputDirectory': output,
            'overwatchInstall': str(self.root/'Overwatch café'), 'activeTab': 'toolchain'})['ok'])
        restored = BrowserAPI(self.root/'settings', app_dir=self.app_dir)
        self.addCleanup(restored.shutdown)
        boot = restored.bootstrap()['data']
        self.assertEqual(boot['workflow']['outputDirectory'], output)
        self.assertEqual(boot['workflow']['activeTab'], 'toolchain')
        self.assertEqual(boot['outputFolder'], clips)
        self.assertEqual(boot['workflow']['overwatchInstall'], str(self.root/'Overwatch café'))

    def test_use_library_follows_portable_move_and_honors_configured_library(self):
        self.api.workflow_settings({'outputDirectory': str(self.root/'Custom')})
        settings = self.api.use_library_directory()['data']
        self.api.workflow_settings({'outputDirectory': settings['outputDirectory']})
        catalog = json.loads(self.api._catalog_path.read_text(encoding='utf-8'))
        self.assertEqual(catalog['workflow']['outputDirectory'], '')
        moved = WorkflowPaths(catalog, self.root/'Moved app')
        self.assertEqual(moved.settings()['outputDirectory'], str(self.root/'Moved app/library'))
        catalog['libraryDirectory'] = str(self.root/'Configured Library')
        self.assertEqual(WorkflowPaths(catalog, self.app_dir).settings()['outputDirectory'], catalog['libraryDirectory'])

    def test_reject_relative_paths_and_unknown_tabs_without_resetting_good_values(self):
        original = self.api.workflow_settings()['data']
        for patch_value in ({'outputDirectory': 'relative/path'}, {'activeTab': 'invented'}):
            self.assertFalse(self.api.workflow_settings(patch_value)['ok'])
        self.assertEqual(original, self.api.workflow_settings()['data'])

    def test_validation_and_output_runtime_separation(self):
        missing = self.api.toolchain_validate()['data']
        self.assertFalse(missing['ok'])
        self.assertIn('overwatchInstall', missing['errors'])
        self.api.workflow_settings({'outputDirectory': str(self.app_dir/'toolchain/runtime/export')})
        result = self.api.toolchain_validate()['data']
        self.assertIn('outputDirectory', result['errors'])
        self.assertFalse(self.api.toolchain_start({'category': 'skin'})['ok'])

    def test_job_receives_shared_paths_and_original_selection(self):
        settings = self.api.workflow_settings({'overwatchInstall': str(self.root/'Game 日本語'),
            'outputDirectory': str(self.root/'Exports 日本語')})['data']
        selection = {'hero': 'Lúcio', 'category': 'skin', 'asset': 'Overwatch 2', 'includeAnimations': True}
        with patch.object(self.api._toolchain, 'executable', return_value=Path('DataTool.exe')), \
             patch.object(self.api._datatool, 'start_export') as start, \
             patch('app.validate_paths', return_value={'ok': True, 'errors': {}}):
            self.assertTrue(self.api.toolchain_start(selection)['ok'])
        start.assert_called_once_with(Path('DataTool.exe'), settings['overwatchInstall'], settings['outputDirectory'], selection)

    def test_runtime_never_installed_inside_game(self):
        self.api.workflow_settings({'overwatchInstall': str(self.root)})
        with patch.object(self.api._toolchain, 'install_async') as install:
            self.assertFalse(self.api.toolchain_install()['ok'])
            install.assert_not_called()
        self.api.workflow_settings({'overwatchInstall': ''})
        (self.root/'data').mkdir()
        (self.root/'.build.info').write_text('fixture')
        with patch.object(self.api._toolchain, 'install_async') as install:
            self.assertFalse(self.api.toolchain_install()['ok'])
            install.assert_not_called()

    def test_background_index_refresh_does_not_change_active_source(self):
        first = Path(__file__).parent/'demo-assets'
        second = Path(__file__).parent/'material-assets'
        self.assertTrue(self.api.scan_root(str(first))['ok'])
        self.assertTrue(self.api.scan_root(str(second), False)['ok'])
        self.assertEqual(self.api._root, first.resolve())
        self.assertEqual(self.api.bootstrap()['data']['lastRoot'], str(first.resolve()))

    def test_bridge_allowlists_match(self):
        source = Path(__file__).resolve().parents[1]
        for path in ('desktop/preload.cjs', 'desktop/main.cjs'):
            content = (source/path).read_text(encoding='utf-8')
            for method in API_METHODS:
                self.assertIn("'"+method+"'", content, (path, method))


if __name__ == '__main__':
    unittest.main()
