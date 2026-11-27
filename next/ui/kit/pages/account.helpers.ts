// Pure helpers for one account's page (account.tsx): full reset labels with absolute weekday/clock
// and relative countdown, burn-rate pace tick and projection, limit titles and money balances.

import type { QuotaLimit } from "../api/quota";
import { clock, leftPercent, resetPassed, resetsIn, windowName } from "../parts/account";
import { strings } from "../strings";
import { shortDuration } from "../time";

const t = strings("account.helpers", {
  resetsAt: "Resets {when} · in {duration}",
  resetAvailable: "Reset available",
  paceRunsOutAt: "At this pace it runs out at {when}, {before} before it resets.",
  paceRunsOutOn: "At this pace it runs out {when}, {before} before it resets.",
  balanceLeft: "{amount} left",
  balanceTitle: "Credit balance",
});

const DAY_MS = 86_400_000;
const MINUTE_MS = 60_000;
const MIN_PACE_ELAPSED_MS = 5 * MINUTE_MS;
const MIN_PACE_FRACTION = 0.02;

const startOfDay = (ms: number) => {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
};

const shortWeekday = (d: Date) => d.toLocaleDateString("en-GB", { weekday: "short" });
const shortMonth = (d: Date) => d.toLocaleDateString("en-GB", { month: "short" });

/** Calendar date and 24-hour clock: "Mon 14:00" within a week, else "17 Jul 08:15". */
export function absoluteWhen(at: Date, now = Date.now()): string {
  const days = Math.round((startOfDay(at.getTime()) - startOfDay(now)) / DAY_MS);
  const time = clock(at);
  if (days < 7) return `${shortWeekday(at)} ${time}`;
  return `${at.getDate()} ${shortMonth(at)} ${time}`;
}

export interface ResetInfo {
  text: string;
  at?: Date;
  passed?: boolean;
}

/** Absolute and relative reset line for a limit on an account's page: "Resets Mon 14:00 · in 1h 52m". */
export function resetLabel(l: QuotaLimit, now = Date.now()): ResetInfo | undefined {
  const left = resetsIn(l, now);
  if (left === undefined) {
    return resetPassed(l, now) ? { text: t("resetAvailable"), passed: true } : undefined;
  }
  const at = new Date((l.resets_at as number) * 1000);
  const when = absoluteWhen(at, now);
  const duration = shortDuration(left / 1000);
  return { text: t("resetsAt", { when, duration }), at };
}

export interface PaceInfo {
  /** Where an even burn across the window sits right now, in % left (0..100). */
  evenLeft: number;
  /** Whether to draw the pace tick on the bar (hidden right at the rounded ends). */
  showTick: boolean;
  /** fast: burning ahead of an even pace and projected to run out before reset; ok: lasts until reset; even: nothing used yet. */
  kind: "fast" | "ok" | "even";
  /** Plain sentence under the meter bar, shown only when the present pace runs out before reset. */
  text?: string;
}

/**
 * Where an even burn would be on a window's meter and whether the current burn runs out before the
 * window resets. Only defined for actively running, bounded, percentage-based windows with > 0% left.
 */
export function paceOf(l: QuotaLimit, now = Date.now()): PaceInfo | undefined {
  if (l.unlimited) return undefined;
  const left = leftPercent(l);
  if (left === undefined || left <= 0) return undefined;
  if (!l.window_seconds || l.window_seconds <= 0 || !l.resets_at) return undefined;
  const windowMs = l.window_seconds * 1000;
  const remainingMs = l.resets_at * 1000 - now;
  if (remainingMs <= 0 || remainingMs >= windowMs) return undefined;
  const elapsedMs = windowMs - remainingMs;
  const evenLeft = Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
  const showTick = evenLeft >= 2 && evenLeft <= 98;
  const minElapsed = Math.max(MIN_PACE_ELAPSED_MS, windowMs * MIN_PACE_FRACTION);
  const used = 100 - left;
  if (used <= 0 || elapsedMs < minElapsed) {
    return { evenLeft, showTick, kind: "even" };
  }
  if (left < evenLeft) {
    const timeToEmptyMs = (left / used) * elapsedMs;
    const beforeResetMs = remainingMs - timeToEmptyMs;
    if (beforeResetMs >= MINUTE_MS) {
      const runsOutAt = new Date(now + timeToEmptyMs);
      const sameDay = startOfDay(runsOutAt.getTime()) === startOfDay(now);
      const when = sameDay ? clock(runsOutAt) : absoluteWhen(runsOutAt, now);
      const before = shortDuration(beforeResetMs / 1000);
      return {
        evenLeft,
        showTick,
        kind: "fast",
        text: sameDay ? t("paceRunsOutAt", { when, before }) : t("paceRunsOutOn", { when, before }),
      };
    }
  }
  return { evenLeft, showTick, kind: "ok" };
}

/** A limit's heading on the account page: standard window name, or the provider's label for scoped/feature limits. */
export function limitTitle(l: QuotaLimit): string {
  if ((l.models?.length || l.feature) && l.label?.trim()) return l.label.trim();
  const base = windowName(l);
  if (l.models?.length) return `${base} · ${l.models.join(", ")}`;
  return base;
}

/** Whether a limit reports a money balance (USD) without a percentage gauge. */
export function isBalanceLimit(l: QuotaLimit): boolean {
  return (
    !l.feature &&
    !l.models?.length &&
    !(l.window_seconds && l.window_seconds > 0) &&
    l.unit?.toLowerCase() === "usd" &&
    l.remaining !== undefined &&
    leftPercent(l) === undefined
  );
}

const USD = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });

/** Formatted money balance: "$42.10 left". */
export function balanceLabel(l: QuotaLimit): { title: string; value: string } {
  return {
    title: l.label?.trim() || t("balanceTitle"),
    value: t("balanceLeft", { amount: USD.format(l.remaining ?? 0) }),
  };
}
