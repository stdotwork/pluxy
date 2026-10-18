const { chromium, webkit } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.PICKER_TEST_URL || 'http://127.0.0.1:1437';

(async () => {
  let scenarios = 0;
  for (const engine of [chromium, webkit].filter(engine => !process.env.PICKER_ENGINE || engine.name() === process.env.PICKER_ENGINE)) {
    const browser = await engine.launch({ headless: true });
    try {
      const page = await browser.newPage({ locale: 'zh-CN', viewport: { width: 1100, height: 720 } });
      const errors = [];
      page.on('pageerror', error => errors.push(String(error)));
      await page.goto(`${base}/tests/fixtures/credential-settings.html`);
      await page.locator('.credential-settings-body[aria-busy="false"]').waitFor();
      const body = page.locator('.credential-settings-body');
      for (let index = 0; index < 3; index++) await page.getByRole('button', { name: '添加模型', exact: true }).click();
      const input = page.getByRole('combobox', { name: '模型名称', exact: true }).last();
      const placeRowAboveFooter = () => input.evaluate(element => {
        const scroller = element.closest('.credential-settings-body');
        const footer = element.closest('[role="dialog"]').querySelector('.credential-settings-footer');
        scroller.scrollTop += element.getBoundingClientRect().bottom - (footer.getBoundingClientRect().top - 40);
      });
      const menu = page.locator('.credential-model-menu');
      const bounds = () => page.evaluate(() => {
        const dialog = document.querySelector('[role="dialog"]');
        const rect = document.querySelector('.credential-model-menu').getBoundingClientRect();
        return {
          top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right,
          minTop: dialog.querySelector('.credential-settings-heading').getBoundingClientRect().bottom + 12,
          maxBottom: dialog.querySelector('.credential-settings-footer').getBoundingClientRect().top - 12,
          minLeft: dialog.getBoundingClientRect().left + 12, maxRight: dialog.getBoundingClientRect().right - 12,
          popover: document.querySelector('.credential-model-menu').matches(':popover-open'),
          trigger: [...document.querySelectorAll('.agent-model-picker.open')].map(element => element.getBoundingClientRect()).map(box => ({ left: box.left, top: box.top, bottom: box.bottom }))[0],
        };
      });

      await placeRowAboveFooter();
      await input.click();
      await menu.waitFor();
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      const placed = await bounds();
      assert.equal(placed.popover, true, `${engine.name()}: the menu must enter the top layer inside a dialog`);
      assert.ok(placed.top >= placed.minTop - 1 && placed.bottom <= placed.maxBottom + 1, `${engine.name()}: the menu must stay between the dialog heading and footer: ${JSON.stringify(placed)}`);
      assert.ok(placed.left >= placed.minLeft - 1 && placed.right <= placed.maxRight + 1, `${engine.name()}: the menu must stay inside the dialog frame`);
      assert.ok(Math.abs(placed.left - placed.trigger.left) < 2, `${engine.name()}: the menu must align with its trigger: ${JSON.stringify(placed)}`);
      assert.ok(Math.abs(placed.bottom - (placed.trigger.top - 6)) < 2 || Math.abs(placed.top - (placed.trigger.bottom + 6)) < 2, `${engine.name()}: the menu must open next to its trigger: ${JSON.stringify(placed)}`);
      scenarios++;

      await page.getByRole('option').filter({ has: page.getByText('gpt-3', { exact: true }) }).click();
      assert.equal(await input.inputValue(), 'gpt-3', `${engine.name()}: option clicks inside the dialog must select`);
      assert.equal(await menu.count(), 0);
      scenarios++;

      for (const target of [input, page.getByRole('button', { name: '模型名称', exact: true }).last()]) {
        await target.click();
        await menu.waitFor();
        await target.press('Escape');
        assert.equal(await menu.count(), 0, `${engine.name()}: Escape closes the picker`);
        assert.equal(await page.evaluate(() => document.querySelectorAll('.credential-settings-dialog').length), 1);
        await page.keyboard.press('Escape');
        await page.waitForFunction(() => window.fixtureDialogCloses > 0);
        assert.equal(await page.evaluate(() => window.fixtureDialogCloses), 1, `${engine.name()}: the first Escape must not close the dialog`);
        await page.evaluate(() => { window.fixtureDialogCloses = 0; });
        scenarios++;
      }

      await input.click();
      await menu.waitFor();
      await body.evaluate(element => { element.scrollTop = 0; });
      await page.waitForFunction(() => !document.querySelector('.credential-model-menu'));
      assert.equal(await page.getByRole('dialog').count(), 1);
      scenarios++;
      assert.deepEqual(errors, [], `${engine.name()}: page errors`);
    } finally { await browser.close(); }
  }
  console.log(`PASS: credential dialog model picker, ${scenarios} scenarios (top layer, dialog bounds, trigger alignment, option click, Escape from input and toggle, scroll-out close).`);
})().catch(error => { console.error(error); process.exitCode = 1; });
