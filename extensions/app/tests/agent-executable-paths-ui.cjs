// Run Vite on port 1421, then node tests/agent-executable-paths-ui.cjs.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(String(error)));
    for (const client of ['codex', 'opencode', 'deepseek-harness', 'claude-desktop', 'zcode', 'workbuddy']) {
      const dual = ['codex', 'opencode', 'deepseek-harness'].includes(client);
      await page.goto(`http://localhost:1421/tests/fixtures/agent-backups.html?reset-selections&client=${client}`);
      await page.getByRole('tab', { name: '配置管理', exact: true }).click();
      const rows = page.locator('.agent-management-row');
      const desktopRow = rows.filter({ has: page.getByRole('heading', { name: dual ? '桌面程序路径' : '手动指定 Agent 程序路径', exact: true }) });
      const desktopPath = `C:/Custom Apps/${client}/Desktop.exe`;
      await page.evaluate(path => { window.fixtureSelectedProgram = path; }, desktopPath);
      await desktopRow.getByRole('button', { name: '选择程序文件', exact: true }).click();
      await desktopRow.getByText(desktopPath, { exact: true }).waitFor();
      if (dual) {
        const cliPath = `C:/Custom CLI/${client}.cmd`;
        await page.evaluate(path => { window.fixtureSelectedProgram = path; }, cliPath);
        await rows.filter({ has: page.getByRole('heading', { name: '命令行程序路径', exact: true }) })
          .getByRole('button', { name: '选择程序文件', exact: true }).click();
        await page.getByText(cliPath, { exact: true }).waitFor();
      }
      await page.reload();
      await page.waitForFunction(() => window.fixtureCalls.some(call => call.cmd === 'get_agent_models'));
      const detection = await page.evaluate(() => window.fixtureCalls.find(call => call.cmd === 'get_agent_config_statuses').args.executableOverrides);
      assert.equal(detection[dual ? `${client}:app` : client], desktopPath);
      if (dual) assert.equal(detection[client], `C:/Custom CLI/${client}.cmd`);
      // The fixture normally exposes only Harness Web; enable its desktop target for this transport check.
      if (client === 'deepseek-harness') {
        await page.evaluate(() => { window.fixtureClientStatusesOverride = { 'deepseek-harness': { launchTargets: [{ id: 'app', label: 'APP', detail: 'desktop' }] } }; });
        await page.locator('.agent-client-list-heading button').click();
      }
      const launch = page.locator('.agent-launch-actions button').filter({ hasText: dual ? '启动 App' : '启动 ' }).first();
      await launch.click();
      await page.waitForFunction(() => window.fixtureCalls.some(call => call.cmd === 'launch_agent'));
      const args = await page.evaluate(() => window.fixtureCalls.find(call => call.cmd === 'launch_agent').args);
      assert.equal(args.target, 'app');
      assert.equal(args[dual ? 'desktopExecutablePath' : 'executablePath'], desktopPath);
      if (client !== 'deepseek-harness') {
        await page.getByRole('button', { name: '重启 App', exact: true }).click();
        await page.waitForFunction(() => window.fixtureCalls.some(call => call.cmd === 'restart_agent_app'));
        const restart = await page.evaluate(() => window.fixtureCalls.find(call => call.cmd === 'restart_agent_app').args);
        assert.deepEqual(restart, { client, executablePath: desktopPath });
      }
      await page.getByRole('tab', { name: '配置管理', exact: true }).click();
      await desktopRow.getByRole('button', { name: '清除手动路径', exact: true }).click();
      const saved = await page.evaluate(() => JSON.parse(localStorage.getItem('cpa-gui.agent-executable-paths.v1')));
      assert.equal(saved[dual ? `${client}:app` : client], undefined);
      if (dual) assert.equal(saved[client], `C:/Custom CLI/${client}.cmd`);
    }
    assert.deepEqual(errors, []);
    console.log('PASS: six desktop clients, independent CLI paths, persistence, launch, restart and clear');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
