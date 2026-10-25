// Exercise production viewport loading/binding without creating WebGL or a DOM.
import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import { PreviewViewport, DEFAULT_VIEW_OPTIONS } from '../web/viewer.js';
import { createPoseEvaluator } from '../web/pose.js';

let checks = 0, maximumError = 0;
assert.deepEqual(DEFAULT_VIEW_OPTIONS, {grid: true, skeleton: false, wireframe: false, lockRoot: false, textured: true, pov: false}); checks++;
const quaternion = (x, y, z) => new THREE.Quaternion().setFromEuler(new THREE.Euler(x, y, z, 'ZYX')).toArray();
const transform = (position, rotation) => new THREE.Matrix4().compose(
  new THREE.Vector3(...position), new THREE.Quaternion(...rotation), new THREE.Vector3(1, 1, 1),
).toArray();
function close(actual, expected, label, tolerance = 2e-5) {
  const av = actual.toArray ? actual.toArray() : actual;
  const ev = expected.toArray ? expected.toArray() : expected;
  assert.equal(av.length, ev.length, label);
  const error = Math.max(0, ...av.map((value, i) => Math.abs(value - ev[i])));
  assert.ok(av.every(Number.isFinite), `${label}: nonfinite values`);
  assert.ok(error < tolerance, `${label}: maximum error ${error}`);
  maximumError = Math.max(maximumError, error);
  checks++;
}
const tri = (skinned = true) => ({
  name: 'triangle', positions: [.2, .3, .1, .8, -.1, .4, -.3, .2, .6],
  normals: [0, 1, 0, 0, 1, 0, 0, 1, 0], indices: [0, 1, 2],
  skinIndices: skinned ? [0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0] : [],
  skinWeights: skinned ? [1, 0, 0, 0, 1, 0, 0, 0, .25, .75, 0, 0] : [],
});
const parent = {
  id: 'parent', name: 'Parent rig', parentId: null, attachmentBone: null,
  attachmentMatrix: transform([1, -.5, .7], quaternion(.1, -.4, .3)),
  bones: [
    { name: 'p-root', parent: -1, pos: [.6, .8, -.4], rot: quaternion(.2, .4, -.3) },
    { name: 'p-tip', parent: 0, pos: [.3, .5, .7], rot: quaternion(-.2, .1, .5) },
  ], meshes: [tri()],
};
const child = {
  id: 'child', name: 'Child rig', parentId: 'parent', attachmentBone: 'p-tip',
  attachmentMatrix: transform([.25, -.2, .3], quaternion(.4, .2, -.1)),
  bones: [
    { name: 'c-root', parent: -1, pos: [.1, .3, .4], rot: quaternion(-.1, .2, .3) },
    { name: 'c-tip', parent: 0, pos: [-.2, .4, .1], rot: quaternion(.3, -.1, .5) },
  ], meshes: [tri()],
};
const grandchild = {
  id: 'grandchild', name: 'Nested rigid prop', parentId: 'child', attachmentBone: 'c-tip',
  attachmentMatrix: transform([.3, -.1, .2], quaternion(.2, -.2, .3)),
  bones: [], meshes: [tri(false)],
};
const sibling = {
  id: 'sibling', name: 'Group attachment', parentId: 'parent', attachmentBone: null,
  attachmentMatrix: transform([-.5, .2, .7], quaternion(.2, .3, .1)),
  bones: [], meshes: [tri(false)],
};
// Child model order deliberately precedes its parent, as an additional check of
// the viewer's deferred parenting pass.
const asset = { name: 'Synthetic attachment audit', models: [grandchild, child, sibling, parent] };
const clipFor = (prefix) => ({ bones: [
  { name: `${prefix}-root`,
    positions: { frames: [0, 10], values: [.2, .3, -.1, .4, -.2, .6] },
    rotations: { frames: [0, 10], values: [...quaternion(.2, -.3, .1), ...quaternion(.6, .2, -.4)] },
    scales: { frames: [0, 10], values: [1, 1, 1, 1.7, .6, 1.3] } },
  { name: `${prefix}-tip`,
    rotations: { frames: [0, 10], values: [...quaternion(.1, .2, .3), ...quaternion(-.2, .6, .4)] },
    scales: { frames: [0, 10], values: [1, 1, 1, .7, 1.4, .8] } },
] });

