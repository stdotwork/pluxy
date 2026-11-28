// What the shared parts say about an account, worked out without drawing anything: its condition
// and the group it falls in, the sentence that says why, the one fix for it, its name, its
// provider's name, its plan's tier and the limits a row shows. AccountRow draws these; a page that
// names an account's condition elsewhere (a badge, a count) uses the same functions.

import { type Account, type AccountCooldown, leftOut, providerMissing } from "../api/accounts";
import type { ClientKey } from "../api/config";
import type { QuotaLimit } from "../api/quota";
import { stateText } from "../states";
import { strings } from "../strings";
import { shortDuration } from "../time";

const t = strings("parts.account", {
  needsYou: "Needs you",
  waiting: "Waiting",
  serving: "Serving",
  turnedOff: "Turned off",
  needsSignIn: "{provider} no longer accepts its sign-in.",
  keyRefused: "{provider} refused its key. Change it in pluxy's settings file.",
  closed: "{provider} closed this account. Check it with {provider}, then turn it back on.",
  cannotBeRead: "pluxy cannot read this account's saved sign-in.",
  providerStopped: "{provider} is not running, so nothing reaches this account.",
  limitUsedUp: "Its limit is used up.",
  limitUsedUpBack: "Its limit is used up. Back {when}, in {duration}.",
  rateLimited: "{provider} rate-limited it.",
  rateLimitedBack: "{provider} rate-limited it. Back {when}, in {duration}.",
  refused: "{provider} refused its sign-in.",
  refusedAgain: "{provider} refused its sign-in. Trying again {when}.",
  failing: "{provider} kept failing.",
  failingAgain: "{provider} kept failing. Trying again {when}.",
  modelCooling: "Cooling down on {model}, back in {duration}.",
  modelsCooling: "Cooling down on {count} models, back in {duration}.",
  youTurnedItOff: "You turned it off.",
  signInAgain: "Sign in again",
  turnOn: "Turn on",
  clearCooldown: "Clear cooldown",
  seeWhatToDo: "See what to do",
  openExtensions: "Open Extensions",
  noProvider: "No provider",
  unchecked: "Not checked",
  uncheckedWhy: "pluxy could not check whether {provider} is running.",
  at: "at {time}",
  tomorrowAt: "tomorrow at {time}",
  dayAt: "{day} at {time}",
  dateAt: "on {date} at {time}",
  fiveHours: "5h",
  day: "day",
  week: "wk",
  month: "mo",
  fiveHourLimit: "5-hour limit",
  dailyLimit: "Daily limit",
  weeklyLimit: "Weekly limit",
  monthlyLimit: "Monthly limit",
  hoursLimit: "{count}-hour limit",
  daysLimit: "{count}-day limit",
  minutesLimit: "{count}-minute limit",
  keyMasked: "Key {masked}",
});

// ---- condition --------------------------------------------------------------------------------

/** The four groups a list sorts accounts into: what only the person can fix first. */
export type Group = "needs" | "waiting" | "serving" | "off";

export const GROUPS: readonly Group[] = ["needs", "waiting", "serving", "off"];

/** A group's name, as a list's heading says it. */
export function groupWord(group: Group): string {
  return t(group === "needs" ? "needsYou" : group === "off" ? "turnedOff" : group);
}

/** What keeps an account from serving, or that it serves; one condition word each. */
export type ConditionKind =
  | "serving"
  | "cooling"
  | "exhausted"
  | "needs_login"
  | "banned"
  | "broken"
  | "no_provider"
  | "off";

export interface Condition {
  kind: ConditionKind;
  group: Group;
  /** When a wait ends (ms since 1970), if core says. */
  until?: number;
  /** The block an account cools down under: its kind (rate_limit, cooldown) and error code. */
  block?: { kind: string; code?: string };
  /** The models it cools down on while it serves the others, with when each is back. */
  resting: { model: string; until: number }[];
}

