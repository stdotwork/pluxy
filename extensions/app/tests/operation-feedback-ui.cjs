// Run Vite on port 1421, then node tests/operation-feedback-ui.cjs.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.FEEDBACK_TEST_BASE_URL || 'http://127.0.0.1:1421';
(async () => {
  const browser = await chromium.launch({ ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {}), headless: true, args: ['--no-proxy-server'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    page.setDefaultTimeout(10000);
    page.setDefaultNavigationTimeout(30000);
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    const open = query => page.goto(`${base}/tests/fixtures/operation-feedback.html?${query || ''}`, { waitUntil: 'domcontentloaded' });
    const stack = page.locator('.app-notice-stack');
    const notice = stack.locator('.inline-notice');
    const positions = () => page.locator('[data-testid="toolbar"], [data-testid="panel"], [data-testid="after"]').evaluateAll(nodes => nodes.map(node => {
      const r = node.getBoundingClientRect(); return [r.x, r.y, r.width, r.height];
    }));
    const button = name => page.getByRole('button', { name, exact: true });
    const waitNotices = count => page.waitForFunction(count => document.querySelectorAll('.app-notice-stack .inline-notice').length === count, count);
    await open();
    await page.clock.install();
    const initial = await positions();
    await button('Success').click(); await waitNotices(1);
    assert.deepEqual(await positions(), initial, 'Showing feedback does not move form or toolbar');
    assert.equal(await page.locator('[data-testid="panel"] .inline-notice').count(), 0, 'Escapes clipped/transformed ancestor');
    await button('Second').click(); await waitNotices(2);
    assert.equal(await stack.count(), 1, 'All owners share one stack');
    const rects = await notice.evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().toJSON()));
    assert.ok(rects[0].bottom <= rects[1].top, 'Simultaneous notices do not overlap');
    await notice.first().getByRole('button').click(); await waitNotices(1);
    assert.deepEqual(await positions(), initial, 'Closing feedback does not move page');
    await button('Clear').click(); await waitNotices(0);
    assert.equal(await stack.count(), 0, 'Unused host is cleaned up');

    await button('Success').click(); await waitNotices(1);
    await page.clock.fastForward(3500);
    await button('Success').click();
    await page.clock.fastForward(3500); await waitNotices(1);
    await page.clock.fastForward(3000); await waitNotices(0);
    await button('Success').click(); await waitNotices(1);
    await notice.locator('.action-feedback-text').focus();
    await page.clock.fastForward(7000); await waitNotices(1);
    await page.getByTestId('after').focus();
    await page.clock.fastForward(6100); await waitNotices(0);
    assert.equal(await page.getByTestId('after').evaluate(node => node === document.activeElement), true, 'Feedback does not steal focus');
    await button('Success').click(); await waitNotices(1);
    await notice.hover(); await page.clock.fastForward(7000); await waitNotices(1);
    await page.mouse.move(0, 0); await page.clock.fastForward(6100); await waitNotices(0);

    await button('Error').click(); await waitNotices(1);
    await page.clock.fastForward(30000); await waitNotices(1);
    assert.equal(await notice.getAttribute('role'), 'alert');
    assert.ok(await notice.locator('.action-feedback-text').evaluate(node => node.scrollHeight > node.clientHeight), 'Long errors retain scrollable full text');
    assert.deepEqual(await positions(), initial);
    await button('Toggle source').click(); await waitNotices(0);
    assert.equal(await stack.count(), 0, 'Unmounting owner removes its notices');
    await button('Toggle source').click(); await waitNotices(1);
    await button('Clear').click(); await waitNotices(0);
    await button('Agent result').click(); await waitNotices(1);
    assert.deepEqual(await positions(), initial, 'Agent pending and success feedback leave controls stable');
    await button('Quota result').click(); await waitNotices(2);
    const quotaText = await notice.last().innerText();
    for (const text of ['test.json', '重置请求已提交', '额度刷新失败', '避免重复消耗', 'query failed']) assert.ok(quotaText.includes(text));
    assert.deepEqual(await positions(), initial, 'Quota reset error does not resize its row');
    await button('Clear').click(); await waitNotices(0);

    await button('Second').click(); await waitNotices(1);
    await button('Tall dialog').click();
    const footer = page.getByTestId('dialog-footer');
    const dialogBefore = await footer.boundingBox();
    await button('Dialog error').click(); await waitNotices(2);
    assert.equal(await stack.evaluate(node => node.parentElement.matches('.app-dialog[role="dialog"][aria-modal="true"]')), true, 'Notifications move into the open modal and stay clickable');
    assert.deepEqual(await footer.boundingBox(), dialogBefore, 'Dialog error does not move dialog actions');
    const floated = await stack.boundingBox();
    assert.ok(floated.y + floated.height <= dialogBefore.y, 'Stack avoids dialog action buttons, allowing immediate retry');
    await notice.last().getByRole('button').click(); await waitNotices(1);
    await button('Close dialog').click();
    await page.waitForFunction(() => document.querySelector('.app-notice-stack')?.parentElement === document.body);
    await notice.getByRole('button').click(); await waitNotices(0);

    await button('Error').click(); await waitNotices(1);
    await button('Confirmation dialog').click();
    const confirmation = page.getByRole('alertdialog');
    await confirmation.locator('.app-notice-stack').waitFor();
    const confirmBefore = await confirmation.boundingBox();
    await button('Confirm').focus();
    await page.keyboard.press('Tab');
    const longText = notice.locator('.action-feedback-text');
    assert.equal(await longText.evaluate(node => node === document.activeElement), true, 'Capture-phase focus trap includes the notification text');
    await page.keyboard.press('End');
    await page.clock.fastForward(500);
    await page.waitForFunction(() => document.querySelector('.app-notice-stack .action-feedback-text')?.scrollTop > 0);
    await page.keyboard.press('Tab');
    assert.equal(await notice.getByRole('button').evaluate(node => node === document.activeElement), true);
    await page.keyboard.press('Tab');
    assert.equal(await confirmation.locator('button').first().evaluate(node => node === document.activeElement), true);
    await page.keyboard.press('Shift+Tab');
    await page.keyboard.press('Enter'); await waitNotices(0);
    assert.equal(await confirmation.count(), 1, 'Dismissing a notice does not activate confirmation');
    assert.deepEqual(await confirmation.boundingBox(), confirmBefore, 'Notifications do not resize ordinary modals');
    assert.equal(await confirmation.locator('button').first().evaluate(node => node === document.activeElement), true, 'Focus stays in the modal after notification dismissal');
    await button('Confirm').click();
    await confirmation.waitFor({ state: 'detached' });

    for (const theme of ['light', 'dark']) for (const width of [360, 640, 1280]) {
      await page.setViewportSize({ width, height: 700 }); await open(`theme=${theme}`);
      await button('Error').click(); await waitNotices(1);
      const r = await stack.boundingBox();
      assert.ok(r.x >= 0 && r.y >= 0 && r.x + r.width <= width && r.y + r.height <= 700, `${theme} ${width}: stack fits viewport`);
      assert.ok(Math.abs(width - (r.x + r.width) - 24) < 1 && Math.abs(700 - (r.y + r.height) - 24) < 1, 'Anchored at bottom right');
    }

    const evaluateSurface = () => notice.evaluate(node => {
      const style = getComputedStyle(node);
      const probe = document.createElement('span');
      probe.style.color = style.getPropertyValue('--notice-background');
      node.append(probe);
      const tint = getComputedStyle(probe).color;
      probe.style.color = `color-mix(in srgb, ${tint} 90%, transparent)`;
      const materialTint = getComputedStyle(probe).color;
      probe.remove();
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 1;
      const context = canvas.getContext('2d');
      const luminance = rgb => rgb.map(value => value / 255).map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
        .reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
      context.fillStyle = style.color; context.fillRect(0, 0, 1, 1);
      const text = luminance([...context.getImageData(0, 0, 1, 1).data].slice(0, 3));
      const contrasts = ['#000000', '#ffffff'].map(base => {
        context.fillStyle = base; context.fillRect(0, 0, 1, 1);
        context.fillStyle = style.backgroundColor; context.fillRect(0, 0, 1, 1);
        context.fillStyle = document.documentElement.dataset.windowMaterial ? materialTint : tint;
        context.fillRect(0, 0, 1, 1);
        const background = luminance([...context.getImageData(0, 0, 1, 1).data].slice(0, 3));
        return (Math.max(text, background) + 0.05) / (Math.min(text, background) + 0.05);
      });
      const alpha = color => color.startsWith('rgba') || color.startsWith('color(')
        ? Number(color.match(/(?:,|\/)\s*([\d.]+)\s*\)$/)?.[1] ?? 1) : 1;
      return { background: style.backgroundColor, image: style.backgroundImage,
        blur: style.backdropFilter, alpha: alpha(style.backgroundColor), tint, materialTint, contrast: Math.min(...contrasts) };
    });
    const surfaces = [];
    for (const theme of ['dark', 'light']) for (const material of [false, true]) for (const tone of ['Success', 'Error']) {
      await open(`theme=${theme}${material ? '&material=vibrancy' : ''}`);
      await button(tone).click(); await waitNotices(1);
      const surface = await evaluateSurface();
      const label = `${theme} ${tone} material=${material}`;
      if (material) {
        assert.ok(surface.alpha > 0.8 && surface.alpha < 1, `${label}: translucent overlay surface`);
        assert.equal(surface.blur, 'blur(4px)', `${label}: overlay blur`);
        assert.notEqual(surface.background, surface.tint, `${label}: surface is not bare tint`);
        assert.ok(surface.image.includes(surface.materialTint), `${label}: retains tone layer`);
      } else {
        assert.equal(surface.alpha, 1, `${label}: floating notice is opaque`);
        assert.ok(surface.image.includes(surface.tint), `${label}: retains original tint`);
      }
      assert.ok(surface.contrast >= 4.5, `${label}: readable text contrast ${surface.contrast}`);
      surfaces.push({ theme, material, tone, ...surface });
    }
    for (const themeId of ['graphite-dark', 'sakura-light']) for (const tone of ['Success', 'Error']) {
      await open(`themeId=${themeId}`);
      await button(tone).click(); await waitNotices(1);
      const surface = await evaluateSurface();
      const label = `${themeId} ${tone}`;
      assert.equal(surface.alpha, 1, `${label}: floating notice is opaque`);
      assert.ok(surface.image.includes(surface.tint), `${label}: retains original tint`);
      assert.ok(surface.contrast >= 4.5, `${label}: readable text contrast ${surface.contrast}`);
      surfaces.push({ themeId, tone, ...surface });
    }
    console.log('Floating notice surfaces:', JSON.stringify(surfaces));

    await page.setViewportSize({ width: 1280, height: 900 });
    await open('scenario=pricing');
    const table = page.locator('.usage-pricing-table'); await table.waitFor();
    const priceBefore = await table.boundingBox();
    const sync = page.locator('.usage-pricing-actions .primary-button');
    await sync.click();
    const priceDialog = page.locator('.usage-price-sync-dialog');
    await priceDialog.waitFor({ state: 'visible' });
    assert.equal(await notice.count(), 0, 'Preview does not save prices');
    await priceDialog.getByRole('button', { name: '保存所选 1 项' }).click(); await waitNotices(1);
    assert.ok((await notice.innerText()).includes('已保存 1 个模型价格'));
    assert.deepEqual(await table.boundingBox(), priceBefore, 'Real pricing success never shifts table');
    if (process.env.FEEDBACK_SCREENSHOT) await page.screenshot({ path: process.env.FEEDBACK_SCREENSHOT });
    await notice.getByRole('button').click(); await waitNotices(0);
    assert.deepEqual(await table.boundingBox(), priceBefore);
    await page.evaluate(() => { window.feedbackFixture.failSync = true; });
    await sync.click(); await waitNotices(1);
    assert.ok((await notice.innerText()).includes('price sync failed'));
    assert.deepEqual(await table.boundingBox(), priceBefore, 'Real pricing error never shifts table');
    assert.deepEqual(errors, []);
    console.log('PASS: no layout shifts, shared stack, owner cleanup, success expiry/repeat/hover/focus, persistent errors, quota details, native modal, light/dark at 3 widths, real pricing success/error.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
