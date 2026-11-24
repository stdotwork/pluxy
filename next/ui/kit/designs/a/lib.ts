// Direction A's rules and number formats, with nothing drawn: how an account's state reads on the
// board and what fixes it, how a usage window is paced, what path a request took, and the compact
// figures a dense screen needs. Everything here is a pure function of core's replies and a clock.

import {
  type Account,
  leftOut,
  providerMissing,
  type QuotaLimit,
  type UsageEvent,
  type UsageTried,
} from "../../api";

// ---- figures --------------------------------------------------------------------------------

/** A count in at most five characters: 1,284, 12.4k, 4.21M. */
export function compact(n: number): string {
  const v = Math.abs(n);
  if (v < 10_000) return Math.round(n).toLocaleString("en-US");
  if (v < 1_000_000) return `${trim(n / 1_000, v < 100_000 ? 1 : 0)}k`;
  if (v < 1_000_000_000) return `${trim(n / 1_000_000, v < 100_000_000 ? 2 : 1)}M`;
  return `${trim(n / 1_000_000_000, 2)}B`;
}

/** Rounded down to digits decimals, so a figure never claims more than there was. */
function trim(n: number, digits: number): string {
  const f = 10 ** digits;
  return (Math.floor(n * f) / f).toFixed(digits);
}

/** A latency: 812ms, 1.8s, 1m 04s. */
export function latency(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${trim(ms / 1000, ms < 10_000 ? 1 : 0)}s`;
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

/**
 * A wait, as its two largest units: 42s, 4m 12s, 2h 14m, 3d 4h. Seconds are rounded up, so a
 * wait that is not over never reads 0s.
 */
export function wait(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
  return `${Math.floor(s / 86_400)}d ${Math.floor((s % 86_400) / 3600)}h`;
}

/**
 * A wait that is hours or days away, without the seconds a countdown needs: 42s, 41m, 2h 14m,
 * 3d 4h. Past 99 days it reads 99d+.
 */
export function waitCoarse(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.ceil(s / 60)}m`;
  if (s >= 99 * 86_400) return "99d+";
  return wait(ms);
}

