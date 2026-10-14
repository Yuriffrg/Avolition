"""Exercise object-scoped output through the actual parser and export API."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from create_material_demo import look_bytes, material_bytes, png_bytes
from owpreview.formats import load_asset
from test_formats import clip_bytes, entity_bytes, model_bytes, pack, string


class SelectedObjectAPITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.root = self.base / 'Source'
        self.root.mkdir()
        for label, guid, color in [('Arms', '00000000013E', (180, 40, 70, 255)),
                                   ('Weapon', '0000000001D4', (40, 150, 220, 255))]:
            model = model_bytes(name=guid + '.00C')
            # Replace the fixture's empty material-look reference with a real one.
            (self.root / f'{label}.owmdl').write_bytes(model[:4] + string(label + '-look.owmat') + model[5:])
            (self.root / f'{label}-look.owmat').write_bytes(look_bytes([(4000, label + '-material.owmat')]))
            (self.root / f'{label}-material.owmat').write_bytes(material_bytes([(label + '.png', 2903569922)]))
            (self.root / f'{label}.png').write_bytes(png_bytes(2, 2, lambda _x, _y: color))
        self.entity = self.root / 'Gameplay1P.owentity'
        self.entity.write_bytes(entity_bytes(name='Gameplay1P', model='Arms.owmdl', relative='.',
                                             children=[('Weapon.owentity', 'bone_0002')]))
        (self.root / 'Weapon.owentity').write_bytes(entity_bytes(name='Weapon', model='Weapon.owmdl', relative='.'))
        self.source = self.root / 'source.owanimclip'
        self.source.write_bytes(clip_bytes())
        self.api = BrowserAPI(self.base / 'Profile')
        self.assertTrue(self.api.scan_root(str(self.root))['ok'])
        self.output = self.base / 'Output'
        self.asset = load_asset(self.entity, self.root)
        self.assertEqual([model['name'] for model in self.asset['models']],
                         ['00000000013E.00C', '0000000001D4.00C'])
        self.options = dict(clipEnabled=True, enabled=False, c4dEnabled=True, includeMesh=True,
                            bakeTextures=True, rootMotion=True, entityPath=str(self.entity),
                            rootPath=str(self.root), selectedModelId='model-0', rigId='auto')
        self.captured = []
        status_patch = patch.object(self.api._c4d_bridge, 'status', return_value={'connected': True, 'octane': True})
        export_patch = patch.object(self.api._c4d_bridge, 'export', side_effect=self.capture)
        self.bridge_status = status_patch.start()
        self.bridge_export = export_patch.start()
        self.addCleanup(status_patch.stop)
        self.addCleanup(export_patch.stop)

    def capture(self, payload, destination, texture_folder):
        self.captured.append(dict(payload=copy.deepcopy(payload), textures={path.name: path.read_bytes()
                              for path in texture_folder.glob('*.png')}))
        # This stands in for Cinema's writer, not its shader/animation evaluation.
        destination.write_bytes(json.dumps(payload, sort_keys=True).encode('utf-8'))
        return {'warnings': []}

    def export(self, options=None, nickname='reload'):
        result = self.api.export_animation(str(self.source), nickname, str(self.output),
                                           self.options if options is None else options)
        self.assertTrue(result['ok'], result)
        return result['data']

    def test_c4d_and_original_clip_use_model_prefix_and_only_selected_materials(self):
        before = {path.name: path.read_bytes() for path in self.root.iterdir()}
        result = self.export()
        self.assertEqual(Path(result['clipPath']).name, '00000000013E_reload.owanimclip')
        self.assertEqual(Path(result['c4dPath']).name, '00000000013E_reload.c4d')
        self.assertEqual(Path(result['clipPath']).read_bytes(), self.source.read_bytes())
        payload = self.captured[0]['payload']
        self.assertEqual(payload['name'], '00000000013E_reload')
        self.assertEqual([model['id'] for model in payload['asset']['models']], ['model-0'])
        self.assertEqual({node['modelId'] for node in payload['asset']['exportHierarchy']}, {'model-0'})
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'model-0'})
        self.assertEqual(len(payload['animation']['tracks']), 2)
        self.assertEqual(set(payload['materials']), {'model-0'})
        self.assertEqual({Path(spec['path']).name for spec in payload['asset']['textures'].values()}, {'Arms.png'})
        self.assertEqual(result['textureCount'], 1)
        self.assertEqual(set(path.name for path in (self.output / 'tex').iterdir()), set(self.captured[0]['textures']))
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.root.iterdir()})

    def test_same_clip_exports_independent_objects_without_filename_collision(self):
        arms = self.export()
        weapon = self.export(dict(self.options, selectedModelId='model-1', rigId='model-0'))
        self.assertEqual(Path(arms['c4dPath']).name, '00000000013E_reload.c4d')
        self.assertEqual(Path(weapon['c4dPath']).name, '0000000001D4_reload.c4d')
        self.assertEqual(Path(weapon['clipPath']).name, '0000000001D4_reload.owanimclip')
        self.assertEqual(Path(arms['clipPath']).read_bytes(), Path(weapon['clipPath']).read_bytes())
        payload = self.captured[-1]['payload']
        self.assertEqual([model['id'] for model in payload['asset']['models']], ['model-1'])
        self.assertEqual({node['modelId'] for node in payload['asset']['exportHierarchy']}, {'model-1'})
        self.assertEqual({track['modelId'] for track in payload['animation']['tracks']}, {'model-1'})
        self.assertEqual(set(payload['materials']), {'model-1'})
        self.assertEqual({Path(spec['path']).name for spec in payload['asset']['textures'].values()}, {'Weapon.png'})
        self.assertEqual(payload['asset']['models'][0]['bones'], self.asset['models'][1]['bones'])

    def test_raw_clip_only_has_prefix_without_bridge_or_textures(self):
        result = self.export(dict(self.options, c4dEnabled=False, selectedModelId='model-1'))
        self.assertEqual(Path(result['path']).name, '0000000001D4_reload.owanimclip')
        self.assertEqual(Path(result['path']).read_bytes(), self.source.read_bytes())
        self.assertEqual(result['textureCount'], 0)
        self.assertFalse((self.output / 'tex').exists())
        self.assertEqual(list(self.output.iterdir()), [Path(result['path'])])
        self.bridge_status.assert_not_called()
        self.bridge_export.assert_not_called()

    def test_c4d_only_does_not_publish_a_raw_clip(self):
        result = self.export(dict(self.options, clipEnabled=False, bakeTextures=False))
        self.assertEqual(Path(result['path']).name, '00000000013E_reload.c4d')
        self.assertEqual(list(self.output.iterdir()), [Path(result['path'])])
        self.assertNotIn('clipPath', result)
        self.assertEqual(result['textureCount'], 0)
        self.assertEqual(set(self.captured[0]['payload']['materials']), {'model-0'})
        for material in self.captured[0]['payload']['materials']['model-0'].values():
            self.assertEqual(material['maps'], {})

    def test_legacy_unscoped_export_keeps_old_filename_and_whole_entity(self):
        options = dict(self.options)
        options.pop('selectedModelId')
        result = self.export(options)
        self.assertEqual(Path(result['clipPath']).name, 'reload.owanimclip')
        self.assertEqual(Path(result['c4dPath']).name, 'reload.c4d')
        payload = self.captured[0]['payload']
        self.assertEqual(len(payload['asset']['models']), 2)
        self.assertEqual(payload['asset']['exportHierarchy'], self.asset['exportHierarchy'])
        self.assertEqual(set(payload['materials']), {'model-0', 'model-1'})
        self.assertEqual(result['textureCount'], 2)

    def test_invalid_object_cannot_fall_back_to_whole_entity_or_publish_raw_copy(self):
        for c4d_enabled in (False, True):
            with self.subTest(c4d_enabled=c4d_enabled):
                result = self.api.export_animation(str(self.source), 'reload', str(self.output),
                    dict(self.options, selectedModelId='missing', c4dEnabled=c4d_enabled))
                self.assertFalse(result['ok'], result)
                self.assertIn('valid object', result['error'])
                self.assertFalse(self.output.exists())
        self.bridge_export.assert_not_called()

    def test_captured_object_is_independent_of_later_viewport_load(self):
        self.assertTrue(self.api.load_entity(str(self.root / 'Weapon.owmdl'))['ok'])
        result = self.export()
        self.assertEqual(Path(result['c4dPath']).name, '00000000013E_reload.c4d')
        self.assertEqual(self.captured[0]['payload']['asset']['models'][0]['name'], '00000000013E.00C')


if __name__ == '__main__':
    unittest.main()
