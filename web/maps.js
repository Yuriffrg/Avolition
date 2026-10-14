import {PreviewViewport} from './viewer.js';
import {ObjectVisibilityMenu} from './object-menu.js';
import {mapPreviewMaterialOptions} from './materials.js';
import {MapLibraryTree} from './map-library.js';

// Maps have their own loaded scene and library; switching workspaces preserves both.
export function initMaps(api, bootstrap, hooks = {}) {
  const $ = id => document.getElementById(id);
  const state = {rows:[], path:'', root:bootstrap.maps?.root||'', loadedRoot:'', names:{}, request:0, loading:false,
    scanning:false, exporting:!!bootstrap.mapExport?.busy, settings:bootstrap.maps || {}, saves:Promise.resolve(), saveError:null};
  let viewport = null;
  const message = (text, error=false) => {
    $('maps-status').textContent = text; $('maps-status').classList.toggle('error', error);
  };
  const menu = new ObjectVisibilityMenu(null, id => menu.setActiveModel(id), {
    prefix:'maps-objects', ids:{polygonCount:'maps-polygon-count'}, compactLargeScenes:true,
    labels:{empty:'Load an exported .owmap file to view its objects.', tree:'Map object hierarchy'},
    onRename:async (key,name,path) => {
      const names = await api('save_object_name',path,key,name);
      if (state.path === path) {
        if (key === 'scene') {
          const previous = state.names.scene || $('maps-scene-name').textContent;
          if ($('maps-export-name').value === previous) $('maps-export-name').value = names.scene;
          $('maps-scene-name').textContent = names.scene;
        }
        state.names = names;
      }
      return names;
    },
  });
  const library = new MapLibraryTree($('maps-list'), {onSelect:row=>load(row.path,state.root),
    empty:$('maps-library-empty'), count:$('maps-count')});
  const controls = () => {
    $('maps-scan').disabled = state.scanning || state.exporting;
    $('maps-export').disabled = !state.path || state.loading || state.exporting;
    $('maps-browse-root').disabled = !!bootstrap.browserMode;
    $('maps-browse-output').disabled = !!bootstrap.browserMode;
    $('maps-open-output').disabled = !!bootstrap.browserMode;
    $('maps-export').textContent = state.exporting ? 'Exporting…' : 'Export C4D';
    $('maps-export-spinner').hidden = !state.exporting;
  };
  function ensureViewport() {
    if (viewport) return viewport;
    viewport = new PreviewViewport($('maps-viewport')); menu.viewport = viewport;
    viewport.setOptions({grid:$('maps-grid').checked,textured:$('maps-textures').checked,
      wireframe:$('maps-wireframe').checked,skeleton:false});
    return viewport;
  }
  function save(patch) {
    state.settings = {...state.settings,...patch};
    // Retry the complete visible settings so an unrelated checkbox save cannot
    // conceal a previously rejected output path.
    const desired = {...state.settings};
    const operation = state.saves.catch(()=>{}).then(()=>api('maps_settings',desired));
    state.saves = operation;
    operation.then(()=>{state.saveError=null;}).catch(error=>{state.saveError=error;message(error.message,true);});
    return operation;
  }
  function render() {
    library.setRows(state.rows);
    library.setSelected(state.path);
    library.setQuery($('maps-search').value);
  }
  async function scan(root=$('maps-root').value.trim()) {
    if (state.scanning) return;
    state.scanning=true;controls();message('Scanning map library…');
    try {
      const data=await api('scan_maps',root);state.root=data.root;state.rows=data.maps;
      $('maps-root').value=data.root;state.settings.root=data.root;render();
      message(`${data.maps.length} maps found.${data.warnings?.length?' '+data.warnings.join(' · '):''}`);
    } catch(error) {message(error.message,true);}
    finally {state.scanning=false;controls();}
  }
  async function load(path,root) {
    if(state.exporting) return;
    try {await menu.flushRenames();} catch(error) {message(error.message,true);return;}
    const request=++state.request;state.loading=true;state.path='';controls();
    $('maps-loading').hidden=false;$('maps-loading-label').textContent='Loading map geometry…';
    message('Loading map…');
    try {
      const asset=await api('load_map',path,root);if(request!==state.request)return;
      const viewer=ensureViewport(),stats=viewer.loadAsset(asset);
      state.path=path;state.loadedRoot=root;state.names=asset.objectNames||{};
      menu.setSceneName(asset.name || path.split(/[\\/]/).pop().replace(/\.owmap$/i,''),path);
      menu.setNames(state.names);
      $('maps-empty').hidden=true;$('maps-scene-name').textContent=state.names.scene||asset.name||path.split(/[\\/]/).pop();
      $('maps-source-file').textContent=path;$('maps-source-file').title=path;
      $('maps-export-name').value=state.names.scene||asset.name||'Map';
      $('maps-polygon-count').hidden=false;
      $('maps-mesh-stats').textContent=`${stats.models.toLocaleString()} objects · ${stats.meshes.toLocaleString()} meshes`;
      $('maps-detail-objects').textContent=stats.models.toLocaleString();
      $('maps-detail-polygons').textContent=stats.polygons.toLocaleString();render();
      $('maps-loading-label').textContent='Loading map materials…';
      const previewOptions=mapPreviewMaterialOptions(asset);
      $('maps-loading-label').textContent=`Loading map materials (${previewOptions.textureEdge}px previews)…`;
      const materials=await viewer.loadMaterials(asset,id=>api('load_texture',id,previewOptions.textureEdge),previewOptions);if(request!==state.request)return;
      const warnings=[...(asset.warnings||[]),...(materials.warnings||[])];
      $('maps-notes').textContent=warnings.join('\n');$('maps-notes-panel').hidden=!warnings.length;
      $('maps-material-status').textContent=`${materials.texturedMeshes.toLocaleString()} meshes textured · ${previewOptions.textureEdge}px previews. C4D uses full-resolution textures.`;
      message(`Loaded ${asset.name||'map'}.${warnings.length?' See map notes for omitted or approximated content.':''}`);
    } catch(error) {
      if(request!==state.request)return;
      viewport?.clearAsset();state.path='';$('maps-empty').hidden=false;
      state.names={};menu.setSceneName('Map','');
      $('maps-scene-name').textContent='No map loaded';$('maps-source-file').textContent='No map loaded';$('maps-source-file').title='';
      $('maps-export-name').value='';$('maps-detail-objects').textContent='—';$('maps-detail-polygons').textContent='—';
      $('maps-polygon-count').hidden=true;$('maps-mesh-stats').textContent='Static map preview';
      $('maps-notes').textContent='';$('maps-notes-panel').hidden=true;$('maps-material-status').textContent='Static geometry and common material maps.';
      $('maps-empty-message').textContent=error.message;message(error.message,true);
    } finally {if(request===state.request){state.loading=false;$('maps-loading').hidden=true;controls();}}
  }
  async function watchExport(job) {
    while (job.busy) {
      const seconds = Math.max(0, Math.floor(job.elapsedSeconds || 0));
      const elapsed = `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
      const count = job.total > 0 ? ` (${job.completed || 0} / ${job.total})` : '';
      message(`${job.message || 'Exporting map'}${count} · ${elapsed}`);
      await new Promise(resolve => setTimeout(resolve, 1000));
      try { job = await api('export_map_status', job.id); }
      catch (error) {
        // A lost status reply does not cancel a native save. Keep the export
        // locked and recover the same job instead of permitting a duplicate.
        message(`Export is still pending. Checking its status again: ${error.message}`, true);
        await new Promise(resolve => setTimeout(resolve, 2000));
      }
    }
    if (job.state !== 'Completed' || !job.result) throw new Error(job.error || 'Map export did not complete.');
    const result = job.result;
    message(`Saved ${result.name} to ${result.c4dPath || result.path}. Open the saved .c4d file in Cinema 4D.`);
    if(result.warnings?.length){$('maps-notes').textContent=result.warnings.join('\n');$('maps-notes-panel').hidden=false;}
  }
  async function exportMap() {
    if(state.exporting || state.loading || !state.path)return;
    state.exporting=true;controls();message('Preparing Cinema 4D map export…');
    const captured={path:state.path,root:state.loadedRoot,name:$('maps-export-name').value,
      output:$('maps-output').value.trim(),bake:$('maps-bake-textures').checked};
    try {
      await menu.flushRenames();await save({outputFolder:captured.output,bakeTextures:captured.bake});
      const job=await api('export_map_start',captured.path,captured.name,captured.root,captured.output,captured.bake);
      await watchExport(job);
    } catch(error) {message(`Map export failed: ${error.message}`,true);}
    finally {state.exporting=false;controls();}
  }
  const browse=async (method,field)=>{
    try {const value=await api(method);if(value){$(field).value=value;if(field==='maps-root')await scan(value);
      if(field==='maps-output')await save({outputFolder:value});}}
    catch(error){message(error.message,true);}
  };
  $('maps-root').value=state.settings.root||bootstrap.workflow?.libraryDirectory||'';
  $('maps-output').value=state.settings.outputFolder||bootstrap.outputFolder||'';
  $('maps-bake-textures').checked=state.settings.bakeTextures!==false;
  $('maps-scan').addEventListener('click',()=>scan());
  $('maps-root').addEventListener('keydown',event=>{if(event.key==='Enter')scan();});
  $('maps-search').addEventListener('input',render);
  $('maps-browse-root').addEventListener('click',()=>browse('browse_root','maps-root'));
  $('maps-browse-output').addEventListener('click',()=>browse('browse_output','maps-output'));
  $('maps-frame').addEventListener('click',event=>viewport?.frameModel({all:event.shiftKey}));
  $('maps-export').addEventListener('click',exportMap);
  $('maps-output').addEventListener('change',()=>save({outputFolder:$('maps-output').value.trim()}));
  $('maps-bake-textures').addEventListener('change',()=>save({bakeTextures:$('maps-bake-textures').checked}));
  $('maps-open-output').addEventListener('click',async()=>{
    try {await save({outputFolder:$('maps-output').value.trim()});await api('open_map_output');}
    catch(error){message(error.message,true);}
  });
  for(const id of ['maps-grid','maps-textures','maps-wireframe'])$(id).addEventListener('change',()=>{
    viewport?.setOptions({grid:$('maps-grid').checked,textured:$('maps-textures').checked,wireframe:$('maps-wireframe').checked});
  });
  controls();render();
  if (bootstrap.mapExport?.busy) {
    watchExport(bootstrap.mapExport).catch(error=>message(`Map export failed: ${error.message}`,true))
      .finally(()=>{state.exporting=false;controls();});
  }
  return {activate(){try{ensureViewport();}catch(error){message(error.message,true);}},
    exporting:()=>state.exporting, async flush(){await menu.flushRenames();await state.saves;if(state.saveError)throw state.saveError;},
    refreshLibrary:async root=>{if(!state.root || hooks.sameDirectory?.(root,state.root))await scan(root);}};
}
