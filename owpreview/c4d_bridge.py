"""Local, JSON-only export queue shared with the Cinema 4D companion plugin.

Cinema owns native document creation. Jobs write only inside their unique queue
directory; Avolition publishes the completed file with its other exports.
"""
import json
import os
from pathlib import Path
import re
import shutil
import time
import uuid

PROTOCOL = 1
MAX_PAYLOAD = 256 * 1024 * 1024
MAP_EXPORT_TIMEOUT = 30 * 60
QUEUE_START_TIMEOUT = 150
SETUP_MESSAGE = ('C4D export needs Cinema 4D with Octane and the included Avolition C4D Bridge. '
                 'Copy the Avolition C4D Bridge folder from this download into Cinema 4D\'s '
                 'plugins folder, restart Cinema 4D, then export again. Keep Cinema 4D open.')
MAP_BRIDGE_MESSAGE = ('Install the bundled Avolition C4D Bridge 0.7.5, replacing the old bridge '
                      'copy, then restart Cinema 4D with Octane. Keep only one bridge installed.')


def map_bridge_message(status):
    version = status.get('bridgeVersion') or 'unknown version'
    return f'Cinema 4D is connected through bridge {version}, but that bridge does not support maps. {MAP_BRIDGE_MESSAGE}'


def queue_root():
    # Keep the existing mailbox so renamed and previously installed companions
    # can still discover each other. New isolated installations can use either alias.
    override = os.environ.get('AVOLITION_C4D_QUEUE') or os.environ.get('WHITEHOUND_C4D_QUEUE')
    return Path(override) if override else Path(os.environ.get('LOCALAPPDATA') or Path.home()) / 'Whitehound/c4d-bridge'


def write_json(path, value):
    path = Path(path)
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(',', ':')), encoding='utf-8')
    temporary.replace(path)


def read_json(path, limit=MAX_PAYLOAD):
    path = Path(path)
    if path.stat().st_size > limit:
        raise ValueError('Cinema 4D export request is too large.')
    return json.loads(path.read_text(encoding='utf-8'))


