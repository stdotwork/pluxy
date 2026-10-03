const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');

(async () => {
  const { createServer } = await import('vite');
  const react = (await import('@vitejs/plugin-react')).default;
  const server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()],
    optimizeDeps: { entries: ['tests/fixtures/auth-file-list.html'] }, logLevel: 'error',
    server: { host: '127.0.0.1', port: 0, watch: null } });
  let browser;
  const screenshotDir = path.resolve('bin-work/auth-file-list-qa');
  try {
    await server.listen();
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
    const page = await browser.newPage({ viewport: { width: 1800, height: 1000 } });
    page.setDefaultTimeout(10000);
    const errors = [], externalRequests = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => {
      if (route.request().url().startsWith(`${base}/`)) return route.continue();
      externalRequests.push(route.request().url());
      return route.abort();
    });
    await page.clock.install({ time: new Date('2026-10-03T04:00:00Z') });
    const cards = () => page.locator('.auth-file-card');
    const card = number => cards().filter({ has: page.locator(`.auth-card-filename[title="${String(number).padStart(2, '0')}-account.json"]`) });
    const pagination = () => page.locator('.auth-list-pagination');
    const next = () => pagination().getByRole('button', { name: '下一页', exact: true });
    const previous = () => pagination().getByRole('button', { name: '上一页', exact: true });
    const open = async (query = '') => {
      await page.goto(`${base}/tests/fixtures/auth-file-list.html?${query}`);
      await card(1).waitFor();
      await page.waitForFunction(() => window.authFileListFixture.reads === 1);
    };
    const assertFits = async label => {
      const dimensions = await cards().evaluateAll(nodes => nodes.map(node => ({ width: node.clientWidth, scroll: node.scrollWidth })));
      assert.ok(dimensions.every(size => size.scroll <= size.width + 1), `${label}: cards overflow ${JSON.stringify(dimensions)}`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, `${label}: page overflow`);
    };
    await fs.mkdir(screenshotDir, { recursive: true });
    await open();
    assert.equal(await cards().count(), 10, 'Default page contains ten of twelve credentials');
    assert.equal(await previous().isDisabled(), true);
    assert.ok((await pagination().innerText()).includes('1 / 2'));
    assert.equal(await page.locator('.auth-file-list-head > span').count(), 7);
    const cells = await card(1).locator('.auth-credential-row > .auth-list-cell').evaluateAll(nodes => nodes.map(node => {
      const bounds = node.getBoundingClientRect(); return { left: bounds.left, right: bounds.right };
    }));
    assert.equal(cells.length, 7, 'Desktop row includes all seven columns');
    assert.ok(cells.slice(1).every((cell, index) => cell.left >= cells[index].right - 1), 'Desktop columns stay in one horizontal row');
    await next().click();
    await card(11).waitFor();
    assert.equal(await cards().count(), 2);
    assert.equal(await next().isDisabled(), true);
    assert.ok((await pagination().innerText()).includes('2 / 2'));

    await page.locator('.auth-files-toolbar input').fill('account');
    await card(1).waitFor();
    assert.equal(await cards().count(), 10, 'Search resets pagination even when all credentials match');
    assert.equal(await previous().isDisabled(), true);
    await next().click();
    await page.locator('.auth-files-toolbar select').first().selectOption({ label: 'Codex' });
    await card(1).waitFor();
    assert.equal(await previous().isDisabled(), true, 'Provider filter resets to the first page');
    await page.locator('.auth-files-toolbar select').first().selectOption('all');
    await next().click();
    await page.locator('.auth-files-toolbar select').last().selectOption('disabled');
    await card(3).waitFor();
    assert.equal(await cards().count(), 2, 'Disabled filter retains matching rows');
    assert.equal(await previous().isDisabled(), true);
    await page.locator('.auth-files-toolbar select').last().selectOption('all');
    await pagination().locator('select').selectOption('20');
    await card(12).waitFor();
    assert.equal(await cards().count(), 12, 'Page size twenty shows all twelve credentials');
    assert.equal(await next().isDisabled(), true);

    const detailsButton = card(1).locator('.auth-list-details-button');
    assert.equal(await card(1).locator('.auth-list-details').isVisible(), false);
    await detailsButton.click();
    assert.equal(await detailsButton.getAttribute('aria-expanded'), 'true');
    assert.equal(await card(1).locator('.auth-list-details').isVisible(), true);
    assert.ok((await card(1).locator('.auth-list-details').innerText()).includes('Fictional account note'));
    await detailsButton.click();
    assert.equal(await card(1).locator('.auth-list-details').isVisible(), false);

    const extraQuota = card(4).locator('.credential-quota-more');
    assert.equal(await extraQuota.getAttribute('open'), null);
    assert.equal(await extraQuota.getByText('Additional model B', { exact: true }).isVisible(), false);
    await extraQuota.locator('summary').click();
    assert.equal(await extraQuota.getByText('Additional model B', { exact: true }).isVisible(), true, 'All additional quotas remain accessible');
    assert.ok((await extraQuota.innerText()).includes('No quota limit supplied'));
    assert.equal(await card(1).locator('.auth-file-request-block').count(), 20);
    await card(1).locator('.auth-file-request-block').nth(18).focus();
    assert.equal(await card(1).getByRole('tooltip').isVisible(), true, 'Recent request details are keyboard accessible');
    await page.keyboard.press('Escape');
    await card(1).locator('.auth-file-request-block').nth(18).blur();
    assert.equal(await card(2).getByRole('button', { name: '清除冷却', exact: true }).isVisible(), true);
    await card(2).locator('.auth-health-summary').click();
    assert.ok((await card(2).locator('.auth-health-body').innerText()).includes('gpt-fictional-layout'), 'Model cooldown details remain available');

    await card(1).getByRole('switch').click();
    await page.waitForFunction(() => window.authFileListFixture.reads === 2);
    assert.equal(await card(1).getByRole('switch').getAttribute('aria-checked'), 'false');
    let updates = await page.evaluate(() => window.authFileListFixture.requests.filter(request => request.method === 'PATCH'));
    assert.equal(updates.length, 1);
    assert.equal(updates[0].path, '/credentials/status');
    assert.deepEqual(updates[0].body, { name: '01-account.json', disabled: true });
    await card(1).getByRole('switch').click();
    await page.waitForFunction(() => window.authFileListFixture.reads === 3);
    updates = await page.evaluate(() => window.authFileListFixture.requests.filter(request => request.method === 'PATCH'));
    assert.deepEqual(updates[1].body, { name: '01-account.json', disabled: false });
    assert.equal(await card(1).getByRole('switch').getAttribute('aria-checked'), 'true');
    assert.equal(await card(3).getByRole('switch').getAttribute('aria-checked'), 'false', 'Sibling status stays unchanged');

    await card(12).getByRole('button', { name: '删除', exact: true }).click();
    const deleteDialog = page.getByRole('alertdialog', { name: '删除', exact: true });
    await deleteDialog.waitFor();
    await deleteDialog.getByRole('button', { name: '删除', exact: true }).click();
    await card(12).waitFor({ state: 'detached' });
    assert.equal(await cards().count(), 11);
    assert.deepEqual(await page.evaluate(() => window.authFileListFixture.requests.find(request => request.method === 'DELETE')?.query), { name: '12-account.json' });
    assert.deepEqual(await page.evaluate(() => window.authFileListFixture.unhandled), []);

    const cases = ['light', 'dark'].flatMap(theme => [1800, 1280, 390].map(width => ({ theme, width, locale: 'zh-CN' })));
    cases.push({ theme: 'light', width: 1800, locale: 'en' }, { theme: 'light', width: 390, locale: 'en' });
    for (const { theme, width, locale } of cases) {
      await page.setViewportSize({ width, height: 1000 });
      await open(`theme=${theme}&locale=${locale}`);
      await assertFits(`${locale} ${theme} ${width}`);
      await page.screenshot({ path: path.join(screenshotDir, `list-${locale}-${theme}-${width}.png`), fullPage: true });
      await card(1).locator('.auth-list-details-button').click();
      await card(2).locator('.auth-health-summary').click();
      await card(4).locator('.credential-quota-more > summary').click();
      await assertFits(`${locale} ${theme} ${width} expanded`);
      await card(4).screenshot({ path: path.join(screenshotDir, `expanded-quota-${locale}-${theme}-${width}.png`) });
      assert.deepEqual(await page.evaluate(() => window.authFileListFixture.unhandled), []);
    }
    assert.deepEqual(errors, []);
    assert.deepEqual(externalRequests, []);
    console.log('PASS: offline credential list, seven desktop columns, ten/two pagination, search/provider/status reset, page size twenty, details, all quota rows, request keyboard tooltip, cooldown controls, exact status PATCH, deletion, light/dark 1800/1280/390 and English desktop/mobile without page or card overflow.');
    console.log(`Screenshots: ${screenshotDir}`);
  } finally { await browser?.close(); await server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
