// Real read-only flat DataTool Widowmaker export, real renderer/backend/parsers.
// No fake assets, export calls, native bridge, or mocked API responses.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const project = path.resolve(__dirname,'..');
const scratch = fs.mkdtempSync(path.resolve(project,'../preview-build/real-objects-tree-ui-'));
const assets = process.env.WHITEHOUND_REAL_ASSET_ROOT || 'C:\\Users\\Admin\\Desktop\\Overwatch Tool\\Datatool\\Widowmaker';
const entity1P = path.join(assets,'Entities','Gameplay1P','Gameplay1P.owentity');
const entity3P = path.join(assets,'Entities','Gameplay3P','Gameplay3P.owentity');
const arms = path.join(assets,'animations','000000000156.owanimclip');
const weapon = path.join(assets,'animations','000000003879.owanimclip');
const body = path.join(assets,'animations','000000000231.owanimclip');
function inventory(folder) {
  return fs.readdirSync(folder,{withFileTypes:true}).flatMap(entry => {
    const file=path.join(folder,entry.name);
    if(entry.isDirectory())return inventory(file);
    const stat=fs.statSync(file);return [{file,size:stat.size,modified:stat.mtimeMs}];
  }).sort((a,b)=>a.file.localeCompare(b.file));
}
const before = inventory(assets);
const digest = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const hashed = before.filter(({file})=>/\.(owentity|owmdl)$/i.test(file)
  || [arms,weapon,body].includes(file)).map(({file})=>({file,digest:digest(file)}));
for(const file of [entity1P,entity3P,arms,weapon,body])assert.ok(fs.existsSync(file),'Required real source: '+file);
const report = {passed:false,scratch,realAssetRoot:assets,realParser:true,realRenderer:true,
  mockedApis:false,nativeExportRequested:false,sourceFileCount:before.length,hashedSourceCount:hashed.length};
