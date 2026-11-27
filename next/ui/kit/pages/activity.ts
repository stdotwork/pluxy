// What the Requests and Usage tabs work out without drawing anything: the filters and the address
// that carries them, the story of why a request went where it did, whether its trace links to a
// log, the spreadsheet of requests, and the hourly buckets added into local days.

import { useCallback, useEffect, useState } from "preact/hooks";
import { type Account, problem } from "../api";
import type { UsageBucket, UsageGroup, UsageSummary } from "../api/usage";
import type { UsageEvent, UsageEventsReply, UsageFilters, UsageLeftOut } from "../api/usageEvents";
import { csvField } from "../csv";
import type { Reply } from "../host";
import { backWhen } from "../parts/account";
import { failureWord, stateText } from "../states";
import { strings } from "../strings";

const t = strings("activity", {
  after: { one: "after {count} other", other: "after {count} others" },
  fallbackNote: "fallback (step {step})",
  succeededAfter: {
    one: "{account} succeeded after {count} other account failed.",
    other: "{account} succeeded after {count} other accounts failed.",
  },
  fallback: "{asked} did not work, so pluxy used its fallback {model} (step {step}).",
  fallbackUnnamed: "{asked} did not work, so pluxy used its fallback (step {step}).",
  failedOne: "{account} failed: {word}.",
  failedEvery: "Every account pluxy tried failed.",
  noAccount: "No account could take it.",
  noServe: "No account serves {model}.",
  keyRefused: "pluxy did not accept the key this request came with.",
  couldNotSend: "pluxy could not send it to any account: {word}.",
  coolingBack: "cooling down, back {when}",
  coolingNoTime: "cooling down",
  exhaustedBack: "limit used up, back {when}",
  exhaustedNoTime: "limit used up",
  needsSignIn: "needs sign-in",
  closed: "closed by provider",
  off: "turned off",
  busy: "busy with other requests",
  skipped: "not reached before the request gave up",
  notTried: "not tried",
  backupNote: "backup",
  whyPinned: "The request asked for {account} by name.",
  whySession: "{account} answered this conversation's last request, so pluxy kept the conversation there.",
  whyOnly: "{account} is the only account that serves {model}.",
  whyFirst: "{account} comes first in its provider's account order.",
  whyBackup:
    "{account} is a backup in its provider's account order: no account above it could take the request then.",
  whyOnlyFree: "{account} was the only account in the first group that could take it then.",
  whyRoundRobin: "It was {account}'s turn: the accounts in the first group take turns.",
  whyWeighted: "{account} was next by weight: the first group splits requests by weight.",
  whyFillFirst:
    "{account} is the first account in the first group that could take it: the group fills one first.",
  whyLeastActive: "{account} had the fewest requests running in the first group.",
  whySoonestReset: "{account}'s limit resets soonest in the first group, so pluxy spends it first.",
  hotspotSome: "{count} of the {total} failed requests asked for {model}.",
  hotspotAll: "All {total} failed requests asked for {model}.",
  figRequestsOne: "1 request",
  figRequests: "{count} requests",
  figSucceeded: "{percent} succeeded",
  figTokens: "{tokens} tokens",
  figCached: "{tokens} tokens, {cached} from cache",
  figTime: "{time} avg time",
  today: "Today",
  yesterday: "Yesterday",
  csvTime: "Time",
  csvModel: "Model",
  csvProvider: "Provider",
  csvAccount: "Account",
  csvKey: "Client key",
  csvFailed: "Failed",
  csvStatus: "Status",
  csvLatency: "Latency",
  csvTokens: "Tokens",
  csvTrace: "Trace ID",
});

// ---- filters and the address ------------------------------------------------------------------

/** What a tab filters by, as its address holds it. */
export interface Filters {
  failed: boolean;
  provider: string;
  account: string;
  model: string;
  key: string;
}

export const NO_FILTERS: Filters = { failed: false, provider: "", account: "", model: "", key: "" };

/** The filters that choose from a list of names (everything but All | Failed). */
export const PICKED = ["provider", "account", "model", "key"] as const;
export type Picked = (typeof PICKED)[number];

