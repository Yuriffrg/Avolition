'use strict';
// Map rendering benchmark in headless Chromium against the Go core's --serve:
// loads the benchmark map into a PreviewViewport, loads its textures the way
// the Maps tab does, then orbits the camera and reports draw calls,
// triangles, frame times, time to first frame and JS heap.
//
//   PLAYWRIGHT_MODULE=$(npm root -g)/playwright \
//     node gocore/bench/render_map.cjs <owio-core> <library> [frames] [--baseline <old web dir>] [--out <dir>]
//
// --baseline runs the same steps with another copy of web/ (e.g. the previous
// commit's, from `git archive`) and prints both rows plus a pixel comparison
// of the textured map from the same camera. --out saves those screenshots.
//
// Chromium renders with SwiftShader here (no GPU): draw-call and triangle
// counts are exact, frame times only show CPU-side cost (draw submission is
// included, but software rasterisation dominates "frame incl. raster", so keep
// the frame count small: one frame of the 11M-triangle map takes ~25 s).

const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const args = process.argv.slice(2);
const option = name => { const i = args.indexOf(name); return i < 0 ? null : args.splice(i, 2)[1]; };
const baseline = option('--baseline'), outDir = option('--out');
const [core, library, framesText] = args;
if (!core || !library) { console.error('usage: render_map.cjs <owio-core> <library> [frames] [--baseline <web dir>] [--out <dir>]'); process.exit(2); }
const frames = Number(framesText) || 8;
const map = path.join(library, 'Maps/Bench/Bench/Default/Bench.owmap');
const mapRoot = path.join(library, 'Maps/Bench');

async function measure(browser, webRoot) {
  const log = message => process.stderr.write(`[${path.basename(path.dirname(webRoot))}/web] ${message}\n`);
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'avolition-render-bench-'));
  const server = spawn(core, ['--serve', '--port', '0', '--data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, AVOLITION_WEB_ROOT: webRoot },
  });
  try {
    const address = await new Promise((resolve, reject) => {
      server.on('error', reject);
      server.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) resolve(match[0]); });
    });
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const cdp = await page.context().newCDPSession(page);
    const heap = async () => { await cdp.send('HeapProfiler.collectGarbage'); return (await cdp.send('Runtime.getHeapUsage')).usedSize; };
    await page.goto(address);
    await page.waitForFunction(() => window.pywebview?.api && !document.querySelector('#scan-root').disabled);
    await page.evaluate(async () => {
      const { PreviewViewport } = await import('/viewer.js');
      const host = document.createElement('div');
      host.style.cssText = 'position:fixed;left:0;top:0;width:960px;height:600px;z-index:99999';
      const canvas = document.createElement('canvas');
      host.append(canvas);
      document.body.append(host);
      window.benchViewport = new PreviewViewport(canvas);
      window.benchViewport.renderer.setAnimationLoop(null);
    });
    const heapBefore = await heap();
    log('loading map and textures');
    const loaded = await page.evaluate(async ([mapPath, root]) => {
      const { mapPreviewMaterialOptions } = await import('/materials.js');
      const viewport = window.benchViewport, gl = viewport.renderer.getContext();
      const finish = () => gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array(4));
      const start = performance.now();
      const result = await window.pywebview.api.load_map(mapPath, root);
      if (!result?.ok) throw new Error(`load_map: ${result.error}`);
      const reply = performance.now();
      const stats = viewport.loadAsset(result.data);
      viewport.invalidate();
      viewport._renderFrame();
      finish();
      const firstFrame = performance.now();
      const options = mapPreviewMaterialOptions(result.data);
      const materials = await viewport.loadMaterials(result.data,
        id => window.pywebview.api.load_texture(id, options.textureEdge).then(reply => {
          if (!reply?.ok) throw new Error(reply?.error); return reply.data;
        }), options);
      viewport.invalidate();
      viewport._renderFrame();
      finish();
      window.benchAsset = result.data; // the Maps tab keeps the asset while shown
      return { reply: reply - start, firstFrame: firstFrame - start, sceneAndFrame: firstFrame - reply,
        textured: performance.now() - start, placements: stats.models, meshes: stats.meshes, texturedMeshes: materials.texturedMeshes };
    }, [map, mapRoot]);
    const heapAfter = await heap();
    log(`textured after ${loaded.textured.toFixed(0)} ms; orbiting`);
    const orbit = await page.evaluate(async count => {
      const viewport = window.benchViewport, gl = viewport.renderer.getContext();
      const finish = () => gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array(4));
      const { camera, controls } = viewport;
      const target = controls.target.clone(), offset = camera.position.clone().sub(target);
      const radius = Math.hypot(offset.x, offset.z), height = offset.y;
      // An untimed warm-up frame compiles any remaining shader variants.
      const views = [];
      for (let i = 0; i < count; i++) views.push(Math.atan2(offset.x, offset.z) + i / count * Math.PI * 2);
      const place = angle => {
        camera.position.set(target.x + Math.sin(angle) * radius, target.y + height, target.z + Math.cos(angle) * radius);
        camera.lookAt(target);
        camera.updateMatrixWorld();
      };
      for (const angle of views.slice(0, 1)) { place(angle); viewport.invalidate(); viewport._renderFrame(); finish(); }
      let submit = 0, total = 0, calls = 0, triangles = 0;
      for (const angle of views) {
        place(angle);
        const t0 = performance.now();
        // What _renderFrame does after a camera move (minus the controls).
        viewport._updateCameraClipping();
        viewport._mapBatches?.cull(viewport.camera);
        viewport.renderer.render(viewport.scene, viewport.camera);
        const t1 = performance.now();
        finish();
        const t2 = performance.now();
        submit += t1 - t0; total += t2 - t0;
        calls += viewport.renderer.info.render.calls; triangles += viewport.renderer.info.render.triangles;
      }
      // Screenshots: the framed overview and a close view of its centre.
      const shots = {};
      for (const [name, scale] of [['overview', 1], ['close', .06]]) {
        place(views[0]);
        camera.position.sub(target).multiplyScalar(scale).add(target);
        camera.lookAt(target); camera.updateMatrixWorld();
        viewport.invalidate(); viewport._renderFrame();
        const { width, height: h } = gl.canvas;
        const pixels = new Uint8Array(width * h * 4);
        gl.readPixels(0, 0, width, h, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
        const canvas = document.createElement('canvas');
        canvas.width = width; canvas.height = h;
        const context = canvas.getContext('2d'), image = context.createImageData(width, h);
        for (let y = 0; y < h; y++) image.data.set(pixels.subarray((h - 1 - y) * width * 4, (h - y) * width * 4), y * width * 4);
        context.putImageData(image, 0, 0);
        shots[name] = { calls: viewport.renderer.info.render.calls, triangles: viewport.renderer.info.render.triangles, width, height: h, png: canvas.toDataURL('image/png'), pixels: Array.from(image.data) };
      }
      return { submit: submit / count, total: total / count, calls: calls / count, triangles: triangles / count, shots };
    }, frames);
    await page.close();
    return { ...loaded, ...orbit, heap: heapAfter - heapBefore };
  } finally {
    server.kill();
  }
}

