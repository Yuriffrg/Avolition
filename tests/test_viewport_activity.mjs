// Viewport activity and modal ownership, without requiring a GPU by default.
import assert from 'node:assert/strict';
import {MOUSE} from '../web/vendor/three.module.js';
import {PreviewViewport} from '../web/viewer.js';

let checks = 0;
const equal = (actual, expected, label) => { assert.deepEqual(actual, expected, label); checks++; };
const state = {hidden:false,inert:false,dialog:null,modal:null,focusedModal:null};
const doc = {activeElement:{closest:()=>state.focusedModal},querySelector:()=>state.modal};
const canvas = {ownerDocument:doc,isConnected:true,closest:selector=>selector==='dialog'?state.dialog:(state.hidden||state.inert)};
let framed = [], renders = 0, updates = 0, resized = 0, loop;
const view = Object.create(PreviewViewport.prototype);
Object.assign(view, {canvas,_active:true,_disposed:false,_renderDirty:true,_renderCallback:()=>{},
  renderer:{setAnimationLoop:value=>{loop=value;},render:()=>{renders++;}},
  controls:{enabled:true,mouseButtons:{LEFT:MOUSE.ROTATE},update:()=>{updates++;return false;}},
  frameModel:options=>framed.push(options),_resize:()=>{resized++;},_updateCameraClipping:()=>{},
});
const key = (value, extra={}) => ({key:value,target:{matches:()=>false},preventDefault(){this.prevented=true;},...extra});
view._handleKeyDown(key('F',{shiftKey:true}));equal(framed,[{all:true}],'Visible workspace retains Shift+F.');
const two=key('2');view._handleKeyDown(two);equal(view.controls.mouseButtons.LEFT,MOUSE.DOLLY);
equal(two.prevented,true);view._handleKeyUp(key('2'));equal(view.controls.mouseButtons.LEFT,MOUSE.ROTATE);
view._renderFrame();equal(renders,1);equal(updates,1);
for(const blocked of ['hidden','inert','closed-dialog','modal-background','detached']) {
  state.hidden=blocked==='hidden';state.inert=blocked==='inert';
  state.dialog=blocked==='closed-dialog'?{open:false}:null;
  state.modal=blocked==='modal-background'?{contains:()=>false}:null;
  canvas.isConnected=blocked!=='detached';
  view.controls.mouseButtons.LEFT=MOUSE.PAN;view._navigationKey='1';view.invalidate();
  view._handleKeyDown(key('f'));view._handleKeyDown(key('3'));view._renderFrame();
  equal(framed.length,1,`${blocked} does not frame a background viewport.`);
  equal(renders,1,`${blocked} does not submit a frame.`);
  equal(updates,1,`${blocked} does not advance damped navigation.`);
  equal(view._navigationKey,null,`${blocked} releases a held navigation shortcut.`);
}
Object.assign(state,{hidden:false,inert:false,dialog:{open:true},modal:{contains:element=>element===canvas}});
canvas.isConnected=true;
view._handleKeyDown(key('f'));equal(framed.length,2,'The modal owns the frame shortcut.');
view._renderFrame();equal(renders,2,'Opening a dialog paints the deferred scene.');
state.focusedModal={contains:()=>false};view._handleKeyDown(key('f'));
equal(framed.length,2,'A second focused modal also suppresses the previous modal.');
state.focusedModal=null;
for(const target of [{matches:()=>true},{matches:()=>false,isContentEditable:true}])view._handleKeyDown(key('f',{target}));
equal(framed.length,2,'Typing and nested editable content do not control the camera.');
view.controls.enabled=false;view._handleKeyDown(key('f'));equal(framed.length,2,'Application close preparation remains respected.');
view.setActive(false);equal(loop,null,'Explicitly inactive previews stop their animation loop.');
equal(view.controls.enabled,false);view._renderFrame();equal(renders,2);
view.setActive(true);equal(loop,view._renderCallback);equal(resized,1);equal(view.controls.enabled,true);
view._handleKeyDown(key('f'));equal(framed.length,3,'Retained preview resumes on reopen.');
view._disposed=true;view.setActive(true);view._handleKeyDown(key('f'));view._renderFrame();
equal(framed.length,3);equal(renders,2);equal(resized,1,'Disposed viewports cannot be resurrected.');
console.log(JSON.stringify({checks,webglRequired:false}));

