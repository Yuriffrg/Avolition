// SPDX-License-Identifier: GPL-3.0-or-later
import {Euler, MathUtils, Matrix4, Plane, Raycaster, Sphere, Vector2, Vector3} from './vendor/three.module.js';

// Static map placements retain their geometry and transforms when their draw calls are batched.
export function pickPreviewSurface(view, ndc) {
  const {camera, grid} = view;
  camera.updateMatrixWorld(true);
  const caster = new Raycaster();
  caster.setFromCamera(ndc, camera);
  const {ray} = caster;
  const forward = camera.getWorldDirection(new Vector3()).dot(ray.direction);
  caster.near = camera.near / forward;
  caster.far = camera.far / forward;
  const sphere = new Sphere(), offset = new Vector3(), candidates = [];
  for (const model of view.models) {
    if (!model.group.visible) continue;
    for (const mesh of model.meshes) {
      if (!mesh.visible) continue;
      if (mesh.isSkinnedMesh) {
        mesh.updateWorldMatrix(true, false);
        mesh.computeBoundingSphere();
        if (mesh.boundingBox) mesh.computeBoundingBox();
        sphere.copy(mesh.boundingSphere);
      } else {
        if (!mesh.geometry.boundingSphere) mesh.geometry.computeBoundingSphere();
        sphere.copy(mesh.geometry.boundingSphere);
      }
      sphere.applyMatrix4(mesh.matrixWorld);
      offset.subVectors(sphere.center, ray.origin);
      const along = offset.dot(ray.direction);
      const chordSquared = sphere.radius * sphere.radius - offset.cross(ray.direction).lengthSq();
      if (chordSquared < 0) continue;
      const halfChord = Math.sqrt(chordSquared);
      if (along + halfChord >= caster.near && along - halfChord <= caster.far)
        candidates.push({mesh, near: along - halfChord});
    }
  }
  candidates.sort((a,b) => a.near - b.near);
  let nearest = null;
  const hits = [];
  for (const {mesh, near} of candidates) {
    if (near > caster.far) break;
    hits.length = 0;
    mesh.raycast(caster, hits);
    for (const hit of hits) {
      if (hit.distance <= caster.far) {
        nearest = hit.point;
        caster.far = hit.distance;
      }
    }
  }
  if (nearest || view._mapScene || !grid.visible) return nearest;
  grid.updateWorldMatrix(true, false);
  if (!grid.geometry.boundingBox) grid.geometry.computeBoundingBox();
  const localRay = ray.clone().applyMatrix4(new Matrix4().copy(grid.matrixWorld).invert());
  const local = localRay.intersectPlane(new Plane(new Vector3(0,1,0),0), new Vector3());
  if (!local || !grid.geometry.boundingBox.containsPoint(local.setY(0))) return null;
  const world = local.applyMatrix4(grid.matrixWorld), distance = world.distanceTo(ray.origin);
  return distance >= caster.near && distance <= caster.far ? world : null;
}

export class PivotNavigation {
  constructor(view) {
    this.view = view;
    this.canvas = view.canvas;
    this.camera = view.camera;
    this.window = this.canvas.ownerDocument.defaultView;
    this.marker = this.canvas.ownerDocument.createElement('span');
    this.marker.className = 'preview-pivot';
    this.marker.hidden = true;
    this.marker.setAttribute('aria-hidden', 'true');
    this.canvas.parentElement.append(this.marker);
    this.listeners = new AbortController();
    const listen = (element, type, handler, options = {}) =>
      element.addEventListener(type, handler.bind(this), {...options, signal: this.listeners.signal});
    // The stock controls continue to own touch gestures and modified shortcuts.
    listen(this.canvas, 'pointerdown', this.down, {capture:true});
    listen(this.canvas, 'pointermove', this.move);
    listen(this.canvas, 'pointerup', this.endPointer);
    listen(this.canvas, 'pointercancel', this.endPointer);
    listen(this.canvas, 'lostpointercapture', this.endPointer);
    listen(this.canvas, 'wheel', this.wheel, {capture:true, passive:false});
    listen(this.window, 'blur', this.cancel);
  }

  depth() {
    return this.camera.getWorldDirection(new Vector3()).dot(this.pivot.clone().sub(this.camera.position));
  }

  choose(event) {
    const {camera, view} = this;
    const rect = this.canvas.getBoundingClientRect();
    if (!rect.width || !rect.height) return false;
    const ndc = new Vector2((event.clientX-rect.left)*2/rect.width-1, 1-(event.clientY-rect.top)*2/rect.height);
    const controls = view.controls;
    const damping = controls.enableDamping;
    controls.enableDamping = false;
    controls.update();
    controls.enableDamping = damping;
    const previous = this.pivot && this.depth();
    const hit = pickPreviewSurface(view,ndc);
    const distance = previous > camera.near ? previous :
      MathUtils.clamp(camera.position.distanceTo(controls.target), camera.near*4, camera.far*.8);
    this.pivot = hit || camera.getWorldDirection(new Vector3()).multiplyScalar(distance).add(camera.position);
    this.showPivot();
    return true;
  }

