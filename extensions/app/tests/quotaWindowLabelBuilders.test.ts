import { describe, expect, it } from 'bun:test';
import { quotaRowsFor } from '../src/services/quotaService';

// Expected strings are docs/terms.md ("Quota windows"): a window of exactly 5 hours, 24 hours,
// 7 days or 28-31 days gets the named label whichever provider reports it; any other length is a
// whole `{n}-{unit} limit` phrase in its largest whole unit.
const DAY = 86_400;
const LENGTHS: Array<[string, number, string]> = [
  ['5 hours', 5 * 3_600, '5-hour limit'],
  ['24 hours', DAY, 'Daily limit'],
  ['7 days', 7 * DAY, 'Weekly limit'],
  ['30 days', 30 * DAY, 'Monthly limit'],
  ['2 hours', 2 * 3_600, '2-hour limit'],
  ['3 days', 3 * DAY, '3-day limit'],
  ['90 minutes', 90 * 60, '90-minute limit'],
  ['90 seconds', 90, '90-second limit'],
];

describe('window labels built in code', () => {
  it.each(LENGTHS)('Codex names a %s window by its length', (_name, seconds, expected) => {
    const rows = quotaRowsFor('codex', {
      rate_limit: { primary_window: { used_percent: 10, limit_window_seconds: seconds } },
    });
    expect(rows.map((row) => row.label)).toEqual([expected]);
  });

  it.each(LENGTHS)('Meta names a %s window by its length', (_name, seconds, expected) => {
    const rows = quotaRowsFor('meta', {
      subs_usage: { window: { used_percent: 10, window_duration_mins: seconds / 60 } },
    });
    // Meta always reports its unnamed window first and the weekly window second.
    expect(rows.map((row) => row.label)).toEqual([expected, 'Weekly limit']);
  });

  const KIMI_UNITS: Array<[string, number, string]> = [
    ['TIME_UNIT_SECOND', 1, 'seconds'],
    ['TIME_UNIT_MINUTE', 60, 'minutes'],
    ['TIME_UNIT_HOUR', 3_600, 'hours'],
    ['TIME_UNIT_DAY', DAY, 'days'],
    ['TIME_UNIT_WEEK', 7 * DAY, 'weeks'],
  ];

  it.each(LENGTHS)('Kimi names a %s window by its length, in whichever unit it sends', (_name, seconds, expected) => {
    // Every unit that expresses this length as a whole number of that unit.
    const sends = KIMI_UNITS.filter(([, unitSeconds]) => seconds % unitSeconds === 0);
    expect(sends.length).toBeGreaterThan(0);
    for (const [timeUnit, unitSeconds] of sends) {
      const rows = quotaRowsFor('kimi', {
        limits: [{ window: { duration: seconds / unitSeconds, timeUnit }, detail: { limit: 100, used: 1 } }],
      });
      expect(rows.map((row) => row.label)).toEqual([expected]);
    }
  });

  it('puts the scope before the window label in sentence case', () => {
    const rows = quotaRowsFor('codex', {
      code_review_rate_limit: {
        primary_window: { used_percent: 1, limit_window_seconds: 5 * 3_600 },
        secondary_window: { used_percent: 2, limit_window_seconds: 7 * DAY },
      },
      additional_rate_limits: [{
        limit_name: 'GPT-5.3-Codex-Spark',
        rate_limit: { primary_window: { used_percent: 3, limit_window_seconds: DAY } },
      }],
    });
    expect(rows.map((row) => row.label)).toEqual([
      'Code review 5-hour limit',
      'Code review weekly limit',
      'GPT-5.3-Codex-Spark daily limit',
    ]);
  });
});
