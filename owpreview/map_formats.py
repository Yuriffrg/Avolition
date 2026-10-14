"""Bounded OWLib OWMAP 2.0/2.1 records and reusable preview geometry.

Coordinates remain raw Y-up metres (XYZW quaternions), like ``formats``.
See MAP_FORMAT_REFERENCE.md for the writer/reader contract and limitations.
"""
from __future__ import annotations

from dataclasses import dataclass
import math
import os
from pathlib import Path, PureWindowsPath
import struct

from .formats import FormatError, IDENTITY, _Reader, _matrix, _multiply, _path, load_asset
from .materials import MaterialResolver, is_collision_material


@dataclass(frozen=True)
class MapLimits:
    file_bytes: int = 128 * 1024 * 1024
    groups: int = 100_000
    instances: int = 100_000
    lights: int = 100_000
    sounds: int = 100_000
    sound_files: int = 100_000
    models: int = 100_000
    unique_vertices: int = 10_000_000
    mesh_instances: int = 200_000
    graph_nodes: int = 500_000


DEFAULT_LIMITS = MapLimits()
_LIGHT_20_EXTRA = struct.calcsize('<IIBBBBII3f4f3f4f3f4fffIHHII')


def _limits(value):
    result = value or DEFAULT_LIMITS
    if not isinstance(result, MapLimits) or any(
            not isinstance(v, int) or isinstance(v, bool) or v < 0 for v in vars(result).values()):
        raise ValueError('Map limits must be non-negative integers in MapLimits.')
    return result


def _string(reader):
    value = reader.string()
    if len(value) > 4096 or '\x00' in value:
        reader.fail('invalid or oversized map name/reference')
    return value


def _header(reader, limits):
    major, minor = reader.unpack('HH')
    if (major, minor) not in ((2, 0), (2, 1)):
        reader.fail(f'unsupported map version {major}.{minor}; expected 2.0 or 2.1')
    name = _string(reader)
    objects, details, lights = reader.unpack('III')
    reader.count(objects, limits.groups, 'map object groups')
    reader.count(details, limits.instances, 'map details')
    reader.count(lights, limits.lights, 'map lights')
    return dict(name=name or reader.path.stem, path=str(reader.path), version=f'{major}.{minor}',
                objectCount=objects, detailCount=details, lightCount=lights)


def read_map_header(path, *, limits=None):
    """Read only a bounded header for the folder browser; not a validity verdict."""
    path, limits = _path(path, '.owmap'), _limits(limits)
    size = path.stat().st_size
    if size > limits.file_bytes:
        raise FormatError(f'{path.name}: map exceeds the {limits.file_bytes:,}-byte limit.')
    with path.open('rb') as handle:
        reader = _Reader(path, handle.read(1024 * 1024 + 24))
    return dict(_header(reader, limits), size=size)


def _record(reader):
    position, scale, rotation = reader.values('f', 3), reader.values('f', 3), reader.values('f', 4)
    length = math.sqrt(sum(v * v for v in rotation))
    if length < 1e-12:
        reader.fail('map placement has a zero quaternion')
    rotation = [v / length for v in rotation]
    matrix = _matrix(position, rotation)
    for column in range(3):
        for row in range(3):
            matrix[column * 4 + row] *= scale[column]
    return dict(position=position, scale=scale, rotation=rotation, matrix=matrix)


