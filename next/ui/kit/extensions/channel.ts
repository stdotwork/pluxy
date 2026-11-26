// The panel's side of the extension page channel, version 0 (extension-pages/README.md): it
// answers one page's calls on one port, for the extension the panel bound the page to. It
// checks every message's shape and size, each method's permission and the call rate, and
// answers each well-formed call once. Nothing a page sends can name another extension.

export const CHANNEL_VERSION = 0;
export const MAX_MESSAGE_BYTES = 64 * 1024;
export const MAX_CALLS_PER_SECOND = 30;
export const MAX_WAITING = 8;
const MAX_ID = 64;

export type ErrorCode =
  | "bad_request"
  | "too_large"
  | "unknown_method"
  | "forbidden"
  | "rate_limited"
  | "invalid_config"
  | "unavailable";

export interface CallError {
  code: ErrorCode;
  message: string;
}

export type Outcome = { result: unknown } | { error: CallError };

/** What the panel does for a page. Each runs for the bound extension only. */
export interface Services {
  theme(): unknown;
  configGet(): Promise<Outcome>;
  configSet(values: Record<string, unknown>): Promise<Outcome>;
}

/** The extension a page belongs to, as core lists it: never what the page says. */
export interface Binding {
  extension: string;
  permissions: readonly string[];
}

/** The part of a MessagePort the channel uses. */
export interface Port {
  postMessage(message: unknown): void;
  onmessage: ((e: MessageEvent) => void) | null;
  start?(): void;
  close(): void;
}

interface Method {
  permission?: string;
  params: "none" | "values";
  run(s: Services, params: Record<string, unknown> | null): Outcome | Promise<Outcome>;
}

const METHODS: Record<string, Method> = {
  "theme.get": { params: "none", run: (s) => ({ result: s.theme() }) },
  "config.get": { permission: "config.read", params: "none", run: (s) => s.configGet() },
  "config.set": {
    permission: "config.write",
    params: "values",
    run: (s, p) => s.configSet((p as { values: Record<string, unknown> }).values),
  },
};

export function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return false;
  const proto = Object.getPrototypeOf(v);
  return proto === Object.prototype || proto === null;
}

/** The message's size as JSON in UTF-8 bytes, or undefined when it is not JSON. */
export function jsonBytes(v: unknown): number | undefined {
  try {
    const text = JSON.stringify(v);
    return text === undefined ? undefined : new TextEncoder().encode(text).length;
  } catch {
    return undefined;
  }
}

function validId(id: unknown): id is string {
  return typeof id === "string" && id.length >= 1 && id.length <= MAX_ID;
}

function sameKeys(o: Record<string, unknown>, keys: string[]): boolean {
  const own = Object.keys(o);
  return own.length === keys.length && keys.every((k) => Object.hasOwn(o, k));
}

export interface Channel {
  /** Sends the page an event, such as the theme when it changes. */
  event(name: string, data: unknown): void;
  /** Stops answering, drops answers still to come and closes the port. */
  close(): void;
  readonly closed: boolean;
}

/** Serves one page's port. now is for tests. */
export function serve(port: Port, binding: Binding, services: Services, now = () => Date.now()): Channel {
  let closed = false;
  let waiting = 0;
  const recent: number[] = [];

  const send = (m: unknown) => {
    if (!closed) port.postMessage(m);
  };
  const fail = (id: string, code: ErrorCode, message: string) =>
    send({ v: CHANNEL_VERSION, id, error: { code, message } });

  port.onmessage = (e) => {
    if (closed) return;
    const m = e.data;
    const id = isPlainObject(m) && validId(m.id) ? m.id : undefined;
    const size = jsonBytes(m);
    if (size === undefined || size > MAX_MESSAGE_BYTES) {
      if (id) fail(id, size === undefined ? "bad_request" : "too_large", "the message is not small JSON");
      return;
    }
    if (
      !isPlainObject(m) ||
      !id ||
      !sameKeys(m, ["v", "id", "method", "params"]) ||
      m.v !== CHANNEL_VERSION
    ) {
      if (id) fail(id, "bad_request", "a call is exactly {v: 0, id, method, params}");
      return;
    }
    if (typeof m.method !== "string" || (m.params !== null && !isPlainObject(m.params))) {
      fail(id, "bad_request", "method must be a string and params an object or null");
      return;
    }
    const t = now();
    while (recent.length > 0 && (recent[0] ?? 0) <= t - 1000) recent.shift();
    if (recent.length >= MAX_CALLS_PER_SECOND || waiting >= MAX_WAITING) {
      fail(id, "rate_limited", "too many calls");
      return;
    }
    recent.push(t);
    const method = Object.hasOwn(METHODS, m.method) ? METHODS[m.method] : undefined;
    if (!method) {
      fail(id, "unknown_method", "there is no such method");
      return;
    }
    if (method.permission && !binding.permissions.includes(method.permission)) {
      fail(id, "forbidden", `the extension did not declare ${method.permission}`);
      return;
    }
    const params = m.params as Record<string, unknown> | null;
    const paramsOk =
      method.params === "none"
        ? params === null
        : params !== null && sameKeys(params, ["values"]) && isPlainObject(params.values);
    if (!paramsOk) {
      fail(
        id,
        "bad_request",
        method.params === "none" ? "params must be null" : "params must be {values: {...}}",
      );
      return;
    }
    let out: Outcome | Promise<Outcome>;
    try {
      out = method.run(services, params);
    } catch {
      out = { error: { code: "unavailable", message: "the call failed" } };
    }
    if (!(out instanceof Promise)) {
      send({ v: CHANNEL_VERSION, id, ...out });
      return;
    }
    // Only calls that wait for core count as waiting.
    waiting++;
    out
      .catch((): Outcome => ({ error: { code: "unavailable", message: "the call failed" } }))
      .then((o) => {
        waiting--;
        send({ v: CHANNEL_VERSION, id, ...o });
      });
  };
  port.start?.();

  return {
    event(name, data) {
      send({ v: CHANNEL_VERSION, event: name, data });
    },
    close() {
      if (closed) return;
      closed = true;
      port.onmessage = null;
      port.close();
    },
    get closed() {
      return closed;
    },
  };
}

/** The version of the channel a page's hello asks for, when the panel speaks one of them. */
export function pickVersion(hello: Record<string, unknown>): number | undefined {
  const versions = hello.versions;
  if (!Array.isArray(versions) || versions.length > 16) return undefined;
  return versions.includes(CHANNEL_VERSION) ? CHANNEL_VERSION : undefined;
}

/** Whether m is a page's hello. */
export function isHello(m: unknown): m is Record<string, unknown> {
  return isPlainObject(m) && m.pluxy === "page" && m.type === "hello";
}
