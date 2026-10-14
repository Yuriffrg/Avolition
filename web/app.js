import { PreviewViewport } from './viewer.js';
import { ObjectVisibilityMenu } from './object-menu.js';
import { visibleClips } from './library-clips.js';
import { modelContext, matchClip, compareMatchScores, indexOccurrences } from './clip-matching.js';
import { createExportQueue } from './export-queue.js';
import { initToolchain, sameDirectory } from './toolchain.js';
import { initMaps } from './maps.js';
import { createModelBrowser } from './model-browser.js';
import { sortModelEntries } from './model-library.js';

const $ = (id) => document.getElementById(id);
const ui = Object.fromEntries([...document.querySelectorAll('[id]')].map((node) => [node.id, node]));
const state = {
  ready: false, root: '', entities: [], clips: [], filtered: [], selectedPath: '',
  entityLoaded: false, loadedClip: null, frame: 0, playing: false, loop: true,
  speed: 1, entityRequest: 0, clipRequest: 0, scanRequest: 0,
  rows: new Map(), annotationVersions: new Map(), annotationTimers: new Map(), annotationErrors: new Map(),
  annotationChains: new Map(), animationFrame: 0, previousTime: 0, displayedFrame: -1,
  browserMode: false, heartbeat: 0, heartbeatPending: false, closing: false, closed: false,
  outputFolder: '', outputRevision: 0, outputSaveChain: Promise.resolve(), outputSaveRequest: null,
  prepareClosePromise: null, preparedForClose: false, closeControlSnapshot: [],
  geometryStats: null, materialStats: null, loadedEntityPath: '', loadedEntityRoot: '',
  exportSettingsChain: Promise.resolve(), exportSettingsError: '',
  c4dStatusPending: false,
  assignments: new Map(), activeModelId: '', clipLoadPromise: null,
  queueAddPromise: null,
  matchModels: [], matchContext: null, matchScores: new Map(), occurrences: new Map(),
  bindingRequest: 0, bindingJob: null, bindingProgress: null,
  librarySettingsChain: Promise.resolve(), librarySettingsError: '',
};
let viewport;
let toolchainUI;
let exportQueue;
let mapsUI;
let modelBrowser;
let viewportError = '';
try { viewport = new PreviewViewport(ui.viewport); }
catch (error) {
  viewportError = `3D preview could not start: ${error.message || error}`;
  $('viewport-empty').querySelector('h2').textContent = '3D preview is unavailable';
  $('viewport-empty').querySelector('h2').hidden = false;
  $('viewport-empty').querySelector('p').textContent = viewportError;
}

const objectMenu = new ObjectVisibilityMenu(viewport, selectObject, {
  onRename: (key, name, path) => api('save_object_name', path, key, name),
});

function setStatus(message, kind = '') {
  if (state.closed) return;
  $('status-text').textContent = String(message);
  $('status-text').title = String(message);
  $('status-text').parentElement.className = `status-bar ${kind}`;
}

function setLoading(message) {
  $('viewport-loading').hidden = !message;
  if (message) $('loading-label').textContent = message;
}

async function api(method, ...args) {
  if (!state.ready || !window.pywebview?.api?.[method]) {
    throw new Error('The local application is not connected. Reopen Avolition.exe to use your files.');
  }
  const result = await window.pywebview.api[method](...args);
  if (!result || result.ok !== true) throw new Error(result?.error || `${method} did not complete.`);
  return result.data;
}

function formatSize(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return '—';
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  if (value >= 1024) return `${Math.round(value / 1024).toLocaleString()} KB`;
  return `${value} B`;
}

function duration(clip) {
  if (!clip) return 0;
  const seconds = Number(clip.durationSeconds);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds;
  return Math.max(0, Number(clip.durationFrames) || 0) / (Number(clip.fps) || 30);
}

function formatDuration(clip) {
  return clip?.error ? 'Invalid' : `${duration(clip).toFixed(2)} s`;
}

function sourceName(path) { return String(path || '').split(/[\\/]/).pop(); }
function cleanPath(path) {
  const value = String(path || '').trim();
  return ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'")))
    ? value.slice(1, -1) : value;
}
function clipName(clip) { return clip?.annotation?.name || clip?.name || sourceName(clip?.path) || 'Unnamed clip'; }
function selectedClip() { return state.clips.find((clip) => clip.path === state.selectedPath); }
function timelineClips() { return [...state.assignments.values()]; }
function timelineFPS() { return Math.max(1, ...timelineClips().map(clip => Number(clip.fps) || 30)); }
function maxFrame() {
  const seconds = Math.max(0, ...timelineClips().map(clip => Math.max(0,
    Number(clip.lastFrame ?? (clip.durationFrames - 1)) || 0) / (Number(clip.fps) || 30)));
  return Math.max(0, Math.ceil(seconds * timelineFPS() - 1e-9));
}
function hasTimeline() { return state.assignments.size > 0; }

function libraryContext() {
  const model = state.matchModels.find(item => item.id === state.activeModelId);
  return state.entityLoaded && model ? modelContext(model, state.loadedEntityPath) : null;
}

function libraryObjectLabel(model) {
  const context = modelContext(model, state.loadedEntityPath);
  // Perspective identifies the root character only. Attached objects keep their
  // source names; bone counts never determine a role or a character identity.
  const root = !model.parentId;
  const perspective = sourceName(state.loadedEntityPath).replace(/\.owentity$/i, '').toLowerCase();
  const role = root && perspective === 'gameplay1p' ? 'Gameplay1P arms'
    : root && perspective === 'gameplay3p' ? 'Gameplay3P body' : '';
  return `${role ? role + ' · ' : ''}${model.name || model.id} (${context.totalBoneCount} bones)`;
}

function updateLibraryObjectControl() {
  const select = $('library-object');
  const models = state.entityLoaded ? state.matchModels.filter(model => model.bones?.length) : [];
  select.replaceChildren(...models.map(model => new Option(libraryObjectLabel(model), model.id)));
  if (!models.length) select.append(new Option('Load an entity to select an object', ''));
  select.value = state.activeModelId;
  select.disabled = !models.length || state.closing;
}

function updateMatchingStatus() {
  const status = $('library-match-status');
  if (!state.entityLoaded || !state.activeModelId) status.textContent = 'Load an object to compare its bones.';
  else if (state.bindingProgress) status.textContent = `Comparing bone tracks · ${state.bindingProgress.done.toLocaleString()} / ${state.bindingProgress.total.toLocaleString()} clips`;
  else {
    const unknown = state.clips.filter(clip => !clip.error && !Array.isArray(clip.binding?.boneNames)).length;
    status.textContent = `Bars show bones covered on this object.${unknown ? ` ${unknown.toLocaleString()} unavailable.` : ''}`;
  }
}

