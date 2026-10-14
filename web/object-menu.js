// Visibility belongs to the viewport, never to the source asset or export settings.
// Parent links come from the loaded asset. Missing/cyclic links remain roots;
// neither bone counts nor object names are used to infer a relationship.
function validParent(key, parents) {
  const seen = new Set([key]);
  let current = parents.get(key);
  while (current != null && parents.has(current)) {
    if (seen.has(current)) return null;
    seen.add(current); current = parents.get(current);
  }
  return parents.has(parents.get(key)) ? parents.get(key) : null;
}

export function buildObjectHierarchy(objects = []) {
  const models = new Map();
  for (const object of objects) {
    if (object?.id == null || models.has(object.id)) continue;
    models.set(object.id, {key:`model:${object.id}`, kind:'model', name:object.name || object.id,
      modelId:object.id, object, children:[]});
  }
  const parents = new Map([...models].map(([id, node]) => [id, node.object.parentId ?? null]));
  for (const node of models.values()) {
    for (const [index, mesh] of (node.object.meshes || []).entries()) {
      node.children.push({key:`mesh:${node.modelId}:${mesh.id}`, kind:'mesh',
        name:mesh.name || `Mesh ${index + 1}`, modelId:node.modelId, meshId:mesh.id,
        object:node.object, children:[]});
    }
    const hierarchy = node.object.boneHierarchy;
    if (Array.isArray(hierarchy) && hierarchy.length) {
      const skeleton = {key:`skeleton:${node.modelId}`, kind:'skeleton',
        name:`Skeleton (${hierarchy.length} bones)`, modelId:node.modelId, children:[]};
      const bones = hierarchy.map((bone, index) => ({key:`bone:${node.modelId}:${index}`,
        kind:'bone', name:bone.name || String(bone.id ?? `Bone ${index + 1}`),
        modelId:node.modelId, boneId:bone.id ?? index, boneIndex:index, children:[]}));
      const boneParents = new Map(hierarchy.map((bone, index) => [index,
        Number.isInteger(bone.parentIndex) && bone.parentIndex >= 0 && bone.parentIndex < bones.length
          ? bone.parentIndex : null]));
      bones.forEach((bone, index) => {
        const parent = validParent(index, boneParents);
        (parent == null ? skeleton : bones[parent]).children.push(bone);
      });
      node.children.push(skeleton);
    }
  }
  const roots = [];
  for (const [id, node] of models) {
    const parent = validParent(id, parents);
    (parent == null ? roots : models.get(parent).children).push(node);
  }
  return roots;
}

export function validateObjectName(value) {
  if (typeof value !== 'string') throw new Error('Enter an object name.');
  if (/[\u0000-\u001f\u007f-\u009f\ud800-\udfff]/u.test(value)) throw new Error('Object names cannot contain control characters.');
  const name = value.trim();
  if (!name) throw new Error('Object names cannot be empty.');
  if (name.length > 160) throw new Error('Keep object names within 160 characters.');
  return name;
}