const viewport = Object.create(PreviewViewport.prototype);
Object.assign(viewport, {
  models: [], options: { ...DEFAULT_VIEW_OPTIONS, lockRoot: false },
  scene: new THREE.Scene(), assetRoot: new THREE.Group(), grid: new THREE.Group(), clip: null, rig: 'auto', frame: 0,
});
viewport.scene.add(viewport.assetRoot);
viewport.frameModel = () => {}; // No camera/DOM needed for the binding audit.
const stats = viewport.loadAsset(asset);
assert.deepEqual(stats, { models: 4, meshes: 4, bones: 4, vertices: 12, polygons: 4 }); checks++;
for (const model of viewport.models) {
  assert.equal(model.overlays.length, model.meshes.length); checks++;
  model.meshes.forEach((mesh, i) => {
    const overlay = model.overlays[i];
    assert.equal(overlay.geometry, mesh.geometry); checks++;
    assert.equal(overlay.visible, false); checks++;
    assert.equal(overlay.material.color.getHex(), 0x000000); checks++;
    assert.equal(overlay.material.wireframeLinewidth, 1); checks++;
    assert.equal(overlay.material.depthTest, true); checks++;
    assert.equal(overlay.material.depthWrite, false); checks++;
    assert.equal(overlay.renderOrder, 1); checks++;
    assert.equal(mesh.material.wireframe, false); checks++;
    assert.equal(mesh.material.polygonOffset, false); checks++;
    if (mesh.isSkinnedMesh) {
      assert.equal(overlay.skeleton, mesh.skeleton); checks++;
      close(overlay.bindMatrix.elements, mesh.bindMatrix.elements, `${model.source.id} overlay bind`);
    }
  });
}
viewport.setOptions({ wireframe: true });
for (const model of viewport.models) model.meshes.forEach((mesh, i) => {
  assert.equal(mesh.material.wireframe, false); checks++;
  assert.equal(mesh.material.polygonOffset, true); checks++;
  assert.equal(model.overlays[i].visible, true); checks++;
});

const sourceById = new Map(asset.models.map(model => [model.id, model]));
const rest = new Map(asset.models.map(model => [model.id,
  createPoseEvaluator(model.bones).evaluate(0).worldMatrices.map(array => new THREE.Matrix4().fromArray(array)),
]));

function expectedState(activeClips, frame, sampleFrames = {}) {
  const results = new Map();
  function resolve(id) {
    if (results.has(id)) return results.get(id);
    const source = sourceById.get(id);
    const group = new THREE.Matrix4().fromArray(source.attachmentMatrix);
    if (source.parentId) {
      const parentState = resolve(source.parentId);
      const parentSource = sourceById.get(source.parentId);
      const boneIndex = parentSource.bones.findIndex(bone => bone.name === source.attachmentBone);
      group.premultiply(boneIndex < 0 ? parentState.group : parentState.bones[boneIndex]);
    }
    const pose = createPoseEvaluator(source.bones, activeClips[id]?.bones || []).evaluate(sampleFrames[id] ?? frame);
    const modelBoneMatrices = pose.worldMatrices.map(array => new THREE.Matrix4().fromArray(array));
    const result = { group, modelBoneMatrices, bones: modelBoneMatrices.map(matrix => group.clone().multiply(matrix)) };
    results.set(id, result);
    return result;
  }
  asset.models.forEach(model => resolve(model.id));
  return results;
}

