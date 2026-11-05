const { chromium, webkit } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.CHOICE_TEST_URL || 'http://127.0.0.1:1437';

(async () => {
  let scenarios = 0;
  for (const engine of [chromium, webkit].filter(engine => !process.env.CHOICE_ENGINE || engine.name() === process.env.CHOICE_ENGINE)) {
    const browser = await engine.launch({ headless: true });
    try {
      const page = await browser.newPage();
      await page.goto(`${base}/tests/fixtures/choice-controls.html`);
      const run = name => !process.env.CHOICE_CASE || process.env.CHOICE_CASE === name;
      if (run('pending')) {
        for (const [label, role, button] of [['Guarded', 'tab', 'Programmatic tab'], ['Plain guarded', 'radio', 'Programmatic segment']]) {
          const group = page.getByRole(role === 'tab' ? 'tablist' : 'radiogroup', { name: label, exact: true });
          await group.getByRole(role, { name: 'one', exact: true }).focus();
          await page.keyboard.press('ArrowRight');
          await page.getByRole('button', { name: 'Reject', exact: true }).evaluate(element => element.click());
          const outside = page.getByRole('button', { name: button, exact: true });
          await outside.click();
          await page.waitForFunction(({label, role}) => document.querySelector(`[aria-label="${label}"] [${role === 'tab' ? 'aria-selected' : 'aria-checked'}="true"]`)?.textContent === 'two', {label, role});
          await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
          assert.equal(await group.evaluate(element => element.contains(document.activeElement)), false, `${label}: mouse activation must not pull focus back into the group`);
          scenarios++;
        }
        await page.reload();
      }
      if (run('manual')) {
        const group = page.getByRole('tablist', { name: 'Manual', exact: true });
        await group.getByRole('tab', { name: 'one', exact: true }).focus();
        for (const [key, value] of [['ArrowRight', 'two'], ['End', 'three'], ['Home', 'one'], ['ArrowLeft', 'three']]) {
          await page.keyboard.press(key);
          assert.equal(await group.getByRole('tab', { name: value, exact: true }).evaluate(element => document.activeElement === element), true);
          assert.equal(await group.getByRole('tab', { name: 'one', exact: true }).getAttribute('aria-selected'), 'true');
        }
        await page.keyboard.press('Enter');
        assert.equal(await group.getByRole('tab', { name: 'three', exact: true }).getAttribute('aria-selected'), 'true');
        await page.keyboard.press('Home');await page.keyboard.press('Space');
        assert.equal(await group.getByRole('tab', { name: 'one', exact: true }).getAttribute('aria-selected'), 'true');scenarios++;
      }
      if (run('modifiers')) {
        for (const [label, role] of [['Guarded', 'tab'], ['Plain guarded', 'radio']]) {
          const group = page.getByRole(role === 'tab' ? 'tablist' : 'radiogroup', { name: label, exact: true });
          for (const modifier of ['altKey', 'ctrlKey', 'metaKey', 'shiftKey']) {
            const untouched = await group.getByRole(role, { name: 'one', exact: true }).evaluate((element, modifier) => {
              const event = new KeyboardEvent('keydown', { key: 'ArrowRight', [modifier]: true, bubbles: true, cancelable: true });
              return element.dispatchEvent(event);
            }, modifier);
            assert.equal(untouched, true, `${label} ${modifier}: must not prevent default`);
            await page.getByRole('button', { name: 'Accept', exact: true }).evaluate(element => element.click());
            assert.equal(await group.getByRole(role, { name: 'one', exact: true }).getAttribute(role === 'tab' ? 'aria-selected' : 'aria-checked'), 'true');
          }
          scenarios++;
        }
      }
      if (run('plugins')) {
        await page.goto(`${base}/tests/fixtures/plugins.html`);
        await page.locator('.plugin-card').first().waitFor();
        const storeRequests = () => page.evaluate(() => window.pluginsFixture.requests.filter(request => request.path === '/plugins/store').length);
        const initialRequests = await storeRequests();
        await page.locator('#plugin-tab-installed').focus();await page.keyboard.press('ArrowRight');
        await page.waitForFunction(() => document.activeElement?.id === 'plugin-tab-store');
        await page.waitForTimeout(200);
        assert.equal(await storeRequests(), initialRequests, 'arrows must not load the plugin store');
        assert.equal(await page.locator('#plugin-tab-installed').getAttribute('aria-selected'), 'true');
        await page.keyboard.press('Enter');
        await page.waitForFunction(() => window.pluginsFixture.requests.some(request => request.path === '/plugins/store'));
        assert.equal(await page.locator('#plugin-tab-store').getAttribute('aria-selected'), 'true');scenarios++;
      }
      if (run('panel')) {
        await page.goto(`${base}/tests/fixtures/plugins.html`);
        await page.locator('.plugin-card').first().waitFor();
        const box = await page.locator('#plugin-tab-panel').evaluate(element => { const { width, height } = element.getBoundingClientRect(); return { width, height }; });
        assert.ok(box && box.width > 0 && box.height > 0, 'the plugin panel must have a real layout box');scenarios++;
      }
      if (process.env.CHOICE_CASE) continue;
      await page.goto(`${base}/tests/fixtures/choice-controls.html`);
      const tabs = page.getByRole('tablist', { name: 'Guarded', exact: true });
      const radios = page.getByRole('radiogroup', { name: 'Delayed' });
      const focused = locator => locator.evaluate(element => element === document.activeElement);
      const acceptWithoutFocus = () => page.getByRole('button', { name: 'Accept', exact: true }).evaluate(element => element.click());
      await tabs.getByRole('tab', { name: 'one', exact: true }).focus();
      await page.keyboard.press('ArrowRight');
      assert.equal(await tabs.getByRole('tab', { name: 'one', exact: true }).getAttribute('aria-selected'), 'true');
      assert.equal(await focused(tabs.getByRole('tab', { name: 'one', exact: true })), true);
      await page.getByRole('button', { name: 'Reject', exact: true }).evaluate(element => element.click());
      assert.equal(await tabs.getByRole('tab', { name: 'two', exact: true }).getAttribute('aria-selected'), 'false');
      scenarios++;
      await page.keyboard.press('ArrowRight');
      await acceptWithoutFocus();
      await page.waitForFunction(() => document.activeElement?.id === 'fixture-two');
      assert.equal(await tabs.getByRole('tab', { name: 'two', exact: true }).getAttribute('aria-selected'), 'true');
      for (const [key, value] of [['End', 'three'], ['ArrowRight', 'one'], ['ArrowLeft', 'three'], ['Home', 'one']]) {
        await page.keyboard.press(key);
        await acceptWithoutFocus();
        await page.waitForFunction(id => document.activeElement?.id === id, `fixture-${value}`);
        scenarios++;
      }
      await radios.getByRole('radio', { name: 'one', exact: true }).focus();
      await page.keyboard.press('ArrowDown');
      await page.getByRole('button', { name: 'Reject', exact: true }).evaluate(element => element.click());
      await page.waitForFunction(() => document.querySelector('[aria-label="Delayed"] [aria-checked="true"]') === document.activeElement);
      await page.keyboard.press('ArrowDown');
      scenarios++;
      assert.equal(await radios.getByRole('radio', { name: 'one', exact: true }).getAttribute('aria-checked'), 'true');
      await acceptWithoutFocus();
      await page.waitForFunction(() => document.querySelector('[aria-label="Delayed"] [aria-checked="true"]') === document.activeElement);
      assert.equal(await radios.getByRole('radio', { name: 'two', exact: true }).getAttribute('aria-checked'), 'true');
      scenarios++;
      for (const [key, value] of [['End', 'three'], ['ArrowDown', 'one'], ['ArrowUp', 'three'], ['Home', 'one']]) {
        await page.keyboard.press(key);
        await acceptWithoutFocus();
        await page.waitForFunction(value => document.activeElement?.getAttribute('data-value') === value, value);
        scenarios++;
      }
      await tabs.evaluate(element => { element.style.direction = 'rtl'; });
      await tabs.getByRole('tab', { name: 'one', exact: true }).focus();
      await page.keyboard.press('ArrowLeft');
      await acceptWithoutFocus();
      await page.waitForFunction(() => document.activeElement?.id === 'fixture-two');
      scenarios++;
      const guarded = page.getByRole('tablist', { name: 'Dialog guarded' });
      await guarded.getByRole('tab', { name: 'one', exact: true }).focus();
      await page.keyboard.press('ArrowRight');
      await page.getByRole('dialog', { name: 'Navigation guard' }).waitFor();
      assert.equal(await guarded.getByRole('tab', { name: 'one', exact: true }).getAttribute('aria-selected'), 'true');
      await page.getByRole('button', { name: 'Confirm navigation' }).click();
      await page.waitForFunction(() => document.activeElement?.id === 'dialog-two');
      scenarios++;
      await page.getByRole('button', { name: 'Remove OpenAI' }).click();
      const home = page.locator('.home-protocol-tabs');
      assert.equal(await home.getByRole('tab', { name: 'claude', exact: true }).getAttribute('aria-selected'), 'true');
      await home.getByRole('tab', { name: 'claude', exact: true }).focus();
      await page.keyboard.press('ArrowRight');
      await page.waitForFunction(() => document.activeElement?.id === 'home-protocol-gemini');
      assert.equal(await page.locator('#home-protocol-panel').getAttribute('aria-labelledby'), 'home-protocol-gemini');
      scenarios++;
    } finally { await browser.close(); }
  }
  console.log(`Choice controls: ${scenarios} scenarios passed across Chromium and WebKit`);
})().catch(error => { console.error(error); process.exit(1); });
