import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {MaterialLibrary} from '../web/materials.js';
import {PreviewViewport, DEFAULT_VIEW_OPTIONS} from '../web/viewer.js';

let checks = 0;
const equal = (a, b, label) => { assert.equal(a, b, label); checks++; };
const ok = (value, label) => { assert.ok(value, label); checks++; };
const png = id => ({dataUri: `data:image/png;base64,${id}`, width: 2, height: 2});
const loader = async () => new THREE.Texture({width: 2, height: 2});
const map = (id, uvIndex = 0, colorSpace) => ({textureId: id, uvIndex, colorSpace});
const fetches = [];
const library = new MaterialLibrary(async id => { fetches.push(id); return png(id); }, {loadTexture: loader, maxAnisotropy: 16});
const descriptor = {name: 'packed channels', maps: {
  baseColor: map('base'), normal: map('normal'), roughness: map('rough'), metalness: map('metal'),
  ao: map('ao', 1), emissive: map('emission', 0, 'linear'), alpha: map('alpha'),
}};
const {material, hasTextures} = await library.createMaterial(descriptor, [0, 1]);
equal(hasTextures, true);
equal(material.map.colorSpace, THREE.SRGBColorSpace);
equal(material.normalMap.colorSpace, THREE.NoColorSpace);
equal(material.emissiveMap.colorSpace, THREE.NoColorSpace);
equal(material.roughnessMap.colorSpace, THREE.NoColorSpace);
equal(material.metalnessMap.colorSpace, THREE.NoColorSpace);
equal(material.aoMap.channel, 1);
equal(material.normalMap.flipY, true);
equal(material.map.wrapS, THREE.RepeatWrapping);
equal(material.map.wrapT, THREE.RepeatWrapping);
equal(material.map.anisotropy, 8);
equal(material.metalness, 1, 'metalness map cannot be multiplied by zero');
equal(material.roughness, 1, 'roughness map is already decoded');
equal(material.emissive.r, 1);
equal(material.normalScale.y, 1, 'decoded OW normals require no extra green flip');
equal(material.transparent, false);
equal(material.alphaTest, 0);
equal(material.alphaMap, null, 'packed alpha must not become opacity for opaque material');
equal(fetches.includes('alpha'), false);
equal((await library.createMaterial(descriptor, [1, 0])).material, material, 'descriptor/UV cache');
const uvVariant = await library.createMaterial({maps: {baseColor: map('base', 1)}}, [1]);
equal(fetches.filter(id => id === 'base').length, 1, 'decode source only once');
ok(uvVariant.material.map !== material.map, 'UV variant has its own sampler');
equal(uvVariant.material.map.image, material.map.image, 'variants share image storage');
equal(uvVariant.material.map.channel, 1);
const linearVariant = await library.createMaterial({maps: {roughness: map('base')}}, [0]);
equal(linearVariant.material.roughnessMap.colorSpace, THREE.NoColorSpace);
equal(material.map.colorSpace, THREE.SRGBColorSpace, 'linear reuse cannot mutate albedo sampler');
const tiled = (await library.createMaterial({maps: {baseColor: {...map('base'), repeat: [2, 3]}}})).material;
equal(tiled.map.repeat.x, 2); equal(tiled.map.repeat.y, 3);
equal(material.map.repeat.x, 1, 'tiled reuse must not mutate an untiled sampler');
ok(tiled.map !== material.map);
equal(fetches.filter(id => id === 'base').length, 1);

const masked = (await library.createMaterial({alphaMode: 'MASK', alphaCutoff: .4, maps: {alpha: map('alpha')}})).material;
equal(masked.alphaTest, .4);
equal(masked.transparent, false);
ok(masked.alphaMap);
const blended = (await library.createMaterial({alphaMode: 'BLEND', opacity: .3})).material;
equal(blended.transparent, true);
equal(blended.depthWrite, false);
equal(blended.opacity, .3);
const unknownAlpha = (await library.createMaterial({alphaMode: 'unexpected', opacity: .1})).material;
equal(unknownAlpha.transparent, false);
equal(unknownAlpha.opacity, 1);
const missingUV = await library.createMaterial({maps: {baseColor: map('no-uv', 1)}}, [0]);
equal(missingUV.hasTextures, false);
equal(fetches.includes('no-uv'), false);
ok(library.warnings.some(w => w.includes('missing UV channel 2')));

