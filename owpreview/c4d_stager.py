"""Insert a reviewed, detached animation library into the current C4D document."""
import hashlib
import json
import math
import os
from pathlib import Path
import uuid

from .c4d_motion import _children, _motion_head, SOURCE_END
from .c4d_scene import (IDENTITY, _build_detached, _check_poses, _pose_snapshots,
                        _walk, build_scene)
from .c4d_octane import IMAGE_PLUGIN, PARAMETERS, _relative_texture


# Private data on actual objects and NLA sources is serialized and participates
# in the same Undo as their insertion. Document IDs are only review tickets.
MARKER = 1066202606
_reviewed_document = None
_review_token = None


def _cameras(c4d, doc):
    for _, node in _walk(doc.GetFirstObject()):
        if node.CheckType(c4d.Ocamera) and node[c4d.CAMERA_PROJECTION] == c4d.Pperspective:
            yield format(node.GetGUID() & 0xffffffffffffffff, '016x'), node


def _camera_labels(cameras):
    """For duplicate camera names show the hierarchy and a short native ID."""
    names = [node.GetName() for _, node in cameras]
    result = []
    for ident, node in cameras:
        name = node.GetName()
        label = name
        if names.count(name) > 1:
            path = []
            parent = node
            while parent is not None:
                path.append(parent.GetName())
                parent = parent.GetUp()
            label = ' / '.join(reversed(path)) + f' [{ident[-6:]}]'
        result.append(dict(id=ident, name=name, label=label))
    return result


def _destination(c4d, doc, rig_ids=None):
    global _review_token, _reviewed_document
    if doc is None:
        raise ValueError('Open a Cinema 4D project before creating a Stager rig.')
    if not _same_review(doc):
        _reviewed_document, _review_token = doc, uuid.uuid4().hex
    cameras = list(_cameras(c4d, doc))
    existing = {}
    if rig_ids:
        from whitehound_camera.registration import registered_rig
        for kind in ('pov', 'actor'):
            info = registered_rig(doc, rig_ids[kind]) if rig_ids.get(kind) else None
            if info is None:
                continue
            entry = {}
            if info['target'] is not None:
                entry['targetName'] = info['target'].GetName()
            if info['target'] is None or info['root'] is None or None in info['sources'].values():
                entry['needsRepair'] = True
            if kind == 'pov':
                camera = info.get('camera') or info.get('root')
                while camera is not None:
                    camera_id = next((ident for ident, node in cameras if node == camera), None)
                    if camera_id:
                        entry['cameraId'] = camera_id
                        break
                    camera = camera.GetUp()
            existing[kind] = entry
    return dict(documentId=_review_token, documentName=doc.GetDocumentName(),
                cameras=_camera_labels(cameras), existingRigs=existing)


def _same_review(doc):
    try:
        return _reviewed_document == doc
    except ReferenceError:
        # The user closed the previously reviewed document and reopened a file.
        return False


def _marker(node):
    return node.GetDataInstance().GetContainerInstance(MARKER)


def _mark(c4d, node, rig_id, role, ident='', fingerprint=''):
    data = c4d.BaseContainer()
    for index, value in enumerate((rig_id, role, ident, fingerprint), 1):
        data.SetString(index, value)
    node.GetDataInstance().SetContainer(MARKER, data)


def _get_registration():
    try:
        from whitehound_camera.registration import register_rig
    except ImportError as exc:
        raise RuntimeError('Install the matching Avolition Stager plugin and restart Cinema 4D.') from exc
    return register_rig


def _source_list(doc):
    head = doc.GetNLARoot(False)
    return list(_children(head.GetDown())) if head is not None else []


def _affected_clips(c4d, doc, sources):
    clips = []
    for _, owner in _walk(doc.GetFirstObject()):
        tag = owner.GetTag(c4d.Tmotionsystem)
        if tag is None:
            continue
        for layer in _children(_motion_head(c4d, tag).GetDown()):
            for clip in _children(_motion_head(c4d, layer).GetDown()):
                if clip[c4d.ID_MT_CLIP_SOURCE] in sources:
                    clips.append(f'{owner.GetName()} / {layer.GetName()} / {clip.GetName()}')
    if not clips:
        return ''
    remaining = f' (+{len(clips) - 8} more)' if len(clips) > 8 else ''
    return ' Linked Motion Clips: ' + ', '.join(clips[:8]) + remaining + '.'