function startLibraryMatching() {
  if (state.bindingJob || !state.entityLoaded || state.closing || state.closed) return;
  const pending = state.clips.filter(clip => !clip.error && !clip.binding);
  if (!pending.length) { updateMatchingStatus(); return; }
  const token = state.bindingRequest;
  const grouped = new Map();
  for (const clip of pending) {
    const key = clip.duplicateKey?.startsWith('sha256:') ? clip.duplicateKey : clip.path;
    if (!grouped.has(key)) grouped.set(key, []);
    grouped.get(key).push(clip);
  }
  const groups = [...grouped.values()];
  state.bindingProgress = {done:0, total:pending.length}; updateMatchingStatus();
  const job = (async () => {
    try {
      for (let offset = 0; offset < groups.length; offset += 128) {
        if (token !== state.bindingRequest || state.closing || state.closed) return;
        const batch = groups.slice(offset, offset + 128);
        const result = await api('read_clip_bindings', batch.map(group => group[0].path));
        if (token !== state.bindingRequest || state.closing || state.closed) return;
        const byPath = new Map((result.clips || []).map(item => [item.path,item]));
        for (const group of batch) {
          let binding = byPath.get(group[0].path) || {boneNames:null,error:'Bone tracks could not be read.'};
          // A source copy can disappear after scanning. An identical accessible
          // copy can still supply its bones without hiding the original path.
          if (!Array.isArray(binding.boneNames) && group.length > 1) {
            const fallback = await api('read_clip_bindings', [group[1].path]);
            if (token !== state.bindingRequest || state.closing || state.closed) return;
            if (Array.isArray(fallback.clips?.[0]?.boneNames)) binding = fallback.clips[0];
          }
          for (const clip of group) clip.binding = binding;
          state.bindingProgress.done += group.length;
        }
        updateMatchingStatus();
        // Yield between batches so preview, selection and export IPC can run.
        await new Promise(resolve => setTimeout(resolve, 0));
      }
    } catch (error) {
      if (token === state.bindingRequest) for (const clip of pending) if (!clip.binding) clip.binding = {boneNames:null,error:error.message || String(error)};
    } finally {
      if (token === state.bindingRequest) {
        state.bindingJob = null; state.bindingProgress = null;
        if (!state.closed && !state.closing) renderLibrary();
      }
    }
  })();
  state.bindingJob = job;
}

function saveLibraryOptions() {
  const changes = {sort:$('clip-sort').value, hideDuplicates:$('hide-duplicates').checked};
  state.librarySettingsChain = state.librarySettingsChain.catch(() => {}).then(async () => {
    try { await api('library_settings', changes); state.librarySettingsError = ''; }
    catch (error) { state.librarySettingsError = error.message || String(error); setStatus(`Library preferences not saved: ${state.librarySettingsError}`, 'error'); }
  });
}

function renderLibrary({ reveal = false } = {}) {
  const query = $('clip-search').value.trim().toLocaleLowerCase();
  const sort = $('clip-sort').value;
  const matching = visibleClips(state.clips, {query, hideDuplicates:$('hide-duplicates').checked,
    selectedPath:state.selectedPath, entityPath:state.loadedEntityPath});
  const context = libraryContext(); state.matchContext = context;
  state.filtered = matching;
  state.matchScores = new Map(context ? state.filtered.map(clip => [clip.path,matchClip(clip,context,state.occurrences)]) : []);
  updateLibraryObjectControl(); updateMatchingStatus();
  state.filtered.sort((a, b) => {
    let compared = 0;
    if (sort === 'object-match' && context) return compareMatchScores(state.matchScores.get(a.path),state.matchScores.get(b.path));
    if (sort === 'nicknamed-first') {
      const nameA = String(a.annotation?.name || '').trim();
      const nameB = String(b.annotation?.name || '').trim();
      compared = Number(Boolean(nameB)) - Number(Boolean(nameA));
      if (!compared && nameA && nameB) compared = nameA.localeCompare(nameB, undefined, { numeric: true });
      if (!compared) compared = String(a.name || sourceName(a.path)).localeCompare(String(b.name || sourceName(b.path)), undefined, { numeric: true });
      return compared || String(a.path).localeCompare(String(b.path));
    }
    if (sort === 'tracks-desc' || (sort === 'object-match' && !context)) compared = (b.boneCount || 0) - (a.boneCount || 0);
    if (sort === 'tracks-asc') compared = (a.boneCount || 0) - (b.boneCount || 0);
    if (sort === 'size-desc') compared = (b.size || 0) - (a.size || 0);
    if (sort === 'size-asc') compared = (a.size || 0) - (b.size || 0);
    if (sort === 'duration-desc') compared = duration(b) - duration(a);
    if (sort === 'duration-asc') compared = duration(a) - duration(b);
    return compared || clipName(a).localeCompare(clipName(b), undefined, { numeric: true })
      || String(a.path).localeCompare(String(b.path));
  });
  const list = $('clip-list');
  const scroll = list.scrollTop;
  const fragment = document.createDocumentFragment();
  state.rows.clear();
  for (const clip of state.filtered) {
    const row = document.createElement('button');
    row.type = 'button';
    row.className = `clip-row${clip.path === state.selectedPath ? ' is-selected' : ''}${clip.error ? ' has-error' : ''}`;
    row.setAttribute('role', 'listitem');
    row.setAttribute('aria-label', `${clipName(clip)}, ${formatDuration(clip)}, ${formatSize(clip.size)}`);
    row.setAttribute('aria-current', clip.path === state.selectedPath ? 'true' : 'false');
    row.title = clip.error ? `${clip.relativePath || clip.path}\n${clip.error}` : clip.relativePath || clip.path;
    if ($('hide-duplicates').checked && clip.duplicateCount > 1) row.title += `\n${clip.duplicateCount} identical copies across the export root. Uncheck Hide duplicates to see every path.`;
    const main = document.createElement('span');
    main.className = 'clip-main';
    const name = document.createElement('span');
    name.className = 'clip-name';
    name.textContent = clipName(clip).replace(/\.owanimclip$/i, '');
    const original = document.createElement('span');
    original.className = 'clip-original';
    original.textContent = clip.error ? 'Could not read clip' : `${Number(clip.boneCount) || 0} tracks · ${clip.annotation?.name
      ? (clip.name || sourceName(clip.path)) : `${Number(clip.fps) || '—'} fps`}`;
    main.append(name, original);
    const numbers = document.createElement('span');
    numbers.className = 'clip-numbers';
    const seconds = document.createElement('span');
    seconds.textContent = formatDuration(clip);
    const size = document.createElement('span');
    size.className = 'clip-size';
    size.textContent = formatSize(clip.size);
    numbers.append(seconds, size);
    row.append(main, numbers);
    if (context) {
      const score = state.matchScores.get(clip.path);
      const coverage = document.createElement('span'); coverage.className = 'clip-coverage';
      const label = document.createElement('span'); label.className = 'clip-coverage-label';
      label.textContent = score.known ? `${score.matched} / ${score.totalBoneCount} bones covered${score.sameModelFolder ? ' · Model folder' : ''}`
        : clip.error || score.error ? 'Bone coverage unavailable' : 'Comparing bones…';
      if (score.known) {
        const track = document.createElement('span'); track.className = 'clip-coverage-track'; track.setAttribute('aria-hidden', 'true');
        const fill = document.createElement('span'); fill.className = 'clip-coverage-fill'; fill.style.width = `${Math.max(0,Math.min(1,score.coverage))*100}%`;
        track.append(fill); coverage.append(label,track);
        coverage.title = `${score.matched} of ${score.trackCount} clip tracks match; ${score.extraTracks} unmatched.${score.sameModelFolder ? ' This clip or an identical copy is in the selected model’s animation folder.' : ''} Coverage is not a guarantee of the correct motion.`;
        row.setAttribute('aria-label', `${row.getAttribute('aria-label')}, ${label.textContent}`);
      } else { coverage.append(label); coverage.title = score.error || 'Bone track metadata is being read.'; }
      row.append(coverage);
    }
    row.addEventListener('click', () => selectClip(clip.path));
    state.rows.set(clip.path, row);
    fragment.append(row);
  }
  list.replaceChildren(fragment);
  list.scrollTop = scroll;
  list.hidden = !state.filtered.length;
  $('library-empty').hidden = !!state.filtered.length;
  const emptyTitle = $('library-empty').querySelector('h3');
  const emptyDescription = $('library-empty').querySelector('p');
  if (state.clips.length && !state.filtered.length) {
    emptyTitle.textContent = 'No matching animations';
    emptyDescription.textContent = 'Try another filename or nickname, or select All track counts.';
  } else if (state.root) {
    emptyTitle.textContent = 'No animation clips found';
    emptyDescription.textContent = 'Choose an export folder containing .owanimclip files. All subfolders are included.';
  } else {
    emptyTitle.textContent = 'Your library starts here';
    emptyDescription.textContent = 'Paste your export root above, then scan to find .owanimclip files.';
  }
  const unique = new Set(state.clips.map(clip => clip.duplicateKey || clip.path)).size;
  $('clip-count').textContent = ($('hide-duplicates').checked ? unique : state.clips.length).toLocaleString();
  $('clip-count').title = `${state.clips.length.toLocaleString()} source files · ${unique.toLocaleString()} unique clips`;
  $('library-summary').textContent = state.root
    ? `${state.filtered.length.toLocaleString()} clips${$('hide-duplicates').checked && state.clips.length > unique ? ` · ${(state.clips.length-unique).toLocaleString()} duplicates hidden` : ` of ${state.clips.length.toLocaleString()}`}`
    : 'No folder scanned';
  $('prev-clip').disabled = state.filtered.length < 1;
  $('next-clip').disabled = state.filtered.length < 1;
  if (reveal) state.rows.get(state.selectedPath)?.scrollIntoView({ block: 'nearest' });
}