def read_map(path, *, limits=None):
    """Parse every record, including unrendered lights/sounds, with strict bounds."""
    path, limits = _path(path, '.owmap'), _limits(limits)
    if path.stat().st_size > limits.file_bytes:
        raise FormatError(f'{path.name}: map exceeds the {limits.file_bytes:,}-byte limit.')
    reader = _Reader.file(path)
    result = _header(reader, limits)
    instances, lights, sounds = [], [], []
    groups = 0
    for object_index in range(result['objectCount']):
        source = _string(reader)
        count = reader.unpack('I')[0]
        groups += count
        reader.count(groups, limits.groups, 'map material groups')
        for group_index in range(count):
            look, count = _string(reader), reader.unpack('I')[0]
            reader.count(len(instances) + count + result['detailCount'], limits.instances, 'map instances')
            if count > reader.remaining // 40:
                reader.fail('truncated map placement records')
            for record_index in range(count):
                instances.append(dict(source=source, material=look, category='Objects',
                                      objectIndex=object_index, groupIndex=group_index,
                                      recordIndex=record_index, **_record(reader)))
    for index in range(result['detailCount']):
        source, look = _string(reader), _string(reader)
        instances.append(dict(source=source, material=look, category='Details',
                              recordIndex=index, **_record(reader)))
    for _ in range(result['lightCount']):
        position, rotation = reader.values('f', 3), reader.values('f', 4)
        kind, fov, color = reader.unpack('I')[0], reader.values('f', 1)[0], reader.values('f', 3)
        light = dict(position=position, rotation=rotation, type=kind, fov=fov, color=color)
        if result['version'] == '2.0':
            reader.take(_LIGHT_20_EXTRA)
        else:
            light.update(intensity=reader.values('f', 1)[0], projectionTextures=list(reader.unpack('QQ')))
        lights.append(light)
    count = reader.unpack('I')[0]
    reader.count(count, limits.sounds, 'map sounds')
    file_total = 0
    for _ in range(count):
        position, count = reader.values('f', 3), reader.unpack('i')[0]
        reader.count(count, limits.sound_files, 'sound file count')
        file_total += count
        reader.count(file_total, limits.sound_files, 'map sound files')
        sounds.append(dict(position=position, files=[_string(reader) for _ in range(count)]))
    if reader.remaining:
        reader.fail(f'unexpected {reader.remaining:,} trailing map bytes')
    return dict(result, instances=instances, lights=lights, sounds=sounds, soundCount=len(sounds))


def scan_maps(root, *, max_files=500_000):
    """Find actual map headers without decoding geometry or following directory links."""
    root = Path(root).expanduser().resolve()
    if not root.is_dir():
        raise ValueError('Choose an existing map source folder.')
    maps, errors, count = [], [], 0
    def on_error(error):
        errors.append(str(error))
    for directory, subdirs, files in os.walk(root, followlinks=False, onerror=on_error):
        subdirs[:] = [name for name in subdirs if not (Path(directory) / name).is_symlink()]
        for filename in files:
            count += 1
            if count > max_files:
                raise ValueError(f'Map scan exceeds {max_files:,} files. Choose a smaller source folder.')
            path = Path(directory) / filename
            if path.suffix.lower() != '.owmap':
                continue
            try:
                if not path.resolve().is_relative_to(root):
                    raise ValueError(f'{path.name}: map reference leaves the source folder.')
                maps.append(dict(read_map_header(path), relativePath=str(path.relative_to(root))))
            except (OSError, ValueError) as error:
                errors.append(str(error))
    maps.sort(key=lambda item: (item['name'].casefold(), item['path'].casefold()))
    return dict(root=str(root), maps=maps, errors=errors)


def _reference(owner, value, root, extensions):
    """OWLib emits map-relative paths; absolute/traversing map references are invalid."""
    if not value:
        return None
    parts = value.replace('\\', '/').split('/')
    win = PureWindowsPath(value)
    if (win.drive or win.root or value.startswith('/') or '..' in parts or
            any(':' in part for part in parts) or '\x00' in value):
        raise FormatError(f'{owner.name}: map reference must stay within its export folder: {value}')
    path = (owner.parent / Path(*parts)).resolve()
    if not path.is_relative_to(root) or not path.is_relative_to(owner.parent):
        raise FormatError(f'{owner.name}: map reference leaves its export folder: {value}')
    if path.suffix.lower() not in extensions:
        raise FormatError(f'{owner.name}: unsupported referenced file: {value}')
    return path


def expand_map_asset(asset):
    """Resolve compact geometry without copying numeric arrays or changing prototypes."""
    prototypes, models = asset.get('meshPrototypes', {}), []
    for source in asset.get('models', []):
        prototype = source.get('meshPrototype')
        geometry = source if prototype is None else prototypes.get(prototype)
        if not isinstance(geometry, dict) or not isinstance(geometry.get('meshes'), list):
            raise FormatError('Map object references a missing mesh prototype.')
        names = source.get('meshNames', {})
        meshes = [dict(mesh, name=names.get(str(index), mesh['name']))
                  for index, mesh in enumerate(geometry['meshes'])]
        model = dict(source, meshes=meshes)
        model.pop('meshPrototype', None)
        models.append(model)
    result = dict(asset, models=models)
    result.pop('meshPrototypes', None)
    return result


