// The rules of Order & fallback: the routing settings it shows, in words for a person, and how each
// one's value is read from and written to core's settings. Only this page shows the routing
// settings: Config links here.

import type { CoreSettings, SettingsChange } from "../api/settings";
import { strings } from "../strings";

const t = strings("routing.rules", {
  strategy: "How a group shares requests",
  roundRobin: "Take turns",
  roundRobinAbout: "Each account in the group takes the next request in turn.",
  weighted: "Split by weight",
  weightedAbout: "Accounts take requests in proportion to their weight.",
  fillFirst: "Fill one first",
  fillFirstAbout: "Uses the first account until it is busy or out, then the next.",
  leastActive: "Least busy first",
  leastActiveAbout: "Sends each request to the account with the fewest requests running.",
  soonestReset: "Soonest reset first",
  soonestResetAbout: "Spends the limit that resets soonest first, so less of it goes unused.",
  affinity: "Keep a conversation on one account",
  affinityAbout:
    "Keeps a coding tool's conversation on the account it started on, so the provider's cache keeps working.",
  ttl: "Release a quiet conversation after",
  ttlAbout: "How long after its last request a conversation keeps its account, such as 1h or 30m.",
  ttlOff: "Turn on Keep a conversation on one account first.",
  rounds: "Extra rounds when every account fails",
  roundsAbout: "Empty: pluxy decides.",
  roundsPlaceholder: "Automatic",
  perRound: "Accounts to try per round",
  perRoundAbout: "0 tries every account.",
  longestWait: "Longest wait for a free account (seconds)",
  longestWaitAbout: "0 never waits; the request fails at once.",
  rest: "Rest an account after it fails",
  restAbout: "Off: a failed account is tried again at once.",
  providerError: "Rest after a provider error (seconds)",
  providerErrorAbout: "0 uses pluxy's own wait, from 1 second up to a minute; -1 never rests.",
  limitResets: "Show tools when a limit resets",
  limitResetsAbout:
    'When every account is out until a reset, tools such as Codex show the provider\'s "limit reached" message with its reset time.',
  prefixed: "Prefixed accounts answer only prefixed names",
  prefixedAbout: "An account with a prefix answers only names that start with it.",
  notNumber: "Enter a number.",
  notWhole: "Enter a whole number.",
  tooSmall: "Enter {min} or more.",
});

/** How a rule's control reads: a choice, a switch, a duration as text, or a number. */
export type RuleKind = "strategy" | "switch" | "text" | "number";

export interface RuleDef {
  /** The setting, by its path in core's settings. */
  key: string;
  kind: RuleKind;
  /** Under More rules, not shown at first. */
  more: boolean;
  /** A number's lowest value. */
  min?: number;
  /** A number with no fraction. */
  whole?: boolean;
  /** A number that may be empty, which core reads as its own choice (null). */
  nullable?: boolean;
}

/** The routing rules the page shows, in the order it shows them. */
export const RULES: readonly RuleDef[] = [
  { key: "routing.strategy", kind: "strategy", more: false },
  { key: "routing.session-affinity", kind: "switch", more: false },
  { key: "routing.session-affinity-ttl", kind: "text", more: true },
  { key: "routing.retry.request-retry", kind: "number", more: true, min: 0, whole: true, nullable: true },
  { key: "routing.retry.max-retry-credentials", kind: "number", more: true, min: 0, whole: true },
  { key: "routing.retry.max-retry-interval", kind: "number", more: true, min: 0 },
  { key: "routing.cooldown.disable-cooling", kind: "switch", more: true },
  { key: "routing.cooldown.transient-error-cooldown-seconds", kind: "number", more: true, min: -1 },
  { key: "routing.cooldown.quota-response-mode", kind: "switch", more: true },
  { key: "routing.force-model-prefix", kind: "switch", more: true },
];

export interface RuleWords {
  name: string;
  about: string;
  /** The number or text field's placeholder. */
  placeholder?: string;
}

