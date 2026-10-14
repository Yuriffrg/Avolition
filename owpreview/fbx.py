"""Deterministic, standalone binary FBX 7.4 mesh/skin/animation exporter.

Encoding and element layout follow the installed Blender FBX exporter's public
format conventions. No Blender runtime or Autodesk SDK is required. Geometric
coordinates and animation translations are baked to centimetres. Native FBX
Rrs inheritance preserves Overwatch's NONE scale inheritance without inserting
helper joints or changing the original game hierarchy.
"""
from collections import Counter
import math
from pathlib import Path
import struct
import zlib
import numpy as np

from .export_pose import PoseEvaluator, decompose, euler_xyz, rotation
from .formats import build_export_hierarchy

KTIME = 46186158000
VERSION = 7400
EXPORT_SCALE = 100.


def _p(kind, value):
    if kind in ('S', 'R'):
        raw = value.encode('utf-8') if isinstance(value, str) else value
        return kind.encode() + struct.pack('<I', len(raw)) + raw
    if kind in ('d', 'f', 'i', 'l'):
        dtype = {'d': '<f8', 'f': '<f4', 'i': '<i4', 'l': '<i8'}[kind]
        a = np.asarray(value, dtype=dtype).reshape(-1)
        raw = a.tobytes(); compressed = zlib.compress(raw, 1)
        encoding = int(len(compressed) < len(raw))
        raw = compressed if encoding else raw
        return kind.encode() + struct.pack('<III', len(a), encoding, len(raw)) + raw
    fmt = {'I': 'i', 'L': 'q', 'D': 'd', 'F': 'f', 'C': '?'}[kind]
    return kind.encode() + struct.pack('<' + fmt, value)


class _Node:
    def __init__(self, name, *props):
        self.name = name.encode() if isinstance(name, str) else name
        self.props = list(props)
        self.children = []

    def add(self, name, *props):
        node = _Node(name, *props); self.children.append(node); return node

    def encode(self, start):
        props = b''.join(_p(*p) for p in self.props)
        cursor = start + 13 + len(self.name) + len(props)
        parts = []
        for child in self.children:
            part = child.encode(cursor); cursor += len(part); parts.append(part)
        if self.children or self.name in (b'AnimationStack', b'AnimationLayer'):
            parts.append(bytes(13)); cursor += 13
        if cursor >= 2**32: raise ValueError('FBX 7.4 export exceeds its 4 GiB file limit.')
        return struct.pack('<IIIB', cursor, len(self.props), len(props), len(self.name)) + self.name + props + b''.join(parts)


def _value(node, name, value, kind='I'):
    return node.add(name, (kind, value))


def _properties(node):
    return node.add('Properties70')


def _prop(props, name, value, kind='number', animated=False):
    types = {'number': ('Number', '', 'D'), 'double': ('double', 'Number', 'D'),
             'int': ('int', 'Integer', 'I'), 'enum': ('enum', '', 'I'),
             'bool': ('bool', '', 'I'), 'string': ('KString', '', 'S'),
             'time': ('KTime', 'Time', 'L'), 'color': ('Color', '', 'D'),
             'vector': ('Vector3D', 'Vector', 'D'),
             'compound': ('Compound', '', None), 'object': ('object', '', None),
             'Lcl Translation': ('Lcl Translation', '', 'D'),
             'Lcl Rotation': ('Lcl Rotation', '', 'D'),
             'Lcl Scaling': ('Lcl Scaling', '', 'D')}
    typ, subtype, code = types[kind]
    values = [] if code is None else list(value) if isinstance(value, (tuple, list, np.ndarray)) else [value]
    return props.add('P', ('S', name), ('S', typ), ('S', subtype), ('S', 'A' if animated else ''),
                     *((code, v) for v in values))


def _name(name, cls):
    return str(name).replace('\0', '').encode('utf-8') + b'\0\1' + cls.encode()


def _matrix(value):
    return np.asarray(value).T.reshape(-1)


