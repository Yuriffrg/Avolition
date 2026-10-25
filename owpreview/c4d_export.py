"""Prepare native Cinema 4D scene data outside Cinema's Python runtime."""
import copy
import math
import numpy as np
from .export_pose import PoseEvaluator, decompose


def source_identity(source_path):
    """Read source identity independently of display names and host path syntax."""
    parts = [part for part in str(source_path or '').replace('\\', '/').split('/') if part]
    gameplay_1p = bool(parts and parts[-1].lower() == 'gameplay1p.owentity')
    for index in range(len(parts) - 3, -1, -1):
        if parts[index].lower() == 'heroes' and parts[index + 1] not in ('.', '..'):
            return gameplay_1p, parts[index + 1]
    return gameplay_1p, ''


def select_model_asset(asset, model_id):
    """Project an entity to one model and its complete native skeleton.

    This is an object export, not a subtree export: attached models and the
    parent's rig are excluded. An attached object's exported root receives its
    existing native hierarchy's *rest* world matrix, so detaching it does not
    move the mesh or change any joint's local bind/animation transforms. Parent
    animation is deliberately not baked into this independent object's clip.

    Call before texture baking to avoid generating maps for unrelated objects.
    The projection owns its selected data and never changes the loaded asset.
    """
    matches = [model for model in asset.get('models', []) if model.get('id') == model_id]
    if len(matches) != 1:
        raise ValueError('Choose a valid object from the loaded entity for C4D export.')
    original = matches[0]
    graph = asset.get('exportHierarchy', [])
    if not graph:
        raise ValueError('Entity hierarchy metadata is missing. Reload the entity before C4D export.')
    by_id = {node['id']: node for node in graph}
    if len(by_id) != len(graph):
        raise ValueError('The export hierarchy contains duplicate node identifiers.')
    selected_nodes = [node for node in graph if node.get('modelId') == model_id]
    if not selected_nodes:
        raise ValueError('The selected object is missing from the export hierarchy. Reload the entity.')
    selected_ids = {node['id'] for node in selected_nodes}
    world_matrices = {}

    def rest_world(node_id):
        pending, seen = [], set()
        current = node_id
        while current and current not in world_matrices:
            if current not in by_id or current in seen:
                raise ValueError('The export hierarchy contains a missing parent or a cycle.')
            pending.append(current)
            seen.add(current)
            current = by_id[current].get('parentId')
        world = world_matrices[current] if current else np.eye(4)
        for ident in reversed(pending):
            values = by_id[ident].get('matrix', np.eye(4).reshape(-1).tolist())
            matrix = np.asarray(values, dtype=np.float64)
            if matrix.size != 16 or not np.isfinite(matrix).all():
                raise ValueError('The export hierarchy contains an invalid rest transform.')
            matrix = matrix.reshape(4, 4).T
            if not np.allclose(matrix[3], [0, 0, 0, 1], atol=1e-7, rtol=0):
                raise ValueError('The export hierarchy contains an invalid affine rest transform.')
            world = world @ matrix
            world_matrices[ident] = world
        return world_matrices[node_id]

    isolated_graph = copy.deepcopy(selected_nodes)
    for node in isolated_graph:
        if node.get('parentId') not in selected_ids:
            matrix = rest_world(node['id'])
            try:
                decompose(matrix)
            except ValueError as error:
                raise ValueError('The selected object has a rest attachment transform that Cinema 4D '
                                 'cannot preserve without its parent hierarchy.') from error
            node['parentId'] = None
            node['matrix'] = matrix.T.reshape(-1).tolist()

    model = copy.deepcopy(original)
    model.update(parentId=None, attachmentBone=None, attachmentMatrix=np.eye(4).reshape(-1).tolist())
    material_keys = {str(mesh.get('materialKey', '0')) for mesh in model.get('meshes', [])}
    model['materials'] = {key: value for key, value in model.get('materials', {}).items()
                          if str(key) in material_keys}
    texture_ids = {mapping['textureId'] for material in model['materials'].values()
                   for mapping in material.get('maps', {}).values() if mapping.get('textureId')}
    textures = {key: copy.deepcopy(value) for key, value in asset.get('textures', {}).items()
                if key in texture_ids}
    return dict(asset, models=[model], exportHierarchy=isolated_graph, textures=textures,
                warnings=list(asset.get('warnings', [])))


