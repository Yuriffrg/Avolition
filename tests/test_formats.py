"""Format regression fixtures generated from OWLib's documented binary layout."""

import math
from pathlib import Path
import struct
import tempfile
import unittest

from owpreview.formats import FormatError, load_asset, load_clip, read_clip_header


def pack(fmt, *values):
    return struct.pack('<' + fmt, *values)


def string(value):
    raw = value.encode('utf-8')
    length = len(raw)
    prefix = bytearray()
    while length >= 128:
        prefix.append((length & 127) | 128)
        length >>= 7
    prefix.append(length)
    return bytes(prefix) + raw


def model_bytes(bones=None, mesh=True, sockets=None, name='Fixture', version=(2, 0), slots=5):
    if bones is None:
        bones = [('bone_0001', -1, (0, 0, 0), (0, 0, 0)),
                 ('bone_0002', 0, (0, 2, 0), (0, 0, math.pi / 2))]
    sockets = sockets or []
    blob = pack('HH', *version) + string('') + string(name)
    blob += pack('IHII', 123, len(bones), 1 if mesh else 0, len(sockets))
    for bone_name, parent, position, rotation in bones:
        # Exported non-unit scale is deliberately ignored like BLModel.
        blob += string(bone_name) + pack('h', parent) + pack('9f', *position, 2, 3, 4, *rotation)
    if mesh:
        blob += string('Triangle') + pack('QBIIB', 4000, 1, 3, 1, slots)
        blob += pack('9f', 0, 0, 0, 1, 0, 0, 0, 0, 1)  # raw Y-up: (0,1,0)
        blob += pack('9f', *((0, -1, 0) * 3))  # raw normal +Z
        blob += pack('12f', *((1, 0, 0, 1) * 3))
        blob += pack('6f', 0, 0, 1, 0, 0, 1)
        if slots:
            indices = ([0, 1, -1, 0, 1] * 3) if slots == 5 else ([0] * 3 * slots)
            weights = ([.1, .2, .9, .2, .5] * 3) if slots == 5 else ([1/slots] * 3 * slots)
            blob += pack(f'{len(indices)}h', *indices)
            blob += pack(f'{len(weights)}f', *weights)
        blob += pack('24f', *([0] * 24))
        blob += pack('III', 0, 1, 2)
    for socket_name, bone_name, position, rotation in sockets:
        blob += string(socket_name) + string(bone_name) + pack('7f', *position, *rotation)
    return blob


def entity_bytes(name='Hero', model='Body.00C', children=None, minor=1, relative='../../'):
    children = children or []
    blob = string('owentity') + pack('HH', 2, minor)
    blob += string(name) + string(model) + string('null') + pack('IIIi', 1, 2, 0, len(children))
    for reference, attachment in children:
        blob += string(reference) + pack('QQII', 1, 2, 1, 2) + string(attachment)
    if minor >= 1:
        blob += string('null') + string(relative)
    return blob


def clip_bytes(bones=None, duration=31, fps=30.0, version=(2, 0)):
    if bones is None:
        bones = [('bone_0002', [(0, [(30, (0, 4, 0)), (0, (0, 2, 0))]),
                                (1, [(0, (0, 0, 0, 2)), (30, (0, 0, 1, 0))]),
                                (2, [(0, (1, 1, 1))])])]
    blob = pack('HHIfI', *version, len(bones), fps, duration)
    for name, tracks in bones:
        blob += string(name) + pack('I', len(tracks))
        for kind, keys in tracks:
            components = len(keys[0][1]) if keys else (4 if kind == 1 else 3)
            blob += pack('III', kind, len(keys), components)
            for frame, values in keys:
                blob += pack('I', frame) + pack(f'{components}f', *values)
    return blob


class FormatTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, name, data):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def assertVectorAlmostEqual(self, actual, expected):
        self.assertEqual(len(actual), len(expected))
        for a, b in zip(actual, expected):
            self.assertAlmostEqual(a, b, places=6)

    def test_mesh_y_up_and_weight_merge(self):
        path = self.write('model.owmdl', model_bytes())
        asset = load_asset(path)
        model = asset['models'][0]
        mesh = model['meshes'][0]
        self.assertEqual(mesh['positions'], [0, 0, 0, 1, 0, 0, 0, 1, 0])
        self.assertEqual(mesh['normals'], [0, 0, 1] * 3)
        self.assertEqual(mesh['indices'], [0, 1, 2])
        self.assertEqual(mesh['skinIndices'], [1, 0, 0, 0] * 3)
        self.assertVectorAlmostEqual(mesh['skinWeights'], [.7, .3, 0, 0] * 3)
        self.assertEqual(model['bones'][1]['pos'], [0, 2, 0])
        self.assertEqual(model['bones'][1]['scale'], [1, 1, 1])
        self.assertVectorAlmostEqual(model['bones'][1]['rot'], [0, 0, math.sqrt(.5), math.sqrt(.5)])
        self.assertTrue(any('neutral' in warning for warning in asset['warnings']))

    def test_export_hierarchy_retains_importer_names_without_changing_preview(self):
        self.write('Body.owmdl', model_bytes(name='Body.00C', sockets=[
            ('gun_socket', 'bone_0002', (8, 9, 10), (0, 0, 0, 1))]))
        self.write('Weapon.owmdl', model_bytes(name='Weapon.00C'))
        self.write('Child.owentity', entity_bytes(name='HeaderChild', model='Weapon.owmdl', relative='.'))
        path = self.write('Main.owentity', entity_bytes(name='HeaderRoot', model='Body.owmdl',
                         children=[('Child.owentity', 'gun_socket')], relative='.'))
        asset = load_asset(path, self.root)
        graph = {n['id']: n for n in asset['exportHierarchy']}
        self.assertEqual(graph['model-0:entity']['name'], 'HeaderRoot')
        self.assertEqual(graph['model-1:entity']['name'], 'Child')
        self.assertEqual(graph['model-0:model']['name'], 'Body.00C')
        self.assertEqual(graph['model-0:skeleton']['name'], 'Body_Skeleton')
        self.assertEqual(graph['model-0:bone:1']['name'], 'Pelvis')
        self.assertEqual(graph['model-0:bone:1']['sourceName'], 'bone_0002')
        self.assertEqual(graph['model-1:entity']['parentId'], 'model-0:bone:1')
        self.assertEqual(graph['model-1:entity']['matrix'][12:15], [0., 0., 0.])
        self.assertEqual(graph['model-1:mesh:0']['parentId'], 'model-1:model')
        # Existing preview attachments and raw animation track names are stable.
        self.assertEqual(asset['models'][0]['bones'][1]['name'], 'bone_0002')
        self.assertNotEqual(asset['models'][1]['attachmentMatrix'][12:15], [0., 0., 0.])

    def test_direct_model_export_has_no_artificial_entity_wrapper(self):
        path = self.write('Standalone.owmdl', model_bytes(name='Model header'))
        asset = load_asset(path)
        graph = asset['exportHierarchy']
        self.assertFalse(any(node['type'] == 'entity' for node in graph))
        self.assertEqual(graph[0]['name'], 'Model header')
        self.assertIsNone(graph[0]['parentId'])

    def test_child_export_name_uses_original_reference_guid(self):
        self.write('Body.owmdl', model_bytes())
        self.write('Entities/00000000002A.003/00000000002A.003.owentity',
                   entity_bytes(name='Different header', model='Body.owmdl', relative='../..'))
        main = self.write('Main.owentity', entity_bytes(model='Body.owmdl', relative='.',
                         children=[('00000000002A.003', '')]))
        asset = load_asset(main, self.root)
        child = next(n for n in asset['exportHierarchy'] if n['id'] == 'model-1:entity')
        self.assertEqual(child['name'], '00000000002A')

    def test_xyz_order_multiaxis(self):
        # +90 X then +90 Y then +90 Z gives quaternion (.0, .7071, .0, .7071).
        bones = [('bone_0001', -1, (1, 2, 3), (math.pi/2, math.pi/2, math.pi/2))]
        path = self.write('rotation.owmdl', model_bytes(bones=bones, mesh=False))
        bone = load_asset(path)['models'][0]['bones'][0]
        self.assertVectorAlmostEqual(bone['rot'], [0, math.sqrt(.5), 0, math.sqrt(.5)])

    def test_128_byte_string_and_utf8(self):
        for name in ('a' * 128, 'é' * 64, 'a' * 256):
            with self.subTest(name=name):
                path = self.write('long.owmdl', model_bytes(name=name))
                self.assertEqual(load_asset(path)['models'][0]['name'], name)

    def test_clip_header_and_sorted_absolute_tracks(self):
        path = self.write('000001.owanimclip', clip_bytes())
        header = read_clip_header(path)
        self.assertEqual(header['fps'], 30)
        self.assertEqual(header['durationFrames'], 31)
        self.assertEqual(header['lastFrame'], 30)
        self.assertEqual(header['durationSeconds'], 1)
        self.assertEqual(header['boneCount'], 1)
        self.assertNotIn('bones', header)
        result = load_clip(path)
        bone = result['bones'][0]
        self.assertEqual(bone['name'], 'bone_0002')
        self.assertEqual(bone['positions']['frames'], [0, 30])
        self.assertEqual(bone['positions']['values'], [0, 2, 0, 0, 4, 0])
        self.assertEqual(bone['rotations']['values'], [0, 0, 0, 1, 0, 0, 1, 0])
        self.assertEqual(bone['scales']['values'], [1, 1, 1])

    def test_one_sample_has_zero_duration(self):
        path = self.write('still.owanimclip', clip_bytes(bones=[('bone_0001', [(0, [(0, (0, 1, 0))])])], duration=1))
        for metadata in (read_clip_header(path), load_clip(path)):
            self.assertEqual(metadata['durationFrames'], 1)
            self.assertEqual(metadata['lastFrame'], 0)
            self.assertEqual(metadata['durationSeconds'], 0)

    def test_short_clip_header(self):
        path = self.write('broken.owanimclip', b'\x02\x00')
        with self.assertRaisesRegex(FormatError, 'truncated'):
            read_clip_header(path)

    def test_bad_clip_fps_and_version(self):
        for kwargs in ({'fps': 0}, {'fps': float('nan')}, {'version': (3, 0)}, {'duration': 0}):
            with self.subTest(kwargs=kwargs):
                path = self.write('bad.owanimclip', clip_bytes(**kwargs))
                with self.assertRaises(FormatError):
                    load_clip(path)

    def test_truncated_model_and_clip(self):
        for extension, blob in (('owmdl', model_bytes()), ('owanimclip', clip_bytes())):
            path = self.write('truncated.' + extension, blob[:-3])
            with self.assertRaisesRegex(FormatError, 'truncated'):
                (load_asset if extension == 'owmdl' else load_clip)(path)

    def test_cycle_in_bones(self):
        bones = [('bone_0001', 1, (0, 0, 0), (0, 0, 0)),
                 ('bone_0002', 0, (0, 0, 0), (0, 0, 0))]
        path = self.write('cycle.owmdl', model_bytes(bones=bones, mesh=False))
        with self.assertRaisesRegex(FormatError, 'cycle'):
            load_asset(path)

    def test_forward_parent_is_supported(self):
        bones = [('bone_0001', 1, (0, 1, 0), (0, 0, 0)),
                 ('bone_0002', -1, (0, 1, 0), (0, 0, 0))]
        sockets = [('socket', 'bone_0001', (0, 3, 0), (0, 0, 0, 1))]
        path = self.write('forward.owmdl', model_bytes(bones=bones, mesh=False, sockets=sockets))
        socket = load_asset(path)['models'][0]['sockets'][0]
        self.assertVectorAlmostEqual(socket['matrix'][12:15], [0, 1, 0])

    def test_null_hardpoint_reference_stays_in_model_space(self):
        bones = [('bone_0000', -1, (0, 5, 0), (0, 0, 0))]
        sockets = [('socket', 'bone_0000', (0, 3, 0), (0, 0, 0, 1))]
        path = self.write('unbound.owmdl', model_bytes(bones=bones, mesh=False, sockets=sockets))
        socket = load_asset(path)['models'][0]['sockets'][0]
        self.assertIsNone(socket['bone'])
        self.assertVectorAlmostEqual(socket['matrix'][12:15], [0, 3, 0])

    def test_entity_child_attaches_in_bone_local_space(self):
        # The parent's bone_0002 rest transform is translation(0,2,0) Rz(90).
        # A socket at model (1,2,0), Rz(90) has bone-local (0,-1,0).
        sockets = [('hardpoint_0001', 'bone_0002', (1, 2, 0), (0, 0, math.sqrt(.5), math.sqrt(.5)))]
        self.write('Models/Body.00C/Body.owmdl', model_bytes(sockets=sockets))
        self.write('Models/Weapon.00C/Weapon.owmdl', model_bytes(name='Weapon', slots=0))
        path = self.write('Entities/Hero/Hero.owentity', entity_bytes(children=[('Weapon', 'hardpoint_0001')]))
        self.write('Entities/Weapon/Weapon.owentity', entity_bytes(name='Weapon', model='Weapon.00C'))
        asset = load_asset(path)
        parent, child = asset['models']
        self.assertEqual(child['parentId'], parent['id'])
        self.assertEqual(child['attachmentBone'], 'bone_0002')
        self.assertVectorAlmostEqual(child['attachmentMatrix'][12:15], [0, -1, 0])
        self.assertVectorAlmostEqual(child['attachmentMatrix'][:12], [1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0])

    def test_entity_20_default_layout(self):
        self.write('Models/Body.00C/Body.owmdl', model_bytes())
        path = self.write('Entities/Hero/Hero.owentity', entity_bytes(minor=0))
        self.assertEqual(len(load_asset(path)['models']), 1)

    def test_entity_windows_paths_and_selected_root(self):
        self.write('Models/Body.00C/Body.owmdl', model_bytes())
        path = self.write('Copied/Hero.owentity', entity_bytes(relative='..\\..\\missing'))
        self.assertEqual(len(load_asset(path, self.root)['models']), 1)

    def test_missing_children_report_warning(self):
        self.write('Models/Body.00C/Body.owmdl', model_bytes())
        path = self.write('Entities/Hero/Hero.owentity', entity_bytes(children=[('Missing', 'null')]))
        result = load_asset(path)
        self.assertEqual(len(result['models']), 1)
        self.assertTrue(any('missing child entity Missing' in warning for warning in result['warnings']))

    def test_missing_main_model_is_actionable_error(self):
        path = self.write('Entities/Hero/Hero.owentity', entity_bytes())
        with self.assertRaisesRegex(FormatError, 'missing model Body.00C'):
            load_asset(path)

    def test_entity_cycle_is_warning(self):
        self.write('Models/Body.00C/Body.owmdl', model_bytes())
        path = self.write('Entities/Hero/Hero.owentity', entity_bytes(children=[('Hero', 'null')]))
        result = load_asset(path)
        self.assertEqual(len(result['models']), 1)
        self.assertTrue(any('cycle' in warning for warning in result['warnings']))

    def test_wrong_track_components_rejected(self):
        path = self.write('bad.owanimclip', clip_bytes(bones=[('bone', [(1, [(0, (1, 2, 3))])])]))
        with self.assertRaisesRegex(FormatError, 'needs 4 components'):
            load_clip(path)

    def test_non_finite_track_rejected(self):
        path = self.write('bad.owanimclip', clip_bytes(bones=[('bone', [(0, [(0, (float('inf'), 0, 0))])])]))
        with self.assertRaisesRegex(FormatError, 'non-finite'):
            load_clip(path)

    def test_unknown_track_skipped_and_duration_extended(self):
        bones = [('bone', [(4, [(0, (1, 2))]), (0, [(60, (1, 2, 3))])])]
        path = self.write('future.owanimclip', clip_bytes(bones=bones))
        clip = load_clip(path)
        self.assertEqual(clip['durationFrames'], 61)
        self.assertEqual(clip['lastFrame'], 60)
        self.assertEqual(clip['durationSeconds'], 2)
        self.assertEqual(len(clip['warnings']), 2)


if __name__ == '__main__':
    unittest.main()
