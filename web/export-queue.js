// Entries capture source paths and per-object assignments. Selection is deliberately
// session-only: restoring a list never opts an entry into writing files.
export function createExportQueue(api, hooks) {
  const $ = id => document.getElementById(id);
  let entries = [], selected = new Set(), results = new Map();
  let mutation = null, batch = null;
  const busy = () => Boolean(mutation || batch || hooks.closing());
  const sourceName = value => String(value || '').split(/[\\/]/).pop();
  function message(text, error = false) {
    $('export-queue-status').textContent = text;
    $('export-queue-status').classList.toggle('error', error);
  }
  function render() {
    const list = $('export-queue-list'); list.replaceChildren();
    for (const entry of entries) {
      const row = document.createElement('div'); row.className = 'export-queue-row'; row.role = 'listitem'; row.dataset.exportId = entry.id;
      const check = document.createElement('input'); check.type = 'checkbox'; check.className = 'export-queue-check'; check.dataset.exportId = entry.id;
      check.checked = selected.has(entry.id); check.disabled = busy(); check.setAttribute('aria-label', `Export ${entry.name}`);
      check.addEventListener('change', () => { check.checked ? selected.add(entry.id) : selected.delete(entry.id); updateActions(); });
      const info = document.createElement('div'); info.className = 'export-queue-info';
      const name = document.createElement('span'); name.className = 'export-queue-name'; name.textContent = `${entry.name}.c4d`;
      const detail = document.createElement('span'); detail.className = 'export-queue-assignment';
      detail.textContent = `${sourceName(entry.entityPath)} · ${entry.assignments.length} assigned animation${entry.assignments.length === 1 ? '' : 's'} · whole entity`;
      info.title = [entry.entityPath, ...entry.assignments.map(item => `${item.modelId}: ${item.clipPath}`)].join('\n'); info.append(name, detail);
      const status = document.createElement('span'); status.className = 'export-queue-state';
      const result = results.get(entry.id); status.textContent = result?.label || 'Ready'; status.title = result?.detail || '';
      if (result?.error) status.classList.add('error');
      const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'export-queue-remove'; remove.textContent = 'Remove';
      remove.setAttribute('aria-label', `Remove ${entry.name} from export list`); remove.disabled = busy();
      remove.addEventListener('click', () => removeEntry(entry.id)); row.append(check, info, status, remove); list.append(row);
    }
    if (!entries.length) {
      const empty = document.createElement('p'); empty.className = 'export-queue-empty';
      empty.textContent = 'Name an animation setup and choose Add to export. Then check the entries you want to save.'; list.append(empty);
    }
    updateActions();
  }
  function updateActions() {
    $('export-queue-summary').textContent = `${selected.size} of ${entries.length} selected`;
    $('export-selected').disabled = !selected.size || busy();
    $('export-selected').textContent = batch ? 'Exporting…' : 'Export selected';
    $('fbx-options-button').title = `Export Manager · ${entries.length} entries`;
    for (const id of ['output-path', 'fbx-mesh', 'fbx-textures', 'fbx-root-motion']) $(id).disabled = busy();
    $('browse-output').disabled = busy() || hooks.browserMode();
    $('open-export-output').disabled = busy();
    hooks.changed();
  }
  async function removeEntry(id) {
    if (busy()) return;
    mutation = (async () => {
      try { const result = await api('export_queue_remove', id); entries = result.entries; selected.delete(id); results.delete(id); message('Removed from the list. Existing exports are unchanged.'); }
      catch (error) { message(error.message || String(error), true); }
    })(); render();
    try { await mutation; } finally { mutation = null; render(); }
  }
  async function add(snapshot) {
    if (busy()) throw new Error('Wait for the current export-list operation to finish.');
    const same = entries.find(entry => entry.name === snapshot.name && entry.entityPath === snapshot.entityPath && entry.rootPath === snapshot.rootPath
      && entry.motionClipName === snapshot.motionClipName
      && JSON.stringify(entry.assignments) === JSON.stringify(snapshot.assignments)
      && JSON.stringify(entry.objectNames || {}) === JSON.stringify(snapshot.objectNames || {}));
    if (same) return {entry:same, existing:true};
    mutation = api('export_queue_save', snapshot); render();
    try { const result = await mutation; entries = result.entries; selected.delete(result.entry.id); return result; }
    finally { mutation = null; render(); }
  }
  async function runSelected() {
    if (busy() || !selected.size) return;
    const jobs = entries.filter(entry => selected.has(entry.id)).map(entry => ({...entry}));
    // Capture settings once for the entire batch, independently of preview state.
    const output = $('output-path').value.trim(), options = hooks.settings();
    if (!output) { message('Choose an output directory before exporting.', true); $('output-path').focus(); return; }
    batch = (async () => {
      let completed = 0, failed = 0;
      try {
        if (!await hooks.saveOutput()) throw new Error($('output-help').textContent || 'Output directory could not be saved.');
        await hooks.saveSettings();
        for (const [index, entry] of jobs.entries()) {
          results.set(entry.id, {label:'Exporting…'}); message(`Exporting ${index + 1} of ${jobs.length}: ${entry.name}`); render();
          try {
            const result = await api('export_queue_run', entry.id, output, options);
            const file = result.c4dPath || result.path;
            if (result.skipped || !file) throw new Error('Cinema 4D did not return a saved scene.');
            completed++; selected.delete(entry.id);
            results.set(entry.id, {label:'Completed', detail:[file, ...(result.warnings || [])].join('\n')});
          } catch (error) { failed++; results.set(entry.id, {label:'Failed', error:true, detail:error.message || String(error)}); }
          render();
        }
        const failures = jobs.filter(entry => results.get(entry.id)?.error).map(entry => `${entry.name}: ${results.get(entry.id).detail}`);
        message(`${completed} completed${failed ? ` · ${failed} failed. ${failures.join(' · ')}` : '.'}`, failed > 0);
      } catch (error) { message(error.message || String(error), true); }
    })(); render();
    try { await batch; } finally { batch = null; render(); }
  }
  $('fbx-options-button').addEventListener('click', () => { render(); $('export-menu').showModal(); $('fbx-options-button').setAttribute('aria-expanded', 'true'); });
  $('close-export-menu').addEventListener('click', () => $('export-menu').close());
  $('export-menu').addEventListener('close', () => $('fbx-options-button').setAttribute('aria-expanded', 'false'));
  $('export-selected').addEventListener('click', runSelected);
  $('open-export-output').addEventListener('click', async () => {
    try { if (!await hooks.saveOutput()) return; const result = await api('open_export_output'); if (typeof result === 'string') message(`Output directory: ${result}`); }
    catch (error) { message(error.message || String(error), true); }
  });
  return {
    restore(value) { entries = Array.isArray(value) ? value : []; selected.clear(); results.clear(); render(); },
    add, busy, render, exporting: () => Boolean(batch),
    async flush() { await mutation; await batch; },
  };
}
