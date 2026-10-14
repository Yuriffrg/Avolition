"""Read DataTool's OW2 model, entity and animation preview data.

SPDX-License-Identifier: GPL-3.0-or-later

Format layouts are based on the supplied OWLib/TankLib exporters and the
io_scene_owm readers shipped with Overwatch Tools. This module needs neither
Blender nor Cinema 4D. All output transforms use raw OW Y-up coordinates,
quaternions in XYZW order and column-major matrices for Three.js.

The exporter rotates mesh vertices +90 degrees around X but leaves bones and
animation tracks in the original coordinates. Undo only that mesh conversion.
Bone Euler rotations are Blender XYZ (Rz @ Ry @ Rx); rest scale is one, matching
the successfully tested Blender/C4D armature importer. Clip tracks are returned
as exported; the playback evaluator applies Blender's parent-local child poses
and rest-basis root-position behavior.
"""

from __future__ import annotations

import math
import os
from pathlib import Path
import struct
from typing import Any


class FormatError(ValueError):
    """An unsupported, incomplete or inconsistent exported asset."""


MAX_FILE_BYTES = 512 * 1024 * 1024
MAX_VERTICES = 2_000_000
MAX_FACES = 4_000_000
MAX_MODELS = 512
MAX_ENTITY_DEPTH = 48
IDENTITY = [1.0, 0.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0,
            0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 0.0, 1.0]


class _Reader:
    def __init__(self, path: Path, data: bytes):
        self.path = path
        self.data = memoryview(data)
        self.offset = 0

    @classmethod
    def file(cls, path: Path):
        size = path.stat().st_size
        if size > MAX_FILE_BYTES:
            raise FormatError(f"{path.name}: file exceeds the 512 MB preview limit")
        return cls(path, path.read_bytes())

    @property
    def remaining(self):
        return len(self.data) - self.offset

    def fail(self, message):
        raise FormatError(f"{self.path.name}, byte {self.offset}: {message}")

    def take(self, size: int):
        if size < 0 or size > self.remaining:
            self.fail(f"truncated data (need {size:,} bytes, have {self.remaining:,})")
        start = self.offset
        self.offset += size
        return self.data[start:self.offset]

    def unpack(self, fmt: str):
        return struct.unpack('<' + fmt, self.take(struct.calcsize('<' + fmt)))

    def string(self):
        # .NET BinaryWriter strings use a 7-bit-encoded UTF-8 byte count. Unlike
        # the legacy reader this also handles exact 128-byte strings correctly.
        length = 0
        for shift in range(0, 35, 7):
            value = self.unpack('B')[0]
            length |= (value & 0x7f) << shift
            if not value & 0x80:
                break
        else:
            self.fail("invalid string length")
        if length > 1024 * 1024:
            self.fail("string exceeds 1 MB")
        try:
            result = self.take(length).tobytes().decode('utf-8')
        except UnicodeDecodeError:
            self.fail("invalid UTF-8 string")
        return '' if result == 'null' else result

    def values(self, kind: str, count: int):
        if count < 0:
            self.fail("negative array count")
        raw = self.take(struct.calcsize('<' + kind) * count)
        values = list(struct.unpack(f'<{count}{kind}', raw))
        if kind == 'f' and not all(math.isfinite(value) for value in values):
            self.fail("non-finite numeric value")
        return values

    def count(self, count, maximum, label):
        if not 0 <= count <= maximum:
            self.fail(f"invalid {label}: {count:,} (preview limit {maximum:,})")


def _version(reader, major, minor, kind):
    if major != 2:
        reader.fail(f"unsupported {kind} version {major}.{minor}; expected OW2 version 2.x")
    if kind == 'entity' and minor not in (0, 1):
        reader.fail(f"unsupported entity version {major}.{minor}; expected 2.0 or 2.1")


def _path(path, extension):
    path = Path(path).expanduser().resolve()
    if path.suffix.lower() != extension:
        raise FormatError(f"Select an {extension} file: {path.name}")
    if not path.is_file():
        raise FileNotFoundError(f"File not found: {path}")
    return path


def _normalize_quaternion(values):
    length = math.sqrt(sum(value * value for value in values))
    if length < 1e-12:
        return [0.0, 0.0, 0.0, 1.0]
    return [value / length for value in values]


