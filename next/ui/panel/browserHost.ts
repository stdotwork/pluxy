// The kit's host in a browser, for the panel core serves. The panel only ever talks to the core
// that served it: every call goes to this origin's /v0/management, and only those calls carry
// the management key.

import type { Feature, Host, Method, Reply } from "../kit/host";

const API = "/v0/management";
const KEY = "pluxy.key";
const PREFS = "pluxy.";

/**
 * What a person reads when pluxy refused `GET /status` at sign-in with a status other than the ones
 * the sign-in names itself. Core's reason (its error message) goes with the status; a core with
 * remote management off gets what to change on its machine. The desktop app says the same in
 * `check_key` (next/app/src-tauri/src/conn.rs).
 */
export function refusal(status: number, error?: { code: string; message: string }): string {
  if (status === 403 && error?.code === "remote_disabled") {
    return 'This pluxy answers only the computer it runs on. On that computer, set "management_allow_remote": true in pluxy\'s config.json (run pluxy help there to see where the file is) and save it. It picks the change up by itself, so then just sign in again.';
  }
  return error?.message
    ? `The reply from pluxy was ${status}: ${error.message}`
    : `The reply from pluxy was ${status}.`;
}

/** Storage that may be missing or refuse (private windows, blocked site data). */
function store(kind: "localStorage" | "sessionStorage"): Storage | undefined {
  try {
    return window[kind];
  } catch {
    return undefined;
  }
}

function read(s: Storage | undefined, name: string): string | null {
  try {
    return s?.getItem(name) ?? null;
  } catch {
    return null;
  }
}

function write(s: Storage | undefined, name: string, value: string | null) {
  try {
    if (value === null) s?.removeItem(name);
    else s?.setItem(name, value);
  } catch {
    // Nothing to keep it in: it lasts until the page closes.
  }
}

/** Calls fn with a pref's new value when another tab of this panel changes it. */
export function watchPref(name: string, fn: (value: string | null) => void) {
  window.addEventListener("storage", (event) => {
    if (event.storageArea === store("localStorage") && event.key === PREFS + name) fn(event.newValue);
  });
}

/**
 * heard is called after each request to core with whether any reply came back, so the panel can
 * say whether pluxy answers.
 */
export function browserHost(options?: { heard?: (answered: boolean) => void }): Host {
  const local = store("localStorage");
  const session = store("sessionStorage");
  let key = read(session, KEY) ?? read(local, KEY);
  let remember = read(session, KEY) === null && read(local, KEY) !== null;
  let rejectedKey: string | undefined;
  window.addEventListener("storage", (event) => {
    if (remember && event.storageArea === local && event.key === KEY && event.newValue) {
      key = event.newValue;
    }
  });

  function keep(candidate: string) {
    key = candidate;
    rejectedKey = undefined;
    write(remember ? local : session, KEY, candidate);
    write(remember ? session : local, KEY, null);
  }

  async function send<T>(method: Method, path: string, body: unknown, withKey: string): Promise<Reply<T>> {
    if (!path.startsWith("/") || path.split("/").includes("..")) {
      return { status: 400, error: { code: "invalid_request", message: `bad management path ${path}` } };
    }
    const headers: Record<string, string> = { Authorization: `Bearer ${withKey}` };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    let resp: Response;
    try {
      resp = await fetch(API + path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: "omit",
        cache: "no-store",
        redirect: "error",
        referrerPolicy: "no-referrer",
      });
    } catch {
      options?.heard?.(false);
      return { status: 0 };
    }
    options?.heard?.(true);
    let text: string;
    try {
      text = await resp.text();
    } catch {
      return { status: 0 };
    }
    let json: unknown;
    try {
      json = text ? JSON.parse(text) : undefined;
    } catch {
      json = undefined;
    }
    const err = (json as { error?: { code: string; message: string } } | undefined)?.error;
    return resp.ok ? { status: resp.status, body: json as T } : { status: resp.status, error: err };
  }

  const host: Host = {
    async call<T>(method: Method, path: string, body?: unknown) {
      if (!key) return { status: 401, error: { code: "unauthorized", message: "not signed in" } };
      let usedKey = key;
      let reply = await send<T>(method, path, body, usedKey);
      if (reply.status === 401 && key && key !== usedKey) {
        usedKey = key;
        reply = await send<T>(method, path, body, usedKey);
      }
      if (reply.status === 401) rejectedKey = usedKey;
      if (method === "POST" && path === "/management-key/rotate" && reply.status === 200) {
        const value = (reply.body as { value?: unknown } | undefined)?.value;
        if (typeof value === "string" && value.length > 0) keep(value);
      }
      return reply;
    },
    signedIn: () => key !== null,
    async signIn(candidate, keepOnDevice) {
      const r = await send("GET", "/status", undefined, candidate);
      if (r.status === 401) return "This management key was not accepted.";
      if (r.status === 404) return "No pluxy answered at this address.";
      if (r.status !== 200) return r.status === 0 ? "Could not reach pluxy." : refusal(r.status, r.error);
      remember = keepOnDevice;
      keep(candidate);
      return undefined;
    },
    signOut() {
      const failed = rejectedKey ?? key;
      key = null;
      rejectedKey = undefined;
      if (read(session, KEY) === failed) write(session, KEY, null);
      if (read(local, KEY) === failed) write(local, KEY, null);
    },
    prefs: {
      get: (name) => read(local, PREFS + name),
      set: (name, value) => write(local, PREFS + name, value),
    },
    features: new Set<Feature>(["signOut", "rotateManagementKey"]),
    address: window.location.origin,
  };
  return host;
}
