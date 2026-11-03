// Usage tables (Request Details, Price Statistics, Agent Usage) and panels (Price Statistics, Data Management) across window sizes, aspect ratios and pixel densities, on the browser mock.
// Wide tables scroll inside their panel and fade the edge that hides columns; the two small panels end with their content.
// Run with a Vite dev server at USAGE_TABLES_TEST_BASE, or let the script start one.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

// [width, height, deviceScaleFactor]: phones, tablets, laptops, desktops, ultrawide, portrait monitor and a short window.
const displays = [
  [320, 640, 2], [375, 812, 3], [414, 896, 2], [768, 1024, 2], [1024, 768, 1], [800, 600, 1.25], [1280, 800, 1.5],
  [1366, 768, 1], [1440, 900, 2], [1920, 1080, 1], [2560, 1440, 1], [3440, 1440, 1], [1080, 1920, 1], [1280, 420, 1],
];
// Playwright hides scrollbars by default, like an overlay-scrollbar system; a second pass shows classic ones, as on Windows.
const passes = [
  { name: 'overlay scrollbars', showScrollbars: false, displays: displays.filter(([width]) => [375, 1024, 1440].includes(width)) },
  { name: 'classic scrollbars', showScrollbars: true, displays },
];

const openUsage = async (page, base) => {
  await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
  await page.goto(`${base}/?mock=running`);
  await page.locator('.app-shell').waitFor();
  await page.locator('#browser-mock-toolbar').evaluate(element => { element.style.display = 'none'; });
  await page.locator('.nav-section button').filter({ has: page.getByText('Usage', { exact: true }) }).first().click();
};
const frames = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));

// Page-level problems and the horizontal scrollers of the current tab.
const inspect = page => page.evaluate(() => {
  const issues = [];
  for (const node of [document.documentElement, document.body, document.querySelector('.content'), document.querySelector('.content > .page')].filter(Boolean)) {
    if (node.scrollWidth > node.clientWidth + 1) issues.push(`page scrolls sideways (${node.className || node.tagName}: ${node.scrollWidth} > ${node.clientWidth})`);
  }
  const scrollers = [...document.querySelectorAll('.usage-table-wrap, .agent-usage-table-wrap')].filter(element => element.getBoundingClientRect().width > 0).map(element => {
    const style = getComputedStyle(element);
    const bar = element.offsetWidth - element.clientWidth - parseFloat(style.borderLeftWidth) - parseFloat(style.borderRightWidth);
    return { name: element.className.split(' ').filter(name => !name.startsWith('scroll-edge'))[0], overflowing: element.scrollWidth > element.clientWidth + 1,
      start: element.hasAttribute('data-scroll-start'), end: element.hasAttribute('data-scroll-end'),
      masked: getComputedStyle(element).maskImage !== 'none', bar, barVariable: element.style.getPropertyValue('--scroll-edge-bar') };
  });
  return { issues, scrollers };
});

