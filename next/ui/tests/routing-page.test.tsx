// Order & fallback through Preact's DOM renderer against a fake host: its loads (nothing read, nothing
// readable, one section failed), the rules and the values they send, and the fallbacks' list, its
// settings-file notice and what its editor checks before it asks core.
import { afterEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { CoreSettings } from "../kit/api/settings";
import type { Host, Method, Reply } from "../kit/host";
import { RoutingPage } from "../kit/pages/routing";
import { forgetWindows } from "../kit/parts";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  forgetWindows();
});

const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});

const settings = (over: Partial<CoreSettings["routing"]> = {}): CoreSettings => ({
  routing: {
    strategy: "round-robin",
    "session-affinity": false,
    "session-affinity-ttl": "1h",
    "force-model-prefix": false,
    retry: { "request-retry": null, "max-retry-credentials": 0, "max-retry-interval": 30 },
    cooldown: {
      "disable-cooling": false,
      "transient-error-cooldown-seconds": 0,
      "quota-response-mode": "retryable",
    },
    ...over,
  },
  observability: {
    logs: {
      "logging-to-file": false,
      "logs-max-total-size-mb": 0,
      "request-log": false,
      "error-logs-max-files": 10,
    },
    usage: { "usage-statistics-enabled": true },
  },
  token_count_estimate: false,
  "proxy-url": "",
  management_allow_remote: false,
  listen: "127.0.0.1:8317",
  from_this_computer: true,
});

type Answer = Reply<unknown> | "pending";
const ok = (body: unknown): Reply<unknown> => ({ status: 200, body });
const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

async function mount(answers: Record<string, Answer> = {}) {
  const calls: Call[] = [];
  const base: Record<string, Answer> = {
    "GET /accounts": ok({
      accounts: [account("a1"), account("a2"), account("g1", { extension: "pluxy/gemini" })],
    }),
    "GET /settings": ok(settings()),
    "GET /chains": ok({ chains: [] }),
    "GET /models": ok({ models: [] }),
    "GET /config": ok({ format: "json", hash: "x", content: "{}" }),
    "GET /status": ok({
      extensions: [{ id: "pluxy/claude" }, { id: "pluxy/gemini" }, { id: "pluxy/codex" }],
    }),
  };
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      calls.push({ method, path, body });
      const answer = answers[`${method} ${path}`] ?? base[`${method} ${path}`];
      if (answer === "pending") return new Promise<Reply<unknown>>(() => {});
      return answer ?? ok({});
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const page = await mountIn(
    <KitContext.Provider value={kit}>
      <RoutingPage />
    </KitContext.Provider>,
  );
  const changes = () => calls.filter((c) => c.method !== "GET").map((c) => ({ ...c }));
  return { ...page, calls, changes };
}

const attr = (n: FakeNode, name: string) => n.attributes.find((a) => a.name === name)?.value;
const byLabel = (root: FakeNode, tag: string, label: string) => {
  const found = all(root, tag).find((n) => attr(n, "aria-label") === label);
  if (!found) throw new Error(`no ${tag} labelled ${label}`);
  return found;
};
const text = (root: FakeNode, tag: string) => all(root, tag).map((n) => n.textContent);

/** The switch of a rule, found by the row's name. */
const toggle = (root: FakeNode, name: string) => {
  const label = all(root, "label").find((l) => l.textContent.startsWith(name));
  const box = label && all(label, "input")[0];
  if (!box) throw new Error(`no switch ${name}`);
  return box;
};
async function flip(box: FakeNode) {
  box.checked = !box.checked;
  await act(async () => {
    box.dispatchEvent(new Event("change"));
  });
  await settle();
}
async function enter(input: FakeNode, value: string) {
  input.value = value;
  await act(async () => {
    input.dispatchEvent(new Event("input"));
  });
  await act(async () => {
    input.dispatchEvent(Object.assign(new Event("keydown"), { key: "Enter" }));
  });
  await settle();
}