def _automatic_vertex_normals(mesh, missing):
    """Blender's zero custom normals use the incident smooth surface normal.

    Accumulate corner-angle-weighted face directions by vertex index, never by
    position: coincident split vertices must keep their own shading boundaries.
    Only visit faces touching missing normals; leave geometry and valid normals
    untouched. Thin nondegenerate triangles are valid, including map trim.
    """
    label = mesh.get('name', 'Mesh')
    positions = np.asarray(mesh['positions'], dtype=np.float64).reshape(-1, 3)
    indices = np.asarray(mesh.get('indices', []))
    if (not np.isfinite(positions).all() or indices.ndim != 1 or indices.size % 3
            or (indices.size and (indices.dtype.kind not in 'iu'
                or np.any(indices < 0) or np.any(indices >= len(positions))))):
        raise ValueError(f'{label}: invalid geometry for automatic normals.')
    faces = indices.astype(np.int64).reshape(-1, 3)
    faces = faces[np.any(missing[faces], axis=1)]
    points = positions[faces]
    cross = np.cross(points[:, 1] - points[:, 0], points[:, 2] - points[:, 0])
    areas = np.linalg.norm(cross, axis=1)
    usable = np.isfinite(areas) & (areas > 0)
    faces, points, cross, areas = faces[usable], points[usable], cross[usable], areas[usable]
    directions = cross / areas[:, None]
    accumulated = np.zeros_like(positions)
    strongest = np.zeros_like(positions)
    largest = np.zeros(len(positions))
    for corner in range(3):
        vertices = faces[:, corner]
        edge_a = points[:, (corner + 1) % 3] - points[:, corner]
        edge_b = points[:, (corner + 2) % 3] - points[:, corner]
        angles = np.arctan2(areas, np.einsum('ij,ij->i', edge_a, edge_b))
        np.add.at(accumulated, vertices, directions * angles[:, None])
        # Opposite-winding faces can cancel. Prefer a real incident direction
        # from the largest face in that case, with stable ties.
        for face in np.flatnonzero(missing[vertices]):
            vertex = vertices[face]
            if areas[face] > largest[vertex]:
                largest[vertex] = areas[face]
                strongest[vertex] = directions[face]
    lengths = np.linalg.norm(accumulated, axis=1)
    smooth = lengths > 1e-12
    accumulated[smooth] /= lengths[smooth, None]
    accumulated[~smooth] = strongest[~smooth]
    unused = missing & (largest == 0)
    # These vertices have no nondegenerate incident polygon, so the placeholder
    # cannot affect a rendered surface. C4D still requires a unit direction.
    accumulated[unused] = [0, 1, 0]
    return accumulated[missing], int(np.count_nonzero(unused))


def _normalized_normal_asset(asset):
    """Match Blender custom-normal normalization without changing shared assets.

    OW's packed normals are not exactly unit length. Blender normalizes them
    when installing custom split normals; Cinema's NormalTag expects unit
    vectors. Normalize before the bridge so existing bridge 0.7.2 installations
    receive corrected data too. Zero custom normals request automatic shading
    in Blender. Resolve those from geometry; keep authored directions and split
    vertices. Malformed/nonfinite data is still rejected.
    """
    models = []
    warnings = list(asset.get('warnings', []))
    for model in asset.get('models', []):
        meshes = []
        for mesh in model.get('meshes', []):
            normals = mesh.get('normals')
            if not normals:
                meshes.append(mesh)
                continue
            label = mesh.get('name', 'Mesh')
            if len(normals) != len(mesh['positions']) or len(normals) % 3:
                raise ValueError(f'{label}: invalid normal array length.')
            vectors = np.array(normals, dtype=np.float64, copy=True).reshape(-1, 3)
            lengths = np.linalg.norm(vectors, axis=1)
            if not np.isfinite(vectors).all() or not np.isfinite(lengths).all():
                raise ValueError(f'{label}: normals must contain finite directions for C4D export.')
            missing = lengths < 1e-12
            vectors[~missing] /= lengths[~missing, None]
            if np.any(missing):
                vectors[missing], unused = _automatic_vertex_normals(mesh, missing)
                count = int(np.count_nonzero(missing))
                if count > unused:
                    warnings.append(f'{label}: generated {count - unused} zero-length vertex normal(s) '
                                    'from mesh geometry for C4D export.')
                if unused:
                    warnings.append(f'{label}: used placeholder normals for {unused} vertex/vertices '
                                    'without a nondegenerate polygon.')
            meshes.append(dict(mesh, normals=vectors.reshape(-1).tolist()))
        models.append(dict(model, meshes=meshes))
    return dict(asset, models=models, warnings=list(dict.fromkeys(warnings)))