def _library_state(c4d, doc, payload):
    from whitehound_camera import registration as stager
    rig_id = payload['rigId']
    registration = stager.registered_rig(doc, rig_id)
    if registration is None:
        return None, None, {}, [], False
    # Sending a take creates camera/actor copies with the original objects'
    # private data. Stager's native registry points at the actual source rig.
    root, target = registration['root'], registration['target']
    sources = dict(registration['sources'])
    repair = root is None or target is None or None in sources.values()
    if repair:
        if getattr(stager, 'REGISTRATION_REPAIR_VERSION', 0) < 1:
            raise ValueError('Recovering this saved rig requires Avolition Stager 0.19.1 or later. '
                             'Install its Cinema 4D plugin and restart Cinema 4D.')
        assigned = registration.get('assignedRoot')
        if root is None and assigned is not None:
            mark = _marker(assigned)
            if mark is not None and mark.GetString(1) == rig_id and mark.GetString(2) == 'root':
                root = assigned
        target = target or registration.get('assignedTarget')
        if root is None and target is not None:
            parent = target
            while parent is not None:
                mark = _marker(parent)
                if mark is not None and mark.GetString(1) == rig_id and mark.GetString(2) == 'root':
                    root = parent
                    break
                parent = parent.GetUp()
            if root is None:
                raise ValueError('The surviving Motion System is outside its imported rig. '
                                 'Restore its character root before importing.')
        if root is None:
            # Take hierarchies clone even private data. A matching marker by
            # itself is insufficient evidence to adopt or overwrite an object.
            if any((mark := _marker(obj)) is not None and mark.GetString(1) == rig_id
                   and mark.GetString(2) in ('root', 'member') for _, obj in _walk(doc.GetFirstObject())):
                raise ValueError('A copy or part of this rig remains in the scene. Restore the original '
                                 'character root or reconnect its Actor/POV assignment in Stager.')
        elif target is None:
            try:
                target = _one_target(c4d, root)
            except ValueError as exc:
                raise ValueError('The imported character remains, but its Motion System cannot be identified. '
                                 'Undo the hierarchy edit or reconnect its original Motion System in Stager.') from exc
    if root is not None:
        mark = _marker(root)
        if mark is None or mark.GetString(1) != rig_id or mark.GetString(2) != 'root':
            raise ValueError('The saved character link points to a different object. Review its Stager assignment.')
        if target is None or target.GetTag(c4d.Tmotionsystem) is None:
            raise ValueError('The registered target has lost its Motion System tag. Restore the tag before importing.')
    available = _source_list(doc)
    scene_roots = list(_children(doc.GetFirstObject()))
    misplaced = []
    for ident, node in list(sources.items()):
        if node is None:
            matches = [obj for obj in (*available, *scene_roots) if (mark := _marker(obj)) is not None
                       and mark.GetString(1) == rig_id and mark.GetString(2) == 'source'
                       and mark.GetString(3) == ident]
            if len(matches) > 1:
                raise ValueError('Several copies of the same Motion Source remain. Restore its original link in Stager.')
            if not matches:
                del sources[ident]
                continue
            node = sources[ident] = matches[0]
        mark = _marker(node) if node is not None else None
        if (mark is None or mark.GetString(1) != rig_id or mark.GetString(2) != 'source'
                or mark.GetString(3) != ident):
            raise ValueError('A registered Motion Source was removed. Restore it before importing.')
        if mark.GetString(5) and mark.GetString(5) != payload['rigFingerprint']:
            raise ValueError('The surviving Motion Sources belong to a different rig geometry or bind pose.')
        if node not in available:
            # C4D 2026 Redo can restore the first-ever NLA sources at scene
            # root. Only repair the exact linked nodes on an explicit import.
            if node not in scene_roots:
                raise ValueError('A registered Motion Source left its document library.')
            misplaced.append(node)
    if root is not None and _marker(root).GetString(4) != payload['rigFingerprint']:
        raise ValueError('The imported rig geometry, materials or bind pose changed. Existing takes were preserved.'
                         + _affected_clips(c4d, doc, sources.values()))
    for row in payload['actions']:
        if row['id'] in sources and _marker(sources[row['id']]).GetString(4) != row['fingerprint']:
            raise ValueError(f'{row["name"]} has different animation data. Its Motion Source was preserved.'
                             + _affected_clips(c4d, doc, [sources[row['id']]]))
    return root, target, sources, misplaced, repair


