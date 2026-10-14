"""Explicit animation sets retain attachment motion and independent clip timing.

Native-save checks use the existing fake C4D API, not an installed Cinema 4D.
The NumPy checks evaluate complete parent chains independently of that fake.
"""
import copy
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import numpy as np

from owpreview.c4d_export import make_payload, make_set_payload
from owpreview.c4d_scene import build_scene
from owpreview.export_pose import PoseEvaluator
from test_c4d_scene import make_api
from test_selected_object_export import fixture, world_matrices


def assigned_fixture():
    asset, arms = fixture()
    arms.update(name='Arms reload', fps=24, lastFrame=24)
    arms['bones'][0]['positions'] = dict(frames=[0, 24], values=[0, 0, 0, 2, 0, 3])
    arms['bones'][1]['positions'] = dict(frames=[0, 24], values=[0, 2, 0, 0, 4, 0])
    gun = dict(name='Gun reload', fps=30, lastFrame=15, bones=[
        dict(name='bone_0001', positions=dict(frames=[0, 15], values=[0, 0, 0, 0, 1, 0])),
        dict(name='bone_004A', positions=dict(frames=[0, 15], values=[0, 3, 0, 0, 3, -2])),
        dict(name='bone_0075', positions=dict(frames=[0, 15], values=[0, 0, 0, 99, 99, 99]))])
    return asset, [dict(modelId='arms', clip=arms), dict(modelId='gun', clip=gun)]


def animated_worlds(payload, frame):
    overrides = {f'{track["modelId"]}:bone:{track["boneIndex"]}': track['frames'][frame]
                 for track in payload['animation']['tracks']}
    return world_matrices(payload['asset']['exportHierarchy'], overrides)


