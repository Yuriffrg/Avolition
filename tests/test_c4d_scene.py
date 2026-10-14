"""Native scene contract tests; the fake API does not replace real C4D QA."""
import copy
import math
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from owpreview.c4d_scene import build_scene, _matrix_values, _matrix, _ordered_graph, IDENTITY


class Vec(tuple):
    def __new__(cls, x=0, y=0, z=0): return tuple.__new__(cls, (x, y, z))
    def __getnewargs__(self): return tuple(self)
    x = property(lambda s: s[0]); y = property(lambda s: s[1]); z = property(lambda s: s[2])


class Matrix:
    def __init__(self, off=None, v1=None, v2=None, v3=None):
        self.off = off or Vec(); self.v1 = v1 or Vec(1, 0, 0)
        self.v2 = v2 or Vec(0, 1, 0); self.v3 = v3 or Vec(0, 0, 1)


class Node:
    def __init__(self, kind=0):
        self.kind, self.name = kind, ''
        self.values, self.tags, self.children, self.tracks, self.shaders = {'ID_BASEOBJECT_ROTATION_ORDER': 0}, [], [], [], []
        self.parent, self.next, self.matrix = None, None, Matrix()
    def __getitem__(self, key): return self.values[key]
    def __setitem__(self, key, value): self.values[key] = value
    def GetName(self): return self.name
    def SetName(self, name): self.name = name
    def GetNext(self): return self.next
    def GetDown(self): return self.children[0] if self.children else None
    def GetUp(self): return self.parent
    def GetFirstTag(self): return self.tags[0] if self.tags else None
    def GetFirstShader(self): return self.shaders[0] if self.shaders else None
    def CheckType(self, kind): return self.kind == kind
    def GetType(self): return self.kind
    def InsertUnderLast(self, parent):
        if parent.children: parent.children[-1].next = self
        parent.children.append(self); self.parent = parent
    def SetMl(self, matrix): self.matrix = matrix
    def GetMg(self): return self.matrix  # Fixture roots are identity.
    def GetRelPos(self): return self.matrix.off
    def GetRelRot(self): return Vec()  # Rotation conversion belongs to the C4D runtime.
    def GetRelScale(self): return Vec(*(math.sqrt(sum(x*x for x in v)) for v in (self.matrix.v1, self.matrix.v2, self.matrix.v3)))
    def InsertTag(self, tag, pred=None):
        self.tags.insert(self.tags.index(pred)+1 if pred else 0, tag); tag.parent = self
        for i, value in enumerate(self.tags): value.next = self.tags[i+1] if i+1 < len(self.tags) else None
    def InsertTrackSorted(self, track):
        if self.tracks: self.tracks[-1].next = track
        self.tracks.append(track)
    def GetFirstCTrack(self): return self.tracks[0] if self.tracks else None
    def InsertShader(self, shader):
        if self.shaders: self.shaders[-1].next = shader
        self.shaders.append(shader)
    def Message(self, _): pass
    def DelBit(self, _): pass


class Polygon(Node):
    def __init__(self, points, polygons):
        super().__init__(2); self.points = [None]*points; self.polygons = [None]*polygons
    def SetPoint(self, i, point): self.points[i] = point
    def SetPolygon(self, i, face): self.polygons[i] = face
    def SetPhong(self, *args): pass
    def GetPointCount(self): return len(self.points)
    def GetPolygonCount(self): return len(self.polygons)


class DataTag(Node):
    def __init__(self, count): super().__init__(); self.data = [None]*count
    def SetSlow(self, i, *values): self.data[i] = values
    def GetDataAddressW(self): return self.data
    @staticmethod
    def Set(handle, i, value): handle[i] = value


class WeightTag(Node):
    def __init__(self): super().__init__(); self.joints=[]; self.weights={}
    def AddJoint(self, joint): self.joints.append(joint); return len(self.joints)-1
    def GetJointCount(self): return len(self.joints)
    def GetJoint(self, index, doc): return self.joints[index]
    def SetWeightMap(self, i, values): self.weights[i] = values
    def SetGeomMg(self, matrix): self.geom = matrix
    def CalculateBoneStates(self, index): self.bind = [j.matrix for j in self.joints]
    def WeightDirty(self): pass


class Key:
    def SetValue(self, curve, value): self.value = value
    def SetInterpolation(self, curve, mode): self.mode = mode


