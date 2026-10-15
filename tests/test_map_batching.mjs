// Static map batching (web/map-batching.js) through the viewer, without a GPU:
// placements sharing geometry and material draw as one InstancedMesh, and
// visibility, wireframe, texture switching, mirroring and disposal follow the
// per-placement meshes exactly.
import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {PreviewViewport, DEFAULT_VIEW_OPTIONS} from '../web/viewer.js';
import {MapBatches} from '../web/map-batching.js';

let checks = 0;
const equal = (actual, expected, label) => { assert.deepEqual(actual, expected, label); checks++; };
const ok = (value, label) => { assert.ok(value, label); checks++; };
const translate = (x, y, z) => new THREE.Matrix4().makeTranslation(x, y, z).toArray();
const triangle = {positions: [-1, 0, -1, 1, 0, -1, 0, 3, 1], indices: [0, 1, 2]};
const asset = {map: {version: '2.1'}, meshPrototypes: {
  building: {meshes: [{name: 'Building', ...triangle, materialKey: 'stone'}]},
  lamp: {meshes: [{name: 'Lamp', positions: [...triangle.positions], indices: [0, 1, 2], materialKey: 'glass'}, {name: 'Post', ...triangle, materialKey: 'stone'}]},
}, models: [
  {id: 'a', name: 'A', bones: [], meshPrototype: 'building', attachmentMatrix: translate(-10, 0, 0), materials: {stone: {baseColor: [.4, .4, .4]}}},
  {id: 'b', name: 'B', bones: [], meshPrototype: 'building', attachmentMatrix: translate(10, 0, 0), materials: {stone: {baseColor: [.4, .4, .4]}}},
  {id: 'c', parentId: 'b', name: 'C', bones: [], meshPrototype: 'building', attachmentMatrix: translate(0, 5, 0), materials: {stone: {baseColor: [.6, .6, .6]}}},
  {id: 'mirror', name: 'Mirror', bones: [], meshPrototype: 'building', attachmentMatrix: new THREE.Matrix4().makeScale(-1, 1, 1).setPosition(0, 0, 20).toArray()},
  {id: 'lamp', name: 'Lamp', bones: [], meshPrototype: 'lamp', attachmentMatrix: translate(0, 0, -20),
    materials: {glass: {alphaMode: 'BLEND', opacity: .5}, stone: {baseColor: [.4, .4, .4]}}},
]};
let renders = 0;
const view = Object.create(PreviewViewport.prototype);
Object.assign(view, {
  models: [], options: {...DEFAULT_VIEW_OPTIONS}, scene: new THREE.Scene(), assetRoot: new THREE.Group(), grid: new THREE.Group(),
  camera: new THREE.PerspectiveCamera(38, 1.5, .01, 10000), canvas: {closest: () => false},
  renderer: {render() { renders++; }, capabilities: {getMaxAnisotropy: () => 1}},
  controls: {target: new THREE.Vector3(), update() { view.camera.lookAt(this.target); return false; }}, frame: 0, clip: null, rig: 'auto',
});
view.scene.add(view.assetRoot);

const batches = () => view._mapBatches ? [...view._mapBatches.root.children] : [];
const drawn = () => batches().filter(mesh => mesh.visible && mesh.count > 0);
const batchOf = mesh => drawn().find(batch => batch.geometry === mesh.geometry && batch.material === mesh.material
  && (batch.matrixWorld.determinant() < 0) === (mesh.matrixWorld.determinant() < 0));
const selfDrawn = () => { const list = []; view.assetRoot.traverse(o => { if (o.isMesh && o.visible && o.layers.test(view.camera.layers)) list.push(o); }); return list; };
// World-space instance transforms of a batch (its own matrix applied).
const worlds = batch => Array.from({length: batch.count}, (_, i) =>
  new THREE.Matrix4().fromArray(batch.instanceMatrix.array, i * 16).premultiply(batch.matrixWorld).elements.slice(12, 15));
const model = id => view.models.find(item => item.source.id === id);
const frame = () => { view.invalidate(); view._renderFrame(); };

equal(view.loadAsset(asset).meshes, 6);
frame();
const neutral = model('a').meshes[0].material;
const shared = batchOf(model('a').meshes[0]);
ok(shared, 'Identical placements share one neutral batch.');
equal(shared.count, 4, 'Building A, B, C and the lamp post share geometry and the neutral material.');
equal(worlds(shared).sort(), [[-10, 0, 0], [0, 0, -20], [10, 0, 0], [10, 5, 0]], 'Instances use composed world transforms, including attachments.');
const mirrored = drawn().find(batch => batch.geometry === shared.geometry && batch !== shared);
equal(drawn().length, 3, 'Six meshes draw with three calls: shared, mirrored and the lamp glass.');
ok(mirrored.matrixWorld.determinant() < 0, 'A mirrored placement keeps its flipped front face.');
equal(worlds(mirrored), [[0, 0, 20]]);
equal(selfDrawn(), [], 'Batched placement meshes no longer draw themselves.');
equal(model('a').meshes[0].visible, true, 'Placement visibility stays the user-facing state.');

view.setObjectVisible('b', null, false); frame();
equal(batchOf(model('a').meshes[0]).count, 3, 'Hiding a placement removes its instance.');
equal(view.getObjects().find(item => item.id === 'b').visible, false);
view.setAllObjectsVisible(false); frame();
equal(drawn(), [], 'Hiding everything draws nothing.');
view.setAllObjectsVisible(true); frame();
equal(batchOf(model('a').meshes[0]).count, 4);
const allocated = batchOf(model('a').meshes[0]);
view.setObjectVisible('a', '0', false); view.setObjectVisible('a', '0', true); frame();
equal(batchOf(model('a').meshes[0]), allocated, 'Visibility toggles reuse the instance buffer.');

