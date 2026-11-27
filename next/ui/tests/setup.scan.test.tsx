// The Setup page while core scans: the one scan asked on opening, the re-reads every second while
// visible, the line that says pluxy is looking, and the accounts read again when the page is
// looked at again. Against a fake core (setupWorld.tsx), with fake timers.
import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { SetupPage } from "../kit/pages/setup";
import { fakeWindow, restoreDom } from "./fakeDom";
import {
  box,
  buttonIn,
  CLAUDE_CODE,
  flush,
  IMPORT,
  kindRow,
  openStep,
  pick,
  step,
  withPart,
  world,
} from "./setupWorld";

const LOOKING = "Looking at this computer… This takes a few seconds.";

beforeEach(() => {
  Object.defineProperty(globalThis, "location", { configurable: true, value: { hash: "#/setup" } });
});
afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

async function advance(ms: number) {
  for (let done = 0; done < ms; done += 100) {
    await act(async () => {
      jest.advanceTimersByTime(100);
    });
    await flush();
  }
}

const reads = (w: ReturnType<typeof world>) => w.sent("GET", "/setup").length;

test("a pluxy that never scanned is asked to scan once, as the page opens", async () => {
  const w = world({ unscanned: true });
  await w.mount(<SetupPage />);
  expect(w.sent("POST", "/setup/scan")).toHaveLength(1);
});

test("a first scan that fails says so at the top with Try again, and stops saying it is looking", async () => {
  let fail = true;
  const w = world({
    unscanned: true,
    reply: (c) =>
      c.path === "/setup/scan" && fail
        ? { status: 500, error: { code: "internal", message: "the scan broke" } }
        : undefined,
  });
  const m = await w.mount(<SetupPage />);
  expect(w.sent("POST", "/setup/scan")).toHaveLength(1);
  expect(m.text()).not.toContain(LOOKING);
  expect(m.text()).toContain("Could not look at this computer: the scan broke.");
  fail = false;
  await act(async () => {
    buttonIn(m.root, "Try again").dispatchEvent(new Event("click"));
  });
  await flush();
  expect(w.sent("POST", "/setup/scan")).toHaveLength(2);
  expect(m.text()).not.toContain("Could not look at this computer");
});

test("Try again after a refused preview waits for core's background scan, then previews what it found", async () => {
  jest.useFakeTimers();
  let refused = false;
  const w = world({
    imports: [IMPORT],
    reply: (c) => {
      if (c.path === "/setup/import") {
        if (refused)
          return {
            status: 200,
            body: { dry_run: true, brought: [], changed: [], not_brought: [], needs: [] },
          };
        refused = true;
        return { status: 400, error: { code: "invalid_request", message: "no file" } };
      }
      // Core answers a scan at once and scans in the background (StartSetupScan).
      if (c.path === "/setup/scan") {
        w.state.setup.scanning = true;
        return { status: 200, body: structuredClone(w.state.setup) };
      }
      return undefined;
    },
  });
  const m = await w.mount(<SetupPage />);
  const line = () => withPart(m.root, "setup-import")[0] ?? m.root;
  const sentDirs = () =>
    w.sent("POST", "/setup/import").map((c) => (c.body as { auth_dir?: string }).auth_dir);
  await act(async () => {
    buttonIn(line(), "Preview import").dispatchEvent(new Event("click"));
  });
  await flush();
  await act(async () => {
    buttonIn(line(), "Try again").dispatchEvent(new Event("click"));
  });
  await flush();
  // Still scanning: no new preview yet.
  await advance(1000);
  expect(sentDirs()).toEqual(["~/.cli-proxy-api/auths"]);
  // The scan ends with the config naming another auth folder.
  w.state.setup.scanning = false;
  w.state.setup.scanned_at = "2026-11-20T11:00:00Z";
  w.state.setup.found.imports = [{ ...IMPORT, auth_dir: "~/fresh/auths" }];
  await advance(1000);
  expect(sentDirs()).toEqual(["~/.cli-proxy-api/auths", "~/fresh/auths"]);
  expect(withPart(m.root, "setup-report")).toHaveLength(1);
});

test("while the first scan runs the page says it is looking, reads every second, and shows what it found", async () => {
  jest.useFakeTimers();
  const w = world({
    unscanned: true,
    scanning: true,
    clients: [],
    picks: [pick("pluxy/claude", "Claude")],
  });
  const m = await w.mount(<SetupPage />);
  expect(m.text()).toContain(LOOKING);
  expect(w.sent("POST", "/setup/scan")).toHaveLength(0);
  expect(reads(w)).toBe(1);
  // Steps stay usable meanwhile.
  expect(openStep(m.root)).toBe(1);
  await advance(1000);
  expect(reads(w)).toBe(2);
  // The scan ends: Claude Code is found, and core suggests Claude for it.
  w.state.setup.scanning = false;
  w.state.setup.scanned_at = "2026-11-20T10:00:00Z";
  w.state.setup.found.clients = [CLAUDE_CODE];
  w.state.setup.picks = [
    pick("pluxy/claude", "Claude", {
      suggested: true,
      because: "Claude Code is installed and signed in on this computer",
    }),
  ];
  await advance(1000);
  expect(reads(w)).toBe(3);
  expect(m.text()).not.toContain(LOOKING);
  expect(box(kindRow(m.root, "Claude"))?.checked).toBe(true);
  expect(kindRow(m.root, "Claude").textContent).toContain(
    "Claude Code is installed and signed in on this computer",
  );
  expect(openStep(m.root)).toBe(1);
  await advance(3000);
  expect(reads(w)).toBe(3);
});

test("a hidden page does not read while the scan runs", async () => {
  jest.useFakeTimers();
  const w = world({ unscanned: true, scanning: true });
  const m = await w.mount(<SetupPage />);
  (m.document as unknown as { visibilityState: string }).visibilityState = "hidden";
  await advance(3000);
  expect(reads(w)).toBe(1);
});

test("leaving while core scans stops the re-reads", async () => {
  jest.useFakeTimers();
  const w = world({ unscanned: true, scanning: true });
  const m = await w.mount(<SetupPage />);
  await advance(1000);
  expect(reads(w)).toBe(2);
  await m.unmount();
  await advance(3000);
  expect(reads(w)).toBe(2);
});

test("the accounts are read again on focus and when the page is shown again", async () => {
  const w = world();
  const m = await w.mount(<SetupPage />);
  const before = w.sent("GET", "/accounts").length;
  w.state.accounts.push({
    id: "acc_1",
    source: "login",
    state: "active",
    refreshable: false,
    extension: "pluxy/claude",
  });
  fakeWindow().dispatch("focus");
  await flush();
  expect(w.sent("GET", "/accounts").length).toBe(before + 1);
  expect(step(m.root, 1).textContent).toContain("1 account connected.");
  m.document.dispatch("visibilitychange");
  await flush();
  expect(w.sent("GET", "/accounts").length).toBe(before + 2);
});
