// What the Home page works out from core's replies, apart from the page so it can be tested
// without a browser: the verdict, the counts line, the last seven local days, the hotspot among
// the failures, the line about coding tools, and the one stale notice a round of failed reads gets.

import type { Account } from "../api/accounts";
import type { SetupClient } from "../api/setup";
import type { UsageBucket, UsageGroup } from "../api/usage";
import {
  accountCondition,
  backWhen,
  conditionUnchecked,
  type Group,
  leftPercent,
  rowWindows,
} from "../parts/account";
import type { Windows } from "../parts/hooks";
import { strings } from "../strings";

const t = strings("home.logic", {
  safeMode: "pluxy is in safe mode.",
  safeModeNote:
    "No provider runs in safe mode, so every request fails. Restart pluxy without safe mode to serve again.",
  needsYou: { one: "{count} account needs you.", other: "{count} accounts need you." },
  nothingServing: "Nothing is serving.",
  nothingServingNote: "Requests from your coding tools fail until an account serves again.",
  firstBack: " The first is back {when}.",
  nothingNeedsYou: "Nothing needs you.",
  everythingServing: "Everything is serving.",
  statusUnread: "Could not check which providers are running.",
  statusUnreadNote: "Accounts with a provider stay unchecked until pluxy's status is read.",
  mostFailed: "Accounts are serving, but most requests failed.",
  mostFailedNote: "Activity lists the failed requests, with the reason for each.",
  noAccounts: "pluxy has no accounts yet.",
  waiting: "{count} waiting",
  serving: "{count} serving",
  notChecked: "{count} not checked",
  turnedOff: "{count} turned off",
  requests: {
    one: "{n} request in 7 days, {percent} succeeded",
    other: "{n} requests in 7 days, {percent} succeeded",
  },
  hotspot: "{count} of the {total} failed requests asked for {model}.",
  hotspotAll: "All {total} failed requests asked for {model}.",
  connected: "{names} {verb} connected to pluxy.",
  installed: "{names} {verb} installed but not connected to pluxy.",
  noTool: "No coding tool is connected to pluxy yet.",
  is: "is",
  are: "are",
  twoTools: "{first} and {second}",
  manyTools: { one: "{first} and {count} other tool", other: "{first} and {count} other tools" },
  today: "Today",
  chart: "{total} requests in the last 7 days, {failed} failed",
  chartOne: "1 request in the last 7 days, {failed} failed",
  day: "{day}: {total} requests, {failed} failed",
  dayOne: "{day}: 1 request, {failed} failed",
});

// ---- the accounts -------------------------------------------------------------------------------

/** How many accounts are in each group, and in all. */
export interface Counts {
  needs: number;
  waiting: number;
  serving: number;
  off: number;
  unchecked?: number;
  total: number;
}

/** Day one's heading: pluxy has no accounts yet. */
export const noAccountsTitle = () => t("noAccounts");

/** The accounts of each group, in core's order, and when the first of the waiting ones is back. */
export interface Grouped {
  groups: Record<Group, Account[]>;
  /** Accounts whose provider status could not be checked because GET /status failed. */
  unchecked: Account[];
  counts: Counts;
  /** The earliest end among the waiting accounts that have one (ms since 1970). */
  firstBack?: number;
  /** Each account's place in core's list, by ID. */
  order: Map<string, number>;
}

/** Sorts accounts into the kit's four groups by their condition (parts/account.ts). */
export function groupAccounts(
  list: Account[],
  running?: ReadonlySet<string>,
  now = Date.now(),
  statusFailed = false,
): Grouped {
  const groups: Record<Group, Account[]> = { needs: [], waiting: [], serving: [], off: [] };
  const unchecked: Account[] = [];
  let firstBack: number | undefined;
  const order = new Map<string, number>();
  for (const [i, a] of list.entries()) {
    order.set(a.id, i);
    const c = accountCondition(a, running, now);
    if (statusFailed && conditionUnchecked(a, c, running)) {
      unchecked.push(a);
      continue;
    }
    groups[c.group].push(a);
    if (c.group === "waiting" && c.until !== undefined && c.until > now)
      firstBack = firstBack === undefined ? c.until : Math.min(firstBack, c.until);
  }
  const counts: Counts = {
    needs: groups.needs.length,
    waiting: groups.waiting.length,
    serving: groups.serving.length,
    off: groups.off.length,
    ...(unchecked.length > 0 ? { unchecked: unchecked.length } : {}),
    total: list.length,
  };
  return { groups, unchecked, counts, firstBack, order };
}

