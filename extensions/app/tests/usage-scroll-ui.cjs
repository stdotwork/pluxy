const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

const base = 'http://127.0.0.1:1421';

function parseCsv(text) {
  const rows = [];
  let row = [];
  let field = '';
  let quoted = false;
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index];
    if (character === '"') {
      if (quoted && text[index + 1] === '"') { field += '"'; index += 1; }
      else quoted = !quoted;
    } else if (!quoted && (character === ',' || character === '\n')) {
      row.push(field);
      field = '';
      if (character === '\n') { rows.push(row); row = []; }
    } else if (quoted || character !== '\r') field += character;
  }
  if (field || row.length) { row.push(field); rows.push(row); }
  return rows;
}

(async () => {
  const browser = await chromium.launch({
    channel: 'msedge', headless: true, args: ['--no-proxy-server'],
    ignoreDefaultArgs: ['--hide-scrollbars'],
  });
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 700 } });
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', (route) => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
    await page.goto(`${base}/tests/fixtures/usage-layout.html?tab=events&locale=en`, { waitUntil: 'domcontentloaded' });
    await page.locator('.usage-table-top-scrollbar:not(.is-hidden)').waitFor();
    await page.locator('.usage-page-size-select').selectOption('200');
    await page.waitForFunction(() => document.querySelectorAll('.usage-events-table tbody tr').length === 200);

    assert.equal(await page.getByRole('heading', { name: 'Request Event Log' }).count(), 1);
    const firstRow = page.locator('.usage-events-table tbody tr').first();
    assert.equal(await firstRow.locator('.usage-td-total').getAttribute('title'), '1,200 tokens', 'The displayed total uses the recorded total instead of adding cache and reasoning again');
    assert.equal(await firstRow.locator('.tone-input').getAttribute('aria-label'), 'Input: 1000');
    assert.equal(await firstRow.locator('.tone-output').getAttribute('aria-label'), 'Output: 200');
    assert.equal(await firstRow.locator('.tone-reasoning').getAttribute('aria-label'), 'Reasoning: 100');
    assert.equal(parseFloat(await firstRow.locator('.usage-td-cache > strong').textContent()), 40);
    assert.equal(await firstRow.locator('.tone-cache-read').textContent(), '400');
    assert.equal(await firstRow.locator('.tone-cache-write').textContent(), '50');
    assert.equal(await firstRow.locator('.usage-td-time small').count(), 1, 'The request date remains visible below its time');
    assert.ok((await firstRow.locator('.usage-td-latency small').textContent()).includes('200'), 'The latency cell includes first-token latency');
    assert.equal(await page.locator('.usage-events-summary .usage-page-size-select').count(), 0);
    assert.equal(await page.locator('.usage-events-footer .usage-page-size-select').count(), 1, 'Pagination remains in the bottom footer');

    // Multiple input updates can arrive before the next animation frame. None
    // may be dropped, and queued programmatic scroll events must not echo back.
    const rapidScroll = await page.evaluate(async () => {
      const bar = document.querySelector('.usage-table-top-scrollbar');
      const table = document.querySelector('.usage-table-wrap');
      await new Promise(requestAnimationFrame);
      const positions = [80, 180, 320, 240, 420];
      const samples = positions.map((position) => {
        bar.scrollLeft = position;
        bar.dispatchEvent(new Event('scroll'));
        return { expected: position, actual: table.scrollLeft };
      });
      await new Promise(requestAnimationFrame);
      await new Promise(requestAnimationFrame);
      return { samples, bar: bar.scrollLeft, table: table.scrollLeft };
    });
    for (const { expected, actual } of rapidScroll.samples) {
      assert.equal(actual, expected, 'Every scrollbar position reaches the table without a frame lock');
    }
    assert.equal(rapidScroll.table, 420);
    assert.equal(rapidScroll.bar, 420);

    const reverseScroll = await page.evaluate(async () => {
      const bar = document.querySelector('.usage-table-top-scrollbar');
      const table = document.querySelector('.usage-table-wrap');
      const samples = [360, 200, 90].map((position) => {
        table.scrollLeft = position;
        table.dispatchEvent(new Event('scroll'));
        return { expected: position, actual: bar.scrollLeft };
      });
      // A vertical scroll / delayed echo must not overwrite a newer bar input.
      bar.scrollLeft = 300;
      table.scrollTop = 200;
      table.dispatchEvent(new Event('scroll'));
      bar.dispatchEvent(new Event('scroll'));
      await new Promise(requestAnimationFrame);
      await new Promise(requestAnimationFrame);
      return { samples, bar: bar.scrollLeft, table: table.scrollLeft };
    });
    for (const { expected, actual } of reverseScroll.samples) assert.equal(actual, expected);
    assert.equal(reverseScroll.bar, 300, 'Vertical scrolling cannot rewind pending horizontal input');
    assert.equal(reverseScroll.table, 300);

    await page.waitForTimeout(1200); // Exercise the existing one-second background refresh.
    assert.deepEqual(await page.evaluate(() => ({
      bar: document.querySelector('.usage-table-top-scrollbar').scrollLeft,
      table: document.querySelector('.usage-table-wrap').scrollLeft,
    })), { bar: 300, table: 300 }, 'Background refresh preserves the horizontal position');

    for (const width of [850, 1400, 1100]) {
      await page.setViewportSize({ width, height: 700 });
      await page.waitForFunction(() => {
        const bar = document.querySelector('.usage-table-top-scrollbar');
        const table = document.querySelector('.usage-table-wrap');
        return Math.abs(bar.scrollLeft - table.scrollLeft) < 1
          && Math.abs((bar.scrollWidth - bar.clientWidth) - (table.scrollWidth - table.clientWidth)) < 1;
      });
    }

    // Real pointer dragging covers the native thumb with a large request page.
    const bar = page.locator('.usage-table-top-scrollbar');
    await bar.evaluate((element) => { element.scrollLeft = 0; });
    await page.waitForFunction(() => document.querySelector('.usage-table-wrap').scrollLeft === 0);
    const box = await bar.boundingBox();
    await page.mouse.move(box.x + 60, box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + 300, box.y + box.height / 2, { steps: 30 });
    await page.mouse.up();
    if (process.env.SCREENSHOT_PATH) await page.screenshot({ path: process.env.SCREENSHOT_PATH });
    await page.waitForFunction(() => {
      const bar = document.querySelector('.usage-table-top-scrollbar');
      const table = document.querySelector('.usage-table-wrap');
      return bar.scrollLeft > 0 && Math.abs(bar.scrollLeft - table.scrollLeft) < 1;
    });

    await bar.evaluate((element) => { element.scrollLeft = element.scrollWidth; });
    await page.waitForFunction(() => {
      const table = document.querySelector('.usage-table-wrap');
      return table.scrollLeft === table.scrollWidth - table.clientWidth;
    });
    for (const delta of [-30, 90]) {
      const previous = await page.locator('.usage-table-wrap').evaluate((element) => ({
        left: element.scrollLeft, max: element.scrollWidth - element.clientWidth,
      }));
      const handle = await page.locator('.usage-th-latency .usage-col-resizer').boundingBox();
      // Use the part inside this sticky header; the next header overlaps its edge.
      await page.mouse.move(handle.x + 1, handle.y + handle.height / 2);
      await page.mouse.down();
      await page.mouse.move(handle.x + 1 + delta, handle.y + handle.height / 2, { steps: 15 });
      await page.mouse.up();
      await page.waitForFunction(({ oldMax, delta }) => {
        const bar = document.querySelector('.usage-table-top-scrollbar');
        const table = document.querySelector('.usage-table-wrap');
        const max = table.scrollWidth - table.clientWidth;
        return (delta > 0 ? max > oldMax : max < oldMax)
          && Math.abs(bar.scrollLeft - table.scrollLeft) < 1
          && Math.abs((bar.scrollWidth - bar.clientWidth) - max) < 1;
      }, { oldMax: previous.max, delta });
      const current = await page.locator('.usage-table-wrap').evaluate((element) => ({
        left: element.scrollLeft, max: element.scrollWidth - element.clientWidth,
      }));
      assert.equal(current.left, Math.min(previous.left, current.max), 'Column resizing only clamps at the new boundary');
    }

    await page.locator('.usage-col-settings-btn').click();
    const checkboxes = page.locator('.usage-column-option input');
    for (let index = 2; index < await checkboxes.count(); index += 1) await checkboxes.nth(index).uncheck();
    await page.locator('.usage-column-dialog-actions .primary-button').click();
    await page.waitForFunction(() => document.querySelector('.usage-table-top-scrollbar').classList.contains('is-hidden'));
    assert.equal(await page.locator('.usage-events-table th').count(), 2, 'Column visibility applies to the header and every request row');
    assert.equal(await firstRow.locator('td').count(), 2);
    assert.equal(await page.locator('.usage-table-wrap').evaluate((element) => element.scrollLeft), 0);

    // Export carries complete request data even when most display columns are hidden.
    const downloadPromise = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Export Page CSV' }).click();
    const download = await downloadPromise;
    assert.match(download.suggestedFilename(), /^usage-events-page-1-\d{4}-\d{2}-\d{2}\.csv$/);
    const stream = await download.createReadStream();
    const chunks = [];
    for await (const chunk of stream) chunks.push(chunk);
    const exported = parseCsv(Buffer.concat(chunks).toString('utf8').replace(/^\uFEFF/, ''));
    const [headers, ...records] = exported;
    assert.equal(records.length, 200, 'CSV contains exactly the current page');
    const values = Object.fromEntries(headers.map((header, index) => [header, records[0][index]]));
    assert.deepEqual(Object.fromEntries(['input_tokens', 'output_tokens', 'reasoning_tokens', 'cache_read_tokens', 'cache_creation_tokens', 'total_tokens'].map(key => [key, values[key]])), {
      input_tokens: '1000', output_tokens: '200', reasoning_tokens: '100', cache_read_tokens: '400', cache_creation_tokens: '50', total_tokens: '1200',
    });
    assert.equal(values.api_key_remark, 'Test key, "local"', 'CSV correctly escapes commas and quotes');
    assert.equal(values.endpoint, '/v1/responses');
    assert.equal(values.row_id, '1');
    assert.equal(records.at(-1)[headers.indexOf('row_id')], '200');

    await page.locator('.usage-col-settings-btn').click();
    await page.locator('.usage-column-select-all').click();
    await page.locator('.usage-column-dialog-actions .primary-button').click();
    await page.locator('.usage-table-top-scrollbar:not(.is-hidden)').waitFor();
    await bar.evaluate((element) => { element.scrollLeft = 250; });
    await page.waitForFunction(() => document.querySelector('.usage-table-wrap').scrollLeft === 250);

    const timeResize = page.locator('.usage-th-time .usage-col-resizer');
    await timeResize.focus();
    const beforeWidth = Number(await timeResize.getAttribute('aria-valuenow'));
    await page.keyboard.press('ArrowRight');
    assert.equal(Number(await timeResize.getAttribute('aria-valuenow')), beforeWidth + 10, 'Keyboard resizing remains available');
    await page.keyboard.press('Home');
    assert.equal(Number(await timeResize.getAttribute('aria-valuenow')), 84);

    await page.locator('.usage-page-size-select').selectOption('20');
    await page.waitForFunction(() => document.querySelector('.usage-pagination-info').textContent.trim() === '1 / 20');
    const originalTime = await firstRow.locator('.usage-td-time strong').textContent();
    await page.getByRole('button', { name: 'Next', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.usage-pagination-info').textContent.trim() === '2 / 20');
    assert.notEqual(await firstRow.locator('.usage-td-time strong').textContent(), originalTime, 'Bottom pagination loads a different request page');
    assert.equal(await page.locator('.usage-pagination-summary').textContent(), 'Showing 21 - 40 of 400');

    assert.equal(await page.getByRole('button', { name: 'Reset Filters', exact: true }).count(), 1, 'The request log keeps a clear filters control visible');
    assert.equal(await page.getByRole('button', { name: 'Reset Filters', exact: true }).isDisabled(), true, 'The clear filters control is disabled without active filters');
    await page.getByRole('combobox', { name: 'Model', exact: true }).selectOption('test-model');
    await page.waitForFunction(() => document.querySelector('.usage-pagination-summary').textContent === 'Showing 1 - 20 of 266');
    assert.equal(await page.locator('.usage-events-table tbody strong[title="test-model"]').count(), 20, 'Changing a filter resets pagination and shows matching models');
    await page.getByRole('combobox', { name: 'Source', exact: true }).selectOption('test-source');
    await page.waitForFunction(() => document.querySelector('.usage-pagination-summary').textContent === 'Showing 1 - 20 of 133');
    assert.ok((await page.locator('.usage-td-source').allTextContents()).every(text => text === 'Test source'));
    await page.getByRole('combobox', { name: 'Request result', exact: true }).selectOption('failed');
    await page.waitForFunction(() => document.querySelector('.usage-pagination-summary').textContent === 'Showing 1 - 20 of 66');
    assert.equal(await page.locator('.usage-events-table tbody .usage-result.failed').count(), 20);
    assert.equal(await page.getByRole('button', { name: 'Reset Filters', exact: true }).isDisabled(), false, 'Active filters enable the clear filters control');
    await page.getByRole('combobox', { name: 'Model', exact: true }).selectOption('');
    await page.getByRole('combobox', { name: 'Source', exact: true }).selectOption('');
    await page.getByRole('combobox', { name: 'Request result', exact: true }).selectOption('all');
    await page.waitForFunction(() => document.querySelector('.usage-pagination-summary').textContent === 'Showing 1 - 20 of 400');
    assert.equal(await page.getByRole('combobox', { name: 'Model', exact: true }).inputValue(), '');
    assert.equal(await page.getByRole('combobox', { name: 'Source', exact: true }).inputValue(), '');
    assert.equal(await page.getByRole('combobox', { name: 'Request result', exact: true }).inputValue(), 'all');
    assert.deepEqual(errors, [], 'Request log interactions do not produce runtime errors');

    console.log('PASS: request values, CSV export, filters, bottom pagination, column visibility/resizing, and synchronized horizontal scrolling with 200 rows.');
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
