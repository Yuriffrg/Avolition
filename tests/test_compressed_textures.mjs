import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {MaterialLibrary, compressedTextureFormats, setTextureFormatPreference} from '../web/materials.js';

// GPU block-compressed previews: format negotiation with the host and
// CompressedTexture construction (the browser upload is covered by
// tests/test_compressed_texture_ui.cjs).

const renderer = extensions => ({extensions: {has: name => extensions.includes(name)}});
const all = renderer(['EXT_texture_compression_bptc', 'WEBGL_compressed_texture_s3tc', 'WEBGL_compressed_texture_s3tc_srgb']);
assert.deepEqual(compressedTextureFormats(all), ['bc7', 'bc3', 'bc1']);
assert.deepEqual(compressedTextureFormats(renderer(['WEBGL_compressed_texture_s3tc'])), [], 'BC1/BC3 need sRGB S3TC');
assert.deepEqual(compressedTextureFormats(renderer(['EXT_texture_compression_bptc'])), ['bc7']);
assert.deepEqual(compressedTextureFormats(null), []);

const blocks = (format, width, height) => new Uint8Array(Math.ceil(width / 4) * Math.ceil(height / 4) * (format === 'bc1' ? 8 : 16));
const compressed = (format, width, height) => {
  const mips = [];
  for (let w = width, h = height; ; w = Math.max(1, w >> 1), h = Math.max(1, h >> 1)) {
    mips.push({width: w, height: h, data: blocks(format, w, h)});
    if (w === 1 && h === 1) break;
  }
  return {format, mips, width, height};
};
const png = {dataUri: 'data:image/png;base64,AAAA', width: 2, height: 2};
const loader = async () => new THREE.Texture({width: 2, height: 2});
const map = (id, colorSpace) => ({textureId: id, uvIndex: 0, colorSpace});

// Without a host function (Python test server, unit tests) nothing is negotiated.
{
  const calls = [];
  const library = new MaterialLibrary(async id => { calls.push(id); return png; }, {loadTexture: loader, renderer: all});
  const {material} = await library.createMaterial({maps: {baseColor: map('a')}}, [0]);
  assert.equal(material.map.isCompressedTexture, undefined);
  assert.equal(material.map.flipY, true);
  library.dispose();
}

const negotiations = [];
let release;
globalThis.window = {pywebview: {setTextureFormats: formats => {
  negotiations.push(formats);
  return new Promise(resolve => { release = resolve; });
}}};

const requests = [];
const replies = {
  color: compressed('bc7', 8, 4), rough: compressed('bc1', 4, 4), alpha: compressed('bc3', 16, 16), plain: png,
  broken: {...compressed('bc7', 8, 8), mips: [{width: 8, height: 8, data: new Uint8Array(3)}]},
};
const library = new MaterialLibrary(async id => { requests.push(id); return replies[id]; },
  {loadTexture: loader, renderer: all, maxAnisotropy: 4});
const pending = library.createMaterial({alphaMode: 'MASK', maps: {
  baseColor: map('color'), roughness: map('rough'), alpha: map('alpha'), normal: map('plain'), emissive: map('broken'),
}}, [0]);
await new Promise(resolve => setTimeout(resolve, 10));
assert.deepEqual(negotiations, [['bc7', 'bc3', 'bc1']]);
assert.deepEqual(requests, [], 'textures wait until the host has the formats');
release(true);
const {material} = await pending;

const color = material.map;
assert.equal(color.isCompressedTexture, true);
assert.equal(color.format, THREE.RGBA_BPTC_Format);
assert.equal(color.colorSpace, THREE.SRGBColorSpace);
assert.equal(color.flipY, false, 'the core sends compressed rows bottom to top');
assert.equal(color.generateMipmaps, false);
assert.equal(color.minFilter, THREE.LinearMipmapLinearFilter);
assert.equal(color.mipmaps.length, 4);
assert.deepEqual(color.mipmaps.map(mip => [mip.width, mip.height]), [[8, 4], [4, 2], [2, 1], [1, 1]]);
assert.equal(color.wrapS, THREE.RepeatWrapping);
assert.equal(color.anisotropy, 4);
assert.equal(material.roughnessMap.format, THREE.RGB_S3TC_DXT1_Format);
assert.equal(material.roughnessMap.colorSpace, THREE.NoColorSpace);
assert.equal(material.alphaMap.format, THREE.RGBA_S3TC_DXT5_Format);
assert.equal(material.normalMap.isCompressedTexture, undefined, 'PNG replies still work');
assert.equal(material.normalMap.flipY, true);
assert.equal(material.emissiveMap, null, 'a malformed compressed reply falls back');
assert.ok(library.warnings.includes('A compressed texture preview is malformed.'), library.warnings);
// Level-0 pixels count as before (a reply that fails to upload, like a PNG
// that fails to decode, still counts).
assert.equal(library.pixelCount, 8 * 4 + 4 * 4 + 16 * 16 + 2 * 2 + 8 * 8);

// A second view of the same source (another colour space) clones it: it
// shares the uploaded data and keeps flipY off.
const variant = await library.createMaterial({maps: {baseColor: map('color', 'linear')}}, [0]);
assert.notEqual(variant.material.map, color);
assert.equal(variant.material.map.source, color.source);
assert.equal(variant.material.map.isCompressedTexture, true);
assert.equal(variant.material.map.flipY, false);
assert.equal(variant.material.map.colorSpace, THREE.NoColorSpace);
assert.equal(variant.material.map.mipmaps.length, 4);
assert.equal(requests.filter(id => id === 'color').length, 1);
library.dispose();

// Negotiation happens once per format list; a preference overrides detection.
new MaterialLibrary(async () => png, {renderer: all}).dispose();
assert.equal(negotiations.length, 1);
setTextureFormatPreference([]);
globalThis.window.pywebview.setTextureFormats = async formats => { negotiations.push(formats); return true; };
const pngOnly = new MaterialLibrary(async () => png, {renderer: all});
assert.deepEqual(await pngOnly.formats, []);
assert.deepEqual(negotiations.at(-1), []);
setTextureFormatPreference(null);
delete globalThis.window;
console.log('compressed texture checks passed');