def make_payload(asset, clip, materials, *, include_mesh=True, root_motion=True,
                 clip_name=None, rig_id='auto', scope_model_id=None, motion_clip_name=None):
    if scope_model_id is not None:
        if rig_id not in ('auto', scope_model_id):
            raise ValueError('The animation target must be the object selected for C4D export.')
        asset = select_model_asset(asset, scope_model_id)
        rig_id = scope_model_id
        material_keys = {str(mesh.get('materialKey', '0')) for mesh in asset['models'][0].get('meshes', [])}
        materials = {scope_model_id: {key: value for key, value in materials.get(scope_model_id, {}).items()
                                      if str(key) in material_keys}}
    models = asset.get('models', [])
    names = {bone['name'] for bone in clip.get('bones', [])}
    candidates = [model for model in models if model.get('bones')]
    selected = (max(candidates, key=lambda model: (
        sum(bone['name'] in names for bone in model['bones']), len(model['bones'])), default=None)
        if rig_id == 'auto' else next((model for model in candidates if model['id'] == rig_id), None))
    if selected is None:
        raise ValueError('Choose a model with a skeleton for C4D export.')
    matched = sum(bone['name'] in names for bone in selected['bones'])
    if names and not matched:
        raise ValueError('The selected animation has no matching bones on this rig.')
    fps, last = float(clip['fps']), int(clip.get('lastFrame', clip.get('durationFrames', 1)-1))
    if not math.isfinite(fps) or not 0 < fps <= 1000 or last < 0:
        raise ValueError('Invalid animation timing.')
    if (last+1)*len(selected['bones']) > 250_000:
        raise ValueError('Native C4D export exceeds 250,000 sampled bone frames. Choose a shorter clip.')
    if not asset.get('exportHierarchy'):
        raise ValueError('Entity hierarchy metadata is missing. Reload the entity before C4D export.')
    native_asset = _normalized_normal_asset(asset) if include_mesh else asset
    evaluator = PoseEvaluator(selected['bones'], clip.get('bones', []))
    tracks = [{'modelId': selected['id'], 'boneIndex': i, 'frames': []} for i in range(len(selected['bones']))]
    for frame in range(last+1):
        pose = evaluator.evaluate(frame, lock_root=not root_motion)
        for i, track in enumerate(tracks):
            parent = evaluator.parents[i]
            local = pose['world'][i] if parent < 0 else np.linalg.inv(pose['world'][parent]) @ pose['world'][i]
            try:
                decompose(local)
            except ValueError as error:
                raise ValueError(f'C4D cannot represent this clip\'s nonuniform scale without extra hierarchy nodes '
                                 f'({selected["bones"][i]["name"]}, frame {frame}). '
                                 'This clip cannot be represented as C4D PSR animation without changing the original hierarchy.') from error
            track['frames'].append(local.T.reshape(-1).tolist())
    gameplay_1p, hero_name = source_identity(asset.get('path'))
    return {'version': 1, 'asset': native_asset, 'materials': materials,
            'warnings': list(native_asset.get('warnings', [])),
            'includeMesh': bool(include_mesh), 'name': clip_name or clip.get('name', 'Animation'),
            'motionClip': bool(tracks),
            'motionClipName': motion_clip_name or clip_name or clip.get('name', 'Animation'),
            'gameplay1P': gameplay_1p, 'heroName': hero_name,
            'animation': {'fps': fps, 'lastFrame': last, 'tracks': tracks, 'matchedBones': matched}}