test("before anything is read the page says it is loading, once, and shows no empty state", async () => {
  const page = await mount({
    "GET /accounts": "pending",
    "GET /settings": "pending",
    "GET /chains": "pending",
  });
  expect(page.text().match(/Loading the order…/g)).toHaveLength(1);
  expect(page.text()).not.toContain("No accounts yet");
  expect(page.text()).not.toContain("No fallbacks yet");
});

test("when nothing can be read the page says so once, with core's error under Details and Try again", async () => {
  const page = await mount({ "GET /accounts": hiccup, "GET /settings": hiccup, "GET /chains": hiccup });
  expect(page.text()).toContain("Could not read the order.");
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).toContain("Try again");
  expect(page.text()).not.toContain("No accounts yet");
  expect(page.text()).not.toContain("Fallbacks");
});

test("one section that cannot be read says so beside the others that work", async () => {
  const page = await mount({ "GET /chains": hiccup, "GET /settings": hiccup });
  expect(page.text()).toContain("Could not read the fallbacks.");
  expect(page.text()).toContain("Could not read the rules.");
  // The order still shows: Claude has two accounts.
  expect(text(page.root, "h3")).toContain("Claude");
  expect(page.text()).not.toContain("No fallbacks yet");
});

test("with no accounts the order says to add one first, and links to the page that does", async () => {
  const page = await mount({ "GET /accounts": ok({ accounts: [] }) });
  expect(page.text()).toContain("No accounts yet.");
  expect(page.text()).toContain("Add an account first, then choose here which one pluxy uses first.");
  const add = all(page.root, "a").find((a) => a.textContent === "+ Add an account");
  expect(add && attr(add, "href")).toBe("#/add-account");
});

test("when every provider has one account there is nothing to order, and it says so", async () => {
  const page = await mount({
    "GET /accounts": ok({ accounts: [account("a"), account("g", { extension: "pluxy/gemini" })] }),
  });
  expect(page.text()).toContain("Every provider has one account, so there is nothing to order yet.");
  expect(page.text()).not.toContain("One account each");
});

test("providers with one account are named in one line under the order of the others", async () => {
  const page = await mount();
  expect(page.text()).toContain("One account each, so nothing to order: Gemini.");
  expect(text(page.root, "h4")).toEqual(["First"]);
  expect(page.text()).toContain("Priority 0");
});

test("More rules is closed, and holds the eight rules the first two leave out", async () => {
  const page = await mount();
  const more = all(page.root, "details").find((d) => text(d, "summary")[0] === "More rules");
  expect(more).toBeDefined();
  expect(more && attr(more, "open")).toBeUndefined();
  for (const name of [
    "Release a quiet conversation after",
    "Extra rounds when every account fails",
    "Accounts to try per round",
    "Longest wait for a free account (seconds)",
    "Rest an account after it fails",
    "Rest after a provider error (seconds)",
    "Show tools when a limit resets",
    "Prefixed accounts answer only prefixed names",
  ])
    expect(more?.textContent).toContain(name);
  expect(page.text()).toContain("How a group shares requests");
  expect(page.text()).toContain("Keep a conversation on one account");
});

test("a core on Soonest reset first shows that choice and its sentence", async () => {
  const page = await mount({ "GET /settings": ok(settings({ strategy: "soonest-reset" })) });
  expect(page.text()).toContain("Soonest reset first");
  expect(page.text()).toContain("Spends the limit that resets soonest first, so less of it goes unused.");
});

test("Rest an account after it fails sends the inverse of disable-cooling", async () => {
  const page = await mount();
  const box = toggle(page.root, "Rest an account after it fails");
  // Cooling is not disabled, so resting reads on; turning it off disables cooling.
  expect(box.checked).toBe(true);
  await flip(box);
  expect(page.changes()[0]).toEqual({
    method: "PATCH",
    path: "/settings",
    body: { routing: { cooldown: { "disable-cooling": true } } },
  });
});

test("Show tools when a limit resets is the usage-limit quota response", async () => {
  const page = await mount();
  const box = toggle(page.root, "Show tools when a limit resets");
  expect(box.checked).toBe(false);
  await flip(box);
  expect(page.changes()[0]?.body).toEqual({
    routing: { cooldown: { "quota-response-mode": "usage-limit" } },
  });
});

