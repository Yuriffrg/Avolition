"""Create editable Octane Universal materials inside a Cinema 4D Python process.

Input descriptors come from export_textures.bake_materials(). This module uses
only Python's standard library and Cinema 4D's installed Octane plugin. Plugin
and parameter IDs were checked against Octane 1.9.5's public resource headers.
No Octane binaries or shader implementation are bundled with Avolition.
"""
from __future__ import annotations

import importlib
import json
import math
from pathlib import Path, PurePosixPath, PureWindowsPath


MATERIAL_PLUGIN = 1029501
RENDERER_PLUGIN = 1029525
IMAGE_PLUGIN = 1029508
PROJECTION_PLUGIN = 1031460
TRANSFORM_PLUGIN = 1030961
EMISSION_PLUGIN = 1029642
RGB_PLUGIN = 1029504
MULTIPLY_PLUGIN = 1029516
UNIVERSAL_TYPE = 2516

# Public Octane resource IDs. Prefer the installed plugin's exported symbols;
# the fallbacks also work when c4dpy has not exposed those names on c4d yet.
PARAMETERS = {
    'OCT_MATERIAL_TYPE': 2509,
    'OCT_MATERIAL_DIFFUSE_COLOR': 2515, 'OCT_MATERIAL_DIFFUSE_LINK': 2517,
    'OCT_MATERIAL_DIFFUSE_MIX': 2519, 'OCT_MATERIAL_DIFFUSE_FLOAT': 2518,
    'OCT_MATERIAL_SPECULAR_COLOR': 2522, 'OCT_MATERIAL_SPECULAR_FLOAT': 2523,
    'OCT_MATERIAL_ROUGHNESS_FLOAT': 2532, 'OCT_MATERIAL_ROUGHNESS_LINK': 2533,
    'OCT_MATERIAL_ROUGHNESS_MIX': 2534, 'OCT_MAT_SPECULAR_MAP_FLOAT': 2613,
    'OCT_MAT_SPECULAR_MAP_LINK': 2587, 'OCT_MATERIAL_NORMAL_LINK': 2542,
    'OCT_MATERIAL_OPACITY_FLOAT': 2547, 'OCT_MATERIAL_OPACITY_LINK': 2545,
    'OCT_MATERIAL_OPACITY_MIX': 2548, 'OCT_MATERIAL_EMISSION': 2557,
    'OCT_MAT_USE_COLOR': 2563, 'OCT_MAT_USE_SPECULAR': 2564,
    'OCT_MAT_USE_SPECULAR_MAP': 2586, 'OCT_MAT_USE_ROUGHNESS': 2565,
    'OCT_MAT_USE_NORMAL': 2570, 'OCT_MAT_USE_OPACITY': 2571,
    'OCT_MAT_USE_EMISSION': 2575, 'OCT_MAT_BRDF_MODEL': 2585,
    'OCT_MAT_METALLIC_MODE': 2606, 'OCT_DIELECTRIC_INDEX': 2612,
    'OCT_MATERIAL_SMOOTH': 2559,
    'IMAGETEXTURE_FILE': 1100, 'IMAGETEXTURE_GAMMA': 1102,
    'IMAGETEXTURE_POWER_FLOAT': 1109, 'IMAGETEXTURE_MODE': 1105,
    'IMAGETEX_COLOR_SPACE': 1118, 'IMAGETEXTURE_PROJECTION_LINK': 1111,
    'IMAGETEXTURE_TRANSFORM_LINK': 1110, 'IMAGETEX_BORDER_MODE': 1112,
    'IMAGETEX_BORDER_MODE_V': 1122,
    'TEX_PROJECTION_TYPE': 1360, 'UVW_SET': 1388,
    'TRANSFORM_TYPE': 1264, 'TRANSFORM_SX': 1269, 'TRANSFORM_SY': 1270,
    'TRANSFORM_SZ': 1271, 'TRANSFORM_SCL_LOCK': 1275,
    'TRANSFORM_TX': 1272, 'TRANSFORM_TY': 1273, 'TRANSFORM_TZ': 1274,
    'TEXEMISSION_EFFIC_OR_TEX': 2170, 'TEXEMISSION_POWER': 2171,
    'TEXEMISSION_SURFACE_BRIGHTNESS': 2175, 'TEXEMISSION_DOUBLE_SIDED': 2180,
    'RGBSPECTRUMSHADER_COLOR': 1200,
    'MULTIPLY_TEXTURE1': 1140, 'MULTIPLY_TEXTURE2': 1141,
}


