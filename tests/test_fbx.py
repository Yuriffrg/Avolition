import copy
import hashlib
import json
from pathlib import Path
import struct
import tempfile
import unittest
import zlib
import numpy as np

from owpreview.export_pose import PoseEvaluator, compose, euler_xyz, rotation
from owpreview.fbx import export_fbx, _Scene
from owpreview.formats import _euler_xyz, load_asset, load_clip


def read_binary_fbx(path):
    """Read serialized records independently of the writer for SDK schema checks."""
    data = Path(path).read_bytes()
    assert data[:23] == b'Kaydara FBX Binary  \x00\x1a\x00'
    assert struct.unpack_from('<I', data, 23)[0] == 7400

    def element(start):
        end, count, length, name_length = struct.unpack_from('<IIIB', data, start)
        if end == 0: return None, start + 13
        cursor = start + 13
        name = data[cursor:cursor + name_length].decode()
        cursor += name_length
        properties_end = cursor + length
        props = []
        for _ in range(count):
            kind = chr(data[cursor]); cursor += 1
            if kind in 'SR':
                size, = struct.unpack_from('<I', data, cursor); cursor += 4
                value = data[cursor:cursor + size]; cursor += size
                if kind == 'S': value = value.decode('utf-8')
            elif kind in 'dfil':
                size, encoding, byte_count = struct.unpack_from('<III', data, cursor); cursor += 12
                raw = data[cursor:cursor + byte_count]; cursor += byte_count
                if encoding: raw = zlib.decompress(raw)
                value = struct.unpack('<' + str(size) + {'d': 'd', 'f': 'f', 'i': 'i', 'l': 'q'}[kind], raw)
            else:
                fmt = '<' + {'I': 'i', 'L': 'q', 'D': 'd', 'F': 'f', 'C': '?'}[kind]
                value, = struct.unpack_from(fmt, data, cursor); cursor += struct.calcsize(fmt)
            props.append(value)
        assert cursor == properties_end
        children = []
        while cursor < end:
            child, cursor = element(cursor)
            if child is not None: children.append(child)
        assert cursor == end
        return {'name': name, 'props': props, 'children': children}, end

    roots = []; cursor = 27
    while True:
        value, cursor = element(cursor)
        if value is None: return roots
        roots.append(value)


def child(node, name):
    return next(c for c in node['children'] if c['name'] == name)


def properties(node):
    return {p['props'][0]: p['props'] for p in child(node, 'Properties70')['children']}


def oracle_fixture():
    fixture = json.loads((Path(__file__).parent / 'fixtures/playback_fixture.json').read_text())
    bones = [{'name': b['name'], 'parent': b['parent'], 'pos': b['position'],
              'rot': _euler_xyz(b['rotation']), 'scale': b['sourceScale']} for b in fixture['bones']]
    tracks = []
    for b in fixture['clip']['bones']:
        converted = {'name': b['name']}
        for kind in ('positions', 'rotations', 'scales'):
            converted[kind] = {'frames': [k['frame'] for k in b[kind]], 'values': [v for k in b[kind] for v in k['data']]}
        tracks.append(converted)
    return fixture, bones, tracks


