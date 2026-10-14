"""Matching FBX/clip names, collision safety, and failed-export cleanup."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from owpreview.export_bundle import export_pair, export_bundle, fbx_options


class ExportBundleTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.source = self.base / 'source.owanimclip'
        self.source.write_bytes(b'original animation bytes')
        self.output = self.base / 'Output'
        self.fbx_bytes = b'FBX fixture'

    def build(self, destination, textures):
        destination.write_bytes(self.fbx_bytes)
        textures.mkdir()
        (textures / 'shared_color.png').write_bytes(b'PNG fixture')
        return {'warnings': ['Test preview note']}

    def export(self, builder=None):
        return export_pair(self.source, self.output, 'Wave.owanimclip', builder or self.build)

    def test_matching_names_relative_textures_and_source_untouched(self):
        result = self.export()
        self.assertEqual(Path(result['path']).name, 'Wave.owanimclip')
        self.assertEqual(Path(result['fbxPath']).name, 'Wave.fbx')
        self.assertEqual(Path(result['textureFolder']), self.output / 'tex')
        self.assertEqual((self.output / 'Wave.owanimclip').read_bytes(), self.source.read_bytes())
        self.assertEqual(result['textureCount'], 1)
        self.assertEqual(result['warnings'], ['Test preview note'])
        self.assertFalse(result['reused'])
        self.assertFalse(list(self.output.glob('.whitehound-export-*')))

    def test_repeat_reuses_both_files_without_touching_timestamps(self):
        first = self.export()
        times = {file: Path(file).stat().st_mtime_ns for file in first['files']}
        repeated = self.export()
        self.assertTrue(repeated['reused'])
        self.assertEqual(first['files'], repeated['files'])
        self.assertEqual(times, {file: Path(file).stat().st_mtime_ns for file in repeated['files']})

    def test_fbx_collision_numbers_both_even_when_raw_clip_matches(self):
        first = self.export()
        self.fbx_bytes = b'FBX with different axis or rig'
        second = self.export()
        self.assertEqual(Path(second['path']).name, 'Wave (2).owanimclip')
        self.assertEqual(Path(second['fbxPath']).name, 'Wave (2).fbx')
        self.assertEqual(Path(first['fbxPath']).read_bytes(), b'FBX fixture')
        self.assertEqual(len(list((self.output / 'tex').glob('*.png'))), 1)

    def test_existing_identical_raw_copy_can_gain_matching_fbx(self):
        self.output.mkdir()
        target = self.output / 'Wave.owanimclip'
        target.write_bytes(self.source.read_bytes())
        original_time = target.stat().st_mtime_ns
        result = self.export()
        self.assertEqual(result['path'], str(target))
        self.assertFalse(result['reused'])
        self.assertEqual(target.stat().st_mtime_ns, original_time)

    def test_existing_fbx_alone_is_never_replaced(self):
        self.output.mkdir()
        (self.output / 'Wave.fbx').write_bytes(b'User artwork')
        result = self.export()
        self.assertEqual(Path(result['fbxPath']).name, 'Wave (2).fbx')
        self.assertEqual((self.output / 'Wave.fbx').read_bytes(), b'User artwork')

    def test_build_failure_publishes_no_files(self):
        def failed(destination, textures):
            destination.write_bytes(b'partial FBX')
            raise ValueError('Bad skeleton')
        with self.assertRaisesRegex(ValueError, 'Bad skeleton'):
            self.export(failed)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_publish_failure_removes_only_new_files(self):
        self.output.mkdir()
        prior = self.output / 'keep.fbx'
        prior.write_bytes(b'User work')
        from owpreview import export_bundle
        original = export_bundle.shutil.copyfileobj

        def fail_fbx(source, target, size):
            if str(target.name).endswith('.fbx'):
                target.write(b'partial')
                raise OSError('Disk full')
            return original(source, target, size)

        with patch('owpreview.export_bundle.shutil.copyfileobj', side_effect=fail_fbx):
            with self.assertRaisesRegex(OSError, 'Disk full'):
                self.export()
        self.assertEqual(prior.read_bytes(), b'User work')
        self.assertFalse((self.output / 'Wave.fbx').exists())
        self.assertFalse((self.output / 'Wave.owanimclip').exists())
        self.assertFalse(list((self.output / 'tex').glob('*.png')))

    def test_changed_existing_texture_causes_error_without_overwriting(self):
        self.output.mkdir()
        (self.output / 'tex').mkdir()
        prior = self.output / 'tex/shared_color.png'
        prior.write_bytes(b'Edited by user')
        with self.assertRaises(FileExistsError):
            self.export()
        self.assertEqual(prior.read_bytes(), b'Edited by user')
        self.assertFalse((self.output / 'Wave.owanimclip').exists())

    def test_options_have_y_up_and_keep_motion_defaults(self):
        self.assertEqual(fbx_options(), {'enabled': False, 'upAxis': 'Y', 'includeMesh': True,
                                        'bakeTextures': True, 'rootMotion': True, 'c4dEnabled': True, 'clipEnabled': False})
        self.assertEqual(fbx_options({'upAxis': 'Z'})['upAxis'], 'Z')
        for invalid in ({'upAxis': 'X'}, {'enabled': 'yes'}, {'rootMotion': 1},
                        {'clipEnabled': 1}, {'c4dEnabled': 'yes'}, []):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                fbx_options(invalid)

    def test_three_formats_publish_with_one_collision_suffix(self):
        self.output.mkdir()
        (self.output/'Wave.c4d').write_bytes(b'Existing C4D project')
        def build(paths, textures):
            for kind, path in paths.items():
                path.write_bytes(('New ' + kind).encode())
        result = export_bundle(self.source, self.output, 'Wave.owanimclip', build, ('clip', 'fbx', 'c4d'))
        self.assertEqual(Path(result['path']).stem, 'Wave (2)')
        self.assertEqual(Path(result['fbxPath']).stem, 'Wave (2)')
        self.assertEqual(Path(result['c4dPath']).stem, 'Wave (2)')
        self.assertEqual((self.output/'Wave.c4d').read_bytes(), b'Existing C4D project')

    def test_c4d_failure_does_not_publish_fbx_or_clip(self):
        def build(paths, textures):
            paths['fbx'].write_bytes(b'Completed FBX')
            paths['c4d'].write_bytes(b'Partial native scene')
            raise RuntimeError('Octane missing')
        with self.assertRaisesRegex(RuntimeError, 'Octane missing'):
            export_bundle(self.source, self.output, 'Wave.owanimclip', build, ('clip', 'fbx', 'c4d'))
        self.assertEqual(list(self.output.iterdir()), [])

    def test_disabled_formats_do_not_influence_collision_or_get_touched(self):
        self.output.mkdir()
        original_clip = self.output/'Wave.owanimclip'
        original_fbx = self.output/'Wave.fbx'
        original_clip.write_bytes(b'User clip: intentionally different')
        original_fbx.write_bytes(b'User FBX: intentionally different')
        before = {path: (path.read_bytes(), path.stat().st_mtime_ns)
                  for path in (original_clip, original_fbx)}
        def build(paths, textures):
            self.assertEqual(set(paths), {'c4d'})
            paths['c4d'].write_bytes(b'Native scene fixture')
        result = export_bundle(self.source, self.output, 'Wave.owanimclip', build, ('c4d',))
        self.assertEqual(result['name'], 'Wave.c4d')
        self.assertEqual(result['path'], result['c4dPath'])
        self.assertEqual(result['files'], [str(self.output/'Wave.c4d')])
        self.assertNotIn('clipPath', result)
        self.assertNotIn('fbxPath', result)
        self.assertEqual(before, {path: (path.read_bytes(), path.stat().st_mtime_ns) for path in before})

    def test_scene_only_can_use_source_folder_and_source_stem(self):
        def build(paths, textures):
            paths['fbx'].write_bytes(b'FBX fixture')
        result = export_bundle(self.source, self.base, self.source.name, build, ('fbx',))
        self.assertEqual(Path(result['path']), self.source.with_suffix('.fbx'))
        self.assertEqual(self.source.read_bytes(), b'original animation bytes')

    def test_empty_selection_does_not_invoke_builder_or_create_folder(self):
        def unexpected(*args):
            self.fail('Empty selection called the builder')
        result = export_bundle(self.source, self.output, 'Wave.owanimclip', unexpected, ())
        self.assertTrue(result['skipped'])
        self.assertEqual(result['files'], [])
        self.assertIsNone(result['path'])
        self.assertFalse(self.output.exists())

    def test_scene_only_failure_rolls_back_new_scenes_without_touching_disabled_clip(self):
        self.output.mkdir()
        original = self.output/'Wave.owanimclip'
        original.write_bytes(b'Existing unrelated clip')
        def build(paths, textures):
            for kind, destination in paths.items():
                destination.write_bytes(kind.encode())
        from owpreview import export_bundle as module
        copy = module.shutil.copyfileobj
        def fail_c4d(source, target, size):
            if str(target.name).endswith('.c4d'):
                target.write(b'partial')
                raise OSError('Disk full')
            return copy(source, target, size)
        with patch('owpreview.export_bundle.shutil.copyfileobj', side_effect=fail_c4d):
            with self.assertRaisesRegex(OSError, 'Disk full'):
                export_bundle(self.source, self.output, 'Wave.owanimclip', build, ('fbx', 'c4d'))
        self.assertEqual(list(self.output.iterdir()), [original])
        self.assertEqual(original.read_bytes(), b'Existing unrelated clip')


if __name__ == '__main__':
    unittest.main()
