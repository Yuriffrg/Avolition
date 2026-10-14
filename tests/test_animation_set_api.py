"""Combined-scene API transactions with parsed fixtures and a mocked C4D save.

Native writer fixture bytes are not real C4D documents. These tests exercise
real parsing, texture baking, sampled payloads, staging and publication.
"""
from copy import deepcopy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from create_material_demo import generate
from owpreview.formats import load_asset
from test_c4d_api import MockBridgeClient, MOCK_C4D_BYTES, PNG_MAGIC
from test_formats import clip_bytes, entity_bytes, model_bytes


class AnimationSetAPITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.root = generate(self.base / 'Exports 日本語')
        self.entity = self.root / 'Entities/MaterialRobot/MaterialRobot.owentity'
        self.entity.write_bytes(entity_bytes(name='Material Robot', model='MaterialRobot.00C',
                                            children=[('Weapon.owentity', 'bone_0003')]))
        self.write('Entities/Weapon/Weapon.owentity', entity_bytes(name='Weapon', model='Weapon.00C',
                                                                children=[('Charm.owentity', 'bone_0002')]))
        self.write('Models/Weapon.00C/Weapon.owmdl', model_bytes(name='Weapon.00C'))
        self.write('Entities/Charm/Charm.owentity', entity_bytes(name='Charm', model='Charm.00C'))
        self.write('Models/Charm.00C/Charm.owmdl', model_bytes(name='Charm.00C'))
        self.arms = self.write('Animations/Arms move.owanimclip', clip_bytes([
            ('bone_0001', [(0, [(0, (0, 0, 0)), (24, (4, 2, 6))])]),
            ('bone_0003', [(0, [(0, (0, 0, 0)), (24, (0, 1, 0))])])], duration=25, fps=24))
        self.gun = self.write('Animations/Gun move.owanimclip', clip_bytes([
            ('bone_0002', [(0, [(0, (0, 2, 0)), (15, (0, 4, 0))])]),
            ('bone_0075', [(0, [(0, (0, 0, 0)), (15, (9, 9, 9))])])], duration=16, fps=30))
        self.profile = self.base / 'Profile'
        self.api = BrowserAPI(self.profile)
        self.assertTrue(self.api.scan_root(str(self.root))['ok'])
        self.bridge = MockBridgeClient()
        self.api._c4d_bridge = self.bridge
        self.output = self.base / 'Scene output 日本語'
        self.assignments = [{'modelId': 'model-0', 'clipPath': str(self.arms)},
                            {'modelId': 'model-1', 'clipPath': str(self.gun)}]
        self.options = {'rootPath': str(self.root), 'includeMesh': True, 'bakeTextures': True, 'rootMotion': True,
                        'clipEnabled': True, 'enabled': True, 'c4dEnabled': False}

    def write(self, relative, data):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def export(self, name='Reload 日本語', options=None):
        result = self.api.export_animation_set(str(self.entity), self.assignments, name, str(self.output),
                                                self.options if options is None else options)
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_publishes_only_one_c4d_with_all_models_and_records_export(self):
        originals = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        annotations = deepcopy(self.api._catalog['annotations'])
        asset = load_asset(self.entity, self.root)
        self.assertEqual(len(asset['models']), 3)
        result = self.export()
        call = self.bridge.calls[-1]
        self.assertEqual(Path(result['path']), self.output / 'Reload 日本語.c4d')
        self.assertEqual(result['path'], result['c4dPath'])
        self.assertEqual(Path(result['path']).read_bytes(), MOCK_C4D_BYTES)
        self.assertNotIn('clipPath', result)
        self.assertNotIn('fbxPath', result)
        self.assertEqual(list(self.output.glob('*.owanimclip')), [])
        self.assertEqual(list(self.output.glob('*.fbx')), [])
        self.assertIsNone(call['stagedFbx'])
        payload = call['payload']
        self.assertEqual(payload['name'], 'Reload 日本語')
        self.assertEqual(payload['asset']['exportHierarchy'], asset['exportHierarchy'])
        self.assertEqual(len(payload['asset']['models']), 3)
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'model-0', 'model-1'})
        self.assertEqual((result['fps'], result['lastFrame']), (30, 30))
        self.assertEqual(result['assignments'], payload['animation']['assignments'])
        self.assertEqual(payload['animation']['assignments'][1]['unmatchedBoneNames'], ['bone_0075'])
        self.assertGreater(result['textureCount'], 0)
        self.assertTrue(all(data.startswith(PNG_MAGIC) for data in call['textures'].values()))
        self.assertEqual(len(call['textures']), result['textureCount'])
        self.assertFalse(call['destination'].parent.exists(), 'Completed staging directory must be removed.')
        saved = json.loads((self.profile / 'catalog.json').read_text(encoding='utf-8'))
        self.assertTrue(set(map(self.api._key, result['files'])) <= set(saved['exportedFiles']))
        self.assertIn(self.api._key(self.output), saved['exportDirectories'])
        self.assertIn(self.api._key(self.output), saved['managedOutputDirectories'])
        self.assertEqual(annotations, saved['annotations'])
        self.assertEqual(originals, {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()})

    def test_collision_never_overwrites_existing_files_or_raw_clip(self):
        self.output.mkdir()
        existing = {'Reload.c4d': b'Existing Cinema project',
                    'Reload.owanimclip': b'Existing source copy', 'Reload.fbx': b'Existing FBX'}
        for name, data in existing.items():
            (self.output / name).write_bytes(data)
        result = self.export('Reload.c4d', dict(self.options, bakeTextures=False))
        self.assertEqual(Path(result['path']).name, 'Reload (2).c4d')
        for name, data in existing.items():
            self.assertEqual((self.output / name).read_bytes(), data)
        self.assertEqual(result['files'], [str(self.output / 'Reload (2).c4d')])
        self.assertEqual({p.name for p in self.output.iterdir()}, {*existing, 'Reload (2).c4d'})

    def test_native_failure_discards_staging_without_altering_catalog_or_existing_outputs(self):
        self.output.mkdir()
        (self.output / 'Existing.c4d').write_bytes(b'Existing scene')
        before = {p: p.read_bytes() for p in self.output.rglob('*') if p.is_file()}
        catalog = (self.profile / 'catalog.json').read_bytes()
        self.bridge.fail = True
        result = self.api.export_animation_set(str(self.entity), self.assignments, 'Broken',
                                               str(self.output), self.options)
        self.assertFalse(result['ok'])
        self.assertIn('Mock native writer failed', result['error'])
        call = self.bridge.calls[-1]
        self.assertTrue(call['textures'], 'Failure must exercise already-baked texture cleanup.')
        self.assertFalse(call['destination'].parent.exists())
        self.assertEqual(before, {p: p.read_bytes() for p in self.output.rglob('*') if p.is_file()})
        self.assertEqual(catalog, (self.profile / 'catalog.json').read_bytes())
        self.assertEqual(list(self.output.iterdir()), [self.output / 'Existing.c4d'])

    def test_mesh_and_texture_options_use_captured_settings_and_retain_material_factors(self):
        self.api.set_export_settings({'includeMesh': True, 'bakeTextures': True, 'rootMotion': False})
        for include_mesh, bake_textures in ((False, True), (True, False)):
            with self.subTest(include_mesh=include_mesh, bake_textures=bake_textures):
                with patch('owpreview.export_textures.bake_materials', side_effect=AssertionError('Unexpected texture bake')):
                    result = self.export(str(include_mesh), dict(self.options, includeMesh=include_mesh,
                                                                 bakeTextures=bake_textures))
                call = self.bridge.calls[-1]
                self.assertEqual(call['payload']['includeMesh'], include_mesh)
                self.assertEqual(result['textureCount'], 0)
                self.assertFalse(call['textures'])
                self.assertTrue(call['payload']['materials'])
                for materials in call['payload']['materials'].values():
                    for material in materials.values():
                        self.assertEqual(material['maps'], {})
        self.assertFalse((self.output / 'tex').exists())

    def test_preview_in_place_is_ignored_and_explicit_root_motion_controls_export(self):
        payloads = []
        for name, preview_lock, root_motion in [('Free', False, True), ('Preview locked', True, True),
                                                ('Export locked', False, False)]:
            self.export(name, dict(self.options, bakeTextures=False, lockRoot=preview_lock, rootMotion=root_motion))
            payloads.append(self.bridge.calls[-1]['payload'])
        self.assertEqual(payloads[0]['animation'], payloads[1]['animation'])
        root = lambda payload: next(track for track in payload['animation']['tracks']
                                    if track['modelId'] == 'model-0' and track['boneIndex'] == 0)
        free, locked = root(payloads[0])['frames'], root(payloads[2])['frames']
        self.assertAlmostEqual(free[-1][12] - free[0][12], 4)
        self.assertAlmostEqual(free[-1][14] - free[0][14], 6)
        self.assertAlmostEqual(locked[-1][12], locked[0][12])
        self.assertAlmostEqual(locked[-1][14], locked[0][14])
        self.assertAlmostEqual(locked[-1][13] - locked[0][13], 2)

    def test_invalid_assignments_names_paths_and_options_create_no_output(self):
        bad_clip = self.write('Animations/Corrupt.owanimclip', b'broken')
        no_match = self.write('Animations/Other rig.owanimclip', clip_bytes([('bone_FFFF', [(0, [(0, (0, 0, 0))])])]))
        cases = [dict(assignments={}), dict(assignments=[]), dict(assignments=None), dict(assignments=['bad']),
                 dict(assignments=[self.assignments[0], self.assignments[0]]),
                 dict(assignments=[dict(modelId='missing', clipPath=str(self.arms))]),
                 dict(assignments=[dict(modelId='model-0', clipPath=str(self.root / 'missing.owanimclip'))]),
                 dict(assignments=[dict(modelId='model-0', clipPath='relative.owanimclip')]),
                 dict(assignments=[dict(modelId='model-0', clipPath=str(self.entity))]),
                 dict(assignments=[dict(modelId='model-0', clipPath=str(bad_clip))]),
                 dict(assignments=[dict(modelId='model-0', clipPath=str(no_match))]),
                 dict(scene_name=''), dict(scene_name='../outside'), dict(scene_name='CON'),
                 dict(entity_path='relative.owentity'), dict(entity_path=str(self.root / 'missing.owentity')),
                 dict(options=[]), dict(options=dict(self.options, rootPath='relative')),
                 dict(output_folder='relative')]
        before = (self.profile / 'catalog.json').read_bytes()
        with patch.object(self.bridge, 'status', side_effect=AssertionError('Invalid inputs must fail before bridge access')):
            for override in cases:
                with self.subTest(override=override):
                    args = dict(entity_path=str(self.entity), assignments=self.assignments, scene_name='Invalid',
                                output_folder=str(self.output), options=self.options)
                    args.update(override)
                    result = self.api.export_animation_set(**args)
                    self.assertFalse(result['ok'], result)
                    self.assertNotIn('Invalid inputs must fail', result['error'])
                    self.assertFalse(self.output.exists())
                    self.assertFalse(self.bridge.calls)
                    self.assertEqual(before, (self.profile / 'catalog.json').read_bytes())

    def test_offline_bridge_leaves_no_output_or_catalog_changes(self):
        before = (self.profile / 'catalog.json').read_bytes()
        with patch.object(self.bridge, 'status', return_value={'connected': False, 'message': 'Cinema is not connected.'}):
            result = self.api.export_animation_set(str(self.entity), self.assignments, 'Offline', str(self.output), self.options)
        self.assertFalse(result['ok'])
        self.assertEqual(result['error'], 'Cinema is not connected.')
        self.assertFalse(self.output.exists())
        self.assertEqual(before, (self.profile / 'catalog.json').read_bytes())


if __name__ == '__main__':
    unittest.main()
