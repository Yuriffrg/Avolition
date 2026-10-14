// HISTORICAL HARNESS: v0.8.7-era Analyze Library / Animation evidence UI.
// Retained for the earlier interface; current UI deliberately removed it.
// Current object-matching tests are listed in README.md; no assertions are skipped.
// Real packaged Electron -> IPC -> frozen backend analysis of a read-only local
// export. No DataTool extraction or C4D/animation export is performed.
const {_electron}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const crypto=require('node:crypto');
const build=path.resolve(__dirname,'../../preview-build');
const executable=path.resolve(process.env.WHITEHOUND_EXE || path.join(build,'whitehound-v087/dist/Whitehound/Whitehound.exe'));
const assets=process.env.WHITEHOUND_REAL_ROOT || 'C:\\Users\\Admin\\Desktop\\Overwatch Tool\\Datatool\\Widowmaker';
const clip=path.join(assets,'animations','00000000033C.owanimclip');
const entity=path.join(assets,'Entities','Gameplay1P','Gameplay1P.owentity');
const scratch=fs.mkdtempSync(path.join(build,'analysis-desktop-'));
const profile=path.join(scratch,'Profile');
const sha=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const tree=folder=>fs.readdirSync(folder,{withFileTypes:true}).flatMap(entry=>{
  const file=path.join(folder,entry.name);
  if(entry.isDirectory())return tree(file);
  const stat=fs.statSync(file);return [[path.relative(assets,file),stat.size,stat.mtimeMs]];
}).sort((a,b)=>a[0].localeCompare(b[0]));
const before={files:tree(assets),clip:sha(clip),entity:sha(entity)};
const report={passed:false,executable,scratch,profile,assets,clip,entity,hidden:true,qaOnlyNoSandbox:true,realAssets:true,realPackagedBackend:true,mockedAnalysis:false,realExtraction:false};
let application,page;
const errors=[];
async function launch(){
  application=await _electron.launch({executablePath:executable,args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:60000});
  page=await application.firstWindow({timeout:60000});
  page.setDefaultTimeout(60000);
  page.on('pageerror',error=>errors.push(error.message));
  await application.evaluate(({BrowserWindow,dialog})=>{
    const window=BrowserWindow.getAllWindows()[0];window.hide();window.setSkipTaskbar(true);window.webContents.setBackgroundThrottling(false);
    globalThis.qaCloseMessages=[];dialog.showMessageBox=async(...args)=>{globalThis.qaCloseMessages.push(args.at(-1));return{response:0,checkboxChecked:false};};
  });
  await page.waitForFunction(()=>window.pywebview?.api&&!document.querySelector('#scan-root').disabled&&/^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  const native=await application.evaluate(({BrowserWindow})=>{
    const window=BrowserWindow.getAllWindows()[0],prefs=window.webContents.getLastWebPreferences();
    return{visible:window.isVisible(),url:window.webContents.getURL(),sandbox:prefs.sandbox,contextIsolation:prefs.contextIsolation,nodeIntegration:prefs.nodeIntegration};
  });
  assert.deepEqual(native,{visible:false,url:'whitehound://app/index.html',sandbox:true,contextIsolation:true,nodeIntegration:false});
  const methods=await page.evaluate(()=>['analysis_start','analysis_status','analysis_index','analysis_detail'].map(name=>[name,typeof window.pywebview.api[name]]));
  assert.ok(methods.every(([,kind])=>kind==='function'));
  report.native=native;report.methods=methods;
}
async function close(){
  const event=application.waitForEvent('close',{timeout:30000});
  await application.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows()[0].close());
  await event;application=null;page=null;
}
async function scan(){
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').dispatchEvent('click');
  await page.waitForFunction(()=>document.querySelectorAll('.clip-row').length>100&&!document.querySelector('#scan-root').disabled,undefined,{timeout:120000});
}
async function detail(){
  const result=await page.evaluate(async({clip,assets})=>await window.pywebview.api.analysis_detail(clip,assets),{clip,assets});
  assert.equal(result.ok,true,result.error);return result.data;
}
(async()=>{
  await launch();
  await page.locator('#export-c4d').uncheck();
  assert.equal(await page.locator('#export-clip').isChecked(),false);
  await scan();
  await page.locator('#output-path').fill(path.join(scratch,'Unused copies'));
  await page.locator('#output-path').press('Enter');
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js'),load=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;window.qaLoadCount=(window.qaLoadCount||0)+1;return load.apply(this,args);};
  });
  await page.locator('#entity-select').selectOption(entity);
  await page.locator('#load-entity').dispatchEvent('click');
  await page.waitForFunction(()=>window.qaViewport?.models.length>0&&document.querySelector('#viewport-loading').hidden,undefined,{timeout:120000});
  await page.locator('#clip-search').fill('00000000033C');
  await page.locator('.clip-row').first().dispatchEvent('click');
  await page.waitForFunction(()=>!document.querySelector('#timeline').disabled&&document.querySelector('#viewport-loading').hidden);
  if(await page.locator('#play-pause').getAttribute('aria-label')==='Pause')await page.locator('#play-pause').dispatchEvent('click');
  await page.locator('#timeline').fill('0');
  const retained=await page.evaluate(()=>{
    window.qaBefore={canvas:document.querySelector('#viewport'),viewport:window.qaViewport,models:[...window.qaViewport.models],loads:window.qaLoadCount};
    return{rig:document.querySelector('#rig-select').value,name:document.querySelector('#detail-filename').textContent,nickname:document.querySelector('#clip-name').value,frame:document.querySelector('#timeline').value,assignments:window.qaViewport.assignments.map(({modelId,clip})=>({modelId,name:clip.name}))};
  });
  await page.locator('#analyze-library').dispatchEvent('click');
  await page.waitForFunction(()=>!document.querySelector('#analysis-filters').hidden&&!document.querySelector('#analyze-library').disabled,undefined,{timeout:120000});
  const first=await detail();
  assert.ok(first);
  assert.equal(first.asset.sourceName,null);
  assert.equal(first.asset.resource.fullGuid,null,'The short display name must not invent full GUID identity bits.');
  assert.equal(typeof first.asset.id,'string');
  assert.equal(first.asset.resource.type,'006');
  assert.ok(first.asset.models.some(model=>model.matchedBoneCount===42&&model.animatedBoneCount===42));
  assert.equal(await page.locator('#detail-filename').textContent(),retained.name);
  assert.equal(await page.locator('#rig-select').inputValue(),retained.rig);
  assert.equal(await page.locator('#clip-name').inputValue(),retained.nickname);
  assert.equal(await page.locator('#timeline').inputValue(),retained.frame);
  assert.deepEqual(await page.evaluate(()=>window.qaViewport.assignments.map(({modelId,clip})=>({modelId,name:clip.name}))),retained.assignments);
  assert.equal(await page.evaluate(()=>window.qaBefore.canvas===document.querySelector('#viewport')&&window.qaBefore.viewport===window.qaViewport&&window.qaBefore.models.every((model,index)=>model===window.qaViewport.models[index])&&window.qaBefore.loads===window.qaLoadCount),true);
  await page.locator('#animation-evidence>summary').dispatchEvent('click');
  await page.waitForFunction(()=>document.querySelector('#animation-evidence-content').textContent.includes('Original source name'));
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Original source nameUnknown/);
  assert.match(await page.locator('#animation-evidence-content').textContent(),/42 \/ 42 animated bone IDs matched/);
  await page.screenshot({path:path.join(scratch,'native-analysis.png')});
  report.initial={status:await page.locator('#analysis-status').textContent(),cache:first.cache,modelAssociations:first.asset.models.map(({label,kind,matchedBoneCount,animatedBoneCount})=>({label,kind,matchedBoneCount,animatedBoneCount})),sourceName:first.asset.sourceName,resource:first.asset.resource,previewPreserved:true};
  const cacheFile=first.cache.path,cacheBefore={sha:sha(cacheFile),mtime:fs.statSync(cacheFile).mtimeMs};
  await close();
  await launch();
  assert.equal(await page.locator('#export-c4d').isChecked(),false);
  assert.equal(await page.locator('#root-path').inputValue(),assets);
  await scan();
  // No Analyze click after restart: the existing disk cache is checked and
  // restored by the regular scan/applyCatalog path through actual native IPC.
  await page.waitForFunction(()=>!document.querySelector('#analysis-filters').hidden&&!document.querySelector('#analyze-library').disabled,undefined,{timeout:120000});
  const second=await detail();
  assert.equal(second.cache.revision,first.cache.revision);
  assert.equal(second.cache.analyzedAt,first.cache.analyzedAt);
  assert.deepEqual({sha:sha(cacheFile),mtime:fs.statSync(cacheFile).mtimeMs},cacheBefore,'Valid cache is reused without rewriting its analysis timestamp.');
  assert.equal(second.asset.resource.fullGuid,null);
  const wrongRoot=await page.evaluate(async({clip,scratch})=>window.pywebview.api.analysis_detail(clip,scratch),{clip,scratch});
  assert.equal(wrongRoot.ok,true);assert.equal(wrongRoot.data,null,'An overlapping or unrelated root cannot silently return another cache.');
  report.restart={validatedCacheReused:true,revision:second.cache.revision,analyzedAt:second.cache.analyzedAt,rootIsolation:true};
  await page.screenshot({path:path.join(scratch,'native-analysis-restored.png')});
  await close();
  assert.deepEqual({files:tree(assets),clip:sha(clip),entity:sha(entity)},before,'Read-only real asset tree is unchanged after a native restart.');
  assert.equal(fs.existsSync(path.join(scratch,'Unused copies')),false,'No export directory is created.');
  assert.deepEqual(errors,[]);
  report.sourceFilesUnchanged=before.files.length;
  report.errors=errors;report.passed=true;
  console.log(JSON.stringify({passed:true,scratch,initial:report.initial.status,restart:report.restart,sourceFilesUnchanged:report.sourceFilesUnchanged}));
})().catch(error=>{report.error=error.stack;console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  await application?.close();
});
