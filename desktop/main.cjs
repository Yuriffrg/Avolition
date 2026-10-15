'use strict';

const { app, BrowserWindow, Menu, dialog, ipcMain, protocol, net, session, nativeTheme, shell } = require('electron');
const { spawn } = require('node:child_process');
const { pathToFileURL } = require('node:url');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { ReplyReader } = require('./frames.cjs');

const APP_URL = 'whitehound://app/index.html';
const ALLOWED_METHODS = new Set([
  'bootstrap', 'browse_root', 'browse_output', 'set_output_folder', 'set_export_settings', 'export_animation', 'export_animation_set', 'c4d_status',
  'browse_entity', 'scan_root', 'load_entity', 'preview_entity', 'release_entity_preview', 'load_texture', 'load_animation', 'save_annotation', 'ping', 'close_app',
  'workflow_settings', 'use_library_directory', 'browse_workflow_directory', 'open_toolchain_output',
  'toolchain_status', 'toolchain_check', 'toolchain_install', 'toolchain_validate', 'toolchain_list', 'toolchain_start', 'toolchain_cancel',
  'analysis_start', 'analysis_status', 'analysis_index', 'analysis_detail', 'read_clip_bindings', 'library_settings', 'export_queue_save', 'export_queue_remove', 'export_queue_run', 'open_export_output', 'maps_settings', 'scan_maps', 'load_map', 'export_map', 'export_map_start', 'export_map_status', 'browse_map', 'open_map_output', 'object_names', 'save_object_name',
]);
const REQUEST_TIMEOUT = 180000;
const MAX_PENDING = 128;
const MAX_REQUEST_BYTES = 1024 * 1024;
const MAX_RESPONSE_BYTES = 256 * 1024 * 1024;
// Ask for bulk arrays as binary frames (frames.cjs). Cores that do not support
// them ignore the request key and reply in JSON. AVOLITION_BINARY_ARRAYS=0
// forces JSON replies, e.g. to compare both paths.
const BINARY_ARRAYS = process.env.AVOLITION_BINARY_ARRAYS !== '0';
// GPU block formats ("bc7", "bc3", "bc1") the renderer can sample, set once by
// the renderer (window.pywebview.setTextureFormats). They go with every
// binary load_texture request as "textureFormats", so the core may answer
// with block-compressed mips instead of a PNG. Cores that do not know the key
// ignore it. AVOLITION_COMPRESSED_TEXTURES=0 keeps PNG replies.
const TEXTURE_FORMATS = new Set(['bc7', 'bc3', 'bc1']);
const COMPRESSED_TEXTURES = BINARY_ARRAYS && process.env.AVOLITION_COMPRESSED_TEXTURES !== '0';
let textureFormats = [];
const CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'";

function optionsFromArgs(argv) {
  const result = { testHidden: argv.includes('--test-hidden') };
  const names = new Map([
    ['--smoke-test', 'smokeReport'], ['--screenshot', 'screenshot'], ['--root', 'root'], ['--data-dir', 'dataDir'],
  ]);
  for (let index = 0; index < argv.length; index++) {
    const name = names.get(argv[index]);
    if (!name) continue;
    const value = argv[++index];
    if (!value || value.startsWith('--')) throw new Error(`Missing path for ${argv[index - 1]}`);
    result[name] = path.resolve(value);
  }
  if (result.screenshot && !result.smokeReport) throw new Error('--screenshot requires --smoke-test');
  if (result.smokeReport) {
    if (path.extname(result.smokeReport).toLowerCase() !== '.json') throw new Error('Smoke report must be a .json file.');
    if (!result.dataDir) {
      result.dataDir = path.join(path.dirname(result.smokeReport), `${path.parse(result.smokeReport).name}-state-${Date.now()}`);
    }
    if (result.screenshot && (!within(path.dirname(result.smokeReport), result.screenshot)
        || path.extname(result.screenshot).toLowerCase() !== '.png')) {
      throw new Error('Smoke screenshot must be a PNG inside the supplied report directory.');
    }
  }
  return result;
}

