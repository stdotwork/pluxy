// Direction C's pure helpers: the time ranges, the address (page and filters in the hash), how
// numbers and times are written, what state an account is in and what fixes it, and which colour
// and letters a provider gets.

import { type Account, leftOut, providerMissing, type QuotaLimit } from "../../api";
import type { IconName } from "./icons";

// ---- ranges --------------------------------------------------------------------------------

export type RangeId = "1h" | "24h" | "7d" | "30d";

export interface Range {
  id: RangeId;
  /** As a sentence part: "in the last 24 hours". */
  words: string;
  /** The series core is asked for: step and number of steps. */
  series: string;
}

export const RANGES: Range[] = [
  { id: "1h", words: "the last hour", series: "bucket=5m&buckets=12" },
  { id: "24h", words: "the last 24 hours", series: "bucket=1h&buckets=24" },
  { id: "7d", words: "the last 7 days", series: "bucket=6h&buckets=28" },
  { id: "30d", words: "the last 30 days", series: "bucket=24h&buckets=30" },
];

export const rangeOf = (id: string | null | undefined): Range =>
  RANGES.find((r) => r.id === id) ?? (RANGES[1] as Range);

// ---- the address ---------------------------------------------------------------------------

/** What a page is asked to show: the path after "#/" and the filters after its "?". */
export interface Address {
  /** The path's parts, such as ["accounts", "aurora-pro"]. */
  parts: string[];
  params: URLSearchParams;
}

export function parseAddress(path: string): Address {
  const [before, after] = path.split("?", 2);
  return {
    parts: (before ?? "").split("/").filter(Boolean).map(decodeURIComponent),
    params: new URLSearchParams(after ?? ""),
  };
}

/** A hash for a page with filters; empty and undefined values are left out. */
export function hashFor(page: string, params: Record<string, string | undefined> = {}): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const text = q.toString();
  return `#/${page}${text ? `?${text}` : ""}`;
}

/** The filters a request can be picked by, as the address and core's usage calls name them. */
export const FILTERS = ["key", "model", "provider", "account", "failed"] as const;
export type FilterName = (typeof FILTERS)[number];
export type Filters = Partial<Record<FilterName, string>>;

export function filtersOf(params: URLSearchParams): Filters {
  const out: Filters = {};
  for (const name of FILTERS) {
    const v = params.get(name);
    if (v) out[name] = v;
  }
  return out;
}

/** The filters as core's usage query parameters ("&model=x&key=y"), or "". */
export function filterQuery(filters: Filters): string {
  return FILTERS.map((name) => (filters[name] ? `&${name}=${encodeURIComponent(filters[name])}` : "")).join(
    "",
  );
}

export const hasFilters = (filters: Filters) => FILTERS.some((name) => !!filters[name]);

// ---- numbers and times ---------------------------------------------------------------------

const whole = new Intl.NumberFormat("en-US");

export const count = (n: number) => whole.format(n);

/** A large number in few characters: 611,741 as "612k". */
export function compact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  if (n < 10_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  return `${Math.round(n / 1_000_000)}M`;
}

/** A share of a whole as a percentage; "<1%" for a share that is not zero but rounds to it. */
export function share(part: number, wholeOf: number): string {
  if (wholeOf <= 0) return "0%";
  const p = (part / wholeOf) * 100;
  if (p > 0 && p < 1) return "<1%";
  if (p > 99 && p < 100) return ">99%";
  return `${Math.round(p)}%`;
}

/** How many of a hundred requests were answered, floored so that 99.96% is not "100%". */
export function answered(ok: number, total: number): string {
  if (total <= 0) return "–";
  const p = Math.floor((ok / total) * 1000) / 10;
  return `${p % 1 === 0 ? p.toFixed(0) : p.toFixed(1)}%`;
}

export function latency(ms: number | undefined): string {
  if (ms === undefined) return "–";
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
}

/** A length of time as the UI's terms write it: 45s, 12m, 2h 58m, 3d 4h. */
export function duration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) {
    const m = Math.floor((s % 3600) / 60);
    return m ? `${Math.floor(s / 3600)}h ${m}m` : `${Math.floor(s / 3600)}h`;
  }
  const h = Math.floor((s % 86400) / 3600);
  return h ? `${Math.floor(s / 86400)}d ${h}h` : `${Math.floor(s / 86400)}d`;
}

/** A time or Unix seconds as milliseconds, or undefined. */
export function millis(t: string | number | undefined): number | undefined {
  if (t === undefined || t === "" || t === 0) return undefined;
  const ms = typeof t === "number" ? t * 1000 : Date.parse(t);
  return Number.isFinite(ms) ? ms : undefined;
}

