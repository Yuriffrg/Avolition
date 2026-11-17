const {openAnimationSetup} = require('./open-animation-setup.cjs');
// Real packaged Electron + backend IPC, hidden QA window. Only native dialogs
// and shell.openPath are intercepted; no release or DataTool results are faked.
const {_electron} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const build = path.resolve(__dirname, '../../preview-build');
const executable = path.resolve(process.env.WHITEHOUND_EXE || path.join(build, 'avolition-v0811/dist/Avolition/Avolition.exe'));
const scratch = fs.mkdtempSync(path.join(build, 'toolchain-desktop-'));
const profile = path.join(scratch, 'Profile');
const assets = path.join(scratch, 'Exports');
const customOutput = path.join(scratch, 'Output with spaces 日本語');
const invalidGame = path.join(scratch, 'Not an Overwatch install 游戏');
const library = path.join(path.dirname(executable), 'library');
fs.mkdirSync(customOutput, {recursive:true});
fs.mkdirSync(invalidGame, {recursive:true});
fs.cpSync(path.join(__dirname, 'demo-assets'), assets, {recursive:true});
let application, page;
const errors = [];
const report = {passed:false, executable, scratch, hidden:true, qaOnlyNoSandbox:true, realExtraction:false, mockedDataTool:false};

async function launch() {
  application = await _electron.launch({executablePath:executable,args:['--no-sandbox','--test-hidden','--data-dir',profile],timeout:45000});
  page = await application.firstWindow({timeout:45000});
  page.setDefaultTimeout(25000);
  page.on('pageerror',error=>errors.push(error.message));
  await application.evaluate(({BrowserWindow,dialog,shell})=>{
    const window=BrowserWindow.getAllWindows()[0];window.hide();window.setSkipTaskbar(true);window.webContents.setBackgroundThrottling(false);
    globalThis.__picks=[];globalThis.__pickerCalls=[];globalThis.__opened=[];globalThis.__closeMessages=[];
    dialog.showOpenDialog=async(...args)=>{globalThis.__pickerCalls.push(args.at(-1));const chosen=globalThis.__picks.shift();return{canceled:!chosen,filePaths:chosen?[chosen]:[]};};
    dialog.showMessageBox=async(...args)=>{globalThis.__closeMessages.push(args.at(-1));return{response:0,checkboxChecked:false};};
    shell.openPath=async target=>{globalThis.__opened.push(target);return '';};
  });
  await page.waitForFunction(()=>window.pywebview?.api&&!document.querySelector('#scan-root').disabled&&/^(Ready\.|Library restored)/.test(document.querySelector('#status-text').textContent));
  await page.waitForFunction(()=>document.querySelector('#toolchain-category').options.length===4);
  const native=await application.evaluate(({BrowserWindow})=>{const window=BrowserWindow.getAllWindows()[0];const prefs=window.webContents.getLastWebPreferences();return{visible:window.isVisible(),url:window.webContents.getURL(),sandbox:prefs.sandbox,contextIsolation:prefs.contextIsolation,nodeIntegration:prefs.nodeIntegration};});
  assert.deepEqual(native,{visible:false,url:'whitehound://app/index.html',sandbox:true,contextIsolation:true,nodeIntegration:false});
  assert.equal(await page.locator('#browse-overwatch-install').isEnabled(),true);
  assert.equal(await page.locator('#browse-toolchain-output').isEnabled(),true);
  report.nativeWindow=native;
}

async function pick(selector,value) {
  await application.evaluate((_electron,value)=>{globalThis.__picks.push(value);},value);
  await page.locator(selector).dispatchEvent('click');
}

async function close() {
  const closed=application.waitForEvent('close',{timeout:25000});
  await application.evaluate(({BrowserWindow})=>{BrowserWindow.getAllWindows()[0].close();});
  await closed;application=null;page=null;
}

