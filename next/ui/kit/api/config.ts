import type { Host } from "../host";
import { one } from "./path";

export interface ClientKey {
  id: string;
  masked: string;
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
  addClientKey: () => host.call<{ key: ClientKey; value: string }>("POST", "/client-keys"),
  deleteClientKey: (id: string) => host.call<void>("DELETE", `/client-keys/${one(id)}`),
  rotateManagementKey: () => host.call<{ value: string }>("POST", "/management-key/rotate"),
});
