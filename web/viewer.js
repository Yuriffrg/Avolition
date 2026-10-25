import * as THREE from './vendor/three.module.js';
import { PreviewOrbitControls } from './navigation.js';
import { createPoseEvaluator } from './pose.js';
import { MaterialLibrary } from './materials.js';
import { MapBatches } from './map-batching.js';

// Binary core replies deliver mesh arrays as typed arrays, used without a
// copy; JSON replies deliver number arrays.
const attribute = (values, size, TypedArray) =>
  new THREE.BufferAttribute(values instanceof TypedArray ? values : new TypedArray(values), size);
const isTyping = target => target?.matches?.('input, textarea, select, [contenteditable="true"]');
export const DEFAULT_VIEW_OPTIONS = Object.freeze({grid: true, skeleton: false, wireframe: false, lockRoot: false, textured: true, pov: false});
const POV_FOV = 70;
// OW's camera looks along local +Z; Three's camera looks along local -Z.
const POV_CAMERA_BASIS = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0, 1, 0), Math.PI);
const isCameraBone = bone => ['bone_007d', 'camera'].includes(String(bone.name).toLowerCase());

// Map backdrops can be kilometres from the playable streets. Use the placement
// distribution only when it clearly separates those few outliers from a large
// majority of the scene. This affects the camera, never geometry or exports.
export function mapContentBounds(placements, fullBounds) {
  if (placements.length < 100) return fullBounds;
  const centers = placements.map(bounds => bounds.getCenter(new THREE.Vector3()));
  const central = new THREE.Box3();
  for (const axis of ['x', 'y', 'z']) {
    const values = centers.map(center => center[axis]).sort((a, b) => a - b);
    central.min[axis] = values[Math.floor((values.length - 1) * .025)];
    central.max[axis] = values[Math.ceil((values.length - 1) * .975)];
  }
  const centralSize = central.getSize(new THREE.Vector3());
  const span = Math.max(centralSize.x, centralSize.y, centralSize.z);
  if (span < .01) return fullBounds;
  // Padding retains normal edge placements and long, thin map layouts.
  central.expandByVector(centralSize.multiplyScalar(.2).addScalar(span * .02));
  const content = new THREE.Box3();
  let retained = 0;
  for (let i = 0; i < placements.length; i++) {
    const size = placements[i].getSize(new THREE.Vector3());
    if (!central.containsPoint(centers[i]) || Math.max(size.x, size.y, size.z) > span * 1.5) continue;
    content.union(placements[i]); retained++;
  }
  if (retained < placements.length * .85 || content.isEmpty()) return fullBounds;
  const fullSize = fullBounds.getSize(new THREE.Vector3()), size = content.getSize(new THREE.Vector3());
  const diameter = Math.max(size.x, size.y, size.z);
  if (diameter < .01 || Math.max(fullSize.x, fullSize.y, fullSize.z) < diameter * 2) return fullBounds;
  return content.expandByScalar(diameter * .02);
}

export class PreviewViewport {
  constructor(canvas) {
    this.canvas = canvas;
    this._active = true;
    this._disposed = false;
    this._eventWindow = canvas.ownerDocument?.defaultView || window;
    this.options = {...DEFAULT_VIEW_OPTIONS};
    this.models = [];
    this.clip = null;
    this.assignments = [];
    this.activeModelId = null;
    this._assignmentFps = null;
    this._timelineFps = null;
    this.rig = 'auto';
    this.frame = 0;
    this._renderDirty = true;
    this.scene = new THREE.Scene();
    this.scene.background = new THREE.Color('#111115');
    this.camera = new THREE.PerspectiveCamera(38, 1, 0.01, 10000);
    this.camera.position.set(3.5, 2.5, 4.5);
    this.renderer = new THREE.WebGLRenderer({canvas, antialias: true, alpha: false, powerPreference: 'high-performance'});
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.renderer.toneMapping = THREE.ACESFilmicToneMapping;
    this.renderer.toneMappingExposure = 1.2;
    this._createEnvironment();
    this.scene.add(new THREE.HemisphereLight(0xf5f4f7, 0x36343d, 2.7));
    const key = new THREE.DirectionalLight(0xf8f7fa, 3.2);
    key.position.set(4, 7, 5);
    const rim = new THREE.DirectionalLight(0xd1d0d7, 2.3);
    rim.position.set(-4, 3, -5);
    this.scene.add(key, rim);
    this.grid = new THREE.GridHelper(10, 20, 0x45434b, 0x28272e);
    this.scene.add(this.grid);
    this.assetRoot = new THREE.Group();
    this.scene.add(this.assetRoot);
    this.controls = new PreviewOrbitControls(this.camera, canvas);
    this.controls.target.set(0, 1, 0);
    this.controls.enableDamping = true;
    this.controls.dampingFactor = 0.12;
    this.controls.screenSpacePanning = true;
    this.controls.mouseButtons.MIDDLE = THREE.MOUSE.PAN;
    this.controls.mouseButtons.RIGHT = THREE.MOUSE.DOLLY;
    this._controlsChange = () => this.invalidate();
    this.controls.addEventListener('change', this._controlsChange);
    this._navigationKey = null;
    this._keydown = event => this._handleKeyDown(event);
    this._keyup = event => this._handleKeyUp(event);
    this._blur = () => this._handleKeyUp();
    this._contextmenu = event => event.preventDefault();
    this._pointerdown = () => { if (this._canUseCanvas()) canvas.focus({preventScroll: true}); };
    this._eventWindow.addEventListener('keydown', this._keydown);
    this._eventWindow.addEventListener('keyup', this._keyup);
    this._eventWindow.addEventListener('blur', this._blur);
    canvas.addEventListener('contextmenu', this._contextmenu);
    canvas.addEventListener('pointerdown', this._pointerdown);
    this.resizeObserver = new ResizeObserver(() => this._resize());
    this.resizeObserver.observe(canvas.parentElement);
    this._resize();
    this._renderCallback = () => this._renderFrame();
    this.renderer.setAnimationLoop(this._renderCallback);
  }