def load_map(path, root_path=None, *, compact=True, limits=None):
    """Load referenced models once, retaining each placement's complete local TRS.

    The normal entity/model loader still handles bones, submeshes and materials.
    Every placed instance has independent model IDs and attachment links. Compact
    prototypes contain geometry only; material bindings remain per instance.
    """
    path, limits = _path(path, '.owmap'), _limits(limits)
    root = Path(root_path).expanduser().resolve() if root_path else path.parent
    if not root.is_dir() or not path.is_relative_to(root):
        raise ValueError('The selected map must be inside its existing map source folder.')
    parsed = read_map(path, limits=limits)
    warnings, warning_set = [], set()
    def warn(message):
        if message not in warning_set:
            warning_set.add(message)
            if len(warnings) < 500:
                warnings.append(message)
    models, graph, prototypes, textures = [], [], {}, {}
    sources, variants, prototype_keys = {}, {}, {}
    resolver = MaterialResolver(path, root)
    vertices, mesh_instances, polygon_instances, loaded_instances = 0, 0, 0, 0
    collision_meshes, collision_polygons = 0, 0
    for index, instance in enumerate(parsed['instances']):
        source_path = _reference(path, instance['source'], root, {'.owmdl', '.owentity'})
        look_path = _reference(path, instance['material'], root, {'.owmat'})
        if source_path is None:
            warn(f'Placement {index + 1}: empty model reference; object was not loaded.')
            continue
        key = str(source_path)
        if key not in sources:
            try:
                sources[key] = load_asset(source_path, root)
                textures.update(sources[key].get('textures', {}))
            except (OSError, ValueError) as error:
                warn(f'{instance["source"]}: object could not be loaded: {error}')
                sources[key] = None
        source = sources[key]
        if source is None:
            continue
        variant_key = (key, str(look_path) if look_path else '')
        if variant_key not in variants:
            variant = []
            replaced_neutral = {f'{Path(model["path"]).name}: no material look reference; using neutral shading.'
                                for model in source['models'] if look_path}
            for warning in source.get('warnings', []):
                if warning not in replaced_neutral:
                    warn(warning)
            for source_model in source['models']:
                model = dict(source_model)
                # BLMaterial.bindEntityLook visits every child recursively.
                # Slots absent from that look retain the model's prior binding.
                if look_path and model.get('hasModel', True):
                    if look_path.is_file():
                        material_model = dict(model, materialPath=str(look_path))
                        model['materials'] = dict(model.get('materials', {}))
                        for slot, descriptor in resolver.for_model(material_model, partial=True).items():
                            if descriptor is None:
                                model['materials'].pop(slot, None)
                            else:
                                model['materials'][slot] = descriptor
                        model['materialLook'] = str(look_path)
                    else:
                        warn(f'{instance["material"]}: material look is missing; retaining available source materials.')
                kept = [(i, mesh) for i, mesh in enumerate(model.get('meshes', []))
                        if not is_collision_material(model.get('materials', {}).get(mesh['materialKey']))]
                model['_meshIndexMap'] = {old: new for new, (old, _) in enumerate(kept)}
                model['_collisionMeshCount'] = len(model.get('meshes', [])) - len(kept)
                model['_collisionPolygonCount'] = sum(len(mesh.get('indices', [])) // 3
                    for i, mesh in enumerate(model.get('meshes', [])) if i not in model['_meshIndexMap'])
                model['meshes'] = [dict(mesh, sourceMeshIndex=i) for i, mesh in kept]
                # Collision-only descriptors must not become native materials or
                # consume texture decoding time after their meshes are removed.
                used = {mesh['materialKey'] for mesh in model['meshes']}
                model['materials'] = {key: value for key, value in model.get('materials', {}).items() if key in used}
                variant.append(model)
            variants[variant_key] = variant
        variant = variants[variant_key]
        if len(models) + len(variant) > limits.models or len(graph) + len(source['exportHierarchy']) > limits.graph_nodes:
            raise FormatError('Map exceeds the model/hierarchy preview limit. Choose a smaller map variant.')
        prefix = f'map-{index}-'
        identifiers = {model['id']: prefix + model['id'] for model in variant}
        originals = {model['id']: model for model in variant}
        for original in variant:
            model = dict(original)
            collision_meshes += model.pop('_collisionMeshCount')
            collision_polygons += model.pop('_collisionPolygonCount')
            model.pop('_meshIndexMap')
            mesh_instances += len(model.get('meshes', []))
            if mesh_instances > limits.mesh_instances:
                raise FormatError('Map exceeds the placed-mesh preview limit. Choose a smaller map variant.')
            polygon_instances += sum(len(mesh.get('indices', [])) // 3 for mesh in model.get('meshes', []))
            # Models referenced directly and through entities share geometry.
            prototype_key = (model.get('path'), model.get('hasModel', True),
                             tuple(mesh['sourceMeshIndex'] for mesh in model.get('meshes', [])))
            if prototype_key not in prototype_keys:
                prototype_id = f'geometry-{len(prototypes)}'
                prototype_keys[prototype_key] = prototype_id
                vertices += sum(len(mesh.get('positions', [])) // 3 for mesh in model.get('meshes', []))
                if vertices > limits.unique_vertices:
                    raise FormatError('Map exceeds the unique-geometry preview limit. Choose a smaller map variant.')
                prototypes[prototype_id] = dict(meshes=model.get('meshes', []))
            model.update(id=identifiers[original['id']], parentId=identifiers.get(original.get('parentId')),
                         meshPrototype=prototype_keys[prototype_key], mapInstanceId=f'map-{index}',
                         mapCategory=instance['category'])
            model.pop('meshes', None)
            if not original.get('parentId'):
                model['attachmentMatrix'] = _multiply(instance['matrix'], original.get('attachmentMatrix', IDENTITY))
            models.append(model)
        for original_node in source['exportHierarchy']:
            original_model = originals[original_node['modelId']]
            if original_node.get('type') == 'mesh' and original_node['meshIndex'] not in original_model['_meshIndexMap']:
                continue
            node = dict(original_node, id=prefix + original_node['id'],
                        parentId=prefix + original_node['parentId'] if original_node.get('parentId') else None,
                        modelId=identifiers[original_node['modelId']])
            if node.get('type') == 'mesh':
                node['sourceMeshIndex'] = original_node['meshIndex']
                node['meshIndex'] = original_model['_meshIndexMap'][original_node['meshIndex']]
            own_top = original_node['id'] == original_model['id'] + (':entity' if original_model.get('isEntity') else ':model')
            if own_top:
                attachment = original_model.get('attachmentMatrix', IDENTITY)
                node['matrix'] = (_multiply(instance['matrix'], attachment)
                                  if not original_model.get('parentId') else list(attachment))
            graph.append(node)
        loaded_instances += 1
    textures.update(resolver.textures)
    used_textures = {mapping['textureId'] for model in models for material in model['materials'].values()
                     for mapping in material.get('maps', {}).values()}
    textures = {key: value for key, value in textures.items() if key in used_textures}
    for warning in resolver.warnings:
        warn(warning)
    if parsed['lightCount']:
        warn(f'{parsed["lightCount"]:,} map lights were read but are not included in preview or C4D export.')
    if parsed['soundCount']:
        warn(f'{parsed["soundCount"]:,} map sound emitters were read but are not included in preview or C4D export.')
    if collision_meshes:
        summary = f'{collision_meshes:,} engine hit-test meshes ({collision_polygons:,} polygons) were omitted, matching the Blender map importer.'
        warning_set.add(summary)
        # Keep this scene-level result visible even when individual shader
        # diagnostics have filled the bounded warning list.
        warnings.insert(0, summary)
        del warnings[500:]
    if len(warning_set) > len(warnings):
        warnings.append(f'{len(warning_set) - len(warnings):,} additional asset warnings were omitted.')
    if not mesh_instances:
        raise FormatError('No map meshes could be loaded. Keep the map beside its exported Models/Entities folders. ' +
                          (' '.join(warnings[:3]) if warnings else 'This map contains no model placements.'))
    asset = dict(name=parsed['name'], path=str(path), models=models, meshPrototypes=prototypes,
                 exportHierarchy=graph, textures=textures, textureRoot=str(root), warnings=warnings,
                 map=dict(version=parsed['version'], instanceCount=len(parsed['instances']),
                          loadedInstanceCount=loaded_instances, missingInstanceCount=len(parsed['instances']) - loaded_instances,
                          modelCount=len(models), prototypeCount=len(prototypes), meshCount=mesh_instances,
                          polygonCount=polygon_instances, uniqueVertexCount=vertices,
                          lightCount=parsed['lightCount'], soundCount=parsed['soundCount'],
                          omittedCollisionMeshCount=collision_meshes, omittedCollisionPolygonCount=collision_polygons,
                          unsupported=['lights', 'sounds', 'effects']))
    return asset if compact else expand_map_asset(asset)
