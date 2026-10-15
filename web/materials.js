import * as THREE from './vendor/three.module.js';

// The decoder supplies role-specific PNGs. Packed channels and OW normal math
// belong in the decoder; the renderer must not reinterpret them a second time.
const MAPS = Object.freeze({
  baseColor: ['map', true], normal: ['normalMap', false],
  roughness: ['roughnessMap', false], metalness: ['metalnessMap', false],
  emissive: ['emissiveMap', true], alpha: ['alphaMap', false], ao: ['aoMap', false],
});
const finite = (value, fallback) => Number.isFinite(value) ? value : fallback;
const clamp = (value, fallback, min = 0, max = 1) => Math.max(min, Math.min(max, finite(value, fallback)));
const color = (value, fallback) => new THREE.Color().setRGB(...(
  Array.isArray(value) && value.length >= 3 && value.slice(0, 3).every(Number.isFinite)
    ? value.slice(0, 3).map(v => Math.max(0, v)) : fallback
));
class CancelledMaterials extends Error {}

function textureViewKey(map, role) {
  const repeat = Array.isArray(map.repeat) && map.repeat.length >= 2 && map.repeat.slice(0, 2).every(Number.isFinite)
    ? map.repeat.slice(0, 2) : [1, 1];
  const space = MAPS[role][1] && map.colorSpace !== 'linear' ? THREE.SRGBColorSpace : THREE.NoColorSpace;
  return JSON.stringify([map.textureId, map.uvIndex === 1 ? 1 : 0, space, repeat]);
}

// Cover the whole scene within a bounded GPU budget instead of giving only the
// first 128 maps full-size textures. Export baking never uses these sizes.
export function mapPreviewMaterialOptions(asset) {
  const descriptors = new Set(), views = new Set();
  for (const model of asset.models || []) for (const descriptor of Object.values(model.materials || {})) {
    const signature = JSON.stringify(descriptor);
    if (descriptors.has(signature)) continue;
    descriptors.add(signature);
    for (const role of Object.keys(MAPS)) {
      const map = descriptor.maps?.[role];
      if (!map?.textureId || role === 'alpha' && !['MASK', 'BLEND'].includes(descriptor.alphaMode)) continue;
      views.add(textureViewKey(map, role));
    }
  }
  const maxPixels = 64_000_000;
  const edge = 2 ** Math.floor(Math.log2(Math.sqrt(maxPixels / Math.max(1, views.size))));
  return {maxTextures:views.size, maxPixels, textureEdge:Math.max(32, Math.min(512, edge)), maxAnisotropy:1};
}

/** Owns every texture/material created for one loaded entity. */
export class MaterialLibrary {
  constructor(resolveTexture, {
    loadTexture = uri => new THREE.TextureLoader().loadAsync(uri),
    maxTextures = 128, maxPixels = 64_000_000, concurrency = 4, maxAnisotropy = 1,
  } = {}) {
    this.resolveTexture = resolveTexture;
    this.loadTexture = loadTexture;
    this.maxTextures = Math.max(0, maxTextures);
    this.maxPixels = Math.max(0, maxPixels);
    this.concurrency = Math.max(1, concurrency);
    this.maxAnisotropy = Math.max(1, Math.min(8, maxAnisotropy));
    this.sources = new Map();
    this.views = new Map();
    this.materialCache = new WeakMap();
    this.sharedMaterialCache = new Map();
    this.textures = new Set();
    this.materials = new Set();
    this.warningCounts = new Map();
    this.active = 0;
    this.queue = [];
    this.textureCount = 0;
    this.pixelCount = 0;
    this.disposed = false;
  }

  _check() { if (this.disposed) throw new CancelledMaterials(); }
  _warn(message) { this.warningCounts.set(message, (this.warningCounts.get(message) || 0) + 1); }
  get warnings() {
    return [...this.warningCounts].map(([message, count]) => count > 1 ? `${message} (${count})` : message);
  }

  async _limited(work) {
    this._check();
    await new Promise((resolve, reject) => {
      if (this.active < this.concurrency) { this.active++; resolve(); }
      else this.queue.push({resolve, reject});
    });
    try { this._check(); return await work(); }
    finally {
      const next = this.queue.shift();
      if (next) next.resolve();
      else this.active--;
    }
  }

