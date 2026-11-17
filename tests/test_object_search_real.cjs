// Real structured Widowmaker export through the source backend and renderer.
// Matching/sorting/preview only: no asset changes, native exports, or mock APIs.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const {spawn} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const project=path.resolve(__dirname,'..');
const scratch=fs.mkdtempSync(path.resolve(project,'../preview-build/object-search-real-'));
const root=process.env.WHITEHOUND_REAL_ASSET_ROOT || 'C:\\Users\\Admin\\Desktop\\Overwatch Tool\\Whitehound\\library\\Heroes\\Widowmaker\\Skin\\Overwatch\\Overwatch Classic';
const entity3P=path.join(root,'Entities','Gameplay3P','Gameplay3P.owentity');
const entity1P=path.join(root,'Entities','Gameplay1P','Gameplay1P.owentity');
const sources=[entity3P,entity1P,
  path.join(root,'Models','00000000011F.00C','Animations','0','32','000000000A07.owanimclip'),
  path.join(root,'Models','00000000011F.00C','Animations','0','32','000000000231.owanimclip'),
  path.join(root,'Models','00000000013E.00C','Animations','0','32','000000000156.owanimclip'),
  path.join(root,'Models','0000000001D4.00C','Animations','0','4','000000003879.owanimclip')];
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const before=sources.map(file=>[file,hash(file)]);
const server=spawn(process.env.WHITEHOUND_TEST_PYTHON || 'python',[path.join(project,'app.py'),'--serve','--port','0','--data-dir',path.join(scratch,'Profile')],
  {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
let browser,log='';
const errors=[],report={passed:false,scratch,realAssets:root,mockedApis:false,nativeExportAttempted:false};
(async()=>{
  const address=await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(new Error('Backend startup: '+log)),15000);
    server.once('error',error=>{clearTimeout(timer);reject(error);});
    server.stderr.on('data',chunk=>{log+=chunk;});
    server.stdout.on('data',chunk=>{log+=chunk;const match=log.match(/http:\/\/127\.0\.0\.1:\d+/);if(match){clearTimeout(timer);resolve(match[0]);}});
  });
  browser=await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
  const page=await browser.newPage({viewport:{width:1600,height:1000}});
  page.setDefaultTimeout(30000);page.on('pageerror',error=>errors.push(error.message));
  await page.goto(address);
  await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled);
  assert.equal(await page.locator('#clip-sort').inputValue(),'object-match');
  assert.equal(await page.locator('#analyze-library, #animation-evidence').count(),0,'Matching requires no analysis/evidence workflow.');
  assert.equal(await page.locator('#track-count-filter, #track-groups, #export-c4d, #export-clip, #clip-name').count(),0,
    'Simplified library removes track groups and automatic nickname exports.');
  await page.locator('#root-path').fill(root);await page.locator('#scan-root').click();
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='241');
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js'),load=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;return load.apply(this,args);};
  });
  const loadEntity=async value=>{
    await page.locator('#entity-select').selectOption(value);await page.locator('#load-entity').click();
    await page.waitForFunction(()=>window.qaViewport?.models.length>0&&document.querySelector('#viewport-loading').hidden);
    await page.waitForFunction(()=>document.querySelector('#library-match-status').textContent.startsWith('Bars show bones'));
  };
  const row=name=>page.locator('.clip-row').filter({has:page.locator('.clip-name',{hasText:new RegExp('^'+name+'$')})});
  const coverage=async(name,wanted)=>assert.match(await row(name).locator('.clip-coverage-label').innerText(),wanted);
  const pausePlayback=async()=>{if(await page.locator('#play-pause').getAttribute('aria-label')==='Pause')await page.locator('#play-pause').click();};
  const assignments=()=>page.evaluate(()=>window.qaViewport.assignments.map(({modelId,clip})=>({modelId,path:clip.path})));
  await page.locator('#library-filters > summary').click();
  await loadEntity(entity3P);
  assert.match(await page.locator('#library-object option:checked').innerText(),/190 bones/);
  await coverage('000000000A07',/186 \/ 190 bones covered · Model folder/);
  await coverage('000000000231',/108 \/ 190 bones covered · Model folder/);
  await coverage('00000000371D',/190 \/ 190 bones covered/);
  const ratio=await row('000000000231').locator('.clip-coverage-fill').evaluate(element=>parseFloat(element.style.width));
  assert.ok(Math.abs(ratio-108/190*100)<.001,'The visual bar shows selected skeleton coverage, not108/112.');
  assert.match(await row('000000000231').locator('.clip-coverage').getAttribute('title'),/108 of 112 clip tracks match; 4 unmatched/);
  const order=await page.locator('.clip-name').allTextContents();
  assert.ok(order.indexOf('00000000371D')<order.indexOf('000000000A07'),'241tracks with190 matchingbones rank ahead of190tracks with186 matches.');

  const modelFilter=page.getByRole('checkbox',{name:'Hide unlikely matches'});
  assert.ok(await modelFilter.isChecked());
  for (const id of ['015D','0957','0958','0959','0073','0075','00F6','8DAE','04A0']) {
    assert.equal(await row(id.padStart(12,'0')).count(),0, 'Hide incompatible clip '+id);
  }
  for (const id of ['3733','0678','02A6','033B','863B','036B','0FD0']) {
    assert.equal(await row(id.padStart(12,'0')).count(),1, 'Keep compatible body clip '+id);
  }
  await modelFilter.uncheck();
  await loadEntity(entity3P);
  await page.waitForFunction(()=>Array.from(document.querySelectorAll('.clip-row')).some(
    el=>el.querySelector('.clip-name')?.textContent==='0000000004A0' && el.title.includes('within 5 cm')));
  assert.match(await row('00000000015D').getAttribute('title'),/another model/);
  await page.locator('#hide-duplicates').uncheck();
  assert.equal(await page.locator('.clip-row').count(),246);
  await page.locator('#hide-duplicates').check();
  assert.equal(await page.locator('.clip-row').count(),241);
  await page.locator('#clip-search').fill('00000000023');
  assert.equal(await page.locator('.clip-row').count(),4,'Text search includes every matching track count.');
  await page.locator('#clip-search').fill('');
  await row('000000000A07').click();
  await page.waitForFunction(()=>window.qaViewport.assignments.length===1&&document.querySelector('#viewport-loading').hidden);
  await pausePlayback();await page.locator('#timeline').fill('10');
  const saved=await assignments();
  await modelFilter.check();
  assert.deepEqual(await assignments(),saved);
  assert.equal(await page.locator('#timeline').inputValue(),'10');
  await page.locator('#clip-sort').selectOption('tracks-desc');
  await page.locator('#clip-search').fill('00000000023');
  assert.deepEqual(await assignments(),saved,'Sorting and search do not replace assigned animation.');
  assert.equal(await page.locator('#timeline').inputValue(),'10');
  await page.locator('#clip-search').fill('');
  await page.locator('#clip-sort').selectOption('object-match');
  assert.deepEqual(await assignments(),saved);
  await page.screenshot({path:path.join(scratch,'body-coverage.png')});

  await page.locator('#library-object').selectOption('model-1');
  assert.ok(await modelFilter.isDisabled());
  assert.equal(await page.locator('#rig-select').inputValue(),'model-1','Sidebar object selector and animation target are the same selection.');
  assert.match(await page.locator('#library-object option:checked').innerText(),/19 bones/);
  await coverage('000000003879',/18 \/ 19 bones covered · Model folder/);
  await coverage('0000000000F2',/0 \/ 19 bones covered/);
  const gunOrder=await page.locator('.clip-name').allTextContents();
  assert.ok(gunOrder.indexOf('000000003879')<gunOrder.indexOf('0000000000F2'),'Equal19trackcounts use actual joint matches and source context.');
  await row('000000003879').click();
  await page.waitForFunction(()=>window.qaViewport.assignments.length===2&&document.querySelector('#viewport-loading').hidden);
  await pausePlayback();const paired=await assignments();
  await page.locator('#clip-sort').selectOption('name');await page.locator('#clip-search').fill('0000000000F2');
  assert.deepEqual(await assignments(),paired,'Browsing a different candidate leaves both assigned clips intact.');
  await page.locator('#clip-search').fill('');await page.locator('#clip-sort').selectOption('object-match');
  await page.screenshot({path:path.join(scratch,'weapon-coverage.png')});
  await loadEntity(entity1P);
  await page.locator('#library-object').selectOption('model-0');
  assert.match(await page.locator('#library-object option:checked').innerText(),/85 bones/);
  assert.ok(await modelFilter.isDisabled());
  assert.equal(await row('00000000015D').count(),1);
  await coverage('000000000156',/85 \/ 85 bones covered · Model folder/);
  assert.match(await row('000000000156').locator('.clip-original').innerText(),/93 tracks/);
  assert.equal(await row('000000000156').locator('.clip-coverage-fill').evaluate(element=>parseFloat(element.style.width)),100);
  await page.screenshot({path:path.join(scratch,'pov-coverage.png')});
  for(const [file,digest] of before)assert.equal(hash(file),digest,'Source assets remain unchanged.');
  assert.deepEqual(errors,[]);
  Object.assign(report,{passed:true,fullBodyCoverage:[186,108,190],bodyDenominator:190,weaponCoverage:[18,0],weaponDenominator:19,
    povCoverage:[85,85],povClipTracks:93,trackGroupsRemoved:true,searchAndDedupeCounts:true,assignmentsPreserved:true,assetsUnchanged:true});
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=String(error.stack||error);console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  if(browser)await browser.close();server.kill();
});