class BridgeClient:
    def __init__(self, root=None):
        self.root = Path(root) if root is not None else queue_root()

    def status(self):
        live = []
        for path in self.root.glob('status-*.json'):
            try:
                value = read_json(path, 65536)
                if (isinstance(value, dict) and value.get('protocol') == PROTOCOL
                        and isinstance(value.get('instance'), str) and value['instance']
                        and 0 <= time.time() - value['heartbeat'] < 10):
                    live.append(value)
            except (OSError, ValueError, KeyError, TypeError):
                continue
        if not live:
            return {'connected': False, 'message': SETUP_MESSAGE}
        # Prefer a map-capable Octane instance over a newer heartbeat from an
        # older open Cinema version. Both preflight and job dispatch use this rule.
        value = max(live, key=lambda v: (bool(v.get('octane')), v.get('mapInstances') is True, v['heartbeat']))
        version = value.get('bridgeVersion') or 'unknown version'
        if not value.get('octane'):
            message = f'Cinema 4D connected (bridge {version}), but Octane is not loaded. Enable Octane and restart Cinema 4D.'
        elif value.get('mapInstances') is True:
            message = f'Cinema 4D connected (bridge {version}). Octane materials and map exports ready.'
        else:
            message = f'Cinema 4D connected (bridge {version}). Octane materials ready for animations. Update the Avolition C4D Bridge for map exports.'
        return dict(value, connected=True, message=message)

    def export(self, payload, destination, texture_folder, timeout=None, *, progress=None):
        timeout = (MAP_EXPORT_TIMEOUT if payload.get('mapInstances') else 150) if timeout is None else timeout
        status = self.status()
        if not status['connected']:
            raise RuntimeError(SETUP_MESSAGE)
        if not status.get('octane'):
            raise RuntimeError(status['message'])
        if payload.get('mapInstances') and status.get('mapInstances') is not True:
            raise RuntimeError(map_bridge_message(status))
        job = self.root / 'jobs' / uuid.uuid4().hex
        job.mkdir(parents=True)
        completed = False
        try:
            if progress:
                progress('Sending map geometry and textures to Cinema 4D')
            write_json(job/'payload.json', payload)
            if (job/'payload.json').stat().st_size > MAX_PAYLOAD:
                raise ValueError('Cinema 4D export exceeds the 256 MB job limit.')
            textures = Path(texture_folder)
            if textures.exists():
                (job/'tex').mkdir()
                for path in textures.glob('*.png'):
                    shutil.copy2(path, job/'tex'/path.name)
            write_json(job/'request.json', {'protocol': PROTOCOL, 'instance': status['instance'],
                                          'expiresAt': time.time() + timeout})
            deadline = time.monotonic() + min(timeout, QUEUE_START_TIMEOUT)
            started = False
            if progress:
                progress('Waiting for Cinema 4D to start the export')
            while time.monotonic() < deadline:
                if not started and (job/'processing').exists():
                    started = True
                    # Give a large map its own native-build budget, separately
                    # from queue pickup and earlier parsing/texture baking.
                    deadline = time.monotonic() + timeout
                    if progress:
                        progress('Cinema 4D is building, saving and verifying the map')
                if (job/'result.json').is_file():
                    result = read_json(job/'result.json', 1024 * 1024)
                    completed = True
                    if not result.get('ok'):
                        raise RuntimeError(result.get('error', 'Cinema 4D export failed.'))
                    scene = job/'scene.c4d'
                    if not scene.is_file() or scene.stat().st_size == 0:
                        raise RuntimeError('Cinema 4D did not save the native scene.')
                    shutil.copyfile(scene, destination)
                    return result.get('data', {})
                time.sleep(.1)
            if not started:
                raise RuntimeError('Cinema 4D did not start the export. Close any modal Cinema 4D dialog and check that the Avolition bridge is running.')
            raise RuntimeError(f'Cinema 4D did not finish the export within {timeout / 60:g} minutes.')
        finally:
            if completed or not (job/'processing').exists():
                # job is always a newly created UUID directory under our queue.
                shutil.rmtree(job, ignore_errors=True)
            else:
                (job/'cancelled').touch()


class BridgeService:
    """Call tick from Cinema 4D's main-thread MessageData timer."""
    def __init__(self, exporter, capabilities, root=None):
        self.root = Path(root) if root is not None else queue_root()
        self.root.mkdir(parents=True, exist_ok=True)
        (self.root/'jobs').mkdir(exist_ok=True)
        self.instance = uuid.uuid4().hex
        self.exporter, self.capabilities = exporter, capabilities
        self.busy = False

    def tick(self):
        if self.busy:
            return
        write_json(self.root/f'status-{self.instance}.json', dict(
            self.capabilities(), protocol=PROTOCOL, instance=self.instance, heartbeat=time.time()))
        for job in sorted((self.root/'jobs').iterdir()):
            if not re.fullmatch(r'[0-9a-f]{32}', job.name) or not job.is_dir():
                continue
            request = job/'request.json'
            if not request.is_file() or (job/'result.json').exists() or (job/'processing').exists():
                continue
            try:
                value = read_json(request, 65536)
                if value.get('protocol') != PROTOCOL or value.get('instance') != self.instance:
                    continue
                if value.get('expiresAt', 0) < time.time() or (job/'cancelled').exists():
                    continue
                with (job/'processing').open('x'):
                    pass
            except (OSError, ValueError):
                continue
            self.busy = True
            try:
                payload = read_json(job/'payload.json')
                result = self.exporter(payload, job/'scene.c4d')
                write_json(job/'result.json', {'ok': True, 'data': result})
            except Exception as error:
                write_json(job/'result.json', {'ok': False, 'error': str(error) or type(error).__name__})
            finally:
                self.busy = False
                if (job/'cancelled').exists():
                    shutil.rmtree(job, ignore_errors=True)
            break  # Leave Cinema's event loop responsive between jobs.
