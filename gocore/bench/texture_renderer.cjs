'use strict';
// Texture benchmark of the --serve path in headless Chromium: PNG previews
// versus GPU block-compressed previews (README "Compressed textures"), through
// the renderer's path (MaterialLibrary requests and builds every texture of
// the scene's materials, then three.js uploads them and the GPU finishes).
//
//   PLAYWRIGHT_MODULE=$(npm root -g)/playwright \
//     node gocore/bench/texture_renderer.cjs <owio-core> <library> [reps]
//
// Cases: the Big character's 2048² textures and the Bench map's textures at
// the map preview size (mapPreviewMaterialOptions). Every repetition starts a
// fresh core and page, so the core's caches are cold and both paths decode
// and encode. Columns: time until the last reply arrived, until every texture
// was built (PNG: decoded by the browser), the upload of all of them (PNG:
// with generated mipmaps), the total, bytes received for load_texture, and
// the GPU memory the textures
// take (RGBA8 plus a full mip chain for PNGs; the compressed mips' size
// otherwise). Rendering uses SwiftShader (no GPU) here, so uploads cost CPU
// time a GPU driver would not; PNG decoding is CPU work in Chromium either
// way. Times are medians in ms.

const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const [core, library, repsText] = process.argv.slice(2);
if (!core || !library) { console.error('usage: texture_renderer.cjs <owio-core> <library> [reps]'); process.exit(2); }
const reps = Number(repsText) || 3;
const webRoot = path.resolve(__dirname, '../../web');
const entity = path.join(library, 'Heroes/Big/Skin0/Entities/Gameplay3P/Gameplay3P.owentity');
const map = path.join(library, 'Maps/Bench/Bench/Default/Bench.owmap');
const mapRoot = path.join(library, 'Maps/Bench');

async function measure(browser, mode, scene) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'avolition-texture-bench-'));
  const server = spawn(core, ['--serve', '--port', '0', '--data-dir', dataDir], {
    stdio: ['ignore', 'pipe', 'inherit'], env: { ...process.env, AVOLITION_WEB_ROOT: webRoot },
  });
  try {
    const address = await new Promise((resolve, reject) => {
      server.on('error', reject);
      server.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) resolve(match[0]); });
    });
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await page.goto(address);
    await page.waitForFunction(() => window.pywebview?.api && !document.querySelector('#scan-root').disabled);
    const result = await page.evaluate(async ([mode, scene, entity, map, mapRoot]) => {
      const THREE = await import('/vendor/three.module.js');
      const materials = await import('/materials.js');
      const canvas = document.createElement('canvas');
      canvas.width = 800; canvas.height = 600;
      document.body.append(canvas);
      const renderer = new THREE.WebGLRenderer({ canvas, powerPreference: 'high-performance' });
      const gl = renderer.getContext();
      const finish = () => gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array(4));
      const api = window.pywebview.api;
      const loaded = scene === 'map' ? await api.load_map(map, mapRoot) : await api.load_entity(entity);
      if (!loaded.ok) throw new Error(loaded.error);
      const asset = loaded.data;
      materials.setTextureFormatPreference(mode === 'png' ? [] : null);
      const options = scene === 'map' ? materials.mapPreviewMaterialOptions(asset) : {};
      const edge = scene === 'map' ? options.textureEdge : 2048;
      let gpuBytes = 0, count = 0, lastReply = 0, browser = 0;
      const formats = new Set();
      performance.setResourceTimingBufferSize(100000);
      performance.clearResourceTimings();
      const start = performance.now();
      const resolve = async id => {
        const reply = await api.load_texture(id, edge);
        lastReply = performance.now();
        if (!reply.ok) throw new Error(reply.error);
        const data = reply.data;
        count++;
        if (data.format) {
          formats.add(data.format);
          gpuBytes += data.mips.reduce((sum, mip) => sum + mip.data.byteLength, 0);
        } else {
          formats.add('png');
          gpuBytes += Math.round(data.width * data.height * 4 * 4 / 3);
        }
        return data;
      };
      // The viewer's path without drawing the meshes (SwiftShader would
      // dominate): MaterialLibrary requests, decodes and builds every
      // texture, then each is uploaded (with mipmaps) and the GPU finishes.
      const library = new materials.MaterialLibrary(resolve, { renderer, ...options });
      const descriptors = new Map();
      for (const model of asset.models || []) for (const descriptor of Object.values(model.materials || {})) {
        descriptors.set(JSON.stringify(descriptor), descriptor);
      }
      await Promise.all([...descriptors.values()].map(descriptor => library.createMaterial(descriptor, [0, 1])));
      const built = performance.now();
      for (const texture of library.textures) renderer.initTexture(texture);
      finish();
      const uploaded = performance.now();
      const bytes = performance.getEntriesByType('resource').filter(e => e.name.includes('/api/load_texture'))
        .reduce((sum, e) => sum + e.encodedBodySize, 0);
      const result = { frame: uploaded - start, replies: lastReply - start, built: built - start, upload: uploaded - built,
        bytes, gpuBytes, count, formats: [...formats].join('+'), warnings: library.warnings };
      library.dispose();
      renderer.dispose();
      return result;
    }, [mode, scene, entity, map, mapRoot]);
    await page.close();
    return result;
  } finally {
    server.kill();
  }
}

async function main() {
  const browser = await chromium.launch({ args: ['--enable-unsafe-swiftshader'] });
  const median = values => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
  const rows = [];
  try {
    for (const scene of ['entity', 'map']) {
      const samples = { png: [], compressed: [] };
      for (let rep = 0; rep < reps; rep++) {
        for (const mode of ['png', 'compressed']) samples[mode].push(await measure(browser, mode, scene));
      }
      for (const mode of ['png', 'compressed']) {
        const list = samples[mode];
        const row = { scene, mode, ...list[0] };
        for (const key of ['frame', 'replies', 'built', 'upload', 'bytes', 'gpuBytes']) row[key] = median(list.map(item => item[key]));
        rows.push(row);
      }
    }
  } finally {
    await browser.close();
  }
  const mb = value => (value / 1e6).toFixed(1);
  console.log('| textures | mode | formats | count | last reply (ms) | textures built (ms) | upload + finish (ms) | total (ms) | received | GPU memory |');
  console.log('|---|---|---|---:|---:|---:|---:|---:|---:|---:|');
  for (const row of rows) {
    const label = row.scene === 'map' ? 'Bench map' : 'Big character 2048²';
    console.log(`| ${label} | ${row.mode} | ${row.formats} | ${row.count} | ${row.replies.toFixed(0)} | ${row.built.toFixed(0)} | ${row.upload.toFixed(0)} | ${row.frame.toFixed(0)} | ${mb(row.bytes)} MB | ${mb(row.gpuBytes)} MB |`);
  }
  for (const row of rows) if (row.warnings.length) console.log(`${row.scene} ${row.mode} warnings: ${row.warnings.join('; ')}`);
}

main().catch(error => { console.error(error); process.exit(1); });
