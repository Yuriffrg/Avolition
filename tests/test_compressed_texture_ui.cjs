// Renders preview textures through the real renderer path twice: as PNGs
// decoded by the browser (GPU-generated mipmaps) and, when the core and the
// WebGL implementation support it, as block-compressed mips from the core.
// Both must look the same, at full size and minified, with the same
// orientation. Against the Python core (no compressed replies) both runs use
// PNGs and the test checks that nothing was negotiated.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');
const assert = require('node:assert/strict');
const source = path.resolve(__dirname, '..');
const base = path.resolve(source, '../preview-build');
fs.mkdirSync(base, {recursive:true});
const folder = fs.mkdtempSync(path.join(base, 'compressed-texture-ui-'));

// A 64×64 RGBA PNG with distinct quadrants (orientation), gradients, edges
// and, for odd variants, varying alpha.
function crc32(buffer) {
  let crc = ~0;
  for (const byte of buffer) { crc ^= byte; for (let k = 0; k < 8; k++) crc = crc >>> 1 ^ (0xedb88320 & -(crc & 1)); }
  return ~crc >>> 0;
}
function chunk(type, data) {
  const head = Buffer.alloc(8); head.writeUInt32BE(data.length); head.write(type, 4, 'ascii');
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])));
  return Buffer.concat([head, data, crc]);
}
function pattern(variant) {
  const size = 64, rows = [];
  for (let y = 0; y < size; y++) {
    const row = [0];
    for (let x = 0; x < size; x++) {
      const top = y < size / 2, left = x < size / 2;
      let r = top ? (left ? 230 : 40) : (left ? 30 : 200), g = top ? 40 + 3 * x : 180 - 2 * x, b = Math.min(255, x * 2 + y + variant * 20);
      if ((x + y) % 16 === 0) r = g = b = 255;
      const a = variant % 2 ? Math.round(255 * (0.5 + 0.5 * Math.sin(x / 5 + y / 9))) : 255;
      row.push(r, Math.min(255, g), b, a);
    }
    rows.push(Buffer.from(row));
  }
  const header = Buffer.alloc(13); header.writeUInt32BE(size); header.writeUInt32BE(size, 4); header.set([8, 6, 0, 0, 0], 8);
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', header),
    chunk('IDAT', zlib.deflateSync(Buffer.concat(rows))), chunk('IEND', Buffer.alloc(0))]);
}
const assets = path.join(folder, 'assets');
fs.cpSync(path.join(source, 'tests/material-assets'), assets, {recursive:true});
const textureDir = path.join(assets, 'Models/MaterialRobot.00C/Textures');
fs.readdirSync(textureDir).forEach((name, index) => fs.writeFileSync(path.join(textureDir, name), pattern(index)));