// Every scroller that overflows fades its right edge at first, fades the left edge once scrolled, drops the right fade at the end, and
// reaches its last column. Scrollers that fit show no fade.
const checkScrollers = async (page, label, expected) => {
  const first = await inspect(page);
  assert.deepEqual(first.issues, [], label);
  assert.equal(first.scrollers.length, expected, `${label}: scrollers on the tab`);
  for (const [index, scroller] of first.scrollers.entries()) {
    const where = `${label} ${scroller.name} #${index}`;
    if (!scroller.overflowing) {
      assert.deepEqual([scroller.start, scroller.end, scroller.masked], [false, false, false], `${where}: fits, so no fade`);
      continue;
    }
    assert.deepEqual([scroller.start, scroller.end, scroller.masked], [false, true, true], `${where}: only the right edge fades at the start`);
    const handle = (await page.locator('.usage-table-wrap, .agent-usage-table-wrap').elementHandles())[index];
    await handle.evaluate(element => element.scrollTo({ left: element.scrollWidth, behavior: 'instant' }));
    await frames(page);
    const atEnd = await handle.evaluate(element => {
      const wrap = element.getBoundingClientRect();
      const lastHeader = [...element.querySelectorAll('thead th')].filter(cell => cell.getBoundingClientRect().width > 0).pop().getBoundingClientRect();
      return { start: element.hasAttribute('data-scroll-start'), end: element.hasAttribute('data-scroll-end'), masked: getComputedStyle(element).maskImage !== 'none',
        lastColumnReachable: lastHeader.right <= wrap.right + 1, remaining: element.scrollWidth - element.clientWidth - element.scrollLeft };
    });
    assert.deepEqual([atEnd.start, atEnd.end, atEnd.masked], [true, false, true], `${where}: only the left edge fades at the end`);
    assert.equal(scroller.barVariable, `${scroller.bar}px`, `${where}: the fade is told the vertical scrollbar's width`);
    assert.ok(atEnd.lastColumnReachable, `${where}: the last column is reachable`);
    await handle.evaluate(element => element.scrollTo({ left: 0, behavior: 'instant' }));
    await frames(page);
    const back = await handle.evaluate(element => [element.hasAttribute('data-scroll-start'), element.hasAttribute('data-scroll-end')]);
    assert.deepEqual(back, [false, true], `${where}: scrolling back restores the first state`);
    // Keyboard focus turns the fade off: a mask would clip the focus outline drawn outside the box.
    if (await handle.evaluate(element => element.tabIndex >= 0)) {
      // Reach it with Tab, as a keyboard user would (focus set from script is not :focus-visible).
      await handle.evaluate(element => { const before = [...document.querySelectorAll('button, [href], input, select, [tabindex]')].filter(node => node.tabIndex >= 0 && node.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING).pop(); before?.focus(); });
      for (let step = 0; step < 5 && !(await handle.evaluate(element => document.activeElement === element)); step++) await page.keyboard.press('Tab');
      await frames(page);
      const focused = await handle.evaluate(element => [element.matches(':focus-visible'), getComputedStyle(element).maskImage === 'none']);
      assert.deepEqual(focused, [true, true], `${where}: keyboard focus shows the focus ring unfaded`);
      await handle.evaluate(element => element.blur());
    }
  }
};

// Space between the end of a panel's content (its last element, or the table inside a scroller) and the panel's own bottom edge.
const emptyBelow = (page, selector) => page.evaluate(selector => {
  const panel = document.querySelector(selector);
  const shown = [...panel.children].filter(child => child.getBoundingClientRect().height > 0).pop();
  const last = shown.querySelector(':scope > table') ?? shown;
  return { gap: panel.getBoundingClientRect().bottom - last.getBoundingClientRect().bottom };
}, selector);