function within(parent, child) {
  const relative = path.relative(parent, child);
  return relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

const options = optionsFromArgs(process.argv);
const localData = process.env.LOCALAPPDATA || app.getPath('appData');
const dataDirectory = options.dataDir || process.env.AVOLITION_DATA_DIR || process.env.WHITEHOUND_DATA_DIR
  || process.env.OWIO_DATA_DIR || process.env.OWPREVIEW_DATA_DIR || path.join(localData, 'Avolition');
const desktopDirectory = path.join(dataDirectory, 'desktop');
const chromiumDirectory = path.join(desktopDirectory, 'chromium');
fs.mkdirSync(chromiumDirectory, { recursive: true });
const logPath = path.join(desktopDirectory, 'avolition.log');
app.setName('Avolition');
app.setPath('userData', desktopDirectory);
app.setPath('sessionData', chromiumDirectory);
app.setAppUserModelId('Avolition.Desktop');
protocol.registerSchemesAsPrivileged([{ scheme: 'whitehound', privileges: { standard: true, secure: true, supportFetchAPI: true } }]);

function log(message) {
  try {
    if (fs.existsSync(logPath) && fs.statSync(logPath).size > 2 * 1024 * 1024) {
      fs.writeFileSync(logPath, 'Avolition log rotated\n');
    }
    fs.appendFileSync(logPath, `${new Date().toISOString()} ${String(message).slice(0, 12000)}\n`, 'utf8');
  } catch (_) { /* Logging must not interrupt close/save operations. */ }
}

function timeout(promise, milliseconds, label) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${label} timed out`)), milliseconds); }),
  ]).finally(() => clearTimeout(timer));
}

class Backend {
  constructor(executable) {
    this.pending = new Map();
    this.nextId = 1;
    this.reader = new ReplyReader(message => this.deliver(message), MAX_RESPONSE_BYTES);
    this.failed = null;
    this.stopping = false;
    const applicationDirectory = app.isPackaged ? path.dirname(process.execPath) : path.resolve(__dirname, '..');
    const args = ['--stdio', '--app-dir', applicationDirectory];
    // Omitting this on normal launch lets the core migrate the legacy catalog.
    if (options.dataDir) args.push('--data-dir', options.dataDir);
    if (options.root) args.push('--root', options.root);
    this.child = spawn(executable, args, {
      cwd: path.dirname(executable), windowsHide: true, shell: false, stdio: ['pipe', 'pipe', 'pipe'],
      env: { ...process.env, PYTHONIOENCODING: 'utf-8', PYTHONUNBUFFERED: '1' },
    });
    this.child.stdout.on('data', chunk => this.accept(chunk));
    this.child.stderr.on('data', chunk => log(`core: ${chunk.toString('utf8')}`));
    this.child.stdin.on('error', error => this.fail(error));
    this.child.on('error', error => this.fail(error));
    this.child.on('exit', (code, signal) => {
      if (!this.stopping) this.fail(new Error(`The local importer stopped (code ${code}, signal ${signal || 'none'}).`));
    });
    log(`Started local importer PID ${this.child.pid || 'pending'}`);
  }

  fail(error) {
    if (this.failed || this.stopping) return;
    this.failed = error instanceof Error ? error : new Error(String(error));
    log(this.failed.stack || this.failed.message);
    for (const entry of this.pending.values()) { clearTimeout(entry.timer); entry.reject(this.failed); }
    this.pending.clear();
    if (mainWindow && !mainWindow.isDestroyed() && !finishingSmoke) void fatal(this.failed);
  }

  accept(chunk) {
    if (this.failed || this.stopping) return;
    try { this.reader.push(chunk); }
    catch (error) {
      this.fail(error.code === 'RESPONSE_TOO_LARGE' ? error : new Error(`Could not read the local importer response: ${error.message}`));
    }
  }

  deliver(message) {
    if (this.failed || this.stopping) return;
    const entry = this.pending.get(message.id);
    if (!entry) return; // A response to a request that timed out earlier.
    if (!message.result || typeof message.result.ok !== 'boolean') throw new Error('Invalid local importer response.');
    this.pending.delete(message.id);
    clearTimeout(entry.timer);
    entry.resolve(message.result);
  }

  call(method, args = []) {
    if (this.failed) return Promise.reject(this.failed);
    if (this.stopping || !this.child.stdin.writable) return Promise.reject(new Error('The local importer is closed.'));
    if (this.pending.size >= MAX_PENDING) return Promise.reject(new Error('Too many operations are pending. Wait for the current import to finish.'));
    const id = this.nextId++;
    const message = BINARY_ARRAYS ? { id, method, args, binary: true } : { id, method, args };
    if (method === 'load_texture' && COMPRESSED_TEXTURES && textureFormats.length) message.textureFormats = textureFormats;
    const request = JSON.stringify(message) + '\n';
    if (Buffer.byteLength(request) > MAX_REQUEST_BYTES) return Promise.reject(new Error('Request is too large.'));
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${method.replaceAll('_', ' ')} timed out. The importer may still be finishing the operation.`));
      }, REQUEST_TIMEOUT);
      this.pending.set(id, { resolve, reject, timer });
      this.child.stdin.write(request, 'utf8', error => {
        if (!error) return;
        const entry = this.pending.get(id);
        if (entry) { this.pending.delete(id); clearTimeout(entry.timer); entry.reject(error); }
      });
    });
  }

  stop() {
    if (this.stopPromise) return this.stopPromise;
    this.stopping = true;
    for (const entry of this.pending.values()) { clearTimeout(entry.timer); entry.reject(new Error('Avolition is closing.')); }
    this.pending.clear();
    const child = this.child;
    this.stopPromise = new Promise(resolve => {
      if (child.exitCode !== null || child.signalCode !== null) { resolve(); return; }
      let killTimer;
      let finalTimer;
      const done = () => { clearTimeout(killTimer); clearTimeout(finalTimer); resolve(); };
      child.once('exit', done);
      // UI saves have finished before normal shutdown. Let the serial core see
      // EOF, then bound cleanup so neither a hidden core nor an app can linger.
      killTimer = setTimeout(() => {
        try { child.kill(); } catch (error) { log(`Core shutdown: ${error.message}`); }
        finalTimer = setTimeout(done, 1000);
      }, 1500);
      child.stdin.end();
    });
    return this.stopPromise;
  }
}

