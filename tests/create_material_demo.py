"""Generate original textured/skinned test geometry; contains no game assets."""

from pathlib import Path
import argparse
import struct
import sys
import zlib

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from test_formats import clip_bytes, entity_bytes, pack, string
from create_demo import BONES, _cube, quaternion


def material_bytes(textures=(), inputs=(), shader=0):
    blob = pack('HHIQQI', 3, 0, 0, len(textures), len(inputs), shader)
    for reference, binding in textures:
        blob += string(reference) + pack('I', binding)
    for key, value in inputs:
        blob += pack('II', key, len(value)) + value
    return blob


def look_bytes(entries=()):
    return pack('HHIQ', 3, 0, 1, len(entries)) + b''.join(
        pack('Q', key) + string(reference) for key, reference in entries)


def png_bytes(width, height, pixel):
    """Deterministic RGBA PNG encoder using only the standard library."""
    def chunk(kind, value):
        return struct.pack('>I', len(value)) + kind + value + struct.pack('>I', zlib.crc32(kind + value))
    raw = b''.join(b'\0' + bytes(component for x in range(width) for component in pixel(x, y))
                   for y in range(height))
    return (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', width, height, 8, 6, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(raw, 9)) + chunk(b'IEND', b''))


def textured_model():
    meshes = [
        ('Body', (0, 1.30, 0), (.65, .83, .32), 0),
        ('Head', (0, 1.99, 0), (.46, .38, .37), 0),
        ('Left leg', (-.16, .44, 0), (.22, .88, .25), 0),
        ('Right leg', (.16, .44, 0), (.22, .88, .25), 0),
        ('Left arm', (-.69, 1.65, 0), (.67, .22, .26), 1),
        ('Left hand', (-1.07, 1.65, 0), (.18, .27, .29), 1),
        ('Right arm', (.69, 1.65, 0), (.67, .22, .26), 2),
        ('Right hand', (1.07, 1.65, 0), (.18, .27, .29), 2),
    ]
    blob = pack('HH', 2, 0) + string('ModelLooks/Default.owmat') + string('Material Robot (synthetic)')
    blob += pack('IHII', 1, len(BONES), len(meshes), 0)
    for name, parent, position in BONES:
        blob += string(name) + pack('h', parent) + pack('9f', *position, 1, 1, 1, 0, 0, 0)
    for mesh in meshes:
        # Add a distinct second UV set to the original rigidly skinned box.
        cube = bytearray(_cube(*mesh))
        header = len(string(mesh[0]))
        cube[header + 8] = 2
        uv_end = header + struct.calcsize('<QBIIB') + 24 * (12 + 12 + 16 + 8)
        uv2 = pack('48f', *((.125, .125, .875, .125, .875, .875, .125, .875) * 6))
        blob += cube[:uv_end] + uv2 + cube[uv_end:]
    return blob


def generate(destination=None):
    root = Path(destination) if destination else Path(__file__).resolve().parent / 'material-assets'
    root.mkdir(parents=True, exist_ok=True)

    def write(relative, value):
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(value)
        return path

    model_root = 'Models/MaterialRobot.00C/'
    write(model_root + 'MaterialRobot.owmdl', textured_model())
    write('Entities/MaterialRobot/MaterialRobot.owentity', entity_bytes(name='Material Robot', model='MaterialRobot.00C'))
    write(model_root + 'ModelLooks/Default.owmat', look_bytes([
        (1, '../../Materials/000000B001.owmat'),
        (2, '../../Materials/000000B002.owmat'),
        (3, '../../Materials/000000B003.owmat')]))
    paths = lambda *entries: [('../Textures/' + name + '.png', binding) for name, binding in entries]
    write(model_root + 'Materials/000000B001.owmat', material_bytes(paths(
        ('000000A001', 2903569922), ('000000A004', 378934698), ('000000A005', 548341454),
        ('000000A006', 3335614873), ('000000A007', 3166598269)), [(3250491852, pack('f', 2))]))
    write(model_root + 'Materials/000000B002.owmat', material_bytes(paths(
        ('000000A002', 2903569922), ('000000A004', 378934698), ('000000A005', 548341454))))
    write(model_root + 'Materials/000000B003.owmat', material_bytes(paths(
        ('000000A003', 1239794147), ('000000A004', 378934698), ('000000A005', 1117188170)), shader=51))
    pixels = {
        '000000A001': lambda x, y: (220, 70, 45, 255) if (x//8 + y//8)%2 else (235, 225, 190, 255),
        '000000A002': lambda x, y: (20, 170, 220, 255) if (x//4 + y//4)%2 else (25, 60, 80, 255),
        '000000A003': lambda x, y: (185, 110, 230, 0 if x%8 < 2 else 255),
        '000000A004': lambda x, y: (160 if x%8 < 4 else 96, 160 if y%8 < 4 else 96, 32, 255),
        '000000A005': lambda x, y: (220 if x < 16 else 100, 180 if y < 16 else 60, 0, 255),
        '000000A006': lambda x, y: (150 if (x//8+y//8)%2 else 255,) * 3 + (255,),
        '000000A007': lambda x, y: (35 if 13 <= y < 18 else 0,) * 3 + (255,),
    }
    for name, pixel in pixels.items():
        write(model_root + 'Textures/' + name + '.png', png_bytes(32, 32, pixel))
    write('Animations/001_Wave.owanimclip', clip_bytes([
        ('bone_0003', [(1, [(frame, quaternion(2, angle)) for frame, angle in
                          [(0, 0), (15, 80), (30, 40), (45, 85), (60, 0)]])])], duration=61))
    write('Animations/002_Turn.owanimclip', clip_bytes([
        ('bone_0001', [(1, [(frame, quaternion(1, angle)) for frame, angle in
                          [(0, 0), (30, 80), (60, 0), (90, -80), (120, 0)]])])], duration=121))
    (root / 'README.txt').write_text(
        'ORIGINAL SYNTHETIC MATERIAL TEST ASSETS\n\n'
        'Generated by tests/create_material_demo.py; no game artwork.\n'
        'Load Entities/MaterialRobot/MaterialRobot.owentity with this folder as export root.\n'
        'Eight rigidly skinned boxes, three bones, two clips, two distinct UV sets.\n'
        'Body: red/cream checker, decoded normal, packed PBR, UV2 AO, masked tinted emission.\n'
        'Left arm: cyan checker, decoded normal, packed PBR.\n'
        'Right arm: lavender stripes with explicit hair alpha cutout and roughness map.\n'
        'Textures deliberately use arbitrary IDs; OWMAT binding hashes select their role.\n', encoding='utf-8')
    return root


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    print(generate(parser.parse_args().output))
