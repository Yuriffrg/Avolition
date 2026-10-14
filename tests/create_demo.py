"""Create original, synthetic OW2 fixtures for end-to-end viewport testing.

Run from the application folder: python tests/create_demo.py
These simple geometric shapes contain no Blizzard/game asset data.
"""

from __future__ import annotations

import argparse
import math
from pathlib import Path
import sys

# Share exactly the same .NET string and OWLib-layout encoders as parser tests.
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from test_formats import clip_bytes, entity_bytes, pack, string


BONES = [('bone_0001', -1, (0, 1, 0)),
         ('bone_0002', 0, (-.36, .65, 0)),
         ('bone_0003', 0, (.36, .65, 0))]


def _cube(name, center, size, bone_index):
    """One hard-normal box with consistent outward winding and rigid skinning."""
    cx, cy, cz = center
    hx, hy, hz = (value / 2 for value in size)
    faces = [
        ((1, 0, 0), [(1,-1,-1), (1,1,-1), (1,1,1), (1,-1,1)]),
        ((-1, 0, 0), [(-1,-1,1), (-1,1,1), (-1,1,-1), (-1,-1,-1)]),
        ((0, 1, 0), [(-1,1,-1), (-1,1,1), (1,1,1), (1,1,-1)]),
        ((0, -1, 0), [(-1,-1,1), (-1,-1,-1), (1,-1,-1), (1,-1,1)]),
        ((0, 0, 1), [(1,-1,1), (1,1,1), (-1,1,1), (-1,-1,1)]),
        ((0, 0, -1), [(-1,-1,-1), (-1,1,-1), (1,1,-1), (1,-1,-1)]),
    ]
    positions, normals, indices = [], [], []
    for face_index, (normal, corners) in enumerate(faces):
        for x, y, z in corners:
            # OWLib exports +90 X: (x, -z, y). Reader undoes this once.
            positions.extend((cx + x*hx, -(cz + z*hz), cy + y*hy))
            normals.extend((normal[0], -normal[2], normal[1]))
        start = face_index * 4
        indices.extend((start, start+1, start+2, start, start+2, start+3))
    vertex_count = len(positions) // 3
    blob = string(name) + pack('QBIIB', bone_index+1, 1, vertex_count, len(indices)//3, 1)
    blob += pack(f'{len(positions)}f', *positions)
    blob += pack(f'{len(normals)}f', *normals)
    blob += pack(f'{vertex_count*4}f', *((1, 0, 0, 1) * vertex_count))
    blob += pack(f'{vertex_count*2}f', *((0, 0, 1, 0, 1, 1, 0, 1) * 6))
    blob += pack(f'{vertex_count}h', *([bone_index] * vertex_count))
    blob += pack(f'{vertex_count}f', *([1] * vertex_count))
    blob += pack(f'{vertex_count*8}f', *((1, 1, 1, 1) * vertex_count * 2))
    blob += pack(f'{len(indices)}I', *indices)
    return blob


def robot_model():
    meshes = [
        ('Torso', (0, 1.38, 0), (.65, .66, .32), 0),
        ('Pelvis', (0, .91, 0), (.49, .26, .32), 0),
        ('Neck', (0, 1.78, 0), (.16, .2, .16), 0),
        ('Head', (0, 2, 0), (.46, .37, .37), 0),
        ('Left eye', (-.12, 2.04, .21), (.1, .075, .05), 0),
        ('Right eye', (.12, 2.04, .21), (.1, .075, .05), 0),
        ('Left leg', (-.16, .44, 0), (.22, .69, .25), 0),
        ('Right leg', (.16, .44, 0), (.22, .69, .25), 0),
        ('Left foot', (-.16, .085, .1), (.26, .17, .44), 0),
        ('Right foot', (.16, .085, .1), (.26, .17, .44), 0),
        ('Left arm', (-.67, 1.65, 0), (.6, .19, .23), 1),
        ('Left hand', (-1.02, 1.65, 0), (.16, .24, .27), 1),
        ('Right arm', (.67, 1.65, 0), (.6, .19, .23), 2),
        ('Right hand', (1.02, 1.65, 0), (.16, .24, .27), 2),
    ]
    blob = pack('HH', 2, 0) + string('') + string('Preview Robot (synthetic)')
    blob += pack('IHII', 1, len(BONES), len(meshes), 0)
    for name, parent, position in BONES:
        blob += string(name) + pack('h', parent) + pack('9f', *position, 1, 1, 1, 0, 0, 0)
    for mesh in meshes:
        blob += _cube(*mesh)
    return blob


def quaternion(axis, degrees):
    angle = math.radians(degrees) / 2
    values = [0, 0, 0, math.cos(angle)]
    values[axis] = math.sin(angle)
    return tuple(values)


def generate(destination=None):
    root = Path(destination) if destination else Path(__file__).resolve().parent / 'demo-assets'
    root.mkdir(parents=True, exist_ok=True)

    def write(relative, contents):
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(contents)

    write('Models/PreviewRobot.00C/PreviewRobot.owmdl', robot_model())
    write('Entities/PreviewRobot/PreviewRobot.owentity',
          entity_bytes(name='Preview Robot', model='PreviewRobot.00C'))

    wave_keys = [(0, 0), (15, 80), (25, 40), (35, 85), (45, 40), (55, 85), (70, 0), (90, 0)]
    write('Animations/001_Wave.owanimclip', clip_bytes(
        bones=[('bone_0003', [(1, [(frame, quaternion(2, angle)) for frame, angle in wave_keys])])],
        duration=91))
    write('Animations/002_Turn.owanimclip', clip_bytes(
        bones=[('bone_0001', [(1, [(frame, quaternion(1, angle)) for frame, angle in
                                 [(0, 0), (30, 80), (60, 0), (90, -80), (120, 0)]])])],
        duration=121))
    write('Animations/003_Root_Move.owanimclip', clip_bytes(
        bones=[('bone_0001', [(0, [(0, (0, 0, 0)), (30, (1.5, 0, 0)),
                                 (60, (0, .6, 0)), (90, (-1.5, 0, 0)), (120, (0, 0, 0))])]),
               ('bone_0002', [(1, [(0, quaternion(2, 0)), (60, quaternion(2, -40)), (120, quaternion(2, 0))])]),
               ('bone_0003', [(1, [(0, quaternion(2, 0)), (60, quaternion(2, 40)), (120, quaternion(2, 0))])])],
        duration=121))
    (root / 'README.txt').write_text(
        'SYNTHETIC PREVIEW TEST ASSETS\n\n'
        'Original geometric robot generated by tests/create_demo.py. No game assets.\n'
        'Set the browser root to this folder, then load PreviewRobot.owentity.\n'
        '001_Wave: right arm waves overhead, 3 seconds.\n'
        '002_Turn: turns 80 degrees left and right around the vertical axis, 4 seconds.\n'
        '003_Root_Move: side-to-side movement and a hop, 4 seconds.\n'
        'All clips are 30 fps. Robot has three bones and fourteen rigidly skinned boxes.\n',
        encoding='utf-8')
    return root


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    print(generate(args.output))