const server = spawn(process.env.WHITEHOUND_TEST_PYTHON || process.env.OWIO_TEST_PYTHON || 'python', [path.join(source,'app.py'),'--serve','--port','0','--data-dir',path.join(folder,'profile')], {
  windowsHide:true, stdio:['ignore','pipe','pipe'], env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}
});
let browser;
let errorLog = '';
server.stderr.on('data', chunk => {errorLog += chunk.toString();});
(async () => {
  const address = await new Promise((resolve,reject) => {
    const timer=setTimeout(()=>reject(new Error('Server startup timed out: '+errorLog)),15000);
    server.once('error',reject);
    server.stdout.on('data', chunk => {
      const match=chunk.toString().match(/http:\/\/127\.0\.0\.1:\d+/);
      if(match){clearTimeout(timer);resolve(match[0]);}
    });
  });
  browser = await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page=await browser.newPage({viewport:{width:800,height:600},deviceScaleFactor:1});
  const errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  // three.js reports unsupported compressed formats as warnings; driver
  // performance notes (readPixels stalls) are expected here.
  page.on('console',message=>{
    if((message.type()==='warning'||message.type()==='error')&&!/GL Driver Message/.test(message.text()))errors.push(message.text());
  });
  await page.goto(address);
  await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled);
  const entity = path.join(assets, 'Entities/MaterialRobot/MaterialRobot.owentity');
  const result = await page.evaluate(async entity => {
    const THREE = await import('/vendor/three.module.js');
    const materials = await import('/materials.js');
    const api = window.pywebview.api;
    const loaded = await api.load_entity(entity);
    if (!loaded.ok) throw new Error(loaded.error);
    // Every (texture, role, colour space) the fixture's materials use.
    const uses = new Map();
    for (const model of loaded.data.models || []) for (const descriptor of Object.values(model.materials || {})) {
      for (const [role, map] of Object.entries(descriptor.maps || {})) {
        uses.set(JSON.stringify([map.textureId, role, map.colorSpace]), {id: map.textureId, role, colorSpace: map.colorSpace});
      }
    }
    const property = {baseColor: 'map', normal: 'normalMap', roughness: 'roughnessMap', metalness: 'metalnessMap',
      emissive: 'emissiveMap', alpha: 'alphaMap', ao: 'aoMap'};
    const canvas = document.createElement('canvas');
    const renderer = new THREE.WebGLRenderer({canvas, preserveDrawingBuffer:true, antialias:false});
    const gl = renderer.getContext();
    const scene = new THREE.Scene(), camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 0, 1);
    const plane = new THREE.Mesh(new THREE.PlaneGeometry(2, 2), new THREE.MeshBasicMaterial());
    scene.add(plane);
    const draw = (texture, size) => {
      canvas.width = canvas.height = size;
      renderer.setSize(size, size, false);
      plane.material.dispose();
      plane.material = new THREE.MeshBasicMaterial({map: texture, transparent: false});
      renderer.render(scene, camera);
      const pixels = new Uint8Array(size * size * 4);
      gl.readPixels(0, 0, size, size, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
      return Array.from(pixels);
    };
    const run = async preference => {
      materials.setTextureFormatPreference(preference);
      const resolve = async id => { const reply = await api.load_texture(id, 64); if (!reply.ok) throw new Error(reply.error); return reply.data; };
      const library = new materials.MaterialLibrary(resolve, {renderer});
      const formats = await library.formats;
      const out = {formats, textures: []};
      for (const {id, role, colorSpace} of uses.values()) {
        const {material} = await library.createMaterial({alphaMode: 'MASK', maps: {[role]: {textureId: id, colorSpace}}}, [0]);
        const texture = material[property[role]];
        out.textures.push({id, role, colorSpace: texture.colorSpace, compressed: Boolean(texture.isCompressedTexture),
          format: texture.format, flipY: texture.flipY, full: draw(texture, 64), small: draw(texture, 8)});
      }
      library.dispose();
      return out;
    };
    const supported = materials.compressedTextureFormats(renderer);
    const png = await run([]);
    const bc7 = await run(null);
    // The S3TC path (BC1 for opaque previews, BC3 with alpha), as without BPTC.
    const s3tc = supported.includes('bc1') ? await run(['bc3', 'bc1']) : null;
    materials.setTextureFormatPreference(null);
    renderer.dispose();
    return {png, bc7, s3tc, supported, hostNegotiates: typeof window.pywebview.setTextureFormats === 'function'};
  }, entity);
  assert.deepEqual(errors, []);
  assert.ok(result.png.textures.length >= 7 && result.png.textures.length === result.bc7.textures.length);
  assert.ok(result.png.textures.some(texture => texture.colorSpace === 'srgb'), 'covers sRGB colour maps');
  assert.ok(result.png.textures.some(texture => texture.colorSpace !== 'srgb'), 'covers linear maps');
  assert.ok(result.png.textures.every(texture => !texture.compressed && texture.flipY));
  const compressing = result.hostNegotiates && result.supported.length > 0;
  const runs = [['default', result.bc7, 3, 6]];
  if (compressing) {
    assert.deepEqual(result.bc7.formats, result.supported);
    assert.ok(result.bc7.textures.every(texture => texture.compressed && !texture.flipY), 'every texture arrives compressed');
    if (result.s3tc) {
      assert.deepEqual(result.s3tc.formats, ['bc3', 'bc1']);
      const formats = new Set(result.s3tc.textures.map(texture => texture.format));
      assert.ok(result.s3tc.textures.every(texture => texture.compressed && !texture.flipY));
      // Preview maps are RGB (alpha is its own scalar map), so they use BC1.
      assert.deepEqual([...formats], [33776], 'BC1 (RGB_S3TC_DXT1)');
      // BC1 has only 4 colours per block, which shows on tiny mips of this
      // busy pattern (each 8×8-level texel averages 64 source texels).
      runs.push(['s3tc', result.s3tc, 4, 10]);
    }
  } else {
    assert.ok(result.bc7.textures.every(texture => !texture.compressed), 'no compressed replies without negotiation');
  }
  const compare = (a, b) => {
    let sum = 0, worst = 0;
    for (let i = 0; i < a.length; i++) {
      if (i % 4 === 3) continue;
      const d = Math.abs(a[i] - b[i]); sum += d; worst = Math.max(worst, d);
    }
    return {mean: sum / (a.length * 3 / 4), worst};
  };
  const report = [];
  for (const [name, run, fullLimit, smallLimit] of runs) {
    for (let i = 0; i < result.png.textures.length; i++) {
      const p = result.png.textures[i], c = run.textures[i];
      const full = compare(p.full, c.full), small = compare(p.small, c.small);
      report.push(`${name} ${c.role} ${c.colorSpace || 'linear'} format ${c.format}: 64px mean ${full.mean.toFixed(2)} max ${full.worst}, 8px mean ${small.mean.toFixed(2)} max ${small.worst}`);
      // Block compression is lossy; a flipped or misread texture differs by
      // ~100 on average, so a small mean difference shows that orientation,
      // colour space, format and mip levels all match the PNG path.
      if (process.env.DEBUG_REPORT) continue;
      assert.ok(full.mean < fullLimit, `${name} ${p.id} ${p.role}: full-size mean difference ${full.mean}`);
      assert.ok(small.mean < smallLimit, `${name} ${p.id} ${p.role}: minified mean difference ${small.mean}`);
    }
  }
  console.log(report.join('\n'));
  console.log(`compressed texture UI checks passed (${compressing ? 'compressed: ' + result.supported.join(', ') : 'PNG only'})`);
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(async () => {
  await browser?.close();
  server.kill();
});