function markSelectedRow() {
  for (const [path, row] of state.rows) {
    const selected = path === state.selectedPath;
    row.classList.toggle('is-selected', selected);
    row.setAttribute('aria-current', String(selected));
  }
}

function renderDetails() {
  const clip = selectedClip();
  $('detail-filename').textContent = clip ? (clip.name || sourceName(clip.path)) : 'Select an animation';
  $('detail-path').textContent = clip ? (clip.relativePath || clip.path) : 'Original files stay untouched.';
  $('detail-path').title = clip?.path || '';
  $('detail-duration').textContent = clip ? formatDuration(clip) : '—';
  $('detail-fps').textContent = clip && !clip.error ? `${Number(clip.fps) || '—'} fps` : '—';
  $('detail-size').textContent = clip ? formatSize(clip.size) : '—';
  $('detail-bones').textContent = clip && !clip.error ? String(clip.boneCount ?? '—') : '—';
  $('match-message').hidden = !clip?.error;
  if (clip?.error) {
    $('match-message').textContent = clip.error;
    $('match-message').className = 'match-message warning';
  }
  $('playing-title').textContent = clip ? clipName(clip) : 'No animation selected';
  $('playing-title').title = clip?.path || '';
  updateObjectControls();
}

function renderEntities() {
  state.entities = sortModelEntries(state.entities);
  const select = $('entity-select');
  const previous = select.value;
  select.replaceChildren();
  if (!state.entities.length) {
    const option = document.createElement('option');
    option.value = '';
    option.textContent = state.root ? 'No entity or model found' : 'Scan a folder to find entities';
    select.append(option);
  }
  for (const entity of state.entities) {
    const option = document.createElement('option');
    option.value = entity.path;
    option.textContent = entity.relativePath || entity.name || sourceName(entity.path);
    select.append(option);
  }
  if (state.entities.some((item) => item.path === previous)) select.value = previous;
  $('load-entity').disabled = !select.value || !state.ready || !viewport;
  $('browse-models').disabled = !state.ready || !state.entities.length;
  modelBrowser?.setEntries(state.entities);
}

function applyCatalog(catalog, { outputRevision = state.outputRevision } = {}) {
  if (!catalog || !Array.isArray(catalog.clips)) return;
  state.bindingRequest++;
  state.bindingJob = null; state.bindingProgress = null;
  state.root = catalog.root || '';
  state.clips = catalog.clips;
  state.occurrences = indexOccurrences(state.clips);
  state.entities = Array.isArray(catalog.entities) ? catalog.entities : [];
  $('root-path').value = state.root;
  renderEntities();
  renderLibrary();
  renderDetails();
  startLibraryMatching();
}

async function scanRoot() {
  if (state.closing || state.closed) return;
  let path = $('root-path').value.trim();
  if ((path.startsWith('"') && path.endsWith('"')) || (path.startsWith("'") && path.endsWith("'"))) path = path.slice(1, -1);
  if (!path) { setStatus('Paste the full path to your export root, or choose Browse.', 'error'); $('root-path').focus(); return; }
  const request = ++state.scanRequest;
  const outputRevision = state.outputRevision;
  $('scan-root').disabled = true;
  $('scan-root').textContent = 'Scanning…';
  setStatus('Scanning subfolders for entities, models and animation clips…', 'busy');
  try {
    await state.outputSaveChain;
    await flushAllAnnotations();
    const catalog = await api('scan_root', path);
    if (request !== state.scanRequest) return;
    // Edits can continue during a slow scan. Keep and save those newer labels.
    await flushAllAnnotations();
    if (request !== state.scanRequest) return;
    const edited = new Map(state.clips.filter((clip) => state.annotationVersions.has(clip.path))
      .map((clip) => [clip.path, { ...clip.annotation }]));
    for (const clip of catalog.clips || []) if (edited.has(clip.path)) clip.annotation = edited.get(clip.path);
    state.clipRequest++;
    state.selectedPath = '';
    state.loadedClip = null;
    state.assignments.clear();
    // A scan of another root must not leave a pending or old entity under the
    // newly populated entity selector. Same-root scans may keep loaded geometry.
    const normalized = value => String(value || '').replaceAll('/', '\\').replace(/[\\]+$/, '').toLowerCase();
    if (!state.entityLoaded || normalized(catalog.root) !== normalized(state.loadedEntityRoot)) {
      state.entityRequest++;
      state.entityLoaded = false;
      state.loadedEntityPath = state.loadedEntityRoot = state.activeModelId = '';
      state.geometryStats = state.materialStats = null;
      state.matchModels = [];
      viewport?.clearAsset();
      objectMenu.refresh();
      $('polygon-count').hidden = true;
      objectMenu.setSceneName('', '');
      $('scene-name').textContent = 'NO ENTITY LOADED';
      $('scene-name').title = '';
      $('viewport-empty').hidden = false;
      $('rig-select').replaceChildren(new Option('Load an entity to select an object', ''));
      $('rig-select').disabled = true;
      updateMaterialStatus();
    }
    setPlaying(false);
    viewport?.setClip(null);
    updateObjectControls();
    if (state.entityLoaded) setLoading('');
    applyCatalog(catalog, { outputRevision });
    resetTimeline();
    const invalid = state.clips.filter((clip) => clip.error).length;
    const warningText = (catalog.warnings || []).slice(0, 2).join(' · ');
    const message = `Found ${state.entities.length} entities / models and ${state.clips.length} animations${invalid ? ` (${invalid} unreadable)` : ''}.`;
    setStatus(`${message}${warningText ? ` ${warningText}` : ' Load an entity to start previewing.'}`, invalid || warningText ? 'error' : '');
  } catch (error) { if (request === state.scanRequest) setStatus(error.message || String(error), 'error'); }
  finally {
    if (request === state.scanRequest) {
      $('scan-root').disabled = !state.ready;
      $('scan-root').textContent = 'Scan folder ↗';
    }
  }
}

async function refreshToolchainLibrary(outputDirectory) {
  // Refresh the existing index, keeping the loaded scene, selected clip and
  // manual per-object assignments alive. A tab change never calls this hook.
  const request = ++state.scanRequest;
  await flushAllAnnotations();
  const activate = !state.root || sameDirectory(state.root, outputDirectory);
  const catalog = await api('scan_root', outputDirectory, activate);
  if (request !== state.scanRequest || state.closing || state.closed) return;
  if (activate) applyCatalog(catalog);
  setStatus(`Toolchain export completed. Library refreshed: ${catalog.entities.length} entities / models and ${catalog.clips.length} animations.`);
}