function verify(activeClips, frame, label, sampleFrames = {}) {
  viewport.setFrame(frame);
  viewport.scene.updateMatrixWorld(true);
  const expected = expectedState(activeClips, frame, sampleFrames);
  for (const model of viewport.models) {
    const source = model.source, state = expected.get(source.id);
    close(model.group.matrixWorld.elements, state.group.elements, `${label} ${source.id} group`);
    model.bones.forEach((bone, i) => close(bone.matrixWorld.elements, state.bones[i].elements, `${label} ${source.id} bone ${i}`));
    for (let meshIndex = 0; meshIndex < model.meshes.length; meshIndex++) {
      const mesh = model.meshes[meshIndex], input = source.meshes[meshIndex];
      model.skeleton?.update(); // Update the exact float32 matrices sent to GPU.
      for (let vertex = 0; vertex < input.positions.length / 3; vertex++) {
        const point = new THREE.Vector3().fromArray(input.positions, vertex * 3);
        const desired = new THREE.Vector3();
        if (mesh.isSkinnedMesh) {
          for (let slot = 0; slot < 4; slot++) {
            const weight = input.skinWeights[vertex * 4 + slot];
            if (!weight) continue;
            const index = input.skinIndices[vertex * 4 + slot];
            const deformation = state.modelBoneMatrices[index].clone().multiply(rest.get(source.id)[index].clone().invert());
            desired.addScaledVector(point.clone().applyMatrix4(deformation), weight);
          }
          desired.applyMatrix4(state.group);
          const gpuPosition = new THREE.Vector4(point.x, point.y, point.z, 1).applyMatrix4(mesh.bindMatrix);
          const gpuSkinned = new THREE.Vector4(0, 0, 0, 0);
          for (let slot = 0; slot < 4; slot++) {
            const index = input.skinIndices[vertex * 4 + slot];
            const weight = input.skinWeights[vertex * 4 + slot];
            const packed = new THREE.Matrix4().fromArray(model.skeleton.boneMatrices, index * 16);
            gpuSkinned.addScaledVector(gpuPosition.clone().applyMatrix4(packed), weight);
          }
          gpuSkinned.applyMatrix4(mesh.bindMatrixInverse).applyMatrix4(mesh.matrixWorld);
          close(gpuSkinned.toArray().slice(0, 3), desired, `${label} ${source.id} GPU vertex ${vertex}`);
        } else desired.copy(point).applyMatrix4(state.group);
        const actual = mesh.getVertexPosition(vertex, new THREE.Vector3()).applyMatrix4(mesh.matrixWorld);
        close(actual, desired, `${label} ${source.id} CPU vertex ${vertex}`);
        const overlay = model.overlays[meshIndex];
        const overlayPoint = overlay.getVertexPosition(vertex, new THREE.Vector3()).applyMatrix4(overlay.matrixWorld);
        close(overlayPoint, desired, `${label} ${source.id} animated wire vertex ${vertex}`);
      }
    }
    if (model.helper) {
      const positions = model.helper.geometry.getAttribute('position');
      let endpoint = 0;
      for (const bone of model.helper.bones) {
        if (!bone.parent?.isBone) continue;
        for (const expectedBone of [bone, bone.parent]) {
          const helperPoint = new THREE.Vector3().fromBufferAttribute(positions, endpoint++).applyMatrix4(model.helper.matrixWorld);
          close(helperPoint, expectedBone.getWorldPosition(new THREE.Vector3()), `${label} ${source.id} helper endpoint ${endpoint}`);
        }
      }
    }
  }
}

verify({}, 0, 'Rest bind');
const pClip = clipFor('p');
assert.equal(viewport.setClip(pClip).matched, 2); checks++;
verify({ parent: pClip }, 10, 'Animated parent/socket');

const cClip = clipFor('c');
assert.equal(viewport.setClip(cClip).matched, 2); checks++;
verify({ child: cClip }, 10, 'Animated child/rig switch');

// The production UI selects one rig, but the same math must also be sound if
// both levels move (exercise the binding algorithm with explicit evaluators).
for (const model of viewport.models) {
  const active = { parent: pClip, child: cClip }[model.source.id];
  model.evaluator = createPoseEvaluator(model.source.bones, active?.bones || []);
}
verify({ parent: pClip, child: cClip }, 5, 'Nested animated rigs');
viewport.setClip(null);
verify({}, 0, 'Cleared clip restores attachment bind');

