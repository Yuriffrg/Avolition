"""Generate a large synthetic export library for benchmarking both cores.

Uses the same OWLib-layout encoders as the Python tests; contains no game data.
Usage: python3 gocore/bench/make_library.py OUTPUT [--scale 1.0]
"""
import argparse
from array import array
import math
from pathlib import Path
import random
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / 'tests'), str(ROOT)]
from test_formats import clip_bytes, entity_bytes, pack, string  # noqa: E402
from create_material_demo import look_bytes, material_bytes  # noqa: E402
from test_map_formats import map_bytes, record_bytes  # noqa: E402
from PIL import Image  # noqa: E402

BINDINGS = (2903569922, 378934698, 548341454, 3335614873)  # albedo, normal, PBR, AO


def grid_mesh(name, material_key, columns, rows, bones, offset, uv_count=2, slots=4):
    """A wavy grid of columns*rows vertices, skinned across up to `bones` joints."""
    v = columns * rows
    positions, normals, uvs = array('f'), array('f'), array('f')
    for j in range(rows):
        for i in range(columns):
            x, z = i / columns + offset, j / rows
            y = 0.05 * math.sin(i * 0.3) * math.cos(j * 0.2)
            positions.extend((x, -z, y + 1.0))  # OWLib +90 X layout
            normals.extend((0.0, 0.0, 1.0))
            uvs.extend((i / columns, j / rows))
    faces = array('I')
    for j in range(rows - 1):
        for i in range(columns - 1):
            a = j * columns + i
            faces.extend((a, a + 1, a + columns, a + 1, a + columns + 1, a + columns))
    indices = array('H', [(k // 7) % bones if bones else 0 for k in range(v)] * slots) if slots else array('H')
    weights = array('f', [1.0 / slots] * (v * slots)) if slots else array('f')
    blob = string(name) + pack('QBIIB', material_key, uv_count, v, len(faces) // 3, slots)
    blob += positions.tobytes() + normals.tobytes() + array('f', (1, 0, 0, 1) * v).tobytes()
    for _ in range(uv_count):
        blob += uvs.tobytes()
    blob += indices.tobytes() + weights.tobytes() + bytes(32 * v) + faces.tobytes()
    return blob


def model_bytes(name, bone_count, meshes, look='ModelLooks/Default.owmat'):
    blob = pack('HH', 2, 0) + string(look) + string(name)
    blob += pack('IHII', 1, bone_count, len(meshes), 0)
    for b in range(bone_count):
        parent = -1 if b == 0 else (b - 1) // 2
        blob += string(f'bone_{b + 1:04x}') + pack('h', parent) + pack('9f', 0, 0.05, 0, 1, 1, 1, 0, 0, 0)
    return blob + b''.join(meshes)


def clip(bone_count, frames, seed):
    rng = random.Random(seed)
    bones = []
    for b in range(bone_count):
        keys = []
        for f in range(0, frames, 2):
            angle = math.radians(rng.uniform(-30, 30)) / 2
            keys.append((f, (math.sin(angle), 0, 0, math.cos(angle))))
        tracks = [(1, keys)]
        if b == 0:
            tracks.append((0, [(f, (f * 0.01, 0, 0)) for f in range(0, frames, 2)]))
        bones.append((f'bone_{b + 1:04x}', tracks))
    return clip_bytes(bones, duration=frames)


def texture(path, size, seed):
    rng = random.Random(seed)
    base = Image.radial_gradient('L').resize((size, size))
    noise = Image.effect_noise((size, size), 40 + rng.random() * 20)
    rgb = Image.merge('RGB', (base, noise, Image.linear_gradient('L').resize((size, size))))
    path.parent.mkdir(parents=True, exist_ok=True)
    rgb.convert('RGBA').save(path, compress_level=6)


def write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def textured_set(root, model_dir, prefix, textures, size, seed):
    write(root / model_dir / 'ModelLooks/Default.owmat', look_bytes([(1, '../Materials/M1.owmat')]))
    refs = []
    for t in range(textures):
        name = f'{prefix}{t:03d}'
        texture(root / model_dir / 'Textures' / f'{name}.png', size, seed + t)
        refs.append((f'../Textures/{name}.png', BINDINGS[t % len(BINDINGS)]))
    write(root / model_dir / 'Materials/M1.owmat', material_bytes(refs))


def generate(out, scale):
    out = Path(out)
    heroes, skins = max(1, round(12 * scale)), 2
    # Characters: realistic rigs, many clips per skin.
    for h in range(heroes):
        for s in range(skins):
            base = out / f'Heroes/Hero{h:02d}/Skin{s}'
            name = f'Hero{h:02d}S{s}'
            meshes = [grid_mesh(f'Mesh {m}', 1, 70, 70, 120, m) for m in range(8)]
            write(base / f'Models/{name}.00C/{name}.owmdl', model_bytes(name, 120, meshes))
            textured_set(base, f'Models/{name}.00C', name, 4, 1024, h * 10 + s)
            for kind in ('Gameplay3P', 'Gameplay1P'):
                write(base / f'Entities/{kind}/{kind}.owentity', entity_bytes(name=kind, model=f'{name}.00C'))
            for c in range(round(30 * scale) or 1):
                write(base / f'Animations/{c:03d}_Clip.owanimclip', clip(120, 60, h * 1000 + s * 100 + c))
    # One very heavy character: 16 meshes x 40k vertices, 200 bones, 2K textures.
    big = out / 'Heroes/Big/Skin0'
    meshes = [grid_mesh(f'Big mesh {m}', 1, 200, 200, 200, m) for m in range(16)]
    write(big / 'Models/Big.00C/Big.owmdl', model_bytes('Big', 200, meshes))
    textured_set(big, 'Models/Big.00C', 'Big', 6, 2048, 7)
    write(big / 'Entities/Gameplay3P/Gameplay3P.owentity', entity_bytes(name='Gameplay3P', model='Big.00C'))
    write(big / 'Animations/000_Big.owanimclip', clip(200, 240, 99))
    # A map: unique props with looks and textures, many placements.
    # DataTool layout: map/variant/<file>.owmap, with references inside the variant folder.
    maproot = out / 'Maps/Bench/Bench/Default'
    props = max(10, round(300 * scale))
    rng = random.Random(5)
    objects = []
    for p in range(props):
        model_dir = f'Models/Prop{p:03d}'
        write(maproot / model_dir / f'Prop{p:03d}.owmdl',
              model_bytes(f'Prop{p:03d}', 0, [grid_mesh('Prop', 4000, 40, 40, 0, 0, uv_count=1, slots=0)], look=''))
        tex = f'Textures/T{p % 80:03d}.png'
        if not (maproot / tex).exists():
            texture(maproot / tex, 512, p)
        write(maproot / model_dir / 'ModelLooks/Look.owmat', look_bytes([(4000, '../Materials/Mat.owmat')]))
        write(maproot / model_dir / 'Materials/Mat.owmat',
              material_bytes([(f'../../../{tex}', BINDINGS[0])]))
        records = [record_bytes(position=(rng.uniform(-200, 200), rng.uniform(0, 20), rng.uniform(-200, 200)),
                                rotation=(0, math.sin(a := rng.uniform(0, 3.14)), 0, math.cos(a)))
                   for _ in range(12)]
        objects.append((f'{model_dir}/Prop{p:03d}.owmdl', [(f'{model_dir}/ModelLooks/Look.owmat', records)]))
    write(maproot / 'Bench.owmap', map_bytes(objects=objects, name='Bench map'))
    return out



if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output')
    parser.add_argument('--scale', type=float, default=1.0)
    args = parser.parse_args()
    print(generate(args.output, args.scale))
