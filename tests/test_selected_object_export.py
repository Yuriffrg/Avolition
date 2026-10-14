"""Selected-object exports retain a full rig without exporting its neighbours."""
import copy
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import numpy as np
from PIL import Image

from owpreview.c4d_export import make_payload, select_model_asset
from owpreview.c4d_scene import _ordered_graph, build_scene
from owpreview.export_pose import PoseEvaluator, compose
from owpreview.export_textures import bake_materials
from owpreview.formats import build_export_hierarchy
from test_c4d_scene import make_api


def fixture():
    models, textures = [], {}
    for index, (mid, parent) in enumerate((('arms', None), ('gun', 'arms'), ('charm', 'gun'))):
        material_key = f'mat-{mid}'
        mesh = dict(name=f'Submesh_{mid}', positions=[0, 0, 0, 1, 0, 0, 0, 1, 0],
                    indices=[0, 1, 2], normals=[0, 0, .97] * 3,
                    uvs=[.1, .2, .3, .4, .5, .6], uvs2=[.7, .8, .9, 1.1, 1.2, 1.3],
                    skinIndices=[0, 1, 0, 0] * 3, skinWeights=[.25, .75, 0, 0] * 3,
                    materialKey=material_key)
        bones = [dict(name='bone_0001', parent=-1, pos=[index + 1, 0, 0], rot=[0, 0, 0, 1]),
                 dict(name='bone_004A', parent=0, pos=[0, index + 2, 0], rot=[0, 0, 0, 1])]
        models.append(dict(id=mid, name=f'{index + 1:012X}.00C', modelGuid=f'{index + 1:012X}',
                           parentId=parent, attachmentBone='bone_004A' if parent else None,
                           attachmentMatrix=compose([9, 8, 7], [0, 0, 0, 1], [1, 1, 1]).T.reshape(-1).tolist(),
                           isEntity=True, entityName='Gameplay1P' if not parent else f'{mid}_Entity',
                           hasModel=True, bones=bones, meshes=[mesh], sockets=[],
                           materials={material_key: dict(name=material_key, maps={
                               'baseColor': dict(textureId=mid, uvIndex=0, colorSpace='srgb')})}))
        textures[mid] = dict(path=f'{mid}.png', channel='rgb', sourceColorSpace='srgb')
    # A no-longer-used material must not cause extra texture output either.
    models[1]['materials']['unused'] = dict(name='unused', maps={'baseColor': dict(textureId='unused')})
    textures['unused'] = dict(path='unused.png', channel='rgb')
    asset = dict(name='Gameplay1P', path='Gameplay1P.owentity', models=models, textures=textures, warnings=[])
    asset['exportHierarchy'] = build_export_hierarchy(asset)
    # Exercise a nonidentity top-level placement and an attached hierarchy.
    asset['exportHierarchy'][0]['matrix'] = compose([3, 4, 5], [0, 0, .7071067811865475, .7071067811865475],
                                                    [1, 1, 1]).T.reshape(-1).tolist()
    clip = dict(name='Reload', fps=30, lastFrame=2, bones=[
        dict(name='bone_0001', positions=dict(frames=[0, 2], values=[0, 0, 0, 0, 0, 1])),
        dict(name='bone_004A', positions=dict(frames=[0, 2], values=[0, 3, 0, 0, 4, 0]))])
    return asset, clip


def world_matrices(graph, overrides=None):
    worlds = {}
    for node in _ordered_graph(graph):
        values = (overrides or {}).get(node['id'], node.get('matrix', np.eye(4).reshape(-1)))
        local = np.asarray(values).reshape(4, 4).T
        worlds[node['id']] = worlds[node['parentId']] @ local if node.get('parentId') else local
    return worlds


