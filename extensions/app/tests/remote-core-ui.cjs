const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');

(async () => {
  const { createServer } = await import('vite');
  const react = (await import('@vitejs/plugin-react')).default;
  const server = await createServer({ configFile: false, root: path.resolve(__dirname, '..'), plugins: [react()],
    logLevel: 'error', server: { host: '127.0.0.1', port: 0, watch: null } });
  let browser;
  try {
    await server.listen();
    const base = `http://127.0.0.1:${server.httpServer.address().port}`;
    browser = await chromium.launch({ channel: process.platform === 'win32' ? 'msedge' : undefined, headless: true, args: ['--no-proxy-server'] });
    const page = await (await browser.newContext({ viewport: { width: 1505, height: 1000 } })).newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    await page.route('**/*', route => route.request().url().startsWith(`${base}/`) ? route.continue() : route.abort());
    await page.addInitScript(() => localStorage.setItem('easy-cli-proxy-api.locale', 'en'));
    await page.goto(`${base}/?mock=stopped&mockPluginPages=2`);
    const nav = name => page.locator('.nav-section').getByRole('button', { name, exact: true });
    const savedConnection = async () => JSON.parse(await page.evaluate(() => sessionStorage.getItem('browser-mock.core-connection')));
    const openDialog = async () => {
      await page.getByRole('button', { name: 'Core connection settings', exact: true }).click();
      return page.getByRole('dialog', { name: 'Core connection' });
    };

    // Home: the gear beside Refresh opens the dialog; Escape discards the draft.
    let dialog = await openDialog();
    assert.equal(await dialog.getByRole('radio', { name: 'Local core' }).getAttribute('aria-checked'), 'true');
    assert.equal(await dialog.getByRole('button', { name: 'Use local core' }).isDisabled(), true, 'unchanged Local cannot be saved');
    await dialog.getByRole('radio', { name: 'Remote core' }).click();
    const save = dialog.getByRole('button', { name: 'Save & connect', exact: true });
    const testButton = dialog.getByRole('button', { name: 'Test connection', exact: true });
    assert.equal(await save.isDisabled(), true, 'an empty Remote draft cannot be saved');
    await dialog.getByLabel('Management URL').fill('core.example/v8');
    await dialog.getByLabel('Management key', { exact: true }).fill('secret');
    await dialog.getByText(/Enter only the scheme, host and port/).waitFor();
    assert.equal(await save.isDisabled(), true, 'an invalid URL cannot be saved');
    assert.equal(await testButton.isDisabled(), true, 'an invalid URL cannot be tested');
    await dialog.getByLabel('Management URL').fill('http://10.0.0.5:8317');
    await dialog.getByText(/HTTP is unencrypted/).waitFor();
    await dialog.getByRole('button', { name: 'Show key' }).click();
    assert.equal(await dialog.getByLabel('Management key', { exact: true }).getAttribute('type'), 'text', 'the key can be revealed');
    await testButton.click();
    await dialog.getByText('Connection failed').waitFor();
    await dialog.getByText('Browser Mock: no remote core').waitFor();
    assert.equal(await savedConnection(), null, 'Test must not save');
    await dialog.getByLabel('Management URL').fill('http://10.0.0.6:8317');
    assert.equal(await dialog.getByText('Connection failed').count(), 0, 'editing clears the stale result');
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'detached' });
    dialog = await openDialog();
    assert.equal(await dialog.getByRole('radio', { name: 'Local core' }).getAttribute('aria-checked'), 'true', 'Escape discarded the draft');
    assert.equal(await dialog.getByLabel('Management URL').count(), 0);
    await dialog.getByRole('button', { name: 'Close', exact: true }).first().click();

    // Advanced Features hosts the same component and reads the same saved state.
    await nav('Advanced Features').click();
    await page.locator('#config-subpage-tab-software').click();
    const card = page.locator('#config-core-connection');
    await card.waitFor({ state: 'visible' });
    assert.equal(await card.getByRole('radio', { name: 'Local core' }).getAttribute('aria-checked'), 'true');
    assert.equal(await page.locator('#config-native-software').getByText('Silent Launch').count() > 0, true, 'Silent Launch shown locally');

    // Saving from the Home dialog reloads into Remote mode.
    await nav('Home').click();
    dialog = await openDialog();
    await dialog.getByRole('radio', { name: 'Remote core' }).click();
    await dialog.getByLabel('Management URL').fill('https://other.example');
    await dialog.getByLabel('Management key', { exact: true }).fill('secret');
    await Promise.all([page.waitForEvent('load'), dialog.getByRole('button', { name: 'Save & connect', exact: true }).click()]);
    await page.locator('.app-shell').waitFor();
    assert.equal((await savedConnection()).managementOrigin, 'https://other.example');

    // Remote Home keeps Local's layout: overview, Model Health, the same Runtime Control grid and Connect to CPA.
    await page.locator('[data-stat="credentials"] .home-stat-value').filter({ hasText: /^\d+$/ }).waitFor();
    assert.equal(await page.locator('[data-stat="usage"] .home-stat-value').textContent(), '—', 'local usage is never shown as the remote core\'s');
    assert.equal(await page.getByRole('button', { name: 'Model Health' }).isEnabled(), true);
    const runtime = page.locator('.control-panel');
    await runtime.getByText('Connected', { exact: true }).waitFor();
    await runtime.getByText('other.example', { exact: true }).waitFor();
    assert.equal(await runtime.locator('.panel-detail-row').count(), 6, 'same grid as Local');
    assert.equal(await runtime.getByText('443', { exact: true }).count(), 1, 'port comes from the remote origin');
    for (const name of ['Stop', 'Restart']) {
      const button = runtime.getByRole('button', { name, exact: true });
      assert.equal(await button.isDisabled(), true, `${name} shown but disabled remotely`);
      assert.equal(await button.getAttribute('title'), 'Managed on the remote host');
    }
    await page.locator('.home-access-panel').getByText('https://other.example/v1').waitFor();
    await page.getByRole('button', { name: 'Core connection settings', exact: true }).click();
    dialog = page.getByRole('dialog', { name: 'Core connection' });
    assert.equal(await dialog.getByRole('radio', { name: 'Remote core' }).getAttribute('aria-checked'), 'true');
    assert.equal(await dialog.getByLabel('Management URL').inputValue(), 'https://other.example');
    assert.equal(await dialog.getByRole('button', { name: 'Save & connect', exact: true }).isDisabled(), true, 'unchanged Remote cannot be saved');
    await page.keyboard.press('Escape');

    await page.waitForFunction(() => document.querySelector('.sidebar-easy-entry')?.disabled);
    for (const name of ['Agent Configuration', 'Usage', 'Version Management']) {
      assert.equal(await nav(name).isDisabled(), true, `${name} locked remotely`);
      assert.equal(await nav(name).getAttribute('title'), 'Not available in Remote mode yet');
    }
    await page.locator('#nav-plugin-children').waitFor();

    // Advanced Features keeps every category and search; core values are read-only.
    await nav('Advanced Features').click();
    await page.getByText(/Showing the remote core's current settings/).waitFor();
    for (const id of ['general', 'aliases', 'routing', 'requests', 'oauth', 'diagnostics', 'software']) assert.equal(await page.locator(`#config-subpage-tab-${id}`).isEnabled(), true, `${id} category available remotely`);
    const keys = page.locator('#config-native-keys');
    await keys.getByRole('button', { name: /Copy/ }).first().waitFor();
    assert.equal(await keys.getByRole('button', { name: 'Add Authentication Key' }).isDisabled(), true, 'remote keys are read-only');
    assert.equal(await keys.getByRole('button', { name: /Copy/ }).first().isEnabled(), true, 'remote keys can be copied');
    await page.getByRole('searchbox').fill('retry');
    await page.locator('.config-search-result-list').waitFor();
    await page.getByRole('searchbox').fill('');
    await page.locator('#config-subpage-tab-software').click();
    const software = page.locator('#config-native-software');
    await software.waitFor({ state: 'visible' });
    assert.equal(await software.getByRole('switch', { name: 'Start Core on Launch' }).isDisabled(), true, 'start core on launch shown but disabled');
    assert.equal(await software.getByText('Silent Launch').count(), 0, 'Silent Launch hidden remotely');
    assert.equal(await card.getByLabel('Management URL').inputValue(), 'https://other.example', 'both hosts show the saved connection');

    await nav('Plugins').click();
    await page.getByRole('region', { name: 'Plugins' }).waitFor();
    const install = page.getByRole('button', { name: 'Install local plugin' });
    assert.equal(await install.isDisabled(), true, 'local plugin install shown but disabled remotely');
    assert.equal(await install.getAttribute('title'), 'Not available in Remote mode yet');
    assert.deepEqual(errors, []);
    console.log('PASS: one connection component on Home and in Advanced Features; Remote keeps the Local layout with remote values, read-only settings and in-place disabled local actions; Silent Launch hidden.');
  } finally { await browser?.close(); await server.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
