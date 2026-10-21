const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');

(async () => {
  const { createServer } = await import('vite');
  const react = (await import('@vitejs/plugin-react')).default;
  const server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()],
    optimizeDeps: { entries: ['tests/fixtures/plugin-quota.html'] }, logLevel: 'error', server: { host: '127.0.0.1', port: 0, watch: null } });
  let browser;
  try {
    await server.listen();
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    browser = await chromium.launch({ channel: 'msedge', headless: true });
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    await page.goto(base + '/tests/fixtures/plugin-quota.html');
    await page.locator('.real-quota-card').first().waitFor();
    assert.equal(await page.locator('.real-quota-card').count(), 2);
    await page.getByRole('button', { name: /刷新额度/ }).first().click();
    await page.waitForFunction(() => document.querySelectorAll('.real-quota-track').length === 2);
    assert.equal(await page.getByRole('progressbar').first().getAttribute('aria-valuenow'), '75');
    assert.equal(await page.getByText('12 credits', { exact: true }).count(), 2);
    const calls = await page.evaluate(() => window.pluginQuotaCalls);
    assert.equal(calls.filter(call => call.path === '/plugins').length, 1);
    assert.deepEqual(calls.filter(call => call.path.endsWith('/quota')).map(call => call.body.auth_index).sort(), ['one', 'two']);
    await fs.mkdir('bin-work/plugin-quota-qa', { recursive: true });
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true);
      await page.screenshot({ path: `bin-work/plugin-quota-qa/${width}.png`, fullPage: true });
    }
    assert.deepEqual(errors, []);
    console.log('PASS: plugin Gemini cards, legacy discovery, refresh-all routing, summaries and responsive layout');
  } finally { await browser?.close(); await server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
