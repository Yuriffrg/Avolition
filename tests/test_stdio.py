"""JSON-lines desktop bridge tests using isolated state and real fixture files."""

import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from app import BrowserAPI, run_stdio


DEMO = Path(__file__).parent / 'demo-assets'


class FlushedOutput(io.StringIO):
    def __init__(self):
        super().__init__()
        self.flush_count = 0

    def flush(self):
        self.flush_count += 1
        return super().flush()


class StdioTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name).resolve()
        self.api = BrowserAPI(self.workspace / 'state', desktop_mode=True)

    def exchange(self, requests):
        lines = [item if isinstance(item, str) else json.dumps(item, ensure_ascii=False)
                 for item in requests]
        incoming = io.StringIO('\n'.join(lines) + '\n')
        outgoing = FlushedOutput()
        run_stdio(self.api, input_stream=incoming, output_stream=outgoing)
        replies = [json.loads(line) for line in outgoing.getvalue().splitlines()]
        self.assertEqual(len(replies), len(requests), outgoing.getvalue())
        self.assertGreaterEqual(outgoing.flush_count, len(requests))
        return replies

    def request(self, ident, method, *args):
        return {'id': ident, 'method': method, 'args': list(args)}

    def assert_ok(self, response, ident):
        self.assertEqual(response['id'], ident)
        self.assertTrue(response['result']['ok'], response)
        return response['result']['data']

    def test_native_bootstrap_and_request_ids(self):
        replies = self.exchange([self.request(1, 'bootstrap'), self.request('日本語-id', 'bootstrap')])
        for reply, ident in zip(replies, (1, '日本語-id')):
            data = self.assert_ok(reply, ident)
            self.assertFalse(data['browserMode'])
            self.assertEqual(data['appName'], 'Avolition')

    def test_unknown_private_methods_are_rejected_without_dispatch(self):
        requests = [self.request(index, method) for index, method in enumerate(
            ('does_not_exist', '_save', '__getattribute__', '__class__'), 1)]
        requests.append(self.request('still-alive', 'bootstrap'))
        with patch.object(self.api, '_save', side_effect=AssertionError('Private method dispatched')) as private:
            replies = self.exchange(requests)
        private.assert_not_called()
        for reply in replies[:-1]:
            self.assertFalse(reply['result']['ok'], reply)
            self.assertTrue(reply['result'].get('error'))
        self.assert_ok(replies[-1], 'still-alive')

    def test_malformed_json_requests_and_arguments_do_not_end_session(self):
        malformed = [
            '{bad json',
            '[]',
            'null',
            '"not an object"',
            {'id': 'object-args', 'method': 'scan_root', 'args': {}},
            {'id': 'null-args', 'method': 'bootstrap', 'args': None},
            {'id': 'string-args', 'method': 'bootstrap', 'args': 'bad'},
            {'id': 'numeric-method', 'method': 123, 'args': []},
            {'id': 'missing-method', 'args': []},
            self.request('missing-arguments', 'save_annotation'),
            self.request('too-many-arguments', 'bootstrap', 'extra'),
            self.request('bad-annotation', 'save_annotation', str(self.workspace / 'clip.owanimclip'), 'text'),
        ]
        replies = self.exchange(malformed + [self.request('recovered', 'bootstrap')])
        for request, reply in zip(malformed, replies[:-1]):
            with self.subTest(request=request):
                self.assertFalse(reply['result']['ok'], reply)
                self.assertTrue(reply['result'].get('error'))
                if isinstance(request, dict):
                    self.assertEqual(reply['id'], request['id'])
        self.assert_ok(replies[-1], 'recovered')

    def test_utf8_labels_paths_and_export_round_trip(self):
        root = self.workspace / 'Héro 日本語'
        root.mkdir()
        source = root / '00001.owanimclip'
        original = (DEMO / 'Animations' / '001_Wave.owanimclip').read_bytes()
        source.write_bytes(original)
        output = self.workspace / 'Choisis 日本語'
        annotation = {'name': 'Salut café 日本語 👋', 'notes': 'First line\nПривет — deuxième ligne', 'favorite': True}
        replies = self.exchange([
            self.request('scan', 'scan_root', str(root)),
            self.request('name', 'save_annotation', str(source), annotation),
            self.request('folder', 'set_output_folder', str(output)),
            self.request('copy', 'export_animation', str(source), annotation['name'], str(output),
                         {'clipEnabled': True, 'enabled': False, 'c4dEnabled': False}),
            self.request('clip', 'load_animation', str(source)),
            self.request('restore', 'bootstrap'),
        ])
        for request_id, reply in zip(('scan', 'name', 'folder', 'copy', 'clip', 'restore'), replies):
            self.assert_ok(reply, request_id)
        self.assertEqual(replies[1]['result']['data'], annotation)
        copy = replies[3]['result']['data']
        self.assertEqual(copy['name'], annotation['name'] + '.owanimclip')
        self.assertEqual(Path(copy['path']).read_bytes(), original)
        self.assertEqual(source.read_bytes(), original)
        self.assertIn(annotation, replies[-1]['result']['data']['catalog'].values())
        restarted = BrowserAPI(self.workspace / 'state', desktop_mode=True).bootstrap()
        self.assertIn(annotation, restarted['data']['catalog'].values())

    def test_repeated_entity_model_and_animation_loads_in_one_session(self):
        entity = DEMO / 'Entities' / 'PreviewRobot' / 'PreviewRobot.owentity'
        model = DEMO / 'Models' / 'PreviewRobot.00C' / 'PreviewRobot.owmdl'
        clips = sorted((DEMO / 'Animations').glob('*.owanimclip'))
        self.assertTrue(entity.is_file())
        self.assertTrue(model.is_file())
        self.assertEqual(len(clips), 3)
        requests = [self.request('scan', 'scan_root', str(DEMO))]
        for repeat in range(3):
            requests.append(self.request(f'entity-{repeat}', 'load_entity', str(entity)))
            requests.append(self.request(f'model-{repeat}', 'load_entity', str(model)))
            requests.extend(self.request(f'clip-{repeat}-{index}', 'load_animation', str(clip))
                            for index, clip in enumerate(clips))
        replies = self.exchange(requests)
        for request, reply in zip(requests, replies):
            with self.subTest(request=request['id']):
                data = self.assert_ok(reply, request['id'])
                if request['method'] == 'load_entity':
                    self.assertTrue(data['models'])
                if request['method'] == 'load_animation':
                    self.assertGreater(data['boneCount'], 0)
                    self.assertGreater(data['durationFrames'], 1)

    def test_load_failure_does_not_poison_following_requests(self):
        bad = self.workspace / 'truncated.owanimclip'
        bad.write_bytes(b'not a clip')
        valid = DEMO / 'Animations' / '001_Wave.owanimclip'
        replies = self.exchange([
            self.request('broken', 'load_animation', str(bad)),
            self.request('missing', 'load_entity', str(self.workspace / 'missing.owentity')),
            self.request('valid', 'load_animation', str(valid)),
            self.request('valid-again', 'load_animation', str(valid)),
        ])
        self.assertFalse(replies[0]['result']['ok'])
        self.assertFalse(replies[1]['result']['ok'])
        self.assertEqual(self.assert_ok(replies[2], 'valid'), self.assert_ok(replies[3], 'valid-again'))


if __name__ == '__main__':
    unittest.main()
