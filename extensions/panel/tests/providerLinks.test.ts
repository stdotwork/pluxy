import { describe, expect, test } from 'bun:test';
import { OAUTH_PROVIDERS } from '../src/features/oauth/providers';
import { getKimiPlatformUrl } from '../src/features/providers/kimi';
import { getSponsorProviderDefinition } from '../src/features/providers/sponsorDefinitions';

const links = (): string[] =>
  [
    ...OAUTH_PROVIDERS.flatMap((card) => (card.sponsor ? [card.sponsor.signUpUrl] : [])),
    getKimiPlatformUrl('zh-CN'),
    getKimiPlatformUrl('en'),
    getSponsorProviderDefinition('apikeyFun').registerUrl,
    getSponsorProviderDefinition('apikeyFun').dashboardUrl,
    getSponsorProviderDefinition('fennoAI').registerUrl,
    getSponsorProviderDefinition('qiniuCloud').registerUrl,
    getSponsorProviderDefinition('kimi').registerUrl,
  ].filter((url): url is string => typeof url === 'string');

describe('provider links', () => {
  test('are the providers own pages, with no referral parameters or short links', () => {
    expect(links()).toEqual([
      'https://platform.kimi.com/',
      'https://platform.kimi.ai/',
      'https://platform.kimi.com/',
      'https://platform.kimi.ai/',
      'https://apikey.fan/register',
      'https://apikey.fan/dashboard',
      'https://api.fenno.ai/register',
    ]);
    for (const url of links()) {
      expect(new URL(url).search).toBe('');
    }
  });

  test('Qiniu Cloud has no sign-up link', () => {
    expect(getSponsorProviderDefinition('qiniuCloud').registerUrl).toBeUndefined();
  });
});
