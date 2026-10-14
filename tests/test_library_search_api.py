"""Library browser API contracts and persisted preference compatibility."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import API_METHODS, BrowserAPI
from owpreview.formats import read_clip_binding_metadata
from test_formats import clip_bytes


class LibrarySearchAPITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.profile = self.base / 'Profile'
        self.api = BrowserAPI(self.profile)

    def test_native_and_backend_allowlists_include_both_real_methods(self):
        root = Path(__file__).resolve().parents[1]
        for name in ('read_clip_bindings', 'library_settings'):
            self.assertIn(name, API_METHODS)
            self.assertTrue(callable(getattr(self.api, name)))
            for script in ('desktop/main.cjs', 'desktop/preload.cjs'):
                self.assertIn("'" + name + "'", (root / script).read_text(encoding='utf-8'))

    def test_binding_api_returns_real_names_and_unknown_errors_in_request_order(self):
        source = self.base / '日本語 names.owanimclip'
        source.write_bytes(clip_bytes([('joint.Custom 日本語', []), ('bone_0075', [(0, [])])]))
        requested = [str(source), str(self.base / 'missing.owanimclip'), 'relative.owanimclip', None]
        result = self.api.read_clip_bindings(requested)
        self.assertTrue(result['ok'], result)
        clips = result['data']['clips']
        self.assertEqual(clips[0], {'path': str(source), 'boneNames': ['joint.Custom 日本語', 'bone_0075']})
        self.assertTrue(all(clip['boneNames'] is None and clip.get('error') for clip in clips[1:]))
        self.assertEqual([clip['path'] for clip in clips], requested)
        with patch('owpreview.clip_bindings.read_clip_binding_metadata', wraps=read_clip_binding_metadata) as read:
            cached = self.api.read_clip_bindings([str(source)])
            self.assertTrue(cached['ok'])
            self.assertEqual(read.call_count, 0, 'The API should reuse its existing binding index.')

    def test_invalid_binding_batch_is_wrapped_as_api_error(self):
        for value in (None, {}, 'path.owanimclip', ['relative'] * 129):
            with self.subTest(value=type(value).__name__):
                result = self.api.read_clip_bindings(value)
                self.assertFalse(result['ok'])
                self.assertIn('128', result['error'])
        self.assertEqual(self.api.read_clip_bindings([]), {'ok': True, 'data': {'clips': []}})

    def test_fresh_defaults_agree_between_bootstrap_and_read_only_preferences(self):
        expected = {'sort': 'object-match', 'hideDuplicates': True}
        self.assertEqual(self.api.library_settings(), {'ok': True, 'data': expected})
        self.assertEqual(self.api.bootstrap()['data']['libraryOptions'], expected)
        self.assertFalse((self.profile / 'catalog.json').exists(), 'Reading defaults must not write the catalog.')

    def test_partial_updates_persist_and_survive_restart_without_changing_other_settings(self):
        source = self.base / 'Clip.owanimclip'
        source.write_bytes(clip_bytes())
        annotation = {'name': 'User reload', 'notes': 'Keep this label', 'favorite': False}
        self.assertTrue(self.api.save_annotation(str(source), annotation)['ok'])
        self.assertTrue(self.api.workflow_settings({'activeTab': 'toolchain'})['ok'])
        original = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        first = self.api.library_settings({'sort': 'tracks-desc'})
        self.assertEqual(first, {'ok': True, 'data': {'sort': 'tracks-desc', 'hideDuplicates': True}})
        second = self.api.library_settings({'hideDuplicates': False})
        self.assertEqual(second, {'ok': True, 'data': {'sort': 'tracks-desc', 'hideDuplicates': False}})
        saved = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        self.assertEqual({key: value for key, value in saved.items() if key != 'libraryOptions'}, original)
        restarted = BrowserAPI(self.profile)
        self.assertEqual(restarted.library_settings(), second)
        self.assertEqual(restarted.bootstrap()['data']['libraryOptions'], second['data'])
        self.assertEqual(restarted.bootstrap()['data']['catalog'], original['annotations'])
        # Returned values must not alias mutable catalog preferences.
        second['data']['sort'] = 'bad mutation'
        self.assertEqual(restarted.library_settings()['data']['sort'], 'tracks-desc')

    def test_all_supported_sorts_are_accepted(self):
        sorts = ('object-match', 'tracks-desc', 'tracks-asc', 'name', 'nicknamed-first',
                 'size-desc', 'size-asc', 'duration-desc', 'duration-asc')
        for sort in sorts:
            result = self.api.library_settings({'sort': sort})
            self.assertEqual(result, {'ok': True, 'data': {'sort': sort, 'hideDuplicates': True}})

    def test_bad_preference_values_preserve_saved_and_in_memory_state(self):
        self.assertTrue(self.api.library_settings({'sort': 'name', 'hideDuplicates': False})['ok'])
        original = (self.profile / 'catalog.json').read_bytes()
        expected = self.api.library_settings()
        invalid = [[], 'name', 42, {'unknown': True}, {'sort': 'guess-action'}, {'sort': None},
                   {'sort': []}, {'sort': {}}, {'hideDuplicates': 0}, {'hideDuplicates': 1},
                   {'hideDuplicates': 'false'}, {'hideDuplicates': None},
                   {'sort': 'tracks-desc', 'hideDuplicates': 'true'}]
        for changes in invalid:
            with self.subTest(changes=changes):
                result = self.api.library_settings(changes)
                self.assertFalse(result['ok'], result)
                self.assertEqual(self.api.library_settings(), expected)
                self.assertEqual((self.profile / 'catalog.json').read_bytes(), original)

    def test_missing_or_invalid_legacy_preferences_use_defaults_without_losing_annotations(self):
        for saved_options in ('missing', None, [], 'tracks-desc',
                              {'sort': 'deleted-mode', 'hideDuplicates': None},
                              {'sort': [], 'hideDuplicates': 'false'}):
            with self.subTest(saved_options=saved_options):
                profile = self.base / ('Legacy-' + str(len(list(self.base.iterdir()))))
                profile.mkdir()
                catalog = {'annotations': {'source': {'name': 'Existing nickname'}},
                           'outputFolder': str(self.base / 'Existing output')}
                if saved_options != 'missing':
                    catalog['libraryOptions'] = saved_options
                (profile / 'catalog.json').write_text(json.dumps(catalog), encoding='utf-8')
                before = (profile / 'catalog.json').read_bytes()
                api = BrowserAPI(profile)
                self.assertEqual(api.library_settings()['data'], {'sort': 'object-match', 'hideDuplicates': True})
                self.assertEqual(api.bootstrap()['data']['catalog'], catalog['annotations'])
                self.assertEqual(api.bootstrap()['data']['outputFolder'], catalog['outputFolder'])
                self.assertEqual((profile / 'catalog.json').read_bytes(), before)


if __name__ == '__main__':
    unittest.main()
