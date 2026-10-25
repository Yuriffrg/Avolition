"""Simplify an exported Gameplay1P rig while preserving retained animation.

Planning uses source bone identity and actual skin influences, never display
names alone. Native edits operate only on the detached export document, before
the Motion Source is created. The original skin bind state is not recalculated.
"""
from __future__ import annotations

import math


def _children(first):
    while first is not None:
        yield first
        first = first.GetNext()


def _tracks(obj):
    return _children(obj.GetFirstCTrack())


def _bone_role(node):
    source = str(node.get('sourceName', '')).casefold()
    if source == 'bone_0050':
        return 'left'
    if source == 'bone_0035':
        return 'right'
    # Synthetic/legacy payloads can predate sourceName. Current user renames
    # retain sourceName and therefore cannot accidentally change rig selection.
    if not source:
        return {'clavicle_l': 'left', 'clavicle_r': 'right'}.get(str(node.get('name', '')).casefold())
    return None


def plan_gameplay1p(graph, models):
    """Return an ID-based pruning/reparenting plan without editing source data."""
    nodes = {node['id']: node for node in graph}
    parents = {node['id']: node.get('parentId') for node in graph}
    children = {ident: [] for ident in nodes}
    for ident, parent in parents.items():
        if parent in children:
            children[parent].append(ident)

    def ancestors(ident, relationships=parents):
        seen = set()
        while ident is not None and ident not in seen:
            seen.add(ident)
            yield ident
            ident = relationships.get(ident)

    candidates = []
    for root in graph:
        if root['type'] != 'skeleton':
            continue
        roles = {'left': [], 'right': []}
        for node in graph:
            role = _bone_role(node) if node['type'] == 'bone' else None
            if role and node['modelId'] == root['modelId'] and root['id'] in ancestors(node['id']):
                roles[role].append(node['id'])
        if all(len(roles[role]) == 1 for role in roles):
            candidates.append((len(list(ancestors(root['id']))), root, roles))
    if not candidates:
        return None
    _, skeleton, roles = min(candidates, key=lambda entry: entry[0])
    primary = skeleton['modelId']
    clavicles = [roles['left'][0], roles['right'][0]]
    arm_ids = {node['id'] for node in graph if node['type'] == 'bone'
               and any(root in ancestors(node['id']) for root in clavicles)}

    camera_roots = {node['id'] for node in graph if node['type'] == 'bone'
                    and node['modelId'] == primary
                    and (str(node.get('sourceName', '')).casefold() == 'bone_007d'
                         or not node.get('sourceName') and str(node.get('name', '')).casefold() == 'camera')}
    camera_ids = {node['id'] for node in graph if node['type'] == 'bone'
                  and any(root in ancestors(node['id']) for root in camera_roots)} - arm_ids
    bone_ids = {(node['modelId'], int(node['boneIndex'])): node['id']
                for node in graph if node['type'] == 'bone'}
    camera_mesh_indices, mixed_camera_meshes = set(), []
    for index, mesh in enumerate(models[primary].get('meshes', [])):
        influence = {bone_ids.get((primary, int(joint))) for joint, weight
                     in zip(mesh.get('skinIndices', []), mesh.get('skinWeights', [])) if float(weight) > 0}
        if influence and influence <= camera_ids:
            camera_mesh_indices.add(index)
        elif influence & camera_ids:
            mixed_camera_meshes.append(mesh['name'])
    camera_mesh_ids = {node['id'] for node in graph if node['type'] == 'mesh'
                       and node['modelId'] == primary and int(node['meshIndex']) in camera_mesh_indices}

    # Drop only structural null leaves: meshes, skeleton roots and the exported
    # entity root are always retained. Repeat to remove now-empty helper chains.
    empty = set()
    while True:
        more = {ident for ident, node in nodes.items() if ident not in empty
                and node['type'] in ('entity', 'model') and parents[ident] is not None
                and not any(child not in empty and child not in camera_mesh_ids for child in children[ident])}
        if not more:
            break
        empty.update(more)

    weighted = set()
    for mid, model in models.items():
        skin_meshes = [(index, mesh) for index, mesh in enumerate(model.get('meshes', []))
                       if mesh.get('skinIndices') and mesh.get('skinWeights')]
        if not skin_meshes:
            # Missing skin metadata is not proof that a skeleton is unused.
            weighted.update(node['id'] for node in graph if node['type'] == 'bone' and node['modelId'] == mid)
            continue
        indices = {int(index) for mesh_index, mesh in skin_meshes
                   if mid != primary or mesh_index not in camera_mesh_indices
                   for index, weight in zip(mesh['skinIndices'], mesh['skinWeights']) if float(weight) > 0}
        weighted.update(node['id'] for node in graph if node['type'] == 'bone'
                        and node['modelId'] == mid and int(node['boneIndex']) in indices)
    attached = {ident for ident, node in nodes.items() if node['type'] == 'bone'
                and any(child not in empty and child not in camera_mesh_ids and nodes[child]['type'] != 'bone'
                        for child in children[ident])}
    essential = weighted | attached | set(clavicles)
    promoted = {ident: skeleton['id'] for ident in clavicles if parents[ident] != skeleton['id']}

    # Preserve any remaining weighted/attachment branches outside the arms, but
    # collapse their unused engine root. Mixed camera/arm meshes stay intact.
    for ident in (node['id'] for node in graph if node['id'] in essential and node['id'] not in arm_ids):
        if nodes[ident]['modelId'] != primary:
            continue
        chain = []
        for current in ancestors(ident):
            if current == skeleton['id']:
                break
            chain.append(current)
        if len(chain) > 1 and chain[-1] not in essential:
            promoted[chain[-2]] = skeleton['id']
    planned_parents = dict(parents, **promoted)
    retained = set()
    for ident in essential:
        retained.update(ancestors(ident, planned_parents))
    removed_bones = {ident for ident, node in nodes.items()
                     if node['type'] == 'bone' and ident not in retained}
    removed = empty | removed_bones | camera_mesh_ids
    for ident, node in nodes.items():
        if ident not in removed and planned_parents.get(ident) in removed:
            raise ValueError('Gameplay1P cleanup would orphan a required object.')
    return dict(skeleton=skeleton['id'], primaryModel=primary, clavicles=clavicles,
                promoted=promoted, removed=removed, removedBones=removed_bones,
                removedEmptyNulls=empty,
                removedCameraMeshIds=camera_mesh_ids, mixedCameraMeshes=mixed_camera_meshes,
                outsideArmJoints=[node['id'] for node in graph if node['type'] == 'bone'
                                  and node['modelId'] == primary and node['id'] not in arm_ids
                                  and node['id'] not in removed])


