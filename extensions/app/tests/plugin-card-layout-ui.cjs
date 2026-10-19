const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const base = process.env.PLUGIN_LAYOUT_URL || 'http://127.0.0.1:1461';
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: 'en' });
    await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
    await page.goto(`${base}/?mock=running`);
    await page.locator('.app-shell').waitFor();
    assert.equal(new URL(page.url()).origin, base);
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('app:navigate', { detail: 'plugins' })));
    await page.locator('.plugin-menus button').nth(1).waitFor();
    const failures = [];
    let scenarios = 0;
    for (const theme of ['light', 'dark']) {
      await page.evaluate(async theme => (await import('/src/theme.ts')).initializeTheme().setPreference(theme), theme);
      await page.evaluate(() => Promise.all(document.getAnimations().filter(a => a.effect?.getTiming().iterations !== Infinity).map(a => a.finished.catch(() => {}))));
      if (process.env.PLUGIN_LAYOUT_SHOTS) {
        fs.mkdirSync(process.env.PLUGIN_LAYOUT_SHOTS, { recursive: true });
        await page.screenshot({ path: path.join(process.env.PLUGIN_LAYOUT_SHOTS, `${theme}.png`), fullPage: true });
      }
      const layout = await page.evaluate(() => {
        const card = document.querySelector('.plugin-card:has(.plugin-menus)');
        const bounds = card.getBoundingClientRect();
        const style = getComputedStyle(card);
        const remove = card.querySelector('.plugin-delete').getBoundingClientRect();
        const menus = [...card.querySelectorAll('.plugin-menus button')].map(button => button.getBoundingClientRect().top);
        return { rightGap: bounds.right - parseFloat(style.borderRightWidth) - parseFloat(style.paddingRight) - remove.right, menus, iconWidth: remove.width };
      });
      console.log(theme, JSON.stringify(layout));
      for (const [name, valid] of [
        ['delete aligns with content right edge', Math.abs(layout.rightGap) <= 1],
        ['resource buttons share a row', layout.menus.length === 2 && Math.abs(layout.menus[0] - layout.menus[1]) <= 1],
        ['icon retains trunk width', layout.iconWidth >= 30],
      ]) {
        scenarios++;
        if (!valid) failures.push(`${theme}: ${name}`);
      }
    }
    assert.deepEqual(failures, []);
    console.log(`Plugin card layout: ${scenarios} assertions passed`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
