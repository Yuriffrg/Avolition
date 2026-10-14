// Camera/scene-update audit in source units, without requiring a GPU.
import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {PreviewViewport, DEFAULT_VIEW_OPTIONS, mapContentBounds} from '../web/viewer.js';
import {PreviewOrbitControls} from '../web/navigation.js';

let checks = 0;
const equal = (actual, expected, label) => { assert.deepEqual(actual, expected, label); checks++; };
const ok = (value, label) => { assert.ok(value, label); checks++; };
const transform = (x, y, z) => new THREE.Matrix4().makeTranslation(x, y, z).toArray();
const asset = {map: {version:'2.1'}, meshPrototypes: {building: {meshes: [{
  name: 'Building', sourceMeshIndex: 3, positions: [-1,0,-1, 1,0,-1, 0,3,1], indices: [0,1,2], materialKey: 'stone',
}]}}, models: [
  {id:'west', name:'West', bones:[], meshPrototype:'building', attachmentMatrix:transform(-200,0,0), materials:{stone:{baseColor:[.4,.4,.4]}}},
  {id:'east', name:'East', bones:[], meshPrototype:'building', attachmentMatrix:transform(200,0,0), materials:{stone:{baseColor:[.4,.4,.4]}}},
  {id:'attached', parentId:'east', name:'Attached', bones:[], meshPrototype:'building', attachmentMatrix:transform(0,2,8)},
]};
const sourceBefore = JSON.stringify(asset);
let renders = 0, moved = false, hidden = false;
const view = Object.create(PreviewViewport.prototype);
Object.assign(view, {
  models:[], options:{...DEFAULT_VIEW_OPTIONS}, scene:new THREE.Scene(), assetRoot:new THREE.Group(), grid:new THREE.Group(),
  camera:new THREE.PerspectiveCamera(38,1.5,.01,10000), canvas:{closest:()=>hidden},
  renderer:{render(){renders++;},capabilities:{getMaxAnisotropy:()=>1}}, controls:{target:new THREE.Vector3(),update(){const result=moved;moved=false;return result;}},
  frame:0, clip:null, rig:'auto',
});
view.scene.add(view.assetRoot);
equal(view.loadAsset(asset), {models:3,meshes:3,bones:0,vertices:9,polygons:3});
equal(view.models[0].meshes[0].geometry,view.models[1].meshes[0].geometry,'Repeated geometry shares one GPU buffer.');
equal(view.models[0].meshes[0].material,view.models[1].meshes[0].material,'Neutral map materials are shared.');
equal(view.models[0].overlays[0].material,view.models[2].overlays[0].material,'Wire materials are shared.');
equal(view.getObjects()[0].meshes[0].id,'3','Filtered source mesh indices remain stable.');
equal(view.models[2].group.matrixWorld.elements.slice(12,15),[200,2,8],'Nested instance translations use original map units.');
equal(view.assetRoot.scale.toArray(),[1,1,1],'Preview does not rescale exported source geometry.');
ok(view.controls.minDistance < .01,'A 402-metre scene permits centimetre-scale inspection.');
ok(view.camera.far < 10000,'Framing no longer creates a hundreds-of-kilometres depth range.');
const diameter = 402;
ok(view.grid.scale.x * 10 >= diameter && view.grid.scale.x * 10 <= diameter * 2.5,'Grid width follows map extent.');
view.assetRoot.traverse(object=>equal(object.matrixWorldAutoUpdate,false,'Static world transforms are composed once.'));
view._renderFrame(); equal(renders,1);
for(let i=0;i<300;i++)view._renderFrame();
equal(renders,1,'An idle map submits no redundant frames.');
moved=true;view._renderFrame();equal(renders,2,'Camera damping/navigation continues rendering.');
view.camera.position.copy(view.controls.target).add(new THREE.Vector3(0,0,.005));
moved=true;view._renderFrame();
ok(view.camera.near < .005,'Close zoom reduces the near clipping distance.');
const farCorner = view._mapBounds.max.clone();
ok(view.camera.far > view.camera.position.distanceTo(farCorner),'The map remains within the far clip plane.');
hidden=true;view.invalidate();view._renderFrame();equal(renders,3,'Hidden workspaces do not render.');
hidden=false;view._renderFrame();equal(renders,4,'A pending update appears when its tab returns.');
view.setObjectVisible('west','3',false);view._renderFrame();equal(renders,5,'Visibility updates invalidate the frame.');
equal(view.getVisiblePolygonCount(),2);
view.setOptions({wireframe:true});view._renderFrame();equal(renders,6,'Display options invalidate the frame.');
equal(view.models[0].overlays[0].visible,false,'Wireframe respects hidden objects.');
equal(view.models[1].overlays[0].visible,true);
const materialResult = await view.loadMaterials(asset,async()=>{throw Error('No textures in this fixture.');});
equal(materialResult.applied,2);
view._renderFrame();equal(renders,7,'Arriving materials invalidate the frame.');
const frozenMatrices = view.models.map(model=>model.group.matrixWorld.toArray());
view.setFrame(99);view._renderFrame();
equal(view.models.map(model=>model.group.matrixWorld.toArray()),frozenMatrices,'Static frame updates do not rebuild transforms.');
equal(JSON.stringify(asset),sourceBefore,'Navigation/visibility/materials never change source data.');
const materialDisposals = new Map();
for(const item of new Set(view.models.flatMap(model=>[model.materialBindings[0].neutral,model.overlays[0].material]))) {
  materialDisposals.set(item,0);item.addEventListener('dispose',()=>materialDisposals.set(item,materialDisposals.get(item)+1));
}
view.clearAsset();
for(const count of materialDisposals.values())equal(count,1,'Shared materials are disposed exactly once.');
equal(view._staticScene,false);equal(view._mapBounds,null);equal(view.assetRoot.matrixWorldAutoUpdate,true);
view._renderFrame();equal(renders,9,'Clearing a map repaints the empty scene.');
// Dense main content with a few distant/giant backdrops frames the streets;
// evenly distributed or disconnected districts must keep their full extent.
const boxAt=(x,y,z,size=2)=>new THREE.Box3(new THREE.Vector3(x-size/2,y-size/2,z-size/2),new THREE.Vector3(x+size/2,y+size/2,z+size/2));
const union=boxes=>boxes.reduce((bounds,box)=>bounds.union(box),new THREE.Box3());
const streets=Array.from({length:225},(_,i)=>boxAt(i%15*5,0,Math.floor(i/15)*5));
const city=[...streets,boxAt(1000,100,0,1500),boxAt(10000,0,0)];
const cityBounds=union(city),streetBounds=mapContentBounds(city,cityBounds);
ok(streetBounds!==cityBounds,'Distant and oversized backdrop placements do not shrink the main-content view.');
ok(streets.every(box=>streetBounds.containsBox(box)),'The complete ordinary street placement set remains framed.');
ok(streetBounds.getSize(new THREE.Vector3()).x<100,'Main content receives a useful camera extent.');
ok(streetBounds.containsBox(union(streets)),'Content framing includes a margin around buildings.');
for(const [label,boxes] of [
  ['long linear map',Array.from({length:120},(_,i)=>boxAt(i*10,0,0))],
  ['two disconnected districts',Array.from({length:120},(_,i)=>boxAt((i>=60?1000:0)+i%60,0,i%5))],
  ['small scene',[...streets.slice(0,20),boxAt(10000,0,0)]],
  ['ambiguous size distribution',[...Array.from({length:60},(_,i)=>boxAt(i,0,0)),...Array.from({length:60},(_,i)=>boxAt(i,0,0,200))]],
]) {
  const full=union(boxes);equal(mapContentBounds(boxes,full),full,`${label} retains full-scene framing.`);
}
const framingAsset={map:{version:'2.1'},meshPrototypes:{building:asset.meshPrototypes.building},
  models:city.map((bounds,i)=>{const center=bounds.getCenter(new THREE.Vector3()),size=bounds.getSize(new THREE.Vector3());
    return {id:'building-'+i,name:'Building '+i,bones:[],meshPrototype:'building',
      attachmentMatrix:new THREE.Matrix4().compose(center,new THREE.Quaternion(),size.multiplyScalar(.5)).toArray()};})};
