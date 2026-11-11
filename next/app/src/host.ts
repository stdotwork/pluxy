// The kit's host in the desktop app. The native side holds the management key: every management
// call goes to it (the core_call command), and it adds the key and sends the call to the core
// the app is connected to. A key the person types passes through here once, to the native side.

import { invoke } from "@tauri-apps/api/core";
import type { Feature, Host, Method, Reply } from "../../ui/kit/host";

export type Mode = "managed" | "local" | "remote";

/** The app's connection to a core, as the native side reports it. Never holds the key. */
export interface Connection {
  /** Unset until the person picks how the app reaches core. */
  mode?: Mode;
  /** The core the app talks to (its address), shown while connected. */
  target?: string;
  signed_in: boolean;
  /** Whether a key for local or remote mode is kept on this device. */
  remembered: boolean;
  url?: string;
  ca_file?: string;
  core_path?: string;
  core_source?: string;
  /** The core this app runs, in managed mode: missing until a version is installed; version is
   * the installed version it runs; adopt names a core program waiting to be adopted. */
  core?: {
    state: "running" | "stopped" | "missing";
    pid?: number;
    version?: string;
    adopt?: string;
    detail?: string;
  };
  /** Why the app is not connected, when it is not. */
  error?: string;
}

/** What the person sets on the Connection page. */
export interface ConnectionChange {
  mode: Mode;
  url?: string;
  key?: string;
  remember?: boolean;
  ca_file?: string;
  core_path?: string;
  core_source?: string;
}

/** The app's own core versions, in managed mode. */
export interface Versions {
  source?: string;
  available: { version: string }[];
  source_error?: string;
  installed: { id: string; kind: "signed" | "adopted"; version?: string; adopted_from?: string }[];
  selected?: string;
  previous?: string;
  failed?: { version: string; reason: string };
  outcome?: {
    seq: number;
    action: "install" | "rollback" | "adopt";
    version: string;
    result: "done" | "cancelled" | "failed";
    detail?: string;
  };
}

export const connectionGet = () => invoke<Connection>("connection_get");
export const connectionSet = (change: ConnectionChange) => invoke<Connection>("connection_set", { change });
export const coreRestart = () => invoke<Connection>("core_restart");
export const coreVersions = () => invoke<Versions>("core_versions");
/** Each opens the native confirmation and returns; the app sends pluxy-connection when done. */
export const coreInstall = (version: string) => invoke<void>("core_install", { version });
export const coreRollback = (version: string) => invoke<void>("core_rollback", { version });
export const coreAdopt = () => invoke<void>("core_adopt");

function read(name: string): string | null {
  try {
    return localStorage.getItem(name);
  } catch {
    return null;
  }
}

function write(name: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(name);
    else localStorage.setItem(name, value);
  } catch {
    // Nothing to keep it in: it lasts until the window closes.
  }
}

/**
 * The host for one connection; the app makes a new one when the connection changes. changed
 * gets the connection after signing out (also when core refused the key), with any error.
 */
export function appHost(conn: Connection, changed: (c: Connection) => void): Host {
  let signed = conn.signed_in;
  const features = new Set<Feature>();
  if (conn.mode === "local" || conn.mode === "remote") features.add("signOut");
  return {
    async call<T>(method: Method, path: string, body?: unknown): Promise<Reply<T>> {
      try {
        return await invoke<Reply<T>>("core_call", { method, path, body: body ?? null });
      } catch (e) {
        return { status: 0, error: { code: "unavailable", message: String(e) } };
      }
    },
    signedIn: () => signed,
    async signIn(key, remember) {
      try {
        const r = await invoke<Connection>("sign_in", { key, remember });
        signed = r.signed_in;
        // Also when signed in: keeping the key may have failed.
        return r.error ?? (r.signed_in ? undefined : "core refused this key");
      } catch (e) {
        return String(e);
      }
    },
    signOut() {
      signed = false;
      invoke<Connection>("sign_out").then(changed, (e) =>
        changed({ ...conn, signed_in: false, error: `signing out: ${e}` }),
      );
    },
    prefs: {
      get: (name) => read(`pluxy.${name}`),
      set: (name, value) => write(`pluxy.${name}`, value),
    },
    features,
  };
}
