'use strict';
// End-to-end renderer benchmark of the --serve path in headless Chromium:
// JSON replies vs binary frames, from the request to typed arrays in the
// renderer and to the first rendered frame.
//
//   PLAYWRIGHT_MODULE=$(npm root -g)/playwright \
//     node gocore/bench/transport_renderer.cjs <owio-core> <library> [reps]
//
// Binary mode calls the page's injected bridge (window.pywebview.api), which
// asks for frames; JSON mode posts the same request without the
// X-Avolition-Binary header and reads response.json(), as the bridge did
// before. "typed" adds the conversion of every mesh/track array into the typed
// array the viewer needs (none for binary); "frame" adds PreviewViewport
// .loadAsset() and one rendered, finished frame (textures: TextureLoader
// decode, upload and a frame). Rendering uses SwiftShader (no GPU), so the
// frame column overstates GPU upload costs. Times are medians in ms.

const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const [core, library, repsText] = process.argv.slice(2);
if (!core || !library) { console.error('usage: transport_renderer.cjs <owio-core> <library> [reps]'); process.exit(2); }
const reps = Number(repsText) || 3;
const webRoot = path.resolve(__dirname, '../../web');
const entity = path.join(library, 'Heroes/Big/Skin0/Entities/Gameplay3P/Gameplay3P.owentity');
const map = path.join(library, 'Maps/Bench/Bench/Default/Bench.owmap');
const mapRoot = path.join(library, 'Maps/Bench');

