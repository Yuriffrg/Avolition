"""Fast binding metadata is exact, bounded and independent of pose loading."""
from pathlib import Path
import os
import tempfile
import unittest
from unittest.mock import patch

from owpreview.clip_bindings import ClipBindingIndex
from owpreview.formats import (FormatError, _ClipBindingReader, load_clip,
                               read_clip_binding_metadata)
from test_formats import clip_bytes, pack, string


class ClipBindingTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()

    def write(self, name, data):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def test_exact_arbitrary_names_and_empty_records_match_full_parser(self):
        names = ['bone_0075', 'bone_004A', 'custom.Root 日本語', 'a' * 128, 'cloth_0002']
        source = self.write('Names 日本語.owanimclip', clip_bytes([
            (name, [] if index % 2 else [(0, [(0, (0, 0, 0))])]) for index, name in enumerate(names)]))
        metadata = read_clip_binding_metadata(source)
        self.assertEqual(metadata['boneNames'], names)
        self.assertEqual(metadata['boneNames'], [bone['name'] for bone in load_clip(source)['bones']])
        self.assertEqual(metadata['boneCount'], len(names))
        self.assertEqual(metadata['path'], str(source))
        self.assertEqual(metadata['warnings'], [])

    def test_successful_empty_clip_is_not_a_parse_error(self):
        source = self.write('Empty.owanimclip', clip_bytes([], duration=1))
        self.assertEqual(read_clip_binding_metadata(source)['boneNames'], [])
        self.assertEqual(ClipBindingIndex().read([str(source)]),
                         {'clips': [{'path': str(source), 'boneNames': []}]})

    def test_key_arrays_are_sought_over_without_decoding_or_allocation(self):
        keys = [(frame, (1, 2, 3)) for frame in range(5000)]
        source = self.write('Many keys.owanimclip', clip_bytes([('bone_0001', [(0, keys)]),
                                                               ('bone_0002', [(0, keys)])], duration=5000))
        read_sizes, skipped = [], []
        original_take, original_skip = _ClipBindingReader.take, _ClipBindingReader.skip

        def take(reader, size):
            read_sizes.append(size)
            return original_take(reader, size)

        def skip(reader, size):
            skipped.append(size)
            return original_skip(reader, size)

        with patch.object(_ClipBindingReader, 'take', take), patch.object(_ClipBindingReader, 'skip', skip), \
             patch('owpreview.formats._normalize_quaternion', side_effect=AssertionError('Decoded animation values')):
            metadata = read_clip_binding_metadata(source)
        self.assertEqual(metadata['boneNames'], ['bone_0001', 'bone_0002'])
        self.assertEqual(skipped, [5000 * 16, 5000 * 16])
        self.assertLess(sum(read_sizes), 128)
        self.assertEqual(sum(read_sizes) + sum(skipped), source.stat().st_size)

    def test_unknown_tracks_and_trailing_extensions_preserve_binding_names(self):
        source = self.write('Extensions.owanimclip', clip_bytes([('raw_foo', [(7, [(2, (1, 2))])])]) + b'extra')
        metadata = read_clip_binding_metadata(source)
        full = load_clip(source)
        self.assertEqual(metadata['boneNames'], [bone['name'] for bone in full['bones']])
        self.assertEqual(metadata['warnings'], full['warnings'])

    def test_structural_corruption_is_an_error_not_zero_matches(self):
        header = pack('HHIfI', 2, 0, 1, 30, 1)
        bone = header + string('bone_0001')
        cases = {
            'truncated-header': b'bad',
            'bad-version': pack('HHIfI', 1, 0, 0, 30, 1),
            'invalid-fps': pack('HHIfI', 2, 0, 0, float('nan'), 1),
            'invalid-duration': pack('HHIfI', 2, 0, 0, 30, 0),
            'too-many-bones': pack('HHIfI', 2, 0, 65537, 30, 1),
            'no-bone-record': header,
            'truncated-string': header + b'\x05abc',
            'invalid-utf8': header + b'\x01\xff' + pack('I', 0),
            'invalid-string-length': header + b'\xff' * 5,
            'empty-bone-name': header + string('') + pack('I', 0),
            'duplicate-bone': clip_bytes([('same', []), ('same', [])]),
            'too-many-channels': bone + pack('I', 17),
            'truncated-channel-header': bone + pack('I', 1) + b'abcd',
            'wrong-components': bone + pack('IIII', 1, 1, 0, 3),
            'too-many-components': bone + pack('IIII', 1, 9, 0, 17),
            'too-many-keys': bone + pack('IIII', 1, 0, 10000001, 3),
            'truncated-keys': bone + pack('IIII', 1, 0, 2, 3) + pack('I3f', 0, 0, 0, 0),
            'truncated-unknown-keys': bone + pack('IIII', 1, 9, 2, 4),
            'duplicate-channel': clip_bytes([('same', [(0, []), (0, [])])]),
        }
        index = ClipBindingIndex()
        for label, data in cases.items():
            with self.subTest(label=label):
                source = self.write(label + '.owanimclip', data)
                with self.assertRaises(FormatError):
                    read_clip_binding_metadata(source)
                result = index.read([str(source)])['clips'][0]
                self.assertIsNone(result['boneNames'])
                self.assertTrue(result['error'])
                self.assertEqual(result['path'], str(source))

    def test_cache_reuses_names_but_returns_independent_lists(self):
        source = self.write('Cached.owanimclip', clip_bytes([('first', []), ('second', [])]))
        index = ClipBindingIndex()
        with patch('owpreview.clip_bindings.read_clip_binding_metadata', wraps=read_clip_binding_metadata) as read:
            result = index.read([str(source), str(source)])
            self.assertEqual(read.call_count, 1)
            result['clips'][0]['boneNames'].append('mutated')
            self.assertEqual(result['clips'][1]['boneNames'], ['first', 'second'])
            self.assertEqual(index.read([str(source)])['clips'][0]['boneNames'], ['first', 'second'])
            self.assertEqual(read.call_count, 1)

    def test_changed_file_invalidates_cache_including_replacement_with_same_mtime_size(self):
        source = self.write('Changed.owanimclip', clip_bytes([('first', [])]))
        index = ClipBindingIndex()
        self.assertEqual(index.read([str(source)])['clips'][0]['boneNames'], ['first'])
        old = source.stat()
        source.write_bytes(clip_bytes([('other', [])]))
        os.utime(source, ns=(old.st_atime_ns, old.st_mtime_ns + 2_000_000_000))
        self.assertEqual(index.read([str(source)])['clips'][0]['boneNames'], ['other'])
        old = source.stat()
        replacement = self.write('Replacement.owanimclip', clip_bytes([('third', [])]))
        os.utime(replacement, ns=(old.st_atime_ns, old.st_mtime_ns))
        replacement.replace(source)
        self.assertEqual(source.stat().st_size, old.st_size)
        self.assertEqual(source.stat().st_mtime_ns, old.st_mtime_ns)
        self.assertEqual(index.read([str(source)])['clips'][0]['boneNames'], ['third'])

    def test_read_during_change_returns_unknown_and_does_not_cache_stale_names(self):
        source = self.write('Race.owanimclip', clip_bytes([('first', [])]))
        index = ClipBindingIndex()

        def change(path):
            result = read_clip_binding_metadata(path)
            source.write_bytes(clip_bytes([('longer replacement', [])]))
            return result

        with patch('owpreview.clip_bindings.read_clip_binding_metadata', side_effect=change):
            result = index.read([str(source)])['clips'][0]
        self.assertIsNone(result['boneNames'])
        self.assertIn('changed', result['error'])
        self.assertEqual(index.read([str(source)])['clips'][0]['boneNames'], ['longer replacement'])

    def test_failures_do_not_poison_cache_and_mixed_requests_keep_order(self):
        source = self.write('Good.owanimclip', clip_bytes([('bone_A', [])]))
        missing = self.root / 'Missing.owanimclip'
        wrong_type = self.write('File.txt', b'not a clip')
        index = ClipBindingIndex()
        requested = [str(source), str(missing), str(wrong_type), 'relative.owanimclip', None, 42]
        result = index.read(requested)['clips']
        self.assertEqual(result[0]['boneNames'], ['bone_A'])
        self.assertEqual([entry['path'] for entry in result], requested[:4] + [None, None])
        self.assertTrue(all(entry['boneNames'] is None and entry['error'] for entry in result[1:]))
        missing.write_bytes(clip_bytes([('bone_B', [])]))
        self.assertEqual(index.read([str(missing)])['clips'][0]['boneNames'], ['bone_B'])

    def test_cache_has_lru_count_and_memory_bounds(self):
        sources = [self.write(f'{number}.owanimclip', clip_bytes([(f'bone_{number}', [])])) for number in range(3)]
        index = ClipBindingIndex(max_entries=2)
        with patch('owpreview.clip_bindings.read_clip_binding_metadata', wraps=read_clip_binding_metadata) as read:
            index.read([str(source) for source in sources[:2]])
            index.read([str(sources[0]), str(sources[2])])
            self.assertEqual(read.call_count, 3)
            index.read([str(sources[0])])
            self.assertEqual(read.call_count, 3)
            index.read([str(sources[1])])
            self.assertEqual(read.call_count, 4)
        for index in (ClipBindingIndex(max_entries=0), ClipBindingIndex(max_bytes=1)):
            with patch('owpreview.clip_bindings.read_clip_binding_metadata', wraps=read_clip_binding_metadata) as read:
                index.read([str(sources[0]), str(sources[0])])
                self.assertEqual(read.call_count, 2)
                self.assertEqual(len(index._cache), 0)

    def test_batch_bound_and_empty_request(self):
        index = ClipBindingIndex()
        self.assertEqual(index.read([]), {'clips': []})
        for paths in (None, 'path.owanimclip', {}, ['missing'] * 129):
            with self.assertRaisesRegex(ValueError, '128'):
                index.read(paths)
        self.assertEqual(len(index.read(['relative.owanimclip'] * 128)['clips']), 128)


if __name__ == '__main__':
    unittest.main()
