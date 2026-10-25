// Camera attachment, lifecycle and orbit restoration without a GPU.
import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {PreviewViewport, DEFAULT_VIEW_OPTIONS} from '../web/viewer.js';
import {PreviewOrbitControls} from '../web/navigation.js';

let checks = 0;
const equal = (a,b,label) => {assert.deepEqual(a,b,label);checks++;};
const close = (a,b,label) => {assert.ok(a.distanceTo(b)<1e-6,label);checks++;};
const rotation = new THREE.Quaternion().setFromAxisAngle(new THREE.Vector3(0,1,0),Math.PI/4).toArray();
const asset = {models:[
  {id:'arms',name:'Arms',parentId:null,bones:[
    {name:'root',parent:-1,pos:[0,0,0],rot:[0,0,0,1]},
    {name:'bone_007D',parent:0,pos:[0,1.65,0],rot:[0,0,0,1]},
  ],meshes:[{name:'arms',positions:[-.4,1,.6,.4,1,.6,0,1.5,.6],indices:[0,1,2]}]},
  {id:'weapon',name:'Weapon',parentId:'arms',bones:[{name:'weapon-root',parent:-1,pos:[0,0,0],rot:[0,0,0,1]}],meshes:[]},
]};
const clip = {fps:30,lastFrame:30,bones:[
  {name:'root',positions:{frames:[0,30],values:[0,0,0,3,0,0]}},
  {name:'bone_007D',positions:{frames:[0,30],values:[0,1.65,0,0,1.8,.2]},rotations:{frames:[0,30],values:[0,0,0,1,...rotation]}},
]};
function makeView() {
  const view = Object.create(PreviewViewport.prototype);
  Object.assign(view,{models:[],options:{...DEFAULT_VIEW_OPTIONS},scene:new THREE.Scene(),assetRoot:new THREE.Group(),
    grid:new THREE.Group(),camera:new THREE.PerspectiveCamera(38,16/9,.01,10000),frame:0,canvas:{closest:()=>false},
    renderer:{render(){},setAnimationLoop(){}},_resize(){},_active:true});
  view.controls = new PreviewOrbitControls(view.camera);
  view.scene.add(view.assetRoot);
  return view;
}
const view=makeView();
equal(DEFAULT_VIEW_OPTIONS.pov,false,'POV starts off.');
view.loadAsset(asset);
equal(view.getPOVStatus(),{available:true,active:false});
view.setAssignments([{modelId:'arms',clip}],'arms');
const orbit={position:view.camera.position.clone(),quaternion:view.camera.quaternion.clone(),target:view.controls.target.clone(),
  fov:view.camera.fov,near:view.camera.near,far:view.camera.far};
view.setOptions({pov:true});
equal(view.getPOVStatus(),{available:true,active:true});
equal(view.controls.enabled,false,'POV disables orbit gestures.');
close(view.camera.position,new THREE.Vector3(0,1.65,0),'POV uses the camera joint position.');
close(view.camera.getWorldDirection(new THREE.Vector3()),new THREE.Vector3(0,0,1),'POV faces source +Z.');
view.setFrame(30,30);
close(view.camera.position,new THREE.Vector3(3,1.8,.2),'POV follows root and camera animation.');
close(view.camera.getWorldDirection(new THREE.Vector3()),new THREE.Vector3(Math.SQRT1_2,0,Math.SQRT1_2),'POV follows animated rotation.');
view.setAssignments([{modelId:'arms',clip}],'weapon');
view.setFrame(30,30);
close(view.camera.position,new THREE.Vector3(3,1.8,.2),'Weapon selection keeps its parent arms camera.');
view.setOptions({lockRoot:true});
close(view.camera.position,new THREE.Vector3(0,1.8,.2),'In-place moves the POV with the same evaluated pose.');
view.setActive(false);view.setActive(true);
equal(view.controls.enabled,false,'Tab changes cannot enable orbit while POV is active.');
const povPosition=view.camera.position.clone();view.frameModel();
close(view.camera.position,povPosition,'Frame model keeps the POV attached.');
view.setOptions({pov:false});
equal(view.controls.enabled,true);close(view.camera.position,orbit.position,'Orbit position restored.');
close(view.controls.target,orbit.target,'Orbit target restored.');
assert.ok(view.camera.quaternion.angleTo(orbit.quaternion)<1e-6);checks++;
equal([view.camera.fov,view.camera.near,view.camera.far],[orbit.fov,orbit.near,orbit.far],'Orbit lens restored.');
view.setOptions({pov:true});view.clearAsset();
equal(view.getPOVStatus(),{available:false,active:false});
equal(view.controls.enabled,true,'Clearing an entity restores normal navigation.');
view.loadAsset({models:[{id:'uncalibrated',name:'No camera',bones:[],meshes:asset.models[0].meshes}]});
equal(view.getPOVStatus(),{available:false,active:false},'Missing camera joints never create a guessed viewpoint.');
view.loadAsset(asset);
equal(view.getPOVStatus().active,true,'POV preference resumes for the next compatible entity.');
view.loadAsset({...asset,map:{version:'2.1'}});
equal(view.getPOVStatus(),{available:false,active:false},'Map views never use first-person animation cameras.');
equal(view.options.pov,true,'The preference survives incompatible assets.');
equal(JSON.stringify(asset.models[0].bones),JSON.stringify([
  {name:'root',parent:-1,pos:[0,0,0],rot:[0,0,0,1]},
  {name:'bone_007D',parent:0,pos:[0,1.65,0],rot:[0,0,0,1]},
]),'POV changes no export-source bones.');
console.log(JSON.stringify({checks,webglRequired:false}));

