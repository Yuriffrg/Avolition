"""Validated display names applied to export copies, never source bindings."""
from __future__ import annotations

import copy
import re
import unicodedata

MAX_NAME_UNITS = 160
MAX_OVERRIDES = 100000
_KEY = re.compile(r'(?:scene|model:[A-Za-z0-9_.-]+|mesh:[A-Za-z0-9_.-]+:[0-9]+)\Z')


def validate_name(name):
    if not isinstance(name, str):
        raise ValueError('Enter an object name.')
    if any(unicodedata.category(char) in ('Cc', 'Cs') for char in name):
        raise ValueError('Object names cannot contain control characters.')
    name = name.strip()
    if not name:
        raise ValueError('Object names cannot be empty.')
    if len(name.encode('utf-16-le')) // 2 > MAX_NAME_UNITS:
        raise ValueError(f'Keep object names within {MAX_NAME_UNITS} characters.')
    return name


def object_name_keys(asset):
    """Stable names supported by both entity and map preview assets."""
    keys = {'scene'}
    for model in asset.get('models', []):
        ident = str(model['id'])
        keys.add('model:' + ident)
        keys.update(f'mesh:{ident}:{mesh.get("sourceMeshIndex", index)}' for index, mesh in enumerate(_meshes(asset, model)))
    return keys


def _meshes(asset, model):
    prototype = model.get('meshPrototype')
    if prototype is None:
        return model.get('meshes', [])
    source = asset.get('meshPrototypes', {}).get(prototype)
    if not isinstance(source, dict) or not isinstance(source.get('meshes'), list):
        raise ValueError('The map object references a missing mesh prototype.')
    return source['meshes']


def validate_names(names, asset=None):
    """Return a clean mapping; reject unknown/bone keys when an asset is supplied."""
    if names is None:
        return {}
    if not isinstance(names, dict) or len(names) > MAX_OVERRIDES:
        raise ValueError('Invalid saved object names.')
    allowed = object_name_keys(asset) if asset is not None else None
    cleaned = {}
    for key, name in names.items():
        if not isinstance(key, str) or len(key) > 512 or not _KEY.fullmatch(key):
            raise ValueError('Only scene, model and mesh display names can be changed.')
        if allowed is not None and key not in allowed:
            raise ValueError('The renamed object is not in this asset. Reload it and try again.')
        cleaned[key] = validate_name(name)
    return cleaned


def apply_object_names(asset, names):
    """Copy an asset and change only names consumed by scene exporters.

    Entity wrappers use entityName. A standalone model has no entity wrapper,
    so a scene override names its root model unless that model has its own name.
    Paths, GUIDs, IDs, joints, attachments, matrices and material keys are intact.
    Maps retain shared immutable geometry arrays; only named wrappers are copied.
    """
    names = validate_names(names, asset)
    is_map = 'map' in asset or 'meshPrototypes' in asset
    if is_map:
        result = dict(asset)
        result['models'] = []
        for model in asset.get('models', []):
            named_model = dict(model)
            if 'meshes' in model:
                named_model['meshes'] = [dict(mesh) for mesh in model['meshes']]
            if 'meshNames' in model:
                named_model['meshNames'] = dict(model['meshNames'])
            result['models'].append(named_model)
        result['exportHierarchy'] = [dict(node) for node in asset.get('exportHierarchy', [])]
    else:
        result = copy.deepcopy(asset)
    if 'scene' in names:
        result['name'] = names['scene']
        roots = [model for model in result.get('models', []) if not model.get('parentId')]
        if not is_map and len(roots) == 1:
            root = roots[0]
            if root.get('isEntity', True):
                root['entityName'] = names['scene']
            elif f'model:{root["id"]}' not in names:
                root['name'] = names['scene']
    for model in result.get('models', []):
        ident = str(model['id'])
        if f'model:{ident}' in names:
            model['name'] = names[f'model:{ident}']
        for index, mesh in enumerate(_meshes(result, model)):
            key = f'mesh:{ident}:{mesh.get("sourceMeshIndex", index)}'
            if key in names:
                if model.get('meshPrototype') is not None:
                    model.setdefault('meshNames', {})[str(index)] = names[key]
                else:
                    mesh['name'] = names[key]
    # Compact maps carry a prebuilt export graph so repeated source models keep
    # their exact attachment/instance transforms. Only graph labels may change.
    graph = result.get('exportHierarchy', [])
    for node in graph:
        ident = str(node.get('modelId', ''))
        key = f'model:{ident}' if node.get('type') == 'model' else (
            f'mesh:{ident}:{node.get("sourceMeshIndex", node.get("meshIndex"))}' if node.get('type') == 'mesh' else None)
        if key in names:
            node['name'] = names[key]
    if 'scene' in names and not is_map:
        graph_roots = [node for node in graph if not node.get('parentId')]
        if len(graph_roots) == 1 and graph_roots[0].get('type') == 'entity':
            graph_roots[0]['name'] = names['scene']
    return result
