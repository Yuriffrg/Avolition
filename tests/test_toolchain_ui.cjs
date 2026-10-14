// Renderer integration with explicitly mocked release/process APIs. No game extraction.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../web');
const scratch = fs.mkdtempSync(path.resolve(__dirname, '../../preview-build/toolchain-ui-'));
async function serveFixture(route) {
  const requestPath = new URL(route.request().url()).pathname;
  const name = requestPath === '/' ? 'index.html' : requestPath.slice(1);
  if (!['index.html', 'styles.css', 'toolchain.js'].includes(name)) return route.fulfill({status:404,body:''});
  let content = fs.readFileSync(path.join(root, name), 'utf8');
  if (name === 'index.html') content = content.replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
  return route.fulfill({status:200,contentType:name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html',body:content});
}
let browser;
const report = {mockedApis: true, realExtraction: false, scratch};
(async () => {
  browser = await chromium.launch({headless:true});
  const context = await browser.newContext({viewport:{width:1440,height:940}});
  // All renderer fixtures are fulfilled locally; this mocked test needs no network access.
  await context.route('http://whitehound.test/**', serveFixture);
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('http://whitehound.test/');
  await page.evaluate(async () => {
    const {initToolchain, formatToolchainDate, sameDirectory} = await import('./toolchain.js');
    window.helpers = {formatToolchainDate, sameDirectory};
    window.settings = {activeTab:'animations',overwatchInstall:'D:\\Games\\Overwatch 游戏',outputDirectory:'D:\\Whitehound\\library',libraryDirectory:'D:\\Whitehound\\library'};
    window.calls = [];
    window.refreshed = [];
    window.snapshot = {runtime:{state:'Ready',installed:null,latest:null,updateAvailable:false},job:{state:'Ready',busy:false,logs:[],catalog:{}},capabilities:{categories:[{id:'skin',label:'Skins + gameplay animations',description:'Supported skins and their gameplay model animations.'},{id:'emote',label:'Emotes',description:'Hero emote animations.'}]}};
    window.backend = async (method, ...args) => {
      window.calls.push([method,...structuredClone(args)]);
      if (method === 'workflow_settings') {Object.assign(window.settings,args[0]);return {...window.settings};}
      if (method === 'toolchain_status') return structuredClone(window.snapshot);
      if (method === 'toolchain_validate') return window.validation || {ok:true,errors:{}};
      if (method === 'use_library_directory') {window.settings.outputDirectory=window.settings.libraryDirectory;return {...window.settings};}
      if (method === 'browse_workflow_directory') return args[0] === 'overwatchInstall' ? 'E:\\New 游戏\\Overwatch' : 'F:\\Custom exports';
      if (method === 'toolchain_list') {window.snapshot.job={...window.snapshot.job,state:'Loading',busy:true,kind:'catalog',jobId:'catalog-1'};return {};}
      if (method === 'toolchain_start') {window.snapshot.job={...window.snapshot.job,state:'Exporting',busy:true,kind:'export',jobId:'export-1',logs:[{seq:1,stream:'stdout',text:'Export started'}]};return {};}
      if (method === 'toolchain_cancel') {window.snapshot.job={...window.snapshot.job,state:'Cancelled',busy:false};return {};}
      return {};
    };
    window.controller=initToolchain(window.backend,{refreshLibrary:output=>window.refreshed.push(output)},{workflow:window.settings});
    window.originalCanvas=document.querySelector('#viewport');
    window.originalCanvas.dataset.preserved='yes';
  });
  await page.waitForFunction(() => document.querySelector('#toolchain-category').options.length === 2);
  assert.equal(await page.locator('#workflow-animations').isVisible(),true);
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false,'Toolchain paths do not take space in Animations.');
  assert.equal(await page.locator('#analyze-library, #analysis-filters, #animation-evidence').count(),0,'Retired analysis controls are absent.');
  assert.equal(await page.locator('#toolchain-open-output').count(),0,'The redundant Toolchain output-folder shortcut is removed.');
  assert.equal(await page.locator('#maps-open-output,#open-export-output').count(),2,'Map and animation export destinations retain their folder shortcuts.');
  assert.equal(await page.locator('#root-help').innerText(),'Scans character models and animations in subfolders. Map exports are excluded.');
  assert.equal(await page.locator('#toolchain-output').inputValue(),'D:\\Whitehound\\library');
  await page.locator('#clip-search').fill('keep this selection');
  await page.locator('#tab-toolchain').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true);
  const geometry = await page.evaluate(() => {
    const rect = selector => {const r=document.querySelector(selector).getBoundingClientRect();return {x:r.x,y:r.y,w:r.width,h:r.height,bottom:r.bottom};};
    return {left:rect('.workflow-directory'),right:rect('.workflow-directory:last-child'),tabs:rect('.workflow-tabs'),badge:rect('.local-badge'),dirs:rect('.workflow-directories')};
  });
  assert.equal(geometry.left.w,geometry.right.w,'Toolchain directory groups occupy equal halves.');
  assert.ok(Math.abs(geometry.tabs.y+geometry.tabs.h/2-geometry.badge.y-geometry.badge.h/2)<1,'Tabs align with Local Preview.');
  assert.ok(Math.abs(geometry.tabs.bottom-geometry.dirs.y)<2,'Directory row immediately follows header.');
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true);
  assert.equal(await page.locator('#workflow-animations').isVisible(),false);
  assert.equal(await page.locator('#toolchain-install').isVisible(),true);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  await page.locator('#tab-animations').click();
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  assert.equal(await page.locator('#toolchain-output').inputValue(),'D:\\Whitehound\\library','Hidden Toolchain paths keep their values.');
  assert.equal(await page.locator('#clip-search').inputValue(),'keep this selection');
  assert.equal(await page.evaluate(() => window.originalCanvas===document.querySelector('#viewport') && window.originalCanvas.dataset.preserved==='yes'),true);
  await page.locator('#tab-animations').focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#tab-maps').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false,'Map preview has no Toolchain installation fields.');
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#tab-toolchain').getAttribute('aria-selected'),'true');
  await page.locator('#browse-overwatch-install').click();
  await page.waitForFunction(() => document.querySelector('#overwatch-install').value==='E:\\New 游戏\\Overwatch');
  await page.locator('#toolchain-output').fill('E:\\Exports with spaces\\字符');
  await page.locator('#toolchain-output').blur();
  await page.evaluate(() => window.controller.flush());
  assert.equal(await page.evaluate(() => window.settings.outputDirectory),'E:\\Exports with spaces\\字符');
  assert.equal(await page.evaluate(() => window.settings.activeTab),'toolchain');
  await page.locator('#use-library-directory').click();
  await page.waitForFunction(() => document.querySelector('#toolchain-output').value==='D:\\Whitehound\\library');
  await page.evaluate(async () => {
    window.snapshot.runtime={state:'Ready',installed:{version:'test-installed',releaseDate:'2025-01-02T03:04:05Z',packageUpdatedAt:'2025-01-03T03:04:05Z',installedAt:'2025-02-01T04:05:06Z'},latest:{version:'test-available',releaseDate:'2025-04-01T00:00:00Z',packageUpdatedAt:'2025-04-02T00:00:00Z'},updateAvailable:true};
    await window.controller.refresh();
  });
  assert.equal(await page.locator('#toolchain-version').textContent(),'test-installed');
  assert.equal(await page.locator('#toolchain-release-date').textContent(),'2025-01-02 03:04:05 UTC');
  assert.match(await page.locator('#toolchain-available').textContent(),/test-available.*2025-04-01/);
  assert.equal(await page.locator('#toolchain-update').isVisible(),true);
  assert.equal(await page.locator('#toolchain-install').isVisible(),false);
  await page.evaluate(() => {window.validation={ok:false,errors:{overwatchInstall:'Choose an installed Overwatch folder.',outputDirectory:'This folder is not writable.'}};});
  await page.locator('#toolchain-load-heroes').click();
  await page.waitForFunction(() => !document.querySelector('#overwatch-install-error').hidden);
  assert.equal(await page.locator('#toolchain-output-error').textContent(),'This folder is not writable.');
  assert.equal(await page.evaluate(() => window.calls.some(call=>call[0]==='toolchain_list')),false);
  await page.locator('#overwatch-install').fill('E:\\New 游戏\\Overwatch\\_retail_');
  await page.locator('#overwatch-install').blur();
  await page.evaluate(() => {window.validation={ok:true,errors:{},overwatchInstall:'E:\\New 游戏\\Overwatch'};});
  await page.locator('#toolchain-load-heroes').click();
  await page.waitForFunction(() => document.querySelector('#toolchain-job-state').textContent==='Loading');
  assert.equal(await page.locator('#overwatch-install').inputValue(),'E:\\New 游戏\\Overwatch','Validated _retail_ selection resolves to the game root.');
  assert.equal(await page.locator('#toolchain-update').isDisabled(),true);
  assert.equal(await page.locator('#toolchain-cancel').isDisabled(),false);
  await page.evaluate(async () => {
    window.snapshot.job={...window.snapshot.job,busy:false,state:'Completed',catalog:{install:window.settings.overwatchInstall,heroes:[{id:'test',name:'Hero Fixture'}],unlocks:{'Hero Fixture':[{id:'skin-fixture',name:'Skin Fixture | Literal',category:'skin'},{id:'emote-fixture',name:'Emote Fixture',category:'emote'}]}}};
    await window.controller.refresh();
  });
  await page.locator('#toolchain-hero').selectOption('Hero Fixture');
  await page.locator('#toolchain-asset').selectOption('Skin Fixture | Literal');
  await page.locator('#toolchain-export').click();
  await page.waitForFunction(() => document.querySelector('#toolchain-job-state').textContent==='Exporting');
  const call=await page.evaluate(() => window.calls.find(call=>call[0]==='toolchain_start'));
  assert.equal(call[1].asset,'Skin Fixture | Literal','Renderer passes user choice as a literal field.');
  assert.equal(call[1].includeAnimations,true);
  assert.equal(call[1].includeSound,false);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  await page.locator('#tab-animations').click();
  assert.equal(await page.evaluate(() => window.snapshot.job.busy),true,'Tab switch does not cancel a job.');
  await page.locator('#tab-toolchain').click();
  await page.locator('#toolchain-cancel').click();
  await page.waitForFunction(() => document.querySelector('#toolchain-job-state').textContent==='Cancelled');
  await page.evaluate(async () => {
    window.snapshot.job={...window.snapshot.job,state:'Completed',kind:'export',jobId:'export-complete',result:{outputDirectory:'D:\\Whitehound\\library'},logs:[{seq:1,stream:'stdout',text:'Export complete'}]};
    await window.controller.refresh();await window.controller.refresh();
  });
  assert.deepEqual(await page.evaluate(() => window.refreshed),['D:\\Whitehound\\library'],'Successful library export refreshes once.');
  await page.evaluate(async () => {
    window.snapshot.runtime.error='GitHub is offline. Your installed copy remains available.';
    window.snapshot.job={...window.snapshot.job,state:'Failed',error:'Unsupported game build.',jobId:'failed-1',logs:[{seq:1,stream:'stderr',text:'<script>not HTML</script>'}]};
    await window.controller.refresh();
  });
  assert.equal(await page.locator('#toolchain-log-panel').getAttribute('open'),'');
  assert.match(await page.locator('#toolchain-log').textContent(),/\[stderr\] <script>not HTML<\/script>/);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),false,'A failed update check leaves valid installed runtime usable.');
  await page.screenshot({path:path.join(scratch,'toolchain.png')});
  await page.locator('#tab-animations').click();
  await page.screenshot({path:path.join(scratch,'animations.png')});
  await page.setViewportSize({width:1024,height:700});
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth<=innerWidth),true);
  await page.screenshot({path:path.join(scratch,'animations-compact.png')});
  // Layout-only assignment fixtures exercise the compact viewport budget.
  await page.evaluate(() => {
    const list = document.querySelector('#animation-set');
    list.hidden = false;
    document.querySelector('#animation-set-export-controls').hidden = false;
    for (let index = 0; index < 3; index++) {
      const row = document.createElement('div');row.className = 'animation-set-row';
      const object = document.createElement('button');object.className = 'animation-set-object';object.textContent = `Fixture model ${index} (85 bones)`;
      const clip = document.createElement('span');clip.className = 'animation-set-clip';clip.textContent = `Fixture clip ${index} · 1.50 s`;
      const clear = document.createElement('button');clear.className = 'animation-set-clear';clear.textContent = 'Clear';
      row.append(object, clip, clear);list.append(row);
    }
  });
  const assignmentGeometry = await page.evaluate(() => ({viewportHeight:document.querySelector('.viewport-wrap').getBoundingClientRect().height,timelineBottom:document.querySelector('.timeline-panel').getBoundingClientRect().bottom,footerTop:document.querySelector('.status-bar').getBoundingClientRect().top}));
  assert.ok(assignmentGeometry.viewportHeight>=150,'Compact assignment setup retains a usable preview.');
  assert.ok(assignmentGeometry.timelineBottom<=assignmentGeometry.footerTop+1,'Assigned objects do not push playback below the window.');
  await page.screenshot({path:path.join(scratch,'assignments-compact.png')});
  assert.equal(await page.evaluate(() => window.helpers.formatToolchainDate('not a date')),'Unknown');
  assert.equal(await page.evaluate(() => window.helpers.sameDirectory('D:/Library/','d:\\library')),true);
  await page.evaluate(() => {window.validation={ok:true,errors:{}};});
  await page.locator('#tab-toolchain').click();
  await page.locator('#overwatch-install').fill('F:\\Another Game Installation');
  await page.locator('#overwatch-install').blur();
  await page.evaluate(() => window.controller.flush());
  assert.equal(await page.locator('#toolchain-hero').isDisabled(),true,'Changing the game directory invalidates the prior catalog.');
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  await page.evaluate(async () => {window.controller.suspend();await window.controller.flush();await window.controller.refresh();});
  assert.equal(await page.locator('#toolchain-check').isDisabled(),true,'Polling and save completion cannot re-enable controls during close.');
  assert.equal(await page.locator('#tab-animations').isDisabled(),true);
  await page.evaluate(() => window.controller.resume());
  assert.equal(await page.locator('#toolchain-check').isDisabled(),false);
  assert.equal(await page.locator('#tab-animations').isDisabled(),false);
  const browserPage = await context.newPage();
  await browserPage.goto('http://whitehound.test/');
  await browserPage.evaluate(async () => {
    const {initToolchain} = await import('./toolchain.js');
    window.controller = initToolchain(async (method, changes) => {
      if(method==='toolchain_status') return {runtime:{},job:{},capabilities:{}};
      if(method==='workflow_settings') throw new Error('Use an absolute directory path.');
      return {};
    }, {}, {browserMode:true,workflow:{activeTab:'toolchain',outputDirectory:'D:\\Library'}});
  });
  assert.equal(await browserPage.locator('#workflow-toolchain').isVisible(),true,'A restored Toolchain tab shows its directories immediately.');
  assert.equal(await browserPage.locator('#toolchain-directories').isVisible(),true);
  assert.equal(await browserPage.locator('#browse-overwatch-install').isDisabled(),true);
  assert.equal(await browserPage.locator('#browse-toolchain-output').isDisabled(),true);
  assert.equal(await browserPage.locator('#toolchain-open-output').count(),0);
  await browserPage.locator('#toolchain-output').fill('relative/path');
  await browserPage.locator('#toolchain-output').blur();
  await browserPage.waitForFunction(() => !document.querySelector('#toolchain-output-error').hidden);
  assert.equal(await browserPage.locator('#toolchain-output-error').textContent(),'Use an absolute directory path.');
  await browserPage.evaluate(() => window.controller.stop());
  await browserPage.close();
  await page.evaluate(() => window.controller.stop());
  assert.deepEqual(errors,[]);
  report.passed=true;
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=error.stack;console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  if(browser) await browser.close();
});
