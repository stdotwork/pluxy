// What direction B says, and the rules it says it by: an account's condition in one of four
// groups (needs you, waiting, serving, turned off), durations and numbers as the UI terms write
// them, provider names, and why a request went where it did, in words. Nothing here draws.

import {
  type Account,
  leftOut,
  providerMissing,
  type QuotaLimit,
  type UsageEvent,
  type UsageTried,
} from "../../api";

// ---- numbers and time -------------------------------------------------------------------------

/** A count with thousands separators. */
export const num = (n: number) => n.toLocaleString("en");

/** A large count in a few characters: 950, 1.2k, 48k, 3.1M. */
export function short(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  if (n < 10_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  return `${Math.round(n / 1_000_000)}M`;
}

const WORDS = ["No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine"];

/** A count that starts a sentence: a word up to nine, then figures. */
export const spell = (n: number) => WORDS[n] ?? num(n);

/** one or other, by count: plural(2, "account") is "2 accounts". */
export function plural(count: number, one: string, other = `${one}s`): string {
  return `${num(count)} ${count === 1 ? one : other}`;
}

/** A share as a whole percentage that never rounds a part to all or to nothing. */
export function percent(part: number, whole: number): string {
  if (whole <= 0) return "0%";
  const p = (part / whole) * 100;
  if (part > 0 && p < 1) return "under 1%";
  if (part < whole && p > 99) return "over 99%";
  return `${Math.round(p)}%`;
}

/** A length of time as the UI terms write it: 45s, 12m, 3h, 2d 4h. */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const minutes = Math.round(s / 60);
  if (minutes < 60) return `${minutes}m`;
  if (minutes < 24 * 60) {
    const h = Math.floor(minutes / 60);
    const m = minutes % 60;
    return m === 0 || h >= 6 ? `${h}h` : `${h}h ${m}m`;
  }
  const hours = Math.round(s / 3600);
  const d = Math.floor(hours / 24);
  const h = hours % 24;
  return h === 0 ? `${d}d` : `${d}d ${h}h`;
}

