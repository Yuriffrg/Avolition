const {openAnimationSetup} = require('./open-animation-setup.cjs');
// Real binary fixtures and browser: choosing an attached rig breaks equal
// joint-match ties by its assigned parent's duration without changing sort modes.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {spawn, spawnSync} = require('node:child_process');

(async () => {
  const project = path.resolve(__dirname, '..');
  const scratch = fs.mkdtempSync(path.resolve(project, '../preview-build/duration-suggestions-'));
  const assets = path.join(scratch, 'Assets');
  const python = process.env.WHITEHOUND_TEST_PYTHON || 'python';
  const fixture = `import sys
from pathlib import Path
sys.path[:0] = [str(Path(sys.argv[1]) / 'tests'), sys.argv[1]]
from test_formats import model_bytes, entity_bytes, clip_bytes
root = Path(sys.argv[2])
def write(name, content):
    p = root / name
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_bytes(content)
write('Arms.owmdl', model_bytes(name='Arms'))
write('Weapon.owmdl', model_bytes(name='Weapon'))
write('Gameplay1P.owentity', entity_bytes(name='Gameplay1P', model='Arms.owmdl', children=[('Weapon.owentity', 'bone_0002')], relative='.'))
write('Weapon.owentity', entity_bytes(name='Weapon', model='Weapon.owmdl', relative='.'))
for name, duration, fps in [('Arms', 16, 30), ('A long weapon', 61, 60), ('Z matching weapon', 31, 60)]:
    write('Animations/' + name + '.owanimclip', clip_bytes(duration=duration, fps=fps, bones=[('bone_0001', [(0, [(0, (0,0,0)), (duration-1, (1,0,0))])])]))
`;
  const generated = spawnSync(python, ['-X', 'utf8', '-c', fixture, project, assets], {encoding:'utf8',windowsHide:true});
  assert.equal(generated.status, 0, generated.stderr || generated.stdout);
  let server, serverLog = '', browser;
  const errors = [];
  try {
    server = spawn(python, [path.join(project,'app.py'),'--serve','--port','0','--data-dir',path.join(scratch,'Profile')],
      {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
    const address = await new Promise((resolve,reject) => {
      const timer = setTimeout(()=>reject(new Error('Server startup timed out: '+serverLog)),15000);
      server.once('error',e=>{clearTimeout(timer);reject(e);});
      server.once('exit',code=>{clearTimeout(timer);reject(new Error(`Server exited ${code}: ${serverLog}`));});
      server.stderr.on('data',chunk=>{serverLog+=chunk;});
      server.stdout.on('data',chunk=>{serverLog+=chunk;const match=serverLog.match(/http:\/\/127\.0\.0\.1:\d+/);if(match){clearTimeout(timer);resolve(match[0]);}});
    });
    browser = await chromium.launch({headless:true,args:['--enable-unsafe-swiftshader']});
    const page = await browser.newPage({viewport:{width:1440,height:940}});
    page.on('pageerror',e=>errors.push(e.message));
    await page.goto(address);
    await page.waitForFunction(()=>!document.querySelector('#scan-root').disabled);
    await page.locator('#root-path').fill(assets);
    await page.locator('#scan-root').click();
    await page.waitForFunction(()=>document.querySelector('#clip-count').textContent==='3');
    await page.locator('#entity-select').selectOption(path.join(assets,'Gameplay1P.owentity'));
    await page.locator('#load-entity').click();
    await page.waitForFunction(()=>document.querySelector('#viewport-loading').hidden && !document.querySelector('#rig-select').disabled
      && document.querySelectorAll('#rig-select option').length===2 && !document.querySelector('#library-match-status').textContent.includes('Comparing'));
    const names = ()=>page.locator('#clip-list .clip-name').allTextContents();
    const before = await names();
    assert.ok(before.indexOf('A long weapon') < before.indexOf('Z matching weapon'));
    await page.locator('.clip-row').filter({hasText:'Arms'}).click();
    await page.waitForFunction(()=>document.querySelectorAll('#animation-set .animation-set-row').length===1 && document.querySelector('#viewport-loading').hidden);
    await page.locator('#objects-menu > summary').click();
    await page.locator('#objects-list [data-kind="model"][data-model-id="model-1"] > .object-tree-row .object-tree-name').click();
    assert.equal(await page.locator('#rig-select').inputValue(),'model-1');
    await page.locator('#library-filters > summary').click();
    const prioritized = await names();
    assert.ok(prioritized.indexOf('Z matching weapon') < prioritized.indexOf('A long weapon'));
    assert.match(await page.locator('#library-match-status').innerText(),/Equal bone matches favor 0\.50 s clips/);
    assert.match(await page.locator('.clip-row').filter({hasText:'Z matching weapon'}).getAttribute('title'),/Duration matches Arms/);
    await page.locator('#clip-sort').selectOption('name');
    const alphabetical = await names();
    assert.ok(alphabetical.indexOf('A long weapon') < alphabetical.indexOf('Z matching weapon'));
    assert.doesNotMatch(await page.locator('#library-match-status').innerText(),/Equal bone matches/);
    await page.locator('#clip-sort').selectOption('object-match');
    await openAnimationSetup(page);
    await page.locator('#rig-select').selectOption('model-0');
    const ownAssignment = await names();
    assert.ok(ownAssignment.indexOf('A long weapon') < ownAssignment.indexOf('Z matching weapon'));
    assert.doesNotMatch(await page.locator('#library-match-status').innerText(),/Equal bone matches/);
    await openAnimationSetup(page);
    await page.locator('#clear-object-animation').click();
    await page.locator('#rig-select').selectOption('model-1');
    assert.deepEqual(await names(), before);
    assert.deepEqual(errors, []);
    const report = {passed:true,parentDurationSeconds:0.5,weaponFPS:60,clickedObjectTree:true,explicitSortPreserved:true,ownAssignmentExcluded:true,clearRestoresOrder:true,errors,scratch};
    fs.writeFileSync(path.join(scratch,'report.json'),JSON.stringify(report,null,2));
    console.log(JSON.stringify(report));
  } finally {
    await browser?.close();
    if (server && server.exitCode===null) server.kill();
  }
})().catch(error=>{console.error(error);process.exitCode=1;});
