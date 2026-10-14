"""API transactions with real assets/FBX/textures and an explicitly mocked C4D writer.

These tests validate application wiring and publication only. The fixture bytes
are not Cinema 4D files and do not establish native Cinema 4D/Octane compatibility.
"""

from copy import deepcopy
import json
from itertools import product
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from create_material_demo import generate
from owpreview.formats import load_asset
from test_formats import clip_bytes, model_bytes


MOCK_C4D_BYTES = b'MOCK C4D SCENE: API transaction fixture, not a native Cinema 4D file.'
FBX_MAGIC = b'Kaydara FBX Binary  \x00\x1a\x00'
PNG_MAGIC = b'\x89PNG\r\n\x1a\n'


class MockBridgeClient:
    """Record the bridge contract, substituting only Cinema's native writer."""

    def __init__(self, fail=False):
        self.calls = []
        self.fail = fail

    def status(self):
        return {'connected': True, 'octane': True, 'message': 'Mock test bridge connected.'}

    def export(self, payload, destination, texture_folder):
        destination, texture_folder = Path(destination), Path(texture_folder)
        self.calls.append({
            'payload': deepcopy(payload),
            'destination': destination,
            'textures': {p.name: p.read_bytes() for p in texture_folder.glob('*.png')},
            'stagedFbx': (destination.parent/'animation.fbx').read_bytes()
                         if (destination.parent/'animation.fbx').exists() else None,
        })
        destination.write_bytes(MOCK_C4D_BYTES)
        if self.fail:
            raise RuntimeError('Mock native writer failed after writing a partial scene.')
        return {'warnings': ['Mock native writer: native C4D compatibility is not tested.']}


