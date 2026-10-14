// Real map parser/backend/renderer with synthetic OWLib binary fixtures.
// Native C4D success is covered by mocked bridge tests, not claimed here.
const {chromium,_electron}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const {spawn,spawnSync}=require('node:child_process');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const project=path.resolve(__dirname,'..'),scratch=fs.mkdtempSync(path.resolve(project,'../preview-build/maps-ui-'));
const source=path.join(scratch,'Library source 日本語'),assets=path.join(source,'Maps','Test','Build');
const characterAssets=path.join(source,'Heroes','Test'),profile=path.join(scratch,'Profile');
const library=path.join(scratch,'Toolchain library'),output=path.join(scratch,'Map output 日本語');
const python=process.env.WHITEHOUND_TEST_PYTHON||'python',executable=process.env.WHITEHOUND_EXE;
const fixture=spawnSync(python,['-c',`import shutil, sys
from pathlib import Path
sys.path[:0]=[str(Path(sys.argv[1])/'tests'),sys.argv[1]]
from test_map_formats import write_fixture
from test_formats import clip_bytes
root=Path(sys.argv[2]); write_fixture(root)
(root/'Broken.owmap').write_bytes(b'bad')
(root/'Idle.owanimclip').write_bytes(clip_bytes())
character=Path(sys.argv[3]); character.mkdir(parents=True, exist_ok=True)
for filename in ('Body.owentity', 'Child.owentity', 'Body.owmdl', 'Child.owmdl', 'Idle.owanimclip'):
    shutil.copy2(root/filename, character/filename)
`,project,assets,characterAssets],{encoding:'utf8',windowsHide:true});
assert.equal(fixture.status,0,fixture.stderr);
for(const folder of [profile,library,output])fs.mkdirSync(folder,{recursive:true});
fs.writeFileSync(path.join(profile,'catalog.json'),JSON.stringify({libraryDirectory:library,
 maps:{root:assets,outputFolder:output,bakeTextures:true}}));