async function browseRoot() {
  try {
    const path = await api('browse_root');
    if (path) { $('root-path').value = path; await scanRoot(); }
  } catch (error) { setStatus(error.message || String(error), 'error'); }
}

async function loadEntity(path) {
  if (!path || !viewport || state.closing || state.closed) return false;
  try { await objectMenu.flushRenames(); }
  catch (error) {setStatus(error.message, 'error');return false;}
  flushAnnotation(state.selectedPath);
  const request = ++state.entityRequest;
  const entityRoot = state.root;
  state.clipRequest++;
  state.entityLoaded = false;
  state.loadedEntityPath = '';
  state.loadedClip = null;
  state.assignments.clear();
  state.activeModelId = '';
  state.matchModels = [];
  state.geometryStats = null;
  state.materialStats = null;
  viewport.clearAsset();
  objectMenu.refresh();
  updateObjectControls();
  renderLibrary();
  $('polygon-count').hidden = true;
  $('polygon-count').textContent = 'Polygons: 0';
  $('scene-name').textContent = 'NO ENTITY LOADED';
  $('scene-name').title = '';
  $('viewport-empty').hidden = false;
  $('rig-select').replaceChildren(new Option('Load an entity to select an object', ''));
  $('rig-select').disabled = true;
  updateMaterialStatus();
  setPlaying(false);
  resetTimeline();
  $('load-entity').disabled = true;
  setLoading('Loading entity and skeleton…');
  setStatus(`Loading ${sourceName(path)}…`, 'busy');
  let loadSucceeded = false;
  try {
    const asset = await api('load_entity', path);
    if (request !== state.entityRequest) return;
    const stats = viewport.loadAsset(asset) || {};
    state.entityLoaded = true;
    state.loadedEntityPath = path;
    state.loadedEntityRoot = entityRoot;
    state.matchModels = asset.models || [];
    objectMenu.setSceneName(sourceName(path).replace(/\.(owentity|owmdl)$/i, ''), path);
    objectMenu.setNames(asset.objectNames || {});
    $('animation-set-name').value = sourceName(path).replace(/\.(owentity|owmdl)$/i, '') + '_';
    $('animation-set-export-status').textContent = '';
    $('animation-set-name').placeholder = sourceName(path).replace(/\.(owentity|owmdl)$/i, '') + '_nickname';
    $('viewport-empty').hidden = true;
    $('scene-name').textContent = sourceName(path);
    $('scene-name').title = path;
    const rigs = viewport.getRigs() || [];
    $('rig-select').replaceChildren();
    for (const rig of rigs.filter(item => item.bones)) $('rig-select').append(new Option(`${rig.name || rig.id} (${rig.bones} bones)`, rig.id));
    state.activeModelId = $('rig-select').value || '';
    $('rig-select').disabled = !state.activeModelId;
    if (state.activeModelId) viewport.setAssignments([], state.activeModelId);
    updateObjectControls();
    renderLibrary();
    const meshCount = stats.meshes ?? stats.meshCount ?? asset.meshes?.length;
    const boneCount = stats.bones ?? stats.boneCount ?? rigs.reduce((sum, rig) => sum + (Number(rig.bones) || 0), 0);
    state.geometryStats = {meshes: meshCount, bones: boneCount, polygons: stats.polygons || 0};
    $('polygon-count').textContent = `Polygons: ${state.geometryStats.polygons.toLocaleString()}`;
    $('polygon-count').hidden = false;
    objectMenu.refresh();
    updateMaterialStatus('Loading materials…');
    setLoading('Loading materials and textures…');
    const clipRequestBeforeMaterials = state.clipRequest;
    let materials;
    try { materials = await viewport.loadMaterials(asset, (id) => api('load_texture', id)); }
    catch (error) { materials = {applied: 0, texturedMeshes: 0, warnings: [`Materials unavailable: ${error.message || error}`]}; }
    if (request !== state.entityRequest) return;
    const warnings = [...(Array.isArray(asset.warnings) ? asset.warnings.filter(Boolean) : []), ...(materials?.warnings || [])];
    state.materialStats = {...materials, warnings};
    updateMaterialStatus();
    if (state.clipRequest === clipRequestBeforeMaterials) {
      setStatus(`Loaded ${sourceName(path)}.${warnings.length ? ` Preview notes: ${warnings.slice(0, 2).join(' · ')}` : ' Click an animation to preview.'}`);
      setLoading('');
    }
    loadSucceeded = true;
    startLibraryMatching();
    if (state.selectedPath && state.clipRequest === clipRequestBeforeMaterials) selectClip(state.selectedPath);
  } catch (error) {
    if (request === state.entityRequest) { state.entityLoaded = false; setStatus(error.message || String(error), 'error'); }
  } finally {
    if (request === state.entityRequest) { $('load-entity').disabled = !$('entity-select').value; if (!loadSucceeded) setLoading(''); }
  }
  return loadSucceeded;
}

function showMatch(result, clip) {
  const message = $('match-message');
  const matched = Number(result?.matched) || 0;
  const total = Number(result?.total) || Number(clip.boneCount) || 0;
  message.hidden = false;
  message.className = `match-message${matched === 0 || matched < total ? ' warning' : ''}`;
  message.textContent = `${matched} / ${total} tracks matched${result?.rigName ? ` · ${result.rigName}` : ''}.${matched === 0 ? ' Choose another rig or an animation exported for this entity.' : ''}`;
  return matched;
}

function activeModel() {
  return viewport?.getRigs().find(rig => rig.id === state.activeModelId);
}

function updateObjectControls() {
  const model = activeModel();
  const summaries = new Map([...state.assignments].map(([id, clip]) => [id, clipName(state.clips.find(item => item.path === clip.path) || clip)]));
  objectMenu.setActiveModel(state.activeModelId, summaries);
  $('clear-object-animation').disabled = !state.assignments.has(state.activeModelId);
  $('object-animation-status').textContent = model
    ? `Click a library clip to change this object's animation. ${state.assignments.size} assigned; other objects keep their clips.`
    : 'Choose an object, then browse animations. Each object keeps its own clip.';
  renderAnimationSet();
  updateLibraryObjectControl();
}

function renderAnimationSet() {
  const list = $('animation-set');
  const visible = state.entityLoaded && state.assignments.size > 0;
  list.hidden = !visible;
  $('animation-set-export-controls').hidden = !visible;
  list.replaceChildren();
  for (const [modelId, clip] of state.assignments) {
    const rig = viewport?.getRigs().find(item => item.id === modelId);
    const row = document.createElement('div'); row.className = 'animation-set-row';
    const select = document.createElement('button'); select.type = 'button';
    select.className = 'animation-set-object' + (modelId === state.activeModelId ? ' is-active' : '');
    select.textContent = `${rig?.name || modelId} (${rig?.bones || 0} bones)`;
    select.title = 'Select this object to browse or change its animation';
    select.setAttribute('aria-pressed', String(modelId === state.activeModelId));
    select.disabled = state.closing;
    select.addEventListener('click', () => selectObject(modelId));
    const label = document.createElement('span'); label.className = 'animation-set-clip';
    label.textContent = `${clipName(state.clips.find(item => item.path === clip.path) || clip)} · ${duration(clip).toFixed(2)} s`;
    label.title = clip.path;
    const clear = document.createElement('button'); clear.type = 'button'; clear.className = 'animation-set-clear';
    clear.textContent = 'Clear'; clear.title = `Clear animation for ${rig?.name || modelId}`;
    clear.disabled = state.closing;
    clear.addEventListener('click', () => { if (modelId !== state.activeModelId) selectObject(modelId); clearObjectAnimation(); });
    row.dataset.modelId = modelId; row.append(select, label, clear); list.append(row);
  }
  $('export-animation-set').disabled = !visible || !!state.clipLoadPromise || !!state.queueAddPromise || !!exportQueue?.busy() || state.closing;
  $('export-animation-set').textContent = state.queueAddPromise ? 'Adding…' : 'Add to export';
  $('animation-set-name').disabled = !!state.queueAddPromise || !!exportQueue?.busy() || state.closing;
}

