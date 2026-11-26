// What the Accounts list and an account's page work out without drawing anything: the groups and
// their order, the counts line, the search, the batch's targets, and an account's place in its
// provider's order, its kind of sign-in and a name's length.

import type { Account } from "../api";
import {
  accountCondition,
  accountName,
  type Condition,
  conditionWord,
  GROUPS,
  type Group,
  providerName,
} from "../parts/account";
import { strings } from "../strings";
import { groupTiers } from "./routing";

const t = strings("accounts.words", {
  accounts: { one: "{count} account", other: "{count} accounts" },
  atProviders: { one: "at {count} provider", other: "at {count} providers" },
  needYou: { one: "{count} needs you", other: "{count} need you" },
  serving: "{count} serving",
  waiting: "{count} waiting",
  turnedOff: "{count} turned off",
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

/** The counts line: "11 accounts at 4 providers · 2 need you · 7 serving · 1 waiting · 1 turned off", no zero counts. */
export function countsLine(list: Account[], running?: ReadonlySet<string>, now = Date.now()): string {
  const n: Record<Group, number> = { needs: 0, waiting: 0, serving: 0, off: 0 };
  for (const a of list) n[accountCondition(a, running, now).group]++;
  const providers = new Set(list.map((a) => a.extension).filter(Boolean)).size;
  const total = providers
    ? `${t("accounts", { count: list.length })} ${t("atProviders", { count: providers })}`
    : t("accounts", { count: list.length });
  return [
    total,
    n.needs ? t("needYou", { count: n.needs }) : "",
    n.serving ? t("serving", { count: n.serving }) : "",
    n.waiting ? t("waiting", { count: n.waiting }) : "",
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

/** The groups that have accounts, in GROUPS order. */
export const shownGroups = (groups: Record<Group, unknown[]>) => GROUPS.filter((g) => groups[g].length > 0);

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