def _euler_xyz(values):
    x, y, z = (v * 0.5 for v in values)
    cx, sx, cy, sy, cz, sz = (math.cos(x), math.sin(x), math.cos(y),
                            math.sin(y), math.cos(z), math.sin(z))
    return _normalize_quaternion([sx * cy * cz - cx * sy * sz,
                                  cx * sy * cz + sx * cy * sz,
                                  cx * cy * sz - sx * sy * cz,
                                  cx * cy * cz + sx * sy * sz])


def _matrix(position, rotation):
    x, y, z, w = rotation
    return [1 - 2 * (y*y + z*z), 2 * (x*y + z*w), 2 * (x*z - y*w), 0.0,
            2 * (x*y - z*w), 1 - 2 * (x*x + z*z), 2 * (y*z + x*w), 0.0,
            2 * (x*z + y*w), 2 * (y*z - x*w), 1 - 2 * (x*x + y*y), 0.0,
            *position, 1.0]


def _multiply(a, b):
    return [sum(a[k*4 + row] * b[col*4 + k] for k in range(4))
            for col in range(4) for row in range(4)]


def _inverse_rigid(m):
    inverse = [m[0], m[4], m[8], 0.0,
               m[1], m[5], m[9], 0.0,
               m[2], m[6], m[10], 0.0, 0.0, 0.0, 0.0, 1.0]
    for row in range(3):
        inverse[12 + row] = -sum(inverse[k*4 + row] * m[12 + k] for k in range(3))
    return inverse


def _bone_globals(bones, reader):
    """Resolve arbitrary parent order and reject cycles without recursion."""
    result = [None] * len(bones)
    for start in range(len(bones)):
        trail, visiting = [], set()
        index = start
        while index != -1 and result[index] is None:
            if index in visiting:
                reader.fail("cycle in bone hierarchy")
            visiting.add(index)
            trail.append(index)
            index = bones[index]['parent']
        parent_matrix = IDENTITY if index == -1 else result[index]
        for index in reversed(trail):
            bone = bones[index]
            parent_matrix = _multiply(parent_matrix, _matrix(bone['pos'], bone['rot']))
            result[index] = parent_matrix
    return result


def _to_y_up(values):
    result = []
    for offset in range(0, len(values), 3):
        x, y, z = values[offset:offset + 3]
        result.extend((x, z, -y))
    return result


def _skin(indices, weights, bone_count, vertex_count, slots):
    out_indices, out_weights = [], []
    orphan_count = 0
    for vertex in range(vertex_count):
        combined = {}
        for entry in range(vertex * slots, (vertex + 1) * slots):
            index, weight = indices[entry], weights[entry]
            if 0 <= index < bone_count and weight > 0:
                combined[index] = combined.get(index, 0.0) + weight
        selected = sorted(combined.items(), key=lambda pair: pair[1], reverse=True)[:4]
        total = sum(pair[1] for pair in selected)
        if not total:
            # A zero-weight Three.js vertex collapses to the origin. Use the
            # first joint for anomalous unweighted vertices and report it.
            selected, total = [(0, 1.0)], 1.0
            orphan_count += 1
        out_indices.extend([pair[0] for pair in selected] + [0] * (4 - len(selected)))
        out_weights.extend([pair[1] / total for pair in selected] + [0.0] * (4 - len(selected)))
    return out_indices, out_weights, orphan_count


