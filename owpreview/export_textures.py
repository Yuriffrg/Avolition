"""Portable common-PBR texture baking without scene lighting or game shaders.

SPDX-License-Identifier: GPL-3.0-or-later

The input is load_asset()'s registered material/texture manifest. Output maps
keep explicit UV sets, repeat and color space; incompatible UV layouts are not
silently flattened. Pixel decoding is shared with the preview, but export uses
the original image resolution rather than TextureStore's 2048px preview cache.
"""

from __future__ import annotations

import copy
import hashlib
from io import BytesIO
import json
import math
from pathlib import Path
import re
import warnings

from PIL import Image, ImageMath

from .textures import IMAGE_EXTENSIONS, MAX_FILE_BYTES, MAX_SOURCE_PIXELS, process_channels


_ROLES = ('baseColor', 'normal', 'roughness', 'metalness', 'emissive', 'alpha', 'ao')
_LINEAR = [value / 255 for value in range(256)]
_SRGB = [value / 12.92 if value <= .04045 else ((value + .055) / 1.055) ** 2.4 for value in _LINEAR]


def _finite(value, default=1.0):
    try:
        value = float(value)
        return value if math.isfinite(value) else default
    except (ValueError, TypeError):
        return default


def _color(value, default):
    if not isinstance(value, (list, tuple)) or len(value) != 3:
        value = default
    return [max(0, _finite(component)) for component in value]


def _mapping(value):
    return {'uvIndex': int(value.get('uvIndex', 0)),
            'repeat': [float(part) for part in value.get('repeat', [1, 1])],
            'colorSpace': value.get('colorSpace', 'linear')}


def _same_uv(a, b):
    return a.get('uvIndex', 0) == b.get('uvIndex', 0) and all(
        abs(float(x)-float(y)) < 1e-7 for x, y in zip(a.get('repeat', [1, 1]), b.get('repeat', [1, 1])))


def _encode(band, srgb=True):
    def expression(args):
        value = args['max'](args['min'](args['v'], 1), 0)
        if srgb:
            value = ((value <= .0031308) * (12.92 * value)
                     + (value > .0031308) * (1.055 * value ** (1/2.4) - .055))
        return args['convert'](value * 255 + .5, 'L')
    return ImageMath.lambda_eval(expression, v=band)


def _resized(image, size):
    return image if image.size == size else image.resize(size, Image.Resampling.BILINEAR)


def _combined_size(images):
    size = (max(image.width for image in images), max(image.height for image in images))
    if size[0] * size[1] > MAX_SOURCE_PIXELS:
        raise ValueError('Combined texture dimensions exceed the 64-megapixel export limit.')
    return size


def _composite_color(color, color_space, factor, masks, size, gain=1):
    """Multiply in linear light and encode one portable sRGB map."""
    converted_masks = []
    result_bands = []
    try:
        for mask in masks:
            resized_mask = _resized(mask, size)
            scalar = resized_mask.getchannel('R').point(_LINEAR, 'F')
            converted_masks.append(scalar)
            if resized_mask is not mask:
                resized_mask.close()
        for index, channel in enumerate('RGB'):
            if color is None:
                band = Image.new('F', size, 1)
            else:
                band = color.getchannel(channel).point(_SRGB if color_space == 'srgb' else _LINEAR, 'F')
                if band.size != size:
                    # GPU sRGB sampling filters after conversion to linear
                    # light. Resizing encoded RGB first would darken edges.
                    original_band = band
                    band = band.resize(size, Image.Resampling.BILINEAR)
                    original_band.close()
            try:
                value = ImageMath.lambda_eval(lambda args: args['v'] * factor[index] / gain, v=band)
                for mask in converted_masks:
                    following = ImageMath.lambda_eval(lambda args: args['a'] * args['b'], a=value, b=mask)
                    value.close()
                    value = following
                try:
                    result_bands.append(_encode(value))
                finally:
                    value.close()
            finally:
                band.close()
        return Image.merge('RGB', result_bands)
    finally:
        for image in result_bands + converted_masks:
            image.close()


def _scale_scalar(image, factor):
    values = [min(255, max(0, round(value * factor))) for value in range(256)]
    band = image.getchannel('R').point(values)
    try:
        return Image.merge('RGB', (band, band, band))
    finally:
        band.close()