async function addAnimationSet() {
  if (!state.entityLoaded || !state.assignments.size || state.clipLoadPromise || state.queueAddPromise || exportQueue.busy() || state.closing) return;
  const input = $('animation-set-name'), status = $('animation-set-export-status');
  const prefix = sourceName(state.loadedEntityPath).replace(/\.(owentity|owmdl)$/i, '') + '_';
  let name = input.value.trim().replace(/\.c4d$/i, '');
  if (!name || name === prefix) { status.textContent = 'Enter a nickname for this animation setup.'; status.classList.add('error'); input.focus(); return; }
  if (!name.startsWith(prefix)) name = prefix + name;
  const snapshot = {name, entityPath:state.loadedEntityPath, rootPath:state.loadedEntityRoot,
    assignments:[...state.assignments].map(([modelId, clip]) => ({modelId, clipPath:clip.path}))};
  const entityAtStart = state.loadedEntityPath;
  const namedClipPath = state.selectedPath;
  status.classList.remove('error'); status.textContent = 'Adding to export list…';
  state.queueAddPromise = (async () => {
    try {
      await objectMenu.flushRenames();
      snapshot.objectNames = state.loadedEntityPath === entityAtStart ? {...objectMenu.names} : await api('object_names', entityAtStart);
      const result = await exportQueue.add(snapshot);
      // Keep library nickname search useful; the queue owns its independent name.
      annotationChanged({name:name.slice(prefix.length)}, namedClipPath);
      await flushAllAnnotations();
      if (state.loadedEntityPath === entityAtStart) input.value = prefix;
      status.textContent = `${result.existing ? 'Already in' : 'Added to'} export list: ${result.entry.name}.c4d. Open Export Manager to select and save it.`;
      setStatus(status.textContent);
      return true;
    } catch (error) { status.textContent = `Not added: ${error.message || error}`; status.classList.add('error'); return false; }
    finally { state.queueAddPromise = null; renderAnimationSet(); }
  })(); renderAnimationSet();
  return state.queueAddPromise;
}

function applyAssignments() {
  const result = viewport.setAssignments([...state.assignments].map(([modelId, clip]) => ({modelId, clip})), state.activeModelId);
  updateObjectControls();
  return result;
}

function selectObject(modelId) {
  if (!state.entityLoaded || state.closing || state.closed || modelId === state.activeModelId) return;
  if (!viewport.getRigs().some(rig => rig.id === modelId && rig.bones)) return;
  // Save local labels before changing the active object.
  flushAnnotation(state.selectedPath);
  state.clipRequest++;
  setPlaying(false);
  state.activeModelId = modelId;
 
  $('rig-select').value = modelId;
  state.loadedClip = state.assignments.get(modelId) || null;
  state.selectedPath = state.loadedClip?.path || '';
  const match = applyAssignments();
  resetTimeline();
  renderLibrary({reveal:true});
  renderDetails();
  if (state.loadedClip) { showMatch(match, state.loadedClip); seek(0); }
  setLoading('');
  setStatus(`Animating ${activeModel()?.name || modelId}. Click any clip to assign it to this object.`);
}

function clearObjectAnimation() {
  if (!state.entityLoaded || state.closing || state.closed) return;
  flushAnnotation(state.selectedPath);
  state.clipRequest++;
  setPlaying(false);
  state.assignments.delete(state.activeModelId);
  state.loadedClip = null;
  state.selectedPath = '';
  applyAssignments();
  resetTimeline();
  renderLibrary();
  renderDetails();
  setLoading('');
  setStatus('Animation cleared for this object. Other object assignments remain in the preview.');
}

function selectClip(path) {
  const pending = loadSelectedClip(path);
  state.clipLoadPromise = pending;
  renderAnimationSet();
  pending.finally(() => { if (state.clipLoadPromise === pending) state.clipLoadPromise = null; renderAnimationSet(); });
  return pending;
}

async function loadSelectedClip(path) {
  if (state.closing || state.closed) return;
  const clip = state.clips.find(entry => entry.path === path);
  if (!clip) return;
  flushAnnotation(state.selectedPath);
  state.selectedPath = path;
  const request = ++state.clipRequest, entityRequest = state.entityRequest, modelId = state.activeModelId;
  setPlaying(false);
  state.loadedClip = null;
  resetTimeline();
  markSelectedRow();
  renderDetails();
  if (!state.entityLoaded || !viewport || !modelId) {
    setStatus('Animation selected. Load an entity and choose an object to preview it.'); return;
  }
  // Replace only this object's assignment, including while a new file is loading.
  state.assignments.delete(modelId);
  applyAssignments();
  resetTimeline();
  if (clip.error) { setLoading(''); setStatus(`Cannot preview ${clip.name}: ${clip.error}`, 'error'); return; }
  setLoading('Loading animation…');
  setStatus(`Loading ${clipName(clip)} for ${activeModel()?.name || modelId}…`, 'busy');
  try {
    const animation = await api('load_animation', path);
    if (request !== state.clipRequest || entityRequest !== state.entityRequest || modelId !== state.activeModelId) return;
    state.assignments.set(modelId, animation);
    state.loadedClip = animation;
    const matched = showMatch(applyAssignments(), animation);
    resetTimeline();
    seek(0);
    $('playing-title').textContent = clipName(clip);
    setStatus(matched ? `Previewing ${clipName(clip)} on ${activeModel()?.name || modelId}. Other object animations are retained.`
      : 'No joint names match on this object. You can continue browsing or select another object.', matched ? '' : 'error');
    setPlaying(matched > 0 && maxFrame() > 0);
    } catch (error) {
    if (request === state.clipRequest && entityRequest === state.entityRequest) {
      state.loadedClip = null;
      state.assignments.delete(modelId);
      applyAssignments();
      resetTimeline();
      $('match-message').hidden = false;
      $('match-message').className = 'match-message warning';
      $('match-message').textContent = error.message || String(error);
      setStatus(error.message || String(error), 'error');
    }
  } finally { if (request === state.clipRequest && entityRequest === state.entityRequest) setLoading(''); }
}

function resetTimeline() {
  state.frame = 0;
  state.displayedFrame = -1;
  const loaded = hasTimeline();
  const max = maxFrame();
  $('timeline').max = String(max);
  $('timeline').disabled = !loaded;
  $('frame-input').max = String(max);
  $('frame-input').disabled = !loaded;
  $('play-pause').disabled = !loaded || max === 0;
  $('step-back').disabled = !loaded;
  $('step-forward').disabled = !loaded;
  $('timeline-middle').textContent = loaded ? String(Math.floor(max / 2)) : '—';
  $('timeline-end').textContent = `${loaded ? max + 1 : 0} frames`;
  $('total-time').textContent = `${(max / timelineFPS()).toFixed(2)} s`;
  $('fps-label').textContent = loaded ? `${timelineFPS()} FPS` : 'FRAME';
  if (viewport && loaded) viewport.setFrame(0, timelineFPS());
  updateTimeline(true);
}

