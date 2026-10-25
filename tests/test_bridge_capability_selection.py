"""Prefer an updated running C4D companion and reject old map exporters early."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from owpreview.c4d_bridge import BridgeClient, FEATURE_BRIDGE_MESSAGE, read_json, write_json
from owpreview.map_export import export_map


class BridgeCapabilitySelectionTests(unittest.TestCase):
    def test_capable_instance_wins_over_newer_old_bridge_and_dispatches(self):
        with tempfile.TemporaryDirectory() as tmp, patch('owpreview.c4d_bridge.time.time', return_value=100):
            root = Path(tmp)
            queue = root / 'queue'
            queue.mkdir()
            for instance, age, octane, maps, motion, skeleton, timing, exact, gameplay, height in (
                    ('updated', 2, True, True, True, True, True, True, True, True),
                    ('absolute-height-079', .1, True, True, True, True, True, True, True, None),
                    ('combined-motion-078', .1, True, True, True, True, True, None, None, None),
                    ('separate-motion-077', .1, True, True, True, None, True, None, None, None),
                    ('old', .1, True, True, None, None, None, None, None, None),
                    ('partial', .2, True, True, True, True, False, True, False, False),
                    ('invalid-flags', .3, True, True, True, 'true', 1, 'true', 1, 'true'),
                    ('no-octane', 0, False, True, True, True, True, True, True, True),
                    ('no-maps', 0, True, False, True, True, True, True, True, True)):
                write_json(queue / f'status-{instance}.json', dict(protocol=1, instance=instance,
                           heartbeat=100-age, octane=octane, mapInstances=maps,
                           motionClips=motion, skeletonMotionClips=motion, combinedMotionClips=skeleton,
                           mapTiming24=timing, exactFrameTimes=exact, optimizedGameplay1P=gameplay, povHeightZero=height))
            client = BridgeClient(queue)
            self.assertEqual(client.status()['instance'], 'updated')
            targets = []

            def reply(_seconds):
                job, = (queue / 'jobs').iterdir()
                targets.append(read_json(job / 'request.json')['instance'])
                (job / 'scene.c4d').write_bytes(b'Simulated native scene')
                write_json(job / 'result.json', {'ok': True, 'data': {}})

            with patch('owpreview.c4d_bridge.time.sleep', side_effect=reply):
                client.export({'motionClip': True, 'gameplay1P': True}, root / 'out.c4d', root / 'tex')
            self.assertEqual(targets, ['updated'])
            write_json(queue / 'status-newest.json', dict(protocol=1, instance='newest', heartbeat=99,
                       octane=True, mapInstances=True, motionClips=True, skeletonMotionClips=True, combinedMotionClips=True,
                       mapTiming24=True, exactFrameTimes=True, optimizedGameplay1P=True, povHeightZero=True))
            self.assertEqual(client.status()['instance'], 'newest')

    def test_map_timing_is_checked_before_payload_or_texture_work(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / 'output'
            bridge, progress = Mock(), Mock()
            bridge.status.return_value = dict(connected=True, octane=True, mapInstances=True,
                                              bridgeVersion='0.7.5', mapTiming24=False)
            with patch('owpreview.map_export.make_static_payload') as payload, \
                    patch('owpreview.map_export._bake_map_materials') as bake:
                with self.assertRaisesRegex(RuntimeError, 'updated motion clip and map timing support') as error:
                    export_map({'name': 'Map'}, output, bridge=bridge, progress=progress)
                self.assertEqual(str(error.exception), FEATURE_BRIDGE_MESSAGE)
                payload.assert_not_called()
                bake.assert_not_called()
            progress.assert_not_called()
            bridge.export.assert_not_called()
            self.assertFalse(output.exists())