export class ObjectVisibilityMenu {
  constructor(viewport, onSelectModel = null, options = {}) {
    this.viewport = viewport;
    this.onSelectModel = onSelectModel;
    this.onRename = options.onRename;
    this.compactLargeScenes = options.compactLargeScenes === true;
    this.prefix = options.prefix || 'objects';
    this.labels = {scene:'Loaded entity', empty:'Load an entity or model to view its objects.', tree:'Loaded object hierarchy', ...options.labels};
    const ids = options.ids || {};
    const get = (key, fallback) => {
      const value = ids[key] || fallback;
      if (value instanceof Element) return value;
      return options.container?.querySelector(`[id="${value}"]`) || document.getElementById(value);
    };
    this.panel = get('panel', `${this.prefix}-menu`);
    this.list = get('list', `${this.prefix}-list`);
    if (!this.list) throw new Error('The object tree container is missing.');
    this.summary = get('summary', `${this.prefix}-summary`);
    this.showAll = get('showAll', `${this.prefix}-show-all`);
    this.hideAll = get('hideAll', `${this.prefix}-hide-all`);
    this.polygonCount = get('polygonCount', this.prefix === 'objects' ? 'polygon-count' : `${this.prefix}-polygon-count`);
    this.renameStatus = get('status', `${this.prefix}-rename-status`);
    if (this.onRename && !this.renameStatus) {
      this.renameStatus = document.createElement('p'); this.renameStatus.id = `${this.prefix}-rename-status`;
      this.renameStatus.className = 'object-rename-status'; this.renameStatus.setAttribute('role', 'status');
      this.renameStatus.setAttribute('aria-live', 'polite'); this.renameStatus.hidden = true;
      (this.panel || this.list.parentElement).append(this.renameStatus);
    }
    this.names = {}; this.editing = null; this.pendingRenames = new Set(); this.renameFailures = new Map();
    this.refreshPending = false;
    this.activeModelId = null;
    this.bindingsSummary = new Map();
    this.sceneName = this.labels.scene;
    this.sceneKey = '';
    this.expanded = new Map();
    this.focusKey = 'scene';
    this.inputs = [];
    this.targets = [];
    this.nodes = new Map();
    this.list.setAttribute('role', 'tree');
    this.list.setAttribute('aria-label', this.labels.tree);
    this.list.addEventListener('keydown', event => this.keydown(event));
    this.showAll?.addEventListener('click', () => {
      this.viewport?.setAllObjectsVisible(true); this.update();
    });
    this.hideAll?.addEventListener('click', () => {
      this.viewport?.setAllObjectsVisible(false); this.update();
    });
    this.refresh();
  }

  setSceneName(name, sceneKey = name) {
    if (this.sceneKey !== sceneKey) {
      this.cancelRename(); this.names = {};
      this.sceneKey = sceneKey; this.expanded.clear(); this.focusKey = 'scene';
    }
    this.sceneName = name || this.labels.scene;
    this.refresh();
  }

  setNames(names = {}) {
    this.names = names && typeof names === 'object' && !Array.isArray(names) ? {...names} : {};
    for (const record of this.nodes.values()) this.displayName(record);
  }

  displayName(record) {
    const {node, name, expander} = record;
    const display = this.names[node.key] || node.name;
    name.textContent = display;
    name.title = `Original: ${node.name}${this.canRename(node) ? '\nDouble-click or press F2 to rename for export.' : ''}`;
    if (record.group) expander.setAttribute('aria-label', `${record.group.hidden ? 'Expand' : 'Collapse'} ${display}`);
  }

  canRename(node) { return !!this.onRename && ['scene', 'model', 'mesh'].includes(node.kind); }

  renameMessage(text = '', error = false) {
    if (!this.renameStatus) return;
    this.renameStatus.textContent = text; this.renameStatus.hidden = !text;
    this.renameStatus.classList.toggle('error', error);
  }

  async beginRename(key) {
    const record = this.nodes.get(key);
    if (!record || !this.canRename(record.node)) return;
    if (this.editing?.key === key) return;
    if (this.editing && !await this.commitRename(false)) return;
    // A previous save can finish after another asset has been loaded.
    if (this.nodes.get(key) !== record) return;
    const input = document.createElement('input'); input.type = 'text'; input.className = 'object-rename-input';
    input.maxLength = 160; input.value = this.names[key] || record.node.name;
    input.setAttribute('aria-label', `Rename ${record.node.name}`); input.spellcheck = false;
    input.addEventListener('click', event => event.stopPropagation());
    input.addEventListener('dblclick', event => event.stopPropagation());
    input.addEventListener('keydown', event => {
      event.stopPropagation();
      if (event.key === 'Enter') { event.preventDefault(); if (!event.isComposing) void this.commitRename(true); }
      else if (event.key === 'Escape') { event.preventDefault(); this.cancelRename(true); }
    });
    input.addEventListener('blur', () => { if (this.editing?.input === input) void this.commitRename(false); });
    this.editing = {key, sceneKey:this.sceneKey, record, input, promise:null};
    record.name.hidden = true; record.row.insertBefore(input, record.name);
    this.renameMessage(); input.focus(); input.select();
  }

