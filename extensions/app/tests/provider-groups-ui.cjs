const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');

(async () => {
  const { createServer } = await import('vite');
  const react = (await import('@vitejs/plugin-react')).default;
  const server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()],
    optimizeDeps: { entries: ['tests/fixtures/provider-groups.html'] }, logLevel: 'error',
    server: { host: '127.0.0.1', port: 0, watch: null } });
  let browser;
  try {
    await server.listen();
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(base) ? route.continue() : route.abort());
    await page.goto(`${base}/tests/fixtures/provider-groups.html`);
    const rows = page.locator('.real-provider-row');
    const primary = () => rows.filter({ hasText: 'Primary gateway' });
    const groups = () => page.evaluate(() => window.groupFixture.groups);
    const dialog = page.getByRole('dialog', { name: 'Edit Group', exact: true });
    await primary().waitFor();
    assert.equal(await rows.count(), 2, 'One row per group, not per key');
    const initial = await groups();
    await primary().getByRole('button', { name: 'Edit', exact: true }).click();
    assert.equal(await dialog.locator('.provider-group-key').count(), 2);
    await dialog.getByLabel('Priority', { exact: true }).last().fill('7');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });
    let saved = await groups();
    assert.deepEqual(saved, [{ ...initial[0], priority: 7 }, initial[1]]);

    await primary().getByRole('button', { name: 'Edit', exact: true }).click();
    const second = dialog.locator('.provider-group-key').nth(1);
    await second.locator('summary').click();
    await second.getByLabel('Override: Priority', { exact: true }).uncheck();
    await second.getByLabel('Key 2', { exact: true }).fill('rotated-second-key');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });
    saved = await groups();
    assert.equal(saved[0].keys[1]['api-key'], 'rotated-second-key');
    assert.ok(!('priority' in saved[0].keys[1]), 'Unchecking restores inheritance');
    assert.deepEqual(saved[0].keys[1].models, initial[0].keys[1].models);
    assert.deepEqual(saved[0].keys[0], initial[0].keys[0]);

    await primary().getByRole('button', { name: 'Edit', exact: true }).click();
    await dialog.getByLabel('Key for Model Discovery').selectOption({ index: 1 });
    await dialog.getByRole('button', { name: /Fetch Models|Get Models|Select Models/ }).click();
    await page.waitForFunction(() => window.groupFixture.probes.length > 0);
    const probe = await page.evaluate(() => window.groupFixture.probes.at(-1));
    assert.equal(probe.header.Authorization, 'Bearer rotated-second-key');
    assert.equal(probe.header['X-Key'], 'second');
    assert.ok(!('X-Group' in probe.header));
    await page.locator('.model-discovery-dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();

    await page.getByRole('button', { name: 'Add Group', exact: true }).click();
    const add = page.getByRole('dialog', { name: 'Add Group', exact: true });
    await add.getByLabel('Group Name', { exact: true }).fill('New multi-key group');
    await add.getByLabel('Base URL', { exact: true }).fill('https://new.example.test/v1');
    await add.getByLabel('Key 1', { exact: true }).fill('new-key-a');
    await add.getByRole('button', { name: 'Add Key', exact: true }).click();
    await add.getByLabel('Key 2', { exact: true }).fill('new-key-b');
    await add.getByRole('button', { name: 'Save', exact: true }).click();
    await add.waitFor({ state: 'detached' });
    saved = await groups();
    assert.equal(saved.length, 3);
    assert.deepEqual(saved[2].keys, [{ 'api-key': 'new-key-a' }, { 'api-key': 'new-key-b' }]);

    await primary().getByRole('button', { name: 'Edit', exact: true }).click();
    await page.evaluate(() => { window.groupFixture.groups[0].keys[1].weight = 9; });
    const writes = await page.evaluate(() => window.groupFixture.writes.length);
    await dialog.getByLabel('Priority', { exact: true }).last().fill('9');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await dialog.locator('.action-feedback-message').waitFor();
    assert.equal(await page.evaluate(() => window.groupFixture.writes.length), writes, 'Stale save must not write');
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();

    await fs.mkdir(path.resolve('bin-work/provider-groups-qa'), { recursive: true });
    for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(viewport);
      await page.goto(`${base}/tests/fixtures/provider-groups.html?locale=zh-CN`);
      await rows.first().getByRole('button', { name: '编辑', exact: true }).click();
      const form = page.locator('.api-provider-dialog');
      await page.screenshot({ path: path.resolve(`bin-work/provider-groups-qa/edit-${viewport.width}.png`) });
      const dimensions = await form.evaluate(el => ({width: el.clientWidth, scroll: el.scrollWidth, offenders: [...el.querySelectorAll('*')].filter(child=>child.getBoundingClientRect().right > el.getBoundingClientRect().right).map(child=>({tag:child.tagName,cls:child.className,width:child.getBoundingClientRect().width})).slice(0,12)}));
      assert.ok(dimensions.scroll <= dimensions.width + 1, JSON.stringify({viewport,dimensions}));
      await page.screenshot({ path: path.resolve(`bin-work/provider-groups-qa/edit-${viewport.width}.png`) });
      await form.locator('.provider-group-key').nth(1).locator('summary').click();
      await form.locator('.provider-group-key').nth(1).scrollIntoViewIfNeeded();
      assert.ok(await form.evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'Expanded overrides fit');
      await page.screenshot({ path: path.resolve(`bin-work/provider-groups-qa/overrides-${viewport.width}.png`) });
      await form.getByRole('button', { name: '取消', exact: true }).click();
    }
    assert.deepEqual(errors, []);
    console.log('PASS: native group editing, key rotation and inheritance, discovery credentials, multi-key creation, stale-save protection and responsive layout.');
  } finally { await browser?.close(); await server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
