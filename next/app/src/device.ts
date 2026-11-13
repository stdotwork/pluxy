// This device's extension catalog, as the native side reports it (src-tauri/src/device.rs):
// app packages installed on this computer, apart from the connected core's: page-only packages
// and curated native tools (src-tauri/src/device_admission.rs). Each call
// answers in the kit's reply shape, so pages can use the kit's loading and action helpers.

import { invoke } from "@tauri-apps/api/core";
import type { Reply } from "../../ui/kit/host";

export interface DevicePage {
  id: string;
  title: string;
  short: string;
  /** Its address on the asset scheme, which serves it only to its own page webview. */
  url: string;
}

export interface DeviceRelease {
  version: string;
  sha256: string;
  size: number;
  /** Unix seconds. */
  installed_at: number;
  manifest_version: number;
  app_version: number;
  host_versions: number[];
  ui_version?: number;
  pages: DevicePage[];
  /** Resources the package asks for; none is granted yet. */
  resources: string[];
  /** Whether it declares settings; they are not served on this device yet. */
  settings: boolean;
  tier: "web" | "native";
  /** A native tool's declared operations, as [id, description]. */
  operations: [string, string][];
  /** A curated release's signer: its publisher and key ID. */
  signer: string | null;
}

export interface DevicePackage {
  id: string;
  enabled: boolean;
  /** "curated" for a release signed by a curated first-party key, otherwise "unverified". */
  trust: "curated" | "unverified";
  tier: "web" | "native";
  /** A native tool: whether the person enabled it in a native confirmation for this release. */
  authorized: boolean;
  source: { kind: "local" | "registry"; name: string; index?: string };
  active: DeviceRelease;
  last_good?: DeviceRelease;
}

export interface DeviceCatalog {
  safe_mode: boolean;
  packages: DevicePackage[];
  warning?: string;
}

export interface DeviceOffer {
  id: string;
  /** Versions with an archive for this computer, newest first. */
  versions: string[];
  /** Why the app refuses it whatever the archive holds (a reserved publisher). */
  refused?: string;
}

export interface DeviceRegistry {
  name: string;
  url: string;
  error?: string;
  packages: DeviceOffer[];
}

async function reply<T>(cmd: string, args?: Record<string, unknown>): Promise<Reply<T>> {
  try {
    return { status: 200, body: await invoke<T>(cmd, args) };
  } catch (e) {
    return { status: 400, error: { code: "refused", message: String(e) } };
  }
}

/** Applies only the newest of the replies it is given, in the order it was given them: a reply
 * that comes after a newer one's is dropped. */
export function newest<T>(apply: (value: T) => void): (reply: Promise<T>) => void {
  let n = 0;
  return (reply) => {
    const mine = ++n;
    reply.then((v) => {
      if (mine === n) apply(v);
    });
  };
}

export const device = {
  list: () => reply<DeviceCatalog>("device_list"),
  registries: () => reply<DeviceRegistry[]>("device_registries"),
  install: (registry: string, id: string, version?: string) =>
    reply<DeviceCatalog>("device_install", { registry, id, version: version ?? null }),
  /** Opens the native file chooser; the window names no path. */
  installFile: () => reply<DeviceCatalog>("device_install_file"),
  /** Enabling a native tool opens the native confirmation and answers once it is closed. */
  setEnabled: (id: string, enabled: boolean) => reply<DeviceCatalog>("device_enable", { id, enabled }),
  remove: (id: string) => reply<DeviceCatalog>("device_remove", { id }),
  rollback: (id: string) => reply<DeviceCatalog>("device_rollback", { id }),
};