  finishRename(edit, focus = false) {
    if (this.editing !== edit) return;
    this.editing = null; edit.input.remove(); edit.record.name.hidden = false;
    this.displayName(edit.record);
    if (this.refreshPending) { this.refreshPending = false; this.refresh(); }
    if (focus) { this.focusKey = edit.key; this.syncFocus(true); }
  }

  cancelRename(focus = false) {
    const edit = this.editing;
    if (!edit) return;
    // A submitted save cannot be cancelled; its result still belongs to its captured asset.
    if (edit.promise && edit.sceneKey === this.sceneKey && focus) return;
    if (!edit.promise) this.renameFailures.delete(`${edit.sceneKey}\n${edit.key}`);
    this.finishRename(edit, focus); this.renameMessage();
  }

  commitRename(focus = false) {
    const edit = this.editing;
    if (!edit) return Promise.resolve(true);
    if (edit.promise) return edit.promise;
    let name;
    try { name = validateObjectName(edit.input.value); }
    catch (error) {
      edit.input.setAttribute('aria-invalid', 'true'); this.renameMessage(error.message, true);
      return Promise.resolve(false);
    }
    const failureKey = `${edit.sceneKey}\n${edit.key}`;
    if (name === (this.names[edit.key] || edit.record.node.name)) {
      this.renameFailures.delete(failureKey); this.finishRename(edit, focus); return Promise.resolve(true);
    }
    const promise = Promise.resolve().then(() => this.onRename(edit.key, name, edit.sceneKey)).then(saved => {
      if (saved?.ok === false) throw new Error(saved.error || 'The object name could not be saved.');
      const names = saved?.names || saved;
      if (!names || typeof names !== 'object' || Array.isArray(names)) throw new Error('The saved object names were not returned.');
      this.renameFailures.delete(failureKey);
      if (edit.sceneKey === this.sceneKey) { this.setNames(names); this.renameMessage('Object name saved.'); }
      this.finishRename(edit, focus); return true;
    }).catch(error => {
      this.renameFailures.set(failureKey, error.message || String(error));
      if (this.editing === edit) { edit.input.disabled = false; edit.input.setAttribute('aria-invalid', 'true'); }
      if (edit.sceneKey === this.sceneKey) this.renameMessage(`Name not saved: ${error.message || error}`, true);
      return false;
    }).finally(() => { edit.promise = null; this.pendingRenames.delete(promise); });
    edit.promise = promise; this.pendingRenames.add(promise);
    edit.input.disabled = true; edit.input.removeAttribute('aria-invalid'); this.renameMessage('Saving object name…');
    return promise;
  }

  async whenIdle() {
    await Promise.all([...this.pendingRenames]);
    if (this.renameFailures.size) throw new Error(`Object names could not be saved: ${[...this.renameFailures.values()][0]}`);
  }

  async flushRenames() {
    if (this.editing && !await this.commitRename(false)) throw new Error(this.renameStatus?.textContent || 'The object name could not be saved.');
    await this.whenIdle();
  }

  refresh() {
    if (this.editing) { this.refreshPending = true; return; }
    const objects = this.viewport?.getObjects() || [];
    this.lazyChildren = this.compactLargeScenes && objects.length > 100;
    const focused = this.list.contains(document.activeElement);
    this.inputs = []; this.targets = []; this.nodes = new Map();
    this.list.replaceChildren();
    if (objects.length) {
      const root = {key:'scene', kind:'scene', name:this.sceneName, children:buildObjectHierarchy(objects)};
      this.list.append(this.item(root, null, 1, 1, 1));
    } else {
      const empty = document.createElement('p'); empty.className = 'objects-empty';
      empty.textContent = this.labels.empty; this.list.append(empty);
    }
    this.update();
    this.setActiveModel(this.activeModelId, this.bindingsSummary);
    this.syncFocus(focused);
  }

