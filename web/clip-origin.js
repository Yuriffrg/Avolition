import { createPoseEvaluator } from './pose.js';

const originRadius = 0.05; // OW skeleton positions are in metres.
const distance = matrix => Math.hypot(matrix[12], matrix[13], matrix[14]);

// Reduce the skeleton to the marker and its parents. Child positions must be
// evaluated through the same rest transforms and scale rules as the viewport.
export function createOriginProbe(entityPath, model) {
  if (!/(?:^|[\\/])Gameplay3P\.owentity$/i.test(entityPath) || !model || model.parentId) return null;
  const bones = model.bones || [];
  const marker = aliases => bones.findIndex(bone => aliases.includes(String(bone.name).toLowerCase()));
  for (const index of [marker(['bone_0011', 'head']), marker(['bone_007d', 'camera'])]) {
    if (index < 0) continue;
    const indices = [];
    const seen = new Set();
    for (let i = index; i !== -1; i = bones[i].parent ?? -1) {
      if (!Number.isInteger(i) || !bones[i] || seen.has(i)) return null;
      indices.push(i);
      seen.add(i);
    }
    indices.reverse();
    if (indices.length > 1024) return null;
    const chain = indices.map((i, n) => ({...bones[i], parent:n - 1}));
    try {
      // A marker already at the origin in bind pose provides no evidence.
      const rest = createPoseEvaluator(chain).evaluate(0).worldMatrices.at(-1);
      if (distance(rest) <= originRadius) continue;
      return {
        modelId: model.id,
        names: chain.map(bone => bone.name),
        matches(firstPose) {
          if (!Array.isArray(firstPose) || !firstPose.length) return false;
          try {
            const sample = createPoseEvaluator(chain, firstPose).evaluate(0).worldMatrices.at(-1);
            return distance(sample) <= originRadius;
          } catch { return false; }
        },
      };
    } catch { return null; }
  }
  return null;
}