  invalidate() { this._renderDirty = true; }

  _canUseCanvas() {
    if (this._disposed || this._active === false || this.canvas.isConnected === false
        || this.canvas.closest('[hidden], [inert]')) return false;
    const dialog = this.canvas.closest('dialog');
    if (dialog && !dialog.open) return false;
    const doc = this.canvas.ownerDocument;
    // showModal() makes the background inert without adding an inert attribute.
    // Prefer the focused modal to handle a second dialog above an existing one.
    const modal = doc?.activeElement?.closest?.('dialog:modal') || doc?.querySelector?.('dialog:modal');
    return !modal || modal.contains(this.canvas);
  }

  _handleKeyDown(event) {
    if (!this._canUseCanvas() || this.controls.enabled === false) { this._handleKeyUp(); return; }
    if (isTyping(event.target) || event.target?.isContentEditable) return;
    if (['1', '2', '3'].includes(event.key)) {
      this._navigationKey = event.key;
      this.controls.mouseButtons.LEFT = {'1': THREE.MOUSE.PAN, '2': THREE.MOUSE.DOLLY, '3': THREE.MOUSE.ROTATE}[event.key];
      event.preventDefault();
    }
    if (event.key.toLowerCase() === 'f' && !event.ctrlKey && !event.metaKey) this.frameModel({all:event.shiftKey});
  }

  _handleKeyUp(event) {
    if (!event || event.key === this._navigationKey) {
      this._navigationKey = null;
      if (this.controls.mouseButtons) this.controls.mouseButtons.LEFT = THREE.MOUSE.ROTATE;
    }
  }

  setActive(active) {
    if (this._disposed) return;
    this._active = Boolean(active);
    this.controls.enabled = this._active && !this._povOrbitState;
    this._handleKeyUp();
    this.renderer.setAnimationLoop(this._active ? this._renderCallback : null);
    if (this._active) this._resize();
    this.invalidate();
  }

  dispose() {
    if (this._disposed) return;
    this._disposed = true;
    this._active = false;
    this.renderer.setAnimationLoop(null);
    this._handleKeyUp();
    this.resizeObserver.disconnect();
    this._eventWindow.removeEventListener('keydown', this._keydown);
    this._eventWindow.removeEventListener('keyup', this._keyup);
    this._eventWindow.removeEventListener('blur', this._blur);
    this.canvas.removeEventListener('contextmenu', this._contextmenu);
    this.canvas.removeEventListener('pointerdown', this._pointerdown);
    this.controls.removeEventListener('change', this._controlsChange);
    this.controls.dispose();
    this._clear();
    this._environmentTarget?.dispose();
    this._environmentTarget = null;
    this.scene.environment = null;
    this.grid.geometry.dispose();
    for (const material of Array.isArray(this.grid.material) ? this.grid.material : [this.grid.material]) material.dispose();
    this.scene.clear();
    this.renderer.dispose();
    this.renderer.forceContextLoss();
  }

  _renderFrame() {
    if (!this._canUseCanvas()) { this._handleKeyUp(); return; }
    // Damping still gets a tick while settling. Static maps no longer submit
    // thousands of draw calls every frame after the camera has stopped.
    const moved = this._povOrbitState ? false : this.controls.update();
    if (!moved && !this._renderDirty) return;
    // Material and visibility changes always invalidate; camera moves alone
    // leave the map batches as they are.
    if (this._renderDirty) this._mapBatches?.sync();
    this._updateCameraClipping();
    this._mapBatches?.cull(this.camera);
    this.renderer.render(this.scene, this.camera);
    this._renderDirty = false;
  }

