const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const base = process.env.AGENT_CLIENT_TEST_URL || 'http://127.0.0.1:1421';
(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
  try {
    const page = await browser.newPage();
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    const toggle = () => page.getByRole('switch', { name: '使用 openai 提供商标识', exact: true });
    const open = async (query = '') => {
      await page.goto(`${base}/tests/fixtures/agent-backups.html?reset-selections&client=codex&${query}`, { waitUntil: 'domcontentloaded' });
      await page.getByRole('tab', { name: '基础配置', exact: true }).click();
      assert.equal(await toggle().count(), 0);
      await page.getByRole('tab', { name: '配置管理', exact: true }).click();
      await toggle().waitFor();
      await page.waitForFunction(() => !document.querySelector('.agent-provider-identity input')?.disabled);
    };
    const confirm = () => page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
    const calls = () => page.evaluate(() => window.fixtureCalls);
    for (const width of [1280, 360]) {
      await page.setViewportSize({ width, height: 900 });
      await open();
      await toggle().click();
      assert.equal(await page.getByRole('checkbox', { name: '同时同步会话', exact: true }).isChecked(), false);
      await page.getByRole('dialog').getByRole('button', { name: '取消', exact: true }).click();
      assert.equal((await calls()).filter(c => c.cmd === 'set_codex_openai_provider').length, 0);
      await toggle().click();
      await confirm();
      await page.waitForFunction(() => document.querySelector('.agent-provider-identity input').checked);
      assert.equal((await calls()).filter(c => c.cmd === 'repair_codex_session_metadata').length, 0);
      await toggle().click();
      await page.getByRole('checkbox', { name: '同时同步会话', exact: true }).check();
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      fs.mkdirSync('misc', { recursive: true });
      await page.screenshot({ path: `misc/codex-provider-${width}.png`, fullPage: true });
      await confirm();
      await page.waitForFunction(() => !document.querySelector('.agent-provider-identity input').checked);
      const invoked = await calls();
      const sync = invoked.findIndex(c => c.cmd === 'repair_codex_session_metadata');
      assert.ok(sync > invoked.findIndex(c => c.cmd === 'set_codex_openai_provider'));
      assert.equal(invoked[sync].args.expectedProvider, 'cpa-gui');
    }
    await open('fail-provider-sync');
    await toggle().click();
    await page.getByRole('checkbox', { name: '同时同步会话', exact: true }).check();
    await confirm();
    await page.getByText(/提供商已切换，但会话同步失败/).waitFor();
    assert.equal(await toggle().isChecked(), true);
    await open('fail-provider');
    await toggle().click();
    await page.getByRole('checkbox', { name: '同时同步会话', exact: true }).check();
    await confirm();
    await page.getByText(/CPA 未接受当前 Codex OAuth 凭证/).waitFor();
    assert.equal(await toggle().isChecked(), false);
    assert.equal((await calls()).filter(c => c.cmd === 'repair_codex_session_metadata').length, 0);
    assert.deepEqual(errors, []);
    console.log('Codex provider switch UI checks passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