def _load_model(path: Path, warnings: list):
    r = _Reader.file(path)
    major, minor = r.unpack('HH')
    _version(r, major, minor, 'model')
    material, name = r.string(), r.string()
    guid, bone_count, mesh_count, socket_count = r.unpack('IHII')
    r.count(mesh_count, 16384, 'mesh count')
    r.count(socket_count, 65536, 'socket count')
    bones = []
    bone_names = set()
    for index in range(bone_count):
        bone_name = r.string()
        parent = r.unpack('h')[0]
        pos, source_scale, rot = r.values('f', 3), r.values('f', 3), r.values('f', 3)
        if not -1 <= parent < bone_count or parent == index:
            r.fail(f"invalid parent {parent} for bone {index}")
        if not bone_name or bone_name in bone_names:
            r.fail(f"missing or duplicate bone name: {bone_name!r}")
        bone_names.add(bone_name)
        bones.append({'name': bone_name, 'sourceName': bone_name, 'parent': parent,
                      'pos': pos, 'rot': _euler_xyz(rot), 'scale': [1.0, 1.0, 1.0]})
    globals_ = _bone_globals(bones, r)
    meshes = []
    vertex_total = face_total = 0
    for mesh_index in range(mesh_count):
        mesh_name = r.string() or f'Mesh {mesh_index + 1}'
        material_key, uv_count, vertex_count, face_count, slots = r.unpack('QBIIB')
        vertex_total += vertex_count
        face_total += face_count
        r.count(vertex_total, MAX_VERTICES, 'total model vertices')
        r.count(face_total, MAX_FACES, 'total model triangles')
        r.count(uv_count, 16, 'UV channel count')
        r.count(slots, 32, 'skin influences per vertex')
        # Verify the entire fixed-sized mesh before allocating its arrays.
        mesh_bytes = vertex_count * (12 + 12 + 16 + uv_count*8 + slots*6 + 32) + face_count*12
        if mesh_bytes > r.remaining:
            r.fail(f"truncated mesh {mesh_name!r}: requires {mesh_bytes:,} bytes")
        positions = _to_y_up(r.values('f', vertex_count * 3))
        normals = _to_y_up(r.values('f', vertex_count * 3))
        r.take(vertex_count * 16)  # Three derives tangent space from positions/UVs.
        uvs = r.values('f', vertex_count * 2) if uv_count else []
        uvs2 = r.values('f', vertex_count * 2) if uv_count > 1 else []
        r.take(max(0, uv_count - 2) * vertex_count * 8)
        # OWLib's skeleton.Lookup is ushort[]; 0xffff is its missing-bone
        # sentinel. Values above 32767 are valid when the skeleton is large.
        indices = r.values('H', vertex_count * slots)
        weights = r.values('f', vertex_count * slots)
        r.take(vertex_count * 32)  # Two game-specific vertex color channels.
        faces = r.values('I', face_count * 3)
        if any(index >= vertex_count for index in faces):
            r.fail(f"triangle index outside vertex array in {mesh_name!r}")
        skin_indices, skin_weights = [], []
        if slots and bone_count:
            skin_indices, skin_weights, orphans = _skin(indices, weights, bone_count, vertex_count, slots)
            if orphans:
                warnings.append(f'{path.name}: {mesh_name} has {orphans} vertices without valid skin weights; attached to first joint.')
        meshes.append({'name': mesh_name, 'materialKey': str(material_key),
                       'positions': positions, 'normals': normals, 'uvs': uvs, 'uvs2': uvs2,
                       'indices': faces, 'skinIndices': skin_indices, 'skinWeights': skin_weights})
    sockets = []
    bone_lookup = {bone['name']: index for index, bone in enumerate(bones)}
    for _ in range(socket_count):
        socket_name, bone_name = r.string(), r.string()
        position, rotation = r.values('f', 3), _normalize_quaternion(r.values('f', 4))
        matrix = _matrix(position, rotation)
        # OWLib writes hardpoints in model space, not parent-bone local space.
        # Convert once so child entities continue following animated bones.
        # The exporter writes bone_0000 when the hardpoint has no bone GUID.
        # Treat that null reference as model-space even if a root bone happens
        # to carry the same name.
        if bone_name != 'bone_0000' and bone_name in bone_lookup:
            matrix = _multiply(_inverse_rigid(globals_[bone_lookup[bone_name]]), matrix)
        else:
            bone_name = None
        sockets.append({'name': socket_name, 'bone': bone_name, 'matrix': matrix})
    if r.remaining:
        warnings.append(f'{path.name}: ignored {r.remaining:,} trailing bytes from model version {major}.{minor}.')
    return {'name': name or path.stem, 'path': str(path), 'guid': str(guid),
            'bones': bones, 'meshes': meshes, 'sockets': sockets, 'materialPath': material}


