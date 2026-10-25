const { chromium } = require(process.env.PLAYWRIGHT_MODULE);
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const source = path.resolve(__dirname, '..');
const base = path.resolve(source, '../preview-build');
fs.mkdirSync(base, {recursive:true});
const folder = fs.mkdtempSync(path.join(base, 'texture-ui-'));
fs.mkdirSync(folder, {recursive:true});
const server = spawn(process.env.WHITEHOUND_TEST_PYTHON || process.env.OWIO_TEST_PYTHON || 'python', [path.join(source,'app.py'),'--serve','--port','0','--data-dir',path.join(folder,'profile')], {
  windowsHide:true, stdio:['ignore','pipe','pipe'], env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}
});
let browser;
let errorLog = '';
server.stderr.on('data', chunk => {errorLog += chunk.toString();});
(async () => {
  const address = await new Promise((resolve,reject) => {
    const timer=setTimeout(()=>reject(new Error('Server startup timed out: '+errorLog)),15000);
    server.once('error',reject);
    server.stdout.on('data', chunk => {
      const match=chunk.toString().match(/http:\/\/127\.0\.0\.1:\d+/);
      if(match){clearTimeout(timer);resolve(match[0]);}
    });
  });
  browser = await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page=await browser.newPage({viewport:{width:1440,height:940},deviceScaleFactor:1});
  const errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(address);
  await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled);
  assert.equal(await page.title(),'Avolition');
  assert.equal(await page.locator('#viewport-error-title').isHidden(),true);
  assert.equal((await page.locator('#viewport-empty').innerText()).includes('making overwatch'),false);
  assert.equal(await page.locator('#app-logo, .avolition-empty-logo, .empty-note').count(),0);
  assert.equal(await page.locator('.local-badge .version').innerText(),'v0.8.29');
  assert.equal(await page.locator('#polygon-count').isHidden(),true);
  assert.equal(await page.locator('#favorites-filter, #favorite-clip, .clip-favorite, #i-star').count(),0);
  assert.equal(await page.locator('#clip-notes, #status-extra').count(),0);
  assert.equal(await page.evaluate(()=>getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()),'#72dadd');
  await page.screenshot({path:path.join(folder,'splash.png')});
  await page.locator('#root-path').fill(path.join(source,'tests/material-assets'));
  await page.locator('#scan-root').click();
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='2');
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#viewport-empty').hidden && document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('.avolition-empty-logo').count(),0);
  assert.equal(await page.locator('#show-textures').isChecked(),true);
  assert.match(await page.locator('#mesh-stats').innerText(),/8 textured meshes/);
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 96');
  await page.screenshot({path:path.join(folder,'textured.png')});
  await page.locator('#show-textures').uncheck();
  assert.match(await page.locator('#mesh-stats').innerText(),/Neutral shading/);
  await page.screenshot({path:path.join(folder,'neutral.png')});
  await page.locator('#show-textures').check();
  await page.locator('.clip-row').filter({hasText:'001_Wave'}).click();
  await page.waitForFunction(()=>!document.querySelector('#timeline').disabled);
  await page.locator('#timeline').fill('15');
  await page.locator('#show-wireframe').check();
  await page.locator('#show-skeleton').uncheck();
  await page.locator('#timeline').fill('29');
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 96');
  await page.locator('#show-skeleton').check();
  await page.screenshot({path:path.join(folder,'preview.png')});
  await page.setViewportSize({width:1060,height:700});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  const bounds=await page.evaluate(()=>{
    const box=id=>{const {x,y,width,height}=document.querySelector(id).getBoundingClientRect();return {x,y,width,height};};
    return {counter:box('#polygon-count'),frame:box('#frame-model'),label:box('.scene-label'),viewport:box('.viewport-wrap')};
  });
  assert.ok(bounds.counter.x>=bounds.label.x+bounds.label.width,'Polygon counter must not overlap the scene name');
  assert.ok(bounds.counter.x+bounds.counter.width<=bounds.frame.x,'Polygon counter must not overlap the frame button');
  assert.ok(bounds.frame.x+bounds.frame.width<=bounds.viewport.x+bounds.viewport.width);
  assert.ok(bounds.counter.y>=bounds.viewport.y && bounds.counter.y<bounds.viewport.y+50,'Count belongs inside the viewport top-right');
  await page.screenshot({path:path.join(folder,'compact.png')});
  const materialStats=await page.locator('#material-status').innerText();
  // Replace the real fixture with a parent and a child sharing one source mesh.
  // Each rendered mesh counts once, while their wire overlays do not count.
  await page.evaluate(()=>{
    window.__loadEntity=window.pywebview.api.load_entity;
    window.pywebview.api.load_entity=async(...args)=>{
      const result=await window.__loadEntity(...args);
      const parent=result.data.models[0]; parent.meshes=parent.meshes.slice(0,1);
      const child=structuredClone(parent); child.id='child-count-test'; child.parentId=parent.id;
      result.data.models=[parent,child];
      return result;
    };
  });
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 24');
  await page.evaluate(()=>{window.pywebview.api.load_entity=async()=>({ok:true,data:{models:[]}});});
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 0');
  await page.evaluate(()=>{window.pywebview.api.load_entity=async()=>({ok:false,error:'Test entity load failed'});});
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#status-text').textContent==='Test entity load failed');
  assert.equal(await page.locator('#polygon-count').isHidden(),true);
  assert.equal(await page.locator('#polygon-count').textContent(),'Polygons: 0');
  assert.equal(await page.locator('#scene-name').innerText(),'NO ENTITY LOADED');
  assert.equal(await page.locator('#mesh-stats').innerText(),'Neutral mesh preview');
  await page.evaluate(()=>{window.pywebview.api.load_entity=window.__loadEntity; delete window.__loadEntity;});
  await page.locator('#load-entity').click();
  await page.waitForFunction(()=>document.querySelector('#viewport-loading').hidden);
  assert.equal(await page.locator('#polygon-count').innerText(),'Polygons: 96');
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({materialStats,passed:true,screenshots:folder,polygons:96,childPolygons:24,failedLoadReset:true,favoritesRemoved:true,compactBounds:bounds,notesRemoved:true,palette:true,uncaughtErrors:errors}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{
  if(browser)await browser.close();
  if(server.exitCode===null)server.kill();
});