class FbxTests(unittest.TestCase):
    def test_pose_matches_actual_blender_oracle(self):
        fixture, bones, tracks = oracle_fixture()
        ev = PoseEvaluator(bones, tracks)
        for sample in fixture['samples']:
            pose = ev.evaluate(sample['frame'])
            for i, bone in enumerate(bones):
                np.testing.assert_allclose(pose['world'][i].T.reshape(-1), sample['matrices'][bone['name']], atol=fixture['matrixTolerance'], rtol=0)

    def test_rest_reset_and_root_lock(self):
        fixture, bones, tracks = oracle_fixture()
        ev = PoseEvaluator(bones, tracks)
        unlocked = ev.evaluate(10)['world'][0]
        locked = ev.evaluate(10, True)['world'][0]
        np.testing.assert_allclose(locked[:3, 3], [bones[0]['pos'][0], unlocked[1, 3], bones[0]['pos'][2]])
        rest = PoseEvaluator(bones).evaluate(0)
        for i, b in enumerate(bones):
            np.testing.assert_allclose(rest['world'][i].T.reshape(-1), fixture['restMatrices'][b['name']], atol=fixture['matrixTolerance'], rtol=0)

    def test_compensation_is_exact(self):
        _, bones, tracks = oracle_fixture()
        ev = PoseEvaluator(bones, tracks)
        for frame in (0, 5, 10, 15, 20):
            p = ev.evaluate(frame); assembled = [None]*len(bones)
            for i in ev.order:
                parent = ev.parents[i]
                matrix = compose(p['positions'][i], p['quaternions'][i], p['scales'][i])
                if parent >= 0:
                    s = p['scales'][parent]
                    matrix = assembled[parent] @ np.diag(np.r_[1/s, 1.]) @ compose(p['positions'][i]*s, p['quaternions'][i], p['scales'][i])
                assembled[i] = matrix
                np.testing.assert_allclose(matrix, p['world'][i], atol=1e-10)

    def test_euler_roundtrip_and_continuity(self):
        previous = None
        for degrees in range(0, 721, 5):
            q = _euler_xyz([0.3, 0.8, np.radians(degrees)])
            e = euler_xyz(rotation(q), previous)
            np.testing.assert_allclose(rotation(_euler_xyz(e)), rotation(q), atol=1e-10)
            if previous is not None: self.assertLess(np.max(abs(e-previous)), 0.2)
            previous = e

    def test_deterministic_binary_with_geometry_and_animation(self):
        root = Path(__file__).parent / 'demo-assets'
        asset = load_asset(root / 'Entities/PreviewRobot/PreviewRobot.owentity', root)
        clip = load_clip(root / 'Animations/001_Wave.owanimclip')
        with tempfile.TemporaryDirectory() as temp:
            paths = [Path(temp)/'first.fbx', Path(temp)/'second.fbx']
            results = [export_fbx(path, asset, clip) for path in paths]
            self.assertEqual(paths[0].read_bytes(), paths[1].read_bytes())
            self.assertTrue(paths[0].read_bytes().startswith(b'Kaydara FBX Binary'))
            self.assertEqual(results[0]['frameCount'], clip['durationFrames'])
            self.assertGreater(results[0]['meshCount'], 0)
            self.assertIn(b'TransformLink', paths[0].read_bytes())
            self.assertIn(b'AnimationCurve', paths[0].read_bytes())
            self.assertIn(b'LayerElementUV', paths[0].read_bytes())

    def test_export_options_preserve_bones_without_helpers(self):
        _, bones, tracks = oracle_fixture()
        asset = {'name': 'Oracle', 'models': [{'id': 'model-0', 'name': 'Rig', 'bones': bones, 'meshes': []}]}
        clip = {'name': 'Oracle', 'fps': 30, 'lastFrame': 20, 'bones': tracks}
        with tempfile.TemporaryDirectory() as temp:
            result = export_fbx(Path(temp)/'pose.fbx', asset, clip, up_axis='Z', include_mesh=False, root_motion=False)
            self.assertEqual(result['helperBoneCount'], 0)
            self.assertEqual(result['modelGroupBoneCount'], 0)
            self.assertEqual(result['meshCount'], 0)
            self.assertEqual(result['upAxis'], 'Z')
            with self.assertRaises(ValueError): export_fbx(Path(temp)/'bad.fbx', asset, clip, rig_id='missing')

    def test_sdk_default_attributes_resolve_meshes_and_skeletons(self):
        root = Path(__file__).parent / 'demo-assets'
        asset = load_asset(root / 'Entities/PreviewRobot/PreviewRobot.owentity', root)
        clip = load_clip(root / 'Animations/001_Wave.owanimclip')
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'sdk.fbx'
            result = export_fbx(path, asset, clip)
            records = {n['name']: n for n in read_binary_fbx(path)}
        objects = {n['props'][0]: n for n in records['Objects']['children']}
        connections = [n['props'] for n in records['Connections']['children']]
        mesh_count = joint_count = 0
        for ident, node in objects.items():
            if node['name'] != 'Model' or node['props'][2] not in ('Mesh', 'LimbNode'): continue
            # Match SDK GetNodeAttribute semantics: a connection alone does not
            # select it; an absent DefaultAttributeIndex defaults to invalid -1.
            index = properties(node).get('DefaultAttributeIndex', [None]*4 + [-1])[4]
            attributes = [objects[c[1]] for c in connections if c[0] == 'OO' and c[2] == ident
                          and objects[c[1]]['name'] in ('Geometry', 'NodeAttribute')]
            self.assertGreaterEqual(index, 0, node['props'][1])
            self.assertLess(index, len(attributes), node['props'][1])
            attribute = attributes[index]
            self.assertEqual(attribute['props'][2], node['props'][2])
            if node['props'][2] == 'Mesh':
                mesh_count += 1
                self.assertGreater(len(child(attribute, 'Vertices')['props'][0]), 0)
                self.assertGreater(len(child(attribute, 'PolygonVertexIndex')['props'][0]), 0)
            else:
                joint_count += 1
                self.assertEqual(child(attribute, 'TypeFlags')['props'], ['Skeleton'])
        self.assertEqual(mesh_count, result['meshCount'])
        self.assertEqual(joint_count, result['boneCount'] + result['helperBoneCount'] + result['modelGroupBoneCount'])

    def test_animation_take_is_discoverable_and_channels_are_marked_animated(self):
        root = Path(__file__).parent / 'demo-assets'
        asset = load_asset(root / 'Entities/PreviewRobot/PreviewRobot.owentity', root)
        clip = load_clip(root / 'Animations/001_Wave.owanimclip')
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'motion.fbx'
            export_fbx(path, asset, clip, clip_name='Wave example')
            records = {n['name']: n for n in read_binary_fbx(path)}
        objects = {n['props'][0]: n for n in records['Objects']['children']}
        stack = next(n for n in objects.values() if n['name'] == 'AnimationStack')
        stack_name = stack['props'][1].split('\x00\x01')[0]
        doc = child(records['Documents'], 'Document')
        self.assertEqual(properties(doc)['ActiveAnimStackName'][4], stack_name)
        self.assertEqual(child(records['Takes'], 'Current')['props'], [stack_name])
        take = child(records['Takes'], 'Take')
        self.assertEqual(take['props'], [stack_name])
        self.assertEqual(child(take, 'LocalTime')['props'], [properties(stack)['LocalStart'][4], properties(stack)['LocalStop'][4]])
        self.assertGreater(properties(stack)['LocalStop'][4], 0)
        connections = [n['props'] for n in records['Connections']['children']]
        animated_channels = 0
        for c in connections:
            if c[0] != 'OP' or objects[c[2]]['name'] != 'Model': continue
            self.assertEqual(properties(objects[c[2]])[c[3]][3], 'A+')
            curve_node = objects[c[1]]
            self.assertEqual(properties(curve_node)['d'][1:4], ['Compound', '', ''])
            for key in ('d|X', 'd|Y', 'd|Z'):
                self.assertTrue(any(edge[0] == 'OP' and edge[2:] == [c[1], key] for edge in connections))
            animated_channels += 1
        self.assertGreater(animated_channels, 0)
        self.assertTrue(any(len(set(child(n, 'KeyValueFloat')['props'][0])) > 1
                            for n in objects.values() if n['name'] == 'AnimationCurve'))

    def test_unbaked_mask_cannot_make_constant_white_emission(self):
        for descriptor in [
            {'emissiveMode': 'baseColorMask', 'emissiveIntensity': 16},
            {'unbakedMaps': ['emissive'], 'emissiveIntensity': 16,
             'maps': {'emissive': {'relativePath': 'tex/mask.png'}}},
        ]:
            scene = _Scene(); scene.material(descriptor, 'Material')
            material = scene.objects.children[0]
            props = next(n for n in material.children if n.name == b'Properties70')
            strength = next(n for n in props.children if n.props[0][1] == 'EmissiveFactor')
            self.assertEqual(strength.props[-1][1], 0.)
            self.assertFalse(any(n.name == b'Texture' for n in scene.objects.children))

    def test_duplicate_attachment_names_remain_unchanged_on_every_rig(self):
        _, bones, tracks = oracle_fixture()
        asset = {'name': 'Duplicates', 'models': [
            {'id': 'child', 'name': 'Other', 'bones': bones[:1], 'meshes': []},
            {'id': 'main', 'name': 'Selected', 'bones': bones, 'meshes': []}]}
        clip = {'name': 'Oracle', 'fps': 30, 'lastFrame': 1, 'bones': tracks}
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'pose.fbx'
            result=export_fbx(path, asset, clip, rig_id='main')
            self.assertFalse(any('duplicate bone' in warning for warning in result['warnings']))
            records = {n['name']: n for n in read_binary_fbx(path)}
        names = [n['props'][1].split('\x00\x01')[0] for n in records['Objects']['children']
                 if n['name'] == 'Model' and n['props'][2] == 'LimbNode']
        self.assertEqual(names.count('root'), 2)
        self.assertFalse(any('__WH_' in name or 'child__' in name for name in names))

    def test_centimetres_are_baked_into_geometry_animation_and_bind_pose(self):
        root = Path(__file__).parent / 'demo-assets'
        asset = load_asset(root / 'Entities/PreviewRobot/PreviewRobot.owentity', root)
        clip = load_clip(root / 'Animations/001_Wave.owanimclip')
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'scale.fbx'
            result = export_fbx(path, asset, clip)
            records = {n['name']: n for n in read_binary_fbx(path)}
        objects = {n['props'][0]: n for n in records['Objects']['children']}
        all_nodes = [n for n in objects.values() if n['name'] == 'Model']
        nodes = {(n['props'][1].split('\x00\x01')[0], n['props'][2]): n for n in all_nodes}
        geometry = next(n for n in objects.values() if n['name'] == 'Geometry')
        expected_vertices = np.asarray(asset['models'][0]['meshes'][0]['positions']) * 100.
        np.testing.assert_allclose(child(geometry, 'Vertices')['props'][0], expected_vertices)
        self.assertEqual(properties(records['GlobalSettings'])['UnitScaleFactor'][4], 1.)
        self.assertEqual(result['unit'], 'centimetres')
        self.assertEqual(result['scale'], 100.)
        for node in asset['exportHierarchy']:
            subtype = {'bone': 'LimbNode', 'mesh': 'Mesh'}.get(node['type'], 'Null')
            actual = nodes[(node['name'], subtype)]
            np.testing.assert_allclose(properties(actual)['Lcl Translation'][4:], np.asarray(node['matrix'])[12:15] * 100.)
            np.testing.assert_allclose(properties(actual)['Lcl Scaling'][4:], [1., 1., 1.])
        connections = [n['props'] for n in records['Connections']['children']]
        ev = PoseEvaluator(asset['models'][0]['bones'], clip['bones'])
        pose = ev.evaluate(clip['lastFrame'])
        for source in asset['exportHierarchy']:
            if source['type'] != 'bone': continue
            model = nodes[(source['name'], 'LimbNode')]
            curve_node = next(c[1] for c in connections if c[0] == 'OP' and c[2] == model['props'][0] and c[3] == 'Lcl Translation')
            for channel, letter in enumerate('XYZ'):
                curve = objects[next(c[1] for c in connections if c[0] == 'OP' and c[2] == curve_node and c[3] == 'd|' + letter)]
                self.assertAlmostEqual(child(curve, 'KeyValueFloat')['props'][0][-1], pose['positions'][source['boneIndex']][channel] * 100., places=4)
        # Bind poses carry the same centimetre positions as the joints.
        bind_pose = next(n for n in objects.values() if n['name'] == 'Pose')
        by_ident = {n['props'][0]: n for n in all_nodes}
        for entry in bind_pose['children']:
            if entry['name'] != 'PoseNode': continue
            ident = child(entry, 'Node')['props'][0]
            matrix = np.array(child(entry, 'Matrix')['props'][0]).reshape(4, 4).T
            self.assertTrue(np.isfinite(matrix).all())
            self.assertIn(ident, by_ident)

    def test_original_source_wrappers_names_and_parent_connections(self):
        from test_formats import model_bytes, entity_bytes
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'main.owmdl').write_bytes(model_bytes(name='Main.00C'))
            (root / 'child.owmdl').write_bytes(model_bytes(name='Child.00C'))
            (root / 'main.owentity').write_bytes(entity_bytes(name='Gameplay1P', model='main.owmdl', children=[('child.owentity', 'bone_0002')], relative='.'))
            (root / 'child.owentity').write_bytes(entity_bytes(name='Child header ignored', model='child.owmdl', relative='.'))
            asset = load_asset(root / 'main.owentity', root)
            clip = {'name': 'Rest', 'fps': 30., 'lastFrame': 0, 'bones': []}
            path = root / 'source.fbx'; export_fbx(path, asset, clip)
            records = {n['name']: n for n in read_binary_fbx(path)}
        objects = {n['props'][0]: n for n in records['Objects']['children'] if n['name'] == 'Model'}
        links = {n['props'][1]: n['props'][2] for n in records['Connections']['children']
                 if n['props'][0] == 'OO' and n['props'][1] in objects and (n['props'][2] in objects or n['props'][2] == 0)}
        paths = {}
        def path_of(ident):
            if not ident: return ()
            return path_of(links[ident]) + (objects[ident]['props'][1].split('\x00\x01')[0],)
        for ident, node in objects.items(): paths[path_of(ident)] = node
        pelvis = ('Gameplay1P', 'Main.00C', 'main_Skeleton', 'bone_0001', 'Pelvis')
        self.assertIn(pelvis, paths)
        self.assertIn(pelvis + ('child', 'Child.00C', 'child_Skeleton', 'bone_0001', 'Pelvis'), paths)
        self.assertIn(('Gameplay1P', 'Main.00C', 'Triangle'), paths)
        self.assertIn(pelvis + ('child', 'Child.00C', 'Triangle'), paths)
        self.assertEqual(properties(paths[pelvis])['InheritType'][4], 2)
        self.assertEqual(properties(paths[pelvis[:-1]])['InheritType'][4], 1)
        self.assertTrue(all(not any(part.startswith('__WH_') for part in path) for path in paths))


if __name__ == '__main__': unittest.main()
