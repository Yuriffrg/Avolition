"""OWMAT 3.0 look/material descriptors for the standalone texture preview.

SPDX-License-Identifier: GPL-3.0-or-later

Binary layouts follow OWLib DataTool/SaveLogic/Model.cs. Texture binding hashes
come from io_scene_owm shader_metadata.py. File names are references, never a
source of inferred texture roles. Images are decoded lazily by the host API.
"""

from __future__ import annotations

import hashlib
import json
import math
import os
from pathlib import Path
import struct

from .formats import FormatError, _Reader


SUPPORTED_IMAGES = {'.png', '.jpg', '.jpeg', '.tga', '.dds', '.tif', '.tiff', '.bmp', '.webp'}
BASE_COLOR = {2903569922, 3093211343, 67369114, 1716930793, 1239794147, 3411049489}
NORMAL = {378934698, 229655782, 562391268, 2762290483}
PBR = {548341454, 2018946191, 763550166, 1239777102, 3111105361}
EMISSION = {3166598269, 1523270506, 1493127177}
ALPHA = {1482859648, 1140682086}
AO = {3335614873, 3761386704}
ALBEDO_ALPHA = {67369114, 1239794147}
COMBINED_DECAL = 3989656707
HAIR_ROUGHNESS = 1117188170
UV_EMISSION = 3344068240
EMISSION_STRENGTH = (3250491852, 2446772623)
OPACITY = 565460110

# io_scene_owm shader_metadata.CollisionMaterials. These exact material GUIDs
# mark the game's invisible engine hit-test surfaces, not visible architecture.
# The Blender map importer removes them by default (SettingTypes.removeCollision).
COLLISION_MATERIALS = frozenset({
    '0000000034A0', '000000002DD4', '000000002D77', '0000000034A2',
    '000000000796', '000000000005', '000000000794', '0000000048EF',
    '0000000034A3', '000000000797', '0000000007A2', '0000000007A0',
    '0000000007C0', '0000000007A1',
})


def is_collision_material(material):
    """Classify only a referenced material's exact GUID, never shader or color."""
    if not isinstance(material, dict) or not material.get('sourcePath'):
        return False
    return Path(material['sourcePath']).stem.upper() in COLLISION_MATERIALS

# Populated only with channels confirmed against the supplied Blender node
# library. Unrecognized packed data is reported, rather than guessed.
PBR_CHANNELS = {'roughness': ('g', 'invert'), 'metalness': ('r', 'ow-metalness')}


def read_owmat(path):
    """Read an OWMAT record while respecting each static input's byte length."""
    path = Path(path).resolve()
    if path.stat().st_size > 32 * 1024 * 1024:
        raise FormatError(f'{path.name}: material file exceeds 32 MB')
    r = _Reader.file(path)
    major, minor, kind = r.unpack('HHI')
    if (major, minor) != (3, 0):
        r.fail(f'unsupported material version {major}.{minor}; expected 3.0')
    if kind == 1:
        count = r.unpack('Q')[0]
        r.count(count, 16384, 'material look entries')
        entries = []
        for _ in range(count):
            entries.append((str(r.unpack('Q')[0]), r.string()))
        # OWLib writes an extra zero long for a null (not empty) material list.
        if r.remaining and not (count == 0 and r.remaining == 8 and r.unpack('Q')[0] == 0):
            r.fail('unexpected trailing model-look data')
        return {'kind': 'look', 'path': str(path), 'entries': entries}
    if kind != 0:
        r.fail(f'unknown material record type {kind}')
    texture_count, static_count, shader = r.unpack('QQI')
    r.count(texture_count, 4096, 'texture count')
    r.count(static_count, 65536, 'static input count')
    textures = []
    for _ in range(texture_count):
        textures.append((r.string(), r.unpack('I')[0]))
    inputs = {}
    for _ in range(static_count):
        key, length = r.unpack('II')
        r.count(length, 1024 * 1024, 'static input byte length')
        inputs[key] = r.take(length).tobytes()
    if r.remaining:
        r.fail(f'unexpected {r.remaining} trailing material bytes')
    return {'kind': 'material', 'path': str(path), 'shader': shader,
            'textures': textures, 'inputs': inputs}


def _default_root(asset):
    for ancestor in asset.parents:
        if ancestor.name.lower() in ('models', 'entities'):
            return ancestor.parent
    return asset.parent


