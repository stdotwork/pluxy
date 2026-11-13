// The kit's host in the desktop app. The native side holds the management key: every management
// call goes to it (the core_call command), and it adds the key and sends the call to the core
// the app is connected to. Keys are entered only in a native dialog.

import { invoke } from "@tauri-apps/api/core";
import { flushSync } from "preact/compat";
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
  core?: { state: "running" | "stopped" | "recovering" | "paused"; pid?: number; detail?: string };
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
/** Starts the app's own core only if it is not running; a running one is left alone. */
export const coreStart = () => invoke<Connection>("core_start");
/** The Connection page's core button: Restart while the core runs, Start otherwise. */
export const coreAction = (c: Connection) => (c.core?.state === "running" ? coreRestart : coreStart);

/** Core's rotation route. The window never calls it: the native side rotates the key itself. */
const ROTATE = "/management-key/rotate";

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
 * refreshed gets it after a rotation, which can change how the key is kept and the error the
 * Connection page shows, but not the connection itself.
 */
export function appHost(
  conn: Connection,
  changed: (c: Connection) => void,
  refreshed: (c: Connection) => void = () => {},
): Host {
  let signed = conn.signed_in;
  const features = new Set<Feature>(["rotateManagementKey"]);
  if (conn.mode === "local" || conn.mode === "remote") features.add("signOut");
  return {
    async call<T>(method: Method, path: string, body?: unknown): Promise<Reply<T>> {
      try {
        // Native, behind a native confirmation: the reply says how the new key is kept, never the key.
        if (method === "POST" && path === ROTATE) {
          const reply = await invoke<Reply<T>>("management_key_rotate");
          connectionGet().then(refreshed, () => {});
          return reply;
        }
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
      // Remove Shell before it renders its browser sign-in form, including on a core 401.
      flushSync(() => changed({ ...conn, signed_in: false }));
      invoke<Connection>("sign_out").then(changed, (e) =>
        changed({ ...conn, signed_in: false, error: `signing out: ${e}` }),
      );
    },
    prefs: {
      get: (name) => read(`pluxy.${name}`),
      set: (name, value) => write(`pluxy.${name}`, value),
    },
    features,
    address: conn.target?.replace(/\/+$/, "") || undefined,
  };
}

/** Privacy mode, kept natively (privacy-mode.json), never in the window's storage. */
export const privacyGet = () => invoke<boolean>("privacy_get");
export const privacySet = (on: boolean) => invoke<boolean>("privacy_set", { on });

export interface StartupPreferences {
  autostart_enabled: boolean;
  silent_start_enabled: boolean;
  start_core_on_launch: boolean;
}
export interface StartupView extends StartupPreferences {
  registration_owned: boolean;
  registration_conflict: boolean;
}
export const startupGet = () => invoke<StartupView>("startup_get");
export const startupSet = (preferences: StartupPreferences) =>
  invoke<StartupView>("startup_set", { preferences });

/** What closing the window does: the old app's choices and names. */
export type CloseBehavior = "ask" | "exit" | "minimize-to-tray";
export interface WindowPreferences {
  close_behavior: CloseBehavior;
  /** Whether the desktop shows the app's tray icon now; without it, closing the window quits. */
  tray: boolean;
}
export const windowGet = () => invoke<WindowPreferences>("window_get");
export const windowSet = (closeBehavior: CloseBehavior) =>
  invoke<WindowPreferences>("window_set", { closeBehavior });
/** The answer to the close prompt. */
export const windowClose = (action: Exclude<CloseBehavior, "ask">, remember: boolean) =>
  invoke<void>("window_close", { action, remember });
