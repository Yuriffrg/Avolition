// HISTORICAL HARNESS: v0.8.9-and-earlier nickname-triggered export workflow.
// Intentionally retained for old builds; current UI removed these controls.
// Superseded by test_export_queue_ui.cjs and test_desktop.cjs; see README.md.
// Real Whitehound UI, catalog, and preferences; only the external Cinema 4D
// connection/export replies are simulated. This does not claim native rendering.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const project = path.resolve(__dirname, '..');
const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/c4d-ui-'));
const output = path.join(scratch, 'Saved animations');
const assets = path.join(project, 'tests/material-assets');
const server = spawn(process.env.WHITEHOUND_TEST_PYTHON || 'python',
  [path.join(project, 'app.py'), '--serve', '--port', '0', '--data-dir', path.join(scratch, 'profile')],
  {cwd:project, windowsHide:true, stdio:['ignore','pipe','pipe'], env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
let browser, log = '';
const requests = [], errors = [];
let connection = {connected:false, octane:false, message:'Cinema 4D is not connected.'};
let rejectExport = false;
const ready = page => page.waitForFunction(() => !document.querySelector('#scan-root').disabled
  && /^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
const exportCount = async (count) => {
  for (let i=0; i<100 && requests.length<count; i++) await new Promise(resolve=>setTimeout(resolve,50));
  assert.equal(requests.length, count);
};

(async () => {
  const address = await new Promise((resolve,reject) => {
    const timer = setTimeout(()=>reject(new Error('Server startup timed out: '+log)),15000);
    server.once('error',error=>{clearTimeout(timer);reject(error);});
    server.stderr.on('data',chunk=>{log+=chunk.toString();});
    server.stdout.on('data',chunk=>{
      log+=chunk.toString();
      const match=log.match(/http:\/\/127\.0\.0\.1:\d+/);
      if(match){clearTimeout(timer);resolve(match[0]);}
    });
  });
  browser = await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport:{width:1440,height:940}});
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('**/api/c4d_status', route=>route.fulfill({json:{ok:true,data:connection}}));
  await page.route('**/api/export_animation', async route=>{
    const args=route.request().postDataJSON();
    requests.push(args);
    if(rejectExport) return route.fulfill({json:{ok:false,error:'Cinema 4D disconnected. Keep it open with Avolition C4D Bridge installed.'}});
    const [,nickname,folder,options]=args;
    assert.ok(options.clipEnabled || options.enabled || options.c4dEnabled, 'all-off must not call export API');
    const data={textureCount:options.enabled||options.c4dEnabled?6:0,warnings:[],reused:false};
    if(options.clipEnabled)data.clipPath=path.join(folder,nickname+'.owanimclip');
    if(options.enabled)data.fbxPath=path.join(folder,nickname+'.fbx');
    if(options.c4dEnabled)data.c4dPath=path.join(folder,nickname+'.c4d');
    data.files=[data.clipPath,data.fbxPath,data.c4dPath].filter(Boolean);
    data.path=data.files[0]; data.name=path.basename(data.path);
    return route.fulfill({json:{ok:true,data}});
  });
  await page.goto(address); await ready(page);
  assert.equal(await page.locator('#export-clip').isChecked(),false);
  assert.equal(await page.locator('#export-c4d').isChecked(),true);
  assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(),0);
  await page.locator('#fbx-options-button').click();
  assert.match(await page.locator('.c4d-setup').innerText(),/Avolition C4D Bridge/);
  await page.locator('#check-c4d').click();
  await page.waitForFunction(()=>document.querySelector('#c4d-connection-status').classList.contains('error'));
  assert.match(await page.locator('#c4d-connection-status').innerText(),/not connected/);
  connection={connected:true,octane:false};
  await page.locator('#check-c4d').click();
  await page.waitForFunction(()=>/Octane is not loaded/.test(document.querySelector('#c4d-connection-status').textContent));
  connection={connected:true,octane:true};
  await page.locator('#check-c4d').click();
  await page.waitForFunction(()=>document.querySelector('#c4d-connection-status').classList.contains('success'));
  assert.equal(requests.length,0,'connection checks must not export');
  await page.setViewportSize({width:1060,height:700});
  await page.waitForFunction(()=>{
    const box=document.querySelector('#fbx-options').getBoundingClientRect();
    return box.x>=0&&box.y>=0&&box.right<=innerWidth&&box.bottom<=innerHeight;
  });
  const box=await page.locator('#fbx-options').boundingBox();
  assert.ok(box.x>=0&&box.y>=0&&box.x+box.width<=1060&&box.y+box.height<=700);
  await page.screenshot({path:path.join(scratch,'c4d-options.png')});
  await page.keyboard.press('Escape');
  const controls=await page.locator('.fbx-export-control').evaluate(element=>{
    const parent=element.getBoundingClientRect();
    const boxes=[...element.children].map(child=>child.getBoundingClientRect());
    return boxes.every((box,index)=>box.left>=parent.left&&box.right<=parent.right
      && (!index||box.left>=boxes[index-1].right));
  });
  assert.equal(controls,true,'both format controls and options fit without overlap at 1060px');
  await page.waitForFunction(async()=>{
    const result=await window.pywebview.api.bootstrap();
    return result.ok&&result.data.fbxOptions.c4dEnabled===true&&result.data.fbxOptions.enabled===false&&result.data.fbxOptions.clipEnabled===false;
  });
  await page.reload(); await ready(page);
  assert.equal(await page.locator('#export-c4d').isChecked(),true);
  assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(),0);
  await page.locator('#root-path').fill(assets);
  await page.locator('#scan-root').click();
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='2');
  await page.locator('#output-path').fill(output);
  await page.locator('#output-path').press('Enter');
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#viewport-empty').hidden&&document.querySelector('#viewport-loading').hidden);
  await page.locator('.clip-row').filter({hasText:'001_Wave'}).click();
  await page.waitForFunction(()=>!document.querySelector('#timeline').disabled);
  await page.locator('#viewport-settings > summary').click();
  await page.locator('#lock-root').check();
  await page.locator('#objects-menu > summary').click();
  await page.locator('#objects-hide-all').click();
  assert.match(await page.locator('#objects-summary').innerText(), /^0 of 8 meshes shown$/);
  await page.getByRole('button', {name:'Close objects',exact:true}).click();
  await page.locator('#clip-name').fill('Native wave');
  await page.locator('#clip-name').press('Enter');
  await exportCount(1);
  await page.waitForFunction(()=>document.querySelector('#export-status').classList.contains('success'));
  assert.equal(requests[0][3].c4dEnabled,true);
  assert.equal(requests[0][3].enabled,false);
  assert.equal(requests[0][3].clipEnabled,false);
  assert.equal(requests[0][3].rootMotion,true,'In-place preview must not lock exported root motion');
  assert.equal(requests[0][3].includeMesh,true,'Hiding preview meshes must not disable mesh export');
  assert.equal(Object.hasOwn(requests[0][3],'lockRoot'),false);
  assert.equal(Object.hasOwn(requests[0][3],'visibility'),false);
  assert.equal(Object.hasOwn(requests[0][3],'hiddenObjects'),false);
  assert.match(requests[0][3].entityPath,/MaterialRobot\.owentity$/);
  assert.match(await page.locator('#export-status').innerText(),/Saved: Native wave\.c4d/);
  assert.doesNotMatch(await page.locator('#export-status').innerText(),/owanimclip|\.fbx/);
  assert.equal((await page.locator('#export-status').getAttribute('title')).split('\n').length,1);
  assert.match(await page.locator('#export-status').getAttribute('title'),/Native wave\.c4d/);
  await page.locator('#export-clip').check();
  await exportCount(2);
  await page.waitForFunction(()=>document.querySelector('#export-status').classList.contains('success'));
  assert.equal(requests[1][3].c4dEnabled,true);
  assert.equal(requests[1][3].enabled,false);
  assert.equal(requests[1][3].clipEnabled,true);
  assert.equal(requests[0][3].enabled,false,'completed export snapshot must retain its original format selection');
  assert.match(await page.locator('#export-status').innerText(),/Native wave\.owanimclip \+ Native wave\.c4d/);
  const paths=(await page.locator('#export-status').getAttribute('title')).split('\n');
  assert.equal(paths.length,2);
  assert.equal(new Set(paths).size,2);
  assert.ok(paths.some(filename=>filename.endsWith('.owanimclip')));
  assert.equal(paths.some(filename=>filename.endsWith('.fbx')),false);
  assert.ok(paths.some(filename=>filename.endsWith('.c4d')));
  await page.screenshot({path:path.join(scratch,'c4d-export-complete.png')});
  rejectExport=true;
  await page.locator('#clip-name').fill('Retry wave');
  await page.locator('#clip-name').press('Enter');
  await exportCount(3);
  await page.waitForFunction(()=>document.querySelector('#export-status').classList.contains('error'));
  assert.match(await page.locator('#export-status').innerText(),/Cinema 4D disconnected/);
  rejectExport=false;
  await page.locator('#clip-name').press('Enter');
  await exportCount(4);
  await page.waitForFunction(()=>document.querySelector('#export-status').classList.contains('success'));
  await page.locator('#export-c4d').uncheck(); await exportCount(5);
  await page.waitForFunction(()=>document.querySelector('#export-status').classList.contains('success'));
  assert.match(await page.locator('#export-status').innerText(),/Saved: Retry wave\.owanimclip$/);
  assert.equal((await page.locator('#export-status').getAttribute('title')).split('\n').length,1);
  assert.deepEqual([requests[4][3].clipEnabled,requests[4][3].enabled,requests[4][3].c4dEnabled],[true,false,false]);
  assert.equal(requests[0][3].clipEnabled,false,'adding clip format must not mutate the C4D-only snapshot');
  await page.locator('#export-clip').uncheck();
  await page.locator('#output-path').fill('');
  await page.locator('#clip-name').fill('Label only');
  await page.locator('#clip-name').press('Enter');
  await page.waitForFunction(()=>/No files exported. Nickname saved locally./.test(document.querySelector('#export-status').textContent));
  assert.equal(requests.length,5,'all-off nickname commit must not call export API');
  assert.equal(await page.locator('#export-status').getAttribute('title'),'');
  assert.deepEqual(await page.evaluate(()=>window.whitehoundPrepareClose()),{ok:true},'all-off close needs no output folder');
  await page.reload(); await ready(page);
  for(const id of ['export-clip','export-c4d']) assert.equal(await page.locator('#'+id).isChecked(),false);
  assert.equal(requests.length,5,'reload must not export a saved nickname');
  await page.locator('#scan-root').click();
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='2');
  await page.locator('.clip-row').filter({hasText:'Label only'}).click();
  assert.equal(await page.locator('#viewport-empty').isVisible(),true);
  const before=requests.length;
  assert.deepEqual(await page.evaluate(()=>window.whitehoundPrepareClose()),{ok:true});
  assert.equal(requests.length,before,'passive selected nickname must not trigger native export on close');
  assert.deepEqual(errors,[]);
  const report={passed:true,scratch,simulatedNativeExportRequests:requests.length,
    settingsPersisted:true,connectionStates:3,passiveCloseWithoutEntity:true,formatCombinations:3,allOffWithoutOutput:true,inPlacePreviewOnly:true,objectVisibilityPreviewOnly:true,fbxRemoved:true};
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  console.log(JSON.stringify(report));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{
  await browser?.close(); if(server.exitCode===null)server.kill();
});