if(process.argv.includes('--gpu')) {
  const {createRequire}=await import('node:module');
  const {readFile}=await import('node:fs/promises');
  const {fileURLToPath}=await import('node:url');
  const path=await import('node:path');
  const require=createRequire(import.meta.url);
  const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
  const webRoot=fileURLToPath(new URL('../web/',import.meta.url));
  const serve=async route=>{
    try {
      const pathname=new URL(route.request().url()).pathname;
      if(pathname==='/') {
        await route.fulfill({contentType:'text/html',body:'<!doctype html><style>.holder{width:480px;height:320px}canvas{width:100%;height:100%}</style><div class="holder"><canvas id="main" tabindex="0"></canvas></div><div class="holder" hidden><canvas id="maps" tabindex="0"></canvas></div><dialog id="browser"><input id="search"><div class="holder"><canvas id="preview" tabindex="0"></canvas></div></dialog>'});return;
      }
      const filename=path.resolve(webRoot,`.${pathname}`);
      if(!filename.startsWith(path.resolve(webRoot)+path.sep))throw Error('Invalid path');
      await route.fulfill({contentType:'text/javascript',body:await readFile(filename)});
    }catch{await route.fulfill({status:404,body:''});}
  };
  let browser;
  try {
    browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
    const page=await browser.newPage();const errors=[];page.on('pageerror',error=>errors.push(error.message));
    // Serve source modules in process: this test needs no network or local server.
    await page.route('**/*',serve);
    await page.goto('http://avolition.test/');
    const initial=await page.evaluate(async()=>{
      const {PreviewViewport}=await import('/viewer.js');
      window.views={};window.counts={};
      window.asset={models:[{id:'model',name:'Model',bones:[],meshes:[{name:'Triangle',positions:[0,0,0,1,0,0,0,1,0],indices:[0,1,2],uvs:[0,0,1,0,0,1],materialKey:'A'}]}]};
      window.frames=async count=>{for(let i=0;i<count;i++)await new Promise(requestAnimationFrame);};
      for(const id of ['main','maps','preview']) {
        const view=window.views[id]=new PreviewViewport(document.getElementById(id));
        const count=window.counts[id]={render:0,frame:0,keys:0};
        const render=view.renderer.render.bind(view.renderer),frame=view.frameModel.bind(view),keys=view._handleKeyDown.bind(view);
        view.renderer.render=(...args)=>{count.render++;return render(...args);};
        view.frameModel=(...args)=>{count.frame++;return frame(...args);};
        view._handleKeyDown=(...args)=>{count.keys++;return keys(...args);};
        view.loadAsset(window.asset);count.frame=0;
      }
      await window.frames(8);
      return structuredClone(window.counts);
    });
    assert.ok(initial.main.render>0);assert.equal(initial.preview.render,0,'Closed dialog has no hidden attribute and still does not render.');assert.equal(initial.maps.render,0);
    await page.keyboard.press('f');
    assert.deepEqual(await page.evaluate(()=>Object.fromEntries(Object.entries(window.counts).map(([id,count])=>[id,count.frame]))),{main:1,maps:0,preview:0});
    await page.evaluate(()=>{document.getElementById('browser').showModal();window.views.preview.setActive(true);document.getElementById('preview').focus();});
    await page.keyboard.down('2');
    const modal=await page.evaluate(async()=>{await window.frames(8);return {counts:structuredClone(window.counts),navigation:Object.fromEntries(Object.entries(window.views).map(([id,view])=>[id,view._navigationKey]))};});
    assert.equal(modal.navigation.preview,'2');assert.equal(modal.navigation.main,null);assert.equal(modal.navigation.maps,null);assert.ok(modal.counts.preview.render>0);
    await page.keyboard.up('2');await page.keyboard.press('f');
    assert.deepEqual(await page.evaluate(()=>Object.fromEntries(Object.entries(window.counts).map(([id,count])=>[id,count.frame]))),{main:1,maps:0,preview:1});
    await page.locator('#search').fill('');await page.keyboard.type('f123');
    assert.equal(await page.locator('#search').inputValue(),'f123');
    assert.equal(await page.evaluate(()=>window.counts.preview.frame),1,'Search typing never frames the preview.');
    const cleared=await page.evaluate(async()=>{
      const view=window.views.preview,dialog=document.getElementById('browser');
      const textured=structuredClone(window.asset);textured.models[0].materials={A:{maps:{baseColor:{textureId:'slow'}}}};
      view.loadAsset(textured);
      let release;
      const pending=view.loadMaterials(textured,()=>new Promise(resolve=>{release=resolve;}));
      await Promise.resolve();
      view.setActive(false);view.clearAsset();dialog.close();
      release({dataUri:'data:image/png;base64,unused-after-cancellation',width:1,height:1});
      const result=await pending,before=window.counts.preview.render;
      await window.frames(8);
      const stopped=window.counts.preview.render===before;
      dialog.showModal();view.setActive(true);view.loadAsset(window.asset);await window.frames(8);
      const resumed=window.counts.preview.render>before;
      const geometry=view.models[0].meshes[0].geometry;let disposed=0;geometry.addEventListener('dispose',()=>disposed++);
      view.dispose();view.dispose();view.setActive(true);
      const keys=window.counts.preview.keys;
      window.dispatchEvent(new KeyboardEvent('keydown',{key:'f'}));await window.frames(4);
      return {cancelled:result.cancelled,stopped,resumed,models:view.models.length,disposed,
              listenerRemoved:window.counts.preview.keys===keys,contextLost:view.renderer.getContext().isContextLost()};
    });
    assert.deepEqual(cleared,{cancelled:true,stopped:true,resumed:true,models:0,disposed:1,listenerRemoved:true,contextLost:true});
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({gpuChecks:18,initial,cleared}));
  }finally{await browser?.close();}
}