def make_set_payload(asset, assignments, materials, *, include_mesh=True,
                     root_motion=True, name='Animation set', motion_clip_name=None):
    """Bake explicit per-model clips into one existing native entity hierarchy.

    ``assignments`` contains ``{modelId, clip}`` records, where ``clip`` is a
    parsed ``load_clip`` result. Bone names are matched only within that model;
    shared names on another skeleton never acquire its animation implicitly.
    All source models remain in the scene. Unassigned models retain their rest
    pose and inherit movement through the existing parent-joint attachment.

    Each clip begins at zero seconds at its own frame rate. Sample at the
    highest assigned frame rate through the longest duration, holding shorter
    clips at their last frame. The final common frame rounds up when necessary,
    adding at most one held frame instead of stretching any clip. This remains
    bridge protocol 1 and requires no Cinema plugin replacement.
    """
    from .c4d_scene import _ordered_graph

    if not isinstance(assignments, (list, tuple)) or not assignments:
        raise ValueError('Assign an animation to at least one object before exporting the set.')
    models = asset.get('models', [])
    by_id = {model['id']: model for model in models}
    if len(by_id) != len(models):
        raise ValueError('The loaded entity contains duplicate object identifiers.')
    if not asset.get('exportHierarchy'):
        raise ValueError('Entity hierarchy metadata is missing. Reload the entity before C4D export.')
    graph = _ordered_graph(asset['exportHierarchy'])
    graph_bones = {(node['modelId'], node.get('boneIndex')) for node in graph if node['type'] == 'bone'}
    prepared, seen = [], set()
    for assignment in assignments:
        if not isinstance(assignment, dict):
            raise ValueError('Invalid animation assignment.')
        mid, clip = assignment.get('modelId'), assignment.get('clip')
        if not isinstance(mid, str) or mid not in by_id or not by_id[mid].get('bones'):
            raise ValueError('Choose an object with a skeleton for every animation assignment.')
        if mid in seen:
            raise ValueError('Each object can have only one assigned animation in a set.')
        seen.add(mid)
        if not isinstance(clip, dict):
            raise ValueError('An animation assignment is missing its clip data.')
        model = by_id[mid]
        if any((mid, index) not in graph_bones for index in range(len(model['bones']))):
            raise ValueError('An assigned object is missing joints from the export hierarchy. Reload the entity.')
        names = {bone['name'] for bone in clip.get('bones', [])}
        model_names = {bone['name'] for bone in model['bones']}
        matched = len(names & model_names)
        if not matched:
            raise ValueError(f'The animation assigned to {model.get("name", mid)} has no matching bones.')
        try:
            fps = float(clip['fps'])
            last_value = clip.get('lastFrame', clip.get('durationFrames', 1) - 1)
            last = int(last_value)
        except (KeyError, TypeError, ValueError, OverflowError) as error:
            raise ValueError('Invalid animation timing in the set.') from error
        if not math.isfinite(fps) or not 0 < fps <= 1000 or last < 0 or last != last_value:
            raise ValueError('Invalid animation timing in the set.')
        prepared.append(dict(model=model, clip=clip, fps=fps, last=last,
                             matched=matched, missing=sorted(names - model_names)))

    fps = max(item['fps'] for item in prepared)
    duration = max(item['last'] / item['fps'] for item in prepared)
    # Suppress numerical noise for a source endpoint already on this grid.
    last = max(0, math.ceil(duration * fps - 1e-9))
    if (last + 1) * sum(len(item['model']['bones']) for item in prepared) > 250_000:
        raise ValueError('Native C4D export exceeds 250,000 sampled bone frames. Choose shorter clips.')
    tracks, summaries = [], []
    for item in prepared:
        model, clip = item['model'], item['clip']
        evaluator = PoseEvaluator(model['bones'], clip.get('bones', []))
        model_tracks = [dict(modelId=model['id'], boneIndex=index, frames=[])
                        for index in range(len(model['bones']))]
        for frame in range(last + 1):
            source_frame = min(item['last'], frame / fps * item['fps'])
            pose = evaluator.evaluate(source_frame, lock_root=not root_motion)
            for index, track in enumerate(model_tracks):
                parent = evaluator.parents[index]
                local = (pose['world'][index] if parent < 0 else
                         np.linalg.inv(pose['world'][parent]) @ pose['world'][index])
                try:
                    decompose(local)
                except ValueError as error:
                    raise ValueError('C4D cannot represent this clip\'s nonuniform scale without extra hierarchy nodes '
                                     f'({model.get("name", model["id"])}, {model["bones"][index]["name"]}, '
                                     f'frame {frame}). The original hierarchy has been retained.') from error
                track['frames'].append(local.T.reshape(-1).tolist())
        tracks.extend(model_tracks)
        summaries.append(dict(modelId=model['id'], clipName=clip.get('name', 'Animation'),
                              fps=item['fps'], lastFrame=item['last'],
                              durationSeconds=item['last'] / item['fps'],
                              matchedBones=item['matched'],
                              unmatchedBoneNames=item['missing']))
    native_asset = _normalized_normal_asset(asset) if include_mesh else asset
    gameplay_1p, hero_name = source_identity(asset.get('path'))
    return {'version': 1, 'asset': native_asset, 'materials': materials,
            'warnings': list(native_asset.get('warnings', [])),
            'includeMesh': bool(include_mesh), 'name': name,
            'motionClip': bool(tracks),
            'motionClipName': motion_clip_name or name,
            'gameplay1P': gameplay_1p, 'heroName': hero_name,
            'animation': {'fps': fps, 'lastFrame': last, 'durationSeconds': duration,
                          'tracks': tracks, 'assignments': summaries,
                          'matchedBones': sum(item['matched'] for item in prepared)}}
