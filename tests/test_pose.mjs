import assert from 'node:assert/strict';
import fs from 'node:fs';
import { Matrix4, Quaternion, Euler } from '../web/vendor/three.module.js';
import { createPoseEvaluator, MIN_SCALE_MAGNITUDE } from '../web/pose.js';

const fixture = JSON.parse(fs.readFileSync(new URL('./fixtures/playback_fixture.json', import.meta.url), 'utf8'));
const bones = fixture.bones.map(bone => ({
  name: bone.name, parent: bone.parent, pos: bone.position,
  rot: new Quaternion().setFromEuler(new Euler(...bone.rotation, 'ZYX')).toArray(),
  scale: bone.sourceScale, // Deliberately nonunit: the evaluator must ignore these.
}));
const clip = fixture.clip.bones.map(bone => {
  const result = { name: bone.name };
  for (const kind of ['positions', 'rotations', 'scales']) {
    result[kind] = { frames: bone[kind].map(key => key.frame), values: bone[kind].flatMap(key => key.data) };
  }
  return result;
});
let checks = 0, maximumError = 0;
function close(actual, expected, description, tolerance = fixture.matrixTolerance) {
  assert.equal(actual.length, expected.length, description);
  const error = Math.max(0, ...actual.map((x, i) => Math.abs(x - expected[i])));
  assert.ok(actual.every(Number.isFinite), `${description}: nonfinite values`);
  assert.ok(error <= tolerance, `${description}: maximum error ${error}`);
  maximumError = Math.max(maximumError, error);
  checks++;
}
const evaluator = createPoseEvaluator(bones, clip);
for (const sample of fixture.samples) {
  const pose = evaluator.evaluate(sample.frame);
  bones.forEach((bone, index) => {
    close(pose.worldMatrices[index], sample.matrices[bone.name], `Blender frame ${sample.frame} ${bone.name}`);
    const local = new Matrix4().fromArray(pose.localMatrices[index]);
    const assembled = bone.parent < 0 ? local : new Matrix4().fromArray(pose.worldMatrices[bone.parent]).multiply(local);
    close(assembled.elements, pose.worldMatrices[index], `Three hierarchy frame ${sample.frame} ${bone.name}`);
  });
}

// Out-of-range time clamps each channel to its endpoint.
for (const [frame, sample] of [[-100, fixture.samples[0]], [100, fixture.samples.at(-1)]]) {
  const pose = evaluator.evaluate(frame);
  bones.forEach((bone, i) => close(pose.worldMatrices[i], sample.matrices[bone.name], `Clamped frame ${frame} ${bone.name}`));
}

// A new clip with no channels must restore the rest pose after any other clip.
evaluator.evaluate(20);
const restEvaluator = createPoseEvaluator(bones);
const rest = restEvaluator.evaluate(10);
bones.forEach((bone, i) => close(rest.worldMatrices[i], fixture.restMatrices[bone.name], `Clip switch rest ${bone.name}`));

// Seeking backward or repeatedly returns the same pose without accumulated state.
evaluator.evaluate(20);
const repeated = evaluator.evaluate(0);
bones.forEach((bone, i) => close(repeated.worldMatrices[i], fixture.samples[0].matrices[bone.name], `Repeated sample ${bone.name}`));

// Child-before-parent input order preserves all named world matrices.
const order = [2, 3, 1, 0];
const reordered = order.map(i => ({ ...bones[i], parent: bones[i].parent < 0 ? -1 : order.indexOf(bones[i].parent) }));
const reorderPose = createPoseEvaluator(reordered, clip).evaluate(10);
reordered.forEach((bone, i) => close(reorderPose.worldMatrices[i], fixture.samples[2].matrices[bone.name], `Reordered ${bone.name}`));

// Source quaternion sign cannot change the animation after matrix preprocessing.
const antipodal = clip.map(bone => ({ ...bone, rotations: {
  frames: bone.rotations.frames,
  values: bone.rotations.values.map((value, i) => Math.floor(i / 4) % 2 ? -value : value),
} }));
const antipodalPose = createPoseEvaluator(bones, antipodal).evaluate(10);
bones.forEach((bone, i) => close(antipodalPose.worldMatrices[i], fixture.samples[2].matrices[bone.name], `Antipodal signs ${bone.name}`));

// Lock only root X/Z, preserving its animated vertical position.
const unlocked = evaluator.evaluate(10).worldMatrices[0].slice();
const locked = evaluator.evaluate(10, { lockRoot: true }).worldMatrices[0];
close([locked[12], locked[13], locked[14]], [bones[0].pos[0], unlocked[13], bones[0].pos[2]], 'Root horizontal lock', 1e-10);

// A zero-scaled parent must still yield finite, reconstructable local matrices.
const flatBones = [
  { name: 'root', parent: -1, pos: [0, 0, 0], rot: [0, 0, 0, 1] },
  { name: 'child', parent: 0, pos: [1, 2, 3], rot: [0, 0, 0, 1] },
];
const flatPose = createPoseEvaluator(flatBones, [{ name: 'root', scales: { frames: [0], values: [0, -1e-10, 2] } }]).evaluate(0);
close([flatPose.worldMatrices[0][0], flatPose.worldMatrices[0][5], flatPose.worldMatrices[0][10]], [MIN_SCALE_MAGNITUDE, -MIN_SCALE_MAGNITUDE, 2], 'Near-zero signed scales', 1e-12);
close(new Matrix4().fromArray(flatPose.worldMatrices[0]).multiply(new Matrix4().fromArray(flatPose.localMatrices[1])).elements, flatPose.worldMatrices[1], 'Zero-scale child reconstructs', 1e-10);

assert.throws(() => createPoseEvaluator([{ ...bones[0], parent: 1 }, { ...bones[1], parent: 0 }]), /cycle/); checks++;
assert.throws(() => createPoseEvaluator([{ ...bones[0], parent: 20 }]), /parent/); checks++;
assert.throws(() => evaluator.evaluate(NaN), /finite/); checks++;

// Blender canonical rest-basis sign intentionally preserves its long-path seam.
const seamBones = [{ name: 'root', parent: -1, pos: [0, 0, 0], rot: [0, 0, 0, 1] }];
const seamKeys = [170, 190].flatMap(degrees => new Quaternion().setFromEuler(new Euler(0, 0, degrees * Math.PI / 180)).toArray());
const seam = createPoseEvaluator(seamBones, [{ name: 'root', rotations: { frames: [0, 20], values: seamKeys } }]).evaluate(10);
close(seam.worldMatrices[0], new Matrix4().identity().elements, 'Blender canonical-sign seam', 1e-10);

console.log(JSON.stringify({ checks, blenderMatrices: 20, maximumError, tolerance: fixture.matrixTolerance }));