class _Resolver:
    def __init__(self, root):
        self.root = Path(root).expanduser().resolve() if root else None
        self.index = None

    @staticmethod
    def clean(reference):
        return reference.replace('\\', os.sep).replace('/', os.sep)

    def find(self, reference, owner, kind, relative='../../'):
        if not reference:
            return None
        folder, extension, original_extension = {
            'model': ('Models', '.owmdl', '.00C'),
            'entity': ('Entities', '.owentity', ''),
        }[kind]
        reference = self.clean(reference)
        relative = self.clean(relative or '')
        ref_path = Path(reference)
        identifier = ref_path.name
        if identifier.lower().endswith(extension):
            identifier = identifier[:-len(extension)]
        filename = identifier
        if original_extension and filename.upper().endswith(original_extension):
            filename = filename[:-len(original_extension)]
        filename += extension
        candidates = [owner.parent / reference]
        if ref_path.suffix.lower() == extension:
            candidates.append(ref_path)
        base = owner.parent / relative
        candidates += [base / folder / identifier / filename,
                       base / folder / filename, base / reference,
                       owner.parent / filename]
        for ancestor in list(owner.parents)[:5]:
            candidates.append(ancestor / folder / identifier / filename)
        if self.root:
            candidates += [self.root / folder / identifier / filename,
                           self.root / folder / filename,
                           self.root / reference,
                           self.root / filename]
        for candidate in candidates:
            if candidate.suffix.lower() == extension and candidate.is_file():
                return candidate.resolve()
        # A copied entity can still work if its DataTool root was supplied.
        # Avoid choosing an arbitrary asset when multiple names collide.
        if self.root and self.root.is_dir():
            if self.index is None:
                self.index = {}
                count = 0
                for directory, _, files in os.walk(self.root):
                    for entry in files:
                        count += 1
                        if count > 500_000:
                            raise FormatError('Export root exceeds 500,000 files; select a smaller character folder.')
                        if entry.lower().endswith(('.owmdl', '.owentity')):
                            self.index.setdefault(entry.lower(), []).append(Path(directory) / entry)
            matches = self.index.get(filename.lower(), [])
            if len(matches) == 1:
                return matches[0].resolve()
            if len(matches) > 1:
                raise FormatError(f'Ambiguous reference {filename}: found {len(matches)} matches under the root.')
        return None


def _read_entity(path):
    r = _Reader.file(path)
    if r.string() != 'owentity':
        r.fail('invalid entity identifier')
    major, minor = r.unpack('HH')
    _version(r, major, minor, 'entity')
    name, model, effect = r.string(), r.string(), r.string()
    guid, model_id, effect_id, child_count = r.unpack('IIIi')
    r.count(child_count, 4096, 'child entity count')
    children = []
    for _ in range(child_count):
        reference = r.string()
        hardpoint, variant, hardpoint_index, variant_index = r.unpack('QQII')
        attachment = r.string()
        children.append({'reference': reference, 'attachment': attachment})
    material_look, relative = (r.string(), r.string()) if minor >= 1 else ('', '../../')
    return {'name': name, 'model': model, 'children': children, 'relative': relative,
            'materialLook': material_look}


