import test from 'node:test';
import assert from 'node:assert/strict';
import * as THREE from '../web/vendor/three.module.js';
import {PreviewOrbitControls} from '../web/navigation.js';
import {PreviewViewport} from '../web/viewer.js';
import {PivotNavigation, pickPreviewSurface} from '../web/pivot-navigation.js';

function surface(z, size=32) {
  const mesh = new THREE.Mesh(new THREE.PlaneGeometry(size,size),
    new THREE.MeshBasicMaterial({side:THREE.DoubleSide}));
  mesh.position.z=z;
  mesh.updateMatrixWorld(true);
  return mesh;
}

function harness() {
  const camera=new THREE.PerspectiveCamera(60,2,.5,120);
  camera.updateMatrixWorld(true);
  const controls=new PreviewOrbitControls(camera);
  controls.target.set(0,0,-10);
  controls.enableDamping=true;
  controls.update();
  const marker={style:{},setAttribute(){},remove(){this.removed=true;}};
  const win=new EventTarget();Object.assign(win,{setTimeout,clearTimeout});
  const canvas=new EventTarget();
  Object.assign(canvas,{ownerDocument:{defaultView:win,createElement:()=>marker},clientHeight:300,
    parentElement:{append(){}},getBoundingClientRect:()=>({left:10,top:20,width:600,height:300}),
    focus(){},setPointerCapture(id){this.capture=id;},hasPointerCapture(id){return this.capture===id;},
    releasePointerCapture(){this.capture=null;}});
  const mesh=surface(-10),group=new THREE.Group();group.add(mesh);group.updateMatrixWorld(true);
  const grid=new THREE.GridHelper(10,10);grid.visible=false;
  const view={camera,controls,canvas,grid,models:[{group,meshes:[mesh]}],allowed:true,
    _canUseCanvas(){return this.allowed;},invalidate(){}};
  const nav=new PivotNavigation(view);
  const event=(x=310,y=170,fields={})=>({clientX:x,clientY:y,pointerId:1,pointerType:'mouse',
    button:0,preventDefault(){},stopImmediatePropagation(){},...fields});
  return {nav,view,camera,controls,canvas,marker,mesh,group,grid,event};
}

function screen(point,camera) {
  camera.updateMatrixWorld(true);
  const projected=point.clone().project(camera);
  return new THREE.Vector2(projected.x,projected.y);
}

test('off-center orbit and dolly retain the picked pixel through render updates',()=>{
  const {nav,camera,controls,event,mesh,marker}=harness();
  nav.down(event(390,196,{altKey:true}));
  const anchor=nav.pivot.clone(),before=screen(anchor,camera),rotation=camera.quaternion.clone();
  assert.equal(marker.hidden,false);
  const raycast=mesh.raycast;
  mesh.raycast=()=>{throw Error('Drag should reuse the chosen surface');};
  for(let i=1;i<=35;i++) {
    nav.move(event(390+i*2,196+i));controls.update();
    assert.ok(screen(anchor,camera).distanceTo(before)<1e-10);
  }
  assert.ok(camera.quaternion.angleTo(rotation)>.01);
  nav.endPointer(event());assert.equal(marker.hidden,true);
  mesh.raycast=raycast;
  nav.down(event(390,196,{button:2,altKey:true}));
  const pivot=nav.pivot.clone(),pixel=screen(pivot,camera),distance=camera.position.distanceTo(pivot);
  nav.move(event(410,190));controls.update();
  assert.ok(camera.position.distanceTo(pivot)<distance);
  assert.ok(screen(pivot,camera).distanceTo(pixel)<1e-10);
  nav.dispose();assert.equal(marker.removed,true);
});

test('Stager dolly uses both drag axes and gives equal results across event rates',()=>{
  function drag(points) {
    const {nav,camera,event}=harness();
    nav.down(event(390,196,{button:2,altKey:true}));
    const start=camera.position.distanceTo(nav.pivot);
    for(const [x,y] of points)nav.move(event(x,y));
    const factor=camera.position.distanceTo(nav.pivot)/start;
    nav.dispose();return factor;
  }
  assert.ok(drag([[390,236]])>1,'Downward drag moves away.');
  assert.ok(drag([[430,196]])<1,'Rightward drag approaches.');
  assert.ok(Math.abs(drag([[430,236]])-1)<1e-12,'Equal diagonal components cancel.');
  assert.ok(Math.abs(drag([[470,196]])-drag([[410,196],[430,196],[450,196],[470,196]]))<1e-12);
});