const ms = (time?: string) => {
  const n = time ? Date.parse(time) : Number.NaN;
  return Number.isNaN(n) ? undefined : n;
};

// The cooldown that ends last among those over the whole account (not one model).
function lastBlock(blocks: AccountCooldown[], now: number) {
  let last: { c: AccountCooldown; until: number } | undefined;
  for (const c of blocks) {
    const until = ms(c.until) ?? 0;
    if (c.scope !== "model" && until > now && (!last || until > last.until)) last = { c, until };
  }
  return last;
}

/**
 * What condition an account is in, by the kit's rule for counting accounts (api/accounts.ts,
 * standing) cut finer. Turned off comes first; then what only the person can fix (Needs you);
 * then what comes back by itself (Waiting); else it serves, maybe cooling down on some models.
 * running is the extensions core runs (runningExtensions); without it, no account reads
 * Provider stopped.
 */
export function accountCondition(a: Account, running?: ReadonlySet<string>, now = Date.now()): Condition {
  if (a.disabled) return { kind: "off", group: "off", resting: [] };
  const out = leftOut(a);
  if (out === "broken") return { kind: "broken", group: "needs", resting: [] };
  if (out === "needs_login" || a.state === "reauth_required")
    return { kind: "needs_login", group: "needs", resting: [] };
  if (out === "banned") return { kind: "banned", group: "needs", resting: [] };
  if (providerMissing(a, running)) return { kind: "no_provider", group: "needs", resting: [] };
  const blocks = a.cooldowns ?? [];
  const block = lastBlock(blocks, now);
  // Core's health reads exhausted for a quota block on one model too. A core that lists its
  // cooldowns says what is blocked: with no block over the whole account, only those models wait.
  const oneModel =
    out === "exhausted" &&
    a.cooldowns !== undefined &&
    !block &&
    blocks.some((c) => c.scope === "model" && (ms(c.until) ?? 0) > now);
  if ((out === "exhausted" && !oneModel) || block?.c.kind === "quota") {
    return { kind: "exhausted", group: "waiting", until: ms(a.health?.until) ?? block?.until, resting: [] };
  }
  const resting = blocks
    .filter((c) => c.scope === "model" && c.model && (ms(c.until) ?? 0) > now)
    .map((c) => ({ model: c.model ?? "", until: ms(c.until) ?? 0 }));
  if (block) {
    return {
      kind: "cooling",
      group: "waiting",
      until: block.until,
      block: { kind: block.c.kind, code: block.c.error?.code },
      resting,
    };
  }
  // A core that names the cooldown only by its health and its end. One that lists its cooldowns
  // has said what cools: its health reads cooling for a block on one model too.
  const until = ms(a.health?.until) ?? ms(a.cooldown_until);
  if (a.cooldowns === undefined && a.health?.state === "cooling" && until !== undefined && until > now) {
    return { kind: "cooling", group: "waiting", until, resting };
  }
  return { kind: "serving", group: "serving", resting };
}

const STATE: Record<ConditionKind, string> = {
  serving: "active",
  cooling: "cooling",
  exhausted: "exhausted",
  needs_login: "needs_login",
  banned: "banned",
  broken: "broken",
  no_provider: "no_provider",
  off: "disabled",
};

/** The state stateText and StateBadge read for a condition, of "account". */
export const conditionState = (c: Condition) => STATE[c.kind];

/** The condition in a word or two, the same as stateText(…, "account") says it. */
export const conditionWord = (c: Condition) => stateText(conditionState(c), "account");

/**
 * Whether an account's condition waits on core's status: one that is not turned off and needs
 * nothing only the person can fix serves or waits only if its provider runs (providerMissing).
 * While the running extensions are not known (running undefined), such a condition is not
 * settled: a page that could not read the status says so instead of "Serving".
 */
export const conditionUnchecked = (a: Account, c: Condition, running?: ReadonlySet<string>) =>
  running === undefined && !!a.extension && (c.group === "serving" || c.group === "waiting");