function updateTimeline(force = false) {
  const frame = Math.floor(state.frame);
  if (!force && state.displayedFrame === frame) return;
  state.displayedFrame = frame;
  $('timeline').value = String(frame);
  $('timeline').style.setProperty('--progress', `${maxFrame() ? Math.min(100, (state.frame / maxFrame()) * 100) : 0}%`);
  if (document.activeElement !== $('frame-input')) $('frame-input').value = String(frame);
  $('current-time').textContent = (state.frame / timelineFPS()).toFixed(2);
}

function seek(frame) {
  if (!hasTimeline() || !viewport) return;
  state.frame = Math.max(0, Math.min(maxFrame(), Number(frame) || 0));
  viewport.setFrame(state.frame, timelineFPS());
  updateTimeline(true);
}

function setPlaying(playing) {
  state.playing = !!playing && !state.closing && hasTimeline() && maxFrame() > 0;
  state.previousTime = 0;
  $('play-icon').setAttribute('href', state.playing ? '#i-pause' : '#i-play');
  $('play-pause').setAttribute('aria-label', state.playing ? 'Pause' : 'Play');
  if (state.playing && !state.animationFrame) state.animationFrame = requestAnimationFrame(playbackTick);
  if (!state.playing && state.animationFrame) { cancelAnimationFrame(state.animationFrame); state.animationFrame = 0; }
}

function playbackTick(timestamp) {
  state.animationFrame = 0;
  if (!state.playing || !hasTimeline() || !viewport) return;
  const elapsed = state.previousTime ? Math.min((timestamp - state.previousTime) / 1000, 0.15) : 0;
  state.previousTime = timestamp;
  let frame = state.frame + elapsed * timelineFPS() * state.speed;
  const max = maxFrame();
  if (frame > max) {
    if (state.loop) frame %= (max + 1);
    else { frame = max; setPlaying(false); }
  }
  state.frame = frame;
  viewport.setFrame(Math.min(frame, max), timelineFPS());
  updateTimeline();
  if (state.playing) state.animationFrame = requestAnimationFrame(playbackTick);
}

function togglePlaying() {
  if (!hasTimeline()) return;
  if (!state.playing && state.frame >= maxFrame()) seek(0);
  setPlaying(!state.playing);
}

function nextClip(direction) {
  if (!state.filtered.length) return;
  const current = state.filtered.findIndex((clip) => clip.path === state.selectedPath);
  const next = current < 0 ? (direction > 0 ? 0 : state.filtered.length - 1)
    : (current + direction + state.filtered.length) % state.filtered.length;
  const clip = state.filtered[next];
  selectClip(clip.path);
  state.rows.get(clip.path)?.scrollIntoView({ block: 'nearest' });
}

function annotationChanged(patch, path = state.selectedPath) {
  const clip = state.clips.find(item => item.path === path);
  if (!clip) return;
  // Preserve any legacy fields, including notes, without exposing or resetting them.
  clip.annotation = { name: '', ...clip.annotation, ...patch };
  const version = (state.annotationVersions.get(clip.path) || 0) + 1;
  state.annotationVersions.set(clip.path, version);
  state.annotationErrors.delete(clip.path);
  if (path === state.selectedPath) $('playing-title').textContent = clipName(clip);
  renderLibrary();
  updateObjectControls();
  clearTimeout(state.annotationTimers.get(clip.path));
  state.annotationTimers.set(clip.path, setTimeout(() => flushAnnotation(clip.path), 650));
}

function flushAnnotation(path) {
  if (!path || (!state.annotationTimers.has(path) && !state.annotationErrors.has(path))) return;
  clearTimeout(state.annotationTimers.get(path));
  state.annotationTimers.delete(path);
  const clip = state.clips.find((entry) => entry.path === path);
  if (!clip) return;
  const annotation = { ...clip.annotation };
  const version = state.annotationVersions.get(path);
  const previous = state.annotationChains.get(path) || Promise.resolve();
  const save = previous.catch(() => {}).then(() => api('save_annotation', path, annotation)).then((saved) => {
    if (state.annotationVersions.get(path) !== version) return;
    state.annotationErrors.delete(path);
    if (saved && typeof saved === 'object') clip.annotation = { ...annotation, ...saved };
    if (path === state.selectedPath) {
      renderAnimationSet();
    }
  }).catch((error) => {
    if (state.annotationVersions.get(path) === version) state.annotationErrors.set(path, error.message || String(error));
    setStatus(`Could not save changes for ${clipName(clip)}: ${error.message || error}`, 'error');
  });
  state.annotationChains.set(path, save);
}

async function flushAllAnnotations() {
  for (const path of new Set([...state.annotationTimers.keys(), ...state.annotationErrors.keys()])) flushAnnotation(path);
  await Promise.all([...state.annotationChains.values()]);
  return state.annotationErrors.size === 0;
}

function outputStatus(message = '', error = false) {
  $('output-help').hidden = !message;
  $('output-help').textContent = message;
  $('output-help').classList.toggle('error', error);
}

function applyOutputFolder(value) {
  state.outputFolder = String(value || '');
  $('output-path').value = state.outputFolder;
  outputStatus();
}

function saveOutputFolder() {
  if (!state.ready || state.closed) return Promise.resolve(false);
  const value = cleanPath($('output-path').value);
  const revision = state.outputRevision;
  const previous = state.outputSaveRequest;
  if (previous?.value === value && previous.revision === revision) return previous.promise;
  outputStatus('Saving output folder…');
  const promise = state.outputSaveChain.catch(() => {}).then(async () => {
    if (revision !== state.outputRevision) return false;
    try {
      const result = await api('set_output_folder', value);
      if (revision === state.outputRevision && cleanPath($('output-path').value) === value) {
        // Keep the committed input stable while any copy uses its snapshot.
        // The backend may expand environment variables or normalize separators.
        state.outputFolder = result.outputFolder;
        outputStatus();
      }
      return true;
    } catch (error) {
      if (revision === state.outputRevision) {
        outputStatus(`Output folder not saved: ${error.message || error}`, true);
        state.outputSaveRequest = null;
      }
      return false;
    }
  });
  state.outputSaveRequest = { value, revision, promise };
  state.outputSaveChain = promise;
  return promise;
}

async function browseOutput() {
  if (state.browserMode || state.closing || state.closed) return;
  try {
    const path = await api('browse_output');
    if (!path) return;
    state.outputRevision++;
    $('output-path').value = path;
      await saveOutputFolder();
  } catch (error) { outputStatus(error.message || String(error), true); }
}

function currentExportSettings() {
  // Queued scenes always use C4D; legacy format preferences cannot enable copies.
  // Preview controls (In-place and object visibility) never enter the export snapshot.
  return {clipEnabled: false, enabled: false, c4dEnabled: true, upAxis: 'Y',
    includeMesh: $('fbx-mesh').checked, bakeTextures: $('fbx-textures').checked,
    rootMotion: $('fbx-root-motion').checked};
}

function applyExportSettings(value = {}) {
  $('fbx-mesh').checked = value.includeMesh !== false;
  $('fbx-textures').checked = value.bakeTextures !== false;
  $('fbx-root-motion').checked = value.rootMotion !== false;
}

function saveExportSettings() {
  const options = currentExportSettings();
  state.exportSettingsChain = state.exportSettingsChain.catch(() => {}).then(async () => {
    try { await api('set_export_settings', options); state.exportSettingsError = ''; }
    catch (error) { state.exportSettingsError = error.message || String(error); setStatus(`Export options could not be saved: ${state.exportSettingsError}`, 'error'); }
  });
  return state.exportSettingsChain;
}