async function main() {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'avolition-renderer-bench-'));
  const server = spawn(core, ['--serve', '--port', '0', '--data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, AVOLITION_WEB_ROOT: webRoot },
  });
  const address = await new Promise((resolve, reject) => {
    server.on('error', reject);
    server.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) resolve(match[0]); });
  });
  const browser = await chromium.launch({ args: ['--enable-unsafe-swiftshader', '--js-flags=--expose-gc'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await page.goto(address);
    await page.waitForFunction(() => window.pywebview?.api && !document.querySelector('#scan-root').disabled);
    await page.evaluate(async () => {
      const THREE = await import('/vendor/three.module.js');
      const { PreviewViewport } = await import('/viewer.js');
      const host = document.createElement('div');
      host.style.cssText = 'position:fixed;left:0;top:0;width:800px;height:600px;z-index:99999';
      const canvas = document.createElement('canvas');
      host.append(canvas);
      document.body.append(host);
      const viewport = new PreviewViewport(canvas);
      viewport.renderer.setAnimationLoop(null);
      const gl = viewport.renderer.getContext();
      const finish = () => gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array(4));
      const token = [...document.scripts].map(s => s.textContent).join('').match(/"X-Preview-Token":"([^"]+)"/)[1];
      const json = (method, args) => fetch('/api/' + method, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Preview-Token': token }, body: JSON.stringify(args),
      }).then(r => r.json());
      const call = (binary, method, args) => binary ? window.pywebview.api[method](...args) : json(method, args);
      performance.setResourceTimingBufferSize(100000);
      const lastBytes = () => performance.getEntriesByType('resource').filter(e => e.name.includes('/api/')).at(-1)?.encodedBodySize || 0;
      // What the viewer needs: each mesh/track array as a typed array (made
      // and dropped here: the viewer converts JSON arrays itself).
      const TYPES = { positions: Float32Array, normals: Float32Array, uvs: Float32Array, uvs2: Float32Array,
        indices: Uint32Array, skinIndices: Uint16Array, skinWeights: Float32Array, frames: Uint32Array, values: Float64Array };
      const typed = value => {
        let bytes = 0;
        const walk = node => {
          if (!node || typeof node !== 'object' || ArrayBuffer.isView(node)) return;
          if (Array.isArray(node)) { for (const item of node) walk(item); return; }
          for (const key in node) {
            const item = node[key];
            if (TYPES[key] && Array.isArray(item) && (item.length === 0 || typeof item[0] === 'number')) bytes += new TYPES[key](item).byteLength;
            else if (ArrayBuffer.isView(item)) bytes += item.byteLength;
            else walk(item);
          }
        };
        walk(value);
        return bytes;
      };
      window.benchReset = () => { window.lastAsset = null; viewport._clear(); };
      window.benchAsset = async (binary, method, args) => {
        performance.clearResourceTimings();
        const start = performance.now();
        const result = await call(binary, method, args);
        const reply = performance.now();
        if (!result?.ok) throw new Error(`${method}: ${result?.error}`);
        const transferred = lastBytes();
        const arrayBytes = typed(result.data);
        const typedAt = performance.now();
        const frameStart = performance.now();
        viewport.loadAsset(result.data);
        viewport.renderer.render(viewport.scene, viewport.camera);
        finish();
        const frame = reply - start + performance.now() - frameStart;
        window.lastAsset = result.data; // The app keeps the asset while it is shown.
        return { reply: reply - start, typed: typedAt - start, frame, bytes: transferred, arrayBytes };
      };
      const material = new THREE.MeshBasicMaterial();
      const quad = new THREE.Mesh(new THREE.PlaneGeometry(2, 2), material);
      const scene = new THREE.Scene();
      scene.add(quad);
      const camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 0, 2);
      camera.position.z = 1;
      window.benchTexture = async (binary, id) => {
        performance.clearResourceTimings();
        const start = performance.now();
        const result = await call(binary, 'load_texture', [id, 2048]);
        const reply = performance.now();
        if (!result?.ok) throw new Error(`load_texture: ${result?.error}`);
        const transferred = lastBytes();
        // materials.js: PNG bytes through a blob URL, or the data URI.
        const png = result.data.png;
        const url = png ? URL.createObjectURL(new Blob([png], { type: 'image/png' })) : result.data.dataUri;
        const texture = await new THREE.TextureLoader().loadAsync(url);
        if (png) URL.revokeObjectURL(url);
        texture.colorSpace = THREE.SRGBColorSpace;
        material.map?.dispose();
        material.map = texture;
        material.needsUpdate = true;
        viewport.renderer.render(scene, camera);
        finish();
        return { reply: reply - start, typed: reply - start, frame: performance.now() - start, bytes: transferred, arrayBytes: png?.byteLength || 0 };
      };
    });

    const cdp = await page.context().newCDPSession(page);
    // JS heap after a full GC (typed array bytes live outside it).
    const memory = async () => {
      await cdp.send('HeapProfiler.collectGarbage');
      const usage = await cdp.send('Runtime.getHeapUsage');
      return usage.usedSize;
    };
    const withMemory = async fn => {
      await page.evaluate(() => window.benchReset());
      const before = await memory();
      const result = await fn();
      return { ...result, heap: await memory() - before };
    };
    const median = values => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
    const rows = [];
    const run = async (label, fn) => {
      const samples = { json: [], binary: [] };
      for (let rep = 0; rep < reps; rep++) {
        for (const mode of ['json', 'binary']) samples[mode].push(await fn(mode === 'binary'));
      }
      for (const mode of ['json', 'binary']) {
        const list = samples[mode];
        rows.push({ label, mode, ...Object.fromEntries(Object.keys(list[0]).map(key => [key, median(list.map(item => item[key]))])) });
      }
    };
    await run('load_entity Big', binary => withMemory(() => page.evaluate(([b, p]) => window.benchAsset(b, 'load_entity', [p]), [binary, entity])));
    const textures = await page.evaluate(() => Object.keys(window.lastAsset.textures || {}).slice(0, 3));
    await run('load_map Bench', binary => withMemory(() => page.evaluate(([b, p, r]) => window.benchAsset(b, 'load_map', [p, r]), [binary, map, mapRoot])));
    await page.evaluate(([p]) => window.benchAsset(true, 'load_entity', [p]), [entity]); // re-register the Big textures
    await run('load_texture 2048 (x3)', async binary => {
      const results = [];
      for (const id of textures) results.push(await page.evaluate(([b, t]) => window.benchTexture(b, t), [binary, id]));
      return { ...Object.fromEntries(Object.keys(results[0]).map(key => [key, results.reduce((sum, item) => sum + item[key], 0)])), heap: 0 };
    });
    const mb = value => (value / 1e6).toFixed(1);
    console.log('| reply | mode | transferred | reply object (ms) | typed arrays in renderer (ms) | first frame (ms) | JS heap held (MB) | typed array bytes (MB) |');
    console.log('|---|---|---:|---:|---:|---:|---:|---:|');
    for (const row of rows) {
      console.log(`| ${row.label} | ${row.mode} | ${mb(row.bytes)} MB | ${row.reply.toFixed(0)} | ${row.typed.toFixed(0)} | ${row.frame.toFixed(0)} | ${row.heap ? mb(row.heap) : '—'} | ${mb(row.arrayBytes)} |`);
    }
  } finally {
    await browser.close();
    server.kill();
  }
}

main().catch(error => { console.error(error); process.exit(1); });