  _reserve(pixels) {
    if (this.textureCount >= this.maxTextures || this.pixelCount + pixels > this.maxPixels) {
      throw new Error('Texture preview budget reached; remaining maps use a fallback.');
    }
    this.textureCount++;
    this.pixelCount += pixels;
  }

  _source(id) {
    if (!this.sources.has(id)) this.sources.set(id, this._limited(async () => {
      this._reserve(0);
      const result = await this.resolveTexture(id);
      this._check();
      // Binary core replies carry the PNG bytes ("png"); JSON replies a data URI.
      const png = result?.png instanceof Uint8Array ? result.png : null;
      if (!result || !png && (typeof result.dataUri !== 'string' || !result.dataUri.startsWith('data:image/png;base64,'))) {
        throw new Error('A texture could not be decoded as a PNG preview.');
      }
      const width = Number(result.width), height = Number(result.height);
      if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width < 1 || height < 1) {
        throw new Error('A texture preview has invalid dimensions.');
      }
      if (this.pixelCount + width * height > this.maxPixels) {
        throw new Error('Texture preview budget reached; remaining maps use a fallback.');
      }
      this.pixelCount += width * height;
      const url = png ? URL.createObjectURL(new Blob([png], {type: 'image/png'})) : result.dataUri;
      let texture;
      try { texture = await this.loadTexture(url); }
      finally { if (png) URL.revokeObjectURL(url); }
      if (this.disposed) {
        texture.dispose();
        texture.image?.close?.();
        throw new CancelledMaterials();
      }
      this.textures.add(texture);
      if (result.warning) this._warn(/downscal|resiz|reduc/i.test(result.warning)
        ? 'Large textures were reduced for preview.' : String(result.warning));
      return {texture, pixels: width * height, used: false};
    }).catch(error => {
      if (!(error instanceof CancelledMaterials)) this._warn(error.message || 'A texture could not be loaded.');
      return null;
    }));
    return this.sources.get(id);
  }

  async _texture(map, role) {
    if (!map || typeof map.textureId !== 'string' || !map.textureId) return null;
    const uvIndex = map.uvIndex === 1 ? 1 : 0;
    const repeat = Array.isArray(map.repeat) && map.repeat.length >= 2 && map.repeat.slice(0, 2).every(Number.isFinite)
      ? map.repeat.slice(0, 2) : [1, 1];
    const isColor = MAPS[role][1];
    const colorSpace = isColor && map.colorSpace !== 'linear' ? THREE.SRGBColorSpace : THREE.NoColorSpace;
    const key = textureViewKey(map, role);
    if (!this.views.has(key)) this.views.set(key, (async () => {
      const source = await this._source(map.textureId);
      this._check();
      if (!source) return null;
      if (source.used) this._reserve(source.pixels);
      const texture = source.used ? source.texture.clone() : source.texture;
      source.used = true;
      this.textures.add(texture);
      texture.colorSpace = colorSpace;
      texture.channel = uvIndex;
      // Exported .owmdl UVs already contain the exporter's V conversion.
      texture.flipY = true;
      texture.wrapS = texture.wrapT = THREE.RepeatWrapping;
      texture.repeat.set(...repeat);
      texture.anisotropy = this.maxAnisotropy;
      texture.needsUpdate = true;
      return texture;
    })().catch(error => {
      if (!(error instanceof CancelledMaterials)) this._warn(error.message || 'A texture could not be loaded.');
      return null;
    }));
    return this.views.get(key);
  }

  createMaterial(descriptor, uvChannels = [0]) {
    if (!descriptor || typeof descriptor !== 'object') return Promise.resolve(null);
    let cached = this.materialCache.get(descriptor);
    if (!cached) this.materialCache.set(descriptor, cached = new Map());
    const uvKey = [...uvChannels].sort().join(',');
    if (!cached.has(uvKey)) {
      // JSON IPC duplicates descriptor objects for repeated map placements.
      // Share the actual GPU material when its settings and UV channels agree.
      const signature = JSON.stringify([descriptor, uvKey]);
      if (!this.sharedMaterialCache.has(signature)) this.sharedMaterialCache.set(signature, this._createMaterial(descriptor, new Set(uvChannels)));
      cached.set(uvKey, this.sharedMaterialCache.get(signature));
    }
    return cached.get(uvKey);
  }

  async _createMaterial(descriptor, uvChannels) {
    this._check();
    const mode = ['MASK', 'BLEND'].includes(descriptor.alphaMode) ? descriptor.alphaMode : 'OPAQUE';
    const maps = {};
    await Promise.all(Object.entries(MAPS).map(async ([role, [property]]) => {
      const map = descriptor.maps?.[role];
      if (!map || (role === 'alpha' && mode === 'OPAQUE')) return;
      const channel = map.uvIndex === 1 ? 1 : 0;
      if (!uvChannels.has(channel)) { this._warn(`A material map needs missing UV channel ${channel + 1}.`); return; }
      const texture = await this._texture(map, role);
      if (texture) maps[property] = texture;
    }));
    this._check();
    // A missing mask is zero emission, never an unmasked full-surface glow.
    const missingEmission = Boolean(descriptor.maps?.emissive && !maps.emissiveMap);
    const missingEmissionTint = descriptor.emissiveMode === 'baseColorMask' && descriptor.maps?.baseColor && !maps.map;
    const disableEmission = missingEmission || missingEmissionTint;
    const missingBaseColor = descriptor.maps?.baseColor && !maps.map;
    const missingRoughness = descriptor.maps?.roughness && !maps.roughnessMap;
    const missingMetalness = descriptor.maps?.metalness && !maps.metalnessMap;
    const material = new THREE.MeshStandardMaterial({
      name: String(descriptor.name || 'Overwatch material'),
      color: missingBaseColor ? new THREE.Color(0xb5b3bc) : color(descriptor.baseColor, [1, 1, 1]),
      roughness: missingRoughness ? .7 : clamp(descriptor.roughness, maps.roughnessMap ? 1 : .7),
      metalness: missingMetalness ? 0 : clamp(descriptor.metalness, maps.metalnessMap ? 1 : 0),
      emissive: disableEmission ? new THREE.Color(0, 0, 0) : color(descriptor.emissive, maps.emissiveMap ? [1, 1, 1] : [0, 0, 0]),
      emissiveIntensity: disableEmission ? 0 : Math.max(0, finite(descriptor.emissiveIntensity, 1)),
      opacity: mode === 'OPAQUE' ? 1 : clamp(descriptor.opacity, 1),
      transparent: mode === 'BLEND', depthWrite: mode !== 'BLEND',
      alphaTest: mode === 'MASK' ? clamp(descriptor.alphaCutoff, .5) : 0,
      side: descriptor.doubleSided === false ? THREE.FrontSide : THREE.DoubleSide,
      normalMapType: THREE.TangentSpaceNormalMap,
      aoMapIntensity: clamp(descriptor.aoIntensity, 1),
      polygonOffsetFactor: 1, polygonOffsetUnits: 1,
      ...maps,
    });
    if (descriptor.emissiveMode === 'baseColorMask') {
      // Verified Blender groups tint a linear emission mask by their albedo/AO.
      // Each map retains its own UV set, including separate secondary-UV AO.
      material.onBeforeCompile = shader => {
        shader.fragmentShader = shader.fragmentShader.replace('#include <emissivemap_fragment>', `
#include <emissivemap_fragment>
totalEmissiveRadiance *= diffuseColor.rgb;
${descriptor.emissiveUsesAO === true ? '#ifdef USE_AOMAP\n  totalEmissiveRadiance *= texture2D( aoMap, vAoMapUv ).r;\n#endif' : ''}
`);
      };
      material.customProgramCacheKey = () => `whitehound-base-color-emission-mask-v1-ao${Number(descriptor.emissiveUsesAO === true)}`;
    }
    this.materials.add(material);
    return {material, hasTextures: Object.keys(maps).length > 0};
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    for (const entry of this.queue.splice(0)) entry.reject(new CancelledMaterials());
    for (const material of this.materials) material.dispose();
    const images = new Set();
    for (const texture of this.textures) { images.add(texture.image); texture.dispose(); }
    for (const image of images) image?.close?.();
    this.materials.clear();
    this.textures.clear();
    this.sources.clear();
    this.views.clear();
    this.sharedMaterialCache.clear();
  }
}
