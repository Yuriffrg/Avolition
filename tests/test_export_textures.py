"""Portable map export: pixels, UV honesty, resolution and immutable sources."""

import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from PIL import Image

from owpreview.export_textures import bake_materials
from owpreview.textures import process_channels


def linear(value):
    value /= 255
    return value/12.92 if value <= .04045 else ((value+.055)/1.055)**2.4


def srgb(value):
    return round(255*(12.92*value if value <= .0031308 else 1.055*value**(1/2.4)-.055))


class ExportTextureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.output = self.root/'Output/tex'
        self.asset = {'path': str(self.root/'model.owmdl'), 'textureRoot': str(self.root),
                      'textures': {}, 'models': [{'id': 'model-0', 'name': 'Example', 'materials': {}}], 'warnings': []}
        self.material = {'name': 'Surface', 'baseColor': [1, 1, 1], 'roughness': 1, 'metalness': 1,
                         'emissive': [1, 1, 1], 'emissiveIntensity': 1, 'alphaMode': 'OPAQUE',
                         'opacity': 1, 'maps': {}}
        self.asset['models'][0]['materials']['9000'] = self.material

    def tearDown(self):
        self.temp.cleanup()

    def image(self, role, pixels=(128, 128, 128, 255), size=(1, 1), channel='rgb',
              transform='none', uv=0, repeat=(1, 1), space='linear', input_space='linear'):
        path = self.root/(role+'.png')
        image = Image.new('RGBA', size, pixels)
        image.save(path)
        image.close()
        self.asset['textures'][role] = {'path': str(path), 'channel': channel, 'transform': transform,
                                        'inputColorSpace': input_space}
        self.material['maps'][role] = {'textureId': role, 'uvIndex': uv, 'repeat': list(repeat), 'colorSpace': space}
        return path

    def bake(self, prefix=''):
        self.result = bake_materials(self.asset, self.output, prefix)
        return self.result['materials']['model-0']['9000']

    def pixel(self, material, role):
        with Image.open(material['maps'][role]['path']) as image:
            return image.convert('RGB').getpixel((0, 0))

    def test_basecolor_ao_bakes_in_linear_light_without_lighting(self):
        self.image('baseColor', space='srgb')
        self.image('ao', channel='r')
        material = self.bake()
        expected = srgb(linear(128)*(128/255))
        self.assertEqual(self.pixel(material, 'baseColor'), (expected,)*3)
        self.assertNotIn('ao', material['maps'])
        self.assertEqual(material['baseColor'], [1, 1, 1])
        self.assertEqual(material['maps']['baseColor']['colorSpace'], 'srgb')

    def test_constant_basecolor_and_ao_create_portable_color_texture(self):
        self.material['baseColor'] = [.5, .25, 0]
        self.image('ao', channel='r', uv=1)
        material = self.bake()
        self.assertEqual(self.pixel(material, 'baseColor'), tuple(srgb(c*128/255) for c in [.5, .25, 0]))
        self.assertEqual(material['maps']['baseColor']['uvIndex'], 1)
        self.assertNotIn('ao', material['maps'])

    def test_mismatched_resolutions_resample_color_in_linear_light(self):
        path = self.image('baseColor', size=(2, 1), space='srgb')
        image = Image.new('RGBA', (2, 1))
        image.putdata([(0, 0, 0, 255), (255, 255, 255, 255)])
        image.save(path)
        image.close()
        self.image('ao', (255, 255, 255, 255), size=(4, 1), channel='r')
        material = self.bake()
        with Image.open(material['maps']['baseColor']['path']) as baked:
            self.assertEqual(baked.size, (4, 1))
            self.assertEqual(baked.getpixel((1, 0)), (srgb(.25),)*3)
            self.assertEqual(baked.getpixel((2, 0)), (srgb(.75),)*3)

    def test_masked_emission_tint_ao_baked_with_hdr_strength_preserved(self):
        self.image('baseColor', (128, 64, 32, 255), space='srgb')
        self.image('ao', (128,)*3+(255,), channel='r')
        self.image('emissive', (128,)*3+(255,), channel='r')
        self.material.update(emissiveMode='baseColorMask', emissiveUsesAO=True,
                             emissiveIntensity=16, emissive=[.5, 1, 1])
        material = self.bake()
        expected = tuple(srgb(linear(c)*(128/255)**2*t) for c, t in zip([128, 64, 32], [.5, 1, 1]))
        self.assertEqual(self.pixel(material, 'emissive'), expected)
        self.assertEqual(material['emissiveIntensity'], 16)
        self.assertEqual(material['emissive'], [1, 1, 1])
        self.assertNotIn('emissiveMode', material)
        self.assertNotIn('emissiveUsesAO', material)

    def test_emission_that_ignores_ao_uses_original_unoccluded_basecolor(self):
        self.image('baseColor', space='srgb')
        self.image('ao', (0, 0, 0, 255), channel='r')
        self.image('emissive', (255, 255, 255, 255), channel='r')
        self.material.update(emissiveMode='baseColorMask', emissiveUsesAO=False)
        material = self.bake()
        self.assertEqual(self.pixel(material, 'baseColor'), (0, 0, 0))
        self.assertEqual(self.pixel(material, 'emissive'), (128, 128, 128))

    def test_color_emission_retains_radiance_above_png_range_as_factor(self):
        self.image('emissive', (255, 128, 0, 255), space='srgb')
        self.material.update(emissive=[2, 1, 1], emissiveIntensity=4)
        material = self.bake()
        self.assertEqual(material['emissiveIntensity'], 8)
        self.assertEqual(self.pixel(material, 'emissive'), (255, srgb(linear(128)/2), 0))

    def test_ao_uv_or_repeat_mismatch_retained_in_manifest_with_warning(self):
        self.image('baseColor', space='srgb', repeat=(2, 1))
        self.image('ao', (0, 0, 0, 255), channel='r', uv=1)
        material = self.bake()
        self.assertEqual(self.pixel(material, 'baseColor'), (128, 128, 128))
        self.assertEqual(material['maps']['ao']['uvIndex'], 1)
        self.assertEqual(material['maps']['baseColor']['repeat'], [2, 1])
        self.assertTrue(any('different UV' in warning for warning in self.result['warnings']))

    def test_emission_uv_mismatch_not_misrepresented_as_baked_rgb(self):
        self.image('baseColor', (255, 0, 0, 255), space='srgb')
        self.image('emissive', uv=1)
        self.material['emissiveMode'] = 'baseColorMask'
        material = self.bake()
        self.assertEqual(material['emissiveMode'], 'baseColorMask')
        self.assertEqual(material['unbakedMaps'], ['emissive'])
        self.assertEqual(material['maps']['emissive']['uvIndex'], 1)
        self.assertTrue(any('requires the source shader' in warning for warning in self.result['warnings']))

    def test_packed_pbr_normal_and_alpha_share_preview_channel_math(self):
        for role, channel, transform in [('roughness', 'g', 'invert'), ('metalness', 'r', 'ow-metalness'),
                                         ('normal', 'rgb', 'ow-normal'), ('alpha', 'a', 'none')]:
            self.image(role, (153, 51, 5, 17), channel=channel, transform=transform)
        self.material['roughness'] = .5
        material = self.bake()
        self.assertEqual(self.pixel(material, 'roughness'), (102, 102, 102))
        self.assertEqual(self.pixel(material, 'alpha'), (17, 17, 17))
        self.assertTrue(all(abs(value-51) <= 1 for value in self.pixel(material, 'metalness')))
        with Image.open(self.root/'normal.png') as image:
            expected = process_channels(image.convert('RGBA'), {'transform': 'ow-normal'})
            self.assertEqual(self.pixel(material, 'normal'), expected.getpixel((0, 0)))
            expected.close()

    def test_export_keeps_original_resolution_above_preview_limit(self):
        self.image('normal', size=(2050, 2))
        material = self.bake()
        with Image.open(material['maps']['normal']['path']) as image:
            self.assertEqual(image.size, (2050, 2))

    def test_png_references_relative_and_source_and_asset_unchanged(self):
        source = self.image('baseColor', space='srgb')
        original = source.read_bytes()
        manifest = copy.deepcopy(self.asset)
        material = self.bake('../invalid/prefix:?')
        reference = material['maps']['baseColor']
        self.assertEqual(reference['relativePath'], 'tex/'+Path(reference['path']).name)
        self.assertTrue(Path(reference['path']).is_relative_to(self.output))
        self.assertNotIn('textureId', reference)
        self.assertEqual(source.read_bytes(), original)
        self.assertEqual(self.asset, manifest)

    def test_repeat_bake_reuses_same_files_without_rewriting(self):
        self.image('baseColor', space='srgb')
        material = self.bake('walk')
        path = Path(material['maps']['baseColor']['path'])
        stamp = path.stat().st_mtime_ns
        again = self.bake('walk')
        self.assertEqual(again['maps']['baseColor']['path'], str(path))
        self.assertEqual(path.stat().st_mtime_ns, stamp)
        self.assertEqual(len(list(self.output.iterdir())), 1)

    def test_changed_source_content_gets_new_name_and_keeps_previous_export(self):
        source = self.image('baseColor', space='srgb')
        previous = Path(self.bake()['maps']['baseColor']['path'])
        previous_bytes = previous.read_bytes()
        Image.new('RGBA', (1, 1), (255, 0, 0, 255)).save(source)
        current = Path(self.bake()['maps']['baseColor']['path'])
        self.assertNotEqual(current, previous)
        self.assertEqual(previous.read_bytes(), previous_bytes)
        self.assertEqual(len(list(self.output.iterdir())), 2)

    def test_conflicting_existing_filename_is_never_overwritten(self):
        self.image('baseColor', space='srgb')
        first = Path(self.bake()['maps']['baseColor']['path'])
        first.write_bytes(b'unrelated existing data')
        second = Path(self.bake()['maps']['baseColor']['path'])
        self.assertNotEqual(first, second)
        self.assertEqual(first.read_bytes(), b'unrelated existing data')
        self.assertEqual(self.bake()['maps']['baseColor']['path'], str(second))

    def test_same_material_in_child_model_reuses_files_but_keeps_bindings(self):
        self.image('baseColor', space='srgb')
        child = copy.deepcopy(self.asset['models'][0])
        child['id'] = 'model-child'
        self.asset['models'].append(child)
        with patch('owpreview.export_textures.process_channels', wraps=process_channels) as decode:
            self.bake()
        self.assertEqual(decode.call_count, 1)
        self.assertEqual(set(self.result['materials']), {'model-0', 'model-child'})
        self.assertEqual(len(self.result['files']), 1)
        self.result['materials']['model-child']['9000']['maps']['baseColor']['repeat'][0] = 99
        self.assertEqual(self.result['materials']['model-0']['9000']['maps']['baseColor']['repeat'], [1, 1])

    def test_unregistered_or_external_source_is_omitted_and_reported(self):
        self.image('normal')
        self.asset['textures']['normal']['path'] = str(self.root.parent/'outside.png')
        self.material['maps']['baseColor'] = {'textureId': 'missing'}
        material = self.bake()
        self.assertEqual(material['maps'], {})
        self.assertEqual(self.result['files'], [])
        self.assertTrue(any('not registered' in warning for warning in self.result['warnings']))
        self.assertTrue(any('outside the export root' in warning for warning in self.result['warnings']))


if __name__ == '__main__':
    unittest.main()