test("a core that already shows the limit message reads the switch on, and off sends retryable", async () => {
  const on = settings();
  on.routing.cooldown["quota-response-mode"] = "usage-limit";
  const page = await mount({ "GET /settings": ok(on) });
  const box = toggle(page.root, "Show tools when a limit resets");
  expect(box.checked).toBe(true);
  await flip(box);
  expect(page.changes()[0]?.body).toEqual({ routing: { cooldown: { "quota-response-mode": "retryable" } } });
});

test("Extra rounds shows Automatic when empty, and emptying it sends null", async () => {
  const set = settings();
  set.routing.retry["request-retry"] = 3;
  const page = await mount({ "GET /settings": ok(set) });
  const input = byLabel(page.root, "input", "Extra rounds when every account fails");
  expect(attr(input, "placeholder")).toBe("Automatic");
  await enter(input, "");
  expect(page.changes()[0]?.body).toEqual({ routing: { retry: { "request-retry": null } } });
});

test("a number below its lowest value is refused before anything is sent, and the saved value comes back", async () => {
  const page = await mount();
  const input = byLabel(page.root, "input", "Accounts to try per round");
  await enter(input, "-3");
  expect(page.changes()).toEqual([]);
  expect(page.text()).toContain("Enter 0 or more.");
});

test("the release time is off, with the reason, until a conversation is kept on one account", async () => {
  const page = await mount();
  expect(page.text()).toContain("Turn on Keep a conversation on one account first.");
  const on = settings({ "session-affinity": true });
  const again = await mount({ "GET /settings": ok(on) });
  expect(again.text()).not.toContain("Turn on Keep a conversation on one account first.");
});

test("a rule that core refuses goes back to the saved value with core's message", async () => {
  const page = await mount({
    "PATCH /settings": { status: 400, error: { code: "bad", message: "no such strategy" } },
  });
  const box = toggle(page.root, "Keep a conversation on one account");
  await flip(box);
  expect(page.text()).toContain("no such strategy");
  expect(box.checked).toBe(false);
});

test("with a config.yaml settings file there is a notice and no way to add a fallback", async () => {
  const page = await mount({ "GET /config": ok({ format: "yaml", hash: "x", content: "" }) });
  expect(page.text()).toContain(
    "pluxy's settings file is CLIProxyAPI's config.yaml, which cannot hold fallbacks.",
  );
  expect(all(page.root, "button").some((b) => b.textContent === "+ Add a fallback")).toBe(false);
});

test("with no fallbacks the list says what happens without one", async () => {
  const page = await mount();
  expect(page.text()).toContain(
    "No fallbacks yet. When every account for a model is out, its requests wait or fail until one is back.",
  );
});

test("fallbacks are lines of the model asked for and its steps in order", async () => {
  const page = await mount({
    "GET /chains": ok({
      chains: [
        {
          name: "claude-opus-4-7",
          steps: [
            { model: "claude-opus-4-7" },
            { model: "gpt-5.5", provider: "pluxy/codex" },
            { model: "g", provider: "pluxy/gemini", accounts: ["g1"] },
          ],
        },
      ],
    }),
  });
  expect(page.text()).toContain("claude-opus-4-7 → gpt-5.5 (Codex) → g (Gemini, only g1)");
});

test("Save checks the editor and asks core nothing while something is wrong", async () => {
  const page = await mount();
  await act(async () => {
    button(page.root, "+ Add a fallback").dispatchEvent(new Event("click"));
  });
  await act(async () => {
    all(page.root, "form")[0]?.dispatchEvent(new Event("submit"));
  });
  await settle();
  expect(page.text()).toContain("Enter the model a tool asks for.");
  expect(page.changes()).toEqual([]);
});