class Track(Node):
    def __init__(self, obj, desc): super().__init__(); self.desc = desc; self.keys=[]
    def GetCurve(self): return self
    def AddKey(self, time):
        key = Key(); key.time = time; self.keys.append(key); return {'key': key}
    def GetKeyCount(self): return len(self.keys)


class Render(Node):
    def __init__(self): super().__init__(); self.video = None
    def GetFirstVideoPost(self): return self.video
    def InsertVideoPost(self, video): self.video = video


class Document(Node):
    def __init__(self):
        super().__init__(); self.roots=[]; self.materials=[]; self.render = Render()
        self.values['DOCUMENT_DOCUNIT'] = SimpleNamespace(SetUnitScale=lambda scale, unit: None)
    def SetDocumentName(self, name): self.name = name
    def SetDocumentPath(self, path): self.path = path
    def SetFps(self, fps): self.fps = fps
    def SetMinTime(self, value): pass
    def SetMaxTime(self, value): self.max = value
    def SetLoopMinTime(self, value): pass
    def SetLoopMaxTime(self, value): pass
    def SetTime(self, value): self.time = value
    def InsertObject(self, obj, pred=None):
        if self.roots: self.roots[-1].next = obj
        self.roots.append(obj)
    def GetFirstObject(self): return self.roots[0] if self.roots else None
    def GetFirstMaterial(self): return self.materials[0] if self.materials else None
    def InsertMaterial(self, mat):
        if self.materials: self.materials[-1].next = mat
        self.materials.append(mat)
    def GetActiveRenderData(self): return self.render
    def ExecutePasses(self, *args):
        def update(obj):
            if obj.tracks:
                positions=[]
                for track in obj.tracks[:3]:
                    key=min(track.keys,key=lambda key:abs(key.time-self.time))
                    positions.append(key.value)
                obj.matrix.off=Vec(*positions)
            for child in obj.children: update(child)
        for obj in self.roots: update(obj)


def make_api():
    api = ModuleType('c4d')
    names = ('MSG_UPDATE NOTOK DTYPE_VECTOR DTYPE_REAL ID_BASEOBJECT_REL_POSITION ID_BASEOBJECT_REL_ROTATION ID_BASEOBJECT_ROTATION_ORDER '
             'ID_BASEOBJECT_REL_SCALE CINTERPOLATION_LINEAR DOCUMENT_DOCUNIT DOCUMENT_UNIT_CM '
             'TEXTURETAG_MATERIAL TEXTURETAG_PROJECTION TEXTURETAG_PROJECTION_UVW BUILDFLAGS_NONE '
             'SAVEDOCUMENTFLAGS_DONTADDTORECENTLIST FORMAT_C4DEXPORT PLUGINTYPE_MATERIAL PLUGINTYPE_SHADER '
             'RDATA_RENDERENGINE BIT_VPDISABLED').split()
    for name in names: setattr(api, name, name)
    api.VECTOR_X, api.VECTOR_Y, api.VECTOR_Z = 0, 1, 2
    api.ROTATIONORDER_DEFAULT = 0
    api.Onull, api.Opolygon, api.Ojoint, api.Oskin = 1, 2, 3, 4
    api.SCENEFILTER_OBJECTS, api.SCENEFILTER_MATERIALS = 1, 2
    api.Vector, api.Matrix, api.BaseObject, api.BaseMaterial, api.BaseShader = Vec, Matrix, Node, Node, Node
    api.PolygonObject, api.CPolygon = Polygon, lambda *a: a
    api.UVWTag, api.NormalTag, api.TextureTag = DataTag, DataTag, Node
    api.DescID, api.DescLevel, api.CTrack, api.BaseTime = lambda *a: a, lambda *a: a, Track, float
    api.plugins = SimpleNamespace(FindPlugin=lambda *a: True)
    api.utils = SimpleNamespace(GetOptimalAngle=lambda old, new, order: new)
    api.modules = ModuleType('c4d.modules'); api.modules.character = SimpleNamespace(CAWeightTag=WeightTag)
    api.saved, api.killed = [], []
    def save(doc, path, flags, kind):
        api.saved.append(doc); Path(path).write_bytes(b'FAKE_C4D_API_CONTRACT'); return True
    api.documents = SimpleNamespace(BaseDocument=Document, BaseVideoPost=Node, SaveDocument=save,
                                    LoadDocument=lambda *a: copy.deepcopy(api.saved[-1]),
                                    KillDocument=lambda doc: api.killed.append(doc))
    return api