/** How long ago a time was: "now", "4m ago", "3h ago", "2d ago". */
export function ago(t: string, now: number): string {
  const ms = millis(t);
  if (ms === undefined) return "";
  const s = Math.round((now - ms) / 1000);
  if (s < 10) return "now";
  return `${duration(s).split(" ")[0]} ago`;
}

export function clock(t: string | number | undefined): string {
  const ms = millis(t);
  if (ms === undefined) return "";
  return new Date(ms).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function clockExact(t: string | number | undefined): string {
  const ms = millis(t);
  return ms === undefined ? "" : new Date(ms).toLocaleString();
}

// ---- an account's condition ----------------------------------------------------------------

export type Tone = "ok" | "warn" | "danger" | "off";

export type Fix = "signin" | "clear" | "enable" | "extensions" | "remove";

/** What an account is doing now, in the UI's words, and the one thing that would fix it. */
export interface Condition {
  key:
    | "available"
    | "cooling"
    | "exhausted"
    | "needs_login"
    | "banned"
    | "broken"
    | "disabled"
    | "no_provider";
  /** Two or three words: "Available", "Limit used up". */
  word: string;
  tone: Tone;
  icon: IconName;
  /** Whether requests reach the account at all. */
  serving: boolean;
  /** When the block ends, in milliseconds, if core knows. */
  until?: number;
  /** One sentence on why, for people. */
  why?: string;
  fix?: Fix;
}

export const FIX_WORDS: Record<Fix, string> = {
  signin: "Sign in again",
  clear: "Clear cooldown",
  enable: "Enable",
  extensions: "Open Extensions",
  remove: "Remove",
};

function latestCooldown(
  a: Account,
  now: number,
): { until: number; model?: string; why?: string } | undefined {
  let best: { until: number; model?: string; why?: string } | undefined;
  for (const c of a.cooldowns ?? []) {
    const until = Date.parse(c.until) || 0;
    if (until > now && (!best || until > best.until)) best = { until, model: c.model, why: c.error?.message };
  }
  const whole = Date.parse(a.cooldown_until ?? "") || 0;
  if (whole > now && (!best || whole > best.until)) best = { until: whole };
  return best;
}

export function conditionOf(a: Account, running: ReadonlySet<string> | undefined, now: number): Condition {
  if (a.disabled) {
    return {
      key: "disabled",
      word: "Disabled",
      tone: "off",
      icon: "off",
      serving: false,
      why: "Turned off here. No request is sent to it.",
      fix: "enable",
    };
  }
  const out = leftOut(a);
  const until = millis(a.health?.until);
  if (out === "broken") {
    return {
      key: "broken",
      word: "Unreadable auth file",
      tone: "danger",
      icon: "broken",
      serving: false,
      why: a.health?.why ?? "pluxy cannot read this account's auth file.",
      fix: "remove",
    };
  }
  if (out === "banned") {
    return {
      key: "banned",
      word: "Closed by provider",
      tone: "danger",
      icon: "banned",
      serving: false,
      why: sentence(a.health?.why) ?? "The provider closed this account.",
      fix: "enable",
    };
  }
  if (out === "needs_login" || a.state === "reauth_required") {
    return {
      key: "needs_login",
      word: "Needs sign-in",
      tone: "danger",
      icon: "signIn",
      serving: false,
      why: sentence(a.health?.why ?? a.reason) ?? "The sign-in is no longer accepted.",
      fix: a.source === "login" ? "signin" : undefined,
    };
  }
  if (out === "exhausted") {
    return {
      key: "exhausted",
      word: "Limit used up",
      tone: "warn",
      icon: "exhausted",
      serving: false,
      until: until && until > now ? until : undefined,
      why: sentence(a.health?.why) ?? "A usage limit is used up.",
      fix: "clear",
    };
  }
  if (providerMissing(a, running)) {
    return {
      key: "no_provider",
      word: "No provider running",
      tone: "warn",
      icon: "noProvider",
      serving: false,
      why: `${a.extension ?? "Its extension"} is not running, so no request reaches this account.`,
      fix: "extensions",
    };
  }
  const cooling = latestCooldown(a, now);
  if (cooling || (a.health?.state === "cooling" && until && until > now)) {
    return {
      key: "cooling",
      word: "Cooling down",
      tone: "warn",
      icon: "cooling",
      serving: true,
      until: cooling?.until ?? until,
      why: cooling?.model
        ? `Paused for the model ${cooling.model} after a rate limit. It still serves its other models.`
        : (sentence(cooling?.why ?? a.health?.why) ?? "Paused after an error."),
      fix: "clear",
    };
  }
  return { key: "available", word: "Available", tone: "ok", icon: "ok", serving: true };
}

/** Core's reasons are lower-case fragments: as a sentence. */
function sentence(text: string | undefined): string | undefined {
  const s = text?.trim();
  if (!s) return undefined;
  const head = s.charAt(0).toUpperCase() + s.slice(1);
  return /[.!?]$/.test(head) ? head : `${head}.`;
}

/** "Resets in 2h 58m", "Until 14:05", or nothing when core knows no end. */
export function untilWords(c: Condition, now: number): string | undefined {
  if (!c.until || c.until <= now) return undefined;
  const left = duration((c.until - now) / 1000);
  return c.key === "exhausted" ? `Resets in ${left}` : `${left} left`;
}

/** The order accounts are listed in when what needs someone comes first. */
export const CONDITION_RANK: Record<Condition["key"], number> = {
  needs_login: 0,
  broken: 1,
  banned: 2,
  no_provider: 3,
  exhausted: 4,
  cooling: 5,
  available: 6,
  disabled: 7,
};

// ---- usage windows -------------------------------------------------------------------------

/** A limit's label by the UI's terms: a window of a well-known length always gets its name. */
export function limitName(l: QuotaLimit): string {
  const s = l.window_seconds ?? 0;
  if (s === 18000) return "5-hour limit";
  if (s === 86400) return "Daily limit";
  if (s === 604800) return "Weekly limit";
  if (s >= 28 * 86400 && s <= 31 * 86400) return "Monthly limit";
  return l.label || l.id;
}

export function limitPercent(l: QuotaLimit): number | undefined {
  if (l.unlimited) return undefined;
  if (l.used_percent !== undefined) return l.used_percent;
  if (l.limit && l.limit > 0 && (l.used !== undefined || l.remaining !== undefined)) {
    return ((l.used ?? l.limit - (l.remaining ?? 0)) / l.limit) * 100;
  }
  return undefined;
}

/** How much of the window's time has gone, 0 to 100, when core says when it ends and how long it is. */
export function limitElapsed(l: QuotaLimit, now: number): number | undefined {
  if (!l.resets_at || !l.window_seconds) return undefined;
  const left = l.resets_at - now / 1000;
  if (left < 0 || left > l.window_seconds) return undefined;
  return ((l.window_seconds - left) / l.window_seconds) * 100;
}

// ---- providers -----------------------------------------------------------------------------

/** The display names the UI's terms fix, by the extension's own name. */
const PROVIDER_NAMES: Record<string, string> = {
  claude: "Claude",
  codex: "Codex",
  gemini: "Gemini",
  devin: "Devin",
  kimi: "Kimi",
  xai: "xAI",
  meta: "Muse (Meta)",
  vertex: "Vertex AI",
  aistudio: "AI Studio",
  "openai-compatible": "OpenAI Compatible",
};

export interface Provider {
  /** The extension's ID, such as pluxy/claude. */
  id: string;
  name: string;
  /** Two letters that go wherever the colour goes. */
  letters: string;
  /** Its colour, 0 to 4; 5 is the shared colour of every provider past the fifth. */
  slot: number;
}

export function providerName(id: string): string {
  const own = id.split("/").pop() ?? id;
  const known = PROVIDER_NAMES[own];
  if (known) return known;
  return own.charAt(0).toUpperCase() + own.slice(1);
}

const lettersOf = (name: string) => {
  const words = name.split(/[\s(-]+/).filter(Boolean);
  const two = words.length > 1 ? `${words[0]?.charAt(0)}${words[1]?.charAt(0)}` : name.slice(0, 2);
  return two.charAt(0).toUpperCase() + two.charAt(1).toLowerCase();
};

/**
 * The providers a core has, each with its colour. Colours go to the providers with accounts
 * first, by their IDs in order, so a provider keeps its colour whatever is filtered or selected;
 * an account of one more provider can move them.
 */
export class Providers {
  private readonly byId = new Map<string, Provider>();
  readonly list: Provider[];

  /** first: the providers that have accounts; rest: the other extensions core runs. */
  constructor(first: Iterable<string>, rest: Iterable<string> = []) {
    const head = [...new Set(first)].filter(Boolean).sort();
    const sorted = [...head, ...[...new Set(rest)].filter((id) => id && !head.includes(id)).sort()];
    this.list = sorted.map((id, i) => {
      const name = providerName(id);
      return { id, name, letters: lettersOf(name), slot: Math.min(i, 5) };
    });
    for (const p of this.list) this.byId.set(p.id, p);
  }

  of(id: string | undefined): Provider {
    const known = id ? this.byId.get(id) : undefined;
    if (known) return known;
    const name = id ? providerName(id) : "Unknown";
    return { id: id ?? "", name, letters: id ? lettersOf(name) : "?", slot: 5 };
  }
}
