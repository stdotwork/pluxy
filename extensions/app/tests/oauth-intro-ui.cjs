// OAuth sign-in banner wraps on narrow windows instead of squeezing its text to one letter per line.
// Run with a Vite dev server at OAUTH_TEST_BASE, or let the script start one.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

const displays = [[320, 640], [375, 812], [414, 896], [768, 1024], [1024, 768], [1440, 900]];

(async () => {
  let server;
  let browser;
  let base = process.env.OAUTH_TEST_BASE;
  try {
    if (!base) {
      const { createServer } = await import('vite');
      const react = (await import('@vitejs/plugin-react')).default;
      server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()], logLevel: 'error', server: { host: '127.0.0.1', port: 0 } });
      await server.listen();
      base = `http://127.0.0.1:${server.httpServer.address().port}`;
    }
    const channel = process.env.PLAYWRIGHT_CHANNEL || (process.platform === 'win32' ? 'msedge' : undefined);
    browser = await chromium.launch({ channel, headless: true, args: ['--no-proxy-server'] });
    for (const [width, height] of displays) {
      const context = await browser.newContext({ viewport: { width, height } });
      await context.addInitScript(() => localStorage.setItem('easy-cli-proxy-api.locale', 'en'));
      const page = await context.newPage();
      await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
      await page.goto(`${base}/?mock=running`);
      await page.locator('.app-shell').waitFor();
      await page.locator('.nav-section button').filter({ has: page.getByText('OAuth', { exact: true }) }).click();
      await page.locator('.oauth-intro').waitFor();
      const banner = await page.locator('.oauth-intro').evaluate(element => {
        const box = element.getBoundingClientRect();
        const text = element.querySelector('.notice-main').getBoundingClientRect();
        const action = element.querySelector('.oauth-intro-action').getBoundingClientRect();
        return { height: box.height, textWidth: text.width, width: box.width, actionInside: action.left >= box.left - 1 && action.right <= box.right + 1 };
      });
      const display = `${width}x${height}`;
      assert.ok(banner.textWidth >= banner.width * 0.6, `${display}: banner text keeps most of the banner width (${Math.round(banner.textWidth)} of ${Math.round(banner.width)}px)`);
      assert.ok(banner.height <= 260, `${display}: banner stays a few lines tall (${Math.round(banner.height)}px)`);
      assert.equal(banner.actionInside, true, `${display}: the action button stays inside the banner`);
      await context.close();
    }
    console.log(`OAuth banner: ${displays.length} displays passed`);
  } finally {
    if (browser) await browser.close();
    if (server) await server.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
