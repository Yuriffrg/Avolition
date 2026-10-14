// Production object-tree DOM and keyboard behavior, isolated from WebGL/IPC.
// The viewport supplies deterministic object metadata; no local server is needed.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const project=path.resolve(__dirname,'..');
const source=fs.readFileSync(path.join(project,'web/object-menu.js'),'utf8');
let browser,checks=0;
const equal=(actual,expected,label)=>{assert.deepEqual(actual,expected,label);checks++;};
(async()=>{
  browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:900,height:800}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.setContent(`<!doctype html><style>${fs.readFileSync(path.join(project,'web/styles.css'),'utf8')}</style>
    <section id="objects-menu" style="height:740px;width:550px;overflow:auto"><button id="objects-show-all">Show all</button>
    <button id="objects-hide-all">Hide all</button><span id="objects-summary"></span><span id="polygon-count"></span><div id="objects-list"></div></section>`);
  await page.evaluate(async source=>{
    const {ObjectVisibilityMenu}=await import('data:text/javascript;charset=utf-8,'+encodeURIComponent(source));
    const objects=Array.from({length:121},(_,i)=>({id:'m'+i,name:'Building '+i,parentId:null,bones:2,
      boneHierarchy:[{id:'0',name:'root',parentIndex:-1},{id:'1',name:'hinge',parentIndex:0}],
      meshes:[{id:'3',name:'Surface '+i,visible:true,polygons:2},{id:'7',name:'Trim '+i,visible:true,polygons:3}]}));
    objects.push({...structuredClone(objects[0]),id:'nested',name:'Attached detail',parentId:'m0'});
    window.qaVisibility=[];window.qaRenames=[];window.qaSelections=[];window.qaNames={};window.qaObjects=objects;
    const snapshot=()=>objects.map(object=>({...object,visible:object.meshes.every(mesh=>mesh.visible),
      indeterminate:object.meshes.some(mesh=>mesh.visible)&&object.meshes.some(mesh=>!mesh.visible),meshes:object.meshes.map(mesh=>({...mesh}))}));
    const viewport={getObjects:snapshot,getVisiblePolygonCount:()=>objects.flatMap(object=>object.meshes).reduce((sum,mesh)=>sum+(mesh.visible?mesh.polygons:0),0),
      setObjectVisible(id,meshId,visible){window.qaVisibility.push({id,meshId,visible});const object=objects.find(object=>object.id===id);
        for(const mesh of object.meshes)if(meshId==null||mesh.id===String(meshId))mesh.visible=visible;},
      setAllObjectsVisible(visible){for(const object of objects)for(const mesh of object.meshes)mesh.visible=visible;}};
    const menu=new ObjectVisibilityMenu(viewport,id=>{window.qaSelections.push(id);menu.setActiveModel(id);},{compactLargeScenes:true,
      onRename:async(key,name,sceneKey)=>{window.qaRenames.push({key,name,sceneKey});window.qaNames={...window.qaNames,[key]:name};return window.qaNames;}});
    window.qaMenu=menu;menu.setSceneName('Large map','fixture-map');
    window.qaNames={'mesh:m0:7':'Saved trim','model:m1':'Saved building'};menu.setNames(window.qaNames);
  },source);
  const item=key=>page.locator(`[data-node-key="${key}"]`);
  const row=key=>item(key).locator(':scope > .object-tree-row');
  const expand=key=>row(key).locator('.object-tree-expander').click();
  const visible=key=>row(key).locator('input.object-visibility');
  const active=()=>page.evaluate(()=>document.activeElement.dataset.nodeKey);
  equal(await page.evaluate(()=>window.qaObjects.length),122);
  equal(await page.locator('[data-kind="model"]').count(),121,'Only the 121 root model rows are initially mounted.');
  equal(await page.locator('[data-kind="mesh"],[data-kind="skeleton"],[data-kind="bone"]').count(),0,'Collapsed descendants allocate no DOM rows.');
  equal(await page.locator('[data-kind="model"][aria-expanded="false"]').count(),121);
  equal(await page.locator('#objects-summary').innerText(),'244 of 244 meshes shown','Summary includes unmounted meshes.');
  equal(await page.locator('#polygon-count').innerText(),'Polygons: 610');
  await expand('model:m0');
  equal(await page.locator('[data-kind="mesh"]').count(),2,'Opening one model mounts only its own meshes.');
  equal(await item('mesh:m0:3').count(),1);equal(await item('mesh:m0:7').count(),1);
  equal(await item('mesh:m0:0').count(),0,'Filtered source mesh IDs do not become display indices.');
  equal(await row('mesh:m0:7').locator('.object-tree-name').innerText(),'Saved trim','Saved names apply when a descendant first mounts.');
  equal(await item('model:nested').getAttribute('aria-expanded'),'false');
  equal(await item('mesh:nested:3').count(),0,'Nested models remain unmounted until opened.');
  equal(await page.locator('[data-kind="bone"]').count(),0,'Skeleton bones remain lazy too.');
  await visible('mesh:m0:7').uncheck();
  equal(await page.evaluate(()=>window.qaVisibility.at(-1)),{id:'m0',meshId:'7',visible:false});
  equal(await visible('model:m0').evaluate(element=>element.indeterminate),true);
  equal(await visible('scene').getAttribute('aria-checked'),'mixed');
  equal(await page.locator('#objects-summary').innerText(),'243 of 244 meshes shown');
  await page.locator('#objects-hide-all').click();await expand('model:m1');
  equal(await visible('mesh:m1:3').isChecked(),false,'A late-mounted mesh reflects current global visibility.');
  equal(await row('model:m1').locator('.object-tree-name').innerText(),'Saved building');
  equal(await page.locator('#objects-summary').innerText(),'0 of 244 meshes shown');
  await page.locator('#objects-show-all').click();
  equal(await visible('mesh:m0:7').isChecked(),true);equal(await visible('mesh:m1:3').isChecked(),true);
  await item('model:m0').focus();
  const order=[];for(let i=0;i<5;i++){await page.keyboard.press('ArrowDown');order.push(await active());}
  equal(order,['mesh:m0:3','mesh:m0:7','skeleton:m0','model:nested','model:m1'],'Keyboard order follows DOM order after lazy mounting.');
  await item('model:nested').focus();await page.keyboard.press('Enter');
  equal(await page.evaluate(()=>window.qaSelections.at(-1)),'nested');
  equal(await item('model:nested').getAttribute('aria-selected'),'true');
  await page.keyboard.press('ArrowRight');
  equal(await item('mesh:nested:3').count(),1);equal(await item('model:nested').getAttribute('aria-selected'),'true','Mounting descendants preserves active selection.');
  await page.keyboard.press('ArrowRight');equal(await active(),'mesh:nested:3');
  await page.keyboard.press('ArrowLeft');equal(await active(),'model:nested');
  await page.keyboard.press('ArrowLeft');equal(await item('model:nested').getAttribute('aria-expanded'),'false');
  await page.keyboard.press('ArrowDown');equal(await active(),'model:m1','Collapsed descendants are skipped by keyboard navigation.');
  await expand('skeleton:m0');equal(await page.locator('[data-kind="bone"]').count(),1);
  await expand('bone:m0:0');equal(await page.locator('[data-kind="bone"]').count(),2,'Each skeleton level mounts independently.');
  await item('mesh:m0:7').focus();await page.keyboard.press('F2');
  await page.locator('.object-rename-input').fill('New trim β');await page.keyboard.press('Enter');
  await page.waitForFunction(()=>!document.querySelector('.object-rename-input'));
  equal(await page.evaluate(()=>window.qaRenames.at(-1)),{key:'mesh:m0:7',name:'New trim β',sceneKey:'fixture-map'});
  await expand('model:m0');await expand('model:m0');
  equal(await row('mesh:m0:7').locator('.object-tree-name').innerText(),'New trim β','Collapse/reopen retains renamed labels.');
  equal(await page.locator('[data-node-key="mesh:m0:7"]').count(),1,'Reopening never duplicates mounted children.');
  await page.evaluate(()=>window.qaMenu.refresh());
  equal(await row('mesh:m0:7').locator('.object-tree-name').innerText(),'New trim β','Refresh preserves expansion and names.');
  equal(await item('model:m120').getAttribute('aria-expanded'),'false');
  equal(await item('mesh:m120:3').count(),0,'Refreshing does not mount untouched model descendants.');
  equal(await page.locator('#objects-list [tabindex="0"]').count(),1,'Roving focus retains one keyboard entry point.');
  await page.keyboard.press('End');equal(await active(),'model:m120');
  await page.keyboard.press('Home');equal(await active(),'scene');
  await page.evaluate(()=>{window.qaMenu.setSceneName('Other map','other-map');});
  equal(await page.locator('[data-kind="mesh"],[data-kind="bone"]').count(),0,'Switching maps clears expansions and lazy descendants.');
  equal(await row('model:m1').locator('.object-tree-name').innerText(),'Building 1','Names do not leak into another scene.');
  // Small entity scenes retain their familiar eager model hierarchy.
  await page.evaluate(()=>{window.qaObjects.splice(3);window.qaMenu.refresh();});
  equal(await page.locator('[data-kind="model"][aria-expanded="true"]').count(),3);
  equal(await page.locator('[data-kind="mesh"]').count(),6);
  equal(errors,[]);
  console.log(JSON.stringify({passed:true,checks,productionObjectTree:true,fixtureViewport:true,networkRequired:false}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