  _resize() {
    if (this._disposed || this._active === false) return;
    const box = this.canvas.parentElement.getBoundingClientRect();
    const width = Math.max(1, box.width), height = Math.max(1, box.height);
    this.renderer.setSize(width, height, false);
    this.camera.aspect = width / height;
    this.camera.updateProjectionMatrix();
    this.invalidate();
  }

  _createEnvironment() {
    // A small neutral studio supplies reflections for metals, which receive
    // virtually no diffuse hemisphere illumination. This remains across loads.
    const room = new THREE.Scene();
    const geometries = [], materials = [];
    let generator;
    try {
      const shellGeometry = new THREE.BoxGeometry(20, 20, 20);
      const shellMaterial = new THREE.MeshBasicMaterial({color: 0x999999, side: THREE.BackSide});
      geometries.push(shellGeometry); materials.push(shellMaterial);
      room.add(new THREE.Mesh(shellGeometry, shellMaterial));
      const panelGeometry = new THREE.PlaneGeometry(1, 1);
      const panelMaterial = new THREE.MeshBasicMaterial({color: new THREE.Color(2.5, 2.5, 2.5), side: THREE.DoubleSide});
      geometries.push(panelGeometry); materials.push(panelMaterial);
      for (const [position, scale] of [[[0, 9, 0], [9, 7]], [[-9, 2, 3], [5, 9]], [[7, 3, -9], [6, 8]]]) {
        const panel = new THREE.Mesh(panelGeometry, panelMaterial);
        panel.position.set(...position); panel.scale.set(scale[0], scale[1], 1);
        panel.lookAt(0, 0, 0); room.add(panel);
      }
      generator = new THREE.PMREMGenerator(this.renderer);
      this._environmentTarget = generator.fromScene(room, .05, .1, 100);
      this.scene.environment = this._environmentTarget.texture;
      this.scene.environmentIntensity = .85;
      this._environmentWarning = null;
    } catch {
      this._environmentTarget?.dispose();
      this._environmentTarget = null;
      this.scene.environment = null;
      this._environmentWarning = 'Studio reflections are unavailable; materials use direct lighting.';
    } finally {
      generator?.dispose();
      for (const geometry of geometries) geometry.dispose();
      for (const material of materials) material.dispose();
    }
  }

  _clear() {
    this._restoreOrbitCamera();
    this._assetGeneration = (this._assetGeneration || 0) + 1;
    this._materialLibrary?.dispose();
    this._materialLibrary = null;
    this._loadedAsset = null;
    this._mapBatches?.dispose();
    this._mapBatches = null;
    const disposedGeometry = new Set();
    const disposedMaterials = new Set();
    const disposeMaterial = material => {
      if (!disposedMaterials.has(material)) { material.dispose(); disposedMaterials.add(material); }
    };
    for (const model of this.models) {
      model.helper?.removeFromParent();
      model.helper?.dispose();
      model.skeleton?.dispose();
      for (const overlay of model.overlays || []) {
        overlay.removeFromParent();
        disposeMaterial(overlay.material);
        // Overlay geometry and skeleton are shared with the shaded mesh.
      }
      for (const mesh of model.meshes) {
        if (!disposedGeometry.has(mesh.geometry)) {
          mesh.geometry.dispose(); disposedGeometry.add(mesh.geometry);
        }
      }
      for (const binding of model.materialBindings) disposeMaterial(binding.neutral);
    }
    this.assetRoot.clear();
    this.models = [];
    this.clip = null;
    this.assignments = [];
    this.activeModelId = null;
    this._assignmentFps = null;
    this._timelineFps = null;
    this._staticScene = false;
    this._mapScene = false;
    this._mapBounds = null;
    this._framingBounds = null;
    this.assetRoot.matrixWorldAutoUpdate = true;
    this.invalidate();
  }

  clearAsset() {
    if (this._disposed) return;
    this._clear();
    this.frame = 0;
  }

