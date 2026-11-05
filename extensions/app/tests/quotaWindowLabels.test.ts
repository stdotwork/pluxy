import { describe, expect, it } from 'bun:test';
import { en, ja, zhCN, zhTW, type MessageKey } from '../src/i18n/resources';

// Expected wording comes from docs/terms.md (English, and the weekly and monthly rows of the
// other-languages table). The zh-CN and zh-TW five-hour, daily and hour/minute wording is the
// panel's, which has its own cross-provider test (extensions/panel/tests/quotaLimitLabels.test.ts).
// Japanese five-hour and daily wording exists only in the app; it is pinned as it stands.
const LOCALES = {
  en: {
    messages: en,
    fiveHour: '5-hour limit',
    daily: 'Daily limit',
    weekly: 'Weekly limit',
    monthly: 'Monthly limit',
    hours: '{hours}-hour limit',
    minutes: '{minutes}-minute limit',
    fable: 'Weekly Fable limit',
  },
  'zh-CN': {
    messages: zhCN,
    fiveHour: '5 小时限额',
    daily: '日限额',
    weekly: '周限额',
    monthly: '月限额',
    hours: '{hours} 小时限额',
    minutes: '{minutes} 分钟限额',
    fable: 'Fable 周限额',
  },
  'zh-TW': {
    messages: zhTW,
    fiveHour: '5 小時限額',
    daily: '日限額',
    weekly: '週限額',
    monthly: '月限額',
    hours: '{hours} 小時限額',
    minutes: '{minutes} 分鐘限額',
    fable: 'Fable 週限額',
  },
  ja: {
    messages: ja,
    fiveHour: '5 時間制限',
    daily: '日間制限',
    weekly: '週間制限',
    monthly: '月間制限',
    hours: '{hours} 時間制限',
    minutes: '{minutes} 分間制限',
    fable: 'Fable 週間制限',
  },
} as const;

// Every key that names a fixed quota window, grouped by the window it names.
const FIVE_HOUR_KEYS: MessageKey[] = ['quota.service.limit.fiveHours', 'quota.service.window.fiveHour'];
const DAILY_KEYS: MessageKey[] = ['quota.service.daily', 'quota.service.antigravity.daily'];
const WEEKLY_KEYS: MessageKey[] = ['quota.service.limit.week', 'quota.service.window.sevenDay', 'quota.service.weekly'];
const MONTHLY_KEYS: MessageKey[] = ['quota.service.limit.month'];
// Scoped weekly windows: key, the English scope phrase, and the scope word every locale keeps.
const SCOPED_WEEKLY: ReadonlyArray<[MessageKey, string, string]> = [
  ['quota.service.window.sevenDayOAuth', 'OAuth apps', 'OAuth'],
  ['quota.service.window.sevenDayOpus', 'Opus', 'Opus'],
  ['quota.service.window.sevenDaySonnet', 'Sonnet', 'Sonnet'],
  ['quota.service.window.sevenDayCowork', 'Cowork', 'Cowork'],
];

describe('quota window labels use one wording for every provider', () => {
  for (const [locale, terms] of Object.entries(LOCALES)) {
    describe(locale, () => {
      const text = (key: MessageKey) => terms.messages[key];

      it('names each fixed window the same on every key that shows it', () => {
        for (const key of FIVE_HOUR_KEYS) expect(text(key)).toBe(terms.fiveHour);
        for (const key of DAILY_KEYS) expect(text(key)).toBe(terms.daily);
        for (const key of WEEKLY_KEYS) expect(text(key)).toBe(terms.weekly);
        for (const key of MONTHLY_KEYS) expect(text(key)).toBe(terms.monthly);
      });

      it('names the Meta hour and minute windows with a whole phrase', () => {
        expect(text('quota.service.meta.windowHours')).toBe(terms.hours);
        expect(text('quota.service.meta.windowMinutes')).toBe(terms.minutes);
      });

      it('builds scoped weekly labels from the weekly term', () => {
        for (const [key, englishScope, scopeWord] of SCOPED_WEEKLY) {
          const label = text(key);
          if (locale === 'en') expect(label).toBe(`Weekly ${englishScope} limit`);
          else expect(label).toContain(terms.weekly);
          expect(label).toContain(scopeWord);
        }
        expect(text('quota.service.window.sevenDayFable')).toBe(terms.fable);
      });

      it('never spells a weekly window as a number of days', () => {
        const labels = [...WEEKLY_KEYS, ...SCOPED_WEEKLY.map(([key]) => key), 'quota.service.window.sevenDayFable' as const]
          .map(text);
        for (const label of labels) expect(label).not.toMatch(/\b7\b|7[- ]?day|1w|7 ?天|7 ?日/i);
      });
    });
  }
});
