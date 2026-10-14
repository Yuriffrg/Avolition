import {PreviewViewport} from './viewer.js';
import {describeModelEntry, modelBrowserGroups, modelPathKey} from './model-library.js';

export function createModelBrowser({api,onLoad,onOpen,onClose,previewDelay=160}={}) {
  const $=id=>document.getElementById(`model-browser-${id}`), dialog=document.getElementById('model-browser');
  let entries=[],selected=null,viewport=null,serial=0,session=0,timer=null,pending=null,flight=null;
  let release=Promise.resolve(),committing=false,focusKey='',previousFocus=null;
  const expanded=new Set(),searchCollapsed=new Set(),rows=new Map();let visible=[],lastQuery='';
  const isOpen=()=>dialog.open;
  const message=(text,error=false)=>{$('status').textContent=text;$('status').classList.toggle('error',error);};
  const controls=()=>{$('load').disabled=!selected||committing;$('load').textContent=committing?'Loading…':'Load into Animations';};
  function clearPreview() {
    viewport?.clearAsset();$('loading').hidden=true;$('empty').hidden=false;$('stats').textContent='';
    $('notes').textContent='';$('notes-panel').hidden=true;
  }
  function setEntries(values=[]) {
    const seen=new Set();entries=[];
    for(const entry of Array.isArray(values)?values:[]){const item=describeModelEntry(entry);if(item&&!seen.has(item.key)){entries.push(item);seen.add(item.key);}}
    const current=selected&&entries.find(item=>item.key===selected.key);
    if(selected&&!current){serial++;pending=null;clearTimeout(timer);timer=null;selected=null;clearPreview();
      $('title').textContent='Choose a model';$('context').textContent='Select an item to preview it before loading.';$('path').textContent='';
      message('The selected item is no longer in this library.');}
    else if(current)selected=current;
    filters();render();controls();
  }
  function options(select,values,label) {
    const previous=select.value;select.replaceChildren(new Option(label,''),...values.map(value=>new Option(value,value)));
    if(values.includes(previous))select.value=previous;
  }
  function filters() {
    const order=values=>[...new Set(values)].sort((a,b)=>a.localeCompare(b,undefined,{numeric:true,sensitivity:'base'}));
    options($('hero'),order(entries.map(item=>item.hero)),'All heroes');
    options($('skin'),order(entries.filter(item=>!$('hero').value||item.hero===$('hero').value).map(item=>item.skin)),'All skins');
  }
  function focus(key) {
    const record=rows.get(key);if(!record)return;focusKey=key;
    for(const [candidate,{element}] of rows)element.tabIndex=candidate===key?0:-1;
    record.element.focus({preventScroll:true});record.element.scrollIntoView({block:'nearest'});
  }
  function render() {
    const groups=modelBrowserGroups(entries,{query:$('search').value,hero:$('hero').value,skin:$('skin').value,kind:$('kind').value,sort:$('sort').value});
    const query=$('search').value.trim(),searching=!!query,hadFocus=$('list').contains(document.activeElement);
    if(query!==lastQuery){searchCollapsed.clear();lastQuery=query;}
    rows.clear();visible=[];
    const make=(nodes,parent=null,level=1)=>nodes.map((node,index)=>{
      const leaf=node.kind==='asset',open=!leaf&&(searching?!searchCollapsed.has(node.key):expanded.has(node.key));
      const boneText=node.boneCount===null?'— bones':`${node.boneCount?.toLocaleString()} ${node.boneCount===1?'bone':'bones'}`;
      const item=document.createElement('div');item.className='model-browser-item';item.dataset.key=node.key;item.dataset.kind=node.kind;
      item.setAttribute('role','treeitem');item.tabIndex=-1;item.setAttribute('aria-level',String(level));
      item.setAttribute('aria-label',leaf?`${node.label}, ${node.boneCount===null?'bone count unavailable':boneText}, ${node.detail}`:`${node.label}, ${node.count} items`);
      item.setAttribute('aria-posinset',String(index+1));item.setAttribute('aria-setsize',String(nodes.length));
      if(leaf){item.dataset.path=node.path;item.setAttribute('aria-selected',String(node.key===selected?.key));}
      else item.setAttribute('aria-expanded',String(open));
      const row=document.createElement('div');row.className='model-browser-row';row.title=leaf?node.path:node.label;
      const marker=document.createElement('span');marker.className='model-browser-marker';marker.textContent=leaf?(node.assetKind==='entity'?'◇':'△'):open?'−':'+';marker.setAttribute('aria-hidden','true');
      const label=document.createElement('span');label.className='model-browser-label';label.textContent=node.label;
      const detail=document.createElement('span');detail.className='model-browser-detail';detail.textContent=leaf?node.detail:String(node.count);
      if(leaf)detail.title=node.size===null?(node.sizeError||'File size unavailable. Rescan the library to refresh it.')
        :`Source file size: ${node.size.toLocaleString()} bytes. Referenced models and textures are not included.`;
      row.append(marker,label);
      if(leaf){const bones=document.createElement('span');bones.className='model-browser-bones';bones.textContent=boneText;
        bones.title=node.boneCount===null?(node.boneCountError||'Bone count unavailable. Rescan the library to refresh it.')
          :node.assetKind==='entity'?'Total bones in this entity, including attached models.':'Bones in this model.';
        row.append(bones);}
      row.append(detail);item.append(row);visible.push(node.key);rows.set(node.key,{node,element:item,parent,open});
      row.addEventListener('click',()=>{focusKey=node.key;if(leaf)select(node);else toggle(node.key);focus(node.key);});
      item.addEventListener('focus',event=>{if(event.target===item)focusKey=node.key;});
      if(open){const children=document.createElement('div');children.setAttribute('role','group');children.className='model-browser-children';children.append(...make(node.children,node.key,level+1));item.append(children);}
      return item;
    });
    $('list').replaceChildren(...make(groups));
    const total=groups.reduce((sum,group)=>sum+group.count,0);$('count').textContent=`${total} ${total===1?'item':'items'}`;
    $('no-results').hidden=total>0;$('no-results').textContent=entries.length?'No models match these filters.':'Scan a library folder to find entities and models.';
    const key=rows.has(focusKey)?focusKey:rows.has(selected?.key)?selected.key:visible[0];
    if(key){rows.get(key).element.tabIndex=0;focusKey=key;if(hadFocus)focus(key);}
  }
  function toggle(key) {
    if($('search').value.trim())searchCollapsed.has(key)?searchCollapsed.delete(key):searchCollapsed.add(key);
    else expanded.has(key)?expanded.delete(key):expanded.add(key);
    render();
  }
  function select(item,{immediate=false}={}) {
    selected=entries.find(entry=>entry.key===item.key)||item;controls();render();
    $('title').textContent=selected.label;$('context').textContent=`${selected.context} · ${selected.detail}`;
    $('path').textContent=selected.path;$('path').title=selected.path;
    serial++;clearTimeout(timer);pending={item:selected,serial,session};clearPreview();
    message('Preparing preview…');$('loading').hidden=false;
    if(immediate)pump();else timer=setTimeout(()=>{timer=null;pump();},previewDelay);
  }
  function current(request) {return isOpen()&&request.serial===serial&&request.session===session;}
  function ensureViewport() {
    if(!viewport)viewport=new PreviewViewport($('canvas'));
    viewport.setActive?.(true);return viewport;
  }
  function pump() {
    if(flight||timer||!pending||!isOpen())return;
    const request=pending;pending=null;
    flight=(async()=>{
      await release;if(!current(request))return;
      try {
        const asset=await api('preview_entity',request.item.path);if(!current(request))return;
        const viewer=ensureViewport(),stats=viewer.loadAsset(asset);
        $('empty').hidden=true;$('stats').textContent=`${stats.models.toLocaleString()} objects · ${stats.meshes.toLocaleString()} meshes · ${stats.bones.toLocaleString()} bones`;
        message('Loading preview materials…');
        let materials;
        try {materials=await viewer.loadMaterials(asset,id=>api('load_texture',id,512),{maxAnisotropy:2});}
        catch(error){materials={warnings:[`Some preview materials are unavailable: ${error.message||error}`]};}
        if(!current(request))return;
        const warnings=[...(asset.warnings||[]),...(materials.warnings||[])];
        $('notes').textContent=warnings.join('\n');$('notes-panel').hidden=!warnings.length;
        message(warnings.length?'Preview ready. See preview notes for details.':'Preview ready. Load this item when you are ready to animate it.');
      }catch(error){
        if(!current(request))return;
        clearPreview();message(`Preview unavailable: ${error.message||error}. You can still load this item into Animations.`,true);
      }finally{if(current(request))$('loading').hidden=true;}
    })().finally(()=>{flight=null;pump();});
  }
  function open(path='') {
    if(isOpen())return;
    session++;previousFocus=document.activeElement;dialog.hidden=false;dialog.showModal();onOpen?.();viewport?.setActive?.(true);
    const item=entries.find(entry=>entry.key===modelPathKey(path))||null;
    $('search').value='';$('kind').value='';$('hero').value=item?.hero||'';filters();$('skin').value=item?.skin||'';
    expanded.clear();
    if(item){expanded.add(`hero:${item.hero}`);expanded.add(`skin:${JSON.stringify([item.hero,item.skin])}`);select(item,{immediate:true});}
    else {selected=null;clearPreview();$('title').textContent='Choose a model';$('context').textContent='Select an item to preview it before loading.';$('path').textContent='';message('Select an entity or model from the library.');
      if(new Set(entries.map(entry=>entry.hero)).size===1){const hero=entries[0]?.hero;if(hero)expanded.add(`hero:${hero}`);for(const entry of entries)expanded.add(`skin:${JSON.stringify([entry.hero,entry.skin])}`);}}
    render();controls();$('search').focus();
  }
  function close() {
    if(!isOpen())return release;
    serial++;session++;clearTimeout(timer);timer=null;pending=null;
    viewport?.setActive?.(false);clearPreview();dialog.close();dialog.hidden=true;onClose?.();
    const previous=release;release=previous.catch(()=>{}).then(()=>api('release_entity_preview')).catch(()=>{});
    previousFocus?.focus?.({preventScroll:true});return release;
  }
  async function commit() {
    if(!selected||committing)return;const path=selected.path,opened=session;
    committing=true;controls();message('Loading into Animations…');
    try {await onLoad?.(path);if(isOpen()&&session===opened)close();}
    catch(error){if(isOpen()&&session===opened)message(`Could not load this item: ${error.message||error}`,true);}
    finally{committing=false;controls();}
  }
  $('close').addEventListener('click',close);$('cancel').addEventListener('click',close);$('load').addEventListener('click',commit);
  dialog.addEventListener('cancel',event=>{event.preventDefault();close();});
  $('frame').addEventListener('click',()=>viewport?.frameModel());
  $('search').addEventListener('input',render);
  $('search').addEventListener('keydown',event=>{if(event.key==='ArrowDown'){event.preventDefault();focus(visible[0]);}});
  const revealFilter=()=>{
    const hero=$('hero').value,skin=$('skin').value;
    if(hero){expanded.add(`hero:${hero}`);const skins=[...new Set(entries.filter(item=>item.hero===hero).map(item=>item.skin))];
      if(skin)expanded.add(`skin:${JSON.stringify([hero,skin])}`);
      else if(skins.length===1)expanded.add(`skin:${JSON.stringify([hero,skins[0]])}`);}
    render();
  };
  $('hero').addEventListener('change',()=>{$('skin').value='';filters();revealFilter();});
  $('skin').addEventListener('change',revealFilter);$('kind').addEventListener('change',render);
  $('sort').addEventListener('change',render);
  $('list').addEventListener('keydown',event=>{
    const record=rows.get(event.target.closest('[role="treeitem"]')?.dataset.key);if(!record||event.altKey||event.ctrlKey||event.metaKey)return;
    const {node,parent,open}=record,index=visible.indexOf(node.key);let next=node.key;
    if(event.key==='ArrowDown')next=visible[Math.min(index+1,visible.length-1)];
    else if(event.key==='ArrowUp')next=visible[Math.max(index-1,0)];
    else if(event.key==='Home')next=visible[0];else if(event.key==='End')next=visible.at(-1);
    else if(event.key==='ArrowRight'){if(node.kind!=='asset'){if(!open)toggle(node.key);else next=visible[index+1];}}
    else if(event.key==='ArrowLeft'){if(node.kind!=='asset'&&open)toggle(node.key);else next=parent||node.key;}
    else if(event.key==='Enter'||event.key===' '){if(node.kind==='asset')select(node);else toggle(node.key);}
    else return;
    event.preventDefault();event.stopPropagation();
    if(next!==node.key&&rows.get(next)?.node.kind==='asset')select(rows.get(next).node);
    focus(next);
  });
  controls();
  return {setEntries,open,close,isOpen,async whenIdle(){
    while(timer||flight){if(flight)await flight;else await new Promise(resolve=>setTimeout(resolve,20));}await release;
  }};
}