  loadAsset(asset) {
    if (this._disposed) throw new Error('This preview viewport has been disposed.');
    this._clear();
    this.rig = 'auto';
    this._mapScene = Boolean(asset.map);
    this._staticScene = this._mapScene;
    const lookup = new Map();
    const geometryCache = new WeakMap();
    let mapNeutral, mapWire;
    let vertices = 0, meshes = 0, boneCount = 0, polygons = 0;
    try {
      for (const entry of asset.models) {
        const prototype = entry.meshPrototype ? asset.meshPrototypes?.[entry.meshPrototype] : null;
        if (entry.meshPrototype && !prototype) throw new Error(`Missing map geometry: ${entry.meshPrototype}`);
        const source = prototype ? {...prototype, ...entry,
          meshes: prototype.meshes.map((mesh,index) => entry.meshNames?.[index] ? {...mesh,name:entry.meshNames[index]} : mesh)} : entry;
        const group = new THREE.Group();
        group.name = source.name;
        if (source.attachmentMatrix) group.matrix.fromArray(source.attachmentMatrix);
        group.matrixAutoUpdate = false;
        const bones = source.bones.map(item => {
          const bone = new THREE.Bone();
          bone.name = item.name;
          bone.matrixAutoUpdate = false;
          return bone;
        });
        const evaluator = createPoseEvaluator(source.bones);
        const rest = evaluator.evaluate(0);
        bones.forEach((bone, index) => {
          bone.matrix.fromArray(rest.localMatrices[index]);
          const parent = source.bones[index].parent;
          (parent >= 0 ? bones[parent] : group).add(bone);
        });
        const model = {source, group, bones, meshes: [], overlays: [], materialBindings: [], evaluator, skeleton: bones.length ? new THREE.Skeleton(bones) : null};
        this.models.push(model);
        lookup.set(source.id, model);
        boneCount += bones.length;
        for (const [meshIndex, data] of source.meshes.entries()) {
          if (!data.positions.length) continue;
          let geometry = geometryCache.get(data.positions);
          const skinned = bones.length && data.skinIndices?.length && data.skinWeights?.length;
          if (!geometry) {
          geometry = new THREE.BufferGeometry();
          geometry.setAttribute('position', attribute(data.positions, 3, Float32Array));
          geometry.setIndex(ArrayBuffer.isView(data.indices) ? new THREE.BufferAttribute(data.indices, 1) : data.indices);
          if (data.normals?.length === data.positions.length) geometry.setAttribute('normal', attribute(data.normals, 3, Float32Array));
          else geometry.computeVertexNormals();
          const uvCount = data.positions.length / 3 * 2;
          if (data.uvs?.length === uvCount) geometry.setAttribute('uv', attribute(data.uvs, 2, Float32Array));
          if (data.uvs2?.length === uvCount) geometry.setAttribute('uv1', attribute(data.uvs2, 2, Float32Array));
          if (skinned) {
            geometry.setAttribute('skinIndex', attribute(data.skinIndices, 4, Uint16Array));
            geometry.setAttribute('skinWeight', attribute(data.skinWeights, 4, Float32Array));
          }
          geometry.computeBoundingSphere();
          geometryCache.set(data.positions, geometry);
          }
          const material = mapNeutral || new THREE.MeshStandardMaterial({
            color: 0xb5b3bc, roughness: 0.7, metalness: 0.08, side: THREE.DoubleSide,
            // Offset filled surfaces only while their wire overlay is shown.
            polygonOffset: this.options.wireframe, polygonOffsetFactor: 1, polygonOffsetUnits: 1,
          });
          if (this._staticScene) mapNeutral = material;
          const mesh = skinned ? new THREE.SkinnedMesh(geometry, material) : new THREE.Mesh(geometry, material);
          mesh.name = data.name;
          // Rest bounds are not safe for animated limbs or root motion.
          mesh.frustumCulled = !skinned;
          model.meshes.push(mesh);
          model.materialBindings.push({id: String(data.sourceMeshIndex ?? meshIndex), mesh, data, neutral: material, textured: null});
          group.add(mesh);
          const wireMaterial = mapWire || new THREE.MeshBasicMaterial({
            color: 0x000000, wireframe: true, wireframeLinewidth: 1,
            side: THREE.FrontSide, depthTest: true, depthWrite: false, toneMapped: false,
          });
          if (this._staticScene) mapWire = wireMaterial;
          const overlay = skinned ? new THREE.SkinnedMesh(geometry, wireMaterial) : new THREE.Mesh(geometry, wireMaterial);
          overlay.name = `${data.name} (wire overlay)`;
          overlay.visible = this.options.wireframe;
          overlay.frustumCulled = !skinned;
          // Draw all shaded surfaces first: their depth hides back-side edges.
          overlay.renderOrder = 1;
          model.overlays.push(overlay);
          group.add(overlay);
          vertices += data.positions.length / 3;
          // Count each source mesh once, excluding wire overlays and helpers.
          polygons += Math.floor(data.indices.length / 3);
          meshes++;
        }
        if (model.skeleton) {
          // Build each helper before attaching child models. Otherwise a parent
          // helper would also draw a hidden child's bones a second time.
          const helper = new THREE.SkeletonHelper(group);
          helper.setColors(new THREE.Color(0xce9fe4), new THREE.Color(0x72dadd));
          helper.visible = this.options.skeleton;
          helper.material.depthTest = false;
          helper.material.transparent = true;
          helper.renderOrder = 2;
          this.scene.add(helper);
          model.helper = helper;
        }
      }
      for (const model of this.models) {
        const parent = lookup.get(model.source.parentId);
        const socketBone = parent?.bones.find(bone => bone.name === model.source.attachmentBone);
        (socketBone || parent?.group || this.assetRoot).add(model.group);
      }
      this.assetRoot.updateMatrixWorld(true);
      for (const model of this.models) {
        if (!model.skeleton) continue;
        model.skeleton.calculateInverses();
        model.meshes.forEach((mesh, index) => {
          if (!mesh.isSkinnedMesh) return;
          mesh.bind(model.skeleton, mesh.matrixWorld);
          model.overlays[index].bind(model.skeleton, mesh.bindMatrix);
        });
      }
      this.setFrame(0);
      this.frameModel();
      if (this._staticScene) {
        // All map instance transforms have now been composed, including nested
        // entity attachments. They remain immutable in the preview.
        this.assetRoot.traverse(object => {
          object.matrixAutoUpdate = false;
          object.matrixWorldAutoUpdate = false;
        });
        // Placements sharing geometry and material draw as one instanced batch.
        this._mapBatches = new MapBatches(this.scene);
        for (const model of this.models) for (const mesh of [...model.meshes, ...model.overlays]) this._mapBatches.add(mesh);
        this._mapBatches.sync();
      }
      this._loadedAsset = asset;
      this._updatePOVCamera();
      return {models: this.models.length, meshes, bones: boneCount, vertices, polygons};
    } catch (error) {
      this._clear();
      throw error;
    }
  }

