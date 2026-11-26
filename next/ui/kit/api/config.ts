import type { Host } from "../host";
import { one } from "./path";

export interface ClientKey {
  id: string;
  masked: string;
  /** The key's name, such as the tool it was made for; absent for a key with none. */
  label?: string;
}

export interface ConfigDocument {
  format: "json" | "yaml";
  hash: string;
  content: string;
}
export interface ConfigValidation {
  valid: boolean;
  notices?: string[];
  error?: string;
}
export const configApi = (host: Host) => ({
  config: () => host.call<ConfigDocument>("GET", "/config"),
  validateConfig: (content: string) => host.call<ConfigValidation>("POST", "/config/validate", { content }),
  saveConfig: (content: string, base_hash: string) =>
    host.call<ConfigDocument>("PUT", "/config", { content, base_hash }),
  clientKeys: () => host.call<{ keys: ClientKey[] }>("GET", "/client-keys"),
  /** Makes a key; label, when given, is its name. The value comes back once, in this reply. */
  addClientKey: (label?: string) =>
    host.call<{ key: ClientKey; value: string }>("POST", "/client-keys", label ? { label } : undefined),
  /** Sets a key's name; an empty name clears it. A pluxy older than key names answers 405. */
  renameClientKey: (id: string, label: string) =>
    host.call<ClientKey>("PATCH", `/client-keys/${one(id)}`, { label: label.trim() || null }),
  deleteClientKey: (id: string) => host.call<void>("DELETE", `/client-keys/${one(id)}`),
  rotateManagementKey: () => host.call<{ value?: string }>("POST", "/management-key/rotate"),
});
