"""Gameplay1P cleanup plans and native-contract skin/world preservation tests."""
import copy
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import numpy as np

from owpreview.c4d_pov import plan_gameplay1p, optimize_gameplay1p
from owpreview.c4d_scene import build_scene, IDENTITY
from tests.test_c4d_scene import make_api, Node, Polygon, WeightTag, Matrix, Vec, Track, Key


def fixture(animated=True, mixed=False):
    names = [('bone_0001',-1,'Root'),('bone_007D',0,'Camera'),('bone_0092',1,'Camera card'),
             ('bone_0002',0,'Pelvis'),('bone_0005',3,'Spine'),('bone_0050',4,'Clavicle_L'),
             ('bone_001C',5,'Hand_L'),('unused',6,'Unused'),('bone_0035',4,'Clavicle_R'),
             ('bone_003A',8,'Hand_R'),('bone_004A',9,'Weapon_R')]
    graph = [dict(id='entity',type='entity',name='Gameplay1P',parentId=None,modelId='arms'),
             dict(id='model',type='model',name='Arms',parentId='entity',modelId='arms'),
             dict(id='skeleton',type='skeleton',name='Arms_Skeleton',parentId='model',modelId='arms')]
    for index,(source,parent,name) in enumerate(names):
        matrix=IDENTITY.copy();matrix[13]=.1
        graph.append(dict(id=f'b{index}',type='bone',name=name,sourceName=source,
                          boneIndex=index,modelId='arms',parentId=f'b{parent}' if parent>=0 else 'skeleton',matrix=matrix))
    def make_mesh(name,influences):
        return dict(name=name,positions=[0,0,0,1,0,0,0,1,0],indices=[0,1,2],
                    skinIndices=[value for index in influences for value in (index,0,0,0)],
                    skinWeights=[1,0,0,0]*3,materialKey='0')
    meshes=[make_mesh('Arms',[6,9,6]),make_mesh('Cards',[2,6,2] if mixed else [2,2,2])]
    for index,mesh in enumerate(meshes):
        graph.append(dict(id=f'mesh{index}',type='mesh',name=mesh['name'],parentId='model',
                          modelId='arms',meshIndex=index))
    # An actual attached mesh protects its unweighted Weapon_R owner; the empty
    # sibling helper does not protect any otherwise-unused engine branch.
    graph.extend([dict(id='weapon',type='entity',name='Weapon',parentId='b10',modelId='weapon'),
                  dict(id='weapon-mesh',type='mesh',name='Weapon mesh',parentId='weapon',modelId='weapon',meshIndex=0),
                  dict(id='empty',type='entity',name='Empty engine helper',parentId='entity',modelId='empty')])
    models=[dict(id='arms',bones=[{} for _ in names],meshes=meshes),
            dict(id='weapon',bones=[],meshes=[dict(name='Weapon mesh',positions=[0,0,0,1,0,0,0,1,0],indices=[0,1,2])]),
            dict(id='empty',bones=[],meshes=[])]
    frames=[]
    for frame in range(3):
        matrix=IDENTITY.copy();matrix[12]=frame*.1;matrix[13]=.1
        frames.append(matrix)
    tracks=[dict(modelId='arms',boneIndex=0,frames=frames)] if animated else []
    return dict(name='Reload',motionClipName='Reload',gameplay1P=True,motionClip=True,includeMesh=True,
                asset=dict(exportHierarchy=graph,models=models),
                animation=dict(fps=30,lastFrame=2 if animated else 0,tracks=tracks),materials={})


def array(matrix):
    result=np.eye(4)
    for column,vector in enumerate((matrix.v1,matrix.v2,matrix.v3,matrix.off)):
        result[:3,column]=[vector.x,vector.y,vector.z]
    return result


class Affine(Matrix):
    @classmethod
    def from_array(cls,value):
        return cls(off=Vec(*value[:3,3]),v1=Vec(*value[:3,0]),v2=Vec(*value[:3,1]),v3=Vec(*value[:3,2]))
    def __invert__(self):return self.from_array(np.linalg.inv(array(self)))
    def __mul__(self,other):
        if isinstance(other,Matrix):return self.from_array(array(self)@array(other))
        value=array(self)@np.array([other.x,other.y,other.z,1])
        return Vec(*value[:3])


