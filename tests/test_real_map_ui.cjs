// Read-only regression using an actual DataTool map. No extraction, C4D export,
// mock asset, or mocked API response is performed. Baseline mode records 0.8.12.
const {chromium,_electron}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const {spawn}=require('node:child_process');
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),assert=require('node:assert/strict');
const project=path.resolve(__dirname,'..');
const scratch=fs.mkdtempSync(path.resolve(project,'../preview-build/real-map-ui-'));
const root=process.env.AVOLITION_REAL_MAP_ROOT||"C:\\Users\\Admin\\Desktop\\Avolition\\library\\Maps\\King's Row (Winter)\\6B1";
const mapFile=process.env.AVOLITION_REAL_MAP_FILE||path.join(root,'Evening - Hybrid.owmap');
const executable=process.env.WHITEHOUND_EXE,baseline=process.env.AVOLITION_MAP_BASELINE==='1';
const profile=path.join(scratch,'Profile');
const timeout=Number(process.env.AVOLITION_MAP_TIMEOUT_MS)||600000;
function inventory(folder){return fs.readdirSync(folder,{withFileTypes:true}).flatMap(entry=>{
  const file=path.join(folder,entry.name);if(entry.isDirectory())return inventory(file);
  const stat=fs.statSync(file);return [{file,size:stat.size,modified:stat.mtimeMs}];
}).sort((a,b)=>a.file.localeCompare(b.file));}
const digest=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
assert.ok(fs.existsSync(mapFile),'Required real map export: '+mapFile);
const before=inventory(root),beforeHash=digest(mapFile);
const report={passed:false,scratch,baseline,packaged:!!executable,mapFile,realAssetRoot:root,
  realParser:true,realRenderer:true,mockedApis:false,nativeExportRequested:false,sourceFileCount:before.length};