// Explicit assignments keep each rig independent. The selected rig supplies
// the timeline, while all other rigs sample the same seconds at their own FPS.
const parentAssignment = {...pClip, fps: 30, durationFrames: 11};
const childAssignment = {...cClip, fps: 60, durationFrames: 11};
const entries = [{modelId: 'parent', clip: parentAssignment}, {modelId: 'child', clip: childAssignment}];
const match = viewport.setAssignments(entries, 'parent');
assert.equal(match.matched, 2); checks++;
assert.equal(match.assignments.length, 2); checks++;
assert.equal(viewport.clip, parentAssignment); checks++;
verify({parent: parentAssignment, child: childAssignment}, 3, 'Explicit rigs own FPS', {parent: 3, child: 6});
verify({parent: parentAssignment, child: childAssignment}, 8, 'Shorter clip holds final frame', {parent: 8, child: 10});
viewport.setAssignments(entries, 'child');
assert.equal(viewport.clip, childAssignment); checks++;
verify({parent: parentAssignment, child: childAssignment}, 6, 'Changing selected object preserves both assignments', {parent: 3, child: 6});
assert.equal(viewport.assignments[0].clip, parentAssignment); checks++;
viewport.setAssignments([{modelId: 'parent', clip: parentAssignment}], 'child');
assert.equal(viewport.clip, null, 'an unassigned active object does not steal another rig clip'); checks++;
verify({parent: parentAssignment}, 4, 'Removing an assignment restores only that rig', {parent: 4});
const noOverlap = {fps: 30, durationFrames: 10, bones: [{name: 'unrelated-joint'}]};
assert.equal(viewport.setAssignments([{modelId: 'parent', clip: noOverlap}], 'parent').matched, 0,
  'manual browsing is not blocked by perspective or joint compatibility'); checks++;
verify({}, 5, 'Unmatched animation leaves selected rig in rest pose');
viewport.setAssignments(entries, 'parent');
viewport.setFrame(3);
const savedFrame = viewport.frame, savedEntries = viewport.assignments;
const savedMatrices = viewport.models.flatMap(model => model.bones.map(bone => bone.matrix.toArray()));
for (const invalid of [
  [{modelId: 'parent', clip: parentAssignment}, {modelId: 'parent', clip: childAssignment}],
  [{modelId: 'unknown', clip: parentAssignment}],
  [{modelId: 'grandchild', clip: parentAssignment}],
  [{modelId: 'parent', clip: {...parentAssignment, fps: 0}}],
  [{modelId: 'parent', clip: {...parentAssignment, fps: NaN}}],
  [{modelId: 'parent', clip: {fps: 30, bones: [{name: 'p-root', positions: {frames: [0, 1], values: [1]}}]}}],
]) {
  assert.throws(() => viewport.setAssignments(invalid, 'parent')); checks++;
  assert.equal(viewport.assignments, savedEntries, 'failed assignment is transactional'); checks++;
  assert.equal(viewport.frame, savedFrame); checks++;
  assert.deepEqual(viewport.models.flatMap(model => model.bones.map(bone => bone.matrix.toArray())), savedMatrices); checks++;
}
assert.throws(() => viewport.setAssignments(entries, 'unknown')); checks++;
assert.throws(() => viewport.setAssignments(entries, 'grandchild')); checks++;
viewport.setClip(null);
verify({}, 0, 'Legacy clip clearing removes explicit assignments');
assert.deepEqual(viewport.assignments, []); checks++;

// Real OW arms and weapon rigs reuse names. Binding by model ID must prevent
// either rig's root keys from leaking into the other, including attachments.
const sharedAsset = structuredClone(asset);
for (const model of sharedAsset.models) {
  model.bones.forEach((bone, index) => { bone.name = `shared-${index}`; });
  if (model.attachmentBone) model.attachmentBone = 'shared-1';
}
const sharedView = Object.create(PreviewViewport.prototype);
Object.assign(sharedView, {
  models: [], options: {...DEFAULT_VIEW_OPTIONS}, scene: new THREE.Scene(),
  assetRoot: new THREE.Group(), grid: new THREE.Group(), clip: null, rig: 'auto', frame: 0,
});
sharedView.scene.add(sharedView.assetRoot);
sharedView.frameModel = () => {};
sharedView.loadAsset(sharedAsset);
const sharedClip = (x, fps) => ({fps, durationFrames: 30, bones: [{name: 'shared-0',
  positions: {frames: [0, 30], values: [x, 0, 0, x + 6, 0, 0]}}]});