/** The filters an address's query holds. */
export function filtersOf(query: URLSearchParams): Filters {
  const failed = query.get("failed");
  return {
    failed: failed === "1" || failed === "true",
    provider: query.get("provider") ?? "",
    account: query.get("account") ?? "",
    model: query.get("model") ?? "",
    key: query.get("key") ?? "",
  };
}

/** The address's query for some filters (and a trace to open), without the leading "?". */
export function queryOf(f: Filters, trace?: string): string {
  const q = new URLSearchParams();
  if (f.failed) q.set("failed", "1");
  for (const key of PICKED) if (f[key]) q.set(key, f[key]);
  if (trace) q.set("trace", trace);
  return q.toString();
}

export const hasFilters = (f: Filters) => f.failed || PICKED.some((k) => f[k] !== "");

/**
 * An account in a filter is its ID, which is what a request records as its account (its tries
 * name accounts by public ID instead). An address may name it by its public ID: that is turned
 * into the ID.
 */
export function accountValue(value: string, accounts: Account[] | undefined): string {
  return accounts?.find((x) => x.id === value || x.public_id === value)?.id ?? value;
}

/** The filters as core's calls take them. */
export function coreFilters(f: Filters, accounts?: Account[], since = ""): UsageFilters {
  return {
    since,
    model: f.model,
    provider: f.provider,
    account: f.account ? accountValue(f.account, accounts) : "",
    key_id: f.key,
    failed: f.failed ? "true" : "",
  };
}