def load_asset(path, root_path=None) -> dict[str, Any]:
    """Read one .owmdl or recursively assemble an .owentity and its children.

    models[] form a hierarchy by id/parentId. attachmentBone is a raw bone name
    on the parent model, or None for the model root. attachmentMatrix is relative
    to that parent/bone and uses Three.js Matrix4.fromArray column-major order.
    Missing child assets become visible warnings while the remaining mesh loads.
    """
    path = Path(path).expanduser().resolve()
    if path.suffix.lower() not in ('.owmdl', '.owentity'):
        raise FormatError('Select an .owentity or .owmdl file.')
    if not path.is_file():
        raise FileNotFoundError(f'File not found: {path}')
    models, warnings = [], []
    resolver = _Resolver(root_path)
    from .materials import MaterialResolver
    material_resolver = MaterialResolver(path, root_path, warnings)
    active = set()

    def add(current, parent=None, attachment='', depth=0, reference_name=None):
        current = current.resolve()
        if depth > MAX_ENTITY_DEPTH:
            raise FormatError('Entity hierarchy exceeds 48 levels.')
        if current in active:
            raise FormatError(f'Entity reference cycle: {current.name}')
        if len(models) >= MAX_MODELS:
            raise FormatError('Entity exceeds 512 model nodes.')
        active.add(current)
        try:
            entity = None
            if current.suffix.lower() == '.owentity':
                entity = _read_entity(current)
                model_path = resolver.find(entity['model'], current, 'model', entity['relative'])
                if entity['model'] and not model_path:
                    warnings.append(f"{current.name}: missing model {entity['model']}; keep the original Models folder beside Entities or set its export root.")
            else:
                model_path = current
            if model_path:
                model = _load_model(model_path, warnings)
                model['materials'] = material_resolver.for_model(model, entity['materialLook'] if entity else '')
            else:
                model = {'name': (entity['name'] if entity else '') or current.stem,
                         'path': str(current), 'bones': [], 'meshes': [], 'sockets': [], 'materials': {}}
            model.update({'id': f'model-{len(models)}', 'parentId': parent['id'] if parent else None,
                          'attachmentBone': None, 'attachmentMatrix': list(IDENTITY)})
            # Preserve the source scene's wrappers separately from the compact
            # preview model list. C4D's importer names entity children from their
            # reference filename and skeletons from the model filename, not its
            # numeric internal index. Exporters must not manufacture bone names.
            model.update({'isEntity': entity is not None,
                          'entityName': ((reference_name if parent else entity['name']) or current.stem) if entity else None,
                          'entityPath': str(current) if entity else None,
                          'modelGuid': model_path.stem if model_path else None,
                          'hasModel': model_path is not None})
            if parent and attachment:
                socket = next((socket for socket in parent['sockets'] if socket['name'] == attachment), None)
                if socket:
                    model['attachmentBone'] = socket['bone']
                    model['attachmentMatrix'] = socket['matrix']
                elif any(bone['name'] == attachment for bone in parent['bones']):
                    model['attachmentBone'] = attachment
                else:
                    warnings.append(f'{current.name}: attachment {attachment} not found; attached to parent root.')
            models.append(model)
            if entity:
                for child in entity['children']:
                    if not child['reference']:
                        continue
                    try:
                        child_path = resolver.find(child['reference'], current, 'entity')
                        if child_path:
                            add(child_path, model, child['attachment'], depth + 1,
                                Path(resolver.clean(child['reference'])).stem)
                        else:
                            warnings.append(f"{current.name}: missing child entity {child['reference']}.")
                    except (OSError, FormatError) as error:
                        warnings.append(f"Skipped child {child['reference']}: {error}")
        finally:
            active.remove(current)

    add(path)
    if not any(model['meshes'] or model['bones'] for model in models):
        detail = '\n'.join(warnings[:5])
        raise FormatError(f'No model or skeleton could be loaded from {path.name}.\n{detail}'.strip())
    asset = {'name': path.stem, 'path': str(path), 'models': models,
             'textures': material_resolver.textures, 'textureRoot': str(material_resolver.root),
             'warnings': list(dict.fromkeys(warnings))}
    asset['exportHierarchy'] = build_export_hierarchy(asset)
    return asset


