"""Real-filesystem regression checks for non-destructive renamed clip copies."""

from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from test_formats import clip_bytes

CLIP_ONLY = {"clipEnabled": True, "enabled": False, "c4dEnabled": False}


class ExportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name).resolve()
        self.root = self.workspace / 'Extracted Hero'
        self.source_dir = self.root / 'Models' / 'Hero' / 'Animations'
        self.source_dir.mkdir(parents=True)
        self.state = self.workspace / 'app-state'
        self.source = self.source_dir / '000000000001.owanimclip'
        self.bytes = clip_bytes()
        self.source.write_bytes(self.bytes)
        self.source_mtime = self.source.stat().st_mtime_ns
        self.api = BrowserAPI(self.state)
        self.ok(self.api.scan_root(str(self.root)))

    def ok(self, result):
        self.assertTrue(result['ok'], result)
        return result['data']

    def export(self, nickname='Wave', source=None, output=None):
        arguments = [str(source or self.source), nickname]
        if output is not None:
            arguments.append(str(output))
        return self.ok(self.api.export_animation(*arguments, options=CLIP_ONLY))

    def assert_original_untouched(self):
        self.assertEqual(self.source.read_bytes(), self.bytes)
        self.assertEqual(self.source.stat().st_mtime_ns, self.source_mtime)

    def assert_copy(self, result, path, expected=None, reused=False):
        self.assertEqual(Path(result['path']).resolve(), path.resolve())
        self.assertEqual(result['name'], path.name)
        self.assertEqual(result['reused'], reused)
        self.assertEqual(path.read_bytes(), self.bytes if expected is None else expected)

    def test_default_folder_and_identical_copy(self):
        folder = self.root / 'Renamed Animations'
        initial = self.ok(self.api.bootstrap())
        self.assertEqual(Path(initial['outputFolder']), folder)
        result = self.export('Wave')
        self.assert_copy(result, folder / 'Wave.owanimclip')
        self.assert_original_untouched()

    def test_extension_is_appended_once_case_insensitively(self):
        result = self.export('Victory.OWANIMCLIP')
        destination = Path(result['path'])
        self.assertEqual(destination.name.lower(), 'victory.owanimclip')
        self.assertEqual(destination.read_bytes(), self.bytes)
        self.assert_original_untouched()

    def test_unicode_names_and_whitespace_normalization(self):
        result = self.export('  Salutation café 日本語  ')
        self.assertEqual(result['name'], 'Salutation café 日本語.owanimclip')
        self.assert_original_untouched()

    def test_repeat_export_reuses_existing_copy_without_rewriting(self):
        first = self.export('Wave')
        path = Path(first['path'])
        mtime = path.stat().st_mtime_ns
        repeated = self.export('Wave')
        self.assert_copy(repeated, path, reused=True)
        self.assertEqual(path.stat().st_mtime_ns, mtime)
        self.assertEqual(len(list(path.parent.glob('*.owanimclip'))), 1)
        self.assert_original_untouched()

    def test_different_source_with_same_content_reuses_existing_copy(self):
        first = self.export('Same motion')
        second_source = self.source_dir / '000000000002.owanimclip'
        second_source.write_bytes(self.bytes)
        reused = self.export('Same motion', source=second_source)
        self.assert_copy(reused, Path(first['path']), reused=True)
        self.assertEqual(second_source.read_bytes(), self.bytes)

    def test_collision_preserves_existing_bytes_and_numbers_new_file(self):
        first = self.export('Jump')
        original_destination = Path(first['path'])
        different = clip_bytes(duration=32)
        source_two = self.source_dir / '000000000002.owanimclip'
        source_two.write_bytes(different)
        second = self.export('Jump', source=source_two)
        self.assert_copy(second, original_destination.with_name('Jump (2).owanimclip'), expected=different)
        self.assertEqual(original_destination.read_bytes(), self.bytes)
        repeated = self.export('Jump', source=source_two)
        self.assertEqual(repeated['path'], second['path'])
        self.assertTrue(repeated['reused'])
        self.assertEqual(len(list(original_destination.parent.glob('*.owanimclip'))), 2)
        self.assert_original_untouched()

    def test_existing_directory_is_not_overwritten_by_export(self):
        output = self.root / 'Renamed Animations'
        blocked = output / 'Wave.owanimclip'
        blocked.mkdir(parents=True)
        (blocked / 'keep.txt').write_text('keep', encoding='utf-8')
        result = self.export('Wave')
        self.assert_copy(result, output / 'Wave (2).owanimclip')
        self.assertEqual((blocked / 'keep.txt').read_text(encoding='utf-8'), 'keep')

    def test_windows_invalid_and_reserved_names_are_rejected(self):
        invalid = ['', '   ', '.', '..', '../escape', r'..\escape', 'walk/run',
                   'bad:name', 'bad<name', 'bad>name', 'bad"name', 'bad|name',
                   'bad?name', 'bad*name', 'bad\x00name', 'bad\x1fname', 'walk.',
                   'CON', 'con.owanimclip', 'NUL.test', 'PRN', 'AUX', 'COM1',
                   'com9', 'LPT1', 'lpt9', 'COM¹', 'LPT²', 'NUL.owanimclip', 'x'*300]
        for nickname in invalid:
            with self.subTest(nickname=nickname):
                response = self.api.export_animation(str(self.source), nickname, options=CLIP_ONLY)
                self.assertFalse(response['ok'], response)
                self.assertTrue(response.get('error'))
        folder = self.root / 'Renamed Animations'
        self.assertEqual(list(folder.glob('*.owanimclip')), [])
        self.assert_original_untouched()

    def test_reserved_name_prefix_is_not_over_rejected(self):
        for nickname in ('Console', 'AUXiliary', 'COM10', 'LPT10'):
            with self.subTest(nickname=nickname):
                self.assertEqual(self.export(nickname)['name'], nickname + '.owanimclip')

    def test_missing_wrong_extension_and_directory_sources_fail(self):
        wrong = self.source_dir / 'original.txt'
        wrong.write_bytes(self.bytes)
        for source in (self.source_dir / 'missing.owanimclip', wrong, self.source_dir):
            with self.subTest(source=source):
                result = self.api.export_animation(str(source), 'Wave', options=CLIP_ONLY)
                self.assertFalse(result['ok'], result)
        self.assert_original_untouched()

    def test_exact_source_destination_is_rejected_without_modifying_source(self):
        result = self.api.export_animation(str(self.source), self.source.stem, str(self.source.parent), CLIP_ONLY)
        self.assertFalse(result['ok'], result)
        self.assertTrue(result.get('error'))
        self.assertEqual(list(self.source_dir.glob('*.owanimclip')), [self.source])
        self.assert_original_untouched()
        scan = self.ok(self.api.scan_root(str(self.root)))
        self.assertEqual([Path(row['path']) for row in scan['clips']], [self.source])

    def test_custom_absolute_output_folder_is_created_outside_root(self):
        output = self.workspace / 'Animation Picks' / 'New Folder'
        selected = self.ok(self.api.set_output_folder(str(output)))
        self.assertEqual(Path(selected['outputFolder']), output)
        self.assert_copy(self.export('Wave'), output / 'Wave.owanimclip')
        self.assert_original_untouched()

    def test_invalid_output_paths_fail(self):
        regular_file = self.workspace / 'not-a-directory.txt'
        regular_file.write_text('keep', encoding='utf-8')
        for output in ('', ' ', 'relative/output', str(regular_file)):
            with self.subTest(output=output):
                result = self.api.set_output_folder(output)
                self.assertFalse(result['ok'], result)
        self.assertEqual(regular_file.read_text(encoding='utf-8'), 'keep')
        self.assert_original_untouched()

    def test_legacy_per_root_destinations_survive_but_queue_keeps_common_output(self):
        first_output = self.workspace / 'Hero Picks'
        self.ok(self.api.set_output_folder(str(first_output)))
        second_root = self.workspace / 'Other Hero'
        second_root.mkdir()
        second_scan = self.ok(self.api.scan_root(str(second_root)))
        self.assertEqual(Path(second_scan['outputFolder']), second_root / 'Renamed Animations')
        second_output = self.workspace / 'Other Picks'
        self.ok(self.api.set_output_folder(str(second_output)))
        first_scan = self.ok(self.api.scan_root(str(self.root)))
        self.assertEqual(Path(first_scan['outputFolder']), first_output)
        restarted = BrowserAPI(self.state)
        self.assertEqual(Path(self.ok(restarted.bootstrap())['outputFolder']), second_output)
        self.assertEqual(Path(self.ok(restarted.scan_root(str(second_root)))['outputFolder']), second_output)

    def test_export_subfolder_is_excluded_from_root_scan(self):
        self.export('Wave')
        scan = self.ok(self.api.scan_root(str(self.root)))
        self.assertEqual([Path(row['path']) for row in scan['clips']], [self.source])

    def test_previous_output_subfolders_stay_excluded_after_change_and_restart(self):
        self.export('Wave')
        custom = self.root / 'Chosen Motions'
        self.ok(self.api.set_output_folder(str(custom)))
        self.export('Turn')
        restarted = BrowserAPI(self.state)
        scan = self.ok(restarted.scan_root(str(self.root)))
        self.assertEqual([Path(row['path']) for row in scan['clips']], [self.source])
        self.assertEqual(Path(scan['outputFolder']), custom)

    def test_output_equal_to_root_excludes_only_exported_files(self):
        root_original = self.root / '000000000003.owanimclip'
        root_original.write_bytes(clip_bytes(duration=40))
        self.ok(self.api.set_output_folder(str(self.root)))
        exported = self.export('Wave')
        self.assert_copy(exported, self.root / 'Wave.owanimclip')
        scan = self.ok(self.api.scan_root(str(self.root)))
        self.assertEqual({Path(row['path']) for row in scan['clips']}, {self.source, root_original})
        restarted = BrowserAPI(self.state)
        scanned = self.ok(restarted.scan_root(str(self.root)))
        self.assertEqual({Path(row['path']) for row in scanned['clips']}, {self.source, root_original})

    def test_output_in_existing_source_folder_keeps_originals_visible(self):
        self.ok(self.api.set_output_folder(str(self.source_dir)))
        # Merely selecting a folder must not remove existing originals from
        # the library before the user has even exported anything.
        selected = self.ok(self.api.scan_root(str(self.root)))
        self.assertEqual([Path(row['path']) for row in selected['clips']], [self.source])
        copy = self.export('Wave')
        self.assert_copy(copy, self.source_dir / 'Wave.owanimclip')
        restarted = BrowserAPI(self.state)
        scanned = self.ok(restarted.scan_root(str(self.root)))
        self.assertEqual([Path(row['path']) for row in scanned['clips']], [self.source])

    def test_failed_write_cleans_only_new_incomplete_copy(self):
        output = self.root / 'Renamed Animations'
        output.mkdir()
        existing = output / 'Wave.owanimclip'
        previous = clip_bytes(duration=32)
        existing.write_bytes(previous)

        def fail_after_partial_write(original, destination, length):
            destination.write(original.read(10))
            raise OSError('Simulated disk write failure')

        with patch('owpreview.export_bundle.shutil.copyfileobj', side_effect=fail_after_partial_write):
            response = self.api.export_animation(str(self.source), 'Wave', options=CLIP_ONLY)
        self.assertFalse(response['ok'], response)
        self.assertIn('Simulated disk write failure', response['error'])
        self.assertEqual(existing.read_bytes(), previous)
        self.assertFalse((output / 'Wave (2).owanimclip').exists())
        self.assert_original_untouched()

    def test_concurrent_repeat_requests_do_not_overwrite_or_duplicate(self):
        # Both native bridge and loopback server may dispatch overlapping calls.
        # Equal requests should converge on one immutable destination copy.
        with ThreadPoolExecutor(max_workers=4) as workers:
            results = list(workers.map(lambda _: self.api.export_animation(str(self.source), 'Concurrent', options=CLIP_ONLY), range(8)))
        destinations = [self.ok(result) for result in results]
        self.assertEqual(len({row['path'] for row in destinations}), 1)
        self.assertEqual(sum(not row['reused'] for row in destinations), 1)
        self.assertEqual(Path(destinations[0]['path']).read_bytes(), self.bytes)
        self.assert_original_untouched()


if __name__ == '__main__':
    unittest.main()
