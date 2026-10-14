import copy
import tempfile
from pathlib import Path
from types import SimpleNamespace
import unittest

from owpreview.c4d_octane import (create_octane_material, create_octane_materials, configure_octane_renderer,
                                PARAMETERS, MATERIAL_PLUGIN, IMAGE_PLUGIN,
                                PROJECTION_PLUGIN, EMISSION_PLUGIN, MULTIPLY_PLUGIN, RENDERER_PLUGIN)


class Node:
    def __init__(self, plugin):
        self.plugin = plugin
        self.values = {}
        self.shaders = []
        self.name = ''

    def __setitem__(self, key, value): self.values[key] = value
    def __getitem__(self, key): return self.values[key]
    def SetName(self, name): self.name = name
    def InsertShader(self, shader): self.shaders.append(shader)
    def Message(self, message): pass
    def GetType(self): return self.plugin
    def GetNext(self): return None
    def DelBit(self, bit): self.disabled = False


class Document:
    def __init__(self): self.materials = []
    def InsertMaterial(self, material): self.materials.append(material)


def api(available=True):
    return SimpleNamespace(plugins=SimpleNamespace(FindPlugin=lambda plugin, kind: available),
                           PLUGINTYPE_MATERIAL=1, PLUGINTYPE_SHADER=2,
                           BaseMaterial=Node, BaseShader=Node,
                           Vector=lambda *values: tuple(values), MSG_UPDATE=99)


def get(node, name): return node[PARAMETERS[name]]


