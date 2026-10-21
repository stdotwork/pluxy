import { createRoot } from 'react-dom/client';
import { mockIPC } from '@tauri-apps/api/mocks';
import { I18nProvider } from '../../src/i18n';
import { QuotaPage } from '../../src/pages/QuotaPage';
import '../../src/styles/index.css';

localStorage.setItem('easy-cli-proxy-api.locale', 'zh-CN');
const calls: unknown[] = [];
Object.assign(window, { pluginQuotaCalls: calls });
mockIPC(async (command, args) => {
  if (command === 'set_app_locale') return null;
  if (command !== 'management_request') throw new Error(`Unexpected command: ${command}`);
  const request = args?.request as { path: string; body?: { auth_index?: string } };
  calls.push(request);
  if (request.path === '/credentials') return { files: [
    { name: 'gemini-plugin.json', provider: 'gemini', supports_quota: true, quota_plugin_id: 'gemini-quota', auth_index: 'one' },
    { name: 'gemini-legacy.json', provider: 'gemini', supports_quota: 'true', quota_provider: 'gemini', auth_index: 'two' },
    { name: 'ordinary-gemini.json', provider: 'gemini', auth_index: 'three' },
    { name: 'disabled-gemini.json', provider: 'gemini', supports_quota: true, quota_plugin_id: 'gemini-quota', disabled: true, auth_index: 'four' },
  ] };
  if (request.path === '/plugins') return { plugins: [{ id: 'gemini-quota', registered: true, supports_quota: true, quota_provider: 'gemini' }] };
  if (request.path === '/plugins/gemini-quota/quota') return {
    subscription: { plan: 'Pro' }, groups: [{ displayName: 'Gemini Models', buckets: [{ window: 'Daily', remainingFraction: 0.75 }] }],
    summary: [{ key: 'credits', label: 'Credits', value: 12, unit: 'credits' }],
  };
  throw new Error(`Unexpected request: ${request.path}`);
});
createRoot(document.getElementById('root')!).render(<I18nProvider><main className="content"><QuotaPage /></main></I18nProvider>);
