// The library uses exported paths, not loaded geometry, to identify maps and
// their build/variant folders. Source paths remain untouched for opening files.
const collator = new Intl.Collator(undefined, {numeric:true, sensitivity:'base'});
const parts = value => String(value || '').split(/[\\/]+/).filter(Boolean);
const stem = value => String(value || '').replace(/\.owmap$/i, '');
const identity = value => {
  const path = String(value || '').replaceAll('\\', '/');
  return /^(?:[a-z]:\/|\/\/)/i.test(path) ? path.toLocaleLowerCase('en-US') : path;
};

function location(row) {
  const absolute = parts(row.path), relative = parts(row.relativePath);
  const marker = absolute.map(part => part.toLowerCase()).lastIndexOf('maps');
  const relativeMarker = relative.map(part => part.toLowerCase()).lastIndexOf('maps');
  const exported = marker >= 0 ? absolute.slice(marker + 1)
    : relativeMarker >= 0 ? relative.slice(relativeMarker + 1) : relative;
  const directories = exported.slice(0, -1);
  const map = typeof row.mapName === 'string' && row.mapName.trim() ? row.mapName.trim() : directories.shift();
  // Explicit metadata can supply the map while the path still supplies its
  // variant folders; avoid displaying the map name twice in that case.
  if (row.mapName && directories[0] === map) directories.shift();
  const groups = row.variantGroup != null ? parts(row.variantGroup) : directories;
  return {map, groups, name:row.name || stem(exported.at(-1) || absolute.at(-1)) || 'Map'};
}

