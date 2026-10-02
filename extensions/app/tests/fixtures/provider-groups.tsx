import React from 'react';
import { createRoot } from 'react-dom/client';
import { mockIPC } from '@tauri-apps/api/mocks';
import { I18nProvider } from '../../src/i18n';
import {
  ApiAccessPage,
  apiAccessRemarkLocatorFromRecord,
  providerRemarkIdentity,
  type ApiAccessRemarkLocator,
  type ProviderSection,
} from '../../src/pages/ApiAccessPage';
import '../../src/styles/index.css';

const query = new URLSearchParams(location.search);
const denseLayout = query.get('layout') === 'dense';
localStorage.setItem('easy-cli-proxy-api.locale', query.get('locale') ?? 'en');
const fixture = window as typeof window & { groupFixture: { groups: Record<string, unknown>[], writes: unknown[], probes: unknown[] } };
fixture.groupFixture = {
  groups: denseLayout ? [
    {
      name: 'Mock Atlas primary', 'base-url': 'https://atlas.example.test/v1', priority: 10,
      models: [{ name: 'mock-codex-large' }, { name: 'mock-codex-small' }, { name: 'mock-codex-fast' }],
      keys: [{ 'api-key': 'mock-atlas-key-one' }, { 'api-key': 'mock-atlas-key-two' }, { 'api-key': 'mock-atlas-key-three' }],
    },
    {
      name: 'Mock Borealis partial',
      'base-url': 'https://borealis-layout-only.example.test/a-deliberately-long-routing-path/organization-fictional-team/workspace-for-responsive-layout-verification/openai-compatible/v1',
      priority: 0, models: [{ name: 'mock-codex-reasoning' }, { name: 'mock-codex-compact' }],
      keys: [{ 'api-key': 'mock-borealis-active' }, { 'api-key': 'mock-borealis-paused', 'excluded-models': ['*'] }],
    },
    {
      name: 'Mock Cirrus gateway with a deliberately long fictional group name',
      'base-url': 'https://cirrus.example.test/v1', priority: null,
      models: [{ name: 'mock-codex-standard' }], keys: [{ 'api-key': 'mock-cirrus-only-key' }],
    },
    {
      name: 'Mock Dusk disabled', 'base-url': 'https://dusk.example.test/v1', priority: 5,
      'excluded-models': ['*'], models: [],
      keys: [{ 'api-key': 'mock-dusk-key-one' }, { 'api-key': 'mock-dusk-key-two' }],
    },
  ] : [{ name: 'Primary gateway', 'base-url': 'https://gateway.example.test/v1', priority: 2,
    headers: { 'X-Group': 'shared' }, models: [{ name: 'gpt-original' }, { name: 'gpt-original', alias: 'gpt-fast' }],
    keys: [{ 'api-key': 'key-inherited', weight: 2, priority: null },
      { 'api-key': 'key-overridden', weight: 5, priority: 0, 'proxy-url': 'direct', headers: { 'X-Key': 'second' }, models: [{ name: 'private-model' }], 'excluded-models': [], 'disable-cooling': false }],
  }, { name: 'Backup gateway', 'base-url': 'https://backup.example.test/v1', models: [{ name: 'backup-model' }], keys: [{ 'api-key': 'backup-key' }] }],
  writes: [], probes: [],
};
const denseRemarks = [
  'Fictional layout fixture · 三个测试密钥',
  'One fictional key paused · 用于检查部分启用状态',
  '仅用于响应式布局测试的虚构备注：故意保留很长的说明，验证窄窗口中的名称、备注、URL、密钥数量和操作仍然清楚。 Fictional layout-only remark with enough text to exercise wrapping and truncation without real credentials or services.',
  'Disabled fictional group · 无已配置模型',
];
const remarks = new Map<string, string>();
if (denseLayout) fixture.groupFixture.groups.forEach((group, index) => {
  remarks.set(providerRemarkIdentity('codex-api-key', apiAccessRemarkLocatorFromRecord('codex-api-key', group)), denseRemarks[index]);
});
mockIPC((cmd, args: any) => {
  if (cmd === 'set_app_locale') return null;
  if (cmd === 'resolve_api_access_remarks') return args.queries.map((item: ApiAccessRemarkLocator & { providerSection: ProviderSection }) =>
    denseLayout ? remarks.get(providerRemarkIdentity(item.providerSection, item)) ?? '' : '');
  if (cmd === 'save_api_access_remark') {
    if (denseLayout) {
      const update = args.update as { providerSection: ProviderSection; previousRecords: ApiAccessRemarkLocator[]; records: ApiAccessRemarkLocator[]; remark: string };
      update.previousRecords.forEach(record => remarks.delete(providerRemarkIdentity(update.providerSection, record)));
      update.records.forEach(record => remarks.set(providerRemarkIdentity(update.providerSection, record), update.remark));
    }
    return null;
  }
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