test("a fallback core refuses keeps the editor open with core's message", async () => {
  const page = await mount({
    "PUT /chains/m": { status: 422, error: { code: "refused", message: "no account serves step 2" } },
  });
  await act(async () => {
    button(page.root, "+ Add a fallback").dispatchEvent(new Event("click"));
  });
  const [name] = all(page.root, "form")[0] ? all(all(page.root, "form")[0] as FakeNode, "input") : [];
  if (!name) throw new Error("no editor");
  name.value = "m";
  await act(async () => {
    name.dispatchEvent(new Event("input"));
  });
  const step2 = byLabel(page.root, "input", "Model, step 2");
  step2.value = "other";
  await act(async () => {
    step2.dispatchEvent(new Event("input"));
  });
  await act(async () => {
    all(page.root, "form")[0]?.dispatchEvent(new Event("submit"));
  });
  await settle();
  expect(page.changes()).toEqual([
    {
      method: "PUT",
      path: "/chains/m",
      body: { steps: [{ model: "m" }, { model: "other" }] },
    },
  ]);
  expect(page.text()).toContain("pluxy could not save this fallback: no account serves step 2");
  expect(all(page.root, "form")).toHaveLength(1);
});

test("account rows use the shared AccountRow with live limits, plan pill and at most one filled fix", async () => {
  const page = await mount({
    "GET /accounts": ok({
      accounts: [
        account("need-1", { priority: 20, state: "reauth_required" }),
        account("need-2", { priority: 10, state: "reauth_required" }),
        account("paused-1", { priority: 5, disabled: true }),
        account("live-1", {
          priority: 0,
          quota: {
            fetched_at: new Date().toISOString(),
            quota: {
              plan: "Max 20x",
              limits: [{ id: "5h", window_seconds: 5 * 3600, used_percent: 42 }],
            },
          },
        }),
      ],
    }),
  });
  const rows = all(page.root, "div").filter((n) => attr(n, "data-part") === "account-row");
  expect(rows).toHaveLength(4);
  expect(page.text()).toContain("Max 20x");
  expect(page.text()).toContain("58% left");
  // Only the first repair button is filled (primary); the second is outlined (quiet).
  const fixes = all(page.root, "a").filter((n) => n.textContent === "Sign in again");
  expect(fixes.map((n) => attr(n, "data-variant"))).toEqual(["primary", "quiet"]);
  // Opening the fallback editor steps back every row fix so Save is the view's one filled button.
  await act(async () => {
    button(page.root, "+ Add a fallback").dispatchEvent(new Event("click"));
  });
  const fixesWithEditor = all(page.root, "a").filter((n) => n.textContent === "Sign in again");
  expect(fixesWithEditor.map((n) => attr(n, "data-variant"))).toEqual(["quiet", "quiet"]);
  await act(async () => {
    button(page.root, "Cancel").dispatchEvent(new Event("click"));
  });
  // Turning on a disabled account sends the PATCH in place.
  await act(async () => {
    button(page.root, "Turn on").dispatchEvent(new Event("click"));
  });
  await settle();
  expect(page.changes()).toContainEqual({
    method: "PATCH",
    path: "/accounts/paused-1",
    body: { disabled: false },
  });
});

test("editing an existing fallback opens the editor inline inside its row and renders step chips", async () => {
  const page = await mount({
    "GET /chains": ok({
      chains: [
        {
          name: "claude-opus-4-7",
          steps: [
            { model: "claude-opus-4-7", provider: "pluxy/claude" },
            { model: "gpt-5.5", provider: "pluxy/codex" },
          ],
        },
      ],
    }),
  });
  const fallback = all(page.root, "div").find((n) => attr(n, "data-part") === "fallback");
  if (!fallback) throw new Error("no fallback row");
  const chips = all(fallback, "span").filter((n) => attr(n, "class") === "fallback-chip");
  expect(chips).toHaveLength(2);
  const marks = all(fallback, "span").filter((n) => attr(n, "data-part") === "provider-mark");
  expect(marks).toHaveLength(2);
  await act(async () => {
    button(fallback, "Edit").dispatchEvent(new Event("click"));
  });
  expect(attr(fallback, "data-open")).toBe("");
  expect(all(fallback, "form").filter((n) => attr(n, "data-part") === "fallback-editor")).toHaveLength(1);
});