/** The address's query as a page reads it, and a way to change it without making a history step. */
export function useAddressQuery(): [URLSearchParams, (query: string) => void] {
  const read = () => {
    const at = location.hash.indexOf("?");
    return new URLSearchParams(at < 0 ? "" : location.hash.slice(at + 1));
  };
  const [query, setQuery] = useState(read);
  useEffect(() => {
    const onHash = () => setQuery(read());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  const write = useCallback((next: string) => {
    const at = location.hash.indexOf("?");
    const path = at < 0 ? location.hash : location.hash.slice(0, at);
    history.replaceState(null, "", `${path || "#/activity"}${next ? `?${next}` : ""}`);
    setQuery(new URLSearchParams(next));
  }, []);
  return [query, write];
}

// ---- the story --------------------------------------------------------------------------------

/** A request's story: what to say about why it went where it did, and what to do next. */
export interface Story {
  /** A few words for the row's line, beside the account: "after 2 others", "fallback (step 2)". */
  note?: string;
  /** Sentences, in order. */
  lines: string[];
  /** After the lines: each account the request left out, and why. */
  leftOut: { account: string; text: string }[];
  /**
   * Why pluxy chose the account that answered, as a sentence; order: the reason is the account
   * order of Order & fallback, which the story links to.
   */
  reason?: { text: string; order: boolean };
  /** The model no account serves: the story links to Accounts. */
  noServe?: boolean;
  /** The one next step of a failed request: an attempt's account, or its log. */
  next?: { kind: "account"; account: string } | { kind: "log" };
}

/** Why an account was left out of a request, after its name. */
export function leftOutText(l: UsageLeftOut, now = Date.now()): string {
  const until = l.until ? Date.parse(l.until) : Number.NaN;
  const back = !Number.isNaN(until) && until > now ? backWhen(until, now) : undefined;
  switch (l.state) {
    case "cooling":
      return back ? t("coolingBack", { when: back }) : t("coolingNoTime");
    case "exhausted":
      return back ? t("exhaustedBack", { when: back }) : t("exhaustedNoTime");
    case "needs_login":
      return t("needsSignIn");
    case "banned":
      return t("closed");
    case "disabled":
      return t("off");
    case "busy":
      return t("busy");
    case "skipped":
      return t("skipped");
    case "ok":
      return t("notTried");
    default: {
      // A state this does not know: core's own word for it, in lower case.
      const word = stateText(l.state, "account");
      return word.charAt(0).toLowerCase() + word.slice(1);
    }
  }
}

/** The model a fallback step used, when the record says. */
function fallbackModel(e: UsageEvent): string | undefined {
  const last = e.tried?.[e.tried.length - 1]?.model;
  const used = last || e.served_model || e.upstream_model;
  return used && used !== e.model ? used : undefined;
}

/**
 * Why routing chose the account that answered, by the record's why; undefined for a record
 * that does not say, or a reason this does not know. order: the reason is the account order.
 */
function reasonOf(e: UsageEvent, account: string): Story["reason"] {
  const model = (e.chain_step ?? 0) > 1 ? (fallbackModel(e) ?? e.model) : e.model;
  const said = (text: string, order: boolean) => ({ text, order });
  switch (e.why) {
    case "pinned":
      return said(t("whyPinned", { account }), false);
    case "session":
      return said(t("whySession", { account }), false);
    case "only":
      return said(t("whyOnly", { account, model }), false);
    case "first":
      return said(t("whyFirst", { account }), true);
    case "backup":
      return said(t("whyBackup", { account }), true);
    case "only-free":
      return said(t("whyOnlyFree", { account }), true);
    case "round-robin":
      return said(t("whyRoundRobin", { account }), true);
    case "weighted":
      return said(t("whyWeighted", { account }), true);
    case "fill-first":
      return said(t("whyFillFirst", { account }), true);
    case "least-active":
      return said(t("whyLeastActive", { account }), true);
    case "soonest-reset":
      return said(t("whySoonestReset", { account }), true);
    default:
      return undefined;
  }
}

/**
 * Why a request went where it did, by what its record holds. name turns an account's ID or
 * public ID into the name shown (masked by the caller's naming).
 */
export function whyOf(e: UsageEvent, name: (account: string) => string, now = Date.now()): Story {
  const tried = e.tried ?? [];
  const step = e.chain_step ?? 0;
  const lines: string[] = [];
  const story: Story = { lines, leftOut: [] };
  const notes: string[] = [];
  if (step > 1) {
    const model = fallbackModel(e);
    lines.push(
      model ? t("fallback", { asked: e.model, model, step }) : t("fallbackUnnamed", { asked: e.model, step }),
    );
    notes.push(t("fallbackNote", { step }));
  }
  if (!e.failed) {
    const others = tried.slice(0, -1).filter((x) => x.result !== "ok").length;
    const account = e.account ?? tried[tried.length - 1]?.account;
    if (others > 0) {
      lines.push(t("succeededAfter", { account: account ? name(account) : "", count: others }));
      notes.unshift(t("after", { count: others }));
    } else if (e.why === "backup") {
      notes.unshift(t("backupNote"));
    }
    if (account) story.reason = reasonOf(e, name(account));
    story.note = notes.join(", ") || undefined;
    return story;
  }
  story.note = notes.join(", ") || undefined;
  if (tried.length === 1) {
    const only = tried[0];
    if (only) {
      lines.push(t("failedOne", { account: name(only.account), word: failureWord(only.result, e.status) }));
    }
  } else if (tried.length > 1) {
    lines.push(t("failedEvery"));
  } else if (e.left_out && e.left_out.length > 0) {
    lines.push(t("noAccount"));
  } else if (e.error_code === "not_found") {
    lines.push(t("noServe", { model: e.model }));
    story.noServe = true;
  } else if (e.error_code === "auth" || e.error_code === "permission") {
    lines.push(t("keyRefused"));
  } else {
    lines.push(t("couldNotSend", { word: failureWord(e) }));
  }
  // The accounts routing left out, after those it tried, if any.
  story.leftOut = (e.left_out ?? []).map((l) => ({ account: name(l.account), text: leftOutText(l, now) }));
  const last = tried[tried.length - 1];
  story.next = last ? { kind: "account", account: last.account } : { kind: "log" };
  return story;
}

/**
 * How a request's trace shows: a failed request's log is always kept, so it links there; a
 * request that succeeded has a log only while core logs every request, else it is text to copy.
 * requestLog is what core's settings say, or undefined while they are not read.
 */
export function traceMode(e: Pick<UsageEvent, "failed">, requestLog: boolean | undefined): "link" | "text" {
  return e.failed || requestLog === true ? "link" : "text";
}

// ---- the day headings -------------------------------------------------------------------------

const dayStart = (time: number) => {
  const d = new Date(time);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
};

/** The heading of the day a request came on: Today, Yesterday, Friday 20 November. */
export function dayHeading(time: string, now = Date.now()): string {
  const at = Date.parse(time);
  if (Number.isNaN(at)) return time;
  const days = Math.round((dayStart(now) - dayStart(at)) / 86_400_000);
  if (days === 0) return t("today");
  if (days === 1) return t("yesterday");
  return new Date(at).toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" });
}

/** Requests in order, grouped under the heading of the day each came on. */
export function byDay(events: UsageEvent[], now = Date.now()): { day: string; events: UsageEvent[] }[] {
  const days: { day: string; events: UsageEvent[] }[] = [];
  for (const e of events) {
    const day = dayHeading(e.timestamp, now);
    const last = days[days.length - 1];
    if (last && last.day === day) last.events.push(e);
    else days.push({ day, events: [e] });
  }
  return days;
}

// ---- the spreadsheet --------------------------------------------------------------------------

/** How many requests a save holds, at most, and how many it reads at a time. */
export const SAVE_LIMIT = 10_000;
export const SAVE_PAGE = 1000;

export type Newest = { events: UsageEvent[]; capped: boolean } | { error: string } | { unauthorized: true };

/**
 * The newest requests that match, newest first, up to SAVE_LIMIT, read SAVE_PAGE at a time
 * through read(before, limit). capped: more were kept than the limit holds.
 */
export async function readNewest(
  read: (before: number, limit: number) => Promise<Reply<UsageEventsReply>>,
  limit = SAVE_LIMIT,
  page = SAVE_PAGE,
): Promise<Newest> {
  const events: UsageEvent[] = [];
  let before = 0;
  for (;;) {
    const r = await read(before, Math.min(page, limit - events.length));
    if (r.status === 401) return { unauthorized: true };
    const why = problem(r);
    if (why !== undefined || !r.body) return { error: why ?? "" };
    events.push(...r.body.events);
    if (!r.body.more || r.body.events.length === 0) return { events, capped: false };
    if (events.length >= limit) return { events, capped: true };
    before = r.body.next;
  }
}

/** The file: a header row, then each request, as the spreadsheet keeps its columns. */
export function csvOf(events: UsageEvent[]): string {
  const rows: (string | number)[][] = [
    [
      t("csvTime"),
      t("csvModel"),
      t("csvProvider"),
      t("csvAccount"),
      t("csvKey"),
      t("csvFailed"),
      t("csvStatus"),
      t("csvLatency"),
      t("csvTokens"),
      t("csvTrace"),
    ],
    ...events.map((e) => [
      e.timestamp,
      e.model,
      e.provider ?? "",
      e.account ?? "",
      e.key_id ?? "",
      String(e.failed),
      e.status ?? "",
      e.latency_ms,
      e.tokens?.total_tokens ?? "",
      e.id ?? "",
    ]),
  ];
  return `${rows.map((row) => row.map(csvField).join(",")).join("\r\n")}\r\n`;
}

/** The file's name: pluxy-requests-2026-11-25.csv, by the local date. */
export function csvName(now = new Date()): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `pluxy-requests-${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}.csv`;
}

// ---- Usage's ranges and local days ------------------------------------------------------------

export type Range = "24h" | "7d" | "30d";
export const RANGES: readonly Range[] = ["24h", "7d", "30d"];

/** How many local days a range shows; the 24 hours show hours. */
export const rangeDays = (range: Range) => (range === "7d" ? 7 : range === "30d" ? 30 : 0);

/** The query of GET /usage for a range, in hourly steps: 24 hours back, or local midnight 6 or 29 days ago. */
export function usageSince(range: Range, now = new Date()): string {
  if (range === "24h") return "24h";
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (rangeDays(range) - 1));
  return start.toISOString();
}

