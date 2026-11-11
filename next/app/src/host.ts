// The kit's host in the desktop app. The native side holds the management key: every management
// call goes to it (the core_call command), and it adds the key and sends the call to the core
// the app is connected to. Keys are entered only in a native dialog.

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
  /** The core this app runs, in managed mode. */
  core?: { state: "running" | "stopped"; pid?: number; detail?: string };
  /** Why the app is not connected, when it is not. */
  error?: string;
}

/** What the person sets on the Connection page. */
export interface ConnectionChange {
  mode: Mode;
  url?: string;
  ca_file?: string;
  core_path?: string;
}

export const connectionGet = () => invoke<Connection>("connection_get");
export const connectionSet = (change: ConnectionChange) => invoke<Connection>("connection_set", { change });
export interface KeyEntryResult {
  status: "signed_in" | "wrong_key" | "cancelled" | "error";
  connection: Connection;
}
export const keyEntry = (change?: ConnectionChange) =>
  invoke<KeyEntryResult>("key_entry", { change: change ?? null });
export const coreRestart = () => invoke<Connection>("core_restart");

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
    async signIn() {
      const result = await keyEntry();
      signed = result.connection.signed_in;
      changed(result.connection);
      return result.status === "signed_in" ? undefined : (result.connection.error ?? "Sign-in cancelled");
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