const leftClip = sharedClip(1, 30), rightClip = sharedClip(20, 60);
const unchangedSharedAsset = JSON.stringify(sharedAsset);
const unchangedSharedClips = JSON.stringify([leftClip, rightClip]);
sharedView.setAssignments([{modelId: 'parent', clip: leftClip}, {modelId: 'child', clip: rightClip}], 'parent');
sharedView.setObjectVisible('parent', null, false);
sharedView.setFrame(10);
for (const [id, clip, frame] of [['parent', leftClip, 10], ['child', rightClip, 20]]) {
  const model = sharedView.models.find(model => model.source.id === id);
  const expected = createPoseEvaluator(model.source.bones, clip.bones).evaluate(frame);
  model.bones.forEach((bone, index) => close(bone.matrix, expected.localMatrices[index], `Reused bone name ${id}/${index}`));
}
const sharedParent = sharedView.models.find(model => model.source.id === 'parent');
const sharedChild = sharedView.models.find(model => model.source.id === 'child');
close(sharedChild.group.matrixWorld, sharedParent.bones[1].matrixWorld.clone().multiply(sharedChild.group.matrix),
  'Animated gun remains attached to its separately animated hidden parent');
sharedView.setOptions({lockRoot: true});
assert.equal(JSON.stringify(sharedAsset), unchangedSharedAsset, 'preview assignments do not mutate exported source geometry'); checks++;
assert.equal(JSON.stringify([leftClip, rightClip]), unchangedSharedClips, 'in-place never mutates source animation'); checks++;
sharedView.setOptions({lockRoot: false});
assert.equal(sharedParent.meshes[0].visible, false, 'assignment and in-place changes preserve visibility'); checks++;
assert.equal(sharedView.getObjects().find(object => object.id === 'parent').meshes[0].modelId, 'parent'); checks++;
assert.equal(sharedView.getObjects().find(object => object.id === 'parent').bones, 2); checks++;
// A combined sequence supplies its own clock, independent of the object being
// browsed. The longer parent clip continues after the short child clip ends.
for (const active of ['parent', 'child']) {
  sharedView.setAssignments([{modelId: 'parent', clip: leftClip}, {modelId: 'child', clip: rightClip}], active);
  sharedView.setFrame(45, 60);
  for (const [id, clip, frame] of [['parent', leftClip, 22.5], ['child', rightClip, 29]]) {
    const model = sharedView.models.find(model => model.source.id === id);
    const expected = createPoseEvaluator(model.source.bones, clip.bones).evaluate(frame);
    model.bones.forEach((bone, index) => close(bone.matrix, expected.localMatrices[index], `Combined clock ${active}/${id}/${index}`));
  }
  close(sharedChild.group.matrixWorld, sharedParent.bones[1].matrixWorld.clone().multiply(sharedChild.group.matrix),
    `Combined clock keeps gun attached while browsing ${active}`);
  sharedView.setFrame(12, 60);
  const beforeOptions = sharedView.models.flatMap(model => model.bones.map(bone => bone.matrix.toArray()));
  sharedView.setOptions({wireframe: true});
  assert.deepEqual(sharedView.models.flatMap(model => model.bones.map(bone => bone.matrix.toArray())), beforeOptions,
    'Display option changes preserve the explicit timeline FPS'); checks++;
  sharedView.setOptions({lockRoot: true});
  for (const [id, clip, frame] of [['parent', leftClip, 6], ['child', rightClip, 12]]) {
    const model = sharedView.models.find(model => model.source.id === id);
    const expected = createPoseEvaluator(model.source.bones, clip.bones).evaluate(frame, {lockRoot: true});
    model.bones.forEach((bone, index) => close(bone.matrix, expected.localMatrices[index], `Combined in-place clock ${active}/${id}/${index}`));
  }
  sharedView.setOptions({lockRoot: false, wireframe: false});
  assert.deepEqual(sharedView.models.flatMap(model => model.bones.map(bone => bone.matrix.toArray())), beforeOptions,
    'Returning from in-place restores the same combined moment'); checks++;
}
for (const fps of [0, -1, NaN, Infinity]) {
  const frameBefore = sharedView.frame;
  assert.throws(() => sharedView.setFrame(99, fps), /positive frame rate/); checks++;
  assert.equal(sharedView.frame, frameBefore, 'Invalid shared FPS does not move the frame'); checks++;
}
sharedView.loadAsset(sharedAsset);
assert.deepEqual(sharedView.assignments, [], 'a different loaded entity starts unassigned'); checks++;
assert.equal(sharedView.activeModelId, null); checks++;
sharedView.clearAsset();

