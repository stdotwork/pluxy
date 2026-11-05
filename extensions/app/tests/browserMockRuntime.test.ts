import { describe, expect, test } from 'bun:test';
import {
  createBrowserMockRuntime,
  resolveBrowserMockOptions,
} from '../src/mocks/browserMockRuntime';

describe('browser mock options', () => {
  test('defaults to a running core and accepts stored scenarios', () => {
    expect(resolveBrowserMockOptions('', null)).toEqual({ mode: 'running', delayMs: 0 });
    expect(resolveBrowserMockOptions('', 'stopped')).toEqual({ mode: 'stopped', delayMs: 0 });
  });

  test('URL options override storage and clamp delay', () => {
    expect(resolveBrowserMockOptions('?mock=empty&mockDelay=9000', 'running'))
      .toEqual({ mode: 'empty', delayMs: 5000 });
    expect(resolveBrowserMockOptions('?mock=off&mockDelay=25', 'running'))
      .toEqual({ mode: 'off', delayMs: 25 });
  });
});

describe('browser mock runtime', () => {
  test('persists tags in downloads while omitting them from the core list response', async () => {
    const runtime = createBrowserMockRuntime('running');
    const request = (method: string, path: string, extra = {}) => runtime.invoke('management_request', { request: { method, path, ...extra } });
    await request('PATCH', '/credentials/fields', { body: { name: 'codex-personal.json', tags: [' team ', '', 'team', 'production', null] } });
    expect(await request('GET', '/credentials/download', { query: { name: 'codex-personal.json' } }))
      .toMatchObject({ tags: ['team', 'production'] });
    const listed = await request('GET', '/credentials') as { files: Array<{ name: string; tags?: string[] }> };
    expect(listed.files.find(file => file.name === 'codex-personal.json')).not.toHaveProperty('tags');
    await request('PATCH', '/credentials/fields', { body: { name: 'codex-personal.json', note: 'unchanged tags' } });
    expect(await request('GET', '/credentials/download', { query: { name: 'codex-personal.json' } }))
      .toMatchObject({ tags: ['team', 'production'], note: 'unchanged tags' });
    await request('PATCH', '/credentials/fields', { body: { name: 'codex-personal.json', tags: [] } });
    expect(await request('GET', '/credentials/download', { query: { name: 'codex-personal.json' } })).toMatchObject({ tags: [] });
  });

  test('saves, preserves and clears the Codex approval model', async () => {
    const runtime = createBrowserMockRuntime('running');
    const save = (request: Record<string, unknown>) => runtime.invoke('save_codex_model_catalog_editor', { request });
    expect(await save({ models: [], review_model: { model: 'custom-review-model' } }))
      .toMatchObject({ snapshot: { defaultAutoReviewModel: 'custom-review-model' }, synchronizationError: null });
    await save({ models: [] });
    expect(await runtime.invoke('get_codex_model_catalog_editor'))
      .toMatchObject({ defaultAutoReviewModel: 'custom-review-model' });
    expect(await save({ models: [], review_model: { model: null } }))
      .toMatchObject({ snapshot: { defaultAutoReviewModel: null } });
  });

  test('offers fast and standard request samples in the first usage rows', async () => {
    const runtime = createBrowserMockRuntime('running');
    const page = await runtime.invoke('get_usage_events', { query: { page_size: 3 } }) as {
      items: Array<{ reasoning_effort: string; service_tier: string }>;
    };
    expect(page.items.map(({ reasoning_effort, service_tier }) => [reasoning_effort, service_tier]))
      .toEqual([['medium', 'priority'], ['medium', 'fast'], ['medium', 'default']]);
  });

  test('offers a long failed request for the expanded error preview', async () => {
    const page = await createBrowserMockRuntime('running').invoke('get_usage_events', { query: { page_size: 6 } }) as {
      items: Array<{ failed: boolean; failure_body: string }>;
    };
    expect(page.items[5].failed).toBe(true);
    expect(page.items[5].failure_body.length).toBeGreaterThan(300);
  });

  test('empty scenario exposes empty credentials, providers and usage after installing the core', async () => {
    const runtime = createBrowserMockRuntime('empty');
    await runtime.invoke('install_bundled_core');
    await runtime.invoke('start_core_process');
    expect(await runtime.invoke('management_request', { request: { method: 'GET', path: '/credentials' } })).toMatchObject({ files: [] });
    const providers = await runtime.invoke('management_request', { request: { method: 'GET', path: '/config/api-keys' } }) as Record<string, unknown[]>;
    expect(Object.values(providers).every(groups => groups.length === 0)).toBe(true);
    expect(await runtime.invoke('get_usage_overview')).toMatchObject({ totalRequests: 0, totalTokens: 0, timeline: [] });
    expect(await runtime.invoke('get_usage_events')).toMatchObject({ items: [], total: 0 });
    expect(await runtime.invoke('get_usage_analysis')).toEqual({ models: [], providers: [], sources: [], apiKeys: [] });
    expect(await runtime.invoke('get_usage_pricing')).toMatchObject({ rows: [], totalCost: 0 });
  });

  test('filters usage before pagination, including boolean and date filters', async () => {
    const runtime = createBrowserMockRuntime('running');
    const first = await runtime.invoke('get_usage_events', { query: { failed: true, page_size: 1 } }) as { items: Array<Record<string, unknown>>; total: number; totalPages: number };
    expect(first).toMatchObject({ total: 2, totalPages: 2 });
    expect(first.items).toHaveLength(1);
    expect(first.items[0].failed).toBe(true);
    const next = await runtime.invoke('get_usage_events', { query: { failed: true, page_size: 1, page: 2 } }) as typeof first;
    expect(next.items[0].id).not.toBe(first.items[0].id);
    const exact = await runtime.invoke('get_usage_events', { query: {
      start: first.items[0].timestamp, end: first.items[0].timestamp,
      provider: first.items[0].provider, model: first.items[0].model, source: first.items[0].source,
      api_key_hash: first.items[0].api_key_hash, failed: true, canceled: false,
    } }) as typeof first;
    expect(exact.items).toEqual(first.items);
    expect(await runtime.invoke('get_usage_events', { query: { model: 'absent-model' } })).toMatchObject({ items: [], total: 0 });
    expect(await runtime.invoke('get_usage_events', { query: { canceled: true } })).toMatchObject({ total: 1 });
  });

  test('unknown routes fail explicitly and canceled OAuth sessions cannot create credentials', async () => {
    const runtime = createBrowserMockRuntime('running');
    await expect(runtime.invoke('management_request', { request: { method: 'GET', path: '/missing-route' } })).rejects.toThrow('not implemented');
    await expect(runtime.invoke('management_request', { request: { method: 'POST', path: '/requests/api-call', body: { url: 'https://example.test/missing', method: 'GET' } } })).rejects.toThrow('not implemented');
    const session = await runtime.invoke('start_oauth_login', { provider: 'kimi' }) as { state: string };
    await runtime.invoke('management_request', { request: { method: 'DELETE', path: '/oauth/session', query: { state: session.state } } });
    expect(await runtime.invoke('get_oauth_status', { state: session.state })).toMatchObject({ status: 'error' });
    const credentials = await runtime.invoke('management_request', { request: { method: 'GET', path: '/credentials' } }) as { files: Array<{ name: string }> };
    expect(credentials.files.some(file => file.name === 'kimi-mock-login.json')).toBe(false);
  });

  test('latency simulation also works outside a browser', async () => {
    expect(await createBrowserMockRuntime('running', undefined, 1).invoke('get_core_status')).toMatchObject({ ready: true });
  });

  test('lists selectable core versions for downgrade support', async () => {
    const runtime = createBrowserMockRuntime('running');
    const versions = await runtime.invoke('list_core_versions') as Array<{ version: string; assetName: string }>;
    expect(versions.map((item) => item.version)).toEqual(['7.3.16', '7.3.15', '7.3.14']);
    expect(versions[1].assetName).toBe('CLIProxyAPI_7.3.15_windows_amd64.zip');
  });

  for (const scenario of ['running', 'empty'] as const) {
    test(`installs the bundled version independently of the latest release (${scenario})`, async () => {
      const runtime = createBrowserMockRuntime(scenario);
      const bundled = await runtime.invoke('detect_bundled_core') as { version: string; assetName: string };
      const latest = await runtime.invoke('check_latest_core') as { version: string };
      expect(bundled.version).not.toBe(latest.version);

      if (scenario === 'running') {
        await runtime.invoke('install_core_version', { version: latest.version });
      }
      expect(await runtime.invoke('install_bundled_core')).toMatchObject(bundled);
      expect(await runtime.invoke('get_core_status')).toMatchObject({
        installed: true,
        currentVersion: bundled.version,
        running: scenario === 'running',
      });
    });
  }

  test('models the core process lifecycle and emits status events', async () => {
    const events: Array<{ event: string; payload: unknown }> = [];
    const runtime = createBrowserMockRuntime('stopped', (event, payload) => {
      events.push({ event, payload });
    });

    expect(await runtime.invoke('get_core_status')).toMatchObject({
      installed: true,
      running: false,
      ready: false,
    });
    expect(await runtime.invoke('start_core_process')).toMatchObject({
      running: true,
      ready: true,
      processId: 42817,
    });
    expect(events.at(-1)).toMatchObject({
      event: 'core-status-changed',
      payload: { running: true, ready: true },
    });
  });

  test('persists config and management mutations in memory', async () => {
    const runtime = createBrowserMockRuntime('running');

    await runtime.invoke('add_core_api_key', { apiKey: 'sk-test', remark: 'test' });
    expect(await runtime.invoke('get_core_config_settings')).toMatchObject({
      apiKeys: expect.arrayContaining([{ apiKey: 'sk-test', remark: 'test' }]),
    });

    await runtime.invoke('management_request', {
      request: {
        method: 'PUT',
        path: '/config/api-keys/gemini',
        body: [{ name: 'test', keys: [{ 'api-key': 'AIza-test' }], models: [{ name: 'gemini-test' }] }],
      },
    });
    expect(await runtime.invoke('management_request', {
      request: { method: 'GET', path: '/config/api-keys/gemini' },
    })).toEqual([{ name: 'test', keys: [{ 'api-key': 'AIza-test' }], models: [{ name: 'gemini-test' }] }]);
  });

  test('uses normalized ratios for percentage-based usage metrics', async () => {
    const runtime = createBrowserMockRuntime('running');
    expect(await runtime.invoke('get_usage_overview')).toMatchObject({
      successRate: 96.47,
      cacheHitRate: 0.386,
    });
  });

  test('provides an explicit error scenario', async () => {
    const runtime = createBrowserMockRuntime('error');
    await expect(runtime.invoke('get_core_status')).rejects.toThrow('Browser Mock error scenario');
    expect(await runtime.invoke('plugin:app|version')).toBe('0.2.97-mock');
  });
});

