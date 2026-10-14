// Evidence is a separate read-only layer. This module never loads or assigns clips,
// writes annotations, renames files, or uses the currently previewed rig as proof.
export const RESOLUTION_LABELS = Object.freeze({
  'source-name-found': 'Source name found',
  'referenced-by-named-context': 'Referenced by named context',
  'user-verified': 'User verified',
  suggested: 'Suggested',
  unknown: 'Unknown',
});

const pathKey = path => String(path || '').replaceAll('\\', '/').toLocaleLowerCase();
const items = value => Array.isArray(value) ? value : [];

// Filter occurrences before display-only duplicate collapsing. A copy under one
// context must not suppress another identical copy that matches these filters.
export function filterAnalysisClips(clips, index, filters = {}) {
  if (!filters.model && !filters.context && !filters.status) return clips;
  const entries = new Map(items(index?.entries).map(entry => [pathKey(entry.path), entry]));
  return clips.filter(clip => {
    const entry = entries.get(pathKey(clip.path));
    if (!entry) return filters.status === 'unknown' && !filters.model && !filters.context;
    return (!filters.model || items(entry.modelIds).includes(filters.model))
      && (!filters.context || items(entry.contextIds).includes(filters.context))
      && (!filters.status || items(entry.statuses).includes(filters.status));
  });
}