def _persist_textures(doc, directory):
    """Replace validated job-relative shader paths before the job is collected."""
    base = Path(os.environ.get('LOCALAPPDATA') or Path.home()) / 'Avolition/Stager/textures'
    files = {}
    for material in _children(doc.GetFirstMaterial()):
        for shader in _children(material.GetFirstShader()):
            if shader.GetType() != IMAGE_PLUGIN:
                continue
            param = PARAMETERS['IMAGETEXTURE_FILE']
            relative = _relative_texture({'relativePath': str(shader[param])}, directory)
            if relative not in files:
                content = (directory / relative).read_bytes()
                digest = hashlib.sha256(content).hexdigest()
                path = base / (digest + '.png')
                base.mkdir(parents=True, exist_ok=True)
                if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                    temporary = base / (uuid.uuid4().hex + '.tmp')
                    try:
                        temporary.write_bytes(content)
                        temporary.replace(path)
                    finally:
                        temporary.unlink(missing_ok=True)
                files[relative] = str(path)
            shader[param] = files[relative]


def _scales(matrix):
    return tuple(math.hypot(*matrix[i:i+3]) for i in (0, 4, 8))


def _rig_scales(payload, base_action):
    tracks = {(t['modelId'], t['boneIndex']): t for t in base_action['animation']['tracks']}
    result = {}
    for node in payload['asset']['exportHierarchy']:
        if node['type'] == 'bone':
            track = tracks.get((node['modelId'], node['boneIndex']))
            result[node['id']] = _scales(track['frames'][0] if track else node.get('matrix', IDENTITY))
    return result


def _action_payload(payload, row, include_mesh, rig_scales):
    # Every source describes the same rig. Explicit rest keys reset unassigned
    # joints, e.g. an arms-only Idle played immediately after a weapon Reload.
    graph = payload['asset']['exportHierarchy']
    animation = row['animation']
    authored = {(t['modelId'], t['boneIndex']): t for t in animation['tracks']}
    if not authored:
        raise ValueError(f'{row["name"]} has no animated joints.')
    tracks = []
    warnings = list(payload.get('warnings', []))
    for node in graph:
        if node['type'] != 'bone':
            continue
        key = (node['modelId'], node['boneIndex'])
        track = authored.pop(key, None)
        if track is None:
            track = dict(modelId=key[0], boneIndex=key[1],
                         frames=[node.get('matrix', IDENTITY)] * (animation['lastFrame'] + 1))
        # A shared hierarchy has one static scale per joint. Preserve its
        # Idle scale and all source rotations/translations. ADS Down on the
        # real Widowmaker has a different static scale on bone_00DE.
        scale = _scales(track['frames'][0])
        wanted = rig_scales[node['id']]
        if any(not math.isclose(s, w, rel_tol=1e-5, abs_tol=1e-6) for s, w in zip(scale, wanted)):
            if min(scale) < 1e-10:
                raise ValueError(f'{node["name"]} has a zero joint scale.')
            factors = [w / s for w, s in zip(wanted, scale)]
            adjusted = []
            for matrix in track['frames']:
                values = list(matrix)
                for start, factor in zip((0, 4, 8), factors):
                    values[start:start + 3] = [v * factor for v in values[start:start + 3]]
                adjusted.append(values)
            track = dict(track, frames=adjusted)
            warnings.append(f'{row["name"]}: {node["name"]} uses the rig’s shared static scale '
                            f'({", ".join(f"{v:.6g}" for v in wanted)}).')
        tracks.append(track)
    if authored:
        raise ValueError('An animation track references a missing joint.')
    return dict(payload, animation=dict(animation, tracks=tracks), includeMesh=include_mesh,
                motionClip=True, motionClipName=row['name'], warnings=warnings)


def _one_target(c4d, root):
    nodes = [root, *(obj for _, obj in _walk(root.GetDown()))]
    targets = [node for node in nodes if node.GetTag(c4d.Tmotionsystem) is not None]
    if len(targets) != 1:
        raise ValueError('The imported hierarchy must have one Motion System target.')
    return targets[0]