test('pan follows the pointer exactly at the selected depth and preserves rotation',()=>{
  const {nav,camera,controls,event}=harness();
  nav.down(event(390,196,{button:1,altKey:true}));
  const pivot=nav.pivot.clone(),before=screen(pivot,camera),rotation=camera.quaternion.clone();
  nav.move(event(420,181));controls.update();
  const after=screen(pivot,camera);
  assert.ok(Math.abs(after.x-before.x-.1)<1e-12);
  assert.ok(Math.abs(after.y-before.y-.1)<1e-12);
  assert.ok(camera.quaternion.angleTo(rotation)<1e-7);
  nav.dispose();
});

test('wheel reuses its surface and repicks after cursor movement, with equivalent delta modes',()=>{
  const {nav,camera,controls,event,mesh,marker}=harness();
  const raycast=mesh.raycast;let picks=0;
  mesh.raycast=function(...args){picks++;return raycast.apply(this,args);};
  const wheel=event(381,180,{deltaY:-60,deltaMode:0});
  nav.wheel(wheel);const pivot=nav.pivot.clone(),before=screen(pivot,camera),distance=camera.position.distanceTo(pivot);
  nav.wheel(wheel);controls.update();
  assert.equal(picks,1);assert.ok(camera.position.distanceTo(pivot)<distance);
  assert.ok(screen(pivot,camera).distanceTo(before)<1e-10);
  nav.wheel({...wheel,clientX:400});assert.equal(picks,2);
  nav.cancel();assert.equal(marker.hidden,true);nav.dispose();
  function distanceAfter(deltaMode,deltaY) {
    const {nav,camera,event}=harness();nav.wheel(event(381,180,{deltaMode,deltaY}));
    const distance=camera.position.distanceTo(nav.pivot);nav.dispose();return distance;
  }
  assert.ok(Math.abs(distanceAfter(0,16)-distanceAfter(1,1))<1e-12);
  assert.ok(Math.abs(distanceAfter(0,30)-distanceAfter(2,.1))<1e-12);
});

test('picking respects visibility, projection clipping and static batch geometry',()=>{
  const {view,camera,nav}=harness();
  const near=surface(-.1),hidden=surface(-3),far=surface(-115,300),batch=surface(-40);
  hidden.visible=false;batch.layers.mask=0;
  view.models[0].meshes=[near,hidden,far,batch];
  assert.equal(pickPreviewSurface(view,new THREE.Vector2()).z,-40);
  view.models[0].meshes=[far];
  assert.ok(Math.abs(pickPreviewSurface(view,new THREE.Vector2(.8,0)).z+115)<1e-10);
  far.position.z=-121;far.updateMatrixWorld(true);
  assert.equal(pickPreviewSurface(view,new THREE.Vector2()),null);
  view.models[0].group.visible=false;
  view.models[0].meshes=[batch];assert.equal(pickPreviewSurface(view,new THREE.Vector2()),null);
  nav.dispose();
});

test('animated skin picking refreshes bounds beyond the rest-pose box',()=>{
  const {view,nav,camera}=harness(),geometry=new THREE.PlaneGeometry(2,2);
  geometry.setAttribute('skinIndex',new THREE.Uint16BufferAttribute(new Uint16Array(16),4));
  geometry.setAttribute('skinWeight',new THREE.Float32BufferAttribute([1,0,0,0,1,0,0,0,1,0,0,0,1,0,0,0],4));
  const bone=new THREE.Bone(),mesh=new THREE.SkinnedMesh(geometry,new THREE.MeshBasicMaterial({side:THREE.DoubleSide}));
  mesh.position.z=-10;mesh.add(bone);mesh.bind(new THREE.Skeleton([bone]));
  mesh.updateMatrixWorld(true);mesh.computeBoundingBox();mesh.computeBoundingSphere();
  view.models[0].meshes=[mesh];assert.equal(pickPreviewSurface(view,new THREE.Vector2()).z,-10);
  bone.position.x=12;mesh.updateMatrixWorld(true);camera.position.x=12;
  const hit=pickPreviewSurface(view,new THREE.Vector2());
  assert.ok(hit && hit.distanceTo(new THREE.Vector3(12,0,-10))<1e-10);
  camera.position.x=0;assert.equal(pickPreviewSurface(view,new THREE.Vector2()),null);
  nav.dispose();
});

test('finite transformed character grid and empty-space fallback match Stager',()=>{
  const {nav,view,camera,grid,event}=harness();view.models=[];grid.visible=true;
  grid.position.set(8,-2,-13);grid.scale.setScalar(2);grid.rotation.y=.4;
  camera.position.set(8,4,0);camera.lookAt(grid.position);
  assert.ok(pickPreviewSurface(view,new THREE.Vector2()).distanceTo(grid.position)<1e-11);
  camera.lookAt(79,-2,-13);assert.equal(pickPreviewSurface(view,new THREE.Vector2()),null);
  camera.lookAt(grid.position);view._mapScene=true;
  assert.equal(pickPreviewSurface(view,new THREE.Vector2()),null,'Map backdrop grids do not replace scene surfaces.');
  view.controls.target.copy(grid.position);view.controls.update();
  nav.pivot=new THREE.Vector3(8,4,-5);const depth=nav.depth();
  nav.down(event(395,176,{altKey:true}));
  assert.ok(Math.abs(nav.depth()-depth)<1e-10);
  assert.ok(screen(nav.pivot,camera).length()<1e-10);
  nav.dispose();
});

