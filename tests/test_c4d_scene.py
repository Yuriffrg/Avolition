"""Native scene contract tests; the fake API does not replace real C4D QA."""
import copy
import math
import struct
from pathlib import Path
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from owpreview.c4d_scene import build_scene, _skin, _matrix_values, _matrix, _ordered_graph, _check_poses, IDENTITY


class Vec(tuple):
    def __new__(cls, x=0, y=0, z=0): return tuple.__new__(cls, (x, y, z))
    def __getnewargs__(self): return (self.x,self.y,self.z)
    def __iter__(self): raise TypeError("'c4d.Vector' object is not iterable")
    x = property(lambda s: s[0]); y = property(lambda s: s[1]); z = property(lambda s: s[2])


class Matrix:
    def __init__(self, off=None, v1=None, v2=None, v3=None):
        self.off = off or Vec(); self.v1 = v1 or Vec(1, 0, 0)
        self.v2 = v2 or Vec(0, 1, 0); self.v3 = v3 or Vec(0, 0, 1)


class Node:
    def __init__(self, kind=0):
        self.kind, self.name = kind, ''
        self.values, self.tags, self.children, self.tracks, self.shaders = {'ID_BASEOBJECT_ROTATION_ORDER': 0}, [], [], [], []
        self.parent, self.next, self.matrix, self.nla = None, None, Matrix(), None
        self.motion = Node() if kind == 465003001 else None
        if kind == 'Tmotionsystem': self.nla=Node()
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
    def SetMg(self, matrix): self.matrix = matrix
    def GetMg(self): return self.matrix  # Fixture roots are identity.
    def GetRelPos(self): return self.matrix.off
    def GetRelRot(self): return Vec()  # Rotation conversion belongs to the C4D runtime.
    def GetRelScale(self): return Vec(*(math.sqrt(sum(v[i]*v[i] for i in range(3))) for v in (self.matrix.v1, self.matrix.v2, self.matrix.v3)))
    def InsertTag(self, tag, pred=None):
        self.tags.insert(self.tags.index(pred)+1 if pred else 0, tag); tag.parent = self
        for i, value in enumerate(self.tags): value.next = self.tags[i+1] if i+1 < len(self.tags) else None
    def InsertTrackSorted(self, track):
        if self.tracks: self.tracks[-1].next = track
        self.tracks.append(track); track.parent = self
    def GetFirstCTrack(self): return self.tracks[0] if self.tracks else None
    def InsertShader(self, shader):
        if self.shaders: self.shaders[-1].next = shader
        self.shaders.append(shader)
    def Message(self, _): pass
    def DelBit(self, _): pass
    def GetNLARoot(self, create=True):
        if self.nla is None and create: self.nla=Node()
        return self.nla
    def GetBranchInfo(self, flags=0):
        heads=([self.motion] if self.motion is not None else []) + ([self.nla] if self.nla is not None else [])
        return [{'id':'NLAbase','head':head} for head in heads]
    def GetClone(self, flags):
        result=copy.deepcopy(self); result.parent=None; result.next=None
        if flags & 4: result.children=[]
        if flags & 128: result.tracks=[]; result.tags=[]; result.nla=None
        return result
    def Remove(self):
        parent=self.parent
        for items in (parent.tracks,parent.tags,parent.children):
            if self in items:
                items.remove(self)
                for index,item in enumerate(items): item.next=items[index+1] if index+1<len(items) else None
                break
        self.parent=None;self.next=None


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
    def GetDescriptionID(self): return self.desc


class DescLevel(tuple):
    def __new__(cls,*args): return tuple.__new__(cls,args)
    def __getnewargs__(self): return tuple(self)
    id=property(lambda self:self[0])


class DescID(DescLevel):
    def GetDepth(self): return len(self)


