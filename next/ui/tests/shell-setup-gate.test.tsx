// The shell's Setup gate (kit/shell.tsx): what GET /setup decides, and what happens when it answers
// after SETUP_GATE_WAIT_MS has already let Home appear.
import { afterEach, beforeEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import type { Host, Reply } from "../kit/host";
import type { PrivacyState } from "../kit/privacy";
import { type PageDef, SETUP_GATE_WAIT_MS, SETUP_PAGE, Shell, setupGate } from "../kit/shell";
import type { ThemeState } from "../kit/theme/state";
import { mountIn, restoreDom, settle } from "./fakeDom";
import { withPart } from "./setupWorld";

const place = {
  hash: "",
  replaced: [] as string[],
  replace(url: string) {
    this.replaced.push(url);
    this.hash = url;
  },
};
const originalLocation = Object.getOwnPropertyDescriptor(globalThis, "location");
const originalSetTimeout = globalThis.setTimeout;
const originalClearTimeout = globalThis.clearTimeout;

beforeEach(() => {
  place.hash = "";
  place.replaced = [];
  Object.defineProperty(globalThis, "location", { configurable: true, value: place });
});

afterEach(() => {
  globalThis.setTimeout = originalSetTimeout;
  globalThis.clearTimeout = originalClearTimeout;
  restoreDom();
  if (originalLocation) Object.defineProperty(globalThis, "location", originalLocation);
  else Reflect.deleteProperty(globalThis, "location");
});

test("setupGate opens Setup only on a first run with an empty hash, and hides it on 404", () => {
  expect(setupGate({ status: 200, body: { first_run: true } }, "")).toEqual({ offered: true, open: true });
  expect(setupGate({ status: 200, body: { first_run: true } }, "#/")).toEqual({ offered: true, open: true });
  expect(setupGate({ status: 200, body: { first_run: true } }, "#/accounts")).toEqual({
    offered: true,
    open: false,
  });
  expect(setupGate({ status: 200, body: { first_run: false } }, "")).toEqual({ offered: true, open: false });
  expect(setupGate({ status: 404 }, "")).toEqual({ offered: false, open: false });
});

test("a Setup answer that arrives after SETUP_GATE_WAIT_MS leaves Home on screen", async () => {
  let resolveSetup: (r: Reply<{ first_run: boolean }>) => void = () => {};
  const gateTimers: (() => void)[] = [];
  globalThis.setTimeout = ((fn: () => void, ms?: number) => {
    if (ms === SETUP_GATE_WAIT_MS) {
      gateTimers.push(fn);
      return 99999 as unknown as ReturnType<typeof setTimeout>;
    }
    return originalSetTimeout(fn, ms);
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((id: unknown) => {
    if (id === 99999) return;
    originalClearTimeout(id as ReturnType<typeof setTimeout>);
  }) as typeof clearTimeout;

  const host: Host = {
    call: async <T,>(_: string, path: string): Promise<Reply<T>> => {
      if (path === "/setup") {
        return (await new Promise<Reply<{ first_run: boolean }>>((r) => {
          resolveSetup = r;
        })) as unknown as Reply<T>;
      }
      return { status: 200, body: {} as T };
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  };
  const themes = {
    resolved: { layout: { nav: "sidebar", tabs: "top" } },
    connect: () => () => {},
    subscribe: () => () => {},
  } as unknown as ThemeState;
  const privacy = { on: false, set: () => {}, subscribe: () => () => {} } as unknown as PrivacyState;
  const pages: PageDef[] = [
    { id: "status", dest: "home", title: "Home", render: () => <p>Home body</p> },
    { id: SETUP_PAGE, dest: "settings", kind: "topic", title: "Setup", render: () => <p>Setup body</p> },
  ];

  const m = await mountIn(<Shell host={host} themes={themes} privacy={privacy} pages={pages} />);
  // While GET /setup is pending and within the wait, only the focused bare ground is shown.
  expect(withPart(m.root, "focus")).toHaveLength(1);
  expect(m.text()).not.toContain("Home body");
  expect(gateTimers).toHaveLength(1);

  // Once SETUP_GATE_WAIT_MS elapses, Home appears in the frame.
  await act(async () => {
    gateTimers[0]?.();
  });
  await settle();
  expect(m.text()).toContain("Home body");
  expect(withPart(m.root, "nav")).toHaveLength(1);

  // A late first_run: true answer does not replace Home or rewrite the address, and Setup stays offered.
  await act(async () => {
    resolveSetup({ status: 200, body: { first_run: true } });
  });
  await settle();
  expect(place.replaced).toEqual([]);
  expect(place.hash).toBe("");
  expect(m.text()).toContain("Home body");
  expect(m.text()).not.toContain("Setup body");
  expect(m.text()).toContain("Settings");
});