for (const useAO of [true, false]) {
  const emission = (await library.createMaterial({emissiveMode: 'baseColorMask', emissiveUsesAO: useAO, emissiveIntensity: 16,
    maps: {baseColor: map('base'), ao: map('ao', 1), emissive: map('emission', 0, 'linear')}}, [0, 1])).material;
  const shader = {fragmentShader: THREE.ShaderLib.standard.fragmentShader};
  emission.onBeforeCompile(shader);
  ok(shader.fragmentShader.includes('totalEmissiveRadiance *= diffuseColor.rgb;'));
  equal(shader.fragmentShader.includes('totalEmissiveRadiance *= texture2D( aoMap, vAoMapUv ).r;'), useAO);
  equal(emission.emissiveIntensity, 16);
  ok(emission.customProgramCacheKey().endsWith(`ao${Number(useAO)}`));
}

// Disposal covers both inactive texture variants and materials shared by meshes.
const disposals = new Map();
for (const item of [...library.textures, ...library.materials]) {
  disposals.set(item, 0);
  item.addEventListener('dispose', () => disposals.set(item, disposals.get(item) + 1));
}
library.dispose(); library.dispose();
for (const count of disposals.values()) equal(count, 1);

let active = 0, peak = 0;
const limited = new MaterialLibrary(async id => {
  active++; peak = Math.max(peak, active);
  await new Promise(resolve => setTimeout(resolve, 1));
  active--; return png(id);
}, {loadTexture: loader, concurrency: 2, maxTextures: 3, maxPixels: 8});
const loaded = await Promise.all(Array.from({length: 12}, (_, i) => limited.createMaterial({maps: {baseColor: map(`limit${i}`)}})));
ok(peak <= 2, 'bridge decode concurrency is bounded');
equal(loaded.filter(result => result.hasTextures).length, 2, 'pixel budget prevents upload');
ok(limited.warnings.some(w => w.includes('budget reached')));
limited.dispose();

let failCalls = 0;
const failed = new MaterialLibrary(async () => { failCalls++; throw new Error('Texture file is missing.'); }, {loadTexture: loader});
const failures = await Promise.all([0, 1].map(uv => failed.createMaterial({maps: {baseColor: map('missing', uv)}}, [0, 1])));
equal(failCalls, 1);
ok(failures.every(result => !result.hasTextures));
equal(failed.warnings.length, 1, 'failed cached texture emits one warning');
const missingEmission = (await failed.createMaterial({emissive:[1, 1, 1], emissiveIntensity:16,
  maps:{emissive:map('missing')}})).material;
equal(missingEmission.emissiveIntensity, 0, 'failed emission mask must not create unmasked glow');
equal(missingEmission.emissive.r, 0);
const missingTint = (await failed.createMaterial({emissive:[1, 1, 1], emissiveIntensity:16, emissiveMode:'baseColorMask',
  maps:{baseColor:map('missing')}})).material;
equal(missingTint.emissiveIntensity, 0, 'missing tint cannot produce white mask emission');
const missingPBR = (await failed.createMaterial({baseColor:[1,1,1], roughness:1, metalness:1,
  maps:{baseColor:map('missing'), roughness:map('missing'), metalness:map('missing')}})).material;
equal(missingPBR.color.getHex(), 0xb5b3bc, 'failed albedo falls back to neutral');
equal(missingPBR.roughness, .7);
equal(missingPBR.metalness, 0, 'map multiplier must not become fully metallic fallback');
failed.dispose();

function viewport() {
  const view = Object.create(PreviewViewport.prototype);
  Object.assign(view, {models: [], options: {...DEFAULT_VIEW_OPTIONS}, scene: new THREE.Scene(), assetRoot: new THREE.Group(),
    grid: new THREE.Group(), clip: null, rig: 'auto', frame: 0, frameModel() {}});
  view.scene.add(view.assetRoot);
  return view;
}
const asset = name => ({models: [{id: name, name, bones: [], materials: {key: descriptor}, meshes: [{
  name, materialKey: 'key', positions: [0, 0, 0, 1, 0, 0, 0, 1, 0], indices: [0, 1, 2],
  uvs: [0, 0, 1, 0, 0, 1], uvs2: [.2, .2, .8, .2, .2, .8],
}]}]});
const view = viewport(), first = asset('first');
view.loadAsset(first);
equal(view.models[0].meshes[0].geometry.hasAttribute('uv1'), true);
const neutral = view.models[0].meshes[0].material;
view.setOptions({textured: false, wireframe: true});
view.setFrame(7);
const applied = await view.loadMaterials(first, async id => png(id), {loadTexture: loader});
equal(applied.applied, 1); equal(applied.texturedMeshes, 1);
equal(view.models[0].meshes[0].material, neutral);
equal(view.frame, 7, 'material arrival must preserve animation frame');
view.setOptions({textured: true});
ok(view.models[0].meshes[0].material !== neutral);
equal(view.models[0].meshes[0].material.polygonOffset, true);
equal(view.models[0].overlays[0].visible, true);
view.setOptions({textured: false});
equal(view.models[0].meshes[0].material, neutral);

