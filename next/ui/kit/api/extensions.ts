// The running extensions' pages and settings (next/ui/extension-pages/README.md).

import type { Host } from "../host";
import { seg } from "./path";

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

export const extensionsApi = (host: Host) => ({
  ui: () => host.call<UI>("GET", "/ui"),
  settings: (id: string) => host.call<Settings>("GET", `/extensions/${seg(id)}/config`),
  changeSettings: (id: string, values: Record<string, unknown>) =>
    host.call<Settings>("PATCH", `/extensions/${seg(id)}/config`, { values }),
});