(async () => {
  let server;
  let base = process.env.USAGE_TABLES_TEST_BASE;
  try {
    if (!base) {
      const { createServer } = await import('vite');
      const react = (await import('@vitejs/plugin-react')).default;
      server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()], logLevel: 'error', server: { host: '127.0.0.1', port: 0 } });
      await server.listen();
      base = `http://127.0.0.1:${server.httpServer.address().port}`;
    }
    const channel = process.env.PLAYWRIGHT_CHANNEL || (process.platform === 'win32' ? 'msedge' : undefined);
    let runs = 0;
    for (const pass of passes) {
      const browser = await chromium.launch({
        channel, headless: true, timeout: Number(process.env.PLAYWRIGHT_LAUNCH_TIMEOUT) || undefined, args: ['--no-proxy-server'],
        ignoreDefaultArgs: pass.showScrollbars ? ['--hide-scrollbars'] : undefined,
      });
      try {
        for (const [width, height, deviceScaleFactor] of pass.displays) {
          const display = `${pass.name} ${width}x${height}@${deviceScaleFactor}x`;
          const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor });
          await context.addInitScript(() => localStorage.setItem('easy-cli-proxy-api.locale', 'en'));
          const page = await context.newPage();
          page.setDefaultTimeout(60000);
          const errors = [];
          page.on('pageerror', error => errors.push(String(error)));
          await openUsage(page, base);

          // Request Details
          await page.getByRole('tab', { name: 'Request Details', exact: true }).click();
          await page.locator('.usage-events-table tbody tr').first().waitFor();
          await frames(page);
          await checkScrollers(page, `${display} Request Details`, 1);
          if (width <= 414) {
            // Phones keep the first columns readable: the time and model headers sit inside the scroller, not cut off.
            const visible = await page.evaluate(() => {
              const wrap = document.querySelector('.usage-table-wrap').getBoundingClientRect();
              return [...document.querySelectorAll('.usage-events-table thead th')].slice(0, 2).map(cell => { const box = cell.getBoundingClientRect(); return box.left >= wrap.left - 1 && box.right <= wrap.right + 1; });
            });
            assert.deepEqual(visible, [true, true], `${display}: Time and Model columns fit on a phone`);
          }

          // Price Statistics
          await page.getByRole('tab', { name: 'Price Statistics', exact: true }).click();
          await page.locator('.usage-pricing-table tbody tr').first().waitFor();
          await frames(page);
          await checkScrollers(page, `${display} Price Statistics`, 1);
          const pricing = await emptyBelow(page, '.usage-pricing-panel');
          assert.ok(pricing.gap <= 40, `${display}: the price list panel ends with its table (${Math.round(pricing.gap)}px empty below)`);
          const toolbar = await page.evaluate(() => {
            const panel = document.querySelector('.usage-pricing-panel').getBoundingClientRect();
            return [...document.querySelectorAll('.usage-pricing-actions button, .usage-pricing-actions input, .usage-pricing-actions select, .usage-pricing-actions .select-control')]
              .filter(control => control.getBoundingClientRect().width > 0)
              .map(control => { const box = control.getBoundingClientRect(); return { name: control.textContent.trim() || control.tagName, inside: box.left >= panel.left - 1 && box.right <= panel.right + 1, width: box.width }; });
          });
          for (const control of toolbar) {
            assert.ok(control.inside, `${display}: toolbar control "${control.name}" stays inside the panel`);
            assert.ok(control.width >= 24, `${display}: toolbar control "${control.name}" keeps a usable width (${Math.round(control.width)}px)`);
          }
          const buttons = await page.evaluate(() => [...document.querySelectorAll('.usage-pricing-actions button')].map(button => button.getBoundingClientRect().width));
          for (const buttonWidth of buttons) assert.ok(buttonWidth < 200, `${display}: toolbar buttons are button-sized, not full width (${Math.round(buttonWidth)}px)`);
          await page.getByRole('button', { name: 'Add Manually', exact: true }).click();
          await page.locator('.usage-price-editor').waitFor();
          const editor = await page.evaluate(() => {
            const panel = document.querySelector('.usage-pricing-panel').getBoundingClientRect();
            return [...document.querySelectorAll('.usage-price-editor-actions button')].map(button => { const box = button.getBoundingClientRect(); return box.left >= panel.left - 1 && box.right <= panel.right + 1; });
          });
          assert.ok(editor.length > 0 && editor.every(Boolean), `${display}: the price editor's buttons stay inside the panel`);
          assert.deepEqual((await inspect(page)).issues, [], `${display}: price editor open`);

          // Agent Usage
          await page.getByRole('tab', { name: 'Agent Usage', exact: true }).click();
          await page.locator('.agent-usage-table-wrap').first().waitFor();
          const rankingTabs = page.locator('.agent-usage-ranking-tabs button');
          for (let tab = 0; tab < await rankingTabs.count(); tab++) {
            await rankingTabs.nth(tab).click();
            await page.locator('.agent-usage-table-wrap').first().waitFor();
            await frames(page);
            await checkScrollers(page, `${display} Agent Usage ${await rankingTabs.nth(tab).innerText()}`, 1);
          }

          // Data Management
          await page.getByRole('tab', { name: 'Data Management', exact: true }).click();
          await page.locator('.usage-data-management-panel').waitFor();
          await frames(page);
          const management = await emptyBelow(page, '.usage-data-management-panel');
          assert.ok(management.gap <= 40, `${display}: the data management panel ends with its content (${Math.round(management.gap)}px empty below)`);
          const gaps = await page.evaluate(() => [...document.querySelectorAll('.usage-data-management-action > div:first-child')].map(block => block.getBoundingClientRect().bottom - block.lastElementChild.getBoundingClientRect().bottom));
          for (const gap of gaps) assert.ok(gap <= 8, `${display}: a data management card's text is not stretched into a blank gap (${Math.round(gap)}px)`);
          assert.deepEqual((await inspect(page)).issues, [], `${display}: Data Management`);

          assert.deepEqual(errors, [], `${display}: no page errors`);
          await context.close();
          runs++;
        }
      } finally {
        await browser.close();
      }
    }
    console.log(`Usage tables: ${runs} display runs passed`);
  } finally {
    if (server) await server.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