class BaseTime(float):
    def __new__(cls,value=0,fps=None):
        # Native seconds construction loses sub-millisecond precision. The
        # two-argument frame/fps form retains the exact frame fraction.
        numerator, denominator = (int(value*1000),1000) if fps is None else (value,fps)
        result=float.__new__(cls,numerator/denominator)
        result.numerator,result.denominator=numerator,denominator
        return result
    def __getnewargs__(self): return self.numerator,self.denominator
    def Get(self): return float(self)
    def GetNumerator(self): return self.numerator
    def GetDenominator(self): return self.denominator


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
    def GetFps(self): return self.fps
    def SetMinTime(self, value): self.minimum=value
    def GetMinTime(self): return self.minimum
    def SetMaxTime(self, value): self.max = value
    def GetMaxTime(self): return self.max
    def SetLoopMinTime(self, value): self.loopmin=value
    def GetLoopMinTime(self): return self.loopmin
    def SetLoopMaxTime(self, value): self.loopmax=value
    def GetLoopMaxTime(self): return self.loopmax
    def SetTime(self, value): self.time = value
    def GetTime(self): return self.time
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
        def evaluate(obj, tracks):
            positions=[obj.matrix.off[i] for i in range(3)]
            for track in tracks:
                if track.desc[0].id=='ID_BASEOBJECT_REL_POSITION':
                    key=min(track.keys,key=lambda key:abs(key.time-self.time))
                    positions[track.desc[1].id]=key.value
            obj.matrix.off=Vec(*positions)
        def update(obj):
            evaluate(obj,obj.tracks)
            for child in obj.children: update(child)
        def apply(source,target):
            evaluate(target,source.tracks)
            for child in source.children:
                match=next(obj for obj in target.children if obj.name==child.name)
                apply(child,match)
        def motion(obj):
            for tag in obj.tags:
                if tag.kind=='Tmotionsystem' and tag.nla:
                    for layer in tag.nla.children:
                        for clip in layer.motion.children:
                            apply(clip.values['ID_MT_CLIP_SOURCE'],obj)
            for child in obj.children: motion(child)
        for obj in self.roots:
            update(obj)
            motion(obj)