/**
 * The least left of the windows a row shows (0 to 100), or nothing when none of them says: a
 * window that cannot be read is no reason to rank an account first.
 */
export function leastLeft(w?: Windows): number | undefined {
  if (w?.state !== "read") return undefined;
  const left = rowWindows(w.limits)
    .map(leftPercent)
    .filter((n): n is number => n !== undefined);
  return left.length > 0 ? Math.min(...left) : undefined;
}

/** One provider's accounts in Home's list, in the order the list shows them. */
export interface ProviderGroup {
  /** The extension ID; "" for accounts with no provider. */
  extension: string;
  accounts: Account[];
  serving: number;
  unchecked: number;
}

/**
 * Home's list under the pools: every account that does not need the person (those are in the band
 * above), grouped by provider in the order core lists each provider's first account. In a group:
 * the waiting ones first, soonest back first; then the serving ones, least left first; then any
 * unchecked ones; then those turned off.
 */
export function providerGroups(
  grouped: Grouped,
  windows: ReadonlyMap<string, Windows>,
  running?: ReadonlySet<string>,
  now = Date.now(),
): ProviderGroup[] {
  const waiting = [...grouped.groups.waiting].sort((x, y) => {
    const ux = accountCondition(x, running, now).until ?? Number.POSITIVE_INFINITY;
    const uy = accountCondition(y, running, now).until ?? Number.POSITIVE_INFINITY;
    return ux - uy;
  });
  const ordered = [
    ...waiting,
    ...servingOrder(grouped.groups.serving, windows),
    ...grouped.unchecked,
    ...grouped.groups.off,
  ];
  const out = new Map<string, ProviderGroup>();
  for (const a of ordered) {
    const key = a.extension ?? "";
    const g = out.get(key) ?? { extension: key, accounts: [], serving: 0, unchecked: 0 };
    g.accounts.push(a);
    if (grouped.groups.serving.includes(a)) g.serving++;
    if (grouped.unchecked.includes(a)) g.unchecked++;
    out.set(key, g);
  }
  // Providers in core's order of their first account.
  const first = new Map<string, number>();
  const all = [
    ...grouped.groups.needs,
    ...grouped.groups.waiting,
    ...grouped.groups.serving,
    ...grouped.unchecked,
    ...grouped.groups.off,
  ];
  for (const a of all) {
    const key = a.extension ?? "";
    if (!first.has(key)) first.set(key, grouped.order.get(a.id) ?? 0);
  }
  return [...out.values()].sort((x, y) => (first.get(x.extension) ?? 0) - (first.get(y.extension) ?? 0));
}

/** The serving accounts with the least left first; those whose limits are not known come last, in core's order. */
export function servingOrder(list: Account[], windows: ReadonlyMap<string, Windows>): Account[] {
  const rank = (a: Account) => leastLeft(windows.get(a.id)) ?? Number.POSITIVE_INFINITY;
  return list
    .map((a, i) => ({ a, i }))
    .sort((x, y) => rank(x.a) - rank(y.a) || x.i - y.i)
    .map((x) => x.a);
}

// ---- the verdict --------------------------------------------------------------------------------

export type VerdictKind =
  | "safe-mode"
  | "no-accounts"
  | "needs-you"
  | "unchecked"
  | "nothing-serving"
  | "nothing-needs-you"
  | "most-failed"
  | "serving";

export interface Verdict {
  kind: VerdictKind;
  headline: string;
  /** What it means, under the headline, when there is something to add. */
  note?: string;
}

/**
 * The page's verdict, the first that matches: safe mode (without GET /status, none is known);
 * no accounts at all; accounts that need the person; nothing serving; accounts that wait; else
 * everything serves. firstBack is when the first waiting account is back, for the note. week is
 * the last seven days' requests: when most of them failed, the verdict says so in place of the two
 * that call the accounts fine, because the person reads those as "it works".
 */
