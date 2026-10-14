// Production tree renderer with isolated in-memory save callbacks. This checks
// editing/keyboard/async behavior; Python tests exercise export-copy validation.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs=require('node:fs');
const path=require('node:path');
const assert=require('node:assert/strict');
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const moduleSource=fs.readFileSync(path.resolve(__dirname,'../web/object-menu.js'),'utf8');
let browser;
(async()=>{
  browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:1100,height:760}});
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  const controls=prefix=>`<section id="${prefix}-menu"><button id="${prefix}-show-all">Show all</button><button id="${prefix}-hide-all">Hide all</button><p id="${prefix}-summary"></p><div id="${prefix}-list"></div></section>`;
  await page.route('https://rename.test/**',route=>{
    if(route.request().url().endsWith('/object-menu.js'))return route.fulfill({contentType:'text/javascript',body:moduleSource});
    return route.fulfill({contentType:'text/html',body:`<!doctype html><html><body>${controls('objects')}${controls('maps-objects')}<p id="polygon-count"></p><p id="maps-polygon-count"></p><button id="outside">Outside</button><script type="module">
    import {ObjectVisibilityMenu,validateObjectName} from './object-menu.js';
    window.validateObjectName=validateObjectName;window.saved={};window.calls=[];window.selected=[];window.keys=[];
    const makeView=()=>({objects:[{id:'model-0',name:'Original model',parentId:null,bones:1,
      boneHierarchy:[{id:'0',name:'bone_0001',parentIndex:-1}],meshes:[{id:'0',name:'Original mesh',visible:true,polygons:1}]}],
      getObjects(){return this.objects.map(o=>({...o,visible:o.meshes.every(m=>m.visible)}));},getVisiblePolygonCount(){return this.objects.flatMap(o=>o.meshes).filter(m=>m.visible).length;},
      setAllObjectsVisible(visible){this.objects.forEach(o=>o.meshes.forEach(m=>m.visible=visible));},setObjectVisible(id,mid,visible){this.objects.find(o=>o.id===id).meshes.filter(m=>mid==null||m.id===mid).forEach(m=>m.visible=visible);}});
    const rename=async(key,name,asset)=>{window.calls.push({key,name,asset});if(window.hold)await new Promise(resolve=>window.release=resolve);if(window.fail){window.fail=false;throw new Error('Disk is read-only');}window.saved[asset]={...window.saved[asset],[key]:name};return window.saved[asset];};
    window.view=makeView();window.menu=new ObjectVisibilityMenu(window.view,id=>window.selected.push(id),{onRename:rename});
    const mapView=makeView();mapView.objects[0].bones=0;mapView.objects[0].boneHierarchy=[];window.mapSelected=[];
    window.mapMenu=new ObjectVisibilityMenu(mapView,id=>{window.mapSelected.push(id);window.mapMenu.setActiveModel(id);},{prefix:'maps-objects',ids:{polygonCount:'maps-polygon-count'},labels:{scene:'Loaded map',tree:'Map objects'},onRename:rename});
    window.menu.setSceneName('Gameplay1P','entity-path');window.mapMenu.setSceneName('Map','map-path');
    document.addEventListener('keydown',event=>window.keys.push(event.key));window.ready=true;
    </script></body></html>`});
  });
  await page.goto('https://rename.test/');await page.waitForFunction(()=>window.ready);
  const item=(key,prefix='objects')=>page.locator(`#${prefix}-list [data-node-key="${key}"]`);
  const label=(key,prefix)=>item(key,prefix).locator(':scope > .object-tree-row > .object-tree-name');
  const editor=()=>page.locator('#objects-list .object-rename-input');
  const waitSaved=()=>page.waitForFunction(()=>!document.querySelector('#objects-list .object-rename-input'));
  assert.equal(await page.locator('#maps-objects-list').getAttribute('aria-label'),'Map objects');
  assert.equal(await page.evaluate(()=>{const ids=[...document.querySelectorAll('[id]')].map(e=>e.id);return new Set(ids).size===ids.length;}),true);
  const validation=await page.evaluate(()=>[' ','a\n','x'.repeat(161),'😀'.repeat(81)].map(value=>{try{validateObjectName(value);return false;}catch{return true;}}));
  assert.deepEqual(validation,[true,true,true,true]);
  assert.equal(await page.evaluate(()=>validateObjectName('  Name / 日本語 😀  ')),'Name / 日本語 😀');

  await label('model:model-0').dblclick();
  assert.equal(await editor().inputValue(),'Original model');
  await editor().fill('Arms 日本語');await editor().press('ArrowLeft');await editor().press('Space');
  await editor().fill('Arms 日本語');await editor().press('Enter');await waitSaved();
  await pause(550);
  assert.equal(await label('model:model-0').innerText(),'Arms 日本語');
  assert.match(await label('model:model-0').getAttribute('title'),/Original: Original model/);
  assert.deepEqual(await page.evaluate(()=>window.selected),['model-0'],'The first click selects the object immediately; the second opens its name editor.');
  assert.deepEqual(await page.evaluate(()=>window.keys),[],'Editing keys do not reach viewport/timeline shortcuts.');
  assert.equal(await page.evaluate(()=>window.view.objects[0].name),'Original model');
  assert.deepEqual(await page.evaluate(()=>window.calls),[{key:'model:model-0',name:'Arms 日本語',asset:'entity-path'}]);

  await item('mesh:model-0:0').focus();await page.keyboard.press('F2');
  await editor().fill('  Barrel β  ');await page.locator('#outside').focus();await waitSaved();
  assert.equal(await label('mesh:model-0:0').innerText(),'Barrel β','Blur saves the trimmed display value.');
  await label('scene').dblclick();await editor().fill(' ');await editor().press('Enter');
  assert.match(await page.locator('#objects-rename-status').innerText(),/cannot be empty/);
  assert.equal(await editor().getAttribute('aria-invalid'),'true');
  const invalidFlush=await page.evaluate(async()=>{try{await window.menu.flushRenames();return '';}catch(error){return error.message;}});
  assert.match(invalidFlush,/cannot be empty/);
  await editor().press('Escape');await waitSaved();
  assert.equal(await label('scene').innerText(),'Gameplay1P');
  await page.evaluate(()=>window.menu.whenIdle());
  await label('scene').dblclick();await editor().fill('Named setup');await editor().press('Enter');await waitSaved();
  assert.equal(await label('scene').innerText(),'Named setup');

  const skeleton=item('skeleton:model-0');
  await skeleton.locator(':scope > .object-tree-row > .object-tree-expander').click();
  await item('bone:model-0:0').focus();await page.keyboard.press('F2');
  assert.equal(await editor().count(),0,'Bone names cannot be edited.');
  await label('bone:model-0:0').dblclick();
  assert.equal(await editor().count(),0);
  assert.equal(await label('bone:model-0:0').innerText(),'bone_0001');

  await label('model:model-0','maps-objects').dblclick();
  const mapEditor=page.locator('#maps-objects-list .object-rename-input');
  await mapEditor.fill('Spawn structure');await mapEditor.press('Enter');
  await page.waitForFunction(()=>!document.querySelector('#maps-objects-list .object-rename-input'));
  assert.equal(await label('model:model-0','maps-objects').innerText(),'Spawn structure');
  assert.equal(await label('model:model-0').innerText(),'Arms 日本語','Two trees keep independent names and asset identity.');
  await page.evaluate(()=>window.mapSelected=[]);
  await item('model:model-0','maps-objects').focus();await page.keyboard.press('Enter');
  assert.deepEqual(await page.evaluate(()=>window.mapSelected),['model-0'],'Static models can be selected without bones.');
  assert.equal(await item('model:model-0','maps-objects').getAttribute('aria-selected'),'true');
  await item('mesh:model-0:0','maps-objects').focus();await page.keyboard.press('Enter');
  assert.deepEqual(await page.evaluate(()=>window.mapSelected),['model-0','model-0'],'Static mesh selection selects its containing object.');

  await page.evaluate(()=>window.hold=true);
  await label('model:model-0').dblclick();await editor().fill('Awaited rename');await editor().press('Enter');
  await page.waitForFunction(()=>Boolean(window.release));
  assert.equal(await editor().isDisabled(),true);
  await page.evaluate(()=>{window.flushed=false;window.menu.flushRenames().then(()=>window.flushed=true);});
  await pause(60);assert.equal(await page.evaluate(()=>window.flushed),false);
  await page.evaluate(()=>{window.hold=false;window.release();window.release=null;});
  await page.waitForFunction(()=>window.flushed);
  assert.equal(await label('model:model-0').innerText(),'Awaited rename');

  await page.evaluate(()=>window.fail=true);
  await label('model:model-0').dblclick();await editor().fill('Rejected name');await editor().press('Enter');
  await page.waitForFunction(()=>document.querySelector('#objects-rename-status').textContent.includes('Disk is read-only'));
  assert.equal(await editor().isEnabled(),true);
  const failedIdle=await page.evaluate(async()=>{try{await window.menu.whenIdle();return '';}catch(error){return error.message;}});
  assert.match(failedIdle,/Disk is read-only/);
  await editor().fill('Recovered name');await editor().press('Enter');await waitSaved();
  await page.evaluate(()=>window.menu.whenIdle());
  assert.equal(await label('model:model-0').innerText(),'Recovered name');

  await label('model:model-0').dblclick();await editor().fill('Draft');
  await page.evaluate(()=>window.menu.refresh());assert.equal(await editor().inputValue(),'Draft','An incidental refresh cannot discard the active editor.');
  await editor().press('Escape');await waitSaved();
  assert.equal(await label('model:model-0').innerText(),'Recovered name');
  await page.evaluate(()=>{window.menu.setSceneName('Another asset','other-path');window.menu.setNames({'model:model-0':'Other asset label'});});
  assert.equal(await label('model:model-0').innerText(),'Other asset label');
  assert.equal(await label('scene').innerText(),'Another asset');
  assert.equal(await label('mesh:model-0:0').innerText(),'Original mesh');
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({passed:true,mockedSaveCallbacks:true,twoTrees:true,doubleClick:true,F2:true,blurSave:true,
    unicode:true,validation:true,escapeCancel:true,boneNamesPreserved:true,flushWaits:true,saveFailureRecovery:true,originalNamesRetained:true,staticObjectSelection:true}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
