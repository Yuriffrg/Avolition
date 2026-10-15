import * as THREE from './vendor/three.module.js';

// Static map scenes repeat the same prop geometry with the same few materials
// thousands of times. Drawing every placement as its own mesh makes CPU draw
// submission dominate, so placements that share (geometry, material) are drawn
// together as one InstancedMesh.
//
// The per-placement meshes stay in the scene graph as the source of truth:
// names, visibility, materials, world matrices and bounds are read from them
// as before (object tree, framing, texture loading, wireframe overlays). Only
// their own draw is switched off (an empty layer mask) while a batch draws
// them. sync() follows whatever material and visibility each mesh has now, so
// callers change meshes as usual and call sync() before rendering. cull()
// then keeps, per frame, only the instances inside the camera frustum, the
// same bounding-sphere test the renderer applied to each mesh before, so a
// close view still draws only nearby placements.

// A mirrored placement (negative determinant) is drawn by a batch whose own
// matrix mirrors once more. The renderer then flips the front face for the
// batch exactly as it does for a single mirrored mesh, and the instance
// matrices (MIRROR × world) have a positive determinant.
const MIRROR = new THREE.Matrix4().makeScale(-1, 1, 1);
const _matrix = new THREE.Matrix4();
const _frustum = new THREE.Frustum();

class Batch {
  constructor(root, geometry, material, renderOrder, mirrored) {
    Object.assign(this, {root, geometry, material, renderOrder, mirrored});
    this.members = new Set();
    this.mesh = null;
    this.drawn = [];
  }

  /** Size the instance buffer for the current members; false once empty. */
  allocate() {
    const size = this.members.size, capacity = this.mesh ? this.mesh.instanceMatrix.count : 0;
    if (!size) { this._release(); return false; }
    if (size > capacity || size < capacity / 4) {
      // Capacity covers hidden members too, so visibility toggles never
      // reallocate; it shrinks when most members moved to another material.
      this._release();
      const mesh = new THREE.InstancedMesh(this.geometry, this.material, size);
      mesh.name = 'Map batch';
      mesh.renderOrder = this.renderOrder;
      // Instances are culled one by one in pack().
      mesh.frustumCulled = false;
      mesh.matrixAutoUpdate = false;
      if (this.mirrored) mesh.matrix.copy(MIRROR);
      mesh.updateMatrixWorld(true);
      this.root.add(mesh);
      this.mesh = mesh;
    }
    this.drawn = null;
    return true;
  }

  /** Write the visible (and, with a frustum, in-view) instances. */
  pack(frustum) {
    const drawn = [];
    for (const entry of this.members) {
      if (entry.mesh.visible && (!frustum || !entry.mesh.frustumCulled || frustum.intersectsSphere(entry.sphere))) drawn.push(entry);
    }
    // Camera moves usually keep most batches' instance sets unchanged.
    if (this.drawn && drawn.length === this.drawn.length && drawn.every((entry, index) => entry === this.drawn[index])) return;
    this.drawn = drawn;
    const array = this.mesh.instanceMatrix.array;
    drawn.forEach((entry, index) => {
      const world = this.mirrored ? _matrix.multiplyMatrices(MIRROR, entry.mesh.matrixWorld) : entry.mesh.matrixWorld;
      array.set(world.elements, index * 16);
    });
    this.mesh.count = drawn.length;
    this.mesh.visible = drawn.length > 0;
    this.mesh.instanceMatrix.clearUpdateRanges();
    this.mesh.instanceMatrix.addUpdateRange(0, drawn.length * 16);
    this.mesh.instanceMatrix.needsUpdate = true;
    this.mesh.boundingSphere = null;
    this.mesh.boundingBox = null;
  }

  _release() {
    if (!this.mesh) return;
    this.mesh.removeFromParent();
    // Frees the instance buffer only; geometry and material belong to the viewer.
    this.mesh.dispose();
    this.mesh = null;
  }
}

export class MapBatches {
  constructor(scene) {
    this.root = new THREE.Group();
    this.root.name = 'Map batches';
    scene.add(this.root);
    this.entries = [];
    this.batches = new Map();
    this.dirty = new Set();
  }

  /** Whether a mesh drawn with this material is drawn by a batch. */
  batchable(mesh, material = mesh.material) {
    // Skinned meshes need their own bones. Transparent surfaces need the
    // renderer's per-object back-to-front sorting, which instances lack.
    return Boolean(mesh.isMesh && !mesh.isSkinnedMesh && !mesh.isInstancedMesh && material?.isMaterial
      && !material.transparent);
  }

  /** Track a mesh whose world matrix is final. */
  add(mesh) {
    if (!mesh.geometry.boundingSphere) mesh.geometry.computeBoundingSphere();
    const entry = {mesh, mask: mesh.layers.mask, mirrored: mesh.matrixWorld.determinant() < 0,
      sphere: mesh.geometry.boundingSphere.clone().applyMatrix4(mesh.matrixWorld),
      batch: null, material: undefined, visible: undefined};
    this.entries.push(entry);
    this._place(entry);
  }

  /** Apply material and visibility changes; returns whether anything changed. */
  sync() {
    for (const entry of this.entries) {
      if (entry.mesh.material !== entry.material || entry.mesh.visible !== entry.visible) this._place(entry);
    }
    const changed = this.dirty.size > 0;
    for (const batch of this.dirty) {
      if (batch.allocate()) batch.pack(null);
      else this.batches.delete(batch.key);
    }
    this.dirty.clear();
    return changed;
  }

  /** Draw only the instances inside this camera's view (after sync()). */
  cull(camera) {
    camera.updateMatrixWorld();
    _matrix.multiplyMatrices(camera.projectionMatrix, camera.matrixWorldInverse);
    _frustum.setFromProjectionMatrix(_matrix, camera.coordinateSystem, camera.reversedDepth);
    for (const batch of this.batches.values()) batch.pack(_frustum);
  }

  _place(entry) {
    const {mesh} = entry;
    const batch = this.batchable(mesh) ? this._batch(mesh, entry.mirrored) : null;
    if (batch !== entry.batch) {
      if (entry.batch) { entry.batch.members.delete(entry); this.dirty.add(entry.batch); }
      if (batch) batch.members.add(entry);
      entry.batch = batch;
      // A mask of 0 matches no camera: the batch draws this mesh instead.
      mesh.layers.mask = batch ? 0 : entry.mask;
    }
    if (batch) this.dirty.add(batch);
    entry.material = mesh.material;
    entry.visible = mesh.visible;
  }

  _batch(mesh, mirrored) {
    const key = `${mesh.geometry.id}:${mesh.material.id}:${mesh.renderOrder}:${mirrored ? 1 : 0}`;
    let batch = this.batches.get(key);
    if (!batch) {
      batch = new Batch(this.root, mesh.geometry, mesh.material, mesh.renderOrder, mirrored);
      batch.key = key;
      this.batches.set(key, batch);
    }
    return batch;
  }

  /** Stop batching: every tracked mesh draws itself again. */
  dispose() {
    for (const entry of this.entries) entry.mesh.layers.mask = entry.mask;
    for (const batch of this.batches.values()) batch._release();
    this.root.removeFromParent();
    this.entries = [];
    this.batches.clear();
    this.dirty.clear();
  }
}
