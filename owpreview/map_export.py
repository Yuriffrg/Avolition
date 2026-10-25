"""Static maps through Avolition's existing transactional native C4D bridge."""
from pathlib import Path
import re

from .c4d_bridge import BridgeClient, map_bridge_message, FEATURE_BRIDGE_MESSAGE
from .c4d_export import _normalized_normal_asset
from .c4d_scene import _ordered_graph, _asset_models
from .export_bundle import export_bundle
from .export_textures import bake_materials


def _scene_name(value):
    name = str(value or '').strip()
    if name.lower().endswith('.c4d'):
        name = name[:-4]
    if (not name or name in ('.', '..') or re.search(r'[<>:"/\\|?*\x00-\x1f]', name)
            or name.endswith(('.', ' ')) or
            re.fullmatch(r'(?i)(CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])', name.split('.')[0])):
        raise ValueError('Enter a map scene name that Windows can use as a filename.')
    if len(name.encode('utf-16-le')) // 2 > 180:
        raise ValueError('Keep the map scene filename within 180 characters.')
    return name


def make_static_payload(asset, materials=None, name=None):
    """No inferred animation: preserve exact map placement and rest attachments.

    Compact geometry stays compact over IPC. Normalization is performed once
    per prototype, not once for every placement of the same model.
    """
    if not isinstance(asset, dict) or not asset.get('map'):
        raise ValueError('Load a map before exporting a static map scene.')
    _ordered_graph(asset.get('exportHierarchy', []))
    resolved = _asset_models(asset)
    if not any(model['meshes'] for model in resolved.values()):
        raise ValueError('The loaded map has no meshes to export.')
    if 'meshPrototypes' in asset:
        keys = list(asset['meshPrototypes'])
        normalized = _normalized_normal_asset({'models': [asset['meshPrototypes'][key] for key in keys],
                                               'warnings': asset.get('warnings', [])})
        prepared = dict(asset, meshPrototypes=dict(zip(keys, normalized['models'])),
                        warnings=normalized['warnings'])
    else:
        prepared = _normalized_normal_asset(asset)
    return dict(version=1, asset=prepared, name=name or asset.get('name', 'Map'),
                materials=materials or {}, includeMesh=True, mapInstances=True, mapTiming24=True,
                animation={'fps': 24, 'lastFrame': 0, 'tracks': []},
                warnings=list(prepared.get('warnings', [])))


def _bake_map_materials(asset, texture_folder, progress=None):
    # One appearance may occur thousands of times. The map adapter preserves
    # shared immutable material dictionaries, as does apply_object_names.
    representatives, appearances, aliases = [], {}, {}
    for model in asset['models']:
        key = id(model.get('materials'))
        if key not in appearances:
            appearances[key] = model['id']
            representatives.append(model)
        aliases[model['id']] = appearances[key]
    baked = bake_materials(dict(asset, models=representatives), texture_folder, progress=progress)
    baked['materials'] = {mid: baked['materials'].get(source, {}) for mid, source in aliases.items()}
    return baked


def export_map(asset, output_folder, scene_name=None, *, bake_textures=True, bridge=None, progress=None):
    """Export an already loaded/renamed map; publish only a verified native file."""
    if not isinstance(bake_textures, bool):
        raise ValueError('Bake textures must be on or off.')
    name = _scene_name(asset.get('name') if scene_name is None else scene_name)
    folder = Path(output_folder).expanduser()
    if not folder.is_absolute() or (folder.exists() and not folder.is_dir()):
        raise ValueError('Choose a full path to a map output folder.')
    folder = folder.resolve()
    client = bridge or BridgeClient()
    status = client.status()
    if not status.get('connected') or not status.get('octane'):
        raise RuntimeError(status.get('message', 'Cinema 4D with Octane is not connected.'))
    if status.get('mapInstances') is not True:
        raise RuntimeError(map_bridge_message(status))
    if status.get('mapTiming24') is not True:
        raise RuntimeError(FEATURE_BRIDGE_MESSAGE)
    if progress:
        progress('Preparing map geometry')
    payload = make_static_payload(asset, name=name)
    def build(destinations, texture_folder):
        if bake_textures:
            baked = _bake_map_materials(asset, texture_folder, progress)
        else:
            baked = {'materials': {model['id']: {str(key): dict(value, maps={})
                     for key, value in model.get('materials', {}).items()} for model in asset['models']},
                     'warnings': []}
        payload['materials'] = baked['materials']
        if progress:
            report = client.export(payload, destinations['c4d'], texture_folder, progress=progress)
            progress('Publishing the C4D file and textures to the output folder')
        else:
            report = client.export(payload, destinations['c4d'], texture_folder)
        return {'warnings': list(dict.fromkeys(payload.get('warnings', []) + baked.get('warnings', []) +
                                              report.get('warnings', [])))}
    return export_bundle(asset['path'], folder, name + '.c4d', build, ('c4d',))
