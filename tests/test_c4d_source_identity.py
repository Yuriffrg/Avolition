"""Native layout hints come from original paths while source timing is retained."""
import copy
import unittest

from owpreview.c4d_export import make_payload, make_set_payload, source_identity
from test_selected_object_export import fixture
from test_combined_export import assigned_fixture


class SourceIdentityTests(unittest.TestCase):
    def test_identity_is_path_based_and_cross_platform(self):
        for path, expected in (
                (r'C:\Exports\Heroes\Widowmaker\Entities\Gameplay1P\Gameplay1P.owentity', (True, 'Widowmaker')),
                ('/Exports/Heroes/Lúcio/Entities/Gameplay1P.owentity', (True, 'Lúcio')),
                ('//NAS/export/Heroes/士兵/Entities/Gameplay3P.owentity', (False, '士兵')),
                ('/Exports/Gameplay1P.owmdl', (False, '')),
                ('/Exports/Gameplay1P_Reload.owentity', (False, '')),
                ('/Exports/gameplay1p.OWENTITY', (True, '')),
                ('/NotHeroes/Widowmaker/Gameplay1P.owentity', (True, '')),
                ('/Heroes/outer/Exports/HEROES/Tracer/Entities/Gameplay1P.owentity', (True, 'Tracer')),
                ('/Heroes/../Gameplay1P.owentity', (True, '')),
                ('/Heroes/Gameplay1P.owentity', (True, '')),
                ('', (False, ''))):
            with self.subTest(path=path):
                self.assertEqual(source_identity(path), expected)

    def test_metadata_ignores_display_names_and_preserves_original_fps(self):
        asset, clip = fixture()
        asset['path'] = r'C:\Exports\Heroes\Widowmaker\Entities\Gameplay1P\Gameplay1P.owentity'
        asset['name'] = 'Custom character name'
        for node in asset['exportHierarchy']:
            node['name'] = 'Custom object name'
        before = copy.deepcopy(asset)
        for fps in (24, 30, 60, 29.97, 59.94):
            payload = make_payload(asset, dict(clip, fps=fps), {}, clip_name='Reload')
            self.assertTrue(payload['gameplay1P'])
            self.assertEqual(payload['heroName'], 'Widowmaker')
            self.assertEqual(payload['animation']['fps'], fps)
            self.assertEqual(payload['asset']['exportHierarchy'], asset['exportHierarchy'])
        self.assertEqual(asset, before)
        asset['path'] = '/Exports/Heroes/Widowmaker/Entities/Gameplay3P.owentity'
        asset['name'] = 'Gameplay1P'
        for node in asset['exportHierarchy']:
            node['name'] = 'Gameplay1P'
        self.assertFalse(make_payload(asset, clip, {})['gameplay1P'])

    def test_combined_metadata_keeps_maximum_source_fps_policy(self):
        asset, assignments = assigned_fixture()
        asset['path'] = '/Exports/Heroes/Widowmaker/Entities/Gameplay1P.owentity'
        payload = make_set_payload(asset, assignments, {})
        self.assertEqual((payload['gameplay1P'], payload['heroName']), (True, 'Widowmaker'))
        self.assertEqual(payload['animation']['fps'], 30)
        self.assertEqual([item['fps'] for item in payload['animation']['assignments']], [24, 30])
        self.assertEqual(payload['animation']['durationSeconds'], 1)

    def test_mixed_30_and_60_fps_preserves_seconds_and_highest_source_rate(self):
        asset, assignments = assigned_fixture()
        asset['path'] = '/Exports/Heroes/Widowmaker/Entities/Gameplay1P.owentity'
        assignments[0]['clip']['fps'], assignments[1]['clip']['fps'] = 30, 60
        animation = make_set_payload(asset, assignments, {})['animation']
        self.assertEqual((animation['fps'], animation['lastFrame'], animation['durationSeconds']), (60, 48, .8))
        self.assertEqual([item['fps'] for item in animation['assignments']], [30, 60])
        self.assertEqual([item['durationSeconds'] for item in animation['assignments']], [.8, .25])
