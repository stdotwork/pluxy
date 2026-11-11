// Accounts (next/internal/server/manage.go).

import type { Host } from "../host";
import { one } from "./path";

interface AccountError {
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
  cooldown_until?: string;
  cooldowns?: { until: string }[];
  last_error?: AccountError;
  active_requests?: number;
  check?: { state: string; detail?: string; error?: AccountError };
}

export const accountsApi = (host: Host) => ({
  accounts: () => host.call<{ accounts: Account[] }>("GET", "/accounts"),
  changeAccountRouting: (id: string, change: { priority?: number; weight?: number | null }) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, change),
  setDisabled: (id: string, disabled: boolean) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, { disabled }),
  refresh: (id: string) => host.call<{ account: Account }>("POST", `/accounts/${one(id)}/refresh`),
  removeAccount: (id: string) => host.call<void>("DELETE", `/accounts/${one(id)}`),
});