const framingSource=JSON.stringify(framingAsset);
view.loadAsset(framingAsset);
const focusedCenter=view.controls.target.clone(),fullFramingBounds=view._mapBounds.clone();
ok(focusedCenter.x<100,'Map loading initially centers the principal content.');
const overviewOffset=view.camera.position.clone().sub(view.controls.target);
ok(overviewOffset.y>Math.hypot(overviewOffset.x,overviewOffset.z),'Maps initially look down into streets from an elevated overview.');
ok(fullFramingBounds.max.x>9999,'Complete backdrop bounds remain available to clipping.');
ok(view.camera.far>9000,'Far clipping still reaches distant retained geometry.');
equal(view.models.length,city.length,'Framing does not remove any map instances.');
view.frameModel({all:true});
equal(view.controls.target.toArray(),fullFramingBounds.getCenter(new THREE.Vector3()).toArray(),'Explicit full-scene framing remains accessible.');
ok(view.camera.position.y>view.controls.target.y,'Full-scene map framing retains the elevated overview.');
view.frameModel();equal(view.controls.target.toArray(),focusedCenter.toArray(),'Normal framing restores the principal content.');
equal(JSON.stringify(framingAsset),framingSource,'Both framing modes preserve source transforms and export scale.');
view.clearAsset();
const characterAsset={...asset,map:undefined};
view.loadAsset(characterAsset);
const characterOffset=view.camera.position.clone().sub(view.controls.target);
ok(characterOffset.y>0&&characterOffset.y<Math.hypot(characterOffset.x,characterOffset.z)*.3,'Character framing retains its lower viewing angle.');
view.clearAsset();
const riggedMap = {map:{version:'2.1'},models:[{id:'door',name:'Door',bones:[
  {name:'hinge',parent:-1,pos:[0,0,0],rot:[0,0,0,1]},
],meshes:[{name:'door',positions:[0,0,0,1,0,0,0,2,0],indices:[0,1,2],
  skinIndices:[0,0,0,0,0,0,0,0,0,0,0,0],skinWeights:[1,0,0,0,1,0,0,0,1,0,0,0]}]}]};
