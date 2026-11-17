// Measured object matches for library browsing. These scores never assign,
// reject, rename or infer the action performed by an animation.
const validCount = value => Number.isInteger(value) && value >= 0;
const cleanPath = value => {
  const source = String(value || '').replaceAll('\\', '/');
  const prefix = source.startsWith('//') ? '//' : source.startsWith('/') ? '/' : '';
  const parts = [];
  for (const part of source.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (parts.length && parts.at(-1) !== '..') parts.pop(); else parts.push(part); }
    else parts.push(part);
  }
  // Whitehound's native Windows filesystem is case insensitive. Normalize
  // complete paths, not just GUIDs, so another hero/skin cannot gain provenance.
  return prefix + parts.join('/').toLowerCase();
};
const modelKey = value => String(value || '').replace(/\.(?:owmdl|00c)$/i, '').toLowerCase();
const basename = value => cleanPath(value).split('/').at(-1) || '';
const compareText = (a, b) => String(a || '').localeCompare(String(b || ''), 'en');

function clipTiming(clip) {
  if (!clip || clip.error) return null;
  const fps = Number(clip.fps);
  let seconds = clip.durationSeconds;
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0) {
    const frame = clip.lastFrame ?? (Number.isInteger(clip.durationFrames) ? clip.durationFrames - 1 : null);
    if (!Number.isFinite(frame) || frame < 0 || !Number.isFinite(fps) || fps <= 0) return null;
    seconds = frame / fps;
  }
  return {seconds, fps:Number.isFinite(fps) && fps > 0 ? fps : null};
}

// Attached objects usually accompany their parent's clip (arms and weapon).
// Otherwise use the last assigned other object, never the selected object's own
// assignment. Counts and filenames do not identify a weapon or an action.
export function assignmentDurationReference(assignments = new Map(), selectedModelId = '', models = []) {
  const usable = new Map();
  for (const [modelId, clip] of assignments) {
    const timing = clipTiming(clip);
    if (modelId !== selectedModelId && timing?.seconds > 0) usable.set(modelId, {modelId, clip, ...timing});
  }
  const byId = new Map(models.map(model => [model.id, model]));
  const visited = new Set([selectedModelId]);
  let parent = byId.get(selectedModelId)?.parentId;
  while (parent && !visited.has(parent)) {
    if (usable.has(parent)) return usable.get(parent);
    visited.add(parent);
    parent = byId.get(parent)?.parentId;
  }
  return [...usable.values()].at(-1) || null;
}

export function matchesDuration(clip, reference) {
  const timing = clipTiming(clip);
  if (!timing || !reference || !Number.isFinite(reference.seconds) || reference.seconds <= 0) return false;
  // A frame of tolerance accommodates clips sampled at different rates. Missing
  // frame rates require an exact duration instead of inventing a default FPS.
  const frameSeconds = [timing.fps, reference.fps].filter(fps => Number.isFinite(fps) && fps > 0).map(fps => 1 / fps);
  const tolerance = frameSeconds.length === 2 ? Math.max(...frameSeconds) : 0;
  return Math.abs(timing.seconds - reference.seconds) <= tolerance + 1e-9;
}

