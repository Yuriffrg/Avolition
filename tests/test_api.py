import json
from pathlib import Path
import tempfile
import unittest
from app import BrowserAPI


class APITests(unittest.TestCase):
    def test_scan_persistence_and_repeated_load(self):
        root = Path(__file__).parent / 'demo-assets'
        with tempfile.TemporaryDirectory() as state:
            api = BrowserAPI(state)
            self.assertFalse(api.scan_root('')['ok'])
            scan = api.scan_root(str(root))
            self.assertTrue(scan['ok'], scan)
            self.assertEqual(len(scan['data']['clips']), 3)
            entity = next(item for item in scan['data']['entities'] if item['kind'] == 'entity')
            for _ in range(3):
                self.assertTrue(api.load_entity(entity['path'])['ok'])
                for clip in scan['data']['clips']:
                    self.assertTrue(api.load_animation(clip['path'])['ok'])
            clip = scan['data']['clips'][0]
            original = Path(clip['path']).read_bytes()
            note = {'name': 'Wave <test>', 'notes': 'Saved locally', 'favorite': True}
            self.assertEqual(api.save_annotation(clip['path'], note)['data'], note)
            restored = BrowserAPI(state).scan_root(str(root))
            self.assertEqual(restored['data']['clips'][0]['annotation'], note)
            self.assertEqual(Path(clip['path']).read_bytes(), original)

    def test_bad_file_does_not_abort_scan(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'bad.owanimclip').write_bytes(b'bad')
            api = BrowserAPI(root/'state')
            result = api.scan_root(str(root))
            self.assertTrue(result['ok'])
            self.assertIn('error', result['data']['clips'][0])
            self.assertFalse(api.load_animation(str(root/'missing.owanimclip'))['ok'])
            self.assertFalse(api.load_entity(str(root/'missing.owentity'))['ok'])


if __name__ == '__main__':
    unittest.main()
