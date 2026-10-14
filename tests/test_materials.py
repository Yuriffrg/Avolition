"""Reference-bound material parsing and Blender-derived texture role regressions."""

from pathlib import Path
import json
import tempfile
import unittest

from owpreview.formats import FormatError, load_asset
from owpreview.materials import MaterialResolver, read_owmat, is_collision_material
from test_formats import entity_bytes, model_bytes, pack, string
from create_material_demo import generate, look_bytes, material_bytes, png_bytes, textured_model


class MaterialTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.warnings = []
        self.resolver = MaterialResolver(self.root / 'model.owmdl', self.root, self.warnings)

    def tearDown(self):
        self.temp.cleanup()

    def write(self, relative, value):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(value)
        return path

    def texture(self, name='image.png'):
        return self.write(name, png_bytes(2, 2, lambda x, y: (30, 100, 200, 150)))

    def describe(self, bindings, inputs=(), shader=0, name='material.owmat'):
        self.texture()
        return self.resolver.descriptor(self.write(name, material_bytes(
            [('image.png', key) for key in bindings], inputs, shader)))

    def operation(self, material, role):
        return self.resolver.textures[material['maps'][role]['textureId']]

    def test_model_look_uint64_keys_and_long_strings(self):
        key = 2**64 - 1
        reference = '../' + 'a'*150 + '.owmat'
        path = self.write('look.owmat', look_bytes([(key, reference)]))
        self.assertEqual(read_owmat(path)['entries'], [(str(key), reference)])

    def test_null_look_writer_quirk_and_empty_look(self):
        for suffix in (b'', pack('Q', 0)):
            path = self.write('empty.owmat', look_bytes() + suffix)
            self.assertEqual(read_owmat(path)['entries'], [])
        path.write_bytes(look_bytes() + pack('Q', 1))
        with self.assertRaises(FormatError):
            read_owmat(path)

    def test_unknown_padded_static_inputs_keep_record_alignment(self):
        path = self.write('material.owmat', material_bytes([], [
            (123, b'unknown input\0\0'), (3250491852, pack('f', 3) + b'padding'),
            (565460110, pack('f', .75))]))
        record = read_owmat(path)
        self.assertEqual(record['inputs'][123], b'unknown input\0\0')
        self.assertEqual(record['inputs'][565460110], pack('f', .75))
        self.assertEqual(self.resolver._input(record, 3250491852, 'f'), 3)

    def test_short_and_nonfinite_known_inputs_warn_without_crashing(self):
        material = self.describe([3166598269], [(3250491852, b'\0'), (2446772623, pack('f', float('nan')))])
        self.assertEqual(material['emissiveIntensity'], 16)
        self.assertTrue(any('short static' in warning for warning in self.warnings))
        self.assertTrue(any('non-finite static' in warning for warning in self.warnings))

    def test_malformed_material_records_fail_strictly(self):
        valid = material_bytes([('a.png', 2903569922)], [(3, b'test')])
        invalid = [valid[:index] for index in (0, 4, 8, 20, len(valid)-1)]
        invalid += [pack('HHI', 2, 0, 0), pack('HHI', 3, 1, 0), pack('HHI', 3, 0, 2),
                    pack('HHIQQI', 3, 0, 0, 5000, 0, 0), valid + b'extra',
                    pack('HHIQQI', 3, 0, 0, 0, 1, 0) + pack('II', 123, 2**30)]
        for index, blob in enumerate(invalid):
            with self.subTest(index=index):
                with self.assertRaises(FormatError):
                    read_owmat(self.write('bad.owmat', blob))

    def test_writer_model_look_paths_and_reference_based_material_keys(self):
        root = generate(self.root)
        asset = load_asset(root / 'Entities/MaterialRobot/MaterialRobot.owentity', root)
        model = asset['models'][0]
        self.assertEqual(set(model['materials']), {'1', '2', '3'})
        self.assertEqual(len(asset['textures']), 10)
        self.assertTrue(all(mesh['materialKey'] in model['materials'] for mesh in model['meshes']))
        self.assertTrue(all(Path(operation['path']).is_relative_to(root) for operation in asset['textures'].values()))
        self.assertFalse(any('missing' in warning or 'unresolved' in warning for warning in asset['warnings']))
        # Paths/descriptors only: images remain lazily fetched, not duplicated in JSON.
        self.assertNotIn('data:image', json.dumps(asset))
        self.assertEqual(model['materials']['1']['maps']['ao']['uvIndex'], 1)

    def test_second_uv_set_preserved_without_changing_skinning(self):
        model = load_asset(self.write('model.owmdl', textured_model()), self.root)['models'][0]
        mesh = model['meshes'][0]
        self.assertEqual(mesh['uvs'][:8], [0, 0, 1, 0, 1, 1, 0, 1])
        self.assertEqual(mesh['uvs2'][:8], [.125, .125, .875, .125, .875, .875, .125, .875])
        self.assertEqual(len(mesh['uvs']), len(mesh['uvs2']))
        self.assertEqual(mesh['skinIndices'][:4], [0, 0, 0, 0])
        self.assertEqual(mesh['skinWeights'][:4], [1, 0, 0, 0])
        self.assertEqual(model['bones'][0]['pos'], [0, 1, 0])

    def test_entity_material_look_overrides_model_header(self):
        root = generate(self.root)
        model_folder = root / 'Models/MaterialRobot.00C'
        self.write('Models/MaterialRobot.00C/ModelLooks/Alternate.owmat',
                   look_bytes([(key, '../../Materials/000000B002.owmat') for key in (1, 2, 3)]))
        blob = entity_bytes(name='Alternate', model='MaterialRobot.00C')
        index = blob.rfind(string('null'))
        entity = self.write('Entities/Alternate/Alternate.owentity',
                            blob[:index] + string('Alternate.01A') + blob[index+len(string('null')):])
        material_set = load_asset(entity, root)['models'][0]['materials']
        self.assertEqual({item['name'] for item in material_set.values()}, {'000000B002'})
        self.assertTrue(model_folder.exists())

    def test_missing_material_or_texture_keeps_geometry(self):
        root = generate(self.root)
        (root/'Models/MaterialRobot.00C/Materials/000000B002.owmat').unlink()
        (root/'Models/MaterialRobot.00C/Textures/000000A001.png').unlink()
        asset = load_asset(root/'Entities/MaterialRobot/MaterialRobot.owentity', root)
        self.assertEqual(len(asset['models'][0]['meshes']), 8)
        self.assertNotIn('2', asset['models'][0]['materials'])
        self.assertNotIn('baseColor', asset['models'][0]['materials']['1']['maps'])
        self.assertTrue(any('missing referenced file' in warning for warning in asset['warnings']))

    def test_ordinary_and_blender_full_look_filename_relative_resolution(self):
        target = self.write('Models/A/Materials/a.owmat', material_bytes())
        owner = self.root/'Models/A/ModelLooks/default.owmat'
        self.assertEqual(self.resolver.resolve(owner, '../../Materials/a.owmat', file_relative=True), target)
        texture = self.texture('Models/A/Textures/123.png')
        self.assertEqual(self.resolver.resolve(target, '..\\Textures\\123.png'), texture)

    def test_unique_exact_basename_fallback_and_ambiguous_rejection(self):
        image = self.texture('Moved/123.png')
        owner = self.root/'Material.owmat'
        self.assertEqual(self.resolver.resolve(owner, 'old/123.png'), image)
        self.texture('Another/123.png')
        resolver = MaterialResolver(owner, self.root, self.warnings)
        self.assertIsNone(resolver.resolve(owner, 'old/123.png'))
        self.assertTrue(any('ambiguous' in warning for warning in self.warnings))

    def test_datatool_same_reference_dds_fallback(self):
        target = self.write('Textures/image.dds', b'DDS fixture header not decoded here')
        owner = self.root/'Materials/material.owmat'
        self.assertEqual(self.resolver.resolve(owner, '../Textures/image.png'), target)
        self.assertTrue(any('DDS fallback' in warning for warning in self.warnings))

    def test_absolute_escape_is_never_registered(self):
        with tempfile.TemporaryDirectory() as other:
            image = Path(other)/'image.png'
            image.write_bytes(png_bytes(1, 1, lambda x, y: (0, 0, 0, 255)))
            material = self.resolver.descriptor(self.write('mat.owmat', material_bytes([(str(image), 2903569922)])))
            self.assertEqual(material['maps'], {})
            self.assertEqual(self.resolver.textures, {})
            self.assertTrue(any('outside export root' in warning for warning in self.warnings))

    def test_invalid_texture_path_warns_without_aborting_material(self):
        material = self.resolver.descriptor(self.write('mat.owmat', material_bytes([
            ('invalid\0path.png', 2903569922)])))
        self.assertEqual(material['maps'], {})
        self.assertTrue(any('invalid referenced path' in warning for warning in self.warnings))

    def test_role_hashes_drive_channels_normal_and_pbr_not_filenames(self):
        material = self.describe([2903569922, 378934698, 548341454, 3166598269])
        self.assertEqual(self.operation(material, 'baseColor')['channel'], 'rgb')
        self.assertEqual(self.operation(material, 'normal')['transform'], 'ow-normal')
        self.assertEqual(self.operation(material, 'roughness')['channel'], 'g')
        self.assertEqual(self.operation(material, 'roughness')['transform'], 'invert')
        self.assertEqual(self.operation(material, 'metalness')['channel'], 'r')
        self.assertEqual(self.operation(material, 'metalness')['transform'], 'ow-metalness')
        self.assertEqual(self.operation(material, 'emissive')['channel'], 'luminance')
        self.assertEqual(material['metalness'], 1)
        self.assertEqual(material['roughness'], 1)
        self.assertEqual(material['emissiveMode'], 'baseColorMask')
        self.assertEqual(material['emissiveIntensity'], 16)
        self.assertTrue(material['emissiveUsesAO'])
        self.assertEqual(material['alphaMode'], 'OPAQUE')
        self.assertNotIn('alpha', material['maps'])

    def test_albedo_alpha_is_ao_for_detail_not_opacity(self):
        material = self.describe([2903569922], shader=217)
        self.assertEqual(self.operation(material, 'ao')['channel'], 'a')
        self.assertEqual(material['alphaMode'], 'OPAQUE')
        self.assertNotIn('alpha', material['maps'])

    def test_explicit_hair_alpha_and_glass_alpha(self):
        hair = self.describe([1239794147], shader=51, name='hair.owmat')
        glass = self.describe([67369114, 1493127177], shader=38, name='glass.owmat')
        self.assertEqual(self.operation(hair, 'alpha')['channel'], 'a')
        self.assertEqual(hair['alphaMode'], 'MASK')
        self.assertEqual(glass['alphaMode'], 'BLEND')
        self.assertEqual(glass['emissiveIntensity'], 0)
        self.assertNotIn('emissiveMode', glass)
        self.assertEqual(glass['maps']['emissive']['colorSpace'], 'srgb')

    def test_emission_strength_and_shader_specific_metalness(self):
        for shader, inputs, strength in [(0, [], 16), (217, [], 16), (221, [], 16),
                                        (167, [], 32), (43, [(4081294361, bytes(16))], 32), (34, [], 128)]:
            with self.subTest(shader=shader):
                material = self.describe([548341454, 3166598269], inputs, shader, name=f'{shader}.owmat')
                self.assertEqual(material['emissiveIntensity'], strength)
                if shader in (167, 43, 221):
                    self.assertNotIn('metalness', material['maps'])
                    self.assertEqual(material['metalness'], 0)
                if shader == 221:
                    self.assertEqual(material['roughness'], 0)
                    self.assertNotIn('roughness', material['maps'])
                self.assertEqual(material['emissiveUsesAO'], shader in (0, 34))

    def test_complex_emission_omitted_not_guessed(self):
        for shader in (43, 44, 50, 51):
            material = self.describe([3166598269], shader=shader, name=f'{shader}.owmat')
            self.assertNotIn('emissive', material['maps'])
            self.assertEqual(material['emissive'], [0, 0, 0])
        self.assertTrue(any('layered shader emission' in warning for warning in self.warnings))

    def test_decal_alpha_red_and_static_opacity_not_wrong_global_multiplier(self):
        material = self.describe([3989656707], [(565460110, pack('f', .2))], shader=34)
        self.assertEqual(self.operation(material, 'alpha')['channel'], 'r')
        self.assertEqual(self.operation(material, 'alpha')['inputColorSpace'], 'srgb')
        self.assertEqual(material['opacity'], 1)
        self.assertTrue(any('opacity control' in warning for warning in self.warnings))

    def test_uv_selectors_are_one_based_and_basic_scale_explicit(self):
        material = self.describe([2903569922, 3166598269, 3335614873], [
            (3344068240, pack('I', 2)), (1883253226, pack('ff', 2, 3)),
            (2166182138, pack('ff', 10, 10))])
        self.assertEqual(material['maps']['emissive']['uvIndex'], 1)
        self.assertEqual(material['maps']['ao']['uvIndex'], 1)
        self.assertEqual(material['maps']['baseColor']['repeat'], [2, 3])
        self.assertEqual(material['maps']['emissive']['repeat'], [1, 1])

    def test_unsupported_uv_skips_emission_without_turning_mesh_white(self):
        material = self.describe([3166598269], [(3344068240, pack('I', 5))])
        self.assertNotIn('emissive', material['maps'])
        self.assertEqual(material['emissive'], [0, 0, 0])
        self.assertTrue(any('unsupported UV channel' in warning for warning in self.warnings))

    def test_ow2_hair_strand_maps_switch_alpha_to_uv2(self):
        material = self.describe([1239794147, 4101268840, 758934576], shader=51)
        self.assertEqual(material['maps']['alpha']['uvIndex'], 1)
        self.assertEqual(material['maps']['baseColor']['uvIndex'], 0)

    def test_repeated_image_operation_id_shared_across_materials(self):
        a = self.describe([2903569922], name='a.owmat')
        b = self.describe([2903569922], name='b.owmat')
        self.assertEqual(a['maps']['baseColor']['textureId'], b['maps']['baseColor']['textureId'])
        self.assertEqual(len(self.resolver.textures), 1)

    def test_unknown_hash_warns_and_does_not_infer_from_image_name(self):
        material = self.describe([123456])
        self.assertEqual(material['maps'], {})
        self.assertEqual(self.resolver.textures, {})
        self.assertTrue(any('123456' in warning for warning in self.warnings))

    def test_collision_classification_uses_exact_source_material_guid(self):
        self.assertTrue(is_collision_material({'sourcePath': str(self.root/'0000000007a1.owmat')}))
        self.assertFalse(is_collision_material({'name': '0000000007A1', 'shaderId': 33}))
        self.assertFalse(is_collision_material({'sourcePath': str(self.root/'My0000000007A1.owmat')}))
        self.assertFalse(is_collision_material({'sourcePath': str(self.root/'0000000007A1-copy.owmat')}))


if __name__ == '__main__':
    unittest.main()
