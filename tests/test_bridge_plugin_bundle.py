"""Companion distribution identity and capabilities without native Cinema APIs."""
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
import runpy
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'tools'))
from bundle_c4d_bridge import bundle

SOURCE = Path(__file__).resolve().parents[1]
PLUGIN_ID = 1066202606


class BridgePluginBundleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.target = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def test_bundle_has_official_folder_entry_and_compatible_internal_modules(self):
        output = bundle(SOURCE, self.target)
        self.assertEqual(output.name, 'Avolition C4D Bridge')
        self.assertEqual([p.name for p in output.glob('*.pyp')], ['Avolition C4D Bridge.pyp'])
        self.assertFalse((self.target / 'Whitehound C4D Bridge').exists())
        self.assertTrue((output / 'LICENSE').is_file())
        for name in ('c4d_bridge.py', 'c4d_scene.py', 'c4d_octane.py'):
            self.assertEqual((output / 'whitehound_c4d' / name).read_bytes(), (SOURCE / 'owpreview' / name).read_bytes())
        readme = (output / 'README.txt').read_text(encoding='utf-8')
        self.assertIn('Avolition C4D Bridge — 0.7.5', readme)
        self.assertIn('renaming an old folder inside plugins does not disable it', readme)

    def test_bundle_refuses_legacy_copy_without_deleting_it(self):
        legacy = self.target / 'Whitehound C4D Bridge'
        legacy.mkdir()
        sentinel = legacy / 'WhitehoundBridge.pyp'
        sentinel.write_text('existing user content', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'older Whitehound'):
            bundle(SOURCE, self.target)
        self.assertEqual(sentinel.read_text(encoding='utf-8'), 'existing user content')

    def test_bundle_refuses_mixed_entry_points(self):
        mixed = self.target / 'Avolition C4D Bridge'
        mixed.mkdir()
        (mixed / 'WhitehoundBridge.pyp').write_text('old entry', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'another plugin entry'):
            bundle(SOURCE, self.target)

    def load_entry(self, *, register=True, octane=True):
        output = bundle(SOURCE, self.target)
        registrations, services = [], []
        class Service:
            def __init__(self, build, capabilities):
                self.build, self.capabilities, self.ticks = build, capabilities, 0
                services.append(self)
            def tick(self):
                self.ticks += 1
        def register_plugin(*args):
            registrations.append(args)
            return register
        fake = SimpleNamespace(GetC4DVersion=lambda: 2026302, MSG_TIMER=100, PLUGINTYPE_MATERIAL=3,
            plugins=SimpleNamespace(MessageData=object, FindPlugin=lambda *args: object() if octane else None,
                                    RegisterMessagePlugin=register_plugin))
        stdout = StringIO()
        with patch.dict(sys.modules, {'c4d': fake,
            'whitehound_c4d': SimpleNamespace(),
            'whitehound_c4d.c4d_bridge': SimpleNamespace(BridgeService=Service),
            'whitehound_c4d.c4d_scene': SimpleNamespace(build_scene=lambda *args: None)}), \
                patch.object(sys, 'path', list(sys.path)), redirect_stdout(stdout):
            entry = runpy.run_path(str(output / 'Avolition C4D Bridge.pyp'), run_name='__main__')
        return output, entry, registrations, services, stdout.getvalue()

    def test_native_entry_registers_stable_id_and_advertises_maps(self):
        output, entry, registrations, services, stdout = self.load_entry()
        self.assertEqual(len(registrations), 1)
        ident, name, flags, plugin = registrations[0]
        self.assertEqual((ident, name, flags), (PLUGIN_ID, 'Avolition C4D Bridge', 0))
        expected = dict(bridgeVersion='0.7.5', bridgeName='Avolition C4D Bridge', pluginPath=str(output.resolve()),
                        cinemaVersion=2026302, mapInstances=True, octane=True)
        self.assertEqual(entry['capabilities'](), expected)
        self.assertEqual(plugin.GetTimer(), 1000)
        self.assertTrue(plugin.CoreMessage(100, None))
        self.assertEqual(len(services), 1)
        self.assertEqual(services[0].ticks, 1)
        self.assertEqual(services[0].capabilities(), expected)
        self.assertEqual(stdout, '')

    def test_missing_octane_is_not_misreported(self):
        _output, entry, _registration, _services, _stdout = self.load_entry(octane=False)
        self.assertFalse(entry['capabilities']()['octane'])
        self.assertTrue(entry['capabilities']()['mapInstances'])

    def test_duplicate_plugin_registration_explains_single_copy_migration(self):
        _output, _entry, registrations, services, stdout = self.load_entry(register=False)
        self.assertEqual(registrations[0][0], PLUGIN_ID)
        self.assertEqual(services, [])
        self.assertIn('Registration failed', stdout)
        self.assertIn('outside its plugin search paths', stdout)


if __name__ == '__main__':
    unittest.main()