const version = allocated.instanceMatrix.version;
frame();
equal(allocated.instanceMatrix.version, version, 'An unchanged view uploads no instance data.');
view.controls.target.set(-10, 0, 0); view.camera.position.set(-10, 8, .01);
frame();
equal(worlds(batchOf(model('a').meshes[0])), [[-10, 0, 0]], 'Instances outside the camera frustum are culled, like single meshes were.');
equal(batchOf(model('mirror').meshes[0]), undefined, 'A batch without instances in view issues no draw.');
view.frameModel(); frame();
equal(batchOf(model('a').meshes[0]).count, 4, 'Framing the map again draws every placement.');

view.setOptions({wireframe: true}); frame();
const wire = batchOf(model('a').overlays[0]);
equal([wire.count, wire.renderOrder], [4, 1], 'Wire overlays batch the same way and still draw after surfaces.');
ok(neutral.polygonOffset, 'Filled surfaces are offset while the wireframe shows.');
view.setObjectVisible('c', null, false); frame();
equal(batchOf(model('a').overlays[0]).count, 3, 'Overlays follow hidden placements.');
view.setObjectVisible('c', null, true);
view.setOptions({wireframe: false}); frame();
equal(drawn().length, 3, 'Hidden overlays leave no batches drawing.');

const result = await view.loadMaterials(asset, async () => { throw Error('No textures in this fixture.'); });
equal(result.applied, 5, "Every placement with a material descriptor gets one."); frame();
const stone = model('a').materialBindings[0].textured, light = model('c').materialBindings[0].textured;
ok(stone !== light && model('b').materialBindings[0].textured === stone, 'Equal descriptors share a material, different ones do not.');
equal(batchOf(model('a').meshes[0]).count, 3, 'Batches follow the textured material actually shown.');
equal(batchOf(model('c').meshes[0]).count, 1);
const glass = model('lamp').meshes[0];
ok(glass.material.transparent, 'Fixture has a blended material.');
equal(selfDrawn(), [glass], 'Transparent surfaces keep per-object sorting and draw themselves.');
equal(batchOf(model('mirror').meshes[0]).material, neutral, 'Placements without a material keep the neutral one.');
view.setOptions({textured: false}); frame();
equal(batchOf(model('a').meshes[0]).count, 4, 'Turning textures off regroups by the neutral material.');
equal(selfDrawn(), [], 'The neutral glass lamp is batched again.');
view.setOptions({textured: true}); frame();
equal(batchOf(model('a').meshes[0]).count, 3, 'Turning textures on restores the textured batches.');
equal(drawn().filter(batch => batch.material === neutral).map(batch => batch.count), [1], 'Only the untextured mirror stays neutral.');

const instanced = batches(), disposed = new Set();
for (const mesh of instanced) mesh.addEventListener('dispose', () => disposed.add(mesh));
view.clearAsset();
equal(disposed.size, instanced.length, 'Clearing the map frees every batch buffer.');
ok(!view.scene.getObjectByName('Map batches'), 'Batches leave the scene with the map.');
equal(view._mapBatches, null);

const character = {...asset, map: undefined};
view.loadAsset(character); frame();
equal(view._mapBatches, null, 'Characters are never batched.');
equal(selfDrawn().length, 6, 'Character meshes draw themselves as before.');
view.clearAsset();

const rigged = {map: {version: '2.1'}, models: [
  {id: 'door', name: 'Door', bones: [{name: 'hinge', parent: -1, pos: [0, 0, 0], rot: [0, 0, 0, 1]}], meshes: [{name: 'door', ...triangle}]},
  {id: 'wall', name: 'Wall', bones: [], meshes: [{name: 'wall', ...triangle}]},
]};
view.loadAsset(rigged); frame();
equal(drawn().length, 1, 'Static rig placements batch at rest.');
view.setAssignments([{modelId: 'door', clip: {fps: 30, bones: [{name: 'hinge', positions: {frames: [0, 10], values: [0, 0, 0, 5, 0, 0]}}]}}], 'door');
view.setFrame(10); frame();
equal(view._mapBatches, null, 'Animating a map returns every placement to its own animated mesh.');
equal(selfDrawn().length, 2);
view.clearAsset();

// Module level: a geometry/material pair used by several meshes, then moved.
const scene = new THREE.Scene(), geometry = new THREE.BoxGeometry(), red = new THREE.MeshBasicMaterial(), blue = new THREE.MeshBasicMaterial();
const meshes = Array.from({length: 5}, (_, i) => { const mesh = new THREE.Mesh(geometry, red); mesh.position.x = i; mesh.updateMatrixWorld(); return mesh; });
const module = new MapBatches(scene);
for (const mesh of meshes) module.add(mesh);
equal(module.sync(), true); equal(module.sync(), false, 'Unchanged meshes cost no rebuild.');
equal(module.root.children.map(mesh => mesh.count), [5]);
for (const mesh of meshes.slice(0, 4)) mesh.material = blue;
module.sync();
equal(module.root.children.map(mesh => [mesh.material === blue, mesh.count]).sort(), [[false, 1], [true, 4]], 'Material switches move instances between batches.');
for (const mesh of meshes) mesh.material = blue;
module.sync();
equal(module.root.children.map(mesh => mesh.count), [5], 'Empty batches are released.');
module.dispose();
equal(meshes.map(mesh => mesh.layers.mask), [1, 1, 1, 1, 1], 'Disposal lets each mesh draw itself again.');
equal(scene.children, []);
console.log(JSON.stringify({checks}));
