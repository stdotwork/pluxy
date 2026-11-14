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

/** Another package's operations a package may call through the host. */
export interface DeviceImport {
  package: string;
  /** The target's export contracts it works with. */
  contract: number[];
  operations: string[];
  /** Required: enabled only with the target enabled. Optional: a call fails while it is missing. */
  required: boolean;
}

export interface DeviceExports {
  contract: number;
  operations: string[];
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
  /** Resources the package asks for, by ID. */
  resources: string[];
  /** The same, as Access grants them. */
  declared: DeviceResource[];
  /** Who signed it, from the registry it came from; null when unsigned. Whether that is a
   * curated signature is the package's `trust`. */
  signer: DeviceSigner | null;
  /** Whether it declares settings; they are not served on this device yet. */
  settings: boolean;
  tier: "web" | "native";
  /** A native tool's declared operations, as [id, description, effect]: read, write, launch or
   * "shows content" (disclose). */
  operations: [string, string, string][];
  imports: DeviceImport[];
  /** A native tool's operations other packages may import. */
  exports: DeviceExports | null;
}

export interface DeviceResource {
  id: string;
  kind: "file" | "subtree";
  operations: string[];
}

export interface DeviceSigner {
  publisher: string;
  key_id: string;
  /** The Ed25519 public key, in hexadecimal. */
  public_key: string;
  /** The registry's index this signer is approved for, or would be. */
  source: string;
  approved: boolean;
  /** Whether Access may use this signature: a web package whose publisher no trust root
   * reserves. Otherwise no signer approval or grant is offered for it. */
  access: boolean;
}

/** A file or folder the person granted to a package (src-tauri/src/access.rs). */
export interface AccessGrant {
  id: string;
  package: string;
  resource: string;
  kind: "file" | "subtree";
  operations: string[];
  /** What the person chose. */
  selected: string;
  /** What it resolved to when they confirmed it. */
  target: string;
  /** The release it was granted to. */
  sha256: string;
  /** A document portal file: the grant ends when the app quits. */
  session_only: boolean;
  /** The link or file changed since: refused until granted again. */
  changed: boolean;
  granted_at: number;
}

export interface AccessList {
  signers: { publisher: string; source: string; key_id: string; public_key: string }[];
  grants: AccessGrant[];
  warning?: string | null;
}

export interface DevicePackage {
  id: string;
  enabled: boolean;
  /** "curated" for a release signed by a curated first-party key, otherwise "unverified". */
  trust: "curated" | "unverified";
  tier: "web" | "native";
  /** A native tool: whether the person enabled it in a native confirmation for this release. */
  authorized: boolean;
  /** A web package: whether the person allowed its native imports for this release. */
  imports_allowed: boolean;
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
  /** Why installing the version offered is refused, as far as the index shows (a reserved
   * publisher, or a signature that is missing, partial or does not hold). */
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
  /** Allowing a web package's native imports opens the native confirmation and answers once it
   * is closed; revoking needs none. */
  setImports: (id: string, allowed: boolean) => reply<DeviceCatalog>("device_imports", { id, allowed }),
};

/** Access: grants start here, in the window; native code asks for the file and the
 * confirmation, and the window never names a path. */
export const access = {
  list: () => reply<AccessList>("access_list"),
  /** Opens a native confirmation of the package's signer. */
  approveSigner: (id: string) => reply<{ outcome: "approved" | "cancelled" }>("access_signer", { id }),
  /** Opens the native file chooser, then a native confirmation. */
  grant: (id: string, resource: string) =>
    reply<{ outcome: "granted" | "cancelled"; grant?: AccessGrant }>("access_grant", { id, resource }),
  revoke: (grant: string) => reply<{ finished: number }>("access_revoke", { grant }),
};