def _layer_clip(c4d, target):
    layer = _motion_head(c4d, target.GetTag(c4d.Tmotionsystem)).GetDown()
    return _motion_head(c4d, layer).GetDown()


def _stage(c4d, payload, destination, publish, rig_scales, include_mesh):
    """Build geometry/weights once, then evaluate every source on the same rig."""
    rows = sorted(payload['actions'], key=lambda row: row['role'] != 'idle')
    def assemble(candidate, objects, report):
        root = candidate.GetFirstObject()
        if root is None:
            raise ValueError('The selected setups contain no character hierarchy.')
        if root.GetNext() is not None:
            # The regular exporter can group independent skeletons under a
            # top-level Joints, leaving their skinned meshes in sibling trees.
            branches = list(_children(root))
            root = c4d.BaseObject(c4d.Onull)
            root.SetName(payload.get('heroName') or payload['asset'].get('name') or 'Character')
            candidate.InsertObject(root)
            for branch in branches:
                branch.Remove()
                branch.InsertUnderLast(root)
        target = _one_target(c4d, root)
        clip = _layer_clip(c4d, target)
        first_source = clip[c4d.ID_MT_CLIP_SOURCE]
        sources = {rows[0]['id']: first_source}
        for row in rows[1:]:
            def append(scratch, animated_objects, action_report):
                source, = _source_list(scratch)
                fps = action_report['fps']
                last = row['animation']['lastFrame']
                frames = sorted({0, last // 2, last})
                ids = list(animated_objects)
                expected = _pose_snapshots(c4d, scratch, frames, fps, [animated_objects[i] for i in ids])
                source.Remove()
                source.InsertUnderLast(candidate.GetNLARoot())
                clip[c4d.ID_MT_CLIP_SOURCE] = source
                clip[c4d.ID_MT_CLIP_END] = source[SOURCE_END]
                actual = _pose_snapshots(c4d, candidate, frames, fps, [objects[i] for i in ids])
                for frame, wanted, got in zip(frames, expected, actual):
                    for ident, before, after in zip(ids, wanted, got):
                        _check_poses([[before]], [[after]],
                                     f'{row["name"]}: {objects[ident].GetName()} differs at frame {frame}. '
                                     f'Maximum matrix difference {max(abs(a-b) for a, b in zip(before,after)):.6g}.')
                sources[row['id']] = source
                for warning in action_report['warnings']:
                    if warning not in report['warnings']:
                        report['warnings'].append(warning)
            _build_detached(_action_payload(payload, row, False, rig_scales), destination, append)
        clip[c4d.ID_MT_CLIP_SOURCE] = first_source
        clip[c4d.ID_MT_CLIP_END] = first_source[SOURCE_END]
        candidate.SetTime(c4d.BaseTime(0))
        candidate.ExecutePasses(None, True, True, True, c4d.BUILDFLAGS_NONE)
        if include_mesh:
            _persist_textures(candidate, destination.parent)
        return publish(candidate, root, target, objects, sources, report)
    return _build_detached(_action_payload(payload, rows[0], include_mesh, rig_scales), destination, assemble)


def _place_in_project_units(c4d, doc, root):
    from whitehound_camera.units import document_units
    factor = 1.0 / document_units(doc)['cmPerWorldUnit']
    if factor != 1.0:
        # The staged root is an entity null above the skinned rig. Convert its
        # placement once, leaving native source keys and skin bind states intact.
        matrix = root.GetMl()
        root.SetMl(c4d.Matrix(off=matrix.off * factor, v1=matrix.v1 * factor,
                              v2=matrix.v2 * factor, v3=matrix.v3 * factor))


def _create(c4d, doc, payload, destination):
    register = _get_registration()
    kind = payload['kind']
    rows = payload['actions']
    if kind not in ('pov', 'actor') or not payload['rigId'] or not payload['rigFingerprint']:
        raise ValueError('A rig type and a stable rig identity are required.')
    if not rows or len(set(row['id'] for row in rows)) != len(rows):
        raise ValueError('Select one or more distinct named animation setups.')
    if any(not row['id'] or not row['name'] or not row['fingerprint'] for row in rows):
        raise ValueError('Each setup needs an identity, nickname and animation fingerprint.')
    rig_id = payload['rigId']
    motion_id = payload.get('rootMotionId')
    camera = None
    if kind == 'pov':
        camera = dict(_cameras(c4d, doc)).get(payload.get('cameraId'))
        if camera is None:
            raise ValueError('Select a perspective camera from the reviewed Cinema 4D project.')
    existing, existing_target, linked, misplaced, repair = _library_state(c4d, doc, payload)
    if existing is not None:
        rig_scales = json.loads(_marker(existing).GetString(5))
    else:
        rig_scales = _rig_scales(payload, next((row for row in rows if row['role'] == 'idle'), rows[0]))
    if existing_target is not None and motion_id:
        hits = [node for node in [existing, *(obj for _, obj in _walk(existing.GetDown()))]
                if (mark := _marker(node)) is not None and mark.GetString(3) == motion_id]
        if len(hits) != 1:
            raise ValueError('The reviewed root-motion object is no longer in the imported rig.')
        existing_motion = hits[0]
    else:
        existing_motion = existing_target

    def insert(candidate=None, root=None, target=None, objects=None, staged=None, report=None):
        # Recheck after the detached build. A cancelled mailbox job must never
        # alter an editor project after Avolition reported a pickup timeout.
        if (destination.parent / 'cancelled').exists():
            raise ValueError('The Stager import was cancelled.')
        if c4d.documents.GetActiveDocument() != doc:
            raise ValueError('Review the current Cinema 4D project again.')
        sources = dict(linked)
        first_import = existing is None
        if first_import:
            _place_in_project_units(c4d, doc, root)
            if motion_id and motion_id not in objects:
                raise ValueError('The reviewed root-motion object was removed by rig optimization.')
            motion = objects[motion_id] if motion_id else target
            # Actor choices remain addressable even after their display names change.
            node_ids = {node['id'] for node in payload['asset']['exportHierarchy']
                        if node['type'] != 'mesh'}
            for ident, node in objects.items():
                if ident in node_ids:
                    _mark(c4d, node, rig_id, 'member', ident)
            root_id = next((ident for ident, node in objects.items() if node == root), '')
            _mark(c4d, root, rig_id, 'root', root_id, payload['rigFingerprint'])
            _marker(root).SetString(5, json.dumps(rig_scales, separators=(',', ':')))
            first_source = _layer_clip(c4d, target)[c4d.ID_MT_CLIP_SOURCE]
            base_id = next(ident for ident, node in staged.items() if node == first_source)
        else:
            root, target, motion = existing, existing_target, existing_motion
        c4d.StopAllThreads()
        if not doc.StartUndo():
            raise RuntimeError('Cinema 4D could not start the rig insertion Undo.')
        recorded = False

        def record_new(node):
            nonlocal recorded
            if not doc.AddUndo(c4d.UNDOTYPE_NEWOBJ, node):
                node.Remove()
                raise RuntimeError('Cinema 4D could not record an insertion Undo.')
            recorded = True

        try:
            if existing is not None:
                # This also anchors rollback if registration of a reused rig
                # fails before a new source or Stager layer was inserted.
                if not doc.AddUndo(c4d.UNDOTYPE_CHANGE_SMALL, existing):
                    raise RuntimeError('Cinema 4D could not record a rig update Undo.')
                recorded = True
            for source in misplaced:
                if not doc.AddUndo(c4d.UNDOTYPE_HIERARCHY_PSR, source):
                    raise RuntimeError('Cinema 4D could not record the Motion Source recovery Undo.')
                source.Remove()
                source.InsertUnderLast(doc.GetNLARoot())
            for row in rows:
                ident = row['id']
                if ident in sources:
                    continue
                source = staged[ident]
                _mark(c4d, source, rig_id, 'source', ident, row['fingerprint'])
                _marker(source).SetString(5, payload['rigFingerprint'])
                source.Remove()
                source.InsertUnderLast(doc.GetNLARoot())
                record_new(source)
                sources[ident] = source
            if first_import:
                for material in list(_children(candidate.GetFirstMaterial())):
                    material.Remove()
                    doc.InsertMaterial(material)
                    record_new(material)
                root.Remove()
                doc.InsertObject(root, parent=camera)
                record_new(root)
                clip = _layer_clip(c4d, target)
                clip[c4d.ID_MT_CLIP_SOURCE] = sources[base_id]
                _mark(c4d, clip, rig_id, 'baseClip', base_id)
                if clip[c4d.ID_MT_CLIP_SOURCE] not in sources.values():
                    raise RuntimeError('The imported Motion Clip lost its source link.')
                for _, obj in _walk(root.GetDown()):
                    for tag in obj.GetTags():
                        if hasattr(tag, 'GetJointCount') and any(
                                tag.GetJoint(i, doc) is None for i in range(tag.GetJointCount())):
                            raise RuntimeError('A skin lost a joint while inserting the rig.')
                        if tag.CheckType(c4d.Ttexture) and tag[c4d.TEXTURETAG_MATERIAL] is None:
                            raise RuntimeError('A mesh lost its Octane material while inserting the rig.')
            elif repair:
                # Never repurpose artist-authored clips. New imports mark the
                # original Layer 0 clip with its stable setup ID.
                for layer in _children(_motion_head(c4d, target.GetTag(c4d.Tmotionsystem)).GetDown()):
                    for clip in _children(_motion_head(c4d, layer).GetDown()):
                        mark = _marker(clip)
                        if (mark is not None and mark.GetString(1) == rig_id and mark.GetString(2) == 'baseClip'
                                and mark.GetString(3) in sources and clip[c4d.ID_MT_CLIP_SOURCE] is None):
                            if not doc.AddUndo(c4d.UNDOTYPE_CHANGE_SMALL, clip):
                                raise RuntimeError('Cinema 4D could not record the Motion Clip recovery Undo.')
                            clip[c4d.ID_MT_CLIP_SOURCE] = sources[mark.GetString(3)]
            assignments = []
            for row in rows:
                item = dict(id=row['id'], name=row['name'], role=row['role'], source=sources[row['id']])
                if row.get('binding') is not None:
                    item['key'] = row['binding']
                if row.get('edge'):
                    item['edge'] = row['edge']
                assignments.append(item)
            options = dict(repair=True, recovered_sources=linked) if repair else {}
            registered = register(doc, rig_id=rig_id, kind=kind, root=root, target=target,
                                  root_motion=motion, camera=camera, assignments=assignments, **options)
        except Exception:
            # Undo within this still-open group, never against the user's prior
            # history, even when validation fails before the first insert.
            if recorded and not doc.DoUndo(True):
                raise RuntimeError('Cinema 4D could not roll back the failed rig insertion.')
            raise
        finally:
            doc.EndUndo()
            c4d.EventAdd()
        warnings = list(report['warnings']) if report else []
        if repair:
            warnings.append('Recovered the saved Stager rig registration in this project.')
            if registered.get('missingSources'):
                warnings.append(f"{registered['missingSources']} previously registered Motion Source(s) are "
                                'still missing. Select their saved animation setups to restore them.')
        if misplaced:
            warnings.append('Restored Motion Sources returned to the Object Manager by Cinema 4D Redo: '
                            + ', '.join(node.GetName() for node in misplaced) + '.')
        return dict(documentName=doc.GetDocumentName(), rigName=root.GetName(),
                    targetName=target.GetName(), sourceCount=len(sources),
                    addedSources=len(sources)-len(linked), reusedRig=not first_import,
                    repairedRegistration=repair, warnings=warnings)

    new = [row for row in rows if row['id'] not in linked]
    if existing is None:
        return _stage(c4d, payload, destination, insert, rig_scales, True)
    if not new:
        return insert()
    return _stage(c4d, dict(payload, actions=new), destination, insert, rig_scales, False)


def dispatch(payload, destination):
    import c4d
    operation = payload.get('operation')
    if operation is None:
        return build_scene(payload, destination)
    doc = c4d.documents.GetActiveDocument()
    if operation == 'stagerDestination':
        _get_registration()
        return _destination(c4d, doc, payload.get('rigIds'))
    if operation != 'createStagerRig':
        raise ValueError('Unsupported Cinema 4D bridge operation.')
    if doc is None or not _same_review(doc) or not _review_token or payload.get('documentId') != _review_token:
        raise ValueError('The active Cinema 4D project changed. Review the destination again.')
    return _create(c4d, doc, payload, Path(destination).resolve())