// Object visibility belongs only to renderables, never the source asset or its
// bone attachment hierarchy. Duplicate names and an empty source mesh must not
// make the per-mesh controls ambiguous.
const visibilityAsset = structuredClone(asset);
const visibilityParent = visibilityAsset.models.find(model => model.id === 'parent');
visibilityParent.meshes.push({...tri(false), positions: [], indices: []}, tri(false));
for (const model of visibilityAsset.models) {
  model.materials = {plain: {name: 'Simple test material', baseColor: [.4, .5, .6]}};
  for (const mesh of model.meshes) mesh.materialKey = 'plain';
}
const unchangedSource = JSON.stringify(visibilityAsset);
const visibleView = Object.create(PreviewViewport.prototype);
Object.assign(visibleView, {
  models: [], options: {...DEFAULT_VIEW_OPTIONS, skeleton: true, wireframe: true},
  scene: new THREE.Scene(), assetRoot: new THREE.Group(), grid: new THREE.Group(),
  camera: new THREE.PerspectiveCamera(38, 1, .01, 10000),
  controls: {target: new THREE.Vector3(), update() {}}, clip: null, rig: 'auto', frame: 0,
});
visibleView.scene.add(visibleView.assetRoot);
visibleView.loadAsset(visibilityAsset);
const modelFor = id => visibleView.models.find(model => model.source.id === id);
const objectsFor = id => visibleView.getObjects().find(model => model.id === id);
assert.deepEqual(objectsFor('parent').meshes.map(mesh => mesh.id), ['0', '2']); checks++;
assert.equal(visibleView.getVisiblePolygonCount(), 5); checks++;
assert.equal(modelFor('parent').helper.bones.length, 2, 'parent helper excludes attached child bones'); checks++;
assert.equal(modelFor('child').helper.bones.length, 2); checks++;
const menuSnapshot = visibleView.getObjects();
menuSnapshot[0].meshes[0].visible = false;
assert.equal(visibleView.getVisiblePolygonCount(), 5, 'menu snapshots cannot mutate visibility'); checks++;
assert.equal(visibleView.setObjectVisible('parent', '2', false), true); checks++;
assert.equal(objectsFor('parent').visible, false); checks++;
assert.equal(objectsFor('parent').indeterminate, true); checks++;
assert.equal(modelFor('parent').meshes[0].visible, true); checks++;
assert.equal(modelFor('parent').overlays[1].visible, false); checks++;
assert.equal(visibleView.getVisiblePolygonCount(), 4); checks++;
assert.equal(visibleView.setObjectVisible('parent', '1', false), false, 'empty meshes are not menu objects'); checks++;
assert.equal(visibleView.setObjectVisible('missing-model', null, false), false); checks++;

// Hiding an animated parent must preserve a visible child's socket transform.
const childBefore = modelFor('child').group.matrixWorld.clone();
visibleView.setObjectVisible('parent', null, false);
assert.equal(objectsFor('parent').indeterminate, false); checks++;
assert.equal(modelFor('parent').helper.visible, false); checks++;
assert.equal(modelFor('child').helper.visible, true); checks++;
assert.equal(modelFor('child').meshes[0].visible, true); checks++;
visibleView.setClip(pClip);
visibleView.setFrame(10);
const childAfter = modelFor('child').group.matrixWorld;
assert.ok(!childBefore.equals(childAfter), 'hidden parent continues animating attached visible child'); checks++;
close(childAfter.elements, expectedState({parent: pClip}, 10).get('child').group.elements,
  'hidden parent preserves exact child attachment');
for (const model of visibleView.models) {
  assert.equal(model.group.visible, true); checks++;
  for (const bone of model.bones) { assert.equal(bone.visible, true); checks++; }
}