/** One bar of the chart: an hour or a local day. */
export interface Bar {
  /** When it starts. */
  start: Date;
  requests: number;
  failed: number;
  tokens: number;
}

/** The 24 hours, as core's hourly steps. */
export function hourBars(series: UsageBucket[]): Bar[] {
  return series.map((b) => ({
    start: new Date(b.start),
    requests: b.requests,
    failed: b.failure_count,
    tokens: b.total_tokens,
  }));
}

/**
 * Hourly steps added into the last days local days, oldest first, today last. A step counts in
 * the local day it starts in; a day with no step has zeros.
 */
export function localDays(series: UsageBucket[], days: number, now = new Date()): Bar[] {
  const bars: Bar[] = [];
  for (let i = days - 1; i >= 0; i--) {
    bars.push({
      start: new Date(now.getFullYear(), now.getMonth(), now.getDate() - i),
      requests: 0,
      failed: 0,
      tokens: 0,
    });
  }
  for (const b of series) {
    const at = new Date(b.start);
    const day = new Date(at.getFullYear(), at.getMonth(), at.getDate()).getTime();
    const bar = bars.find((x) => x.start.getTime() === day);
    if (!bar) continue;
    bar.requests += b.requests;
    bar.failed += b.failure_count;
    bar.tokens += b.total_tokens;
  }
  return bars;
}

