"""Checks for mixed-coverage sources in a shared, zero-scale-key library."""
import copy
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from owpreview import c4d_stager as stager
from owpreview.c4d_scene import IDENTITY


class StagerSourceTests(unittest.TestCase):
    def setUp(self):
        self.arm = dict(id='arms:0', type='bone', modelId='arms', boneIndex=0,
                        name='bone_00DE', matrix=list(IDENTITY))
        self.gun = dict(self.arm, id='gun:0', modelId='gun', name='Weapon')
        self.payload = dict(asset=dict(exportHierarchy=[self.arm, self.gun]))

    def action(self, name, scale):
        frame = list(IDENTITY)
        # A quarter turn and a translation make accidental TRS changes visible.
        frame[:3] = [0, scale, 0]
        frame[4:7] = [-scale, 0, 0]
        frame[10] = scale
        frame[12:15] = [1, 2, 3]
        return dict(name=name, animation=dict(fps=60, lastFrame=2, tracks=[
            dict(modelId='arms', boneIndex=0, frames=[frame] * 3)]))

    def test_idle_scale_is_shared_and_each_source_keeps_its_position_rotation_and_rate(self):
        idle = self.action('Idle', 1.197265625)
        down = self.action('ADS Down', 1.)
        original = copy.deepcopy(down)
        scales = stager._rig_scales(self.payload, idle)
        candidate = stager._action_payload(self.payload, down, False, scales)
        self.assertEqual(down, original)
        self.assertEqual(candidate['animation']['fps'], 60)
        arm, gun = candidate['animation']['tracks']
        self.assertEqual(arm['frames'], idle['animation']['tracks'][0]['frames'])
        self.assertEqual(gun, dict(modelId='gun', boneIndex=0, frames=[IDENTITY] * 3))
        self.assertIn('ADS Down: bone_00DE', candidate['warnings'][0])
        self.assertFalse(candidate['includeMesh'])

    def test_missing_joint_does_not_silently_become_a_rest_track(self):
        row = self.action('Reload', 1)
        scales = stager._rig_scales(self.payload, row)
        row['animation']['tracks'][0]['modelId'] = 'other'
        with self.assertRaisesRegex(ValueError, 'missing joint'):
            stager._action_payload(self.payload, row, True, scales)

    def test_switched_document_cannot_consume_review(self):
        doc = SimpleNamespace(GetDocumentName=lambda: 'Watchpoint', GetFirstObject=lambda: None)
        other = SimpleNamespace(GetDocumentName=lambda: 'Watchpoint', GetFirstObject=lambda: None)
        active = [doc]
        c4d = SimpleNamespace(documents=SimpleNamespace(GetActiveDocument=lambda: active[0]))
        with tempfile.TemporaryDirectory() as folder, patch.dict('sys.modules', c4d=c4d), \
                patch.object(stager, '_get_registration'), patch.object(stager, '_reviewed_document', None), \
                patch.object(stager, '_review_token', None), patch.object(stager, '_create') as insert:
            destination = Path(folder) / 'scene.c4d'
            review = stager.dispatch({'operation': 'stagerDestination'}, destination)
            active[0] = other
            with self.assertRaisesRegex(ValueError, 'project changed'):
                stager.dispatch(dict(operation='createStagerRig', documentId=review['documentId']), destination)
            insert.assert_not_called()

    def test_closed_review_can_be_replaced_after_reopening_project(self):
        class ClosedDocument:
            def __eq__(self, other):
                raise ReferenceError('document is not alive')

        reopened = SimpleNamespace(GetDocumentName=lambda: 'Watchpoint', GetFirstObject=lambda: None)
        with patch.object(stager, '_reviewed_document', ClosedDocument()), patch.object(stager, '_review_token', 'old'):
            self.assertFalse(stager._same_review(reopened))
            review = stager._destination(None, reopened)
            self.assertNotEqual(review['documentId'], 'old')
            self.assertTrue(stager._same_review(reopened))

    def test_missing_registration_does_not_claim_a_surviving_take_clone(self):
        info = dict(root=None, target=None, sources={'idle': None})
        registration = SimpleNamespace(registered_rig=lambda *args: info, REGISTRATION_REPAIR_VERSION=1)
        marker = SimpleNamespace(GetString=lambda field: {1: 'rig', 2: 'root'}[field])
        clone = SimpleNamespace(GetDataInstance=lambda: SimpleNamespace(GetContainerInstance=lambda key: marker))
        with patch.dict('sys.modules', whitehound_camera=SimpleNamespace(registration=registration)), \
                patch.object(stager, '_walk', return_value=[((), clone)]):
            with self.assertRaisesRegex(ValueError, 'copy or part'):
                stager._library_state(None, SimpleNamespace(GetFirstObject=lambda: clone), {'rigId': 'rig'})