// Clip changes, rig changes, material loading and option toggles cannot bring a
// hidden mesh or its wire overlay back. The source used by exports is unchanged.
visibleView.setObjectVisible('child', null, false);
visibleView.setRig('child');
visibleView.setClip(cClip);
visibleView.setFrame(5);
visibleView.setOptions({wireframe: false, skeleton: false, textured: false, lockRoot: true});
const materialReport = await visibleView.loadMaterials(visibilityAsset, async () => {
  throw new Error('Untextured test material should not fetch images');
});
assert.equal(materialReport.applied, 5); checks++;
visibleView.setOptions({wireframe: true, skeleton: true, textured: true, lockRoot: false});
for (const id of ['parent', 'child']) {
  const model = modelFor(id);
  assert.ok(model.meshes.every(mesh => !mesh.visible)); checks++;
  assert.ok(model.overlays.every(mesh => !mesh.visible)); checks++;
  assert.equal(model.helper.visible, false); checks++;
}
assert.equal(modelFor('grandchild').meshes[0].visible, true, 'hiding a rig does not hide its attached prop'); checks++;
assert.equal(visibleView.getVisiblePolygonCount(), 2); checks++;
assert.equal(visibleView._loadedAsset, visibilityAsset); checks++;
assert.equal(JSON.stringify(visibilityAsset), unchangedSource, 'visibility and in-place are preview-only state'); checks++;

// Framing uses visible geometry, while hiding every object leaves a finite,
// unchanged camera instead of centering invisible rig bounds.
visibleView.setAllObjectsVisible(false);
assert.equal(visibleView.getVisiblePolygonCount(), 0); checks++;
assert.ok(visibleView.getObjects().every(model => !model.visible && !model.indeterminate)); checks++;
const hiddenCamera = visibleView.camera.position.clone(), hiddenTarget = visibleView.controls.target.clone();
visibleView.frameModel();
close(visibleView.camera.position, hiddenCamera, 'hide-all preserves camera');
close(visibleView.controls.target, hiddenTarget, 'hide-all preserves target');
visibleView.setObjectVisible('child', '0', true);
const childMesh = modelFor('child').meshes[0];
childMesh.computeBoundingBox();
const visibleBounds = new THREE.Box3().expandByObject(childMesh);
visibleView.frameModel();
close(visibleView.controls.target, visibleBounds.getCenter(new THREE.Vector3()), 'frame only visible meshes');
assert.equal(visibleView.getVisiblePolygonCount(), 1); checks++;
assert.equal(modelFor('child').helper.visible, true); checks++;
assert.equal(modelFor('parent').helper.visible, false); checks++;
visibleView.setAllObjectsVisible(true);
assert.equal(visibleView.getVisiblePolygonCount(), 5); checks++;
assert.ok(visibleView.getObjects().every(model => model.visible && !model.indeterminate)); checks++;
visibleView.setAllObjectsVisible(false);
visibleView.loadAsset(visibilityAsset);
assert.equal(visibleView.getVisiblePolygonCount(), 5, 'new asset loads reset object visibility'); checks++;
assert.ok(visibleView.models.every(model => model.overlays.every(mesh => mesh.visible))); checks++;
visibleView.clearAsset();
assert.deepEqual(visibleView.getObjects(), []); checks++;
assert.equal(visibleView.getVisiblePolygonCount(), 0); checks++;
assert.equal(visibleView._loadedAsset, null); checks++;
assert.equal(JSON.stringify(visibilityAsset), unchangedSource); checks++;

const oldMeshes = viewport.models.flatMap(model => model.meshes);
const oldOverlays = viewport.models.flatMap(model => model.overlays);
viewport.setOptions({ wireframe: false });
for (const overlay of oldOverlays) { assert.equal(overlay.visible, false); checks++; }
const disposals = new Map();
for (const item of [...oldMeshes.map(mesh => mesh.geometry), ...oldMeshes.map(mesh => mesh.material), ...oldOverlays.map(mesh => mesh.material)]) {
  disposals.set(item, 0);
  item.addEventListener('dispose', () => disposals.set(item, disposals.get(item) + 1));
}
viewport._clear();
assert.equal(viewport.models.length, 0); checks++;
assert.equal(viewport.assetRoot.children.length, 0); checks++;
assert.equal(viewport.scene.children.filter(item => item.isSkeletonHelper).length, 0); checks++;
assert.equal(oldMeshes.length, 4); checks++;
for (const [item, count] of disposals) { assert.equal(count, 1, `${item.type} must be disposed once`); checks++; }
for (const overlay of oldOverlays) { assert.equal(overlay.parent, null); checks++; }

console.log(JSON.stringify({ checks, maximumError, nestedModels: 4, webglRequired: false }));
