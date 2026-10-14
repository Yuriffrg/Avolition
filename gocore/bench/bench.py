"""Drive a core over the --stdio protocol and report time, reply size and memory.

Usage: python3 gocore/bench/bench.py [--label NAME] [--json OUT] LIBRARY -- CORE_COMMAND...
Memory is the peak resident set of the core and all its child processes,
sampled every 5 ms, so a Go core that still delegates to Python is charged
for both processes.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time


def tree_rss_mb(pid):
    children = {}
    for entry in os.listdir('/proc'):
        if entry.isdigit():
            try:
                with open(f'/proc/{entry}/stat') as handle:
                    ppid = int(handle.read().rsplit(')', 1)[1].split()[1])
                children.setdefault(ppid, []).append(int(entry))
            except (OSError, ValueError, IndexError):
                pass
    total, stack = 0, [pid]
    while stack:
        current = stack.pop()
        try:
            with open(f'/proc/{current}/statm') as handle:
                total += int(handle.read().split()[1]) * os.sysconf('SC_PAGE_SIZE')
        except OSError:
            pass
        stack.extend(children.get(current, []))
    return total / 2**20


class Core:
    def __init__(self, command, data_dir, app_dir):
        self.started = time.perf_counter()
        self.process = subprocess.Popen(command + ['--stdio', '--data-dir', data_dir, '--app-dir', app_dir],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self.next_id = 0
        self.peak = 0.0
        self.window_peak = 0.0
        self.running = True
        threading.Thread(target=self._sample, daemon=True).start()

    def _sample(self):
        while self.running:
            rss = tree_rss_mb(self.process.pid)
            self.peak = max(self.peak, rss)
            self.window_peak = max(self.window_peak, rss)
            time.sleep(0.005)

    def call(self, method, *args):
        self.next_id += 1
        self.window_peak = 0.0
        start = time.perf_counter()
        self.process.stdin.write(json.dumps({'id': self.next_id, 'method': method, 'args': list(args)}).encode() + b'\n')
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        elapsed = time.perf_counter() - start
        reply = json.loads(line)
        if not reply['result']['ok']:
            raise RuntimeError(f"{method} failed: {reply['result']['error']}")
        return reply['result']['data'], elapsed, len(line)

    def close(self):
        self.process.stdin.close()
        self.process.wait(60)
        self.running = False


def textures_of(asset):
    ids = set()
    def walk(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key == 'textureId' and isinstance(item, str):
                    ids.add(item)
                else:
                    walk(item)
        elif isinstance(value, list):
            for item in value:
                walk(item)
    walk(asset)
    return sorted(ids)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('library')
    parser.add_argument('command', nargs='+')
    parser.add_argument('--label', default='core')
    parser.add_argument('--json')
    args = parser.parse_args()
    library = Path(args.library).resolve()
    app_dir = str(Path(__file__).resolve().parents[2])
    rows = []
    with tempfile.TemporaryDirectory() as data_dir:
        core = Core(args.command, data_dir, app_dir)

        def step(label, method, *call_args, repeat=1):
            total, size, peak = 0.0, 0, 0.0
            data = None
            for _ in range(repeat):
                data, elapsed, size = core.call(method, *call_args)
                total += elapsed
                peak = max(peak, core.window_peak)
            rows.append({'step': label, 'ms': total / repeat * 1000, 'replyMB': size / 2**20, 'peakMB': peak})
            return data

        step('startup + bootstrap', 'bootstrap')
        rows[-1]['ms'] = (time.perf_counter() - core.started) * 1000
        step('scan_root (721 clips, 49 entities)', 'scan_root', str(library))
        big = library / 'Heroes/Big/Skin0/Entities/Gameplay3P/Gameplay3P.owentity'
        asset = step('load_entity (640k vertices, 200 bones)', 'load_entity', str(big))
        textures = textures_of(asset)
        start = time.perf_counter()
        sizes = 0
        for texture in textures:
            _, _, size = core.call('load_texture', texture, 2048)
            sizes += size
        rows.append({'step': f'load_texture x{len(textures)} (2048px)', 'ms': (time.perf_counter() - start) * 1000,
                     'replyMB': sizes / 2**20, 'peakMB': core.window_peak})
        step('load_animation (200 bones, 240 frames)', 'load_animation',
             str(library / 'Heroes/Big/Skin0/Animations/000_Big.owanimclip'))
        clips = sorted(library.glob('Heroes/Hero00/Skin0/Animations/*.owanimclip'))
        start = time.perf_counter()
        for clip in clips:
            core.call('load_animation', str(clip))
        rows.append({'step': f'load_animation x{len(clips)} (120 bones)', 'ms': (time.perf_counter() - start) * 1000,
                     'replyMB': 0, 'peakMB': core.window_peak})
        hero = library / 'Heroes/Hero00/Skin0/Entities/Gameplay3P/Gameplay3P.owentity'
        step('load_entity x10 (35k vertices)', 'load_entity', str(hero), repeat=10)
        step('scan_maps', 'scan_maps', str(library / 'Maps'))
        maps = step('load_map (300 props, 3600 placements)', 'load_map',
                    str(library / 'Maps/Bench/Bench/Default/Bench.owmap'), str(library / 'Maps/Bench'))
        map_textures = textures_of(maps)
        start = time.perf_counter()
        for texture in map_textures:
            core.call('load_texture', texture, 512)
        rows.append({'step': f'map load_texture x{len(map_textures)} (512px)',
                     'ms': (time.perf_counter() - start) * 1000, 'replyMB': 0, 'peakMB': core.window_peak})
        core.close()
        rows.append({'step': 'peak memory (whole session)', 'ms': 0, 'replyMB': 0, 'peakMB': core.peak})
    print(f'\n{args.label}')
    for row in rows:
        print(f"  {row['step']:<44} {row['ms']:9.1f} ms  {row['replyMB']:7.1f} MB reply  {row['peakMB']:7.1f} MB peak")
    if args.json:
        Path(args.json).write_text(json.dumps({'label': args.label, 'rows': rows}, indent=2))


if __name__ == '__main__':
    main()