class WorldNode(Node):
    def GetMg(self):
        local=Affine.from_array(array(self.matrix))
        return self.parent.GetMg()*local if self.parent else local
    def SetMg(self,matrix):self.matrix=(~self.parent.GetMg()*matrix) if self.parent else matrix
    def SetRelPos(self,position):self.matrix.off=position


class NativeKey(Key):
    def GetValue(self):return self.value
    def GetTime(self):return self.time
    def GetInterpolation(self):return self.mode


class NativeCurve(Track):
    def AddKey(self,time):
        key=NativeKey();key.time=time;self.keys.append(key);return {'key':key}
    def GetKey(self,index):return self.keys[index]
    def SetKeyDirty(self):pass


class Skin(WeightTag):
    def GetGeomMg(self):return self.geom
    def CalculateBoneStates(self,index):self.bind=[joint.GetMg() for joint in self.joints]
    def GetJointRestState(self,index):return dict(m_oMg=self.bind[index])
    def GetWeightMap(self,index,count):return self.weights.get(index,[0.0]*count)
    def RemoveJoint(self,joint):
        index=self.joints.index(joint);self.joints.pop(index);self.bind.pop(index)
        self.weights={key-(key>index):value for key,value in self.weights.items() if key!=index}


class WorldPolygon(Polygon,WorldNode):
    def GetDeformCache(self):
        tag=next((tag for tag in self.tags if isinstance(tag,Skin)),None)
        if tag is None:return None
        points=[]
        inverse=~self.GetMg()
        for index,point in enumerate(self.points):
            total=np.zeros(3)
            for slot,joint in enumerate(tag.joints):
                weight=tag.GetWeightMap(slot,len(self.points))[index]
                value=inverse*(joint.GetMg()*(~tag.bind[slot]*(tag.geom*point)))
                total+=weight*np.array([value.x,value.y,value.z])
            points.append(Vec(*total))
        return SimpleNamespace(GetAllPoints=lambda:points)


def native_api():
    api=make_api();api.Matrix=Affine;api.BaseObject=WorldNode;api.PolygonObject=WorldPolygon
    api.CTrack=NativeCurve
    api.modules.character.CAWeightTag=Skin
    return api


