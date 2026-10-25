"""Avolition's native C4D/Octane export companion. No scene edits or network."""
import os
import sys
import c4d

sys.path.insert(0, os.path.dirname(__file__))
from whitehound_c4d.c4d_bridge import BridgeService
from whitehound_c4d.c4d_scene import build_scene

# Private prototype IDs, separate from the user's Overwatch Entities/Maps IDs.
PLUGIN_ID = 1066202606
BRIDGE_VERSION = '0.7.10'
BRIDGE_NAME = 'Avolition C4D Bridge'


def capabilities():
    return {'bridgeVersion': BRIDGE_VERSION, 'bridgeName': BRIDGE_NAME,
            'pluginPath': os.path.dirname(os.path.abspath(__file__)),
            'cinemaVersion': c4d.GetC4DVersion(), 'mapInstances': True,
            'motionClips': True, 'skeletonMotionClips': True, 'combinedMotionClips': True, 'mapTiming24': True,
            'exactFrameTimes': True, 'optimizedGameplay1P': True, 'povHeightZero': True,
            'octane': c4d.plugins.FindPlugin(1029501, c4d.PLUGINTYPE_MATERIAL) is not None}


class AvolitionBridge(c4d.plugins.MessageData):
    def __init__(self):
        self.service = None

    def GetTimer(self):
        return 1000

    def CoreMessage(self, ident, container):
        if ident == c4d.MSG_TIMER:
            try:
                if self.service is None:
                    self.service = BridgeService(build_scene, capabilities)
                self.service.tick()
            except Exception as error:
                print('[Avolition C4D Bridge]', error)
        return True


if __name__ == '__main__':
    if not c4d.plugins.RegisterMessagePlugin(PLUGIN_ID, BRIDGE_NAME, 0, AvolitionBridge()):
        print('[Avolition C4D Bridge] Registration failed. Keep only one bridge copy installed. '
              'Close Cinema 4D and move older Whitehound C4D Bridge or Avolition C4D Bridge '
              'folders outside its plugin search paths, then restart.')
