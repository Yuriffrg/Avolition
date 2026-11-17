"""Build a native Cinema 4D document without touching the editor document.

This module runs inside Cinema 4D/c4dpy, not the standalone Python backend.
Input transforms use the preview's raw Y-up metres; C4D receives the working
Entities importer's Z reflection and centimetres. No third-party Python
packages are needed in Cinema 4D.
"""
from __future__ import annotations

import math
from collections import deque
from pathlib import Path

IDENTITY = [1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]


def _matrix_values(values, label='transform'):
    """Validate TRS representability before Cinema 4D can discard shear."""
    if len(values) != 16:
        raise ValueError(f'{label}: expected a 16-component matrix.')
    values = [float(v) for v in values]
    if not all(math.isfinite(v) for v in values):
        raise ValueError(f'{label}: non-finite matrix.')
    if any(abs(values[i]) > 1e-7 for i in (3, 7, 11)) or abs(values[15] - 1) > 1e-7:
        raise ValueError(f'{label}: invalid affine matrix.')
    columns = [values[i:i+3] for i in (0, 4, 8)]
    lengths = [math.sqrt(sum(v*v for v in column)) for column in columns]
    if min(lengths) < 1e-10:
        raise ValueError(f'{label}: zero scale cannot be exported to native joint PSR.')
    for a, b in ((0, 1), (0, 2), (1, 2)):
        dot = sum(x*y for x, y in zip(columns[a], columns[b])) / (lengths[a]*lengths[b])
        if abs(dot) > 1e-5:
            raise ValueError(f'{label}: this animation needs a sheared local joint transform. '
                             'Cinema 4D cannot retain that transform as native PSR without changing the hierarchy.')
    return values


def _matrix(c4d, values, label='transform'):
    m = _matrix_values(values, label)
    return c4d.Matrix(off=c4d.Vector(m[12]*100, m[13]*100, -m[14]*100),
                      v1=c4d.Vector(m[0], m[1], -m[2]),
                      v2=c4d.Vector(m[4], m[5], -m[6]),
                      v3=c4d.Vector(-m[8], -m[9], m[10]))


def _ordered_graph(graph):
    identifiers = [node['id'] for node in graph]
    if len(set(identifiers)) != len(identifiers):
        raise ValueError('The export hierarchy contains duplicate node identifiers.')
    children, ready, ordered = {}, deque(), []
    for node in graph:
        if not node.get('parentId'):
            ready.append(node)
        else:
            children.setdefault(node['parentId'], []).append(node)
    # Linear traversal matters for maps with thousands of placed objects.
    while ready:
        node = ready.popleft()
        _matrix_values(node.get('matrix', IDENTITY), node['name'])
        ordered.append(node)
        ready.extend(children.get(node['id'], ()))
    if len(ordered) != len(graph):
        raise ValueError('The export hierarchy contains a missing parent or a cycle.')
    return ordered


def _asset_models(asset):
    """Resolve compact map geometry while retaining individual native objects."""
    models, prototypes = {}, asset.get('meshPrototypes', {})
    for model in asset.get('models', []):
        ident = model['id']
        if ident in models:
            raise ValueError('The scene contains duplicate model identifiers.')
        prototype = model.get('meshPrototype')
        geometry = model if prototype is None else prototypes.get(prototype)
        if not isinstance(geometry, dict) or not isinstance(geometry.get('meshes'), list):
            raise ValueError('Map object references a missing mesh prototype. Update the C4D bridge and reload the map.')
        names = model.get('meshNames', {})
        meshes = [dict(mesh, name=names.get(str(index), mesh['name']))
                  for index, mesh in enumerate(geometry['meshes'])]
        models[ident] = dict(model, meshes=meshes)
    return models