view.loadAsset(riggedMap);equal(view._staticScene,true,'Rest-pose map rigs also use static transforms.');
view.setAssignments([{modelId:'door',clip:{fps:30,bones:[{name:'hinge',positions:{frames:[0,10],values:[0,0,0,5,0,0]}}]}}],'door');
view.setFrame(10);
equal(view._staticScene,false,'An explicit animation safely resumes transform updates.');
equal(view.models[0].bones[0].matrixWorld.elements.slice(12,15),[5,0,0]);
equal(view._mapScene,true,'Map camera navigation stays in map units.');
view.clearAsset();
// Use real OrbitControls (not the render-loop stub above) to reproduce pending
// gesture damping at the moment Frame or a new map replaces the camera view.
const camera = new THREE.PerspectiveCamera(38,1.5,.01,10000);
camera.position.set(3,5,4);
const orbit = new PreviewOrbitControls(camera);
orbit.enableDamping=true;orbit.dampingFactor=.12;
orbit.domElement={clientHeight:600,clientWidth:900};
view.camera=camera;view.controls=orbit;view.loadAsset(framingAsset);
const almost=(actual,expected,label)=>ok(actual.distanceTo(expected)<1e-8,label);
const stirCamera=()=>{orbit.rotateLeft(.12);orbit.rotateUp(-.12);orbit.pan(30,20);orbit.dollyIn(.94);};
for(const all of [false,true]) {
  view.frameModel({all});const position=camera.position.clone(),target=orbit.target.clone();
  stirCamera();view.frameModel({all});
  almost(camera.position,position,'Frame restores the intended view even while an orbit gesture is damping.');
  almost(orbit.target,target,'Frame clears pending pan displacement.');
  equal(orbit.enableDamping,true,'Normal damped navigation remains enabled after framing.');
  for(let i=0;i<120;i++)orbit.update();
  almost(camera.position,position,'The framed camera does not continue tilting down or sideways.');
  almost(orbit.target,target,'The framed target remains stationary.');
}
view.frameModel();const initialPosition=camera.position.clone(),initialTarget=orbit.target.clone();
stirCamera();view.loadAsset(framingAsset);
for(let i=0;i<120;i++)orbit.update();
almost(camera.position,initialPosition,'Opening another map cannot inherit residual camera movement.');
almost(orbit.target,initialTarget,'New-map framing cannot inherit a panning offset.');
for(const [yaw,pitch] of [[.4,.2],[-1,-.3],[.3,-1.2],[0,2.8]]) {
  orbit.rotateLeft(yaw);orbit.rotateUp(pitch);orbit.pan(12,-8);
  for(let i=0;i<120;i++)orbit.update();
  const right=new THREE.Vector3(1,0,0).applyQuaternion(camera.quaternion);
  ok(Math.abs(right.y)<1e-10,'Orbit and pan preserve a level world horizon, including underside and near-pole views.');
  equal(camera.up.toArray(),[0,1,0],'Map navigation retains its original Y-up coordinate system.');
}
view.clearAsset();
console.log(JSON.stringify({checks,webglRequired:false,sourceUnitsPreserved:true,idleFramesSkipped:300}));

