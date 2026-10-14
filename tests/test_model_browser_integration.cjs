// Real parser, textures, renderer and application state, with an isolated profile.
// Set WHITEHOUND_EXE for native preload/stdio integration; no export is requested.
const {chromium,_electron}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const {spawn,spawnSync}=require('node:child_process');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const project=path.resolve(__dirname,'..'),scratch=fs.mkdtempSync(path.resolve(project,'../preview-build/model-browser-integration-'));
const library=path.join(scratch,'Library 日本語'),profile=path.join(scratch,'Profile');
const python=process.env.WHITEHOUND_TEST_PYTHON||'python',executable=process.env.WHITEHOUND_EXE;
const fixture=spawnSync(python,['-c',`import shutil,sys
from pathlib import Path
source,library=map(Path,sys.argv[1:])
for hero in ('Mercy','Widowmaker'):
    for skin in ('Classic','Alternate'):
        root=library/'Heroes'/hero/'Skin'/'Overwatch'/skin
        shutil.copytree(source/'tests/material-assets',root)
        entity=(root/'Entities/MaterialRobot/MaterialRobot.owentity').read_bytes()
        for name in ('Gameplay1P','Gameplay3P','00000000ABCD.003'):
            target=root/'Entities'/name/(name+'.owentity');target.parent.mkdir(parents=True)
            target.write_bytes(entity)
        (root/'Entities/Broken.owentity').write_bytes(b'bad')
`,project,library],{encoding:'utf8',windowsHide:true});
assert.equal(fixture.status,0,fixture.stderr);
const entity=(hero,skin,name)=>path.join(library,'Heroes',hero,'Skin','Overwatch',skin,'Entities',name,name+'.owentity');
const initial=entity('Widowmaker','Classic','Gameplay1P'),candidate=entity('Widowmaker','Classic','Gameplay3P');
let server,browser,application,page,log='';const errors=[];
const report={passed:false,scratch,packaged:!!executable,realParser:true,realRenderer:true,nativeExportRequested:false};
const click=locator=>executable?locator.dispatchEvent('click'):locator.click();
const row=filename=>page.locator('#model-browser-list [data-kind="asset"]').filter({has:page.locator(`.model-browser-label`,{hasText:filename})}).first().locator(':scope > .model-browser-row');
async function launch(){
 if(executable){
  application=await _electron.launch({executablePath:path.resolve(executable),args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:45000});
  page=await application.firstWindow();
  await application.evaluate(({BrowserWindow})=>{const win=BrowserWindow.getAllWindows()[0];win.hide();win.setSkipTaskbar(true);win.setContentSize(1500,940);win.webContents.setBackgroundThrottling(false);});
 }else{
  server=spawn(python,[path.join(project,'app.py'),'--serve','--port','0','--data-dir',profile],{cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe']});
  const address=await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error(log)),15000);server.once('error',reject);server.stderr.on('data',value=>log+=value);server.stdout.on('data',value=>{log+=value;const match=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(match){clearTimeout(timer);resolve(match[0]);}});});
  browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});page=await browser.newPage({viewport:{width:1500,height:940}});await page.goto(address);
 }
 page.setDefaultTimeout(30000);page.on('pageerror',error=>errors.push(error.message));
 await page.waitForFunction(()=>window.pywebview?.api&&!document.querySelector('#scan-root').disabled&&/^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
 await page.evaluate(async()=>{const {PreviewViewport}=await import('./viewer.js'),original=PreviewViewport.prototype.loadAsset;window.qaViews={};
  PreviewViewport.prototype.loadAsset=function(...args){window.qaViews[this.canvas.id]=this;return original.apply(this,args);};});
}
async function mainState(){return page.evaluate(()=>{const v=window.qaViews.viewport;return {
 scene:document.querySelector('#scene-name').textContent,path:document.querySelector('#scene-name').title,
 assignments:v.assignments.map(item=>[item.modelId,item.clip?.path||item.clip?.name]),frame:v.frame,
 camera:v.camera.position.toArray(),target:v.controls.target.toArray(),rotation:v.camera.quaternion.toArray(),
 objects:v.getObjects().map(item=>[item.id,item.name,item.visible]),
 textures:v.models.flatMap(model=>model.materialBindings.map(binding=>binding.textured?.uuid)),
 setup:document.querySelector('#animation-set').textContent,
 };});}