let mainWindow = null;
let backend = null;
let allowClose = false;
let closePromise = null;
let finishingSmoke = false;
let fatalShown = false;
let loaded = false;

async function requestClose() {
  if (allowClose || !mainWindow || mainWindow.isDestroyed()) return true;
  if (closePromise) return closePromise;
  closePromise = (async () => {
    try {
      if (loaded) {
        const prepared = await timeout(mainWindow.webContents.executeJavaScript(
          "typeof window.whitehoundPrepareClose === 'function' ? window.whitehoundPrepareClose() : ({ok:true})", true,
        ), REQUEST_TIMEOUT, 'Saving changes before close');
        if (prepared?.busy) return false;
        if (!prepared?.ok) throw new Error(prepared?.error || 'Changes could not be saved.');
      }
    } catch (error) {
      log(`Close save failed: ${error.stack || error}`);
      if (options.smokeReport) throw error;
      const choice = await dialog.showMessageBox(mainWindow, {
        type: 'error', title: 'Avolition', message: 'Avolition could not finish saving your changes.',
        detail: `${error.message}\n\nLog: ${logPath}`, buttons: ['Keep Avolition open', 'Close without saving'], defaultId: 0, cancelId: 0,
      });
      if (choice.response !== 1) return false;
    }
    await backend?.stop();
    allowClose = true;
    mainWindow?.close();
    return true;
  })().finally(() => { closePromise = null; });
  return closePromise;
}

function trustedSender(event) {
  if (!mainWindow || event.sender !== mainWindow.webContents || event.senderFrame !== mainWindow.webContents.mainFrame) return false;
  try { const url = new URL(event.senderFrame.url); return url.protocol === 'whitehound:' && url.host === 'app'; }
  catch (_) { return false; }
}

