// Actual renderer, parser and isolated catalog. Synthetic fixtures deliberately
// include nested attachments and a mesh-free skeleton; no export is requested.
// Set WHITEHOUND_EXE to exercise the packaged native protocol and preload IPC.
const {chromium, _electron} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn, spawnSync} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/objects-tree-ui-'));
const assets = path.join(scratch, 'Hierarchy assets 日本語');
const profile = path.join(scratch, 'Profile');
const entity = path.join(assets, 'Gameplay1P.owentity');
const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
const executable = process.env.WHITEHOUND_EXE;
const fixtureScript = path.join(scratch, 'make_fixture.py');
fs.writeFileSync(fixtureScript, `import sys
from pathlib import Path
sys.path[:0] = [str(Path(sys.argv[1]) / 'tests'), sys.argv[1]]
from test_formats import model_bytes, entity_bytes, clip_bytes
root = Path(sys.argv[2])
def write(name, data):
    target = root / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
bones = [('root_joint', -1, (0,0,0), (0,0,0)),
         ('branch_joint', 0, (0,1,0), (0,0,0)),
         ('tip_joint', 1, (0,2,0), (0,0,0)),
         ('sibling_joint', 0, (1,1,0), (0,0,0))]
for name in ('Arms', 'Weapon', 'Scope', 'SkeletonPivot', 'Marker'):
    write(name + '.owmdl', model_bytes(name=name, bones=bones, mesh=name != 'SkeletonPivot'))
write('Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='Arms.owmdl', relative='.',
      children=[('Weapon.owentity', 'branch_joint'), ('SkeletonPivot.owentity', 'sibling_joint')]))
write('Weapon.owentity', entity_bytes(name='Weapon', model='Weapon.owmdl', relative='.',
      children=[('Scope.owentity', 'tip_joint')]))
write('Scope.owentity', entity_bytes(name='Scope', model='Scope.owmdl', relative='.'))
write('SkeletonPivot.owentity', entity_bytes(name='SkeletonPivot', model='SkeletonPivot.owmdl', relative='.',
      children=[('Marker.owentity', 'branch_joint')]))
write('Marker.owentity', entity_bytes(name='Marker', model='Marker.owmdl', relative='.'))
write('Animations/Reload.owanimclip', clip_bytes(duration=61,
      bones=[('branch_joint', [(0, [(0, (0,1,0)), (60, (0,2,0))])])]))
`);
const fixture = spawnSync(python, [fixtureScript, project, assets], {encoding:'utf8', windowsHide:true});
assert.equal(fixture.status, 0, fixture.stderr || fixture.stdout);
let server, browser, application, page, log = '';
const errors = [];
const report = {passed:false, scratch, packaged:Boolean(executable), nativeIPC:Boolean(executable), syntheticAssets:true,
  realParser:true, realRenderer:true, nativeExportRequested:false};
