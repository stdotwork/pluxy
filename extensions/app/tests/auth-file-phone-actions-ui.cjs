// Authentication Files card action buttons at phone widths, on the browser mock: no button runs past its card or clips its label.
// Run with a Vite dev server at AUTH_FILES_TEST_BASE, or let the script start one.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

const widths = [320, 375, 414];

(async () => {
  let server;
  let browser;
  let base = process.env.AUTH_FILES_TEST_BASE;
  try {
    if (!base) {
      const { createServer } = await import('vite');
      const react = (await import('@vitejs/plugin-react')).default;
      server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()], logLevel: 'error', server: { host: '127.0.0.1', port: 0 } });
      await server.listen();
      base = `http://127.0.0.1:${server.httpServer.address().port}`;
    }
    const channel = process.env.PLAYWRIGHT_CHANNEL || (process.platform === 'win32' ? 'msedge' : undefined);
    browser = await chromium.launch({ channel, headless: true, timeout: Number(process.env.PLAYWRIGHT_LAUNCH_TIMEOUT) || undefined, args: ['--no-proxy-server'] });
    for (const width of widths) {
      const context = await browser.newContext({ viewport: { width, height: 800 }, deviceScaleFactor: 2 });
      await context.addInitScript(() => localStorage.setItem('easy-cli-proxy-api.locale', 'en'));
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(String(error)));
      await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
      await page.goto(`${base}/?mock=running`);
      await page.locator('.app-shell').waitFor();
      await page.locator('#browser-mock-toolbar').evaluate(element => { element.style.display = 'none'; });
      await page.locator('.nav-section button').filter({ has: page.getByText('OAuth', { exact: true }) }).click();
      await page.getByRole('tab', { name: 'Authentication Files' }).click();
      await page.locator('.auth-file-card').first().waitFor();
      const result = await page.evaluate(() => {
        const issues = [];
        const cards = [...document.querySelectorAll('.auth-file-card')];
        let buttons = 0;
        cards.forEach((card, index) => {
          const rect = card.getBoundingClientRect();
          for (const button of card.querySelectorAll('.auth-card-actions button')) {
            const box = button.getBoundingClientRect();
            if (box.width === 0 || box.height === 0) continue;
            buttons += 1;
            const name = button.getAttribute('aria-label') || button.textContent.trim();
            if (box.left < rect.left - 1 || box.right > rect.right + 1) issues.push(`card ${index}: button "${name}" runs past the card (${Math.round(box.right)} > ${Math.round(rect.right)})`);
            if (button.scrollWidth > button.clientWidth + 1) issues.push(`card ${index}: button "${name}" clips its label`);
          }
        });
        return { issues, cards: cards.length, buttons };
      });
      assert.ok(result.cards > 0 && result.buttons > 0, `${width}px: cards with action buttons are shown`);
      assert.deepEqual(result.issues, [], `${width}px`);
      assert.deepEqual(errors, [], `${width}px: no page errors`);
      await context.close();
    }
    console.log(`Authentication Files phone actions: ${widths.join(', ')} px passed`);
  } finally {
    if (browser) await browser.close();
    if (server) await server.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
