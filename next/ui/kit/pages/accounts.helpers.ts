// What the Accounts list and an account's page work out without drawing anything: the groups and
// their order, the counts line, the search, the batch's targets, and an account's place in its
// provider's order, its kind of sign-in and a name's length.

import type { Account } from "../api";
import {
  accountCondition,
  accountName,
  type Condition,
  conditionUnchecked,
  conditionWord,
  type Group,
  providerName,
} from "../parts/account";
import { strings } from "../strings";
import { groupTiers } from "./routing.helpers";

const t = strings("accounts.words", {
  accounts: { one: "{count} account", other: "{count} accounts" },
  providerAccounts: { one: "{count} {provider} account", other: "{count} {provider} accounts" },
  atProviders: { one: "at {count} provider", other: "at {count} providers" },
  needYou: { one: "{count} needs you", other: "{count} need you" },
  serving: "{count} serving",
  waiting: "{count} waiting",
  turnedOff: "{count} turned off",
  notChecked: "{count} not checked",
  signInWith: "a sign-in with {provider}",
  apiKey: "an API key",
  inSettingsFile: "{kind}, set in pluxy's settings file",
  first: "First among {provider} accounts",
  backup: "Backup {count}",
  tooLong: "Use at most 64 characters.",
});

/** The most characters a name may have, trimmed. */
export const NAME_MAX = 64;

/** Why a name cannot be saved, or nothing when it can: a trimmed name over NAME_MAX characters. */
export function nameProblem(name: string): string | undefined {
  return [...name.trim()].length > NAME_MAX ? t("tooLong") : undefined;
}

const byName = (a: Account, b: Account) =>
  accountName(a).localeCompare(accountName(b)) || a.id.localeCompare(b.id);

/**
 * The accounts in their groups, in order: Needs you and Waiting by soonest back (an account with no
 * time last), then by name; Serving and Turned off by name. So no row moves while the page
 * refreshes unless its condition changed.
 */
export function groupAccounts(
  list: Account[],
  running?: ReadonlySet<string>,
  now = Date.now(),
): Record<Group, { account: Account; c: Condition }[]> {
  const groups: Record<Group, { account: Account; c: Condition }[]> = {
    needs: [],
    waiting: [],
    serving: [],
    off: [],
  };
  for (const account of list) {
    const c = accountCondition(account, running, now);
    groups[c.group].push({ account, c });
  }
  const soonest = (x: { account: Account; c: Condition }, y: { account: Account; c: Condition }) =>
    (x.c.until ?? Number.POSITIVE_INFINITY) - (y.c.until ?? Number.POSITIVE_INFINITY) ||
    byName(x.account, y.account);
  groups.needs.sort(soonest);
  groups.waiting.sort(soonest);
  groups.serving.sort((x, y) => byName(x.account, y.account));
  groups.off.sort((x, y) => byName(x.account, y.account));
  return groups;
}

export interface ProviderCount {
  extension: string;
  name: string;
  count: number;
}

/**
 * The providers represented in a list, in the order core lists their first account, each with its
 * display name and total account count. Accounts without an extension are left out.
 */
export function providerCounts(list: Account[]): ProviderCount[] {
  const counts = new Map<string, number>();
  for (const a of list) {
    if (!a.extension) continue;
    counts.set(a.extension, (counts.get(a.extension) ?? 0) + 1);
  }
  return [...counts].map(([extension, count]) => ({
    extension,
    name: providerName(extension),
    count,
  }));
}

export type StateCounts = Record<Group | "unchecked", number>;

/** How many accounts fall in each condition group, matching countsLine. */
export function stateCounts(
  list: Account[],
  running?: ReadonlySet<string>,
  now = Date.now(),
  unchecked = false,
): StateCounts {
  const n: StateCounts = { needs: 0, waiting: 0, serving: 0, off: 0, unchecked: 0 };
  for (const a of list) {
    const c = accountCondition(a, running, now);
    n[unchecked && conditionUnchecked(a, c, running) ? "unchecked" : c.group]++;
  }
  return n;
}

/**
 * The counts line: "11 accounts at 4 providers · 2 need you · 7 serving · 1 waiting · 1 turned
 * off", no zero counts. unchecked: the status could not be read, so the accounts that serve or wait
 * only if their provider runs count as "not checked" (conditionUnchecked).
 */
export function countsLine(
  list: Account[],
  running?: ReadonlySet<string>,
  now = Date.now(),
  unchecked = false,
  provider?: string,
): string {
  const n = stateCounts(list, running, now, unchecked);
  const providers = new Set(list.map((a) => a.extension).filter(Boolean)).size;
  const total = provider
    ? t("providerAccounts", { count: list.length, provider })
    : providers
      ? `${t("accounts", { count: list.length })} ${t("atProviders", { count: providers })}`
      : t("accounts", { count: list.length });
  return [
    total,
    n.needs ? t("needYou", { count: n.needs }) : "",
    n.serving ? t("serving", { count: n.serving }) : "",
    n.waiting ? t("waiting", { count: n.waiting }) : "",
    n.unchecked ? t("notChecked", { count: n.unchecked }) : "",
    n.off ? t("turnedOff", { count: n.off }) : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * Whether an account matches what was typed in Find an account: its name, ID, public ID, provider's
 * name, condition word or last error, case not counting. Nothing typed matches every account.
 */
export function matchesSearch(a: Account, c: Condition, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    a.label,
    a.id,
    a.public_id,
    providerName(a.extension),
    a.extension,
    conditionWord(c),
    a.last_error?.message,
  ]
    .filter((s): s is string => !!s)
    .some((s) => s.toLowerCase().includes(q));
}

export type BatchAction = "turnOn" | "turnOff" | "clearCooldown" | "remove";

/**
 * The accounts of a selection a batch action is sent for. Core refuses to turn off or remove an
 * account set in its settings file, so those two leave such accounts out. Turn on reaches them:
 * it is what lifts a provider's closing, and core lifts one from a settings-file account too.
 */
export function batchTargets(selected: Account[], action: BatchAction): Account[] {
  if (action === "turnOn" || action === "clearCooldown") return selected;
  return selected.filter((a) => a.source !== "config");
}

/**
 * The account's place in its provider's order, as routing tries them: "First among Claude
 * accounts" in the first tier, else "Backup {n}" for the n-th tier after it.
 */
export function orderWord(a: Account, all: Account[]): string {
  const provider = providerName(a.extension);
  const tiers = groupTiers(all.filter((x) => x.extension === a.extension));
  const at = tiers.findIndex((tier) => tier.accounts.some((x) => x.id === a.id));
  return at <= 0 ? t("first", { provider }) : t("backup", { count: at });
}

/** How the account signs in: "a sign-in with Claude", "an API key" or core's kind, and where it is set. */
export function signsInWith(a: Account): string {
  const kind =
    a.kind === "oauth"
      ? t("signInWith", { provider: providerName(a.extension) })
      : a.kind === "api_key"
        ? t("apiKey")
        : (a.kind ?? "");
  return a.source === "config" ? t("inSettingsFile", { kind }) : kind;
}
