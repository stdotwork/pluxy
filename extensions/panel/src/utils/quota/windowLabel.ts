/**
 * Label for a quota window whose length the provider reports, so every provider names the same
 * length the same way (docs/terms.md). Exactly 5 hours, 24 hours, 7 days or 28-31 days get the
 * named label; any other length gets a whole phrase in its largest whole unit.
 */

const MINUTE_SECONDS = 60;
const HOUR_SECONDS = 3_600;
const DAY_SECONDS = 86_400;
const FIVE_HOUR_SECONDS = 5 * HOUR_SECONDS;
const WEEK_SECONDS = 7 * DAY_SECONDS;
const MIN_MONTH_SECONDS = 28 * DAY_SECONDS;
const MAX_MONTH_SECONDS = 31 * DAY_SECONDS;

export type QuotaWindowLabel = {
  labelKey: string;
  labelParams?: { count: number };
};

export function quotaWindowLabel(seconds: number): QuotaWindowLabel {
  if (seconds === FIVE_HOUR_SECONDS) return { labelKey: 'quota_window.five_hour' };
  if (seconds === DAY_SECONDS) return { labelKey: 'quota_window.daily' };
  if (seconds === WEEK_SECONDS) return { labelKey: 'quota_window.weekly' };
  if (seconds >= MIN_MONTH_SECONDS && seconds <= MAX_MONTH_SECONDS) {
    return { labelKey: 'quota_window.monthly' };
  }
  if (seconds % DAY_SECONDS === 0) {
    return { labelKey: 'quota_window.days', labelParams: { count: seconds / DAY_SECONDS } };
  }
  if (seconds % HOUR_SECONDS === 0) {
    return { labelKey: 'quota_window.hours', labelParams: { count: seconds / HOUR_SECONDS } };
  }
  if (seconds % MINUTE_SECONDS === 0) {
    return { labelKey: 'quota_window.minutes', labelParams: { count: seconds / MINUTE_SECONDS } };
  }
  return { labelKey: 'quota_window.seconds', labelParams: { count: seconds } };
}
