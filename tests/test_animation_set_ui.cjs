// Production renderer/backend on real, read-only Widowmaker files. Only native
// Cinema 4D export responses are mocked; this is not a native C4D round trip.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');

const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/animation-set-ui-'));
const assets = process.env.WHITEHOUND_REAL_ASSET_ROOT || 'C:\\Users\\Admin\\Desktop\\Overwatch Tool\\Whitehound\\library\\Heroes\\Widowmaker\\Skin\\Overwatch\\Overwatch Classic';
const entity = path.join(assets, 'Entities', 'Gameplay1P', 'Gameplay1P.owentity');
const arms = path.join(assets, 'Models', '00000000013E.00C', 'Animations', '0', '32', '000000000156.owanimclip');
const gun = path.join(assets, 'Models', '0000000001D4.00C', 'Animations', '0', '4', '000000003879.owanimclip');
const output = path.join(scratch, 'Combined exports 日本語');
const hash = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const unchanged = [entity, arms, gun].map(file => ({file, hash:hash(file)}));
const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
const server = spawn(python, [path.join(project, 'app.py'), '--serve', '--port', '0', '--data-dir', path.join(scratch, 'Profile')],
  {cwd:project, windowsHide:true, stdio:['ignore','pipe','pipe'], env:{...process.env, PYTHONDONTWRITEBYTECODE:'1'}});