test('digit shortcuts, cancellation and disabled or inactive views keep gestures isolated',()=>{
  const {nav,view,event,canvas,marker,camera}=harness();
  for(const [key,mode] of [['1','pan'],['2','dolly'],['3','orbit']]) {
    view._navigationKey=key;nav.down(event());assert.equal(nav.drag.mode,mode);nav.cancel();
  }
  view._navigationKey=null;
  for(const fields of [{pointerType:'touch'},{ctrlKey:true},{metaKey:true},{shiftKey:true},{isPrimary:false}]) {
    nav.down(event(310,170,fields));assert.equal(nav.drag,null);
  }
  nav.down(event(310,170,{altKey:true,shiftKey:true}));assert.equal(nav.drag.mode,'orbit');
  nav.endPointer(event(310,170,{pointerId:2}));assert.ok(nav.drag);
  nav.cancel();assert.equal(canvas.capture,null);assert.equal(marker.hidden,true);
  const position=camera.position.clone();view.controls.enabled=false;
  nav.down(event());nav.wheel(event(310,170,{deltaY:-40}));assert.equal(nav.drag,null);
  view.controls.enabled=true;view.allowed=false;nav.down(event());assert.equal(nav.drag,null);
  view.allowed=true;nav.down(event());view.allowed=false;nav.move(event(350));assert.equal(nav.drag,null);
  assert.deepEqual(camera.position,position);
  view.allowed=true;nav.down(event());nav.reset();assert.equal(nav.pivot,null);assert.equal(marker.hidden,true);
  nav.dispose();
});

test('physical digit and numpad keys handle Shift and overlapping shortcuts',()=>{
  const view=Object.create(PreviewViewport.prototype);
  Object.assign(view,{_canUseCanvas:()=>true,controls:{enabled:true,mouseButtons:{}},_navigationKeys:new Set()});
  const key=(code,value,fields={})=>({code,key:value,preventDefault(){},...fields});
  view._handleKeyDown(key('Digit1','!'));assert.equal(view._navigationKey,'1');
  view._handleKeyDown(key('Numpad2','ArrowDown'));assert.equal(view._navigationKey,'2');
  view._handleKeyUp(key('Numpad2','ArrowDown'));assert.equal(view._navigationKey,'1');
  view._handleKeyUp(key('Digit1','1'));assert.equal(view._navigationKey,null);
  view._handleKeyDown(key('Digit3','3',{ctrlKey:true}));assert.equal(view._navigationKey,null);
  view._handleKeyDown(key('Digit3','3',{target:{matches:()=>true}}));assert.equal(view._navigationKey,null);
  view._handleKeyDown(key('Digit3','3'));view._handleKeyUp();assert.equal(view._navigationKey,null);
});

test('an owned drag consumes overlapping wheel and pointer starts before stock controls',()=>{
  const {nav,canvas,camera,event}=harness();
  nav.down(event(390,196,{altKey:true}));
  const pivot=nav.pivot.clone(),position=camera.position.clone();
  let forwarded=0;
  for(const type of ['pointerdown','wheel'])canvas.addEventListener(type,()=>{forwarded++;});
  function dispatch(type,fields) {
    const input=new Event(type,{cancelable:true});
    Object.assign(input,{clientX:400,clientY:200,pointerId:2,button:1,...fields});
    canvas.dispatchEvent(input);
    return input;
  }
  assert.equal(dispatch('pointerdown',{pointerType:'touch',isPrimary:false}).defaultPrevented,true);
  assert.equal(dispatch('pointerdown',{ctrlKey:true}).defaultPrevented,true);
  assert.equal(dispatch('wheel',{deltaY:-80,deltaMode:0}).defaultPrevented,true);
  assert.equal(dispatch('wheel',{deltaY:-80,ctrlKey:true}).defaultPrevented,true);
  assert.equal(forwarded,0);
  assert.deepEqual(nav.pivot,pivot);assert.deepEqual(camera.position,position);
  nav.endPointer(event());
  assert.equal(dispatch('wheel',{deltaY:-80,ctrlKey:true}).defaultPrevented,false);
  assert.equal(dispatch('pointerdown',{metaKey:true}).defaultPrevented,false);
  assert.equal(forwarded,2);
  nav.dispose();
});
