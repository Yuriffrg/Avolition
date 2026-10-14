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

export function matchClip(clip, context, allOccurrences = []) {
  const related = relatedOccurrences(clip, allOccurrences);
  const sameModelFolder = Boolean(context?.modelFolder && related.some(row => {
    const source = modelPathParts(row.path);
    return source && source.modelFolder === context.modelFolder && source.exportRoot === context.exportRoot
      && source.modelGuid === context.modelGuid && source.remainder[0] === 'animations';
  }));
  const binding = !clip?.error && related.find(row => validBinding(row?.binding))?.binding;
  const trackCount = validCount(clip?.boneCount) ? clip.boneCount : binding ? new Set(binding.boneNames).size : null;
  const score = {name:String(clip?.name || basename(clip?.path)), path:String(clip?.path || ''),
    known:false, matched:null, totalBoneCount:context?.totalBoneCount || 0, trackCount,
    extraTracks:null, coverage:null, sameModelFolder, sourceRank:Number(sameModelFolder),
    error:clip?.error || clip?.binding?.error || null};
  if (!context?.boneNames?.size || !binding) return score;
  const names = new Set(binding.boneNames);
  let matched = 0;
  for (const name of context.boneNames) if (names.has(name)) matched++;
  return {...score, known:true, matched, extraTracks:Math.max(0, trackCount - matched),
    coverage:matched / context.totalBoneCount, error:null};
}

// Scores can be cached once per render, keeping sort comparisons constant time.
export function compareMatchScores(a, b) {
  return Number(Boolean(b?.known)) - Number(Boolean(a?.known))
    || Number(b?.sourceRank || 0) - Number(a?.sourceRank || 0)
    || (a?.known && b?.known ? b.matched - a.matched || a.extraTracks - b.extraTracks : 0)
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