async function checkC4DConnection() {
  if (!state.ready || state.closing || state.closed || $('check-c4d').disabled) return;
  const button = $('check-c4d');
  const message = $('c4d-connection-status');
  state.c4dStatusPending = true;
  button.disabled = true;
  message.className = 'c4d-connection-status';
  message.textContent = 'Checking Cinema 4D…';
  try {
    const result = await api('c4d_status');
    const available = result.connected === true && result.octane === true;
    message.textContent = result.message || (available ? 'Cinema 4D and Octane are ready.'
      : result.connected ? 'Cinema 4D is connected, but Octane is not loaded.'
      : 'Open Cinema 4D with Avolition C4D Bridge installed to connect.');
    message.classList.add(available ? 'success' : 'error');
  } catch (error) {
    message.textContent = error.message || String(error);
    message.classList.add('error');
  } finally {
    state.c4dStatusPending = false;
    if (!state.closing && !state.closed) button.disabled = false;
  }
}

function startHeartbeat() {
  clearInterval(state.heartbeat);
  if (!state.browserMode || state.closing || state.closed) return;
  state.heartbeat = setInterval(async () => {
    if (state.heartbeatPending || state.closing || state.closed) return;
    state.heartbeatPending = true;
    try { await api('ping'); }
    catch (error) {
      if (!state.closing && !state.closed) {
        setPlaying(false);
        setStatus('Connection to the local application was lost. Reopen Avolition.exe to reconnect.', 'error');
      }
    } finally { state.heartbeatPending = false; }
  }, 10000);
}

function resumeAfterClosePreparation() {
  state.closing = false;
  state.preparedForClose = false;
  state.prepareClosePromise = null;
  for (const { element, disabled } of state.closeControlSnapshot) element.disabled = disabled;
  state.closeControlSnapshot = [];
  // In-flight scan/load responses were invalidated while preparing to close.
  $('scan-root').disabled = !state.ready;
  $('check-c4d').disabled = !state.ready || state.c4dStatusPending;
  $('scan-root').textContent = 'Scan folder ↗';
  $('load-entity').disabled = !state.ready || !viewport || !$('entity-select').value;
  if (viewport) viewport.controls.enabled = true;
  startHeartbeat();
  toolchainUI?.resume();
  renderAnimationSet();
  exportQueue.render();
  if (state.bindingJob) state.bindingJob.finally(startLibraryMatching);
  else startLibraryMatching();
}

function prepareClose() {
  if (state.closed || state.preparedForClose) return Promise.resolve({ ok: true });
  if (state.prepareClosePromise) return state.prepareClosePromise;
  // A batch can contain many long native saves. Keep the window usable rather
  // than exceeding the host's close timeout while awaiting the entire list.
  if (exportQueue.exporting() || mapsUI?.exporting()) {
    const error = 'An export is running. Wait for the batch to finish, then close Avolition.';
    setStatus(error, 'busy');
    return Promise.resolve({ok:false, busy:true, error});
  }
  state.closing = true;
  modelBrowser?.close();
  setPlaying(false);
  state.closeControlSnapshot = [...document.querySelectorAll('button,input,select,textarea')]
    .map((element) => ({ element, disabled: element.disabled }));
  toolchainUI?.suspend();
  for (const { element } of state.closeControlSnapshot) element.disabled = true;
  setLoading('');
  setStatus('Saving changes before closing Avolition…', 'busy');
  state.prepareClosePromise = (async () => {
    try {
      await objectMenu.flushRenames();
      await mapsUI?.flush();
      await toolchainUI?.flush();
      saveLibraryOptions();
      await state.librarySettingsChain;
      if (state.librarySettingsError) throw new Error(`Library preferences could not be saved: ${state.librarySettingsError}`);
      await state.clipLoadPromise;
      await state.queueAddPromise;
      await exportQueue.flush();
      state.clipRequest++;
      state.entityRequest++;
      state.scanRequest++;
      const hasOutput = !!cleanPath($('output-path').value);
      const outputSave = hasOutput ? saveOutputFolder() : Promise.resolve(true);
      const annotationsSaved = await flushAllAnnotations();
      const outputSaved = await outputSave;
      await state.outputSaveChain;
        await state.exportSettingsChain;
      if (state.exportSettingsError) throw new Error(`Export options could not be saved: ${state.exportSettingsError}`);
      if (!annotationsSaved) {
        const [path, error] = state.annotationErrors.entries().next().value || [];
        throw new Error(`Nicknames could not be saved${path ? ` for ${sourceName(path)}` : ''}${error ? `: ${error}` : '.'}`);
      }
      if (!outputSaved) throw new Error($('output-help').textContent || 'The output folder could not be saved.');
      state.preparedForClose = true;
      toolchainUI?.stop();
      clearInterval(state.heartbeat);
      state.heartbeat = 0;
      if (viewport) viewport.controls.enabled = false;
      setStatus('Changes saved. Closing Avolition…');
      return { ok: true };
    } catch (error) {
      const message = error.message || String(error);
      resumeAfterClosePreparation();
      setStatus(`Avolition is still open: ${message}`, 'error');
      return { ok: false, error: message };
    }
  })();
  return state.prepareClosePromise;
}

// The native window awaits this helper before destroying its renderer.
// It only saves and prepares the UI; the host owns the actual close operation.
window.whitehoundPrepareClose = prepareClose;

async function closeApp() {
  // This control exists only in the optional developer HTTP preview.
  if (!state.browserMode || state.closing || state.closed) return;
  const prepared = await prepareClose();
  if (!prepared.ok) return;
  try {
    await api('close_app');
    setStatus('Application closed. You can close this tab.');
    state.closed = true;
    state.ready = false;
    state.bindingRequest++;
    viewport?.renderer.setAnimationLoop(null);
    document.body.classList.add('app-closed');
    $('viewport').tabIndex = -1;
    $('close-app').textContent = 'App closed';
  } catch (error) {
    resumeAfterClosePreparation();
    setStatus(`Could not close the application: ${error.message || error}`, 'error');
  }
}

function setOptions() {
  viewport?.setOptions({ grid: $('show-grid').checked, skeleton: $('show-skeleton').checked,
    wireframe: $('show-wireframe').checked, lockRoot: $('lock-root').checked, textured: $('show-textures').checked });
  updateMaterialStatus();
  if (hasTimeline()) viewport?.setFrame(state.frame, timelineFPS());
}

function updateMaterialStatus(pending = '') {
  const stats = state.materialStats;
  const enabled = $('show-textures').checked;
  const count = stats?.texturedMeshes || 0;
  const materialCount = stats?.applied || 0;
  const label = !enabled ? 'Neutral shading' : pending ? 'Loading materials' : count ? `${count} textured meshes`
    : materialCount ? 'Material shading' : 'Neutral shading';
  if (state.geometryStats) $('mesh-stats').textContent = [state.geometryStats.meshes != null ? `${state.geometryStats.meshes} meshes` : '',
    `${state.geometryStats.bones} bones`, label].filter(Boolean).join(' · ');
  else $('mesh-stats').textContent = 'Neutral mesh preview';
  $('material-status').textContent = pending || (!enabled ? 'Neutral shading. Enable Textures to show materials.'
    : !state.geometryStats ? 'Textures load from the entity’s material files.'
    : count ? `${count} meshes textured.${stats?.warnings?.length ? ' See preview notes.' : ''}`
    : materialCount ? 'Material colors loaded; no texture maps available.' : 'No supported material textures found; using neutral shading.');
  $('material-status').title = (stats?.warnings || []).join('\n');
}

