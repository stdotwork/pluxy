// The management API calls the kit's pages use, with the shapes core replies in
// (next/internal/core/management.go and next/internal/server/manage.go).

import type { Host, Reply } from "./host";

export interface Revision {
  revision: number;
  hash?: string;
  at: string;
  error?: string;
}

export interface ExtensionStatus {
  id: string;
  version: string;
  generation: number;
  state: string;
  pid?: number;
  restarts: number;
  crashes_in_a_row: number;
  last_error?: string;
  active_requests: number;
  current: boolean;
}

export interface Status {
  safe_mode: boolean;
  desired: Revision;
  active: Revision;
  extensions: ExtensionStatus[];
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
}

export interface Package {
  id: string;
  version: string;
  registry: string;
  url: string;
  sha256: string;
  enabled: boolean;
  installed_at: string;
  state: string;
  error?: string;
}

export interface Registry {
  name: string;
  packages?: { id: string; releases: { version: string; protocol: number[]; platforms: string[] }[] }[];
  error?: string;
}

export interface UIPage {
  id: string;
  title: string;
  short: string;
  /** Empty when core has no pages origin for this panel (UI.pages_error says why). */
  url: string;
}

export interface UIExtension {
  id: string;
  version: string;
  permissions: string[];
  pages: UIPage[];
  config: boolean;
}

/** The extensions' pages and settings (next/ui/extension-pages/README.md). */
export interface UI {
  pages_origin?: string;
  pages_error?: string;
  extensions: UIExtension[];
}

/** One field of a settings schema, as the manifest gives it. */
export interface SettingField {
  type: "string" | "integer" | "number" | "boolean";
  title?: string;
  description?: string;
  default?: unknown;
  enum?: string[];
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  writeOnly?: boolean;
}

export interface SettingsSchema {
  type: "object";
  properties: Record<string, SettingField>;
  required?: string[];
}

/** What core shows of an extension's settings: never a writeOnly value. */
export interface SettingsValues {
  values: Record<string, unknown>;
  set: string[];
}

export interface Settings extends SettingsValues {
  schema: SettingsSchema;
}

const seg = (s: string) => s.split("/").map(encodeURIComponent).join("/");

export const api = (host: Host) => ({
  status: () => host.call<Status>("GET", "/status"),
  reload: () => host.call<Status>("POST", "/reload"),
  accounts: () => host.call<{ accounts: Account[] }>("GET", "/accounts"),
  setDisabled: (id: string, disabled: boolean) =>
    host.call<{ account: Account }>("PATCH", `/accounts/${encodeURIComponent(id)}`, { disabled }),
  refresh: (id: string) =>
    host.call<{ account: Account }>("POST", `/accounts/${encodeURIComponent(id)}/refresh`),
  removeAccount: (id: string) => host.call<void>("DELETE", `/accounts/${encodeURIComponent(id)}`),
  packages: () => host.call<{ packages: Package[] }>("GET", "/packages"),
  registries: () => host.call<{ registries: Registry[] }>("GET", "/registries"),
  install: (id: string, registry: string) =>
    host.call<{ package: Package }>("POST", "/packages", { id, registry, enable: true }),
  setEnabled: (id: string, enabled: boolean) =>
    host.call<{ package: Package }>("POST", `/packages/${seg(id)}/${enabled ? "enable" : "disable"}`),
  removePackage: (id: string) => host.call<void>("DELETE", `/packages/${seg(id)}`),
  ui: () => host.call<UI>("GET", "/ui"),
  settings: (id: string) => host.call<Settings>("GET", `/extensions/${seg(id)}/config`),
  changeSettings: (id: string, values: Record<string, unknown>) =>
    host.call<Settings>("PATCH", `/extensions/${seg(id)}/config`, { values }),
});

export type Api = ReturnType<typeof api>;

/** A reply's error as one line for people. */
export function problem(r: Reply<unknown>): string | undefined {
  if (r.status >= 200 && r.status < 300) return undefined;
  if (r.status === 0) return "core could not be reached";
  return r.error?.message ?? `core answered ${r.status}`;
}