if (process.argv.includes('--gpu')) {
  const {createRequire}=await import('node:module');
  const {readFile,mkdir}=await import('node:fs/promises');
  const {fileURLToPath}=await import('node:url');
  const path=await import('node:path');
  const require=createRequire(import.meta.url);
  const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
  const webRoot=fileURLToPath(new URL('../web/',import.meta.url));
  const browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  try {
    const page=await browser.newPage({viewport:{width:1280,height:720}}),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',async route=>{
      const pathname=new URL(route.request().url()).pathname;
      if(pathname==='/')return route.fulfill({contentType:'text/html',body:'<style>body{margin:0}div{width:1280px;height:720px}canvas{width:100%;height:100%}</style><div><canvas id="pov"></canvas></div>'});
      const filename=path.resolve(webRoot,`.${pathname}`);
      if(!filename.startsWith(path.resolve(webRoot)+path.sep))return route.fulfill({status:404});
      await route.fulfill({contentType:'text/javascript',body:await readFile(filename)});
    });
    await page.goto('http://avolition.test/');
    const realAsset=process.env.AVOLITION_POV_ASSET_JSON ? JSON.parse(await readFile(process.env.AVOLITION_POV_ASSET_JSON,'utf8')):asset;
    const realClip=process.env.AVOLITION_POV_CLIP_JSON ? JSON.parse(await readFile(process.env.AVOLITION_POV_CLIP_JSON,'utf8')):clip;
    const result=await page.evaluate(async({asset,clip})=>{
      const {PreviewViewport}=await import('/viewer.js');
      const view=window.povView=new PreviewViewport(document.querySelector('canvas'));
      view.loadAsset(asset);
      const model=view.models.find(model=>model.bones.some(b=>b.name==='bone_007D'));
      view.setAssignments([{modelId:model.source.id,clip}],model.source.id);
      view.setOptions({pov:true,grid:false});view.setFrame(0,clip.fps);
      await new Promise(requestAnimationFrame);await new Promise(requestAnimationFrame);
      return {status:view.getPOVStatus(),position:view.camera.position.toArray(),fov:view.camera.fov,meshes:view.models.reduce((n,m)=>n+m.meshes.length,0)};
    },{asset:realAsset,clip:realClip});
    assert.equal(result.status.active,true);assert.deepEqual(errors,[]);
    const folder=path.resolve(webRoot,'../../preview-build/pov-verification');await mkdir(folder,{recursive:true});
    await page.screenshot({path:path.join(folder,'pov-frame-0.png')});
    await page.evaluate(()=>{window.povView.setFrame(10,30);});
    await page.screenshot({path:path.join(folder,'pov-frame-10.png')});
    console.log(JSON.stringify({gpu:true,...result,screenshots:folder,errors}));
  } finally {await browser.close();}
}