def _matrix_values(matrix):
    return tuple(float(value) for vector in (matrix.off, matrix.v1, matrix.v2, matrix.v3)
                 for value in (vector.x, vector.y, vector.z))


def _source_matrix(matrix):
    """Invert the native centimetres/Z-reflection boundary for _keys()."""
    x, y, z, p = matrix.v1, matrix.v2, matrix.v3, matrix.off
    return [x.x, x.y, -x.z, 0, y.x, y.y, -y.z, 0, -z.x, -z.y, z.z, 0,
            p.x / 100, p.y / 100, -p.z / 100, 1]


def _rest_values(value):
    if isinstance(value, dict):
        return tuple((key, _rest_values(item)) for key, item in sorted(value.items()))
    if hasattr(value, 'v1') and hasattr(value, 'off'):
        return _matrix_values(value)
    return value


def _skin_state(doc, meshes):
    result = []
    for obj, _, _ in meshes:
        for tag in _children(obj.GetFirstTag()):
            if not hasattr(tag, 'GetJointCount') or not hasattr(tag, 'GetWeightMap'):
                continue
            records = []
            for index in range(tag.GetJointCount()):
                joint = tag.GetJoint(index, doc)
                if joint is None:
                    raise RuntimeError('Gameplay1P cleanup found an invalid skin joint link.')
                weights = tuple(tag.GetWeightMap(index, obj.GetPointCount()))
                records.append((joint, weights, _rest_values(tag.GetJointRestState(index))))
            result.append((tag, records, _matrix_values(tag.GetGeomMg())))
    return result


def _verify_skin(doc, meshes, original, removed):
    actual = _skin_state(doc, meshes)
    if len(actual) != len(original):
        raise RuntimeError('Gameplay1P cleanup changed the skin tags.')
    for (tag, records, bind), (old_tag, old_records, old_bind) in zip(actual, original):
        kept = [record for record in old_records if record[0] not in removed]
        if tag != old_tag or bind != old_bind or records != kept:
            raise RuntimeError('Gameplay1P cleanup changed skin weights or their original bind state.')


