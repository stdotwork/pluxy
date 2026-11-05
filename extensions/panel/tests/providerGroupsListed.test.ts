import { describe, expect, test } from 'bun:test';
import {
  buildProviderGroups,
  listProviderGroups,
} from '../src/features/providers/useProviderWorkbench';
import type { Config } from '../src/types';

const ids = (config: Config) => listProviderGroups(buildProviderGroups(config)).map((g) => g.id);

describe('listed provider groups', () => {
  test('hides the APIKEY.FUN preset group when the config has no providers for it', () => {
    expect(ids({})).not.toContain('apikeyFun');
  });

  test('lists the APIKEY.FUN group with its providers when the config has them', () => {
    const config: Config = {
      claudeApiKeys: [{ apiKey: 'test-key', baseUrl: 'https://api.apikey.fan' }],
    };
    const groups = listProviderGroups(buildProviderGroups(config));

    expect(groups.find((g) => g.id === 'apikeyFun')?.resources).toHaveLength(1);
    // The same provider is not listed a second time in the generic Claude group.
    expect(groups.find((g) => g.id === 'claude')?.resources).toHaveLength(0);
  });

  test('keeps the other preset groups listed while they are empty', () => {
    for (const brand of ['fennoAI', 'qiniuCloud', 'kimi']) {
      expect(ids({})).toContain(brand);
    }
  });
});
