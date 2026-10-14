"""Model browser bone totals use headers and complete entity references only."""
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from owpreview.bone_counts import BoneCountIndex, read_model_bone_count
from owpreview.formats import load_asset
from create_material_demo import generate
from test_formats import entity_bytes, model_bytes, pack, string


class BoneCountTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def write(self, name, data):
        path = self.root/name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def index(self):
        return BoneCountIndex(self.root, self.root.rglob('*'))

    def assertCount(self, index, path, count):
        self.assertEqual(index.metadata(path), {'boneCount': count, 'boneCountError': ''})

    def assertUnavailable(self, index, path, message):
        result = index.metadata(path)
        self.assertIsNone(result['boneCount'], result)
        self.assertIn(message, result['boneCountError'])

    def test_header_stops_before_bones_geometry_and_materials(self):
        blob = model_bytes(name='Módèle 日本語' * 15)
        model = self.write('Body.owmdl', blob)
        stream = io.BytesIO(blob)
        read = stream.read
        reads = []

        def observe(size=-1):
            reads.append(size)
            return read(size)

        stream.read = observe
        with patch.object(Path, 'open', return_value=stream), \
                patch('owpreview.formats._load_model', side_effect=AssertionError('Decoded geometry')):
            self.assertEqual(read_model_bone_count(model), 2)
            consumed = sum(reads)
        header = pack('HH', 2, 0) + string('') + string('Módèle 日本語' * 15) + pack('IHII', 123, 2, 1, 0)
        self.assertEqual(consumed, len(header))
        self.assertLess(consumed, len(blob))
        self.assertTrue(all(size >= 0 for size in reads))

    def test_repeated_attachment_instances_count_and_cached_headers_are_reused(self):
        body = self.write('Body.owmdl', model_bytes())
        weapon = self.write('Weapon.owmdl', model_bytes(bones=[('root', -1, (0, 0, 0), (0, 0, 0))]))
        child = self.write('Child.owentity', entity_bytes(model='Weapon.owmdl', relative='.'))
        main = self.write('Gameplay3P.owentity', entity_bytes(model='Body.owmdl', relative='.',
                          children=[('Child.owentity', ''), ('Child.owentity', '')]))
        original = Path.read_bytes

        def no_models(path):
            if path.suffix == '.owmdl':
                raise AssertionError('Read whole model file')
            return original(path)

        with patch('owpreview.bone_counts.read_model_bone_count', wraps=read_model_bone_count) as header, \
                patch.object(Path, 'read_bytes', no_models), \
                patch('owpreview.formats._load_model', side_effect=AssertionError('Decoded geometry')):
            index = self.index()
            self.assertCount(index, main, 4)
            self.assertCount(index, child, 1)
            self.assertCount(index, body, 2)
            self.assertCount(index, weapon, 1)
        self.assertEqual(header.call_count, 2)

    def test_null_model_wrappers_and_zero_bone_models_are_known_zero_not_errors(self):
        model = self.write('Static.owmdl', model_bytes(bones=[], slots=0))
        empty = self.write('Empty.owentity', entity_bytes(model='null'))
        self.write('Child.owentity', entity_bytes(model='Body.owmdl', relative='.'))
        self.write('Body.owmdl', model_bytes())
        wrapper = self.write('Wrapper.owentity', entity_bytes(model='null', children=[('Child.owentity', '')]))
        index = self.index()
        self.assertCount(index, model, 0)
        self.assertCount(index, empty, 0)
        self.assertCount(index, wrapper, 2)

    def test_missing_references_never_return_partial_total(self):
        self.write('Body.owmdl', model_bytes())
        missing_model = self.write('NoModel.owentity', entity_bytes(model='Absent.owmdl'))
        missing_child = self.write('NoChild.owentity', entity_bytes(model='Body.owmdl', relative='.',
                                  children=[('Absent.owentity', '')]))
        index = self.index()
        self.assertUnavailable(index, missing_model, 'missing model')
        self.assertUnavailable(index, missing_child, 'missing child entity')
        self.assertUnavailable(index, self.root/'Absent.owmdl', 'Absent.owmdl')

    def test_cycle_corrupt_attachment_and_corrupt_model_headers_are_unavailable(self):
        self.write('Body.owmdl', model_bytes())
        cycle = self.write('Cycle.owentity', entity_bytes(model='Body.owmdl', relative='.', children=[('Cycle.owentity', '')]))
        self.write('Broken.owentity', b'broken entity')
        parent = self.write('Parent.owentity', entity_bytes(model='Body.owmdl', relative='.', children=[('Broken.owentity', '')]))
        self.assertUnavailable(self.index(), cycle, 'cycle')
        self.assertUnavailable(self.index(), parent, 'truncated')
        for name, blob, error in (
                ('truncated', pack('HH', 2, 0) + string(''), 'truncated'),
                ('version', model_bytes(version=(1, 0)), 'unsupported model version'),
                ('utf8', pack('HH', 2, 0) + b'\x01\xff', 'UTF-8'),
                ('meshes', pack('HH', 2, 0) + string('') + string('') + pack('IHII', 1, 2, 20000, 0), 'mesh count')):
            with self.subTest(name=name):
                path = self.write(name+'.owmdl', blob)
                self.assertUnavailable(self.index(), path, error)

    def test_limits_and_failed_deep_root_do_not_poison_valid_subtree_counts(self):
        self.write('Body.owmdl', model_bytes())
        child = self.write('Child.owentity', entity_bytes(model='Body.owmdl', relative='.'))
        broad = self.write('Broad.owentity', entity_bytes(model='Body.owmdl', relative='.',
                           children=[('Child.owentity', '')] * 512))
        for number in range(50):
            self.write(f'Depth{number}.owentity', entity_bytes(model='Body.owmdl', relative='.',
                       children=[(f'Depth{number+1}.owentity', '')] if number < 49 else []))
        index = self.index()
        self.assertUnavailable(index, broad, '512 model nodes')
        self.assertUnavailable(index, self.root/'Depth0.owentity', '48 levels')
        self.assertCount(index, self.root/'Depth10.owentity', 80)
        self.assertCount(index, child, 2)

    def test_scan_reuses_character_index_and_keeps_missing_counts_on_individual_rows(self):
        # Fallback lookup must use scan results, not another recursive root walk.
        model = self.write('Nested/Elsewhere/Body.owmdl', model_bytes())
        main = self.write('Copied/Gameplay3P.owentity', entity_bytes(model='Body.owmdl'))
        broken = self.write('Broken.owmdl', b'broken')
        api = BrowserAPI(self.root/'Profile')
        self.addCleanup(api.shutdown)
        import os
        with patch('app.os.walk', wraps=os.walk) as walk, \
                patch('app.load_asset', side_effect=AssertionError('Loaded a scene')):
            response = api.scan_root(str(self.root))
        self.assertTrue(response['ok'], response)
        data = response['data']
        self.assertEqual(walk.call_count, 1)
        self.assertEqual(data['warnings'], [])
        rows = {Path(item['path']): item for item in data['entities']}
        for path in (model, main):
            self.assertEqual(rows[path]['boneCount'], 2)
            self.assertEqual(rows[path]['boneCountError'], '')
        self.assertIsNone(rows[broken]['boneCount'])
        self.assertTrue(rows[broken]['boneCountError'])

    def test_ambiguous_reference_does_not_pick_arbitrary_skeleton(self):
        self.write('Elsewhere/A/Body.owmdl', model_bytes())
        self.write('Elsewhere/B/Body.owmdl', model_bytes(bones=[], slots=0))
        entity = self.write('Copied/Main.owentity', entity_bytes(model='Body.owmdl'))
        self.assertUnavailable(self.index(), entity, 'Ambiguous reference')

    def test_generated_material_fixture_counts_match_real_loaded_asset_stats(self):
        root = generate(self.root/'Library')
        index = BoneCountIndex(root, root.rglob('*'))
        for path in sorted(root.rglob('*')):
            if path.suffix.lower() not in ('.owmdl', '.owentity'):
                continue
            with self.subTest(path=path):
                asset = load_asset(path, root)
                self.assertCount(index, path, sum(len(model['bones']) for model in asset['models']))


if __name__ == '__main__':
    unittest.main()