const server = spawn(process.env.WHITEHOUND_TEST_PYTHON || 'python',
  [path.join(project,'app.py'),'--serve','--port','0','--data-dir',path.join(scratch,'Profile')],
  {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
let browser, log='';
const errors=[],exportRequests=[];

(async()=>{
  const address=await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(new Error('Backend startup: '+log)),15000);
    server.once('error',error=>{clearTimeout(timer);reject(error);});
    server.stderr.on('data',chunk=>{log+=chunk;});
    server.stdout.on('data',chunk=>{log+=chunk;const found=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(found){clearTimeout(timer);resolve(found[0]);}});
  });
  browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page=await browser.newPage({viewport:{width:1760,height:1080},deviceScaleFactor:1});
  page.setDefaultTimeout(30000);page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(/\/api\/export_(animation|queue)/.test(request.url()))exportRequests.push(request.url());});
  await page.goto(address);
  await page.waitForFunction(()=>window.pywebview?.api && !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  assert.equal(await page.title(),'Avolition');
  assert.equal(await page.locator('.inspector-panel #objects-menu').isVisible(),true);
  assert.equal(await page.locator('#objects-button').count(),0);
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js');
    const load=PreviewViewport.prototype.loadAsset,materials=PreviewViewport.prototype.loadMaterials;
    PreviewViewport.prototype.loadAsset=function(asset){window.qaViewport=this;window.qaAsset=asset;window.qaMaterials=null;return load.call(this,asset);};
    PreviewViewport.prototype.loadMaterials=async function(...args){const result=await materials.apply(this,args);window.qaMaterials=result;return result;};
  });
  await page.locator('#root-path').fill(assets);await page.locator('#scan-root').click();
  await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled && document.querySelectorAll('.clip-row').length>0);
  report.libraryRows=await page.locator('.clip-row').count();
  const model=id=>page.locator(`.object-tree-item[data-kind="model"][data-model-id="${id}"]`);
  const row=item=>item.locator(':scope > .object-tree-row');
  const expander=item=>row(item).locator('.object-tree-expander');
  const screenshot=async name=>{
    await page.evaluate(()=>{
      document.querySelector('.inspector-panel').scrollTop=0;
      document.querySelector('#objects-list').scrollTop=0;
      document.querySelector('#clip-list').scrollTop=0;
      window.scrollTo(0,0);
    });
    await page.screenshot({path:path.join(scratch,name+'-whole.png')});
    await page.locator('#objects-menu').screenshot({path:path.join(scratch,name+'-objects.png')});
  };
  const loaded=async entity=>{
    await page.locator('#entity-select').selectOption(entity);await page.locator('#load-entity').click();
    await page.waitForFunction(entity=>window.qaAsset?.path===entity && window.qaMaterials
      && document.querySelector('#viewport-loading').hidden,entity);
    const source=await page.evaluate(()=>({path:window.qaAsset.path,warnings:window.qaAsset.warnings,
      materials:window.qaMaterials,models:window.qaAsset.models.map(model=>({id:model.id,name:model.name,path:model.path,
        parentId:model.parentId,attachmentBone:model.attachmentBone,bones:model.bones.map(bone=>({name:bone.name,parent:bone.parent})),
        meshes:model.meshes.map(mesh=>({name:mesh.name,polygons:mesh.indices.length/3}))}))}));
    const objects=await page.evaluate(()=>window.qaViewport.getObjects());
    assert.equal(objects.length,source.models.length);
    assert.deepEqual(objects.map(object=>({id:object.id,parentId:object.parentId,attachmentBone:object.attachmentBone})),
      source.models.map(object=>({id:object.id,parentId:object.parentId,attachmentBone:object.attachmentBone})));
    for(const object of source.models){
      const item=model(object.id);assert.equal(await item.count(),1);
      assert.equal(await item.evaluate(element=>element.parentElement.closest('[data-kind="model"]')?.dataset.modelId||null),object.parentId);
      const actualBones=await item.locator(`[data-kind="bone"][data-model-id="${object.id}"]`).evaluateAll(items=>items.map(item=>({
        index:Number(item.dataset.boneIndex),name:item.querySelector(':scope > .object-tree-row > .object-tree-name').textContent,
        parent:item.parentElement.closest('[data-kind="bone"]')?.dataset.boneIndex ?? null,
        visibilityInputs:item.querySelectorAll(':scope > .object-tree-row input').length,
      })).sort((a,b)=>a.index-b.index));
      assert.deepEqual(actualBones,object.bones.map((bone,index)=>({index,name:bone.name,parent:bone.parent>=0?String(bone.parent):null,visibilityInputs:0})),
        'Every joint preserves its actual source parent; joints do not claim independent mesh visibility.');
    }
    const totalMeshes=source.models.reduce((sum,object)=>sum+object.meshes.length,0);
    assert.equal(await page.locator('#objects-list [data-kind="mesh"]').count(),totalMeshes);
    assert.equal(await page.locator('#objects-summary').innerText(),`${totalMeshes} of ${totalMeshes} meshes shown`);
    const meshFree=source.models.filter(object=>!object.meshes.length);
    for(const object of meshFree)assert.equal(await row(model(object.id)).locator('input.object-visibility').count(),0);
    const missing=(source.warnings||[]).filter(warning=>/missing|not found|unresolved/i.test(warning));
    return {source,totalMeshes,meshFree:meshFree.length,missing};
  };
  const choose=async(source,modelId,expectedMatch)=>{
    await row(model(modelId)).locator('.object-tree-name').click();
    assert.equal(await page.locator('#rig-select').inputValue(),modelId);
    await page.locator('#clip-search').fill(path.basename(source,'.owanimclip'));
    assert.equal(await page.locator('.clip-row').count(),1);
    await page.locator('.clip-row').click();
    await page.waitForFunction(({source,modelId})=>window.qaViewport.assignments.some(entry=>entry.modelId===modelId&&entry.clip.path===source)
      && document.querySelector('#viewport-loading').hidden,{source,modelId});
    if(await page.locator('#play-pause').getAttribute('aria-label')==='Pause')await page.locator('#play-pause').click();
    assert.match(await page.locator('#match-message').innerText(),expectedMatch);
  };
  const assignments=()=>page.evaluate(()=>window.qaViewport.assignments.map(({modelId,clip})=>({modelId,path:clip.path})));
  const pov=await loaded(entity1P);
  const armsModel=pov.source.models.find(object=>object.name==='00000000013E.00C');
  const weaponModel=pov.source.models.find(object=>object.name==='0000000001D4.00C');
  assert.equal(armsModel.bones.length,85);assert.equal(weaponModel.bones.length,19);
  assert.equal(weaponModel.parentId,armsModel.id);assert.equal(weaponModel.attachmentBone,'bone_004A');
  assert.equal(pov.totalMeshes,6);
  const armSkeleton=model(armsModel.id).locator(':scope > .object-tree-children > [data-kind="skeleton"]');
  const gunSkeleton=model(weaponModel.id).locator(':scope > .object-tree-children > [data-kind="skeleton"]');
  assert.equal(await armSkeleton.getAttribute('aria-expanded'),'false');
  assert.equal(await gunSkeleton.getAttribute('aria-expanded'),'false');
  await expander(armSkeleton).click();assert.equal(await armSkeleton.getAttribute('aria-expanded'),'true');
  await expander(armSkeleton).click();
  await choose(arms,armsModel.id,/85 \/ 93 tracks matched/);
  await choose(weapon,weaponModel.id,/18 \/ 19 tracks matched/);
  const paired=await assignments();assert.equal(paired.length,2);
  await page.locator('#timeline').fill('45');
  assert.equal(await page.locator('#timeline').getAttribute('max'),'45');
  const poseError=await page.evaluate(()=>{
    let worst=0;
    for(const model of window.qaViewport.models){
      if(!model.assignedClip)continue;
      const clip=model.assignedClip,pose=model.evaluator.evaluate(Math.min(45,clip.lastFrame));
      model.bones.forEach((bone,index)=>bone.matrix.elements.forEach((value,k)=>{worst=Math.max(worst,Math.abs(value-pose.localMatrices[index][k]));}));
    }
    return worst;
  });
  assert.ok(poseError<1e-8,'Real arms hold their last sample while the longer weapon clip reaches its end.');
  const gunMesh=row(model(weaponModel.id)).locator('.object-visibility');
  await row(model(armsModel.id)).locator('.object-tree-name').click();
  await gunMesh.uncheck();
  assert.equal(await page.locator('#rig-select').inputValue(),armsModel.id,'Weapon visibility does not change the selected target.');
  assert.equal(await page.locator('#objects-summary').innerText(),'4 of 6 meshes shown');
  const visible=await page.evaluate(()=>window.qaViewport.getObjects().map(object=>({id:object.id,shown:object.meshes.filter(mesh=>mesh.visible).length})));
  assert.equal(visible.find(object=>object.id===armsModel.id).shown,3);
  assert.deepEqual(await assignments(),paired,'Hiding weapon meshes retains both motion assignments and attachment transforms.');
  await gunMesh.check();
  await expander(model(weaponModel.id)).click();
  assert.equal(await model(weaponModel.id).getAttribute('aria-expanded'),'false');
  await page.locator('#tab-toolchain').click();await page.locator('#tab-animations').click();
  assert.equal(await model(weaponModel.id).getAttribute('aria-expanded'),'false');
  assert.deepEqual(await assignments(),paired);
  await expander(model(weaponModel.id)).click();
  await page.locator('#clip-search').fill('');await page.locator('#timeline').fill('14');
  await screenshot('widowmaker-gameplay1p');
  await expander(gunSkeleton).click();
  await screenshot('widowmaker-gameplay1p-bones');
  await expander(gunSkeleton).click();

  const full=await loaded(entity3P);
  const bodyModel=full.source.models.find(object=>object.name==='00000000011F.00C');
  assert.equal(bodyModel.bones.length,190);assert.equal(full.totalMeshes,9);
  await choose(body,bodyModel.id,/108 \/ 112 tracks matched/);
  const fullGun=full.source.models.find(object=>object.name==='0000000001D4.00C');
  await choose(weapon,fullGun.id,/18 \/ 19 tracks matched/);
  await page.locator('#clip-search').fill('');await page.locator('#timeline').fill('14');
  await screenshot('widowmaker-gameplay3p');
  assert.equal(await page.locator('#objects-menu').isVisible(),true);
  assert.deepEqual(await page.evaluate(async()=>(await window.pywebview.api.bootstrap()).data.exportQueue),[]);
  assert.deepEqual(exportRequests,[]);assert.deepEqual(errors,[]);
  assert.deepEqual(inventory(assets),before,'Every source path, size and modification time stays unchanged.');
  for(const file of hashed)assert.equal(digest(file.file),file.digest,'Entity/model/selected-clip source hashes stay unchanged.');
  const compact=entry=>({models:entry.source.models.map(model=>({id:model.id,name:model.name,parentId:model.parentId,
    attachmentBone:model.attachmentBone,bones:model.bones.length,meshes:model.meshes.length})),
    meshes:entry.totalMeshes,meshFreeNodes:entry.meshFree,missingReferenceWarnings:entry.missing,
    parserWarnings:entry.source.warnings,materialResult:entry.source.materials});
  Object.assign(report,{passed:true,gameplay1P:compact(pov),gameplay3P:compact(full),
    allBoneParentsVerified:true,partialMatches:[85,18,108],sharedTimelineLastFrame:45,
    endPoseError:poseError,visibilityIndependent:true,expansionRetained:true,
    fileInventoryUnchanged:true,sourceHashesUnchanged:true,errors});
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=String(error.stack||error);report.serverLog=log;console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  await browser?.close();server.kill();
});