if(process.argv.includes('--gpu')) {
  const {createRequire}=await import('node:module');
  const {createServer}=await import('node:http');
  const {readFile}=await import('node:fs/promises');
  const {fileURLToPath}=await import('node:url');
  const path=await import('node:path');
  const require=createRequire(import.meta.url);
  const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
  const webRoot=fileURLToPath(new URL('../web/',import.meta.url));
  const server=createServer(async(request,response)=>{
    try {
      const pathname=new URL(request.url,'http://localhost').pathname;
      if(pathname==='/') {response.setHeader('Content-Type','text/html');response.end('<!doctype html><div style="width:640px;height:480px"><canvas id="view" tabindex="0" style="width:100%;height:100%"></canvas></div>');return;}
      const filename=path.resolve(webRoot,`.${pathname}`);
      if(!filename.startsWith(path.resolve(webRoot)+path.sep))throw Error('Invalid path');
      response.setHeader('Content-Type','text/javascript');response.end(await readFile(filename));
    }catch{response.statusCode=404;response.end();}
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  let browser;
  try {
    browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
    const page=await browser.newPage();const errors=[];page.on('pageerror',error=>errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const before=await page.evaluate(async()=>{
      const T=await import('/vendor/three.module.js');const {PreviewViewport}=await import('/viewer.js');
      const view=new PreviewViewport(document.getElementById('view'));window.qaView=view;window.qaRenders=0;
      const original=view.renderer.render.bind(view.renderer);view.renderer.render=(...args)=>{window.qaRenders++;return original(...args);};
      const data={map:{version:'2.1'},meshPrototypes:{tile:{meshes:[{name:'Tile',positions:[-1,0,-1,1,0,-1,0,2,1],indices:[0,1,2]}]}},models:Array.from({length:400},(_,i)=>({id:String(i),name:String(i),bones:[],meshPrototype:'tile',attachmentMatrix:new T.Matrix4().makeTranslation(i%20*5,0,Math.floor(i/20)*5).toArray()}))};
      view.loadAsset(data);
      const frames=async count=>{for(let i=0;i<count;i++)await new Promise(requestAnimationFrame);};window.qaFrames=frames;
      await frames(30);const settled=window.qaRenders;await frames(60);
      return {settled,idle:window.qaRenders-settled,camera:view.camera.position.toArray(),drawCalls:view.renderer.info.render.calls,triangles:view.renderer.info.render.triangles,geometries:view.renderer.info.memory.geometries};
    });
    assert.equal(before.idle,0,'A real WebGL scene remains idle after controls settle.');
    assert.ok(before.triangles>=400,'Frozen map instance transforms render visible geometry.');
    const box=await page.locator('#view').boundingBox();
    await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.down();
    await page.mouse.move(box.x+box.width/2+90,box.y+box.height/2+30,{steps:10});await page.mouse.up();
    const after=await page.evaluate(async()=>{await window.qaFrames(100);const renders=window.qaRenders;await window.qaFrames(60);return {renders,idle:window.qaRenders-renders,camera:window.qaView.camera.position.toArray()};});
    assert.ok(after.renders>before.settled,'Dragging renders the map.');
    assert.notDeepEqual(after.camera,before.camera,'Dragging changes the camera.');
    assert.equal(after.idle,0,'The map returns to idle after a drag.');
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({gpuChecks:6,before,after}));
  }finally{await browser?.close();await new Promise(resolve=>server.close(resolve));}
}