(async()=>{
  await launch();
  assert.equal(await page.locator('#toolchain-output').inputValue(),library,'Fresh default is the actual executable sibling library, not CWD or profile.');
  assert.equal(await page.locator('#root-path').inputValue(),library,'The preview reuses the same established library resolver.');
  assert.equal(await page.locator('#workflow-animations').isVisible(),true);
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false);
  const initial=await page.evaluate(async()=>{const result=await window.pywebview.api.toolchain_status();if(!result.ok)throw Error(result.error);return result.data;});
  assert.equal(initial.runtime.installed,null);
  assert.equal(initial.runtime.latest,null,'Opening Toolchain performs no automatic network check.');
  assert.equal(initial.runtime.root,path.join(path.dirname(executable),'toolchain'));
  assert.equal(initial.job.state,'Ready');
  report.portableDefaults={library,toolchain:initial.runtime.root};
  await page.locator('#tab-toolchain').dispatchEvent('click');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true);
  await pick('#browse-overwatch-install',invalidGame);
  await page.waitForFunction(value=>document.querySelector('#overwatch-install').value===value,invalidGame);
  await page.waitForFunction(()=>!document.querySelector('#overwatch-install-error').hidden);
  assert.match(await page.locator('#overwatch-install-error').textContent(),/\.build\.info|install root/i);
  await pick('#browse-toolchain-output',customOutput);
  await page.waitForFunction(value=>document.querySelector('#toolchain-output').value===value,customOutput);
  const picks=await application.evaluate(()=>globalThis.__pickerCalls);
  assert.deepEqual(picks[0].properties,['openDirectory'],'Game picker cannot create a game folder.');
  assert.deepEqual(picks[1].properties,['openDirectory','createDirectory']);
  assert.equal(picks[1].defaultPath,library);
  await page.locator('#tab-toolchain').dispatchEvent('click');
  assert.equal(await page.locator('#toolchain-state').textContent(),'Not installed');
  assert.equal(await page.locator('#toolchain-install').isEnabled(),true);
  assert.equal(await page.locator('#toolchain-export').isDisabled(),true);
  assert.equal(await page.locator('#toolchain-open-output').count(),0,'The redundant Toolchain output-folder shortcut is removed.');
  assert.equal(await page.locator('#browse-toolchain-output').isEnabled(),true,'The output-directory picker remains available.');
  assert.equal(await page.locator('#use-library-directory').isEnabled(),true,'Use Library remains available.');
  assert.equal(await page.locator('#maps-open-output,#open-export-output').count(),2);
  assert.deepEqual(await application.evaluate(()=>globalThis.__opened),[],'Changing Toolchain directories does not open a folder implicitly.');
  await page.locator('#tab-animations').dispatchEvent('click');
  assert.equal(await page.locator('#export-c4d, #export-clip, #clip-name').count(),0);
  await pick('#browse-root',assets);
  await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='3');
  await page.evaluate(async()=>{
    const {PreviewViewport}=await import('./viewer.js');const original=PreviewViewport.prototype.loadAsset;
    PreviewViewport.prototype.loadAsset=function(...args){window.qaViewport=this;window.qaLoads=(window.qaLoads||0)+1;return original.apply(this,args);};
  });
  assert.equal(await page.locator('#browse-entity').count(),0);
  await page.locator('#entity-select').selectOption(path.join(assets,'Entities/PreviewRobot/PreviewRobot.owentity'));
  await page.locator('#load-entity').dispatchEvent('click');
  await page.waitForFunction(()=>window.qaViewport?.models.length>0&&document.querySelector('#viewport-loading').hidden);
  await page.locator('.clip-row').filter({hasText:'001_Wave'}).dispatchEvent('click');
  await page.waitForFunction(()=>!document.querySelector('#timeline').disabled&&document.querySelector('#viewport-loading').hidden);
  await page.locator('#timeline').fill('17');
  await openAnimationSetup(page);
  await page.locator('#animation-set-name').fill('PreviewRobot_native_tabs');
  await page.locator('#clip-search').fill('001_Wave');
  const before=await page.evaluate(()=>{
    window.qaBefore={canvas:document.querySelector('#viewport'),viewport:window.qaViewport,model:window.qaViewport.models[0],loads:window.qaLoads};
    return{rig:document.querySelector('#rig-select').value,clip:document.querySelector('#playing-title').textContent,frame:document.querySelector('#timeline').value};
  });
  await page.locator('#tab-toolchain').dispatchEvent('click');
  await page.locator('#tab-animations').dispatchEvent('click');
  assert.equal(await page.evaluate(()=>window.qaBefore.canvas===document.querySelector('#viewport')&&window.qaBefore.viewport===window.qaViewport&&window.qaBefore.model===window.qaViewport.models[0]&&window.qaBefore.loads===window.qaLoads),true);
  assert.equal(await page.locator('#rig-select').inputValue(),before.rig);
  assert.equal(await page.locator('#playing-title').textContent(),before.clip);
  assert.equal(await page.locator('#timeline').inputValue(),before.frame);
  assert.equal(await page.locator('#animation-set-name').inputValue(),'PreviewRobot_native_tabs','Tab switches preserve the unsaved setup-name draft.');
  assert.equal(await page.locator('#clip-search').inputValue(),'001_Wave');
  assert.deepEqual(await page.evaluate(async()=>(await window.pywebview.api.bootstrap()).data.exportQueue),[],
    'Typing a name and switching tabs do not implicitly add or export a setup.');
  await page.locator('#export-animation-set').dispatchEvent('click');
  await page.waitForFunction(()=>!document.querySelector('#export-animation-set').disabled
    &&document.querySelector('#animation-set-export-status').textContent.startsWith('Added to export list'));
  const queued=await page.evaluate(async()=>(await window.pywebview.api.bootstrap()).data.exportQueue);
  assert.equal(queued.length,1);assert.equal(queued[0].name,'PreviewRobot_native_tabs');
  assert.equal(queued[0].entityPath,path.join(assets,'Entities/PreviewRobot/PreviewRobot.owentity'));
  await page.locator('#clip-search').fill('native_tabs');
  assert.equal(await page.locator('.clip-row').count(),1,'Explicitly adding a named setup keeps nickname search useful.');
  assert.equal(await page.locator('#timeline').inputValue(),before.frame);
  assert.equal(await page.locator('#toolchain-directories').isVisible(),false,'Preview tab hides Toolchain paths without clearing them.');
  await page.screenshot({path:path.join(scratch,'native-animations.png')});
  await page.locator('#tab-toolchain').dispatchEvent('click');
  const layout=await page.evaluate(()=>{
    const groups=[...document.querySelectorAll('.workflow-directory')].map(el=>el.getBoundingClientRect());const tabs=document.querySelector('.workflow-tabs').getBoundingClientRect(),badge=document.querySelector('.local-badge').getBoundingClientRect(),row=document.querySelector('.workflow-directories').getBoundingClientRect();
    return{widths:groups.map(r=>r.width),aligned:Math.abs(tabs.y+tabs.height/2-badge.y-badge.height/2)<1,adjacent:Math.abs(tabs.bottom-row.y)<2};
  });
  assert.equal(layout.widths[0],layout.widths[1]);assert.equal(layout.aligned,true);assert.equal(layout.adjacent,true);
  report.layout=layout;report.previewPreserved=true;
  await page.screenshot({path:path.join(scratch,'native-toolchain.png')});
  await close();
  await launch();
  assert.equal(await page.locator('#toolchain-output').inputValue(),customOutput,'Custom output survives a real native restart.');
  assert.equal(await page.locator('#overwatch-install').inputValue(),invalidGame,'Unicode game selection survives restart.');
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true,'Active tab survives a real native restart.');
  assert.equal(await page.locator('#toolchain-directories').isVisible(),true,'Toolchain directory visibility follows the restored tab.');
  assert.deepEqual(await page.evaluate(async()=>(await window.pywebview.api.bootstrap()).data.exportQueue),queued,
    'Toolchain restarts preserve independently queued animation setups.');
  await page.locator('#use-library-directory').dispatchEvent('click');
  await page.waitForFunction(value=>document.querySelector('#toolchain-output').value===value,library);
  await close();
  await launch();
  assert.equal(await page.locator('#toolchain-output').inputValue(),library,'Use Library restores the portable default across restart.');
  assert.equal(await page.locator('#workflow-toolchain').isVisible(),true);
  await close();
  const catalog=JSON.parse(fs.readFileSync(path.join(profile,'catalog.json'),'utf8'));
  assert.equal(catalog.workflow.outputDirectory,'','The default remains a resolver marker, not a fixed machine path.');
  assert.equal(catalog.workflow.activeTab,'toolchain');
  assert.deepEqual(errors,[]);
  report.persisted={customDirectory:true,unicodePath:true,activeTab:true,useLibrary:true,animationQueue:true};
  report.setupDraftPreserved=true;report.noImplicitExport=true;report.passed=true;
  console.log(JSON.stringify(report));
})().catch(error=>{report.error=String(error.stack||error);console.error(error);process.exitCode=1;}).finally(async()=>{
  fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
  await application?.close();
});