def _geometry_state(meshes):
    result, points = [], []
    for obj, _, _ in meshes:
        cache = obj.GetDeformCache() if hasattr(obj, 'GetDeformCache') else None
        if cache is None:
            result.append((obj.GetName(), 0))
            continue
        if not hasattr(cache, 'GetAllPoints'):
            raise RuntimeError('Gameplay1P cleanup could not inspect a deformed mesh cache.')
        vertices = cache.GetAllPoints()
        result.append((obj.GetName(), len(vertices)))
        transform = obj.GetMg()
        for point in vertices:
            world = transform * point
            points.append((world.x, world.y, world.z))
    return result, points


def _curve_snapshot(curve):
    return [(curve.GetKey(index).GetTime().Get(), curve.GetKey(index).GetValue(),
             curve.GetKey(index).GetInterpolation()) for index in range(curve.GetKeyCount())]


def _clavicle_branch(obj, clavicles):
    while obj is not None:
        for index, root in enumerate(clavicles):
            if obj == root:
                return index
        obj = obj.GetUp()
    return None


def _mesh_shift_weights(doc, meshes, clavicles):
    """Per-vertex arm weights predict the requested change to skin deformation."""
    result = []
    for obj, _, _ in meshes:
        tags = [tag for tag in _children(obj.GetFirstTag()) if hasattr(tag, 'GetJointCount')]
        if len(tags) > 1:
            raise RuntimeError('Gameplay1P height adjustment cannot verify multiple skin tags on one mesh.')
        count = obj.GetPointCount()
        weights = [[0.0, 0.0] for _ in range(count)]
        if tags:
            tag = tags[0]
            for slot in range(tag.GetJointCount()):
                branch = _clavicle_branch(tag.GetJoint(slot, doc), clavicles)
                if branch is not None:
                    for point, weight in enumerate(tag.GetWeightMap(slot, count)):
                        weights[point][branch] += weight
        else:
            branch = _clavicle_branch(obj, clavicles)
            if branch is not None:
                for point in weights:
                    point[branch] = 1.0
        result.append(weights)
    return result


def _offset_clavicle_y(c4d, doc, clavicles, retained, meshes, poses, geometry, fps, last):
    """Subtract each root's first local Y, without compensating its bind pose."""
    from .c4d_scene import _frame_time, _check_poses

    indices = [next(index for index, (_, obj) in enumerate(retained) if obj == root.GetUp())
               for root in clavicles]
    branches = [_clavicle_branch(obj, clavicles) for _, obj in retained]
    mesh_weights = _mesh_shift_weights(doc, meshes, clavicles)
    offsets, reports, snapshots = [], [], []
    for root in clavicles:
        tracks = list(_tracks(root))
        y_tracks = [track for track in tracks
                    if track.GetDescriptionID().GetDepth() > 1
                    and track.GetDescriptionID()[0].id == c4d.ID_BASEOBJECT_REL_POSITION
                    and track.GetDescriptionID()[1].id == c4d.VECTOR_Y]
        if len(y_tracks) > 1:
            raise RuntimeError('Gameplay1P height adjustment found duplicate position-Y tracks.')
        curve = y_tracks[0].GetCurve() if y_tracks else None
        other = [(track, _curve_snapshot(track.GetCurve())) for track in tracks if track not in y_tracks]
        original = _curve_snapshot(curve) if curve is not None else []
        if original and original[0][0] != 0:
            raise RuntimeError('Gameplay1P height adjustment needs a position-Y key at frame zero.')
        offset = original[0][1] if original else float(root.GetRelPos().y)
        offsets.append(offset)
        if curve is not None:
            for index, (_, value, _) in enumerate(original):
                curve.GetKey(index).SetValue(curve, value - offset)
            curve.SetKeyDirty()
        position = root.GetRelPos()
        root.SetRelPos(c4d.Vector(position.x, 0.0, position.z))
        snapshots.append((curve, original, other))
        reports.append(dict(name=root.GetName(), offsetCm=offset, firstY=0.0,
                            keyCount=len(original), deltasPreserved=True))

    for curve, original, other in snapshots:
        if curve is not None:
            expected = [(time, value-original[0][1], interpolation) for time, value, interpolation in original]
            if _curve_snapshot(curve) != expected:
                raise RuntimeError('Gameplay1P height adjustment changed position-Y keys or their timing.')
        if any(_curve_snapshot(track.GetCurve()) != saved for track, saved in other):
            raise RuntimeError('Gameplay1P height adjustment changed an unrelated animation channel.')

    expected_poses, actual_poses, checked_vertices = [], [], 0
    for frame in range(last + 1):
        doc.SetTime(_frame_time(c4d, frame, fps))
        doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
        # A local-Y shift follows the parent's world Y basis, including any
        # authored entity rotation/scale. It never changes a joint's basis.
        shifts = [tuple(-offset * component for component in poses[frame][index][6:9])
                  for index, offset in zip(indices, offsets)]
        wanted = []
        for matrix, branch in zip(poses[frame], branches):
            changed = list(matrix)
            if branch is not None:
                changed[:3] = [value + delta for value, delta in zip(changed[:3], shifts[branch])]
            wanted.append(changed)
        expected_poses.append(wanted)
        actual_poses.append([_matrix_values(obj.GetMg()) for _, obj in retained])
        if frame in geometry:
            shapes, vertices = _geometry_state(meshes)
            old_shapes, old_vertices = geometry[frame]
            if shapes != old_shapes:
                raise RuntimeError('Gameplay1P height adjustment changed the deformed mesh topology.')
            wanted_vertices, cursor = [], 0
            for (_, count), weights in zip(shapes, mesh_weights):
                if count and count != len(weights):
                    raise RuntimeError('Gameplay1P height adjustment cannot map the deformed skin vertices.')
                for point in range(count):
                    shift = [sum(weights[point][branch] * shifts[branch][axis] for branch in (0, 1))
                             for axis in range(3)]
                    wanted_vertices.append([value + delta for value, delta in zip(old_vertices[cursor], shift)])
                    cursor += 1
            _check_poses([wanted_vertices], [vertices],
                         'Gameplay1P height adjustment did not preserve the expected skin-weighted shift.')
            checked_vertices += len(vertices)
    _check_poses(expected_poses, actual_poses,
                 'Gameplay1P height adjustment did not preserve the expected arm/attachment motion.')
    doc.SetTime(_frame_time(c4d, 0, fps))
    doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
    for root in clavicles:
        if root.GetRelPos().y != 0:
            raise RuntimeError('Gameplay1P height adjustment did not start the clavicle at Y zero.')
    return dict(clavicleYOffsets=reports, verifiedShiftedPoseFrames=last + 1,
                verifiedShiftedDeformedVertices=checked_vertices)