  async loadMaterials(asset, resolveTexture, libraryOptions = {}) {
    const cancelled = () => ({applied: 0, texturedMeshes: 0, warnings: [], cancelled: true});
    if (asset !== this._loadedAsset) return cancelled();
    const generation = this._assetGeneration;
    // A retry on the same entity also invalidates any pending earlier request.
    this._materialLibrary?.dispose();
    for (const model of this.models) for (const binding of model.materialBindings) {
      binding.textured = null;
      binding.mesh.material = binding.neutral;
    }
    this.invalidate();
    const library = new MaterialLibrary(resolveTexture, {
      maxAnisotropy: this.renderer?.capabilities.getMaxAnisotropy() || 1, renderer: this.renderer,
      ...libraryOptions,
    });
    this._materialLibrary = library;
    const current = () => generation === this._assetGeneration && library === this._materialLibrary;
    let applied = 0, texturedMeshes = 0;
    await Promise.all(this.models.flatMap(model => model.materialBindings.map(async binding => {
      const descriptors = model.source.materials;
      if (!descriptors || !Object.hasOwn(descriptors, binding.data.materialKey)) return;
      const uvChannels = [];
      if (binding.mesh.geometry.hasAttribute('uv')) uvChannels.push(0);
      if (binding.mesh.geometry.hasAttribute('uv1')) uvChannels.push(1);
      let result;
      try { result = await library.createMaterial(descriptors[binding.data.materialKey], uvChannels); }
      catch (error) {
        if (!current()) return;
        throw error;
      }
      if (!current() || !result) return;
      await this._precompile(result.material, binding.mesh);
      if (!current()) return;
      binding.textured = result.material;
      result.material.polygonOffset = this.options.wireframe;
      binding.mesh.material = this.options.textured ? result.material : binding.neutral;
      this.invalidate();
      applied++;
      texturedMeshes += Number(result.hasTextures);
    })));
    const warnings = library.warnings;
    if (this._environmentWarning && applied) warnings.push(this._environmentWarning);
    return current() ? {applied, texturedMeshes, warnings} : cancelled();
  }

  // Compile a material's shader in the background before it is first drawn.
  // Otherwise the first frame that uses each new material variant blocks while
  // the GPU driver compiles it (slow with ANGLE on Windows). One compile per
  // material; renderers without compileAsync just skip this.
  _precompile(material, mesh) {
    if (!this.renderer?.compileAsync) return Promise.resolve();
    this._compiled ||= new WeakMap();
    if (!this._compiled.has(material)) {
      const probe = mesh.isSkinnedMesh ? new THREE.SkinnedMesh(mesh.geometry, material)
        // Batched map meshes are drawn with the instancing shader variant.
        : this._mapBatches?.batchable(mesh, material) ? new THREE.InstancedMesh(mesh.geometry, material, 1)
        : new THREE.Mesh(mesh.geometry, material);
      if (mesh.isSkinnedMesh) probe.bind(mesh.skeleton, mesh.bindMatrix);
      this._compiled.set(material, this.renderer.compileAsync(probe, this.camera, this.scene).catch(() => {}));
    }
    return this._compiled.get(material);
  }

  getRigs() {
    return this.models.filter(model => model.bones.length).map(model => ({id: model.source.id, name: model.source.name, bones: model.bones.length}));
  }

  getObjects() {
    return this.models.map(model => {
      const meshes = model.materialBindings.map(binding => ({
        id: binding.id, name: binding.mesh.name || `Mesh ${Number(binding.id) + 1}`,
        modelId: model.source.id,
        visible: binding.mesh.visible, polygons: Math.floor(binding.data.indices.length / 3),
      }));
      const visible = meshes.every(mesh => mesh.visible);
      return {id: model.source.id, modelId: model.source.id, name: model.source.name, parentId:model.source.parentId || null, attachmentBone:model.source.attachmentBone || null,
        boneHierarchy:model.source.bones.map((bone,index) => ({id:String(index),name:bone.name,parentIndex:bone.parent})),
        bones: model.bones.length, visible,
        indeterminate: !visible && meshes.some(mesh => mesh.visible), meshes};
    });
  }

