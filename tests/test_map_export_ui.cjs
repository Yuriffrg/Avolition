// Production Maps controller and DOM, with deterministic background-job replies.
// Geometry/material rendering and native APIs are fixtures; no file is exported.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const web=path.resolve(__dirname,'../web');
const moduleURL=source=>'data:text/javascript;charset=utf-8,'+encodeURIComponent(source).replaceAll("'",'%27');
const viewer=moduleURL(`export class PreviewViewport {
  constructor(){this.objects=[];} setOptions(){} frameModel(){}
  loadAsset(asset){this.objects=asset.objects;return {models:1,meshes:1,polygons:2};}
  async loadMaterials(){return {texturedMeshes:1,warnings:[]};}
  getObjects(){return this.objects;} getVisiblePolygonCount(){return this.objects.length*2;}
  setAllObjectsVisible(){} setObjectVisible(){} clearAsset(){this.objects=[];}
}`);
const materials=moduleURL('export const mapPreviewMaterialOptions=()=>({textureEdge:512});');
const objectMenu=moduleURL(fs.readFileSync(path.join(web,'object-menu.js'),'utf8'));
const mapLibrary=moduleURL(fs.readFileSync(path.join(web,'map-library.js'),'utf8'));
const source=fs.readFileSync(path.join(web,'maps.js'),'utf8')
  .replace("from './viewer.js'",`from '${viewer}'`).replace("from './materials.js'",`from '${materials}'`)
  .replace("from './object-menu.js'",`from '${objectMenu}'`).replace("from './map-library.js'",`from '${mapLibrary}'`);
