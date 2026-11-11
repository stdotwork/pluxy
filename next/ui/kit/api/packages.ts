// Installed extension packages and the registries that offer them.

import type { Host } from "../host";
import { seg } from "./path";

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

export const packagesApi = (host: Host) => ({
  packages: () => host.call<{ packages: Package[] }>("GET", "/packages"),
  registries: () => host.call<{ registries: Registry[] }>("GET", "/registries"),
  install: (id: string, registry: string) =>
    host.call<{ package: Package }>("POST", "/packages", { id, registry, enable: true }),
  setEnabled: (id: string, enabled: boolean) =>
    host.call<{ package: Package }>("POST", `/packages/${seg(id)}/${enabled ? "enable" : "disable"}`),
  removePackage: (id: string) => host.call<void>("DELETE", `/packages/${seg(id)}`),
});
