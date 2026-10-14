// Presentation only. Installation, validation and process ownership live in Python.
export function formatToolchainDate(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return 'Unknown';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? 'Unknown' : `${date.toISOString().replace('T', ' ').replace(/\.\d{3}Z$/, '')} UTC`;
}

export function sameDirectory(left, right) {
  const normalize = value => String(value || '').replaceAll('/', '\\').replace(/\\+$/, '').toLocaleLowerCase();
  return Boolean(left && right) && normalize(left) === normalize(right);
}

export function initToolchain(api, hooks = {}, bootstrap = {}) {
  const $ = id => document.getElementById(id);
  if (!$('workflow-toolchain')) return null;
  const tabs = [...document.querySelectorAll('[data-workflow]')];
  const extractionTabs = [...document.querySelectorAll('[data-toolchain-workflow]')];
  const state = {
    workflow: {overwatchInstall: '', outputDirectory: '', libraryDirectory: '', activeTab: 'animations', ...(bootstrap.workflow || {})},
    status: {runtime: {}, job: {}, capabilities: {}},
    saving: Promise.resolve(), pending: false, polling: false, stopped: false, suspended: false, timer: null,
    processedJob: '', exportOutput: '', catalogSignature: '', mapCatalogSignature: '', categoriesSignature: '', logSignature: '', settingsRevision: 0, statusRevision: 0,
    extractionWorkflow: bootstrap.workflow?.toolchainTab === 'maps' ? 'maps' : 'heroes',
  };
  const busy = () => Boolean(state.status.job?.busy);
  const catalogCurrent = () => sameDirectory(state.status.job?.catalog?.install, state.workflow.overwatchInstall);
  const mapsCurrent = () => sameDirectory(state.status.job?.mapCatalog?.install, state.workflow.overwatchInstall);
  const runtimeBusy = () => Boolean(state.status.runtime?.busy) || ['checking', 'downloading', 'extracting', 'installing', 'validating', 'activating'].includes(String(state.status.runtime?.state || '').toLowerCase());
  const setMessage = (id, message, kind = '') => {
    $(id).textContent = message || '';
    $(id).className = `toolchain-message ${kind}`;
  };

  function showValidation(errors = {}, keys = ['overwatchInstall', 'outputDirectory']) {
    for (const [key, id] of [['overwatchInstall', 'overwatch-install'], ['outputDirectory', 'toolchain-output']]) {
      if (!keys.includes(key)) continue;
      const message = errors[key] || '';
      $(`${id}-error`).textContent = message;
      $(`${id}-error`).hidden = !message;
      $(id).setAttribute('aria-invalid', String(Boolean(message)));
    }
  }

  function showWorkflow(workflow, {paths = true} = {}) {
    state.workflow = {...state.workflow, ...workflow};
    if (paths) {
      $('overwatch-install').value = state.workflow.overwatchInstall || '';
      $('toolchain-output').value = state.workflow.outputDirectory || '';
    }
    $('overwatch-install').title = $('overwatch-install').value;
    $('toolchain-output').title = $('toolchain-output').value;
    $('use-library-directory').title = state.workflow.libraryDirectory || 'Restore the Avolition library folder';
    updateCatalog();
    updateControls();
  }

  function saveSettings(patch) {
    const revision = ++state.settingsRevision;
    state.workflow = {...state.workflow, ...patch};
    updateCatalog();
    updateControls();
    const operation = state.saving.catch(() => {}).then(() => api('workflow_settings', patch));
    state.saving = operation;
    operation.then(workflow => { if (revision === state.settingsRevision) showWorkflow(workflow, {paths: false}); }).catch(error => {
      const message = error.message || String(error);
      const errors = {};
      for (const key of ['overwatchInstall', 'outputDirectory']) if (key in patch) errors[key] = message;
      if (Object.keys(errors).length) showValidation(errors);
      else setMessage('toolchain-message', message, 'error');
    });
    return operation;
  }

  function selectTab(id, persist = true) {
    if (state.suspended) return;
    if (!tabs.some(tab => tab.dataset.workflow === id)) id = 'animations';
    for (const tab of tabs) {
      const active = tab.dataset.workflow === id;
      tab.setAttribute('aria-selected', String(active));
      tab.tabIndex = active ? 0 : -1;
      $(tab.getAttribute('aria-controls')).hidden = !active;
    }
    $('toolchain-directories').hidden = id !== 'toolchain';
    // No reparenting or innerHTML: all animation selections, canvas and playback survive.
    state.workflow.activeTab = id;
    hooks.activeChanged?.(id);
    if (persist) saveSettings({activeTab: id});
    if (id === 'toolchain') poll();
  }

  function option(value, label) {
    const item = document.createElement('option');
    item.value = value;
    item.textContent = label;
    return item;
  }

  function selectExtractionWorkflow(workflow, persist = true) {
    if (state.suspended) return;
    state.extractionWorkflow = workflow === 'maps' ? 'maps' : 'heroes';
    for (const tab of extractionTabs) {
      const active = tab.dataset.toolchainWorkflow === state.extractionWorkflow;
      tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1;
      $(tab.getAttribute('aria-controls')).hidden = !active;
    }
    state.workflow.toolchainTab = state.extractionWorkflow;
    if (persist) saveSettings({toolchainTab: state.extractionWorkflow});
    updateControls();
  }

  function updateMapDetails() {
    if (!$('toolchain-map')) return;
    const map = (mapsCurrent() ? state.status.job?.mapCatalog?.maps || [] : []).find(item => item.id === $('toolchain-map').value);
    $('toolchain-map').title = map ? `${map.name} · ${map.mapGuid}` : '';
    $('toolchain-map-description').textContent = map
      ? [map.mapType, ...(map.gameModes || []), map.description].filter(Boolean).join(' · ')
      : $('toolchain-map').value === '*' ? 'Export every listed map and its available variants. This may take a long time.'
        : 'Load maps from your Overwatch installation, then choose a map or all maps.';
  }

  function updateAssets() {
    const select = $('toolchain-asset');
    const current = select.value;
    const hero = $('toolchain-hero').value;
    const category = $('toolchain-category').value;
    const assets = (catalogCurrent() ? state.status.job?.catalog?.unlocks?.[hero] || [] : []).filter(item => item.category === category);
    select.replaceChildren();
    if (!hero || !category) select.append(option('', 'Choose a hero and category first'));
    else {
      select.append(option('*', 'All assets in this category'));
      for (const asset of assets) select.append(option(asset.name, asset.name));
      if (assets.some(asset => asset.name === current)) select.value = current;
    }
    select.title = select.selectedOptions[0]?.textContent || '';
    select.disabled = !hero || !category || busy() || state.pending;
    const description = (state.status.capabilities?.categories || []).find(item => item.id === category)?.description;
    $('toolchain-category-description').textContent = description || '';
    updateControls();
  }

  function updateCatalog() {
    const categories = state.status.capabilities?.categories || [];
    const categoriesSignature = JSON.stringify(categories);
    let changed = false;
    if (categoriesSignature !== state.categoriesSignature) {
      state.categoriesSignature = categoriesSignature;
      const current = $('toolchain-category').value;
      $('toolchain-category').replaceChildren(...categories.map(item => option(item.id, item.label)));
      if (!categories.length) $('toolchain-category').append(option('', 'No supported workflows available'));
      if (categories.some(item => item.id === current)) $('toolchain-category').value = current;
      changed = true;
    }
    const catalog = catalogCurrent() ? state.status.job.catalog : {};
    const signature = JSON.stringify(catalog);
    if (signature !== state.catalogSignature) {
      state.catalogSignature = signature;
      const current = $('toolchain-hero').value;
      const heroes = catalog.heroes || [];
      $('toolchain-hero').replaceChildren(option('', heroes.length ? 'Choose a hero' : 'Load heroes from your installation'));
      for (const hero of heroes) $('toolchain-hero').append(option(hero.name, hero.name));
      if (heroes.some(hero => hero.name === current)) $('toolchain-hero').value = current;
      changed = true;
    }
    if (changed) updateAssets();
    if ($('toolchain-map')) {
      const maps = mapsCurrent() ? state.status.job?.mapCatalog?.maps || [] : [];
      const signature = JSON.stringify([state.workflow.overwatchInstall, maps]);
      if (signature !== state.mapCatalogSignature) {
        state.mapCatalogSignature = signature;
        const current = $('toolchain-map').value;
        $('toolchain-map').replaceChildren(option('', maps.length ? 'Choose a map' : 'Load maps from your installation'));
        if (maps.length) $('toolchain-map').append(option('*', 'All maps'));
        for (const map of maps) $('toolchain-map').append(option(map.id, `${map.name} · ${map.mapGuid}`));
        if (maps.length && (current === '*' || maps.some(map => map.id === current))) $('toolchain-map').value = current;
        updateMapDetails();
      }
    }
  }

  function updateControls() {
    const runtime = state.status.runtime || {};
    const unavailable = !runtime.installed || runtime.executable === null;
    const locked = state.suspended || state.pending || busy() || runtimeBusy();
    $('toolchain-install').hidden = !unavailable;
    $('toolchain-install').disabled = locked;
    $('toolchain-update').hidden = unavailable || !runtime.updateAvailable;
    $('toolchain-update').disabled = locked;
    $('toolchain-check').disabled = locked;
    $('toolchain-load-heroes').disabled = locked || unavailable;
    $('toolchain-load-assets').disabled = locked || unavailable;
    $('toolchain-category').disabled = locked || !state.status.capabilities?.categories?.length;
    $('toolchain-hero').disabled = locked || !catalogCurrent() || !state.status.job?.catalog?.heroes?.length;
    $('toolchain-asset').disabled = locked || !catalogCurrent() || !$('toolchain-hero').value || !$('toolchain-category').value;
    const selected = state.extractionWorkflow === 'maps'
      ? mapsCurrent() && Boolean($('toolchain-map')?.value)
      : catalogCurrent() && $('toolchain-hero').value && $('toolchain-category').value && $('toolchain-asset').value;
    $('toolchain-export').disabled = locked || unavailable || !selected;
    if ($('toolchain-map')) {
      $('toolchain-map').disabled = locked || !mapsCurrent() || !state.status.job?.mapCatalog?.maps?.length;
      $('toolchain-load-maps').disabled = locked || unavailable;
    }
    $('toolchain-cancel').disabled = state.suspended || !busy() || state.pending;
    for (const input of document.querySelectorAll('#toolchain-export-options input, #toolchain-map-options input')) input.disabled = locked;
    for (const tab of extractionTabs) tab.disabled = state.suspended;
    // Shared paths can change while an export runs. The backend holds that job's captured paths.
    for (const id of ['browse-overwatch-install', 'browse-toolchain-output']) {
      $(id).disabled = state.suspended || Boolean(bootstrap.browserMode);
      $(id).title = bootstrap.browserMode ? 'Folder browsing is available in the Avolition desktop application. You can paste a path here.' : '';
    }
    for (const input of document.querySelectorAll('.workflow-directory input, #use-library-directory, [data-workflow]')) input.disabled = state.suspended;
  }

  function renderStatus() {
    const runtime = state.status.runtime || {};
    const installed = runtime.installed;
    $('toolchain-version').textContent = installed?.version || (installed ? 'Unknown' : 'Not installed');
    $('toolchain-release-date').textContent = installed ? formatToolchainDate(installed.releaseDate) : 'Unavailable';
    $('toolchain-package-date').textContent = installed ? formatToolchainDate(installed.packageUpdatedAt) : 'Unavailable';
    $('toolchain-local-date').hidden = !installed;
    $('toolchain-local-date').textContent = installed ? `Installed / updated locally: ${formatToolchainDate(installed.installedAt)}` : '';
    $('toolchain-state').textContent = runtimeBusy() ? runtime.state : (installed && runtime.executable === null ? 'Runtime unavailable' : installed ? 'Installed' : 'Not installed');
    $('toolchain-state').title = runtime.executable || '';
    const latest = runtime.latest;
    $('toolchain-available').hidden = !latest;
    $('toolchain-available').textContent = latest ? `${runtime.updateAvailable ? 'Update available' : 'Latest checked package'}: ${latest.version || 'Unknown'} · released ${formatToolchainDate(latest.releaseDate)} · package updated ${formatToolchainDate(latest.packageUpdatedAt)}` : '';
    const missingRuntime = installed && runtime.executable === null ? 'The installed DataTool executable is missing. Install Toolchain again to restore it.' : '';
    setMessage('toolchain-message', runtime.error || missingRuntime || runtime.message || '', runtime.error || missingRuntime ? 'error' : '');
    const job = state.status.job || {};
    $('toolchain-job-state').textContent = job.state || 'Ready';
    $('toolchain-job-spinner').hidden = !job.busy;
    setMessage('toolchain-job-message', job.error || job.message || '', job.error ? 'error' : job.state === 'Completed' ? 'success' : '');
    const logs = job.logs || [];
    const logSignature = `${job.jobId || ''}:${logs.length}:${logs.at(-1)?.seq || ''}:${logs.at(-1)?.text || ''}`;
    if (logSignature !== state.logSignature) {
      state.logSignature = logSignature;
      const log = $('toolchain-log');
      const follow = log.scrollHeight - log.scrollTop - log.clientHeight < 45;
      log.textContent = logs.map(line => typeof line === 'string' ? line : `${line.stream === 'stderr' ? '[stderr] ' : ''}${line.text || ''}`).join('\n');
      $('toolchain-log-count').textContent = logs.length ? `(${logs.length} lines)` : '';
      if (follow) log.scrollTop = log.scrollHeight;
    }
    if (job.state === 'Failed') $('toolchain-log-panel').open = true;
    updateCatalog();
    updateControls();
    if (job.jobId && job.kind === 'export' && job.state === 'Completed' && state.processedJob !== job.jobId) {
      state.processedJob = job.jobId;
      const output = job.result?.outputDirectory || state.exportOutput;
      if (sameDirectory(output, state.workflow.libraryDirectory)) {
        Promise.resolve(hooks.refreshLibrary?.(output)).catch(error => setMessage('toolchain-job-message', `Export completed. Library refresh failed: ${error.message || error}`, 'error'));
      }
    }
  }

  async function poll() {
    if (state.stopped || state.polling) return;
    clearTimeout(state.timer);
    state.polling = true;
    try {
      const revision = state.statusRevision;
      const snapshot = await api('toolchain_status');
      if (revision === state.statusRevision) {state.status = snapshot; renderStatus();}
    } catch (error) {
      setMessage('toolchain-message', error.message || error, 'error');
    } finally {
      state.polling = false;
      if (!state.stopped) state.timer = setTimeout(poll, busy() || runtimeBusy() || state.workflow.activeTab === 'toolchain' ? 1000 : 5000);
    }
  }

  async function runAction(action) {
    if (state.pending || state.suspended || state.stopped) return;
    state.pending = true;
    updateControls();
    try {
      const result = await action();
      if (result?.runtime && result?.job) {state.statusRevision += 1; state.status = result; renderStatus();}
      await poll();
    }
    catch (error) { setMessage('toolchain-job-message', error.message || error, 'error'); }
    finally { state.pending = false; updateControls(); }
  }

  async function persistPaths() {
    await saveSettings({overwatchInstall: $('overwatch-install').value.trim(), outputDirectory: $('toolchain-output').value.trim()});
    await state.saving;
  }

  async function validate() {
    await persistPaths();
    const result = await api('toolchain_validate');
    const normalized = {};
    for (const key of ['overwatchInstall', 'outputDirectory']) {
      if (!result.errors?.[key] && result[key] && result[key] !== state.workflow[key]) normalized[key] = result[key];
    }
    // DataTool accepts the game root; selecting _retail_ is normalized by validation.
    if (Object.keys(normalized).length) showWorkflow(await saveSettings(normalized));
    showValidation(result.errors || {});
    return result.ok !== false && !Object.values(result.errors || {}).some(Boolean);
  }

  async function validateField(key, revision = state.settingsRevision) {
    try {
      const result = await api('toolchain_validate');
      if (revision !== state.settingsRevision || state.stopped || state.suspended) return;
      if (!result.errors?.[key] && result[key] && result[key] !== state.workflow[key]) showWorkflow(await saveSettings({[key]: result[key]}));
      showValidation(result.errors || {}, [key]);
    } catch (error) {
      if (revision === state.settingsRevision) showValidation({[key]: error.message || String(error)}, [key]);
    }
  }

  for (const tab of tabs) {
    tab.addEventListener('click', () => selectTab(tab.dataset.workflow));
    tab.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const index = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (tabs.indexOf(tab) + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
      tabs[index].focus();
      selectTab(tabs[index].dataset.workflow);
    });
  }
  for (const tab of extractionTabs) {
    tab.addEventListener('click', () => selectExtractionWorkflow(tab.dataset.toolchainWorkflow));
    tab.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      const index = event.key === 'Home' ? 0 : event.key === 'End' ? extractionTabs.length - 1
        : (extractionTabs.indexOf(tab) + (event.key === 'ArrowRight' ? 1 : -1) + extractionTabs.length) % extractionTabs.length;
      extractionTabs[index].focus(); selectExtractionWorkflow(extractionTabs[index].dataset.toolchainWorkflow);
    });
  }
  for (const [key, inputId, browseId] of [['overwatchInstall', 'overwatch-install', 'browse-overwatch-install'], ['outputDirectory', 'toolchain-output', 'browse-toolchain-output']]) {
    $(inputId).addEventListener('change', () => {
      $(inputId).title = $(inputId).value;
      showValidation({}, [key]);
      const saved = saveSettings({[key]: $(inputId).value.trim()});
      const revision = state.settingsRevision;
      saved.then(() => validateField(key, revision)).catch(() => {});
    });
    $(browseId).addEventListener('click', () => runAction(async () => {
      await state.saving.catch(() => {});
      const path = await api('browse_workflow_directory', key);
      if (!path) return;
      showWorkflow(await saveSettings({[key]: path}));
      await validateField(key);
    }));
  }
  $('use-library-directory').addEventListener('click', () => runAction(async () => {
    await state.saving.catch(() => {});
    showWorkflow(await api('use_library_directory'));
    await validateField('outputDirectory');
  }));
  $('toolchain-check').addEventListener('click', () => runAction(() => api('toolchain_check')));
  for (const id of ['toolchain-install', 'toolchain-update']) $(id).addEventListener('click', () => runAction(() => api('toolchain_install')));
  const loadCatalog = () => runAction(async () => { if (await validate()) return api('toolchain_list'); });
  $('toolchain-load-heroes').addEventListener('click', loadCatalog);
  $('toolchain-load-assets').addEventListener('click', loadCatalog);
  $('toolchain-load-maps')?.addEventListener('click', () => runAction(async () => {
    if (await validate()) return api('toolchain_list', {workflow: 'maps'});
  }));
  $('toolchain-map')?.addEventListener('change', () => { updateMapDetails(); updateControls(); });
  $('toolchain-category').addEventListener('change', updateAssets);
  $('toolchain-hero').addEventListener('change', updateAssets);
  $('toolchain-asset').addEventListener('change', updateControls);
  for (const [id, label, checked] of [
    ['includeAnimations', 'Include animations', true],
    ['includeTextures', 'Include textures', true],
    ['allLods', 'All levels of detail', false],
  ]) {
    for (const [container, prefix] of [['toolchain-export-options', 'toolchain-option'], ['toolchain-map-options', 'toolchain-map-option']]) {
      if (!$(container) || (container === 'toolchain-map-options' && id === 'includeAnimations')) continue;
      const row = document.createElement('label');
      const input = document.createElement('input');
      input.type = 'checkbox'; input.id = `${prefix}-${id}`; input.checked = checked; input.dataset.option = id;
      row.append(input, document.createTextNode(label));
      const option = document.createElement('div');
      option.append(row);
      if (id === 'allLods') {
        const note = document.createElement('p');
        note.id = `${input.id}-note`; note.className = 'toolchain-option-note';
        note.textContent = 'Unchecked exports the highest available detail';
        input.setAttribute('aria-describedby', note.id);
        option.append(note);
      }
      $(container).append(option);
    }
  }
  $('toolchain-export').addEventListener('click', () => runAction(async () => {
    const maps = state.extractionWorkflow === 'maps';
    const selection = maps ? {workflow: 'maps', mapId: $('toolchain-map').value, includeAnimations: false}
      : {category: $('toolchain-category').value, hero: $('toolchain-hero').value, asset: $('toolchain-asset').value};
    selection.includeSound = false;
    for (const input of document.querySelectorAll(maps ? '#toolchain-map-options input' : '#toolchain-export-options input')) selection[input.dataset.option] = input.checked;
    if (!(await validate())) return;
    state.exportOutput = state.workflow.outputDirectory;
    return api('toolchain_start', selection);
  }));
  $('toolchain-cancel').addEventListener('click', () => runAction(() => api('toolchain_cancel')));
  window.addEventListener('beforeunload', () => { state.stopped = true; clearTimeout(state.timer); });
  showWorkflow(state.workflow);
  selectExtractionWorkflow(state.extractionWorkflow, false);
  selectTab(state.workflow.activeTab, false);
  poll();
  return {
    refresh: poll, flush: persistPaths, settings: () => ({...state.workflow}), selectTab,
    suspend() {state.suspended = true; updateControls();},
    resume() {state.suspended = false; updateControls();},
    stop() {state.stopped = true; state.suspended = true; clearTimeout(state.timer); updateControls();},
  };
}
