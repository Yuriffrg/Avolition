"""Native map scene contract; mocked bridge/Cinema API, not a real C4D save."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from owpreview.c4d_scene import _asset_models, _matrix, _ordered_graph, build_scene
from owpreview.map_export import export_map, make_static_payload
from owpreview.map_formats import expand_map_asset, load_map
from owpreview.object_names import apply_object_names
from test_map_formats import write_fixture
from test_c4d_scene import Document, make_api


class MockBridge:
    """Only the external native save is simulated; staging/publication are real."""
    def __init__(self, *, connected=True, capability=True, failure=None):
        self.connected, self.capability, self.failure = connected, capability, failure
        self.calls = []

    def status(self):
        return dict(connected=self.connected, octane=True, mapInstances=self.capability,
                    bridgeVersion='0.7.4' if self.capability else '0.7.2',
                    message='Cinema 4D is disconnected.' if not self.connected else 'Ready')

    def export(self, payload, destination, texture_folder):
        self.calls.append(copy.deepcopy(payload))
        if self.failure:
            raise RuntimeError(self.failure)
        Path(destination).write_bytes(b'MOCK C4D SCENE; NOT A NATIVE FILE')
        return {'warnings': []}


class MapExportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.asset = load_map(write_fixture(self.root / 'source'))

    def tearDown(self):
        self.temp.cleanup()

    def test_static_payload_is_compact_has_no_animation_and_preserves_source(self):
        before = copy.deepcopy(self.asset)
        payload = make_static_payload(self.asset, name='Map name')
        self.assertEqual(payload['animation'], {'fps': 30, 'lastFrame': 0, 'tracks': []})
        self.assertTrue(payload['mapInstances'])
        self.assertEqual(len(payload['asset']['meshPrototypes']), 3)
        self.assertTrue(all('meshes' not in model for model in payload['asset']['models']))
        self.assertEqual(self.asset, before)
        expanded = dict(payload, asset=expand_map_asset(payload['asset']))
        self.assertLess(len(json.dumps(payload)), len(json.dumps(expanded)))

    def test_compact_native_lookup_applies_only_individual_mesh_alias(self):
        self.asset['models'][0]['meshNames'] = {'0': 'First instance only'}
        models = _asset_models(make_static_payload(self.asset)['asset'])
        first, second = list(models.values())[:2]
        self.assertEqual(first['meshes'][0]['name'], 'First instance only')
        self.assertEqual(second['meshes'][0]['name'], 'Triangle')
        self.assertIs(first['meshes'][0]['positions'], second['meshes'][0]['positions'])
        self.asset['models'][0]['meshPrototype'] = 'missing'
        with self.assertRaisesRegex(ValueError, 'missing mesh prototype'):
            make_static_payload(self.asset)

    def test_nonuniform_scale_and_attachments_reach_native_centimetre_transform(self):
        payload = make_static_payload(self.asset)
        nodes = {node['id']: node for node in payload['asset']['exportHierarchy']}
        placed = nodes['map-2-model-0:model']
        native = _matrix(make_api(), placed['matrix'])
        self.assertEqual(native.off, (-100, 200, -300))
        self.assertAlmostEqual(native.v1[1], -2, places=6)
        self.assertAlmostEqual(native.v2[0], -3, places=6)
        self.assertAlmostEqual(native.v3[2], 4, places=6)
        child = nodes['map-3-model-1:entity']
        self.assertEqual(child['parentId'], 'map-3-model-0:bone:1')
        self.assertEqual(child['matrix'], self.asset['models'][-1]['attachmentMatrix'])

    def test_fake_native_scene_retains_all_instances_and_no_animation_tracks(self):
        api = make_api()
        payload = make_static_payload(self.asset)
        with patch.dict(sys.modules, {'c4d': api, 'c4d.modules': api.modules}):
            result = build_scene(payload, self.root / 'mock-native.c4d')
        self.assertEqual(result['meshCount'], 5)
        self.assertEqual(result['boneCount'], 2)
        self.assertEqual(result['trackCount'], 0)
        self.assertEqual(result['frameCount'], 1)
        self.assertEqual(len(api.saved), 1)
        self.assertEqual(len(api.saved[0].roots), 4)
        self.assertEqual(len(api.killed), 2)  # Detached created and verification docs only.

    def test_native_map_reuses_materials_and_evaluates_static_scene_once_per_document(self):
        api = make_api()
        payload = make_static_payload(self.asset)
        descriptor = {'name': 'Shared map surface', 'baseColor': [.2, .4, .6], 'roughness': .7}
        payload['materials'] = {mid: {str(mesh['materialKey']): copy.deepcopy(descriptor)
                                     for mesh in model['meshes']}
                                for mid, model in _asset_models(payload['asset']).items()}
        evaluations = []
        evaluate = Document.ExecutePasses
        def observe(document, *args):
            evaluations.append((id(document), document.time))
            return evaluate(document, *args)
        with patch.dict(sys.modules, {'c4d': api, 'c4d.modules': api.modules}), \
             patch.object(Document, 'ExecutePasses', observe):
            result = build_scene(payload, self.root/'shared-materials.c4d')
        self.assertTrue(result['verifiedNativeScene'])
        self.assertEqual(result['meshCount'], 5)
        self.assertEqual(result['materialCount'], 1)
        doc = api.saved[0]
        self.assertEqual(len(doc.materials), 1)
        assigned = []
        pending = list(doc.roots)
        while pending:
            obj = pending.pop()
            pending.extend(obj.children)
            assigned.extend(tag.values[api.TEXTURETAG_MATERIAL] for tag in obj.tags
                            if api.TEXTURETAG_MATERIAL in tag.values)
        self.assertEqual(len(assigned), 5)
        self.assertTrue(all(material is doc.materials[0] for material in assigned))
        self.assertEqual([frame for _document, frame in evaluations], [0, 0])
        self.assertEqual(len({document for document, _frame in evaluations}), 2,
                         'Both the original and reopened scene still require evaluation and verification.')

    def test_export_stage_publishes_one_c4d_and_preserves_existing_file(self):
        output = self.root / 'sortie café'
        output.mkdir()
        original = output / 'Máp.c4d'
        original.write_bytes(b'EXISTING USER SCENE')
        bridge = MockBridge()
        result = export_map(self.asset, output, 'Máp', bake_textures=False, bridge=bridge)
        self.assertEqual(original.read_bytes(), b'EXISTING USER SCENE')
        self.assertEqual(Path(result['c4dPath']).name, 'Máp (2).c4d')
        self.assertEqual(len(result['files']), 1)
        self.assertFalse(any(output.glob('*.owanimclip')))
        self.assertFalse(any(output.glob('.whitehound-export-*')))
        self.assertEqual(bridge.calls[0]['animation']['tracks'], [])

    def test_old_bridge_and_disconnection_fail_before_output_creation(self):
        for bridge, message in ((MockBridge(capability=False), r'bridge 0\.7\.2.*Avolition C4D Bridge 0\.7\.5'),
                                (MockBridge(connected=False), 'disconnected')):
            output = self.root / 'never-created'
            with patch('owpreview.map_export.bake_materials') as bake:
                with self.subTest(message=message), self.assertRaisesRegex(RuntimeError, message):
                    export_map(self.asset, output, 'Map', bridge=bridge)
                bake.assert_not_called()
            self.assertFalse(output.exists())
            self.assertEqual(bridge.calls, [])

    def test_failed_native_export_publishes_nothing_and_keeps_existing_files(self):
        output = self.root / 'output'
        output.mkdir()
        (output / 'existing.c4d').write_bytes(b'keep')
        with self.assertRaisesRegex(RuntimeError, 'Native save failed'):
            export_map(self.asset, output, 'Map', bridge=MockBridge(failure='Native save failed'))
        self.assertEqual([path.name for path in output.iterdir()], ['existing.c4d'])
        self.assertEqual((output / 'existing.c4d').read_bytes(), b'keep')

    def test_texture_baking_deduplicates_reused_appearances(self):
        from owpreview.export_textures import bake_materials
        bridge = MockBridge()
        with patch('owpreview.map_export.bake_materials', wraps=bake_materials) as bake:
            export_map(self.asset, self.root / 'output', 'Map', bridge=bridge)
        self.assertEqual(bake.call_count, 1)
        self.assertEqual(len(bake.call_args.args[0]['models']), 4)  # Five objects; one look used twice.
        self.assertEqual(len(bridge.calls[0]['materials']), 5)

    def test_names_and_source_data_are_kept_separate(self):
        renamed = apply_object_names(self.asset, {'model:map-0-model-0': 'Gate',
                                                 'mesh:map-0-model-0:0': 'Gate panel'})
        bridge = MockBridge()
        export_map(renamed, self.root / 'output', 'Named map', bake_textures=False, bridge=bridge)
        model = bridge.calls[0]['asset']['models'][0]
        self.assertEqual(model['name'], 'Gate')
        self.assertEqual(model['meshNames']['0'], 'Gate panel')
        self.assertEqual(self.asset['models'][0]['name'], 'Prop')
        self.assertNotIn('meshNames', self.asset['models'][0])

    def test_invalid_file_names_zero_scale_and_shear_cannot_publish(self):
        for name in ('', '..', '../map', 'C:\\map', 'NUL', 'trail.', 'a' * 181):
            with self.subTest(name=name), self.assertRaises(ValueError):
                export_map(self.asset, self.root / 'out', name, bridge=MockBridge())
        for value in (0, .4):
            asset = copy.deepcopy(self.asset)
            matrix = asset['exportHierarchy'][0]['matrix']
            matrix[0 if value == 0 else 1] = value
            with self.assertRaises(ValueError):
                make_static_payload(asset)

    def test_large_graph_ordering_keeps_parents_first(self):
        graph = [{'id': f'n-{i}', 'parentId': f'n-{i-1}' if i else None,
                  'name': str(i)} for i in range(12_000)]
        ordered = _ordered_graph(list(reversed(graph)))
        self.assertEqual([node['id'] for node in ordered], [node['id'] for node in graph])


if __name__ == '__main__':
    unittest.main()
