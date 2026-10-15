// Packaged-application QA. --no-sandbox is solely a restricted-test-host flag;
// production BrowserWindow preferences and normal Avolition launches stay sandboxed.
const {_electron} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {spawnSync} = require('node:child_process');

const build = path.resolve(__dirname, '../../preview-build');
fs.mkdirSync(build, {recursive:true});
const scratch = fs.mkdtempSync(path.join(build, 'desktop-regression-'));
const assets = path.join(scratch, 'Exports');
const profile = path.join(scratch, 'Profile');
const output = path.join(scratch, 'Native picks 日本語');
const entity = path.join(assets, 'Entities/PreviewRobot/PreviewRobot.owentity');
const source = path.join(assets, 'Animations/001_Wave.owanimclip');
const legacyNotes = 'Legacy notes retained after Notes removal.\nPreserve café 日本語.';
fs.cpSync(path.join(__dirname, 'demo-assets'), assets, {recursive:true});
const duplicate = path.join(assets, 'Skins/Alternative/001_Wave.owanimclip');
fs.mkdirSync(path.dirname(duplicate), {recursive:true});
fs.copyFileSync(source, duplicate);
const executable = process.env.WHITEHOUND_EXE || process.env.OWIO_EXE
  || path.join(build, 'avolition-v0812/dist/Avolition/Avolition.exe');
let application;
let page;
const errors = [];
const report = {passed:false, executable, scratch, hidden:true, qaOnlyNoSandbox:true};

async function until(check, label, milliseconds=20000) {
  const end = Date.now()+milliseconds;
  while (Date.now()<end) {
    if (await check()) return;
    await new Promise(resolve => setTimeout(resolve, 75));
  }
  throw new Error('Timed out: '+label);
}

async function launch() {
  application = await _electron.launch({executablePath:executable,
    args:['--no-sandbox', '--test-hidden', '--data-dir', profile], timeout:45000});
  page = await application.firstWindow({timeout:45000});
  page.setDefaultTimeout(25000);
  page.on('pageerror', error => errors.push(error.message));
  await application.evaluate(({BrowserWindow, dialog}) => {
    const window = BrowserWindow.getAllWindows()[0];
    window.hide();
    window.setSkipTaskbar(true);
    window.webContents.setBackgroundThrottling(false);
    globalThis.__pickerQueue = [];
    globalThis.__pickerCalls = [];
    globalThis.__closeMessages = [];
    dialog.showOpenDialog = async (...args) => {
      globalThis.__pickerCalls.push(args.at(-1));
      const selected = globalThis.__pickerQueue.shift();
      return {canceled:!selected, filePaths:selected ? [selected] : []};
    };
    dialog.showMessageBox = async (...args) => {
      globalThis.__closeMessages.push(args.at(-1));
      return {response:0, checkboxChecked:false};
    };
  });
  // The HTML buttons exist before the module installs their event handlers.
  // Wait for connect() to finish, not just for the preload bridge to appear.
  await page.waitForFunction(() => window.pywebview?.api && !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  const native = await application.evaluate(({BrowserWindow}) => {
    const window = BrowserWindow.getAllWindows()[0];
    const preferences = window.webContents.getLastWebPreferences();
    return {title:window.getTitle(), visible:window.isVisible(), url:window.webContents.getURL(),
      sandbox:preferences.sandbox, contextIsolation:preferences.contextIsolation, nodeIntegration:preferences.nodeIntegration};
  });
  assert.equal(native.title, 'Avolition');
  assert.equal(native.visible, false);
  assert.equal(native.url, 'whitehound://app/index.html');
  assert.equal(native.sandbox, true);
  assert.equal(native.contextIsolation, true);
  assert.equal(native.nodeIntegration, false);
  assert.equal(await page.title(), 'Avolition');
  assert.equal(await page.locator('#app-logo, .avolition-empty-logo, .empty-note').count(), 0);
  assert.equal(await page.locator('.local-badge .version').innerText(), 'v0.8.23');
  assert.equal(await page.locator('#clip-notes').count(), 0);
  assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(), 0);
  assert.equal(await page.locator('#clip-name, #export-clip, #export-c4d, #track-groups, #track-count-filter').count(), 0);
  assert.equal(await page.locator('#export-menu #output-path').count(), 1);
  assert.equal(await page.locator('#fbx-options-button').isEnabled(), true);
  assert.equal(await page.locator('#pairing-panel').count(), 0);
  assert.equal(await page.locator('#hide-duplicates').isChecked(), true);
  assert.equal(await page.locator('#status-extra').count(), 0);
  assert.equal(await page.locator('#viewport-error-title').isHidden(), true);
  assert.equal((await page.locator('#viewport-empty').innerText()).includes('making overwatch'), false);
  for (const selector of ['#browse-root', '#browse-output']) assert.equal(await page.locator(selector).isEnabled(), true);
  assert.equal(await page.locator('#browse-entity').count(), 0);
  for (const selector of ['#show-textures', '#show-grid']) assert.equal(await page.locator(selector).isChecked(), true);
  for (const selector of ['#show-skeleton', '#lock-root', '#show-wireframe']) assert.equal(await page.locator(selector).isChecked(), false);
  assert.equal(await page.locator('#close-app').isHidden(), true);
  const isolated = await page.evaluate(async () => ({require:typeof require, process:typeof process,
    browserMode:(await window.pywebview.api.bootstrap()).data.browserMode,
    webgl:!!document.querySelector('#viewport').getContext('webgl2')}));
  assert.deepEqual(isolated, {require:'undefined', process:'undefined', browserMode:false, webgl:true});
  report.nativeWindow = native;
}

