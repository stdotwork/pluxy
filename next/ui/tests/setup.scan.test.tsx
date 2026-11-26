// The Setup page while core scans: its re-reads, leaving, looking again, and a failed re-read.
// And bringing a CLIProxyAPI setup across: the dry run, the import, and a refusal.
// These drive the real page through Preact's DOM renderer against a fake host, using a small DOM
// that carries only the nodes and events Preact needs.
import { afterEach, expect, jest, test } from "bun:test";
import { render } from "preact";
import { act } from "preact/test-utils";
import { api, type ImportReport, type Setup } from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { SetupPage } from "../kit/pages/setup";
import { type Kit, KitContext, useCopy } from "../kit/ui";
import { all, button, type FakeNode, fakeWindow, installDocument, restoreDom, settle } from "./fakeDom";

interface Call {
  method: Method;
  path: string;
  body?: unknown;
  at: number;
}

let clock = 0;

const core = { listen: "127.0.0.1:8317", reachable_from_network: false };
const pick = (id: string, title: string, more: Partial<Setup["picks"][number]> = {}) => ({
  id,
  kind: "provider",
  title,
  summary: `${title} subscriptions and API keys`,
  installed: false,
  suggested: false,
  ...more,
});

function setup(scanning: boolean, scanned_at?: string, client = false): Setup {
  return {
    first_run: true,
    scanning,
    scanned_at,
    found: {
      clients: client
        ? [
            {
              id: "claude-code",
              name: "Claude Code",
              installed: true,
              version: "2.4.1",
              config: "~/.claude/settings.json",
              state: "not-configured",
            },
          ]
        : [],
      sign_ins: [],
      imports: [],
      core,
    },
    picks: [
      pick("pluxy/codex", "Codex", { suggested: true }),
      pick("pluxy/claude", "Claude", { suggested: true }),
    ],
  };
}

afterEach(() => {
  restoreDom();
  clock = 0;
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

function fakeHost(answer: (call: Call) => Reply<unknown>, features: Feature[] = ["signOut"]) {
  const calls: Call[] = [];
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) => {
      const call = { method, path, body, at: clock };
      calls.push(call);
      return answer(call) as Reply<T>;
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(features),
    address: "http://127.0.0.1:8317",
  };
  return { host, calls, api: api(host) };
}

const quiet = (call: Call): Reply<unknown> => {
  if (call.method === "GET" && call.path === "/accounts") return { status: 200, body: { accounts: [] } };
  if (call.method === "GET" && call.path === "/packages") return { status: 200, body: { packages: [] } };
  if (call.method === "POST" && call.path === "/setup/scan") {
    return { status: 200, body: setup(false, "2026-11-20T00:00:00Z") };
  }
  return { status: 200, body: {} };
};

async function mount(answer: (call: Call) => Reply<unknown>, features?: Feature[]) {
  const document = installDocument();
  const fake = fakeHost(answer, features);
  const signedOut = { times: 0 };
  const kit = {
    host: fake.host,
    api: fake.api,
    unauthorized: () => {
      signedOut.times++;
    },
    extensionsChanged: () => {},
  } as Kit;
  const root = document.createElement("main");
  document.body.appendChild(root);
  await act(async () => {
    render(
      <KitContext.Provider value={kit}>
        <SetupPage />
      </KitContext.Provider>,
      root,
    );
  });
  await settle();
  return {
    ...fake,
    root,
    signedOut,
    text: () => root.textContent,
    unmount: async () => {
      await act(async () => render(null, root));
    },
  };
}

function at<T>(items: T[], index: number): T {
  const item = items[index];
  if (item === undefined) throw new Error(`no item at ${index}`);
  return item;
}

function nextSetup(scans: Setup[]): Setup {
  const body = scans.shift();
  if (!body) throw new Error("no setup reply left for this request");
  return body;
}

async function tick(milliseconds: number) {
  clock += milliseconds;
  await act(async () => {
    jest.advanceTimersByTime(milliseconds);
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

async function tickUntil(check: () => boolean, limit = 5000) {
  for (let elapsed = 0; elapsed < limit; elapsed += 100) {
    if (check()) return elapsed;
    await tick(100);
  }
  if (check()) return limit;
  throw new Error(`condition did not become true within ${limit} ms`);
}

test("while scanning, the page reads about once a second, stops, and shows what arrived", async () => {
  jest.useFakeTimers();
  const scans = [setup(true), setup(true), setup(false, "2026-11-20T00:00:00Z", true)];
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: nextSetup(scans) };
    return quiet(call);
  });
  const setupReads = () => fake.calls.filter((call) => call.method === "GET" && call.path === "/setup");

  expect(setupReads()).toHaveLength(1);
  expect(jest.getTimerCount()).toBe(1);
  expect(fake.text()).toContain("pluxy is looking at this machine.");
  expect(fake.text()).not.toContain("Claude Code 2.4.1");

  await tick(999);
  expect(setupReads()).toHaveLength(1);
  await tickUntil(() => setupReads().length === 2);
  expect(at(setupReads(), 1).at - at(setupReads(), 0).at).toBeGreaterThanOrEqual(999);
  expect(at(setupReads(), 1).at - at(setupReads(), 0).at).toBeLessThan(1300);
  expect(fake.text()).toContain("pluxy is looking at this machine.");

  await tickUntil(() => setupReads().length === 3);
  expect(at(setupReads(), 2).at - at(setupReads(), 1).at).toBeGreaterThanOrEqual(999);
  expect(at(setupReads(), 2).at - at(setupReads(), 1).at).toBeLessThan(1300);
  expect(fake.text()).toContain("Claude Code 2.4.1");
  expect(fake.text()).not.toContain("pluxy is looking at this machine.");

  await tick(5000);
  expect(setupReads()).toHaveLength(3);
});

test("leaving while core scans stops the re-reads", async () => {
  jest.useFakeTimers();
  const scans = [setup(true), setup(true)];
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: nextSetup(scans) };
    return quiet(call);
  });
  const setupCalls = () =>
    fake.calls.filter((call) => call.method === "GET" && call.path === "/setup").length;

  await tickUntil(() => setupCalls() === 2);
  expect(setupCalls()).toBe(2);
  expect(jest.getTimerCount()).toBe(1);
  await fake.unmount();
  await tick(5000);
  expect(setupCalls()).toBe(2);
});