  item(node, parentKey, depth, position, count) {
    const item = document.createElement('div');
    item.className = `object-tree-item object-kind-${node.kind}`;
    item.setAttribute('role', 'treeitem'); item.tabIndex = -1;
    item.dataset.nodeKey = node.key; item.dataset.kind = node.kind;
    item.setAttribute('aria-level', String(depth));
    item.setAttribute('aria-posinset', String(position)); item.setAttribute('aria-setsize', String(count));
    if (node.modelId != null) item.dataset.modelId = node.modelId;
    if (node.meshId != null) item.dataset.meshId = node.meshId;
    if (node.kind === 'bone') {
      item.dataset.boneId = node.boneId; item.dataset.boneIndex = node.boneIndex;
    }
    const row = document.createElement('div');
    row.className = `object-tree-row object-row object-${node.kind}-row`;
    const branch = node.children.length > 0;
    const expanded = this.expanded.get(node.key) ?? (node.kind === 'scene' || node.kind === 'model' && !this.lazyChildren);
    const expander = document.createElement(branch ? 'button' : 'span');
    expander.className = 'object-tree-expander' + (branch ? '' : ' is-leaf');
    if (branch) {
      expander.type = 'button'; expander.tabIndex = -1;
      expander.setAttribute('aria-label', `${expanded ? 'Collapse' : 'Expand'} ${node.name}`);
      expander.textContent = expanded ? '−' : '+';
      item.setAttribute('aria-expanded', String(expanded));
      expander.addEventListener('click', event => {
        event.stopPropagation(); this.focusKey = node.key;
        this.setExpanded(node.key, item.getAttribute('aria-expanded') !== 'true');
        this.syncFocus(true);
      });
    } else expander.setAttribute('aria-hidden', 'true');
    const icon = document.createElement('span'); icon.className = `object-tree-icon object-icon-${node.kind}`;
    icon.setAttribute('aria-hidden', 'true');
    const symbol = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    symbol.setAttribute('viewBox', '0 0 24 24'); symbol.setAttribute('focusable', 'false');
    const shape = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    shape.setAttribute('d', {
      scene:'M3 6h6l2 2h10v12H3z',
      model:'M12 3 3 7.5v9L12 21l9-4.5v-9L12 3ZM3 7.5l9 4.5 9-4.5M12 12v9',
      mesh:'M4 19 11 3 21 18 4 19ZM4 19l9-7 8 6M11 3l2 9',
      skeleton:'M12 3v18M5 8h14M5 16h14M5 6v4M19 6v4M5 14v4M19 14v4',
      bone:'M4 4h5v5H4zM15 15h5v5h-5zM9 9l6 6',
    }[node.kind]);
    symbol.append(shape); icon.append(symbol);
    row.append(expander, icon);
    const name = document.createElement('span'); name.className = 'object-tree-name';
    name.id = `${this.prefix}-tree-label-${this.nodes.size}`;
    item.setAttribute('aria-labelledby', name.id); row.append(name);
    const record = {node, item, row, name, expander, parentKey, depth, group:null, input:null, childrenBuilt:false};
    this.nodes.set(node.key, record);
    this.displayName(record);
    if (this.canRename(node)) {
      name.addEventListener('dblclick', event => { event.preventDefault(); event.stopPropagation(); void this.beginRename(node.key); });
      name.addEventListener('click', event => {
        event.stopPropagation();
        if (event.detail > 1) return;
        // Select immediately so a following clip click targets the chosen rig.
        // A second click opens its name editor without changing assignments.
        this.focusKey = node.key; this.syncFocus(true, false); this.select(node);
      });
    }
    if (node.kind === 'scene' || node.kind === 'mesh' || (node.kind === 'model' && node.object.meshes?.length)) {
      const input = document.createElement('input'); input.type = 'checkbox'; input.className = 'object-visibility';
      input.tabIndex = -1;
      if (node.modelId != null) input.dataset.modelId = node.modelId;
      if (node.meshId != null) input.dataset.meshId = node.meshId;
      input.setAttribute('aria-label', node.kind === 'scene' ? 'Show all scene meshes'
        : node.kind === 'model' ? `Show all meshes in ${node.name}` : `Show ${node.name}`);
      input.addEventListener('click', event => event.stopPropagation());
      input.addEventListener('change', () => {
        this.focusKey = node.key;
        if (node.kind === 'scene') this.viewport?.setAllObjectsVisible(input.checked);
        else this.viewport?.setObjectVisible(node.modelId, node.meshId ?? null, input.checked);
        this.update(); this.syncFocus(false);
      });
      row.append(input); record.input = input;
      this.inputs.push({input, node});
    }
    if (node.kind === 'model') {
      const summary = document.createElement('span'); summary.className = 'object-animation-summary';
      row.append(summary); this.targets.push({modelId:node.modelId, group:item, row, summary, bones:node.object.bones});
    }
    row.addEventListener('click', event => {
      if (event.target.closest('input,button')) return;
      this.focusKey = node.key; this.syncFocus(true); this.select(node);
    });
    item.addEventListener('focus', event => {
      if (event.target !== item) return;
      this.focusKey = node.key; this.syncFocus(false);
    });
    item.append(row);
    if (branch) {
      const children = document.createElement('div'); children.className = 'object-tree-children';
      children.setAttribute('role', 'group'); children.hidden = !expanded;
      record.group = children; item.append(children);
      if (expanded || !this.lazyChildren) this.mountChildren(record);
    }
    this.displayName(record);
    return item;
  }

