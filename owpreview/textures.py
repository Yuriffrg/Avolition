"""Decode registered export textures and the Blender importer's packed channels."""
from __future__ import annotations

import base64
from collections import OrderedDict
from io import BytesIO
from pathlib import Path
import threading
import warnings

from PIL import Image, ImageMath, ImageOps

MAX_FILE_BYTES = 128 * 1024 * 1024
MAX_SOURCE_PIXELS = 64 * 1024 * 1024
MAX_PREVIEW_EDGE = 2048
CACHE_BYTES = 32 * 1024 * 1024
IMAGE_EXTENSIONS = {'.png', '.jpg', '.jpeg', '.tga', '.dds', '.tif', '.tiff', '.bmp', '.webp'}
TEXTURE_OWNERS = ('animations', 'maps', 'model-browser')


def _float_channel(image, channel, srgb=False):
    values = [value / 255 for value in range(256)]
    if srgb:
        values = [v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4 for v in values]
    return image.getchannel(channel).point(values, 'F')


def _byte(expression, **images):
    return ImageMath.lambda_eval(lambda args: args['convert'](expression(args), 'L'), **images)


def process_channels(image, spec):
    """Keep color bytes unchanged; decode sRGB for scalar channels when specified."""
    channel = spec.get('channel', 'rgba')
    transform = spec.get('transform', 'none')
    if transform == 'ow-normal':
        # OWM: Normal in the original Blender library uses stereographic packing,
        # not the usual reconstruct-Z-from-RG convention.
        red, green, blue = (_float_channel(image, band) for band in 'RGB')
        x = ImageMath.lambda_eval(lambda a: (2*a['r']-1)/(31.875*a['b']+1), r=red, b=blue)
        y = ImageMath.lambda_eval(lambda a: (2*a['g']-1)/(31.875*a['b']+1), g=green, b=blue)
        q = ImageMath.lambda_eval(lambda a: 2/(1+a['x']**2+a['y']**2), x=x, y=y)
        nx = _byte(lambda a: (a['q']*a['x']+1)*127.5, q=q, x=x)
        ny = _byte(lambda a: (a['q']*a['y']+1)*127.5, q=q, y=y)
        nz = _byte(lambda a: a['q']*127.5, q=q)
        return Image.merge('RGB', (nx, ny, nz))
    if channel in ('rgb', 'rgba'):
        result = image.convert(channel.upper())
    elif channel in ('r', 'g', 'b', 'a'):
        if channel != 'a' and spec.get('inputColorSpace') == 'srgb':
            value = _float_channel(image, channel.upper(), srgb=True)
            result = _byte(lambda a: a['v']*255, v=value)
        else:
            result = image.getchannel(channel.upper())
    elif channel == 'luminance':
        srgb = spec.get('inputColorSpace', 'linear') == 'srgb'
        r, g, b = (_float_channel(image, band, srgb) for band in 'RGB')
        result = _byte(lambda a: 255*(0.21263909*a['r']+0.71516913*a['g']+0.07219274*a['b']), r=r, g=g, b=b)
    else:
        raise ValueError(f'Unsupported texture channel: {channel}')
    if transform == 'invert':
        if result.mode != 'L':
            raise ValueError('Inverted packed channels must be scalar.')
        result = ImageOps.invert(result)
    elif transform == 'ow-metalness':
        if result.mode != 'L':
            raise ValueError('Packed metalness must be a scalar channel.')
        values = result.point([i/255 for i in range(256)], 'F')
        result = _byte(lambda a: a['max'](2*a['v']-1, 0)*(a['v'] > 0.525)*255, v=values)
    elif transform != 'none':
        raise ValueError(f'Unsupported texture transform: {transform}')
    # Three's AO/roughness/metalness maps sample different RGB components.
    # Duplicating the selected scalar into RGB makes their meaning explicit.
    return result.convert('RGB') if result.mode == 'L' else result


