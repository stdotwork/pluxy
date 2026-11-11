import { afterEach, expect, test } from "bun:test";
import { browserHost } from "../panel/browserHost";

class MemoryStorage {
  values = new Map<string, string>();
  getItem(name: string) {
    return this.values.get(name) ?? null;
  }
  setItem(name: string, value: string) {
    this.values.set(name, value);
  }
  removeItem(name: string) {
    this.values.delete(name);
  }
}
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
  if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
  else Reflect.deleteProperty(globalThis, "window");
});
const response = (status: number, body?: unknown) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status });
function tab(localStorage = new MemoryStorage()) {
  const window = Object.assign(new EventTarget(), { localStorage, sessionStorage: new MemoryStorage() });
  Object.defineProperty(globalThis, "window", { configurable: true, value: window });
  return { window, host: browserHost() };
}
function storageEvent(window: EventTarget, storage: MemoryStorage, oldValue: string, newValue: string) {
  window.dispatchEvent(
    Object.assign(new Event("storage"), { key: "pluxy.key", storageArea: storage, oldValue, newValue }),
  );
}

test("an old-key 401 after rotation retries with the new key", async () => {
  const old = crypto.randomUUID();
  const next = crypto.randomUUID();
  const local = new MemoryStorage();
  local.setItem("pluxy.key", old);
  const { host } = tab(local);
  let finish!: (response: Response) => void;
  const authorizations: string[] = [];
  globalThis.fetch = (async (url, init) => {
    authorizations.push(new Headers(init?.headers).get("Authorization") ?? "");
    if (String(url).endsWith("/management-key/rotate")) return response(200, { value: next });
    if (authorizations.length === 1)
      return new Promise<Response>((resolve) => {
        finish = resolve;
      });
    return response(200, {});
  }) as typeof fetch;
  const pending = host.call("GET", "/status");
  await host.call("POST", "/management-key/rotate");
  finish(response(401));
  expect((await pending).status).toBe(200);
  expect(authorizations.at(-1) === `Bearer ${next}`).toBe(true);
  expect(host.signedIn()).toBe(true);
  expect(local.getItem("pluxy.key") === next).toBe(true);
});

test("another remembered tab adopts rotation and retries its in-flight old-key call", async () => {
  const old = crypto.randomUUID();
  const next = crypto.randomUUID();
  const local = new MemoryStorage();
  local.setItem("pluxy.key", old);
  const first = tab(local);
  const second = tab(local);
  let finish!: (response: Response) => void;
  let calls = 0;
  globalThis.fetch = (async (url, init) => {
    if (String(url).endsWith("/management-key/rotate")) return response(200, { value: next });
    if (++calls === 1)
      return new Promise<Response>((resolve) => {
        finish = resolve;
      });
    return response(new Headers(init?.headers).get("Authorization") === `Bearer ${next}` ? 200 : 401, {});
  }) as typeof fetch;
  const pending = second.host.call("GET", "/status");
  await first.host.call("POST", "/management-key/rotate");
  storageEvent(second.window, local, old, next);
  finish(response(401));
  expect((await pending).status).toBe(200);
  expect((await second.host.call("GET", "/status")).status).toBe(200);
  expect(local.getItem("pluxy.key") === next).toBe(true);
});

test("signOut after a 401 cannot erase a newer stored key before its storage event", async () => {
  const old = crypto.randomUUID();
  const next = crypto.randomUUID();
  const local = new MemoryStorage();
  local.setItem("pluxy.key", old);
  const { host } = tab(local);
  globalThis.fetch = (async () => response(401)) as unknown as typeof fetch;
  expect((await host.call("GET", "/status")).status).toBe(401);
  local.setItem("pluxy.key", next);
  host.signOut();
  expect(local.getItem("pluxy.key") === next).toBe(true);
});

test("a normal 401 still signs out and clears its rejected stored key", async () => {
  const local = new MemoryStorage();
  local.setItem("pluxy.key", crypto.randomUUID());
  const { host } = tab(local);
  globalThis.fetch = (async () => response(401)) as unknown as typeof fetch;
  expect((await host.call("GET", "/status")).status).toBe(401);
  host.signOut();
  expect(host.signedIn()).toBe(false);
  expect(local.getItem("pluxy.key")).toBeNull();
});
