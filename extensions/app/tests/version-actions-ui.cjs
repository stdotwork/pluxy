// Version Management action buttons keep their full labels at every window size; the row wraps instead of clipping.
// Run with a Vite dev server at VERSION_TEST_BASE, or let the script start one.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

const displays = [[320, 640], [375, 812], [768, 1024], [1024, 768], [1280, 800], [1440, 900], [1920, 1080]];

(async () => {
  let server;
  let browser;
  let base = process.env.VERSION_TEST_BASE;
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
      await page.locator('.nav-section button').filter({ has: page.getByText('Version Management', { exact: true }) }).click();
      await page.locator('.version-card-actions button').first().waitFor();
      const clipped = await page.evaluate(() => [...document.querySelectorAll('.version-card-actions button')].flatMap(button => {
        const box = button.getBoundingClientRect();
        const card = button.closest('.version-card-actions').getBoundingClientRect();
        const issues = [];
        if (button.scrollWidth > button.clientWidth + 1) issues.push(`"${button.textContent.trim()}" label clipped (${button.scrollWidth} > ${button.clientWidth})`);
        for (const label of button.querySelectorAll('span')) if (label.scrollWidth > label.clientWidth + 1) issues.push(`"${label.textContent.trim()}" label cut with an ellipsis (${label.scrollWidth} > ${label.clientWidth})`);
        if (box.left < card.left - 1 || box.right > card.right + 1) issues.push(`"${button.textContent.trim()}" outside its row`);
        return issues;
      }));
      assert.deepEqual(clipped, [], `${width}x${height}`);
      await context.close();
    }
    console.log(`Version actions: ${displays.length} displays passed`);
  } finally {
    if (browser) await browser.close();
    if (server) await server.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
