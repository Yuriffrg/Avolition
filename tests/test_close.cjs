// HISTORICAL HARNESS: v0.8.9-and-earlier nickname-triggered export workflow.
// Intentionally retained for old builds; current UI removed these controls.
// Superseded by test_export_queue_ui.cjs and test_desktop.cjs; see README.md.
// Exercise the exact renderer helper awaited by Electron's native close handler.
// This test owns its loopback host on 8767 and never touches the user's catalog.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const project = path.resolve(__dirname, '..');
const build = path.resolve(project, '../preview-build');
fs.mkdirSync(build, {recursive:true});
const workspace = fs.mkdtempSync(path.join(build, 'native-close-'));
const assets = path.join(workspace, 'Exports');
const stateDir = path.join(workspace, 'State');
fs.cpSync(path.join(__dirname, 'demo-assets'), assets, {recursive:true});
const python = process.env.WHITEHOUND_TEST_PYTHON || process.env.OWIO_TEST_PYTHON || 'python';
const source = path.join(assets, 'Animations', '001_Wave.owanimclip');
const legacyNotes = 'Legacy notes stay stored without a Notes control.\nPreserve café 日本語.';
const server = spawn(python, [path.join(project, 'app.py'), '--serve', '--port', '8767', '--data-dir', stateDir], {
  cwd:project, windowsHide:true, stdio:['ignore', 'pipe', 'pipe'],
});
let serverLog = '';
let browser;

async function serverReady() {
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Test server did not start: ' + serverLog)), 15000);
    const finish = fn => value => {clearTimeout(timeout); fn(value);};
    server.once('error', finish(reject));
    server.once('exit', code => finish(reject)(new Error(`Test server exited ${code}: ${serverLog}`)));
    server.stderr.on('data', chunk => {serverLog += chunk.toString();});
    server.stdout.on('data', chunk => {
      serverLog += chunk.toString();
      if (serverLog.includes('http://127.0.0.1:8767')) finish(resolve)();
    });
  });
}

(async () => {
  await serverReady();
  browser = await chromium.launch({headless:true, args:['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport:{width:1440, height:940}, deviceScaleFactor:1});
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.setDefaultTimeout(20000);
  await page.goto('http://127.0.0.1:8767');
  await page.waitForFunction(() => typeof window.whitehoundPrepareClose === 'function' && !document.querySelector('#scan-root').disabled);
  assert.equal(await page.title(), 'Whitehound');
  assert.equal(await page.locator('#clip-notes').count(), 0);
  await page.locator('#export-c4d').uncheck();
  await page.locator('#export-clip').check();
  await page.evaluate(async ({source, notes}) => {
    const saved = await window.pywebview.api.save_annotation(source, {name:'', notes, favorite:false});
    if (!saved.ok) throw new Error(saved.error);
  }, {source, notes:legacyNotes});
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '3');
  await page.locator('#load-entity').click();
  await page.waitForFunction(() => document.querySelector('#viewport-empty').hidden);
  await page.locator('.clip-row').filter({hasText:'001_Wave'}).click();
  await page.waitForFunction(() => !document.querySelector('#timeline').disabled);

  await page.evaluate(() => {
    window.__backendClose = window.pywebview.api.close_app;
    window.__closeCalls = 0;
    window.pywebview.api.close_app = async () => {
      window.__closeCalls++;
      throw new Error('The renderer preparation helper must not close the host');
    };
  });
  // Keep Nickname focused: clicking a native X does not commit via a DOM blur.
  await page.locator('#clip-name').fill('invalid/name');
  const failed = await page.evaluate(() => window.whitehoundPrepareClose());
  assert.equal(failed.ok, false, JSON.stringify(failed));
  assert.match(failed.error, /filename|Windows|not saved/i);
  assert.equal(await page.locator('#clip-name').isEnabled(), true);
  assert.equal(await page.locator('#root-path').isEnabled(), true);
  assert.equal(await page.locator('#scan-root').isEnabled(), true);
  assert.equal(await page.locator('#play-pause').getAttribute('aria-label'), 'Play');
  assert.equal(await page.evaluate(() => window.__closeCalls), 0);

  // Slow real backend calls make an early-resolving close helper observable.
  await page.evaluate(() => {
    window.__saveEvents = [];
    for (const [method, delay] of [['save_annotation', 120], ['set_output_folder', 170], ['export_animation', 240]]) {
      const original = window.pywebview.api[method];
      window.pywebview.api[method] = async (...args) => {
        window.__saveEvents.push({method, phase:'start', args});
        await new Promise(resolve => setTimeout(resolve, delay));
        const result = await original(...args);
        window.__saveEvents.push({method, phase:'finish', args, ok:result.ok});
        return result;
      };
    }
  });
  const output = path.join(workspace, 'Close picks 日本語');
  const nickname = 'Close café 日本語';
  await page.locator('#output-path').fill(output);
  await page.locator('#clip-name').fill(nickname);
  const completed = await page.evaluate(async () => {
    const results = await Promise.all([window.whitehoundPrepareClose(), window.whitehoundPrepareClose()]);
    window.__saveEvents.push({phase:'prepared'});
    return {results, events:window.__saveEvents, closeCalls:window.__closeCalls};
  });
  assert.deepEqual(completed.results, [{ok:true}, {ok:true}]);
  assert.equal(completed.closeCalls, 0);
  assert.equal(await page.locator('#clip-name').isDisabled(), true);
  assert.equal(await page.locator('#root-path').isDisabled(), true);
  assert.equal(await page.locator('#scan-root').isDisabled(), true);
  const lastEvent = completed.events.at(-1);
  assert.equal(lastEvent.phase, 'prepared');
  const savedNickname = completed.events.filter(event => event.method === 'save_annotation' && event.phase === 'finish'
    && event.args[1].name === nickname && event.ok);
  assert.ok(savedNickname.length > 0, JSON.stringify(completed.events));
  const exported = completed.events.filter(event => event.method === 'export_animation'
    && event.args[1] === nickname && event.phase === 'finish');
  assert.equal(exported.length, 1, 'Concurrent preparation must not dispatch duplicate copies');
  assert.equal(exported[0].ok, true);
  assert.ok(completed.events.some(event => event.method === 'set_output_folder' && event.phase === 'finish' && event.ok));
  const destination = path.join(output, nickname + '.owanimclip');
  assert.deepEqual(fs.readFileSync(destination), fs.readFileSync(source));
  const catalog = JSON.parse(fs.readFileSync(path.join(stateDir, 'catalog.json'), 'utf8'));
  assert.ok(Object.values(catalog.annotations).some(value => value.name === nickname && value.notes === legacyNotes));
  assert.equal(catalog.outputFolder, output);
  assert.deepEqual(await page.evaluate(() => window.whitehoundPrepareClose()), {ok:true});
  assert.deepEqual(errors, []);

  // Only after successful preparation does the host perform its close action.
  const hostClosed = await page.evaluate(() => window.__backendClose());
  assert.equal(hostClosed.ok, true);
  await page.close();
  console.log(JSON.stringify({passed:true, nativeCloseRejectedInvalidName:true, editingRecovered:true,
    concurrentPreparation:true, metadataAndCopyCompletedBeforeClose:true, legacyNotesPreserved:true, copiedFile:destination,
    uncaughtErrors:errors}, null, 2));
})().catch(error => {
  console.error(error);
  if (serverLog) console.error(serverLog);
  process.exitCode = 1;
}).finally(async () => {
  if (browser) await browser.close();
  if (server.exitCode === null) server.kill();
});
