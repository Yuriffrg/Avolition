"""Mixed map/character libraries stay separate without reading map geometry."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI
from owpreview.library_scan import entity_sort_key, inside_map_assets
from test_formats import clip_bytes, entity_bytes, model_bytes
from test_map_formats import map_bytes


class LibraryScanTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.library = self.root/'library'
        self.library.mkdir()
        self.api = BrowserAPI(self.root/'profile')

    def write(self, relative, content=b'model fixture'):
        path = self.library/relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def character(self, directory='Heroes/Widow'):
        return [self.write(f'{directory}/Entities/Gameplay3P.owentity'),
                self.write(f'{directory}/Models/Body.owmdl'),
                self.write(f'{directory}/Models/Weapon.owmdl'),
                self.write(f'{directory}/Models/Body/Animations/Idle.owanimclip', clip_bytes())]

    def map_export(self, directory='Maps/Kings Row/6B1'):
        prefix = f'{directory}/' if directory else ''
        self.write(f'{prefix}Evening.OWMAP', map_bytes(name='Kings Row'))
        self.write(f'{prefix}Models/Building/Building.owmdl')
        self.write(f'{prefix}Entities/Prop.owentity')
        self.write(f'{prefix}Effects/Detail.owmdl')
        # Invalid map-only clips must be pruned before header/duplicate parsing.
        self.write(f'{prefix}Models/Building/Animations/Door.owanimclip', b'map clip')
        return self.library/directory

    def scan(self, root=None):
        response = self.api.scan_root(str(root or self.library))
        self.assertTrue(response['ok'], response)
        return response['data']

    def test_shared_library_excludes_map_dependencies_and_keeps_character_assets(self):
        character = self.character()
        self.map_export()
        self.map_export('Renamed environment archive/AB2')
        data = self.scan()
        self.assertEqual({Path(item['path']) for item in data['entities']}, set(character[:3]))
        self.assertEqual([Path(item['path']) for item in data['clips']], character[3:])
        self.assertEqual(data['uniqueClipCount'], 1)
        self.assertEqual(data['duplicateClipCount'], 0)
        self.assertEqual(data['warnings'], [])
        maps = self.api.scan_maps(str(self.library))
        self.assertTrue(maps['ok'], maps)
        self.assertEqual(len(maps['data']['maps']), 2)

    def test_map_assets_are_pruned_before_walking_into_large_branches(self):
        import os
        walk = os.walk
        visited = []
        map_root = self.map_export()
        self.character()
        def observe(*args, **kwargs):
            for entry in walk(*args, **kwargs):
                visited.append(Path(entry[0]))
                yield entry
        with patch('app.os.walk', side_effect=observe):
            self.scan()
        self.assertIn(map_root, visited)
        self.assertFalse(any(folder.is_relative_to(map_root/'Models') for folder in visited))
        self.assertFalse(any(folder.is_relative_to(map_root/'Entities') for folder in visited))

    def test_direct_map_container_build_and_nested_model_scans_stay_empty(self):
        map_root = self.map_export()
        for folder in (self.library/'Maps', map_root, map_root/'Models',
                       map_root/'Models/Building', map_root/'Models/Building/Animations',
                       map_root/'Entities', map_root/'Effects'):
            with self.subTest(folder=folder):
                data = self.scan(folder)
                self.assertEqual(data['entities'], [])
                self.assertEqual(data['clips'], [])
                self.assertEqual(data['warnings'], [])

    def test_loose_map_preserves_unrelated_character_siblings(self):
        self.map_export('')
        character = self.character()
        other = self.character('Custom characters/Máp Hero')
        self.write('loose-prop.owmdl')
        data = self.scan()
        self.assertEqual({Path(item['path']) for item in data['entities']}, set(character[:3]+other[:3]))
        self.assertEqual({Path(item['path']) for item in data['clips']}, set(character[3:]+other[3:]))

    def test_case_insensitive_markers_work_without_a_maps_folder_name(self):
        self.write('Environment/Night.oWmAp', map_bytes())
        model = self.write('Environment/mOdElS/Prop.owmdl')
        self.write('Environment/eNtItIeS/Prop.owentity')
        self.assertEqual(self.scan()['entities'], [])
        self.assertEqual(self.scan(model.parent)['entities'], [])

    def test_maps_name_alone_does_not_hide_character_assets(self):
        character = self.character('Maps')
        data = self.scan()
        self.assertEqual({Path(item['path']) for item in data['entities']}, set(character[:3]))
        self.assertEqual([Path(item['path']) for item in data['clips']], character[3:])
        self.assertEqual(len(self.scan(self.library/'Maps/Models')['entities']), 2)

    def test_directory_named_like_a_map_is_not_an_export_marker(self):
        self.character('Custom')
        (self.library/'Custom/Folder.owmap').mkdir()
        self.assertFalse(inside_map_assets(self.library/'Custom/Models'))
        self.assertEqual(len(self.scan()['entities']), 3)

    def test_unreadable_ancestor_does_not_guess_map_ownership(self):
        with patch('owpreview.library_scan.os.scandir', side_effect=PermissionError('not readable')):
            self.assertFalse(inside_map_assets(self.library/'Models/Character'))

    def test_gameplay_entities_lead_scan_without_changing_the_remaining_entity_model_order(self):
        order = ['Zed/Gameplay1P.owentity', 'Alpha/GAMEPLAY1P.OWENTITY',
                 'Alpha/Gameplay3P.owentity', 'Zed/Gameplay3P.owentity',
                 'Zed/Other.owentity', 'Alpha/Other.owentity', 'Alpha/Models/Gameplay1P.owmdl',
                 'Alpha/Models/A_model.owmdl', 'Zed/Models/Z_model.owmdl']
        for name in order:
            self.write(name)
        result = self.scan()['entities']
        actual = [item['relativePath'].replace('\\', '/') for item in result]
        self.assertEqual(actual, [order[1], order[0], order[2], order[3], order[5], order[4],
                                  order[7], order[6], order[8]])

    def test_priority_helper_handles_windows_and_posix_paths_without_skeleton_heuristics(self):
        rows = [
            {'kind': 'model', 'relativePath': 'Models/Gameplay1P.owmdl'},
            {'kind': 'entity', 'relativePath': 'Heroes\\日本語\\GAMEPLAY3P.OWENTITY'},
            {'kind': 'entity', 'relativePath': 'Heroes/日本語/Gameplay1P.owentity'},
            {'kind': 'entity', 'relativePath': 'Hero/Other.owentity'},
            {'kind': 'entity', 'relativePath': 'Hero/Gameplay1P_extra.owentity'},
        ]
        self.assertEqual(sorted(rows, key=entity_sort_key), [rows[2], rows[1], rows[4], rows[3], rows[0]])

    def test_source_file_sizes_preserve_rows_and_counts_when_stat_is_unavailable(self):
        model_data = model_bytes()
        entity_data = entity_bytes(model='Body.owmdl', relative='.')
        model = self.write('Hero/Body.owmdl', model_data)
        entity = self.write('Hero/Gameplay3P.owentity', entity_data)
        self.map_export()
        with patch('app.load_asset', side_effect=AssertionError('Loaded geometry')), \
                patch('owpreview.formats._load_model', side_effect=AssertionError('Decoded geometry')):
            rows = {Path(item['path']): item for item in self.scan()['entities']}
        self.assertEqual(set(rows), {entity, model})
        self.assertEqual(rows[entity]['size'], len(entity_data))
        self.assertEqual(rows[model]['size'], len(model_data))
        self.assertEqual(rows[entity]['boneCount'], 2)
        self.assertNotIn('sizeError', rows[entity])

        original_stat = Path.stat

        def unavailable(path, *args, **kwargs):
            if path == entity:
                raise PermissionError('Source file size unavailable')
            return original_stat(path, *args, **kwargs)

        with patch.object(Path, 'stat', unavailable), \
                patch('app.BoneCountIndex.metadata', return_value={'boneCount': 2, 'boneCountError': ''}):
            data = self.scan()
        rows = {Path(item['path']): item for item in data['entities']}
        self.assertEqual(set(rows), {entity, model})
        self.assertIsNone(rows[entity]['size'])
        self.assertIn('Source file size unavailable', rows[entity]['sizeError'])
        self.assertEqual(rows[entity]['boneCount'], 2)
        self.assertEqual(rows[model]['size'], len(model_data))
        self.assertEqual(data['warnings'], [])


if __name__ == '__main__':
    unittest.main()
