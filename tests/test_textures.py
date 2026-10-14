import base64
from io import BytesIO
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from PIL import Image
from owpreview.textures import TextureStore, process_channels


def unpack(result):
    with Image.open(BytesIO(base64.b64decode(result['dataUri'].split(',', 1)[1]))) as image:
        return image.convert('RGBA')


class TextureTests(unittest.TestCase):
    def test_model_browser_replacement_and_release_preserve_scene_owners_and_caches(self):
        with tempfile.TemporaryDirectory() as folder:
            paths = [Path(folder)/f'{name}.png' for name in ('animation', 'map', 'preview', 'replacement')]
            for path in paths:
                Image.new('RGB', (2, 2), (20, 40, 60)).save(path)
            specs = [{'path': str(path), 'channel': 'rgb'} for path in paths]
            store = TextureStore()
            store.register({'animation': specs[0], 'shared': specs[0]}, owner='animations')
            store.register({'map': specs[1]}, owner='maps')
            animation, map_image, shared = (store.load(key) for key in ('animation', 'map', 'shared'))
            store.register({'preview': specs[2], 'shared': specs[0]}, owner='model-browser')
            self.assertEqual(store.load('preview')['width'], 2)
            store.register({'replacement': specs[3], 'shared': specs[0]}, owner='model-browser')
            with self.assertRaises(ValueError): store.load('preview')
            self.assertEqual(store.load('replacement')['width'], 2)
            self.assertIs(store.load('animation'), animation)
            self.assertIs(store.load('map'), map_image)
            self.assertIs(store.load('shared'), shared)
            store.release('model-browser')
            store.release('model-browser')  # Closing an already closed browser is harmless.
            with self.assertRaises(ValueError): store.load('replacement')
            self.assertIs(store.load('animation'), animation)
            self.assertIs(store.load('map'), map_image)
            self.assertIs(store.load('shared'), shared)
            self.assertEqual(set(store._owners), {'animations', 'maps'})
            self.assertEqual(store._image_bytes, 2 * 2 * 4 * 2)
            self.assertEqual(store._result_bytes, sum(len(value['dataUri']) for value in (animation, map_image, shared)))

    def test_map_preview_size_is_separate_from_character_cache_and_source(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'color.png'
            Image.new('RGBA', (256, 128), (240, 180, 60, 0)).save(path)
            original = path.read_bytes()
            store = TextureStore()
            store.register({'color': {'path': str(path), 'channel': 'rgb'}})
            small = store.load('color', 64)
            full = store.load('color')
            self.assertEqual((small['width'], small['height']), (64, 32))
            self.assertEqual((full['width'], full['height']), (256, 128))
            self.assertIs(store.load('color', 64), small)
            self.assertEqual(unpack(small).getpixel((0, 0)), (240, 180, 60, 255))
            self.assertEqual(path.read_bytes(), original)
            for edge in (True, 0, 31, 2049, '128', 64.5):
                with self.subTest(edge=edge), self.assertRaises(ValueError): store.load('color', edge)

    def test_map_and_animation_texture_registrations_remain_independent(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'color.png'
            Image.new('RGB', (2, 2), (255, 0, 0)).save(path)
            spec = {'path': str(path), 'channel': 'rgb'}
            store = TextureStore()
            store.register({'map': spec}, owner='maps')
            store.register({'character': spec}, owner='animations')
            self.assertEqual(store.load('map')['width'], 2)
            self.assertEqual(store.load('character')['width'], 2)
            store.register({'new_map': spec}, owner='maps')
            with self.assertRaises(ValueError): store.load('map')
            self.assertEqual(store.load('character')['width'], 2)
            self.assertEqual(store.load('new_map')['width'], 2)

    def test_exact_packed_pbr_scalar_channels(self):
        image = Image.new('RGBA', (1, 1), (153, 51, 90, 17))
        rough = process_channels(image, {'channel': 'g', 'transform': 'invert'})
        metal = process_channels(image, {'channel': 'r', 'transform': 'ow-metalness'})
        self.assertEqual(rough.getpixel((0, 0)), (204, 204, 204))
        self.assertTrue(all(abs(v-51) <= 1 for v in metal.getpixel((0, 0))))

    def test_metalness_threshold_and_full_range(self):
        image = Image.new('RGBA', (4, 1))
        image.putdata([(0, 0, 0, 255), (133, 0, 0, 255), (134, 0, 0, 255), (255, 0, 0, 255)])
        values = list(process_channels(image, {'channel': 'r', 'transform': 'ow-metalness'}).get_flattened_data())
        self.assertEqual(values[0], (0, 0, 0))
        self.assertEqual(values[1], (0, 0, 0))
        self.assertTrue(all(12 <= v <= 14 for v in values[2]))
        self.assertEqual(values[3], (255, 255, 255))

    def test_stereographic_normal_not_conventional_rg_reconstruction(self):
        image = Image.new('RGBA', (1, 1), (191, 64, 5, 0))
        actual = process_channels(image, {'transform': 'ow-normal'}).getpixel((0, 0))
        r, g, b = 191/255, 64/255, 5/255
        f = 31.875*b+1
        x, y = (2*r-1)/f, (2*g-1)/f
        q = 2/(1+x*x+y*y)
        expected = [(q*x+1)*127.5, (q*y+1)*127.5, q*127.5]
        self.assertTrue(all(abs(a-e) <= 1 for a, e in zip(actual, expected)), (actual, expected))
        self.assertLess(actual[1], 128, 'Blender does not invert this normal Y channel')

    def test_luminance_respects_source_color_space(self):
        image = Image.new('RGBA', (1, 1), (128, 128, 128, 64))
        linear = process_channels(image, {'channel': 'luminance', 'inputColorSpace': 'linear'})
        srgb = process_channels(image, {'channel': 'luminance', 'inputColorSpace': 'srgb'})
        alpha = process_channels(image, {'channel': 'a', 'inputColorSpace': 'srgb'})
        red = process_channels(image, {'channel': 'r', 'inputColorSpace': 'srgb'})
        self.assertTrue(abs(linear.getpixel((0, 0))[0]-128) <= 1)
        self.assertTrue(abs(srgb.getpixel((0, 0))[0]-55) <= 1)
        self.assertEqual(alpha.getpixel((0, 0)), (64, 64, 64))
        self.assertTrue(abs(red.getpixel((0, 0))[0]-55) <= 1)

    def test_png_tga_and_dds_decode_preserve_pixels_and_originals(self):
        with tempfile.TemporaryDirectory() as folder:
            for extension in ('png', 'tga', 'dds'):
                with self.subTest(extension=extension):
                    path = Path(folder)/('texture.'+extension)
                    image = Image.new('RGBA', (2, 2))
                    image.putdata([(255, 0, 0, 128), (0, 255, 0, 255), (0, 0, 255, 0), (15, 27, 39, 255)])
                    image.save(path)
                    original = path.read_bytes()
                    store = TextureStore()
                    store.register({'known': {'path': str(path), 'channel': 'rgba'}})
                    actual = unpack(store.load('known'))
                    self.assertEqual(list(actual.get_flattened_data()), list(image.get_flattened_data()))
                    self.assertEqual(path.read_bytes(), original)

    def test_unregistered_path_and_stale_identifier_rejected(self):
        store = TextureStore()
        with self.assertRaises(ValueError):
            store.load('C:/private/example.png')
        store.register({'old': {'path': 'unused.png'}})
        store.register({})
        with self.assertRaises(ValueError):
            store.load('old')

    def test_decoded_cache_resets_when_asset_reloads(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'texture.png'
            Image.new('RGB', (2, 2), (255, 0, 0)).save(path)
            store = TextureStore()
            specs = {'color': {'path': str(path), 'channel': 'rgb'}}
            store.register(specs)
            first = store.load('color')
            self.assertIs(store.load('color'), first)
            Image.new('RGB', (2, 2), (0, 255, 0)).save(path)
            store.register(specs)
            self.assertEqual(unpack(store.load('color')).getpixel((0, 0)), (0, 255, 0, 255))

    def test_downsampling_does_not_use_packed_alpha_as_opacity(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'packed.png'
            Image.new('RGBA', (8, 4), (240, 180, 60, 0)).save(path)
            store = TextureStore()
            store.register({'known': {'path': str(path), 'channel': 'rgb'}})
            with patch('owpreview.textures.MAX_PREVIEW_EDGE', 2):
                result = store.load('known')
            self.assertEqual((result['width'], result['height']), (2, 1))
            self.assertEqual(unpack(result).getpixel((0, 0)), (240, 180, 60, 255))
            self.assertIn('resized', result['warning'])


if __name__ == '__main__':
    unittest.main()