def make_api():
    api = ModuleType('c4d')
    names = ('MSG_UPDATE NOTOK DTYPE_VECTOR DTYPE_REAL ID_BASEOBJECT_REL_POSITION ID_BASEOBJECT_REL_ROTATION ID_BASEOBJECT_ROTATION_ORDER '
             'ID_BASEOBJECT_REL_SCALE CINTERPOLATION_LINEAR DOCUMENT_DOCUNIT DOCUMENT_UNIT_CM '
             'TEXTURETAG_MATERIAL TEXTURETAG_PROJECTION TEXTURETAG_PROJECTION_UVW BUILDFLAGS_NONE '
             'SAVEDOCUMENTFLAGS_DONTADDTORECENTLIST FORMAT_C4DEXPORT PLUGINTYPE_MATERIAL PLUGINTYPE_SHADER '
             'RDATA_RENDERENGINE BIT_VPDISABLED Tmotionsystem Omotionclip CTpla '
             'ID_BASEOBJECT_ABS_SCALE ID_BASEOBJECT_FROZEN_SCALE ID_MT_LAYER_VALUE ID_MT_LAYER_MODE '
             'ID_MT_LAYER_MODE_MIX ID_MT_CLIP_SOURCE ID_MT_CLIP_START ID_MT_CLIP_END ID_MT_CLIP_TIMWARPWND '
             'GETBRANCHINFO_NONE NLAbase').split()
    for name in names: setattr(api, name, name)
    api.VECTOR_X, api.VECTOR_Y, api.VECTOR_Z = 0, 1, 2
    api.ROTATIONORDER_DEFAULT = 0
    api.Onull, api.Opolygon, api.Ojoint, api.Oskin = 1, 2, 3, 4
    api.SCENEFILTER_OBJECTS, api.SCENEFILTER_MATERIALS = 1, 2
    api.Vector, api.Matrix, api.BaseObject, api.BaseMaterial, api.BaseShader = Vec, Matrix, Node, Node, Node
    api.PolygonObject, api.CPolygon = Polygon, lambda *a: a
    api.UVWTag, api.NormalTag, api.TextureTag = DataTag, DataTag, Node
    api.BaseTag,api.BaseList2D=Node,Node
    api.COPYFLAGS_NONE,api.COPYFLAGS_NO_HIERARCHY,api.COPYFLAGS_NO_BRANCHES=0,4,128
    api.DescID, api.DescLevel, api.CTrack, api.BaseTime = DescID, DescLevel, Track, BaseTime
    class SplineData:
        def MakeLinearSplineLinear(self, count):
            self.count = count
            return True
    api.SplineData = SplineData
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
    def test_animation_project_keys_source_and_clip_keep_whole_source_frames(self):
        from owpreview.c4d_motion import SOURCE_END
        for fps in (30,60):
            for motion in (False,True):
                with self.subTest(fps=fps,motion=motion):
                    api,payload=make_api(),fixture()
                    payload.update(name='Reload',motionClip=True if motion else False)
                    payload['animation'].update(fps=fps,lastFrame=49)
                    payload['animation']['tracks'][0]['frames']=[IDENTITY.copy() for _ in range(50)]
                    with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
                        report=build_scene(payload,Path(temp)/'reload.c4d')
                    doc=api.saved[0]
                    self.assertEqual(doc.GetFps(),fps)
                    self.assertEqual(doc.GetMaxTime().Get(),49/fps)
                    self.assertEqual(doc.GetLoopMaxTime().Get(),49/fps)
                    bone=doc.nla.children[0].children[0] if motion else doc.roots[0].children[0].children[0].children[0]
                    for track in bone.tracks:
                        self.assertEqual([key.time.Get() for key in track.keys],[frame/fps for frame in range(50)])
                    if motion:
                        self.assertEqual(doc.nla.children[0][SOURCE_END].Get(),49/fps)
                        clip=doc.roots[0].children[0].children[0].tags[0].nla.children[0].motion.children[0]
                        self.assertEqual(clip[api.ID_MT_CLIP_END].Get(),49/fps)
                        self.assertEqual(clip[api.ID_MT_CLIP_TIMWARPWND].count, 2)
                    self.assertTrue(report['verifiedNativeScene'])

    def test_pose_check_accepts_native_translation_cancellation_without_relaxing_basis(self):
        before=[0,600,-650, 0,1,0, -1,0,0, 0,0,1]
        rounded=before.copy();rounded[:3]=[-.0002408268,600.0002408268,-650.0003612403]
        _check_poses([[before]],[[rounded]],'pose differs')
        for index,value in ((0,.1),(4,1.0001),(11,float('nan'))):
            with self.subTest(index=index):
                changed=before.copy();changed[index]=value
                with self.assertRaisesRegex(RuntimeError,'pose differs'):
                    _check_poses([[before]],[[changed]],'pose differs')
        origin=before.copy();origin[:3]=[0,0,0]
        changed=origin.copy();changed[0]=.0002
        with self.assertRaisesRegex(RuntimeError,'pose differs'):
            _check_poses([[origin]],[[changed]],'pose differs')

    def test_motion_clip_rejects_failed_linear_timing_creation(self):
        api, payload = make_api(), fixture()
        payload['motionClip'] = True
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {
                'c4d': api, 'c4d.modules': api.modules}), patch.object(
                api.SplineData, 'MakeLinearSplineLinear', return_value=False):
            with self.assertRaisesRegex(RuntimeError, 'Motion Clip timing spline'):
                build_scene(payload, Path(temp) / 'scene.c4d')
        self.assertEqual(api.saved, [])
        self.assertEqual(len(api.killed), 1)

    def test_motion_clip_belongs_to_renamed_skeleton_with_no_scale_keys(self):
        from owpreview.c4d_motion import motion_snapshot
        api,payload=make_api(),fixture()
        payload.update(name='Gameplay1P_ADS UP',motionClipName='ADS UP',motionClip=True)
        payload['asset']['exportHierarchy'][2]['name']='Custom rig'
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            result=build_scene(payload,Path(temp)/'scene.c4d')
        self.assertEqual(result['motionSourceNames'],['ADS UP'])
        self.assertEqual((result['motionClipCount'],result['motionTrackCount'],result['trackCount']),(1,6,6))
        doc=api.saved[0]
        model=doc.roots[0].children[0];skeleton=model.children[0];bone=skeleton.children[0]
        self.assertEqual(bone.tracks,[])
        self.assertEqual(doc.roots[0].tags,[])
        self.assertEqual(model.tags,[])
        self.assertEqual([tag.GetType() for tag in skeleton.tags],[api.Tmotionsystem])
        self.assertEqual([obj.name for obj in doc.nla.children[0].children],['Wrist_R'])
        snapshot=motion_snapshot(api,doc,2)
        self.assertEqual(snapshot['sources'][0]['count'],6)
        self.assertEqual(snapshot['clips'][0]['name'],'ADS UP')
        self.assertEqual(snapshot['clips'][0]['source'],'ADS UP')
        self.assertEqual(snapshot['clips'][0]['layer'],'Layer 0')
        self.assertEqual(snapshot['clips'][0]['owner'],(0,0,0))
        self.assertEqual(snapshot['clips'][0]['ownerName'],'Custom rig')
        self.assertEqual(snapshot['clips'][0]['start'],0)
        self.assertAlmostEqual(snapshot['clips'][0]['end'],1/29.97)
        self.assertTrue(result['verifiedNativeScene'])

    def test_motion_source_keeps_parameter_keys_but_excludes_scale_and_pla(self):
        from owpreview.c4d_motion import create_motion_clips
        api=make_api();doc=Document();root=Node(api.Onull);root.SetName('Rig');doc.InsertObject(root)
        for kind,param in [(0,api.ID_BASEOBJECT_REL_POSITION),(0,api.ID_BASEOBJECT_REL_ROTATION),
                           (0,'USER_PARAMETER'),(0,api.ID_BASEOBJECT_REL_SCALE),(api.CTpla,'PLA')]:
            track=Track(root,DescID(DescLevel(param)));track.kind=kind;root.InsertTrackSorted(track)
        result=create_motion_clips(api,doc,'Named action',1.0,[(root,[])])
        self.assertEqual(result['motionTrackCount'],3)
        source=doc.nla.children[0]
        self.assertEqual([t.desc[0].id for t in source.tracks],
                         [api.ID_BASEOBJECT_REL_POSITION,api.ID_BASEOBJECT_REL_ROTATION,'USER_PARAMETER'])
        self.assertEqual([t.desc[0].id for t in root.tracks],[api.ID_BASEOBJECT_REL_SCALE,'PLA'])
        self.assertEqual(source.tags,[],'Expression tags and pictures must not be baked or copied.')

    def test_reopened_motion_clip_must_keep_source_link(self):
        api,payload=make_api(),fixture();payload.update(name='ADS UP',motionClip=True)
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]);doc.roots[0].children[0].children[0].tags[0].nla.children[0].motion.children[0][api.ID_MT_CLIP_SOURCE]=None
            return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'lost its Motion Source'):
                build_scene(payload,Path(temp)/'scene.c4d')

    def test_reopened_motion_source_must_preserve_animation(self):
        api,payload=make_api(),fixture();payload.update(name='ADS UP',motionClip=True)
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]);bone=doc.nla.children[0].children[0]
            bone.tracks[0].keys[-1].value+=50
            return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'sampled animated joint transforms'):
                build_scene(payload,Path(temp)/'scene.c4d')

    def test_motion_tag_moved_to_entity_on_reload_is_rejected(self):
        api,payload=make_api(),fixture();payload.update(name='ADS UP',motionClip=True)
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]);skeleton=doc.roots[0].children[0].children[0]
            tag=skeleton.tags[0];tag.Remove();doc.roots[0].InsertTag(tag)
            return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'changed the Motion Clip'):
                build_scene(payload,Path(temp)/'scene.c4d')

    def test_missing_skeleton_cannot_fall_back_to_entity_motion_tag(self):
        api,payload=make_api(),fixture();payload.update(name='ADS UP',motionClip=True)
        graph=payload['asset']['exportHierarchy']
        graph[:]=[node for node in graph if node['type']!='skeleton']
        next(node for node in graph if node['type']=='bone')['parentId']='model'
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(ValueError,'no skeleton root'):
                build_scene(payload,Path(temp)/'scene.c4d')
        self.assertEqual(api.saved,[])

    def test_scale_tracks_on_reloaded_scene_or_source_are_rejected(self):
        for source in (False,True):
            with self.subTest(source=source):
                api,payload=make_api(),fixture();payload.update(name='ADS UP',motionClip=True)
                def load(*args):
                    doc=copy.deepcopy(api.saved[-1])
                    bone=doc.nla.children[0].children[0] if source else doc.roots[0].children[0].children[0].children[0]
                    track=Track(bone,DescID(DescLevel(api.ID_BASEOBJECT_REL_SCALE),DescLevel(api.VECTOR_X)))
                    for frame in (0,1):track.AddKey(BaseTime(frame/29.97))['key'].value=1
                    bone.InsertTrackSorted(track)
                    return doc
                api.documents.LoadDocument=load
                with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
                    with self.assertRaisesRegex(RuntimeError,'scale animation tracks|included animation keys'):
                        build_scene(payload,Path(temp)/'scene.c4d')

    def test_first_frame_nonunit_scale_stays_static_without_scale_tracks(self):
        for varying in (False,True):
            for motion in (False,True):
                with self.subTest(varying=varying,motion=motion):
                    api,payload=make_api(),fixture();payload['motionClip']=motion
                    frames=payload['animation']['tracks'][0]['frames']
                    for index,matrix in enumerate(frames):
                        matrix[0],matrix[5],matrix[10]=(4,5,6) if varying and index else (2,3,4)
                    with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
                        report=build_scene(payload,Path(temp)/'scene.c4d')
                    doc=api.saved[0];bone=doc.roots[0].children[0].children[0].children[0]
                    for frame in (0,1):
                        doc.SetTime(BaseTime(frame/29.97));doc.ExecutePasses()
                        self.assertEqual(bone.GetRelScale(),(2,3,4))
                    self.assertEqual(report['trackCount'],6)
                    self.assertEqual(any('animated scale was omitted' in warning for warning in report['warnings']),varying)
                    if motion:self.assertEqual(doc.nla.children[0].children[0].GetRelScale(),(2,3,4))

    def test_numerical_scale_noise_does_not_warn(self):
        api,payload=make_api(),fixture()
        payload['animation']['tracks'][0]['frames'][1][0]+=1e-7
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'scene.c4d')
        self.assertFalse(any('animated scale was omitted' in warning for warning in report['warnings']))

    def test_static_map_range_starts_at_zero_and_has_one_frame(self):
        api,payload=make_api(),fixture()
        payload['animation']={'fps':24,'lastFrame':0,'tracks':[]}
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'map.c4d')
        self.assertEqual(report['timeline'],dict(fps=24,minimum=0,maximum=1/24,
                                               previewMinimum=0,previewMaximum=1/24,current=0))
        self.assertNotIn('motionClipCount',report)

    def test_reopened_scene_must_keep_project_timing(self):
        api,payload=make_api(),fixture()
        def load(*args):
            doc=copy.deepcopy(api.saved[-1]);doc.SetMinTime(BaseTime(-1/30));return doc
        api.documents.LoadDocument=load
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            with self.assertRaisesRegex(RuntimeError,'project time or frame range'):
                build_scene(payload,Path(temp)/'scene.c4d')

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
                for axis, wanted in enumerate(expected_corner):
                    self.assertAlmostEqual(actual_corner[axis], wanted)
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
                self.assertEqual(result['trackCount'], 6)

    def test_native_weight_quantization_preserves_normalized_totals(self):
        api = make_api()
        joints = [Node() for _ in range(4)]
        obj = Node()
        mesh = dict(name='Skin', positions=[0]*15, skinIndices=[
            2, 1, 0, 3,  3, 1, 3, 0,  3, 1, 0, 2,  0, 1, 2, 3,  2, 1, 0, 3],
            skinWeights=[.5, .5, 0, 0,  .2, .2, .1, 0,  .03, 0, 0, 0,
                         128/255, 109/255, 14/255, 4/255,  1e-12, 1, 0, 0])
        self.assertTrue(_skin(api, api.modules.character, obj, mesh, joints, actor_weights=True))
        tag = obj.tags[0]

        def stored(joint, point):
            # Native SetWeightMap reduces float32 to an unsigned 16-bit fraction.
            number = tag.weights[joint][point]
            return int(struct.unpack('f', struct.pack('f', number))[0] * 65535)

        actual = [[stored(j, p) for j in range(4)] for p in range(5)]
        self.assertEqual(actual, [[0, 32768, 32767, 0], [0, 26214, 0, 39321],
                                  [0, 0, 0, 65535], [128*257, 109*257, 14*257, 4*257],
                                  [0, 65535, 0, 0]])
        self.assertTrue(all(sum(values) == 65535 for values in actual))
        for values, message in (([0]*4, 'no valid skin influence'),
                                ([float('nan'), 1, 0, 0], 'invalid skin influence'),
                                ([.2]*5, 'invalid skin array length')):
            with self.subTest(values=values), self.assertRaisesRegex(ValueError, message):
                _skin(api, api.modules.character, Node(), dict(mesh, positions=[0]*3,
                      skinIndices=[0]*len(values), skinWeights=values), joints, actor_weights=True)

    def test_native_graph_skin_bind_rest_materials_keys_and_detached_save(self):
        api = make_api()
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d':api,'c4d.modules':api.modules}):
            result = build_scene(fixture(), Path(temp)/'scene.c4d')
        self.assertEqual({k:result[k] for k in ('meshCount','boneCount','skinCount','materialCount','trackCount')},
                         dict(meshCount=1,boneCount=1,skinCount=1,materialCount=1,trackCount=6))
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
        self.assertEqual(result['boneCount'],1); self.assertEqual(result['trackCount'],6)

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