  select(node) {
    if (node.kind === 'model' || node.kind === 'mesh') this.onSelectModel?.(node.modelId);
  }

  mountChildren(record) {
    if (record.childrenBuilt || !record.group) return;
    record.childrenBuilt = true;
    const {node, depth} = record;
    node.children.forEach((child, index) => record.group.append(this.item(child, node.key, depth + 1, index + 1, node.children.length)));
  }

  setExpanded(key, expanded) {
    const record = this.nodes.get(key);
    if (!record?.group) return;
    if (expanded && !record.childrenBuilt) {
      this.mountChildren(record); this.update(); this.setActiveModel(this.activeModelId);
    }
    this.expanded.set(key, expanded); record.group.hidden = !expanded;
    record.item.setAttribute('aria-expanded', String(expanded));
    record.expander.textContent = expanded ? '−' : '+';
    record.expander.setAttribute('aria-label', `${expanded ? 'Collapse' : 'Expand'} ${this.names[key] || record.node.name}`);
    this.syncFocus(false);
  }

  visibleItems() {
    return [...this.list.querySelectorAll('[role="treeitem"]')].filter(item => !item.closest('[role="group"][hidden]'))
      .map(item => this.nodes.get(item.dataset.nodeKey));
  }

  syncFocus(focus, scroll = true) {
    const visible = this.visibleItems();
    let record = this.nodes.get(this.focusKey);
    while (record && !visible.includes(record)) record = this.nodes.get(record.parentKey);
    record ||= visible[0];
    this.focusKey = record?.node.key || 'scene';
    for (const candidate of this.nodes.values()) candidate.item.tabIndex = candidate === record ? 0 : -1;
    if (focus && record) {
      record.item.focus({preventScroll:true});
      if (scroll) record.row.scrollIntoView({block:'nearest', inline:'nearest'});
    }
  }