let release;
const pending = new Promise(resolve => { release = resolve; });
let lateTexture, lateDisposals = 0;
const slow = view.loadMaterials(first, async id => png(id), {concurrency: 1, loadTexture: async () => {
  await pending;
  lateTexture = new THREE.Texture({width: 2, height: 2});
  lateTexture.addEventListener('dispose', () => lateDisposals++);
  return lateTexture;
}});
await new Promise(resolve => setTimeout(resolve, 0));
const second = asset('second');
view.loadAsset(second);
release();
equal((await slow).cancelled, true, 'an old load cannot replace the next entity');
equal(lateDisposals, 1, 'texture finishing after cancellation is disposed');
equal(view.models[0].meshes[0].material, view.models[0].materialBindings[0].neutral);
equal((await view.loadMaterials(first, async id => png(id), {loadTexture: loader})).cancelled, true);
delete second.models[0].materials;
const empty = await view.loadMaterials(second, async () => { throw new Error('must not fetch'); }, {loadTexture: loader});
equal(empty.applied, 0); equal(empty.texturedMeshes, 0); equal(empty.warnings.length, 0);
view._clear();
console.log(JSON.stringify({checks, webglRequired: false, textureConcurrency: peak}));

// Optional real WebGL check, independent of the app bridge or shared dev server.
if (process.argv.includes('--gpu')) {
  const {createRequire} = await import('node:module');
  const {createServer} = await import('node:http');
  const {readFile} = await import('node:fs/promises');
  const {fileURLToPath} = await import('node:url');
  const path = await import('node:path');
  const require = createRequire(import.meta.url);
  const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
  const webRoot = fileURLToPath(new URL('../web/', import.meta.url));
  const server = createServer(async (request, response) => {
    try {
      const pathname = new URL(request.url, 'http://localhost').pathname;
      if (pathname === '/') {
        response.setHeader('Content-Type', 'text/html');
        response.end('<!doctype html><div style="width:512px;height:512px"><canvas id="view" style="width:100%;height:100%"></canvas></div>');
        return;
      }
      const filename = path.resolve(webRoot, `.${pathname}`);
      if (!filename.startsWith(path.resolve(webRoot) + path.sep)) throw new Error('Invalid path');
      response.setHeader('Content-Type', 'text/javascript');
      response.end(await readFile(filename));
    } catch { response.statusCode = 404; response.end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({headless: true, args: ['--enable-unsafe-swiftshader']});
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error' && !message.text().includes('404')) errors.push(message.text()); });
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const gpu = await page.evaluate(async () => {
      const T = await import('/vendor/three.module.js');
      const {PreviewViewport} = await import('/viewer.js');
      const view = new PreviewViewport(document.getElementById('view'));
      view.setOptions({grid: false, skeleton: false});
      const shaderErrors = [];
      view.renderer.debug.onShaderError = (gl, program, vertex, fragment) => shaderErrors.push(gl.getProgramInfoLog(program), gl.getShaderInfoLog(vertex), gl.getShaderInfoLog(fragment));
      const canvas = document.createElement('canvas'); canvas.width = canvas.height = 2;
      const context = canvas.getContext('2d');
      const texture = value => {
        context.fillStyle = value; context.fillRect(0, 0, 2, 2);
        return {dataUri: canvas.toDataURL('image/png'), width: 2, height: 2};
      };
      const textures = {base: texture('#cc4020'), normal: texture('#8080ff'), rough: texture('#888888'),
        metal: texture('#202020'), ao: texture('#eeeeee'), emission: texture('#111111'), alpha: texture('#ffffff')};
      const maps = Object.fromEntries(Object.entries({baseColor:'base', normal:'normal', roughness:'rough', metalness:'metal', ao:'ao', emissive:'emission', alpha:'alpha'})
        .map(([role, id]) => [role, {textureId:id, uvIndex:role === 'ao' ? 1 : 0, colorSpace:role === 'baseColor' ? 'srgb' : 'linear'}]));
      maps.baseColor.repeat = [2, 3];
      const descriptor = {name:'GPU fixture', emissiveMode:'baseColorMask', emissiveUsesAO:true, emissiveIntensity:16, maps};
      const data = {models:[{id:'plane', name:'plane', bones:[
        {name:'root', parent:-1, pos:[0,0,0], rot:[0,0,0,1]},
        {name:'tip', parent:0, pos:[0,0,0], rot:[0,0,0,1]},
      ], materials:{m:descriptor}, meshes:[{
        name:'plane', materialKey:'m', positions:[-1,-1,0, 1,-1,0, 1,1,0, -1,1,0], indices:[0,1,2,0,2,3],
        normals:[0,0,1,0,0,1,0,0,1,0,0,1], uvs:[0,0,1,0,1,1,0,1], uvs2:[0,0,1,0,1,1,0,1],
        skinIndices:[0,0,0,0,0,0,0,0,1,0,0,0,1,0,0,0], skinWeights:[1,0,0,0,1,0,0,0,1,0,0,0,1,0,0,0],
      }]}]};
      view.loadAsset(data);
      const report = await view.loadMaterials(data, async id => textures[id]);
      const target = new T.WebGLRenderTarget(128, 128);
      const render = () => {
        const pixels = new Uint8Array(128 * 128 * 4);
        view.renderer.setRenderTarget(target);
        view.renderer.render(view.scene, view.camera);
        view.renderer.readRenderTargetPixels(target, 0, 0, 128, 128, pixels);
        view.renderer.setRenderTarget(null);
        return pixels;
      };
      view.setClip({bones:[{name:'tip', rotations:{frames:[0,10], values:[0,0,0,1,0,0,Math.sin(.3),Math.cos(.3)]}}]});
      const rest = render();
      view.setFrame(10);
      const textured = render();
      view.setOptions({textured:false});
      const neutral = render();
      let changedPixels = 0;
      for (let i = 0; i < textured.length; i += 4) if (Math.abs(textured[i]-neutral[i])+Math.abs(textured[i+1]-neutral[i+1])+Math.abs(textured[i+2]-neutral[i+2]) > 20) changedPixels++;
      let animatedPixels = 0;
      for (let i = 0; i < textured.length; i += 4) if (Math.abs(textured[i]-rest[i])+Math.abs(textured[i+1]-rest[i+1])+Math.abs(textured[i+2]-rest[i+2]) > 20) animatedPixels++;
      view.setOptions({textured:true, wireframe:true}); render();
      for (const alphaMode of ['MASK','BLEND']) {
        descriptor.alphaMode = alphaMode;
        descriptor.emissiveUsesAO = false;
        await view.loadMaterials(data, async id => textures[id]); render();
      }
      const environment = view.scene.environment;
      const repeatedMemory = [];
      for (let i = 0; i < 5; i++) {
        view.loadAsset(data);
        await view.loadMaterials(data, async id => textures[id]);
        view.setFrame(10); render();
        repeatedMemory.push({...view.renderer.info.memory});
      }
      const environmentRetained = view.scene.environment === environment;
      const programCount = view.renderer.info.programs.length;
      const studioEnvironment = Boolean(view.scene.environment);
      const preservedFrame = view.frame;
      target.dispose(); view._clear(); view._environmentTarget?.dispose(); view.renderer.setAnimationLoop(null); view.renderer.dispose();
      return {report, changedPixels, animatedPixels, preservedFrame, shaderErrors, programCount, studioEnvironment, repeatedMemory, environmentRetained};
    });
    assert.deepEqual(errors, []);
    assert.deepEqual(gpu.shaderErrors, []);
    assert.equal(gpu.report.texturedMeshes, 1);
    assert.ok(gpu.changedPixels > 1000, `textured rendering must differ from neutral: ${gpu.changedPixels}`);
    assert.equal(gpu.studioEnvironment, true);
    assert.ok(gpu.animatedPixels > 100);
    assert.equal(gpu.preservedFrame, 10);
    assert.equal(gpu.environmentRetained, true);
    assert.ok(gpu.repeatedMemory.every(memory => memory.textures === gpu.repeatedMemory[0].textures && memory.geometries === gpu.repeatedMemory[0].geometries));
    console.log(JSON.stringify({gpuChecks:9, ...gpu}));
  } finally {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
  }
}