class OctaneMaterialTests(unittest.TestCase):
    def test_pbr_connections_color_data_and_emission(self):
        descriptor = {'name': 'Armour', 'baseColor': [1, 1, 1], 'roughness': 1,
                      'metalness': 1, 'opacity': .6, 'emissiveIntensity': 4,
                      'maps': {role: {'relativePath': f'tex/{role}.png',
                                     'colorSpace': 'srgb' if role in ('baseColor', 'emissive') else 'linear'}
                               for role in ('baseColor', 'roughness', 'metalness', 'normal', 'alpha', 'emissive')}}
        doc = Document()
        material = create_octane_material(doc, descriptor, c4d_module=api())
        self.assertEqual(doc.materials, [material])
        self.assertEqual(material.plugin, MATERIAL_PLUGIN)
        self.assertEqual(get(material, 'OCT_MATERIAL_TYPE'), 2516)
        images = {node.name: node for node in material.shaders if node.plugin == IMAGE_PLUGIN}
        self.assertEqual(len(images), 6)
        for role, node in images.items():
            self.assertEqual(get(node, 'IMAGETEXTURE_FILE'), f'tex/{role}.png')
            color = role in ('baseColor', 'emissive')
            self.assertEqual(get(node, 'IMAGETEX_COLOR_SPACE'), 1 if color else 0)
            self.assertEqual(get(node, 'IMAGETEXTURE_GAMMA'), 2.2 if color else 1.)
            self.assertEqual(get(node, 'IMAGETEXTURE_MODE'), 0 if color or role == 'normal' else 1)
            self.assertEqual(get(get(node, 'IMAGETEXTURE_PROJECTION_LINK'), 'UVW_SET'), 1)
        self.assertIs(get(material, 'OCT_MAT_SPECULAR_MAP_LINK'), images['metalness'])
        self.assertIs(get(material, 'OCT_MATERIAL_NORMAL_LINK'), images['normal'])
        self.assertEqual(get(images['alpha'], 'IMAGETEXTURE_POWER_FLOAT'), .6)
        emission = get(material, 'OCT_MATERIAL_EMISSION')
        self.assertEqual(emission.plugin, EMISSION_PLUGIN)
        self.assertIs(get(emission, 'TEXEMISSION_EFFIC_OR_TEX'), images['emissive'])
        self.assertEqual(get(emission, 'TEXEMISSION_POWER'), 4.)
        self.assertTrue(get(emission, 'TEXEMISSION_SURFACE_BRIGHTNESS'))

    def test_uv2_and_repeat_stay_on_each_image(self):
        material = create_octane_material(Document(), {'maps': {
            'normal': {'relativePath': 'tex/n.png', 'uvIndex': 1, 'repeat': [4., 2.]}}}, c4d_module=api())
        image = get(material, 'OCT_MATERIAL_NORMAL_LINK')
        projection = get(image, 'IMAGETEXTURE_PROJECTION_LINK')
        self.assertEqual(projection.plugin, PROJECTION_PLUGIN)
        self.assertEqual(get(projection, 'TEX_PROJECTION_TYPE'), 2)
        self.assertEqual(get(projection, 'UVW_SET'), 2)
        transform = get(image, 'IMAGETEXTURE_TRANSFORM_LINK')
        self.assertEqual(get(transform, 'TRANSFORM_SX'), .25)
        self.assertEqual(get(transform, 'TRANSFORM_SY'), .5)
        self.assertEqual(get(transform, 'TRANSFORM_SZ'), 1.)

    def test_separate_ao_uv_can_multiply_color(self):
        material = create_octane_material(Document(), {'maps': {
            'baseColor': {'relativePath': 'tex/c.png'},
            'ao': {'relativePath': 'tex/ao.png', 'uvIndex': 1}}}, c4d_module=api())
        multiply = get(material, 'OCT_MATERIAL_DIFFUSE_LINK')
        self.assertEqual(multiply.plugin, MULTIPLY_PLUGIN)
        base, ao = get(multiply, 'MULTIPLY_TEXTURE1'), get(multiply, 'MULTIPLY_TEXTURE2')
        self.assertEqual(get(get(base, 'IMAGETEXTURE_PROJECTION_LINK'), 'UVW_SET'), 1)
        self.assertEqual(get(get(ao, 'IMAGETEXTURE_PROJECTION_LINK'), 'UVW_SET'), 2)

    def test_unbaked_emission_mask_does_not_become_white_emission(self):
        warnings = []
        material = create_octane_material(Document(), {
            'emissiveMode': 'baseColorMask', 'emissiveIntensity': 16,
            'maps': {'emissive': {'relativePath': 'tex/mask.png'}}},
            warnings=warnings, c4d_module=api())
        self.assertFalse(get(material, 'OCT_MAT_USE_EMISSION'))
        self.assertFalse(any(shader.plugin == EMISSION_PLUGIN for shader in material.shaders))
        self.assertIn('manual Octane setup', warnings[0])

    def test_missing_octane_fails_before_material_creation(self):
        doc = Document()
        with self.assertRaisesRegex(RuntimeError, 'Octane for Cinema 4D is not loaded'):
            create_octane_material(doc, {}, c4d_module=api(False))
        self.assertEqual(doc.materials, [])

    def test_invalid_paths_or_missing_baked_image_fail_before_insert(self):
        for relative in ('C:/old/c.png', '../c.png', 'tex/../../c.png', '//server/share/c.png', ''):
            doc = Document()
            with self.assertRaises(ValueError):
                create_octane_material(doc, {'maps': {'baseColor': {'relativePath': relative}}}, c4d_module=api())
            self.assertEqual(doc.materials, [])
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(FileNotFoundError):
                create_octane_material(Document(), {'maps': {'baseColor': {'relativePath': 'tex/missing.png'}}},
                                       texture_root=folder, c4d_module=api())
            (Path(folder) / 'tex').mkdir()
            (Path(folder) / 'tex/exists.png').write_bytes(b'test')
            material = create_octane_material(Document(), {'maps': {'baseColor': {'relativePath': 'tex/exists.png'}}},
                                              texture_root=folder, c4d_module=api())
            self.assertEqual(get(get(material, 'OCT_MATERIAL_DIFFUSE_LINK'), 'IMAGETEXTURE_FILE'), 'tex/exists.png')

    def test_batch_preserves_mesh_assignment_keys(self):
        result = create_octane_materials(Document(), {'model-0': {'123': {'name': 'Body'}}}, c4d_module=api())
        self.assertEqual(result['model-0']['123'].name, 'Body')

    def test_batch_reuses_identical_materials_across_models_and_slots(self):
        descriptor = {'name': 'Brick', 'roughness': .8, 'maps': {
            'baseColor': {'relativePath': 'tex/brick.png', 'repeat': [2, 3], 'uvIndex': 1}}}
        # Canonical equality must survive independent dictionaries and key order.
        reordered = {'maps': {'baseColor': {'uvIndex': 1, 'repeat': [2, 3],
                                           'relativePath': 'tex/brick.png'}},
                     'roughness': .8, 'name': 'Brick'}
        inputs = {'model-a': {'100': descriptor, '200': copy.deepcopy(descriptor)},
                  'model-b': {'300': reordered}}
        before = copy.deepcopy(inputs)
        doc = Document()
        bindings = create_octane_materials(doc, inputs, c4d_module=api())
        self.assertEqual(inputs, before)
        self.assertEqual(len(doc.materials), 1)
        material = doc.materials[0]
        self.assertIs(bindings['model-a']['100'], material)
        self.assertIs(bindings['model-a']['200'], material)
        self.assertIs(bindings['model-b']['300'], material)
        self.assertEqual(len([shader for shader in material.shaders if shader.plugin == IMAGE_PLUGIN]), 1)
        # No native object can be shared across separately created documents.
        second = Document()
        following = create_octane_materials(second, inputs, c4d_module=api())
        self.assertIsNot(following['model-b']['300'], material)
        self.assertEqual(len(second.materials), 1)

    def test_material_name_controls_and_uv_changes_stay_distinct(self):
        descriptor = {'name': 'Brick', 'roughness': .8, 'maps': {
            'baseColor': {'relativePath': 'tex/brick.png', 'uvIndex': 0}}}
        renamed = dict(descriptor, name='Named separately')
        rough = dict(descriptor, roughness=.2)
        second_uv = copy.deepcopy(descriptor)
        second_uv['maps']['baseColor']['uvIndex'] = 1
        doc = Document()
        bindings = create_octane_materials(doc, {'model': {
            str(index): value for index, value in enumerate([descriptor, renamed, rough, second_uv])}},
            c4d_module=api())
        self.assertEqual(len(doc.materials), 4)
        self.assertEqual(len({id(value) for value in bindings['model'].values()}), 4)
        self.assertEqual(bindings['model']['1'].name, 'Named separately')
        self.assertEqual(get(bindings['model']['2'], 'OCT_MATERIAL_ROUGHNESS_FLOAT'), .2)
        image = get(bindings['model']['3'], 'OCT_MATERIAL_DIFFUSE_LINK')
        self.assertEqual(get(get(image, 'IMAGETEXTURE_PROJECTION_LINK'), 'UVW_SET'), 2)

    def test_shared_material_reports_its_shader_limitation_once(self):
        descriptor = {'name': 'Masked light', 'emissiveMode': 'baseColorMask', 'emissiveIntensity': 16,
                      'maps': {'emissive': {'relativePath': 'tex/mask.png'}}}
        warnings = []
        doc = Document()
        create_octane_materials(doc, {'first': {'A': descriptor}, 'second': {'B': copy.deepcopy(descriptor)}},
                                warnings=warnings, c4d_module=api())
        self.assertEqual(len(doc.materials), 1)
        self.assertEqual(len(warnings), 1)
        self.assertIn('manual Octane setup', warnings[0])

    def test_renderer_is_added_once_and_enabled(self):
        settings = Node(0)
        settings.post = None
        settings.GetFirstVideoPost = lambda: settings.post
        settings.InsertVideoPost = lambda node: setattr(settings, 'post', node)
        doc = SimpleNamespace(GetActiveRenderData=lambda: settings)
        c4d = api()
        c4d.documents = SimpleNamespace(BaseVideoPost=Node)
        c4d.BIT_VPDISABLED = 1
        c4d.RDATA_RENDERENGINE = 3000
        first = configure_octane_renderer(doc, c4d_module=c4d)
        second = configure_octane_renderer(doc, c4d_module=c4d)
        self.assertIs(first, second)
        self.assertEqual(settings[3000], RENDERER_PLUGIN)
        self.assertFalse(first.disabled)


if __name__ == '__main__': unittest.main()
