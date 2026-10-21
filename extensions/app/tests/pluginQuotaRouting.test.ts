import { describe, expect, it, spyOn } from 'bun:test';
import { quotaProviderForFile } from '../src/services/quotaService';
import { loadPluginCredentialQuota, resolveQuotaPluginId } from '../src/services/pluginCredentialQuota';
import { pluginsApi, type PluginListEntry } from '../src/services/plugins';
import { managementApi } from '../src/services/managementApi';

const plugin = (id: string, quotaProvider = '') => ({ id, quotaProvider, registered: true, supportsQuota: true } as PluginListEntry);

describe('quota routing parity', () => {
  it('includes advertised Gemini/plugin quotas but does not invent ordinary Gemini support', () => {
    for (const flag of [true, 'true', '1']) expect(quotaProviderForFile({ provider: 'gemini', supports_quota: flag })).toBe('plugin');
    expect(quotaProviderForFile({ provider: 'gemini' })).toBeNull();
    expect(quotaProviderForFile({ provider: 'gemini', supports_quota: false })).toBeNull();
    expect(quotaProviderForFile({ provider: 'codex', supports_quota: true, quota_plugin_id: 'override' })).toBe('codex');
  });
  it('matches quota identity before plugin ID, OAuth identity, and a sole fallback', () => {
    expect(resolveQuotaPluginId([plugin('gemini'), plugin('actual', 'Gemini')], 'gemini')).toBe('actual');
    expect(resolveQuotaPluginId([plugin('gemini'), plugin('other')], 'gemini')).toBe('gemini');
    expect(resolveQuotaPluginId([{ ...plugin('oauth'), oauthProvider: 'gemini' }, plugin('other')], 'gemini')).toBe('oauth');
    expect(resolveQuotaPluginId([plugin('only')], 'gemini')).toBe('only');
    expect(resolveQuotaPluginId([plugin('one'), plugin('two')], 'gemini')).toBeUndefined();
    expect(resolveQuotaPluginId([{ ...plugin('off'), registered: false }], 'gemini')).toBeUndefined();
  });
  it('uses authoritative plugin IDs without fetching a potentially stale plugin list', async () => {
    const list = spyOn(pluginsApi, 'list');
    const post = spyOn(managementApi, 'post').mockResolvedValue({ groups: [] });
    try {
      expect((await loadPluginCredentialQuota({ name: 'gemini.json', auth_index: '7', supports_quota: true, quota_plugin_id: 'actual/plugin' })).status).toBe('success');
      expect(list).not.toHaveBeenCalled();
      expect(post).toHaveBeenCalledWith('/plugins/actual%2Fplugin/quota', { auth_index: '7' });
    } finally { list.mockRestore(); post.mockRestore(); }
  });
  it('shares concurrent legacy lookups and refuses unsupported probe-only routes', async () => {
    const list = spyOn(pluginsApi, 'list').mockResolvedValue({ plugins: [plugin('gemini-quota', 'gemini')] } as Awaited<ReturnType<typeof pluginsApi.list>>);
    const post = spyOn(managementApi, 'post').mockResolvedValue({ groups: [] });
    try {
      const file = { provider: 'gemini', supports_quota: true, quota_provider: 'gemini', auth_index: '7' };
      const results = await Promise.all([loadPluginCredentialQuota(file), loadPluginCredentialQuota({ ...file, auth_index: '8' })]);
      expect(results.every(result => result.status === 'success')).toBe(true);
      expect(list).toHaveBeenCalledTimes(1);
      expect(post).toHaveBeenCalledWith('/plugins/gemini-quota/quota', { auth_index: '8' });
      expect((await loadPluginCredentialQuota({ supports_quota: true, auth_index: '9' })).status).toBe('error');
      expect(post).toHaveBeenCalledTimes(2);
    } finally { list.mockRestore(); post.mockRestore(); }
  });
});