test("Refresh asks core to scan once and then reads its answer again", async () => {
  jest.useFakeTimers();
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") {
      return { status: 200, body: setup(false, "2026-11-20T00:00:00Z") };
    }
    return quiet(call);
  });
  const setupCalls = () =>
    fake.calls.filter((call) => call.method === "GET" && call.path === "/setup").length;
  const scans = () =>
    fake.calls.filter((call) => call.method === "POST" && call.path === "/setup/scan").length;
  expect(setupCalls()).toBe(1);

  button(fake.root, "Refresh").dispatchEvent(new Event("click"));
  await settle();
  expect(scans()).toBe(1);
  expect(setupCalls()).toBe(2);

  await tick(5000);
  expect(scans()).toBe(1);
  expect(setupCalls()).toBe(2);
});

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;

/** The nodes below root carrying data-part, in page order. */
function parts(root: FakeNode, part: string): FakeNode[] {
  return [
    ...(attribute(root, "data-part") === part ? [root] : []),
    ...root.childNodes.flatMap((child) => parts(child, part)),
  ];
}

test("Refresh reads the packages again, so a provider started since shows as running", async () => {
  jest.useFakeTimers();
  const installed: Setup = {
    ...setup(false, "2026-11-20T00:00:00Z"),
    picks: [pick("pluxy/claude", "Claude", { installed: true })],
  };
  const claude = (enabled: boolean) => ({
    id: "pluxy/claude",
    version: "1.0.0",
    registry: "bundled",
    url: "https://packages.example.test/claude",
    sha256: "0".repeat(64),
    enabled,
    installed_at: "2026-11-20T10:00:00Z",
    state: enabled ? "active" : "disabled",
  });
  let turnedOn = false;
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: installed };
    if (call.method === "GET" && call.path === "/packages") {
      return { status: 200, body: { packages: [claude(turnedOn)] } };
    }
    return quiet(call);
  });
  const badge = () => {
    const row = at(parts(fake.root, "setup-pick"), 0);
    return attribute(at(parts(row, "badge"), 0), "data-tone");
  };
  expect(reads(fake.calls, "/packages")).toBe(1);
  expect(badge()).toBe("warn");

  // The provider is turned on somewhere else; the page says so once it is looked at again.
  turnedOn = true;
  await press(fake.root, "Refresh");
  expect(reads(fake.calls, "/packages")).toBe(2);
  expect(badge()).toBe("ok");
});

for (const failure of [
  {
    status: 0,
    shown: "cannot reach pluxy; check that it is running and the address is right, then try again",
  },
  { status: 500, shown: "pluxy returned HTTP 500; check Logs Viewer and try again" },
] as const) {
  test(`a ${failure.status} re-read keeps the page and shows ${failure.shown}`, async () => {
    jest.useFakeTimers();
    let setupReads = 0;
    const fake = await mount((call) => {
      if (call.method === "GET" && call.path === "/setup") {
        setupReads++;
        return setupReads === 1
          ? { status: 200, body: setup(false, "2026-11-20T00:00:00Z", true) }
          : { status: failure.status };
      }
      return quiet(call);
    });
    expect(fake.text()).toContain("Claude Code 2.4.1");

    button(fake.root, "Refresh").dispatchEvent(new Event("click"));
    await settle();
    expect(fake.text()).toContain(failure.shown);
    expect(fake.text()).toContain("Claude Code 2.4.1");
    expect(fake.text()).toContain("Codex");
    expect(fake.text()).toContain("Claude");
  });
}