  _syncObjectVisibility(model) {
    // Only renderables change visibility. Hidden model groups and their bones
    // must continue updating transforms for visible attached objects.
    model.overlays.forEach((overlay, index) => {
      overlay.visible = this.options.wireframe && model.meshes[index].visible;
    });
    if (model.helper) model.helper.visible = this.options.skeleton &&
      (!model.meshes.length || model.meshes.some(mesh => mesh.visible));
  }

  setObjectVisible(modelId, meshId, visible) {
    const model = this.models.find(item => item.source.id === modelId);
    if (!model) return false;
    const bindings = meshId == null ? model.materialBindings :
      model.materialBindings.filter(binding => binding.id === String(meshId));
    if (!bindings.length) return false;
    for (const binding of bindings) binding.mesh.visible = Boolean(visible);
    this._syncObjectVisibility(model);
    this.invalidate();
    return true;
  }

  setAllObjectsVisible(visible) {
    for (const model of this.models) {
      for (const mesh of model.meshes) mesh.visible = Boolean(visible);
      this._syncObjectVisibility(model);
    }
    this.invalidate();
  }

  getVisiblePolygonCount() {
    let polygons = 0;
    for (const model of this.models) for (const binding of model.materialBindings) {
      if (binding.mesh.visible) polygons += Math.floor(binding.data.indices.length / 3);
    }
    return polygons;
  }

  setRig(id = 'auto') {
    this.rig = id;
    return this.setClip(this.clip);
  }

  setClip(clip) {
    const clipBones = clip?.bones || [];
    const names = new Set(clipBones.map(bone => bone.name));
    const scores = this.models.map(model => ({model, matched: model.bones.reduce((sum, bone) => sum + Number(names.has(bone.name)), 0)}));
    const selected = this.rig === 'auto'
      ? scores.filter(item => item.model.bones.length).sort((a, b) => b.matched - a.matched || b.model.bones.length - a.model.bones.length)[0]
      : scores.find(item => item.model.source.id === this.rig);
    const evaluators = this.models.map(model => createPoseEvaluator(model.source.bones, model === selected?.model ? clipBones : []));
    if (clip) this._enableAnimatedTransforms();
    this.clip = clip;
    this.assignments = clip && selected ? [{modelId: selected.model.source.id, clip}] : [];
    this.activeModelId = selected?.model.source.id || null;
    // Legacy single-clip callers pass native frame numbers, including clips
    // without metadata in the pose tests. Explicit assignments use seconds.
    this._assignmentFps = null;
    this.models.forEach((model, index) => { model.evaluator = evaluators[index]; model.assignedClip = null; });
    this.setFrame(0);
    return {matched: selected?.matched || 0, total: clipBones.length, rigName: selected?.model.source.name || 'No skeleton'};
  }

  setAssignments(entries = [], activeModelId = null) {
    if (!Array.isArray(entries)) throw new Error('Animation assignments must be a list.');
    const lookup = new Map(this.models.map(model => [model.source.id, model]));
    const active = activeModelId == null ? null : lookup.get(activeModelId);
    if (activeModelId != null && !active?.bones.length) throw new Error('Choose an object with a skeleton.');
    const clips = new Map(), summaries = [];
    for (const entry of entries) {
      const model = lookup.get(entry?.modelId), clip = entry?.clip;
      if (!model?.bones.length) throw new Error('An animation target is not a loaded skeleton.');
      if (clips.has(entry.modelId)) throw new Error('Each object can have only one animation.');
      if (!clip || !Array.isArray(clip.bones) || !Number.isFinite(clip.fps) || clip.fps <= 0) {
        throw new Error('An assigned animation has invalid frame-rate or track data.');
      }
      const names = new Set(clip.bones.map(bone => bone.name));
      const matched = model.bones.reduce((sum, bone) => sum + Number(names.has(bone.name)), 0);
      clips.set(entry.modelId, clip);
      summaries.push({modelId: entry.modelId, matched, total: clip.bones.length, rigName: model.source.name});
    }
    // Build every evaluator before changing the visible state. A malformed
    // track cannot clear or partially replace the user's existing assignments.
    const evaluators = this.models.map(model => createPoseEvaluator(model.source.bones, clips.get(model.source.id)?.bones || []));
    if (entries.length) this._enableAnimatedTransforms();
    const activeClip = clips.get(activeModelId) || null;
    const fps = activeClip?.fps || entries[0]?.clip.fps || null;
    this.assignments = entries.map(({modelId, clip}) => ({modelId, clip}));
    this.activeModelId = activeModelId;
    this.clip = activeClip;
    this.rig = activeModelId || 'auto';
    this._assignmentFps = fps;
    this.models.forEach((model, index) => {
      model.evaluator = evaluators[index];
      model.assignedClip = clips.get(model.source.id) || null;
    });
    this.setFrame(0);
    const selected = summaries.find(item => item.modelId === activeModelId);
    return {...(selected || {modelId: activeModelId, matched: 0, total: 0, rigName: active?.source.name || 'No skeleton'}), assignments: summaries};
  }