function setTextureFormats(event, formats) {
  if (!trustedSender(event) || !Array.isArray(formats)) return false;
  textureFormats = [...new Set(formats.filter(format => TEXTURE_FORMATS.has(format)))];
  return COMPRESSED_TEXTURES;
}

async function apiRequest(event, method, args) {
  if (!trustedSender(event) || !ALLOWED_METHODS.has(method) || !Array.isArray(args)) {
    return { ok: false, error: 'This operation is not available.' };
  }
  try {
    if (method === 'close_app') {
      setImmediate(() => { if (mainWindow && !mainWindow.isDestroyed()) mainWindow.close(); });
      return { ok: true, data: true };
    }
    if (method === 'browse_workflow_directory') {
      const kind = args[0];
      if (!['overwatchInstall', 'outputDirectory'].includes(kind)) throw new Error('Unknown directory control.');
      const settings = await backend.call('workflow_settings', []);
      if (!settings.ok) return settings;
      const choice = await dialog.showOpenDialog(mainWindow, {
        title: kind === 'overwatchInstall' ? 'Choose Overwatch installation' : 'Choose Toolchain output directory',
        defaultPath: settings.data[kind] || undefined,
        properties: kind === 'overwatchInstall' ? ['openDirectory'] : ['openDirectory', 'createDirectory'],
      });
      return { ok: true, data: choice.canceled ? null : choice.filePaths[0] || null };
    }
    if (method === 'open_toolchain_output' || method === 'open_export_output' || method === 'open_map_output') {
      const output = await backend.call(method, []);
      if (!output.ok) return output;
      const error = await shell.openPath(output.data);
      if (error) throw new Error(error);
      return { ok: true, data: true };
    }
    if (method === 'browse_root' || method === 'browse_output' || method === 'browse_entity' || method === 'browse_map') {
      const map = method === 'browse_map';
      const entity = method === 'browse_entity' || map;
      const choice = await dialog.showOpenDialog(mainWindow, {
        title: map ? 'Open exported map' : entity ? 'Open entity or model' : method === 'browse_output' ? 'Choose output folder' : 'Choose export root',
        properties: entity ? ['openFile'] : ['openDirectory', 'createDirectory'],
        ...(entity ? { filters: [{ name: 'Overwatch entity/model', extensions: map ? ['owmap'] : ['owentity', 'owmdl'] }] } : {}),
      });
      return { ok: true, data: choice.canceled ? null : choice.filePaths[0] || null };
    }
    const result = await backend.call(method, args);
    if (method === 'bootstrap' && result.ok && result.data) result.data.browserMode = false;
    return result;
  } catch (error) { log(`${method}: ${error.stack || error}`); return { ok: false, error: String(error.message || error).slice(0, 4000) }; }
}

async function installLocalProtocol(webRoot) {
  const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.txt': 'text/plain; charset=utf-8' };
  protocol.handle('whitehound', async request => {
    try {
      const url = new URL(request.url);
      if (url.host !== 'app' || request.method !== 'GET') return new Response('Not found', { status: 404 });
      const relative = decodeURIComponent(url.pathname).replace(/^\/+/, '') || 'index.html';
      const file = path.resolve(webRoot, relative);
      if (!within(webRoot, file) || !types[path.extname(file).toLowerCase()]) return new Response('Not found', { status: 404 });
      const response = await net.fetch(pathToFileURL(file).toString());
      const headers = new Headers(response.headers);
      headers.set('Content-Type', types[path.extname(file).toLowerCase()]);
      headers.set('Content-Security-Policy', CSP);
      headers.set('X-Content-Type-Options', 'nosniff');
      return new Response(response.body, { status: response.status, headers });
    } catch (error) { log(`Asset load: ${error.message}`); return new Response('Not found', { status: 404 }); }
  });
  session.defaultSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  session.defaultSession.setPermissionCheckHandler(() => false);
  session.defaultSession.on('will-download', event => event.preventDefault());
  session.defaultSession.webRequest.onBeforeRequest({ urls: ['http://*/*', 'https://*/*', 'ws://*/*', 'wss://*/*'] }, (_details, callback) => callback({ cancel: true }));
}

