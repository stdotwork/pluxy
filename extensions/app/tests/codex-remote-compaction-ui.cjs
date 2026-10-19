const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.AGENT_CLIENT_TEST_URL || 'http://127.0.0.1:1421';
(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(15000);
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    const toggle = () => page.getByRole('checkbox', { name: '远程压缩（实验性）', exact: true });
    const open = async (query = '') => {
      await page.goto(`${base}/tests/fixtures/agent-backups.html?reset-selections&client=codex&${query}`);
      await page.getByRole('tab', { name: '基础配置', exact: true }).click();
      assert.equal(await toggle().count(), 0);
      await page.getByRole('tab', { name: '配置管理', exact: true }).click();
      await toggle().waitFor();
    };
    for (const width of [1280, 360]) {
      await page.setViewportSize({ width, height: 900 });
      await open();
      assert.equal(await toggle().isChecked(), false);
      await toggle().click();
      await page.waitForFunction(() => document.querySelector('.agent-remote-compaction input').checked && !document.querySelector('.agent-remote-compaction input').disabled);
      await page.getByRole('tab', { name: '基础配置', exact: true }).click();
      await page.getByRole('tab', { name: '配置管理', exact: true }).click();
      assert.equal(await toggle().isChecked(), true);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await toggle().click();
      await page.waitForFunction(() => !document.querySelector('.agent-remote-compaction input').checked && !document.querySelector('.agent-remote-compaction input').disabled);
      const calls = await page.evaluate(() => window.fixtureCalls.filter(c => c.cmd === 'set_codex_remote_compaction'));
      assert.deepEqual(calls.map(c => c.args.enabled), [true, false]);
      await page.getByRole('checkbox', { name: '使用 openai 提供商标识', exact: true }).click();
      await page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
      await page.waitForFunction(() => document.querySelector('.agent-remote-compaction input').disabled);
      assert.equal(await toggle().isChecked(), true);
    }
    await open('fail-compaction');
    await toggle().click();
    await page.getByText(/Compaction save failed/).waitFor();
    assert.equal(await toggle().isChecked(), false);
    console.log('Codex remote compaction UI checks passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