/** How long ago, in one unit: now, 12s, 4m, 3h, 2d. */
export function ago(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 2) return "now";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86_400)}d`;
}

/**
 * A share of 0 to 1 as a percentage with one decimal, rounded down: 99.96% of requests succeeding
 * reads 99.9%, never 100%, which is kept for when every one did.
 */
export function percent(share: number): string {
  if (share >= 1) return "100%";
  if (share <= 0) return "0%";
  return `${trim(share * 100, 1)}%`;
}

const two = (n: number) => String(n).padStart(2, "0");

/** A time of day on a 24-hour clock, to the second: 23:04:11. */
export function clock(at: string | number | Date): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return "--:--:--";
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

/** A day and time for something not today: Nov 23 23:04. */
export function stamp(at: string | number | Date, now = Date.now()): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return "";
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  if (new Date(now).toDateString() === d.toDateString()) return hm;
  return `${d.toLocaleDateString("en-US", { month: "short", day: "numeric" })} ${hm}`;
}

// ---- an account on the board ----------------------------------------------------------------

/**
 * How an account stands, in the board's words. The colours follow one rule: green serves, amber
 * waits on a clock and comes back by itself, red needs the person, grey is off by their choice.
 */
export type BoardState =
  | "serving"
  | "cooling"
  | "exhausted"
  | "sign-in"
  | "banned"
  | "broken"
  | "no-provider"
  | "off";

export type BoardTone = "ok" | "warn" | "danger" | "muted";

/** The one action that gets a stuck account serving again. */
export type Fix = "sign-in" | "clear-cooldown" | "enable" | "remove" | "extensions";

export interface Standing {
  state: BoardState;
  tone: BoardTone;
  /** The state in a word or two. */
  label: string;
  /** When the block ends (Unix ms), when core knows. */
  until?: number;
  /** Why, as core or the provider put it. */
  why?: string;
  fix?: Fix;
  /** Models held back while the account still serves the others, and when the last of them comes back. */
  coolingModels: number;
  modelsUntil?: number;
}

const time = (t?: string) => (t ? Date.parse(t) || 0 : 0);

/** How an account stands now. running is the extension IDs core runs, when known. */
export function standingOf(a: Account, now: number, running?: ReadonlySet<string>): Standing {
  const cooldowns = (a.cooldowns ?? []).filter((c) => time(c.until) > now);
  const coolingModels = cooldowns.filter((c) => c.scope === "model").length;
  const base = { coolingModels };
  if (a.disabled)
    return { ...base, coolingModels: 0, state: "off", tone: "muted", label: "disabled", fix: "enable" };
  const out = leftOut(a);
  const why = a.health?.why ?? a.reason ?? a.last_error?.message;
  const until = time(a.health?.until) || undefined;
  if (out === "broken")
    return { ...base, state: "broken", tone: "danger", label: "unreadable", why, fix: "remove" };
  if (out === "banned")
    return { ...base, state: "banned", tone: "danger", label: "banned", why, fix: "enable" };
  if (out === "needs_login" || a.state === "reauth_required")
    return {
      ...base,
      state: "sign-in",
      tone: "danger",
      label: "sign-in expired",
      why: a.reason ?? a.health?.why ?? a.renewal_error?.message,
      fix: "sign-in",
    };
  if (providerMissing(a, running))
    return {
      ...base,
      state: "no-provider",
      tone: "danger",
      label: "no provider",
      why: `Its extension ${a.extension} is not running.`,
      fix: "extensions",
    };
  if (out === "exhausted") return { ...base, state: "exhausted", tone: "warn", label: "used up", until, why };
  // A block on the whole account: a cooldown that is not one model's. Core calls an account
  // "cooling" for a model's cooldown too, though it still serves its other models.
  const whole = Math.max(
    time(a.cooldown_until),
    ...cooldowns.filter((c) => c.scope !== "model").map((c) => time(c.until)),
    a.health?.state === "cooling" && coolingModels === 0 ? time(a.health.until) || now + 1 : 0,
  );
  if (whole > now)
    return {
      ...base,
      state: "cooling",
      tone: "warn",
      label: "cooling down",
      until: whole,
      why:
        a.health?.why ?? cooldowns.find((c) => c.scope !== "model")?.error?.message ?? a.last_error?.message,
      fix: "clear-cooldown",
    };
  if (coolingModels > 0) {
    const held = cooldowns.filter((c) => c.scope === "model");
    return {
      ...base,
      state: "serving",
      tone: "ok",
      label: "serving",
      modelsUntil: Math.max(...held.map((c) => time(c.until))),
      why: held[0]?.error?.message ?? a.health?.why,
      fix: "clear-cooldown",
    };
  }
  return { ...base, state: "serving", tone: "ok", label: "serving" };
}

/** The states that need the person, in the order the board lists them first. */
export const NEEDS_YOU: readonly BoardState[] = ["sign-in", "banned", "broken", "no-provider"];

export interface BoardCounts {
  total: number;
  serving: number;
  waiting: number;
  needYou: number;
  off: number;
}

export function countBoard(standings: Standing[]): BoardCounts {
  const n: BoardCounts = { total: standings.length, serving: 0, waiting: 0, needYou: 0, off: 0 };
  for (const s of standings) {
    if (s.state === "serving") n.serving++;
    else if (s.state === "off") n.off++;
    else if (NEEDS_YOU.includes(s.state)) n.needYou++;
    else n.waiting++;
  }
  return n;
}

/** What a fix's button says. */
export const FIX_LABEL: Record<Fix, string> = {
  "sign-in": "Sign in again",
  "clear-cooldown": "Clear cooldown",
  enable: "Enable",
  remove: "Remove",
  extensions: "Open extensions",
};

/** A provider's name without its publisher: "test/alpha" reads "alpha" where space is short. */
export function shortProvider(id?: string): string {
  if (!id) return "unknown";
  return id.slice(id.lastIndexOf("/") + 1);
}

// ---- usage windows --------------------------------------------------------------------------

export interface Pace {
  /** The share of the window that is used, 0 to 1. */
  used: number;
  /** The share of the window's time that has passed, 0 to 1, when its length and reset are known. */
  elapsed?: number;
  /** Time left to the reset, in ms, when known. */
  resetsIn?: number;
  /**
   * steady: on course to last until the reset. ahead: being used faster than the clock, so it
   * runs out first at this rate. full: used up. Unknown elapsed counts as steady below 90% used.
   */
  course: "steady" | "ahead" | "full";
}

/** How a usage window is going: what is used against how much of its time has passed. */
export function paceOf(limit: QuotaLimit, now: number): Pace | undefined {
  let used: number | undefined;
  if (limit.used_percent !== undefined) used = limit.used_percent / 100;
  else if (limit.used !== undefined && limit.limit) used = limit.used / limit.limit;
  else if (limit.remaining !== undefined && limit.limit) used = 1 - limit.remaining / limit.limit;
  if (used === undefined || limit.unlimited) return undefined;
  used = Math.max(0, Math.min(1, used));
  const resetsIn = limit.resets_at ? limit.resets_at * 1000 - now : undefined;
  let elapsed: number | undefined;
  if (resetsIn !== undefined && limit.window_seconds && resetsIn >= 0) {
    elapsed = Math.max(0, Math.min(1, 1 - resetsIn / (limit.window_seconds * 1000)));
  }
  let course: Pace["course"] = "steady";
  if (used >= 1) course = "full";
  // A tenth of the window ahead of the clock is worth a warning; less is noise early in a window.
  else if (elapsed !== undefined ? used > elapsed + 0.1 && used >= 0.5 : used >= 0.9) course = "ahead";
  return { used, elapsed, resetsIn: resetsIn !== undefined && resetsIn >= 0 ? resetsIn : undefined, course };
}

/** A window's short name for a column head: "5-hour limit" reads 5h, "Weekly limit" wk. */
export function windowName(limit: QuotaLimit): string {
  const s = limit.window_seconds;
  if (s) {
    if (s % 604_800 === 0) return s === 604_800 ? "wk" : `${s / 604_800}wk`;
    if (s % 86_400 === 0) return `${s / 86_400}d`;
    if (s % 3600 === 0) return `${s / 3600}h`;
    return `${Math.round(s / 60)}m`;
  }
  return (limit.label ?? limit.id).replace(/\s*limit$/i, "");
}

/** The windows worth a bar: metered ones, shortest window first; counters such as credits are left out. */
export function meteredWindows(limits: QuotaLimit[] | undefined, now: number): QuotaLimit[] {
  return (limits ?? [])
    .filter((l) => !l.feature && paceOf(l, now) !== undefined)
    .sort(
      (a, b) => (a.window_seconds ?? Number.MAX_SAFE_INTEGER) - (b.window_seconds ?? Number.MAX_SAFE_INTEGER),
    );
}

// ---- a request's path -----------------------------------------------------------------------

export interface Hop {
  /** The account as core recorded it: its public ID for a recorded try, its ID otherwise. */
  account: string;
  provider: string;
  model?: string;
  /** "ok", or the code of the error that ended the try, such as quota or rate_limit. */
  result: string;
  ok: boolean;
}

/**
 * Every try of a request in order. Core records the tries only when there was more than one or the
 * request failed; a request served at the first try has one hop, made from the request itself.
 */
export function pathOf(e: UsageEvent): Hop[] {
  const hop = (t: UsageTried): Hop => ({ ...t, ok: t.result === "ok" });
  if (e.tried?.length) return e.tried.map(hop);
  if (!e.account) return [];
  return [
    {
      account: e.account,
      provider: e.provider ?? "",
      model: e.upstream_model,
      result: e.failed ? (e.error_code ?? "failed") : "ok",
      ok: !e.failed,
    },
  ];
}

/** Whether a request went past its first account: the tail and the list mark these. */
export const fellBack = (e: UsageEvent): boolean =>
  (e.tried?.length ?? 0) > 1 || (e.attempts ?? 1) > 1 || (e.chain_step ?? 1) > 1;

/** Why a request ended where it did, in a sentence for the detail pane. */
export function verdict(e: UsageEvent, name: (account: string) => string = (a) => a): string {
  const hops = pathOf(e);
  const last = hops[hops.length - 1];
  if (!last) return e.failed ? "No account could take this request." : "Served.";
  const failed = hops.filter((h) => !h.ok);
  if (!e.failed && failed.length === 0) {
    return e.chain_step && e.chain_step > 1
      ? `Served by ${name(last.account)} at step ${e.chain_step} of the model's fallback chain: no account of the earlier ${e.chain_step === 2 ? "step" : "steps"} could be tried.`
      : `Served by ${name(last.account)} at the first try.`;
  }
  const reasons = failed.map((h) => `${name(h.account)} (${h.result.replace(/_/g, " ")})`).join(", then ");
  if (!e.failed)
    return `Fell back ${failed.length === 1 ? "once" : `${failed.length} times`}: ${reasons}. Served by ${name(last.account)}.`;
  return `Failed after ${hops.length === 1 ? "one try" : `${hops.length} tries`}: ${reasons}.`;
}