async function hashFile(filename) {
  const hash = crypto.createHash('sha256');
  for await (const chunk of fs.createReadStream(filename)) hash.update(chunk);
  return hash.digest('hex');
}

async function runSmoke() {
  const copyDirectory = path.join(dataDirectory, 'smoke-exports');
  const nickname = `smoke_setup ${Date.now()}`;
  const result = await timeout(mainWindow.webContents.executeJavaScript(`(async () => {
    const $ = id => document.getElementById(id);
    const wait = async (check, label) => {
      const until = performance.now() + 45000;
      while (performance.now() < until) {
        if (check()) return;
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      throw new Error(label + ': ' + ($('status-text')?.textContent || 'UI not ready'));
    };
    await wait(() => window.pywebview?.api && $('scan-root') && !$('scan-root').disabled, 'Bridge startup');
    const boot = await window.pywebview.api.bootstrap();
    if (!boot.ok || boot.data.browserMode) throw new Error('Native backend bootstrap failed');
    if (!$('viewport').getContext('webgl2')) throw new Error('WebGL2 is unavailable');
    if (typeof require !== 'undefined' || typeof process !== 'undefined') throw new Error('Renderer Node isolation failed');
    if (typeof window.whitehoundPrepareClose !== 'function') throw new Error('Close-save helper is missing');
    if (document.querySelector('#favorites-filter, #favorite-clip, .clip-favorite')) throw new Error('Favorites controls are still present');
    if (document.querySelector('#export-fbx, #fbx-up-axis')) throw new Error('Deferred FBX controls are still present');
    const report = {ok:true, bridge:true, webgl:true, isolated:true, browserMode:false, title:document.title, clipsPreviewed:0,
      removedLegacyControls:!document.querySelector('#clip-name,#export-clip,#export-c4d,#track-groups,#track-count-filter'), fbxControlsRemoved:true};
    const root = ${JSON.stringify(options.root || '')};
    if (root) {
      $('root-path').value = root;
      $('scan-root').click();
      await wait(() => !$('scan-root').disabled && !!$('entity-select').value, 'Folder scan');
      $('load-entity').click();
      await wait(() => $('viewport-empty').hidden && $('viewport-loading').hidden, 'Entity load');
      if (!document.querySelector('.inspector-panel #objects-menu') || $('objects-menu').hidden) throw new Error('The object hierarchy is missing from the right inspector.');
      if ($('objects-button')) throw new Error('The old Objects popover control is still present.');
      const meshControls = [...document.querySelectorAll('.object-mesh-row input[data-model-id][data-mesh-id]')];
      if (!meshControls.length) throw new Error('The object hierarchy has no mesh controls.');
      const allPolygons = $('polygon-count').textContent;
      $('objects-hide-all').click();
      if (!$('polygon-count').textContent.startsWith('Polygons: 0 / ')) throw new Error('Hide all did not update visible polygon count.');
      $('objects-show-all').click();
      if ($('polygon-count').textContent !== allPolygons) throw new Error('Show all did not restore the polygon count.');
      const scene = document.querySelector('#objects-list [data-kind="scene"]');
      const expander = scene?.querySelector('.object-tree-expander');
      expander?.click();
      if (scene?.getAttribute('aria-expanded') !== 'false') throw new Error('The object hierarchy did not collapse.');
      expander.click();
      if (scene.getAttribute('aria-expanded') !== 'true') throw new Error('The object hierarchy did not expand.');
      report.objectsMenu = {meshes:meshControls.length, permanent:true, hierarchy:true, hideAll:true, showAll:true};
      const rows = [...document.querySelectorAll('.clip-row')].slice(0, 2);
      if (!rows.length) throw new Error('No clips found in smoke fixture');
      for (const row of rows) {
        row.click();
        await wait(() => !$('timeline').disabled && $('viewport-loading').hidden, 'Clip load');
        $('timeline').value = String(Math.floor(Number($('timeline').max) / 2));
        $('timeline').dispatchEvent(new Event('input', {bubbles:true}));
        report.clipsPreviewed++;
      }
      report.sourceClip = $('detail-path').title;
      $('output-path').value = ${JSON.stringify(copyDirectory)};
      $('output-path').dispatchEvent(new Event('input', {bubbles:true}));
      $('output-path').dispatchEvent(new Event('blur'));
      // Save a named queue snapshot without requiring Cinema or writing exports.
      $('animation-set-name').value = ${JSON.stringify(nickname)};
      $('export-animation-set').click();
      await wait(() => $('animation-set-export-status').textContent.startsWith('Added to'), 'Add to export list');
      $('fbx-options-button').click();
      const checks = [...document.querySelectorAll('#export-queue-list input[type=checkbox]')];
      if (checks.length !== 1 || checks.some(check => check.checked)) throw new Error('Queued exports must start unchecked.');
      if (!$('export-selected').disabled) throw new Error('Export started without a selection.');
      report.queueEntries = checks.length;
      report.queueUnchecked = true;
      const savedQueue = await window.pywebview.api.bootstrap();
      if (!savedQueue.ok || savedQueue.data.exportQueue.length !== 1) throw new Error('Queue snapshot was not saved.');
      report.queueName = savedQueue.data.exportQueue[0].name;
      if (savedQueue.data.exportQueue[0].assignments.length !== 1) throw new Error('Single assignment was not captured.');
      $('close-export-menu').click();
      report.meshStats = $('mesh-stats').textContent;
      report.polygonCount = $('polygon-count').textContent;
      report.favoritesRemoved = !document.querySelector('#favorites-filter, #favorite-clip, .clip-favorite');
      report.match = $('match-message').textContent;
      report.timeline = {frame:$('frame-input').value, max:$('timeline').max};
    }
    report.status = $('status-text').textContent;
    return report;
  })()`, true), 210000, 'Desktop smoke');
  result.windowTitle = mainWindow.getTitle();
  result.url = mainWindow.webContents.getURL();
  result.appName = app.getName();
  result.appVersion = app.getVersion();
  if (result.windowTitle !== 'Avolition' || result.title !== 'Avolition' || result.appName !== 'Avolition') {
    throw new Error('The desktop or document title does not match Avolition.');
  }
  if (result.url !== APP_URL) throw new Error('The desktop UI did not load from its bundled application URL.');
  result.electron = process.versions.electron;
  result.chromium = process.versions.chrome;
  result.logPath = logPath;
  result.noImplicitExports = !fs.existsSync(copyDirectory) || fs.readdirSync(copyDirectory).length === 0;
  if (!result.noImplicitExports) throw new Error('Adding a setup wrote export files.');
  if (options.screenshot) {
    await new Promise(resolve => setTimeout(resolve, 250));
    const capture = await mainWindow.webContents.capturePage();
    if (capture.isEmpty()) throw new Error('Native screenshot is empty.');
    fs.mkdirSync(path.dirname(options.screenshot), { recursive: true });
    fs.writeFileSync(options.screenshot, capture.toPNG());
    result.screenshot = options.screenshot;
  }
  const saved = await timeout(mainWindow.webContents.executeJavaScript('window.whitehoundPrepareClose()', true), REQUEST_TIMEOUT, 'Smoke close-save');
  if (!saved?.ok) throw new Error(saved?.error || 'Close-save helper failed');
  result.closeSaved = true;
  return result;
}