def _api(c4d_module=None):
    if c4d_module is not None: return c4d_module
    try:
        return importlib.import_module('c4d')
    except ImportError as error:
        raise RuntimeError('Octane materials must be created inside Cinema 4D with Octane installed.') from error


def require_octane(c4d_module=None):
    """Fail clearly if the launched Cinema 4D process did not load Octane."""
    api = _api(c4d_module)
    if not api.plugins.FindPlugin(MATERIAL_PLUGIN, api.PLUGINTYPE_MATERIAL) or not api.plugins.FindPlugin(IMAGE_PLUGIN, api.PLUGINTYPE_SHADER):
        raise RuntimeError('Octane for Cinema 4D is not loaded. Install/enable Octane for the selected Cinema 4D version, then retry the Octane export.')
    return api


def configure_octane_renderer(doc, *, c4d_module=None):
    """Select Octane in this export document without starting a render."""
    api = require_octane(c4d_module)
    settings = doc.GetActiveRenderData()
    if settings is None: raise RuntimeError('The Cinema 4D export document has no render settings.')
    video = settings.GetFirstVideoPost()
    while video is not None and video.GetType() != RENDERER_PLUGIN:
        video = video.GetNext()
    if video is None:
        video = api.documents.BaseVideoPost(RENDERER_PLUGIN)
        if video is None: raise RuntimeError('The Octane renderer could not be initialized in the selected Cinema 4D installation.')
        settings.InsertVideoPost(video)
    video.DelBit(api.BIT_VPDISABLED)
    settings[api.RDATA_RENDERENGINE] = RENDERER_PLUGIN
    return video


def _number(value, default=0., low=0., high=None):
    try: value = float(value)
    except (TypeError, ValueError): value = default
    if not math.isfinite(value): value = default
    value = max(low, value)
    return min(high, value) if high is not None else value


def _color(value, default=(1., 1., 1.)):
    if not isinstance(value, (tuple, list)) or len(value) < 3: value = default
    return tuple(_number(v) for v in value[:3])


def _relative_texture(mapping, texture_root):
    text = str(mapping.get('relativePath', '')).replace('\\', '/')
    path = PurePosixPath(text)
    if (not text or path.is_absolute() or PureWindowsPath(text).drive
            or len(path.parts) != 2 or path.parts[0] != 'tex' or '..' in path.parts):
        raise ValueError('Octane export requires baked texture paths of the form tex/filename.png.')
    if texture_root is not None and not (Path(texture_root) / Path(*path.parts)).is_file():
        raise FileNotFoundError(f'Octane export texture is missing: {text}')
    return path.as_posix()