// ---- opening one request ----------------------------------------------------------------------

/** How many requests are read at a time while looking for one, and how many reads at most. */
export const FIND_PAGE = 1000;
export const FIND_PAGES = 10;

export type Found =
  | { rows: UsageEvent[]; more: boolean; found: boolean }
  | { error: string }
  | { unauthorized: true };

/**
 * The newest requests down to the one with this trace ID, read FIND_PAGE at a time (at most
 * FIND_PAGES reads) through read(before, limit). found: the request was among them, and rows end
 * with it. Not found: rows are the first show of them, and the request is no longer kept.
 */
export async function readDownTo(
  trace: string,
  show: number,
  read: (before: number, limit: number) => Promise<Reply<UsageEventsReply>>,
): Promise<Found> {
  const all: UsageEvent[] = [];
  let before = 0;
  let more = false;
  for (let page = 0; page < FIND_PAGES; page++) {
    const r = await read(before, FIND_PAGE);
    if (r.status === 401) return { unauthorized: true };
    const why = problem(r);
    if (why !== undefined || !r.body) return { error: why ?? "" };
    all.push(...r.body.events);
    more = r.body.more;
    const at = all.findIndex((e) => e.id === trace);
    if (at >= 0) return { rows: all.slice(0, at + 1), more: more || at < all.length - 1, found: true };
    if (!more || r.body.events.length === 0) break;
    before = r.body.next;
  }
  return { rows: all.slice(0, show), more: more || all.length > show, found: false };
}

// ---- Usage's figures --------------------------------------------------------------------------

const compact = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
/** A big count as a person says it: 1.2M, 34K, 980. */
export const shortCount = (n: number) => compact.format(n);

/** The share that succeeded, to one decimal and rounded down, so only all of them reads 100%. */
export function succeededPercent(success: number, total: number): string {
  return `${Math.floor((success * 1000) / total) / 10}%`;
}

/**
 * The summary's figures as one line: requests, the share that succeeded, tokens (and those from
 * the cache) and the average time. A part core does not report is left out. time says a length
 * of time in milliseconds as a person reads it.
 */
export function figuresOf(s: UsageSummary, time: (ms: number) => string): string {
  if (s.total_requests === 0) return "";
  const parts = [
    s.total_requests === 1
      ? t("figRequestsOne")
      : t("figRequests", { count: s.total_requests.toLocaleString() }),
    t("figSucceeded", { percent: succeededPercent(s.success_count, s.total_requests) }),
  ];
  const tokens = s.tokens?.total_tokens ?? 0;
  const cached = s.tokens?.cached_tokens ?? 0;
  if (tokens > 0) {
    parts.push(
      cached > 0
        ? t("figCached", { tokens: shortCount(tokens), cached: shortCount(cached) })
        : t("figTokens", { tokens: shortCount(tokens) }),
    );
  }
  if (s.average_latency_ms) parts.push(t("figTime", { time: time(s.average_latency_ms) }));
  return parts.join(" · ");
}

/** The model most failures asked for, when it is plain: at least 3 failed and one model has half or more. */
export function hotspotOf(
  s: Pick<UsageSummary, "failure_count" | "models">,
): { model: string; text: string } | undefined {
  const total = s.failure_count;
  if (total < 3) return undefined;
  const top = [...s.models].sort((a: UsageGroup, b: UsageGroup) => b.failure_count - a.failure_count)[0];
  if (!top || top.failure_count * 2 < total) return undefined;
  return {
    model: top.name,
    text:
      top.failure_count === total
        ? t("hotspotAll", { total, model: top.name })
        : t("hotspotSome", { count: top.failure_count, total, model: top.name }),
  };
}
