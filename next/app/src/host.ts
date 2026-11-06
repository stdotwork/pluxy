// The kit's host in the desktop app. The management key never reaches this webview: every
// management call goes to the app's native side (the core_call command), which adds the key and
// sends the call to the core the app is connected to.

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
  key?: string;
  remember?: boolean;
  ca_file?: string;
  core_path?: string;
}

export const connectionGet = () => invoke<Connection>("connection_get");
export const connectionSet = (change: ConnectionChange) => invoke<Connection>("connection_set", { change });
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

/** The host for one connection; the app makes a new one when the connection changes. */
export function appHost(conn: Connection): Host {
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
        return r.signed_in ? undefined : (r.error ?? "core refused this key");
      } catch (e) {
        return String(e);
      }
    },
    signOut() {
      signed = false;
      invoke("sign_out").catch(() => {});
    },
    prefs: {
      get: (name) => read(`pluxy.${name}`),
      set: (name, value) => write(`pluxy.${name}`, value),
    },
    features,
  };
}
