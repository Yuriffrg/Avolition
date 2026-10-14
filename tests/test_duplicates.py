"""Exact duplicate detection must never discard an original animation row."""
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from owpreview.duplicates import DuplicateIndex
from owpreview.formats import read_clip_header
from test_formats import clip_bytes


class DuplicateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.index = DuplicateIndex()

    def clip(self, relative, content=None):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(clip_bytes() if content is None else content)
        return read_clip_header(path)

    def test_identical_skin_exports_and_renamed_copy_share_one_key(self):
        rows = [self.clip('Default/001.owanimclip'), self.clip('Skin/001.owanimclip'),
                self.clip('Other Skin/reload.owanimclip')]
        original_paths = [row['path'] for row in rows]
        self.assertEqual(self.index.annotate(rows), [])
        self.assertEqual(len({row['duplicateKey'] for row in rows}), 1)
        self.assertEqual([row['duplicateCount'] for row in rows], [3, 3, 3])
        self.assertEqual([row['path'] for row in rows], original_paths)
        for row in rows:
            self.assertEqual(Path(row['path']).read_bytes(), clip_bytes())

    def test_same_name_size_and_headers_with_different_motion_are_distinct(self):
        contents = bytearray(clip_bytes())
        # Keep the entire header, filename and size identical, change track data.
        contents[-1] ^= 1
        rows = [self.clip('Default/001.owanimclip'), self.clip('Skin/001.owanimclip', contents)]
        self.assertEqual(self.index.annotate(rows), [])
        self.assertNotEqual(rows[0]['duplicateKey'], rows[1]['duplicateKey'])
        self.assertEqual([row['duplicateCount'] for row in rows], [1, 1])

    def test_unique_size_or_header_does_not_require_full_file_read(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip', clip_bytes(duration=32)),
                self.clip('003.owanimclip', clip_bytes() + b'padding')]
        with patch.object(Path, 'open', side_effect=AssertionError('Unnecessary full-file read')):
            self.assertEqual(self.index.annotate(rows), [])
        self.assertEqual(len({row['duplicateKey'] for row in rows}), 3)

    def test_repeated_scan_uses_cache_and_same_size_modification_invalidates(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip')]
        self.index.annotate(rows)
        with patch.object(Path, 'open', side_effect=AssertionError('Cached file was reread')):
            self.assertEqual(self.index.annotate(rows), [])
        path = Path(rows[1]['path'])
        old_time = path.stat().st_mtime_ns
        contents = bytearray(path.read_bytes())
        contents[-1] ^= 1
        path.write_bytes(contents)
        os.utime(path, ns=(old_time + 1_000_000_000, old_time + 1_000_000_000))
        self.assertEqual(self.index.annotate(rows), [])
        self.assertNotEqual(rows[0]['duplicateKey'], rows[1]['duplicateKey'])
        self.assertEqual([row['duplicateCount'] for row in rows], [1, 1])

    def test_unreadable_file_is_retained_as_unique_without_aborting(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip'), self.clip('003.owanimclip')]
        inaccessible = rows[1]['path']
        original_open = Path.open

        def guarded_open(path, *args, **kwargs):
            if str(path) == inaccessible:
                raise PermissionError('Access denied')
            return original_open(path, *args, **kwargs)

        with patch.object(Path, 'open', guarded_open):
            warnings = self.index.annotate(rows)
        self.assertEqual(len(warnings), 1)
        self.assertIn('Access denied', rows[1]['duplicateError'])
        self.assertEqual(rows[1]['duplicateCount'], 1)
        self.assertNotEqual(rows[1]['duplicateKey'], rows[0]['duplicateKey'])
        self.assertEqual(rows[0]['duplicateKey'], rows[2]['duplicateKey'])
        self.assertEqual(rows[0]['duplicateCount'], 2)
        # Access can recover on a later scan; failures are not cached.
        self.assertEqual(self.index.annotate(rows), [])
        self.assertEqual([row['duplicateCount'] for row in rows], [3, 3, 3])
        self.assertNotIn('duplicateError', rows[1])

    def test_invalid_headers_are_not_hidden_together(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip')]
        for row in rows:
            row['error'] = 'Truncated clip'
        with patch.object(Path, 'open', side_effect=AssertionError('Invalid file was read')):
            self.index.annotate(rows)
        self.assertNotEqual(rows[0]['duplicateKey'], rows[1]['duplicateKey'])
        self.assertEqual([row['duplicateCount'] for row in rows], [1, 1])

    def test_changed_size_since_header_scan_is_not_grouped(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip')]
        Path(rows[1]['path']).write_bytes(clip_bytes() + b'changed')
        self.assertEqual(len(self.index.annotate(rows)), 1)
        self.assertNotEqual(rows[0]['duplicateKey'], rows[1]['duplicateKey'])
        self.assertIn('changed', rows[1]['duplicateError'])

    def test_cache_is_bounded_and_eviction_does_not_change_results(self):
        index = DuplicateIndex(max_entries=2)
        rows = [self.clip(f'{number:03}.owanimclip') for number in range(4)]
        index.annotate(rows)
        self.assertLessEqual(len(index._cache), 2)
        index.annotate(rows)
        self.assertLessEqual(len(index._cache), 2)
        self.assertEqual([row['duplicateCount'] for row in rows], [4, 4, 4, 4])

    def test_file_modified_during_hash_does_not_leave_a_cached_identity(self):
        rows = [self.clip('001.owanimclip'), self.clip('002.owanimclip')]
        path = Path(rows[1]['path'])
        original_open = Path.open

        class ChangingFile:
            def __init__(self):
                self.file = original_open(path, 'rb')

            def __enter__(self):
                return self

            def read(self, length):
                return self.file.read(length)

            def __exit__(self, *args):
                self.file.close()
                path.write_bytes(clip_bytes() + b'changed')

        def open_while_changing(candidate, *args, **kwargs):
            if candidate == path and args == ('rb',):
                return ChangingFile()
            return original_open(candidate, *args, **kwargs)

        with patch.object(Path, 'open', open_while_changing):
            warnings = self.index.annotate(rows)
        self.assertEqual(len(warnings), 1)
        self.assertNotEqual(rows[0]['duplicateKey'], rows[1]['duplicateKey'])
        self.assertNotIn(self.index._path_key(path), self.index._cache)

    def test_scan_reports_duplicate_counts_and_keeps_each_paths_annotation(self):
        rows = [self.clip('Assets/Default/001.owanimclip'), self.clip('Assets/Skin/001.owanimclip'),
                self.clip('Assets/Default/002.owanimclip', clip_bytes(duration=32))]
        api = BrowserAPI(self.root / 'state')
        self.assertTrue(api.save_annotation(rows[1]['path'], {'name': 'Reload'})['ok'])
        response = api.scan_root(str(self.root / 'Assets'))
        self.assertTrue(response['ok'], response)
        scan = response['data']
        self.assertEqual(len(scan['clips']), 3)
        self.assertEqual(scan['uniqueClipCount'], 2)
        self.assertEqual(scan['duplicateClipCount'], 1)
        by_path = {row['path']: row for row in scan['clips']}
        self.assertEqual(by_path[rows[1]['path']]['annotation']['name'], 'Reload')
        self.assertEqual(by_path[rows[0]['path']]['annotation']['name'], '')
        self.assertEqual(by_path[rows[0]['path']]['duplicateCount'], 2)
        self.assertEqual(by_path[rows[2]['path']]['duplicateCount'], 1)


if __name__ == '__main__':
    unittest.main()