class POVCleanupTests(unittest.TestCase):
    def test_clavicle_y_origin_preserves_keys_deltas_and_other_channels(self):
        api,payload=native_api(),fixture()
        graph={node['id']:node for node in payload['asset']['exportHierarchy']}
        graph['b5']['matrix'][13]=1.2;graph['b8']['matrix'][13]=1.4
        for matrix,y,z in zip(payload['animation']['tracks'][0]['frames'],(.1,.22,.16),(.2,.15,.4)):
            matrix[13]=y;matrix[14]=z
        # One vertex straddles the arms: its expected displacement must be the
        # weighted combination of the distinct left/right height offsets.
        mesh=payload['asset']['models'][0]['meshes'][0]
        mesh['skinIndices'][8:12]=[6,9,0,0];mesh['skinWeights'][8:12]=[.25,.75,0,0]
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'reload.c4d')
        self.assertEqual(report['verifiedShiftedPoseFrames'],3)
        self.assertEqual(report['verifiedShiftedDeformedVertices'],9)
        self.assertEqual([item['keyCount'] for item in report['clavicleYOffsets']],[3,3])
        for item,offset in zip(report['clavicleYOffsets'],(150,170)):
            self.assertAlmostEqual(item['offsetCm'],offset)
            self.assertEqual(item['firstY'],0)
            self.assertTrue(item['deltasPreserved'])
        doc=api.saved[0];source=doc.nla.children[0]
        for root in source.children:
            channels={(track.desc[0].id,track.desc[1].id):track for track in root.tracks}
            for axis,expected in ((api.VECTOR_X,[0,10,20]),(api.VECTOR_Y,[0,12,6]),(api.VECTOR_Z,[-20,-15,-40])):
                track=channels[(api.ID_BASEOBJECT_REL_POSITION,axis)]
                np.testing.assert_allclose([key.value for key in track.keys],expected,atol=1e-12)
                np.testing.assert_allclose([float(key.time) for key in track.keys],[0,1/30,2/30],atol=0)
                self.assertTrue(all(key.mode==api.CINTERPOLATION_LINEAR for key in track.keys))
            for axis in (api.VECTOR_X,api.VECTOR_Y,api.VECTOR_Z):
                self.assertEqual([key.value for key in channels[(api.ID_BASEOBJECT_REL_ROTATION,axis)].keys],[0,0,0])
        mesh=doc.roots[0].children[0].children[1]
        points=mesh.GetDeformCache().GetAllPoints()
        np.testing.assert_allclose([point.y for point in points],[-150,-170,-65],atol=1e-10)
        tag=next(tag for tag in mesh.tags if isinstance(tag,Skin))
        hand_slot=next(i for i,joint in enumerate(tag.joints) if joint.name=='Hand_L')
        self.assertAlmostEqual(tag.bind[hand_slot].off.y,160)

    def test_already_direct_clavicles_are_zeroed_with_or_without_animation(self):
        for animated in (False,True):
            with self.subTest(animated=animated):
                api,payload=native_api(),fixture(animated=animated)
                graph={node['id']:node for node in payload['asset']['exportHierarchy']}
                for index,height in ((5,1.2),(8,1.4)):
                    graph[f'b{index}']['parentId']='skeleton';graph[f'b{index}']['matrix'][13]=height
                    if animated:
                        frames=[]
                        for delta in (0,.15,-.1):
                            matrix=IDENTITY.copy();matrix[13]=height+delta;frames.append(matrix)
                        payload['animation']['tracks'].append(dict(modelId='arms',boneIndex=index,frames=frames))
                with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
                    report=build_scene(payload,Path(temp)/'direct.c4d')
                self.assertEqual(report['optimizedRoots'],[])
                self.assertEqual([item['firstY'] for item in report['clavicleYOffsets']],[0,0])
                self.assertEqual([item['keyCount'] for item in report['clavicleYOffsets']],[3 if animated else 0]*2)
                for item,expected in zip(report['clavicleYOffsets'],(120,140)):
                    self.assertAlmostEqual(item['offsetCm'],expected)
                roots=api.saved[0].roots[0].children[0].children[0].children
                self.assertEqual([root.GetRelPos().y for root in roots],[0,0])
                self.assertEqual(report['trackCount'],12 if animated else 0)
                if animated:
                    for root in api.saved[0].nla.children[0].children:
                        track=next(track for track in root.tracks if track.desc[0].id==api.ID_BASEOBJECT_REL_POSITION and track.desc[1].id==api.VECTOR_Y)
                        np.testing.assert_allclose([key.value for key in track.keys],[0,15,-10],atol=1e-12)

    def test_plan_removes_camera_only_geometry_and_unused_dependencies(self):
        payload=fixture();original=copy.deepcopy(payload)
        plan=plan_gameplay1p(payload['asset']['exportHierarchy'],{m['id']:m for m in payload['asset']['models']})
        self.assertEqual(plan['promoted'],{'b5':'skeleton','b8':'skeleton'})
        self.assertEqual(plan['removedBones'],{'b0','b1','b2','b3','b4','b7'})
        self.assertEqual(plan['removedCameraMeshIds'],{'mesh1'})
        self.assertEqual(plan['removedEmptyNulls'],{'empty'})
        self.assertNotIn('b10',plan['removed'],'The unweighted weapon attachment joint must survive.')
        self.assertEqual(payload,original)

    def test_mixed_camera_and_arm_weights_preserve_mesh_and_needed_branch(self):
        payload=fixture(mixed=True)
        plan=plan_gameplay1p(payload['asset']['exportHierarchy'],{m['id']:m for m in payload['asset']['models']})
        self.assertEqual(plan['removedCameraMeshIds'],set())
        self.assertEqual(plan['mixedCameraMeshes'],['Cards'])
        self.assertEqual(plan['promoted']['b1'],'skeleton')
        self.assertNotIn('b2',plan['removed'])

    def test_source_identity_survives_user_joint_renames(self):
        payload=fixture()
        for node in payload['asset']['exportHierarchy']:
            if node['type']=='bone':node['name']='Renamed '+node['id']
        plan=plan_gameplay1p(payload['asset']['exportHierarchy'],{m['id']:m for m in payload['asset']['models']})
        self.assertEqual(plan['clavicles'],['b5','b8'])
        self.assertEqual(plan['removedCameraMeshIds'],{'mesh1'})

    def test_native_cleanup_bakes_parent_motion_and_preserves_skin_and_attachments(self):
        api,payload=native_api(),fixture()
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'reload.c4d')
        self.assertEqual((report['boneCount'],report['meshCount'],report['skinCount']),(5,2,1))
        self.assertEqual(report['removedCameraMeshes'],['Cards'])
        self.assertEqual(report['removedBoneCount'],6)
        self.assertEqual(report['verifiedPoseFrames'],3)
        self.assertEqual(report['verifiedDeformedVertices'],9)
        self.assertTrue(report['skinVerification'])
        self.assertEqual(report['motionSourceNames'],['Reload'])
        doc=api.saved[0];skeleton=doc.roots[0].children[0].children[0]
        self.assertEqual(skeleton.name,'Joints')
        self.assertEqual([obj.name for obj in skeleton.children],['Clavicle_L','Clavicle_R'])
        mesh=doc.roots[0].children[0].children[1]
        skin=next(tag for tag in mesh.tags if isinstance(tag,Skin))
        self.assertEqual(skin.GetJointCount(),5)
        self.assertEqual([joint.name for joint in skin.joints],['Clavicle_L','Hand_L','Clavicle_R','Hand_R','Weapon_R'])

    def test_static_cleanup_does_not_create_animation_tracks(self):
        api,payload=native_api(),fixture(animated=False)
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'rest.c4d')
        self.assertTrue(report['povOptimized'])
        self.assertEqual(report['trackCount'],0)
        self.assertNotIn('motionClipCount',report)

    def test_native_mixed_weights_keep_camera_branch_and_deformation(self):
        api,payload=native_api(),fixture(mixed=True)
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
            report=build_scene(payload,Path(temp)/'mixed.c4d')
        self.assertEqual((report['boneCount'],report['meshCount'],report['skinCount']),(7,3,2))
        self.assertEqual(report['removedCameraMeshes'],[])
        self.assertEqual(report['preservedOutsideArmJoints'],['Camera','Camera card'])
        self.assertTrue(any('mixed camera/other joint weights' in warning for warning in report['warnings']))
        self.assertEqual(report['verifiedDeformedVertices'],18)
        skeleton=api.saved[0].roots[0].children[0].children[0]
        self.assertEqual([obj.name for obj in skeleton.children],['Clavicle_L','Clavicle_R','Camera'])

    def test_weight_slot_removal_must_preserve_weights_and_bind_rest_states(self):
        for corrupt in ('weights','rest'):
            with self.subTest(corrupt=corrupt):
                api,payload=native_api(),fixture()
                class BrokenSkin(Skin):
                    def RemoveJoint(self,joint):
                        super().RemoveJoint(joint)
                        if corrupt=='weights':
                            slot=next(iter(self.weights));self.weights[slot]=[.5]*len(self.weights[slot])
                        else:self.bind[0]=Affine()
                api.modules.character.CAWeightTag=BrokenSkin
                with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'c4d':api,'c4d.modules':api.modules}):
                    with self.assertRaisesRegex(RuntimeError,'changed skin weights or their original bind state'):
                        build_scene(payload,Path(temp)/'bad.c4d')
                self.assertEqual(api.saved,[])

    def test_other_perspectives_are_untouched(self):
        payload=fixture();payload['gameplay1P']=False
        marker=object()
        result=optimize_gameplay1p(None,None,payload,marker,marker,marker,marker,marker,[])
        self.assertIs(result['objects'],marker)
        self.assertEqual(result['report'],{})


if __name__=='__main__':unittest.main()
