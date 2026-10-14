// HISTORICAL HARNESS: v0.8.7-era Analyze Library / Animation evidence UI.
// Retained for the earlier interface; current UI deliberately removed it.
// Current object-matching tests are listed in README.md; no assertions are skipped.
// Renderer integration using synthetic evidence and mocked backend APIs only.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../web');
const scratch = fs.mkdtempSync(path.resolve(__dirname, '../../preview-build/animation-analysis-ui-'));
const server = http.createServer((req,res) => {
  const name = req.url === '/' ? 'index.html' : req.url.slice(1).split('?')[0];
  if (!['index.html','styles.css','animation-analysis.js'].includes(name)) {res.writeHead(404);res.end();return;}
  let content = fs.readFileSync(path.join(root,name),'utf8');
  if (name === 'index.html') content=content.replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'');
  res.setHeader('Content-Type',name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html');
  res.end(content);
});
let browser;
const report = {mockedApis:true,realAssetAnalysis:false,scratch};
(async()=>{
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  browser = await chromium.launch({headless:true});
  const page = await browser.newPage({viewport:{width:1440,height:940}});
  const errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.evaluate(async()=>{
    const {initAnimationAnalysis}=await import('./animation-analysis.js');
    window.calls=[];
    window.changes=0;
    window.clip={path:'D:\\Fixture\\Named <context>\\001.owanimclip',name:'001.owanimclip',annotation:{name:'Manual label'}};
    window.snapshot={root:'D:\\Fixture',state:'Ready',busy:false,revision:null};
    window.index={root:'D:\\Fixture',revision:'revision-1',entries:[{path:window.clip.path,assetId:'scoped:<test>',modelIds:['model-a','model-b'],contextIds:['context-1','context-2'],statuses:['referenced-by-named-context']}],models:[{id:'model-a',label:'Model A'},{id:'model-b',label:'Model B'}],contexts:[{id:'context-1',label:'Named <context>'},{id:'context-2',label:'Another context'}],assetCount:1,occurrenceCount:2};
    window.detail={asset:{id:'scoped:<test>',resource:{fullGuid:null,type:'006',limitation:'A short filename cannot establish a full GUID.'},sourceName:null,statuses:['referenced-by-named-context'],durationSeconds:1,fps:30,trackCount:2,trackDefinition:'Test fixture track definition',animatedBoneIds:['0001','0002'],models:[{id:'model-a',kind:'explicit-export-path'},{id:'model-b',kind:'compatible-bones',matchedBoneCount:2,animatedBoneCount:2}],contexts:[{id:'context-1',label:'Named <context>',role:'Broad dependency only'},{id:'context-2',label:'Another context'}],suggestedLabels:[],conflicts:['This fixture is reused.'],parseErrors:[],evidence:[{kind:'fixture',source:'Synthetic test only',field:'test-field',value:'18446744073709551615'}]},occurrence:{path:window.clip.path,relativePath:'Named <context>/001.owanimclip',filename:'001.owanimclip',nickname:{value:'Manual label',provenance:'User'},priority:0,group:36},occurrences:[{path:window.clip.path},{path:'D:\\Fixture\\Other\\001.owanimclip'}],limitations:['Exporting Toolchain version is unknown.']};
    window.backend=async(method,...args)=>{
      window.calls.push([method,...structuredClone(args)]);
      if(method==='analysis_status')return structuredClone(window.snapshot);
      if(method==='analysis_index')return structuredClone(window.index);
      if(method==='analysis_detail')return structuredClone(window.detail);
      if(method==='analysis_start'){window.snapshot={...window.snapshot,busy:true,state:'Analyzing',revision:null,message:'Reading local metadata…'};return structuredClone(window.snapshot);}
      throw new Error(`Unexpected API ${method}`);
    };
    window.controller=initAnimationAnalysis(window.backend,{changed:()=>window.changes++,allAnimations:()=>{document.querySelector('#clip-search').value='';}});
    window.canvas=document.querySelector('#viewport');
    window.canvas.dataset.preserved='yes';
    document.querySelector('#playing-title').textContent='Playback stays the same';
    document.querySelector('#clip-name').value='Manual label';
    document.querySelector('#library-empty').hidden=true;
    await window.controller.setRoot('D:\\Fixture');
    await window.controller.select(window.clip);
  });
  assert.equal(await page.locator('#analyze-library').isDisabled(),false);
  assert.equal(await page.locator('#analysis-filters').isVisible(),false);
  await page.locator('#animation-evidence>summary').click();
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Analyze the library/);
  await page.locator('#analyze-library').click();
  await page.waitForFunction(()=>document.querySelector('#analyze-library').disabled);
  assert.equal(await page.locator('#analysis-spinner').isVisible(),true);
  await page.evaluate(async()=>{
    window.snapshot={...window.snapshot,busy:false,state:'Completed',revision:'revision-1',message:'1 asset, 2 occurrences analyzed.'};
    await window.controller.refresh();
  });
  await page.waitForFunction(()=>!document.querySelector('#analysis-filters').hidden);
  assert.equal(await page.locator('#analyze-library').isDisabled(),false);
  assert.match(await page.locator('#animation-evidence-status').textContent(),/Referenced by named context/);
  const evidence=await page.locator('#animation-evidence-content').textContent();
  assert.match(evidence,/Full resource GUIDUnknown/);
  assert.match(evidence,/Original source nameUnknown/);
  assert.match(evidence,/2 \/ 2 animated bone IDs matched/);
  assert.match(evidence,/Named <context>/);
  assert.match(evidence,/Another context/);
  assert.match(evidence,/Original identifier001.owanimclip/);
  assert.match(evidence,/Your nicknameManual label/);
  assert.match(evidence,/18446744073709551615/,'64-bit evidence identifiers remain literal strings.');
  assert.equal(await page.locator('#animation-evidence-content script').count(),0);
  await page.locator('#analysis-filters>summary').click();
  await page.locator('#analysis-model').selectOption('model-b');
  await page.locator('#analysis-context').selectOption('context-2');
  assert.equal(await page.evaluate(()=>window.controller.filtered([window.clip]).length),1);
  assert.equal(await page.locator('#analysis-all').isVisible(),true);
  await page.locator('#clip-search').fill('old search');
  await page.locator('#analysis-all').click();
  assert.equal(await page.locator('#analysis-model').inputValue(),'');
  assert.equal(await page.locator('#analysis-context').inputValue(),'');
  assert.equal(await page.locator('#clip-search').inputValue(),'');
  assert.equal(await page.evaluate(()=>window.canvas===document.querySelector('#viewport')&&window.canvas.dataset.preserved==='yes'),true);
  assert.equal(await page.locator('#playing-title').textContent(),'Playback stays the same');
  assert.equal(await page.locator('#clip-name').inputValue(),'Manual label');
  assert.equal(await page.evaluate(()=>window.calls.some(([method])=>/load_|annotat|export|assign/.test(method))),false,'Evidence UI must not issue playback or nickname/export writes.');
  await page.evaluate(async()=>{
    window.clip.annotation.name='Updated nickname';
    await window.controller.select(window.clip);
  });
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Your nicknameUpdated nickname/,'Current nickname wins over cached inventory label.');
  await page.screenshot({path:path.join(scratch,'evidence.png')});
  await page.setViewportSize({width:1024,height:700});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  assert.ok(await page.locator('#clip-list').evaluate(element=>element.getBoundingClientRect().height)>20,'Compact evidence controls retain scrollable library space.');
  await page.screenshot({path:path.join(scratch,'evidence-compact.png')});
  await page.evaluate(async()=>{
    window.detail={asset:{id:'broken',resource:{},statuses:['unknown'],parseErrors:['Truncated fixture header.']},occurrence:{filename:'broken.owanimclip'},occurrences:[],limitations:[]};
    await window.controller.select({path:'D:\\Fixture\\broken.owanimclip',annotation:{name:''}});
  });
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Truncated fixture header/);
  assert.match(await page.locator('#animation-evidence-status').textContent(),/Unknown/);
  await page.evaluate(()=>window.controller.suspend());
  assert.equal(await page.locator('#analyze-library').isDisabled(),true);
  await page.evaluate(()=>window.controller.resume());
  assert.equal(await page.locator('#analyze-library').isDisabled(),false);
  await page.evaluate(async()=>{
    window.snapshot={...window.snapshot,busy:true,state:'Checking cache',revision:null,message:'Checking current local files…'};
    await window.controller.refresh();
  });
  await page.waitForFunction(()=>document.querySelector('#analysis-filters').hidden);
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Checking local evidence/,'Cache revalidation hides earlier evidence before the worker finishes.');
  await page.evaluate(async()=>{
    window.snapshot={...window.snapshot,busy:false,state:'Failed',revision:null,error:'Files changed during analysis. Analyze again.'};
    await window.controller.refresh();
  });
  await page.waitForFunction(()=>document.querySelector('#analysis-filters').hidden);
  assert.equal(await page.locator('#analysis-status').textContent(),'Files changed during analysis. Analyze again.');
  assert.match(await page.locator('#animation-evidence-content').textContent(),/Analyze the library/,'Failed cache validation discards earlier evidence and clears the checking message.');
  await page.evaluate(async()=>{
    window.snapshot={root:'E:\\Other',state:'Ready',busy:false,revision:null};
    await window.controller.setRoot('E:\\Other');
  });
  assert.equal(await page.locator('#analysis-filters').isVisible(),false,'New roots cannot retain prior evidence filters.');
  await page.evaluate(()=>window.controller.stop());
  assert.deepEqual(errors,[]);
  report.passed=true;
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=error.stack;console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  if(browser)await browser.close();
  server.close();
});