class SelectedObjectExportTests(unittest.TestCase):
    def test_primary_projection_excludes_children_and_keeps_complete_original_rig(self):
        asset, _ = fixture()
        projected = select_model_asset(asset, 'arms')
        self.assertEqual([model['id'] for model in projected['models']], ['arms'])
        self.assertEqual(projected['exportHierarchy'], [node for node in asset['exportHierarchy']
                                                       if node['modelId'] == 'arms'])
        self.assertEqual(projected['models'][0]['bones'], asset['models'][0]['bones'])
        self.assertEqual(projected['models'][0]['meshes'], asset['models'][0]['meshes'])
        self.assertEqual(set(projected['textures']), {'arms'})

    def test_attached_projection_preserves_native_rest_world_placement_without_parent_rig(self):
        asset, _ = fixture()
        original = copy.deepcopy(asset)
        projected = select_model_asset(asset, 'gun')
        self.assertEqual([model['id'] for model in projected['models']], ['gun'])
        self.assertIsNone(projected['models'][0]['parentId'])
        self.assertIsNone(projected['models'][0]['attachmentBone'])
        self.assertEqual({node['modelId'] for node in projected['exportHierarchy']}, {'gun'})
        self.assertEqual(sum(node['type'] == 'bone' for node in projected['exportHierarchy']), 2)
        before, after = world_matrices(asset['exportHierarchy']), world_matrices(projected['exportHierarchy'])
        for node in projected['exportHierarchy']:
            np.testing.assert_allclose(after[node['id']], before[node['id']], atol=1e-12)
            old = next(value for value in asset['exportHierarchy'] if value['id'] == node['id'])
            if old['parentId'] in after:
                self.assertEqual(node, old, 'Internal hierarchy and authored names stay unchanged.')
        self.assertEqual(projected['models'][0]['bones'], asset['models'][1]['bones'])
        self.assertEqual(projected['models'][0]['meshes'], asset['models'][1]['meshes'])
        self.assertEqual(asset, original)
        projected['models'][0]['meshes'][0]['positions'][0] = 123
        projected['textures']['gun']['path'] = 'changed.png'
        self.assertEqual(asset, original, 'Export projection must not alias writable mesh/material state.')

    def test_scoped_animation_uses_only_selected_bones_and_frozen_parent_rest_placement(self):
        asset, clip = fixture()
        all_materials = {model['id']: model['materials'] for model in asset['models']}
        payload = make_payload(asset, clip, all_materials, scope_model_id='gun')
        self.assertEqual(set(payload['materials']), {'gun'})
        self.assertEqual(set(payload['materials']['gun']), {'mat-gun'})
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'gun'})
        self.assertEqual(len(payload['animation']['tracks']), 2)
        evaluator = PoseEvaluator(asset['models'][1]['bones'], clip['bones'])
        rest = world_matrices(asset['exportHierarchy'])
        for frame in range(3):
            replacements = {f'gun:bone:{track["boneIndex"]}': track['frames'][frame]
                            for track in payload['animation']['tracks']}
            actual = world_matrices(payload['asset']['exportHierarchy'], replacements)
            expected = evaluator.evaluate(frame)['world']
            for index in range(2):
                np.testing.assert_allclose(actual[f'gun:bone:{index}'],
                                           rest['gun:skeleton'] @ expected[index], atol=1e-12)
        # Shared raw joint names on the other rigs must not create other tracks.
        self.assertEqual(payload['animation']['matchedBones'], 2)

    def test_default_whole_entity_export_remains_compatible(self):
        asset, clip = fixture()
        payload = make_payload(asset, clip, {}, rig_id='gun')
        self.assertEqual(len(payload['asset']['models']), 3)
        self.assertEqual(payload['asset']['exportHierarchy'], asset['exportHierarchy'])

    def test_texture_baking_processes_only_selected_mesh_materials(self):
        asset, _ = fixture()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            asset.update(path=str(root / 'Gameplay1P.owentity'), textureRoot=str(root))
            for texture_id, texture in asset['textures'].items():
                texture['path'] = str(root / f'{texture_id}.png')
                Image.new('RGB', (2, 2), (120, 60, 30)).save(texture['path'])
            baked = bake_materials(select_model_asset(asset, 'gun'), root / 'tex')
            self.assertEqual(set(baked['materials']), {'gun'})
            self.assertEqual(set(baked['materials']['gun']), {'mat-gun'})
            self.assertEqual(len(baked['files']), 1)
            self.assertEqual(baked['warnings'], [])

    def test_native_scene_receives_one_mesh_skin_and_complete_rig_with_both_uv_sets(self):
        asset, clip = fixture()
        payload = make_payload(asset, clip, {}, scope_model_id='gun')
        native = make_api()
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {
                'c4d': native, 'c4d.modules': native.modules}):
            report = build_scene(payload, Path(temp) / 'gun_reload.c4d')
        self.assertEqual(report['meshCount'], 1)
        self.assertEqual(report['boneCount'], 2)
        self.assertEqual(report['skinCount'], 1)
        self.assertEqual(report['trackCount'], 18)
        self.assertTrue(report['verifiedNativeScene'])
        root = native.saved[0].roots[0]
        self.assertEqual(root.name, 'gun_Entity')
        model = root.children[0]
        self.assertEqual(model.name, '000000000002.00C')
        mesh = next(child for child in model.children if child.kind == native.Opolygon)
        self.assertEqual({tag.name for tag in mesh.tags if tag.name.startswith('UVMap')}, {'UVMap1', 'UVMap2'})
        normal_tag = next(tag for tag in mesh.tags if hasattr(tag, 'data') and isinstance(tag.data[0], dict))
        self.assertEqual(normal_tag.data[0]['a'], (0, 0, -1))
        weight_tag = next(tag for tag in mesh.tags if hasattr(tag, 'joints'))
        self.assertEqual(len(weight_tag.joints), 2)
        self.assertEqual(weight_tag.weights, {0: [.25] * 3, 1: [.75] * 3})

    def test_missing_selection_and_conflicting_target_fail_explicitly(self):
        asset, clip = fixture()
        with self.assertRaisesRegex(ValueError, 'valid object'):
            select_model_asset(asset, 'missing')
        with self.assertRaisesRegex(ValueError, 'animation target'):
            make_payload(asset, clip, {}, rig_id='arms', scope_model_id='gun')
        asset['exportHierarchy'] = [node for node in asset['exportHierarchy'] if node['modelId'] != 'gun']
        with self.assertRaisesRegex(ValueError, 'missing from'):
            select_model_asset(asset, 'gun')

    def test_invalid_external_attachment_is_not_silently_discarded(self):
        asset, _ = fixture()
        entity = next(node for node in asset['exportHierarchy'] if node['id'] == 'gun:entity')
        entity['parentId'] = 'missing-parent'
        with self.assertRaisesRegex(ValueError, 'missing parent'):
            select_model_asset(asset, 'gun')
        asset, _ = fixture()
        parent = next(node for node in asset['exportHierarchy'] if node['id'] == 'arms:bone:1')
        parent['matrix'][4] = .4
        with self.assertRaisesRegex(ValueError, 'rest attachment transform'):
            select_model_asset(asset, 'gun')


if __name__ == '__main__':
    unittest.main()
