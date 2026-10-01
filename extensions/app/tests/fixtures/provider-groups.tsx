import React from 'react';
import { createRoot } from 'react-dom/client';
import { mockIPC } from '@tauri-apps/api/mocks';
import { I18nProvider } from '../../src/i18n';
import { ApiAccessPage } from '../../src/pages/ApiAccessPage';
import '../../src/styles/index.css';

localStorage.setItem('easy-cli-proxy-api.locale', new URLSearchParams(location.search).get('locale') ?? 'en');
const fixture = window as typeof window & { groupFixture: { groups: Record<string, unknown>[], writes: unknown[], probes: unknown[] } };
fixture.groupFixture = {
  groups: [{ name: 'Primary gateway', 'base-url': 'https://gateway.example.test/v1', priority: 2,
    headers: { 'X-Group': 'shared' }, models: [{ name: 'gpt-original' }, { name: 'gpt-original', alias: 'gpt-fast' }],
    keys: [{ 'api-key': 'key-inherited', weight: 2, priority: null },
      { 'api-key': 'key-overridden', weight: 5, priority: 0, 'proxy-url': 'direct', headers: { 'X-Key': 'second' }, models: [{ name: 'private-model' }], 'excluded-models': [], 'disable-cooling': false }],
  }, { name: 'Backup gateway', 'base-url': 'https://backup.example.test/v1', models: [{ name: 'backup-model' }], keys: [{ 'api-key': 'backup-key' }] }],
  writes: [], probes: [],
};
mockIPC((cmd, args: any) => {
  if (cmd === 'set_app_locale' || cmd === 'save_api_access_remark') return null;
  if (cmd === 'resolve_api_access_remarks') return args.queries.map(() => '');
  if (cmd !== 'management_request') throw new Error(`Unexpected ${cmd}`);
  const request = args.request;
  if (request.path === '/requests/api-call') {
    fixture.groupFixture.probes.push(request.body);
    return { status_code: 200, body: { data: [{ id: 'gpt-original' }, { id: 'new-discovered' }] } };
  }
  if (request.method === 'GET') return request.path === '/config/api-keys/codex' ? structuredClone(fixture.groupFixture.groups) : [];
  if (request.path !== '/config/api-keys/codex' || request.method !== 'PUT') throw new Error('Unexpected mutation');
  fixture.groupFixture.writes.push(structuredClone(request.body));
  fixture.groupFixture.groups = structuredClone(request.body);
  return { status: 'ok' };
});
createRoot(document.getElementById('root')!).render(<I18nProvider><div className="app-shell"><aside className="sidebar" /><div className="workspace"><main className="content"><ApiAccessPage /></main></div></div></I18nProvider>);