class _Scene:
    def __init__(self):
        self.objects = _Node('Objects'); self.connections = _Node('Connections'); self.next_id = 1000
        self.model_properties = {}
        self.takes = []

    def obj(self, kind, name, subtype='', cls=None):
        self.next_id += 1
        return self.next_id, self.objects.add(kind, ('L', self.next_id), ('S', _name(name, cls or kind)), ('S', subtype))

    def connect(self, child, parent, prop=None):
        args = [('S', 'OP' if prop else 'OO'), ('L', child), ('L', parent)]
        if prop: args.append(('S', prop))
        self.connections.add('C', *args)

    def model(self, name, subtype, parent, matrix=np.eye(4), inherit_type=1):
        ident, node = self.obj('Model', name, subtype)
        _value(node, 'Version', 232)
        p, r, s = decompose(matrix)
        props = _properties(node)
        self.model_properties[ident] = props
        _prop(props, 'Lcl Translation', p, 'Lcl Translation', True)
        _prop(props, 'Lcl Rotation', np.degrees(r), 'Lcl Rotation', True)
        _prop(props, 'Lcl Scaling', s, 'Lcl Scaling', True)
        _prop(props, 'RotationOrder', 0, 'enum')
        _prop(props, 'InheritType', inherit_type, 'enum')
        _prop(props, 'RotationActive', 1, 'bool')
        # Blender writes this explicitly: the SDK default is -1, which leaves
        # GetNodeAttribute() empty even when geometry/skeleton data is connected.
        # Cinema 4D then imports both meshes and joints as plain null objects.
        if subtype in ('Mesh', 'LimbNode'):
            _prop(props, 'DefaultAttributeIndex', 0, 'int')
        _value(node, 'MultiLayer', 0); _value(node, 'MultiTake', 0)
        _value(node, 'Shading', True, 'C'); _value(node, 'Culling', 'CullingOff', 'S')
        self.connect(ident, parent)
        if subtype == 'LimbNode':
            aid, attr = self.obj('NodeAttribute', name, 'LimbNode')
            _value(attr, 'TypeFlags', 'Skeleton', 'S')
            _prop(_properties(attr), 'Size', 5.)
            self.connect(aid, ident)
        return ident

    def material(self, descriptor, fallback_name):
        desc = descriptor or {}
        ident, node = self.obj('Material', desc.get('name', fallback_name))
        _value(node, 'Version', 102); _value(node, 'ShadingModel', 'phong', 'S'); _value(node, 'MultiLayer', 0)
        props = _properties(node)
        color = desc.get('baseColor', [0.55, 0.6, 0.66])[:3]
        rough = float(desc.get('roughness', 0.65)); metal = float(desc.get('metalness', 0.0))
        unbaked = set(desc.get('unbakedMaps', []))
        if desc.get('emissiveMode') == 'baseColorMask': unbaked.add('emissive')
        emission = 0. if 'emissive' in unbaked else float(desc.get('emissiveIntensity', 0.0))
        _prop(props, 'DiffuseColor', color, 'color', True); _prop(props, 'DiffuseFactor', 1., animated=True)
        _prop(props, 'SpecularColor', [1., 1., 1.], 'color', True); _prop(props, 'SpecularFactor', 0.25, animated=True)
        _prop(props, 'Shininess', ((1-rough)*10)**2, animated=True)
        _prop(props, 'ShininessExponent', ((1-rough)*10)**2, animated=True)
        _prop(props, 'ReflectionFactor', metal, animated=True)
        _prop(props, 'EmissiveColor', desc.get('emissive', [1., 1., 1.])[:3], 'color', True)
        _prop(props, 'EmissiveFactor', emission, animated=True)
        opacity = max(0., min(1., float(desc.get('opacity', 1.))))
        _prop(props, 'Opacity', opacity, animated=True); _prop(props, 'TransparencyFactor', 1.-opacity, animated=True)
        _prop(props, 'TransparentColor', [1.-opacity]*3, 'color', True)
        _prop(props, 'NormalMap', [0., 0., 0.], 'vector'); _prop(props, 'BumpFactor', 1.)
        # FBX's portable material model is Phong; these connections follow Blender's
        # exporter convention. Engines differ in mapping reflection/shininess to PBR.
        properties = {'baseColor': ['DiffuseColor'], 'normal': ['NormalMap'],
                      'roughness': ['Shininess', 'ShininessExponent'], 'metalness': ['ReflectionFactor'],
                      'alpha': ['TransparencyFactor'], 'emissive': ['EmissiveColor']}
        for role, maps in properties.items():
            if role in unbaked: continue
            desc_map = desc.get('maps', {}).get(role)
            if not desc_map or not desc_map.get('relativePath'): continue
            relative = str(desc_map['relativePath']).replace('\\', '/')
            tid, texture = self.obj('Texture', fallback_name + '_' + role)
            vid, video = self.obj('Video', relative, 'Clip')
            _value(video, 'Type', 'Clip', 'S'); _prop(_properties(video), 'Path', relative, 'string')
            _value(video, 'UseMipMap', 0); _value(video, 'Filename', relative, 'S')
            _value(video, 'RelativeFilename', relative, 'S')
            _value(texture, 'Type', 'TextureVideoClip', 'S'); _value(texture, 'Version', 202)
            _value(texture, 'TextureName', _name(fallback_name+'_'+role, 'Texture'), 'S')
            _value(texture, 'Media', _name(relative, 'Video'), 'S')
            _value(texture, 'FileName', relative, 'S'); _value(texture, 'RelativeFilename', relative, 'S')
            tp = _properties(texture)
            _prop(tp, 'CurrentMappingType', 0, 'enum'); _prop(tp, 'WrapModeU', 0, 'enum'); _prop(tp, 'WrapModeV', 0, 'enum')
            _prop(tp, 'UVSet', 'UVMap2' if desc_map.get('uvIndex') else 'UVMap', 'string')
            _prop(tp, 'UseMaterial', 1, 'bool'); _prop(tp, 'PremultiplyAlpha', 0, 'bool')
            repeat = desc_map.get('repeat', [1., 1.])
            _prop(tp, 'Scaling', [1./repeat[0] if repeat[0] else 1., 1./repeat[1] if repeat[1] else 1., 1.], 'vector')
            _prop(tp, 'Translation', [0., 0., 0.], 'vector'); _prop(tp, 'Rotation', [0., 0., 0.], 'vector')
            _prop(tp, 'AlphaSource', 0, 'enum')
            self.connect(vid, tid)
            for prop in maps: self.connect(tid, ident, prop)
        return ident

    def geometry(self, mesh, model_id, mat_id):
        gid, geom = self.obj('Geometry', mesh['name'], 'Mesh')
        _value(geom, 'GeometryVersion', 124)
        vertices = np.asarray(mesh['positions'], dtype=float).reshape(-1, 3) * EXPORT_SCALE
        indices = np.asarray(mesh['indices'], dtype=np.int32).reshape(-1, 3)
        polygon_indices = indices.copy(); polygon_indices[:, 2] = ~polygon_indices[:, 2]
        _value(geom, 'Vertices', vertices, 'd'); _value(geom, 'PolygonVertexIndex', polygon_indices, 'i')
        layers = []
        if mesh.get('normals'):
            normals = np.asarray(mesh['normals']).reshape(-1, 3)[indices.reshape(-1)]
            n = geom.add('LayerElementNormal', ('I', 0)); _value(n, 'Version', 101)
            _value(n, 'Name', '', 'S'); _value(n, 'MappingInformationType', 'ByPolygonVertex', 'S')
            _value(n, 'ReferenceInformationType', 'Direct', 'S'); _value(n, 'Normals', normals, 'd')
            layers.append(('LayerElementNormal', 0, 0))
        for uv_layer, key in enumerate(('uvs', 'uvs2')):
            if not mesh.get(key): continue
            uv = geom.add('LayerElementUV', ('I', uv_layer)); _value(uv, 'Version', 101)
            _value(uv, 'Name', 'UVMap' if uv_layer == 0 else 'UVMap2', 'S')
            _value(uv, 'MappingInformationType', 'ByPolygonVertex', 'S')
            _value(uv, 'ReferenceInformationType', 'IndexToDirect', 'S')
            _value(uv, 'UV', mesh[key], 'd'); _value(uv, 'UVIndex', indices, 'i')
            layers.append(('LayerElementUV', uv_layer, uv_layer))
        m = geom.add('LayerElementMaterial', ('I', 0)); _value(m, 'Version', 101)
        _value(m, 'Name', '', 'S'); _value(m, 'MappingInformationType', 'AllSame', 'S')
        _value(m, 'ReferenceInformationType', 'IndexToDirect', 'S'); _value(m, 'Materials', [0], 'i')
        layers.append(('LayerElementMaterial', 0, 0))
        for layer_index in sorted({item[2] for item in layers}):
            layer = geom.add('Layer', ('I', layer_index)); _value(layer, 'Version', 100)
            for name, typed_index, index in layers:
                if index != layer_index: continue
                element = layer.add('LayerElement'); _value(element, 'Type', name, 'S')
                _value(element, 'TypedIndex', typed_index)
        self.connect(gid, model_id); self.connect(mat_id, model_id)
        return gid

    def skin(self, mesh, geom_id, mesh_id, mesh_world, bone_ids, bone_world, rig_id, rig_world):
        sid, skin = self.obj('Deformer', mesh['name'], 'Skin')
        _value(skin, 'Version', 101); _value(skin, 'Link_DeformAcuracy', 50., 'D')
        self.connect(sid, geom_id)
        ids = np.asarray(mesh['skinIndices'], dtype=int).reshape(-1, 4)
        weights = np.asarray(mesh['skinWeights'], dtype=float).reshape(-1, 4)
        for i, bid in enumerate(bone_ids):
            cid, cluster = self.obj('Deformer', str(i), 'Cluster', 'SubDeformer')
            _value(cluster, 'Version', 100); cluster.add('UserData', ('S', ''), ('S', ''))
            combined = np.sum(np.where(ids == i, weights, 0), axis=1)
            affected = np.nonzero(combined > 0)[0]
            _value(cluster, 'Indexes', affected, 'i'); _value(cluster, 'Weights', combined[affected], 'd')
            _value(cluster, 'Transform', _matrix(np.linalg.inv(bone_world[i]) @ mesh_world), 'd')
            _value(cluster, 'TransformLink', _matrix(bone_world[i]), 'd')
            _value(cluster, 'TransformAssociateModel', _matrix(rig_world), 'd')
            self.connect(cid, sid); self.connect(bid, cid)
        _, pose = self.obj('Pose', mesh['name'], 'BindPose')
        _value(pose, 'Type', 'BindPose', 'S'); _value(pose, 'Version', 100)
        values = [(mesh_id, mesh_world), (rig_id, rig_world)] + list(zip(bone_ids, bone_world))
        _value(pose, 'NbPoseNodes', len(values))
        for bid, matrix in values:
            pn = pose.add('PoseNode'); _value(pn, 'Node', bid, 'L'); _value(pn, 'Matrix', _matrix(matrix), 'd')

    def animation(self, name, times, tracks):
        self.takes.append((name, int(times[0]), int(times[-1])))
        stack_id, stack = self.obj('AnimationStack', name, cls='AnimStack')
        props = _properties(stack)
        for key, value in [('LocalStart', times[0]), ('LocalStop', times[-1]), ('ReferenceStart', times[0]), ('ReferenceStop', times[-1])]:
            _prop(props, key, int(value), 'time')
        layer_id, layer = self.obj('AnimationLayer', name, cls='AnimLayer'); self.connect(layer_id, stack_id)
        props = _properties(layer)
        # These are Blender's FbxAnimLayer template defaults, written directly
        # since this exporter does not omit values through shared templates.
        _prop(props, 'Weight', 100., animated=True)
        for key in ('Mute', 'Solo', 'Lock'): _prop(props, key, 0, 'bool')
        for key in ('BlendMode', 'RotationAccumulationMode', 'ScaleAccumulationMode'):
            _prop(props, key, 0, 'enum')
        for model_id, frames in tracks.items():
            # 'A' means animatable; Blender uses 'A+' for an actual animated
            # property. Preserve that distinction for SDK-based importers.
            for prop in self.model_properties[model_id].children:
                if prop.props[0][1] in ('Lcl Translation', 'Lcl Rotation', 'Lcl Scaling'):
                    prop.props[3] = ('S', 'A+')
            for property_index, (short, name) in enumerate([('T', 'Lcl Translation'), ('R', 'Lcl Rotation'), ('S', 'Lcl Scaling')]):
                values = np.asarray(frames)[:, property_index, :]
                node_id, node = self.obj('AnimationCurveNode', short, cls='AnimCurveNode')
                props = _properties(node)
                _prop(props, 'd', None, 'compound')
                self.connect(node_id, layer_id); self.connect(node_id, model_id, name)
                for channel, letter in enumerate('XYZ'):
                    _prop(props, 'd|'+letter, float(values[0, channel]), animated=True)
                    curve_id, curve = self.obj('AnimationCurve', '', cls='AnimCurve')
                    _value(curve, 'Default', float(values[0, channel]), 'D'); _value(curve, 'KeyVer', 4008)
                    _value(curve, 'KeyTime', times, 'l'); _value(curve, 'KeyValueFloat', values[:, channel], 'f')
                    _value(curve, 'KeyAttrFlags', [4], 'i')  # Linear, sampled at the original FPS.
                    _value(curve, 'KeyAttrDataFloat', [0., 0., 0., 0.], 'f')
                    _value(curve, 'KeyAttrRefCount', [len(times)], 'i')
                    self.connect(curve_id, node_id, 'd|'+letter)

    def write(self, destination, axis, fps, last_frame):
        root = _Node('')
        header = root.add('FBXHeaderExtension')
        _value(header, 'FBXHeaderVersion', 1003); _value(header, 'FBXVersion', VERSION); _value(header, 'EncryptionType', 0)
        stamp = header.add('CreationTimeStamp'); _value(stamp, 'Version', 1000)
        for key, value in [('Year', 1970), ('Month', 1), ('Day', 1), ('Hour', 10), ('Minute', 0), ('Second', 0), ('Millisecond', 0)]: _value(stamp, key, value)
        _value(header, 'Creator', 'Whitehound standalone FBX exporter', 'S')
        _value(root, 'FileId', bytes.fromhex('28b32aebb624ccc2bfc8b02aa92bfcf1'), 'R')
        _value(root, 'CreationTime', '1970-01-01 10:00:00:000', 'S')
        _value(root, 'Creator', 'Whitehound', 'S')
        glob = root.add('GlobalSettings'); _value(glob, 'Version', 1000); p = _properties(glob)
        axes = (1, 2, 1) if axis == 'Y' else (2, 1, -1)
        for key, value in [('UpAxis', axes[0]), ('UpAxisSign', 1), ('FrontAxis', axes[1]), ('FrontAxisSign', axes[2]), ('CoordAxis', 0), ('CoordAxisSign', 1), ('OriginalUpAxis', axes[0]), ('OriginalUpAxisSign', 1)]: _prop(p, key, value, 'int')
        _prop(p, 'UnitScaleFactor', 1., 'double'); _prop(p, 'OriginalUnitScaleFactor', 1., 'double')
        modes = {120: 1, 100: 2, 60: 3, 50: 4, 48: 5, 30: 6, 25: 10, 24: 11, 96: 15, 72: 16}
        _prop(p, 'TimeMode', modes.get(fps, 14), 'enum'); _prop(p, 'CustomFrameRate', fps, 'double')
        _prop(p, 'TimeSpanStart', 0, 'time'); _prop(p, 'TimeSpanStop', round(last_frame/fps*KTIME), 'time')
        docs = root.add('Documents'); _value(docs, 'Count', 1)
        doc = docs.add('Document', ('L', 999), ('S', 'Whitehound'), ('S', 'Scene'))
        props = _properties(doc)
        _prop(props, 'SourceObject', None, 'object')
        _prop(props, 'ActiveAnimStackName', self.takes[0][0] if self.takes else '', 'string')
        _value(doc, 'RootNode', 0, 'L')
        root.add('References')
        definitions = root.add('Definitions'); _value(definitions, 'Version', 100)
        counts = Counter(node.name for node in self.objects.children)
        counts[b'GlobalSettings'] = 1
        _value(definitions, 'Count', sum(counts.values()))
        for name, count in counts.items():
            typ = definitions.add('ObjectType', ('S', name)); _value(typ, 'Count', count)
        root.children.extend([self.objects, self.connections])
        takes = root.add('Takes'); _value(takes, 'Current', self.takes[0][0] if self.takes else '', 'S')
        for name, start, stop in self.takes:
            take = takes.add('Take', ('S', name))
            _value(take, 'FileName', name + '.tak', 'S')
            take.add('LocalTime', ('L', start), ('L', stop))
            take.add('ReferenceTime', ('L', start), ('L', stop))
        data = bytearray(b'Kaydara FBX Binary  \x00\x1a\x00' + struct.pack('<I', VERSION))
        for node in root.children: data.extend(node.encode(len(data)))
        data.extend(bytes(13))
        data.extend(bytes.fromhex('fabcab09d0c8d466b176fb831cf7267e') + bytes(4))
        data.extend(bytes(16 - len(data) % 16))
        data.extend(struct.pack('<I', VERSION) + bytes(120) + bytes.fromhex('f85a8c6adef5d97eece90ce3758f290b'))
        Path(destination).write_bytes(data)