async function connect() {
  if (state.ready || !window.pywebview?.api) return;
  state.ready = true;
  for (const id of ['scan-root', 'browse-root', 'browse-output', 'check-c4d']) $(id).disabled = false;
  try {
    const data = await api('bootstrap');
    $('clip-sort').value = data?.libraryOptions?.sort || 'object-match';
    $('hide-duplicates').checked = data?.libraryOptions?.hideDuplicates !== false;
    mapsUI = initMaps(api, data, {sameDirectory});
    toolchainUI = initToolchain(api, {
      async refreshLibrary(root) {await refreshToolchainLibrary(root);await mapsUI.refreshLibrary(root);},
      activeChanged: id => {if(id === 'maps') mapsUI.activate();},
    }, data);
    applyExportSettings(data?.fbxOptions);
    if (data?.fbxOptions?.enabled || data?.fbxOptions?.clipEnabled) await saveExportSettings();
    state.browserMode = !!data?.browserMode;
    exportQueue.restore(data?.exportQueue);
    $('close-app').hidden = !state.browserMode;
    if (state.browserMode) {
      for (const id of ['browse-root', 'browse-output']) {
        $(id).disabled = true;
        $(id).title = id === 'browse-output' ? 'Paste the output folder path in browser mode'
          : 'Paste the root folder path and scan in browser mode';
      }
      startHeartbeat();
    }
    $('root-path').value = data?.lastRoot || data?.workflow?.libraryDirectory || '';
    if (typeof data?.outputFolder === 'string' && state.outputRevision === 0) applyOutputFolder(data.outputFolder);
    const hasCatalog = Array.isArray(data?.catalog?.clips);
    if (hasCatalog) applyCatalog(data.catalog);
    setStatus(viewportError || data?.warning || (hasCatalog ? 'Library restored. Load an entity to begin.'
      : data?.lastRoot ? 'Ready. Scan your saved export folder to load the library.'
      : 'Ready. Paste your export root above and scan to begin.'), viewportError || data?.warning ? 'error' : '');
  } catch (error) { setStatus(error.message || String(error), 'error'); }
  renderEntities();
}

exportQueue = createExportQueue(api, {
  closing: () => state.closing || !state.ready, browserMode: () => state.browserMode,
  settings: currentExportSettings, saveOutput: saveOutputFolder,
  async saveSettings() { await saveExportSettings(); if (state.exportSettingsError) throw new Error(state.exportSettingsError); },
  changed: renderAnimationSet,
});

modelBrowser = createModelBrowser({
  api,
  onOpen: () => setPlaying(false),
  async onLoad(path) {
    if (state.closing || state.closed) return;
    $('entity-select').value = path;
    if (!await loadEntity(path)) throw new Error($('status-text').textContent || 'The model could not be loaded.');
  },
});

$('scan-root').addEventListener('click', scanRoot);
$('close-app').addEventListener('click', closeApp);
$('browse-root').addEventListener('click', browseRoot);
$('browse-output').addEventListener('click', browseOutput);
$('output-path').addEventListener('input', () => { state.outputRevision++; outputStatus(); });
$('output-path').addEventListener('blur', saveOutputFolder);
$('output-path').addEventListener('keydown', (event) => { if (event.key === 'Enter') { saveOutputFolder(); $('output-path').blur(); } });
$('root-path').addEventListener('keydown', (event) => { if (event.key === 'Enter') scanRoot(); });
$('entity-select').addEventListener('change', () => { $('load-entity').disabled = !state.ready || !viewport || !$('entity-select').value; });
$('load-entity').addEventListener('click', () => loadEntity($('entity-select').value));
$('browse-models').addEventListener('click', () => modelBrowser.open($('entity-select').value));
$('clip-search').addEventListener('input', () => renderLibrary());
$('clip-sort').addEventListener('change', () => { renderLibrary(); saveLibraryOptions(); });
$('check-c4d').addEventListener('click', checkC4DConnection);
for (const id of ['fbx-mesh', 'fbx-textures', 'fbx-root-motion']) $(id).addEventListener('change', saveExportSettings);
$('frame-model').addEventListener('click', () => viewport?.frameModel());
$('play-pause').addEventListener('click', togglePlaying);
$('prev-clip').addEventListener('click', () => nextClip(-1));
$('next-clip').addEventListener('click', () => nextClip(1));
$('step-back').addEventListener('click', () => { setPlaying(false); seek(Math.ceil(state.frame) - 1); });
$('step-forward').addEventListener('click', () => { setPlaying(false); seek(Math.floor(state.frame) + 1); });
$('timeline').addEventListener('input', () => { setPlaying(false); seek($('timeline').value); });
$('frame-input').addEventListener('change', () => { setPlaying(false); seek($('frame-input').value); $('frame-input').value = String(Math.floor(state.frame)); });
$('frame-input').addEventListener('keydown', (event) => { if (event.key === 'Enter') $('frame-input').blur(); });
$('loop-toggle').addEventListener('click', () => { state.loop = !state.loop; $('loop-toggle').setAttribute('aria-pressed', String(state.loop)); $('loop-toggle').classList.toggle('is-active', state.loop); });
$('playback-speed').addEventListener('change', () => { state.speed = Number($('playback-speed').value) || 1; });
for (const id of ['show-grid', 'show-skeleton', 'show-wireframe', 'lock-root', 'show-textures']) $(id).addEventListener('change', setOptions);
$('rig-select').addEventListener('change', () => selectObject($('rig-select').value));
$('library-object').addEventListener('change', () => selectObject($('library-object').value));
$('clear-object-animation').addEventListener('click', clearObjectAnimation);
$('export-animation-set').addEventListener('click', addAnimationSet);
$('animation-set-name').addEventListener('keydown', event => { if (event.key === 'Enter') { event.preventDefault(); addAnimationSet(); } });
$('hide-duplicates').addEventListener('change', () => { renderLibrary({reveal:true}); saveLibraryOptions(); });

document.addEventListener('keydown', (event) => {
  if (state.closing || state.closed) return;
  if (modelBrowser?.isOpen()) return;
  if (event.defaultPrevented || event.target.closest('[role=tab]')) return;
  if (document.getElementById('workflow-animations')?.hidden) return;
  if (event.target.closest('input,textarea,select,[contenteditable=true],#fbx-options-button,#export-menu,#objects-menu,#objects-button,#clear-object-animation,#animation-set,#animation-set-export-controls') || event.altKey || event.ctrlKey || event.metaKey) return;
  if (event.code === 'Space') { event.preventDefault(); if (!event.repeat) togglePlaying(); }
  else if (event.key === 'ArrowLeft') { event.preventDefault(); setPlaying(false); seek(Math.ceil(state.frame) - 1); }
  else if (event.key === 'ArrowRight') { event.preventDefault(); setPlaying(false); seek(Math.floor(state.frame) + 1); }
  else if (event.key === 'ArrowUp') { event.preventDefault(); nextClip(-1); }
  else if (event.key === 'ArrowDown') { event.preventDefault(); nextClip(1); }
  else if (event.key.toLowerCase() === 'f') viewport?.frameModel();
});
document.addEventListener('visibilitychange', () => { if (document.hidden) setPlaying(false); });
window.addEventListener('pywebviewready', connect);
window.addEventListener('beforeunload', () => {
  for (const path of state.annotationTimers.keys()) flushAnnotation(path);
});

for (const id of ['scan-root', 'browse-root', 'browse-output']) $(id).disabled = true;
renderEntities();
renderLibrary();
resetTimeline();
setOptions();
connect();
setTimeout(() => {
  if (!state.ready && !state.closed && !state.closing) setStatus('Waiting for Avolition to connect. Reopen Avolition.exe if this continues.', 'error');
}, 8000);
