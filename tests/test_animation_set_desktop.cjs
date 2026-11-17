const {openAnimationSetup} = require('./open-animation-setup.cjs');
// Packaged Electron, preload IPC, compiled Python backend and real binary
// fixtures. An isolated empty bridge queue makes Cinema deterministically
// unavailable. No native export response or backend API is mocked.
const {_electron} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
const build = path.resolve(__dirname, '../../preview-build');
const project = path.resolve(__dirname, '..');
const executable = path.resolve(process.env.WHITEHOUND_EXE || path.join(build, 'avolition-v0811/dist/Avolition/Avolition.exe'));
const scratch = fs.mkdtempSync(path.join(build, 'animation-set-desktop-'));
const assets = path.join(scratch, 'Fixtures 日本語');
const profile = path.join(scratch, 'Profile');
const output = path.join(scratch, 'Combined output 日本語');
const bridgeQueue = path.join(scratch, 'Empty Cinema bridge');
const entity = path.join(assets, 'Gameplay1P.owentity');
const arms = path.join(assets, 'Animations', 'Arms.owanimclip');
const gun = path.join(assets, 'Animations', 'Weapon.owanimclip');
const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
const fixtureScript = path.join(scratch, 'make_fixture.py');
fs.writeFileSync(fixtureScript, `import sys
from pathlib import Path
sys.path[:0] = [str(Path(sys.argv[1]) / 'tests'), sys.argv[1]]
from test_formats import model_bytes, entity_bytes, clip_bytes
root = Path(sys.argv[2])
def write(name, data):
    p = root / name
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_bytes(data)
write('Arms.owmdl', model_bytes(name='Arms'))
write('Weapon.owmdl', model_bytes(name='Weapon'))
write('Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='Arms.owmdl', children=[('Weapon.owentity', 'bone_0002')], relative='.'))
write('Weapon.owentity', entity_bytes(name='Weapon', model='Weapon.owmdl', relative='.'))
write('Animations/Arms.owanimclip', clip_bytes(duration=16, fps=30, bones=[
    ('bone_0002', [(0, [(0, (0,2,0)), (15, (0,3,0))])]),
    ('bone_FFFF', [(0, [(0, (0,0,0))])])]))
write('Animations/Weapon.owanimclip', clip_bytes(duration=61, fps=60, bones=[
    ('bone_0001', [(0, [(0, (0,0,0)), (60, (1,0,0))])])]))
`);
const fixture = spawnSync(python, [fixtureScript, project, assets], {encoding:'utf8', windowsHide:true});
assert.equal(fixture.status, 0, fixture.stderr || fixture.stdout);
let application, page;
const errors = [];
const report = {passed:false, executable, scratch, hidden:true, qaOnlyNoSandbox:true,
  syntheticAssets:true, mockedBackend:false, nativeC4DExport:false, isolatedDisconnectedBridge:true};

