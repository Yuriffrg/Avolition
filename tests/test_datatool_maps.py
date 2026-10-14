"""Verified map argv/catalog contracts; subprocess is a harmless Python fixture."""
from copy import deepcopy
import unittest

from owpreview.datatool import DataToolError, build_list_args, build_map_extract_args, parse_map_catalog
import test_datatool as fixtures


MAPS = {
    '000000000001.09F': {'GUID': '000000000001.09F', 'Name': "King's Row", 'VariantName': 'King’s Row 日本語',
                        'MapGUID': '000000000D86.002', 'MapType': 'PVP', 'Description': 'Fixture map',
                        'StateA': 'Night', 'StateB': None, 'GameModes': [{'Name': 'Hybrid'}]},
    '000000000002.09F': {'Name': "King's Row", 'MapGUID': '000000000D87.002', 'MapType': 'PVE'},
}


class MapDataToolTests(unittest.TestCase):
    wait = fixtures.DataToolTests.wait
    wait_for_child = fixtures.DataToolTests.wait_for_child
    assert_process_ended = fixtures.DataToolTests.assert_process_ended

    def setUp(self):
        fixtures.DataToolTests.setUp(self)
        script = self.script.read_text(encoding='utf-8')
        script = script.replace('if mode == "list-heroes":',
                                'if mode == "list-maps":\n        value = ' + repr(MAPS) + '\n    elif mode == "list-heroes":')
        script = script.replace('pathlib.Path(args[2],"fixture.owanimclip")',
                                'pathlib.Path(args[2], "fixture.owmap" if mode == "extract-maps" else "fixture.owanimclip")')
        self.script.write_text(script, encoding='utf-8')

    def seed_maps(self, name=None):
        rows = deepcopy(MAPS)
        if name:
            rows['000000000001.09F']['VariantName'] = name
        self.manager._map_catalog = parse_map_catalog(rows, self.game)

    def test_list_and_extract_arguments_use_verified_modes_and_literal_map_names(self):
        listed = build_list_args(self.game, 'maps', self.output / 'Map metadata 日本語.json')
        self.assertEqual(listed, [str(self.game), 'list-maps', '--json',
                                  '--out=' + str(self.output / 'Map metadata 日本語.json'), '--online=false'])
        query = 'Watchpoint: Gibraltar:D86'
        args = build_map_extract_args(self.game, self.output, query,
                                      {'includeTextures': False, 'includeAnimations': False, 'includeSound': True, 'allLods': True})
        self.assertEqual(args[:4], [str(self.game), 'extract-maps', str(self.output), query])
        self.assertIn('--skip-textures=true', args)
        self.assertIn('--skip-animations=true', args)
        self.assertIn('--skip-sound=false', args)
        self.assertIn('--all-lods=true', args)
        for bad in ('', 'Dorado|skin=*', 'Dorado\nOther', 'Dora*do', 'Dorado" --raw=true'):
            with self.subTest(query=bad), self.assertRaises(ValueError):
                build_map_extract_args(self.game, self.output, bad, {})

    def test_real_json_subprocess_preserves_hero_catalog_and_map_metadata(self):
        self.manager.start_catalog(self.exe, self.game)
        heroes = self.wait()['catalog']
        self.manager.start_catalog(self.exe, self.game, workflow='maps')
        state = self.wait()
        self.assertEqual(state['state'], 'Completed', state)
        self.assertEqual(state['catalog'], heroes)
        self.assertEqual(state['result'], {'mapCount': 2, 'workflow': 'maps', 'install': str(self.game)})
        maps = state['mapCatalog']
        self.assertEqual(maps['install'], str(self.game))
        self.assertRegex(maps['loadedAt'], r'^\d{4}-\d{2}-\d{2}T')
        selected = next(row for row in maps['maps'] if row['id'] == '000000000001.09F')
        self.assertEqual(selected['name'], 'King’s Row 日本語')
        self.assertEqual(selected['query'], 'King’s Row 日本語:D86')
        self.assertEqual(selected['gameModes'], ['Hybrid'])
        self.assertEqual(selected['mapType'], 'PVP')
        self.assertEqual(selected['description'], 'Fixture map')
        self.assertEqual(selected['stateA'], 'Night')
        self.assertEqual(len([args for args, _ in self.calls if args[2] == 'list-maps']), 1)
        self.assertFalse(list((self.root / 'job state').glob('catalog-*')))
        self.manager.start_catalog(self.exe, self.game)
        self.assertEqual(self.wait()['mapCatalog'], maps, 'Refreshing heroes must keep map choices.')

    def test_map_id_resolves_exact_query_and_real_argv_without_shell(self):
        self.seed_maps()
        self.manager.start_export(self.exe, self.game, self.output,
                                  {'workflow': 'maps', 'mapId': '000000000001.09F', 'includeSound': True, 'allLods': True})
        result = self.wait()
        self.assertEqual(result['state'], 'Completed', result)
        self.assertTrue((self.output / 'fixture.owmap').exists())
        args, process_options = self.calls[-1]
        self.assertEqual(args[1:5], [str(self.game), 'extract-maps', str(self.output), 'King’s Row 日本語:D86'])
        self.assertFalse(process_options['shell'])
        self.assertEqual(process_options['cwd'], str(self.exe.parent))
        self.assertIn('--skip-sound=false', args)
        self.assertEqual(result['result']['mapId'], '000000000001.09F')
        self.assertEqual(result['result']['workflow'], 'maps')
        self.assertTrue(any('Execution finished' in line['text'] for line in result['logs']))

    def test_catalog_query_uses_low32_map_index_and_safe_numeric_fallback(self):
        rows = deepcopy(MAPS)
        rows['000000000001.09F'].update(VariantName='A|B', MapGUID='012300000D86.002')
        rows['000000000002.09F'].update(Name=None, VariantName=None)
        maps = parse_map_catalog(rows, self.game)['maps']
        self.assertEqual(next(row for row in maps if row['id'].endswith('1.09F'))['query'], 'D86')
        self.assertEqual(next(row for row in maps if row['id'].endswith('2.09F'))['query'], 'Title Screen:D87')
        for payload in ([], None, {}, {'bad': {}}, {'x': {'GUID': 'not-a-guid', 'MapGUID': '000000000D86.002'}}):
            with self.subTest(payload=payload), self.assertRaises(DataToolError):
                parse_map_catalog(payload, self.game)

    def test_unknown_ids_wrong_install_and_bad_workflows_fail_before_launch(self):
        for selection in (None, [], {'workflow': 'other'}, {'workflow': 'maps', 'mapId': '*'}):
            with self.subTest(selection=selection), self.assertRaises(DataToolError):
                self.manager.start_export(self.exe, self.game, self.output, selection)
        self.seed_maps()
        for ident in ('unknown', None, 'Dorado', '000000000001.09F|*'):
            with self.subTest(id=ident), self.assertRaises(DataToolError):
                self.manager.start_export(self.exe, self.game, self.output, {'workflow': 'maps', 'mapId': ident})
        self.manager._map_catalog['install'] = str(self.game / 'other')
        with self.assertRaises(DataToolError):
            self.manager.start_export(self.exe, self.game, self.output, {'workflow': 'maps', 'mapId': '*'})
        with self.assertRaises(DataToolError):
            self.manager.start_catalog(self.exe, self.game, workflow='other')
        self.assertFalse(self.calls)

    def test_all_maps_and_failure_use_same_job_state_machine(self):
        self.seed_maps()
        self.manager.start_export(self.exe, self.game, self.output, {'workflow': 'maps', 'mapId': '*'})
        self.assertEqual(self.wait()['result']['mapName'], 'All maps')
        self.assertEqual(self.calls[-1][0][4], '*')
        self.seed_maps('FailZero')
        self.manager.start_export(self.exe, self.game, self.output, {'workflow': 'maps', 'mapId': '000000000001.09F'})
        failed = self.wait()
        self.assertEqual(failed['state'], 'Failed')
        self.assertTrue(failed['error'])
        self.assertEqual(len(failed['mapCatalog']['maps']), 2)

    def test_map_cancellation_stops_real_fixture_child_and_keeps_catalog(self):
        self.seed_maps('Wait')
        self.manager.start_export(self.exe, self.game, self.output, {'workflow': 'maps', 'mapId': '000000000001.09F'})
        child = self.wait_for_child()
        self.manager.cancel()
        self.assertEqual(self.wait()['state'], 'Cancelled')
        self.assert_process_ended(child)
        self.assertEqual(len(self.manager.snapshot()['mapCatalog']['maps']), 2)


if __name__ == '__main__':
    unittest.main()