/** A scanned machine with a CLIProxyAPI setup, and two providers offered with nothing ticked. */
function withImport(): Setup {
  const base = setup(false, "2026-11-20T00:00:00Z");
  return {
    ...base,
    found: {
      ...base.found,
      imports: [
        { kind: "cliproxyapi", config: "~/.cli-proxy-api/config.yaml", auth_dir: "~/cpa/auths", accounts: 2 },
      ],
    },
    picks: base.picks.map((p) => ({ ...p, suggested: false })),
  };
}

const report = (dry_run: boolean): ImportReport => ({
  dry_run,
  brought: [{ what: "auth file", name: "claude-a.json", as: "pluxy/claude account claude-a" }],
  changed: [],
  not_brought: [{ what: "debug", why: "this core writes no debug-level log lines" }],
  needs: [{ pick: "pluxy/claude", why: "1 account needs it" }],
});

async function press(root: FakeNode, label: string) {
  await act(async () => {
    button(root, label).dispatchEvent(new Event("click"));
  });
  await settle();
}

const imports = (calls: Call[]) => calls.filter((call) => call.path === "/setup/import");
const reads = (calls: Call[], path: string) =>
  calls.filter((call) => call.method === "GET" && call.path === path).length;
const tickedBoxes = (root: FakeNode) => all(root, "input").filter((node) => node.checked).length;

test("bringing a setup across is a dry run first, then the import, which re-reads and ticks what is needed", async () => {
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: withImport() };
    if (call.path === "/setup/import") {
      return { status: 200, body: report((call.body as { dry_run: boolean }).dry_run) };
    }
    return quiet(call);
  });
  expect(tickedBoxes(fake.root)).toBe(0);

  await press(fake.root, "Bring it across");
  expect(imports(fake.calls).map((call) => call.body)).toEqual([
    { config: "~/.cli-proxy-api/config.yaml", auth_dir: "~/cpa/auths", dry_run: true },
  ]);
  expect(fake.text()).toContain("Nothing is written yet.");
  expect(fake.text()).toContain("Comes across (1)");
  expect(fake.text()).toContain("Does not come across (1)");
  // A dry run changes nothing on the page either.
  expect(tickedBoxes(fake.root)).toBe(0);
  expect(reads(fake.calls, "/setup")).toBe(1);

  // Cancel puts the row back as it was, and sends nothing.
  await press(fake.root, "Cancel");
  expect(fake.text()).not.toContain("Comes across (1)");
  expect(imports(fake.calls)).toHaveLength(1);

  await press(fake.root, "Bring it across");
  await press(fake.root, "Bring it across now");
  expect(at(imports(fake.calls), 2).body).toEqual({
    config: "~/.cli-proxy-api/config.yaml",
    auth_dir: "~/cpa/auths",
    dry_run: false,
  });
  expect(fake.text()).toContain("Done. This is what was brought across:");
  expect(fake.text()).toContain("Brought across (1)");
  expect(fake.text()).toContain(
    "Claude is not installed, and 1 account needs it. It is ticked under Providers",
  );
  // The provider the accounts need is ticked, and Start now installs it.
  expect(tickedBoxes(fake.root)).toBe(1);
  expect(fake.text()).toContain("Start installs: Claude.");
  // Setup, the accounts and the packages are read again by the page.
  expect(reads(fake.calls, "/setup")).toBe(2);
  expect(reads(fake.calls, "/accounts")).toBe(2);
  expect(reads(fake.calls, "/packages")).toBe(2);
  expect(fake.signedOut.times).toBe(0);

  await press(fake.root, "Close");
  expect(fake.text()).not.toContain("Brought across (1)");
  expect(tickedBoxes(fake.root)).toBe(1);
});

test("once the report of an import is closed, the row says it was brought across and offers no button", async () => {
  let imported = false;
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") {
      const body = withImport();
      const last_import = { at: "2026-11-20T08:30:05Z", brought: 1, changed: 0, not_brought: 1 };
      if (imported) body.found.imports = body.found.imports.map((row) => ({ ...row, last_import }));
      return { status: 200, body };
    }
    if (call.path === "/setup/import") {
      const dry = (call.body as { dry_run: boolean }).dry_run;
      if (!dry) imported = true;
      return { status: 200, body: report(dry) };
    }
    return quiet(call);
  });
  await press(fake.root, "Bring it across");
  await press(fake.root, "Bring it across now");
  // The report stays, with its Close, though core's record has arrived.
  expect(reads(fake.calls, "/setup")).toBe(2);
  expect(fake.text()).toContain("Done. This is what was brought across:");
  expect(fake.text()).not.toContain("and was brought across");

  await press(fake.root, "Close");
  expect(fake.text()).not.toContain("Done. This is what was brought across:");
  expect(fake.text()).toContain("and was brought across");
  expect(fake.text()).toContain("Of its settings and auth files: 1 brought across, 1 not brought across.");
  expect(all(fake.root, "button").map((node) => node.textContent)).not.toContain("Bring it across");
  expect(imports(fake.calls)).toHaveLength(2);
});

