// HISTORICAL HARNESS: v0.8.9-and-earlier nickname-triggered export workflow.
// Intentionally retained for old builds; current UI removed these controls.
// Superseded by test_export_queue_ui.cjs and test_desktop.cjs; see README.md.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const path = require('node:path');
const fs = require('node:fs');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({headless: true, args: ['--enable-unsafe-swiftshader']});
  const page = await browser.newPage({viewport: {width: 1440, height: 940}, deviceScaleFactor: 1});
  const errors = [];
  const exportRequests = [];
  page.on('request', request => {
    if (request.url().endsWith('/api/export_animation')) exportRequests.push(request.postDataJSON());
  });
  const testRoot = fs.mkdtempSync(path.resolve(__dirname, '../../preview-build/export-browser-'));
  fs.cpSync(path.resolve(__dirname, 'demo-assets'), testRoot, {recursive:true});
  const outputFolder = path.join(testRoot, 'Chosen animations');
  const legacyNotes = 'Legacy notes are preserved without a Notes editor.';
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(process.env.WHITEHOUND_TEST_URL || 'http://127.0.0.1:8765');
  await page.locator('#scan-root').waitFor({state: 'visible'});
  await page.waitForFunction(() => !document.querySelector('#scan-root').disabled);
  assert.equal(await page.locator('#browse-root').isDisabled(), true);
  assert.equal(await page.title(), 'Whitehound');
  assert.equal(await page.locator('#clip-notes').count(), 0);
  assert.equal(await page.locator('#status-extra').count(), 0);
  assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(), 0);
  // Upgrade an older profile that had FBX enabled. It must not remain a hidden
  // active export format, and unrelated C4D/clip settings must be retained.
  await page.evaluate(async () => {
    const saved = (await window.pywebview.api.bootstrap()).data.fbxOptions;
    await window.pywebview.api.set_export_settings({...saved, enabled:true, upAxis:'Z',
      clipEnabled:false, c4dEnabled:true, includeMesh:false, bakeTextures:false, rootMotion:false});
  });
  await page.reload();
  await page.waitForFunction(async () => (await window.pywebview.api.bootstrap()).data.fbxOptions.enabled === false);
  assert.equal(await page.locator('#export-fbx, #fbx-up-axis').count(), 0);
  assert.equal(await page.locator('#export-c4d').isChecked(), true);
  for (const selector of ['#export-clip', '#fbx-mesh', '#fbx-textures', '#fbx-root-motion'])
    assert.equal(await page.locator(selector).isChecked(), false);
  await page.locator('#fbx-options-button').click();
  for (const selector of ['#fbx-mesh', '#fbx-textures', '#fbx-root-motion']) await page.locator(selector).check();
  await page.getByRole('button', {name:'Close export options'}).click();
  await page.locator('#export-c4d').uncheck();
  await page.locator('#export-clip').check();
  for (const selector of ['#show-textures', '#show-grid']) assert.equal(await page.locator(selector).isChecked(), true);
  for (const selector of ['#show-skeleton', '#lock-root', '#show-wireframe']) assert.equal(await page.locator(selector).isChecked(), false);
  await page.locator('#root-path').fill(testRoot);
  await page.evaluate(async legacyNotes => {
    const scan = await window.pywebview.api.scan_root(document.querySelector('#root-path').value);
    for (const clip of scan.data.clips) await window.pywebview.api.save_annotation(clip.path,
      {name:'', notes:clip.name.includes('001_Wave') ? legacyNotes : '', favorite:true});
  }, legacyNotes);
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '3');
  assert.equal(await page.locator('#favorites-filter, #favorite-clip, .clip-favorite, #i-star').count(), 0);
  assert.equal(await page.locator('.clip-row').count(), 3);
  assert.equal(await page.locator('#output-path').inputValue(), path.join(testRoot, 'Renamed Animations'));
  await page.locator('#output-path').fill(outputFolder);
  await page.locator('#output-path').press('Enter');
  await page.locator('#load-entity').click();
  await page.waitForFunction(() => document.querySelector('#viewport-empty').hidden);
  assert.match(await page.locator('#mesh-stats').innerText(), /3 bones/);
  await page.locator('.clip-row').filter({hasText: '001_Wave'}).click();
  await page.waitForFunction(() => !document.querySelector('#timeline').disabled);
  await page.locator('#play-pause').click();
  await page.locator('#timeline').fill('30');
  assert.equal(await page.locator('#frame-input').inputValue(), '30');
  await page.locator('#viewport-settings > summary').click();
  await page.locator('#lock-root').check();
  await page.locator('#clip-name').fill('Friendly wave');
  // Pausing during typing saves metadata but must not create partial filenames.
  await page.waitForTimeout(800);
  assert.equal(fs.existsSync(outputFolder), false);
  await page.locator('#clip-name').press('Tab');
  await page.waitForFunction(() => document.querySelector('#export-status').textContent.startsWith('Saved:'));
  const copied = path.join(outputFolder, 'Preview Robot (synthetic)_Friendly wave.owanimclip');
  assert.deepEqual(fs.readFileSync(copied), fs.readFileSync(path.join(testRoot, 'Animations/001_Wave.owanimclip')));
  assert.equal(exportRequests.at(-1)[3].enabled, false);
  assert.equal(exportRequests.at(-1)[3].rootMotion, true, 'Preview In-place must not change export root motion');
  assert.equal(Object.hasOwn(exportRequests.at(-1)[3], 'lockRoot'), false);
  await page.locator('#lock-root').uncheck();
  assert.equal(await page.locator('#fbx-root-motion').isChecked(), true);
  await page.locator('#clip-name').fill('bad/name');
  await page.locator('#clip-name').press('Enter');
  await page.waitForFunction(() => document.querySelector('#export-status').textContent.startsWith('Export not saved'));
  await page.locator('#clip-name').fill('Friendly wave');
  await page.locator('#clip-name').press('Enter');
  await page.waitForFunction(() => document.querySelector('#export-status').textContent.startsWith('Already saved:'));
  assert.deepEqual(fs.readdirSync(outputFolder), ['Preview Robot (synthetic)_Friendly wave.owanimclip']);
  await page.locator('#clip-search').fill('Friendly wave');
  assert.equal(await page.locator('.clip-row').count(), 1);
  await page.locator('#clip-search').fill('');
  await page.locator('#library-filters > summary').click();
  await page.locator('#clip-sort').selectOption('name');
  assert.equal(await page.locator('.clip-name').first().innerText(), '002_Turn');
  // Named clips stay together, alphabetized by nickname. Empty and whitespace
  // names stay below them with deterministic source-filename ordering.
  await page.locator('#export-clip').uncheck();
  await page.locator('#clip-sort').selectOption('nicknamed-first');
  assert.deepEqual(await page.locator('.clip-name').allTextContents(), ['Friendly wave', '002_Turn', '003_Root_Move']);
  await page.locator('.clip-row').filter({hasText: '002_Turn'}).click();
  await page.locator('#clip-name').fill('Zigzag');
  assert.deepEqual(await page.locator('.clip-name').allTextContents(), ['Friendly wave', 'Zigzag', '003_Root_Move']);
  await page.locator('#clip-name').fill('A turn');
  assert.deepEqual(await page.locator('.clip-name').allTextContents(), ['A turn', 'Friendly wave', '003_Root_Move']);
  await page.locator('#clip-name').fill('   ');
  assert.ok((await page.locator('.clip-row').nth(1).getAttribute('title')).includes('002_Turn'));
  await page.locator('#clip-name').fill('');
  assert.deepEqual(await page.locator('.clip-name').allTextContents(), ['Friendly wave', '002_Turn', '003_Root_Move']);
  await page.locator('#clip-search').fill('Friendly');
  assert.equal(await page.locator('.clip-row').count(), 1);
  await page.locator('#clip-search').fill('');
  await page.locator('#clip-sort').selectOption('tracks-desc');
  for (let i = 0; i < 3; i++) {
    for (const name of ['002_Turn', '003_Root_Move', '001_Wave']) {
      await page.locator('.clip-row').filter({hasText: name}).click();
      await page.waitForFunction(() => !document.querySelector('#timeline').disabled && document.querySelector('#viewport-loading').hidden);
    }
  }
  await page.locator('#play-pause').click();
  await page.locator('#timeline').fill('30');
  await page.locator('#show-skeleton').check();
  await page.locator('#show-wireframe').check();
  await page.locator('#show-wireframe').uncheck();
  await page.locator('#show-wireframe').check();
  const canvas = await page.locator('#viewport').boundingBox();
  await page.keyboard.down('1');
  await page.mouse.move(canvas.x+canvas.width/2, canvas.y+canvas.height/2);
  await page.mouse.down();
  await page.mouse.move(canvas.x+canvas.width/2+30, canvas.y+canvas.height/2+15, {steps: 5});
  await page.mouse.up();
  await page.keyboard.up('1');
  await page.locator('#frame-model').click();
  await page.waitForTimeout(500);
  const screenshots = path.resolve(__dirname, '../../preview-build');
  await page.screenshot({path: path.join(screenshots, 'browser-preview.png')});
  // Use the same renderer module for an independent skin deformation check.
  const deformation = await page.evaluate(async () => {
    const {PreviewViewport} = await import('./viewer.js');
    const THREE = await import('./vendor/three.module.js');
    const folder = document.querySelector('#root-path').value;
    const catalog = (await window.pywebview.api.scan_root(folder)).data;
    const asset = (await window.pywebview.api.load_entity(catalog.entities.find(e => e.kind === 'entity').path)).data;
    const clip = (await window.pywebview.api.load_animation(catalog.clips.find(c => c.name.includes('001_Wave')).path)).data;
    const holder = document.createElement('div'); holder.style.cssText = 'width:300px;height:300px;position:fixed;left:400px;top:200px;z-index:99;opacity:0';
    const canvas = document.createElement('canvas'); canvas.id='test-nav-canvas'; holder.append(canvas); document.body.append(holder);
    const view = new PreviewViewport(canvas); view.loadAsset(asset);
    const mesh = view.models[0].meshes.find(item => item.name === 'Right arm');
    const rest = []; let error = 0;
    for (let i=0; i<mesh.geometry.attributes.position.count; i++) {
      const p = new THREE.Vector3().fromBufferAttribute(mesh.geometry.attributes.position, i);
      rest.push(p.clone()); error = Math.max(error, mesh.applyBoneTransform(i,p).distanceTo(rest[i]));
    }
    view.setClip(clip); view.setFrame(30);
    let distance = 0;
    for (let i=0; i<rest.length; i++) distance = Math.max(distance, mesh.applyBoneTransform(i,rest[i].clone()).distanceTo(rest[i]));
    view.setClip(null);
    let resetError = 0;
    for (let i=0; i<rest.length; i++) resetError = Math.max(resetError, mesh.applyBoneTransform(i,rest[i].clone()).distanceTo(rest[i]));
    window.__motionTestView = view;
    return {restError:error,animatedDistance:distance,resetError};
  });
  assert.ok(deformation.restError < 1e-5, JSON.stringify(deformation));
  assert.ok(deformation.animatedDistance > 0.1, JSON.stringify(deformation));
  assert.ok(deformation.resetError < 1e-5, JSON.stringify(deformation));
  const radius = () => page.evaluate(() => {
    const view=window.__motionTestView;
    return view.camera.position.distanceTo(view.pivotNavigation?.pivot || view.controls.target);
  });
  const pivotNavigation = await page.evaluate(() => Boolean(window.__motionTestView.pivotNavigation));
  const nav = await page.locator('#test-nav-canvas').boundingBox();
  await page.mouse.move(nav.x+100, nav.y+100);
  await page.keyboard.down('Alt');
  await page.mouse.down({button:'right'});
  const beforeVertical = await radius();
  await page.mouse.move(nav.x+100, nav.y+180, {steps:5});
  await page.mouse.up({button:'right'});
  assert.ok(pivotNavigation ? await radius()>beforeVertical : Math.abs(await radius()-beforeVertical)<1e-6,
    'Stager pivot navigation uses vertical dolly, while historical controls use horizontal dolly.');
  await page.mouse.move(nav.x+100, nav.y+100);
  await page.mouse.down({button:'right'});
  const beforeHorizontal = await radius();
  await page.mouse.move(nav.x+180, nav.y+100, {steps:5});
  await page.mouse.up({button:'right'});
  await page.keyboard.up('Alt');
  assert.ok(await radius()<beforeHorizontal, 'Horizontal Alt+RMB must zoom in');
  await page.evaluate(() => {
    const view=window.__motionTestView;
    view.pivotNavigation?.dispose();
    view.renderer.setAnimationLoop(null); view._clear(); view.controls.dispose(); view.renderer.dispose(); view.resizeObserver.disconnect(); view.canvas.parentElement.remove();
    delete window.__motionTestView;
  });
  await page.setViewportSize({width: 1060, height: 700});
  await page.screenshot({path: path.join(screenshots, 'browser-small.png')});
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.reload();
  await page.waitForFunction(() => !document.querySelector('#scan-root').disabled);
  for (const selector of ['#show-skeleton', '#lock-root']) assert.equal(await page.locator(selector).isChecked(), false);
  assert.equal(await page.locator('#output-path').inputValue(), outputFolder);
  await page.locator('#scan-root').click();
  await page.waitForFunction(() => document.querySelector('#clip-count').textContent === '3');
  await page.locator('#library-filters > summary').click();
  await page.locator('#clip-sort').selectOption('nicknamed-first');
  assert.equal(await page.locator('.clip-name').first().innerText(), 'Friendly wave');
  assert.equal(await page.locator('.clip-row').filter({hasText: 'Friendly wave'}).count(), 1);
  const restoredAnnotations = await page.evaluate(async () => Object.values((await window.pywebview.api.bootstrap()).data.catalog));
  assert.ok(restoredAnnotations.some(value => value.name==='Friendly wave' && value.notes===legacyNotes && value.favorite===true));
  assert.equal(await page.locator('#favorites-filter, #favorite-clip, .clip-favorite, #i-star').count(), 0);
  await page.locator('#close-app').click();
  await page.waitForFunction(() => document.querySelector('#status-text').textContent.includes('Application closed'));
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({passed:true,deformation,exportCopy:copied,horizontalZoom:true,nicknamedFirst:true,fbxRemoved:true,legacyFbxDisabled:true,inPlacePreviewOnly:true,previewDefaults:{skeleton:false,inPlace:false},uncaughtErrors:errors}, null, 2));
  await browser.close();
})().catch(error => {console.error(error); process.exit(1);});