function compare(a, b) {
  let max = 0, sum = 0, changed = 0;
  for (let i = 0; i < a.pixels.length; i += 4) {
    let pixel = 0;
    for (let c = 0; c < 3; c++) pixel = Math.max(pixel, Math.abs(a.pixels[i + c] - b.pixels[i + c]));
    max = Math.max(max, pixel); sum += pixel; changed += pixel > 2;
  }
  const count = a.pixels.length / 4;
  return { max, mean: sum / count, changedPercent: changed / count * 100 };
}

async function main() {
  const browser = await chromium.launch({ args: ['--enable-unsafe-swiftshader', '--js-flags=--expose-gc'] });
  try {
    const runs = [];
    if (baseline) runs.push(['baseline', path.resolve(baseline)]);
    runs.push(['current', path.resolve(__dirname, '../../web')]);
    const results = [];
    for (const [label, webRoot] of runs) results.push({ label, ...(await measure(browser, webRoot)) });
    const first = results[0];
    console.log(`Bench map: ${first.placements.toLocaleString()} placements, ${first.meshes.toLocaleString()} meshes, ${first.texturedMeshes.toLocaleString()} textured; ${frames} orbit frames, ${first.shots.overview.width}×${first.shots.overview.height}, SwiftShader (no GPU).`);
    console.log('| web | draw calls | triangles | frame: submit (ms) | frame incl. raster (ms) | load_map reply (ms) | scene + first frame (ms) | first frame (ms) | textured (ms) | JS heap (MB) |');
    console.log('|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|');
    for (const row of results) {
      console.log(`| ${row.label} | ${row.calls.toFixed(0)} | ${Math.round(row.triangles).toLocaleString()} | ${row.submit.toFixed(2)} | ${row.total.toFixed(1)} | ${row.reply.toFixed(0)} | ${row.sceneAndFrame.toFixed(0)} | ${row.firstFrame.toFixed(0)} | ${row.textured.toFixed(0)} | ${(row.heap / 1e6).toFixed(1)} |`);
    }
    if (results.length === 2) {
      for (const name of Object.keys(results[0].shots)) {
        const diff = compare(results[0].shots[name], results[1].shots[name]);
        console.log(`${name} view: ${results.map(row => `${row.label} ${row.shots[name].calls} calls, ${row.shots[name].triangles.toLocaleString()} triangles`).join('; ')}. Pixels (same camera, textured): max channel difference ${diff.max}/255, mean ${diff.mean.toFixed(4)}, ${diff.changedPercent.toFixed(3)}% of pixels differ by more than 2.`);
      }
    }
    if (outDir) {
      fs.mkdirSync(outDir, { recursive: true });
      for (const row of results) for (const [name, shot] of Object.entries(row.shots)) {
        fs.writeFileSync(path.join(outDir, `map-${name}-${row.label}.png`), Buffer.from(shot.png.split(',')[1], 'base64'));
      }
    }
  } finally {
    await browser.close();
  }
}

main().catch(error => { console.error(error); process.exit(1); });
