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
}

export const accountsApi = (host: Host) => ({
  accounts: () => host.call<{ accounts: Account[] }>("GET", "/accounts"),
  setDisabled: (id: string, disabled: boolean) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${one(id)}`, { disabled }),
  refresh: (id: string) => host.call<{ account: Account }>("POST", `/accounts/${one(id)}/refresh`),
  removeAccount: (id: string) => host.call<void>("DELETE", `/accounts/${one(id)}`),
});