describe('browser mock native provider groups', () => {
  const providers = ['gemini', 'interactions', 'vertex', 'codex', 'claude', 'xai', 'meta', 'openai-compatibility'] as const;
  const request = (runtime: ReturnType<typeof createBrowserMockRuntime>, method: string, path: string, body?: unknown) =>
    runtime.invoke('management_request', { request: { method, path, body } });

  for (const provider of providers) {
    test(`preserves native ${provider} groups, overrides, and extensions across saves`, async () => {
      const runtime = createBrowserMockRuntime('running');
      const path = `/config/api-keys/${provider}`;
      const groups = [{
        name: `${provider} production`,
        'base-url': 'https://provider.example/v1',
        headers: { 'X-Team': 'shared' },
        models: [{ name: 'model', alias: 'primary', 'future-model-option': false }],
        'request-retry': 0,
        'future-group-option': { enabled: false, values: [] },
        keys: [
          { 'api-key': 'first', weight: 2, headers: null, models: [], 'future-key-option': 0 },
          { 'api-key': 'second', 'proxy-url': '', headers: {}, models: null },
        ],
      }, { name: `${provider} empty standby`, keys: [], 'future-group-option': null }];

      expect(await request(runtime, 'GET', path)).toBeArray();
      expect(await request(runtime, 'PUT', path, groups)).toEqual({ status: 'ok', 'config-version': 8 });
      expect(await request(runtime, 'GET', path)).toEqual(groups);
      expect(await request(runtime, 'GET', '/config')).toMatchObject({
        'config-version': 8, 'api-keys': { [provider]: groups },
      });

      // Reading the legacy view must not flatten the authoritative state.
      const legacyPath = provider === 'openai-compatibility' ? path.slice('/config/api-keys'.length) : `/${provider}-api-key`;
      await request(runtime, 'GET', legacyPath);
      expect(await request(runtime, 'GET', path)).toEqual(groups);

      // Both writes and reads isolate stored values from caller mutations.
      const expected = structuredClone(groups);
      groups[0].keys[0]['api-key'] = 'mutated request';
      const readBack = await request(runtime, 'GET', path) as typeof groups;
      readBack[0].keys[0]['api-key'] = 'mutated response';
      expect(await request(runtime, 'GET', path)).toEqual(expected);

      await request(runtime, 'PUT', path, []);
      expect(await request(runtime, 'GET', path)).toEqual([]);
    });
  }

  test('returns native groups without flattening inherited and explicit empty values', async () => {
    const runtime = createBrowserMockRuntime('running');
    const groups = [{
      name: 'preserve this group name',
      'base-url': 'https://provider.example',
      priority: 5,
      headers: { 'X-Team': 'shared' },
      models: [{ name: 'shared-model' }],
      keys: [
        { 'api-key': 'first', priority: 0, headers: null, models: [] },
        { 'api-key': 'second', headers: {}, models: null },
      ],
    }];
    await request(runtime, 'PUT', '/config/api-keys/interactions', groups);
    expect(await request(runtime, 'GET', '/config/api-keys/interactions')).toEqual(groups);
  });

  test('updates native OpenAI groups while preserving metadata and keys', async () => {
    const runtime = createBrowserMockRuntime('running');
    const group = {
      name: 'custom group', 'base-url': 'https://provider.example/v1', disabled: false,
      'future-option': { enabled: false },
      keys: [{ 'api-key': 'first', weight: 0, 'proxy-url': '' }, { 'api-key': 'second', weight: 3 }],
    };
    await request(runtime, 'PUT', '/config/api-keys/openai-compatibility', [group]);
    await request(runtime, 'PUT', '/config/api-keys/openai-compatibility', [{ ...group, disabled: true }]);
    expect(await request(runtime, 'GET', '/config/api-keys/openai-compatibility')).toEqual([{ ...group, disabled: true }]);
    await request(runtime, 'PUT', '/config/api-keys/openai-compatibility', [{ ...group, keys: [group.keys[1]] }]);
    expect(await request(runtime, 'GET', '/config/api-keys/openai-compatibility')).toEqual([{ ...group, keys: [group.keys[1]] }]);
  });
});