def optimize_gameplay1p(c4d, doc, payload, objects, joints, skeletons, meshes, models, warnings):
    """Optimize the detached rig and verify its weights and evaluated animation."""
    from .c4d_scene import _frame_time, _keys, _matrix, _matrix_values as validate_matrix, _check_poses

    unchanged = dict(objects=objects, joints=joints, skeletons=skeletons, meshes=meshes, report={})
    if not payload.get('gameplay1P'):
        return unchanged
    graph = payload['asset']['exportHierarchy']
    plan = plan_gameplay1p(graph, models)
    if plan is None:
        warnings.append('Gameplay1P rig cleanup was skipped because both clavicle joints could not be identified.')
        return unchanged
    fps = float(payload.get('animation', {}).get('fps', 30))
    last = int(payload.get('animation', {}).get('lastFrame', 0))
    remove_ids = plan['removed'] & set(objects)
    removed = [obj for ident, obj in objects.items() if ident in remove_ids]
    retained = [(ident, obj) for ident, obj in objects.items() if ident not in remove_ids]
    promote = [(objects[ident], objects[parent]) for ident, parent in plan['promoted'].items()]
    removed_meshes = [obj for ident, obj in objects.items() if ident in plan['removedCameraMeshIds']]
    remaining_meshes = [entry for entry in meshes if entry[0] not in removed_meshes]
    skin = _skin_state(doc, remaining_meshes)
    for _, records, _ in skin:
        if any(joint in removed and any(weight > 0 for weight in weights) for joint, weights, _ in records):
            raise RuntimeError('Gameplay1P cleanup cannot remove a weighted joint.')

    before, geometry, baked = [], {}, [[] for _ in promote]
    sample_frames = {0, last // 2, last}
    for frame in range(last + 1):
        doc.SetTime(_frame_time(c4d, frame, fps))
        doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
        before.append([_matrix_values(obj.GetMg()) for _, obj in retained])
        for index, (obj, parent) in enumerate(promote):
            values = _source_matrix(~parent.GetMg() * obj.GetMg())
            validate_matrix(values, f'{obj.GetName()} optimized frame {frame}')
            baked[index].append(values)
        if frame in sample_frames:
            geometry[frame] = _geometry_state(remaining_meshes)
    # Removing parent scale must never introduce animated scale into the newly
    # baked roots. The caller's no-scale-key policy cannot reproduce that case.
    for (obj, _), frames in zip(promote, baked):
        scales = [tuple(math.hypot(*matrix[start:start + 3]) for start in (0, 4, 8)) for matrix in frames]
        if any(abs(a-b) > 1e-5*max(1, abs(a), abs(b)) for scale in scales[1:]
               for a, b in zip(scales[0], scale)):
            raise ValueError(f'{obj.GetName()}: Gameplay1P cleanup would require animated scale.')

    doc.SetTime(_frame_time(c4d, 0, fps))
    doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
    for (obj, parent), frames in zip(promote, baked):
        obj.Remove()
        obj.InsertUnderLast(parent)
        for track in list(_tracks(obj)):
            if track.GetDescriptionID()[0].id in (c4d.ID_BASEOBJECT_REL_POSITION, c4d.ID_BASEOBJECT_REL_ROTATION):
                track.Remove()
        if payload.get('animation', {}).get('tracks'):
            _keys(c4d, obj, frames, fps, warnings)
        else:
            obj.SetMl(_matrix(c4d, frames[0], obj.GetName()))
    for tag, records, _ in skin:
        for joint, _, _ in records:
            if joint in removed:
                tag.RemoveJoint(joint)
        tag.WeightDirty()
    # Remove descendants before their ancestors; no retained child is under a
    # removed parent after the planned root promotions have been applied.
    def depth(obj):
        result = 0
        while obj.GetUp() is not None:
            result += 1
            obj = obj.GetUp()
        return result
    for obj in sorted(removed, key=depth, reverse=True):
        obj.Remove()
    objects[plan['skeleton']].SetName('Joints')
    _verify_skin(doc, remaining_meshes, skin, removed)

    after, checked_vertices = [], 0
    for frame in range(last + 1):
        doc.SetTime(_frame_time(c4d, frame, fps))
        doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
        after.append([_matrix_values(obj.GetMg()) for _, obj in retained])
        if frame in sample_frames:
            shapes, vertices = _geometry_state(remaining_meshes)
            old_shapes, old_vertices = geometry[frame]
            if shapes != old_shapes:
                raise RuntimeError('Gameplay1P cleanup changed a deformed mesh cache.')
            _check_poses([old_vertices], [vertices], 'Gameplay1P cleanup changed the skinned mesh deformation.')
            checked_vertices += len(vertices)
    _check_poses(before, after, 'Gameplay1P cleanup did not preserve the original world animation.')
    doc.SetTime(_frame_time(c4d, 0, fps))
    doc.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
    remaining_objects = dict(retained)
    remaining_joints = {key: obj for key, obj in joints.items() if obj not in removed}
    remaining_skeletons = {key: obj for key, obj in skeletons.items() if obj not in removed}
    origin_report = _offset_clavicle_y(c4d, doc, [objects[ident] for ident in plan['clavicles']],
                                      retained, remaining_meshes, after, geometry, fps, last)
    # Intentional height changes move the visible arms/weapon. Keep the original
    # bind data: rebinding or frozen-parent compensation would cancel the shift.
    _verify_skin(doc, remaining_meshes, skin, removed)
    outside = [objects[ident].GetName() for ident in plan['outsideArmJoints'] if ident in remaining_objects]
    if outside:
        warnings.append('Gameplay1P cleanup retained joints outside the arms because exported meshes or attachments need them.')
    if plan['mixedCameraMeshes']:
        warnings.append('Gameplay1P cleanup retained meshes with mixed camera/other joint weights: '
                        + ', '.join(plan['mixedCameraMeshes']))
    report = dict(boneCount=len(remaining_joints), meshCount=len(remaining_meshes), skinCount=len(skin),
                  trackCount=sum(1 for obj in remaining_objects.values() for _ in _tracks(obj)),
                  povOptimized=True, removedBoneCount=len(plan['removedBones']),
                  removedEmptyNulls=len(plan['removedEmptyNulls']),
                  removedCameraMeshes=[obj.GetName() for obj in removed_meshes],
                  preservedOutsideArmJoints=outside,
                  optimizedRoots=[obj.GetName() for obj, _ in promote],
                  skinVerification=True, verifiedDeformedVertices=checked_vertices,
                  verifiedPoseFrames=last + 1)
    report.update(origin_report)
    return dict(objects=remaining_objects, joints=remaining_joints, skeletons=remaining_skeletons,
                meshes=remaining_meshes, report=report)