def fixture():
    mesh = dict(name='Glove', positions=[1,2,3, 0,1,0, 1,0,0], indices=[0,1,2],
                normals=[0,0,1]*3, uvs=[0,0, 1,0, 0,1], uvs2=[.2,.3, .4,.5, .6,.7],
                skinIndices=[0,0,0,0]*3, skinWeights=[1,0,0,0]*3, materialKey='A')
    graph = [dict(id='entity',type='entity',name='Gameplay1P',parentId=None,modelId='body'),
             dict(id='model',type='model',name='0001.00C',parentId='entity',modelId='body'),
             dict(id='skeleton',type='skeleton',name='0001_Skeleton',parentId='model',modelId='body'),
             dict(id='bone',type='bone',name='Wrist_R',parentId='skeleton',modelId='body',boneIndex=0),
             dict(id='mesh',type='mesh',name='Glove',parentId='model',modelId='body',meshIndex=0)]
    moved = IDENTITY.copy(); moved[12:15] = [1,2,3]
    return dict(asset={'exportHierarchy': graph, 'models':[dict(id='body', meshes=[mesh], bones=[{}])]},
                animation={'fps':29.97, 'lastFrame':1, 'tracks':[dict(modelId='body',boneIndex=0,frames=[moved,IDENTITY])]},
                materials={'body': {'A': {'baseColor':[.5,.6,.7]}}}, includeMesh=True)