/** Milliseconds as a person reads a wait: 420 ms, 2.2 s, 1 min 5 s. */
export function latency(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`;
}

const CLOCK: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" };

/** The time of day, 24-hour: 14:05. */
export const clock = (t: number | string | Date) => new Date(t).toLocaleTimeString("en-GB", CLOCK);

const startOfDay = (t: number) => {
  const d = new Date(t);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
};

/** A day as a letter dates it: Today, Yesterday, or Friday 20 November. */
export function dayName(t: number | string, now = Date.now()): string {
  const day = startOfDay(new Date(t).getTime());
  const today = startOfDay(now);
  if (day === today) return "Today";
  if (day === today - 86_400_000) return "Yesterday";
  return new Date(day).toLocaleDateString("en-GB", { weekday: "long", day: "numeric", month: "long" });
}

/** A moment as "14:05" today, "tomorrow at 14:05", or "Friday at 14:05" within a week, else the date. */
export function moment(t: number, now = Date.now()): string {
  const day = startOfDay(t);
  const today = startOfDay(now);
  const days = Math.round((day - today) / 86_400_000);
  if (days === 0) return clock(t);
  if (days === 1) return `tomorrow at ${clock(t)}`;
  if (days === -1) return `yesterday at ${clock(t)}`;
  if (days > 1 && days < 7)
    return `${new Date(t).toLocaleDateString("en-GB", { weekday: "long" })} at ${clock(t)}`;
  return `${new Date(t).toLocaleDateString("en-GB", { day: "numeric", month: "long" })} at ${clock(t)}`;
}

/** Today's date as a letter's first line: Tuesday 24 November. */
export const dateline = (now = Date.now()) =>
  new Date(now).toLocaleDateString("en-GB", { weekday: "long", day: "numeric", month: "long" });

// ---- names ------------------------------------------------------------------------------------

// The UI terms' one display name per provider (docs/terms.md), by the extension's own name.
const PROVIDERS: Record<string, string> = {
  claude: "Claude",
  codex: "Codex",
  gemini: "Gemini",
  devin: "Devin",
  kimi: "Kimi",
  xai: "xAI",
  meta: "Muse (Meta)",
  vertex: "Vertex AI",
  "ai-studio": "AI Studio",
  aistudio: "AI Studio",
  "openai-compatible": "OpenAI Compatible",
};

/** A provider's display name from its extension ID: "pluxy/claude" is Claude, "demo/aurora" Aurora. */
export function providerName(extension: string | undefined): string {
  if (!extension) return "No provider";
  const own = extension.slice(extension.lastIndexOf("/") + 1);
  return PROVIDERS[own] ?? own.charAt(0).toUpperCase() + own.slice(1).replace(/[-_]/g, " ");
}

/**
 * A usage window's name by its length, as the UI terms name it whatever the provider calls it;
 * a limit on one feature or with no length keeps the provider's own label.
 */
export function windowName(limit: QuotaLimit): string {
  const s = limit.window_seconds ?? 0;
  if (limit.feature || limit.models?.length || s <= 0) return limit.label || limit.id;
  if (s === 5 * 3600) return "5-hour limit";
  if (s === 86400) return "Daily limit";
  if (s === 7 * 86400) return "Weekly limit";
  if (s >= 28 * 86400 && s <= 31 * 86400) return "Monthly limit";
  if (s % 86400 === 0) return `${s / 86400}-day limit`;
  if (s % 3600 === 0) return `${s / 3600}-hour limit`;
  if (s % 60 === 0) return `${s / 60}-minute limit`;
  return `${s}-second limit`;
}

/** The share of a window that is used, 0 to 100, when the provider says enough to know. */
export function usedPercent(limit: QuotaLimit): number | undefined {
  if (limit.unlimited) return undefined;
  let p = limit.used_percent;
  if (p === undefined && limit.limit && limit.limit > 0) {
    if (limit.used !== undefined) p = (limit.used / limit.limit) * 100;
    else if (limit.remaining !== undefined) p = ((limit.limit - limit.remaining) / limit.limit) * 100;
  }
  return p === undefined ? undefined : Math.max(0, Math.min(100, p));
}

// ---- an account's condition -------------------------------------------------------------------

export type Kind =
  | "serving"
  | "cooling"
  | "exhausted"
  | "needs_login"
  | "banned"
  | "broken"
  | "no_provider"
  | "off";

/** needs: only the person can fix it. waiting: it comes back by itself. */
export type Group = "needs" | "waiting" | "serving" | "off";

export const GROUPS: readonly Group[] = ["needs", "waiting", "serving", "off"];

export interface Condition {
  kind: Kind;
  group: Group;
  /** When a wait ends (Unix ms), if core knows. */
  until?: number;
  /** Core's own words for why. */
  why?: string;
  /** Models the account rests on for a while although it serves its others. */
  resting: { model: string; until: number }[];
}

const time = (t?: string) => (t ? Date.parse(t) || undefined : undefined);

/**
 * What state an account is in, by the kit's own rule for counting accounts (standing, in
 * kit/api/accounts.ts) cut finer: what only the person can fix, what waits and comes back by
 * itself, what serves, and what the person turned off.
 */
export function condition(a: Account, running?: ReadonlySet<string>, now = Date.now()): Condition {
  const resting = (a.cooldowns ?? [])
    .filter((c) => c.scope === "model" && c.model && (time(c.until) ?? 0) > now)
    .map((c) => ({ model: c.model ?? "", until: time(c.until) ?? 0 }));
  const why = a.health?.why ?? a.reason;
  if (a.disabled) return { kind: "off", group: "off", resting: [] };
  const out = leftOut(a);
  if (out === "broken") return { kind: "broken", group: "needs", why, resting: [] };
  if (out === "needs_login" || a.state === "reauth_required")
    return { kind: "needs_login", group: "needs", why: a.reason ?? why, resting: [] };
  if (out === "banned") return { kind: "banned", group: "needs", why, resting: [] };
  if (providerMissing(a, running)) return { kind: "no_provider", group: "needs", resting: [] };
  if (out === "exhausted")
    return { kind: "exhausted", group: "waiting", until: time(a.health?.until), why, resting: [] };
  const whole = Math.max(
    0,
    ...(a.cooldowns ?? []).filter((c) => c.scope !== "model").map((c) => time(c.until) ?? 0),
  );
  if (whole > now) return { kind: "cooling", group: "waiting", until: whole, why, resting };
  return { kind: "serving", group: "serving", resting };
}

/** One word or two for the condition, as a label beside its icon. */
export function conditionWord(c: Condition): string {
  switch (c.kind) {
    case "serving":
      return "Serving";
    case "cooling":
      return "Cooling down";
    case "exhausted":
      return "Limit used up";
    case "needs_login":
      return "Needs a new sign-in";
    case "banned":
      return "Closed by the provider";
    case "broken":
      return "Cannot be read";
    case "no_provider":
      return "Provider is not running";
    case "off":
      return "Turned off";
  }
}

const wait = (until: number | undefined, now: number) =>
  until && until > now ? ` It is back at ${moment(until, now)}, in ${duration(until - now)}.` : "";

/** The condition as a sentence or two: what happened, and what it means for requests. */
export function conditionSentence(a: Account, c: Condition, now = Date.now()): string {
  const provider = providerName(a.extension);
  switch (c.kind) {
    case "serving":
      if (c.resting.length > 0) {
        const first = c.resting[0];
        const models = c.resting.length === 1 ? (first?.model ?? "") : `${plural(c.resting.length, "model")}`;
        const until = Math.max(...c.resting.map((r) => r.until));
        return `Serving, but resting on ${models} for ${duration(until - now)} after a rate limit.`;
      }
      return "Serving.";
    case "cooling":
      return `${provider} asked pluxy to slow down, so it is resting.${wait(c.until, now)}`;
    case "exhausted":
      return `Its usage limit is used up, so requests go to your other accounts.${wait(c.until, now)}`;
    case "needs_login":
      return `${provider} no longer accepts its sign-in, so no request reaches it.`;
    case "banned":
      return `${provider} closed this account. pluxy sends it nothing until you enable it again.`;
    case "broken":
      return "pluxy cannot read this account's credential file, so it sends the account nothing.";
    case "no_provider":
      return `${provider} is not running, so no request reaches this account.`;
    case "off":
      return "You turned it off. It takes no requests.";
  }
}

/** The one thing that fixes a condition, when a button can. */
export type Fix = "sign_in" | "enable" | "clear_cooldown" | "open" | "extensions";

export function fixFor(a: Account, c: Condition): Fix | undefined {
  switch (c.kind) {
    case "needs_login":
      // A config account's credential is changed in core's config, which its page explains.
      return a.source === "login" ? "sign_in" : "open";
    case "banned":
    case "off":
      return "enable";
    case "cooling":
      return "clear_cooldown";
    case "broken":
      return "open";
    case "no_provider":
      return "extensions";
    case "serving":
      return c.resting.length > 0 ? "clear_cooldown" : undefined;
    case "exhausted":
      return undefined;
  }
}

export const FIX_LABEL: Record<Fix, string> = {
  sign_in: "Sign in again",
  enable: "Enable",
  clear_cooldown: "Clear cooldown",
  open: "See what to do",
  extensions: "Open Extensions",
};

// ---- the verdict ------------------------------------------------------------------------------

export type Tone = "ok" | "waiting" | "needs" | "unknown";

export interface Verdict {
  tone: Tone;
  /** The one sentence in large type. */
  headline: string;
  /** What follows it, in ordinary type. */
  rest: string;
}

export interface Counts {
  total: number;
  needs: number;
  waiting: number;
  serving: number;
  off: number;
}

export function countGroups(conditions: Condition[]): Counts {
  const n: Counts = { total: conditions.length, needs: 0, waiting: 0, serving: 0, off: 0 };
  for (const c of conditions) n[c.group]++;
  return n;
}

const are = (n: number) => (n === 1 ? "is" : "are");

/** The first screen's verdict on the accounts: what needs the person first, then what is fine. */
export function verdictOf(n: Counts, safeMode = false): Verdict {
  if (safeMode)
    return {
      tone: "needs",
      headline: "pluxy is in safe mode.",
      rest: "No provider runs in safe mode, so every request from your coding tools fails. Start pluxy without safe mode to serve again.",
    };
  if (n.total === 0)
    return {
      tone: "unknown",
      headline: "No accounts yet.",
      rest: "pluxy is running, but it has no account to send requests to. Add one and your coding tools can use it.",
    };
  const off = n.off > 0 ? ` ${spell(n.off)} ${are(n.off)} turned off.` : "";
  const waiting =
    n.waiting > 0
      ? ` ${spell(n.waiting)} ${are(n.waiting)} waiting for a limit to reset and ${n.waiting === 1 ? "comes" : "come"} back by ${n.waiting === 1 ? "itself" : "themselves"}.`
      : "";
  if (n.serving === 0) {
    return {
      tone: "needs",
      headline: "Nothing is serving.",
      rest: `Requests from your coding tools fail until an account serves again.${n.needs > 0 ? ` ${spell(n.needs)} ${n.needs === 1 ? "account needs" : "accounts need"} you.` : ""}${waiting}${off}`,
    };
  }
  if (n.needs > 0) {
    const others = n.serving === 1 ? "One other is serving" : `${spell(n.serving)} others are serving`;
    return {
      tone: "needs",
      headline: `${plural(n.needs, "account")} ${n.needs === 1 ? "needs" : "need"} you.`,
      rest: `${others}, so requests still go through.${waiting}${off}`,
    };
  }
  if (n.waiting > 0) {
    return {
      tone: "waiting",
      headline: "Nothing needs you.",
      rest: `${spell(n.serving)} ${n.serving === 1 ? "account is" : "accounts are"} serving.${waiting}${off}`,
    };
  }
  return {
    tone: "ok",
    headline:
      n.serving === 1
        ? "Your account is serving."
        : n.off > 0
          ? `${num(n.serving)} accounts are serving.`
          : `All ${num(n.serving)} accounts are serving.`,
    rest: off.trim(),
  };
}

// ---- why a request went where it did ----------------------------------------------------------

// What ended a try, as the rest of a sentence that starts with the account's name.
const RESULT: Record<string, string> = {
  quota: "had used up its limit",
  rate_limit: "was rate limited",
  auth: "was refused its sign-in",
  account_closed: "was closed by its provider",
  banned: "was closed by its provider",
  upstream: "got an error from the provider",
  timeout: "took too long",
  canceled: "was cancelled",
  not_found: "does not serve the model",
  overloaded: "was overloaded",
  bad_request: "was sent a request the provider refused",
};

/** What a try came to, after the account's name: "answered", or why it did not. */
export const triedResult = (t: UsageTried) =>
  t.result === "ok" ? "answered" : (RESULT[t.result] ?? `failed (${t.result})`);

// What ended a failed request, as a sentence about the request.
const FAILED: Record<string, string> = {
  not_found: "No account serves this model.",
  quota: "Every account that serves it had used up its limit.",
  rate_limit: "The provider rate limited it.",
  auth: "The provider refused the account's sign-in.",
  account_closed: "The provider has closed the account.",
  upstream: "The provider returned an error.",
  timeout: "The provider took too long.",
  canceled: "The client gave up before a reply came.",
  bad_request: "The provider refused the request as sent.",
  unauthorized: "The client key was not accepted.",
};

export interface Why {
  /** A few words for the list's line: "after 2 others", "fallback step 2", or nothing. */
  note?: string;
  /** The whole story in a sentence or two. */
  story: string;
  /** True when the request did not go straight to its first account. */
  detour: boolean;
}

/** Why a request went where it did. name turns an account's ID in the record into what is shown. */
export function whyOf(e: UsageEvent, name: (account: string) => string): Why {
  const tried = e.tried ?? [];
  const before = tried.slice(0, -1).filter((t) => t.result !== "ok");
  const step = e.chain_step ?? 0;
  const parts: string[] = [];
  let note: string | undefined;
  if (e.failed) {
    parts.push(FAILED[e.error_code ?? ""] ?? `It failed${e.error_code ? ` (${e.error_code})` : ""}.`);
    if (tried.length > 0) {
      parts.push(
        `pluxy tried ${tried.map((t) => `${name(t.account)}, which ${triedResult(t)}`).join("; then ")}.`,
      );
      if (tried.length > 1) note = `after ${plural(tried.length, "try", "tries")}`;
    }
  } else {
    const last = tried[tried.length - 1];
    const served = e.account ? name(e.account) : last ? name(last.account) : "an account";
    if (before.length === 0) {
      parts.push(`${served} was first in line and answered.`);
    } else {
      parts.push(
        `${before.map((t) => `${name(t.account)} ${triedResult(t)}`).join(", and ")}, so ${served} answered.`,
      );
      note = `after ${plural(before.length, "other")}`;
    }
  }
  if (step > 1) {
    parts.push(
      `That was step ${step} of the model's fallback chain${e.upstream_model && e.upstream_model !== e.model ? `, which asks for ${e.upstream_model}` : ""}.`,
    );
    note = note ? `${note}, fallback step ${step}` : `fallback step ${step}`;
  }
  const sentence = parts.join(" ");
  return {
    note,
    story: sentence.charAt(0).toUpperCase() + sentence.slice(1),
    detour: before.length > 0 || step > 1,
  };
}