let browser, log = '', releaseExport = null, releaseClip = null;
const errors = [], combinedRequests = [], legacyRequests = [];
const report = {passed:false, scratch, realAssets:assets, externalC4DSimulated:true, nativeC4DRoundTrip:false};
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, description, ms=30000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { if (await check()) return; await pause(50); }
  throw new Error('Timed out: '+description);
}
(async () => {
  const address = await new Promise((resolve,reject) => {
    const timer=setTimeout(()=>reject(new Error('Server startup: '+log)),15000);
    server.once('error',error=>{clearTimeout(timer);reject(error);});
    server.stderr.on('data',chunk=>{log+=chunk;});
    server.stdout.on('data',chunk=>{log+=chunk;const match=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(match){clearTimeout(timer);resolve(match[0]);}});
  });
  browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page=await browser.newPage({viewport:{width:1600,height:1000}});
  page.setDefaultTimeout(30000);
  page.on('pageerror',error=>errors.push(error.message));
  let holdExport=true, holdClip='';
  await page.route('**/api/export_queue_run',async route=>{
    const args=route.request().postDataJSON();combinedRequests.push(args);
    if(holdExport){holdExport=false;await new Promise(resolve=>{releaseExport=resolve;});}
    const catalog=JSON.parse(fs.readFileSync(path.join(scratch,'Profile','catalog.json'),'utf8'));
    const entry=catalog.exportQueue.find(entry=>entry.id===args[0]);
    assert.ok(entry);
    const destination=path.join(args[1],entry.name+'.c4d');
    await route.fulfill({json:{ok:true,data:{path:destination,c4dPath:destination,name:path.basename(destination),files:[destination],warnings:[],textureCount:0}}});
  });
  page.on('request',request=>{
    if(/\/api\/export_animation(?:_set)?$/.test(request.url()))legacyRequests.push(request.postDataJSON());
  });
  await page.route('**/api/load_animation',async route=>{
    if(route.request().postDataJSON()[0]===holdClip){
      holdClip='';const response=await route.fetch();
      await new Promise(resolve=>{releaseClip=resolve;});await route.fulfill({response});
    }else await route.continue();
  });
  await page.goto(address);
  await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled);
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false,'Game installation/output controls belong only to Toolchain.');
  assert.equal(await page.locator('#animation-evidence, #analyze-library, #analysis-filters').count(),0,'Evidence controls are removed.');
  await page.locator('#tab-toolchain').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true);
  await page.locator('#overwatch-install').fill('D:\\Game path 日本語');
  await page.locator('#overwatch-install').blur();
  await page.locator('#tab-animations').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  assert.equal(await page.locator('#export-c4d, #export-clip, #clip-name, #track-count-filter, #track-groups').count(),0);
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').click();
  await page.waitForFunction(()=>document.querySelectorAll('#entity-select option').length>1 && !document.querySelector('#scan-root').disabled);
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js');
    const load=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;return load.apply(this,args);};
  });
  await page.locator('#entity-select').selectOption(entity);await page.locator('#load-entity').click();
  await page.waitForFunction(()=>window.qaViewport?.models.length && document.querySelector('#viewport-loading').hidden);
  const choose=async(source, modelId, match)=>{
    await page.locator('#rig-select').selectOption(modelId);
    await page.locator('#clip-search').fill(path.basename(source,'.owanimclip'));
    await page.locator('.clip-row').first().click();
    await page.waitForFunction(({modelId,source})=>window.qaViewport.assignments.some(entry=>entry.modelId===modelId&&entry.clip.path===source)&&document.querySelector('#viewport-loading').hidden,{modelId,source});
    if(await page.locator('#play-pause').getAttribute('aria-label')==='Pause')await page.locator('#play-pause').click();
    assert.match(await page.locator('#match-message').innerText(),match);
  };
  await choose(arms,'model-0',/85 \/ 93 tracks matched/);
  await choose(gun,'model-1',/18 \/ 19 tracks matched/);
  assert.match(await page.locator('#animation-set').innerText(),/000000000156/);
  assert.match(await page.locator('#animation-set').innerText(),/000000003879/);
  const assigned=()=>page.evaluate(()=>window.qaViewport.assignments.map(entry=>({modelId:entry.modelId,path:entry.clip.path})));
  const expected=[{modelId:'model-0',path:arms},{modelId:'model-1',path:gun}];
  assert.deepEqual(await assigned(),expected);
  for(const modelId of ['model-0','model-1']){
    await page.locator('#rig-select').selectOption(modelId);
    assert.equal(await page.locator('#timeline').getAttribute('max'),'45','Longer gun clip remains reachable while browsing arms.');
    assert.equal(await page.locator('#total-time').innerText(),'1.50 s');
    await page.locator('#timeline').fill('45');
    const error=await page.evaluate(()=>{
      let worst=0;
      for(const model of window.qaViewport.models){
        if(!model.assignedClip)continue;
        const clip=model.assignedClip;
        const wanted=model.evaluator.evaluate(Math.min(45,clip.lastFrame));
        model.bones.forEach((bone,index)=>bone.matrix.elements.forEach((value,k)=>{worst=Math.max(worst,Math.abs(value-wanted.localMatrices[index][k]));}));
      }
      return worst;
    });
    assert.ok(error<1e-8,'Short arms hold frame21; gun reaches frame45.');
  }
  await page.locator('#animation-set-name').fill('matched_reload 日本語');
  await page.locator('#export-animation-set').click();
  await page.waitForFunction(()=>!document.querySelector('#export-animation-set').disabled
    &&document.querySelector('#animation-set-export-status').textContent.startsWith('Added to export list'));
  assert.equal(combinedRequests.length,0,'Adding a setup only saves its queue snapshot.');
  const savedQueue=await page.evaluate(()=>window.pywebview.api.bootstrap());
  assert.equal(savedQueue.ok,true);assert.equal(savedQueue.data.exportQueue.length,1);
  const entry=savedQueue.data.exportQueue[0];
  assert.equal(entry.entityPath,entity);assert.equal(entry.rootPath,assets);
  assert.equal(entry.name,'Gameplay1P_matched_reload 日本語');
  assert.deepEqual(entry.assignments,[{modelId:'model-0',clipPath:arms},{modelId:'model-1',clipPath:gun}]);
  await page.locator('#fbx-options-button').click();
  const queued=page.locator(`.export-queue-row[data-export-id="${entry.id}"]`);
  assert.equal(await queued.locator('.export-queue-check').isChecked(),false);
  await page.locator('#output-path').fill(output);await page.locator('#output-path').blur();
  await queued.locator('.export-queue-check').check();
  await page.locator('#export-selected').click();
  await until(()=>Boolean(releaseExport),'combined export started');
  assert.equal(await page.locator('#export-selected').isDisabled(),true,'Prevent duplicate native jobs.');
  assert.equal(combinedRequests.length,1);
  const request=combinedRequests[0];
  assert.equal(request[0],entry.id);assert.equal(request[1],output);
  assert.equal(request[2].rootMotion,true);assert.equal(request[2].includeMesh,true);assert.equal(request[2].bakeTextures,true);
  assert.equal('lockRoot' in request[2],false,'Preview in-place is never sent as export root-motion configuration.');
  await page.locator('#close-export-menu').click();
  await page.locator('#tab-toolchain').click();
  assert.equal(await page.locator('#overwatch-install').inputValue(),'D:\\Game path 日本語');
  releaseExport();releaseExport=null;
  await page.locator('#tab-animations').click();
  await page.waitForFunction(()=>!document.querySelector('#export-animation-set').disabled);
  await page.locator('#fbx-options-button').click();
  assert.equal(await queued.locator('.export-queue-state').innerText(),'Completed');
  assert.match(await queued.locator('.export-queue-state').getAttribute('title'),/\.c4d/);
  assert.equal(await queued.locator('.export-queue-check').isChecked(),false);
  await page.locator('#close-export-menu').click();
  assert.deepEqual(await assigned(),expected,'Tab switching and export retain both assignments.');

  await page.locator('#animation-set-name').fill('not_added');await page.locator('#animation-set-name').blur();
  await pause(700);
  assert.equal(combinedRequests.length,1,'Editing a name does not implicitly export it.');
  const unchangedQueue=await page.evaluate(()=>window.pywebview.api.bootstrap());
  assert.deepEqual(unchangedQueue.data.exportQueue,[entry]);
  await page.locator('#rig-select').selectOption('model-0');
  holdClip=arms;
  await page.locator('#clip-search').fill('000000000156');
  await page.locator('.clip-row').first().click();
  await until(()=>Boolean(releaseClip),'in-flight assigned clip');
  assert.equal(await page.locator('#export-animation-set').isDisabled(),true,'Do not export an incomplete assignment while a clip is loading.');
  releaseClip();releaseClip=null;
  await page.waitForFunction(()=>document.querySelector('#viewport-loading').hidden&&!document.querySelector('#export-animation-set').disabled);
  assert.deepEqual(await assigned(),[{modelId:'model-1',path:gun},{modelId:'model-0',path:arms}]);
  await page.screenshot({path:path.join(scratch,'animation-set-real-widowmaker.png')});
  for(const source of unchanged)assert.equal(hash(source.file),source.hash,'Source assets remain unchanged.');
  assert.deepEqual(errors,[]);assert.deepEqual(legacyRequests,[]);
  Object.assign(report,{passed:true,partialMatches:[85,18],timelineFrames:46,shortClipHolds:true,toolchainOnlyDirectories:true,
    queuedSnapshot:entry,combinedRequests:combinedRequests.map(args=>({id:args[0],output:args[1],options:args[2]})),
    noImplicitExport:true,queuedExport:true,readOnlyAssets:true});
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=error.stack;console.error(error);process.exitCode=1;}).finally(async()=>{
  releaseExport?.();releaseClip?.();
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  if(browser)await browser.close();server.kill();
});