/** The word for a condition not checked yet, and why, for a mark and a reason. */
export const uncheckedWord = () => t("unchecked");
export const uncheckedReason = (a: Account) => t("uncheckedWhy", { provider: providerName(a.extension) });

/** A row that says why under the name: one that needs the person, waits, or cools down on a model. */
export const conditionTall = (c: Condition) =>
  c.group === "needs" || c.group === "waiting" || (c.kind === "serving" && c.resting.length > 0);

// ---- words ------------------------------------------------------------------------------------

/** The time of day, 24-hour: 14:05. */
export function clock(time: number | Date): string {
  return new Date(time).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
}

const startOfDay = (time: number) => {
  const d = new Date(time);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
};

/** A coming moment after "Back" or "Trying again": at 14:05, tomorrow at 14:05, Friday at 14:05. */
export function backWhen(time: number, now = Date.now()): string {
  const days = Math.round((startOfDay(time) - startOfDay(now)) / 86_400_000);
  const at = clock(time);
  if (days <= 0) return t("at", { time: at });
  if (days === 1) return t("tomorrowAt", { time: at });
  const d = new Date(time);
  if (days < 7) return t("dayAt", { day: d.toLocaleDateString(undefined, { weekday: "long" }), time: at });
  return t("dateAt", { date: d.toLocaleDateString(undefined, { day: "numeric", month: "long" }), time: at });
}

/** A provider's display name from its extension ID (docs/terms.md): "pluxy/claude" is Claude. */
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

