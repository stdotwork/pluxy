import { afterEach, describe, expect, it, spyOn } from 'bun:test';
import { managementApi } from '../src/services/managementApi';
import { parseKimiPlanName, parseKimiQuotaUrl } from '../src/services/kimiQuota';
import { loadQuota, providerForFile } from '../src/services/quotaService';
import { pruneQuotaCache, updateQuotaCache } from '../src/services/quotaCache';

const file = { name: 'kimi.json', provider: 'kimi', auth_index: '1' };
const com = 'https://api.kimi.com/coding/v1/usages';
const ai = 'https://api.kimi.ai/coding/v1/usages';
const usage = { usages: { limit_month_total: { used_ratio: 0.25 } } };
let get: ReturnType<typeof spyOn> | undefined;
let post: ReturnType<typeof spyOn> | undefined;
afterEach(() => { get?.mockRestore(); post?.mockRestore(); updateQuotaCache({}); });

describe('Kimi credential routing', () => {
  it('matches explicit domain, canonical base URL, type, provider and filename precedence', () => {
    for (const [credential, metadata, expected] of [
      [{ domain: 'com', base_url: 'https://api.kimi.ai', type: 'kimi-ai' }, file, com],
      [{ domain: 'unknown', type: 'kimi-ai' }, file, com],
      [{ domain: ' AI ', base_url: 'https://api.kimi.com' }, file, ai],
      [{ base_url: 'https://api.kimi.ai', type: 'kimi' }, file, ai],
      [{ 'base-url': 'https://api.kimi.ai' }, file, ai],
      [{ base_url: null, 'base-url': 'https://api.kimi.ai' }, file, com],
      [{ type: 'kimi-ai' }, file, ai],
      [{}, { ...file, provider: 'kimi_ai' }, ai],
      [{}, { name: 'kimi-ai-1.json' }, ai],
      [{ base_url: 'https://api.kimi.ai.attacker.test' }, file, com],
      [{}, file, com],
    ] as const) expect(parseKimiQuotaUrl(credential, metadata)).toBe(expected);
    expect(parseKimiQuotaUrl('{"domain":"ai"}', file)).toBe(ai);
    for (const value of ['secret invalid JSON', null, []]) {
      expect(() => parseKimiQuotaUrl(value, file)).toThrow('Invalid Kimi credential');
    }
  });

  it('recognizes international provider aliases', () => {
    for (const provider of ['kimi', 'kimi-ai', 'kimi_ai']) expect(providerForFile({ provider })).toBe('kimi');
  });

  it('downloads once and queries usages and optional profile on the resolved host', async () => {
    get = spyOn(managementApi, 'get').mockResolvedValue({ domain: 'ai' });
    post = spyOn(managementApi, 'post').mockImplementation(async (_path, body) => ({
      status_code: 200,
      body: JSON.stringify(String(body?.url).endsWith('/me')
        ? { user_id: 'user', user_level_name: '  Pro  ' } : usage),
    }));
    expect(await loadQuota(file)).toMatchObject({ status: 'success', plan: 'Pro', rows: [{ remainingPercent: 75 }] });
    expect(get.mock.calls).toEqual([['/credentials/download', { name: file.name }]]);
    expect(post.mock.calls.map(call => call[1]?.url)).toEqual([ai, ai.replace('/usages', '/me')]);
    expect(post.mock.calls[0][1]).toMatchObject({ authIndex: '1', method: 'GET', header: { Authorization: 'Bearer $TOKEN$' } });
    expect(post.mock.calls[1][2]).toEqual({ timeoutMs: 8000 });
  });

  it('keeps quota usable when the profile fails or has no verified plan', async () => {
    get = spyOn(managementApi, 'get').mockResolvedValue({});
    post = spyOn(managementApi, 'post').mockImplementation(async (_path, body) => {
      if (String(body?.url).endsWith('/me')) throw new Error('profile unavailable');
      return { status_code: 200, body: usage };
    });
    expect(await loadQuota(file)).toMatchObject({ status: 'success', plan: undefined });
    expect(parseKimiPlanName({ user_level_name: 'Pro' })).toBeUndefined();
    expect(parseKimiPlanName({ user_id: 'user', user_level_name: ' ' })).toBeUndefined();
  });

  it('fails safely without upstream requests for unreadable or runtime-only credentials', async () => {
    get = spyOn(managementApi, 'get').mockRejectedValue(new Error('credential secret'));
    post = spyOn(managementApi, 'post');
    const result = await loadQuota(file);
    expect(result.status).toBe('error');
    expect(result.error).not.toContain('secret');
    expect(post).not.toHaveBeenCalled();
    get.mockClear();
    expect((await loadQuota({ ...file, runtime_only: true })).status).toBe('error');
    expect(get).not.toHaveBeenCalled();
  });

  it('rejects invalidated requests after downloading credentials', async () => {
    updateQuotaCache({ removed: { status: 'idle', rows: [] } });
    get = spyOn(managementApi, 'get').mockImplementation(async () => {
      pruneQuotaCache(new Set());
      return {};
    });
    post = spyOn(managementApi, 'post');
    expect((await loadQuota(file)).status).toBe('error');
    expect(post).not.toHaveBeenCalled();
  });

  it('does not query the profile or report success when usage is unauthorized', async () => {
    get = spyOn(managementApi, 'get').mockResolvedValue('{}');
    post = spyOn(managementApi, 'post').mockResolvedValue({ status_code: 401, body: { error: 'expired' } });
    expect(await loadQuota(file)).toMatchObject({ status: 'error', rows: [], error: 'HTTP 401: expired' });
    expect(post).toHaveBeenCalledTimes(1);
    expect(post.mock.calls[0][1]?.url).toBe(com);
  });

  it('does not expose malformed downloaded credential contents', async () => {
    get = spyOn(managementApi, 'get').mockResolvedValue('{"access_token":"test-secret", invalid');
    post = spyOn(managementApi, 'post');
    const result = await loadQuota(file);
    expect(result.status).toBe('error');
    expect(result.error).not.toContain('test-secret');
    expect(post).not.toHaveBeenCalled();
  });

  it('rejects invalidation during optional profile enrichment', async () => {
    updateQuotaCache({ removed: { status: 'idle', rows: [] } });
    get = spyOn(managementApi, 'get').mockResolvedValue({});
    post = spyOn(managementApi, 'post').mockImplementation(async (_path, body) => {
      if (String(body?.url).endsWith('/me')) {
        pruneQuotaCache(new Set());
        throw new Error('profile unavailable');
      }
      return { status_code: 200, body: usage };
    });
    expect(await loadQuota(file)).toMatchObject({ status: 'error', rows: [] });
  });
});
