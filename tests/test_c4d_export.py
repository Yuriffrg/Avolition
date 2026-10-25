"""Native export preparation and JSON bridge transaction checks (no C4D runtime)."""
import copy
import json
import os
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
import numpy as np

from app import BrowserAPI
from owpreview.c4d_bridge import BridgeClient, BridgeService, queue_root, write_json
from owpreview.c4d_export import make_payload
from owpreview.export_pose import PoseEvaluator
from owpreview.formats import load_asset, load_clip, build_export_hierarchy
from test_fbx import oracle_fixture


class C4DExportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.queue = self.root/'queue'

    def fixture(self):
        root = Path(__file__).parent/'material-assets'
        asset = load_asset(root/'Entities/MaterialRobot/MaterialRobot.owentity', root)
        clip = load_clip(root/'Animations/001_Wave.owanimclip')
        return asset, clip

    def test_payload_preserves_bone_indices_hierarchy_and_evaluated_animation(self):
        asset, clip = self.fixture()
        payload = make_payload(asset, clip, {}, clip_name='Wave')
        self.assertEqual(payload['asset']['exportHierarchy'], asset['exportHierarchy'])
        self.assertEqual(payload['name'], 'Wave')
        ev = PoseEvaluator(asset['models'][0]['bones'], clip['bones'])
        frame = clip['lastFrame']//4
        expected = ev.evaluate(frame)['world']
        world = [None]*len(ev.bones)
        tracks = payload['animation']['tracks']
        for i in ev.order:
            local = np.array(tracks[i]['frames'][frame]).reshape(4, 4).T
            world[i] = local if ev.parents[i] < 0 else world[ev.parents[i]] @ local
            np.testing.assert_allclose(world[i], expected[i], atol=1e-8)

    def test_c4d_root_motion_comes_from_export_option_not_preview_state(self):
        root = Path(__file__).parent/'demo-assets'
        entity = root/'Entities/PreviewRobot/PreviewRobot.owentity'
        source = root/'Animations/003_Root_Move.owanimclip'
        api = BrowserAPI(self.root/'profile')
        payloads = []

        def capture(payload, destination, texture_folder):
            payloads.append(payload)
            destination.write_bytes(b'Native writer test fixture')
            return {'warnings': []}

        options = {'enabled':False, 'c4dEnabled':True, 'clipEnabled':False,
                   'entityPath':str(entity.resolve()), 'rootPath':str(root.resolve()),
                   'includeMesh':False, 'bakeTextures':False, 'rootMotion':True}
        with patch.object(api._c4d_bridge, 'status', return_value={'connected':True, 'octane':True}), \
             patch.object(api._c4d_bridge, 'export', side_effect=capture):
            for nickname, preview_lock, root_motion in [('Free',False,True), ('In-place preview',True,True),
                                                       ('Root motion disabled',False,False)]:
                result = api.export_animation(str(source), nickname, str(self.root/'output'),
                                              dict(options, lockRoot=preview_lock, rootMotion=root_motion))
                self.assertTrue(result['ok'], result)
        self.assertEqual(payloads[0]['animation'], payloads[1]['animation'])
        asset = load_asset(entity, root)
        clip = load_clip(source)
        evaluator = PoseEvaluator(asset['models'][0]['bones'], clip['bones'])
        root_index = evaluator.parents.index(-1)
        for payload, locked in [(payloads[0],False), (payloads[2],True)]:
            track = next(t for t in payload['animation']['tracks'] if t['boneIndex'] == root_index)
            for frame in (0, clip['lastFrame']//2, clip['lastFrame']):
                actual = np.array(track['frames'][frame]).reshape(4,4).T
                expected = evaluator.evaluate(frame, lock_root=locked)['world'][root_index]
                np.testing.assert_allclose(actual, expected, atol=1e-8)
        free = np.array(payloads[0]['animation']['tracks'][root_index]['frames'])[:,12:15]
        locked = np.array(payloads[2]['animation']['tracks'][root_index]['frames'])[:,12:15]
        self.assertGreater(np.linalg.norm(free[:,[0,2]]-locked[:,[0,2]], axis=1).max(), .1)
        self.assertEqual(list((self.root/'output').glob('*.owanimclip')), [])
        self.assertEqual(list((self.root/'output').glob('*.fbx')), [])

    def test_custom_normals_match_blender_unit_directions_without_mutating_assets(self):
        asset, clip = self.fixture()
        for model in asset['models']:
            for mesh in model['meshes']:
                # Non-axis-aligned packed game normals, including negative axes.
                sample = ([.1, .2, .96], [-.3, .9, .1], [.7, -.2, -.6])
                mesh['normals'] = [v for i in range(len(mesh['positions'])//3) for v in sample[i % 3]]
        original = copy.deepcopy(asset)
        payload = make_payload(asset, clip, {})
        for before_model, after_model in zip(asset['models'], payload['asset']['models']):
            for before, after in zip(before_model['meshes'], after_model['meshes']):
                raw = np.asarray(before['normals']).reshape(-1, 3)
                actual = np.asarray(after['normals']).reshape(-1, 3)
                np.testing.assert_allclose(np.linalg.norm(actual, axis=1), 1, atol=1e-14)
                np.testing.assert_allclose(actual, raw / np.linalg.norm(raw, axis=1)[:, None], atol=1e-14)
                self.assertEqual({k:v for k,v in before.items() if k != 'normals'},
                                 {k:v for k,v in after.items() if k != 'normals'})
        self.assertEqual(asset, original)

    def test_unit_normals_keep_tags_and_follow_native_polygon_corners(self):
        from owpreview.c4d_scene import _mesh
        from test_c4d_scene import make_api
        asset, clip = self.fixture()
        mesh = asset['models'][0]['meshes'][0]
        mesh.update(positions=[0,0,0, 1,0,0, 0,1,0], indices=[0,1,2], uvs=[], uvs2=[],
                    normals=[.1,.2,.96, -.3,.9,.1, .7,-.2,-.6])
        native = make_payload(asset, clip, {})['asset']['models'][0]['meshes'][0]
        obj = _mesh(make_api(), native)
        self.assertEqual(len(obj.tags), 1, 'Retain the authored Normal tag.')
        expected = np.asarray(mesh['normals']).reshape(-1, 3)
        expected /= np.linalg.norm(expected, axis=1)[:, None]
        expected[:, 2] *= -1
        actual = obj.tags[0].data[0]
        for corner, vertex in zip('abcd', [0, 2, 1, 1]):
                np.testing.assert_allclose([actual[corner][axis] for axis in range(3)], expected[vertex], atol=1e-14)

    def test_invalid_normal_data_is_rejected_only_when_exporting_meshes(self):
        for normals in ([float('nan'),0,1], [float('inf'),0,1], [1,0]):
            with self.subTest(normals=normals):
                asset, clip = self.fixture()
                mesh = asset['models'][0]['meshes'][0]
                count = len(mesh['positions'])//3
                mesh['normals'] = normals * count
                with self.assertRaisesRegex(ValueError, 'normal'):
                    make_payload(asset, clip, {})
                self.assertFalse(make_payload(asset, clip, {}, include_mesh=False)['includeMesh'])

    def test_unrepresentable_shear_is_explicit_not_silently_lost(self):
        _fixture, bones, tracks = oracle_fixture()
        asset = {'models': [{'id':'rig', 'name':'Rig', 'bones':bones, 'meshes':[]}]}
        asset['exportHierarchy'] = build_export_hierarchy(asset)
        clip = {'fps':30, 'lastFrame':20, 'bones':tracks}
        with self.assertRaisesRegex(ValueError, 'nonuniform scale'):
            make_payload(asset, clip, {})

    def test_offline_bridge_is_clear_and_creates_no_jobs(self):
        bridge = BridgeClient(self.queue)
        self.assertFalse(bridge.status()['connected'])
        with self.assertRaisesRegex(RuntimeError, 'Avolition C4D Bridge'):
            bridge.export({}, self.root/'out.c4d', self.root/'tex')
        self.assertFalse(self.queue.exists())

    def test_stale_and_wrong_protocol_status_are_ignored(self):
        self.queue.mkdir()
        write_json(self.queue/'status-old.json', {'protocol':1,'heartbeat':time.time()-30})
        write_json(self.queue/'status-wrong.json', {'protocol':99,'heartbeat':time.time()})
        self.assertFalse(BridgeClient(self.queue).status()['connected'])

    def test_malformed_heartbeat_files_do_not_hide_a_live_bridge(self):
        self.queue.mkdir()
        for index, value in enumerate([[], None, {'protocol': 1, 'heartbeat': time.time()},
                                       {'protocol': 1, 'heartbeat': 'invalid', 'instance': 'broken'}]):
            write_json(self.queue/f'status-invalid-{index}.json', value)
        write_json(self.queue/'status-live.json', dict(protocol=1, instance='live', heartbeat=time.time(),
                                                     bridgeVersion='0.7.4', octane=True, mapInstances=True))
        status = BridgeClient(self.queue).status()
        self.assertEqual(status['instance'], 'live')
        self.assertIn('0.7.4', status['message'])
        self.assertIn('map exports ready', status['message'])

    def test_map_capable_octane_instance_wins_over_newer_old_bridge(self):
        self.queue.mkdir()
        now = time.time()
        for instance, version, maps, octane, age in [('new', '0.7.4', True, True, 2),
                                                    ('old', '0.7.2', False, True, 1),
                                                    ('no-octane', '0.7.4', True, False, 0)]:
            write_json(self.queue/f'status-{instance}.json', dict(protocol=1, instance=instance,
                heartbeat=now-age, bridgeVersion=version, mapInstances=maps, octane=octane))
        self.assertEqual(BridgeClient(self.queue).status()['instance'], 'new')

    def test_map_dispatch_rechecks_capability_before_creating_a_job(self):
        self.queue.mkdir()
        write_json(self.queue/'status-old.json', dict(protocol=1, instance='old', heartbeat=time.time(),
                                                     bridgeVersion='0.7.2', octane=True))
        bridge = BridgeClient(self.queue)
        self.assertTrue(bridge.status()['connected'])
        self.assertIn('0.7.2', bridge.status()['message'])
        with self.assertRaisesRegex(RuntimeError, r'bridge 0\.7\.2.*Avolition C4D Bridge 0\.7\.10'):
            bridge.export({'mapInstances': True}, self.root/'out.c4d', self.root/'tex')
        self.assertFalse((self.queue/'jobs').exists())
        self.assertFalse((self.root/'out.c4d').exists())

    def test_new_export_features_require_advertised_capabilities_before_job_creation(self):
        for flag, capability in [('motionClip', 'combinedMotionClips'), ('motionClip', 'exactFrameTimes'),
                                 ('gameplay1P', 'optimizedGameplay1P'), ('gameplay1P', 'povHeightZero'),
                                 ('mapTiming24', 'mapTiming24')]:
            for advertised in (None, False, 1, 'true'):
                with self.subTest(flag=flag, advertised=advertised):
                    bridge = BridgeClient(self.queue)
                    status = dict(connected=True, octane=True, mapInstances=True,
                                  bridgeVersion='0.7.6', instance='old',
                                  motionClips=True, combinedMotionClips=True, exactFrameTimes=True,
                                  optimizedGameplay1P=True, povHeightZero=True, mapTiming24=True)
                    status[capability] = advertised
                    payload = {flag: True, 'mapInstances': flag == 'mapTiming24'}
                    with patch.object(bridge, 'status', return_value=status):
                        with self.assertRaisesRegex(RuntimeError, r'updated motion clip and map timing support.*0\.7\.10'):
                            bridge.export(payload, self.root/'out.c4d', self.root/'tex')
                    self.assertFalse((self.queue/'jobs').exists(), 'Reject before writing a request or texture copy.')
                    self.assertFalse((self.root/'out.c4d').exists())

    def test_old_bridge_accepts_simple_payloads_and_new_bridge_accepts_requested_features(self):
        scenarios = [('legacy', {}, {}),
                     ('disabled', {'motionClip': False, 'mapTiming24': False, 'gameplay1P': False}, {}),
                     ('motion', {'motionClip': True}, {'combinedMotionClips': True, 'exactFrameTimes': True}),
                     ('gameplay', {'motionClip': True, 'gameplay1P': True},
                      {'combinedMotionClips': True, 'exactFrameTimes': True, 'optimizedGameplay1P': True, 'povHeightZero': True}),
                     ('map', {'mapInstances': True, 'mapTiming24': True}, {'mapTiming24': True})]
        for name, payload, capabilities in scenarios:
            with self.subTest(name=name):
                queue = self.root/name
                bridge = BridgeClient(queue)
                status = dict(connected=True, octane=True, mapInstances=True, instance=name,
                              bridgeVersion='0.7.10' if capabilities else '0.7.5', **capabilities)

                def complete(_seconds):
                    job = next((queue/'jobs').iterdir())
                    self.assertEqual(json.loads((job/'payload.json').read_text(encoding='utf-8')), payload)
                    (job/'scene.c4d').write_bytes(b'Native fixture')
                    write_json(job/'result.json', {'ok': True, 'data': {'accepted': name}})

                with patch.object(bridge, 'status', return_value=status), \
                     patch('owpreview.c4d_bridge.time.sleep', side_effect=complete):
                    result = bridge.export(payload, self.root/(name+'.c4d'), self.root/'tex', timeout=2)
                self.assertEqual(result, {'accepted': name})
                self.assertEqual((self.root/(name+'.c4d')).read_bytes(), b'Native fixture')
                self.assertEqual(list((queue/'jobs').iterdir()), [])

    def test_queue_alias_keeps_existing_companions_discoverable(self):
        with patch.dict(os.environ, {'LOCALAPPDATA': str(self.root)}, clear=True):
            self.assertEqual(queue_root(), self.root/'Whitehound/c4d-bridge')
            os.environ['WHITEHOUND_C4D_QUEUE'] = str(self.root/'legacy')
            self.assertEqual(queue_root(), self.root/'legacy')
            os.environ['AVOLITION_C4D_QUEUE'] = str(self.root/'official')
            self.assertEqual(queue_root(), self.root/'official')

    def run_service(self, callback):
        service = BridgeService(callback, lambda: {'octane':True}, self.queue)
        service.tick()
        stop = threading.Event()
        def loop():
            while not stop.wait(.01): service.tick()
        thread = threading.Thread(target=loop, daemon=True)
        thread.start()
        self.addCleanup(lambda: (stop.set(), thread.join(2)))
        return service

    def test_bridge_handoff_copies_textures_and_waits_for_native_result(self):
        def build(payload, path):
            self.assertEqual(payload['name'], 'Wave 日本語')
            self.assertEqual((path.parent/'tex/a.png').read_bytes(), b'Texture fixture')
            path.write_bytes(b'Native writer test fixture')
            return {'warnings':['Fixture only']}
        self.run_service(build)
        textures = self.root/'tex'; textures.mkdir(); (textures/'a.png').write_bytes(b'Texture fixture')
        target = self.root/'out.c4d'
        result = BridgeClient(self.queue).export({'name':'Wave 日本語'}, target, textures, timeout=2)
        self.assertEqual(target.read_bytes(), b'Native writer test fixture')
        self.assertEqual(result['warnings'], ['Fixture only'])
        self.assertEqual(list((self.queue/'jobs').iterdir()), [])

    def test_large_map_native_save_can_finish_after_the_old_timeout(self):
        clock = [0]
        stages = []
        bridge = BridgeClient(self.queue)
        status = dict(connected=True, octane=True, mapInstances=True, instance='test')

        def advance(_seconds):
            clock[0] += 40
            job = next((self.queue/'jobs').iterdir())
            (job/'processing').touch()
            if clock[0] >= 200:
                (job/'scene.c4d').write_bytes(b'Finished large map')
                write_json(job/'result.json', dict(ok=True, data={'verifiedNativeScene': True}))

        with patch.object(bridge, 'status', return_value=status), \
             patch('owpreview.c4d_bridge.time.monotonic', side_effect=lambda: clock[0]), \
             patch('owpreview.c4d_bridge.time.sleep', side_effect=advance):
            result = bridge.export({'mapInstances': True}, self.root/'large.c4d', self.root/'tex',
                                   progress=lambda message: stages.append(message))
        self.assertTrue(result['verifiedNativeScene'])
        self.assertGreater(clock[0], 180)
        self.assertEqual((self.root/'large.c4d').read_bytes(), b'Finished large map')
        self.assertTrue(any('building' in stage for stage in stages))
        self.assertEqual(list((self.queue/'jobs').iterdir()), [])

    def test_unclaimed_map_job_keeps_a_bounded_start_wait(self):
        clock = [0]
        bridge = BridgeClient(self.queue)
        status = dict(connected=True, octane=True, mapInstances=True, instance='test')
        with patch.object(bridge, 'status', return_value=status), \
             patch('owpreview.c4d_bridge.time.monotonic', side_effect=lambda: clock[0]), \
             patch('owpreview.c4d_bridge.time.sleep', side_effect=lambda _: clock.__setitem__(0, clock[0]+50)):
            with self.assertRaisesRegex(RuntimeError, 'did not start'):
                bridge.export({'mapInstances': True}, self.root/'never.c4d', self.root/'tex')
        self.assertEqual(clock[0], 150)
        self.assertFalse((self.root/'never.c4d').exists())
        self.assertEqual(list((self.queue/'jobs').iterdir()), [])

    def test_bridge_native_failure_does_not_leave_an_output(self):
        def fail(payload, path):
            path.write_bytes(b'Partial')
            raise ValueError('Native scene failed')
        self.run_service(fail)
        with self.assertRaisesRegex(RuntimeError, 'Native scene failed'):
            BridgeClient(self.queue).export({}, self.root/'out.c4d', self.root/'tex', timeout=2)
        self.assertFalse((self.root/'out.c4d').exists())

    def test_api_offline_c4d_does_not_publish_even_if_fbx_enabled(self):
        root = Path(__file__).parent/'material-assets'
        api = BrowserAPI(self.root/'profile')
        result = api.export_animation(str(root/'Animations/001_Wave.owanimclip'), 'Wave', str(self.root/'output'),
            {'enabled':True, 'c4dEnabled':True, 'entityPath':str((root/'Entities/MaterialRobot/MaterialRobot.owentity').resolve()),
             'rootPath':str(root.resolve())})
        self.assertFalse(result['ok'])
        self.assertIn('Avolition C4D Bridge', result['error'])
        self.assertFalse((self.root/'output').exists())


if __name__ == '__main__': unittest.main()