test("after a real import the page reads setup once more, without probing for a changed key", async () => {
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: withImport() };
    if (call.path === "/setup/import") {
      return { status: 200, body: report((call.body as { dry_run: boolean }).dry_run) };
    }
    return quiet(call);
  });
  await press(fake.root, "Bring it across");
  await press(fake.root, "Bring it across now");
  expect(fake.text()).toContain("Done. This is what was brought across:");
  // One read to open the page and one by the page after the import. An import keeps pluxy's
  // management key, so there is no key to probe for and no sign-in to send the person to.
  expect(reads(fake.calls, "/setup")).toBe(2);
  expect(fake.signedOut.times).toBe(0);
});

test("a refused import says why, drops the report, and can be tried again", async () => {
  let status = 422;
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: withImport() };
    if (call.path === "/setup/import") {
      if ((call.body as { dry_run: boolean }).dry_run) return { status: 200, body: report(true) };
      return {
        status,
        error: { code: "refused", message: "tls: a certificate cannot change while core runs" },
      };
    }
    return quiet(call);
  });
  await press(fake.root, "Bring it across");
  await press(fake.root, "Bring it across now");
  expect(fake.text()).toContain(
    "Cannot import this setup: tls: a certificate cannot change while core runs.",
  );
  expect(fake.text()).not.toContain("Comes across (1)");
  expect(fake.text()).not.toContain("Done.");
  expect(tickedBoxes(fake.root)).toBe(0);
  expect(reads(fake.calls, "/setup")).toBe(1);

  // The next dry run clears the refusal.
  await press(fake.root, "Bring it across");
  expect(fake.text()).not.toContain("Cannot import this setup");
  expect(fake.text()).toContain("Comes across (1)");

  // Core refusing the key is not an import's refusal: the shell goes back to signing in.
  status = 401;
  await press(fake.root, "Bring it across now");
  expect(fake.signedOut.times).toBe(1);
});

test("the accounts are read again when the window gets focus, and not after the page is left", async () => {
  let accounts = 1;
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") {
      return { status: 200, body: setup(false, "2026-11-20T00:00:00Z", true) };
    }
    if (call.method === "GET" && call.path === "/accounts") {
      return {
        status: 200,
        body: { accounts: Array.from({ length: accounts }, (_, i) => ({ id: `a${i}` })) },
      };
    }
    return quiet(call);
  });
  const accountReads = () => reads(fake.calls, "/accounts");
  expect(fake.text()).toContain("1 account connected.");
  expect(accountReads()).toBe(1);

  // A sign-in finished in another window: core has one more account.
  accounts = 2;
  await act(async () => fakeWindow().dispatch("focus"));
  await settle();
  expect(accountReads()).toBe(2);
  expect(fake.text()).toContain("2 accounts connected.");

  await fake.unmount();
  expect(fakeWindow().count("focus")).toBe(0);
});

test("Copy says Copied for two seconds after the last press, and leaves no timer when it goes", async () => {
  jest.useFakeTimers();
  const copies: string[] = [];
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    value: { clipboard: { writeText: async (text: string) => void copies.push(text) } },
  });
  const document = installDocument();
  const root = document.createElement("main");
  document.body.appendChild(root);
  function Copy() {
    const { state, copy } = useCopy("the text");
    return (
      <button type="button" onClick={copy}>
        {state === "copied" ? "Copied" : "Copy"}
      </button>
    );
  }
  await act(async () => render(<Copy />, root));
  const pressCopy = async () => {
    at(all(root, "button"), 0).dispatchEvent(new Event("click"));
    await settle();
  };

  await pressCopy();
  expect(copies).toEqual(["the text"]);
  expect(root.textContent).toBe("Copied");
  // A second press within the two seconds copies again and starts the wait again.
  await tick(1500);
  await pressCopy();
  expect(copies).toEqual(["the text", "the text"]);
  await tick(1500);
  expect(root.textContent).toBe("Copied");
  await tick(500);
  expect(root.textContent).toBe("Copy");
  expect(jest.getTimerCount()).toBe(0);

  // Gone while it says Copied: its timer goes with it.
  await pressCopy();
  expect(jest.getTimerCount()).toBe(1);
  await act(async () => render(null, root));
  expect(jest.getTimerCount()).toBe(0);
});
