// Production renderer, local HTML/CSS, mocked native/release/process responses.
// No Overwatch installation or external network is used.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../web');
let browser;
(async () => {
  browser = await chromium.launch({headless:true});
  const context = await browser.newContext({viewport:{width:1440,height:940}});
  await context.route('http://avolition.test/**', route => {
    const filename = new URL(route.request().url()).pathname.slice(1) || 'index.html';
    if (!['index.html','styles.css','toolchain.js'].includes(filename)) return route.fulfill({status:404,body:''});
    let content = fs.readFileSync(path.join(root,filename),'utf8');
    if (filename === 'index.html') content = content.replace(/<script\b[^>]*>[\s\S]*?<\/script>/g,'');
    return route.fulfill({body:content,contentType:filename.endsWith('.js')?'text/javascript':filename.endsWith('.css')?'text/css':'text/html'});
  });
  const page = await context.newPage(), errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto('http://avolition.test/');
  await page.evaluate(async () => {
    const {initToolchain} = await import('./toolchain.js');
    window.calls=[]; window.refreshed=[];
    window.settings={activeTab:'toolchain',toolchainTab:'maps',overwatchInstall:'D:\\Overwatch 游戏',outputDirectory:'E:\\Library',libraryDirectory:'E:\\Library'};
    window.snapshot={runtime:{installed:{version:'fixture'},executable:'C:\\Runtime\\DataTool.exe'},job:{state:'Ready',busy:false,logs:[],
      catalog:{install:window.settings.overwatchInstall,heroes:[{id:'h',name:'Lúcio'}],unlocks:{'Lúcio':[{id:'s',name:'Classic',category:'skin'}]}},
      mapCatalog:{install:window.settings.overwatchInstall,maps:[{id:'000000000001.09F',name:'King’s Row 日本語',query:'King’s Row 日本語:D86',mapGuid:'000000000D86.002',mapType:'PVP',gameModes:['Hybrid'],description:'Map fixture'}]}},
      capabilities:{categories:[{id:'skin',label:'Models + gameplay animations',description:'Skins'}]}};
    window.backend=async (method,...args)=>{
      window.calls.push([method,...structuredClone(args)]);
      if(method==='workflow_settings'){Object.assign(window.settings,args[0]);return {...window.settings};}
      if(method==='toolchain_status')return structuredClone(window.snapshot);
      if(method==='toolchain_validate'){
        if(window.holdValidation){window.holdValidation=false;await new Promise(resolve=>window.releaseValidation=resolve);}
        return {ok:true,errors:{}};
      }
      if(method==='toolchain_list')window.snapshot.job={...window.snapshot.job,state:'Loading',busy:true,kind:'catalog',jobId:'maps-list'};
      if(method==='toolchain_start')window.snapshot.job={...window.snapshot.job,state:'Exporting',busy:true,kind:'export',jobId:'maps-export',logs:[{seq:1,text:'Map export started'}]};
      if(method==='toolchain_cancel')window.snapshot.job={...window.snapshot.job,state:'Cancelled',busy:false};
      return {};
    };
    window.controller=initToolchain(window.backend,{refreshLibrary:folder=>window.refreshed.push(folder)},{workflow:window.settings});
  });
  await page.waitForFunction(()=>document.querySelector('#toolchain-map').options.length===3);
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true);
  assert.equal(await page.locator('#toolchain-maps-panel').isVisible(),true,'Stored inner Maps tab is restored.');
  assert.equal(await page.locator('#toolchain-animations-panel').isVisible(),false);
  await page.locator('#toolchain-tab-animations').click();
  await page.locator('#toolchain-hero').selectOption('Lúcio');
  await page.locator('#toolchain-asset').selectOption('Classic');
  await page.locator('#toolchain-option-includeAnimations').uncheck();
  await page.locator('#toolchain-tab-animations').focus(); await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#toolchain-tab-maps').getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('#tab-toolchain').getAttribute('aria-selected'),'true','Inner arrow navigation leaves the global workflow unchanged.');
  await page.waitForFunction(()=>window.settings.toolchainTab==='maps');
  await page.locator('#toolchain-load-maps').click();
  await page.waitForFunction(()=>window.calls.some(call=>call[0]==='toolchain_list'));
  assert.deepEqual(await page.evaluate(()=>window.calls.find(call=>call[0]==='toolchain_list')),['toolchain_list',{workflow:'maps'}]);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  await page.locator('#toolchain-cancel').click();
  await page.waitForFunction(()=>document.querySelector('#toolchain-job-state').textContent==='Cancelled');
  await page.locator('#toolchain-map').selectOption('000000000001.09F');
  assert.match(await page.locator('#toolchain-map-description').innerText(),/PVP.*Hybrid.*Map fixture/);
  await page.locator('#toolchain-map-option-includeTextures').uncheck();
  await page.locator('#toolchain-map-option-allLods').check();
  await page.evaluate(()=>window.holdValidation=true);
  await page.locator('#toolchain-export').click();
  await page.waitForFunction(()=>Boolean(window.releaseValidation));
  await page.locator('#toolchain-tab-animations').click();
  assert.equal(await page.locator('#toolchain-hero').inputValue(),'Lúcio');
  assert.equal(await page.locator('#toolchain-asset').inputValue(),'Classic');
  assert.equal(await page.locator('#toolchain-option-includeAnimations').isChecked(),false);
  assert.equal(await page.locator('#toolchain-option-includeTextures').isChecked(),true,'Map options do not mutate hero options.');
  await page.evaluate(()=>window.releaseValidation());
  await page.waitForFunction(()=>window.calls.some(call=>call[0]==='toolchain_start'));
  assert.deepEqual(await page.evaluate(()=>window.calls.find(call=>call[0]==='toolchain_start')[1]),
    {workflow:'maps',mapId:'000000000001.09F',includeAnimations:false,includeTextures:false,includeSound:false,allLods:true},
    'The submitted map selection is captured before asynchronous validation and tab switching.');
  await page.waitForFunction(()=>document.querySelector('#toolchain-job-state').textContent==='Exporting');
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  await page.locator('#tab-animations').click(); await page.locator('#tab-toolchain').click();
  assert.equal(await page.locator('#toolchain-job-state').innerText(),'Exporting');
  await page.locator('#toolchain-tab-maps').click();
  assert.equal(await page.locator('#toolchain-map').inputValue(),'000000000001.09F');
  await page.evaluate(async()=>{
    window.snapshot.job={...window.snapshot.job,state:'Completed',busy:false,result:{workflow:'maps',outputDirectory:'E:\\Library'}};
    await window.controller.refresh();
  });
  await page.waitForFunction(()=>window.refreshed.length===1);
  assert.deepEqual(await page.evaluate(()=>window.refreshed),['E:\\Library']);
  await page.evaluate(async()=>{
    window.snapshot.job={...window.snapshot.job,state:'Failed',error:'Unsupported map fixture',result:null,logs:[{seq:2,stream:'stderr',text:'Map failed'}]};
    await window.controller.refresh();
  });
  assert.equal(await page.locator('#toolchain-log-panel').getAttribute('open'),'');
  assert.match(await page.locator('#toolchain-log').innerText(),/\[stderr\] Map failed/);
  await page.locator('#overwatch-install').fill('F:\\Different game'); await page.locator('#overwatch-install').blur();
  await page.waitForFunction(()=>document.querySelector('#toolchain-map').disabled);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true,'Map catalogs from another installation are not reusable.');
  await page.evaluate(()=>window.controller.stop());
  assert.deepEqual(errors,[]);
  console.log(JSON.stringify({passed:true,mockedApis:true,realExtraction:false,persistedInnerTabs:true,independentChoices:true,capturedMapSelection:true,sharedJobLifecycle:true}));
})().catch(error=>{console.error(error);process.exitCode=1;}).finally(async()=>{await browser?.close();});