export function providerName(extension: string | undefined): string {
  if (!extension) return t("noProvider");
  const own = extension.slice(extension.lastIndexOf("/") + 1);
  if (Object.hasOwn(PROVIDERS, own)) return PROVIDERS[own] as string;
  const words = own.replace(/[-_]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * Why an account is in its condition, as one sentence for under its name. Nothing for an account
 * that simply serves. The provider's name stays visible; the rest names no person.
 */
export function accountReason(a: Account, c: Condition, now = Date.now()): string | undefined {
  const provider = providerName(a.extension);
  const left = c.until !== undefined && c.until > now ? c.until : undefined;
  const back =
    left === undefined
      ? undefined
      : { when: backWhen(left, now), duration: shortDuration((left - now) / 1000) };
  switch (c.kind) {
    case "needs_login":
      return a.source === "config" ? t("keyRefused", { provider }) : t("needsSignIn", { provider });
    case "banned":
      return t("closed", { provider });
    case "broken":
      return t("cannotBeRead");
    case "no_provider":
      return t("providerStopped", { provider });
    case "exhausted":
      return back ? t("limitUsedUpBack", back) : t("limitUsedUp");
    case "cooling": {
      if (c.block?.kind === "rate_limit")
        return back ? t("rateLimitedBack", { provider, ...back }) : t("rateLimited", { provider });
      const refused = c.block?.code === "auth" || c.block?.code === "permission";
      if (refused)
        return back ? t("refusedAgain", { provider, when: back.when }) : t("refused", { provider });
      return back ? t("failingAgain", { provider, when: back.when }) : t("failing", { provider });
    }
    case "serving": {
      const first = c.resting[0];
      if (!first) return undefined;
      const duration = shortDuration((Math.max(...c.resting.map((r) => r.until)) - now) / 1000);
      return c.resting.length === 1
        ? t("modelCooling", { model: first.model, duration })
        : t("modelsCooling", { count: c.resting.length, duration });
    }
    case "off":
      return t("youTurnedItOff");
  }
}

// ---- the fix ----------------------------------------------------------------------------------

/** The address of an account's page, or of a page under it (sign-in). */
export const accountHref = (id: string, under?: string) =>
  `#/accounts/${encodeURIComponent(id)}${under ? `/${under}` : ""}`;

export interface Fix {
  label: string;
  /** primary: what only the person can repair; quiet: a choice (turning on); ghost: a small thing. */
  variant: "primary" | "quiet" | "ghost";
  /** A fix that leads to a page. */
  href?: string;
  /** A fix the page runs: onTurnOn or onClearCooldown. */
  run?: "turnOn" | "clearCooldown";
}

/** The one button that fixes an account's condition, if a button can. */
export function accountFix(a: Account, c: Condition): Fix | undefined {
  switch (c.kind) {
    case "needs_login":
      // An account from the settings file cannot sign in: its page says how to change its key.
      return a.source === "config"
        ? { label: t("seeWhatToDo"), variant: "primary", href: accountHref(a.id) }
        : { label: t("signInAgain"), variant: "primary", href: accountHref(a.id, "sign-in") };
    case "broken":
      return { label: t("seeWhatToDo"), variant: "primary", href: accountHref(a.id) };
    case "no_provider":
      return { label: t("openExtensions"), variant: "primary", href: "#/extensions" };
    // Turning an account on is a choice, not a repair: never the filled button.
    case "banned":
    case "off":
      return { label: t("turnOn"), variant: "quiet", run: "turnOn" };
    case "cooling":
      return { label: t("clearCooldown"), variant: "ghost", run: "clearCooldown" };
    case "serving":
      return c.resting.length > 0
        ? { label: t("clearCooldown"), variant: "ghost", run: "clearCooldown" }
        : undefined;
    case "exhausted":
      return undefined;
  }
}

// ---- names ------------------------------------------------------------------------------------

/** An account's name as a person knows it: its label, else its ID. Not masked. */
export const accountName = (a: Pick<Account, "id" | "label">) => a.label?.trim() || a.id;

/**
 * The address an account sends its requests to, as host and port (an API-key account keeps it as
 * its base_url setting); nothing for an account with none, or one that is not a web address.
 */
export function accountServer(a: Pick<Account, "settings">): string | undefined {
  const base = a.settings?.base_url;
  if (typeof base !== "string") return undefined;
  try {
    return new URL(base.trim()).host || undefined;
  } catch {
    return undefined;
  }
}

/**
 * A client key's name: its label, else its masked value ("Key sk…48"). keys are the keys as
 * GET /client-keys lists them; label is the event's own copy of the key's name. mask is for the
 * label, a name a person gave (usePrivacy().id); the masked value never goes through it.
 */
export function keyName(
  keyId: string | undefined,
  keys?: ClientKey[],
  label?: string,
  mask: (label: string) => string = (l) => l,
): string | undefined {
  const key = keyId ? keys?.find((k) => k.id === keyId) : undefined;
  const named = label?.trim() || key?.label?.trim();
  if (named) return mask(named);
  if (key) return t("keyMasked", { masked: key.masked.replace(/\*+/, "…") });
  return undefined;
}

// ---- plan -------------------------------------------------------------------------------------

/**
 * How much a plan weighs on a row, by what it costs: top (the largest plans and pay-as-you-go API
 * use: about $200 a month and up), high (the next: about $100), mid (any other paid plan) or free.
 */
export type PlanTier = "top" | "high" | "mid" | "free";

/**
 * The plan words each provider uses for its largest plans. A word means a different plan at each
 * provider (ChatGPT's Pro is its $200 plan, Claude's Pro its $20 one), so the tier comes from the
 * provider and the plan together, until extensions report a tier themselves.
 */
const TIERS: Record<string, { top: RegExp; high?: RegExp }> = {
  claude: { top: /^max\s*20x\b/, high: /^max\b/ },
  codex: { top: /^pro\b/ },
  gemini: { top: /^ultra\b/ },
  vertex: { top: /^ultra\b/ },
  "ai-studio": { top: /^ultra\b/ },
  aistudio: { top: /^ultra\b/ },
  xai: { top: /^(supergrok\s+heavy|api)\b/ },
  "openai-compatible": { top: /^(paid|api)\b/ },
};

/** Any provider not in TIERS, or a plan with no provider: the words of the largest plans anywhere. */
const ANY = { top: /^(max\s*20x|ultra|api|paid)\b/, high: /^max\b/ };

/**
 * The tier of a plan word at its provider (extension, as an account names it): see TIERS. Free is
 * free everywhere; a plan a provider's table does not place is mid. Themes colour the tiers by
 * data-plan-tier.
 */
export function planTier(plan: string, extension?: string): PlanTier {
  const p = plan.trim().toLowerCase();
  if (/^free\b/.test(p)) return "free";
  const own = extension ? extension.slice(extension.lastIndexOf("/") + 1) : "";
  const tiers = Object.hasOwn(TIERS, own) ? (TIERS[own] as (typeof TIERS)[string]) : ANY;
  if (tiers.top.test(p)) return "top";
  if (tiers.high?.test(p)) return "high";
  return "mid";
}

// ---- limits -----------------------------------------------------------------------------------

const HOUR = 3600;
const DAY = 86400;

const isWindow = (l: QuotaLimit) => !l.feature && !l.models?.length && (l.window_seconds ?? 0) > 0;

/** The windows a row shows: the two shortest that count requests (not one feature, not some models). */
export function rowWindows(limits: QuotaLimit[]): QuotaLimit[] {
  return limits
    .filter(isWindow)
    .sort((a, b) => (a.window_seconds ?? 0) - (b.window_seconds ?? 0))
    .slice(0, 2);
}

const month = (s: number) => s >= 28 * DAY && s <= 31 * DAY;

/** A window's name in a few letters: 5h, day, wk, mo, else the provider's label. */
export function windowShort(l: QuotaLimit): string {
  const s = l.window_seconds ?? 0;
  if (s === 5 * HOUR) return t("fiveHours");
  if (s === DAY) return t("day");
  if (s === 7 * DAY) return t("week");
  if (month(s)) return t("month");
  return l.label || l.id;
}

/** A window's whole name, for its title: 5-hour limit, Weekly limit. */
export function windowName(l: QuotaLimit): string {
  const s = l.window_seconds ?? 0;
  if (s === 5 * HOUR) return t("fiveHourLimit");
  if (s === DAY) return t("dailyLimit");
  if (s === 7 * DAY) return t("weeklyLimit");
  if (month(s)) return t("monthlyLimit");
  if (s > 0 && s % DAY === 0) return t("daysLimit", { count: s / DAY });
  if (s > 0 && s % HOUR === 0) return t("hoursLimit", { count: s / HOUR });
  if (s > 0 && s % 60 === 0) return t("minutesLimit", { count: s / 60 });
  return l.label || l.id;
}

/**
 * The share of a window left, 0 to 100, when the provider says enough to know. A window with a
 * little left never reads 0: 0 means used up.
 */
export function leftPercent(l: QuotaLimit): number | undefined {
  if (l.unlimited) return undefined;
  let used = l.used_percent;
  if (used === undefined && l.limit !== undefined && l.limit > 0) {
    if (l.used !== undefined) used = (l.used / l.limit) * 100;
    else if (l.remaining !== undefined) used = ((l.limit - l.remaining) / l.limit) * 100;
  }
  if (used === undefined || Number.isNaN(used)) return undefined;
  const left = Math.max(0, Math.min(100, 100 - used));
  return left === 0 ? 0 : Math.max(1, Math.round(left));
}

/** A reset this far away is a provider's placeholder, not a time worth counting down to. */
const LONGEST_RESET_MS = 366 * DAY * 1000;

/** How long until a window starts again, or nothing when that time has passed or is not said. */
export function resetsIn(l: QuotaLimit, now = Date.now()): number | undefined {
  if (!l.resets_at) return undefined;
  const left = l.resets_at * 1000 - now;
  return left > 0 && left <= LONGEST_RESET_MS ? left : undefined;
}

/** A reset that has passed: what the provider said about the window is out of date. */
export const resetPassed = (l: QuotaLimit, now = Date.now()) => !!l.resets_at && l.resets_at * 1000 <= now;