class MaterialResolver:
    """Resolve only referenced assets beneath the selected export root."""

    def __init__(self, asset_path, root_path=None, warnings=None):
        self.root = Path(root_path).resolve() if root_path else _default_root(Path(asset_path).resolve())
        self.warnings = warnings if warnings is not None else []
        self.textures = {}
        self._records = {}
        self._descriptors = {}
        self._index = None

    def warn(self, message):
        if message not in self.warnings:
            self.warnings.append(message)

    def _within_root(self, path):
        try:
            return path.resolve().is_relative_to(self.root)
        except (OSError, ValueError):
            return False

    @staticmethod
    def _clean(reference):
        return reference.replace('\\', os.sep).replace('/', os.sep)

    def resolve(self, owner, reference, *, file_relative=False, extension=None):
        if not reference:
            return None
        owner = Path(owner).resolve()
        reference = self._clean(reference)
        ref = Path(reference)
        candidates = []
        if ref.is_absolute():
            candidates.append(ref)
        else:
            # The ModelLook writer prefixes ../.. and Blender resolves it from
            # the full look filename. Other references use its parent folder.
            if file_relative:
                candidates.append(owner / ref)
            candidates.append(owner.parent / ref)
            candidates.append(self.root / ref)
        normalized = []
        for candidate in candidates:
            try:
                candidate = candidate.resolve()
            except (OSError, ValueError):
                self.warn(f'{owner.name}: invalid referenced path was skipped.')
                continue
            if candidate not in normalized and self._within_root(candidate):
                normalized.append(candidate)
        if not normalized:
            self.warn(f'{owner.name}: reference outside export root was skipped: {reference}')
            return None
        for candidate in normalized:
            if candidate.is_file() and (extension is None or candidate.suffix.lower() == extension):
                return candidate
        # DataTool falls back to DDS if requested PNG/TIFF conversion fails.
        # This fallback changes only the extension of the exact reference.
        if extension is None and ref.suffix.lower() in ('.png', '.tif', '.tiff'):
            for candidate in normalized:
                fallback = candidate.with_suffix('.dds')
                if fallback.is_file() and self._within_root(fallback):
                    self.warn(f'{owner.name}: using DataTool DDS fallback for {ref.name}.')
                    return fallback
        # When folders have been copied, accept a unique exact basename, never
        # a filename pattern or an arbitrary duplicate from a different skin.
        if self._index is None:
            self._index = {}
            count = 0
            for directory, _, files in os.walk(self.root, followlinks=False):
                for filename in files:
                    count += 1
                    if count > 500_000:
                        self.warn('Material search stopped after 500,000 files; choose a smaller export root.')
                        break
                    if Path(filename).suffix.lower() in SUPPORTED_IMAGES | {'.owmat'}:
                        self._index.setdefault(filename.lower(), []).append(Path(directory) / filename)
                if count > 500_000:
                    break
        matches = [path.resolve() for path in self._index.get(ref.name.lower(), []) if self._within_root(path)]
        if len(matches) == 1 and (extension is None or matches[0].suffix.lower() == extension):
            return matches[0]
        if len(matches) > 1:
            self.warn(f'{owner.name}: ambiguous referenced file {ref.name}; texture/material skipped.')
        else:
            self.warn(f'{owner.name}: missing referenced file {reference}.')
        return None

    def _read(self, path):
        path = Path(path).resolve()
        if not self._within_root(path):
            raise FormatError(f'Material file is outside export root: {path.name}')
        if path not in self._records:
            self._records[path] = read_owmat(path)
        return self._records[path]

    def _input(self, record, key, fmt, default=None):
        raw = record['inputs'].get(key)
        if raw is None:
            return default
        size = struct.calcsize('<' + fmt)
        if len(raw) < size:
            self.warn(f'{Path(record["path"]).name}: short static input {key} was ignored.')
            return default
        values = struct.unpack_from('<' + fmt, raw)
        if not all(math.isfinite(value) for value in values):
            self.warn(f'{Path(record["path"]).name}: non-finite static input {key} was ignored.')
            return default
        return values[0] if len(values) == 1 else list(values)

    def _texture(self, path, channel, transform='none', input_color_space='linear'):
        descriptor = {'path': str(path), 'channel': channel, 'transform': transform,
                      'inputColorSpace': input_color_space}
        identity = json.dumps(descriptor, sort_keys=True, separators=(',', ':')).encode('utf-8')
        texture_id = hashlib.sha256(identity).hexdigest()[:24]
        self.textures.setdefault(texture_id, descriptor)
        return texture_id

    def descriptor(self, path):
        path = Path(path).resolve()
        if path in self._descriptors:
            return self._descriptors[path]
        record = self._read(path)
        if record['kind'] != 'material':
            raise FormatError(f'{path.name}: a material look cannot be used as a material record')
        shader = record['shader']
        result = {'name': path.stem, 'sourcePath': str(path), 'shaderId': shader,
                  'baseColor': [0.62, 0.62, 0.62], 'roughness': 0.7, 'metalness': 0.0,
                  'emissive': [0.0, 0.0, 0.0], 'opacity': 1.0,
                  'alphaMode': 'OPAQUE', 'alphaCutoff': 0.5, 'doubleSided': True, 'maps': {}}
        unsupported = []
        ow2_skin = shader == 167 or (shader == 43 and 4081294361 in record['inputs'])
        complex_emission = shader in (36, 37, 44, 50, 51, 54) or (shader == 43 and not ow2_skin)
        strength = 0.0 if shader == 38 else 32.0 if ow2_skin else 128.0 if shader in (34, 56) else 16.0
        for key in EMISSION_STRENGTH:
            strength = self._input(record, key, 'f', strength)
        strength = min(10000.0, max(0.0, strength))
        if OPACITY in record['inputs']:
            self.warn(f'{path.name}: game opacity control is not reproduced; texture alpha is used for preview.')
        if shader == 221:
            result['roughness'] = 0.0

        def add_map(role, texture, key, channel='rgb', transform='none', color_space='linear', source_space='linear'):
            if role in result['maps']:
                self.warn(f'{path.name}: additional {role} layer is not supported; using the first mapped layer.')
                return False
            uv = 1 if key == 3335614873 else 0
            if key == 3166598269:
                # Blender's UVMap names start at 1; Three's channels at 0.
                uv = self._input(record, UV_EMISSION, 'I', 1) - 1
            if (role == 'alpha' and key == 1239794147 and shader == 51
                    and {4101268840, 758934576}.issubset({binding for _, binding in record['textures']})):
                uv = 1
            if uv not in (0, 1):
                self.warn(f'{path.name}: unsupported UV channel {uv} for {role}; map skipped.')
                return False
            repeat = [1.0, 1.0]
            if key in (2903569922, 548341454, 378934698):
                repeat = self._input(record, 1883253226, 'ff', repeat)
                if 1883253226 in record['inputs']:
                    self.warn(f'{path.name}: preview honors the known Basic UV Scale metadata; Blender versions may ignore this control.')
                if any(abs(value) > 10000 for value in repeat):
                    self.warn(f'{path.name}: extreme basic UV scale ignored.')
                    repeat = [1.0, 1.0]
            result['maps'][role] = {'textureId': self._texture(texture, channel, transform, source_space),
                                    'uvIndex': uv, 'colorSpace': color_space, 'repeat': repeat}
            return True

        for reference, key in record['textures']:
            supported = key in BASE_COLOR | NORMAL | PBR | EMISSION | ALPHA | AO | {COMBINED_DECAL, HAIR_ROUGHNESS}
            if not supported:
                unsupported.append(key)
                continue
            texture = self.resolve(path, reference)
            if texture is None:
                continue
            if texture.suffix.lower() not in SUPPORTED_IMAGES:
                self.warn(f'{path.name}: unsupported texture format {texture.suffix} for {texture.name}.')
                continue
            if key in BASE_COLOR or key == COMBINED_DECAL:
                add_map('baseColor', texture, key, 'rgb', color_space='srgb', source_space='srgb')
                result['baseColor'] = [1.0, 1.0, 1.0]
            if key in ALBEDO_ALPHA:
                add_map('alpha', texture, key, 'a')
                result['alphaMode'] = 'BLEND' if key == 67369114 else 'MASK'
            if key in NORMAL:
                # Matches OWM: Unpack Blue Channel's stereographic decoding;
                # this is not the usual BC5/sqrt reconstruction.
                add_map('normal', texture, key, 'rgb', transform='ow-normal')
            if key in PBR:
                for role, (channel, transform) in PBR_CHANNELS.items():
                    if shader == 221 or (role == 'metalness' and ow2_skin):
                        continue
                    add_map(role, texture, key, channel, transform)
                    result[role] = 1.0
            if key in EMISSION or key == COMBINED_DECAL:
                if complex_emission:
                    self.warn(f'{path.name}: layered shader emission was omitted from the preview.')
                elif key == 1493127177:
                    if add_map('emissive', texture, key, 'rgb', color_space='srgb', source_space='srgb'):
                        result['emissive'] = [1.0, 1.0, 1.0]
                        result['emissiveIntensity'] = strength
                else:
                    if add_map('emissive', texture, key, 'luminance',
                               source_space='srgb' if key == COMBINED_DECAL else 'linear'):
                        result['emissive'] = [1.0, 1.0, 1.0]
                        result['emissiveIntensity'] = strength
                        result['emissiveMode'] = 'baseColorMask'
                        result['emissiveUsesAO'] = not (ow2_skin or shader in (217, 221))
            if key in ALPHA or key == COMBINED_DECAL:
                channel = 'r' if shader in (34, 56) else 'luminance'
                add_map('alpha', texture, key, channel, source_space='srgb' if key == COMBINED_DECAL else 'linear')
                result['alphaMode'] = 'MASK'
            if key in AO:
                # Three's AO input is scalar; Overwatch's general AO can tint
                # the base color as RGB. The red-channel preview is approximate.
                add_map('ao', texture, key, 'r')
                if key == 3335614873:
                    self.warn(f'{path.name}: colored ambient occlusion is approximated with its red channel.')
            if key == HAIR_ROUGHNESS:
                add_map('roughness', texture, key, 'luminance')
                result['roughness'] = 1.0
            if key == 2903569922 and shader in (44, 217):
                add_map('ao', texture, key, 'a')
        if unsupported:
            self.warn(f'{path.name}: unsupported texture bindings {", ".join(str(value) for value in sorted(set(unsupported)))} were omitted.')
        if shader in (34, 36, 37, 38, 43, 44, 50, 51, 54, 56, 167, 217, 221):
            self.warn(f'{path.name}: shader {shader} is approximated with common PBR maps; game-specific layers, refraction and subsurface effects are not reproduced.')
        self._descriptors[path] = result
        return result

    def for_model(self, model, entity_look='', *, partial=False):
        """Map the model-look's uint64 material keys to per-material descriptors."""
        model_path = Path(model['path']).resolve()
        reference = model.get('materialPath', '')
        if entity_look:
            look_name = Path(self._clean(entity_look)).name
            if look_name.lower().endswith('.owmat'):
                look_name = look_name[:-6]
            if look_name.upper().endswith('.01A'):
                look_name = look_name[:-4]
            reference = str(Path('ModelLooks') / (look_name + '.owmat'))
        model['materialLook'] = reference or None
        if not reference:
            self.warn(f'{model_path.name}: no material look reference; using neutral shading.')
            return {}
        look_path = self.resolve(model_path, reference, extension='.owmat')
        if look_path is None:
            return {}
        try:
            look = self._read(look_path)
            if look['kind'] != 'look':
                self.warn(f'{look_path.name}: expected a model look; no material-key assignments were inferred.')
                return {}
            result = {}
            required_keys = {mesh['materialKey'] for mesh in model['meshes']}
            for key, reference in look['entries']:
                if key not in required_keys:
                    continue
                if partial and not reference:
                    # BLMaterial.bindModelLook explicitly clears a present null
                    # assignment; this differs from an absent override slot.
                    result[key] = None
                    continue
                material_path = self.resolve(look_path, reference, file_relative=True, extension='.owmat')
                if material_path is None:
                    continue
                try:
                    result.setdefault(key, self.descriptor(material_path))
                except (OSError, FormatError) as error:
                    self.warn(f'Skipped material {material_path.name}: {error}')
            if not partial:
                for key in sorted(required_keys - result.keys()):
                    self.warn(f'{model_path.name}: material slot {key} is unresolved; using neutral shading for that mesh.')
            return result
        except (OSError, FormatError) as error:
            self.warn(f'Skipped model look {look_path.name}: {error}')
            return {}
