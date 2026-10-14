"""Mock OWLib export fixtures; no real game installation is required."""
import json
from pathlib import Path
import struct
import tempfile
import unittest

from owpreview.animation_evidence_formats import (decode_guid, read_companion,
                                                  read_effect, read_entity, read_model)
from owpreview.formats import FormatError


def pack(fmt, *values):
    return struct.pack('<' + fmt, *values)


def string(value):
    data = value.encode('utf-8')
    count, prefix = len(data), bytearray()
    while count > 127:
        prefix.append((count & 127) | 128)
        count >>= 7
    prefix.append(count)
    return bytes(prefix) + data


def guid(index, resource_type=6, engine=0, upper_key=0):
    reverse_type = int(f'{resource_type - 1:012b}'[::-1], 2)
    return (engine << 60) | (reverse_type << 48) | (upper_key << 32) | index


def playback(absent=True):
    return pack('Bff', absent, 0, 0) + string('null')


def effect_header(counts=(0, 0, 0, 0, 0, 0, 0), version=(1, 2)):
    return string('oweffect') + pack('HHIf7i', *version, 0xBEEF, 2.0, *counts)


def entity_blob(minor=1, count=1):
    blob = string('owentity') + pack('HH', 2, minor)
    blob += string('Gameplay1P') + string('00000000013E.00C') + string('000000000123.00D')
    blob += pack('IIIi', 0x1D, 0x13E, 0x123, count)
    for _ in range(max(0, count)):
        blob += string('child.003') + pack('QQII', 0x0DC0000000000068, 0xFD80000000001AFF, 0x68, 0x1AFF)
        blob += string('hardpoint_0068')
    if minor == 1:
        blob += string('null') + string('../..')
    return blob


def model_blob(names=('bone_0001', 'bone_00A2'), parents=(-1, 0), version=(2, 0)):
    blob = pack('HH', *version) + string('look.owmat') + string('model.00C')
    blob += pack('IHII', 0xFFFFFFFF, len(names), 0, 0)
    for name, parent in zip(names, parents):
        blob += string(name) + pack('h9f', parent, *([0.] * 9))
    return blob


class AnimationEvidenceFormatsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, name, blob):
        path = self.root / name
        path.write_bytes(blob)
        return path

    def test_guid_is_lossless_above_javascript_safe_integer(self):
        value = guid(0xDEADBEEF, 6, 0xF, 0xABCD)
        result = decode_guid(value)
        self.assertEqual(result['fullGuid'], f'{value:016X}')
        self.assertEqual(result['type'], '006')
        self.assertEqual(result['longKey'], 'ABCDDEADBEEF')
        self.assertEqual(result['index'], 'DEADBEEF')
        self.assertEqual(result['engine'], 'F')
        self.assertEqual(json.loads(json.dumps(result)), result)
        self.assertEqual(decode_guid('0x' + f'{value:016x}'), result)

    def test_same_short_display_can_have_different_full_identity(self):
        a, b = decode_guid(guid(123)), decode_guid(guid(123, engine=1))
        self.assertEqual(a['display'], b['display'])
        self.assertNotEqual(a['fullGuid'], b['fullGuid'])

    def test_guid_rejects_partial_or_unsafe_input(self):
        for value in ('000000000123.006', '123', 1.0, True, -1, 2 ** 64):
            with self.subTest(value=value), self.assertRaises(ValueError):
                decode_guid(value)
        self.assertTrue(decode_guid(0)['isNull'])

    def test_model_preserves_bone_identifier_separate_from_array_position(self):
        result = read_model(self.write('model.owmdl', model_blob()))
        self.assertEqual(result['bones'][1], {'name': 'bone_00A2', 'parentIndex': 0})
        self.assertEqual(result['index'], 'FFFFFFFF')
        self.assertIsNone(result['fullGuid'])
        self.assertEqual(result['unparsedBytes'], 0)

    def test_model_validates_header_skeleton_and_cycles(self):
        blobs = [model_blob(version=(2, 7)), model_blob(names=('same', 'same')),
                 model_blob(parents=(1, 0)), model_blob(parents=(-1, 2)), model_blob()[:-1]]
        for i, blob in enumerate(blobs):
            with self.subTest(i=i), self.assertRaises(FormatError):
                read_model(self.write('model.owmdl', blob))

    def test_model_geometry_is_explicitly_unparsed(self):
        result = read_model(self.write('model.owmdl', model_blob() + b'unparsed geometry'))
        self.assertEqual(result['unparsedBytes'], 17)

    def test_entity_keeps_full_child_identifiers_and_short_indices_distinct(self):
        result = read_entity(self.write('entity.owentity', entity_blob()))
        self.assertEqual(result['modelReference'], '00000000013E.00C')
        self.assertEqual(result['effectReference'], '000000000123.00D')
        child = result['children'][0]
        self.assertEqual(child['identifier']['fullGuid'], 'FD80000000001AFF')
        self.assertEqual(child['identifierIndex'], '00001AFF')
        self.assertEqual(child['hardpoint']['fullGuid'], '0DC0000000000068')
        self.assertIsNone(result['fullGuid'])

    def test_entity_20_omits_21_tail_and_preserves_missing_references(self):
        result = read_entity(self.write('entity.owentity', entity_blob(0, 0)))
        self.assertEqual(result['relativeRoot'], '../../')
        self.assertEqual(result['materialLookReference'], '')

    def test_entity_bad_magic_version_or_count_is_not_evidence(self):
        blobs = [b'\x00' + entity_blob()[9:], entity_blob(9), entity_blob(1, -1), entity_blob()[:-3]]
        for i, blob in enumerate(blobs):
            with self.subTest(i=i), self.assertRaises(FormatError):
                read_entity(self.write('entity.owentity', blob))

    def test_reference_companion_keeps_unresolved_literal_path(self):
        target = 'Models\\枪 0000000001D4.00C\\AnimationEffects\\0000000001A6\\0000000001A6.oweffect'
        blob = string('owanim') + pack('HHIfi', 1, 0, 0x1A6, 30, 1) + string(target)
        result = read_companion(self.write('reference.owanim', blob))
        self.assertEqual(result['animationIndex'], '000001A6')
        self.assertEqual(result['effectReference'], target)
        self.assertIsNone(result['effect'])
        self.assertIsNone(result['fullGuid'])

    def test_companion_validates_kind_version_fps_and_truncation(self):
        for version, fps, kind in [((1, 9), 30, 1), ((1, 0), 0, 1), ((1, 0), float('nan'), 1),
                                   ((1, 0), 30, 2)]:
            blob = string('owanim') + pack('HHIfi', *version, 123, fps, kind) + string('x')
            with self.subTest(version=version, fps=fps, kind=kind), self.assertRaises(FormatError):
                read_companion(self.write('bad.owanim', blob))

    def test_embedded_effect_retains_full_guid_role_references(self):
        animation, model, material, identifier = guid(123), guid(456, 12), guid(789, 8), guid(44, 28)
        effect = effect_header((1, 1, 0, 0, 0, 0, 0))
        effect += playback() + pack('QQQ', animation, material, model) + string('m.owmdl') + string('a.owanim')
        effect += playback(False) + pack('BQQI', 7, animation, identifier, 44) + string('other.owanim')
        blob = string('owanim') + pack('HHIfi', 1, 0, 123, 30, 0)
        blob += string('Models/m/Animations/0/32/a.owanimclip') + string('Models/m/m.owmdl') + effect
        result = read_companion(self.write('data.owanim', blob))
        self.assertEqual(result['clipReference'], 'Models/m/Animations/0/32/a.owanimclip')
        self.assertEqual(result['effect']['dmce'][0]['animation']['fullGuid'], f'{animation:016X}')
        self.assertEqual(result['effect']['dmce'][0]['model']['type'], '00C')
        self.assertEqual(result['effect']['cece'][0]['action'], 7)
        self.assertEqual(result['effect']['cece'][0]['identifier']['fullGuid'], f'{identifier:016X}')

    def test_effect_other_records_and_omitted_sections_keep_stream_aligned(self):
        entity, model, material = guid(33, 3), guid(44, 12), guid(55, 8)
        blob = effect_header((0, 0, 1, 1, 2, 3, 1))
        blob += playback() + pack('QI', entity, 44) + string('entity.owentity')
        blob += playback() + pack('QQ', model, material) + string('model.owmdl')
        blob += playback() + pack('Iii', 19, 1, 2) + string('one.ogg') + string('two.ogg')
        result = read_effect(self.write('effect.oweffect', blob))
        self.assertEqual(result['nece'][0]['entity']['fullGuid'], f'{entity:016X}')
        self.assertEqual(result['rpce'][0]['model']['type'], '00C')
        self.assertEqual(result['svce'][0]['soundReferences'], [['one.ogg', 'two.ogg']])
        self.assertEqual(len(result['warnings']), 2)
        self.assertEqual(result['trailingBytes'], 0)

    def test_effect_rejects_unsupported_or_unbounded_data(self):
        blobs = [effect_header(version=(1, 1)), effect_header((-1, 0, 0, 0, 0, 0, 0)),
                 effect_header((100001, 0, 0, 0, 0, 0, 0)), effect_header((1, 0, 0, 0, 0, 0, 0)),
                 effect_header((0, 0, 0, 0, 0, 0, 1)) + playback() + pack('Ii', 0, -1)]
        for i, blob in enumerate(blobs):
            with self.subTest(i=i), self.assertRaises(FormatError):
                read_effect(self.write('bad.oweffect', blob))

    def test_unknown_trailing_bytes_are_diagnostic_only(self):
        result = read_effect(self.write('effect.oweffect', effect_header() + b'123'))
        self.assertEqual(result['trailingBytes'], 3)
        self.assertTrue(result['warnings'])


if __name__ == '__main__':
    unittest.main()