test("a failed refresh over loaded data keeps the page under the stale notice", async () => {
  let failAccounts = false;
  const page = await mount();
  expect(page.text()).toContain("Claude");
  // Simulate a subsequent reload failing by clicking Refresh when GET /accounts starts failing.
  const callsBefore = page.calls.length;
  // Mount with a stateful host to test stale reload directly.
  restoreDom();
  forgetWindows();
  const base: Record<string, Answer> = {
    "GET /accounts": ok({ accounts: [account("a1"), account("a2")] }),
    "GET /settings": ok(settings()),
    "GET /chains": ok({ chains: [] }),
    "GET /models": ok({ models: [] }),
    "GET /config": ok({ format: "json", hash: "x", content: "{}" }),
    "GET /status": ok({ extensions: [{ id: "pluxy/claude" }] }),
  };
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      if (method === "GET" && path === "/accounts" && failAccounts) return hiccup;
      const ans = base[`${method} ${path}`];
      return ans && ans !== "pending" ? ans : ok({});
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const live = await mountIn(
    <KitContext.Provider value={kit}>
      <RoutingPage />
    </KitContext.Provider>,
  );
  expect(callsBefore).toBeGreaterThan(0);
  expect(live.text()).toContain("a1");
  failAccounts = true;
  await act(async () => {
    button(live.root, "Refresh").dispatchEvent(new Event("click"));
  });
  await settle();
  expect(live.text().match(/Could not refresh\./g)).toHaveLength(1);
  expect(live.text()).toContain("a1");
});

test("a failed refresh keeps the previous freshness stamp instead of saying Updated just now", async () => {
  const realNow = Date.now;
  let clock = Date.UTC(2026, 10, 27, 22, 27, 0);
  Date.now = () => clock;
  try {
    let failAccounts = false;
    let failAll = false;
    const base: Record<string, Answer> = {
      "GET /accounts": ok({ accounts: [account("a1"), account("a2")] }),
      "GET /settings": ok(settings()),
      "GET /chains": ok({ chains: [] }),
      "GET /models": ok({ models: [] }),
      "GET /config": ok({ format: "json", hash: "x", content: "{}" }),
      "GET /status": ok({ extensions: [{ id: "pluxy/claude" }] }),
    };
    const host = {
      call: async (method: Method, path: string): Promise<Reply<unknown>> => {
        if (method === "GET" && failAll) return hiccup;
        if (method === "GET" && path === "/accounts" && failAccounts) return hiccup;
        const ans = base[`${method} ${path}`];
        return ans && ans !== "pending" ? ans : ok({});
      },
      signedIn: () => true,
      prefs: { get: () => null, set: () => {} },
      features: new Set(),
    } as unknown as Host;
    const kit = {
      host,
      api: api(host),
      themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
      unauthorized: () => {},
      extensionsChanged: () => {},
    } as unknown as Kit;
    const live = await mountIn(
      <KitContext.Provider value={kit}>
        <RoutingPage />
      </KitContext.Provider>,
    );
    const fresh = () => all(live.root, "span").find((n) => attr(n, "class") === "routing-fresh");
    expect(fresh()?.textContent).toBe("Updated just now");
    const initialTitle = fresh() ? attr(fresh() as FakeNode, "title") : undefined;

    // 5 minutes later, GET /accounts fails while settings and chains succeed.
    clock += 5 * 60_000;
    failAccounts = true;
    await act(async () => {
      button(live.root, "Refresh").dispatchEvent(new Event("click"));
    });
    await settle();
    expect(fresh()?.textContent).not.toBe("Updated just now");
    expect(fresh()?.textContent).toBe("Updated 5m ago");
    expect(fresh() && attr(fresh() as FakeNode, "title")).toBe(initialTitle);

    // Another 2 minutes later, every read fails: the stamp still stays at the initial read.
    clock += 2 * 60_000;
    failAll = true;
    await act(async () => {
      button(live.root, "Refresh").dispatchEvent(new Event("click"));
    });
    await settle();
    expect(fresh()?.textContent).toBe("Updated 7m ago");
    expect(fresh() && attr(fresh() as FakeNode, "title")).toBe(initialTitle);

    // When a refresh succeeds for every read, the stamp moves forward.
    failAccounts = false;
    failAll = false;
    await act(async () => {
      button(live.root, "Refresh").dispatchEvent(new Event("click"));
    });
    await settle();
    expect(fresh()?.textContent).toBe("Updated just now");
  } finally {
    Date.now = realNow;
  }
});