  setFrame(frame, timelineFps = null) {
    if (timelineFps !== null && (!Number.isFinite(timelineFps) || timelineFps <= 0)) {
      throw new Error('The shared animation timeline requires a positive frame rate.');
    }
    this.frame = frame;
    this._timelineFps = timelineFps;
    if (this._staticScene) { this.invalidate(); return; }
    const sampleFps = timelineFps ?? this._assignmentFps;
    for (const model of this.models) {
      const clip = model.assignedClip;
      let sample = sampleFps && clip ? Math.max(0, frame) / sampleFps * clip.fps : frame;
      const lastFrame = Number.isFinite(clip?.lastFrame) ? clip.lastFrame
        : Number.isFinite(clip?.durationFrames) ? clip.durationFrames - 1 : null;
      if (lastFrame !== null) sample = Math.min(sample, Math.max(0, lastFrame));
      const pose = model.evaluator.evaluate(sample, {lockRoot: this.options.lockRoot});
      for (let i = 0; i < model.bones.length; i++) model.bones[i].matrix.fromArray(pose.localMatrices[i]);
    }
    this.assetRoot.updateMatrixWorld(true);
    this._updatePOVCamera();
    this.invalidate();
  }

  _povCameraBone() {
    if (this._mapScene) return null;
    const byId = new Map(this.models.map(model => [model.source.id, model]));
    // Selecting an attached weapon keeps the camera on its owning arms rig.
    let selected = byId.get(this.activeModelId);
    const visited = new Set();
    while (selected && !visited.has(selected)) {
      visited.add(selected);
      const camera = selected.bones.find(isCameraBone);
      if (camera) return camera;
      selected = byId.get(selected.source.parentId);
    }
    for (const model of this.models) {
      const camera = model.bones.find(isCameraBone);
      if (camera) return camera;
    }
    return null;
  }

  getPOVStatus() {
    return {available: Boolean(this._povCameraBone()), active: Boolean(this._povOrbitState)};
  }

  _restoreOrbitCamera() {
    const previous = this._povOrbitState;
    if (!previous) return;
    this.camera.position.copy(previous.position);
    this.camera.quaternion.copy(previous.quaternion);
    this.camera.up.copy(previous.up);
    for (const key of ['fov', 'near', 'far', 'zoom']) this.camera[key] = previous[key];
    this.camera.updateProjectionMatrix();
    this.camera.updateMatrixWorld(true);
    this.controls.target.copy(previous.target);
    this.controls.enabled = previous.enabled && this._active !== false;
    this._povOrbitState = null;
    this.invalidate();
  }

  _updatePOVCamera() {
    // loadAsset frames the ordinary orbit view before entering a requested POV.
    const bone = this.options.pov && this._loadedAsset && this._povCameraBone();
    if (!bone) { this._restoreOrbitCamera(); return; }
    if (!this._povOrbitState) {
      // Drain old orbit inertia before preserving the view to return to.
      const damping = this.controls.enableDamping;
      this.controls.enableDamping = false;
      this.controls.update();
      this.controls.enableDamping = damping;
      this._povOrbitState = {
        position: this.camera.position.clone(), quaternion: this.camera.quaternion.clone(),
        up: this.camera.up.clone(), target: this.controls.target.clone(), enabled: this.controls.enabled !== false,
        fov: this.camera.fov, near: this.camera.near, far: this.camera.far, zoom: this.camera.zoom,
      };
      this.camera.fov = POV_FOV;
      this.camera.zoom = 1;
      this.camera.near = .001;
      this.camera.far = Math.max(this.camera.far, 1000);
      this.camera.updateProjectionMatrix();
      this._handleKeyUp();
    }
    this.controls.enabled = false;
    bone.getWorldPosition(this.camera.position);
    bone.getWorldQuaternion(this.camera.quaternion);
    this.camera.quaternion.multiply(POV_CAMERA_BASIS).normalize();
    this.camera.updateMatrixWorld(true);
  }

  _enableAnimatedTransforms() {
    if (!this._staticScene) return;
    this._staticScene = false;
    // Animated transforms change world matrices: draw each mesh itself again.
    this._mapBatches?.dispose();
    this._mapBatches = null;
    this.assetRoot.traverse(object => { object.matrixWorldAutoUpdate = true; });
  }

