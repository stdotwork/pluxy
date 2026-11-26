// Accounts (next/internal/server/manage.go).

import type { Host } from "../host";
import { one } from "./path";
import type { QuotaReply } from "./quota";

export interface AccountError {
  code: string;
  message: string;
  status?: number;
  provider_code?: string;
}

export interface Account {
  id: string;
  /** `acc_` and 8 hex digits: what core calls the account outside itself, as in a request's `tried`. */
  public_id?: string;
  source: "config" | "login";
  extension?: string;
  kind?: string;
  state: string;
  disabled?: boolean;
  reason?: string;
  label?: string;
  version?: number;
  expires_at?: number;
  refreshable: boolean;
  renewal_error?: { code: string; message: string };
  created_at?: string;
  updated_at?: string;
  priority?: number;
  weight?: number;
  disable_cooling?: boolean;
  max_concurrency?: number;
  request_retry?: number;
  prefix?: string;
  /** The account's own proxy, with `redacted` in place of a user name and password. */
  proxy_url?: string;
  /** Extra HTTP headers on the account's upstream requests, a placeholder in place of each secret value. */
  headers?: Record<string, string>;
  /** Each model by its upstream name, or with the alias clients use for it. */
  models?: (string | AccountModelEntry)[];
  excluded_models?: string[];
  model_alias?: AccountModelAlias[];
  endpoint?: string;
  /** An account added by login's settings, each secret replaced by a placeholder. */
  settings?: Record<string, unknown>;
  /**
   * The settings hold a number the browser does not read exactly (it would write it back
   * changed), so they cannot be edited here.
   */
  settings_inexact?: boolean;
  check?: { state: string; detail?: string; error?: AccountError; at?: string };
  cooldown_until?: string;
  cooldowns?: AccountCooldown[];
  last_error?: AccountError;
  last_error_at?: string;
  active_requests?: number;
  /** What routing holds against the account now, as core judges it. */
  health?: AccountHealth;
  /**
   * The limits core last read for the account, with when it read them; absent from a core that
   * does not keep them, and for an account it has not read.
   */
  quota?: QuotaReply;
}

/**
 * An account's health in GET /accounts: `state` is ok, cooling, exhausted, needs_login, banned,
 * disabled or broken. A broken account is one whose credential file core cannot read: core knows
 * it only by the file's name, so its `extension`, `kind` and `state` are empty and `why` names the
 * file and the error.
 */
export interface AccountHealth {
  state: string;
  /** When the block ends, if core knows. */
  until?: string;
  why?: string;
  source?: string;
}

const LEFT_OUT = ["banned", "exhausted", "needs_login", "broken"] as const;
export type LeftOut = (typeof LEFT_OUT)[number];

/**
 * The health state that keeps every request away from an account a person has left on: its
 * provider closed it, its usage window ran out, it needs a new sign-in, or core cannot read its
 * credential file. Nothing for an account
 * that serves, one a person turned off, and one cooling (a short block, often of one model).
 */
export function leftOut(a: Account): LeftOut | undefined {
  if (a.disabled) return undefined;
  return LEFT_OUT.find((state) => state === a.health?.state);
}

/**
 * The extension IDs core runs, from its status, or undefined while the status is not read. An
 * account whose extension is not among them has no provider that can serve it.
 */
export function runningExtensions(status?: {
  extensions: { id: string }[];
}): ReadonlySet<string> | undefined {
  return status && new Set(status.extensions.map((e) => e.id));
}

/** An account whose extension core does not run: requests for its models find no account to serve them. */
export function providerMissing(a: Account, running?: ReadonlySet<string>): boolean {
  return !a.disabled && !!running && !!a.extension && !running.has(a.extension);
}

export type Standing = "active" | "unavailable" | "disabled";

/**
 * The one rule every page counts accounts by: disabled when a person turned it off; unavailable
 * when its login is gone, core leaves it out (see leftOut) or its provider is not running; else
 * active. An account cooling for a short while still counts active: it serves other requests.
 */
export function standing(a: Account, running?: ReadonlySet<string>): Standing {
  if (a.disabled) return "disabled";
  if (a.state !== "active" || leftOut(a) || providerMissing(a, running)) return "unavailable";
  return "active";
}

/** How many accounts stand each way (see standing). */
export function countAccounts(list: Account[], running?: ReadonlySet<string>): Record<Standing, number> {
  const n: Record<Standing, number> = { active: 0, unavailable: 0, disabled: 0 };
  for (const a of list) n[standing(a, running)]++;
  return n;
}

export interface AccountModelEntry {
  name: string;
  alias?: string;
}

export interface AccountModelAlias {
  name: string;
  alias: string;
  fork?: boolean;
  display_name?: string;
}

/** The fields PATCH /accounts/{id} changes; null sets one back to its default. */
export interface AccountChange {
  disabled?: boolean;
  /** The account's name as a person gave it; "" or null takes it away. Core refuses it (409) for an account set in its config. */
  label?: string | null;
  prefix?: string | null;
  /** A proxy URL, `direct` or `none`; the URL as the account shows it keeps its password. */
  proxy_url?: string | null;
  /** All the account's headers (one left out is removed); a placeholder from the account keeps its value. */
  headers?: Record<string, string> | null;
  priority?: number | null;
  weight?: number | null;
  disable_cooling?: boolean | null;
  max_concurrency?: number | null;
  request_retry?: number | null;
  models?: AccountModelEntry[] | null;
  excluded_models?: string[] | null;
  model_alias?: AccountModelAlias[] | null;
  /** Replaces the settings as a whole; a placeholder from the account keeps its secret. */
  settings?: Record<string, unknown> | null;
}

export interface AccountCooldown {
  scope: string;
  model?: string;
  kind: string;
  until: string;
  backoff_level?: number;
  error?: AccountError;
}

export interface AccountModel {
  id: string;
  names: string[] | null;
  excluded?: boolean;
}

export interface AccountTestResult {
  ok: boolean;
  model: string;
  served_model?: string;
  latency_ms: number;
  reply?: string;
  finish_reason?: string;
  error?: AccountError;
}

/** What POST /accounts/import added from one auth file, and why it skipped the rest. */
export interface AuthImport {
  imported: { id: string; extension: string; ignored?: string[] }[];
  skipped: string[];
}

export const accountsApi = (host: Host) => ({
  accounts: () => host.call<{ accounts: Account[] }>("GET", "/accounts"),
  changeAccountRouting: (id: string, change: { priority?: number; weight?: number | null }) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, change),
  setDisabled: (id: string, disabled: boolean) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, { disabled }),
  setAccount: (id: string, change: AccountChange) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, change),
  refresh: (id: string) => host.call<{ account: Account }>("POST", `/accounts/${one(id)}/refresh`),
  accountModels: (id: string) => host.call<{ models: AccountModel[] }>("GET", `/accounts/${one(id)}/models`),
  testAccount: (id: string, model?: string) =>
    host.call<{ result: AccountTestResult }>("POST", `/accounts/${one(id)}/test`, { model }),
  resetCooldown: (id: string) =>
    host.call<{ cleared: number; account: Account }>("POST", `/accounts/${one(id)}/cooldown/reset`),
  removeAccount: (id: string) => host.call<void>("DELETE", `/accounts/${one(id)}`),
  /** Imports one auth file of today's core: its name and its text. */
  importAuthFile: (name: string, content: string) =>
    host.call<AuthImport>("POST", "/accounts/import", { name, content }),
});
