import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {MaterialLibrary, mapPreviewMaterialOptions} from '../web/materials.js';

const descriptors=Array.from({length:400},(_,i)=>({name:`Material ${i}`,maps:{
  baseColor:{textureId:`color-${i}`,colorSpace:'srgb'},normal:{textureId:`normal-${i}`},
}}));
const asset={models:descriptors.map(descriptor=>({materials:{0:descriptor}}))};
const options=mapPreviewMaterialOptions(asset);
assert.equal(options.maxTextures,800);
assert.ok(options.textureEdge<=512&&options.maxTextures*options.textureEdge**2<=options.maxPixels);
const calls=[];
const library=new MaterialLibrary(async id=>{calls.push(id);return {dataUri:'data:image/png;base64,test',width:options.textureEdge,height:options.textureEdge};},
  {...options,loadTexture:async()=>new THREE.Texture({width:options.textureEdge,height:options.textureEdge})});
const materials=await Promise.all(descriptors.map(descriptor=>library.createMaterial(descriptor)));
assert.equal(materials.filter(result=>result.material.map&&result.material.normalMap).length,400,'Every material loads beyond the old128-texture cap.');
assert.equal(calls.length,800);
assert.equal(library.warnings.length,0);
assert.equal((await library.createMaterial(JSON.parse(JSON.stringify(descriptors[0])))).material,materials[0].material,'Repeated JSON descriptors reuse a GPU material.');
assert.equal(library.materials.size,400);
const variants={models:[{materials:{0:{maps:{baseColor:{textureId:'same'},roughness:{textureId:'same'},ao:{textureId:'same',uvIndex:1}}}}}]};
assert.equal(mapPreviewMaterialOptions(variants).maxTextures,3,'Independent color spaces and UV channels are budgeted.');
library.dispose();assert.equal(library.sharedMaterialCache.size,0);
console.log('Map preview budget: all800 textures loaded; shared material reuse and bounded pixels passed.');
