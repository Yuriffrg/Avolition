"""Synthetic OWLib map records, real model/material parsing, no game extraction."""
import copy
from dataclasses import replace
import json
import math
from pathlib import Path
import struct
import tempfile
import unittest
from unittest.mock import patch

from owpreview.formats import FormatError, IDENTITY, _multiply
from owpreview.map_formats import (DEFAULT_LIMITS, expand_map_asset, load_map, read_map,
                                   read_map_header, scan_maps)
from test_formats import entity_bytes, model_bytes, pack, string
from create_material_demo import generate, look_bytes, material_bytes


def record_bytes(position=(0, 0, 0), scale=(1, 1, 1), rotation=(0, 0, 0, 1)):
    return pack('10f', *position, *scale, *rotation)


def map_bytes(objects=None, details=None, lights=None, sounds=None, version=(2, 1), name='Fixture map'):
    """objects=[(model,[(look,[recordbytes,...]),...])], details=[(model,look,record)]."""
    objects, details, lights, sounds = objects or [], details or [], lights or [], sounds or []
    result = pack('HH', *version) + string(name) + pack('III', len(objects), len(details), len(lights))
    for model, groups in objects:
        result += string(model) + pack('I', len(groups))
        for look, records in groups:
            result += string(look) + pack('I', len(records)) + b''.join(records)
    for model, look, record in details:
        result += string(model) + string(look) + record
    for light in lights:
        result += light
    result += pack('I', len(sounds))
    for position, files in sounds:
        result += pack('3fi', *position, len(files)) + b''.join(string(file) for file in files)
    return result


def write_fixture(root):
    """Two appearances of one triangle plus an attached entity detail, all real parsers."""
    root = Path(root)
    def write(relative, content):
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path
    write('Models/Prop/Prop.owmdl', model_bytes(bones=[], slots=0, name='Prop'))
    write('Models/Prop/ModelLooks/Blue.owmat', look_bytes([(4000, '../Materials/Blue.owmat')]))
    write('Models/Prop/ModelLooks/Red.owmat', look_bytes([(4000, '../Materials/Red.owmat')]))
    write('Models/Prop/Materials/Blue.owmat', material_bytes(shader=11))
    write('Models/Prop/Materials/Red.owmat', material_bytes(shader=12))
    write('Body.owmdl', model_bytes(name='Body', sockets=[
        ('socket', 'bone_0002', (2, 4, 0), (0, 0, 0, 1))]))
    write('Child.owmdl', model_bytes(bones=[], slots=0, name='Child'))
    write('Child.owentity', entity_bytes(name='Child', model='Child.owmdl', relative='.'))
    write('Body.owentity', entity_bytes(name='Body', model='Body.owmdl', relative='.',
                                      children=[('Child.owentity', 'socket')]))
    return write('Example.owmap', map_bytes(objects=[('Models/Prop/Prop.owmdl', [
        ('Models/Prop/ModelLooks/Blue.owmat', [record_bytes(position=(10, 20, 30)),
                                              record_bytes(position=(40, 50, 60))]),
        ('Models/Prop/ModelLooks/Red.owmat', [record_bytes(position=(-1, 2, 3), scale=(-2, 3, 4),
                                                          rotation=(0, 0, math.sqrt(.5), math.sqrt(.5)))])])],
        details=[('Body.owentity', 'null', record_bytes(position=(100, 200, 300), scale=(2, 3, 4)))]))


class MapFormatTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, name, blob):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(blob)
        return path

    def test_header_and_every_section_for_current_version(self):
        light = pack('7fIf3ffQQ', 1, 2, 3, 0, 0, 0, 1, 1, 45, .1, .2, .3, 100, 12, 13)
        path = self.write('Tést café.owmap', map_bytes(name='Máp ' + 'x' * 128,
            objects=[('Models/Prop.owmdl', [('Look.owmat', [record_bytes()])])],
            details=[('Entity.owentity', 'null', record_bytes(position=(1, 2, 3)))],
            lights=[light], sounds=[((3, 4, 5), ['Sounds/é.ogg', 'Sounds/b.ogg'])]))
        header = read_map_header(path)
        self.assertEqual((header['objectCount'], header['detailCount'], header['lightCount']), (1, 1, 1))
        self.assertNotIn('instances', header)
        parsed = read_map(path)
        self.assertEqual(len(parsed['instances']), 2)
        self.assertEqual(parsed['instances'][1]['material'], '')
        self.assertEqual(parsed['instances'][1]['category'], 'Details')
        self.assertEqual(parsed['lights'][0]['projectionTextures'], [12, 13])
        self.assertEqual(parsed['sounds'][0]['files'], ['Sounds/é.ogg', 'Sounds/b.ogg'])

    def test_legacy_light_layout_preserves_following_sounds(self):
        prefix = pack('7fIf3f', 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1)
        extras = b''.join(bytes(struct.calcsize(fmt)) for fmt in (
            '<IIBBBBII', '<fff', '<ffff', '<fff', '<ffff', '<fff', '<ffff', '<ffIHHII'))
        path = self.write('legacy.owmap', map_bytes(version=(2, 0), lights=[prefix + extras],
                                                 sounds=[((9, 8, 7), ['sound.ogg'])]))
        parsed = read_map(path)
        self.assertEqual(parsed['version'], '2.0')
        self.assertNotIn('intensity', parsed['lights'][0])
        self.assertEqual(parsed['sounds'][0]['position'], [9, 8, 7])

    def test_truncation_corruption_and_future_version_are_rejected(self):
        valid = map_bytes(objects=[('a.owmdl', [('', [record_bytes()])])])
        bad = [valid[:i] for i in (0, 3, 8, 20, len(valid)-1)] + [valid + b'extra', map_bytes(version=(2, 2))]
        bad += [map_bytes(details=[('a.owmdl', '', record_bytes(rotation=(0, 0, 0, 0)))])]
        bad += [map_bytes(details=[('a.owmdl', '', record_bytes(position=(math.nan, 0, 0)))])]
        bad += [map_bytes()[:-4] + pack('I', 1) + pack('3fi', 0, 0, 0, -1)]
        for blob in bad:
            with self.subTest(size=len(blob)):
                with self.assertRaises(FormatError):
                    read_map(self.write('bad.owmap', blob))

    def test_counts_and_file_size_fail_before_geometry_loading(self):
        path = self.write('large.owmap', map_bytes(objects=[('a.owmdl', [('', [record_bytes()] * 3)])]))
        with self.assertRaisesRegex(FormatError, 'instances'):
            read_map(path, limits=replace(DEFAULT_LIMITS, instances=2))
        with self.assertRaisesRegex(FormatError, 'byte limit'):
            read_map_header(path, limits=replace(DEFAULT_LIMITS, file_bytes=8))
        bomb = pack('HH', 2, 1) + string('Bad') + pack('III', 1, 0, 0)
        bomb += string('a.owmdl') + pack('I', 1) + string('') + pack('I', 2**32-1)
        with self.assertRaisesRegex(FormatError, 'instances'):
            read_map(self.write('bomb.owmap', bomb))

    def test_compact_prototypes_share_geometry_across_placements_and_looks(self):
        path = write_fixture(self.root)
        asset = load_map(path)
        self.assertEqual(asset['map']['instanceCount'], 4)
        self.assertEqual(asset['map']['modelCount'], 5)
        self.assertEqual(asset['map']['prototypeCount'], 3)
        self.assertEqual(asset['map']['meshCount'], 5)
        self.assertEqual(asset['map']['polygonCount'], 5)
        self.assertTrue(all('meshes' not in model for model in asset['models']))
        self.assertEqual(len({model['meshPrototype'] for model in asset['models'][:3]}), 1)
        expanded = expand_map_asset(asset)
        self.assertIs(expanded['models'][0]['meshes'][0]['positions'], expanded['models'][2]['meshes'][0]['positions'])
        a, b, c = asset['models'][:3]
        self.assertIs(a['materials'], b['materials'])
        self.assertNotEqual(a['materials']['4000']['sourcePath'], c['materials']['4000']['sourcePath'])
        self.assertFalse(any('Prop.owmdl: no material look reference' in warning for warning in asset['warnings']))
        self.assertNotIn('meshPrototypes', expanded)
        self.assertTrue(all('meshPrototype' not in model for model in expanded['models']))

    def test_mesh_names_are_per_instance_not_shared_prototype(self):
        asset = load_map(write_fixture(self.root))
        asset['models'][0]['meshNames'] = {'0': 'Named first mesh'}
        expanded = expand_map_asset(asset)
        self.assertEqual(expanded['models'][0]['meshes'][0]['name'], 'Named first mesh')
        self.assertEqual(expanded['models'][1]['meshes'][0]['name'], 'Triangle')
        self.assertEqual(next(iter(asset['meshPrototypes'].values()))['meshes'][0]['name'], 'Triangle')

    def test_blender_collision_materials_removed_per_look_with_stable_source_indices(self):
        generate(self.root)
        folder = 'Models/MaterialRobot.00C/'
        self.write(folder + 'Materials/0000000007A1.owmat', material_bytes(shader=33))
        self.write(folder + 'ModelLooks/Collision.owmat', look_bytes([
            (1, '../../Materials/0000000007A1.owmat'),
            (2, '../../Materials/000000B002.owmat'), (3, '../../Materials/000000B003.owmat')]))
        path = self.write('collision.owmap', map_bytes(objects=[(folder + 'MaterialRobot.owmdl', [
            (folder + 'ModelLooks/Collision.owmat', [record_bytes()]),
            (folder + 'ModelLooks/Default.owmat', [record_bytes()])])]))
        asset = load_map(path)
        first, second = asset['models']
        self.assertNotEqual(first['meshPrototype'], second['meshPrototype'])
        meshes = asset['meshPrototypes'][first['meshPrototype']]['meshes']
        self.assertEqual([mesh['sourceMeshIndex'] for mesh in meshes], [4, 5, 6, 7])
        self.assertEqual(len(asset['meshPrototypes'][second['meshPrototype']]['meshes']), 8)
        self.assertEqual(asset['map']['omittedCollisionMeshCount'], 4)
        self.assertEqual(asset['map']['omittedCollisionPolygonCount'], 48)
        self.assertEqual(asset['map']['meshCount'], 12)
        self.assertEqual(set(first['materials']), {'2', '3'})
        nodes = [node for node in asset['exportHierarchy'] if node['modelId'] == first['id'] and node['type'] == 'mesh']
        self.assertEqual([(node['sourceMeshIndex'], node['meshIndex']) for node in nodes], [(4, 0), (5, 1), (6, 2), (7, 3)])
        self.assertTrue(any('engine hit-test' in warning for warning in asset['warnings']))

    def test_map_entity_look_applies_to_children_like_blender(self):
        path = write_fixture(self.root)
        self.write('BodyLook.owmat', look_bytes([(4000, 'BodyMaterial.owmat')]))
        self.write('BodyMaterial.owmat', material_bytes(shader=33))
        path.write_bytes(map_bytes(details=[('Body.owentity', 'BodyLook.owmat', record_bytes())]))
        asset = load_map(path)
        self.assertEqual(len(asset['models']), 2)
        for model in asset['models']:
            self.assertEqual(model['materials']['4000']['name'], 'BodyMaterial')

    def test_partial_map_entity_look_retains_unmentioned_child_slots(self):
        generate(self.root)
        path = self.write('partial-look.owmap', map_bytes(details=[
            ('Entities/MaterialRobot/MaterialRobot.owentity', 'Partial.owmat', record_bytes())]))
        self.write('Partial.owmat', look_bytes([(2, 'Override.owmat')]))
        self.write('Override.owmat', material_bytes(shader=55))
        model = load_map(path)['models'][0]
        self.assertEqual(set(model['materials']), {'1', '2', '3'})
        self.assertEqual(model['materials']['2']['name'], 'Override')
        self.assertEqual(model['materials']['1']['name'], '000000B001')

    def test_present_null_map_look_slot_clears_only_that_binding(self):
        generate(self.root)
        path = self.write('null-slot.owmap', map_bytes(details=[
            ('Entities/MaterialRobot/MaterialRobot.owentity', 'Partial.owmat', record_bytes())]))
        self.write('Partial.owmat', look_bytes([(2, 'null')]))
        model = load_map(path)['models'][0]
        self.assertEqual(set(model['materials']), {'1', '3'})
        self.assertEqual(model['materials']['1']['name'], '000000B001')

    def test_trs_nonuniform_mirror_and_socket_attachments_match_preview_and_native_graph(self):
        asset = load_map(write_fixture(self.root))
        graph = {node['id']: node for node in asset['exportHierarchy']}
        model = asset['models'][2]
        matrix = model['attachmentMatrix']
        expected = [0, -2, 0, 0, -3, 0, 0, 0, 0, 0, 4, 0, -1, 2, 3, 1]
        for actual, wanted in zip(matrix, expected):
            self.assertAlmostEqual(actual, wanted, places=6)
        self.assertEqual(graph[model['id'] + ':model']['matrix'], matrix)
        body, child = asset['models'][3:]
        self.assertEqual(child['parentId'], body['id'])
        self.assertEqual(child['attachmentBone'], 'bone_0002')
        self.assertEqual(graph[body['id'] + ':entity']['matrix'], body['attachmentMatrix'])
        self.assertEqual(graph[child['id'] + ':entity']['matrix'], child['attachmentMatrix'])
        self.assertNotEqual(child['attachmentMatrix'], IDENTITY)
        self.assertEqual(graph[child['id'] + ':entity']['parentId'], body['id'] + ':bone:1')

    def test_missing_assets_are_reported_and_empty_map_errors(self):
        self.write('a.owmdl', model_bytes(bones=[], slots=0))
        path = self.write('partial.owmap', map_bytes(details=[
            ('a.owmdl', 'Missing.owmat', record_bytes()), ('missing.owmdl', '', record_bytes())]))
        asset = load_map(path)
        self.assertEqual(asset['map']['missingInstanceCount'], 1)
        self.assertEqual(asset['models'][0]['materials'], {})
        self.assertTrue(any('material look is missing' in warning for warning in asset['warnings']))
        self.assertTrue(any('could not be loaded' in warning for warning in asset['warnings']))
        self.write('partial.owmap', map_bytes(details=[('missing.owmdl', '', record_bytes())]))
        with self.assertRaisesRegex(FormatError, 'No map meshes'):
            load_map(path)

    def test_map_reference_paths_are_literal_and_cannot_escape(self):
        self.write('model space/é.owmdl', model_bytes(bones=[], slots=0))
        for reference in ('model space/é.owmdl', 'model space\\é.owmdl'):
            asset = load_map(self.write('valid.owmap', map_bytes(details=[(reference, '', record_bytes())])))
            self.assertEqual(len(asset['models']), 1)
        for reference in ('../outside.owmdl', 'C:\\outside.owmdl', '//host/a.owmdl', '/absolute.owmdl',
                          'a:stream.owmdl', 'models/../../outside.owmdl', 'bad.exe'):
            with self.subTest(reference=reference), self.assertRaises(FormatError):
                load_map(self.write('bad.owmap', map_bytes(details=[(reference, '', record_bytes())])))

    def test_aggregate_geometry_instance_and_hierarchy_limits_are_enforced(self):
        path = write_fixture(self.root)
        for changes in ({'unique_vertices': 2}, {'models': 2}, {'mesh_instances': 2}, {'graph_nodes': 2}):
            with self.subTest(changes=changes), self.assertRaisesRegex(FormatError, 'limit'):
                load_map(path, limits=replace(DEFAULT_LIMITS, **changes))

    def test_repeated_source_is_parsed_only_once(self):
        from owpreview.formats import load_asset
        self.write('a.owmdl', model_bytes(bones=[], slots=0))
        path = self.write('many.owmap', map_bytes(objects=[('a.owmdl', [('', [record_bytes()] * 100)])]))
        with patch('owpreview.map_formats.load_asset', wraps=load_asset) as loader:
            asset = load_map(path)
        self.assertEqual(loader.call_count, 1)
        self.assertEqual(len(asset['meshPrototypes']), 1)
        self.assertEqual(len(asset['models']), 100)
        compact_size = len(json.dumps(asset))
        expanded_size = len(json.dumps(expand_map_asset(asset)))
        self.assertLess(compact_size, expanded_size)

    def test_scan_keeps_invalid_map_errors_and_never_invents_assets(self):
        path = write_fixture(self.root)
        self.write('broken.owmap', b'bad')
        self.write('ignored.txt', b'not a map')
        result = scan_maps(self.root)
        self.assertEqual([item['path'] for item in result['maps']], [str(path.resolve())])
        self.assertEqual(len(result['errors']), 1)
        with self.assertRaisesRegex(ValueError, 'smaller'):
            scan_maps(self.root, max_files=1)


if __name__ == '__main__':
    unittest.main()