  keydown(event) {
    if (event.target.closest('.object-rename-input')) { event.stopPropagation(); return; }
    const target = event.target.closest('[role="treeitem"]');
    const record = target && this.nodes.get(target.dataset.nodeKey);
    if (!record || event.altKey || event.ctrlKey || event.metaKey) return;
    if (event.target.closest('button') && (event.key === 'Enter' || event.key === ' ')) return;
    if (event.target.matches('input') && event.key === 'Enter') return;
    const visible = this.visibleItems(), index = visible.indexOf(record);
    let next = null;
    switch (event.key) {
      case 'F2': event.preventDefault(); event.stopPropagation(); void this.beginRename(record.node.key); return;
      case 'ArrowDown': next = visible[Math.min(visible.length - 1, index + 1)]; break;
      case 'ArrowUp': next = visible[Math.max(0, index - 1)]; break;
      case 'Home': next = visible[0]; break;
      case 'End': next = visible.at(-1); break;
      case 'ArrowRight':
        if (record.group && record.group.hidden) this.setExpanded(record.node.key, true);
        else if (record.group) next = this.nodes.get(record.node.children[0].key);
        break;
      case 'ArrowLeft':
        if (record.group && !record.group.hidden) this.setExpanded(record.node.key, false);
        else next = this.nodes.get(record.parentKey);
        break;
      case 'Enter': this.select(record.node); break;
      case ' ':
        if (record.input && !record.input.disabled) record.input.click();
        break;
      default: return;
    }
    event.preventDefault(); event.stopPropagation();
    this.focusKey = (next || record).node.key; this.syncFocus(true);
  }

  setActiveModel(modelId, bindingsSummary = this.bindingsSummary) {
    this.activeModelId = modelId;
    this.bindingsSummary = bindingsSummary || new Map();
    for (const {modelId: targetId, group, summary} of this.targets) {
      const active = targetId === modelId;
      group.classList.toggle('is-animation-target', active);
      group.setAttribute('aria-selected', String(active));
      const binding = this.bindingsSummary instanceof Map ? this.bindingsSummary.get(targetId) : this.bindingsSummary[targetId];
      const text = typeof binding === 'string' ? binding : binding?.nickname || binding?.name || binding?.clipName || '';
      summary.textContent = text ? `Animation: ${text}` : '';
      summary.title = text ? summary.textContent : 'No animation assigned';
    }
  }

  update() {
    const objects = this.viewport?.getObjects() || [];
    const lookup = new Map(objects.map(object => [object.id, object]));
    const meshes = objects.flatMap(object => object.meshes || []);
    const shown = meshes.filter(mesh => mesh.visible).length;
    for (const {input, node} of this.inputs) {
      const object = lookup.get(node.modelId);
      const mesh = node.kind === 'mesh' ? object?.meshes.find(item => String(item.id) === String(node.meshId)) : null;
      input.checked = node.kind === 'scene' ? Boolean(meshes.length && shown === meshes.length)
        : Boolean(node.kind === 'model' ? object?.visible : mesh?.visible);
      input.indeterminate = node.kind === 'scene' ? shown > 0 && shown < meshes.length
        : node.kind === 'model' && Boolean(object?.indeterminate);
      input.disabled = node.kind === 'scene' && !meshes.length;
      input.setAttribute('aria-checked', input.indeterminate ? 'mixed' : String(input.checked));
    }
    if (this.summary) this.summary.textContent = `${shown} of ${meshes.length} meshes shown`;
    if (this.showAll) this.showAll.disabled = !meshes.length;
    if (this.hideAll) this.hideAll.disabled = !meshes.length;
    const total = meshes.reduce((sum, mesh) => sum + (Number(mesh.polygons) || 0), 0);
    const visible = this.viewport?.getVisiblePolygonCount() || 0;
    const count = this.polygonCount;
    if (!count) return;
    count.textContent = visible === total ? `Polygons: ${total.toLocaleString()}`
      : `Polygons: ${visible.toLocaleString()} / ${total.toLocaleString()}`;
    count.title = visible === total ? 'Total loaded polygons' : 'Visible polygons / total loaded polygons';
  }
}