class CombinedExportTests(unittest.TestCase):
    def test_keeps_full_hierarchy_names_meshes_and_does_not_mutate_inputs(self):
        asset, assignments = assigned_fixture()
        original = copy.deepcopy((asset, assignments))
        materials = {model['id']: model['materials'] for model in asset['models']}
        payload = make_set_payload(asset, assignments, materials, name='Reload set')
        self.assertEqual(payload['version'], 1)
        self.assertEqual(payload['name'], 'Reload set')
        self.assertEqual(payload['asset']['exportHierarchy'], asset['exportHierarchy'])
        self.assertEqual([model['id'] for model in payload['asset']['models']], ['arms', 'gun', 'charm'])
        self.assertEqual(payload['materials'], materials)
        self.assertEqual((asset, assignments), original)
        self.assertEqual({(track['modelId'], track['boneIndex']) for track in payload['animation']['tracks']},
                         {('arms', 0), ('arms', 1), ('gun', 0), ('gun', 1)})
        self.assertEqual(payload['animation']['matchedBones'], 4)
        self.assertEqual(payload['animation']['assignments'][1]['unmatchedBoneNames'], ['bone_0075'])

    def test_shared_names_are_scoped_and_shorter_clip_holds_without_stretching(self):
        asset, assignments = assigned_fixture()
        payload = make_set_payload(asset, assignments, {})
        self.assertEqual((payload['animation']['fps'], payload['animation']['lastFrame']), (30, 30))
        by_track = {(track['modelId'], track['boneIndex']): track for track in payload['animation']['tracks']}
        for assignment in assignments:
            mid, clip = assignment['modelId'], assignment['clip']
            bones = next(model['bones'] for model in asset['models'] if model['id'] == mid)
            evaluator = PoseEvaluator(bones, clip['bones'])
            for frame in (0, 7, 15, 23, 30):
                expected = evaluator.evaluate(min(frame / 30 * clip['fps'], clip['lastFrame']))['world']
                actual = [None] * len(bones)
                for index in evaluator.order:
                    parent = evaluator.parents[index]
                    local = np.asarray(by_track[(mid, index)]['frames'][frame]).reshape(4, 4).T
                    actual[index] = local if parent < 0 else actual[parent] @ local
                    np.testing.assert_allclose(actual[index], expected[index], atol=1e-12)
        self.assertEqual(by_track[('gun', 0)]['frames'][15], by_track[('gun', 0)]['frames'][30])
        self.assertNotEqual(by_track[('arms', 0)]['frames'][15], by_track[('arms', 0)]['frames'][30])

    def test_moving_parent_attachment_and_unassigned_charm_inherit_motion(self):
        asset, assignments = assigned_fixture()
        payload = make_set_payload(asset, assignments, {})
        arms_eval = PoseEvaluator(asset['models'][0]['bones'], assignments[0]['clip']['bones'])
        gun_eval = PoseEvaluator(asset['models'][1]['bones'], assignments[1]['clip']['bones'])
        charm_rest = PoseEvaluator(asset['models'][2]['bones']).evaluate(0)['world']
        rest = world_matrices(asset['exportHierarchy'])
        for frame in (0, 15, 30):
            actual = animated_worlds(payload, frame)
            arms = arms_eval.evaluate(frame / 30 * 24)['world']
            gun = gun_eval.evaluate(min(frame, 15))['world']
            parent_hand = rest['arms:skeleton'] @ arms[1]
            # Existing native graph attaches gun entity to the animated hand,
            # with each child's complete local skeleton underneath it.
            np.testing.assert_allclose(actual['gun:entity'], parent_hand, atol=1e-12)
            for index in range(2):
                np.testing.assert_allclose(actual[f'gun:bone:{index}'], parent_hand @ gun[index], atol=1e-12)
                np.testing.assert_allclose(actual[f'charm:bone:{index}'],
                                           parent_hand @ gun[1] @ charm_rest[index], atol=1e-12)
        self.assertFalse(np.allclose(animated_worlds(payload, 0)['charm:mesh:0'],
                                     animated_worlds(payload, 30)['charm:mesh:0']))

    def test_root_motion_option_changes_roots_but_preserves_parent_inheritance(self):
        asset, assignments = assigned_fixture()
        free = make_set_payload(asset, assignments, {}, root_motion=True)
        locked = make_set_payload(asset, assignments, {}, root_motion=False)
        root_track = lambda payload: next(track for track in payload['animation']['tracks']
                                          if track['modelId'] == 'arms' and track['boneIndex'] == 0)
        self.assertNotEqual(root_track(free)['frames'][-1], root_track(locked)['frames'][-1])
        for track in locked['animation']['tracks']:
            if track['boneIndex'] == 0:
                model = next(model for model in asset['models'] if model['id'] == track['modelId'])
                for matrix in track['frames']:
                    self.assertEqual([matrix[12], matrix[14]], [model['bones'][0]['pos'][0], model['bones'][0]['pos'][2]])
        # Locking root translation does not strip the source hand-to-gun link.
        self.assertEqual(locked['asset']['exportHierarchy'], asset['exportHierarchy'])
        self.assertFalse(np.allclose(animated_worlds(locked, 0)['gun:entity'],
                                     animated_worlds(locked, 30)['gun:entity']))

    def test_non_grid_duration_rounds_up_and_clamps_only_the_last_sample(self):
        asset, assignments = assigned_fixture()
        assignments[0]['clip']['lastFrame'] = 23  # 23/24 seconds, not on a 30 FPS grid.
        payload = make_set_payload(asset, assignments, {})
        self.assertEqual(payload['animation']['lastFrame'], 29)
        self.assertEqual(payload['animation']['durationSeconds'], 23 / 24)
        evaluator = PoseEvaluator(asset['models'][0]['bones'], assignments[0]['clip']['bones'])
        final_root = next(track for track in payload['animation']['tracks']
                          if track['modelId'] == 'arms' and track['boneIndex'] == 0)['frames'][-1]
        np.testing.assert_allclose(np.asarray(final_root).reshape(4, 4).T,
                                   evaluator.evaluate(23)['world'][0], atol=1e-12)

    def test_native_writer_receives_all_rigs_and_tracks_with_existing_bridge(self):
        asset, assignments = assigned_fixture()
        native = make_api()
        payload = make_set_payload(asset, assignments, {})
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {
                'c4d': native, 'c4d.modules': native.modules}):
            report = build_scene(payload, Path(temp) / 'reload.c4d')
        self.assertEqual({key: report[key] for key in ('meshCount', 'boneCount', 'skinCount', 'trackCount', 'frameCount')},
                         dict(meshCount=3, boneCount=6, skinCount=3, trackCount=36, frameCount=31))
        self.assertTrue(report['verifiedNativeScene'])
        nodes = []
        def visit(obj):
            nodes.append(obj)
            for child in obj.children:
                visit(child)
        for obj in native.saved[0].roots:
            visit(obj)
        gun_entity = next(obj for obj in nodes if obj.name == 'gun_Entity')
        expected_hand = next(node['name'] for node in asset['exportHierarchy'] if node['id'] == 'arms:bone:1')
        self.assertEqual(gun_entity.parent.name, expected_hand)
        self.assertEqual(len(gun_entity.parent.tracks), 9)
        # Both rigs may share joint display names without sharing track objects.
        animated_joints = [obj for obj in nodes if obj.kind == native.Ojoint and obj.tracks]
        self.assertEqual(len(animated_joints), 4)
        self.assertEqual(len({id(track) for obj in animated_joints for track in obj.tracks}), 36)
        self.assertTrue(all(track.keys[-1].time == 1 for obj in animated_joints for track in obj.tracks))

    def test_include_mesh_false_still_keeps_all_attachment_rigs(self):
        asset, assignments = assigned_fixture()
        native = make_api()
        payload = make_set_payload(asset, assignments, {}, include_mesh=False)
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {
                'c4d': native, 'c4d.modules': native.modules}):
            report = build_scene(payload, Path(temp) / 'skeletons.c4d')
        self.assertEqual(report['meshCount'], 0)
        self.assertEqual(report['boneCount'], 6)
        self.assertEqual(report['trackCount'], 36)

    def test_selected_object_export_is_still_independent(self):
        asset, assignments = assigned_fixture()
        payload = make_payload(asset, assignments[1]['clip'], {}, scope_model_id='gun')
        self.assertEqual([model['id'] for model in payload['asset']['models']], ['gun'])
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'gun'})
        self.assertEqual(payload['animation']['lastFrame'], 15)

    def test_rejects_missing_ambiguous_and_unmatched_assignments(self):
        for case, expected in [('empty', 'at least one'), ('missing-model', 'skeleton'),
                               ('duplicate', 'one assigned'), ('missing-clip', 'clip data'),
                               ('no-match', 'no matching'), ('missing-joint', 'missing joints')]:
            with self.subTest(case=case):
                asset, assignments = assigned_fixture()
                if case == 'empty': assignments = []
                elif case == 'missing-model': assignments[0]['modelId'] = 'absent'
                elif case == 'duplicate': assignments.append(assignments[0])
                elif case == 'missing-clip': assignments[0].pop('clip')
                elif case == 'no-match': assignments[0]['clip']['bones'] = [dict(name='bone_FFFF')]
                elif case == 'missing-joint':
                    asset['exportHierarchy'] = [node for node in asset['exportHierarchy'] if node['id'] != 'gun:bone:0']
                    for node in asset['exportHierarchy']:
                        if node['parentId'] == 'gun:bone:0': node['parentId'] = 'gun:skeleton'
                with self.assertRaisesRegex(ValueError, expected):
                    make_set_payload(asset, assignments, {})

    def test_validates_timing_and_total_sample_budget(self):
        for field, value in [('fps', 0), ('fps', float('nan')), ('lastFrame', -1), ('lastFrame', 1.2)]:
            with self.subTest(field=field, value=value):
                asset, assignments = assigned_fixture()
                assignments[0]['clip'][field] = value
                with self.assertRaisesRegex(ValueError, 'timing'):
                    make_set_payload(asset, assignments, {})
        asset, assignments = assigned_fixture()
        for assignment in assignments:
            assignment['clip'].update(fps=30, lastFrame=70_000)
        with self.assertRaisesRegex(ValueError, '250,000'):
            make_set_payload(asset, assignments, {})


if __name__ == '__main__':
    unittest.main()