def export_fbx(path, asset, clip, *, up_axis='Y', include_mesh=True, root_motion=True,
               materials=None, clip_name=None, rig_id='auto'):
    """Export one selected animation and the entity's nested meshes/skeletons.

    Texture paths must already be staged next to the FBX by the caller. Source
    textures are never edited. Original frame zero is FBX time zero. User-facing
    callers should write into a transaction directory and publish on success.
    """
    if up_axis not in ('Y', 'Z'): raise ValueError('FBX up axis must be Y or Z.')
    models = asset.get('models', [])
    if not models: raise ValueError('Load an entity or model before exporting FBX.')
    clip = clip or {'bones': [], 'fps': 30., 'lastFrame': 0}
    fps = float(clip['fps']); last_frame = int(clip.get('lastFrame', clip.get('durationFrames', 1)-1))
    if not math.isfinite(fps) or not 0 < fps <= 1000 or last_frame < 0: raise ValueError('Invalid animation timing.')
    names = {b['name'] for b in clip.get('bones', [])}
    candidates = [m for m in models if m.get('bones')]
    selected = max(candidates, key=lambda m: (sum(b['name'] in names for b in m['bones']), len(m['bones'])), default=None) if rig_id == 'auto' else next((m for m in models if m['id'] == rig_id), None)
    if selected is None or not selected.get('bones'): raise ValueError('The selected export rig has no skeleton.')
    matched = sum(b['name'] in names for b in selected['bones'])
    if names and not matched: raise ValueError('The selected animation has no matching bones on this rig.')
    if (last_frame+1)*max(1, len(selected['bones'])) > 1_000_000:
        raise ValueError('FBX bake exceeds one million bone frames; choose a shorter clip.')
    warnings = []
    scene = _Scene()
    axis_matrix = np.eye(4)
    if up_axis == 'Z': axis_matrix[:3, :3] = [[1, 0, 0], [0, 0, -1], [0, 1, 0]]
    # Source names and wrappers match the working Cinema 4D entity importer.
    # FBX identifiers, rather than renamed bones, disambiguate attached rigs.
    hierarchy = asset.get('exportHierarchy') or build_export_hierarchy(asset)
    model_lookup = {m['id']: m for m in models}
    state = {m['id']: {'source': m, 'bone_ids': [None] * len(m.get('bones', [])),
                       'bone_world': [None] * len(m.get('bones', []))} for m in models}
    exported, world_matrices = {}, {}
    pending = [n for n in hierarchy if include_mesh or n['type'] != 'mesh']
    while pending:
        ready = [n for n in pending if not n.get('parentId') or n['parentId'] in exported]
        if not ready: raise ValueError('Export hierarchy contains a missing parent or cycle.')
        for node in ready:
            pending.remove(node)
            mid, kind = node['modelId'], node['type']
            parent = node.get('parentId')
            matrix = np.array(node['matrix'], dtype=float).reshape(4, 4).T.copy()
            matrix[:3, 3] *= EXPORT_SCALE
            if parent is None: matrix = axis_matrix @ matrix
            world = world_matrices[parent] @ matrix if parent else matrix
            subtype = {'bone': 'LimbNode', 'mesh': 'Mesh'}.get(kind, 'Null')
            inherit_type = 1
            if kind == 'bone' and model_lookup[mid]['bones'][node['boneIndex']]['parent'] >= 0:
                # Rrs removes only the immediate parent's local scale from
                # rotation/scale inheritance while its translation still uses
                # the full parent matrix. This is the game's joint convention.
                inherit_type = 2
            ident = scene.model(node['name'], subtype, exported[parent] if parent else 0,
                                matrix, inherit_type=inherit_type)
            exported[node['id']], world_matrices[node['id']] = ident, world
            if kind == 'model':
                state[mid].update(group=ident, world=world)
            elif kind == 'bone':
                index = node['boneIndex']
                state[mid]['bone_ids'][index], state[mid]['bone_world'][index] = ident, world
    mesh_count = 0
    if include_mesh:
        for model in models:
            st = state[model['id']]
            material_cache = {}
            supplied = (materials or {}).get(model['id'], {})
            for mesh_index, mesh in enumerate(model.get('meshes', [])):
                key = str(mesh.get('materialKey', '0'))
                if key not in material_cache:
                    descriptor = supplied.get(key) or model.get('materials', {}).get(key)
                    if descriptor:
                        if 'ao' in descriptor.get('maps', {}):
                            warnings.append(f'{model["name"]} / {key}: separate AO map has no portable FBX Phong input and was omitted.')
                        if 'emissive' in descriptor.get('unbakedMaps', []) or descriptor.get('emissiveMode') == 'baseColorMask':
                            warnings.append(f'{model["name"]} / {key}: unbaked emission-mask shader was omitted from FBX.')
                    material_cache[key] = scene.material(descriptor, model['name'] + '_' + key)
                skinned = bool(st['bone_ids'] and mesh.get('skinIndices') and mesh.get('skinWeights'))
                mesh_id = exported[model['id'] + ':mesh:' + str(mesh_index)]
                geom_id = scene.geometry(mesh, mesh_id, material_cache[key])
                if skinned:
                    scene.skin(mesh, geom_id, mesh_id, st['world'], st['bone_ids'], st['bone_world'], st['group'], st['world'])
                mesh_count += 1
    st = state[selected['id']]; ev = PoseEvaluator(selected['bones'], clip.get('bones', []))
    track_ids = st['bone_ids']
    tracks = {bid: np.empty((last_frame+1, 3, 3), dtype=np.float64) for bid in track_ids}
    previous_euler = {}
    for frame in range(last_frame+1):
        pose = ev.evaluate(frame, lock_root=not root_motion)
        for i, bid in enumerate(st['bone_ids']):
            p, q, s = pose['positions'][i], pose['quaternions'][i], pose['scales'][i]
            r = euler_xyz(rotation(q), previous_euler.get(bid)); previous_euler[bid] = r
            tracks[bid][frame] = (p * EXPORT_SCALE, np.degrees(r), s)
    times = [round(frame/fps*KTIME) for frame in range(last_frame+1)]
    scene.animation(clip_name or clip.get('name', 'Animation'), times, tracks)
    scene.write(path, up_axis, fps, last_frame)
    if include_mesh: warnings.append('FBX materials use portable Phong connections; roughness, metallic, transparency and emission interpretation varies by importer.')
    return {'warnings': warnings, 'meshCount': mesh_count, 'boneCount': sum(len(m.get('bones', [])) for m in models),
            'helperBoneCount': 0, 'modelGroupBoneCount': 0, 'frameCount': last_frame+1, 'fps': fps, 'matchedBones': matched,
            'rigId': selected['id'], 'unit': 'centimetres', 'scale': EXPORT_SCALE,
            'upAxis': up_axis, 'bytes': Path(path).stat().st_size}
