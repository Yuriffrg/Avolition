// Production model browser / grouping with a controlled preview service.
// The viewport is a lifecycle spy; real parser/WebGL isolation has a separate app test.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const web=path.resolve(__dirname,'../web'),url=source=>'data:text/javascript;charset=utf-8,'+encodeURIComponent(source).replaceAll("'",'%27');
const viewport=url(`export class PreviewViewport {
  constructor(){if(window.qaWebGLError)throw Error('WebGL unavailable');window.qaConstructed++;window.qaView=this;}
  clearAsset(){window.qaClearCount++;this.asset=null;}setActive(value){window.qaActive=value;}
  loadAsset(asset){if(!Array.isArray(asset.models))throw Error('Malformed geometry');this.asset=asset;window.qaRendered.push(asset.name);return {models:2,meshes:3,bones:7};}
  async loadMaterials(asset,resolve){await resolve('fixture-texture');return {texturedMeshes:3,warnings:['Preview texture resized to 512 pixels.']};}
  frameModel(){window.qaFrames++;}
}`);
const helper=fs.readFileSync(path.join(web,'model-library.js'),'utf8');
const source=fs.readFileSync(path.join(web,'model-browser.js'),'utf8').replace("from './viewer.js'",`from '${viewport}'`).replace("from './model-library.js'",`from '${url(helper)}'`);
const html=fs.readFileSync(path.join(web,'index.html'),'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'').replace(/<link\b[^>]*>/g,'');
const assetPath=(hero,skin,file)=>`C:\\Library\\Heroes\\${hero}\\Skin\\Overwatch\\${skin}\\${file.endsWith('.owmdl')?'Models':'Entities'}\\${file}`;
const entries=[
  {path:assetPath('Widowmaker','Classic','Gameplay3P.owentity'),boneCount:210},
  {path:assetPath('Mercy','Classic','000000000012.owmdl'),boneCount:1,size:1000},
  {path:assetPath('Mercy','Rose','Gameplay3P.owentity')},
  {path:assetPath('Mercy','Classic','000000000099.owentity'),boneCount:null,boneCountError:'Attached model could not be read.',size:null},
  {path:assetPath('Mercy','Classic','Gameplay3P.owentity'),boneCount:0,size:3},
  {path:assetPath('Widowmaker','Classic','Gameplay1P.owentity'),boneCount:105},
  {path:assetPath('Mercy','Classic','Gameplay1P.owentity'),boneCount:86,size:20},
];
const mercy1=entries[6].path,mercy3=entries[4].path,mercyModel=entries[1].path,widow1=entries[5].path;
let browser,checks=0;const equal=(actual,expected,label)=>{assert.deepEqual(actual,expected,label);checks++;};
(async()=>{
  const {describeModelEntry,modelBrowserGroups}=await import(url(helper));
  const numericEntries=[
    ['Gameplay1P.owentity',2,10],['Gameplay3P.owentity',10,2],['A.owmdl',2,10],['B.owmdl',0,0],['C.owentity',null,null],
  ].map(([name,boneCount,size])=>describeModelEntry({path:assetPath('Mercy','Classic',name),boneCount,size}));
  const sortedLabels=sort=>modelBrowserGroups(numericEntries,{sort})[0].children[0].children.map(item=>item.label);
  equal(sortedLabels('gameplay'),['Gameplay1P','Gameplay3P','C','A','B']);
  equal(sortedLabels('joints-desc'),['Gameplay3P','Gameplay1P','A','B','C'],'Joint sorting is numeric, overrides Gameplay priority, and leaves unknowns last.');
  equal(sortedLabels('joints-asc'),['B','Gameplay1P','A','Gameplay3P','C'],'Zero is a known value; equal counts use stable Gameplay/name ordering.');
  equal(sortedLabels('size-desc'),['Gameplay1P','A','Gameplay3P','B','C'],'Size sorting compares raw file bytes across entity and model rows.');
  equal(sortedLabels('size-asc'),['B','Gameplay3P','Gameplay1P','A','C'],'Unknown sizes stay last even in ascending order.');
  browser=await chromium.launch({headless:true});const page=await browser.newPage({viewport:{width:1440,height:940}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));await page.setContent(html);await page.addStyleTag({content:fs.readFileSync(path.join(web,'styles.css'),'utf8')});
  await page.evaluate(async({source,helper,entries})=>{
    window.qaCalls=[];window.qaRequests=[];window.qaRendered=[];window.qaLoads=[];window.qaActive=false;window.qaClearCount=0;window.qaConstructed=0;window.qaFrames=0;
    window.qaAnimationScene={path:'active-entity',assignments:['body-wave','weapon-idle'],camera:[3,4,5]};
    const api=async(method,...args)=>{
      window.qaCalls.push([method,...args]);
      if(method==='preview_entity')return new Promise((resolve,reject)=>window.qaRequests.push({path:args[0],resolve,reject}));
      if(method==='release_entity_preview')return {released:true};
      if(method==='load_texture')return {};
      throw Error('Unexpected API: '+method);
    };
    const {createModelBrowser}=await import('data:text/javascript;charset=utf-8,'+encodeURIComponent(source));
    const {sortModelEntries}=await import('data:text/javascript;charset=utf-8,'+encodeURIComponent(helper));
    window.qaSort=sortModelEntries(entries).map(entry=>entry.path);
    window.qaBrowser=createModelBrowser({api,onLoad:async path=>window.qaLoads.push(path),previewDelay:25});window.qaBrowser.setEntries(entries);
  },{source,helper,entries});
  const click=async selected=>page.evaluate(selected=>{const node=[...document.querySelectorAll('#model-browser-list [data-kind="asset"]')].find(node=>node.dataset.path===selected);if(!node)throw Error('Asset row is not visible: '+selected);node.querySelector('.model-browser-row').click();},selected);
  const finish=async(index,name='Fixture',malformed=false)=>{await page.evaluate(({index,name,malformed})=>window.qaRequests[index].resolve(malformed?{name}:{name,models:[],warnings:[]}),{index,name,malformed});};
  const leaves=()=>page.locator('#model-browser-list [data-kind="asset"]').evaluateAll(nodes=>nodes.map(node=>node.dataset.path));
  equal(await page.evaluate(()=>window.qaCalls.length),0,'Populating entries does not eagerly preview files.');
  equal(await page.evaluate(()=>window.qaSort.slice(0,5)),[mercy1,widow1,mercy3,entries[2].path,entries[0].path],'Gameplay entities lead the default sort.');
  await page.evaluate(path=>window.qaBrowser.open(path),mercy3);await page.waitForFunction(()=>window.qaRequests.length===1);
  equal(await page.locator('#model-browser-hero').inputValue(),'Mercy');equal(await page.locator('#model-browser-skin').inputValue(),'Classic');
  equal(await leaves(),[mercy1,mercy3,entries[3].path,mercyModel],'Gameplay1P then Gameplay3P lead the selected skin.');
  const summaries=await page.locator('#model-browser-list [data-kind="asset"]').evaluateAll(nodes=>nodes.map(node=>({
    label:node.querySelector('.model-browser-label').textContent,bones:node.querySelector('.model-browser-bones').textContent,
    badge:node.querySelector('.model-browser-detail').textContent,title:node.querySelector('.model-browser-row').title,
    boneTitle:node.querySelector('.model-browser-bones').title,
  })));
  equal(summaries.map(item=>item.bones),['86 bones','0 bones','— bones','1 bone'],'Scanned counts are visible while the initial preview has not resolved.');
  equal(await page.locator('#model-browser-list .model-browser-bones:visible').count(),4);
  equal(summaries.map(item=>item.label),['Gameplay1P','Gameplay3P','Entity 000000000099','Model 000000000012'],'Bone counts do not replace friendly names or IDs.');
  equal(summaries.map(item=>item.badge),['Entity','Entity','Entity','Model'],'The asset-type badges remain separate from counts.');
  equal(summaries.map(item=>item.title),[mercy1,mercy3,entries[3].path,mercyModel],'Source paths remain available on each row.');
  equal(summaries[2].boneTitle,'Attached model could not be read.','An unknown count explains the scan problem.');
  equal(await page.evaluate(()=>window.qaRendered),[],'List counts need no loaded preview geometry.');
  equal(await page.evaluate(()=>window.qaRequests[0].path),mercy3);equal(await page.evaluate(()=>window.qaLoads.length),0,'Opening/selection cannot replace the animation scene.');
  equal(await page.locator('#model-browser-sort').inputValue(),'gameplay');
  for(const [sort,expected] of [
    ['joints-desc',[mercy1,mercyModel,mercy3,entries[3].path]],
    ['joints-asc',[mercy3,mercyModel,mercy1,entries[3].path]],
    ['size-desc',[mercyModel,mercy1,mercy3,entries[3].path]],
    ['size-asc',[mercy3,mercy1,mercyModel,entries[3].path]],
    ['gameplay',[mercy1,mercy3,entries[3].path,mercyModel]],
  ]) {
    await page.locator('#model-browser-sort').selectOption(sort);equal(await leaves(),expected,`${sort} orders the visible context.`);
    equal(await page.locator('#model-browser-list [aria-selected="true"]').getAttribute('data-path'),mercy3,'Sorting preserves the selected asset.');
  }
  equal(await page.evaluate(()=>({requests:window.qaRequests.length,loads:window.qaLoads.length,title:document.querySelector('#model-browser-title').textContent,hero:document.querySelector('#model-browser-hero').value,skin:document.querySelector('#model-browser-skin').value})),
    {requests:1,loads:0,title:'Gameplay3P',hero:'Mercy',skin:'Classic'},'Sorting only reorders the list and leaves the pending preview and filters intact.');
  await click(mercy1);await click(mercyModel);
  await page.waitForTimeout(45);equal(await page.evaluate(()=>window.qaRequests.length),1,'Only one geometry request may be in flight.');
  await finish(0,'Stale body');await page.waitForFunction(()=>window.qaRequests.length===2);
  equal(await page.evaluate(()=>window.qaRequests[1].path),mercyModel,'Rapid selections coalesce to the latest pending asset.');
  equal(await page.evaluate(()=>window.qaRendered),[],'A late earlier result never appears as the selected model.');
  await page.locator('#model-browser-sort').selectOption('size-desc');
  await page.locator('#model-browser-close').click();await page.waitForFunction(()=>window.qaCalls.some(call=>call[0]==='release_entity_preview'));
  equal(await page.locator('#model-browser').isVisible(),false);equal(await page.evaluate(()=>window.qaActive),false);
  await page.evaluate(path=>window.qaBrowser.open(path),mercy1);
  equal(await page.locator('#model-browser-sort').inputValue(),'size-desc','Reopening retains the chosen sorting mode for this session.');
  equal(await leaves(),[mercyModel,mercy1,mercy3,entries[3].path]);
  await page.locator('#model-browser-sort').selectOption('gameplay');
  equal(await page.evaluate(()=>window.qaRequests.length),2,'Reopening waits for the earlier request rather than queueing native parses.');
  await finish(1,'Closed stale model');await page.waitForFunction(()=>window.qaRequests.length===3);
  equal(await page.evaluate(()=>window.qaRendered),[],'Closing invalidates an unfinished preview response.');
  equal(await page.evaluate(()=>window.qaConstructed),0,'Closing before geometry arrives never allocates WebGL.');
  equal(await page.evaluate(()=>window.qaRequests[2].path),mercy1);await finish(2,'Mercy first person');
  await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.startsWith('Preview ready'));
  equal(await page.locator('#model-browser-stats').innerText(),'2 objects · 3 meshes · 7 bones');
  equal(await page.locator('#model-browser-status').innerText(),'Preview ready. See preview notes for details.');
  equal(await page.evaluate(()=>window.qaCalls.find(call=>call[0]==='load_texture')),['load_texture','fixture-texture',512]);
  equal(await page.evaluate(()=>window.qaAnimationScene),{path:'active-entity',assignments:['body-wave','weapon-idle'],camera:[3,4,5]});
  await page.evaluate(()=>window.qaBrowser.whenIdle());
  await page.locator('#model-browser-sort').selectOption('size-desc');await page.waitForTimeout(45);
  equal(await page.evaluate(()=>({requests:window.qaRequests.length,rendered:window.qaRendered,asset:window.qaView.asset?.name,loads:window.qaLoads})),
    {requests:3,rendered:['Mercy first person'],asset:'Mercy first person',loads:[]},'Sorting a ready preview never parses it again, replaces it, or loads it into Animations.');
  await page.locator('#model-browser-sort').selectOption('gameplay');
  await page.locator('#model-browser-list [aria-selected="true"]').focus();await page.keyboard.press('ArrowDown');await page.waitForFunction(()=>window.qaRequests.length===4);
  equal(await page.evaluate(()=>window.qaRequests[3].path),mercy3,'Arrow navigation previews the next asset without loading it.');
  await finish(3,'Mercy third person');await page.evaluate(()=>window.qaBrowser.whenIdle());
  await page.locator('#model-browser-kind').selectOption('model');equal(await leaves(),[mercyModel]);
  await page.locator('#model-browser-kind').selectOption('');await page.locator('#model-browser-hero').selectOption('');
  await page.locator('#model-browser-search').fill('Widowmaker Gameplay1P');equal(await leaves(),[widow1],'Text search includes hero context and source names.');
  equal(await page.locator('#model-browser-list .model-browser-bones').innerText(),'105 bones','Counts survive hero/name filtering.');
  await page.locator('#model-browser-list [data-kind="hero"] > .model-browser-row').click();equal(await leaves(),[],'Search groups can still collapse.');
  await page.locator('#model-browser-sort').selectOption('joints-desc');
  equal(await leaves(),[],'Sorting keeps a manually collapsed search hierarchy closed.');
  equal(await page.locator('#model-browser-search').inputValue(),'Widowmaker Gameplay1P','Sorting retains the search query.');
  await page.locator('#model-browser-list [data-kind="hero"] > .model-browser-row').click();equal(await leaves(),[widow1]);
  await click(widow1);await page.waitForFunction(()=>window.qaRequests.length===5);
  await page.evaluate(()=>window.qaRequests[4].reject(Error('The source file was removed')));
  await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.includes('Preview unavailable'));
  equal(await page.locator('#model-browser-load').isEnabled(),true,'An unavailable preview still permits the explicit main loader.');
  equal(await page.evaluate(()=>window.qaLoads.length),0);
  await page.locator('#model-browser-load').click();await page.waitForFunction(()=>!window.qaBrowser.isOpen());
  equal(await page.evaluate(()=>window.qaLoads),[widow1],'Only the explicit Load button commits an item.');
  equal(await page.evaluate(()=>window.qaView.asset),null,'Closing frees the preview model.');
  await page.evaluate(path=>window.qaBrowser.open(path),mercyModel);await page.waitForFunction(()=>window.qaRequests.length===6);await finish(5,'Malformed',true);
  await page.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.includes('Malformed geometry'));
  equal(await page.locator('#model-browser-load').isEnabled(),true);
  await page.keyboard.press('Escape');equal(await page.evaluate(()=>window.qaBrowser.isOpen()),false,'Escape closes the browser without loading.');
  equal(await page.evaluate(()=>window.qaLoads.length),1);
  await page.evaluate(()=>{window.qaBrowser.setEntries([]);window.qaBrowser.open();});
  equal(await page.locator('#model-browser-load').isDisabled(),true);equal(await page.locator('#model-browser-no-results').isVisible(),true);
  await page.locator('#model-browser-cancel').click();equal(await page.evaluate(()=>window.qaRequests.length),6,'Opening an empty library performs no preview request.');
  const noGPU=await browser.newPage();noGPU.on('pageerror',error=>errors.push(error.message));
  await noGPU.setContent(html);
  await noGPU.evaluate(async({source,path})=>{
    window.qaWebGLError=true;window.qaFallback=[];
    const {createModelBrowser}=await import('data:text/javascript;charset=utf-8,'+encodeURIComponent(source));
    window.qaBrowser=createModelBrowser({api:async method=>method==='preview_entity'?{name:'Fixture',models:[],warnings:[]}:{released:true},onLoad:path=>window.qaFallback.push(path)});
    window.qaBrowser.setEntries([{path}]);window.qaBrowser.open(path);
  },{source,path:mercy1});
  await noGPU.waitForFunction(()=>document.querySelector('#model-browser-status').textContent.includes('WebGL unavailable'));
  equal(await noGPU.locator('#model-browser-load').isEnabled(),true,'WebGL failure preserves the direct Load action.');
  await noGPU.locator('#model-browser-load').click();equal(await noGPU.evaluate(()=>window.qaFallback),[mercy1]);
  await noGPU.close();
  equal(errors,[]);
  console.log(JSON.stringify({passed:true,checks,productionBrowser:true,mockedViewport:true,mockedBackend:true,singleInflight:true,networkRequired:false}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
