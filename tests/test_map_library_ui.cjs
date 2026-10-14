// Isolated renderer checks: directory grouping must never load map geometry.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const project = path.resolve(__dirname,'..');
const scratch = fs.mkdtempSync(path.resolve(project,'../preview-build/map-library-ui-'));
const row = (map,build,name) => ({path:`C:/Exports/Maps/${map}/${build}/${name}.owmap`,name,relativePath:`Maps/${map}/${build}/${name}.owmap`});
const rows = [row('Colosseo','B10','Night - Push'),row('Colosseo','B2','Morning - Skirmish'),
  row('Colosseo','B2','Night - Push'),row("King's Row (Winter)",'6B1','Evening - Hybrid'),row('東京','B1','夜 - Hybrid')];
let browser;
(async()=>{
  browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:600,height:700}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.route('http://map-library.test/**',route=>{
    const name = new URL(route.request().url()).pathname;
    if(name==='/map-library.js') return route.fulfill({contentType:'text/javascript',body:fs.readFileSync(path.join(project,'web/map-library.js'),'utf8')});
    if(name==='/styles.css') return route.fulfill({contentType:'text/css',body:fs.readFileSync(path.join(project,'web/styles.css'),'utf8')});
    if(name!=='/') return route.abort();
    return route.fulfill({contentType:'text/html',body:`<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="styles.css">
      <style>body{padding:22px;background:#242428}.demo{width:380px;border:1px solid #3b3b42}header{padding:15px;display:flex;justify-content:space-between;font-size:14px}#search{margin:8px;width:calc(100% - 16px);height:32px}#tree{max-height:520px}</style>
      <section class="demo"><header>Map library <span id="count"></span></header><input id="search" aria-label="Search maps" placeholder="Search maps and variants"><div id="tree"></div><p id="empty"></p></section>
      <script type="module">import {MapLibraryTree} from './map-library.js';window.selected=[];window.tree=new MapLibraryTree(document.querySelector('#tree'),{count:document.querySelector('#count'),empty:document.querySelector('#empty'),onSelect:row=>{selected.push(row.path);tree.setSelected(row.path)}});tree.setRows(${JSON.stringify(rows)});document.querySelector('#search').addEventListener('input',event=>tree.setQuery(event.target.value));</script>`});
  });
  await page.goto('http://map-library.test/');await page.waitForFunction(()=>window.tree);
  const item = name => page.locator('.map-library-item').filter({has:page.locator(`:scope > .map-library-row > .map-library-name`,{hasText:name})});
  const label = name => page.locator('.map-library-name').filter({hasText:new RegExp('^'+name.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')+'$')});
  const colosseo = page.locator('[data-kind="map"]').filter({has:label('Colosseo')});
  assert.equal(await page.locator('#tree').getAttribute('role'),'tree');
  assert.equal(await page.locator('#count').innerText(),'5');
  assert.equal(await page.locator('.map-row').count(),0,'A multi-map library starts compact.');
  await label('Colosseo').click();
  assert.equal(await colosseo.getAttribute('aria-expanded'),'true');
  assert.deepEqual(await colosseo.locator(':scope > [role="group"] > [role="treeitem"] > .map-library-row > .map-library-name').allTextContents(),['B2','B10']);
  const build=page.locator('[data-kind="folder"]').filter({has:label('B2')});
  await build.focus();await page.keyboard.press('ArrowRight');
  assert.equal(await build.getAttribute('aria-expanded'),'true');
  await page.keyboard.press('ArrowDown');await page.keyboard.press('Enter');
  assert.deepEqual(await page.evaluate(()=>window.selected),[rows[1].path]);
  assert.equal(await page.locator('[aria-selected="true"] > .map-library-row > .map-library-name').innerText(),'Morning - Skirmish');
  await page.keyboard.press('ArrowDown');await page.keyboard.press('Enter');
  assert.equal(await page.locator('[aria-selected="true"] > .map-library-row > .map-library-name').innerText(),'Night - Push');
  await page.locator('#search').fill('colosseo night');
  assert.equal(await page.locator('.map-row').count(),2,'Search reveals matches through collapsed folders.');
  assert.equal(await page.locator('[data-kind="map"]').count(),1);
  assert.equal(await page.locator('[aria-selected="true"]').count(),1,'Searching retains the loaded variant.');
  await page.locator('#search').fill('no matches');assert.equal(await page.locator('#empty').isVisible(),true);
  await page.locator('#search').fill('');assert.equal(await page.locator('[aria-selected="true"]').count(),1);
  const chosen=page.locator('[aria-selected="true"]');await chosen.focus();await page.keyboard.press('ArrowLeft');
  assert.equal(await page.evaluate(()=>document.activeElement.dataset.kind),'folder');
  await page.keyboard.press('ArrowLeft');assert.equal(await build.getAttribute('aria-expanded'),'false');
  await page.keyboard.press('ArrowUp');assert.equal(await page.evaluate(()=>document.activeElement.dataset.kind),'map');
  await page.keyboard.press('End');assert.equal(await page.evaluate(()=>document.activeElement.textContent.includes('東京')),true);
  await page.evaluate(()=>tree.setSelected('C:/Exports/Maps/東京/B1/夜 - Hybrid.owmap'));
  assert.equal(await page.locator('[aria-selected="true"] > .map-library-row > .map-library-name').innerText(),'夜 - Hybrid');
  await page.locator('#search').fill('colosseo');
  await page.screenshot({path:path.join(scratch,'map-library.png')});
  assert.equal(await page.locator('.map-row').first().evaluate(element=>getComputedStyle(element).flexDirection),'row');
  assert.equal(await page.locator('.map-row').first().evaluate(element=>element.scrollWidth<=element.clientWidth),true);
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({passed:true,scratch,checks:'Grouping, search, selection, expansion, keyboard and compact rendering',geometryRequests:0}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
