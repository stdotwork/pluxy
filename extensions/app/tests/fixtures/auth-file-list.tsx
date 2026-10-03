import React from 'react';
import { createRoot } from 'react-dom/client';
import { mockIPC } from '@tauri-apps/api/mocks';
import { I18nProvider } from '../../src/i18n';
import { AuthFileManagementPage } from '../../src/pages/AuthFileManagementPage';
import { updateQuotaCache } from '../../src/services/quotaCache';
import { quotaKey, type QuotaState } from '../../src/services/quotaService';
import '../../src/styles/index.css';

// Fictional credentials and local IPC only; this fixture never connects to an upstream service.
const params = new URLSearchParams(location.search);
localStorage.setItem('easy-cli-proxy-api.locale', params.get('locale') || 'zh-CN');
document.documentElement.dataset.theme = params.get('theme') || 'light';
const startedAt = Date.now();
type Request = { path: string; method: string; body?: Record<string, unknown>; query?: Record<string, string> };
const files: Record<string, unknown>[] = Array.from({ length: 12 }, (_, index) => ({
  name: `${String(index + 1).padStart(2, '0')}-account.json`,
  email: index === 0 ? 'long-fictional-account-for-layout@example.test' : `account-${index + 1}@example.test`,
  auth_index: `fixture-account-${index + 1}`, provider: index === 3 ? 'claude' : index === 4 ? 'xai' : 'codex',
  source: 'file', size: 1800 + index * 350, updated_at: '2026-10-01T00:00:00Z',
  disabled: index === 2 || index === 6, status: index === 2 || index === 6 ? 'disabled' : 'active',
  priority: index % 3, plan_type: index % 3 === 0 ? 'Pro 20x' : 'Team',
  note: index === 0 ? 'Fictional account note, visible after opening credential details.' : '',
  success: 1200 + index * 2450, failed: 3 + index * 97,
  recent_requests: Array.from({ length: 20 }, (_, bucket) => ({
    time: `${String(8 + Math.floor(bucket / 6)).padStart(2, '0')}:${String(bucket % 6 * 10).padStart(2, '0')}`,
    success: bucket === 19 ? 0 : (bucket + index) % 7 + 1,
    failed: bucket === 19 || (bucket + index) % 5 ? 0 : 2,
  })),
  cooldowns: index === 1 ? [{ scope: 'model', model_key: 'gpt-fictional-layout', reason: 'quota',
    retry_at: new Date(startedAt + 180000).toISOString(), remaining_seconds: 180, http_status: 429, backoff_level: 0 }] : [],
}));

updateQuotaCache(Object.fromEntries(files.map((file, index) => {
  const quota: QuotaState = {
    status: 'success', plan: String(file.plan_type),
    subscriptionActiveUntil: index === 0 ? '2026-11-01T00:00:00Z' : undefined,
    resetCredits: index === 0 ? 2 : undefined,
    resetCreditsApplicable: index === 0 ? 0 : undefined,
    rows: index === 4 ? [{ label: 'Paid API account', remainingPercent: null, detail: 'Upstream does not report remaining quota.' }]
      : [
        { label: '5h', remainingPercent: index === 1 ? 0 : index === 2 ? 8 : 72 - index * 3, resetAtMs: startedAt + 3 * 3600000 },
        { label: 'Weekly', remainingPercent: 83 - index * 5, resetAtMs: startedAt + 5 * 86400000 },
        ...(index === 3 ? [
          { label: 'Additional model A', remainingPercent: 34, resetAtMs: startedAt + 3600000 },
          { label: 'Additional model B', remainingPercent: null, detail: 'No quota limit supplied for this model.' },
        ] : []),
      ],
  };
  return [quotaKey(file), quota];
})));

const state = { files, requests: [] as Request[], unhandled: [] as string[], reads: 0 };
(window as typeof window & { authFileListFixture: typeof state }).authFileListFixture = state;
mockIPC(async (cmd, args) => {
  if (cmd === 'set_app_locale') return null;
  if (cmd !== 'management_request') {
    state.unhandled.push(cmd);
    throw new Error(`Unhandled fixture command: ${cmd}`);
  }
  const request = args?.request as Request;
  state.requests.push(structuredClone(request));
  if (request.path === '/credentials' && request.method === 'GET') {
    state.reads += 1;
    return { observed_at: new Date().toISOString(), files: structuredClone(state.files) };
  }
  if (request.path === '/credentials/status' && request.method === 'PATCH') {
    const target = state.files.find(file => file.name === request.body?.name);
    if (!target || typeof request.body?.disabled !== 'boolean') throw new Error('Fixture status update needs a known name and boolean');
    target.disabled = request.body.disabled;
    target.status = target.disabled ? 'disabled' : 'active';
    return { status: 'ok' };
  }
  if (request.path === '/credentials' && request.method === 'DELETE') {
    const index = state.files.findIndex(file => file.name === request.query?.name);
    if (index < 0) throw new Error('Fixture deletion requires a known filename');
    state.files.splice(index, 1);
    return { status: 'ok' };
  }
  state.unhandled.push(`${request.method} ${request.path}`);
  throw new Error(`Unhandled fixture request: ${request.method} ${request.path}`);
});

createRoot(document.getElementById('root')!).render(
  <I18nProvider><div className="app-shell">
    <aside className="sidebar" aria-hidden="true" />
    <div className="workspace"><main className="content"><AuthFileManagementPage /></main></div>
  </div></I18nProvider>,
);
