// Real backend, format parsers and renderer. WHITEHOUND_EXE selects packaged
// Electron/native IPC; otherwise run the source HTTP backend. All assets are
// generated QA fixtures; no game, release request or native C4D export is used.
const {chromium, _electron} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn, spawnSync} = require('node:child_process');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/object-search-ui-'));
const assets = path.join(scratch, 'Exports with spaces 日本語');
const profile = path.join(scratch, 'Profile');
const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
const executable = process.env.WHITEHOUND_EXE ? path.resolve(process.env.WHITEHOUND_EXE) : null;
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
def bones(ids):
    return [(f'bone_{identifier:04X}', -1 if index == 0 else 0,
             (0, index, 0), (0, 0, 0)) for index, identifier in enumerate(ids)]
def motion(ids):
    return clip_bytes(bones=[(f'bone_{identifier:04X}', [(0, [(0, (0, 0, 0)),
        (60, (0, index + 1, 0))])]) for index, identifier in enumerate(ids)], duration=61)
write('Alpha/Models/000000000A01.00C/000000000A01.owmdl', model_bytes(name='000000000A01.00C', bones=bones([1, 2, 3])))
write('Alpha/Models/000000000A02.00C/000000000A02.owmdl', model_bytes(name='000000000A02.00C', bones=bones([16, 17])))
write('Alpha/Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='000000000A01.00C', children=[('Gun.owentity', 'bone_0002')], relative='.'))
write('Alpha/Gun.owentity', entity_bytes(name='Gun', model='000000000A02.00C', relative='.'))
write('Beta/Models/000000000B01.00C/000000000B01.owmdl', model_bytes(name='000000000B01.00C', bones=bones([1, 2, 4, 5])))
write('Beta/Gameplay3P.owentity', entity_bytes(name='Gameplay3P', model='000000000B01.00C', relative='.'))
full = motion([1, 2, 3])
write('Alpha/Models/000000000A01.00C/Animations/0/0/001_Full.owanimclip', full)
write('Alpha/Skins/Copy/001_Full.owanimclip', full)
write('Alpha/Models/000000000A01.00C/Animations/0/0/002_Extra.owanimclip', motion([1, 2, 3, 99]))
write('Alpha/Models/000000000A01.00C/Animations/0/0/003_Partial.owanimclip', motion([2]))
write('Alpha/Models/000000000A02.00C/Animations/0/0/004_Gun.owanimclip', motion([16, 17]))
write('Animations/005_Other.owanimclip', motion([65, 66, 67]))
write('Beta/Models/000000000B01.00C/Animations/0/0/006_Beta.owanimclip', motion([1, 2, 4, 5]))
# One valid header with damaged channel payload, plus an unsupported version.
write('Animations/007_Damaged.owanimclip', motion([1, 2])[:-3])
write('Animations/008_Unsupported.owanimclip', clip_bytes(version=(9, 0)))
`);
const fixture = spawnSync(python, [fixtureScript, project, assets], {encoding:'utf8', windowsHide:true});
assert.equal(fixture.status, 0, fixture.stderr || fixture.stdout);

function sourceHashes(folder) {
  const result = {};
  function visit(directory) {
    for (const entry of fs.readdirSync(directory, {withFileTypes:true})) {
      const filename = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(filename);
      else result[path.relative(folder, filename)] = crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
    }
  }
  visit(folder);
  return result;
}
const beforeHashes = sourceHashes(assets);
const server = executable ? null : spawn(python, [path.join(project, 'app.py'), '--serve', '--port', '0', '--data-dir', profile], {
  cwd:project, windowsHide:true, stdio:['ignore','pipe','pipe'],
  env:{...process.env, PYTHONDONTWRITEBYTECODE:'1'},
});
let browser, application, serverLog = '';
const errors = [], bindingRequests = [];
const report = {passed:false, scratch, realBackend:true, syntheticAssets:true, nativeC4DExport:false,
  packaged:Boolean(executable),nativeIPC:Boolean(executable),executable,qaOnlyNoSandbox:Boolean(executable)};

async function launchNative() {
  application = await _electron.launch({executablePath:executable,
    args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:45000});
  const page = await application.firstWindow({timeout:45000});
  const preferences = await application.evaluate(({BrowserWindow}) => {
    const window = BrowserWindow.getAllWindows()[0];
    window.hide(); window.setSkipTaskbar(true); window.setContentSize(1440,940);
    window.webContents.setBackgroundThrottling(false);
    const settings = window.webContents.getLastWebPreferences();
    return {visible:window.isVisible(),sandbox:settings.sandbox,
      contextIsolation:settings.contextIsolation,nodeIntegration:settings.nodeIntegration};
  });
  assert.deepEqual(preferences, {visible:false,sandbox:true,contextIsolation:true,nodeIntegration:false});
  page.setDefaultTimeout(20000);
  page.on('pageerror', error => errors.push(error.message));
  return page;
}

(async () => {
  const address = server ? await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Backend startup timed out: '+serverLog)), 15000);
    server.once('error', error => {clearTimeout(timer); reject(error);});
    server.stderr.on('data', chunk => {serverLog += chunk;});
    server.stdout.on('data', chunk => {
      serverLog += chunk;
      const found = serverLog.match(/http:\/\/127\.0\.0\.1:\d+/);
      if (found) {clearTimeout(timer); resolve(found[0]);}
    });
  }) : null;
  let page;
  if (executable) page = await launchNative();
  else {
    browser = await chromium.launch({headless:true, args:['--enable-unsafe-swiftshader']});
    page = await browser.newPage({viewport:{width:1440, height:940}});
    page.on('pageerror', error => errors.push(error.message));
  }
  page.setDefaultTimeout(20000);
  page.on('request', request => {
    if (request.url().endsWith('/api/read_clip_bindings')) bindingRequests.push(request.postDataJSON());
  });
  const ready = () => page.waitForFunction(() => !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  const row = name => page.locator('.clip-row').filter({has:page.locator('.clip-name', {hasText:new RegExp('^'+name+'$')})});
  const coverage = name => row(name).first().locator('.clip-coverage-label').innerText();
  const pauseAt = async frame => {
    await page.locator('#timeline').fill(String(frame));
    await page.waitForFunction(value => document.querySelector('#timeline').value === String(value), frame);
  };
  const animationSnapshot = () => page.evaluate(() => ({
    assignments:window.qaViewport.assignments.map(({modelId,clip}) => ({modelId,path:clip.path})),
    clip:document.querySelector('#detail-filename').textContent,
    selected:document.querySelector('.clip-row.is-selected')?.title.split('\n')[0] || null,
    frame:document.querySelector('#timeline').value,
    object:document.querySelector('#rig-select').value,
  }));
  const stableState = snapshot => ({assignments:snapshot.assignments,clip:snapshot.clip,frame:snapshot.frame,object:snapshot.object});
  const chooseClip = async name => {
    await row(name).first().click();
    await page.waitForFunction(name => document.querySelector('#detail-filename').textContent === name
      && document.querySelector('#viewport-loading').hidden
      && window.qaViewport.assignments.some(({clip}) => clip.name === name), name);
  };
  const loadEntity = async (relative, modelCount) => {
    await page.locator('#entity-select').selectOption(path.join(assets, relative));
    await page.locator('#load-entity').click();
    await page.waitForFunction(count => window.qaViewport?.models.length === count
      && document.querySelector('#viewport-loading').hidden, modelCount);
    await page.waitForFunction(() => !document.querySelector('#library-match-status').textContent.startsWith('Comparing')
      && document.querySelectorAll('.clip-coverage-label').length > 0
      && [...document.querySelectorAll('.clip-coverage-label')].every(label => !label.textContent.startsWith('Comparing')));
  };

  if (address) await page.goto(address);
  await ready();
  assert.deepEqual(await page.evaluate(() => ['read_clip_bindings','library_settings'].map(method => typeof window.pywebview?.api?.[method])),
    ['function','function'], 'Both matching and preference APIs are exposed by the active transport.');
  assert.equal(await page.locator('#clip-sort').inputValue(), 'object-match');
  assert.equal(await page.locator('#hide-duplicates').isChecked(), true);
  assert.equal(await page.locator('#library-object').isDisabled(), true);
  assert.equal(await page.locator('#analyze-library, #animation-evidence').count(), 0);
  assert.equal(await page.locator('#track-count-filter, #track-groups, #export-c4d, #export-clip, #clip-name').count(), 0,
    'The simplified library has no track groups or automatic nickname export controls.');
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '8');
  assert.equal(await page.locator('.clip-row').count(), 8, 'Nine occurrences yield eight distinct display rows.');
  await page.evaluate(async () => {
    const {PreviewViewport} = await import('./viewer.js');
    const load = PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset = function(...args) {window.qaViewport = this; return load.apply(this, args);};
  });
  await loadEntity('Alpha/Gameplay1P.owentity', 2);
  assert.equal(await page.locator('#library-object option').count(), 2);
  assert.equal(await page.locator('#library-object').inputValue(), 'model-0');
  assert.match(await page.locator('#library-object option:checked').innerText(), /3 bones/);
  assert.match(await coverage('001_Full'), /^3 \/ 3 bones covered/);
  assert.match(await coverage('002_Extra'), /^3 \/ 3 bones covered/);
  assert.match(await coverage('003_Partial'), /^1 \/ 3 bones covered/);
  assert.match(await coverage('005_Other'), /^0 \/ 3 bones covered/);
  assert.equal(await row('001_Full').first().locator('.clip-coverage-fill').evaluate(node => node.style.width), '100%');
  assert.equal(await row('005_Other').locator('.clip-coverage-fill').evaluate(node => node.style.width), '0%');
  assert.match(await page.locator('.clip-row').first().innerText(), /001_Full/, 'Exact folder evidence and actual bone IDs rank the matching clip.');
  assert.equal(await coverage('007_Damaged'), 'Bone coverage unavailable');
  assert.equal(await coverage('008_Unsupported'), 'Bone coverage unavailable');
  assert.equal(await row('007_Damaged').locator('.clip-coverage-fill').count(), 0, 'Unreadable metadata is not represented by a zero-match bar.');
  if (!executable) assert.ok(bindingRequests.length > 0, 'Renderer obtained real bone names from the source backend.');
  const bindingProof = await page.evaluate(async source => window.pywebview.api.read_clip_bindings([source]),
    path.join(assets, 'Alpha/Models/000000000A01.00C/Animations/0/0/001_Full.owanimclip'));
  assert.equal(bindingProof.ok, true, 'Binding metadata crosses the actual backend allowlist.');
  assert.deepEqual(bindingProof.data.clips[0].boneNames, ['bone_0001','bone_0002','bone_0003']);

  // A measured zero is informative, never a hard rejection of playback.
  await chooseClip('005_Other');
  assert.equal(await page.locator('#timeline').isDisabled(), false);
  await chooseClip('001_Full');
  await page.locator('#library-object').selectOption('model-1');
  assert.equal(await page.locator('#rig-select').inputValue(), 'model-1');
  assert.match(await coverage('004_Gun'), /^2 \/ 2 bones covered/);
  assert.match(await coverage('001_Full'), /^0 \/ 2 bones covered/);
  await chooseClip('004_Gun');
  await page.locator('#rig-select').selectOption('model-0');
  assert.equal(await page.locator('#library-object').inputValue(), 'model-0');
  assert.match(await coverage('001_Full'), /^3 \/ 3 bones covered/);
  await pauseAt(17);
  const beforeFilter = await animationSnapshot();
  assert.equal(beforeFilter.assignments.length, 2);
  await page.locator('#clip-search').fill('003_Partial');
  assert.equal(await page.locator('.clip-row').count(), 1);
  assert.match(await page.locator('.clip-name').innerText(), /003_Partial/);
  assert.deepEqual(stableState(await animationSnapshot()), stableState(beforeFilter));
  await page.locator('#clip-search').fill('');
  assert.deepEqual(await animationSnapshot(), beforeFilter, 'Clearing search restores the selected row without changing assignments or frame.');
  await page.locator('#clip-sort').selectOption('name');
  assert.deepEqual(await animationSnapshot(), beforeFilter, 'Sorting does not reassign or restart clips.');

  await page.locator('#clip-search').fill('001_Full');
  assert.equal(await page.locator('.clip-row').count(), 1);
  await page.locator('#hide-duplicates').uncheck();
  assert.equal(await page.locator('.clip-row').count(), 2);
  assert.deepEqual(stableState(await animationSnapshot()), stableState(beforeFilter));
  await page.locator('#clip-search').fill('');
  assert.equal(await page.locator('.clip-row').count(), 9);

  await loadEntity('Beta/Gameplay3P.owentity', 1);
  assert.equal(await page.locator('#library-object option').count(), 1);
  assert.match(await page.locator('#library-object option:checked').innerText(), /4 bones/);
  assert.match(await coverage('006_Beta'), /^4 \/ 4 bones covered/);
  assert.match(await coverage('001_Full'), /^2 \/ 4 bones covered/);
  assert.equal(await row('006_Beta').locator('.clip-coverage-fill').evaluate(node => node.style.width), '100%');
  assert.equal(await row('001_Full').first().locator('.clip-coverage-fill').evaluate(node => node.style.width), '50%');
  // Loading an entity clears its old object assignments, then the established
  // preview behavior may apply the current library selection to the new root.
  await page.waitForFunction(() => window.qaViewport.assignments.length === 1
    && window.qaViewport.assignments[0].clip.name === '001_Full'
    && document.querySelector('#viewport-loading').hidden);
  const afterEntity = await animationSnapshot();
  assert.deepEqual(afterEntity.assignments, [beforeFilter.assignments.find(item => item.modelId === 'model-0')],
    'Only the current selection is reapplied to the new entity; the old gun assignment is removed.');
  assert.equal(await page.locator('#library-object').inputValue(), afterEntity.object);
  await page.setViewportSize({width:1024, height:700});
  const layout = await page.evaluate(() => ({listHeight:document.querySelector('#clip-list').getBoundingClientRect().height,
    overflow:document.documentElement.scrollWidth>innerWidth,
    rowOverflows:[...document.querySelectorAll('.clip-row')].some(row => row.scrollWidth>row.clientWidth+1)}));
  assert.ok(layout.listHeight >= 150, 'Compact search controls retain a useful library list.');
  assert.equal(layout.overflow, false);
  assert.equal(layout.rowOverflows, false);
  await page.screenshot({path:path.join(scratch, 'object-search-compact.png')});

  await page.waitForFunction(async () => {
    const result = await window.pywebview.api.library_settings();
    return result.ok && result.data.sort === 'name' && result.data.hideDuplicates === false;
  });
  if (executable) {
    await application.close(); application = null;
    page = await launchNative();
  } else await page.reload();
  await ready();
  assert.equal(await page.locator('#clip-sort').inputValue(), 'name');
  assert.equal(await page.locator('#hide-duplicates').isChecked(), false);
  assert.equal(await page.locator('#root-path').inputValue(), assets, 'The saved library source survives reconnect/restart.');
  // Startup restores the configured root without automatically scanning it.
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '9');
  const saved = JSON.parse(fs.readFileSync(path.join(profile, 'catalog.json'), 'utf8'));
  assert.deepEqual(saved.libraryOptions, {sort:'name',hideDuplicates:false});
  assert.deepEqual(sourceHashes(assets), beforeHashes, 'Scanning, browsing and filtering leave every fixture byte unchanged.');
  assert.deepEqual(errors, []);
  Object.assign(report, {passed:true,occurrences:9,uniqueClips:8,objectBoneCounts:[3,2,4],layout,
    partialCoverage:true,equalTrackCountsDifferentBones:true,unavailableNotZero:true,
    zeroMatchPlayable:true,filterPreservesAnimation:true,settingsPersisted:true,
    sourceFilesUnchanged:true,bindingRequests:executable ? null : bindingRequests.length,
    nativeRestartVerified:Boolean(executable),errors});
  console.log(JSON.stringify(report));
})().catch(error => {
  report.error = String(error.stack || error); report.serverLog = serverLog;
  console.error(error); process.exitCode = 1;
}).finally(async () => {
  fs.writeFileSync(path.join(scratch, 'report.json'), JSON.stringify(report, null, 2));
  await browser?.close();
  await application?.close();
  server?.kill();
});