class _Builder:
    def __init__(self, api, material, texture_root):
        self.api, self.material, self.texture_root = api, material, texture_root

    def set(self, node, name, value):
        node[getattr(self.api, name, PARAMETERS[name])] = value

    def shader(self, plugin, name):
        shader = self.api.BaseShader(plugin)
        if shader is None:
            raise RuntimeError(f'Octane shader {plugin} could not be created. Check the selected Cinema 4D installation and Octane plugin.')
        shader.SetName(name)
        self.material.InsertShader(shader)
        return shader

    def rgb(self, name, color):
        shader = self.shader(RGB_PLUGIN, name)
        self.set(shader, 'RGBSPECTRUMSHADER_COLOR', self.api.Vector(*color))
        return shader

    def multiply(self, name, first, second):
        shader = self.shader(MULTIPLY_PLUGIN, name)
        self.set(shader, 'MULTIPLY_TEXTURE1', first)
        self.set(shader, 'MULTIPLY_TEXTURE2', second)
        return shader

    def image(self, role, mapping, power=1.):
        relative = _relative_texture(mapping, self.texture_root)
        uv = int(mapping.get('uvIndex', 0))
        if not 0 <= uv <= 2: raise ValueError(f'Octane supports UV sets 1–3; {role} requests set {uv + 1}.')
        repeat = mapping.get('repeat', [1., 1.])
        if not isinstance(repeat, (tuple, list)) or len(repeat) != 2: raise ValueError(f'Invalid UV repeat for {role}.')
        repeat = [float(v) for v in repeat]
        if any(not math.isfinite(v) or abs(v) < 1e-8 for v in repeat): raise ValueError(f'Invalid UV repeat for {role}.')
        shader = self.shader(IMAGE_PLUGIN, role)
        self.set(shader, 'IMAGETEXTURE_FILE', relative)
        self.set(shader, 'IMAGETEXTURE_POWER_FLOAT', power)
        color_role = role in ('baseColor', 'emissive')
        srgb = color_role and mapping.get('colorSpace', 'srgb') == 'srgb'
        # Octane's sRGB option applies the exact transfer curve. Informational
        # maps use Non-Color Data; normal maps must remain three-channel RGB.
        self.set(shader, 'IMAGETEX_COLOR_SPACE', 1 if srgb else 2 if color_role else 0)
        self.set(shader, 'IMAGETEXTURE_GAMMA', 2.2 if srgb else 1.)
        self.set(shader, 'IMAGETEXTURE_MODE', 0 if color_role or role == 'normal' else 1)
        self.set(shader, 'IMAGETEX_BORDER_MODE', 0)
        self.set(shader, 'IMAGETEX_BORDER_MODE_V', 0)
        projection = self.shader(PROJECTION_PLUGIN, f'{role} · UV{uv + 1}')
        self.set(projection, 'TEX_PROJECTION_TYPE', 2)  # Mesh UV.
        self.set(projection, 'UVW_SET', uv + 1)  # Octane uses one-based UV indices.
        self.set(shader, 'IMAGETEXTURE_PROJECTION_LINK', projection)
        if repeat != [1., 1.]:
            transform = self.shader(TRANSFORM_PLUGIN, f'{role} · repeat')
            self.set(transform, 'TRANSFORM_TYPE', 0)  # 2D transformation.
            self.set(transform, 'TRANSFORM_SCL_LOCK', False)
            # Octane transforms texture size, while the source stores repeats.
            for name, value in [('TRANSFORM_SX', 1./repeat[0]), ('TRANSFORM_SY', 1./repeat[1]), ('TRANSFORM_SZ', 1.),
                                ('TRANSFORM_TX', 0.), ('TRANSFORM_TY', 0.), ('TRANSFORM_TZ', 0.)]: self.set(transform, name, value)
            self.set(shader, 'IMAGETEXTURE_TRANSFORM_LINK', transform)
        return shader