  showPivot() {
    const projected = this.pivot.clone().project(this.camera);
    this.marker.style.left = ((projected.x+1)*50)+'%';
    this.marker.style.top = ((1-projected.y)*50)+'%';
    this.marker.hidden = projected.z < -1 || projected.z > 1;
  }

  down(event) {
    if (this.drag) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    if (event.pointerType === 'touch' || event.isPrimary === false || event.ctrlKey || event.metaKey ||
        !this.view.controls.enabled || !this.view._canUseCanvas()) return;
    const buttonMode = ['orbit','pan','dolly'][event.button];
    if (!buttonMode) return;
    const keyMode = {1:'pan',2:'dolly',3:'orbit'}[this.view._navigationKey];
    if (event.shiftKey && !event.altKey && !keyMode) return;
    const mode = !event.altKey && event.button === 0 && keyMode ? keyMode : buttonMode;
    this.cancelWheel();
    if (!this.choose(event)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    this.canvas.focus({preventScroll:true});
    this.canvas.setPointerCapture(event.pointerId);
    this.drag = {id:event.pointerId, mode, x:event.clientX, y:event.clientY};
  }

  move(event) {
    if (!this.drag || this.drag.id !== event.pointerId) return;
    if (!this.view.controls.enabled || !this.view._canUseCanvas()) { this.cancel(); return; }
    const dx = event.clientX-this.drag.x, dy = event.clientY-this.drag.y;
    this.drag.x = event.clientX;
    this.drag.y = event.clientY;
    if (!dx && !dy) return;
    const {camera, pivot} = this;
    if (this.drag.mode === 'orbit') {
      const oldInverse = camera.quaternion.clone().invert();
      const rotation = new Euler().setFromQuaternion(camera.quaternion,'YXZ');
      const speed = Math.PI*1.2 / this.canvas.clientHeight;
      rotation.x = MathUtils.clamp(rotation.x-dy*speed, -Math.PI*.4999, Math.PI*.4999);
      rotation.y -= dx*speed;
      camera.quaternion.setFromEuler(rotation);
      camera.position.sub(pivot).applyQuaternion(oldInverse.premultiply(camera.quaternion)).add(pivot);
    } else if (this.drag.mode === 'pan') {
      const unitsPerPixel = 2*this.depth()*Math.tan(MathUtils.degToRad(camera.getEffectiveFOV())/2) / this.canvas.clientHeight;
      camera.position.add(new Vector3(-dx,dy,0).multiplyScalar(unitsPerPixel).applyQuaternion(camera.quaternion));
    } else {
      this.dolly((dy-dx)*.008);
    }
    this.updated();
  }

  dolly(amount) {
    const depth = this.depth();
    if (depth <= 0) return;
    const near = this.camera.near*1.05, far = this.camera.far*.98;
    const scale = MathUtils.clamp(Math.exp(MathUtils.clamp(amount,-3,3)),near/depth,far/depth);
    this.camera.position.sub(this.pivot).multiplyScalar(scale).add(this.pivot);
  }

  updated() {
    const {camera, view} = this;
    camera.updateMatrixWorld(true);
    // OrbitControls looks at target on each frame. Keep it on the optical axis
    // so an off-center surface can stay under the cursor throughout the gesture.
    const distance = MathUtils.clamp(this.depth(), view.controls.minDistance, view.controls.maxDistance);
    camera.getWorldDirection(view.controls.target).multiplyScalar(distance).add(camera.position);
    this.showPivot();
    view.invalidate();
  }

  wheel(event) {
    if (this.drag) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    if (event.ctrlKey || event.metaKey || !event.deltaY || !this.view.controls.enabled || !this.view._canUseCanvas()) return;
    if (!this.wheelPoint || Math.hypot(event.clientX-this.wheelPoint.x,event.clientY-this.wheelPoint.y)>4) {
      if (!this.choose(event)) return;
      this.wheelPoint = {x:event.clientX, y:event.clientY};
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    const pixels = event.deltaMode===1 ? 16 : event.deltaMode===2 ? this.canvas.clientHeight : 1;
    this.dolly(event.deltaY*pixels*.0015);
    this.updated();
    this.window.clearTimeout(this.wheelTimer);
    this.wheelTimer = this.window.setTimeout(()=>this.cancelWheel(),180);
  }

  cancelWheel() {
    this.window.clearTimeout(this.wheelTimer);
    this.wheelPoint = null;
    this.marker.hidden = true;
  }

  endPointer(event) {
    if (this.drag?.id === event.pointerId) this.cancel();
  }

  cancel() {
    this.cancelWheel();
    const id = this.drag?.id;
    this.drag = null;
    if (id !== undefined && this.canvas.hasPointerCapture(id)) this.canvas.releasePointerCapture(id);
  }

  reset() {
    this.cancel();
    this.pivot = null;
  }

  dispose() {
    this.cancel();
    this.listeners.abort();
    this.marker.remove();
  }
}