def build_export_hierarchy(asset):
    """Source-compatible entity/model/skeleton graph, in raw Y-up metres.

    The working Cinema 4D importer aliases a hardpoint directly to its joint.
    It creates no socket object or socket offset, so export wrappers have
    identity transforms. Preview attachmentMatrix deliberately remains intact.
    Node identifiers are internal only; every displayed name is source-derived.
    """
    from .bone_names import getBoneName
    nodes, states = [], {}
    pending = list(asset.get('models', []))

    def add(ident, kind, name, parent, model_id, matrix=None, **source):
        nodes.append(dict(id=ident, type=kind, name=name, parentId=parent,
                          modelId=model_id, matrix=list(matrix or IDENTITY), **source))
        return ident

    while pending:
        ready = [m for m in pending if not m.get('parentId') or m['parentId'] in states]
        if not ready:
            raise FormatError('Model attachments contain a missing parent or cycle.')
        for model in ready:
            pending.remove(model)
            mid = model['id']
            parent_id = None
            if model.get('parentId'):
                previous = states[model['parentId']]
                parent_id = previous['entity'] or previous['model']
                parent_id = previous['bones'].get(model.get('attachmentBone'), parent_id)
            # Metadata-free assets are supported for callers constructing a
            # scene directly; real parsed .owmdl files do not gain an extra root.
            is_entity = model.get('isEntity', True)
            entity_id = None
            if is_entity:
                name = model.get('entityName') or (asset.get('name', 'Entity') if not model.get('parentId') else model['name'])
                entity_id = add(mid + ':entity', 'entity', name, parent_id, mid)
                parent_id = entity_id
            group = None
            bone_ids = {}
            if model.get('hasModel', True):
                group = add(mid + ':model', 'model', model['name'], parent_id, mid)
                bones = model.get('bones', [])
                if bones:
                    guid = model.get('modelGuid') or Path(model.get('path', model['name'])).stem
                    skeleton = add(mid + ':skeleton', 'skeleton', guid + '_Skeleton', group, mid)
                    for index, bone in enumerate(bones):
                        ident = mid + ':bone:' + str(index)
                        parent = skeleton if bone['parent'] < 0 else mid + ':bone:' + str(bone['parent'])
                        add(ident, 'bone', getBoneName(bone['name']), parent, mid,
                            _matrix(bone['pos'], bone['rot']), boneIndex=index,
                            sourceName=bone['name'])
                        bone_ids[bone['name']] = ident
                for index, mesh in enumerate(model.get('meshes', [])):
                    add(mid + ':mesh:' + str(index), 'mesh', mesh['name'], group, mid, meshIndex=index)
            states[mid] = {'entity': entity_id, 'model': group, 'bones': bone_ids}
    return nodes


def _clip_header(reader, size):
    major, minor, bone_count, fps, duration = reader.unpack('HHIfI')
    _version(reader, major, minor, 'animation clip')
    reader.count(bone_count, 65536, 'animation bone count')
    if not math.isfinite(fps) or not 0 < fps <= 1000:
        reader.fail(f'invalid frame rate {fps}')
    reader.count(duration, 100_000_000, 'animation duration')
    if not duration:
        reader.fail('animation frame count must include frame zero (minimum 1)')
    # The writer stores InfoTableSize = floor(FPS * Duration) + 1, a frame
    # count including frame zero. The final sample is therefore count - 1.
    last_frame = max(0, duration - 1)
    return {'name': reader.path.stem, 'path': str(reader.path),
            'fps': fps, 'durationFrames': duration,
            'lastFrame': last_frame, 'durationSeconds': last_frame / fps, 'boneCount': bone_count,
            'size': size, 'version': f'{major}.{minor}'}


def read_clip_header(path) -> dict[str, Any]:
    """Read 16 bytes only, so indexing many clips does not parse keyframes."""
    path = _path(path, '.owanimclip')
    size = path.stat().st_size
    with path.open('rb') as file:
        data = file.read(16)
    return _clip_header(_Reader(path, data), size)


class _ClipBindingReader(_Reader):
    """Read structural fields while seeking over the uninterpreted key data."""
    def __init__(self, path, source, size):
        self.path, self.source, self.size, self.offset = path, source, size, 0

    @property
    def remaining(self):
        return self.size - self.offset

    def take(self, size):
        if size < 0 or size > self.remaining:
            self.fail(f'truncated data (need {size:,} bytes, have {self.remaining:,})')
        data = self.source.read(size)
        self.offset += len(data)
        if len(data) != size:
            self.fail('file changed or was truncated while reading animation metadata')
        return memoryview(data)

    def skip(self, size):
        if size < 0 or size > self.remaining:
            self.fail(f'truncated keyframe array: requires {size:,} bytes')
        self.source.seek(size, os.SEEK_CUR)
        self.offset += size


