const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.QUOTA_TEST_BASE || 'http://127.0.0.1:1421';

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
    await page.goto(`${base}/tests/fixtures/quota-layout.html`);
    const error = page.locator('.real-quota-card').filter({ has: page.locator('strong[title="failed.json"]') });
    await error.locator('.quota-card-error small').waitFor();
    let scenarios = 0;
    for (const theme of ['light', 'dark']) for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.locator('.content').evaluate((element, width) => { element.style.width = `${width - 40}px`; }, width);
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      const layout = await error.locator('.quota-card-error small').evaluate(detail => {
        const title = document.createRange();
        title.selectNodeContents(detail.parentElement.firstChild);
        return { titleBottom: title.getBoundingClientRect().bottom, detailTop: detail.getBoundingClientRect().top };
      });
      assert.ok(layout.detailTop >= layout.titleBottom + 3, `${theme}/${width}: error details must occupy a separate row below the title: ${JSON.stringify(layout)}`);
      const idle = page.locator('.real-quota-card').filter({ has: page.locator('strong[title="idle.json"]') });
      const sizes = await idle.evaluate(card => {
        const refresh = card.querySelector('.quota-card-actions button');
        const primary = card.querySelector('.quota-card-message button');
        return {
          refreshHeight: refresh.getBoundingClientRect().height,
          refreshWidth: refresh.getBoundingClientRect().width,
          refreshRadius: getComputedStyle(refresh).borderRadius,
          refreshFontSize: parseFloat(getComputedStyle(refresh).fontSize),
          primaryHeight: primary.getBoundingClientRect().height,
          primaryWidth: primary.getBoundingClientRect().width,
          primaryFontSize: parseFloat(getComputedStyle(primary).fontSize),
          cardHeight: card.getBoundingClientRect().height,
        };
      });
      assert.equal(sizes.refreshHeight, 24, `${theme}/${width}: secondary refresh uses the shared tiny size`);
      assert.equal(sizes.refreshRadius, '8px', `${theme}/${width}: secondary refresh uses the shared radius`);
      assert.ok(sizes.refreshHeight < sizes.primaryHeight && sizes.refreshFontSize < sizes.primaryFontSize, `${theme}/${width}: refresh remains visually lighter than Fetch Quota: ${JSON.stringify(sizes)}`);
      assert.ok(sizes.cardHeight <= (width > 1000 ? 140 : 170), `${theme}/${width}: idle card remains compact: ${sizes.cardHeight}`);
      console.log(theme, width, JSON.stringify(sizes));
      scenarios++;
    }
    console.log(`Quota card: ${scenarios} error-layout and compact-refresh scenarios passed`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