export function buildMapLibrary(rows = []) {
  const roots = [], groups = new Map(), seen = new Set();
  for (const row of rows) {
    if (!row || typeof row.path !== 'string' || !row.path.trim()) continue;
    const pathKey = identity(row.path);
    if (seen.has(pathKey)) continue;
    seen.add(pathKey);
    const {map, groups:folders, name} = location(row);
    let siblings = roots;
    const trail = [];
    for (const [index, label] of (map ? [map, ...folders] : []).entries()) {
      trail.push(label);
      const key = `folder:${JSON.stringify(trail)}`;
      let node = groups.get(key);
      if (!node) {
        node = {key, kind:index === 0 ? 'map' : 'folder', name:label, children:[], count:0};
        groups.set(key, node); siblings.push(node);
      }
      node.count++; siblings = node.children;
    }
    siblings.push({key:`variant:${pathKey}`, kind:'variant', name, row, children:[], count:1,
      search:[map, ...folders, name, row.relativePath || ''].filter(Boolean).join(' ').toLocaleLowerCase()});
  }
  const sort = nodes => {
    nodes.sort((a,b) => Number(a.kind === 'variant') - Number(b.kind === 'variant')
      || collator.compare(a.name,b.name) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
    for (const node of nodes) sort(node.children);
  };
  sort(roots);
  return roots;
}

export function filterMapLibrary(nodes, query = '') {
  const tokens = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  if (!tokens.length) return nodes;
  return nodes.flatMap(node => {
    if (node.kind === 'variant') return tokens.every(token => node.search.includes(token)) ? [node] : [];
    const children = filterMapLibrary(node.children, query);
    return children.length ? [{...node, children, count:children.reduce((sum,child) => sum + child.count, 0)}] : [];
  });
}

function icon(kind) {
  const element = document.createElement('span'); element.className = 'map-library-icon'; element.setAttribute('aria-hidden','true');
  element.innerHTML = kind === 'variant'
    ? '<svg viewBox="0 0 18 18"><path d="M3 2h8l4 4v10H3zM11 2v4h4M6 10h6M6 13h6"/></svg>'
    : '<svg viewBox="0 0 18 18"><path d="M1.5 4h6l2 2h7v10h-15z"/></svg>';
  return element;
}

export class MapLibraryTree {
  constructor(container, options = {}) {
    if (!container) throw new Error('The map library container is missing.');
    this.container = container; this.onSelect = options.onSelect;
    this.empty = options.empty; this.count = options.count;
    this.nodes = []; this.byKey = new Map(); this.parents = new Map(); this.elements = new Map();
    this.expanded = new Set(); this.searchCollapsed = new Set(); this.known = new Set();
    this.selected = ''; this.query = ''; this.focused = ''; this.typeahead = ''; this.lastTyped = 0;
    container.classList.add('map-library-tree'); container.setAttribute('role','tree');
    container.setAttribute('aria-label','Maps and variants');
    container.addEventListener('keydown', event => this.keydown(event));
  }

  setRows(rows) {
    this.nodes = buildMapLibrary(rows); this.byKey.clear(); this.parents.clear();
    const visit = (nodes, parent = null) => {
      for (const node of nodes) {
        this.byKey.set(node.key,node); this.parents.set(node.key,parent);
        // A scan of one map opens its only build folder immediately; larger
        // libraries start compact and retain expansion choices across scans.
        if (!this.known.has(node.key) && nodes.length === 1 && node.kind !== 'variant') this.expanded.add(node.key);
        this.known.add(node.key); visit(node.children,node.key);
      }
    };
    visit(this.nodes); this.revealSelected(); this.render();
  }

  setSelected(path) {
    this.selected = path ? `variant:${identity(path)}` : '';
    this.revealSelected(); this.render();
  }

  revealSelected() {
    let parent = this.parents.get(this.selected);
    while (parent) {this.expanded.add(parent); this.searchCollapsed.delete(parent); parent = this.parents.get(parent);}
  }

  setQuery(query) {
    const next = String(query || '').trim();
    if (next !== this.query) this.searchCollapsed.clear();
    this.query = next; this.render();
  }

  isExpanded(key) {return this.query ? !this.searchCollapsed.has(key) : this.expanded.has(key);}

  toggle(key, open = !this.isExpanded(key)) {
    if (this.query) open ? this.searchCollapsed.delete(key) : this.searchCollapsed.add(key);
    else open ? this.expanded.add(key) : this.expanded.delete(key);
    this.focused = key; this.render(); this.focus(key);
  }

  focus(key) {
    const target = this.elements.get(key); if (!target) return;
    for (const element of this.elements.values()) element.tabIndex = element === target ? 0 : -1;
    this.focused = key; target.focus({preventScroll:true}); target.scrollIntoView({block:'nearest'});
  }

  render() {
    const hadFocus = this.container.contains(document.activeElement);
    const filtered = filterMapLibrary(this.nodes,this.query);
    const total = this.nodes.reduce((sum,node) => sum + node.count, 0);
    const visibleCount = filtered.reduce((sum,node) => sum + node.count, 0);
    if (this.count) {this.count.textContent = String(total); this.count.title = `${total} map variants`;}
    if (this.empty) {
      this.empty.hidden = visibleCount > 0;
      this.empty.textContent = this.query ? 'No map variants match this search.' : 'Scan an exported folder to find .owmap files.';
    }
    this.elements.clear(); this.visible = [];
    const make = (nodes, level = 1) => nodes.map((node,index) => {
      const leaf = node.kind === 'variant', expanded = !leaf && this.isExpanded(node.key);
      const item = document.createElement('div'); item.className = 'map-library-item'; item.dataset.nodeKey = node.key;
      item.dataset.kind = node.kind; item.setAttribute('role','treeitem'); item.tabIndex = -1;
      item.setAttribute('aria-level',String(level)); item.setAttribute('aria-posinset',String(index + 1)); item.setAttribute('aria-setsize',String(nodes.length));
      if (!leaf) item.setAttribute('aria-expanded',String(expanded));
      else {item.setAttribute('aria-selected',String(node.key === this.selected)); item.dataset.path = node.row.path;}
      item.setAttribute('aria-label',leaf ? node.name : `${node.name}, ${node.count} variants`);
      this.elements.set(node.key,item); this.visible.push(node.key);
      const row = document.createElement(leaf ? 'button' : 'div'); row.className = 'map-library-row';
      if (leaf) {row.type = 'button'; row.tabIndex = -1; row.classList.add('map-row'); row.dataset.path = node.row.path;
        row.classList.toggle('is-selected',node.key === this.selected); row.title = node.row.relativePath || node.row.path;}
      else row.title = node.name;
      const expander = document.createElement('span'); expander.className = 'map-library-expander'; expander.setAttribute('aria-hidden','true');
      expander.textContent = leaf ? '' : expanded ? '−' : '+'; expander.classList.toggle('is-leaf',leaf);
      const label = document.createElement('span'); label.className = 'map-library-name'; label.textContent = node.name;
      row.append(expander,icon(node.kind),label);
      if (!leaf) {const count = document.createElement('span');count.className = 'map-library-count';count.textContent = String(node.count);count.setAttribute('aria-hidden','true');row.append(count);}
      row.addEventListener('click', event => {event.stopPropagation();this.focus(node.key);if (leaf) this.onSelect?.(node.row); else this.toggle(node.key);});
      item.addEventListener('focus', event => {if(event.target === item)this.focused = node.key;});
      item.append(row);
      if (expanded) {const group = document.createElement('div');group.className = 'map-library-children';group.setAttribute('role','group');group.append(...make(node.children,level + 1));item.append(group);}
      return item;
    });
    this.container.replaceChildren(...make(filtered));
    const active = this.elements.has(this.focused) ? this.focused : this.elements.has(this.selected) ? this.selected : this.visible[0];
    if (active) {this.elements.get(active).tabIndex = 0;this.focused = active;if(hadFocus)this.focus(active);}
    this.container.tabIndex = active ? -1 : 0;
  }

  keydown(event) {
    const item = event.target.closest('.map-library-item');
    if (!item || event.altKey || event.ctrlKey || event.metaKey) return;
    const key = item.dataset.nodeKey, node = this.byKey.get(key), index = this.visible.indexOf(key);
    if (!node) return;
    const move = target => {if(target)this.focus(target);};
    if (event.key === 'ArrowDown') move(this.visible[index + 1]);
    else if (event.key === 'ArrowUp') move(this.visible[index - 1]);
    else if (event.key === 'Home') move(this.visible[0]);
    else if (event.key === 'End') move(this.visible.at(-1));
    else if (event.key === 'ArrowRight') {if(node.kind !== 'variant'){if(!this.isExpanded(key))this.toggle(key,true);else move(this.visible[index + 1]);}}
    else if (event.key === 'ArrowLeft') {if(node.kind !== 'variant' && this.isExpanded(key))this.toggle(key,false);else move(this.parents.get(key));}
    else if (event.key === 'Enter' || event.key === ' ') {if(node.kind === 'variant')this.onSelect?.(node.row);else this.toggle(key);}
    else if (event.key.length === 1 && /\S/u.test(event.key)) {
      const now = Date.now();this.typeahead = now - this.lastTyped < 700 ? this.typeahead + event.key.toLocaleLowerCase() : event.key.toLocaleLowerCase();this.lastTyped = now;
      const order = [...this.visible.slice(index + 1),...this.visible.slice(0,index + 1)];
      move(order.find(candidate => this.byKey.get(candidate).name.toLocaleLowerCase().startsWith(this.typeahead)));
    } else return;
    event.preventDefault(); event.stopPropagation();
  }
}