function modelPathParts(value) {
  const path = cleanPath(value), parts = path.split('/');
  if (!path.startsWith('/') && !/^[a-z]:\//i.test(path)) return null;
  for (let index = parts.length - 2; index >= 0; index--) {
    if (parts[index] === 'models' && /\.00c$/i.test(parts[index + 1])) {
      return {modelFolder: parts.slice(0, index + 2).join('/'),
        exportRoot: parts.slice(0, index).join('/'), modelGuid: modelKey(parts[index + 1]),
        remainder: parts.slice(index + 2)};
    }
  }
  return null;
}

export function modelContext(model, entityPath = '') {
  if (!model) return null;
  const bones = Array.isArray(model.bones) ? model.bones : [];
  const parts = modelPathParts(model.path);
  const guid = modelKey(model.modelGuid || basename(model.path) || model.name);
  const source = parts && parts.modelGuid === guid ? parts : null;
  const perspectives = new Set();
  if (!model.parentId) for (const candidate of [model.entityName, basename(entityPath).replace(/\.owentity$/i, '')]) {
    const match = /^(Gameplay[13]P)$/i.exec(String(candidate || ''));
    if (match) perspectives.add(match[1].toLowerCase() === 'gameplay1p' ? 'Gameplay1P' : 'Gameplay3P');
  }
  return {modelId:model.id || '', name:model.name || model.id || '', path:model.path || '', modelGuid:guid,
    boneNames:new Set(bones.map(bone => bone?.name).filter(name => typeof name === 'string' && name.length)),
    totalBoneCount:bones.length, modelFolder:source?.modelFolder || null,
    exportRoot:source?.exportRoot || null, perspective:perspectives.size === 1 ? [...perspectives][0] : null};
}

const duplicateKey = clip => !clip?.error && !clip?.duplicateError && typeof clip?.duplicateKey === 'string'
  && clip.duplicateKey.length ? clip.duplicateKey : null;

// Build this once after a scan; pass it to matchClip when rendering large
// libraries. Referenced rows remain live when their binding metadata arrives.
export function indexOccurrences(clips = []) {
  const groups = new Map();
  for (const clip of clips) {
    const key = duplicateKey(clip);
    if (!key) continue;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(clip);
  }
  return groups;
}

function relatedOccurrences(clip, occurrences) {
  const key = duplicateKey(clip);
  if (!key) return [clip];
  const candidates = occurrences instanceof Map ? occurrences.get(key) || [] : Array.isArray(occurrences) ? occurrences : [];
  return [clip, ...candidates.filter(row => row !== clip && duplicateKey(row) === key)];
}

const validBinding = binding => binding && !binding.error && Array.isArray(binding.boneNames)
  && binding.boneNames.every(name => typeof name === 'string' && name.length > 0);

// OW joint IDs and their names from owpreview/bone_names.py. A portable
// full-body clip can drive the torso and both legs even on a facial-heavy
// skeleton where those joints account for a small part of the total.
const bodyRegions = [
  ['bone_0002', 'bone_0003', 'bone_0004', 'bone_0005', 'bone_0053', 'Pelvis', 'Spine_1', 'Spine_2', 'Spine_3', 'Hips'],
  ['bone_0055', 'bone_0059', 'bone_005A', 'Thigh_L', 'Knee_L', 'Foot_L'],
  ['bone_005F', 'bone_0063', 'bone_0064', 'Thigh_R', 'Knee_R', 'Foot_R'],
];

function bodyMismatch(context, sources, names, matched, coverage) {
  if (context.perspective !== 'Gameplay3P') return '';
  if (!matched) return 'No clip joints match this body.';
  if (coverage >= 0.5) return '';
  const modelSource = context.modelFolder && sources.some(source => source.modelGuid === context.modelGuid);
  if (modelSource) return ''; // Keep partial actions authored for this model.
  if (matched < names.size / 4) return 'Fewer than a quarter of the clip’s joints match this body.';
  // Loose clips have no declared model. Sparse bindings alone cannot identify
  // an incompatible rig. Apply the stronger rule only with folder evidence.
  if (!context.modelFolder || !sources.length) return '';
  const drivesBody = bodyRegions.every(region => region.some(name => names.has(name) && context.boneNames.has(name)));
  return drivesBody ? '' : 'This clip comes from another model and has limited coverage of this body.';
}

export function matchClip(clip, context, allOccurrences = [], durationReference = null) {
  const related = relatedOccurrences(clip, allOccurrences);
  const sources = related.map(row => modelPathParts(row.path)).filter(source => source?.remainder[0] === 'animations');
  const sameModelFolder = Boolean(context?.modelFolder && sources.some(source => source.modelFolder === context.modelFolder));
  const binding = !clip?.error && related.find(row => validBinding(row?.binding))?.binding;
  const trackCount = validCount(clip?.boneCount) ? clip.boneCount : binding ? new Set(binding.boneNames).size : null;
  const score = {name:String(clip?.name || basename(clip?.path)), path:String(clip?.path || ''),
    known:false, matched:null, totalBoneCount:context?.totalBoneCount || 0, trackCount,
    extraTracks:null, coverage:null, mismatchReason:'', sameModelFolder, sourceRank:Number(sameModelFolder),
    durationMatch:matchesDuration(clip, durationReference),
    error:clip?.error || clip?.binding?.error || null};
  if (!context?.boneNames?.size || !binding) return score;
  const names = new Set(binding.boneNames);
  let matched = 0;
  for (const name of context.boneNames) if (names.has(name)) matched++;
  const coverage = matched / context.totalBoneCount;
  return {...score, known:true, matched, extraTracks:Math.max(0, trackCount - matched), coverage,
    mismatchReason:bodyMismatch(context, sources, names, matched, coverage), error:null};
}

// Scores can be cached once per render, keeping sort comparisons constant time.
export function compareMatchScores(a, b) {
  return Number(Boolean(b?.known)) - Number(Boolean(a?.known))
    || Number(Boolean(a?.mismatchReason)) - Number(Boolean(b?.mismatchReason))
    || Number(b?.sourceRank || 0) - Number(a?.sourceRank || 0)
    || (a?.known && b?.known ? b.matched - a.matched || a.extraTracks - b.extraTracks : 0)
    || Number(Boolean(b?.durationMatch)) - Number(Boolean(a?.durationMatch))
    || compareText(a?.name, b?.name) || compareText(a?.path, b?.path);
}

export function sortClipMatches(a, b, context, allOccurrences = [], matchCache = null) {
  const score = clip => matchCache?.get(clip.path) || matchClip(clip, context, allOccurrences);
  return compareMatchScores(score(a), score(b));
}

export function trackGroups(clips = []) {
  const counts = new Map();
  for (const clip of clips) if (!clip?.error && validCount(clip?.boneCount)) counts.set(clip.boneCount, (counts.get(clip.boneCount) || 0) + 1);
  return [...counts].map(([tracks, count]) => ({tracks, count})).sort((a, b) => b.tracks - a.tracks);
}
