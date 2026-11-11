import type { Host } from "../host";

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
});
