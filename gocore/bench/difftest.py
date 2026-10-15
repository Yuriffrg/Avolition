#!/usr/bin/env python3
"""Differential test: identical request sequences through the Python core
(app.py --stdio) and the Go core (owio-core --stdio),
comparing every reply and the final catalog.json of each core's own fresh
profile.

Usage: difftest.py GO_BINARY [LIBRARY ...]
Defaults to tests/demo-assets, tests/material-assets and the benchmark
library named by AVOLITION_BENCH_LIBRARY, when set. Exit status 1 when any reply differs.
"""
import base64
import io
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

REPO = Path(__file__).resolve().parents[2]
# The library from bench/make_library.py, when AVOLITION_BENCH_LIBRARY names it.
BENCH = Path(os.environ.get('AVOLITION_BENCH_LIBRARY') or '/nonexistent')
# Values that legitimately differ between two runs of the same core.
IGNORED_KEYS = {'elapsedSeconds', 'checkedAt', 'startedAt', 'finishedAt', 'analyzedAt'}


class Core:
    def __init__(self, label, command, data_dir, app_dir, env=None):
        self.label, self.data_dir = label, str(data_dir)
        self.process = subprocess.Popen(command + ['--stdio', '--data-dir', str(data_dir), '--app-dir', str(app_dir)],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, cwd=REPO,
                                        env=dict(os.environ, **(env or {})))
        self.next_id = 0
        self.seconds = 0.0
        self.by_method = {}

    def call(self, method, *args, raw=None):
        self.next_id += 1
        self.method = method or 'raw'
        line = raw if raw is not None else json.dumps({'id': self.next_id, 'method': method, 'args': list(args)}, ensure_ascii=False)
        line = line.replace('<DATA>', json.dumps(self.data_dir)[1:-1])
        started = time.perf_counter()
        self.process.stdin.write(line.encode('utf-8') + b'\n')
        self.process.stdin.flush()
        reply = self.process.stdout.readline()
        elapsed = time.perf_counter() - started
        self.seconds += elapsed
        self.by_method[self.method] = self.by_method.get(self.method, 0) + elapsed
        if not reply:
            raise RuntimeError(f'{self.label} core stopped')
        return json.loads(reply)

    def close(self):
        self.process.stdin.close()
        self.process.wait(timeout=30)


def normalize(value, data_dir):
    if isinstance(value, str):
        return value.replace(data_dir, '<DATA>')
    if isinstance(value, list):
        return [normalize(item, data_dir) for item in value]
    if isinstance(value, dict):
        return {normalize(k, data_dir): normalize(v, data_dir) for k, v in value.items()}
    return value


def png_pixels(uri):
    from PIL import Image
    data = base64.b64decode(uri.split(',', 1)[1])
    with Image.open(io.BytesIO(data)) as image:
        return image.mode, image.size, image.tobytes()


def diff(a, b, path='$', out=None):
    out = [] if out is None else out
    if len(out) >= 20:
        return out
    if isinstance(a, dict) and isinstance(b, dict):
        # Reply key order is not significant to JSON consumers; catalog.json
        # text (including order) is compared separately.
        if sorted(a) != sorted(b):
            out.append(f'{path}: keys {list(a)[:12]} != {list(b)[:12]}')
        for key in a:
            # Map export job ids are random UUIDs in both cores.
            if key == 'id' and 'elapsedSeconds' in a:
                continue
            if key in b and key not in IGNORED_KEYS:
                if key == 'dataUri' and isinstance(a[key], str) and isinstance(b[key], str):
                    if png_pixels(a[key]) != png_pixels(b[key]):
                        out.append(f'{path}.dataUri: decoded pixels differ')
                    continue
                diff(a[key], b[key], f'{path}.{key}', out)
    elif isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            out.append(f'{path}: length {len(a)} != {len(b)}')
        for index, (x, y) in enumerate(zip(a, b)):
            diff(x, y, f'{path}[{index}]', out)
    elif isinstance(a, bool) or isinstance(b, bool):
        if a is not b:
            out.append(f'{path}: {a!r} != {b!r}')
    elif isinstance(a, (int, float)) and isinstance(b, (int, float)):
        if not math.isclose(a, b, rel_tol=1e-6, abs_tol=1e-6):
            out.append(f'{path}: {a!r} != {b!r}')
    elif a != b:
        out.append(f'{path}: {str(a)[:200]!r} != {str(b)[:200]!r}')
    return out