const WORDS: Record<string, () => RuleWords> = {
  "routing.strategy": () => ({ name: t("strategy"), about: "" }),
  "routing.session-affinity": () => ({ name: t("affinity"), about: t("affinityAbout") }),
  "routing.session-affinity-ttl": () => ({ name: t("ttl"), about: t("ttlAbout") }),
  "routing.retry.request-retry": () => ({
    name: t("rounds"),
    about: t("roundsAbout"),
    placeholder: t("roundsPlaceholder"),
  }),
  "routing.retry.max-retry-credentials": () => ({ name: t("perRound"), about: t("perRoundAbout") }),
  "routing.retry.max-retry-interval": () => ({ name: t("longestWait"), about: t("longestWaitAbout") }),
  "routing.cooldown.disable-cooling": () => ({ name: t("rest"), about: t("restAbout") }),
  "routing.cooldown.transient-error-cooldown-seconds": () => ({
    name: t("providerError"),
    about: t("providerErrorAbout"),
  }),
  "routing.cooldown.quota-response-mode": () => ({ name: t("limitResets"), about: t("limitResetsAbout") }),
  "routing.force-model-prefix": () => ({ name: t("prefixed"), about: t("prefixedAbout") }),
};

/** A rule's name and its sentence. */
export const ruleWords = (key: string): RuleWords => (WORDS[key] ?? (() => ({ name: key, about: "" })))();

/** Why the release time cannot be changed, while a conversation is not kept on one account. */
export const ttlOffWhy = () => t("ttlOff");

export type Strategy = CoreSettings["routing"]["strategy"];

/** The ways a group shares requests, each with its sentence. */
export const strategies = (): { value: Strategy; label: string; about: string }[] => [
  { value: "round-robin", label: t("roundRobin"), about: t("roundRobinAbout") },
  { value: "weighted-round-robin", label: t("weighted"), about: t("weightedAbout") },
  { value: "fill-first", label: t("fillFirst"), about: t("fillFirstAbout") },
  { value: "least-active", label: t("leastActive"), about: t("leastActiveAbout") },
  { value: "soonest-reset", label: t("soonestReset"), about: t("soonestResetAbout") },
];

/** A setting's value by its path. */
export const settingAt = (settings: CoreSettings, key: string): unknown =>
  key.split(".").reduce<unknown>((v, k) => (v as Record<string, unknown> | undefined)?.[k], settings);

/** The PATCH body that changes one setting: its path as nested objects. */
export function nest(key: string, value: unknown): SettingsChange {
  const path = key.split(".");
  const last = path.pop() as string;
  const out: Record<string, unknown> = {};
  let at = out;
  for (const k of path) {
    const inner: Record<string, unknown> = {};
    at[k] = inner;
    at = inner;
  }
  at[last] = value;
  return out as SettingsChange;
}

/**
 * Whether a switch reads on for the value core holds. Two read the other way round from core's
 * setting: resting an account after it fails is on when cooling is not disabled, and showing the
 * limit message is on when the quota response is "usage-limit".
 */
export function switchOn(key: string, value: unknown): boolean {
  if (key === "routing.cooldown.disable-cooling") return value !== true;
  if (key === "routing.cooldown.quota-response-mode") return value === "usage-limit";
  return value === true;
}

/** The value core takes for a switch turned on or off (the inverse of switchOn). */
export function switchValue(key: string, on: boolean): boolean | string {
  if (key === "routing.cooldown.disable-cooling") return !on;
  if (key === "routing.cooldown.quota-response-mode") return on ? "usage-limit" : "retryable";
  return on;
}

/** A number setting as the text its field starts with: null (core's own choice) is empty. */
export const numberText = (value: unknown) => (typeof value === "number" ? String(value) : "");

/** What a number field's text means: the value to send, or why it cannot be sent. */
export function parseNumber(def: RuleDef, text: string): { value: number | null } | { error: string } {
  const s = text.trim();
  if (s === "") return def.nullable ? { value: null } : { error: t("notNumber") };
  const n = Number(s);
  if (!Number.isFinite(n)) return { error: t("notNumber") };
  if (def.whole && !Number.isInteger(n)) return { error: t("notWhole") };
  if (def.min !== undefined && n < def.min) return { error: t("tooSmall", { min: def.min }) };
  return { value: n };
}
