import { Matrix4, Quaternion, Vector3 } from './vendor/three.module.js';

// A singular parent cannot express an unscaled child through Object3D's matrix
// hierarchy. Keep almost-flat axes invertible; ordinary scales are unchanged.
export const MIN_SCALE_MAGNITUDE = 1e-6;

function vector(values, fallback, label) {
  const result = values == null ? fallback.slice() : Array.from(values);
  if (result.length !== fallback.length || !result.every(Number.isFinite)) {
    throw new Error(`Invalid ${label}`);
  }
  return result;
}

function safeScale(value) {
  return Math.abs(value) < MIN_SCALE_MAGNITUDE
    ? (value < 0 ? -MIN_SCALE_MAGNITUDE : MIN_SCALE_MAGNITUDE)
    : value;
}

function prepareTrack(track, components, restQuaternion, label) {
  if (!track || !track.frames?.length) return null;
  if (!track.values || track.values.length !== track.frames.length * components) {
    throw new Error(`Invalid ${label}: frame and value counts differ`);
  }
  const values = Array.from(track.values);
  const keys = Array.from(track.frames, (frame, index) => {
    if (!Number.isFinite(frame)) throw new Error(`Invalid ${label}: nonfinite frame`);
    const value = vector(
      values.slice(index * components, (index + 1) * components),
      new Array(components).fill(0), label,
    );
    if (components === 4) {
      const q = new Quaternion(...value).normalize();
      // BLAnimation converts each absolute key through a rest-basis matrix.
      // Blender canonicalizes the resulting quaternion to basis W >= 0.
      // dot(restQ, keyQ) is W of inverse(restQ) * keyQ.
      const sign = restQuaternion.dot(q) < 0 ? -1 : 1;
      q.toArray(value);
      for (let i = 0; i < 4; i++) value[i] *= sign;
    }
    return { frame, value };
  }).sort((a, b) => a.frame - b.frame);
  // Be deterministic for duplicate frames: the last source key wins.
  const unique = [];
  for (const key of keys) {
    if (unique.length && unique.at(-1).frame === key.frame) unique[unique.length - 1] = key;
    else unique.push(key);
  }
  return { keys: unique, components };
}

function sampleTrack(track, frame, target) {
  if (!track) return false;
  const { keys, components } = track;
  let a = keys[0], b = a, alpha = 0;
  if (frame >= keys.at(-1).frame) a = b = keys.at(-1);
  else if (frame > a.frame) {
    let low = 0, high = keys.length - 1;
    while (high - low > 1) {
      const middle = (low + high) >> 1;
      if (keys[middle].frame <= frame) low = middle;
      else high = middle;
    }
    a = keys[low]; b = keys[high];
    alpha = (frame - a.frame) / (b.frame - a.frame);
  }
  const x = a.value[0] + (b.value[0] - a.value[0]) * alpha;
  const y = a.value[1] + (b.value[1] - a.value[1]) * alpha;
  const z = a.value[2] + (b.value[2] - a.value[2]) * alpha;
  if (components === 4) {
    target.set(x, y, z, a.value[3] + (b.value[3] - a.value[3]) * alpha).normalize();
  } else target.set(x, y, z);
  return true;
}

/**
 * Create an absolute OW clip evaluator in raw Y-up model units.
 *
 * bones: {name, parent, pos:[x,y,z], rot:[x,y,z,w], scale:[1,1,1]}[]
 * clipBones: {name, positions?, rotations?, scales?}[], where each track is
 * {frames:[...], values:[flat components...]}.
 *
 * Source rest scale is intentionally ignored, like upstream BLModel. Missing
 * channels always use rest P/Q and unit S. Parent scale changes child position,
 * but never propagates into child orientation or scale (Blender NONE mode).
 *
 * evaluate() reuses its returned arrays; copy them to retain previous samples.
 * localMatrices are suitable for nested Three Bones with matrixAutoUpdate=false.
 */
export function createPoseEvaluator(bones, clipBones = []) {
  const count = bones.length;
  const children = Array.from({ length: count }, () => []);
  const roots = [];
  const parents = bones.map((bone, index) => {
    const parent = bone.parent ?? -1;
    if (!Number.isInteger(parent) || parent < -1 || parent >= count || parent === index) {
      throw new Error(`Invalid skeleton parent for ${bone.name ?? index}`);
    }
    if (parent < 0) roots.push(index);
    else children[parent].push(index);
    return parent;
  });
  const order = roots.slice();
  for (let cursor = 0; cursor < order.length; cursor++) order.push(...children[order[cursor]]);
  if (order.length !== count) throw new Error('Invalid skeleton: parent cycle');

  const clipByName = new Map(clipBones.map(bone => [bone.name, bone]));
  const states = bones.map(bone => {
    const restPosition = new Vector3(...vector(bone.pos, [0, 0, 0], `${bone.name} rest position`));
    const restQuaternion = new Quaternion(...vector(bone.rot, [0, 0, 0, 1], `${bone.name} rest rotation`)).normalize();
    const clip = clipByName.get(bone.name) || {};
    return {
      restPosition, restQuaternion,
      position: new Vector3(), quaternion: new Quaternion(), scale: new Vector3(),
      worldQuaternion: new Quaternion(), world: new Matrix4(), local: new Matrix4(),
      positions: prepareTrack(clip.positions, 3, restQuaternion, `${bone.name} positions`),
      rotations: prepareTrack(clip.rotations, 4, restQuaternion, `${bone.name} rotations`),
      scales: prepareTrack(clip.scales, 3, restQuaternion, `${bone.name} scales`),
    };
  });
  const result = {
    localMatrices: states.map(state => state.local.elements),
    worldMatrices: states.map(state => state.world.elements),
  };
  const inverseParent = new Matrix4();

  return {
    evaluate(frame, { lockRoot = false } = {}) {
      if (!Number.isFinite(frame)) throw new Error('Animation frame must be finite');
      for (const index of order) {
        const state = states[index];
        const { position, quaternion, scale, restPosition, restQuaternion } = state;
        const hasPosition = sampleTrack(state.positions, frame, position);
        if (!hasPosition) position.copy(restPosition);
        if (!sampleTrack(state.rotations, frame, quaternion)) quaternion.copy(restQuaternion);
        if (!sampleTrack(state.scales, frame, scale)) scale.set(1, 1, 1);
        scale.set(safeScale(scale.x), safeScale(scale.y), safeScale(scale.z));

        const parentIndex = parents[index];
        if (parentIndex < 0) {
          // Upstream's root position keys are rest-basis offsets; child keys
          // are complete parent-local positions.
          if (hasPosition) position.applyQuaternion(restQuaternion).add(restPosition);
          if (lockRoot) { position.x = restPosition.x; position.z = restPosition.z; }
          state.worldQuaternion.copy(quaternion);
        } else {
          const parent = states[parentIndex];
          position.applyMatrix4(parent.world);
          state.worldQuaternion.copy(parent.worldQuaternion).multiply(quaternion).normalize();
        }
        state.world.compose(position, state.worldQuaternion, scale);
        if (parentIndex < 0) state.local.copy(state.world);
        else {
          inverseParent.copy(states[parentIndex].world).invert();
          state.local.multiplyMatrices(inverseParent, state.world);
        }
      }
      return result;
    },
  };
}