// ---- ranges ---------------------------------------------------------------------------------

export interface Range {
  id: string;
  label: string;
  /** Core's `since`. */
  since: string;
  /** Core's `bucket`, and how many of them cover the range. */
  bucket: string;
  buckets: number;
  seconds: number;
}

export const RANGES: readonly Range[] = [
  { id: "1h", label: "1h", since: "1h", bucket: "1m", buckets: 60, seconds: 3600 },
  { id: "6h", label: "6h", since: "6h", bucket: "5m", buckets: 72, seconds: 21_600 },
  { id: "24h", label: "24h", since: "24h", bucket: "20m", buckets: 72, seconds: 86_400 },
  { id: "7d", label: "7d", since: "7d", bucket: "2h", buckets: 84, seconds: 604_800 },
  { id: "30d", label: "30d", since: "30d", bucket: "12h", buckets: 60, seconds: 2_592_000 },
];

export const rangeOf = (id: string | null | undefined): Range =>
  RANGES.find((r) => r.id === id) ?? (RANGES[2] as Range);

export const usageQuery = (r: Range, extra = ""): string =>
  `since=${r.since}&bucket=${r.bucket}&buckets=${r.buckets}${extra}`;

/** A request rate for a range, in the unit that keeps it readable: 12.4/min, 3.1/h, 42/day. */
export function rate(requests: number, seconds: number): string {
  if (requests === 0 || seconds <= 0) return "0/min";
  const perMin = (requests / seconds) * 60;
  if (perMin >= 1) return `${trim(perMin, perMin < 100 ? 1 : 0)}/min`;
  const perHour = perMin * 60;
  if (perHour >= 1) return `${trim(perHour, 1)}/h`;
  return `${trim(perHour * 24, 1)}/day`;
}

// ---- finding things -------------------------------------------------------------------------

/**
 * How well text matches what was typed, for the command menu and filters: every typed word must
 * be in it. Higher is better; 0 is no match. A match at the start of the text or of a word in it
 * ranks above one in the middle.
 */
export function matchScore(text: string, typed: string): number {
  const hay = text.toLowerCase();
  const words = typed.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return 1;
  let score = 0;
  for (const w of words) {
    const at = hay.indexOf(w);
    if (at < 0) return 0;
    score += at === 0 ? 3 : /[\s/_.:-]/.test(hay[at - 1] ?? "") ? 2 : 1;
  }
  return score;
}