class TextureStore:
    """Only identifiers registered by a successful entity import can read images."""
    def __init__(self):
        self._lock = threading.RLock()
        self._specs = {}
        self._owners = {}
        self._images = OrderedDict()
        self._image_bytes = 0
        self._results = OrderedDict()
        self._result_bytes = 0

    def register(self, textures, *, owner=None):
        with self._lock:
            previous = self._specs
            specs = {str(key): dict(value) for key, value in (textures or {}).items()}
            if owner is None:
                self._owners = {}
                self._specs = specs
            else:
                if owner not in TEXTURE_OWNERS:
                    raise ValueError('Unknown texture workspace.')
                self._owners[owner] = specs
                self._specs = {key: value for owned in self._owners.values() for key, value in owned.items()}
            if owner == 'model-browser':
                # Browsing candidates must not flush the loaded scenes' decoded
                # textures every time the user changes the preview selection.
                self._prune_unowned_cache(previous)
                return
            for image, _warning in self._images.values():
                image.close()
            self._images.clear()
            self._results.clear()
            self._image_bytes = self._result_bytes = 0

    def _prune_unowned_cache(self, previous):
        for key, result in list(self._results.items()):
            if key[0] not in self._specs or previous.get(key[0]) != self._specs[key[0]]:
                del self._results[key]
                self._result_bytes -= len(result['dataUri'])
        if self._images:
            paths = {str(Path(spec['path']).resolve()) for spec in self._specs.values()}
            for key, (image, _warning) in list(self._images.items()):
                if key[0] not in paths:
                    del self._images[key]
                    self._image_bytes -= image.width * image.height * 4
                    image.close()

    def release(self, owner):
        """Revoke one workspace's texture access without evicting shared data."""
        if owner not in TEXTURE_OWNERS:
            raise ValueError('Unknown texture workspace.')
        with self._lock:
            if owner not in self._owners:
                return
            previous = self._specs
            del self._owners[owner]
            self._specs = {key: value for owned in self._owners.values() for key, value in owned.items()}
            self._prune_unowned_cache(previous)

    def _image(self, path, max_edge=MAX_PREVIEW_EDGE):
        path = Path(path).resolve()
        if path.suffix.lower() not in IMAGE_EXTENSIONS:
            raise ValueError(f'{path.name}: unsupported texture image format')
        key = (str(path), max_edge)
        if key in self._images:
            value = self._images.pop(key)
            self._images[key] = value
            return value
        if path.stat().st_size > MAX_FILE_BYTES:
            raise ValueError(f'{path.name}: texture exceeds the 128 MB preview limit')
        with warnings.catch_warnings():
            warnings.simplefilter('error', Image.DecompressionBombWarning)
            with Image.open(path) as original:
                if original.width * original.height > MAX_SOURCE_PIXELS:
                    raise ValueError(f'{path.name}: texture exceeds the 64-megapixel preview limit')
                original.load()
                rgba = original.convert('RGBA')
        reduced = max(rgba.size) > max_edge
        if reduced:
            ratio = max_edge / max(rgba.size)
            size = (max(1, round(rgba.width*ratio)), max(1, round(rgba.height*ratio)))
            # Alpha is often a packed scalar, not opacity. Resize each channel
            # separately so alpha premultiplication cannot alter normals/masks.
            resized = Image.merge('RGBA', tuple(band.resize(size, Image.Resampling.LANCZOS) for band in rgba.split()))
            rgba.close()
            rgba = resized
        warning = f'{path.name}: preview resized to {rgba.width} × {rgba.height}' if reduced else None
        needed = rgba.width * rgba.height * 4
        while self._images and self._image_bytes + needed > CACHE_BYTES:
            (_key, (old, _warning)) = self._images.popitem(last=False)
            self._image_bytes -= old.width * old.height * 4
            old.close()
        self._images[key] = (rgba, warning)
        self._image_bytes += needed
        return rgba, warning

    def load(self, texture_id, max_edge=None):
        if max_edge is None:
            max_edge = MAX_PREVIEW_EDGE
        elif type(max_edge) is not int or not 32 <= max_edge <= MAX_PREVIEW_EDGE:
            raise ValueError(f'Preview texture size must be an integer from 32 to {MAX_PREVIEW_EDGE}.')
        with self._lock:
            if not isinstance(texture_id, str) or texture_id not in self._specs:
                raise ValueError('Texture is not registered for the loaded entity. Reload the entity.')
            cache_key = (texture_id, max_edge)
            if cache_key in self._results:
                result = self._results.pop(cache_key)
                self._results[cache_key] = result
                return result
            spec = self._specs[texture_id]
            source, warning = self._image(spec['path'], max_edge)
            processed = process_channels(source, spec)
            try:
                buffer = BytesIO()
                processed.save(buffer, format='PNG', compress_level=3)
                data_uri = 'data:image/png;base64,' + base64.b64encode(buffer.getvalue()).decode('ascii')
                result = {'dataUri': data_uri, 'width': processed.width, 'height': processed.height}
                if warning:
                    result['warning'] = warning
            finally:
                processed.close()
            while self._results and self._result_bytes + len(data_uri) > CACHE_BYTES:
                _key, previous = self._results.popitem(last=False)
                self._result_bytes -= len(previous['dataUri'])
            if len(data_uri) <= CACHE_BYTES:
                self._results[cache_key] = result
                self._result_bytes += len(data_uri)
            return result
