const {openAnimationSetup} = require('./open-animation-setup.cjs');
// Source renderer + real backend, parsers and persistent queue catalog.
// Assets are generated fixtures. Successful file export and rig bridge responses
// are simulated; the final failed run reaches the real disconnected bridge. This
// test does not create a Cinema 4D document or contact the user's bridge.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn, spawnSync} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');

const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/export-queue-ui-'));
const assets = path.join(scratch, 'Exports with spaces 日本語');
const profile = path.join(scratch, 'Profile');
const output = path.join(scratch, 'Saved animations 日本語');
const bridge = path.join(scratch, 'Disconnected Cinema bridge');
const entity = path.join(assets, 'Gameplay1P.owentity');
const armsA = path.join(assets, 'Animations', 'Arms_A.owanimclip');
const armsB = path.join(assets, 'Animations', 'Arms_B.owanimclip');
const gun = path.join(assets, 'Animations', 'Gun.owanimclip');
const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
const fixtureScript = path.join(scratch, 'make_fixture.py');
fs.writeFileSync(fixtureScript, `import sys
from pathlib import Path
sys.path[:0] = [str(Path(sys.argv[1]) / 'tests'), sys.argv[1]]
from test_formats import model_bytes, entity_bytes, clip_bytes
root = Path(sys.argv[2])
def write(name, data):
    target = root / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
write('Arms.owmdl', model_bytes(name='Arms'))
write('Gun.owmdl', model_bytes(name='Gun', bones=[('bone_0010', -1, (0, 0, 0), (0, 0, 0)), ('bone_0011', 0, (0, 1, 0), (0, 0, 0))]))
write('Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='Arms.owmdl', children=[('Gun.owentity', 'bone_0002')], relative='.'))
write('Gun.owentity', entity_bytes(name='Gun', model='Gun.owmdl', relative='.'))
write('Animations/Arms_A.owanimclip', clip_bytes())
write('Animations/Arms_B.owanimclip', clip_bytes(bones=[('bone_0002', [(0, [(0, (0, 2, 0)), (30, (0, 7, 0))])])]))
write('Animations/Gun.owanimclip', clip_bytes(bones=[('bone_0011', [(0, [(0, (0, 1, 0)), (90, (0, 3, 0))])])], fps=60, duration=91))
`);
const fixture = spawnSync(python, [fixtureScript, project, assets], {encoding:'utf8', windowsHide:true});
assert.equal(fixture.status, 0, fixture.stderr || fixture.stdout);
fs.mkdirSync(output);
fs.mkdirSync(bridge);
function filesBelow(folder) {
  if (!fs.existsSync(folder)) return [];
  return fs.readdirSync(folder, {withFileTypes:true}).flatMap(entry => {
    const file = path.join(folder, entry.name);
    return entry.isDirectory() ? filesBelow(file) : [file];
  });
}
const sourceHashes = () => Object.fromEntries(filesBelow(assets).map(file =>
  [file, crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')]));
const originals = sourceHashes();
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, description, timeout = 30000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await check()) return; await pause(40); }
  throw new Error('Timed out: ' + description);
}
let server, browser, serverLog = '', releaseExport, releaseRename;
const errors = [], saveRequests = [], runRequests = [], legacyExportRequests = [];
const report = {passed:false, scratch, syntheticAssets:true, realBackend:true,
  realQueuePersistence:true, mockedNativeSuccesses:2, nativeC4DRoundTrip:false};
async function startBackend() {
  server = spawn(python, [path.join(project, 'app.py'), '--serve', '--port', '0', '--data-dir', profile], {
    cwd:project, windowsHide:true, stdio:['ignore','pipe','pipe'],
    env:{...process.env, PYTHONDONTWRITEBYTECODE:'1', WHITEHOUND_C4D_QUEUE:bridge},
  });
  let launchLog = '';
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Backend startup: ' + launchLog)), 15000);
    server.once('error', error => {clearTimeout(timer); reject(error);});
    server.stderr.on('data', chunk => {serverLog += chunk; launchLog += chunk;});
    server.stdout.on('data', chunk => {
      serverLog += chunk; launchLog += chunk;
      const address = launchLog.match(/http:\/\/127\.0\.0\.1:\d+/);
      if (address) {clearTimeout(timer); resolve(address[0]);}
    });
  });
}
async function stopBackend() {
  if (!server || server.exitCode !== null) return;
  const owned = server;
  await new Promise(resolve => {owned.once('exit', resolve); owned.kill();});
  server = null;
}