def create_octane_material(doc, descriptor, name=None, *, texture_root=None, warnings=None, c4d_module=None):
    """Create and insert one Universal material from a baked PBR descriptor.

    texture_root is the directory containing the exported tex folder. Shader
    paths stay relative to the saved .c4d file. The caller assigns texture tags
    and must preserve UV-tag order (UVMap first, UVMap2 second).
    """
    api = require_octane(c4d_module)
    material = api.BaseMaterial(MATERIAL_PLUGIN)
    if material is None: raise RuntimeError('Octane Universal material creation failed.')
    descriptor = descriptor or {}
    label = str(name or descriptor.get('name') or 'Avolition material')
    material.SetName(label)
    builder = _Builder(api, material, texture_root)
    put = lambda key, value: builder.set(material, key, value)
    maps = descriptor.get('maps', {})
    put('OCT_MATERIAL_TYPE', UNIVERSAL_TYPE)
    put('OCT_MATERIAL_SMOOTH', True)
    put('OCT_MAT_BRDF_MODEL', 2)  # GGX.
    put('OCT_MAT_METALLIC_MODE', 0)  # Artistic metal / standard metallic workflow.
    put('OCT_DIELECTRIC_INDEX', 1.5)
    put('OCT_MATERIAL_SPECULAR_COLOR', api.Vector(1., 1., 1.))
    put('OCT_MATERIAL_SPECULAR_FLOAT', 1.)
    for key in ('OCT_MAT_USE_COLOR', 'OCT_MAT_USE_SPECULAR', 'OCT_MAT_USE_SPECULAR_MAP', 'OCT_MAT_USE_ROUGHNESS'): put(key, True)
    color = _color(descriptor.get('baseColor'), (.62, .62, .62))
    put('OCT_MATERIAL_DIFFUSE_COLOR', api.Vector(*color))
    put('OCT_MATERIAL_DIFFUSE_FLOAT', 1.)
    put('OCT_MATERIAL_ROUGHNESS_FLOAT', _number(descriptor.get('roughness'), .65, high=1.))
    put('OCT_MAT_SPECULAR_MAP_FLOAT', _number(descriptor.get('metalness'), 0., high=1.))
    opacity = _number(descriptor.get('opacity'), 1., high=1.)
    put('OCT_MATERIAL_OPACITY_FLOAT', opacity)
    base = builder.image('baseColor', maps['baseColor']) if 'baseColor' in maps else None
    if 'ao' in maps:
        # A different AO UV set cannot be flattened by the portable baker.
        # Octane can multiply independently projected textures directly.
        base = builder.multiply('Albedo × AO', base or builder.rgb('Albedo color', color), builder.image('ao', maps['ao']))
    if base is not None:
        put('OCT_MATERIAL_DIFFUSE_LINK', base)
        put('OCT_MATERIAL_DIFFUSE_MIX', 1.)
    for role, link, mix in [('roughness', 'OCT_MATERIAL_ROUGHNESS_LINK', 'OCT_MATERIAL_ROUGHNESS_MIX'),
                            ('metalness', 'OCT_MAT_SPECULAR_MAP_LINK', None),
                            ('normal', 'OCT_MATERIAL_NORMAL_LINK', None),
                            ('alpha', 'OCT_MATERIAL_OPACITY_LINK', 'OCT_MATERIAL_OPACITY_MIX')]:
        if role in maps:
            put(link, builder.image(role, maps[role], opacity if role == 'alpha' else 1.))
            if mix: put(mix, 1.)
    put('OCT_MAT_USE_NORMAL', 'normal' in maps)
    put('OCT_MAT_USE_OPACITY', 'alpha' in maps or opacity < 1.)
    intensity = _number(descriptor.get('emissiveIntensity'), 1. if 'emissive' in maps else 0.)
    unresolved = descriptor.get('emissiveMode') == 'baseColorMask' or 'emissive' in descriptor.get('unbakedMaps', [])
    if unresolved and intensity > 0.:
        if warnings is not None:
            warnings.append(f'{label}: emission mask uses incompatible UVs and needs manual Octane setup; emission was left off.')
        intensity = 0.
    put('OCT_MAT_USE_EMISSION', intensity > 0.)
    if intensity > 0.:
        emission = builder.shader(EMISSION_PLUGIN, 'Emission')
        source = builder.image('emissive', maps['emissive']) if 'emissive' in maps else builder.rgb('Emission color', _color(descriptor.get('emissive')))
        builder.set(emission, 'TEXEMISSION_EFFIC_OR_TEX', source)
        builder.set(emission, 'TEXEMISSION_POWER', intensity)
        builder.set(emission, 'TEXEMISSION_SURFACE_BRIGHTNESS', True)
        builder.set(emission, 'TEXEMISSION_DOUBLE_SIDED', bool(descriptor.get('doubleSided', True)))
        put('OCT_MATERIAL_EMISSION', emission)
    doc.InsertMaterial(material)
    material.Message(api.MSG_UPDATE)
    return material


def create_octane_materials(doc, materials_by_model, *, texture_root=None, warnings=None, c4d_module=None):
    """Keep each mesh binding while sharing identical native material graphs.

    Map appearances recur across thousands of placements. Compare the complete
    descriptor, including its display name, so different user-visible materials
    stay separate. The cache belongs to this document creation only.
    """
    api = require_octane(c4d_module)
    cache, result = {}, {}
    for model_id, materials in materials_by_model.items():
        bindings = result.setdefault(str(model_id), {})
        for key, descriptor in materials.items():
            signature = json.dumps(descriptor, sort_keys=True, separators=(',', ':'), allow_nan=False)
            if signature not in cache:
                cache[signature] = create_octane_material(doc, descriptor, texture_root=texture_root,
                                                        warnings=warnings, c4d_module=api)
            bindings[str(key)] = cache[signature]
    return result