  setOptions(options) {
    Object.assign(this.options, options);
    this.grid.visible = this.options.grid;
    for (const model of this.models) {
      this._syncObjectVisibility(model);
      for (const binding of model.materialBindings) {
        binding.neutral.polygonOffset = this.options.wireframe;
        if (binding.textured) binding.textured.polygonOffset = this.options.wireframe;
        binding.mesh.material = this.options.textured && binding.textured ? binding.textured : binding.neutral;
      }
    }
    this.setFrame(this.frame, this._timelineFps);
  }

  frameModel({all = false} = {}) {
    if (!this.models.length) return;
    if (this._povOrbitState) { this._updatePOVCamera(); this.invalidate(); return; }
    const fullBounds = new THREE.Box3(), placements = [];
    for (const model of this.models) {
      const placement = new THREE.Box3();
      for (const mesh of model.meshes) {
        if (!mesh.visible) continue;
        if (mesh.isSkinnedMesh) mesh.computeBoundingBox();
        placement.expandByObject(mesh);
      }
      if (!placement.isEmpty()) { placements.push(placement); fullBounds.union(placement); }
    }
    if (fullBounds.isEmpty()) {
      // A skeleton-only asset can still be framed. Hidden mesh objects should
      // not pull the camera back toward their invisible bones.
      for (const model of this.models) {
        if (model.meshes.length) continue;
        for (const bone of model.bones) fullBounds.expandByPoint(bone.getWorldPosition(new THREE.Vector3()));
      }
    }
    if (fullBounds.isEmpty()) return;
    const bounds = this._mapScene && !all ? mapContentBounds(placements, fullBounds) : fullBounds;
    this._framingBounds = bounds.clone();
    const center = bounds.getCenter(new THREE.Vector3());
    const size = bounds.getSize(new THREE.Vector3());
    const diameter = Math.max(size.x, size.y, size.z, 0.1);
    const fov = THREE.MathUtils.degToRad(this.camera.fov);
    const distance = diameter / (2 * Math.tan(fov / 2)) * 1.45 / Math.min(1, this.camera.aspect);
    // Finish any pending damped pan/orbit before replacing the view. Otherwise
    // the old gesture keeps moving this newly framed camera down/sideways.
    // A non-damped update drains OrbitControls through its public API, without
    // depending on vendor internals or changing the normal navigation feel.
    const damping = this.controls.enableDamping;
    this.controls.enableDamping = false;
    this.controls.update();
    // Maps open from above so their surrounding skyline does not cover the
    // streets. Character previews retain their familiar near-horizontal view.
    const direction = this._mapScene ? new THREE.Vector3(0.45, 1.5, 1) : new THREE.Vector3(0.45, 0.22, 1);
    this.camera.position.copy(center).add(direction.normalize().multiplyScalar(distance));
    this.controls.target.copy(center);
    this.camera.near = Math.max(diameter / 1000, 0.0001);
    this.camera.far = Math.max(diameter * 1000, 100);
    this.camera.updateProjectionMatrix();
    // A map may be hundreds of metres across. A percentage of its full width
    // prevented approaching a doorway, even though its geometry was correct.
    this.controls.minDistance = this._mapScene ? Math.max(diameter * 1e-6, .0001) : diameter * 0.015;
    this.controls.maxDistance = diameter * 100;
    this.controls.update();
    this.controls.enableDamping = damping;
    if (this._mapScene) {
      this._mapBounds = fullBounds.clone();
      this._updateCameraClipping();
      // Twenty subdivisions span the map with sensible metre-sized steps.
      // Scaling the ten-unit grid by diameter/2 made every cell map-sized.
      const desiredStep = diameter / 20;
      const magnitude = 10 ** Math.floor(Math.log10(desiredStep));
      const step = ([1, 2, 5, 10].find(value => value * magnitude >= desiredStep) || 10) * magnitude;
      this.grid.scale.setScalar(step * 2);
    } else this.grid.scale.setScalar(diameter / 2);
    this.grid.position.set(center.x, bounds.min.y - diameter * 0.002, center.z);
    this.invalidate();
  }

  _updateCameraClipping() {
    if (!this._mapScene || !this._mapBounds) return;
    const size = this._mapBounds.getSize(new THREE.Vector3());
    const center = this._mapBounds.getCenter(new THREE.Vector3());
    const radius = Math.max(size.length() / 2, .01);
    const distance = this.camera.position.distanceTo(this.controls.target);
    const near = Math.max(.0001, Math.min(radius * 1e-4, distance * .001));
    // Keep the full scene behind the far plane even when panning away from it,
    // while avoiding the old map-width * 1000 depth range.
    const far = Math.max(radius * 2, this.camera.position.distanceTo(center) + radius * 1.1, near * 100);
    if (near !== this.camera.near || far !== this.camera.far) {
      this.camera.near = near; this.camera.far = far;
      this.camera.updateProjectionMatrix();
    }
  }
}