async function picker(button, selected) {
  await application.evaluate((_electron, selected) => {globalThis.__pickerQueue.push(selected);}, selected);
  // Hidden native windows do not reliably route OS pointer input before paint.
  // Dispatch through the real button handler and native IPC picker route.
  await page.locator(button).dispatchEvent('click');
}

async function closeNative() {
  const exited = application.waitForEvent('close', {timeout:25000});
  await application.evaluate(({BrowserWindow}) => {BrowserWindow.getAllWindows()[0].close();});
  await exited;
  application = null;
  page = null;
}

(async () => {
  await launch();
  const initialQueue = await page.evaluate(async () => (await window.pywebview.api.bootstrap()).data.exportQueue);
  assert.deepEqual(initialQueue, []);
  await page.locator('#fbx-options-button').dispatchEvent('click');
  assert.equal(await page.locator('#export-menu').isVisible(), true);
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  assert.equal(await page.locator('#export-queue-list .export-queue-row').count(), 0);
  await page.locator('#close-export-menu').dispatchEvent('click');
  assert.equal(await page.locator('#objects-button, #objects-menu[popover]').count(), 0);
  assert.equal(await page.locator('.inspector-panel #objects-menu').isVisible(), true);
  assert.equal(await page.locator('#objects-list [data-kind="model"]').count(), 0);
  assert.equal(await page.locator('#favorite-clip, #favorites-filter, .clip-favorite').count(), 0);
  report.queueDefaults = {empty:true, accessibleWithoutEntity:true, exportDisabledWithoutSelection:true};
  await page.evaluate(async ({source, notes}) => {
    const saved = await window.pywebview.api.save_annotation(source, {name:'', notes, favorite:true});
    if (!saved.ok) throw new Error(saved.error);
  }, {source, notes:legacyNotes});
  await picker('#browse-root', assets);
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '3');
  assert.equal(await page.locator('.clip-row').count(), 3);
  await page.locator('#hide-duplicates').uncheck();
  assert.equal(await page.locator('.clip-row').count(), 4);
  await page.locator('#hide-duplicates').check();
  assert.equal(await page.locator('.clip-row').count(), 3);
  report.duplicateFiltering = {defaultOn:true, uniqueClips:3, sourceFiles:4, toggled:true};
  assert.equal(await page.locator('#root-path').inputValue(), assets);
  await page.locator('#fbx-options-button').dispatchEvent('click');
  await picker('#browse-output', output);
  await until(async () => await page.locator('#output-path').inputValue() === output, 'Native output picker');
  await page.locator('#close-export-menu').dispatchEvent('click');
  await page.locator('#entity-select').selectOption(entity);
  await page.locator('#load-entity').dispatchEvent('click');
  await page.waitForFunction(() => document.querySelector('#viewport-empty').hidden && document.querySelector('#viewport-loading').hidden);
  const picks = await application.evaluate(() => globalThis.__pickerCalls);
  assert.equal(picks.length, 2, 'Loading a scanned entity needs no additional file picker.');
  assert.deepEqual(picks[0].properties, ['openDirectory', 'createDirectory']);
  assert.deepEqual(picks[1].properties, ['openDirectory', 'createDirectory']);
  assert.match(await page.locator('#mesh-stats').innerText(), /3 bones/);
  assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 168');
  report.polygons = 168;
  assert.equal(await page.locator('#objects-menu').isVisible(), true);
  const targetModel = await page.locator('#rig-select').inputValue();
  assert.ok(targetModel && targetModel !== 'auto', 'Manual object selection must have an explicit model ID');
  assert.equal(await page.locator('#rig-select option[value="auto"]').count(), 0);
  assert.ok(await page.locator('.object-tree-item[data-kind="model"]').count() > 0);
  assert.equal(await page.locator('.object-tree-item[data-kind="model"][aria-selected="true"]').count(), 1);
  const selectedModelName = await page.locator('#rig-select option:checked').innerText();
  const modelPrefix = selectedModelName.replace(/ \(\d+ bones\)$/, '').replace(/\.(?:[0-9a-f]{3}|owmdl)$/i, '');
  report.manualTargets = {explicitModelId:targetModel, modelPrefix, pairingPanelRemoved:true};
  const meshes = page.locator('.object-mesh-row input[data-model-id][data-mesh-id]');
  const meshCount = await meshes.count();
  assert.ok(meshCount > 1, 'The fixture must have independently hideable meshes.');
  assert.equal(await page.locator('#objects-summary').innerText(), `${meshCount} of ${meshCount} meshes shown`);
  await meshes.first().dispatchEvent('click');
  assert.equal(await meshes.first().isChecked(), false);
  assert.match(await page.locator('#polygon-count').innerText(), /^Polygons: \d[\d,]* \/ 168$/);
  assert.equal(await page.locator('#objects-summary').innerText(), `${meshCount-1} of ${meshCount} meshes shown`);
  await page.locator('#objects-hide-all').dispatchEvent('click');
  assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 0 / 168');
  assert.equal(await page.locator('#objects-summary').innerText(), `0 of ${meshCount} meshes shown`);
  await page.locator('#objects-show-all').dispatchEvent('click');
  assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 168');
  // A hidden weapon/submesh must remain hidden while choosing other clips.
  await meshes.first().dispatchEvent('click');
  assert.equal(await page.locator('#objects-menu').isVisible(), true);
  for (let repeat=0; repeat<1; repeat++) {
    for (const clip of ['002_Turn', '001_Wave']) {
      await page.locator('.clip-row').filter({hasText:clip}).dispatchEvent('click');
      await page.waitForFunction(() => !document.querySelector('#timeline').disabled && document.querySelector('#viewport-loading').hidden);
      await page.locator('#timeline').fill('30');
      assert.equal(await page.locator('#frame-input').inputValue(), '30');
      const match = (await page.locator('#match-message').innerText()).match(/(\d+) \/ (\d+) tracks matched/);
      assert.ok(match && Number(match[1])>0 && match[1]===match[2]);
    }
  }
  await page.locator('#step-forward').dispatchEvent('click');
  assert.equal(await page.locator('#frame-input').inputValue(), '31');
  await page.locator('#step-back').dispatchEvent('click');
  assert.equal(await page.locator('#frame-input').inputValue(), '30');
  assert.equal(await meshes.first().isChecked(), false);
  await page.locator('#objects-show-all').dispatchEvent('click');
  assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 168');
  report.objectVisibility = {meshCount, independentMeshes:true, hideAll:true, showAll:true, retainedAcrossClips:true};
  if (process.env.WHITEHOUND_FULL_DESKTOP === '1') {
  await page.locator('#show-wireframe').check();
  await page.locator('#show-wireframe').uncheck();
  // Optional full viewport regression; hidden compositor input can be limited.
  const view = await page.evaluate(async () => {
    const {PreviewViewport} = await import('./viewer.js');
    const catalog = (await window.pywebview.api.scan_root(document.querySelector('#root-path').value)).data;
    const asset = (await window.pywebview.api.load_entity(catalog.entities.find(row => row.kind === 'entity').path)).data;
    const holder = document.createElement('div');
    holder.style.cssText='width:320px;height:300px;position:fixed;left:380px;top:220px;z-index:99;opacity:0';
    const canvas = document.createElement('canvas');canvas.id='native-navigation-test';holder.append(canvas);document.body.append(holder);
    const viewport = new PreviewViewport(canvas);viewport.loadAsset(asset);viewport.controls.enableDamping=false;
    viewport.setOptions({wireframe:true});
    const meshes=viewport.models.flatMap(model => model.meshes), overlays=viewport.models.flatMap(model => model.overlays);
    window.__desktopView=viewport;
    return {facesRemainShaded:meshes.every(mesh => !mesh.material.wireframe),
      blackOverlays:overlays.length>0 && overlays.every(overlay => overlay.visible && overlay.material.color.getHex()===0),
      zoomSpeed:viewport.controls.zoomSpeed};
  });
  assert.equal(view.facesRemainShaded, true);
  assert.equal(view.blackOverlays, true);
  assert.equal(view.zoomSpeed, 3);
  const radius = () => page.evaluate(() => window.__desktopView.camera.position.distanceTo(window.__desktopView.controls.target));
  const navigation = await page.locator('#native-navigation-test').boundingBox();
  const initialRadius = await radius();
  await page.keyboard.down('Alt');
  await page.mouse.move(navigation.x+110, navigation.y+90);
  await page.mouse.down({button:'right'});
  await page.mouse.move(navigation.x+110, navigation.y+165, {steps:5});
  await page.mouse.up({button:'right'});
  assert.ok(Math.abs(await radius()-initialRadius)<1e-6);
  await page.mouse.move(navigation.x+110, navigation.y+90);
  await page.mouse.down({button:'right'});
  await page.mouse.move(navigation.x+190, navigation.y+90, {steps:5});
  await page.mouse.up({button:'right'});
  await page.keyboard.up('Alt');
  assert.ok(await radius()<initialRadius);
  const initialTarget = await page.evaluate(() => window.__desktopView.controls.target.toArray());
  await page.keyboard.down('1');
  await page.mouse.move(navigation.x+120, navigation.y+120);
  await page.mouse.down();
  await page.mouse.move(navigation.x+160, navigation.y+140, {steps:4});
  await page.mouse.up();
  await page.keyboard.up('1');
  assert.notDeepEqual(await page.evaluate(() => window.__desktopView.controls.target.toArray()), initialTarget);
  const initialPosition = await page.evaluate(() => window.__desktopView.camera.position.toArray());
  await page.keyboard.down('3');
  await page.mouse.move(navigation.x+120, navigation.y+120);
  await page.mouse.down();
  await page.mouse.move(navigation.x+175, navigation.y+125, {steps:4});
  await page.mouse.up();
  await page.keyboard.up('3');
  assert.notDeepEqual(await page.evaluate(() => window.__desktopView.camera.position.toArray()), initialPosition);
  await page.evaluate(() => {
    const view=window.__desktopView;view.renderer.setAnimationLoop(null);view._clear();view.controls.dispose();
    view.renderer.dispose();view.resizeObserver.disconnect();view.canvas.parentElement.remove();delete window.__desktopView;
  });
  report.viewport = {repeatedClips:2, scrubbing:true, shadedBlackWireframe:true, horizontalZoom:true, pan:true, orbit:true};
  } else report.viewport = {repeatedClips:2, scrubbing:true};
  if (process.env.WHITEHOUND_CAPTURE_HIDDEN === '1') {
    try {
      await page.screenshot({path:path.join(scratch, 'native-preview.png'), timeout:5000});
      report.screenshot = path.join(scratch, 'native-preview.png');
    } catch (error) { report.screenshotWarning=error.message; }
  }

  // A real failed directory save must still let native close offer recovery.
  // The old nickname field no longer exists; use an existing file as the invalid folder.
  await page.locator('#fbx-options-button').dispatchEvent('click');
  await page.locator('#output-path').fill(source);
  await page.locator('#output-path').press('Enter');
  await page.waitForFunction(() => document.querySelector('#output-help').classList.contains('error'));
  await application.evaluate(({BrowserWindow}) => {BrowserWindow.getAllWindows()[0].close();});
  await until(async () => (await application.evaluate(() => globalThis.__closeMessages.length))===1, 'Native close error dialog');
  assert.equal(await page.locator('#output-path').isEnabled(), true);
  const message = await application.evaluate(() => globalThis.__closeMessages[0]);
  assert.match(message.detail, /output.*file|Choose a folder|not saved/i);
  assert.deepEqual(message.buttons, ['Keep Avolition open', 'Close without saving']);
  await page.locator('#output-path').fill(output);
  await page.locator('#output-path').press('Enter');
  await page.waitForFunction(() => document.querySelector('#output-help').hidden);
  assert.equal(await page.locator('#fbx-root-motion').isChecked(), true);
  await page.locator('.export-menu-settings > summary').dispatchEvent('click');
  await page.locator('#fbx-textures').dispatchEvent('click');
  assert.equal(await page.locator('#fbx-textures').isChecked(), false);
  await page.locator('#close-export-menu').dispatchEvent('click');

  await page.locator('#animation-set-name').fill('bad/name');
  await page.locator('#export-animation-set').dispatchEvent('click');
  await page.waitForFunction(() => document.querySelector('#animation-set-export-status').textContent.startsWith('Not added:'));
  assert.match(await page.locator('#animation-set-export-status').innerText(), /Windows.*filename/);
  assert.equal((await page.evaluate(async () => (await window.pywebview.api.bootstrap()).data.exportQueue)).length, 0);

  const nickname = 'Native close café 日本語';
  const queueName = 'PreviewRobot_'+nickname;
  assert.equal(await page.locator('#favorite-clip, #favorites-filter, .clip-favorite').count(), 0);
  await page.locator('#clip-sort').selectOption('nicknamed-first');
  // Both preview options reset to off when reopening, even after enabling them.
  // Use the real checkbox default action/change handler, as with the native
  // picker buttons above: this QA window is hidden and cannot route OS clicks
  // reliably to controls in the scrolled lower panel.
  for (const selector of ['#show-skeleton', '#lock-root']) {
    assert.equal(await page.locator(selector).isChecked(), false);
    await page.locator(selector).dispatchEvent('click');
    assert.equal(await page.locator(selector).isChecked(), true);
  }
  await page.locator('#animation-set-name').fill(nickname);
  await page.locator('#export-animation-set').dispatchEvent('click');
  await page.waitForFunction(() => document.querySelector('#animation-set-export-status').textContent.startsWith('Added to'));
  assert.equal(await page.locator('.clip-name').first().innerText(), nickname);
  assert.equal(await page.locator('#animation-set-name').inputValue(), 'PreviewRobot_');
  const queued = await page.evaluate(async () => (await window.pywebview.api.bootstrap()).data.exportQueue);
  assert.equal(queued.length, 1);
  assert.equal(queued[0].name, queueName);
  assert.equal(queued[0].entityPath, entity);
  assert.equal(queued[0].rootPath, assets);
  assert.deepEqual(queued[0].assignments, [{modelId:targetModel, clipPath:source}]);
  assert.equal(fs.existsSync(output) && fs.readdirSync(output).length>0, false, 'Adding a queue entry writes no exports.');
  await page.locator('#fbx-options-button').dispatchEvent('click');
  assert.equal(await page.locator('.export-queue-check').isChecked(), false);
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  assert.equal(await page.locator('.export-queue-name').innerText(), queueName+'.c4d');
  assert.match(await page.locator('.export-queue-assignment').innerText(), /1 assigned animation.*whole entity/);
  await page.locator('.export-queue-check').dispatchEvent('click');
  assert.equal(await page.locator('#export-selected').isEnabled(), true);
  if (process.env.WHITEHOUND_CAPTURE_HIDDEN === '1') {
    await page.screenshot({path:path.join(scratch, 'native-export-queue.png'), timeout:5000});
    report.queueScreenshot = path.join(scratch, 'native-export-queue.png');
  }
  await page.locator('#close-export-menu').dispatchEvent('click');
  // Closing with a draft in the new name field must not add or export it.
  await page.locator('#animation-set-name').fill('Unadded draft 日本語');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'animation-set-name');
  await closeNative();
  assert.equal(fs.existsSync(output) && fs.readdirSync(output).length>0, false, 'Native close never exports checked entries or a focused draft.');
  const saved = JSON.parse(fs.readFileSync(path.join(profile,'catalog.json'), 'utf8'));
  assert.ok(Object.values(saved.annotations).some(value => value.name===nickname && value.notes===legacyNotes && value.favorite));
  assert.equal(saved.outputFolder, output);
  assert.deepEqual(saved.exportQueue, queued);
  assert.equal(saved.fbxOptions.bakeTextures, false);
  assert.equal(saved.fbxOptions.rootMotion, true);
  report.nativeClose = {failedSaveRecovered:true, focusedDraftNotQueued:true, queueSaved:true, noImplicitExports:true};

  // Legacy format preferences cannot re-enable raw copies or FBX in the queue UI.
  saved.fbxOptions.enabled = true;
  saved.fbxOptions.clipEnabled = true;
  saved.fbxOptions.c4dEnabled = false;
  saved.fbxOptions.upAxis = 'Z';
  fs.writeFileSync(path.join(profile,'catalog.json'), JSON.stringify(saved,null,2));
  await launch();
  assert.equal(await page.locator('#root-path').inputValue(), assets);
  assert.equal(await page.locator('#output-path').inputValue(), output);
  assert.equal(await page.locator('#clip-sort').inputValue(), 'nicknamed-first');
  await page.locator('#fbx-options-button').dispatchEvent('click');
  assert.equal(await page.locator('#export-queue-list .export-queue-row').count(), 1);
  assert.equal(await page.locator('.export-queue-name').innerText(), queueName+'.c4d');
  assert.equal(await page.locator('.export-queue-check').isChecked(), false, 'Queue selection is session-only and restores unchecked.');
  assert.equal(await page.locator('#export-selected').isDisabled(), true);
  assert.equal(await page.locator('#export-queue-summary').innerText(), '0 of 1 selected');
  assert.equal(await page.locator('#fbx-textures').isChecked(), false);
  assert.equal(await page.locator('#fbx-root-motion').isChecked(), true);
  await page.locator('#close-export-menu').dispatchEvent('click');
  await page.locator('#scan-root').dispatchEvent('click');
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent==='3');
  await page.locator('#clip-sort').selectOption('nicknamed-first');
  assert.equal(await page.locator('.clip-name').first().innerText(), nickname);
  await page.locator('#clip-search').fill(nickname);
  assert.equal(await page.locator('.clip-row').count(), 1);
  await page.locator('.clip-row').dispatchEvent('click');
  assert.equal(await page.locator('#clip-notes').count(), 0);
  const restoredNotes = await page.evaluate(async () => Object.values((await window.pywebview.api.bootstrap()).data.catalog));
  assert.ok(restoredNotes.some(value => value.name===nickname && value.notes===legacyNotes));
  assert.equal(await page.locator('#favorite-clip, #favorites-filter, .clip-favorite').count(), 0);
  const migrated = await page.evaluate(async () => (await window.pywebview.api.bootstrap()).data.fbxOptions);
  assert.equal(migrated.enabled, false);
  assert.equal(migrated.clipEnabled, false);
  assert.equal(migrated.c4dEnabled, true);
  assert.equal(migrated.upAxis, 'Y');
  assert.deepEqual((await page.evaluate(async () => (await window.pywebview.api.bootstrap()).data.exportQueue)), queued);
  await closeNative();
  assert.equal(fs.existsSync(output) && fs.readdirSync(output).length>0, false);
  assert.deepEqual(errors, []);
  report.persistence = true;
  report.legacyNotesPreserved = true;
  report.favoritesRemoved = true;
  report.nicknamedFirst = true;
  report.previewDefaults = {skeleton:false, inPlace:false};
  report.inPlaceDoesNotChangeRootMotionOption = true;
  report.exportQueue = {entries:1, wholeEntity:true, startsUnchecked:true, selectionNotPersisted:true, invalidNameRejected:true,
    restoredWithoutEntity:true, unicodeNameAndOutput:true, noImplicitExports:true};
  report.fbxControlsRemoved = true;
  report.legacyFbxDisabled = true;
  report.legacyRawCopyDisabled = true;
  report.uncaughtErrors = errors;
  report.passed = true;
  fs.writeFileSync(path.join(scratch, 'report.json'), JSON.stringify(report,null,2));
  console.log(JSON.stringify(report,null,2));
})().catch(async error => {
  report.error = error.stack || String(error);
  try {
    report.uiFailure = await page?.evaluate(() => ({status:document.querySelector('#status-text')?.textContent,
      root:document.querySelector('#root-path')?.value, clips:document.querySelector('#clip-count')?.textContent}));
    report.pickerFailure = await application?.evaluate(() => ({calls:globalThis.__pickerCalls, queued:globalThis.__pickerQueue}));
  } catch (_) { /* Keep the original failure when the app has already closed. */ }
  fs.writeFileSync(path.join(scratch,'report.json'), JSON.stringify(report,null,2));
  console.error(error);
  console.error('Desktop diagnostics: '+scratch);
  process.exitCode=1;
}).finally(async () => {
  if (!application) return;
  const pid=application.process().pid;
  try {
    await application.evaluate(({dialog, BrowserWindow}) => {
      dialog.showMessageBox=async()=>({response:1});
      BrowserWindow.getAllWindows().forEach(window=>window.close());
    });
    await application.waitForEvent('close',{timeout:8000});
  } catch (_) {
    if (process.platform==='win32') spawnSync('taskkill.exe',['/PID',String(pid),'/T','/F'],{windowsHide:true,stdio:'ignore'});
    else application.process().kill('SIGKILL');
  }
});