def read_clip_binding_metadata(path) -> dict[str, Any]:
    """Read exact bone-record names without decoding or allocating keyframes.

    Matching follows ``load_clip``: every named bone record counts, including
    records with no populated channels. Structural corruption is rejected;
    frame numbers and numeric key values are deliberately not decoded here.
    Playback's full parser still validates those values when a clip is loaded.
    """
    path = _path(path, '.owanimclip')
    with path.open('rb') as source:
        size = os.fstat(source.fileno()).st_size
        if size > MAX_FILE_BYTES:
            raise FormatError(f'{path.name}: file exceeds the 512 MB preview limit')
        reader = _ClipBindingReader(path, source, size)
        header = _clip_header(reader, size)
        names, seen, warnings = [], set(), []
        components_by_kind = {0: 3, 1: 4, 2: 3}
        track_names = {0: 'positions', 1: 'rotations', 2: 'scales'}
        for _ in range(header['boneCount']):
            name, track_count = reader.string(), reader.unpack('I')[0]
            if not name or name in seen:
                reader.fail(f'missing or duplicate animation bone: {name!r}')
            seen.add(name)
            names.append(name)
            reader.count(track_count, 16, 'track count')
            known_tracks = set()
            for _ in range(track_count):
                kind, key_count, components = reader.unpack('III')
                reader.count(key_count, 10_000_000, 'keyframe count')
                reader.count(components, 16, 'keyframe components')
                if kind in components_by_kind:
                    if components != components_by_kind[kind]:
                        reader.fail(f'{track_names[kind]} track needs {components_by_kind[kind]} components, has {components}')
                    if kind in known_tracks:
                        reader.fail(f'duplicate {track_names[kind]} track for {name}')
                    known_tracks.add(kind)
                else:
                    warnings.append(f'{name}: skipped unknown track type {kind}.')
                reader.skip(key_count * (4 + components * 4))
        if os.fstat(source.fileno()).st_size != size:
            reader.fail('file changed while reading animation metadata; scan again')
        if reader.remaining:
            warnings.append(f'Ignored {reader.remaining:,} trailing bytes from clip version {header["version"]}.')
    return {'path': str(path), 'boneNames': names, 'boneCount': header['boneCount'], 'warnings': warnings}


def load_clip(path) -> dict[str, Any]:
    """Return sorted raw per-bone tracks for the Blender-compatible evaluator."""
    path = _path(path, '.owanimclip')
    r = _Reader.file(path)
    result = _clip_header(r, len(r.data))
    bones, warnings, names = [], [], set()
    max_frame = 0
    component_sizes = {0: 3, 1: 4, 2: 3}
    track_names = {0: 'positions', 1: 'rotations', 2: 'scales'}
    for _ in range(result['boneCount']):
        name, track_count = r.string(), r.unpack('I')[0]
        if not name or name in names:
            r.fail(f'missing or duplicate animation bone: {name!r}')
        names.add(name)
        r.count(track_count, 16, 'track count')
        bone = {'name': name}
        for _ in range(track_count):
            kind, key_count, components = r.unpack('III')
            r.count(key_count, 10_000_000, 'keyframe count')
            r.count(components, 16, 'keyframe components')
            if kind in component_sizes and components != component_sizes[kind]:
                r.fail(f'{track_names[kind]} track needs {component_sizes[kind]} components, has {components}')
            size = key_count * (4 + components*4)
            if size > r.remaining:
                r.fail(f'truncated keyframe array: requires {size:,} bytes')
            if kind not in track_names:
                r.take(size)
                warnings.append(f'{name}: skipped unknown track type {kind}.')
                continue
            track_name = track_names[kind]
            if track_name in bone:
                r.fail(f'duplicate {track_name} track for {name}')
            keyframes = {}
            for _ in range(key_count):
                frame = r.unpack('I')[0]
                r.count(frame, 100_000_000, 'keyframe frame number')
                value = r.values('f', components)
                if kind == 1:
                    value = _normalize_quaternion(value)
                keyframes[frame] = value
                max_frame = max(max_frame, frame)
            frames = sorted(keyframes)
            bone[track_name] = {'frames': frames,
                                'values': [value for frame in frames for value in keyframes[frame]]}
        bones.append(bone)
    if max_frame > result['lastFrame']:
        warnings.append(f'Keyframes extend past header duration; timeline expanded to frame {max_frame}.')
        result['durationFrames'] = max_frame + 1
        result['lastFrame'] = max_frame
        result['durationSeconds'] = max_frame / result['fps']
    if r.remaining:
        warnings.append(f'Ignored {r.remaining:,} trailing bytes from clip version {result["version"]}.')
    result['bones'], result['warnings'] = bones, warnings
    return result