(async () => {
  let address = await startBackend();
  browser = await chromium.launch({headless:true, args:['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport:{width:1540,height:980}});
  page.setDefaultTimeout(30000);
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {
    if (request.url().endsWith('/api/export_queue_save')) saveRequests.push(request.postDataJSON());
    if (/\/api\/export_animation(?:_set)?$/.test(request.url())) legacyExportRequests.push(request.postDataJSON());
  });
  let simulateSuccess = true, activeRuns = 0, maximumActiveRuns = 0;
  await page.route('**/api/export_queue_run', async route => {
    const args = route.request().postDataJSON(); runRequests.push(args);
    if (!simulateSuccess) return route.continue();
    activeRuns++; maximumActiveRuns = Math.max(maximumActiveRuns, activeRuns);
    if (runRequests.length === 1) await new Promise(resolve => {releaseExport = resolve;});
    const entries = JSON.parse(fs.readFileSync(path.join(profile, 'catalog.json'), 'utf8')).exportQueue;
    const saved = entries.find(entry => entry.id === args[0]);
    assert.ok(saved, 'Only a real, persisted queue snapshot can be exported.');
    const destination = path.join(args[1], saved.name + '.c4d');
    activeRuns--;
    await route.fulfill({json:{ok:true,data:{path:destination,c4dPath:destination,
      name:path.basename(destination),files:[destination],warnings:[],textureCount:0}}});
  });
  const ready = () => page.waitForFunction(() => !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  const getQueue = async () => {
    const result = await page.evaluate(() => window.pywebview.api.bootstrap());
    assert.equal(result.ok, true); return result.data.exportQueue;
  };
  const queueRow = id => page.locator(`.export-queue-row[data-export-id="${id}"]`);
  const selection = id => queueRow(id).locator('input.export-queue-check');
  const openDialog = async () => {
    await page.locator('#fbx-options-button').click();
    assert.equal(await page.locator('#export-menu').evaluate(dialog => dialog.open), true);
  };
  const closeDialog = () => page.locator('#close-export-menu').click();
  const choose = async (source, modelId) => {
    await openAnimationSetup(page);
    await page.locator('#rig-select').selectOption(modelId);
    await page.locator('#clip-search').fill(path.basename(source, '.owanimclip'));
    await page.locator('.clip-row').first().click();
    await page.waitForFunction(({source, modelId}) => window.qaViewport.assignments.some(entry =>
      entry.modelId === modelId && entry.clip.path === source) && document.querySelector('#viewport-loading').hidden,
    {source, modelId});
    if (await page.locator('#play-pause').getAttribute('aria-label') === 'Pause') await page.locator('#play-pause').click();
  };
  const add = async (name, count) => {
    await openAnimationSetup(page);
    await page.locator('#animation-set-name').fill(name);
    await page.locator('#export-animation-set').click();
    await page.waitForFunction(() => !document.querySelector('#export-animation-set').disabled
      && document.querySelector('#animation-set-export-status').textContent.startsWith('Added to export list'));
    const entries = await getQueue(); assert.equal(entries.length, count);
    assert.equal(await page.locator('#animation-set-name').inputValue(), '', 'Adding resets the nickname field without an entity prefix.');
    assert.equal(saveRequests.at(-1)[0].motionClipName, name, 'The captured API payload retains the exact nickname.');
    assert.equal(entries.at(-1).motionClipName, name, 'The persisted nickname is independent of the generated export filename.');
    assert.equal(runRequests.length, 0, 'Adding a snapshot does not launch native export.');
    return entries.at(-1);
  };

  await page.goto(address); await ready();
  assert.deepEqual(await page.evaluate(() => ['export_queue_save','export_queue_remove','export_queue_run']
    .map(method => typeof window.pywebview.api[method])), ['function','function','function']);
  assert.equal(await page.locator('#track-count-filter, #track-groups, #export-clip, #export-c4d, #clip-name, .annotation-fields, .source-bar .output-control').count(), 0);
  assert.equal(await page.locator('.library-search-control #clip-search').count(), 1);
  assert.equal(await page.locator('.library-search-control #clip-sort').count(), 1, 'Search and sort occupy one shared control group.');
  assert.equal(await page.locator('#export-menu #output-path').count(), 1, 'Animation output path belongs to the export dialog.');
  assert.equal(await page.locator('#toolchain-directories').isVisible(), false);
  assert.deepEqual(await getQueue(), []);
  await page.locator('#root-path').fill(assets); await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '3');
  await page.evaluate(async () => {
    const {PreviewViewport} = await import('./viewer.js');
    const load = PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset = function(...args) {window.qaViewport = this; return load.apply(this,args);};
  });
  await page.locator('#entity-select').selectOption(entity); await page.locator('#load-entity').click();
  await page.waitForFunction(() => window.qaViewport?.models.length === 2 && document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('#animation-set-name').inputValue(), '', 'Loading an entity starts with a blank nickname.');
  assert.equal(await page.locator('#animation-set-name').getAttribute('placeholder'), 'Animation nickname');
  await choose(armsA, 'model-0');
  await openAnimationSetup(page);
  await page.locator('#export-animation-set').click();
  assert.match(await page.locator('#animation-set-export-status').innerText(), /Enter a nickname/);
  assert.equal(saveRequests.length, 0, 'An empty nickname cannot create an export.');
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('Reload'); await page.locator('#animation-set-name').blur();
  await pause(800);
  assert.equal(saveRequests.length, 0, 'Name changes and blur do not add or export a setup.');
  assert.deepEqual(await getQueue(), []);
  assert.deepEqual(runRequests, []);
  const single = await add('Reload', 1);
  assert.equal(single.name, 'Gameplay1P_Reload');
  assert.equal(single.motionClipName, 'Reload');
  assert.equal(single.entityPath, entity);
  assert.equal(single.rootPath, assets);
  assert.deepEqual(single.assignments, [{modelId:'model-0',clipPath:armsA}],
    'A single assigned clip still captures the complete entity path, not a selected-object export.');
  assert.equal('selectedModelId' in single, false);
  assert.deepEqual(single.objectNames, {});
  const literalPrefix = await add('Gameplay1P_Reload', 2);
  assert.equal(literalPrefix.name, single.name, 'A literal prefix may produce the same output filename.');
  assert.equal(literalPrefix.motionClipName, 'Gameplay1P_Reload');
  assert.notEqual(literalPrefix.id, single.id, 'Distinct motion nicknames are not deduplicated solely by filename.');
  await openDialog();
  const rigTab = page.getByRole('tab', {name:'Stager rig', exact:true});
  const filesTab = page.getByRole('tab', {name:'Cinema export', exact:true});
  assert.equal(await rigTab.getAttribute('aria-selected'), 'true');
  assert.equal(await page.locator('#output-path').isVisible(), false);
  assert.equal(await page.locator('#create-stager-rig').isVisible(), true);
  assert.equal(await queueRow(single.id).locator('.export-queue-name').innerText(), 'Reload');
  await queueRow(literalPrefix.id).locator('.export-queue-remove').click();
  await page.waitForFunction(() => document.querySelectorAll('.export-queue-row').length === 1);
  assert.deepEqual(await getQueue(), [single], 'Removing a differently named motion snapshot keeps the original intact.');
  assert.equal(await selection(single.id).isChecked(), false);
  assert.match(await queueRow(single.id).innerText(), /Gameplay1P\.owentity · 1 assigned animation/);
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  await closeDialog();
  await choose(gun, 'model-1');
  assert.equal(await page.locator('#timeline').getAttribute('max'), '90', 'The combined preview retains the longest clip.');
  // Delay the real persistence request to exercise Add's pending-rename barrier.
  // The response still comes from the real backend and its isolated catalog.
  await page.route('**/api/save_object_name', async route => {
    await new Promise(resolve=>{releaseRename=resolve;});
    await route.continue();
  }, {times:1});
  if (!(await page.locator('#objects-list').isVisible())) await page.locator('#objects-menu > summary').click();
  const armsNode=page.locator('#objects-list [data-node-key="model:model-0"]');
  await armsNode.focus();await page.keyboard.press('F2');
  await page.locator('#objects-list .object-rename-input').fill('Arms 日本語');
  await page.locator('#objects-list .object-rename-input').press('Enter');
  await until(()=>Boolean(releaseRename),'held object-name save');
  const addingPair=add('ADS_Up_日本語',2);
  await page.waitForFunction(()=>document.querySelector('#export-animation-set').textContent==='Adding…');
  assert.equal(saveRequests.length,2,'A queue snapshot waits until the pending object name is persisted.');
  releaseRename();releaseRename=null;
  const pair = await addingPair;
  assert.equal(pair.name, 'Gameplay1P_ADS_Up_日本語');
  assert.equal(pair.motionClipName, 'ADS_Up_日本語', 'Nickname underscores and Unicode remain literal.');
  assert.deepEqual(pair.assignments, [{modelId:'model-0',clipPath:armsA},{modelId:'model-1',clipPath:gun}]);
  assert.deepEqual(pair.objectNames, {'model:model-0':'Arms 日本語'});
  await armsNode.focus();await page.keyboard.press('F2');
  await page.locator('#objects-list .object-rename-input').fill('Later arms');
  await page.locator('#objects-list .object-rename-input').press('Enter');
  await page.waitForFunction(()=>!document.querySelector('#objects-list .object-rename-input'));
  await choose(armsB, 'model-0');
  const changed = await add('Gameplay1P_changed', 3);
  assert.equal(changed.name, 'Gameplay1P_changed', 'An explicit entity prefix is not duplicated.');
  assert.equal(changed.motionClipName, 'Gameplay1P_changed', 'A deliberately typed prefix is part of the nickname.');
  assert.deepEqual(changed.assignments, [{modelId:'model-1',clipPath:gun},{modelId:'model-0',clipPath:armsB}]);
  assert.deepEqual(changed.objectNames, {'model:model-0':'Later arms'});
  assert.deepEqual((await getQueue())[1],pair,'Later object renaming cannot alter saved export labels.');
  assert.deepEqual((await getQueue())[0], single, 'Later browsing and assignments cannot mutate earlier snapshots.');
  await openDialog();
  assert.deepEqual(await page.locator('.export-queue-check').evaluateAll(checks => checks.map(check => check.checked)), [false,false,false]);
  await selection(single.id).focus();
  await page.keyboard.press('Space');
  assert.equal(await selection(single.id).isChecked(), true);
  assert.equal(await selection(single.id).evaluate(check => document.activeElement === check), true,
    'Selecting an export with Space keeps keyboard focus on the same checkbox.');
  assert.equal(await page.locator('#export-queue-summary').innerText(), '1 of 3 selected');
  const inserts = [];
  let legacyFailure = true;
  await page.route('**/api/stager_rig_destination', route => route.fulfill({json:{ok:true,data:{
    instance:'test-instance',documentId:'doc-id',documentName:'Watchpoint test.c4d',motionTargetName:'Joints',
    cameras:[{name:'POV Camera',id:'camera-id'}],rootMotionObjects:[{name:'Locomotion',id:'node-3'}],
    existingRigs:{actor:{needsRepair:true}},
  }}}));
  await page.route('**/api/create_stager_rig', route => {
    if (legacyFailure) {
      legacyFailure = false;
      return route.fulfill({json:{ok:false,error:'The registered rig or its Motion System target is missing. Restore it before importing.'}});
    }
    inserts.push(route.request().postDataJSON()[0]);
    return route.fulfill({json:{ok:true,data:{repairedRegistration:true,warnings:['Test import warning']}}});
  });
  await page.locator('#create-stager-rig').click();
  await page.waitForFunction(() => !document.querySelector('#stager-insert').disabled);
  assert.equal(await page.locator('#stager-document').innerText(), 'Watchpoint test.c4d');
  assert.equal(await page.locator('#stager-camera').inputValue(), 'camera-id');
  assert.equal(await page.getByLabel('Input for Reload').inputValue(), 'KeyR');
  await page.locator('#stager-kind').selectOption('actor');
  assert.equal(await page.locator('#stager-target').innerText(), 'Joints');
  assert.equal(await page.locator('#stager-insert').innerText(), 'Repair rig');
  assert.equal(await page.locator('#stager-root-motion, #stager-root-label').count(), 0);
  await page.getByLabel('Role for Reload').selectOption('idle');
  await page.locator('#stager-insert').click();
  await page.waitForFunction(() => document.querySelector('#stager-status').classList.contains('error'));
  assert.match(await page.locator('#stager-status').innerText(), /restore it before importing.*C4D Bridge 0\.7\.14.*Stager 0\.19\.1/i);
  await page.locator('#stager-insert').click();
  await page.waitForFunction(() => !document.querySelector('#stager-rig-dialog').open);
  assert.deepEqual(inserts, [{instance:'test-instance',documentId:'doc-id',kind:'actor',cameraId:'',
    rootMotionId:'',actions:[{id:single.id,role:'idle',binding:'',edge:'press'}]}]);
  assert.match(await page.locator('#export-queue-status').innerText(), /registration repaired in Watchpoint test\.c4d.*Refresh scene/);
  assert.equal(await page.locator('#stager-warnings').isVisible(), true);
  await rigTab.focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await filesTab.getAttribute('aria-selected'), 'true');
  assert.equal(await filesTab.evaluate(tab => document.activeElement === tab), true);
  assert.equal(await selection(single.id).isChecked(), true);
  assert.equal(await queueRow(single.id).locator('.export-queue-name').innerText(), 'Gameplay1P_Reload.c4d');
  assert.equal(await page.locator('#create-stager-rig').isVisible(), false);
  assert.equal(await page.locator('#stager-warnings').isVisible(), false);
  await page.keyboard.press('Home');
  assert.equal(await page.locator('#stager-warnings').isVisible(), true);
  await page.keyboard.press('End');
  assert.equal(await page.locator('#export-selected').isEnabled(), true);
  await page.locator('#output-path').fill(output); await page.locator('#output-path').blur();
  await page.waitForFunction(async expected => {const result = await window.pywebview.api.bootstrap();
    return result.ok && result.data.outputFolder === expected;}, output);
  await closeDialog();
  await page.locator('#tab-toolchain').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(), true);
  await page.locator('#tab-animations').click();
  await openDialog();
  assert.equal(await selection(single.id).isChecked(), true, 'Tab and dialog switches preserve this session selection.');
  await closeDialog();

  // Restart the actual server/profile, not just a mocked bootstrap response.
  await page.goto('about:blank'); await stopBackend(); address = await startBackend();
  await page.goto(address); await ready();
  assert.deepEqual(await getQueue(), [single,pair,changed], 'Every snapshot survives a real application backend restart.');
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(profile,'catalog.json'),'utf8')).exportQueue.map(entry => entry.motionClipName),
    ['Reload','ADS_Up_日本語','Gameplay1P_changed'], 'Exact nicknames survive persisted catalog storage and backend restart.');
  await openDialog();
  assert.deepEqual(await page.locator('.export-queue-check').evaluateAll(checks => checks.map(check => check.checked)), [false,false,false],
    'Restoring a queue does not silently opt saved entries into writing files.');
  assert.equal(await page.locator('#output-path').inputValue(), output);
  await filesTab.click();
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  await selection(single.id).check(); await selection(changed.id).check();
  await page.locator('#export-selected').click();
  await until(() => Boolean(releaseExport), 'first queued native response');
  const closing = await page.evaluate(() => window.whitehoundPrepareClose());
  assert.equal(closing.ok, false);
  assert.equal(closing.busy, true, 'Closing during a batch returns promptly and leaves the app usable.');
  assert.equal(await page.locator('#close-export-menu').isEnabled(), true);
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  assert.equal(await selection(pair.id).isDisabled(), true);
  assert.equal(await page.locator('#output-path').isDisabled(), true);
  assert.equal(await queueRow(single.id).locator('.export-queue-state').innerText(), 'Exporting…');
  await pause(250); assert.equal(runRequests.length, 1, 'A second job cannot begin before the first has completed.');
  assert.equal(runRequests[0][0], single.id);
  assert.equal(runRequests[0][1], output);
  assert.equal(runRequests[0][2].includeMesh, true);
  assert.equal(runRequests[0][2].bakeTextures, true);
  assert.equal(runRequests[0][2].rootMotion, true);
  assert.equal(runRequests[0][2].clipEnabled, false);
  assert.equal(runRequests[0][2].c4dEnabled, true);
  assert.equal('lockRoot' in runRequests[0][2], false);
  releaseExport(); releaseExport = null;
  await page.waitForFunction(() => document.querySelector('#export-queue-status').textContent === '2 completed.');
  assert.deepEqual(runRequests.map(args => args[0]), [single.id,changed.id], 'Only checked entries are sent, in queue order.');
  assert.equal(maximumActiveRuns, 1);
  assert.equal(await queueRow(single.id).locator('.export-queue-state').innerText(), 'Completed');
  assert.equal(await queueRow(changed.id).locator('.export-queue-state').innerText(), 'Completed');
  assert.equal(await queueRow(pair.id).locator('.export-queue-state').innerText(), 'Ready');
  assert.equal(await selection(single.id).isChecked(), false);
  assert.equal(await selection(changed.id).isChecked(), false);
  assert.equal(filesBelow(output).length, 0, 'Successful native responses above were explicitly simulated; no output file is claimed.');

  simulateSuccess = false;
  await selection(pair.id).check(); await page.locator('#export-selected').click();
  await page.waitForFunction(() => document.querySelector('#export-queue-status').textContent.startsWith('0 completed · 1 failed.'));
  assert.equal(runRequests.at(-1)[0], pair.id);
  assert.equal(await queueRow(pair.id).locator('.export-queue-state').innerText(), 'Failed');
  assert.match(await page.locator('#export-queue-status').innerText(), /Cinema 4D|Avolition C4D Bridge/);
  assert.equal(await selection(pair.id).isChecked(), true, 'Failed work remains selected for an explicit retry.');
  assert.equal(await page.locator('#export-selected').isDisabled(), false);
  assert.equal(filesBelow(output).filter(file => /\.(c4d|owanimclip|fbx)$/i.test(file)).length, 0);
  assert.equal(filesBelow(bridge).filter(file => /request\.json$/.test(file)).length, 0, 'No job was sent to a native Cinema instance.');
  await page.screenshot({path:path.join(scratch,'export-queue-failure.png')});
  await queueRow(pair.id).locator('.export-queue-remove').click();
  await page.waitForFunction(() => document.querySelectorAll('.export-queue-row').length === 2);
  assert.deepEqual(await getQueue(), [single,changed], 'Removing an entry changes the real saved queue.');
  assert.deepEqual(legacyExportRequests, [], 'The renderer never invokes legacy auto-export APIs.');
  assert.deepEqual(sourceHashes(), originals, 'Naming, queueing, previewing and failed export leave source bytes unchanged.');
  assert.deepEqual(errors, []);
  Object.assign(report, {passed:true,saveRequests:saveRequests.length,runRequests:runRequests.length,
    wholeEntitySnapshots:true,singleAndMultipleAssignments:true,noNameBlurExport:true,
    immutableSnapshots:true,objectNamesCaptured:true,awaitsPendingRename:true,realRestartPersistence:true,initialAndRestoredUnchecked:true,
    selectedOnlySequential:true,maximumActiveRuns,honestDisconnectedBridgeFailure:true,
    removalPersisted:true,sourceFilesUnchanged:true,oldControlsRemoved:true,searchSortSharedGroup:true,
    nicknameStartsAndResetsBlank:true,exactMotionClipNames:['Reload','ADS_Up_日本語','Gameplay1P_changed'],
    motionClipNameIndependentOfFilename:true,distinctNicknamesSharingFilenamePreserved:true});
  console.log(JSON.stringify(report));
})().catch(error => {
  report.error = String(error.stack || error); report.serverLog = serverLog;
  console.error(error); process.exitCode = 1;
}).finally(async () => {
  releaseExport?.();
  releaseRename?.();
  fs.writeFileSync(path.join(scratch,'report.json'), JSON.stringify(report,null,2));
  await browser?.close(); await stopBackend();
});