let server,browser,application,page,log='';const errors=[];
const report={passed:false,scratch,packaged:!!executable,syntheticAssets:true,realParser:true,realRenderer:true,liveGameExtraction:false};
const click=locator=>executable?locator.dispatchEvent('click'):locator.click();
async function screenshot(name){
 const file=path.join(scratch,name);
 if(!executable)return page.screenshot({path:file});
 const encoded=await application.evaluate(async({BrowserWindow})=>{
  const image=await BrowserWindow.getAllWindows()[0].webContents.capturePage(undefined,{stayHidden:true,stayAwake:true});
  if(image.isEmpty())throw Error('Native capture is empty.');
  return image.toPNG().toString('base64');
 });
 fs.writeFileSync(file,Buffer.from(encoded,'base64'));
}
async function launch(){
 if(executable){application=await _electron.launch({executablePath:path.resolve(executable),args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:45000});
  page=await application.firstWindow();await application.evaluate(({BrowserWindow,dialog,shell})=>{
   const w=BrowserWindow.getAllWindows()[0];w.hide();w.setSkipTaskbar(true);w.setContentSize(1500,960);w.webContents.setBackgroundThrottling(false);
   globalThis.qaPickerQueue=[];globalThis.qaPickerCalls=[];globalThis.qaOpenedPaths=[];
   dialog.showOpenDialog=async(...args)=>{globalThis.qaPickerCalls.push(args.at(-1));const selected=globalThis.qaPickerQueue.shift();return {canceled:!selected,filePaths:selected?[selected]:[]};};
   shell.openPath=async selected=>{globalThis.qaOpenedPaths.push(selected);return '';};
  });
 }else{server=spawn(python,[path.join(project,'app.py'),'--serve','--port','0','--data-dir',profile],{cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe']});
  const address=await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error(log)),15000);server.stderr.on('data',v=>log+=v);server.stdout.on('data',v=>{log+=v;const m=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(m){clearTimeout(timer);resolve(m[0]);}});server.on('error',reject);});
  browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});page=await browser.newPage({viewport:{width:1500,height:960}});await page.goto(address);
 }
 page.setDefaultTimeout(20000);page.on('pageerror',e=>errors.push(e.message));
 await page.waitForFunction(()=>window.pywebview?.api&&!document.querySelector('#scan-root').disabled
  && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
 await page.evaluate(async()=>{const {PreviewViewport}=await import('./viewer.js');const original=PreviewViewport.prototype.loadAsset;
  PreviewViewport.prototype.loadAsset=function(...args){window.qaViewports??={};window.qaViewports[this.canvas.id]=this;return original.apply(this,args);};});
}
async function nativeRootPicker(selected){
 await application.evaluate((_electron,selected)=>globalThis.qaPickerQueue.push(selected),selected);
 await page.locator('#maps-browse-root').dispatchEvent('click');
}
async function closeNative(){
 const exited=application.waitForEvent('close',{timeout:25000});
 await application.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows()[0].close());
 await exited;application=null;page=null;
}
async function nativeUntil(check){
 const end=Date.now()+10000;
 while(Date.now()<end){if(await check())return;await new Promise(resolve=>setTimeout(resolve,40));}
 throw new Error('Native action did not finish.');
}
async function rename(key,name){const node=page.locator(`#maps-objects-list [data-node-key="${key}"]`);await node.focus();await page.keyboard.press('F2');
 const input=page.locator('#maps-objects-list .object-rename-input');await input.fill(name);await input.press('Enter');
 await page.waitForFunction(()=>!document.querySelector('#maps-objects-list .object-rename-input'));
 assert.equal(await node.locator(':scope > .object-tree-row .object-tree-name').innerText(),name);
}
(async()=>{
 await launch();
 assert.deepEqual(await page.locator('.app-header .workflow-tabs [role="tab"]').allTextContents(),['Animations','Maps','Toolchain']);
 assert.equal(await page.locator('#app-logo,.avolition-empty-logo,.empty-note').count(),0);
 assert.equal(await page.locator('#maps-browse-file,#workflow-maps .map-toolbar').count(),0,'The redundant map header and standalone file picker are removed.');
 assert.equal(await page.locator('#maps-root').inputValue(),assets,'The saved custom Maps source is restored.');
 if(!executable){
  // Only the completed-job notification is simulated. Both library scans and
  // settings persistence still use the real backend and isolated directories.
  await page.evaluate(library=>{
   const api=window.pywebview.api;window.qaOriginalToolchainStatus=api.toolchain_status;window.qaOriginalScanMaps=api.scan_maps;
   window.qaRefreshMapScans=0;
   api.scan_maps=(...args)=>{window.qaRefreshMapScans++;return window.qaOriginalScanMaps(...args);};
   api.toolchain_status=async()=>({ok:true,data:{runtime:{},capabilities:{},job:{jobId:'qa-refresh-only',kind:'export',state:'Completed',busy:false,logs:[],result:{outputDirectory:library}}}});
  },library);
  await click(page.locator('#tab-toolchain'));
  await page.waitForFunction(()=>document.querySelector('#status-text').textContent.startsWith('Toolchain export completed. Library refreshed:'));
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
  assert.equal(await page.evaluate(()=>window.qaRefreshMapScans),0,'Auto-refresh of the default library does not scan over a custom Maps source.');
  assert.equal(await page.locator('#maps-root').inputValue(),assets);
  assert.equal((await page.evaluate(async()=>(await window.pywebview.api.maps_settings()).data.root)),assets);
  await page.evaluate(()=>{window.pywebview.api.toolchain_status=window.qaOriginalToolchainStatus;window.pywebview.api.scan_maps=window.qaOriginalScanMaps;});
  await click(page.locator('#tab-animations'));report.customRootAutoRefresh={passed:true,mockedCompletionOnly:true};
 }
 const scans=await page.evaluate(async({source,assets})=>({
  animations:await window.pywebview.api.scan_root(source,false),
  maps:await window.pywebview.api.scan_maps(assets),
 }),{source,assets});
 assert.equal(scans.animations.ok,true,scans.animations.error);assert.equal(scans.maps.ok,true,scans.maps.error);
 const characterFiles=['Body.owentity','Child.owentity','Body.owmdl','Child.owmdl'].map(name=>path.join(characterAssets,name)).sort();
 assert.deepEqual(scans.animations.data.entities.map(item=>item.path).sort(),characterFiles,
  'A shared Library source includes character entities and models, excluding neighboring map assets.');
 assert.deepEqual(scans.animations.data.clips.map(item=>item.path),[path.join(characterAssets,'Idle.owanimclip')],
  'Map-owned clips are excluded without suppressing the separate character clip.');
 assert.deepEqual(scans.maps.data.maps.map(item=>item.path).sort(),['Broken.owmap','Example.owmap'].map(name=>path.join(assets,name)).sort(),
  'The Maps scanner still lists both variants from the same shared library.');
 report.sharedLibrarySeparation={realBackend:true,characterEntitiesAndModels:4,characterClips:1,mapVariants:2,mapDependenciesExcluded:true};
 await page.locator('#root-path').fill(source);await click(page.locator('#scan-root'));
 await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='1');
 assert.deepEqual((await page.locator('#entity-select option').evaluateAll(options=>options.map(option=>option.value).filter(Boolean))).sort(),characterFiles);
 await page.locator('#entity-select').selectOption(path.join(characterAssets,'Body.owentity'));await click(page.locator('#load-entity'));
 await page.waitForFunction(()=>window.qaViewports?.viewport?.models.length===2&&document.querySelector('#viewport-loading').hidden);
 const animationIds=await page.evaluate(()=>window.qaViewports.viewport.models.map(m=>m.source.id));
 await click(page.locator('#tab-maps'));assert.equal(await page.locator('#workflow-maps').isVisible(),true);
 if(executable)await nativeRootPicker(assets);
 else {await page.locator('#maps-root').fill(assets);await click(page.locator('#maps-scan'));}
 await page.waitForFunction(()=>document.querySelector('#maps-count').textContent==='2');
 await page.locator('#maps-search').fill('Example');assert.equal(await page.locator('.map-row').count(),1);
 await click(page.locator('.map-row'));
 await page.waitForFunction(()=>window.qaViewports?.['maps-viewport']?.models.length===5&&document.querySelector('#maps-loading').hidden);
 if(executable){
  const options=await application.evaluate(()=>globalThis.qaPickerCalls[0]);
  assert.equal(options.title,'Choose export root');assert.deepEqual(options.properties,['openDirectory','createDirectory']);
  assert.equal(options.filters,undefined,'Library source browses for a folder, then the variant tree opens maps.');
  assert.equal(await page.locator('#maps-source-file').innerText(),path.join(assets,'Example.owmap'));
  await nativeRootPicker(null);await nativeUntil(async()=>await application.evaluate(()=>globalThis.qaPickerCalls.length===2));
  assert.equal(await page.locator('#maps-root').inputValue(),assets,'Cancelling Library source browsing retains the scanned folder.');
  assert.equal(await page.locator('#maps-source-file').innerText(),path.join(assets,'Example.owmap'),'Cancelling Library source browsing retains the current map.');
  report.nativeLibraryPicker={directory:true,realScanAndVariantLoad:true,cancelKeepsMap:true,mockedDialogOnly:true};
 }
 assert.equal(await page.locator('#maps-export').isEnabled(),true);
 assert.equal(await page.locator('#maps-objects-summary').innerText(),'5 of 5 meshes shown');
 const geometry=await page.evaluate(()=>{const v=window.qaViewports['maps-viewport'];return {shared:v.models[0].meshes[0].geometry===v.models[1].meshes[0].geometry&&v.models[1].meshes[0].geometry===v.models[2].meshes[0].geometry,
  positions:v.models.slice(0,3).map(m=>m.group.matrix.elements.slice(12,15)),parents:v.getObjects().map(o=>o.parentId),culled:v.models.slice(0,3).every(m=>m.meshes[0].frustumCulled),polygons:v.getVisiblePolygonCount()};});
 assert.equal(geometry.shared,true,'Repeated map models share one GPU geometry.');assert.equal(geometry.culled,true);
 assert.deepEqual(geometry.positions,[[10,20,30],[40,50,60],[-1,2,3]]);assert.equal(geometry.polygons,5);
 const first=page.locator('#maps-objects-list [data-kind="model"]').first();await click(first.locator(':scope > .object-tree-row .object-tree-name'));
 await page.waitForFunction(()=>document.querySelector('#maps-objects-list [data-kind="model"]').getAttribute('aria-selected')==='true');
 const firstId=await first.getAttribute('data-model-id');
 await rename('model:'+firstId,'Courtyard café');await rename('mesh:'+firstId+':0','Stone β');await rename('scene','Renamed map 日本語');
 assert.equal(await page.locator('#maps-scene-name').innerText(),'Renamed map 日本語');
 assert.equal(await page.locator('#maps-export-name').inputValue(),'Renamed map 日本語','Renaming the scene updates an unchanged default export name.');
 await page.locator('#maps-export-name').fill('Custom_scene_filename');
 await rename('scene','Temporary scene label');
 assert.equal(await page.locator('#maps-scene-name').innerText(),'Temporary scene label');
 assert.equal(await page.locator('#maps-export-name').inputValue(),'Custom_scene_filename','Scene renaming preserves an explicitly edited export filename.');
 await rename('scene','Renamed map 日本語');
 assert.equal(await page.locator('#maps-export-name').inputValue(),'Custom_scene_filename');
 const names=await page.evaluate(async p=>(await window.pywebview.api.object_names(p)).data,path.join(assets,'Example.owmap'));
 assert.equal(names['model:'+firstId],'Courtyard café');assert.equal(names['mesh:'+firstId+':0'],'Stone β');
 await click(page.locator('#maps-objects-hide-all'));assert.equal(await page.locator('#maps-objects-summary').innerText(),'0 of 5 meshes shown');
 await click(page.locator('#maps-objects-show-all'));await click(page.locator('#maps-frame'));
 await screenshot('maps-wide.png');
 await click(page.locator('#tab-animations'));
 assert.deepEqual(await page.evaluate(()=>window.qaViewports.viewport.models.map(m=>m.source.id)),animationIds);
 assert.equal(await page.locator('#root-path').inputValue(),source);
 await click(page.locator('#tab-toolchain'));await click(page.locator('#toolchain-tab-maps'));
 await screenshot('toolchain-maps.png');
 await click(page.locator('#tab-maps'));assert.equal(await page.locator('#maps-objects-list [data-node-key="scene"] > .object-tree-row .object-tree-name').innerText(),'Renamed map 日本語');
 if(application)await application.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows()[0].setContentSize(1120,800));else await page.setViewportSize({width:1120,height:800});
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 await screenshot('maps-compact.png');
 await page.locator('#maps-search').fill('Broken');await click(page.locator('.map-row'));
 await page.waitForFunction(()=>document.querySelector('#maps-loading').hidden&&document.querySelector('#maps-status').classList.contains('error'));
 assert.equal(await page.locator('#maps-export').isDisabled(),true);
 assert.equal(await page.locator('#maps-objects-list [data-kind="model"]').count(),0);
 assert.equal(await page.locator('#maps-scene-name').innerText(),'No map loaded');
 assert.equal(await page.locator('#maps-source-file').innerText(),'No map loaded');
 assert.equal(await page.locator('#maps-source-file').getAttribute('title'),'');
 assert.equal(await page.locator('#maps-export-name').inputValue(),'');
 assert.equal(await page.locator('#maps-detail-objects').innerText(),'—');
 assert.equal(await page.locator('#maps-detail-polygons').innerText(),'—');
 assert.equal(await page.locator('#maps-polygon-count').isHidden(),true);
 assert.equal(await page.locator('#maps-mesh-stats').innerText(),'Static map preview');
 assert.equal(await page.locator('#maps-material-status').innerText(),'Static geometry and common material maps.');
 assert.equal(await page.locator('#maps-notes').textContent(),'');
 assert.equal(await page.locator('#maps-notes-panel').isHidden(),true,'A failed load cannot retain notes from the previous map.');
 assert.deepEqual(await page.evaluate(()=>window.qaViewports.viewport.models.map(m=>m.source.id)),animationIds);
 await page.locator('#maps-search').fill('Example');await click(page.locator('.map-row'));
 await page.waitForFunction(()=>document.querySelector('#maps-loading').hidden&&!document.querySelector('#maps-export').disabled);
 assert.equal(await page.locator('#maps-export-name').inputValue(),'Renamed map 日本語');
 assert.equal(await page.locator('#maps-scene-name').innerText(),'Renamed map 日本語');
 assert.equal((await page.evaluate(async p=>(await window.pywebview.api.load_map(p)).data.models[0].name,path.join(assets,'Example.owmap'))),'Prop');
 await page.locator('#maps-output').fill('relative-output');await page.locator('#maps-output').dispatchEvent('change');
 await page.waitForFunction(()=>document.querySelector('#maps-status').classList.contains('error')&&document.querySelector('#maps-status').textContent.includes('absolute'));
 await click(page.locator('#maps-bake-textures'));
 const closeAttempt=await page.evaluate(()=>window.whitehoundPrepareClose());
 assert.equal(closeAttempt.ok,false,'An unrelated checkbox save cannot hide a rejected output path when closing.');
 assert.match(closeAttempt.error,/absolute.*folder path/i);
 assert.equal(await page.locator('#maps-output').isEnabled(),true,'A rejected close leaves the output path editable.');
 const rejectedSettings=await page.evaluate(async()=>(await window.pywebview.api.maps_settings()).data);
 assert.equal(rejectedSettings.outputFolder,output);assert.equal(rejectedSettings.bakeTextures,true);
 await page.locator('#maps-output').fill(output);await page.locator('#maps-output').dispatchEvent('change');
 await page.waitForFunction(async output=>{const result=await window.pywebview.api.maps_settings();return result.ok&&result.data.outputFolder===output&&result.data.bakeTextures===false;},output);
 // This isolated profile has no live bridge. Exercise the real asynchronous
 // API through renderer IPC and recover from the expected connection failure.
 await click(page.locator('#maps-export'));
 await page.waitForFunction(()=>document.querySelector('#maps-status').textContent.startsWith('Map export failed:')
  && !document.querySelector('#maps-export').disabled);
 assert.match(await page.locator('#maps-status').innerText(),/Avolition C4D Bridge/);
 const mapJob=await page.evaluate(async()=>await window.pywebview.api.export_map_status());
 assert.equal(mapJob.ok,true);assert.equal(mapJob.data.state,'Failed');assert.equal(mapJob.data.busy,false);
 assert.ok(mapJob.data.id);assert.equal(await page.locator('#maps-export-spinner').isHidden(),true);
 assert.equal(fs.readdirSync(output).length,0,'A failed native preflight publishes no map files.');
 report.backgroundExport={realBackend:true,realRenderer:true,isolatedMissingBridge:true,failureRecovery:true};
 if(executable){
  assert.equal(await page.locator('#maps-open-output').isEnabled(),true);
  await page.locator('#maps-open-output').dispatchEvent('click');
  await nativeUntil(async()=>await application.evaluate(()=>globalThis.qaOpenedPaths.length===1));
  assert.deepEqual(await application.evaluate(()=>globalThis.qaOpenedPaths),[output]);
  report.nativeOpenOutput={realBackendValidation:true,mockedShellOnly:true,path:output};
  await closeNative();await launch();
  assert.equal(await page.locator('#tab-maps').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#workflow-maps').isVisible(),true);
  assert.equal(await page.locator('#maps-root').inputValue(),assets);
  assert.equal(await page.locator('#maps-output').inputValue(),output);
  assert.equal(await page.locator('#maps-bake-textures').isChecked(),false);
  await click(page.locator('#maps-scan'));
  await page.waitForFunction(()=>document.querySelector('#maps-count').textContent==='2');
  await page.locator('#maps-search').fill('Example');await click(page.locator('.map-row'));
  await page.waitForFunction(()=>!document.querySelector('#maps-export').disabled&&document.querySelector('#maps-loading').hidden);
  assert.equal(await page.locator('#maps-scene-name').innerText(),'Renamed map 日本語');
  assert.equal(await page.locator(`#maps-objects-list [data-node-key="model:${firstId}"] > .object-tree-row .object-tree-name`).innerText(),'Courtyard café');
  await click(page.locator('#tab-toolchain'));
  assert.equal(await page.locator('#toolchain-tab-maps').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#toolchain-maps-panel').isVisible(),true);
  await closeNative();await launch();
  assert.equal(await page.locator('#tab-toolchain').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true);
  assert.equal(await page.locator('#toolchain-tab-maps').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#toolchain-maps-panel').isVisible(),true);
  report.nativeRestart={mapsTab:true,toolchainMapsTab:true,toolchainWorkspace:true,pathsAndBakeTextures:true,objectNames:true};
  await closeNative();
 }
 assert.deepEqual(errors,[]);Object.assign(report,{passed:true,errors,geometry,removedMapHeader:true,libraryScanAndVariantLoad:true,renamePersistence:true,sceneRenameUpdatesDefaultOnly:true,
  independentScenes:true,invalidMapRecovery:true,failedLoadClearsDetails:true,failedOutputSaveBlocksClose:true,repairedOutputSave:true});console.log(JSON.stringify(report));
})().catch(e=>{report.error=String(e.stack||e);report.log=log;console.error(e);process.exitCode=1;}).finally(async()=>{fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));await application?.close();await browser?.close();server?.kill();});

