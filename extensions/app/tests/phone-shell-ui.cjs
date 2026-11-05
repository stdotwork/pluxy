// App shell across window sizes, aspect ratios and pixel densities, on the browser mock: the shell never scrolls sideways,
// the theme menu opens without moving it, and the active navigation item stays in view.
// Run with a Vite dev server at SHELL_TEST_BASE, or let the script start one.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

// [width, height, deviceScaleFactor]: windows from the app's 640px minimum width up: tablets, laptops, desktops, ultrawide, portrait monitor and a short window.
const displays = [
  [640, 800, 2], [768, 1024, 2], [1024, 768, 1], [800, 600, 1.25], [1280, 800, 1.5],
  [1366, 768, 1], [1440, 900, 2], [1920, 1080, 1], [2560, 1440, 1], [3440, 1440, 1], [1080, 1920, 1], [1280, 420, 1],
];

// Everything that could push the shell wider than the window, plus where the sidebar and the theme control sit.
const measure = () => {
  const issues = [];
  const wide = (name, node) => { if (node && node.scrollWidth > node.clientWidth + 1) issues.push(`${name} is wider than its box (${node.scrollWidth} > ${node.clientWidth})`); };
  wide('document', document.documentElement);
  wide('body', document.body);
  wide('.app-shell', document.querySelector('.app-shell'));
  wide('.sidebar', document.querySelector('.sidebar'));
  wide('.content', document.querySelector('.content'));
  const shell = document.querySelector('.app-shell');
  if (shell.scrollLeft !== 0) issues.push(`.app-shell scrolled sideways by ${shell.scrollLeft}px`);
  if (document.documentElement.scrollLeft !== 0) issues.push(`document scrolled sideways by ${document.documentElement.scrollLeft}px`);
  const inside = (name, selector) => {
    const element = document.querySelector(selector);
    if (!element) { issues.push(`${name} is missing`); return; }
    const rect = element.getBoundingClientRect();
    if (rect.left < -1 || rect.right > innerWidth + 1) issues.push(`${name} is outside the window (${Math.round(rect.left)}..${Math.round(rect.right)})`);
    return rect;
  };
  const shellRect = inside('.app-shell', '.app-shell');
  if (shellRect && shellRect.width > innerWidth + 1) issues.push(`.app-shell is ${Math.round(shellRect.width)}px in a ${innerWidth}px window`);
  inside('.sidebar', '.sidebar');
  inside('theme control', '.theme-selector-trigger');
  return issues;
};

(async () => {
  let server;
  let browser;
  let base = process.env.SHELL_TEST_BASE;
  try {
    if (!base) {
      const { createServer } = await import('vite');
      const react = (await import('@vitejs/plugin-react')).default;
      server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()], logLevel: 'error', server: { host: '127.0.0.1', port: 0 } });
      await server.listen();
      base = `http://127.0.0.1:${server.httpServer.address().port}`;
    }
    const channel = process.env.PLAYWRIGHT_CHANNEL || (process.platform === 'win32' ? 'msedge' : undefined);
    browser = await chromium.launch({ channel, headless: true, args: ['--no-proxy-server'], timeout: Number(process.env.PLAYWRIGHT_LAUNCH_TIMEOUT || 30000) });
    for (const [width, height, deviceScaleFactor] of displays) {
      const display = `${width}x${height}@${deviceScaleFactor}x`;
      const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor });
      await context.addInitScript(() => localStorage.setItem('easy-cli-proxy-api.locale', 'en'));
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(String(error)));
      await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
      await page.goto(`${base}/?mock=running`);
      await page.locator('.app-shell').waitFor();
      await page.locator('#browser-mock-toolbar').evaluate(element => { element.style.display = 'none'; });
      const settle = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

      assert.deepEqual(await page.evaluate(measure), [], `${display}: shell with the theme menu closed`);
      if (width <= 900) {
        const brand = await page.locator('.sidebar-brand').boundingBox();
        assert.ok(brand && brand.width >= 30, `${display}: the brand keeps its room in the top bar (${brand && Math.round(brand.width)}px)`);
      }

      await page.locator('.theme-selector-trigger').click();
      await page.locator('.theme-selector-menu').waitFor();
      await settle();
      assert.deepEqual(await page.evaluate(measure), [], `${display}: shell with the theme menu open`);
      const menu = await page.locator('.theme-selector-menu').boundingBox();
      assert.ok(menu.x >= -1 && menu.x + menu.width <= width + 1, `${display}: theme menu sits inside the window`);
      await page.keyboard.press('Escape');
      await page.locator('.theme-selector-menu').waitFor({ state: 'detached' });

      // A late navigation item opened without scrolling to it first (as a link on another page does) ends up inside both the
      // navigation row and the window.
      const last = page.locator('.nav-section button:not(.nav-plugin-toggle)').filter({ has: page.getByText('Plugins', { exact: true }) }).first();
      await last.evaluate(button => button.click());
      await page.waitForFunction(() => document.querySelector('.nav-section button.active')?.textContent?.includes('Plugins'));
      await settle();
      const placement = await page.evaluate(() => {
        const active = document.querySelector('.nav-section button.active').getBoundingClientRect();
        const nav = document.querySelector('.nav-section').getBoundingClientRect();
        return { active, nav, innerWidth, innerHeight };
      });
      const { active, nav } = placement;
      assert.ok(active.left >= nav.left - 1 && active.right <= nav.right + 1 && active.top >= nav.top - 1 && active.bottom <= nav.bottom + 1,
        `${display}: the active item is inside the navigation (${Math.round(active.left)}..${Math.round(active.right)} of ${Math.round(nav.left)}..${Math.round(nav.right)})`);
      assert.ok(active.left >= -1 && active.right <= width + 1, `${display}: the active item is inside the window`);
      assert.deepEqual(await page.evaluate(measure), [], `${display}: shell after navigating`);
      assert.deepEqual(errors, [], `${display}: no page errors`);
      await context.close();
    }
  } finally {
    await browser?.close();
    await server?.close();
  }
  console.log('phone-shell-ui: ok');
})().catch(error => { console.error(error); process.exit(1); });
