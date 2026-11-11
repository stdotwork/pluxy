// Accounts (next/internal/server/manage.go).

import type { Host } from "../host";
import { one } from "./path";

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
  check?: { state: string; detail?: string; error?: AccountError; at?: string };
  cooldown_until?: string;
  cooldowns?: AccountCooldown[];
  last_error?: AccountError;
  last_error_at?: string;
  active_requests?: number;
}

export interface AccountError {
  code: string;
  message: string;
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

export const accountsApi = (host: Host) => ({
  accounts: () => host.call<{ accounts: Account[] }>("GET", "/accounts"),
  setDisabled: (id: string, disabled: boolean) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, { disabled }),
  refresh: (id: string) => host.call<{ account: Account }>("POST", `/accounts/${one(id)}/refresh`),
  accountModels: (id: string) => host.call<{ models: AccountModel[] }>("GET", `/accounts/${one(id)}/models`),
  testAccount: (id: string, model?: string) =>
    host.call<{ result: AccountTestResult }>("POST", `/accounts/${one(id)}/test`, { model }),
  resetCooldown: (id: string) =>
    host.call<{ cleared: number; account: Account }>("POST", `/accounts/${one(id)}/cooldown/reset`),
  removeAccount: (id: string) => host.call<void>("DELETE", `/accounts/${one(id)}`),
});