test("one failed refresh across every read shows one stale notice, keeps an empty fallback list, and Try again reloads everything including limits", async () => {
  let failRefresh = false;
  const calls: string[] = [];
  const base: Record<string, Answer> = {
    "GET /accounts": ok({ accounts: [account("a1"), account("a2")] }),
    "GET /accounts/a1/quota": ok({
      quota: { plan: "Pro", limits: [{ id: "5h", window_seconds: 5 * 3600, used_percent: 15 }] },
    }),
    "GET /accounts/a2/quota": ok({
      quota: { plan: "Pro", limits: [{ id: "5h", window_seconds: 5 * 3600, used_percent: 15 }] },
    }),
    "GET /settings": ok(settings()),
    "GET /chains": ok({
      chains: [
        {
          name: "claude-opus-4-7",
          steps: [{ model: "claude-opus-4-7" }, { model: "gpt-5.5", provider: "pluxy/codex" }],
        },
      ],
    }),
    "GET /models": ok({ models: [] }),
    "GET /config": ok({ format: "json", hash: "x", content: "{}" }),
    "GET /status": ok({ extensions: [{ id: "pluxy/claude" }] }),
  };
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      calls.push(`${method} ${path}`);
      if (method === "GET" && failRefresh) return hiccup;
      const ans = base[`${method} ${path}`];
      return ans && ans !== "pending" ? ans : ok({});
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const live = await mountIn(
    <KitContext.Provider value={kit}>
      <RoutingPage />
    </KitContext.Provider>,
  );
  expect(live.text()).toContain("a1");
  expect(live.text()).toContain("claude-opus-4-7");
  expect(live.text()).toContain("How a group shares requests");

  failRefresh = true;
  await act(async () => {
    button(live.root, "Refresh").dispatchEvent(new Event("click"));
  });
  await settle();
  expect(live.text().match(/Could not refresh\./g)).toHaveLength(1);
  expect(all(live.root, "button").filter((b) => b.textContent === "Try again")).toHaveLength(1);
  expect(live.text()).toContain("a1");
  expect(live.text()).toContain("claude-opus-4-7");
  expect(live.text()).toContain("How a group shares requests");

  // Clicking Try again reloads every read and expires cached limits.
  failRefresh = false;
  calls.length = 0;
  await act(async () => {
    button(live.root, "Try again").dispatchEvent(new Event("click"));
  });
  await settle();
  for (const path of [
    "GET /accounts",
    "GET /settings",
    "GET /chains",
    "GET /models",
    "GET /config",
    "GET /status",
    "GET /accounts/a1/quota",
  ]) {
    expect(calls).toContain(path);
  }
  expect(live.text()).not.toContain("Could not refresh.");

  // An empty fallback list also stays visible under the single stale notice instead of being
  // replaced by "Could not read the fallbacks.".
  base["GET /chains"] = ok({ chains: [] });
  await act(async () => {
    button(live.root, "Refresh").dispatchEvent(new Event("click"));
  });
  await settle();
  expect(live.text()).toContain("No fallbacks yet.");
  failRefresh = true;
  await act(async () => {
    button(live.root, "Refresh").dispatchEvent(new Event("click"));
  });
  await settle();
  expect(live.text().match(/Could not refresh\./g)).toHaveLength(1);
  expect(live.text()).toContain("No fallbacks yet.");
  expect(live.text()).not.toContain("Could not read the fallbacks.");
});

test("saving a rule shows a Saved tick beside it", async () => {
  const page = await mount();
  const box = toggle(page.root, "Keep a conversation on one account");
  await flip(box);
  expect(page.text()).toContain("Saved");
});