describe('browser mock MCP, skills, reverse proxy and ZCode contracts', () => {
  test('lists, saves, toggles, imports, exports and deletes MCP servers in memory', async () => {
    const runtime = createBrowserMockRuntime('running');
    const servers = async () => await runtime.invoke('list_mcp_servers') as Array<{ id: string; enabled: boolean }>;
    expect((await servers()).map(server => server.id)).toEqual(['filesystem', 'docs-search']);
    await runtime.invoke('save_mcp_server', { server: { id: '', name: 'Local Git', transport: 'stdio', command: 'uvx', args: ['mcp-server-git'], env: {}, url: null, headers: {}, enabled: true, description: null } });
    expect((await servers()).map(server => server.id)).toContain('local-git');
    await runtime.invoke('set_mcp_server_enabled', { id: 'docs-search', enabled: true });
    expect((await servers()).find(server => server.id === 'docs-search')?.enabled).toBe(true);
    expect(await runtime.invoke('import_mcp_servers_json', { raw: '{"mcpServers":{"Fetch":{"command":"uvx","args":["mcp-server-fetch"]},"Broken":{}}}' }))
      .toEqual({ added: 1, updated: 0, skipped: 1, errors: ['Broken: needs a command or a url'] });
    expect(JSON.parse(await runtime.invoke('export_mcp_servers_json') as string).mcpServers.Fetch).toEqual({ type: 'stdio', command: 'uvx', args: ['mcp-server-fetch'] });
    await runtime.invoke('delete_mcp_server', { id: 'local-git' });
    expect((await servers()).map(server => server.id)).not.toContain('local-git');
    await expect(runtime.invoke('delete_mcp_server', { id: 'local-git' })).rejects.toThrow("MCP server with id 'local-git' not found");
    await expect(runtime.invoke('import_mcp_servers_json', { raw: '{' })).rejects.toThrow('Failed to parse JSON');
  });

  test('lists skills, returns their body and toggles them by path', async () => {
    const runtime = createBrowserMockRuntime('running');
    const [first] = await runtime.invoke('list_skills') as Array<{ path: string; enabled: boolean }>;
    expect(await runtime.invoke('get_skill_detail', { path: first.path })).toMatchObject({ entry: { path: first.path }, body: expect.stringContaining('name: code-review') });
    await runtime.invoke('set_skill_enabled', { path: first.path, enabled: false });
    expect(((await runtime.invoke('list_skills')) as Array<{ enabled: boolean }>)[0].enabled).toBe(false);
    await expect(runtime.invoke('get_skill_detail', { path: 'missing/SKILL.md' })).rejects.toThrow('no such file');
    expect(await runtime.invoke('open_mcp_skills_directory')).toBeNull();
  });

  test('reverse proxy starts, applies and stops, and refuses a start without a certificate', async () => {
    const runtime = createBrowserMockRuntime('running');
    const config = { enabled: true, listenHost: '0.0.0.0', listenPort: 443, targetPort: 8317, certPath: '/tls/cert.pem', keyPath: '/tls/key.pem' };
    expect(await runtime.invoke('reverse_proxy_get_state')).toMatchObject({ running: false, listening: null });
    expect(await runtime.invoke('reverse_proxy_start', { config })).toMatchObject({ running: true, listening: '0.0.0.0:443', target: '127.0.0.1:8317' });
    expect(await runtime.invoke('reverse_proxy_apply', { config: { ...config, enabled: false } })).toMatchObject({ running: false, listening: null });
    await runtime.invoke('reverse_proxy_start', { config });
    expect(await runtime.invoke('reverse_proxy_stop')).toMatchObject({ running: false, error: null });
    await expect(runtime.invoke('reverse_proxy_start', { config: { ...config, keyPath: '' } })).rejects.toThrow('certificate and a private key');
    expect(await runtime.invoke('reverse_proxy_get_state')).toMatchObject({ running: false, error: 'A certificate and a private key are required' });
  });

  test('saves ZCode subagent and model catalog edits and lists update channels', async () => {
    const runtime = createBrowserMockRuntime('running');
    const subagents = await runtime.invoke('get_zcode_subagent_config') as { subagents: Array<Record<string, unknown>> };
    const saved = await runtime.invoke('save_zcode_subagent_config', { request: { defaultSubagentModel: 'gpt-5.2-codex', defaultThoughtLevel: null, subagents: [{ ...subagents.subagents[0], thoughtLevel: 'high' }] } });
    expect(saved).toMatchObject({ defaultSubagentModel: 'gpt-5.2-codex', defaultThoughtLevel: null, subagents: [{ name: 'explorer', thoughtLevel: 'high' }] });
    expect(await runtime.invoke('get_zcode_subagent_config')).toEqual(saved);

    const catalog = await runtime.invoke('get_zcode_model_catalog_editor') as { models: Array<{ id: string; configuration: Record<string, unknown> }> };
    const edited = { ...catalog.models[1], enabled: false, configuration: { ...catalog.models[1].configuration, maxOutputTokens: 4096, isManual: true } };
    expect(await runtime.invoke('save_zcode_model_catalog_editor', { request: { models: [edited], modelOrder: ['claude-sonnet-4-6', 'gpt-5.2-codex'] } }))
      .toMatchObject({ modelOrder: ['claude-sonnet-4-6', 'gpt-5.2-codex'], models: [{ id: 'gpt-5.2-codex', enabled: true }, { id: 'claude-sonnet-4-6', enabled: false, configuration: { maxOutputTokens: 4096, isManual: true } }] });

    await runtime.invoke('set_update_channel', { channel: 'preview-20260101' });
    expect(await runtime.invoke('list_update_channels')).toEqual(['stable', 'preview', 'preview-20260101']);
  });
});
