"""The frozen animation nickname is independent from the published filename."""
from pathlib import Path
import unittest

from app import BrowserAPI
import test_export_queue_api as queue_fixtures


class MotionClipNameTests(unittest.TestCase):
    # Reuse only fixture construction, not the fixture class's inherited tests.
    setUp = queue_fixtures.ExportQueueAPITests.setUp
    write = queue_fixtures.ExportQueueAPITests.write
    save = queue_fixtures.ExportQueueAPITests.save

    def test_explicit_nickname_persists_and_reaches_bridge_exactly(self):
        for nickname in ('Reload', 'ADS_Up', '装填_左', 'Main_Reload'):
            with self.subTest(nickname=nickname):
                filename = 'Main_' + nickname
                saved = self.save(dict(self.entry, name=filename, motionClipName=nickname))
                restarted = BrowserAPI(self.profile)
                restarted._c4d_bridge = self.bridge
                self.assertEqual(restarted.bootstrap()['data']['exportQueue'][-1]['motionClipName'], nickname)
                restarted.save_annotation(str(self.arms), {'name': 'Later unrelated nickname'})
                result = restarted.export_queue_run(saved['id'], str(self.output),
                    dict(self.options, motionClipName='Caller must not replace frozen nickname'))
                self.assertTrue(result['ok'], result)
                payload = self.bridge.calls[-1]['payload']
                self.assertEqual(payload['name'], filename)
                self.assertEqual(payload['motionClipName'], nickname)
                self.assertEqual(Path(result['data']['c4dPath']).name, filename + '.c4d')

    def test_legacy_queue_strips_only_exact_entity_prefix_once(self):
        for filename, expected in (
                ('Main_reload', 'reload'), ('Main_ADS_Up_日本語', 'ADS_Up_日本語'),
                ('Main_Main_Reload', 'Main_Reload'), ('Custom_reload', 'Custom_reload'),
                ('main_Reload', 'main_Reload'), ('Main_', 'Main_')):
            with self.subTest(filename=filename):
                saved = self.save(dict(self.entry, name=filename))
                result = self.api.export_queue_run(saved['id'], options=self.options)
                self.assertTrue(result['ok'], result)
                self.assertEqual(self.bridge.calls[-1]['payload']['motionClipName'], expected)

    def test_direct_single_export_keeps_nickname_without_object_prefix(self):
        options = dict(self.options, c4dEnabled=True, clipEnabled=False, enabled=False,
                       entityPath=str(self.entity), rootPath=str(self.root), selectedModelId='model-0')
        for nickname in ('Reload', 'ADS_Up_日本語'):
            result = self.api.export_animation(str(self.arms), nickname, str(self.output), options)
            self.assertTrue(result['ok'], result)
            self.assertEqual(Path(result['data']['c4dPath']).name, 'Body_' + nickname + '.c4d')
            self.assertEqual(self.bridge.calls[-1]['payload']['motionClipName'], nickname)

    def test_direct_combined_api_preserves_explicit_full_names(self):
        for name in ('Main_Reload', 'Custom_Name_日本語'):
            result = self.api.export_animation_set(str(self.entity), self.entry['assignments'], name,
                                                    str(self.output), self.options)
            self.assertTrue(result['ok'], result)
            self.assertEqual(self.bridge.calls[-1]['payload']['motionClipName'], name)

    def test_invalid_nickname_does_not_save_queue_or_export(self):
        for value in (None, 1, '', '  ', 'bad\x00name', 'x' * 181):
            self.assertFalse(self.api.export_queue_save(dict(self.entry, motionClipName=value))['ok'])
            result = self.api.export_animation_set(str(self.entity), self.entry['assignments'], 'Scene',
                str(self.output), dict(self.options, motionClipName=value))
            self.assertFalse(result['ok'], result)
        self.assertEqual(self.api.bootstrap()['data']['exportQueue'], [])
        self.assertFalse(self.bridge.calls)
        self.assertFalse(self.output.exists())