def _write_png(folder, stem, image):
    stream = BytesIO()
    image.save(stream, format='PNG', compress_level=6)
    content = stream.getvalue()
    # Exclusive creation protects an existing unrelated file, even when it has
    # the expected hash-derived name. Repeating this bake reuses identical data.
    for index in range(1, 10001):
        candidate = folder / f'{stem}{"" if index == 1 else "_" + str(index)}.png'
        try:
            with candidate.open('xb') as output:
                try:
                    output.write(content)
                except BaseException:
                    output.close()
                    candidate.unlink(missing_ok=True)
                    raise
            return candidate
        except FileExistsError:
            if candidate.is_file() and candidate.read_bytes() == content:
                return candidate
    raise ValueError('Too many conflicting exported texture filenames.')


def bake_materials(asset, target_tex_dir, name_prefix='', *, progress=None):
    """Return {materials:{modelId:{key:descriptor}}, files, warnings}.

    Map descriptors contain path, relativePath (tex/...), uvIndex, repeat and
    colorSpace. Color/AO and compatible emission masks are baked in linear
    light. Emission intensity remains an explicit scalar so HDR brightness is
    not clipped into an 8-bit PNG. unbakedMaps marks incompatible shader math;
    consumers should not treat those retained source maps as complete bakes.
    The caller owns staging/publishing the returned files and FBX transaction.
    """
    target = Path(target_tex_dir).expanduser().resolve()
    target.mkdir(parents=True, exist_ok=True)
    root = Path(asset.get('textureRoot') or Path(asset['path']).parent).resolve()
    specs = asset.get('textures', {})
    output = {'materials': {}, 'files': [], 'warnings': list(dict.fromkeys(asset.get('warnings', [])))}
    prefix = re.sub(r'[^\w.-]+', '_', str(name_prefix)).strip(' ._')[:48]
    prefix = prefix + '_' if prefix else ''
    hashes = {}
    baked_materials = {}
    total = sum(len(model.get('materials', {})) for model in asset.get('models', []))
    completed = 0

    def warn(message):
        if message not in output['warnings']:
            output['warnings'].append(message)

    def read_map(reference):
        texture_id = reference.get('textureId')
        if texture_id not in specs:
            raise ValueError('texture is not registered in this asset')
        spec = specs[texture_id]
        source = Path(spec['path']).resolve()
        if not source.is_relative_to(root):
            raise ValueError('texture reference is outside the export root')
        if source.suffix.lower() not in IMAGE_EXTENSIONS:
            raise ValueError('unsupported texture image format')
        if source.stat().st_size > MAX_FILE_BYTES:
            raise ValueError('texture exceeds the 128 MB source limit')
        # Digest exactly the immutable bytes used by Pillow, so stale caches or
        # a source modified in place cannot reuse the previous exported name.
        raw = source.read_bytes()
        source_hash = hashlib.sha256(raw).hexdigest()
        with warnings.catch_warnings():
            warnings.simplefilter('error', Image.DecompressionBombWarning)
            with Image.open(BytesIO(raw)) as original:
                if original.width * original.height > MAX_SOURCE_PIXELS:
                    raise ValueError('texture exceeds the 64-megapixel source limit')
                original.load()
                rgba = original.convert('RGBA')
        try:
            image = process_channels(rgba, spec)
        finally:
            rgba.close()
        identity = {key: value for key, value in spec.items() if key != 'path'}
        identity['sourceContent'] = source_hash
        hashes[texture_id] = identity
        return image

    for model in asset.get('models', []):
        materials = output['materials'].setdefault(str(model['id']), {})
        for key, source_material in model.get('materials', {}).items():
            if progress:
                progress('Baking map materials', completed, total)
            completed += 1
            # Map appearances can repeat one material across hundreds of source
            # models. Reuse this invocation's completed bake, not its pixels;
            # memory remains bounded to the material currently being processed.
            # A new invocation always rereads source bytes and content hashes.
            source_identity = json.dumps(source_material, sort_keys=True, separators=(',', ':'), allow_nan=False)
            if source_identity in baked_materials:
                materials[str(key)] = copy.deepcopy(baked_materials[source_identity])
                continue
            material = copy.deepcopy(source_material)
            original_maps = source_material.get('maps', {})
            images = {}
            maps = {}
            emitted = {}
            label = f'{model.get("name", model["id"])} / {source_material.get("name", key)}'
            try:
                for role in _ROLES:
                    if role not in original_maps:
                        continue
                    try:
                        mapping = _mapping(original_maps[role])
                        images[role] = read_map(original_maps[role])
                        maps[role] = mapping
                    except (OSError, ValueError, Image.DecompressionBombError, Image.DecompressionBombWarning) as error:
                        warn(f'{label}: {role} map could not be exported: {error}')
                # Identity includes all material controls and source contents,
                # while excluding local paths and ephemeral registry IDs.
                identity = {key: value for key, value in source_material.items()
                            if key not in ('maps', 'sourcePath')}
                identity['maps'] = {role: dict(maps[role], source=hashes[original_maps[role]['textureId']])
                                    for role in images}
                digest = hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(',', ':'),
                                                   allow_nan=False).encode('utf-8')).hexdigest()[:24]
                base_factor = _color(material.get('baseColor'), [.62, .62, .62])
                base_image = images.get('baseColor')
                base_map = maps.get('baseColor')
                ao = images.get('ao')
                if base_image is not None or ao is not None:
                    mapping = base_map or maps['ao']
                    masks = []
                    if ao is not None:
                        if base_map is None or _same_uv(base_map, maps['ao']):
                            masks.append(ao)
                        else:
                            warn(f'{label}: AO uses a different UV set or repeat; retained as a separate AO map instead of baking into base color.')
                    sources = [image for image in [base_image, *masks] if image is not None]
                    size = _combined_size(sources)
                    emitted['baseColor'] = (_composite_color(base_image, (base_map or {}).get('colorSpace', 'linear'),
                                                              base_factor, masks, size), dict(mapping, colorSpace='srgb'))
                    material['baseColor'] = [1, 1, 1]
                    if masks:
                        maps.pop('ao')

                if 'emissive' in images:
                    emit_map = maps['emissive']
                    tint = _color(material.get('emissive'), [1, 1, 1])
                    intensity = max(0, _finite(material.get('emissiveIntensity', 1)))
                    if material.get('emissiveMode') == 'baseColorMask':
                        compatible = base_map is None or _same_uv(base_map, emit_map)
                        use_ao = bool(material.get('emissiveUsesAO')) and ao is not None
                        compatible = compatible and (not use_ao or _same_uv(original_maps['ao'], emit_map))
                        if compatible:
                            factor = [a*b for a, b in zip(base_factor, tint)]
                            gain = max(1, *factor)
                            masks = [images['emissive']] + ([ao] if use_ao else [])
                            sources = [image for image in [base_image, *masks] if image is not None]
                            size = _combined_size(sources)
                            emitted['emissive'] = (_composite_color(base_image, (base_map or {}).get('colorSpace', 'linear'),
                                                                   factor, masks, size, gain), dict(emit_map, colorSpace='srgb'))
                            material['emissive'] = [1, 1, 1]
                            material['emissiveIntensity'] = intensity * gain
                            material.pop('emissiveMode', None)
                            material.pop('emissiveUsesAO', None)
                        else:
                            material.setdefault('unbakedMaps', []).append('emissive')
                            warn(f'{label}: emission mask, base color or AO uses different UV coordinates; retained emission mask requires the source shader and is not a baked color map.')
                    else:
                        gain = max(1, *tint)
                        emitted['emissive'] = (_composite_color(images['emissive'], emit_map.get('colorSpace', 'linear'),
                                                               tint, [], images['emissive'].size, gain), dict(emit_map, colorSpace='srgb'))
                        material['emissive'] = [1, 1, 1]
                        material['emissiveIntensity'] = intensity * gain

                for role in ('roughness', 'metalness'):
                    if role in images:
                        emitted[role] = (_scale_scalar(images[role], max(0, _finite(material.get(role, 1)))), maps[role])
                        material[role] = 1
                for role, mapping in maps.items():
                    if role not in emitted:
                        emitted[role] = (images[role], mapping)
                material['maps'] = {}
                for role, (image, mapping) in emitted.items():
                    path = _write_png(target, f'{prefix}{digest}_{role}', image)
                    material['maps'][role] = dict(mapping, path=str(path), relativePath='tex/' + path.name)
                    if str(path) not in output['files']:
                        output['files'].append(str(path))
                materials[str(key)] = material
                baked_materials[source_identity] = material
            finally:
                # Each source may also appear in emitted; close objects once.
                owned = {id(image): image for image in images.values()}
                for image, _mapping_value in emitted.values():
                    owned[id(image)] = image
                for image in owned.values():
                    image.close()
    if progress:
        progress('Baking map materials', completed, total)
    return output
