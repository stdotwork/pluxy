import { describe, expect, test } from 'bun:test';
import i18n from '@/i18n';
import { buildKimiQuotaRows, quotaWindowLabel } from '@/utils/quota';

// Expected English wording is docs/terms.md ("Quota windows"): 5 h, 24 h, 7 d and 28-31 d get the
// named label whatever the provider; any other length is a whole `{n}-{unit} limit` phrase.
const LENGTHS: Array<[string, number, string]> = [
  ['5 hours', 18_000, '5-hour limit'],
  ['24 hours', 86_400, 'Daily limit'],
  ['7 days', 604_800, 'Weekly limit'],
  ['28 days', 28 * 86_400, 'Monthly limit'],
  ['30 days', 30 * 86_400, 'Monthly limit'],
  ['31 days', 31 * 86_400, 'Monthly limit'],
  ['3 days', 3 * 86_400, '3-day limit'],
  ['2 hours', 7_200, '2-hour limit'],
  ['90 minutes', 5_400, '90-minute limit'],
  ['90 seconds', 90, '90-second limit'],
];

const english = i18n.getFixedT('en');

describe('quotaWindowLabel', () => {
  test.each(LENGTHS)('%s reads as the term from docs/terms.md', (_name, seconds, expected) => {
    const { labelKey, labelParams } = quotaWindowLabel(seconds);
    expect(english(labelKey, labelParams)).toBe(expected);
  });
});

describe('Kimi window labels', () => {
  const labelFor = (duration: number, timeUnit: string) => {
    const [row] = buildKimiQuotaRows({
      limits: [{ detail: { limit: '100', used: '1' }, window: { duration, timeUnit } }],
    });
    return english(row.labelKey!, row.labelParams);
  };

  test.each([
    [5, 'TIME_UNIT_HOUR', '5-hour limit'],
    [300, 'TIME_UNIT_MINUTE', '5-hour limit'],
    [1, 'TIME_UNIT_DAY', 'Daily limit'],
    [1, 'TIME_UNIT_WEEK', 'Weekly limit'],
    [7, 'TIME_UNIT_DAY', 'Weekly limit'],
    [30, 'TIME_UNIT_DAY', 'Monthly limit'],
    [3, 'TIME_UNIT_DAY', '3-day limit'],
    [90, 'TIME_UNIT_SECOND', '90-second limit'],
    [90, 'TIME_UNIT_MINUTE', '90-minute limit'],
  ])('%d %s reads as %s', (duration, timeUnit, expected) => {
    expect(labelFor(duration, timeUnit)).toBe(expected);
  });
});

describe('quota window labels in every locale', () => {
  const LOCALES = ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr'];

  test.each(LOCALES)('%s: fixed windows match the per-provider labels', (locale) => {
    const t = i18n.getFixedT(locale);
    expect(t('quota_window.five_hour')).toBe(t('codex_quota.primary_window'));
    expect(t('quota_window.daily')).toBe(t('antigravity_quota.daily_limit'));
    expect(t('quota_window.weekly')).toBe(t('codex_quota.secondary_window'));
    expect(t('quota_window.monthly')).toBe(t('kimi_quota.monthly_limit'));
  });

  test.each(LOCALES)('%s: the Fable window is named without a model number', (locale) => {
    const text = i18n.getFixedT(locale)('claude_quota.seven_day_fable');
    expect(text).toContain('Fable');
    expect(text).not.toContain('5');
    if (locale === 'en') expect(text).toBe('Weekly Fable limit');
  });

  test.each(LOCALES)('%s: odd lengths are a whole phrase with the number inside', (locale) => {
    const t = i18n.getFixedT(locale);
    for (const [seconds, count] of [
      [90, 90],
      [5_400, 90],
      [7_200, 2],
      [3 * 86_400, 3],
    ]) {
      const { labelKey, labelParams } = quotaWindowLabel(seconds);
      const text = t(labelKey, labelParams);
      expect(text).toContain(String(count));
      expect(text).not.toContain('{{');
      expect(text).not.toMatch(/\d\s?(?:s|m|h|d|w)\b/);
    }
  });

  test('ru: counts take the Russian plural form', () => {
    const t = i18n.getFixedT('ru');
    expect(t('quota_window.hours', { count: 1 })).toBe('Лимит на 1 час');
    expect(t('quota_window.hours', { count: 2 })).toBe('Лимит на 2 часа');
    expect(t('quota_window.hours', { count: 11 })).toBe('Лимит на 11 часов');
    expect(t('quota_window.days', { count: 3 })).toBe('Лимит на 3 дня');
    expect(t('quota_window.minutes', { count: 90 })).toBe('Лимит на 90 минут');
    expect(t('quota_window.seconds', { count: 21 })).toBe('Лимит на 21 секунду');
  });
});
