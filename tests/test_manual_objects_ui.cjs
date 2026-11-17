const {openAnimationSetup} = require('./open-animation-setup.cjs');
// Real renderer + source backend. Fixture files, indexing, parsing, annotation
// saves and named export-list snapshots exercise production code. No export is
// requested; unexpected export calls fail the test. All data is isolated.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn, spawnSync} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/manual-objects-ui-'));
const assets = path.join(scratch, 'Exports');
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
write('00000000013E.owmdl', model_bytes(name='00000000013E.00C'))
write('0000000001D4.owmdl', model_bytes(name='0000000001D4.00C'))
write('Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='00000000013E.owmdl', children=[('Gun.owentity', 'bone_0002')], relative='.'))
write('Gun.owentity', entity_bytes(name='Gun', model='0000000001D4.owmdl', relative='.'))
wave = clip_bytes(duration=91)
turn = clip_bytes(bones=[('bone_0001', [(0, [(0,(0,0,0)), (90,(3,0,0))])])], duration=91)
root_clip = clip_bytes(bones=[('bone_0001', [(0, [(0,(1,0,0)), (90,(2,0,0))])]), ('bone_0002', [(0, [(0,(0,2,0)), (90,(0,3,0))])])], duration=91)
write('Animations/001_Wave.owanimclip', wave)
write('Animations/002_Turn.owanimclip', turn)
write('Animations/003_Root.owanimclip', root_clip)
write('Skins/Copy/001_Wave.owanimclip', wave)
write('Skins/Variant/001_Wave.owanimclip', clip_bytes(duration=91, bones=[('bone_0002', [(0, [(0,(0,2,0)), (90,(0,7,0))])])]))
`);
const fixture = spawnSync(python, [fixtureScript, project, assets], {encoding:'utf8', windowsHide:true});
assert.equal(fixture.status, 0, fixture.stderr || fixture.stdout);
const source = name => path.join(assets, 'Animations', name + '.owanimclip');
const server = spawn(python, [path.join(project, 'app.py'), '--serve', '--port', '0', '--data-dir', path.join(scratch, 'Profile')],
  {cwd:project, windowsHide:true, stdio:['ignore','pipe','pipe'], env:{...process.env, PYTHONDONTWRITEBYTECODE:'1'}});
let browser, log = '', releaseClip = null;
const errors = [], sceneRequests = [], queueRequests = [];
let savedEntries = [];
const report = {passed:false, scratch, nativeExportRequested:false};
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, description, milliseconds=20000) {
  const end = Date.now() + milliseconds;
  while (Date.now() < end) { if (await check()) return; await pause(50); }
  throw new Error('Timed out: ' + description);
}

(async () => {
  const address = await new Promise((resolve,reject) => {
    const timer = setTimeout(() => reject(new Error('Server startup timed out: '+log)), 15000);
    server.once('error', error => { clearTimeout(timer); reject(error); });
    server.stderr.on('data', chunk => { log += chunk; });
    server.stdout.on('data', chunk => { log += chunk; const match=log.match(/http:\/\/127\.0\.0\.1:\d+/); if(match) {clearTimeout(timer);resolve(match[0]);} });
  });
  browser = await chromium.launch({headless:true, args:['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport:{width:1440,height:940}});
  page.setDefaultTimeout(20000);
  page.on('pageerror', error => errors.push(error.message));
  let holdClipPath = '';
  await page.route('**/api/load_animation', async route => {
    if (route.request().postDataJSON()[0] === holdClipPath) {
      holdClipPath = '';
      const response = await route.fetch();
      await new Promise(resolve => {releaseClip = resolve;});
      await route.fulfill({response});
    } else await route.continue();
  });
  await page.route('**/api/export_queue_save', async route => {
    queueRequests.push(route.request().postDataJSON()[0]);
    const response = await route.fetch(), result = await response.json();
    if (result.ok) savedEntries = result.data.entries;
    await route.fulfill({response});
  });
  await page.route(/\/api\/(export_animation|export_animation_set|export_queue_run)$/, async route => {
    sceneRequests.push({url:route.request().url(), args:route.request().postDataJSON()});
    await route.fulfill({json:{ok:false, error:'This test must not request an export.'}});
  });
  await page.goto(address);
  await page.waitForFunction(() => !document.querySelector('#scan-root').disabled && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  assert.equal(await page.title(),'Avolition');
  assert.equal(await page.locator('.inspector-panel #objects-menu').isVisible(),true);
  await page.locator('#objects-menu > summary').click();
  assert.equal(await page.locator('#objects-button, #objects-menu[popover]').count(),0);
  assert.equal(await page.locator('#hide-duplicates').isChecked(), true);
  assert.equal(await page.locator('#pairing-panel').count(), 0, 'The fork contains no automatic pairing UI.');
  assert.equal(await page.locator('#export-c4d, #export-clip, #clip-name').count(), 0, 'Legacy automatic/raw export controls are absent.');
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '4');
  assert.equal(await page.locator('.clip-row').count(), 4);
  assert.equal(await page.locator('.clip-name').filter({hasText:/^001_Wave$/}).count(), 2, 'Same filename with different bytes must remain visible.');
  assert.match(await page.locator('#library-summary').innerText(), /1 duplicates hidden/);
  await page.locator('#library-filters > summary').click();
  await page.locator('#hide-duplicates').uncheck();
  assert.equal(await page.locator('.clip-row').count(), 5);
  await page.locator('#hide-duplicates').check();
  assert.equal(await page.locator('.clip-row').count(), 4);
  report.duplicates = {files:5, unique:4, differentContentPreserved:true};

  // Capture the actual production viewport instance without altering its logic.
  await page.evaluate(async () => {
    const {PreviewViewport} = await import('./viewer.js');
    const load = PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset = function(...args) {window.qaViewport = this; window.qaLoadCount=(window.qaLoadCount||0)+1; return load.apply(this,args);};
  });
  await page.locator('#entity-select').selectOption(path.join(assets,'Gameplay1P.owentity'));
  await page.locator('#load-entity').click();
  await page.waitForFunction(() => window.qaViewport?.models.length === 2 && document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('#rig-select').inputValue(), 'model-0');
  assert.equal(await page.locator('#rig-select option').count(), 2, 'Animation targets are explicit model rigs.');
  assert.equal(await page.locator('#timeline').isDisabled(), true);
  const chooseClip = async relative => {
    await page.evaluate(relative => {
      const row = [...document.querySelectorAll('.clip-row')].find(row => row.title.replaceAll('\\','/').split('\n')[0] === relative);
      if (!row) throw new Error('No row: '+relative);
      row.click();
    }, relative);
    await page.waitForFunction(() => !document.querySelector('#timeline').disabled && document.querySelector('#viewport-loading').hidden);
  };
  const assignments = () => page.evaluate(() => window.qaViewport.assignments.map(({modelId,clip}) => ({modelId,name:clip.name})));
  await chooseClip('Animations/001_Wave.owanimclip');
  assert.deepEqual(await assignments(), [{modelId:'model-0', name:'001_Wave'}]);
  const parentVisible = page.locator('.object-model-row input[data-model-id="model-0"]');
  await parentVisible.uncheck();
  const shown = await page.locator('#objects-summary').innerText();
  const gunTarget = page.locator('.object-tree-item[data-kind="model"][data-model-id="model-1"]');
  await gunTarget.focus(); await page.keyboard.press('Enter');
  assert.equal(await page.locator('#rig-select').inputValue(), 'model-1');
  assert.equal(await parentVisible.isChecked(), false, 'Selecting a tree object with Enter must not change visibility.');
  assert.equal(await page.locator('#objects-summary').innerText(), shown);
  assert.equal(await gunTarget.getAttribute('aria-selected'), 'true');
  assert.equal(await page.locator('#timeline').isDisabled(), false, 'An unassigned active object retains the other object\'s shared timeline.');
  assert.equal(await page.evaluate(() => window.qaViewport.clip), null, 'An unassigned object does not inherit the other object clip.');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#objects-menu').isVisible(),true);
  await chooseClip('Animations/002_Turn.owanimclip');
  assert.deepEqual(await assignments(), [{modelId:'model-0',name:'001_Wave'}, {modelId:'model-1',name:'002_Turn'}]);
  await page.locator('#timeline').fill('30');
  const jointState = await page.evaluate(() => window.qaViewport.models.map(model => model.bones.map(bone => bone.matrix.toArray())));
  assert.notDeepEqual(jointState[0], jointState[1], 'Shared bone names have different assigned poses.');
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-0');
  assert.equal(await page.locator('#playing-title').innerText(), '001_Wave');
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-1');
  assert.equal(await page.locator('#playing-title').innerText(), '002_Turn');
  await openAnimationSetup(page);
  await page.locator('#clear-object-animation').click();
  assert.deepEqual(await assignments(), [{modelId:'model-0',name:'001_Wave'}]);
  assert.equal(await page.locator('#timeline').isDisabled(), false, 'Clearing one object keeps the remaining animation playable.');
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-0');
  assert.equal(await page.locator('#playing-title').innerText(), '001_Wave');
  report.manualBindings = {separateSharedJointPoses:true, restoringTargets:true, clearPreservesOther:true, visibilityIndependent:true};

  // A delayed arms response must not become the gun assignment after selection.
  holdClipPath = source('003_Root');
  await page.evaluate(() => [...document.querySelectorAll('.clip-row')].find(row => row.textContent.includes('003_Root')).click());
  await until(() => Boolean(releaseClip), 'delayed animation request');
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-1');
  const clipResponse = page.waitForResponse(response => response.url().endsWith('/api/load_animation'));
  releaseClip(); releaseClip = null;
  await clipResponse;
  await pause(100);
  assert.deepEqual(await assignments(), [], 'A stale response must not assign the old clip to either target.');
  assert.equal(await page.locator('#timeline').isDisabled(), true);
  assert.equal(await page.locator('#rig-select').inputValue(), 'model-1');
  report.staleResponseIgnored = true;

  // Adding a setup captures assignments without invoking Cinema or writing an
  // export. A single assignment still represents the whole loaded entity.
  await chooseClip('Animations/001_Wave.owanimclip');
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('gun_only');
  await page.locator('#export-animation-set').click();
  await page.waitForFunction(() => document.querySelector('#animation-set-export-status').textContent.startsWith('Added to export list:') && !document.querySelector('#export-animation-set').disabled);
  assert.equal(queueRequests.length, 1);
  assert.deepEqual(queueRequests[0], {name:'Gameplay1P_gun_only', entityPath:path.join(assets,'Gameplay1P.owentity'), rootPath:assets,
    assignments:[{modelId:'model-1',clipPath:source('001_Wave')}]});
  const firstSaved = structuredClone(savedEntries[0]);
  assert.equal(await page.locator('#animation-set-name').inputValue(), 'Gameplay1P_', 'Add resets the name for the next setup.');
  assert.equal(sceneRequests.length, 0, 'Adding one object never exports implicitly.');

  // The same source may animate both objects; adding again stores an independent
  // complete setup and leaves the first snapshot unchanged.
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-0');
  await chooseClip('Animations/001_Wave.owanimclip');
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('reload');
  await page.locator('#animation-set-name').press('Enter');
  await page.waitForFunction(() => document.querySelector('#animation-set-export-status').textContent.includes('Added to export list: Gameplay1P_reload.c4d') && !document.querySelector('#export-animation-set').disabled);
  assert.equal(queueRequests.length, 2);
  assert.deepEqual(queueRequests[1], {name:'Gameplay1P_reload', entityPath:path.join(assets,'Gameplay1P.owentity'), rootPath:assets,
    assignments:[{modelId:'model-1',clipPath:source('001_Wave')}, {modelId:'model-0',clipPath:source('001_Wave')}]});
  assert.equal(savedEntries.length, 2);
  assert.deepEqual(savedEntries[0], firstSaved, 'A later setup cannot mutate the stored first snapshot.');
  assert.notEqual(savedEntries[0].id, savedEntries[1].id);
  await page.locator('#fbx-options-button').click();
  assert.equal(await page.locator('.export-queue-row').count(), 2);
  assert.equal(await page.locator('.export-queue-check:checked').count(), 0, 'New entries require explicit export selection.');
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  assert.match(await page.locator('.export-queue-assignment').first().innerText(), /1 assigned animation/);
  assert.match(await page.locator('.export-queue-assignment').last().innerText(), /2 assigned animations/);
  await page.locator('#close-export-menu').click();
  await openAnimationSetup(page);
  await page.locator('#rig-select').selectOption('model-1');
  assert.equal(sceneRequests.length, 0, 'Naming, adding and selecting objects never invoke an export.');
  report.namedQueue = {oneObjectSnapshot:true, twoObjectSnapshot:true, wholeEntity:true, unchecked:true, noImplicitExport:true};

  // Real loaded Three.js scene and production backend: workflow tabs must keep
  // the selected rig, assignments, nickname, filtering and playback in place.
  if (await page.locator('#play-pause').getAttribute('aria-label') === 'Pause') await page.locator('#play-pause').click();
  await page.locator('#timeline').fill('17');
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('Gameplay1P_next_draft');
  await page.locator('#clip-search').fill('reload');
  await page.locator('#clip-sort').selectOption('nicknamed-first');
  const tabSnapshot = {
    rig: await page.locator('#rig-select').inputValue(),
    nickname: await page.locator('#animation-set-name').inputValue(),
    title: await page.locator('#playing-title').textContent(),
    frame: await page.locator('#timeline').inputValue(),
    assignments: await assignments(),
  };
  await page.evaluate(() => {window.qaBeforeTabs={viewport:window.qaViewport,canvas:document.querySelector('#viewport'),models:[...window.qaViewport.models],loads:window.qaLoadCount};});
  assert.equal(await page.locator('#analyze-library, #animation-evidence, #analysis-filters').count(),0,
    'Unused evidence controls are absent from the animation workflow.');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  report.animationEvidenceRemoved=true;
  await page.locator('#tab-animations').focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true);
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true);
  assert.equal(await page.locator('#timeline').inputValue(),tabSnapshot.frame,'Arrow-key tab navigation does not step the hidden animation.');
  await page.keyboard.press('Space');
  assert.equal(await page.locator('#play-pause').getAttribute('aria-label'),'Play','Space on a workflow tab does not toggle hidden playback.');
  await page.locator('#tab-animations').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  assert.equal(await page.evaluate(() => window.qaBeforeTabs.viewport===window.qaViewport && window.qaBeforeTabs.canvas===document.querySelector('#viewport') && window.qaBeforeTabs.models.every((model,index)=>model===window.qaViewport.models[index]) && window.qaBeforeTabs.loads===window.qaLoadCount),true,'Switching workflows reuses the real viewport, loaded rigs and canvas.');
  assert.equal(await page.locator('#rig-select').inputValue(),tabSnapshot.rig);
  assert.equal(await page.locator('#animation-set-name').inputValue(),tabSnapshot.nickname);
  assert.equal(await page.locator('#playing-title').textContent(),tabSnapshot.title);
  assert.equal(await page.locator('#timeline').inputValue(),tabSnapshot.frame);
  assert.equal(await page.locator('#clip-search').inputValue(),'reload');
  assert.equal(await page.locator('#clip-sort').inputValue(),'nicknamed-first');
  assert.deepEqual(await assignments(),tabSnapshot.assignments);
  await page.locator('#play-pause').click();
  await page.locator('#tab-toolchain').click();
  await page.waitForFunction(frame => document.querySelector('#timeline').value!==frame,tabSnapshot.frame);
  assert.equal(await page.locator('#play-pause').getAttribute('aria-label'),'Pause','Playback continues while the other workflow is visible.');
  await page.locator('#tab-animations').click();
  await page.locator('#play-pause').click();
  assert.deepEqual(await assignments(),tabSnapshot.assignments);
  report.workflowTabs={realLoadedPreviewPreserved:true,keyboardDoesNotStepClip:true,playbackContinues:true,selectionsAndNicknamePreserved:true};

  for (const [width,height] of [[1440,940],[1060,700]]) {
    await page.setViewportSize({width,height});
    await page.locator('#objects-menu').scrollIntoViewIfNeeded();
    const box = await page.locator('#objects-menu').boundingBox();
    assert.ok(box.x >= 0 && box.x+box.width <= width+1, 'Permanent objects panel fits the right inspector.');
    const fit = await page.evaluate(() => {
      const list=document.querySelector('#objects-list');
      return list.scrollWidth<=list.clientWidth+1 && document.documentElement.scrollWidth<=innerWidth;
    });
    assert.equal(fit,true,'Object names and controls do not create horizontal overflow.');
    await page.screenshot({path:path.join(scratch,`manual-objects-${width}.png`)});
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#objects-menu').isVisible(),true);
  }
  assert.deepEqual(sceneRequests, [], 'No scene or raw export was requested during the manual workflow.');
  assert.deepEqual(errors, []);
  report.passed = true; report.compactLayout = true; report.errors = errors;
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  console.log(JSON.stringify(report));
})().catch(error => {
  report.error = String(error.stack || error); report.serverLog = log;
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  console.error(error); process.exitCode = 1;
}).finally(async () => {
  releaseClip?.();
  await browser?.close();
  server.kill();
});

