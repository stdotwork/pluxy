const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');

(async () => {
  const { createServer } = await import('vite');
  const react = (await import('@vitejs/plugin-react')).default;
  const server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()],
    optimizeDeps: { entries: ['tests/fixtures/auth-file-cooldown.html'] }, logLevel: 'error',
    server: { host: '127.0.0.1', port: 0, watch: null } });
  let browser;
  const screenshotDir = path.resolve('bin-work/auth-file-cooldown-qa');
  try {
    await server.listen();
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
    await page.clock.install({ time: new Date('2026-10-03T04:00:00Z') });
    const card = name => page.locator('.auth-file-card').filter({ has: page.locator('.auth-card-identity strong', { hasText: name }) });
    const primary = () => card('01-primary.json');
    const other = () => card('02-other.json');
    const clear = target => target.getByRole('button', { name: '清除冷却', exact: true });
    const confirmation = () => page.getByRole('alertdialog', { name: '清除凭证冷却？', exact: true });
    const requests = () => page.evaluate(() => window.cooldownFixture.resetRequests);
    const open = async query => {
      await page.goto(`${base}/tests/fixtures/auth-file-cooldown.html?${query || ''}`);
      await primary().waitFor();
    };
    const releaseReset = () => page.evaluate(() => window.cooldownFixture.releaseReset());
    const confirmReset = async target => {
      await clear(target).click();
      await confirmation().waitFor();
      await confirmation().getByRole('button', { name: '清除冷却', exact: true }).click();
      await page.waitForFunction(() => Boolean(window.cooldownFixture.releaseReset));
    };
    await fs.mkdir(screenshotDir, { recursive: true });
    await open();

    assert.equal(await primary().locator('details').getAttribute('open'), null);
    assert.equal(await clear(primary()).isVisible(), true, 'Reset is discoverable while details are collapsed');
    for (const name of ['05-empty.json', '06-null.json', '07-absent.json', '08-no-index.json']) {
      assert.equal(await clear(card(name)).count(), 0, `${name} must not offer an unsafe or inapplicable reset`);
    }
    await primary().locator('.auth-health-summary').click();
    const details = primary().locator('.auth-health-body');
    for (const text of ['整个凭证', 'gpt-fictional', 'claude-fictional', '额度或速率限制', '上游服务暂时异常', 'HTTP 429', 'HTTP 503', '退避等级 0']) {
      assert.ok((await details.innerText()).includes(text), `Cooldown detail contains ${text}`);
    }
    assert.equal(await details.locator('time').count(), 4, 'Every cooldown and its observation timestamp are available');
    const beforeTick = await primary().locator('.auth-health-countdown').innerText();
    await page.clock.runFor(3100);
    assert.notEqual(await primary().locator('.auth-health-countdown').innerText(), beforeTick, 'Countdown advances without a list reload');
    assert.equal(await page.evaluate(() => window.cooldownFixture.reads), 1);
    assert.ok((await card('04-expired-soon.json').innerText()).includes('冷却计时已到期'));
    assert.equal(await clear(card('04-expired-soon.json')).isEnabled(), true, 'Elapsed records remain explicitly resettable');
    assert.ok((await card('03-disabled-runtime.json').innerText()).includes('运行时'));
    assert.ok((await card('03-disabled-runtime.json').locator('.auth-file-health').innerText()).includes('已停用'));
    assert.equal(await clear(card('03-disabled-runtime.json')).isEnabled(), true, 'Runtime and disabled credentials can clear existing cooldown records');
    await page.screenshot({ path: path.join(screenshotDir, 'details-light-desktop.png'), fullPage: true });

    await clear(primary()).click();
    await confirmation().waitFor();
    assert.ok((await confirmation().innerText()).includes('01-primary.json'));
    assert.deepEqual(await requests(), [], 'Opening confirmation does not submit a reset');
    await confirmation().getByRole('button', { name: '取消', exact: true }).click();
    assert.deepEqual(await requests(), [], 'Cancel leaves the backend untouched');
    assert.equal(await primary().locator('.auth-health-records li').count(), 3);

    const otherDeadline = await other().locator('.auth-health-records time').getAttribute('datetime');
    const otherBefore = await other().locator('.auth-health-countdown').innerText();
    await confirmReset(primary());
    const pending = primary().locator('.auth-file-health button');
    assert.equal(await pending.isDisabled(), true, 'Pending reset prevents duplicate submission');
    await pending.evaluate(button => button.click());
    const sent = await requests();
    assert.equal(sent.length, 1);
    assert.equal(sent[0].method, 'POST');
    assert.equal(sent[0].path, '/routing/cooldown/reset');
    assert.deepEqual(sent[0].body, { auth_index: 'auth-primary-123' }, 'Only the selected auth_index is sent; no all/model scope');
    assert.equal(await primary().locator('.auth-health-records li').count(), 3, 'Pending request retains existing cooldown details');
    await page.clock.runFor(3000);
    await releaseReset();
    await page.waitForFunction(() => window.cooldownFixture.reads === 2);
    await primary().locator('.auth-file-health').waitFor({ state: 'detached' });
    assert.equal(await other().locator('.auth-health-records li').count(), 1);
    assert.equal(await other().locator('.auth-health-records time').getAttribute('datetime'), otherDeadline);
    assert.notEqual(await other().locator('.auth-health-countdown').innerText(), otherBefore, 'A sibling timer continues instead of restarting or clearing');
    assert.ok((await page.locator('.app-notice-stack').innerText()).includes('01-primary.json'), 'Success identifies the cleared credential');

    await open('resetError=1');
    await primary().locator('.auth-health-summary').click();
    await confirmReset(primary());
    await releaseReset();
    await page.getByText(/Fixture cooldown reset failed/).waitFor();
    assert.equal(await primary().locator('.auth-health-records li').count(), 3, 'A rejected reset retains the displayed records');
    assert.equal(await clear(primary()).isEnabled(), true, 'Failure allows a deliberate retry');
    assert.equal(await page.evaluate(() => window.cooldownFixture.reads), 1, 'Failure does not pretend a refreshed successful state');
    await page.screenshot({ path: path.join(screenshotDir, 'reset-failure.png'), fullPage: true });

    await open();
    await confirmReset(card('03-disabled-runtime.json'));
    await releaseReset();
    await page.waitForFunction(() => window.cooldownFixture.reads === 2);
    await clear(card('03-disabled-runtime.json')).waitFor({ state: 'detached' });
    assert.ok((await card('03-disabled-runtime.json').innerText()).includes('运行时'));
    assert.ok((await card('03-disabled-runtime.json').locator('.auth-file-health').innerText()).includes('已停用'), 'Clearing cooldown preserves disabled state');

    await open();
    await confirmReset(primary());
    await page.evaluate(() => { window.cooldownFixture.holdNextRead = true; });
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await page.waitForFunction(() => Boolean(window.cooldownFixture.releaseRead));
    await releaseReset();
    await page.waitForFunction(() => window.cooldownFixture.reads === 3 && window.cooldownFixture.completedReads === 2);
    await primary().waitFor();
    assert.equal(await primary().locator('.auth-file-health').count(), 0);
    await page.evaluate(() => window.cooldownFixture.releaseRead());
    await page.waitForFunction(() => window.cooldownFixture.completedReads === 3);
    await page.clock.runFor(100);
    assert.equal(await primary().locator('.auth-file-health').count(), 0, 'An older list response cannot resurrect cleared cooldowns');

    for (const theme of ['light', 'dark']) {
      for (const width of [1280, 390]) {
        await page.setViewportSize({ width, height: 900 });
        await open(`theme=${theme}`);
        await primary().locator('.auth-health-summary').click();
        await other().locator('.auth-health-summary').click();
        const sizes = await page.locator('.auth-file-card, .auth-file-health').evaluateAll(nodes => nodes.map(node => ({
          className: node.className, width: node.clientWidth, scroll: node.scrollWidth,
        })));
        assert.ok(sizes.every(size => size.scroll <= size.width + 1), `${theme} ${width}: ${JSON.stringify(sizes)}`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true, `No page overflow at ${theme} ${width}`);
        await page.screenshot({ path: path.join(screenshotDir, `details-${theme}-${width}.png`), fullPage: true });
        await primary().screenshot({ path: path.join(screenshotDir, `card-${theme}-${width}.png`) });
        await clear(primary()).click();
        await confirmation().waitFor();
        assert.equal(await confirmation().evaluate(node => node.scrollWidth <= node.clientWidth + 1), true, 'Confirmation fits viewport');
        await page.screenshot({ path: path.join(screenshotDir, `confirmation-${theme}-${width}.png`), fullPage: true });
        await confirmation().screenshot({ path: path.join(screenshotDir, `dialog-${theme}-${width}.png`) });
        await confirmation().getByRole('button', { name: '取消', exact: true }).click();
      }
    }
    assert.deepEqual(errors, []);
    console.log('PASS: cooldown models/reasons/countdowns, confirmation and cancel, single auth_index POST, pending duplicate protection, isolated success refresh, failure preservation, stale response protection, unknown/empty/missing-index guards, elapsed records, runtime/disabled state, light/dark desktop/mobile layout.');
    console.log(`Screenshots: ${screenshotDir}`);
  } finally { await browser?.close(); await server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
