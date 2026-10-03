// Run Vite on port 1421, then node tests/agent-client-list-ui.cjs.
// PLAYWRIGHT_MODULE and AGENT_CLIENT_TEST_URL can target an existing local setup.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const base = process.env.AGENT_CLIENT_TEST_URL || 'http://127.0.0.1:1421';
const storageKey = 'cpa-gui.agent-visible-clients.v1';
const automaticClients = ['Claude Code', 'Claude Desktop', 'Codex', 'OpenCode', 'Pi'];
const screenshots = path.join(os.tmpdir(), 'easycliproxy-agent-clients');

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true, args: ['--no-proxy-server'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1460, height: 1060 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(base + '/') ? route.continue() : route.abort());
    fs.mkdirSync(screenshots, { recursive: true });

    const clients = () => page.locator('.agent-list-items button strong');
    const visibleClients = () => clients().allTextContents();
    const activeClient = () => page.locator('.agent-list-items button.active strong').textContent();
    const stored = () => page.evaluate(key => localStorage.getItem(key), storageKey);
    const manage = () => page.getByRole('button', { name: '管理客户端', exact: true });
    const dialog = () => page.getByRole('dialog', { name: '管理客户端', exact: true });
    const checkbox = name => dialog().getByRole('checkbox', { name, exact: true });
    const save = () => dialog().getByRole('button', { name: '保存', exact: true });
    const cancel = () => dialog().getByRole('button', { name: '取消', exact: true });
    const search = () => dialog().getByRole('searchbox', { name: '搜索客户端', exact: true });
    const automatic = () => dialog().getByRole('button', { name: '恢复自动显示', exact: true });
    const ready = () => page.waitForFunction(() => window.fixtureCalls?.some(call => call.cmd === 'get_agent_config_statuses')
      && document.querySelector('.agent-client-list-heading button')
      && !document.querySelector('.agent-client-list-heading button').disabled);
    const waitClients = async names => {
      await page.waitForFunction(expected => {
        const actual = Array.from(document.querySelectorAll('.agent-list-items button strong'), node => node.textContent);
        return JSON.stringify(actual) === JSON.stringify(expected);
      }, names);
      assert.deepEqual(await visibleClients(), names);
    };
    const open = async (extra = 'mixed-clients', saved = null) => {
      if (page.url() !== 'about:blank') {
        await page.evaluate(({ key, saved }) => {
          if (saved === null) localStorage.removeItem(key);
          else localStorage.setItem(key, saved);
        }, { key: storageKey, saved });
      }
      await page.goto(`${base}/tests/fixtures/agent-backups.html?reset-selections&client=codex&${extra}`, { waitUntil: 'domcontentloaded' });
      await ready();
      await page.evaluate(() => document.fonts.ready);
    };
    const refresh = async override => {
      await page.evaluate(value => { window.fixtureClientStatusesOverride = value; }, override);
      await page.locator('.agent-client-list-heading button').click();
      await ready();
    };
    const closeAndFocus = async close => {
      await close();
      await dialog().waitFor({ state: 'hidden' });
      assert.equal(await manage().evaluate(node => node === document.activeElement), true,
        'closing the client manager returns keyboard focus to its trigger');
    };
    const assertNoHorizontalOverflow = async label => {
      const metrics = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth }));
      assert.ok(metrics.scrollWidth <= metrics.width + 1, `${label}: the page must fit the viewport`);
      const shown = page.getByRole('dialog');
      if (await shown.count()) {
        const bounds = await shown.evaluate(node => {
          const rect = node.getBoundingClientRect();
          return { left: rect.left, right: rect.right, width: innerWidth, scrollWidth: node.scrollWidth, clientWidth: node.clientWidth };
        });
        assert.ok(bounds.left >= -1 && bounds.right <= bounds.width + 1, `${label}: the dialog must fit the viewport`);
        assert.ok(bounds.scrollWidth <= bounds.clientWidth + 1, `${label}: the dialog must not scroll horizontally`);
      }
    };

    // Installation, config-only and Pi plugin-only detection all keep clients accessible.
    await open();
    await waitClients(automaticClients);
    assert.equal(await activeClient(), 'Codex');
    assert.equal(await page.locator('.agent-list-items button.active').getAttribute('aria-pressed'), 'true');
    assert.equal(await stored(), null, 'automatic defaults do not become a custom preference');

    // Search reaches uninstalled clients; a cancelled draft never affects navigation or storage.
    await manage().click();
    assert.equal(await dialog().getByRole('checkbox').count(), 13);
    await search().fill('herMES');
    assert.equal(await dialog().getByRole('checkbox').count(), 1);
    await checkbox('Hermes Agent').check();
    await search().fill('no-such-client');
    assert.equal(await dialog().getByRole('checkbox').count(), 0);
    await search().fill('');
    assert.equal(await checkbox('Hermes Agent').isChecked(), true, 'search preserves pending choices');
    await closeAndFocus(() => cancel().click());
    await waitClients(automaticClients);
    assert.equal(await stored(), null);
    assert.equal(await activeClient(), 'Codex');
    await manage().click();
    assert.equal(await checkbox('Hermes Agent').isChecked(), false, 'cancel discards the draft');
    await checkbox('Hermes Agent').check();
    await closeAndFocus(() => page.keyboard.press('Escape'));
    assert.equal(await stored(), null, 'Escape also discards changes');

    // A keyboard user cannot escape the dialog, and cannot save an empty client list.
    await manage().click();
    for (let index = 0; index < 20; index++) {
      await page.keyboard.press('Tab');
      assert.equal(await dialog().evaluate(node => node.contains(document.activeElement)), true, 'Tab stays in the modal');
    }
    for (const control of await dialog().getByRole('checkbox').all()) {
      if (await control.isChecked() && await control.isEnabled()) await control.uncheck();
    }
    const remaining = await dialog().getByRole('checkbox', { checked: true }).count();
    assert.ok(remaining <= 1);
    if (remaining === 0) assert.equal(await save().isDisabled(), true, 'empty selection must disable Save');
    else assert.equal(await dialog().getByRole('checkbox', { checked: true }).isDisabled(), true,
      'if the last client cannot be removed, its checkbox must communicate that state');
    await checkbox('Hermes Agent').check();
    for (const name of automaticClients) if (await checkbox(name).isChecked()) await checkbox(name).uncheck();
    assert.equal(await checkbox('Hermes Agent').isChecked(), true);
    await closeAndFocus(() => save().click());
    await waitClients(['Hermes Agent']);
    assert.equal(await activeClient(), 'Hermes Agent', 'removing the active client selects the first remaining client');
    assert.deepEqual(JSON.parse(await stored()), ['hermes']);
    await page.reload();
    await ready();
    await waitClients(['Hermes Agent']);
    assert.equal(await activeClient(), 'Hermes Agent', 'saved visibility is restored on reload');

    // Detection updates automatic mode but never overrides a manually curated list.
    const override = { 'kimi-code': { installed: true, configExists: true, version: '2.0' } };
    await refresh(override);
    await waitClients(['Hermes Agent']);
    await manage().click();
    await automatic().click();
    assert.equal(await checkbox('Kimi Code').isChecked(), true);
    assert.equal(await checkbox('Claude Desktop').isChecked(), true);
    await closeAndFocus(() => cancel().click());
    assert.deepEqual(JSON.parse(await stored()), ['hermes'], 'restoring automatic visibility is still a cancellable draft');
    await manage().click();
    await automatic().click();
    await closeAndFocus(() => save().click());
    assert.equal(await stored(), null, 'saving automatic mode clears the custom preference');
    await waitClients([...automaticClients, 'Kimi Code', 'Hermes Agent']);
    await refresh({ ...override,
      'claude-code': { installed: false, configured: false, configExists: false },
      openclaw: { installed: true },
    });
    await waitClients(['Claude Desktop', 'Codex', 'OpenCode', 'Pi', 'Kimi Code', 'OpenClaw', 'Hermes Agent']);

    // Detection failure and a clean machine still offer a usable selected client and the full catalog.
    await open('not-installed&no-plugin&fresh');
    await waitClients(['Codex']);
    await manage().click();
    assert.equal(await dialog().getByRole('checkbox').count(), 13);
    await closeAndFocus(() => cancel().click());
    await open('mixed-clients&fail-detection');
    await page.getByText(/模拟客户端检测失败/).waitFor();
    await waitClients(['Codex']);
    await refresh({});
    await waitClients(automaticClients);

    await open('mixed-clients&shell');
    await waitClients(automaticClients);
    await page.screenshot({ path: path.join(screenshots, 'automatic-desktop.png'), fullPage: true });
    await manage().click();
    await dialog().waitFor();
    await page.screenshot({ path: path.join(screenshots, 'manager-desktop.png'), fullPage: true });
    await closeAndFocus(() => cancel().click());

    // Both entry points remain usable with narrow windows and English text in dark mode.
    for (const embedded of [false, true]) {
      for (const english of [false, true]) {
        await page.setViewportSize({ width: 360, height: 800 });
        await open(`mixed-clients&shell&${embedded ? 'embedded&' : ''}${english ? 'locale=en&theme=dark' : ''}`);
        await waitClients(automaticClients);
        const label = `${embedded ? 'embedded' : 'full'}-${english ? 'dark-en' : 'light-zh'}`;
        await assertNoHorizontalOverflow(label);
        const trigger = page.getByRole('button', { name: english ? 'Manage Clients' : '管理客户端', exact: true });
        await trigger.click();
        const modal = page.getByRole('dialog', { name: english ? 'Manage Clients' : '管理客户端', exact: true });
        await modal.waitFor();
        await assertNoHorizontalOverflow(`${label} manager`);
        const hermes = modal.getByRole('checkbox', { name: 'Hermes Agent', exact: true });
        await hermes.check();
        assert.equal(await hermes.isChecked(), true, `${label}: the last catalog entry is reachable`);
        await page.screenshot({ path: path.join(screenshots, `${label}-manager.png`), fullPage: true });
        await page.keyboard.press('Escape');
        await modal.waitFor({ state: 'hidden' });
        assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
      }
    }
    assert.deepEqual(errors, []);
    console.log('PASS: automatic detection, custom visibility, persistence, active selection, search, cancellation, empty selection, modal focus, retry, narrow layouts and dark English');
    console.log(`Screenshots: ${screenshots}`);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