const html=fs.readFileSync(path.join(web,'index.html'),'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'').replace(/<link\b[^>]*>/g,'');
let browser,checks=0;const errors=[];
const equal=(actual,expected,label)=>{assert.deepEqual(actual,expected,label);checks++;};
const match=(actual,expected,label)=>{assert.match(actual,expected,label);checks++;};
async function createPage(job=null) {
  const page=await browser.newPage({viewport:{width:1500,height:960}});page.on('pageerror',error=>errors.push(error.message));
  await page.setContent(html);await page.addStyleTag({content:fs.readFileSync(path.join(web,'styles.css'),'utf8')});
  await page.evaluate(async({source,job})=>{
    document.querySelector('#workflow-animations').hidden=true;document.querySelector('#workflow-maps').hidden=false;
    // Advance only the controller's polling/backoff waits. Browser scheduling
    // and Playwright polling remain real so assertions cannot hide races.
    const originalTimeout=window.setTimeout.bind(window);window.qaTimers=[];
    window.setTimeout=(callback,delay,...args)=>{
      if(delay===1000||delay===2000){window.qaTimers.push({callback:()=>callback(...args),delay});return 1;}
      return originalTimeout(callback,delay,...args);
    };
    window.qaCalls=[];window.qaReplies=[];
    window.qaFixture={path:'C:\\Library\\Maps\\Example\\A00\\Night.owmap',root:'C:\\Library',output:'D:\\Exports 日本語'};
    window.qaNextJob={id:'new-job',state:'Queued',busy:true,message:'Queued map export',elapsedSeconds:0,completed:0,total:0};
    const api=async(method,...args)=>{
      window.qaCalls.push([method,...structuredClone(args)]);
      if(method==='export_map_status'){
        const reply=window.qaReplies.shift();if(!reply)throw Error('Missing test status reply');
        if(reply.errorReply)throw Error(reply.errorReply);return structuredClone(reply);
      }
      if(method==='export_map_start')return structuredClone(window.qaNextJob);
      if(method==='maps_settings'){
        if(window.qaHoldSettings){window.qaHoldSettings=false;await new Promise(resolve=>window.qaReleaseSettings=resolve);}
        return {...args[0]};
      }
      if(method==='scan_maps')return {root:window.qaFixture.root,maps:[{name:'Night',path:window.qaFixture.path,relativePath:'Maps\\Example\\A00\\Night.owmap'}]};
      if(method==='load_map')return {name:'Example',objects:[{id:'one',name:'Building',bones:0,meshes:[{id:'3',name:'Wall',visible:true,polygons:2}],visible:true}],objectNames:{},warnings:[]};
      throw Error('Unexpected API: '+method);
    };
    const {initMaps}=await import('data:text/javascript;charset=utf-8,'+encodeURIComponent(source));
    window.qaController=initMaps(api,{browserMode:true,mapExport:job,maps:{root:window.qaFixture.root,outputFolder:window.qaFixture.output,bakeTextures:true}});
  },{source,job});
  return page;
}
async function advance(page,delay) {
  equal(await page.evaluate(()=>window.qaTimers[0]?.delay),delay,'Polling/backoff uses the expected wait.');
  await page.evaluate(()=>window.qaTimers.shift().callback());
}
const status=page=>page.locator('#maps-status').innerText();
(async()=>{
  browser=await chromium.launch({headless:true});
  const restored={id:'restored-job',state:'Running',busy:true,message:'Preparing textures',elapsedSeconds:65,completed:5,total:100};
  const page=await createPage(restored);
  equal(await page.evaluate(()=>window.qaController.exporting()),true,'A running job restored from bootstrap keeps the UI locked.');
  equal(await page.locator('#maps-scan').isDisabled(),true);
  equal(await page.locator('#maps-export').isDisabled(),true);
  equal(await page.locator('#maps-export-spinner').isVisible(),true);
  equal(await status(page),'Preparing textures (5 / 100) · 1:05','Restored progress shows counts and elapsed time.');
  await page.evaluate(()=>window.qaReplies.push({errorReply:'Lost status reply'}));await advance(page,1000);
  await page.waitForFunction(()=>document.querySelector('#maps-status').textContent.includes('Checking its status again'));
  match(await status(page),/still pending.*Lost status reply/);
  equal(await page.evaluate(()=>window.qaController.exporting()),true,'A transport error cannot unlock an in-flight native save.');
  equal(await page.locator('#maps-export').isDisabled(),true);
  await page.locator('#maps-export').dispatchEvent('click');
  equal(await page.evaluate(()=>window.qaCalls.filter(call=>call[0]==='export_map_start').length),0,'Even a synthetic duplicate click cannot submit a second job.');
  await page.evaluate(()=>window.qaReplies.push({id:'restored-job',state:'Running',busy:true,message:'Writing objects',elapsedSeconds:92,completed:30,total:250}));
  await advance(page,2000);await advance(page,1000);
  await page.waitForFunction(()=>document.querySelector('#maps-status').textContent.includes('Writing objects'));
  equal(await status(page),'Writing objects (30 / 250) · 1:32');
  equal(await page.evaluate(()=>window.qaCalls.filter(call=>call[0]==='export_map_status').map(call=>call[1])),['restored-job','restored-job'],'Retries track the same background job.');
  await page.evaluate(()=>window.qaReplies.push({id:'restored-job',state:'Completed',busy:false,result:{name:'Night',c4dPath:'D:\\Actual output\\Night.c4d',path:'D:\\Unrelated fallback',warnings:['One unavailable texture was left neutral.']}}));
  await advance(page,1000);await page.waitForFunction(()=>!window.qaController.exporting());
  equal(await status(page),'Saved Night to D:\\Actual output\\Night.c4d. Open the saved .c4d file in Cinema 4D.','Completion reports the actual native file path and how to open it.');
  equal(await page.locator('#maps-scan').isEnabled(),true);equal(await page.locator('#maps-export-spinner').isHidden(),true);
  equal(await page.locator('#maps-notes-panel').isVisible(),true);equal(await page.locator('#maps-notes').textContent(),'One unavailable texture was left neutral.');
  equal(await page.locator('#maps-status').evaluate(element=>element.classList.contains('error')),false,'Successful recovery clears the transient error style.');
  await page.close();

  const failed=await createPage({...restored,id:'failed-job'});
  await failed.evaluate(()=>window.qaReplies.push({id:'failed-job',state:'Failed',busy:false,error:'Cinema 4D rejected the save path'}));
  await advance(failed,1000);await failed.waitForFunction(()=>!window.qaController.exporting());
  equal(await status(failed),'Map export failed: Cinema 4D rejected the save path');
  equal(await failed.locator('#maps-status').evaluate(element=>element.classList.contains('error')),true);
  equal(await failed.locator('#maps-scan').isEnabled(),true);equal(await failed.locator('#maps-export-spinner').isHidden(),true);
  await failed.close();

  const start=await createPage();
  await start.locator('#maps-scan').click();await start.locator('.map-row').click();
  await start.waitForFunction(()=>!document.querySelector('#maps-export').disabled);
  await start.locator('#maps-export-name').fill('Selected Night');
  await start.evaluate(()=>window.qaHoldSettings=true);
  await start.locator('#maps-export').click();await start.waitForFunction(()=>Boolean(window.qaReleaseSettings));
  equal(await start.evaluate(()=>window.qaController.exporting()),true,'Busy begins before asynchronous settings persistence.');
  await start.locator('#maps-export-name').fill('Edited after submission');
  await start.locator('#maps-output').fill('E:\\Later directory');
  await start.locator('#maps-bake-textures').uncheck();
  await start.locator('#maps-export').dispatchEvent('click');
  await start.evaluate(()=>window.qaReleaseSettings());
  await start.waitForFunction(()=>window.qaCalls.some(call=>call[0]==='export_map_start'));
  equal(await start.evaluate(()=>window.qaCalls.filter(call=>call[0]==='export_map_start')),
    [['export_map_start','C:\\Library\\Maps\\Example\\A00\\Night.owmap','Selected Night','C:\\Library','D:\\Exports 日本語',true]],
    'One background job receives the captured scene, name, destination and options.');
  equal(await status(start),'Queued map export · 0:00');
  await start.evaluate(()=>window.qaReplies.push({errorReply:'Temporary native reply failure'}));
  await advance(start,1000);await start.waitForFunction(()=>document.querySelector('#maps-status').textContent.includes('Temporary native reply failure'));
  equal(await start.locator('#maps-export').isDisabled(),true,'A loaded scene stays locked when a status reply is lost.');
  await start.locator('#maps-export').dispatchEvent('click');
  equal(await start.evaluate(()=>window.qaCalls.filter(call=>call[0]==='export_map_start').length),1,'A retry cannot duplicate an active export of a loaded scene.');
  await start.evaluate(()=>window.qaReplies.push({id:'new-job',state:'Failed',busy:false,error:'Export fixture failure'}));
  await advance(start,2000);
  await advance(start,1000);await start.waitForFunction(()=>!window.qaController.exporting());
  equal(await start.locator('#maps-export').isEnabled(),true,'Failure makes the loaded map available for an explicit retry.');
  equal(await status(start),'Map export failed: Export fixture failure');
  equal(await start.evaluate(()=>window.qaCalls.some(call=>call[0]==='export_map')),false,'The renderer uses the background API, not a blocking native call.');
  await start.close();equal(errors,[]);
  console.log(JSON.stringify({passed:true,checks,productionMapsController:true,mockedBackend:true,nativeExportRequested:false,networkRequired:false}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