class Session:
    def __init__(self, go_binary, workspace):
        self.workspace = workspace
        app_dir = workspace / 'App'
        app_dir.mkdir()
        self.python = Core('Python', [sys.executable, str(REPO / 'app.py')], workspace / 'py-profile', app_dir)
        self.go = Core('Go', [go_binary], workspace / 'go-profile', app_dir)
        self.count = self.failures = 0
        self.by_method = {}

    def call(self, method, *args, raw=None, compare=True):
        left = self.python.call(method, *args, raw=raw)
        right = self.go.call(method, *args, raw=raw)
        self.count += 1
        stats = self.by_method.setdefault(method or 'raw', [0, 0])
        stats[0] += 1
        if compare:
            left, right = dict(left, id=None), dict(right, id=None)
            problems = diff(normalize(left, self.python.data_dir), normalize(right, self.go.data_dir))
            if problems:
                self.failures += 1
                stats[1] += 1
                print(f'DIFF {method} {json.dumps(args, ensure_ascii=False)[:160]}')
                for problem in problems[:8]:
                    print('   ', problem)
        return left

    def close(self):
        self.python.close()
        self.go.close()
        left = json.loads((self.workspace / 'py-profile' / 'catalog.json').read_text(encoding='utf-8'))
        right = json.loads((self.workspace / 'go-profile' / 'catalog.json').read_text(encoding='utf-8'))
        problems = diff(normalize(left, self.python.data_dir), normalize(right, self.go.data_dir), '$catalog')
        raw_left = (self.workspace / 'py-profile' / 'catalog.json').read_text(encoding='utf-8')
        raw_right = (self.workspace / 'go-profile' / 'catalog.json').read_text(encoding='utf-8')
        if not problems and raw_left.replace(self.python.data_dir, '<DATA>') != raw_right.replace(self.go.data_dir, '<DATA>'):
            problems.append('catalog.json text differs (formatting)')
        for problem in problems:
            print('CATALOG', problem)
        self.failures += bool(problems)