async function waitPreview(name){await page.waitForFunction(name=>document.querySelector('#model-browser-title').textContent===name&&document.querySelector('#model-browser-status').textContent.startsWith('Preview ready'),name);}
(async()=>{
 await launch();
 assert.equal(await page.locator('#browse-entity').count(),0,'The obsolete entity-file picker is removed.');
 await page.locator('#root-path').fill(library);await click(page.locator('#scan-root'));
 await page.waitForFunction(()=>!document.querySelector('#browse-models').disabled);
 const order=await page.locator('#entity-select option').evaluateAll(nodes=>nodes.map(node=>node.value));
 assert.equal(order.filter(value=>/Gameplay1P\.owentity$/i.test(value)).length,4);
 assert.ok(order.slice(0,4).every(value=>/Gameplay1P\.owentity$/i.test(value)));
 assert.ok(order.slice(4,8).every(value=>/Gameplay3P\.owentity$/i.test(value)));
 await page.locator('#entity-select').selectOption(initial);await click(page.locator('#load-entity'));
 await page.waitForFunction(()=>window.qaViews.viewport?.models.length&&document.querySelector('#viewport-loading').hidden);
 await click(page.locator('#clip-list .clip-row').first());
 await page.waitForFunction(()=>window.qaViews.viewport?.assignments.length>0&&document.querySelector('#viewport-loading').hidden);
 // Pause at a known frame and move the camera so a stray F key is observable.
 await page.evaluate(()=>{if(document.querySelector('#play-pause').getAttribute('aria-label')==='Pause')document.querySelector('#play-pause').click();const v=window.qaViews.viewport;v.controls.enableDamping=false;v.camera.position.x+=2;v.controls.update();});
 const before=await mainState();assert.ok(before.textures.some(Boolean));
 const scannedRows=await page.evaluate(()=>{
  document.querySelector('#browse-models').click();
  return {previewLoaded:!!window.qaViews['model-browser-canvas'],rows:[...document.querySelectorAll('#model-browser-list [data-kind="asset"]')].map(node=>({
   label:node.querySelector('.model-browser-label').textContent,bones:node.querySelector('.model-browser-bones').textContent,
   badge:node.querySelector('.model-browser-detail').textContent,path:node.dataset.path,title:node.querySelector('.model-browser-row').title,
   boneTitle:node.querySelector('.model-browser-bones').title,
  }))};
 });
 assert.equal(scannedRows.previewLoaded,false,'The list reports scanned counts before opening any preview geometry.');
 assert.deepEqual(scannedRows.rows.slice(0,2).map(item=>[item.label,item.bones,item.badge]),[['Gameplay1P','3 bones','Entity'],['Gameplay3P','3 bones','Entity']]);
 assert.ok(scannedRows.rows.every(item=>item.title===item.path),'Counts preserve full source-path tooltips.');
 assert.equal(scannedRows.rows.find(item=>item.badge==='Model').bones,'3 bones','Standalone models show their own skeleton count.');
 const broken=scannedRows.rows.find(item=>item.label==='Broken');
 assert.equal(broken.bones,'— bones');assert.ok(broken.boneTitle,'Unreadable files explain the unavailable count.');
 report.scannedBoneCountsBeforePreview=true;
 await waitPreview('Gameplay1P');
 assert.equal(await page.locator('#model-browser-hero').inputValue(),'Widowmaker');
 assert.equal(await page.locator('#model-browser-skin').inputValue(),'Classic');
 assert.deepEqual((await page.locator('#model-browser-list [data-kind="asset"] .model-browser-label').allTextContents()).slice(0,2),['Gameplay1P','Gameplay3P']);
 await page.locator('#model-browser-sort').selectOption('size-desc');
 const sizeSorted=await page.locator('#model-browser-list [data-kind="asset"]').evaluateAll(nodes=>nodes.map(node=>node.dataset.path));
 assert.ok(sizeSorted[0].endsWith('.owmdl'),'File-size sorting places the actual large model ahead of its small entity definitions.');
 assert.deepEqual(sizeSorted.map(filename=>fs.statSync(filename).size),sizeSorted.map(filename=>fs.statSync(filename).size).sort((a,b)=>b-a));
 assert.equal(await page.locator('#model-browser-list [aria-selected="true"]').getAttribute('data-path'),initial,'Reordering preserves the preview selection.');
 assert.equal(await page.locator('#model-browser-title').innerText(),'Gameplay1P');
 assert.deepEqual(await mainState(),before,'Sorting the browser preserves the loaded scene, animation and camera.');
 await page.locator('#model-browser-sort').selectOption('gameplay');
 await click(row('Gameplay3P'));await waitPreview('Gameplay3P');
 await page.locator('#model-browser-canvas').focus();await page.keyboard.press('f');
 assert.deepEqual(await mainState(),before,'Preview selection and frame shortcut preserve the loaded scene, clips, camera and GPU materials.');
 const preview=await page.evaluate(()=>{const v=window.qaViews['model-browser-canvas'];return {meshes:v.getObjects().reduce((sum,item)=>sum+item.meshes.length,0),textured:v.models.flatMap(m=>m.materialBindings).filter(b=>b.textured).length};});
 assert.equal(preview.meshes,8);assert.equal(preview.textured,8);report.preview=preview;
 if(!executable){
   await page.screenshot({path:path.join(scratch,'model-browser.png')});
   await page.setViewportSize({width:1060,height:700});
   await page.locator('#model-browser-sort').selectOption('size-desc');
   await page.locator('#model-browser-frame').click();
   for(const id of ['model-browser','model-browser-sort','model-browser-count','model-browser-load','model-browser-cancel']){
     const box=await page.locator('#'+id).boundingBox();
     assert.ok(box&&box.x>=0&&box.y>=0&&box.x+box.width<=1060&&box.y+box.height<=700,id+' fits the minimum window size.');
   }
   await page.screenshot({path:path.join(scratch,'model-browser-sort-1060x700.png')});
   await page.setViewportSize({width:1500,height:940});
   await page.locator('#model-browser-sort').selectOption('gameplay');
 }
 await click(page.locator('#model-browser-cancel'));
 assert.equal(await page.locator('#model-browser').isVisible(),false);
 assert.deepEqual(await mainState(),before,'Cancel leaves the previous setup intact.');
 assert.equal(await page.evaluate(()=>window.qaViews['model-browser-canvas'].models.length),0,'Closed preview releases geometry.');
 await click(page.locator('#browse-models'));await waitPreview('Gameplay1P');
 await page.locator('#model-browser-search').fill('ABCD');
 assert.equal(await page.locator('#model-browser-list [data-kind="asset"]').count(),1);
 assert.equal(await page.locator('#model-browser-list .model-browser-bones').innerText(),'3 bones','Searching by GUID preserves the scanned count.');
 await click(row('Entity 00000000ABCD.003'));await waitPreview('Entity 00000000ABCD.003');
 await page.locator('#model-browser-search').fill('No results here');
 assert.equal(await page.locator('#model-browser-no-results').isVisible(),true);
 await page.locator('#model-browser-search').fill('Broken');await click(row('Broken'));
 await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.startsWith('Preview unavailable'));
 assert.deepEqual(await mainState(),before,'A failed preview does not clear the current scene.');
 await page.locator('#model-browser-search').fill('');await click(row('Gameplay3P'));await waitPreview('Gameplay3P');
 await click(page.locator('#model-browser-load'));
 await page.waitForFunction(path=>!document.querySelector('#model-browser').open&&document.querySelector('#scene-name').title===path&&document.querySelector('#viewport-loading').hidden,candidate);
 assert.equal((await mainState()).path,candidate,'Only the explicit Load action replaces the active entity.');
 assert.equal(await page.locator('#entity-select').inputValue(),candidate);
 await click(page.locator('#browse-models'));await waitPreview('Gameplay3P');
 await page.locator('#model-browser-search').fill('Broken');await click(row('Broken'));
 await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.startsWith('Preview unavailable'));
 await click(page.locator('#model-browser-load'));
 await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.startsWith('Could not load'));
 assert.equal(await page.locator('#model-browser').isVisible(),true,'A failed explicit Load keeps the browser open to choose another candidate.');
 await page.locator('#model-browser-search').fill('');await click(row('Gameplay3P'));await waitPreview('Gameplay3P');
 await click(page.locator('#model-browser-load'));
 await page.waitForFunction(()=>!document.querySelector('#model-browser').open&&document.querySelector('#viewport-loading').hidden);
 assert.equal((await mainState()).path,candidate,'Loading can recover after a broken candidate.');
 // One retained browser context across reopen; no accumulated candidates.
 const contexts=await page.evaluate(()=>Object.keys(window.qaViews));assert.deepEqual(contexts.sort(),['model-browser-canvas','viewport']);
 assert.deepEqual(errors,[]);report.passed=true;report.noPreviewStateMutation=true;report.explicitLoad=true;
 console.log(JSON.stringify(report,null,2));
})().catch(error=>{report.error=String(error.stack||error);console.error(error);process.exitCode=1;}).finally(async()=>{
 fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify({...report,errors,log},null,2));
 if(application)await application.close().catch(()=>{});if(browser)await browser.close();if(server)server.kill();
});
