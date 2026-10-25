"""Missing authored normals are repaired selectively at the native export boundary."""
import copy
import math
from pathlib import Path
import tempfile
import unittest

import numpy as np

from owpreview.c4d_export import _normalized_normal_asset
from owpreview.c4d_scene import _mesh
from owpreview.map_export import export_map, make_static_payload
from owpreview.map_formats import load_map
from test_c4d_scene import make_api
from test_map_export import MockBridge
from test_map_formats import write_fixture


def mesh_fixture(positions, indices, normals, name='Authored mesh'):
    return dict(name=name, positions=np.asarray(positions, dtype=float).reshape(-1).tolist(),
                indices=list(indices), normals=np.asarray(normals, dtype=float).reshape(-1).tolist(),
                uvs=[], uvs2=[], materialKey=4000, skinIndices=[], skinWeights=[])


def asset_fixture(mesh):
    return {'models': [{'id': 'body', 'name': 'Body', 'bones': [], 'meshes': [mesh]}],
            'warnings': ['Existing source note']}


class ExportNormalTests(unittest.TestCase):
    def repair(self, mesh):
        asset = asset_fixture(mesh)
        before = copy.deepcopy(asset)
        result = _normalized_normal_asset(asset)
        self.assertEqual(asset, before, 'Export preparation must not mutate preview/source arrays.')
        self.assertIn('Existing source note', result.get('warnings', []))
        repaired = result['models'][0]['meshes'][0]
        self.assertEqual({k: v for k, v in repaired.items() if k != 'normals'},
                         {k: v for k, v in mesh.items() if k != 'normals'})
        normals = np.asarray(repaired['normals']).reshape(-1, 3)
        self.assertTrue(np.isfinite(normals).all())
        np.testing.assert_allclose(np.linalg.norm(normals, axis=1), 1, atol=1e-12)
        return repaired, result

    def assert_repair_warning(self, result, count):
        warnings = [value for value in result.get('warnings', []) if 'normal' in value.lower()]
        self.assertEqual(len(warnings), 1, warnings)
        self.assertRegex(warnings[0], rf'(?<!\d){count}(?!\d)')

    def test_valid_authored_directions_are_normalized_without_repair_warning(self):
        mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0]], [0, 1, 2],
                            [[.1, .2, .96], [-.3, .9, .1], [.7, -.2, -.6]])
        repaired, result = self.repair(mesh)
        authored = np.array(mesh['normals']).reshape(-1, 3)
        np.testing.assert_allclose(np.array(repaired['normals']).reshape(-1, 3),
                                   authored / np.linalg.norm(authored, axis=1)[:, None], atol=1e-14)
        self.assertEqual(result['warnings'], ['Existing source note'])

    def test_zero_direction_uses_corner_angles_not_triangle_area(self):
        # The huge +Z face contributes 90 degrees; the small -X face contributes
        # 60 degrees. Area weighting would incorrectly make this almost +Z.
        mesh = mesh_fixture([[0, 0, 0], [100, 0, 0], [0, 100, 0],
                             [0, 0, 1], [0, math.sqrt(3)/2, .5]],
                            [0, 1, 2, 0, 3, 4], [[0, 0, 0]] + [[.2, .3, .4]] * 4)
        repaired, result = self.repair(mesh)
        normals = np.array(repaired['normals']).reshape(-1, 3)
        np.testing.assert_allclose(normals[0], np.array([-2, 0, 3]) / math.sqrt(13), atol=1e-12)
        np.testing.assert_allclose(normals[1:], np.tile(np.array([2, 3, 4])/math.sqrt(29), (4, 1)), atol=1e-12)
        self.assert_repair_warning(result, 1)

    def test_thin_triangle_like_real_map_still_supplies_a_direction(self):
        mesh = mesh_fixture([[0, 0, 0], [0, 1.69530439376831, -2.9802322387695312e-8],
                             [0, 1.69530439376831, 0]],
                            [0, 1, 2], [[0, 0, 0], [2, 0, 0], [2, 0, 0]])
        repaired, result = self.repair(mesh)
        np.testing.assert_allclose(np.array(repaired['normals']).reshape(-1, 3), [[1, 0, 0]] * 3, atol=1e-12)
        self.assert_repair_warning(result, 1)

    def test_coincident_split_vertices_keep_distinct_surface_directions(self):
        mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0],
                             [0, 0, 0], [0, 1, 0], [0, 0, 1]],
                            [0, 1, 2, 3, 4, 5],
                            [[0, 0, 0], [0, 2, 0], [0, 2, 0], [0, 0, 0], [0, 2, 0], [0, 2, 0]])
        repaired, result = self.repair(mesh)
        normals = np.array(repaired['normals']).reshape(-1, 3)
        np.testing.assert_allclose(normals[[0, 3]], [[0, 0, 1], [1, 0, 0]], atol=1e-12)
        np.testing.assert_allclose(normals[[1, 2, 4, 5]], [[0, 1, 0]] * 4, atol=1e-12)
        self.assert_repair_warning(result, 2)

    def test_cancelling_angle_sum_uses_largest_incident_face(self):
        # Opposite 90-degree contributions cancel; the +Z face has 4x the area.
        mesh = mesh_fixture([[0, 0, 0], [2, 0, 0], [0, 2, 0], [0, 1, 0], [1, 0, 0]],
                            [0, 3, 4, 0, 1, 2], [[0, 0, 0]] + [[0, 0, 2]] * 4)
        repaired, result = self.repair(mesh)
        np.testing.assert_allclose(repaired['normals'][:3], [0, 0, 1], atol=1e-12)
        self.assert_repair_warning(result, 1)

    def test_opposing_real_map_faces_follow_blender_angle_weighting(self):
        # The exported 11770 mesh has almost-cancelling opposing faces. Blender
        # gives vertex 2 a -Y automatic normal; area weighting would give +Y.
        mesh = mesh_fixture([[7.152557373046875e-6, 24.827350616455078, -17.362180709838867],
                             [21.095237731933594, 24.827350616455078, -2.1153931617736816],
                             [-.13991379737854004, 24.827350616455078, -17.3621768951416],
                             [21.095243453979492, 24.827350616455078, -17.362171173095703]],
                            [0, 1, 2, 3, 2, 1],
                            [[0, .9921875, 0], [0, .9921875, 0], [0, 0, 0], [0, .9921875, 0]])
        repaired, result = self.repair(mesh)
        normals = np.array(repaired['normals']).reshape(-1, 3)
        np.testing.assert_allclose(normals[2], [0, -1, 0], atol=1e-12)
        np.testing.assert_allclose(normals[[0, 1, 3]], [[0, 1, 0]] * 3, atol=1e-12)
        self.assert_repair_warning(result, 1)

    def test_degenerate_only_and_unused_vertices_get_finite_fallbacks(self):
        mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [2, 0, 0], [7, 8, 9]],
                            [0, 1, 2, 0, 0, 1], [[0, 0, 0], [0, 0, 2], [0, 0, 2], [0, 0, 0]])
        repaired, result = self.repair(mesh)
        np.testing.assert_allclose(np.array(repaired['normals']).reshape(-1, 3),
                                   [[0, 1, 0], [0, 0, 1], [0, 0, 1], [0, 1, 0]], atol=1e-12)
        warnings = [value for value in result.get('warnings', []) if 'normal' in value.lower()]
        self.assertTrue(any('2' in value and 'placeholder' in value.lower() for value in warnings), warnings)

    def test_absent_normals_still_use_the_native_default(self):
        mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0]], [0, 1, 2], [])
        before = copy.deepcopy(mesh)
        result = _normalized_normal_asset(asset_fixture(mesh))
        self.assertEqual(mesh, before)
        self.assertEqual(result['models'][0]['meshes'][0]['normals'], [])
        self.assertEqual(result['warnings'], ['Existing source note'])

    def test_nonfinite_normals_are_not_silently_repaired(self):
        for invalid in (float('nan'), float('inf'), -float('inf')):
            with self.subTest(value=invalid):
                mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0]], [0, 1, 2],
                                    [[invalid, 0, 0], [0, 0, 1], [0, 0, 1]])
                with self.assertRaisesRegex(ValueError, 'normal'):
                    _normalized_normal_asset(asset_fixture(mesh))

    def test_malformed_arrays_and_topology_remain_errors(self):
        base = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0]], [0, 1, 2],
                            [[0, 0, 0], [0, 0, 1], [0, 0, 1]])
        for field, invalid in [('normals', [0, 0]), ('normals', [0] * 6),
                               ('positions', [0] * 8), ('indices', [0, 1]),
                               ('indices', [-1, 1, 2]), ('indices', [0, 1, 3]),
                               ('indices', [0, 1.5, 2])]:
            with self.subTest(field=field, values=invalid):
                mesh = dict(base, **{field: invalid})
                with self.assertRaises(ValueError):
                    _normalized_normal_asset(asset_fixture(mesh))

    def test_repaired_normal_follows_native_mirrored_polygon_corners(self):
        mesh = mesh_fixture([[0, 0, 0], [1, 0, 0], [0, 1, 0]], [0, 1, 2],
                            [[0, 0, 0], [0, 2, 2], [3, 0, 4]])
        repaired, _result = self.repair(mesh)
        native = _mesh(make_api(), repaired)
        self.assertEqual(native.polygons, [(0, 2, 1, 1)])
        normals = native.tags[0].data[0]
        expected = {'a': [0, 0, -1], 'b': [.6, 0, -.8],
                    'c': [0, 1/math.sqrt(2), -1/math.sqrt(2)],
                    'd': [0, 1/math.sqrt(2), -1/math.sqrt(2)]}
        for corner, value in expected.items():
                normal = normals[corner]
                np.testing.assert_allclose([normal.x, normal.y, normal.z], value, atol=1e-12)


class MapExportNormalTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.asset = load_map(write_fixture(self.root/'source'))
        first = self.asset['models'][0]
        self.prototype_key = first['meshPrototype']
        self.mesh = self.asset['meshPrototypes'][self.prototype_key]['meshes'][0]
        self.mesh['normals'][:3] = [0, 0, 0]
        self.asset['warnings'].append('Keep original map warning')

    def test_compact_map_repairs_once_per_prototype_and_keeps_warning(self):
        before = copy.deepcopy(self.asset)
        payload = make_static_payload(self.asset)
        self.assertEqual(self.asset, before)
        self.assertTrue(all('meshes' not in model for model in payload['asset']['models']))
        self.assertEqual(set(payload['asset']['meshPrototypes']), set(self.asset['meshPrototypes']))
        repeated = [model for model in self.asset['models'] if model['meshPrototype'] == self.prototype_key]
        self.assertGreater(len(repeated), 1, 'Fixture must exercise reused compact map geometry.')
        warnings = payload['warnings']
        self.assertEqual(warnings, payload['asset']['warnings'])
        self.assertIn('Keep original map warning', warnings)
        repair_warnings = [value for value in warnings if 'normal' in value.lower()]
        self.assertEqual(len(repair_warnings), 1, repair_warnings)
        self.assertRegex(repair_warnings[0], r'(?<!\d)1(?!\d)')
        repaired = payload['asset']['meshPrototypes'][self.prototype_key]['meshes'][0]
        np.testing.assert_allclose(np.linalg.norm(np.array(repaired['normals']).reshape(-1, 3), axis=1), 1, atol=1e-12)
        self.assertEqual(repaired['indices'], self.mesh['indices'])
        self.assertEqual(payload['animation']['tracks'], [])

    def test_map_export_returns_repair_warnings_when_bridge_has_no_warnings(self):
        bridge = MockBridge()
        before = copy.deepcopy(self.asset)
        result = export_map(self.asset, self.root/'output', 'Repaired map', bake_textures=False, bridge=bridge)
        self.assertEqual(self.asset, before)
        self.assertEqual(len(bridge.calls), 1)
        self.assertEqual(result['warnings'], bridge.calls[0]['warnings'])
        self.assertIn('Keep original map warning', result['warnings'])
        self.assertTrue(any('normal' in value.lower() for value in result['warnings']))
        self.assertTrue(Path(result['c4dPath']).is_file())


if __name__ == '__main__':
    unittest.main()