class C4DAPITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.root = generate(self.base/'Exports')
        self.profile = self.base/'Profile'
        self.api = BrowserAPI(self.profile)
        self.assertTrue(self.api.scan_root(str(self.root))['ok'])
        self.bridge = MockBridgeClient()
        self.api._c4d_bridge = self.bridge
        self.entity = self.root/'Entities/MaterialRobot/MaterialRobot.owentity'
        self.source = self.root/'Animations/001_Wave.owanimclip'
        self.output = self.base/'Output'
        self.options = {'clipEnabled': True, 'enabled': False, 'c4dEnabled': True, 'upAxis': 'Y',
                        'includeMesh': True, 'bakeTextures': True, 'rootMotion': True,
                        'entityPath': str(self.entity), 'rootPath': str(self.root), 'rigId': 'auto'}

    def export(self, nickname='Wave', options=None, source=None):
        result = self.api.export_animation(str(source or self.source), nickname, str(self.output),
                                           options if options is not None else self.options)
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_c4d_only_and_combined_exports_publish_matching_names_and_shared_textures(self):
        original = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        for fbx_enabled in (False, True):
            with self.subTest(fbx=fbx_enabled):
                nickname = 'Combined' if fbx_enabled else 'Native only'
                result = self.export(nickname, dict(self.options, enabled=fbx_enabled))
                call = self.bridge.calls[-1]
                self.assertEqual(Path(result['path']).name, nickname+'.owanimclip')
                self.assertEqual(Path(result['path']).read_bytes(), self.source.read_bytes())
                self.assertEqual(Path(result['c4dPath']).name, nickname+'.c4d')
                self.assertEqual(Path(result['c4dPath']).read_bytes(), MOCK_C4D_BYTES)
                self.assertEqual('fbxPath' in result, fbx_enabled)
                if fbx_enabled:
                    self.assertEqual(Path(result['fbxPath']).name, nickname+'.fbx')
                    self.assertTrue(Path(result['fbxPath']).read_bytes().startswith(FBX_MAGIC))
                    self.assertTrue(call['stagedFbx'].startswith(FBX_MAGIC))
                else:
                    self.assertIsNone(call['stagedFbx'])
                    self.assertFalse((self.output/(nickname+'.fbx')).exists())
                self.assertGreater(result['textureCount'], 0)
                self.assertEqual(result['textureCount'], len(call['textures']))
                self.assertEqual(Path(result['textureFolder']), self.output/'tex')
                for filename, data in call['textures'].items():
                    self.assertTrue(data.startswith(PNG_MAGIC))
                    self.assertEqual((self.output/'tex'/filename).read_bytes(), data)
                for materials in call['payload']['materials'].values():
                    for material in materials.values():
                        for mapping in material.get('maps', {}).values():
                            relative = Path(mapping['relativePath'])
                            self.assertEqual(relative.parts[0], 'tex')
                            self.assertTrue((self.output/relative).is_file())
                self.assertEqual(call['payload']['name'], nickname)
                self.assertTrue(call['payload']['includeMesh'])
                self.assertGreater(call['payload']['animation']['matchedBones'], 0)
                self.assertTrue(any('Mock native writer' in warning for warning in result['warnings']))
                persisted = json.loads((self.profile/'catalog.json').read_text(encoding='utf-8'))
                self.assertTrue(set(map(self.api._key, result['files'])) <= set(persisted['exportedFiles']))
                self.assertFalse(call['destination'].parent.exists(), 'Staging directory was retained')
        self.assertEqual(original, {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()})

    def test_independent_format_selections_publish_only_selected_files(self):
        for clip, fbx, c4d in product((False, True), repeat=3):
            with self.subTest(clip=clip, fbx=fbx, c4d=c4d):
                self.output = self.base/f'Output-{int(clip)}{int(fbx)}{int(c4d)}'
                calls_before = len(self.bridge.calls)
                result = self.export(options=dict(self.options, clipEnabled=clip, enabled=fbx,
                                                   c4dEnabled=c4d, bakeTextures=False))
                extensions = [extension for extension, enabled in
                              (('owanimclip', clip), ('fbx', fbx), ('c4d', c4d)) if enabled]
                if not extensions:
                    self.assertTrue(result['skipped'])
                    self.assertIsNone(result['path'])
                    self.assertFalse(self.output.exists())
                else:
                    self.assertEqual(result['name'], 'Wave.'+extensions[0])
                    self.assertEqual(Path(result['path']), self.output/('Wave.'+extensions[0]))
                    self.assertEqual({path.name for path in self.output.iterdir()},
                                     {'Wave.'+extension for extension in extensions})
                    self.assertEqual({Path(path).name for path in result['files']},
                                     {'Wave.'+extension for extension in extensions})
                for kind, selected in (('clip', clip), ('fbx', fbx), ('c4d', c4d)):
                    self.assertEqual(kind+'Path' in result, selected)
                self.assertEqual(len(self.bridge.calls)-calls_before, int(c4d))
                if clip:
                    self.assertEqual(Path(result['clipPath']).read_bytes(), self.source.read_bytes())

    def test_all_off_skips_asset_and_folder_validation_without_touching_catalog_or_bridge(self):
        before = (self.profile/'catalog.json').read_bytes()
        with patch.object(self.bridge, 'status', side_effect=AssertionError('Unexpected bridge query')):
            result = self.api.export_animation('missing.owanimclip', '', 'invalid relative folder',
                        {'clipEnabled': False, 'enabled': False, 'c4dEnabled': False})
        self.assertTrue(result['ok'], result)
        self.assertEqual(result['data'], {'skipped': True, 'files': [], 'path': None, 'name': None})
        self.assertEqual(before, (self.profile/'catalog.json').read_bytes())
        self.assertFalse(self.output.exists())

    def test_raw_clip_only_needs_no_entity_or_cinema_connection(self):
        with patch.object(self.bridge, 'status', side_effect=AssertionError('Unexpected bridge query')):
            result = self.export(options={'clipEnabled': True, 'enabled': False, 'c4dEnabled': False})
        self.assertEqual(result['path'], result['clipPath'])
        self.assertEqual(Path(result['clipPath']).read_bytes(), self.source.read_bytes())
        self.assertEqual(result['files'], [result['clipPath']])

    def test_c4d_only_ignores_disabled_clip_collision_and_catalog_never_hides_original(self):
        self.output = self.source.parent
        before = self.source.read_bytes(), self.source.stat().st_mtime_ns
        result = self.export(self.source.stem, dict(self.options, clipEnabled=False, bakeTextures=False))
        self.assertEqual(Path(result['path']), self.source.with_suffix('.c4d'))
        self.assertNotIn('clipPath', result)
        self.assertNotIn(self.api._key(self.source), self.api._catalog['exportedFiles'])
        self.assertEqual(before, (self.source.read_bytes(), self.source.stat().st_mtime_ns))

    def test_captured_entity_and_shared_settings_reach_native_payload(self):
        asset = load_asset(str(self.entity), root_path=str(self.root))
        rig = asset['models'][0]
        root_index = next(i for i, bone in enumerate(rig['bones']) if bone['parent'] < 0)
        root_bone = rig['bones'][root_index]
        moving = self.root/'Animations/Root motion.owanimclip'
        moving.write_bytes(clip_bytes(bones=[(root_bone['name'], [
            (0, [(0, (0, 0, 0)), (2, (4, 2, 6))])])], duration=3))
        other = self.root/'Other.owmdl'
        other.write_bytes(model_bytes(name='Other preview model'))
        self.assertTrue(self.api.load_entity(str(other))['ok'])
        # Persist the opposite defaults: a request must use its captured options.
        self.assertTrue(self.api.set_export_settings({'includeMesh': True, 'bakeTextures': True,
                                                      'rootMotion': True, 'c4dEnabled': False})['ok'])
        for include_mesh, bake_textures in ((False, True), (True, False)):
            with self.subTest(include_mesh=include_mesh, bake_textures=bake_textures):
                result = self.export('Captured '+str(include_mesh), dict(self.options, upAxis='Z',
                    includeMesh=include_mesh, bakeTextures=bake_textures, rootMotion=False,
                    rigId=rig['id']), moving)
                call = self.bridge.calls[-1]
                payload = call['payload']
                self.assertEqual(payload['asset']['name'], asset['name'])
                self.assertEqual(payload['asset']['models'][0]['name'], rig['name'])
                self.assertNotIn('Other preview model', json.dumps(payload))
                self.assertEqual(payload['includeMesh'], include_mesh)
                self.assertEqual(payload['animation']['lastFrame'], 2)
                self.assertTrue(all(track['modelId'] == rig['id'] for track in payload['animation']['tracks']))
                root_track = next(t for t in payload['animation']['tracks'] if t['boneIndex'] == root_index)
                first, last = root_track['frames'][0], root_track['frames'][-1]
                self.assertAlmostEqual(first[12], last[12], msg='In-place setting did not lock root X')
                self.assertAlmostEqual(first[14], last[14], msg='In-place setting did not lock root Z')
                self.assertAlmostEqual(last[13]-first[13], 2, msg='Native Y-up motion was lost or rotated to Z-up')
                self.assertEqual(result['textureCount'], 0)
                self.assertIsNone(result['textureFolder'])
                self.assertFalse(call['textures'])
                self.assertTrue(payload['materials'], 'Material factors were discarded without textures')
                for materials in payload['materials'].values():
                    for material in materials.values():
                        self.assertEqual(material['maps'], {})
        self.assertFalse((self.output/'tex').exists())
        self.export('Moving root', dict(self.options, bakeTextures=False, rootMotion=True), moving)
        tracks = self.bridge.calls[-1]['payload']['animation']['tracks']
        root_track = next(t for t in tracks if t['boneIndex'] == root_index)
        first, last = root_track['frames'][0], root_track['frames'][-1]
        self.assertAlmostEqual(last[12]-first[12], 4)
        self.assertAlmostEqual(last[14]-first[14], 6)

    def test_native_failure_discards_staged_fbx_clip_and_textures_without_changing_existing_files(self):
        self.output.mkdir()
        (self.output/'tex').mkdir()
        (self.output/'Wave.owanimclip').write_bytes(b'Unrelated existing clip')
        (self.output/'Wave.fbx').write_bytes(b'Unrelated existing FBX')
        (self.output/'Wave.c4d').write_bytes(b'Unrelated existing native project')
        (self.output/'tex/user.png').write_bytes(b'Unrelated existing texture')
        existing = {p: p.read_bytes() for p in self.output.rglob('*') if p.is_file()}
        original = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        catalog_before = (self.profile/'catalog.json').read_bytes()
        self.bridge.fail = True
        result = self.api.export_animation(str(self.source), 'Wave', str(self.output),
                                           dict(self.options, enabled=True))
        self.assertFalse(result['ok'])
        self.assertIn('Mock native writer failed', result['error'])
        call = self.bridge.calls[-1]
        self.assertTrue(call['stagedFbx'].startswith(FBX_MAGIC), 'Failure did not exercise a staged real FBX')
        self.assertTrue(call['textures'], 'Failure did not exercise staged real PNG textures')
        self.assertFalse(call['destination'].parent.exists(), 'Failed native staging directory was retained')
        self.assertEqual(existing, {p: p.read_bytes() for p in self.output.rglob('*') if p.is_file()})
        self.assertEqual(original, {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()})
        self.assertEqual(catalog_before, (self.profile/'catalog.json').read_bytes())
        self.assertEqual(self.api._catalog['exportedFiles'], [])


if __name__ == '__main__':
    unittest.main()