class NativeSceneTests(unittest.TestCase):
    def test_uv_v_matches_entities_flip_for_both_sets_and_all_corners(self):
        api, payload = make_api(), fixture()
        source_mesh = payload['asset']['models'][0]['meshes'][0]
        # Asymmetric and tiled coordinates catch wrong-corner lookup,
        # flipping U, clamping, and applying the conversion only to UVMap1.
        source_mesh['uvs'] = [.125, .2, .375, 1.25, .875, -.5]
        source_mesh['uvs2'] = [.2, .3, .4, .5, .6, .7]
        original = copy.deepcopy(payload)
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            result = build_scene(payload, Path(temp)/'uv.c4d')
        self.assertTrue(result['verifiedNativeScene'])
        mesh = api.saved[0].roots[0].children[0].children[1]
        tags = [tag for tag in mesh.tags if tag.name.startswith('UVMap')]
        self.assertEqual([tag.name for tag in tags], ['UVMap1', 'UVMap2'])
        expected_sets = [
            [(.125, .8, 0), (.875, 1.5, 0), (.375, -.25, 0), (.375, -.25, 0)],
            [(.2, .7, 0), (.6, .3, 0), (.4, .5, 0), (.4, .5, 0)],
        ]
        for tag, expected in zip(tags, expected_sets):
            for actual_corner, expected_corner in zip(tag.data[0], expected):
                for actual, wanted in zip(actual_corner, expected_corner):
                    self.assertAlmostEqual(actual, wanted)
        self.assertEqual(payload, original, 'Native UV conversion must not mutate shared preview/FBX data.')

    def test_joint_polygon_ancestry_does_not_make_joint_a_mesh(self):
        # Cinema can report a polygon-compatible joint through CheckType while
        # its CAJointObject Python wrapper has no point/polygon accessors.
        class Joint(Node):
            def CheckType(self, kind): return kind in (2, 3)

        for include_mesh in (True, False):
            with self.subTest(include_mesh=include_mesh):
                api, payload = make_api(), fixture()
                api.BaseObject = lambda kind: Joint(kind) if kind == api.Ojoint else Node(kind)
                payload['includeMesh'] = include_mesh
                joint = api.BaseObject(api.Ojoint)
                self.assertTrue(joint.CheckType(api.Opolygon))
                self.assertFalse(hasattr(joint, 'GetPointCount'))
                with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
                    result = build_scene(payload, Path(temp)/'scene.c4d')
                self.assertTrue(result['verifiedNativeScene'])
                self.assertEqual(result['meshCount'], int(include_mesh))
                self.assertEqual(result['boneCount'], 1)
                self.assertEqual(result['skinCount'], int(include_mesh))
                self.assertEqual(result['trackCount'], 9)

    def test_native_graph_skin_bind_rest_materials_keys_and_detached_save(self):
        api = make_api()
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            result = build_scene(fixture(), Path(temp)/'scene.c4d')
        self.assertEqual({k:result[k] for k in ('meshCount','boneCount','skinCount','materialCount','trackCount')},
                         dict(meshCount=1,boneCount=1,skinCount=1,materialCount=1,trackCount=9))
        self.assertTrue(result['verifiedNativeScene'])
        doc = api.saved[0]; self.assertEqual(len(api.killed), 2)
        model = doc.roots[0].children[0]; bone = model.children[0].children[0]; mesh = model.children[1]
        self.assertEqual((doc.roots[0].name,model.name,bone.name),('Gameplay1P','0001.00C','Wrist_R'))
        self.assertEqual(bone.kind,api.Ojoint)
        self.assertEqual(mesh.points[0], (100,200,-300)); self.assertEqual(mesh.polygons[0],(0,2,1,1))
        uvs = [tag for tag in mesh.tags if tag.name.startswith('UVMap')]
        self.assertEqual([tag.name for tag in uvs],['UVMap1','UVMap2'])
        self.assertEqual(uvs[0].data[0][1],(0,0,0))
        skin = next(tag for tag in mesh.tags if isinstance(tag,WeightTag))
        self.assertEqual(skin.weights,{0:[1,1,1]}); self.assertEqual(skin.bind[0].off,(0,0,0))
        self.assertEqual(bone.matrix.off,(100,200,-300))
        self.assertEqual([t.keys[0].value for t in bone.tracks[:3]], [100,200,-300])
        self.assertAlmostEqual(bone.tracks[0].keys[1].time,1/29.97)

    def test_rejects_shear_before_creating_document(self):
        api, payload = make_api(), fixture(); payload['animation']['tracks'][0]['frames'][0][4] = .3
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(ValueError, 'sheared local joint'):
                build_scene(payload, Path(temp)/'bad.c4d')
        self.assertEqual(api.saved,[]); self.assertEqual(api.killed,[])

    def test_preserves_existing_file(self):
        api = make_api()
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            path=Path(temp)/'keep.c4d'; path.write_bytes(b'original')
            with self.assertRaises(FileExistsError): build_scene(fixture(),path)
            self.assertEqual(path.read_bytes(),b'original')

    def test_skeleton_only_skips_geometry_and_materials(self):
        api,payload = make_api(),fixture(); payload['includeMesh']=False
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            result=build_scene(payload,Path(temp)/'rig.c4d')
        self.assertEqual(result['meshCount'],0); self.assertEqual(result['materialCount'],0)
        self.assertEqual(result['boneCount'],1); self.assertEqual(result['trackCount'],9)

    def test_failed_native_save_disposes_only_detached_document(self):
        api=make_api(); api.documents.SaveDocument=lambda *a:False
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'failed to save'):
                build_scene(fixture(),Path(temp)/'failure.c4d')
        self.assertEqual(len(api.killed),1)

    def test_reopened_scene_must_preserve_hierarchy_names(self):
        api=make_api()
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]); doc.roots[0].children[0].name='Lost source name'; return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'changed hierarchy'):
                build_scene(fixture(),Path(temp)/'failure.c4d')
        self.assertEqual(len(api.killed),2)

    def test_reopened_scene_must_preserve_sampled_animation(self):
        api=make_api()
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]); bone=doc.roots[0].children[0].children[0].children[0]
            bone.tracks[0].keys[-1].value += 50
            return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'sampled animated joint transforms'):
                build_scene(fixture(),Path(temp)/'failure.c4d')
        self.assertEqual(len(api.killed),2)

    def test_invalid_hierarchy_and_transforms(self):
        graph=fixture()['asset']['exportHierarchy']; graph[0]['parentId']='bone'
        with self.assertRaisesRegex(ValueError,'cycle'): _ordered_graph(graph)
        m=IDENTITY.copy(); m[0]=0
        with self.assertRaisesRegex(ValueError,'zero scale'): _matrix_values(m)
        m=IDENTITY.copy(); m[1]=float('nan')
        with self.assertRaisesRegex(ValueError,'non-finite'): _matrix_values(m)

    def test_mirror_is_basis_conjugation_and_centimetre_translation(self):
        angle=.37; c,s=math.cos(angle),math.sin(angle)
        raw=[c,0,-s,0, 0,1,0,0, s,0,c,0, 2,3,4,1]
        converted=_matrix(make_api(),raw)
        self.assertEqual(converted.off,(200,300,-400))
        self.assertEqual(converted.v1,(c,0,s)); self.assertEqual(converted.v3,(-s,0,c))


if __name__ == '__main__': unittest.main()
