import copy
import unittest

from owpreview.object_names import apply_object_names, object_name_keys, validate_name, validate_names


def fixture():
    return {'name': 'Gameplay1P', 'path': 'source/Gameplay1P.owentity', 'models': [
        {'id': 'model-0', 'name': 'Source arms', 'parentId': None, 'isEntity': True,
         'entityName': 'Gameplay1P', 'path': 'source/Arms.owmdl', 'modelGuid': '00000013E',
         'bones': [{'name': 'bone_0001', 'parent': -1}],
         'meshes': [{'name': 'Submesh_0', 'materialKey': 'original-material', 'indices': [0, 1, 2]}]},
        {'id': 'model-1', 'name': 'Source weapon', 'parentId': 'model-0', 'isEntity': True,
         'entityName': 'Gun', 'attachmentBone': 'bone_0001', 'path': 'source/Gun.owmdl',
         'bones': [{'name': 'bone_0001', 'parent': -1}], 'meshes': [{'name': 'Submesh_0'}]},
    ]}


class ObjectNameTests(unittest.TestCase):
    def test_filtered_map_mesh_names_keep_the_original_mesh_index(self):
        asset = {'map': {}, 'models': [{'id':'map-0-model-0', 'meshPrototype':'p'}],
                 'meshPrototypes': {'p': {'meshes':[{'name':'Visible', 'sourceMeshIndex':2}]}},
                 'exportHierarchy':[{'type':'mesh', 'modelId':'map-0-model-0', 'meshIndex':0,
                                     'sourceMeshIndex':2, 'name':'Visible'}]}
        self.assertIn('mesh:map-0-model-0:2', object_name_keys(asset))
        self.assertNotIn('mesh:map-0-model-0:0', object_name_keys(asset))
        renamed = apply_object_names(asset, {'mesh:map-0-model-0:2':'Facade'})
        self.assertEqual(renamed['models'][0]['meshNames'], {'0':'Facade'})
        self.assertEqual(renamed['exportHierarchy'][0]['name'], 'Facade')
        self.assertEqual(asset['meshPrototypes']['p']['meshes'][0]['name'], 'Visible')

    def test_names_are_trimmed_unicode_display_values_not_filenames(self):
        self.assertEqual(validate_name('  Arms / modèle: 日本語 😀  '), 'Arms / modèle: 日本語 😀')
        self.assertEqual(validate_name('a' * 160), 'a' * 160)
        self.assertEqual(validate_name('😀' * 80), '😀' * 80)

    def test_invalid_names_rejected(self):
        for value in (None, 42, '', '   ', 'a\n', '\tArms', 'a\0b', 'a\x7fb', 'a\x85b', '\ud800',
                      'a' * 161, '😀' * 81):
            with self.subTest(value=repr(value)), self.assertRaises(ValueError):
                validate_name(value)

    def test_stable_keys_preserve_bones_and_source_identifiers(self):
        asset = fixture()
        self.assertEqual(object_name_keys(asset), {'scene', 'model:model-0', 'mesh:model-0:0',
                                                   'model:model-1', 'mesh:model-1:0'})
        for key in ('bone:model-0:0', 'skeleton:model-0', 'model:missing', 'mesh:model-0:99', '__proto__'):
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate_names({key: 'New'}, asset)
        self.assertEqual(validate_names(None, asset), {})
        with self.assertRaises(ValueError):
            validate_names(['invalid'], asset)

    def test_applies_to_export_copy_without_changing_source_or_bindings(self):
        asset = fixture()
        original = copy.deepcopy(asset)
        changed = apply_object_names(asset, {'scene': 'Reload setup', 'model:model-0': 'Arms',
                                            'mesh:model-1:0': 'Weapon shell'})
        self.assertEqual(asset, original)
        self.assertEqual(changed['name'], 'Reload setup')
        self.assertEqual(changed['models'][0]['entityName'], 'Reload setup')
        self.assertEqual(changed['models'][0]['name'], 'Arms')
        self.assertEqual(changed['models'][1]['meshes'][0]['name'], 'Weapon shell')
        for before, after in zip(original['models'], changed['models']):
            for key in ('id', 'parentId', 'path', 'bones', 'modelGuid', 'attachmentBone'):
                self.assertEqual(before.get(key), after.get(key), key)
        changed['models'][0]['meshes'][0]['indices'].append(99)
        self.assertEqual(asset, original, 'The export copy does not share nested mutable source data.')

    def test_standalone_scene_names_root_model_and_specific_model_wins(self):
        asset = fixture()
        asset['models'] = [asset['models'][0]]
        asset['models'][0]['isEntity'] = False
        changed = apply_object_names(asset, {'scene': 'New scene'})
        self.assertEqual(changed['models'][0]['name'], 'New scene')
        changed = apply_object_names(asset, {'scene': 'New scene', 'model:model-0': 'Specific model'})
        self.assertEqual(changed['name'], 'New scene')
        self.assertEqual(changed['models'][0]['name'], 'Specific model')

    def test_multiple_roots_are_not_all_renamed_to_scene(self):
        asset = fixture()
        asset['models'][1]['parentId'] = None
        changed = apply_object_names(asset, {'scene': 'Map'})
        self.assertEqual(changed['name'], 'Map')
        self.assertEqual([item['entityName'] for item in changed['models']], ['Gameplay1P', 'Gun'])

    def test_compact_map_mesh_names_are_per_instance(self):
        asset = {'name': 'Map', 'meshPrototypes': {'shared': {'meshes': [{'name': 'Wall', 'positions': [1, 2, 3]}]}},
                 'models': [{'id': 'model-0', 'name': 'Instance A', 'meshPrototype': 'shared'},
                            {'id': 'model-1', 'name': 'Instance B', 'meshPrototype': 'shared'}]}
        original = copy.deepcopy(asset)
        self.assertIn('mesh:model-1:0', object_name_keys(asset))
        changed = apply_object_names(asset, {'mesh:model-1:0': 'Wall by spawn'})
        self.assertEqual(changed['models'][1]['meshNames'], {'0': 'Wall by spawn'})
        self.assertNotIn('meshNames', changed['models'][0])
        self.assertEqual(changed['meshPrototypes'], original['meshPrototypes'])
        self.assertIs(changed['meshPrototypes'], asset['meshPrototypes'], 'Large immutable prototype arrays remain shared.')
        self.assertEqual(asset, original)
        with self.assertRaises(ValueError):
            apply_object_names(asset, {'mesh:model-1:1': 'Missing'})
        asset['models'][0]['meshPrototype'] = 'missing'
        with self.assertRaisesRegex(ValueError, 'missing mesh prototype'):
            object_name_keys(asset)

    def test_map_scene_name_preserves_single_instance_wrapper_and_geometry_arrays(self):
        asset = fixture()
        asset['models'] = [asset['models'][0]]
        asset['map'] = {'instanceCount': 1}
        asset['exportHierarchy'] = [{'id': 'instance', 'type': 'entity', 'modelId': 'model-0',
                                     'parentId': None, 'name': 'Source instance'}]
        changed = apply_object_names(asset, {'scene': 'Map label', 'mesh:model-0:0': 'Wall'})
        self.assertEqual(changed['name'], 'Map label')
        self.assertEqual(changed['models'][0]['entityName'], 'Gameplay1P')
        self.assertEqual(changed['exportHierarchy'][0]['name'], 'Source instance')
        self.assertEqual(changed['models'][0]['meshes'][0]['name'], 'Wall')
        self.assertEqual(asset['models'][0]['meshes'][0]['name'], 'Submesh_0')
        self.assertIs(changed['models'][0]['meshes'][0]['indices'], asset['models'][0]['meshes'][0]['indices'])
        self.assertIsNot(changed['exportHierarchy'][0], asset['exportHierarchy'][0])

    def test_export_graph_names_change_without_changing_identifiers_or_transforms(self):
        asset = fixture()
        asset['exportHierarchy'] = [
            {'id': 'model-0:entity', 'type': 'entity', 'modelId': 'model-0', 'parentId': None, 'name': 'Gameplay1P'},
            {'id': 'model-0:model', 'type': 'model', 'modelId': 'model-0', 'parentId': 'model-0:entity', 'name': 'Source arms'},
            {'id': 'model-0:bone:0', 'type': 'bone', 'modelId': 'model-0', 'parentId': 'model-0:model',
             'name': 'bone_0001', 'sourceName': 'bone_0001', 'matrix': list(range(16))},
            {'id': 'model-1:mesh:0', 'type': 'mesh', 'modelId': 'model-1', 'meshIndex': 0,
             'parentId': 'model-0:bone:0', 'name': 'Submesh_0', 'matrix': list(range(16))},
        ]
        original = copy.deepcopy(asset)
        changed = apply_object_names(asset, {'scene': 'Named scene', 'model:model-0': 'Named model',
                                            'mesh:model-1:0': 'Named mesh'})
        self.assertEqual([node['name'] for node in changed['exportHierarchy']],
                         ['Named scene', 'Named model', 'bone_0001', 'Named mesh'])
        for before, after in zip(original['exportHierarchy'], changed['exportHierarchy']):
            self.assertEqual({key: value for key, value in before.items() if key != 'name'},
                             {key: value for key, value in after.items() if key != 'name'})
        self.assertEqual(asset, original)


if __name__ == '__main__':
    unittest.main()