export function verdictOf(
  status: { safe_mode: boolean } | undefined,
  counts: Counts,
  firstBack?: number,
  now = Date.now(),
  week?: { total: number; failed: number },
): Verdict {
  if (status?.safe_mode) return { kind: "safe-mode", headline: t("safeMode"), note: t("safeModeNote") };
  if (counts.total === 0) return { kind: "no-accounts", headline: t("noAccounts") };
  if (counts.needs > 0) return { kind: "needs-you", headline: t("needsYou", { count: counts.needs }) };
  if ((counts.unchecked ?? 0) > 0)
    return { kind: "unchecked", headline: t("statusUnread"), note: t("statusUnreadNote") };
  if (counts.serving === 0) {
    const back =
      firstBack !== undefined && firstBack > now ? t("firstBack", { when: backWhen(firstBack, now) }) : "";
    return { kind: "nothing-serving", headline: t("nothingServing"), note: t("nothingServingNote") + back };
  }
  if (week && week.total > 0 && week.failed * 2 > week.total)
    return { kind: "most-failed", headline: t("mostFailed"), note: t("mostFailedNote") };
  if (counts.waiting > 0) return { kind: "nothing-needs-you", headline: t("nothingNeedsYou") };
  return { kind: "serving", headline: t("everythingServing") };
}

// ---- the last seven days -------------------------------------------------------------------------

/** One local day of the last seven. */
export interface Day {
  /** Local midnight that starts the day (ms since 1970). */
  start: number;
  total: number;
  failed: number;
  succeeded: number;
}

export const DAYS = 7;

/** Local midnight of the day daysBack before now's day. Counted in calendar days, so a day of 23 or 25 hours is one. */
export function localMidnight(now: Date, daysBack: number): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() - daysBack);
}

/**
 * The usage call's query: from local midnight six days ago, in hourly steps. Core's daily steps
 * start at midnight UTC, which is not the person's day, so the days are built here from the hours.
 */
export const usageQuery = (now: Date) =>
  `since=${encodeURIComponent(localMidnight(now, DAYS - 1).toISOString())}&bucket=1h`;

/**
 * The seven local days, oldest first, each holding the hourly steps that start inside it.
 * shortcut: a step is counted whole in the day it starts in, so in a time zone whose offset is
 * not a whole number of hours the step across midnight counts in the day before; upgrade by
 * asking core for 30-minute steps when it can.
 */
export function localDays(series: UsageBucket[], now: Date): Day[] {
  const days: Day[] = Array.from({ length: DAYS }, (_, i) => ({
    start: localMidnight(now, DAYS - 1 - i).getTime(),
    total: 0,
    failed: 0,
    succeeded: 0,
  }));
  for (const b of series) {
    const at = new Date(b.start);
    if (Number.isNaN(at.getTime())) continue;
    const day = days.find((d, i) => {
      const next = days[i + 1];
      return at.getTime() >= d.start && (next === undefined || at.getTime() < next.start);
    });
    if (!day) continue;
    day.total += b.requests;
    day.failed += b.failure_count;
    day.succeeded += b.requests - b.failure_count;
  }
  return days;
}

/** What the days add up to: the figures the counts line and the chart's name say. */
export function weekTotals(days: Day[]): { total: number; failed: number; succeeded: number } {
  return days.reduce(
    (sum, d) => ({
      total: sum.total + d.total,
      failed: sum.failed + d.failed,
      succeeded: sum.succeeded + d.succeeded,
    }),
    { total: 0, failed: 0, succeeded: 0 },
  );
}

/** The share of requests that succeeded, to one decimal and rounded down, so only all of them is 100%. */
export function successPercent(success: number, total: number): string | undefined {
  if (total <= 0) return undefined;
  return `${Math.floor((success * 1000) / total) / 10}%`;
}

const WEEKDAY = (d: Date) => d.toLocaleDateString("en-GB", { weekday: "short" });
const MONTH = (d: Date) => d.toLocaleDateString("en-GB", { month: "short" });

/** A day's short name under its bar: Wed, and Today for the last one. */
export function dayLabel(d: Day, now: Date): string {
  return d.start === localMidnight(now, 0).getTime() ? t("today") : WEEKDAY(new Date(d.start));
}

/** A day's date as people write it: Wed 25 Nov. */
export function dayDate(d: Day): string {
  const at = new Date(d.start);
  return `${WEEKDAY(at)} ${at.getDate()} ${MONTH(at)}`;
}

/** What a bar says on hover or focus: Wed 25 Nov: 138 requests, 9 failed. */
export function dayText(d: Day): string {
  return d.total === 1
    ? t("dayOne", { day: dayDate(d), failed: d.failed })
    : t("day", { day: dayDate(d), total: d.total.toLocaleString(), failed: d.failed.toLocaleString() });
}

/** The chart's name, which sums it up for a screen reader. */
export function chartName(totals: { total: number; failed: number }): string {
  return totals.total === 1
    ? t("chartOne", { failed: totals.failed })
    : t("chart", { total: totals.total.toLocaleString(), failed: totals.failed.toLocaleString() });
}

