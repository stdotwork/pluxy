// Accounts (next/internal/server/manage.go).

import type { Host } from "../host";
import { one } from "./path";

export interface AccountError {
  code: string;
  message: string;
  status?: number;
  provider_code?: string;
}

export interface Account {
  id: string;
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
  prefix?: string | null;
  /** A proxy URL, `direct` or `none`; the URL as the account shows it keeps its password. */
  proxy_url?: string | null;
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