async function launch() {
  if (executable) {
    application = await _electron.launch({executablePath:path.resolve(executable),
      args:['--no-sandbox','--test-hidden','--data-dir',profile], timeout:45000});
    page = await application.firstWindow({timeout:45000});
    report.native = await application.evaluate(({BrowserWindow}) => {
      const win=BrowserWindow.getAllWindows()[0]; win.hide(); win.setSkipTaskbar(true);
      win.setContentSize(1440,940); win.webContents.setBackgroundThrottling(false);
      const prefs=win.webContents.getLastWebPreferences();
      return {title:win.getTitle(),visible:win.isVisible(),url:win.webContents.getURL(),
        sandbox:prefs.sandbox,contextIsolation:prefs.contextIsolation,nodeIntegration:prefs.nodeIntegration};
    });
    assert.deepEqual(report.native, {title:'Avolition',visible:false,url:'whitehound://app/index.html',
      sandbox:true,contextIsolation:true,nodeIntegration:false});
  } else {
    server = spawn(python, [path.join(project,'app.py'),'--serve','--port','0','--data-dir',profile],
      {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
    const address = await new Promise((resolve,reject) => {
      const timer=setTimeout(() => reject(new Error('Server startup: '+log)),15000);
      server.once('error',error => {clearTimeout(timer); reject(error);});
      server.stderr.on('data',chunk => {log+=chunk;});
      server.stdout.on('data',chunk => {log+=chunk;const match=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(match){clearTimeout(timer);resolve(match[0]);}});
    });
    browser = await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
    page = await browser.newPage({viewport:{width:1440,height:940}});
    await page.goto(address);
  }
  page.setDefaultTimeout(25000); page.on('pageerror',error => errors.push(error.message));
  await page.waitForFunction(() => window.pywebview?.api && !document.querySelector('#scan-root').disabled
    && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
}
async function resize(width,height) {
  if(application) await application.evaluate(({BrowserWindow},{width,height}) => BrowserWindow.getAllWindows()[0].setContentSize(width,height),{width,height});
  else await page.setViewportSize({width,height});
}
const model = id => page.locator(`.object-tree-item[data-kind="model"][data-model-id="${id}"]`);
const row = item => item.locator(':scope > .object-tree-row');
const expander = item => row(item).locator('.object-tree-expander');
const click = locator => executable ? locator.dispatchEvent('click') : locator.click();

(async () => {
  await launch();
  assert.equal(await page.title(),'Avolition');
  assert.equal(await page.locator('#objects-button, [popovertarget="objects-menu"]').count(),0);
  assert.equal(await page.locator('#objects-menu').isVisible(),true,'Objects remain available without opening a popover.');
  assert.equal(await page.locator('.inspector-panel #objects-menu').count(),1,'Objects belong in the right inspector.');
  assert.equal(await page.locator('#objects-list').getAttribute('role'),'tree');
  assert.equal(await page.locator('#objects-list [data-kind="model"]').count(),0);
  assert.equal(await page.locator('#app-logo, .avolition-empty-logo, .empty-note').count(),0,'The workspace has no header or empty-preview branding.');
  assert.equal(await page.locator('.local-badge .version').innerText(),'v0.8.24');
  const palette = await page.evaluate(() => {
    const rgb = value => (value.match(/[\d.]+/g)||[]).slice(0,3).map(Number);
    return [...document.querySelectorAll('select, option, optgroup')].map(element => {
      const style=getComputedStyle(element),background=rgb(style.backgroundColor),color=rgb(style.color);
      const disabled=element.matches(':disabled') || Boolean(element.closest('select:disabled,optgroup:disabled'));
      return {tag:element.tagName,id:element.id,select:element.closest('select')?.id,label:element.textContent.trim().slice(0,100),disabled,
        background:style.backgroundColor,color:style.color,
        dark:background.length===3 && Math.max(...background)<100,
        lightText:color.length===3 && Math.min(...color)>(disabled?90:120),colorScheme:style.colorScheme};
    });
  });
  assert.ok(palette.some(value=>value.tag==='OPTGROUP'),'The sorting group exercises optgroup styling.');
  assert.ok(palette.every(value=>value.dark && value.lightText),JSON.stringify(palette.filter(value=>!value.dark||!value.lightText)));
  assert.match(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme),/dark/);
  report.dropdownPalette=palette;
  await page.evaluate(async () => {
    const {PreviewViewport}=await import('./viewer.js'),original=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;return original.apply(this,args);};
  });
  await page.locator('#root-path').fill(assets); await click(page.locator('#scan-root'));
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent==='1');
  await page.locator('#entity-select').selectOption(entity); await click(page.locator('#load-entity'));
  await page.waitForFunction(() => window.qaViewport?.models.length===5 && document.querySelector('#viewport-loading').hidden);
  const parsed=await page.evaluate(async entity => (await window.pywebview.api.load_entity(entity)).data.models,entity);
  const objects=await page.evaluate(() => window.qaViewport.getObjects());
  assert.equal(objects.length,5,'Mesh-free skeleton parents stay in the object hierarchy.');
  assert.deepEqual(objects.map(object=>({id:object.id,parentId:object.parentId,attachmentBone:object.attachmentBone})),
    parsed.map(object=>({id:object.id,parentId:object.parentId,attachmentBone:object.attachmentBone})),
    'The tree receives source parent IDs and attachment bones, not inferred model relationships.');
  for(const source of parsed) {
    const item=model(source.id);
    assert.equal(await item.count(),1);
    const parentId=await item.evaluate(element => element.parentElement.closest('[data-kind="model"]')?.dataset.modelId||null);
    assert.equal(parentId,source.parentId,'Model DOM nesting follows the parsed attachment parent.');
    assert.equal(await item.getAttribute('aria-expanded'),'true');
  }
  const scene=page.locator('.object-tree-item[data-kind="scene"]');
  assert.equal(await scene.count(),1); assert.equal(await scene.getAttribute('aria-expanded'),'true');
  assert.equal(await page.locator('#objects-summary').innerText(),'4 of 4 meshes shown');
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 4');
  const skeletons=page.locator('.object-tree-item[data-kind="skeleton"]');
  assert.equal(await skeletons.count(),5);
  assert.deepEqual(await skeletons.evaluateAll(items=>items.map(item=>item.getAttribute('aria-expanded'))),Array(5).fill('false'));
  const firstSkeleton=model('model-0').locator(':scope > .object-tree-children > [data-kind="skeleton"]');
  await click(expander(firstSkeleton));
  assert.equal(await firstSkeleton.getAttribute('aria-expanded'),'true');
  for(const bone of parsed[0].bones) {
    const index=parsed[0].bones.indexOf(bone);
    const item=firstSkeleton.locator(`[data-kind="bone"][data-bone-index="${index}"]`);
    assert.equal(await item.count(),1);
    assert.equal(await row(item).locator('.object-tree-name').innerText(),bone.name);
    const parent=await item.evaluate(element=>element.parentElement.closest('[data-kind="bone"]')?.dataset.boneIndex ?? null);
    assert.equal(parent,bone.parent>=0?String(bone.parent):null,'Joint nesting follows the actual skeleton parent index.');
  }
  // Keyboard collapse/reopen and navigation keep focus in the tree, with no playback side effects.
  await firstSkeleton.focus(); await page.keyboard.press('ArrowLeft');
  assert.equal(await firstSkeleton.getAttribute('aria-expanded'),'false');
  await page.keyboard.press('ArrowRight');
  assert.equal(await firstSkeleton.getAttribute('aria-expanded'),'true');
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.evaluate(()=>document.activeElement.dataset.kind),'bone');
  await page.keyboard.press('Home');
  assert.equal(await scene.evaluate(element=>document.activeElement===element),true);
  await page.keyboard.press('ArrowLeft'); assert.equal(await scene.getAttribute('aria-expanded'),'false');
  await page.keyboard.press('ArrowRight'); assert.equal(await scene.getAttribute('aria-expanded'),'true');
  const weapon=model('model-1');
  await weapon.focus(); await page.keyboard.press('Enter');
  assert.equal(await page.locator('#rig-select').inputValue(),'model-1');
  assert.equal(await weapon.getAttribute('aria-selected'),'true');
  const parentVisible=page.locator('.object-model-row input[data-model-id="model-0"]');
  await click(parentVisible);
  assert.equal(await parentVisible.isChecked(),false);
  assert.equal(await page.locator('#rig-select').inputValue(),'model-1','Visibility does not select a different animation target.');
  assert.equal(await page.locator('#objects-summary').innerText(),'3 of 4 meshes shown');
  assert.equal(await page.locator('.object-model-row input[data-model-id="model-1"]').isChecked(),true,'Hiding parent meshes leaves attached children visible.');
  await weapon.focus(); await page.keyboard.press('Space');
  assert.equal(await page.locator('#rig-select').inputValue(),'model-1');
  assert.equal(await page.locator('#objects-summary').innerText(),'2 of 4 meshes shown');
  assert.equal(await page.locator('.object-model-row input[data-model-id="model-2"]').isChecked(),true);
  const pivot=model('model-3');
  assert.equal(await row(pivot).locator('input.object-visibility').count(),0,'A skeleton-only object has no mesh visibility switch.');
  await pivot.focus(); await page.keyboard.press('Enter');
  assert.equal(await page.locator('#rig-select').inputValue(),'model-3','Skeleton-only nodes remain animation targets.');
  await click(page.locator('#objects-show-all'));
  await click(page.locator('.clip-row').first());
  await page.waitForFunction(() => !document.querySelector('#timeline').disabled && document.querySelector('#viewport-loading').hidden);
  await page.locator('#timeline').fill('12');
  await weapon.focus(); await page.keyboard.press('ArrowLeft');
  assert.equal(await weapon.getAttribute('aria-expanded'),'false');
  assert.equal(await page.locator('#frame-input').inputValue(),'12','Tree arrow keys do not scrub the animation.');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#objects-menu').isVisible(),true,'Escape does not hide the permanent object panel.');
  await page.locator('#tab-toolchain').dispatchEvent('click'); await page.locator('#tab-animations').dispatchEvent('click');
  assert.equal(await weapon.getAttribute('aria-expanded'),'false','Tab switches preserve tree expansion.');
  await click(expander(weapon));
  for(const [width,height] of [[1440,940],[1060,700]]) {
    await resize(width,height);
    await page.locator('#objects-menu').scrollIntoViewIfNeeded();
    const fit=await page.evaluate(() => {
      const panel=document.querySelector('#objects-menu').getBoundingClientRect();
      const inspector=document.querySelector('.inspector-panel').getBoundingClientRect();
      return {panel:{x:panel.x,right:panel.right,width:panel.width},inspector:{x:inspector.x,right:inspector.right},
        horizontalOverflow:document.documentElement.scrollWidth>innerWidth,
        treeHorizontalOverflow:document.querySelector('#objects-list').scrollWidth>document.querySelector('#objects-list').clientWidth+1};
    });
    assert.ok(fit.panel.x>=fit.inspector.x-1 && fit.panel.right<=fit.inspector.right+1);
    assert.equal(fit.horizontalOverflow,false); assert.equal(fit.treeHorizontalOverflow,false);
    await page.screenshot({path:path.join(scratch,`objects-tree-${width}.png`)});
  }
  await resize(1440,940);
  await page.locator('#clip-sort').focus(); await page.keyboard.press('Alt+ArrowDown');
  await page.screenshot({path:path.join(scratch,'dropdown-open-requested.png')});
  await page.keyboard.press('Escape');
  report.popupCapture={requested:true,nativeSurfaceConfirmed:false,note:'Computed styles are verified; page capture may omit a native select popup.'};
  // The renderer commits names through the real backend catalog; source binding
  // labels and the selected animation/frame are unaffected by inline edits.
  const frameBeforeRename=await page.locator('#frame-input').inputValue();
  const targetBeforeRename=await page.locator('#rig-select').inputValue();
  const names={'scene':'Named scene 日本語','model:model-0':'Arms café','mesh:model-0:0':'Sleeve β'};
  for(const [key,name] of Object.entries(names)) {
    const node=page.locator(`#objects-list [data-node-key="${key}"]`);
    await node.focus();await page.keyboard.press('F2');
    const input=page.locator('#objects-list .object-rename-input');
    await input.fill(name);await input.press('Enter');
    await page.waitForFunction(()=>!document.querySelector('#objects-list .object-rename-input'));
    assert.equal(await row(node).locator('.object-tree-name').innerText(),name);
  }
  assert.equal(await page.locator('#frame-input').inputValue(),frameBeforeRename);
  assert.equal(await page.locator('#rig-select').inputValue(),targetBeforeRename);
  const persistedNames=await page.evaluate(async path=>await window.pywebview.api.object_names(path),entity);
  assert.equal(persistedNames.ok,true);assert.deepEqual(persistedNames.data,names);
  const reloaded=await page.evaluate(async path=>await window.pywebview.api.load_entity(path),entity);
  assert.deepEqual(reloaded.data.objectNames,names);
  assert.equal(reloaded.data.models[0].name,parsed[0].name,'Export aliases do not overwrite parsed model identity.');
  assert.deepEqual(reloaded.data.models[0].bones,parsed[0].bones);
  assert.ok(Object.values(JSON.parse(fs.readFileSync(path.join(profile,'catalog.json'),'utf8')).objectNames)
    .some(value=>JSON.stringify(value)===JSON.stringify(names)),'Names are written to the isolated profile catalog.');
  report.objectRenaming={realBackend:true,persisted:true,unicode:true,bindingsPreserved:true,frameAndTargetPreserved:true};
  const queue=await page.evaluate(async()=> (await window.pywebview.api.bootstrap()).data.exportQueue);
  assert.deepEqual(queue,[]); assert.deepEqual(errors,[]);
  Object.assign(report,{passed:true,sourceHierarchy:true,skeletonOnlyObject:true,boneHierarchy:true,
    keyboardTree:true,visibilityIndependent:true,permanentPanel:true,compactLayout:true,workspaceBrandingRemoved:true,errors});
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=String(error.stack||error);report.serverLog=log;console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  await application?.close(); await browser?.close(); server?.kill();
});
