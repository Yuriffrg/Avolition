// Real browser + source backend integration for preview-only mesh visibility.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {spawn} = require('node:child_process');

(async () => {
  const scratch = fs.mkdtempSync(path.resolve(__dirname, '../../preview-build/object-ui-'));
  const assets = path.join(scratch, 'Assets');
  fs.cpSync(path.join(__dirname, 'material-assets'), assets, {recursive:true});
  const browser = await chromium.launch({headless:true, args:['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport:{width:1440, height:940}});
  const errors = [];
  let server, serverLog='';
  page.on('pageerror', error => errors.push(error.message));
  try {
    let address=process.env.WHITEHOUND_TEST_URL;
    if(!address) {
      const project=path.resolve(__dirname,'..');
      server=spawn(process.env.WHITEHOUND_TEST_PYTHON || process.env.OWIO_TEST_PYTHON || 'python',
        [path.join(project,'app.py'),'--serve','--port','0','--data-dir',path.join(scratch,'Profile')],
        {cwd:project,windowsHide:true,stdio:['ignore','pipe','pipe'],env:{...process.env,PYTHONDONTWRITEBYTECODE:'1'}});
      address=await new Promise((resolve,reject) => {
        const timer=setTimeout(() => reject(new Error('Server startup timed out: '+serverLog)),15000);
        server.once('error',error => {clearTimeout(timer);reject(error);});
        server.once('exit',code => {clearTimeout(timer);reject(new Error(`Server exited ${code}: ${serverLog}`));});
        server.stderr.on('data',chunk => {serverLog+=chunk;});
        server.stdout.on('data',chunk => {
          serverLog+=chunk;const match=serverLog.match(/http:\/\/127\.0\.0\.1:\d+/);
          if(match){clearTimeout(timer);resolve(match[0]);}
        });
      });
    }
    await page.goto(address);
    await page.waitForFunction(() => !document.querySelector('#scan-root').disabled);
    assert.equal(await page.title(),'Avolition');
    assert.equal(await page.locator('#objects-button, #objects-menu[popover]').count(),0);
    assert.equal(await page.locator('.inspector-panel #objects-menu').isVisible(),true);
    await page.locator('#root-path').fill(assets);
    await page.locator('#scan-root').click();
    await page.waitForFunction(() => !document.querySelector('#load-entity').disabled);
    await page.locator('#load-entity').click();
    await page.waitForFunction(() => document.querySelector('#viewport-loading').hidden && !document.querySelector('#polygon-count').hidden);
    assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 96');
    assert.equal(await page.locator('#objects-menu').isVisible(), true);
    const meshes = page.locator('.object-mesh-row input');
    assert.equal(await meshes.count(), 8);
    assert.equal(await page.locator('#objects-summary').innerText(), '8 of 8 meshes shown');
    await meshes.first().uncheck();
    assert.equal(await page.locator('#objects-summary').innerText(), '7 of 8 meshes shown');
    assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 84 / 96');
    assert.equal(await page.locator('.object-model-row input').first().evaluate(input => input.indeterminate), true);
    await page.locator('#show-wireframe').check();
    await page.locator('#show-skeleton').check();
    await page.locator('#lock-root').check();
    await page.locator('#show-textures').uncheck();
    for (let index=0; index<2; index++) {
      await page.locator('.clip-row').nth(index).click();
      await page.waitForFunction(() => !document.querySelector('#timeline').disabled && document.querySelector('#viewport-loading').hidden);
      await page.locator('#timeline').fill('20');
      assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 84 / 96');
    }
    assert.equal(await meshes.first().isChecked(), false);
    await page.locator('#objects-hide-all').click();
    assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 0 / 96');
    await meshes.first().check();
    assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 12 / 96');
    await page.locator('#objects-show-all').click();
    assert.equal(await page.locator('#polygon-count').innerText(), 'Polygons: 96');
    for (const width of [1440,1060]) {
      await page.setViewportSize({width, height:width === 1060 ? 700 : 940});
      await page.locator('#objects-menu').scrollIntoViewIfNeeded();
      const box = await page.locator('#objects-menu').boundingBox();
      assert.ok(box.x >= 0 && box.x+box.width <= width+1);
      assert.equal(await page.locator('#objects-list').evaluate(list=>list.scrollWidth<=list.clientWidth+1),true);
      await page.screenshot({path:path.join(scratch, `objects-${width}.png`)});
    }
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#objects-menu').isVisible(),true);
    const scene=page.locator('.object-tree-item[data-kind="scene"]');
    await scene.focus();await page.keyboard.press('ArrowLeft');
    assert.equal(await scene.getAttribute('aria-expanded'),'false');
    await page.keyboard.press('ArrowRight');
    assert.equal(await scene.getAttribute('aria-expanded'),'true');
    await page.locator('#objects-hide-all').click();
    await page.keyboard.press('Escape');
    await page.locator('#load-entity').click();
    await page.waitForFunction(() => document.querySelector('#viewport-loading').hidden && document.querySelector('#polygon-count').textContent === 'Polygons: 96');
    assert.equal(await page.locator('#objects-summary').innerText(), '8 of 8 meshes shown');
    assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(), 0);
    assert.deepEqual(errors, []);
    const report = {passed:true, meshes:8, polygons:96, hiddenAcrossClips:true, resetOnReload:true, compactLayout:true, errors, scratch};
    fs.writeFileSync(path.join(scratch, 'report.json'), JSON.stringify(report,null,2));
    console.log(JSON.stringify(report));
  } finally {
    await browser.close();
    if(server && server.exitCode===null) server.kill();
  }
})().catch(error => {console.error(error);process.exit(1);});