/** The counts line under the verdict: only the parts that are not zero. */
export function countsLine(counts: Counts, week?: { total: number; succeeded: number }): string {
  const parts: string[] = [];
  if (counts.waiting > 0) parts.push(t("waiting", { count: counts.waiting }));
  if (counts.serving > 0) parts.push(t("serving", { count: counts.serving }));
  if (counts.unchecked && counts.unchecked > 0) parts.push(t("notChecked", { count: counts.unchecked }));
  if (counts.off > 0) parts.push(t("turnedOff", { count: counts.off }));
  if (week && week.total > 0) {
    parts.push(
      t("requests", {
        count: week.total,
        n: week.total.toLocaleString(),
        percent: successPercent(week.succeeded, week.total) ?? "",
      }),
    );
  }
  return parts.join(" · ");
}

// ---- what the failures say -----------------------------------------------------------------------

/** The model most of the failed requests asked for, when that is a pattern and not chance. */
export interface Hotspot {
  model: string;
  failed: number;
  total: number;
  text: string;
}

/** The fewest failed requests that can be called a pattern. */
export const HOTSPOT_MIN_FAILED = 3;

/**
 * Among at least HOTSPOT_MIN_FAILED failed requests, the model that more than half of them asked
 * for. Half exactly is a tie, not a pattern. models are the reply's per-model counts.
 */
export function hotspotOf(models: UsageGroup[], failed: number): Hotspot | undefined {
  if (failed < HOTSPOT_MIN_FAILED) return undefined;
  const worst = [...models].sort((a, b) => b.failure_count - a.failure_count)[0];
  if (!worst || worst.failure_count * 2 <= failed) return undefined;
  const text =
    worst.failure_count >= failed
      ? t("hotspotAll", { total: failed.toLocaleString(), model: worst.name })
      : t("hotspot", {
          count: worst.failure_count.toLocaleString(),
          total: failed.toLocaleString(),
          model: worst.name,
        });
  return { model: worst.name, failed: worst.failure_count, total: failed, text };
}

// ---- the coding tools ---------------------------------------------------------------------------

export interface ToolLine {
  text: string;
  /** The link's words; it leads to where tools are connected. A connected tool needs none. */
  link?: "connect" | "connectTool";
  state: "connected" | "installed" | "none";
}

function names(tools: SetupClient[]): string {
  const first = tools[0]?.name ?? "";
  if (tools.length === 1) return first;
  if (tools.length === 2) return t("twoTools", { first, second: tools[1]?.name ?? "" });
  return t("manyTools", { first, count: tools.length - 1 });
}

/**
 * One line on whether a coding tool is connected to pluxy, from what core found on its computer.
 * A tool core cannot read (unchecked) says nothing either way. Connected wins over installed.
 */
export function toolLine(clients: SetupClient[]): ToolLine {
  const known = clients.filter((c) => c.state !== "unchecked");
  const connected = known.filter((c) => c.state === "configured");
  if (connected.length > 0) {
    return {
      state: "connected",
      text: t("connected", { names: names(connected), verb: connected.length === 1 ? t("is") : t("are") }),
    };
  }
  const installed = known.filter((c) => c.installed);
  if (installed.length > 0) {
    return {
      state: "installed",
      link: "connect",
      text: t("installed", { names: names(installed), verb: installed.length === 1 ? t("is") : t("are") }),
    };
  }
  return { state: "none", link: "connectTool", text: t("noTool") };
}

// ---- one failure, said once -----------------------------------------------------------------------

/** What one read of a round came to, as the page keeps it. */
export interface Read<T> {
  /** loading: no reply yet; ready: the last read worked; failed: it did not (data is the read before, if any). */
  phase: "loading" | "ready" | "failed";
  data?: T;
  error?: string;
  /** The HTTP status of a failed read; 0 when nothing answered. */
  status?: number;
  /** When data was read (ms since 1970). */
  at?: number;
}

/**
 * Whether the page says "Could not refresh": when any read of the round failed over data that
 * was read before, it says so once, naming the time of the oldest data on screen. A read that
 * failed with no data has its own place on the page (it is not stale: there is nothing old).
 */
export function staleOf(reads: Read<unknown>[]): { at?: number; error?: string } | undefined {
  const stale = reads.filter((r) => r.phase === "failed" && r.data !== undefined);
  if (stale.length === 0) return undefined;
  const times = stale.map((r) => r.at).filter((n): n is number => n !== undefined);
  return { at: times.length > 0 ? Math.min(...times) : undefined, error: stale[0]?.error };
}