def _mesh(c4d, mesh):
    positions, indices = mesh['positions'], mesh['indices']
    if len(positions) % 3 or len(indices) % 3:
        raise ValueError(f'{mesh["name"]}: invalid mesh array length.')
    count = len(positions)//3
    if any(int(i) != i or i < 0 or i >= count for i in indices):
        raise ValueError(f'{mesh["name"]}: polygon references an invalid point.')
    obj = c4d.PolygonObject(count, len(indices)//3)
    for i in range(count):
        x, y, z = positions[i*3:i*3+3]
        obj.SetPoint(i, c4d.Vector(float(x)*100, float(y)*100, -float(z)*100))
    faces = [(int(indices[i]), int(indices[i+2]), int(indices[i+1]), int(indices[i+1]))
             for i in range(0, len(indices), 3)]
    for i, face in enumerate(faces):
        obj.SetPolygon(i, c4d.CPolygon(*face))
    last_uv = None
    for key, name in (('uvs', 'UVMap1'), ('uvs2', 'UVMap2')):
        if not mesh.get(key):
            continue
        uv = mesh[key]
        if len(uv) != count*2:
            raise ValueError(f'{mesh["name"]}: invalid {name} array length.')
        tag = c4d.UVWTag(len(faces))
        tag.SetName(name)
        for i, face in enumerate(faces):
            # Match the working Entities importer's enabled "Flip UV V".
            # Convert every UV set at the native C4D boundary; leave shared
            # source UVs and baked texture pixels unchanged for preview/FBX.
            tag.SetSlow(i, *[c4d.Vector(float(uv[v*2]), 1.0 - float(uv[v*2+1]), 0) for v in face])
        obj.InsertTag(tag, pred=last_uv)
        last_uv = tag
    normals = mesh.get('normals')
    if normals:
        if len(normals) != count*3:
            raise ValueError(f'{mesh["name"]}: invalid normal array length.')
        tag = c4d.NormalTag(len(faces))
        handle = tag.GetDataAddressW()
        for i, face in enumerate(faces):
            values = [c4d.Vector(float(normals[v*3]), float(normals[v*3+1]), -float(normals[v*3+2]))
                      for v in face]
            c4d.NormalTag.Set(handle, i, dict(zip('abcd', values)))
        obj.InsertTag(tag)
    obj.SetPhong(True, False, 0.0)
    obj.Message(c4d.MSG_UPDATE)
    return obj


def _native_skin_weights(influences):
    """Quantize one normalized vertex to the 65535 units in a C4D Weight tag."""
    total = sum(influences.values())
    if not math.isfinite(total):
        raise ValueError('Invalid skin weight total.')
    quotas = {joint: weight / total * 65535 for joint, weight in influences.items()}
    units = {joint: math.floor(quota) for joint, quota in quotas.items()}
    # Distribute rounding error by largest remainder, breaking ties by joint index.
    missing = 65535 - sum(units.values())
    ranked = sorted(units, key=lambda joint: (units[joint] - quotas[joint], joint))
    for joint in ranked[:missing]:
        units[joint] += 1
    # SetWeightMap first casts to float32, then truncates into uint16. A small
    # offset inside each bucket survives either conversion, including after save.
    return {joint: min(1.0, (value + 0.25) / 65535) if value else 0.0
            for joint, value in units.items()}


def _skin(c4d, character, obj, mesh, joints, *, actor_weights=False):
    indices, weights = mesh.get('skinIndices'), mesh.get('skinWeights')
    if not joints or not indices or not weights:
        return False
    count = len(mesh['positions'])//3
    if len(indices) != count*4 or len(weights) != count*4:
        raise ValueError(f'{mesh["name"]}: invalid skin array length.')
    tag = character.CAWeightTag()
    obj.InsertTag(tag)
    slots = [tag.AddJoint(joint) for joint in joints]
    maps = [[0.0]*count for _ in joints]
    for point in range(count):
        influences = {}
        total = 0.0
        for k in range(4):
            j, weight = int(indices[point*4+k]), float(weights[point*4+k])
            if not math.isfinite(weight):
                raise ValueError(f'{mesh["name"]}: invalid skin influence.')
            if weight <= 0:
                continue
            if not 0 <= j < len(joints):
                raise ValueError(f'{mesh["name"]}: invalid skin influence.')
            influences[j] = influences.get(j, 0.0) + weight
            total += weight
        if not influences:
            raise ValueError(f'{mesh["name"]}: point {point} has no valid skin influence.')
        values = (_native_skin_weights(influences) if actor_weights else
                  {joint: weight / total for joint, weight in influences.items()})
        for joint, value in values.items():
            maps[joint][point] = value
    for i, values in enumerate(maps):
        if any(values):
            tag.SetWeightMap(slots[i], values)
    tag.SetGeomMg(obj.GetMg())
    tag.CalculateBoneStates(c4d.NOTOK)
    tag.WeightDirty()
    skin = c4d.BaseObject(c4d.Oskin)
    if skin is None:
        raise RuntimeError('Cinema 4D could not create the Skin deformer.')
    skin.SetName('OWM Skin')
    skin.InsertUnderLast(obj)
    return True


def _frame_time(c4d, frame, fps):
    """Retain the frame fraction; BaseTime(seconds) rounds to milliseconds."""
    if float(fps).is_integer():
        return c4d.BaseTime(int(frame), int(fps))
    from fractions import Fraction
    rate = Fraction(str(fps)).limit_denominator(1000000)
    return c4d.BaseTime(int(frame) * rate.denominator, rate.numerator)


def _keys(c4d, obj, frames, fps, warnings):
    """Bake position/rotation only, retaining the first frame's static scale."""
    channels = [[], []]
    previous = None
    first_scale, scale_changes = None, False
    order = obj[c4d.ID_BASEOBJECT_ROTATION_ORDER]
    for frame, values in enumerate(frames):
        matrix = _matrix(c4d, values, f'{obj.GetName()} frame {frame}')
        # With fresh objects there is no frozen transform; SetMl includes the
        # same decomposition Cinema 4D uses for its coordinate manager.
        obj.SetMl(matrix)
        scale_value = obj.GetRelScale()
        scale = (scale_value.x, scale_value.y, scale_value.z)
        if first_scale is None:
            first_scale = scale
        elif any(abs(a-b) > 1e-5*max(1.0, abs(a), abs(b)) for a, b in zip(first_scale, scale)):
            scale_changes = True
        rotation = obj.GetRelRot()
        if previous is not None:
            rotation = c4d.utils.GetOptimalAngle(previous, rotation, order)
        previous = rotation
        for target, value in zip(channels, (obj.GetRelPos(), rotation)):
            target.append(value)
    ids = (c4d.ID_BASEOBJECT_REL_POSITION, c4d.ID_BASEOBJECT_REL_ROTATION)
    for param, values in zip(ids, channels):
        for axis, component in enumerate((c4d.VECTOR_X, c4d.VECTOR_Y, c4d.VECTOR_Z)):
            desc = c4d.DescID(c4d.DescLevel(param, c4d.DTYPE_VECTOR, 0),
                              c4d.DescLevel(component, c4d.DTYPE_REAL, 0))
            track = c4d.CTrack(obj, desc)
            obj.InsertTrackSorted(track)
            curve = track.GetCurve()
            for frame, value in enumerate(values):
                key = curve.AddKey(_frame_time(c4d, frame, fps))
                if not key:
                    raise RuntimeError(f'Could not add a key to {obj.GetName()}.')
                key['key'].SetValue(curve, float(value[axis]))
                key['key'].SetInterpolation(curve, c4d.CINTERPOLATION_LINEAR)
    obj.SetMl(_matrix(c4d, frames[0], obj.GetName()))
    if scale_changes:
        warnings.append(f'{obj.GetName()}: animated scale was omitted; the first frame\'s scale is retained.')
    return 6


def _walk(root, prefix=()):
    index, obj = 0, root
    while obj is not None:
        path = prefix + (index,)
        yield path, obj
        yield from _walk(obj.GetDown(), path)
        index += 1
        obj = obj.GetNext()


def _vector_values(vector):
    return (float(vector.x), float(vector.y), float(vector.z))


def _transform_values(matrix):
    return tuple(v for vector in (matrix.off, matrix.v1, matrix.v2, matrix.v3)
                 for v in _vector_values(vector))


def _structural_snapshot(c4d, doc):
    from .c4d_octane import IMAGE_PLUGIN, PARAMETERS
    nodes = []
    for path, obj in _walk(doc.GetFirstObject()):
        record = [path, obj.GetName(), obj.GetType()]
        # CheckType includes native base types. CAJointObject can match polygon
        # ancestry without exposing PolygonObject's Python point accessors.
        if obj.GetType() == c4d.Opolygon:
            record.extend([obj.GetPointCount(), obj.GetPolygonCount()])
        tag = obj.GetFirstTag()
        bindings = []
        while tag is not None:
            if hasattr(tag, 'GetJointCount'):
                links = []
                for index in range(tag.GetJointCount()):
                    joint = tag.GetJoint(index, doc)
                    if joint is None:
                        raise RuntimeError('The scene has a missing joint in a Weight tag.')
                    names = []
                    while joint is not None:
                        names.append(joint.GetName())
                        joint = joint.GetUp()
                    links.append(tuple(reversed(names)))
                bindings.append(tuple(links))
            tag = tag.GetNext()
        record.append(bindings)
        nodes.append(record)
    materials = []
    material = doc.GetFirstMaterial()
    while material is not None:
        images = []
        for _, shader in _walk(material.GetFirstShader()):
            if shader.GetType() == IMAGE_PLUGIN:
                images.append(str(shader[PARAMETERS['IMAGETEXTURE_FILE']]).replace('\\', '/'))
        materials.append((material.GetName(), material.GetType(), sorted(images)))
        material = material.GetNext()
    engine = doc.GetActiveRenderData()[c4d.RDATA_RENDERENGINE]
    return dict(objects=nodes, materials=materials, renderEngine=engine)


def _pose_snapshots(c4d, doc, frames, fps, objects=None):
    snapshots = []
    last_frame = None
    for frame in frames:
        doc.SetTime(_frame_time(c4d, frame, fps))
        doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
        posed = objects if objects is not None else [obj for _, obj in _walk(doc.GetFirstObject())]
        snapshots.append([_transform_values(obj.GetMg()) for obj in posed])
        last_frame = frame
    if last_frame != 0:
        doc.SetTime(c4d.BaseTime(0))
        doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
    return snapshots


def _timeline_snapshot(doc):
    return dict(fps=doc.GetFps(), minimum=doc.GetMinTime().Get(),
                maximum=doc.GetMaxTime().Get(), previewMinimum=doc.GetLoopMinTime().Get(),
                previewMaximum=doc.GetLoopMaxTime().Get(), current=doc.GetTime().Get())


def _check_poses(expected, actual, message, objects=None, frames=None, *, native_nla=False):
    if len(expected) != len(actual):
        raise RuntimeError(message)
    for frame_index, (expected_frame, actual_frame) in enumerate(zip(expected, actual)):
        if len(expected_frame) != len(actual_frame):
            raise RuntimeError(message)
        for index, (before, after) in enumerate(zip(expected_frame, actual_frame)):
            detail = (f'{message} {objects[index].GetName()}, frame {frames[frame_index]}'
                      if objects is not None else message)
            if len(before) != len(after) or not all(math.isfinite(v) for v in (*before, *after)):
                raise RuntimeError(message)
            # Native NLA evaluates transformed translations at finite precision.
            # Cancellation after parent rotation can put micrometre-sized drift
            # on an axis whose expected value is zero. Compare position vectors
            # at their world-space magnitude, keeping the same relative limit;
            # basis/scale components retain their strict per-component check.
            position_scale = max(1.0, math.hypot(*before[:3]), math.hypot(*after[:3]))
            # Cinema's Motion System re-evaluates curves at native precision.
            # The full-body walk measures up to 0.00061 cm of world drift and
            # 1.1e-5 in a basis component over all 25 frames. Limit that
            # allowance to curve-to-NLA conversion. Reload and cross-source
            # comparisons still use the stricter relative tolerances.
            position_limit = max(1e-5 * position_scale, 0.001 if native_nla else 0.0)
            basis_epsilon = 2e-5 if native_nla else 1e-5
            if math.dist(before[:3], after[:3]) > position_limit:
                raise RuntimeError(f'{detail} (position difference {math.dist(before[:3], after[:3]):.6g} cm)')
            if any(abs(a-b) > basis_epsilon*max(1, abs(a), abs(b)) for a, b in zip(before[3:], after[3:])):
                raise RuntimeError(f'{detail} (basis difference {max(abs(a-b) for a,b in zip(before[3:],after[3:])):.6g})')


def _verify_saved(c4d, destination, expected, structure, poses, frames, motion=None, timeline=None):
    """Reopen only the new detached scene, verifying native data survived save."""
    from .c4d_motion import is_scale_track
    flags = c4d.SCENEFILTER_OBJECTS | c4d.SCENEFILTER_MATERIALS
    check = c4d.documents.LoadDocument(str(destination), flags)
    if check is None:
        raise RuntimeError('Cinema 4D could not reopen the scene it just saved.')
    try:
        counts = dict(meshCount=0, boneCount=0, skinCount=0, trackCount=0, materialCount=0)
        pending = [check.GetFirstObject()]
        while pending:
            obj = pending.pop()
            while obj is not None:
                for key, kind in (('meshCount', c4d.Opolygon), ('boneCount', c4d.Ojoint), ('skinCount', c4d.Oskin)):
                    counts[key] += int(obj.GetType() == kind)
                track = obj.GetFirstCTrack()
                while track is not None:
                    if is_scale_track(c4d, track):
                        raise RuntimeError('Saved Cinema 4D scene contains unexpected scale animation tracks.')
                    counts['trackCount'] += 1
                    curve = track.GetCurve()
                    if curve is None or curve.GetKeyCount() != expected['frameCount']:
                        raise RuntimeError('Saved Cinema 4D animation did not retain every baked key.')
                    track = track.GetNext()
                pending.append(obj.GetDown())
                obj = obj.GetNext()
        material = check.GetFirstMaterial()
        while material is not None:
            counts['materialCount'] += 1
            material = material.GetNext()
        if motion is not None:
            from .c4d_motion import motion_snapshot
            actual_motion = motion_snapshot(c4d, check, expected['frameCount'])
            if actual_motion != motion:
                raise RuntimeError('Saved Cinema 4D scene changed the Motion Clip or Motion Source.')
            counts['trackCount'] += sum(source['count'] for source in actual_motion['sources'])
        if any(counts[key] != expected[key] for key in counts):
            raise RuntimeError(f'Cinema 4D scene verification failed: {counts}')
        if _structural_snapshot(c4d, check) != structure:
            raise RuntimeError('Saved Cinema 4D scene changed hierarchy, skin links, materials or texture paths.')
        if timeline is not None and _timeline_snapshot(check) != timeline:
            raise RuntimeError('Saved Cinema 4D scene changed the project time or frame range.')
        actual = _pose_snapshots(c4d, check, frames, expected['fps'])
        _check_poses(poses, actual, 'Saved Cinema 4D scene did not preserve sampled animated joint transforms.')
    finally:
        c4d.documents.KillDocument(check)


def build_scene(payload, destination):
    """Save and verify an independent .c4d scene."""
    return _build_detached(payload, destination)


def _build_detached(payload, destination, consume=None):
    """Build and validate native data, then publish a file or consume the rig."""
    import c4d
    from c4d.modules import character
    from .c4d_octane import create_octane_materials, configure_octane_renderer

    destination = Path(destination).resolve()
    if destination.suffix.lower() != '.c4d':
        raise ValueError('The native scene destination must end in .c4d.')
    if destination.exists():
        raise FileExistsError(f'The native scene destination already exists: {destination}')
    asset = payload['asset']
    graph = _ordered_graph(asset['exportHierarchy'])
    models = _asset_models(asset)
    animation = payload.get('animation', {})
    fps = float(animation.get('fps', 30))
    last = int(animation.get('lastFrame', 0))
    if not math.isfinite(fps) or not 0 < fps <= 1000 or last < 0:
        raise ValueError('The animation frame range or FPS is invalid.')
    for track in animation.get('tracks', []):
        if len(track['frames']) != last+1:
            raise ValueError('Every baked joint track must include every animation frame.')
        for frame, values in enumerate(track['frames']):
            _matrix_values(values, f'{track["modelId"]} bone {track["boneIndex"]} frame {frame}')
    doc = c4d.documents.BaseDocument()
    objects, joints, skeletons, meshes = {}, {}, {}, []
    warnings = list(payload.get('warnings', []))
    report = dict(meshCount=0, boneCount=0, materialCount=0, skinCount=0, trackCount=0,
                  frameCount=last+1, fps=fps, upAxis='Y', unit='centimetres', warnings=warnings)
    try:
        doc.SetDocumentName(destination.name)
        doc.SetDocumentPath(str(destination.parent))
        display_fps = max(1, round(fps))
        doc.SetFps(display_fps)
        # C4D requires a non-empty project range: setting 0..0 moves the
        # minimum to -1 frame. Static maps use exactly one frame, starting at 0.
        end = _frame_time(c4d, last, fps) if last else c4d.BaseTime(1, display_fps)
        doc.SetMinTime(c4d.BaseTime(0))
        doc.SetMaxTime(end)
        doc.SetLoopMinTime(c4d.BaseTime(0))
        doc.SetLoopMaxTime(end)
        doc.SetTime(c4d.BaseTime(0))
        units = doc[c4d.DOCUMENT_DOCUNIT]
        units.SetUnitScale(1.0, c4d.DOCUMENT_UNIT_CM)
        doc[c4d.DOCUMENT_DOCUNIT] = units
        last_root = None
        for node in graph:
            kind, mid = node['type'], node['modelId']
            if kind == 'mesh':
                if not payload.get('includeMesh', True):
                    continue
                mesh = models[mid]['meshes'][int(node['meshIndex'])]
                obj = _mesh(c4d, mesh)
                meshes.append((obj, mesh, mid))
                report['meshCount'] += 1
            else:
                obj = c4d.BaseObject(c4d.Ojoint if kind == 'bone' else c4d.Onull)
                if kind == 'bone':
                    joints[(mid, int(node['boneIndex']))] = obj
                    report['boneCount'] += 1
            if obj is None:
                raise RuntimeError(f'Cinema 4D could not create {node["name"]}.')
            name = node['name']
            if payload.get('gameplay1P') and payload.get('heroName') and kind == 'entity' and not node.get('parentId'):
                name = str(payload['heroName'])
            obj.SetName(name)
            parent = objects.get(node.get('parentId'))
            if parent is not None:
                obj.InsertUnderLast(parent)
            else:
                doc.InsertObject(obj, pred=last_root)
                last_root = obj
            obj.SetMl(_matrix(c4d, node.get('matrix', IDENTITY), node['name']))
            objects[node['id']] = obj
            if kind == 'skeleton':
                if mid in skeletons:
                    raise ValueError('An export model has more than one skeleton root.')
                skeletons[mid] = obj

        materials = create_octane_materials(doc, payload.get('materials', {}) if meshes else {},
                                            texture_root=destination.parent, warnings=warnings)
        configure_octane_renderer(doc)
        # Multiple placements can bind the same native material. Verification
        # counts the saved document's materials, not the number of bindings.
        material = doc.GetFirstMaterial()
        while material is not None:
            report['materialCount'] += 1
            material = material.GetNext()
        for obj, mesh, mid in meshes:
            material = materials.get(mid, {}).get(str(mesh.get('materialKey', '0')))
            if material is not None:
                tag = c4d.TextureTag()
                tag[c4d.TEXTURETAG_MATERIAL] = material
                tag[c4d.TEXTURETAG_PROJECTION] = c4d.TEXTURETAG_PROJECTION_UVW
                obj.InsertTag(tag)
            rig = [joints[(mid, i)] for i in range(len(models[mid].get('bones', [])))]
            actor_weights = payload.get('operation') == 'createStagerRig' and payload.get('kind') == 'actor'
            report['skinCount'] += int(_skin(c4d, character, obj, mesh, rig, actor_weights=actor_weights))

        # Bind states above are captured at REST, before any animated transforms.
        for track in animation.get('tracks', []):
            obj = joints.get((track['modelId'], int(track['boneIndex'])))
            if obj is None:
                raise ValueError('An animation track references a missing joint.')
            report['trackCount'] += _keys(c4d, obj, track['frames'], fps, warnings)
        if payload.get('gameplay1P'):
            from .c4d_pov import optimize_gameplay1p
            optimized = optimize_gameplay1p(c4d, doc, payload, objects, joints, skeletons,
                                            meshes, models, warnings)
            objects, joints, skeletons = (optimized[key] for key in ('objects', 'joints', 'skeletons'))
            meshes = optimized['meshes']
            report.update(optimized['report'])
        sample_frames = sorted({0, last//2, last})
        posed_objects = [obj for _, obj in _walk(doc.GetFirstObject())]
        poses = _pose_snapshots(c4d, doc, sample_frames, fps, posed_objects)
        motion = None
        if payload.get('motionClip') and animation.get('tracks'):
            from .c4d_motion import create_motion_clips, motion_snapshot
            animated_models = {track['modelId'] for track in animation['tracks']}
            if not animated_models.issubset(skeletons):
                raise ValueError('An animated model has no skeleton root for its Motion Clip.')
            rigs = [(root, [joint for (model_id, _), joint in joints.items() if model_id == mid])
                    for mid, root in skeletons.items() if mid in animated_models]
            motion_name = str(payload.get('motionClipName') or payload.get('name') or destination.stem)
            report.update(create_motion_clips(c4d, doc, motion_name, end, rigs))
            motion = motion_snapshot(c4d, doc, last+1)
            if not report['motionClipCount'] or not report['motionTrackCount']:
                raise RuntimeError('Cinema 4D did not create the requested Motion Clip.')
            _check_poses(poses, _pose_snapshots(c4d, doc, sample_frames, fps, posed_objects),
                         'Motion Clip conversion did not preserve sampled animated joint transforms.', posed_objects, sample_frames, native_nla=True)
            # A shared Joints parent can reorder disjoint skeleton branches.
            # The comparison above uses the original object references, so it
            # checks their poses across grouping as well as NLA conversion.
            # Reload uses the final hierarchy, including any new parent null.
            poses = _pose_snapshots(c4d, doc, sample_frames, fps)
        if consume is not None:
            return consume(doc, objects, report)
        # Snapshots leave frame zero evaluated. Avoid repeating that expensive
        # full-scene pass for static maps before structure validation and save.
        structure = _structural_snapshot(c4d, doc)
        timeline = _timeline_snapshot(doc)
        flags = c4d.SAVEDOCUMENTFLAGS_DONTADDTORECENTLIST
        if not c4d.documents.SaveDocument(doc, str(destination), flags, c4d.FORMAT_C4DEXPORT):
            raise RuntimeError('Cinema 4D failed to save the native scene.')
        if not destination.is_file() or destination.stat().st_size == 0:
            raise RuntimeError('Cinema 4D reported success but did not create a scene file.')
        _verify_saved(c4d, destination, report, structure, poses, sample_frames, motion, timeline)
        report['timeline'] = timeline
        report['bytes'] = destination.stat().st_size
        report['verifiedNativeScene'] = True
        return report
    finally:
        c4d.documents.KillDocument(doc)