async function finishSmoke(result) {
  if (finishingSmoke) return;
  finishingSmoke = true;
  try {
    fs.mkdirSync(path.dirname(options.smokeReport), { recursive: true });
    fs.writeFileSync(options.smokeReport, JSON.stringify(result, null, 2), 'utf8');
  } catch (error) { log(`Could not write smoke report: ${error.stack || error}`); result.ok = false; }
  allowClose = true;
  await backend?.stop();
  mainWindow?.destroy();
  app.exit(result.ok ? 0 : 1);
}

async function fatal(error) {
  log(error.stack || error);
  if (options.smokeReport) { await finishSmoke({ ok: false, error: error.message || String(error), logPath }); return; }
  if (fatalShown) return;
  fatalShown = true;
  dialog.showErrorBox('Avolition', `The desktop application could not continue.\n\n${error.message || error}\n\nLog: ${logPath}`);
  // No web browser or external process is opened as a fallback.
  allowClose = true;
  await backend?.stop();
  mainWindow?.destroy();
  app.quit();
}

async function start() {
  Menu.setApplicationMenu(null);
  nativeTheme.themeSource = 'dark';
  const webRoot = app.isPackaged ? path.join(process.resourcesPath, 'web') : path.resolve(__dirname, '..', 'web');
  const executable = process.env.AVOLITION_BACKEND_PATH || process.env.WHITEHOUND_BACKEND_PATH || process.env.OWIO_BACKEND_PATH
    || path.join(process.resourcesPath, 'backend', 'owio-core.exe');
  if (!fs.existsSync(path.join(webRoot, 'index.html'))) throw new Error(`Bundled UI was not found: ${webRoot}`);
  if (!fs.existsSync(executable)) throw new Error(`Local importer was not found: ${executable}`);
  backend = new Backend(path.resolve(executable));
  const check = await backend.call('bootstrap');
  if (!check.ok) throw new Error(check.error || 'The local importer could not initialize.');
  await installLocalProtocol(webRoot);
  ipcMain.handle('whitehound:api', apiRequest);
  ipcMain.handle('whitehound:texture-formats', setTextureFormats);
  mainWindow = new BrowserWindow({
    title: 'Avolition', width: 1440, height: 940, minWidth: 1060, minHeight: 700,
    icon: path.join(webRoot, 'assets', 'avolition.ico'),
    show: false, backgroundColor: '#0d0d10', autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, 'preload.cjs'), contextIsolation: true, nodeIntegration: false,
      sandbox: true, webSecurity: true, allowRunningInsecureContent: false, webviewTag: false,
      backgroundThrottling: !options.smokeReport && !options.testHidden,
    },
  });
  mainWindow.removeMenu();
  mainWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  mainWindow.webContents.on('will-navigate', event => event.preventDefault());
  mainWindow.webContents.on('will-frame-navigate', event => event.preventDefault());
  mainWindow.webContents.on('will-attach-webview', event => event.preventDefault());
  mainWindow.webContents.on('page-title-updated', event => { event.preventDefault(); mainWindow?.setTitle('Avolition'); });
  mainWindow.webContents.on('console-message', (_event, details) => log(`renderer: ${details.message || details}`));
  mainWindow.webContents.on('render-process-gone', (_event, details) => { if (!allowClose) void fatal(new Error(`Renderer stopped: ${details.reason}`)); });
  mainWindow.on('unresponsive', () => log('Renderer reported unresponsive.'));
  mainWindow.on('close', event => {
    if (allowClose) return;
    event.preventDefault();
    void requestClose().catch(fatal);
  });
  mainWindow.on('closed', () => { mainWindow = null; backend?.stop(); });
  mainWindow.once('ready-to-show', () => { if (!options.smokeReport && !options.testHidden) mainWindow.show(); });
  await timeout(mainWindow.loadURL(APP_URL), 30000, 'Desktop page load');
  loaded = true;
  if (options.smokeReport) await finishSmoke(await runSmoke());
}

app.on('before-quit', event => {
  if (!allowClose && mainWindow && !mainWindow.isDestroyed()) {
    event.preventDefault();
    void requestClose().catch(fatal);
  }
});
app.on('window-all-closed', () => { Promise.resolve(backend?.stop()).then(() => app.quit()); });
app.on('will-quit', () => backend?.stop());
process.on('uncaughtException', error => { void fatal(error); });
process.on('unhandledRejection', error => { void fatal(error instanceof Error ? error : new Error(String(error))); });
app.whenReady().then(start).catch(fatal);