export function initAnimationAnalysis(api, hooks = {}) {
  const $ = id => document.getElementById(id);
  const state = {root: '', generation: 0, detailGeneration: 0, revision: null, index: null,
    clip: null, busy: false, pending: false, stopped: false, suspended: false,
    timer: null, polling: false, filters: {model: '', context: '', status: ''}};
  const filterIds = {model: 'analysis-model', context: 'analysis-context', status: 'analysis-resolution'};
  const text = (tag, content, className = '') => {
    const element = document.createElement(tag);
    element.textContent = String(content ?? '');
    if (className) element.className = className;
    return element;
  };
  const known = value => value === null || value === undefined || value === '' ? 'Unknown' : String(value);
  const displayName = item => typeof item === 'string' ? item : String(item?.value || item?.name || item?.label || item?.description || item?.message || 'Unknown');

  function message(content, error = false) {
    $('analysis-status').textContent = content;
    $('analysis-status').classList.toggle('error', error);
  }

  function controls() {
    $('analyze-library').disabled = !state.root || state.busy || state.pending || state.stopped || state.suspended;
    $('analyze-library').textContent = state.busy ? 'Analyzing…' : 'Analyze Library';
    $('analysis-spinner').hidden = !(state.busy || state.pending);
    $('analysis-all').hidden = !Object.values(state.filters).some(Boolean);
    for (const id of Object.values(filterIds)) $(id).disabled = state.stopped || state.suspended;
    const count = Object.values(state.filters).filter(Boolean).length;
    $('analysis-filter-count').textContent = count ? `· ${count} active` : '';
  }

  function choices(id, options, placeholder, selected) {
    const element = $(id);
    element.replaceChildren(new Option(placeholder, ''));
    for (const item of options) element.append(new Option(item.label, item.id));
    element.value = options.some(item => item.id === selected) ? selected : '';
    element.title = element.selectedOptions[0]?.textContent || placeholder;
    return element.value;
  }

  function showIndex(index) {
    state.index = index;
    state.filters.model = choices('analysis-model', items(index?.models), 'All models', state.filters.model);
    state.filters.context = choices('analysis-context', items(index?.contexts), 'All contexts', state.filters.context);
    const available = new Set(items(index?.entries).flatMap(entry => items(entry.statuses)));
    state.filters.status = choices('analysis-resolution', Object.entries(RESOLUTION_LABELS)
      .filter(([id]) => available.has(id)).map(([id, label]) => ({id, label})), 'All statuses', state.filters.status);
    $('analysis-filters').hidden = !index;
    controls();
    hooks.changed?.();
  }

  function pendingDetail(content) {
    $('animation-evidence-status').textContent = '';
    $('animation-evidence-content').replaceChildren(text('p', content));
  }

  function section(container, title, values) {
    container.append(text('h4', title));
    if (!values.length) container.append(text('p', 'Unknown'));
    else {
      const list = document.createElement('ul');
      for (const value of values) list.append(text('li', value));
      container.append(list);
    }
  }

  function fields(container, rows) {
    const list = document.createElement('dl');
    for (const [label, value] of rows) {
      const row = document.createElement('div');
      row.append(text('dt', label), text('dd', known(value)));
      list.append(row);
    }
    container.append(list);
  }

  function showDetail(detail, clip) {
    const container = $('animation-evidence-content');
    container.replaceChildren();
    const asset = detail.asset || {};
    const resource = asset.resource || {};
    const occurrence = detail.occurrence || {};
    const statuses = items(asset.statuses).map(status => RESOLUTION_LABELS[status] || status);
    $('animation-evidence-status').textContent = statuses.join(' · ') || 'Unknown';
    fields(container, [
      ['Original identifier', occurrence.filename || clip?.name || String(clip?.path || '').split(/[\\/]/).pop()],
      ['Full resource GUID', resource.fullGuid],
      ['Resource type', resource.type],
      ['Local asset identity', asset.id],
      ['Selected occurrence', occurrence.relativePath || occurrence.path || clip?.path],
    ]);
    if (resource.limitation) container.append(text('p', resource.limitation));
    section(container, 'Original source name', asset.sourceName ? [displayName(asset.sourceName)] : []);
    container.append(text('h4', 'Your nickname'));
    container.append(text('p', clip?.annotation?.name || occurrence.nickname?.value || 'Not set'));
    section(container, 'Model / entity associations', items(asset.models).map(item => {
      const model = item.label || item.id || item.path || 'Unknown model';
      const kind = ({'explicit-export-path': 'Export folder association', 'entity-reference': 'Entity reference',
        'compatible-bones': 'Compatible bone IDs'})[item.kind] || item.kind || 'Association';
      const matches = Number.isInteger(item.matchedBoneCount) && Number.isInteger(item.animatedBoneCount)
        ? ` · ${item.matchedBoneCount} / ${item.animatedBoneCount} animated bone IDs matched` : '';
      const entityNames = [...new Set(items(item.entityReferences).map(entity => entity.name || entity.relativePath).filter(Boolean))];
      // Put readable exported entity names ahead of resource-ID display names;
      // this is presentation ordering only, never an action-name inference.
      const identifierOnly = name => /^[0-9a-f]{12,16}(?:\.[0-9a-f]{3})?$/i.test(name);
      entityNames.sort((a, b) => Number(identifierOnly(a)) - Number(identifierOnly(b)) || a.localeCompare(b));
      const names = entityNames.slice(0, 4).join(', ');
      const more = entityNames.length > 4 ? ` (+${entityNames.length - 4} more)` : '';
      const entities = names ? ` · Entities: ${names}${more}`
        : items(item.entityIds).length ? ` · ${item.entityIds.length} entity references; inspect evidence for IDs` : '';
      return `${model} — ${kind}${matches}${entities}`;
    }));
    section(container, 'Usage contexts', items(asset.contexts).map(item => {
      const category = item.category ? ` (${item.category})` : '';
      const role = item.role ? ` — ${item.role}` : ' — Role unknown';
      return `${displayName(item)}${category}${role}`;
    }));
    section(container, 'Suggested label', items(asset.suggestedLabels).map(displayName));
    container.append(text('h4', 'Clip metadata'));
    fields(container, [
      ['Duration (seconds)', asset.durationSeconds], ['Frames per second', asset.fps],
      ['Track entries', asset.trackCount], ['Track definition', asset.trackDefinition],
      ['Priority (raw)', occurrence.priority], ['Group (raw)', occurrence.group],
      ['Game / export build', asset.gameBuild], ['Exporting DataTool version', asset.dataToolVersion],
      ['Occurrences', items(detail.occurrences).length],
    ]);
    if (items(asset.conflicts).length) section(container, 'Unresolved conflicts', asset.conflicts.map(displayName));
    if (items(asset.parseErrors).length) section(container, 'Parse errors', asset.parseErrors.map(displayName));
    if (items(detail.limitations).length) section(container, 'Limitations', detail.limitations.map(displayName));
    const raw = document.createElement('details');
    raw.append(text('summary', 'Inspect exact paths, bone IDs and evidence'));
    raw.append(text('pre', JSON.stringify(detail, null, 2)));
    container.append(raw);
    container.append(text('p', 'Model compatibility does not identify an action. Context labels do not recover an original source name.'));
  }

  async function select(clip) {
    state.clip = clip || null;
    const generation = ++state.detailGeneration;
    if (!clip) {pendingDetail('Select a clip to inspect its animation evidence.'); return;}
    if (!state.index) {
      pendingDetail(state.busy ? 'Checking local evidence. Results will appear after the current files have been analyzed.'
        : 'Analyze the library to inspect this clip. Its nickname and preview are unchanged.');
      return;
    }
    pendingDetail('Loading local evidence…');
    try {
      const detail = await api('analysis_detail', clip.path, state.root);
      if (generation !== state.detailGeneration || state.stopped) return;
      if (detail) showDetail(detail, state.clip);
      else pendingDetail('No cached evidence for this file. Analyze the library to include new or changed files.');
    } catch (error) {
      if (generation !== state.detailGeneration || state.stopped) return;
      pendingDetail(error.message || String(error));
      $('animation-evidence-content').firstChild?.classList.add('analysis-error');
    }
  }

  async function poll() {
    if (!state.root || state.stopped || state.suspended || state.polling) return;
    state.polling = true;
    clearTimeout(state.timer);
    const generation = state.generation;
    const root = state.root;
    try {
      const status = await api('analysis_status', root);
      if (generation !== state.generation || state.stopped) return;
      const wasBusy = state.busy;
      state.busy = Boolean(status.busy);
      if (status.error) message(status.error, true);
      else if (status.message) message(status.message);
      else if (state.busy) message('Reading local animation metadata…');
      else message(status.revision ? 'Local evidence ready. Filters retain every source occurrence.' : 'Inspect local metadata without changing your files.');
      if (!status.revision && (state.index || wasBusy !== state.busy)) {
        state.revision = null;
        showIndex(null);
        await select(state.clip);
      }
      if (status.revision && status.revision !== state.revision) {
        const index = await api('analysis_index', root);
        if (generation !== state.generation || state.stopped) return;
        state.revision = status.revision;
        showIndex(index);
        await select(state.clip);
      }
    } catch (error) {
      if (generation === state.generation && !state.stopped) message(error.message || String(error), true);
    } finally {
      state.polling = false;
      controls();
      if (!state.stopped && !state.suspended && (state.busy || generation !== state.generation)) state.timer = setTimeout(poll, 700);
    }
  }

  async function analyze() {
    if (!state.root || state.busy || state.pending || state.stopped || state.suspended) return;
    state.pending = true;
    controls();
    const generation = state.generation;
    try {
      await api('analysis_start', state.root);
      if (generation !== state.generation || state.stopped) return;
      await poll();
    } catch (error) {
      if (generation === state.generation && !state.stopped) message(error.message || String(error), true);
    } finally {state.pending = false; controls();}
  }

  function setRoot(root) {
    if (pathKey(root) === pathKey(state.root)) return poll();
    state.root = String(root || '');
    ++state.generation;
    ++state.detailGeneration;
    state.revision = null;
    state.busy = false;
    state.clip = null;
    state.filters = {model: '', context: '', status: ''};
    showIndex(null);
    clearTimeout(state.timer);
    message(state.root ? 'Checking cached evidence…' : 'Scan a library to analyze its local metadata.');
    pendingDetail('Analyze the library to inspect a selected clip.');
    return poll();
  }

  $('analyze-library').addEventListener('click', analyze);
  for (const [key, id] of Object.entries(filterIds)) $(id).addEventListener('change', () => {
    state.filters[key] = $(id).value;
    $(id).title = $(id).selectedOptions[0]?.textContent || '';
    controls();
    hooks.changed?.();
  });
  $('analysis-all').addEventListener('click', () => {
    state.filters = {model: '', context: '', status: ''};
    for (const id of Object.values(filterIds)) $(id).value = '';
    controls();
    hooks.allAnimations?.();
    hooks.changed?.();
  });
  const dismissFilters = event => {
    if (!$('analysis-filters').contains(event.target)) $('analysis-filters').open = false;
  };
  const escapeFilters = event => { if (event.key === 'Escape') $('analysis-filters').open = false; };
  document.addEventListener('pointerdown', dismissFilters);
  document.addEventListener('keydown', escapeFilters);
  controls();
  return {
    setRoot, select, analyze, refresh: poll,
    filtered: clips => filterAnalysisClips(clips, state.index, state.filters),
    suspend() {state.suspended = true; clearTimeout(state.timer); controls();},
    resume() {state.suspended = false; controls(); poll();},
    stop() {
      state.stopped = true; ++state.detailGeneration; clearTimeout(state.timer); controls();
      document.removeEventListener('pointerdown', dismissFilters);
      document.removeEventListener('keydown', escapeFilters);
    },
  };
}
