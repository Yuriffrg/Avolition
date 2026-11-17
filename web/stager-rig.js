const roleLabels = {
  idle:'Idle', adsUp:'ADS Up', adsDown:'ADS Down', reload:'Reload',
  forward:'Forward', backward:'Backward', left:'Left', right:'Right', death:'Death', action:'Other action',
};
const roles = {pov:['idle','adsUp','adsDown','reload','action'], actor:['idle','forward','backward','left','right','death','action']};
const bindings = [['','Unassigned'], ['Mouse2','Right mouse'], ['Mouse0','Left mouse'], ['Mouse1','Middle mouse'],
  ...Array.from('ABCDEFGHIJKLMNOPQRSTUVWXYZ', letter => ['Key'+letter, letter]),
  ...Array.from('01256789', digit => ['Digit'+digit, digit]), ['Space','Space'], ['ShiftLeft','Left Shift'], ['ControlLeft','Left Ctrl']];

export function setupNickname(entry) {
  if (entry.motionClipName) return entry.motionClipName;
  const prefix = entry.entityPath.split(/[\\/]/).pop().replace(/\.[^.]*$/, '') + '_';
  return entry.name.startsWith(prefix) ? entry.name.slice(prefix.length) : entry.name;
}
function select(label, options, value) {
  const input = document.createElement('select');
  input.setAttribute('aria-label', label);
  for (const [id, name] of options) input.add(new Option(name, id));
  input.value = value;
  return input;
}
function suggestedRole(name, kind) {
  const normalized = name.toLowerCase().replace(/[\s_-]+/g,'');
  return roles[kind].find(role => role.toLowerCase() === normalized) || '';
}

export function editStagerRig(api, entries) {
  const $ = id => document.getElementById(id);
  const dialog = $('stager-rig-dialog'), kind = $('stager-kind'), camera = $('stager-camera');
  const status = $('stager-status'), submit = $('stager-insert');
  let review, inserting = false, result = '', rows = [];
  function updateRole(row) {
    const role = row.role.value;
    const bindable = role && role !== 'idle' && (kind.value === 'pov' || ['action','death'].includes(role));
    row.input.hidden = !bindable;
    row.edge.hidden = !bindable || role === 'death';
    row.input.options[0].disabled = role === 'death';
    row.input.value = ({adsUp:'Mouse2',adsDown:'Mouse2',reload:'KeyR',death:'KeyF'})[role] || '';
    row.edge.value = role === 'adsDown' ? 'release' : 'press';
    row.note.textContent = role === 'idle' ? 'Take start' : '';
  }
  function showTarget() {
    const existing = review?.existingRigs?.[kind.value];
    $('stager-target').textContent = existing?.targetName || review?.motionTargetName || '';
    const notice = $('stager-existing');
    notice.hidden = !existing;
    if (existing) notice.textContent = existing.needsRepair
      ? 'This registration has missing scene links. Import will repair them using the selected setups and keep your Stager bindings.'
      : 'This rig is registered. Existing sources keep their Stager bindings. Choose assignments for any new sources below.';
    submit.textContent = existing?.needsRepair ? 'Repair rig' : existing ? 'Update rig' : 'Create rig';
    camera.disabled = Boolean(existing?.cameraId);
    if (existing?.cameraId) camera.value = existing.cameraId;
  }
  function render() {
    const preset = kind.value, list = $('stager-sources');
    showTarget();
    list.replaceChildren();
    $('stager-camera-label').hidden = preset !== 'pov';
    rows = entries.map(entry => {
      const name = setupNickname(entry), line = document.createElement('div'), label = document.createElement('strong');
      label.textContent = name;
      line.className = 'stager-source';
      const row = {id:entry.id,
        role:select('Role for '+name,[['','Choose role…'], ...roles[preset].map(role=>[role,roleLabels[role]])],suggestedRole(name,preset)),
        input:select('Input for '+name, bindings, ''), edge:select('Trigger for '+name,[['press','Press'],['release','Release']],'press'),
        note:document.createElement('span')};
      row.role.onchange = () => updateRole(row);
      updateRole(row);
      line.append(label,row.role,row.input,row.edge,row.note);
      list.append(line);
      return row;
    });
  }
  function fail(text) {
    const legacyMissingRig = 'The registered rig or its Motion System target is missing. Restore it before importing.';
    status.textContent = text === legacyMissingRig
      ? text + ' To repair the links on import, install Avolition C4D Bridge 0.7.14 and Avolition Stager 0.19.1, then restart Cinema 4D.'
      : text;
    status.classList.add('error');
  }
  kind.value = /Gameplay3P\.owentity$/i.test(entries[0].entityPath) ? 'actor' : 'pov';
  camera.replaceChildren(new Option('Choose camera…',''));
  review = null; submit.disabled = true;
  $('stager-document').textContent = 'Checking Cinema 4D…'; $('stager-target').textContent = '';
  status.textContent = ''; status.classList.remove('error'); kind.onchange = render;
  $('stager-cancel').disabled = false;
  $('stager-cancel').onclick = () => dialog.close();
  dialog.oncancel = event => { if (inserting) event.preventDefault(); };
  render(); dialog.showModal();
  const done = new Promise(resolve => dialog.addEventListener('close', () => resolve(result), {once:true}));
  const request = api('stager_rig_destination', entries.map(entry=>entry.id));
  submit.onclick = async () => {
    if (!review || inserting) return;
    const actions = rows.map(row=>({id:row.id,role:row.role.value,binding:row.input.hidden?'':row.input.value,edge:row.edge.value}));
    if (actions.some(action=>!action.role)) return fail('Choose a role for every source.');
    if (actions.filter(action=>action.role==='idle').length !== 1) return fail('Assign exactly one Idle source.');
    if (kind.value==='pov' && !camera.value) return fail('Choose the POV camera in the destination project.');
    inserting = true; submit.disabled = true; $('stager-cancel').disabled = true;
    const inputs = Array.from(dialog.querySelectorAll('select')); inputs.forEach(input => input.disabled = true);
    status.classList.remove('error'); status.textContent = 'Preparing the rig in Cinema 4D…';
    try {
      const report = await api('create_stager_rig',{instance:review.instance,documentId:review.documentId,kind:kind.value,
        cameraId:kind.value==='pov'?camera.value:'',rootMotionId:'',actions});
      const outcome = report.repairedRegistration ? 'Rig registration repaired in ' : 'Rig ready in ';
      result = {message:outcome+review.documentName+'. Click Refresh scene in Stager.', warnings:report.warnings || []};
      dialog.close();
    } catch (error) { fail(error.message || String(error)); }
    finally { inserting = false; submit.disabled = false; $('stager-cancel').disabled = false; inputs.forEach(input=>input.disabled=false); showTarget(); }
  };
  // Keep the queue locked until a cancelled lookup finishes, so an earlier lookup
  // cannot populate the next dialog with another project's camera identities.
  return (async () => {
    try {
      review = await request;
      if (dialog.open) {
        $('stager-document').textContent = review.documentName;
        for (const item of review.cameras) camera.add(new Option(item.label || item.name,item.id));
        if (review.cameras.length===1) camera.value = review.cameras[0].id;
        showTarget();
        submit.disabled = false;
      }
    } catch (error) { $('stager-document').textContent = 'Unavailable'; fail(error.message || String(error)); }
    return done;
  })();
}