let server,browser,application,page,log='',progressTimer,progressFlight;const errors=[],exportRequests=[];
report.progress=[];report.rendererEvents=[];report.backendEvents=[];
function bounded(operation,milliseconds,label){let timer;return Promise.race([operation,new Promise((_,reject)=>{
  timer=setTimeout(()=>reject(new Error(`${label} exceeded ${milliseconds} ms`)),milliseconds);
})]).finally(()=>clearTimeout(timer));}
function readLiveState(){return page.evaluate(()=>{
  const s=window.qaMapStats,v=window.qaMapViewport,library=v?._materialLibrary;
  return {phase:s?.phase||'before-hook',phaseStarted:s?.phaseStarted,error:s?.error||null,
    loading:!document.querySelector('#maps-loading')?.hidden,loadingLabel:document.querySelector('#maps-loading-label')?.textContent,
    status:document.querySelector('#maps-status')?.textContent,geometryMs:s?.geometryMs,materialMs:s?.materialMs,
    models:v?.models?.length||0,sourceMeshes:window.qaMapAsset?.map?.meshCount||0,
    textureRequested:s?.textureRequested||0,textureCompleted:s?.textureSources?.length||0,textureFailed:s?.textureFailures?.length||0,
    library:{active:library?.active,queued:library?.queue?.length,sources:library?.sources?.size,views:library?.views?.size,
      materials:library?.materials?.size,textureCount:library?.textureCount,pixelCount:library?.pixelCount,warnings:library?.warnings?.slice(0,8)},
    renderCalls:s?.renderCalls||0,renderCpuMs:s?.renderCpuMs||0,rendererMemory:v?{...v.renderer.info.memory}:null,
    notes:document.querySelector('#maps-notes')?.textContent?.slice(0,3000)};
});}
function startProgress(start){
  const tick=()=>{
    if(progressFlight){console.log(JSON.stringify({progress:'Renderer telemetry remains busy',elapsedMs:Date.now()-start,scratch}));return;}
    const flight=readLiveState();progressFlight=flight;
    flight.finally(()=>{if(progressFlight===flight)progressFlight=null;}).catch(()=>{});
    bounded(flight,5000,'Renderer telemetry').then(state=>{
      const entry={elapsedMs:Date.now()-start,...state};report.progress.push(entry);report.lastLiveState=entry;
      fs.writeFileSync(path.join(scratch,'progress.json'),JSON.stringify(entry,null,2));
      console.log(JSON.stringify({progress:'Real map loading',...entry,scratch}));
    }).catch(error=>console.log(JSON.stringify({progress:'Renderer telemetry busy',elapsedMs:Date.now()-start,message:error.message,scratch})));
  };
  tick();progressTimer=setInterval(tick,15000);
}
async function screenshot(name,viewportOnly=false){
  if(application){
    let rectangle={};
    if(viewportOnly){const box=await bounded(page.locator('#maps-viewport').boundingBox(),5000,'Viewport bounds');
      if(box)rectangle={x:Math.round(box.x),y:Math.round(box.y),width:Math.round(box.width),height:Math.round(box.height)};}
    const encoded=await bounded(application.evaluate(async({BrowserWindow},rect)=>{
      const image=await BrowserWindow.getAllWindows()[0].webContents.capturePage(rect,{stayHidden:true,stayAwake:true});
      return image.toPNG().toString('base64');
    },rectangle),15000,'Native hidden-window capture');
    fs.writeFileSync(path.join(scratch,name),Buffer.from(encoded,'base64'));
  }else await bounded(viewportOnly?page.locator('#maps-viewport').screenshot({path:path.join(scratch,name),timeout:12000})
    :page.screenshot({path:path.join(scratch,name),timeout:12000}),15000,'Renderer screenshot');
}
async function captureGpu(closeup=false){
  const captured=await bounded(page.evaluate(async closeup=>{
    const THREE=await import('./vendor/three.module.js');
    const v=window.qaMapViewport,asset=window.qaMapAsset,generation=v?._assetGeneration;
    if(!v||!window.qaMapMaterials||v._loadedAsset!==asset)throw Error('The finished map generation is not available for GPU capture.');
    const original={position:v.camera.position.clone(),target:v.controls.target.clone(),near:v.camera.near,far:v.camera.far,
      damping:v.controls.enableDamping,autoRotate:v.controls.autoRotate};
    let selected=null;
    try{
      v.controls.enableDamping=false;v.controls.autoRotate=false;v._resize();v.assetRoot.updateMatrixWorld(true);
      if(closeup){
        const candidates=[];
        for(const model of v.models){
          const bounds=new THREE.Box3();let polygons=0,textured=0;
          for(const binding of model.materialBindings){
            if(!binding.mesh.visible)continue;
            const geometry=binding.mesh.geometry;if(!geometry.boundingBox)geometry.computeBoundingBox();
            bounds.union(geometry.boundingBox.clone().applyMatrix4(binding.mesh.matrixWorld));
            polygons+=binding.data.indices.length/3;textured+=Number(!!binding.textured?.map);
          }
          if(bounds.isEmpty()||!textured||polygons<150)continue;
          const size=bounds.getSize(new THREE.Vector3()),diameter=Math.max(size.x,size.y,size.z);
          // Select a moderately sized textured map object near the median of
          // actual placements; distant skyboxes and full-map planes cannot win.
          if(diameter<4||diameter>55||size.y<2)continue;
          candidates.push({model,bounds,size,diameter,polygons,textured,center:bounds.getCenter(new THREE.Vector3())});
        }
        if(!candidates.length)throw Error('No moderate-sized textured object is available for close-up QA.');
        const median=axis=>{const values=candidates.map(item=>item.center[axis]).sort((a,b)=>a-b);return values[Math.floor(values.length/2)];};
        const middle=new THREE.Vector3(median('x'),median('y'),median('z'));
        candidates.sort((a,b)=>{
          const score=item=>item.center.distanceTo(middle)+Math.abs(Math.log(item.diameter/18))*8;
          return score(a)-score(b)||a.model.source.id.localeCompare(b.model.source.id);
        });
        const chosen=candidates[0],distance=chosen.diameter/(2*Math.tan(THREE.MathUtils.degToRad(v.camera.fov)/2))*1.35;
        v.controls.target.copy(chosen.center);
        v.camera.position.copy(chosen.center).add(new THREE.Vector3(.8,.6,1).normalize().multiplyScalar(distance));
        v.controls.update();v._updateCameraClipping?.();
        selected={selection:'central moderate-sized textured placement',candidateCount:candidates.length,
          modelId:chosen.model.source.id,modelName:chosen.model.source.name,sourcePath:chosen.model.source.path,
          bounds:{min:chosen.bounds.min.toArray(),max:chosen.bounds.max.toArray(),size:chosen.size.toArray()},
          placementCenter:chosen.center.toArray(),medianCenter:middle.toArray(),polygons:chosen.polygons,texturedMeshes:chosen.textured};
      }
      v.camera.updateProjectionMatrix();v.camera.updateMatrixWorld(true);
      // Read the GPU drawing buffer synchronously after an explicit QA render.
      // A hidden Electron compositor can retain an old loading-overlay frame.
      const beforeGeneration=v._assetGeneration;v.renderer.render(v.scene,v.camera);
      const dataUri=v.canvas.toDataURL('image/png');
      const metadata={capture:'explicit QA-only renderer.render + synchronous canvas PNG',closeup,
        generationBefore:beforeGeneration,generationAfter:v._assetGeneration,loadedPath:asset.path,selected,
        canvas:{width:v.canvas.width,height:v.canvas.height},camera:{position:v.camera.position.toArray(),target:v.controls.target.toArray(),
          near:v.camera.near,far:v.camera.far,distance:v.camera.position.distanceTo(v.controls.target)},
        drawCalls:v.renderer.info.render.calls,triangles:v.renderer.info.render.triangles};
      if(generation!==v._assetGeneration||v._loadedAsset!==asset)throw Error('Map generation changed during GPU capture.');
      if(!dataUri.startsWith('data:image/png;base64,')||dataUri.length<1000)throw Error('GPU capture did not produce a usable PNG.');
      return {dataUri,metadata};
    }finally{
      v.camera.position.copy(original.position);v.controls.target.copy(original.target);v.controls.update();
      v.camera.near=original.near;v.camera.far=original.far;v.camera.updateProjectionMatrix();
      v.controls.enableDamping=original.damping;v.controls.autoRotate=original.autoRotate;v.invalidate?.();
    }
  },closeup),30000,'Explicit viewport GPU capture');
  const name=closeup?'real-map-gpu-closeup.png':'real-map-gpu.png';
  fs.writeFileSync(path.join(scratch,name),Buffer.from(captured.dataUri.split(',')[1],'base64'));
  return {...captured.metadata,file:name};
}
const click=locator=>executable?locator.dispatchEvent('click'):locator.click();
async function launch(){
  if(executable){
    application=await _electron.launch({executablePath:path.resolve(executable),args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:45000});
    page=await application.firstWindow({timeout:45000});
    report.native=await application.evaluate(({BrowserWindow})=>{const win=BrowserWindow.getAllWindows()[0];
      win.hide();win.setSkipTaskbar(true);win.setContentSize(1600,1000);win.webContents.setBackgroundThrottling(false);
      return {title:win.getTitle(),visible:win.isVisible(),url:win.webContents.getURL()};});
  }else{
    server=spawn(process.env.WHITEHOUND_TEST_PYTHON||'python',[path.join(project,'app.py'),'--serve','--port','0','--data-dir',profile],
      {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
    const address=await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(Error('Backend startup: '+log)),20000);
      server.once('error',error=>{clearTimeout(timer);reject(error);});server.stderr.on('data',v=>log+=v);
      server.stdout.on('data',v=>{log+=v;const found=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(found){clearTimeout(timer);resolve(found[0]);}});});
    browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
    page=await browser.newPage({viewport:{width:1600,height:1000},deviceScaleFactor:1});await page.goto(address);
  }
  page.setDefaultTimeout(timeout);page.on('pageerror',error=>errors.push(error.message));
  page.on('console',message=>{const text=message.text();if(text.startsWith('[real-map-qa] ')){
    try{const event=JSON.parse(text.slice(14));report.rendererEvents.push({at:Date.now(),...event});console.log(JSON.stringify({rendererEvent:event,scratch}));}catch{}
  }});
  page.on('request',request=>{if(/\/api\/export_/.test(request.url()))exportRequests.push(request.url());});
  page.on('request',request=>{if(/\/api\/load_map(?:$|[?/])/.test(request.url()))report.backendEvents.push({event:'load_map request',at:Date.now()});});
  page.on('response',response=>{if(/\/api\/load_map(?:$|[?/])/.test(response.url()))report.backendEvents.push({event:'load_map response',at:Date.now(),status:response.status()});});
  page.on('requestfailed',request=>{if(/\/api\/(?:load_map|load_texture)(?:$|[?/])/.test(request.url())){
    const event={event:'API request failed',at:Date.now(),url:request.url(),failure:request.failure()};report.backendEvents.push(event);console.log(JSON.stringify(event));}});
  await page.waitForFunction(()=>window.pywebview?.api&&!document.querySelector('#scan-root').disabled
    &&/^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  report.version=await page.locator('.local-badge .version').innerText();
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js');
    const load=PreviewViewport.prototype.loadAsset,materials=PreviewViewport.prototype.loadMaterials;
    window.qaMapStats={phase:'waiting_for_backend',phaseStarted:performance.now(),geometryMs:null,materialMs:null,textureRequested:0,
      textureSources:[],textureFailures:[],renderCalls:0,renderCpuMs:0};
    const phase=(name,error)=>{window.qaMapStats.phase=name;window.qaMapStats.phaseStarted=performance.now();
      if(error)window.qaMapStats.error=error.message;console.info('[real-map-qa] '+JSON.stringify({phase:name,at:performance.now(),error:error?.message}));};
    PreviewViewport.prototype.loadAsset=function(asset){
      if(this.canvas.id!=='maps-viewport')return load.call(this,asset);
      window.qaMapViewport=this;window.qaMapAsset=asset;window.qaMapMaterials=null;
      const start=performance.now();phase('building_geometry');let result;
      try{result=load.call(this,asset);}catch(error){phase('geometry_error',error);throw error;}
      window.qaMapStats.geometryMs=performance.now()-start;phase('geometry_ready');
      const render=this.renderer.render;
      this.renderer.render=(...args)=>{const at=performance.now();const value=render.apply(this.renderer,args);
        window.qaMapStats.renderCalls++;window.qaMapStats.renderCpuMs+=performance.now()-at;return value;};
      return result;
    };
    PreviewViewport.prototype.loadMaterials=async function(asset,resolve,...options){
      if(this.canvas.id!=='maps-viewport')return materials.call(this,asset,resolve,...options);
      const start=performance.now();phase('loading_materials');let result;
      try{result=await materials.call(this,asset,async id=>{window.qaMapStats.textureRequested++;try{const value=await resolve(id);
        window.qaMapStats.textureSources.push({id,width:value?.width,height:value?.height,warning:value?.warning||null});return value;
      }catch(error){window.qaMapStats.textureFailures.push({id,error:error.message});throw error;}},...options);}
      catch(error){phase('material_error',error);throw error;}
      window.qaMapStats.materialMs=performance.now()-start;window.qaMapMaterials=result;phase('complete');return result;
    };
  });
}
async function sample(mode){return page.evaluate(async mode=>{
  const v=window.qaMapViewport,s=window.qaMapStats;
  // Auto-rotation exercises the same controls/render loop without hidden-window
  // mouse delivery differences. Timing is recorded, never a hardware threshold.
  v.controls.autoRotate=mode==='orbit';v.controls.autoRotateSpeed=1;
  const start=performance.now(),calls=s.renderCalls,cpu=s.renderCpuMs,intervals=[];let last=start;
  await new Promise(resolve=>{const frame=now=>{intervals.push(now-last);last=now;if(now-start>=2000)resolve();else requestAnimationFrame(frame);};requestAnimationFrame(frame);});
  v.controls.autoRotate=false;
  const elapsed=performance.now()-start,sorted=[...intervals].sort((a,b)=>a-b);
  return {mode,elapsedMs:elapsed,rafFrames:intervals.length,rafFps:intervals.length/elapsed*1000,
    renderCalls:s.renderCalls-calls,renderFps:(s.renderCalls-calls)/elapsed*1000,renderCpuMs:s.renderCpuMs-cpu,
    medianFrameMs:sorted[Math.floor(sorted.length/2)]||0,p95FrameMs:sorted[Math.floor(sorted.length*.95)]||0,
    drawCalls:v.renderer.info.render.calls,triangles:v.renderer.info.render.triangles};
},mode);}
(async()=>{
  await launch();await click(page.locator('#tab-maps'));
  await page.locator('#maps-root').fill(root);await click(page.locator('#maps-scan'));
  await page.waitForFunction(()=>!document.querySelector('#maps-scan').disabled&&Number(document.querySelector('#maps-count').textContent)>0);
  report.libraryVariants=Number(await page.locator('#maps-count').innerText());
  await page.locator('#maps-search').fill('Hybrid');
  const leaf=page.locator('.map-row').filter({hasText:'Evening - Hybrid'});
  assert.equal(await leaf.count(),1);
  if(!baseline){assert.equal(await page.locator('#maps-list').getAttribute('role'),'tree');
    report.mapTree=await page.locator('#maps-list .map-library-name').allTextContents();
    assert.ok(report.mapTree.includes("King's Row (Winter)"));assert.ok(report.mapTree.includes('6B1'));}
  const start=Date.now();await click(leaf);console.log(JSON.stringify({progress:'Loading real map geometry and materials',scratch,baseline,mapFile,timeoutMs:timeout}));startProgress(start);
  await page.waitForFunction(()=>document.querySelector('#maps-loading').hidden
    &&(window.qaMapMaterials||document.querySelector('#maps-status').classList.contains('error')),null,{timeout});
  assert.ok(await page.evaluate(()=>window.qaMapMaterials),await page.locator('#maps-status').innerText());
  clearInterval(progressTimer);progressTimer=null;
  report.totalLoadMs=Date.now()-start;
  report.loaded=await page.evaluate(()=>{
    const v=window.qaMapViewport,asset=window.qaMapAsset,stats=window.qaMapStats;
    const properties={baseColor:'map',normal:'normalMap',roughness:'roughnessMap',metalness:'metalnessMap',emissive:'emissiveMap',alpha:'alphaMap',ao:'aoMap'};
    const failed=new Set(stats.textureFailures.map(value=>value.id)),geometries=new Set(),materials=new Set(),textures=new Set(),sources=new Set();
    const coverage={bindings:0,withDescriptor:0,expectedTexturedBindings:0,texturedBindings:0,missingValidMaps:[],missingUV:0,missingFiles:0,expectedMaps:0,appliedMaps:0};
    for(const model of v.models)for(const binding of model.materialBindings){
      coverage.bindings++;geometries.add(binding.mesh.geometry);materials.add(binding.mesh.material);
      const descriptor=model.source.materials?.[binding.data.materialKey];if(!descriptor)continue;coverage.withDescriptor++;
      let expects=false,applies=false;
      for(const [role,map] of Object.entries(descriptor.maps||{})){
        const property=properties[role];if(!property||!map.textureId||role==='alpha'&&!['MASK','BLEND'].includes(descriptor.alphaMode))continue;
        if(!binding.mesh.geometry.hasAttribute(map.uvIndex===1?'uv1':'uv')){coverage.missingUV++;continue;}
        if(failed.has(map.textureId)){coverage.missingFiles++;continue;}
        coverage.expectedMaps++;expects=true;
        const texture=binding.textured?.[property];
        if(texture){coverage.appliedMaps++;applies=true;textures.add(texture);sources.add(texture.source);}
        else coverage.missingValidMaps.push({model:model.source.id,mesh:binding.id,role,textureId:map.textureId});
      }
      coverage.expectedTexturedBindings+=Number(expects);coverage.texturedBindings+=Number(applies);
    }
    const bounds=v._mapBounds?{min:v._mapBounds.min.toArray(),max:v._mapBounds.max.toArray()}:null;
    return {map:asset.map,sourceWarnings:asset.warnings,materials:window.qaMapMaterials,stats,
      coverage:{...coverage,missingValidMaps:coverage.missingValidMaps.slice(0,100),missingValidMapCount:coverage.missingValidMaps.length},
      models:v.models.length,gpu:{geometryObjects:geometries.size,materialObjects:materials.size,textureObjects:textures.size,textureSources:sources.size,
        memory:{...v.renderer.info.memory},programs:v.renderer.info.programs?.length,drawCalls:v.renderer.info.render.calls,triangles:v.renderer.info.render.triangles},
      navigation:{mapMode:!!v._mapScene,bounds,position:v.camera.position.toArray(),target:v.controls.target.toArray(),near:v.camera.near,far:v.camera.far,
        minDistance:v.controls.minDistance,maxDistance:v.controls.maxDistance,distance:v.camera.position.distanceTo(v.controls.target),gridScale:v.grid.scale.toArray()}};
  });
  assert.ok(report.loaded.models>0);assert.ok(report.loaded.coverage.bindings>0);
  report.budgetFallbackWarnings=report.loaded.materials.warnings.filter(value=>/preview budget|texture budget|budget.*fallback/i.test(value));
  console.log(JSON.stringify({progress:'Real map loaded',totalLoadMs:report.totalLoadMs,geometryMs:report.loaded.stats.geometryMs,
    materialMs:report.loaded.stats.materialMs,texturedMeshes:report.loaded.materials.texturedMeshes,bindings:report.loaded.coverage.bindings,
    missingValidMapCount:report.loaded.coverage.missingValidMapCount,budgetFallbackWarnings:report.budgetFallbackWarnings}));
  report.gpuCaptures={overview:await captureGpu(),closeup:await captureGpu(true)};
  await screenshot('real-map-whole.png');await screenshot('real-map-viewport.png',true);
  await page.waitForTimeout(1500);report.performance={idle:await bounded(sample('idle'),30000,'Idle sample'),orbit:await bounded(sample('orbit'),30000,'Orbit sample')};
  if(!baseline){
    assert.deepEqual(report.budgetFallbackWarnings,[],'Valid map textures should not stop at the old first-128 budget.');
    assert.equal(report.loaded.coverage.missingValidMapCount,0,'Every available UV-compatible texture role must bind to its material.');
    assert.equal(report.loaded.coverage.texturedBindings,report.loaded.coverage.expectedTexturedBindings);
    assert.ok(report.loaded.map.omittedCollisionMeshCount>0,'Real engine hit-test meshes are omitted using the verified Blender material list.');
    assert.ok(report.loaded.map.omittedCollisionPolygonCount>0);
    assert.equal(report.loaded.navigation.mapMode,true);assert.ok(report.loaded.navigation.bounds);
    assert.ok(report.loaded.navigation.minDistance<.01,'Map navigation permits approaching individual details.');
  }
  assert.deepEqual(exportRequests,[]);assert.deepEqual(errors,[]);
  assert.deepEqual(inventory(root),before,'Source file paths, sizes and modification times remain unchanged.');
  assert.equal(digest(mapFile),beforeHash,'The real map binary remains byte-for-byte unchanged.');
  Object.assign(report,{passed:true,fileInventoryUnchanged:true,mapHashUnchanged:true,errors,exportRequests});
  console.log(JSON.stringify({passed:true,scratch,baseline,report:path.join(scratch,'report.json'),performance:report.performance}));
})().catch(async error=>{
  clearInterval(progressTimer);progressTimer=null;report.error=String(error.stack||error);report.serverLog=log;console.error(error);process.exitCode=1;
  if(page){try{report.failureState=await bounded(progressFlight||readLiveState(),5000,'Failure-state capture');}catch(captureError){report.failureStateError=captureError.message;}
    try{await screenshot('real-map-failure.png');}catch(captureError){report.failureScreenshotError=captureError.message;}}
  try{report.fileInventoryUnchanged=JSON.stringify(inventory(root))===JSON.stringify(before);report.mapHashUnchanged=digest(mapFile)===beforeHash;}
  catch(sourceError){report.sourceVerificationError=sourceError.message;}
}).finally(async()=>{
  clearInterval(progressTimer);server?.kill();
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  try{if(browser)await bounded(browser.close(),15000,'Browser close');if(application)await bounded(application.close(),15000,'Native close');}
  catch(error){console.error(error.message);application?.process()?.kill();}
});