(async()=>{
  application=await _electron.launch({executablePath:executable,
    args:['--no-sandbox','--test-hidden','--data-dir',profile],
    env:{...process.env,WHITEHOUND_C4D_QUEUE:bridgeQueue},timeout:45000});
  page=await application.firstWindow({timeout:45000});
  page.setDefaultTimeout(25000);
  page.on('pageerror',error=>errors.push(error.message));
  await application.evaluate(({BrowserWindow,dialog})=>{
    const window=BrowserWindow.getAllWindows()[0];
    window.hide();window.setSkipTaskbar(true);window.webContents.setBackgroundThrottling(false);
    dialog.showMessageBox=async()=>({response:0,checkboxChecked:false});
  });
  await page.waitForFunction(()=>window.pywebview?.api && !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  const native=await application.evaluate(({BrowserWindow})=>{
    const window=BrowserWindow.getAllWindows()[0],prefs=window.webContents.getLastWebPreferences();
    return{visible:window.isVisible(),url:window.webContents.getURL(),sandbox:prefs.sandbox,
      contextIsolation:prefs.contextIsolation,nodeIntegration:prefs.nodeIntegration};
  });
  assert.deepEqual(native,{visible:false,url:'whitehound://app/index.html',sandbox:true,contextIsolation:true,nodeIntegration:false});
  assert.equal(await page.evaluate(()=>typeof window.pywebview.api.export_animation_set),'function','Combined export is exposed by the packaged preload allowlist.');
  for(const method of ['export_queue_save','export_queue_run','export_queue_remove'])
    assert.equal(await page.evaluate(method=>typeof window.pywebview.api[method],method),'function',`${method} is exposed by the packaged preload allowlist.`);
  assert.equal(await page.locator('#animation-evidence, #analyze-library, #analysis-filters').count(),0);
  assert.equal(await page.locator('#clip-name, #export-clip, #export-c4d, #track-groups, #track-count-filter').count(),0);
  assert.equal(await page.locator('#fbx-options-button').isEnabled(),true,'The export list is available before loading a model.');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  await page.locator('#tab-toolchain').dispatchEvent('click');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true);
  await page.locator('#tab-animations').dispatchEvent('click');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  await page.locator('#root-path').fill(assets);await page.locator('#scan-root').dispatchEvent('click');
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='2');
  await page.locator('#fbx-options-button').dispatchEvent('click');
  await page.locator('#export-files-tab').click();
  await page.locator('#output-path').fill(output);await page.locator('#output-path').press('Enter');
  await page.waitForFunction(()=>document.querySelector('#output-help').hidden);
  await page.locator('#close-export-menu').dispatchEvent('click');
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js'),load=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;return load.apply(this,args);};
  });
  await page.locator('#entity-select').selectOption(entity);await page.locator('#load-entity').dispatchEvent('click');
  await page.waitForFunction(()=>window.qaViewport?.models.length===2 && document.querySelector('#viewport-loading').hidden);
  await page.locator('.clip-row').filter({hasText:'Arms'}).dispatchEvent('click');
  await page.waitForFunction(()=>window.qaViewport.assignments.length===1 && document.querySelector('#viewport-loading').hidden);
  await page.locator('#clip-details > summary').click();
  assert.match(await page.locator('#match-message').innerText(),/1 \/ 2 tracks matched/,'Partial matching remains accepted in the native renderer.');
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-1');
  await page.locator('.clip-row').filter({hasText:'Weapon'}).dispatchEvent('click');
  await page.waitForFunction(()=>window.qaViewport.assignments.length===2 && document.querySelector('#viewport-loading').hidden);
  if(await page.locator('#play-pause').getAttribute('aria-label')==='Pause')await page.locator('#play-pause').dispatchEvent('click');
  assert.equal(await page.locator('#animation-set .animation-set-row').count(),2);
  for(const modelId of ['model-0','model-1']){
    await openAnimationSetup(page);
    await page.locator('#rig-select').selectOption(modelId);
    assert.equal(await page.locator('#timeline').getAttribute('max'),'60');
    assert.equal(await page.locator('#total-time').innerText(),'1.00 s');
    await page.locator('#timeline').fill('45');
    const error=await page.evaluate(()=>{
      let worst=0;
      for(const model of window.qaViewport.models){
        const clip=model.assignedClip,frame=Math.min(clip.lastFrame,45/60*clip.fps);
        const pose=model.evaluator.evaluate(frame);
        model.bones.forEach((bone,index)=>bone.matrix.elements.forEach((value,k)=>{
          worst=Math.max(worst,Math.abs(value-pose.localMatrices[index][k]));
        }));
      }
      return worst;
    });
    assert.ok(error<1e-8,'The packaged viewport samples mixed FPS in seconds and holds the short clip.');
  }
  const assignments=[{modelId:'model-0',clipPath:arms},{modelId:'model-1',clipPath:gun}];
  const ipc=await page.evaluate(async({entity,assignments,output,assets})=>{
    const api=window.pywebview.api;
    return{status:await api.c4d_status(),
      result:await api.export_animation_set(entity,assignments,'Native combined 日本語',output,
        {rootPath:assets,includeMesh:true,bakeTextures:false,rootMotion:true}),
      invalid:await api.export_animation_set(entity,[assignments[0],assignments[0]],'Invalid duplicate',output,
        {rootPath:assets,includeMesh:false,bakeTextures:false,rootMotion:true})};
  },{entity,assignments,output,assets});
  assert.equal(ipc.status.ok,true);assert.equal(ipc.status.data.connected,false);
  assert.equal(ipc.result.ok,false);
  assert.match(ipc.result.error,/Avolition C4D Bridge/,'Real valid combined IPC reaches the native bridge availability check.');
  assert.doesNotMatch(ipc.result.error,/unknown.*method|not allowed|unsupported.*method/i);
  assert.equal(ipc.invalid.ok,false);assert.match(ipc.invalid.error,/unique animation assignment/);
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('Native UI combined 日本語');
  await page.locator('#export-animation-set').dispatchEvent('click');
  await page.waitForFunction(()=>document.querySelector('#animation-set-export-status').textContent.startsWith('Added to')
    && !document.querySelector('#export-animation-set').disabled);
  const queued=await page.evaluate(async()=>(await window.pywebview.api.bootstrap()).data.exportQueue);
  assert.equal(queued.length,1);assert.equal(queued[0].name,'Gameplay1P_Native UI combined 日本語');
  assert.equal(queued[0].motionClipName,'Native UI combined 日本語','Combined rigs retain one exact motion nickname.');
  assert.equal(queued[0].entityPath,entity);assert.equal(queued[0].rootPath,assets);
  assert.deepEqual(queued[0].assignments,assignments,'The native queue stores both object animations and their source paths.');
  assert.equal(await page.locator('#animation-set-name').inputValue(),'','Adding resets the nickname field to blank.');
  const capture=await application.evaluate(async({BrowserWindow})=>(await BrowserWindow.getAllWindows()[0].webContents.capturePage()).toPNG().toString('base64'));
  fs.writeFileSync(path.join(scratch,'native-animation-set.png'),Buffer.from(capture,'base64'));
  await page.locator('#fbx-options-button').dispatchEvent('click');
  assert.equal(await page.locator('#export-menu').isVisible(),true);
  assert.equal(await page.locator('#export-queue-list .export-queue-row').count(),1);
  assert.equal(await page.locator('.export-queue-check').isChecked(),false,'Adding a setup does not select or export it.');
  assert.equal(await page.locator('#export-selected').isDisabled(),true);
  assert.equal(await page.locator('.export-queue-state').innerText(),'Ready');
  assert.equal(fs.existsSync(output)&&fs.readdirSync(output).some(name=>/\.(?:c4d|owanimclip|fbx)$/i.test(name)),false);
  await page.locator('.export-queue-check').dispatchEvent('click');
  assert.equal(await page.locator('#export-queue-summary').innerText(),'1 of 1 selected');
  await page.locator('#export-selected').dispatchEvent('click');
  await page.waitForFunction(()=>document.querySelector('.export-queue-state').textContent==='Failed'
    && !document.querySelector('#export-selected').disabled);
  assert.match(await page.locator('.export-queue-state').getAttribute('title'),/Avolition C4D Bridge/);
  assert.match(await page.locator('#export-queue-status').innerText(),/0 completed.*1 failed.*Avolition C4D Bridge/);
  assert.equal(await page.locator('.export-queue-check').isChecked(),true,'A failed entry remains selected for retry.');
  assert.equal(await page.locator('.export-queue-name').innerText(),'Gameplay1P_Native UI combined 日本語.c4d');
  assert.equal(await page.locator('#animation-set .animation-set-row').count(),2,'Failed native export keeps assignments for retry.');
  assert.equal(fs.existsSync(output)&&fs.readdirSync(output).some(name=>/\.c4d$/i.test(name)),false,'No scene is claimed or saved without Cinema.');
  assert.equal(fs.existsSync(path.join(bridgeQueue,'jobs')),false,'Disconnected check launches no bridge jobs.');
  const failedCapture=await application.evaluate(async({BrowserWindow})=>(await BrowserWindow.getAllWindows()[0].webContents.capturePage()).toPNG().toString('base64'));
  fs.writeFileSync(path.join(scratch,'native-export-queue-failed.png'),Buffer.from(failedCapture,'base64'));
  await page.locator('#close-export-menu').dispatchEvent('click');
  const closed=application.waitForEvent('close',{timeout:25000});
  await application.evaluate(({BrowserWindow})=>{BrowserWindow.getAllWindows()[0].close();});
  await closed;application=null;
  assert.deepEqual(errors,[]);
  Object.assign(report,{passed:true,nativeWindow:native,partialMatchAccepted:true,mixedFpsClock:true,shortClipHolds:true,
    timelineFrames:61,packagedCombinedIPC:true,bridgeError:ipc.result.error,duplicateAssignmentRejected:true,
    failedExportPreservesAssignments:true,packagedQueueIPC:true,queueStartsUnchecked:true,queueCapturesWholeEntity:true,
    failedEntryRetainedForRetry:true,noImplicitExports:true,noSceneSaved:true,cleanNativeClose:true});
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=String(error.stack||error);console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  await application?.close();
});