def run_library(go_binary, library):
    with tempfile.TemporaryDirectory() as temporary:
        workspace = Path(temporary).resolve()
        s = Session(go_binary, workspace)
        try:
            s.call('bootstrap')
            scan = s.call('scan_root', str(library))['result']['data']
            s.call('scan_root', str(library), False)
            entities = [item['path'] for item in scan['entities']]
            clips = [item['path'] for item in scan['clips']]
            for path in entities[:60]:
                loaded = s.call('load_entity', path)['result']
                if loaded['ok']:
                    for texture in list(loaded['data']['textures'])[:6]:
                        s.call('load_texture', texture)
                        s.call('load_texture', texture, 64)
                s.call('object_names', path)
                s.call('preview_entity', path)
            s.call('release_entity_preview')
            for path in clips[:120]:
                s.call('load_animation', path)
            for start in range(0, min(len(clips), 400), 128):
                s.call('read_clip_bindings', clips[start:start + 128])
            if clips:
                s.call('save_annotation', clips[0], {'name': 'Salut café 日本語', 'notes': 'n\nx', 'favorite': True})
                s.call('save_annotation', clips[0], {'name': 7, 'favorite': 'yes'})
            if entities:
                s.call('save_object_name', entities[0], 'scene', 'Renamed 日本語')
                s.call('save_object_name', entities[0], 'bone:1', 'x')
                s.call('save_object_name', entities[0], 'model:nope', 'x')
                s.call('object_names', entities[0])
                s.call('load_entity', entities[0])
            s.call('library_settings')
            s.call('library_settings', {'sort': 'name', 'hideDuplicates': False})
            s.call('library_settings', {'sort': 'bogus'})
            s.call('set_export_settings', {'enabled': True, 'upAxis': 'Z'})
            s.call('set_export_settings', {'enabled': 'yes'})
            output = '<DATA>/Exports 日本語'
            s.call('set_output_folder', str(output))
            s.call('set_output_folder', 'relative')
            if clips:
                s.call('export_animation', clips[0], 'Copy 日本語', str(output), {'clipEnabled': True, 'enabled': False, 'c4dEnabled': False})
                s.call('export_animation', clips[0], 'Copy 日本語', str(output), {'clipEnabled': True, 'enabled': False, 'c4dEnabled': False})
                s.call('export_animation', clips[0], 'CON', str(output), {'clipEnabled': True, 'enabled': False, 'c4dEnabled': False})
                s.call('export_animation', clips[0], 'x', None, {'clipEnabled': False, 'enabled': False, 'c4dEnabled': False})
                s.call('export_animation', clips[0], 'C4D', str(output), {'clipEnabled': False, 'enabled': False, 'c4dEnabled': True, 'entityPath': entities[0] if entities else ''})
            s.call('open_export_output')
            s.call('c4d_status')
            s.call('workflow_settings')
            s.call('workflow_settings', {'activeTab': 'maps', 'toolchainTab': 'maps'})
            s.call('workflow_settings', {'outputDirectory': 'relative'})
            s.call('use_library_directory')
            s.call('toolchain_validate')
            s.call('toolchain_status')
            s.call('open_toolchain_output')
            s.call('maps_settings')
            s.call('maps_settings', {'root': str(library), 'bakeTextures': False})
            s.call('maps_settings', {'nope': 1})
            maps = s.call('scan_maps', str(library))['result']
            if maps['ok']:
                for item in maps['data']['maps'][:3]:
                    s.call('load_map', item['path'])
                    s.call('object_names', item['path'])
            s.call('open_map_output')
            if maps['ok'] and maps['data']['maps']:
                # Background map export (no Cinema 4D connected): the job fails
                # identically on both cores; compare the final job snapshot.
                first = maps['data']['maps'][0]['path']
                s.call('export_map_start', first, 'Map export 日本語', None, '<DATA>/Map output', False, compare=False)
                finals = []
                for core in (s.python, s.go):
                    deadline = time.monotonic() + 120
                    while True:
                        status = core.call('export_map_status')['result']['data']
                        if not status['busy'] or time.monotonic() > deadline:
                            break
                        time.sleep(0.05)
                    finals.append(normalize(dict(status, id=None), core.data_dir))
                s.count += 1
                problems = diff(finals[0], finals[1], '$mapExport')
                if problems:
                    s.failures += 1
                    print('DIFF export_map_start', problems[:5])
                s.call('export_map', first, 'Map sync', None, '<DATA>/Map output', False)
                s.call('export_map', first, 'CON')
                s.call('export_map_start', first, 'x', None, None, 'yes')
            s.call('export_map_status')
            s.call('export_map_status', 'nope')
            # Export list: valid entries need a skeleton model and a clip.
            if entities and clips:
                loaded = s.python.call('load_entity', entities[0])['result']
                s.go.call('load_entity', entities[0])
                skeletons = [m['id'] for m in loaded['data']['models'] if m.get('bones')] if loaded['ok'] else []
                if skeletons:
                    entry = {'name': 'Setup 日本語.c4d', 'entityPath': entities[0], 'rootPath': str(library),
                             'assignments': [{'modelId': skeletons[0], 'clipPath': clips[0]}]}
                    saved_py = s.python.call('export_queue_save', entry)
                    saved_go = s.go.call('export_queue_save', entry)
                    ident_py = saved_py['result']['data']['entry']['id'] if saved_py['result']['ok'] else None
                    ident_go = saved_go['result']['data']['entry']['id'] if saved_go['result']['ok'] else None
                    problems = diff(normalize(saved_py, s.python.data_dir), normalize(saved_go, s.go.data_dir))
                    problems = [p for p in problems if '.id' not in p]
                    s.count += 1
                    if problems:
                        s.failures += 1
                        print('DIFF export_queue_save', problems[:5])
                    if ident_py and ident_go:
                        s.python.call('export_queue_remove', ident_py)
                        s.go.call('export_queue_remove', ident_go)
                        same = dict(entry, id='a' * 32)
                        s.call('export_queue_save', same)
                        s.call('export_queue_save', dict(entry, id='b' * 32))
                        s.call('export_queue_run', 'a' * 32)
                        s.call('export_queue_remove', 'a' * 32)
                        s.call('export_queue_remove', 'a' * 32)
                s.call('export_queue_save', {'name': 'x'})
                s.call('export_queue_save', {'bogus': 1})
                s.call('export_animation_set', entities[0], [], 'Scene')
                s.call('export_animation_set', entities[0], [{'modelId': 'x', 'clipPath': clips[0]}], 'Scene')
            # Analysis runs in the background on both cores.
            s.call('analysis_status', str(library), compare=False)
            for core in (s.python, s.go):
                deadline = time.monotonic() + 120
                while time.monotonic() < deadline and core.call('analysis_status', str(library))['result']['data']['busy']:
                    time.sleep(0.05)
            s.call('analysis_start', str(library))
            for core in (s.python, s.go):
                deadline = time.monotonic() + 120
                while time.monotonic() < deadline and core.call('analysis_status', str(library))['result']['data']['busy']:
                    time.sleep(0.05)
            s.call('analysis_status', str(library))
            s.call('analysis_index', str(library))
            for path in clips[:5]:
                s.call('analysis_detail', path)
                s.call('analysis_detail', path, str(library))
            # Argument and protocol errors.
            s.call('ping')
            s.call('ping', 1)
            s.call('save_annotation')
            s.call('load_entity', 5)
            s.call('load_entity', None)
            s.call('load_entity', 'relative.owentity')
            s.call('scan_root', None)
            s.call('scan_root', 12)
            s.call('save_annotation', '/x', 'text')
            s.call('load_texture', 'unregistered')
            s.call('load_texture', 'x', 1.5)
            s.call('read_clip_bindings', 'x')
            s.call('object_names', 'relative')
            s.call('export_queue_remove', 'x')
            s.call('export_queue_run', 'f' * 32)
            s.call('browse_root')
            s.call('browse_workflow_directory', 'x')
            for raw in ('{bad json', '[]', '{"id": true, "method": "ping"}', '{"id": 1, "method": "_save"}',
                        '{"id": 2, "method": "ping", "args": {}}', '{"id": "x", "method": "bootstrap", "args": null}'):
                s.call(None, raw=raw)
            s.call('bootstrap')
        finally:
            s.close()
        return s


def main():
    go_binary = sys.argv[1]
    libraries = [Path(p) for p in sys.argv[2:]] or [REPO / 'tests/demo-assets', REPO / 'tests/material-assets'] + (
        [BENCH] if BENCH.is_dir() else [])
    failed = False
    for library in libraries:
        s = run_library(go_binary, library)
        print(f'{library}: {s.count} replies compared, {s.failures} differing; '
              f'Python {s.python.seconds:.2f}s, Go {s.go.seconds:.2f}s')
        slowest = sorted(s.go.by_method, key=s.go.by_method.get, reverse=True)[:6]
        print('   slowest (Python s / Go s): ' + ', '.join(
            f'{name} {s.python.by_method.get(name, 0):.2f}/{s.go.by_method[name]:.2f}' for name in slowest))
        failed |= bool(s.failures)
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
