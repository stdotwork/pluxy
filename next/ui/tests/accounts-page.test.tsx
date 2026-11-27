// The All accounts page through Preact's DOM renderer against a fake host: its loads (before the
// first reply, a failed first read, a failed reload, no accounts), its groups and counts, Find an
// account, Select's bar, and its 10-second refresh.
import { afterEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { ACCOUNTS_VIEW_KEY, AccountsPage } from "../kit/pages/accounts";
import { AuthImport } from "../kit/pages/authImport";
import { forgetWindows } from "../kit/parts";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  forgetWindows();
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});

const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});
const reply = (...items: Account[]): Reply<unknown> => ({ status: 200, body: { accounts: items } });
const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/**
 * Each GET /accounts takes the next reply (the last one is kept); "pending" never arrives. more
 * gives GET /status and GET /usage their replies in turn the same way (else a reply that works).
 */
async function mount(
  replies: (Reply<unknown> | "pending")[],
  more: {
    status?: Reply<unknown>[];
    usage?: Reply<unknown>[];
    listLayout?: "table" | "cards";
    onSetLayout?: (option: string, value: string | undefined) => void;
  } = {},
) {
  const next = (list: Reply<unknown>[] | undefined) => (list && list.length > 1 ? list.shift() : list?.[0]);
  const calls: Call[] = [];
  let reads = 0;
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      calls.push({ method, path, body });
      if (method === "GET" && path === "/status")
        return (
          next(more.status) ?? {
            status: 200,
            body: { extensions: [{ id: "pluxy/claude" }, { id: "pluxy/gemini" }] },
          }
        );
      if (method === "GET" && path.startsWith("/usage?"))
        return next(more.usage) ?? { status: 200, body: { total_requests: 0, accounts: [], models: [] } };
      if (method === "GET" && path === "/accounts") {
        reads++;
        const next = replies.length > 1 ? replies.shift() : replies[0];
        if (!next) throw new Error("no accounts reply");
        return next === "pending" ? new Promise<Reply<unknown>>(() => {}) : next;
      }
      if (method === "DELETE") return { status: 204 };
      return { status: 200, body: {} };
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  let currentList: "table" | "cards" = more.listLayout ?? "table";
  const listeners = new Set<() => void>();
  const themes = {
    get resolved() {
      return { layout: { tabs: "top", list: currentList } };
    },
    subscribe(fn: () => void) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    setLayout(option: string, value: string | undefined) {
      more.onSetLayout?.(option, value);
      if (option === "list" && (value === "table" || value === "cards")) {
        currentList = value;
        for (const fn of listeners) fn();
      }
    },
  };
  const kit = {
    host,
    api: api(host),
    themes,
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const page = await mountIn(
    <KitContext.Provider value={kit}>
      <AccountsPage />
    </KitContext.Provider>,
  );
  const changes = () => calls.filter((c) => c.method !== "GET").map((c) => `${c.method} ${c.path}`);
  return { ...page, calls, changes, reads: () => reads };
}

async function click(node: FakeNode) {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
}

async function type(node: FakeNode, value: string) {
  node.value = value;
  await act(async () => {
    node.dispatchEvent(new Event("input"));
  });
}

async function advance(ms: number) {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
  await settle();
}

const search = (root: FakeNode) =>
  all(root, "input").find((n) => n.attributes.some((a) => a.value === "search"));
const box = (root: FakeNode, name: string) => {
  const found = all(root, "input").find((n) =>
    n.attributes.some((a) => a.name === "aria-label" && a.value === `Select ${name}`),
  );
  if (!found) throw new Error(`no checkbox for ${name}`);
  return found;
};
/** The names on the rows, in the order the page shows them. */
const rowNames = (root: FakeNode) =>
  all(root, "a")
    .filter((n) => n.attributes.some((a) => a.name === "class" && a.value === "account-name"))
    .map((n) => n.textContent);
const headings = (root: FakeNode) => all(root, "h2").map((n) => n.textContent);

test("while the first read is still running, the page says it is loading and counts nothing", async () => {
  const page = await mount(["pending"]);
  expect(page.text()).toContain("Loading accounts…");
  expect(page.text()).not.toContain("0 accounts");
  expect(page.text()).not.toContain("No accounts yet");
});

test("when the first read fails, the page says so with core's error under Details, and no empty state", async () => {
  const page = await mount([hiccup]);
  expect(page.text()).toContain("Could not read the accounts.");
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).toContain("Try again");
  expect(page.text()).not.toContain("Loading");
  expect(page.text()).not.toContain("No accounts yet");
});

test("a failed reload keeps the rows under the stale notice", async () => {
  jest.useFakeTimers();
  const page = await mount([reply(account("work-01"), account("work-02")), hiccup]);
  expect(rowNames(page.root)).toEqual(["work-01", "work-02"]);
  await advance(10_000);
  expect(page.text()).toContain("Could not refresh.");
  expect(rowNames(page.root)).toEqual(["work-01", "work-02"]);
});

test("when core has no accounts, the page says what an account is and offers to add one", async () => {
  const page = await mount([reply()]);
  expect(page.text()).toContain("No accounts yet.");
  expect(page.text()).toContain(
    "An account is a subscription or an API key that pluxy sends your requests through.",
  );
  expect(
    all(page.root, "a").some((a) =>
      a.attributes.some((x) => x.name === "href" && x.value === "#/add-account"),
    ),
  ).toBe(true);
});

test("the groups come in order with their rows, and the counts leave out zeros", async () => {
  const soon = new Date(Date.now() + 5 * 60_000).toISOString();
  const page = await mount([
    reply(
      account("zeta"),
      account("alpha"),
      account("signed-out", { state: "reauth_required" }),
      account("cooling", { cooldowns: [{ scope: "account", kind: "rate_limit", until: soon }] }),
    ),
  ]);
  expect(headings(page.root)).toEqual(["Needs you 1", "Waiting 1", "Serving 2"]);
  expect(rowNames(page.root)).toEqual(["signed-out", "cooling", "alpha", "zeta"]);
  expect(page.text()).toContain("4 accounts at 1 provider · 1 needs you · 2 serving · 1 waiting");
  expect(page.text()).not.toContain("turned off");
  // Two or more accounts offer Select; six or fewer have no search.
  expect(button(page.root, "Select")).toBeDefined();
  expect(search(page.root)).toBeUndefined();
});

test("Find an account matches a public ID and a provider's name, and says when nothing matches", async () => {
  const page = await mount([
    reply(
      ...["a1", "a2", "a3", "a4", "a5"].map((id) => account(id)),
      account("feed", { public_id: "acc_feedbeef" }),
      account("gem", { extension: "pluxy/gemini" }),
    ),
  ]);
  const input = search(page.root);
  if (!input) throw new Error("no search box above 6 accounts");
  await type(input, "acc_feed");
  expect(rowNames(page.root)).toEqual(["feed"]);
  await type(input, "Gemini");
  expect(rowNames(page.root)).toEqual(["gem"]);
  await type(input, "nothing-like-it");
  expect(page.text()).toContain('No account matches "nothing-like-it".');
  await click(button(page.root, "Clear search"));
  expect(rowNames(page.root)).toHaveLength(7);
});

test("Select's Turn off and Remove… skip accounts set in the settings file and say so", async () => {
  const page = await mount([
    reply(account("mine"), account("from-file", { source: "config" }), account("other")),
  ]);
  await click(button(page.root, "Select"));
  await act(async () => {
    for (const name of ["mine", "from-file"]) {
      const b = box(page.root, name);
      b.checked = true;
      b.dispatchEvent(new Event("change"));
    }
  });
  expect(page.text()).toContain("2 selected");
  await click(button(page.root, "Turn off"));
  expect(page.changes()).toEqual(["PATCH /accounts/mine"]);
  expect(page.calls.find((c) => c.method === "PATCH")?.body).toEqual({ disabled: true });
  expect(page.text()).toContain("Skipped 1 account set in pluxy's settings file.");

  await click(button(page.root, "Remove…"));
  expect(page.text()).toContain("Remove 2 accounts?");
  expect(page.text()).toContain("pluxy deletes its saved sign-in. To use it again, add it again.");
  // Nothing is removed before the answer.
  expect(page.changes()).toEqual(["PATCH /accounts/mine"]);
  await click(button(page.root, "Remove"));
  expect(page.changes()).toEqual(["PATCH /accounts/mine", "DELETE /accounts/mine"]);

  // Turn on reaches an account set in the settings file; the removed one is no longer selected.
  await click(button(page.root, "Turn on"));
  expect(page.changes().slice(2)).toEqual(["PATCH /accounts/from-file"]);
});

test("the list is read every 10 seconds, but not while Select is on", async () => {
  jest.useFakeTimers();
  const page = await mount([reply(account("a"), account("b"))]);
  expect(page.reads()).toBe(1);
  await advance(10_000);
  expect(page.reads()).toBe(2);
  await click(button(page.root, "Select"));
  await advance(30_000);
  expect(page.reads()).toBe(2);
  await click(button(page.root, "Done"));
  await advance(10_000);
  expect(page.reads()).toBe(3);
});

test("once the test's document is gone, nothing the page started still runs", async () => {
  jest.useFakeTimers();
  await mount([reply(account("a"), account("b"))]);
  // The page's timers tick against the document; a page left mounted throws here.
  restoreDom();
  expect(() => jest.advanceTimersByTime(120_000)).not.toThrow();
});

// ---- a round that did not all work ------------------------------------------------------------

test("when the status cannot be read, no account reads Serving: they are Not checked, and the page says why", async () => {
  const page = await mount([reply(account("a"), account("b", { disabled: true }))], { status: [hiccup] });
  expect(headings(page.root)).toEqual(["Not checked 1", "Turned off 1"]);
  expect(page.text()).not.toContain("Serving");
  expect(page.text()).toContain("1 not checked");
  expect(page.text()).toContain("Could not check which providers are running.");
  expect(all(page.root, "button").filter((b) => b.textContent === "Try again")).toHaveLength(1);
  // The row's mark is named for what is known.
  const marks = all(page.root, "span").filter((n) =>
    n.attributes.some((x) => x.name === "data-group" && x.value === "unchecked"),
  );
  expect(marks.length).toBeGreaterThan(0);
});

test("a status read that fails over one read before keeps the groups, under the stale notice", async () => {
  jest.useFakeTimers();
  const ok = { status: 200, body: { extensions: [{ id: "pluxy/claude" }] } };
  const page = await mount([reply(account("a"))], { status: [ok, hiccup] });
  expect(headings(page.root)).toEqual(["Serving 1"]);
  expect(page.text()).not.toContain("Could not refresh");
  await advance(10_000);
  expect(headings(page.root)).toEqual(["Serving 1"]);
  expect(page.text()).toContain("Could not refresh. Showing what pluxy said at");
});

test("a week that was read gives every wide row its count, 0 too; one not read gives none, and says so", async () => {
  const week = {
    status: 200,
    body: { total_requests: 3, accounts: [{ name: "a", total_requests: 3 }], models: [] },
  };
  const read = await mount([reply(account("a"), account("b"))], { usage: [week] });
  expect(read.text()).toContain("3 this week");
  expect(read.text()).toContain("0 this week");
  await read.unmount();
  const failed = await mount([reply(account("a"), account("b"))], { usage: [hiccup] });
  expect(failed.text()).not.toMatch(/\d this week/);
  expect(failed.text()).toContain("Could not read this week's requests.");
});

test("an account pluxy cannot read names no provider on its row", async () => {
  const page = await mount([reply(account("x", { extension: undefined, health: { state: "broken" } }))]);
  expect(page.text()).toContain("x");
  expect(page.text()).not.toContain("No provider");
});

test("selected accounts the search hides are counted in the bar and the question, and Remove removes what they say", async () => {
  // Find an account shows above 6 accounts.
  const others = ["c1", "c2", "c3", "c4", "c5"].map((id) => account(id));
  const page = await mount([reply(account("alpha"), account("beta"), ...others)]);
  await click(button(page.root, "Select"));
  await act(async () => {
    box(page.root, "alpha").checked = true;
    box(page.root, "alpha").dispatchEvent(new Event("change"));
    box(page.root, "beta").checked = true;
    box(page.root, "beta").dispatchEvent(new Event("change"));
  });
  const find = search(page.root);
  if (!find) throw new Error("no Find an account");
  await type(find, "alpha");
  expect(rowNames(page.root)).toEqual(["alpha"]);
  expect(page.text()).toContain("2 selected, 1 hidden");
  await click(button(page.root, "Remove…"));
  expect(page.text()).toContain("Remove 2 accounts?");
  expect(page.text()).toContain("The search hides 1 of them.");
  await click(button(page.root, "Remove"));
  expect(page.changes().sort()).toEqual(["DELETE /accounts/alpha", "DELETE /accounts/beta"]);
});

test("provider chips appear for two or more providers, filter rows, show that provider's pool card, and report filter-hidden selections", async () => {
  const page = await mount([
    reply(
      account("claude-a", { extension: "pluxy/claude" }),
      account("claude-b", { extension: "pluxy/claude" }),
      account("gemini-a", { extension: "pluxy/gemini" }),
    ),
  ]);
  const chip = (label: string) => {
    const found = all(page.root, "button").find((b) => b.textContent.includes(label));
    if (!found) throw new Error(`no chip matching ${label}`);
    return found;
  };
  expect(chip("All").textContent).toContain("3");
  expect(chip("Claude").textContent).toContain("2");
  expect(chip("Gemini").textContent).toContain("1");
  expect(
    all(page.root, "section").some((s) =>
      s.attributes.some((a) => a.name === "data-part" && a.value === "pool-card"),
    ),
  ).toBe(false);

  await click(chip("Claude"));
  expect(rowNames(page.root)).toEqual(["claude-a", "claude-b"]);
  expect(
    all(page.root, "section").some((s) =>
      s.attributes.some((a) => a.name === "data-part" && a.value === "pool-card"),
    ),
  ).toBe(true);

  // Selecting an account and switching provider reports it as hidden by the filter.
  await click(button(page.root, "Select"));
  await act(async () => {
    box(page.root, "claude-a").checked = true;
    box(page.root, "claude-a").dispatchEvent(new Event("change"));
  });
  await click(chip("Gemini"));
  expect(rowNames(page.root)).toEqual(["gemini-a"]);
  expect(page.text()).toContain("1 selected, 1 hidden");
  await click(button(page.root, "Remove…"));
  expect(page.text()).toContain("The filter hides it.");
  await click(button(page.root, "Cancel"));
  await click(chip("All"));
  expect(rowNames(page.root)).toEqual(["claude-a", "claude-b", "gemini-a"]);
});

test("selecting a provider chip scopes the summary counts line and state bar to that provider", async () => {
  const page = await mount([
    reply(
      account("claude-bad", { extension: "pluxy/claude", state: "reauth_required" }),
      account("claude-ok", { extension: "pluxy/claude" }),
      account("gemini-ok", { extension: "pluxy/gemini" }),
    ),
  ]);
  const chip = (label: string) => {
    const found = all(page.root, "button").find((b) => b.textContent.includes(label));
    if (!found) throw new Error(`no chip matching ${label}`);
    return found;
  };
  const barGroups = () => {
    const bar = all(page.root, "div").find((d) =>
      d.attributes.some((a) => a.name === "class" && a.value === "accounts-state-bar"),
    );
    return (bar ? all(bar, "span") : []).map((s) => s.attributes.find((a) => a.name === "data-group")?.value);
  };
  const countsText = () =>
    all(page.root, "p").find((p) =>
      p.attributes.some((a) => a.name === "class" && a.value.includes("accounts-counts")),
    )?.textContent;

  expect(countsText()).toBe("3 accounts at 2 providers · 1 needs you · 2 serving");
  expect(barGroups()).toEqual(["serving", "needs"]);

  await click(chip("Claude"));
  expect(countsText()).toBe("2 Claude accounts · 1 needs you · 1 serving");
  expect(barGroups()).toEqual(["serving", "needs"]);

  await click(chip("Gemini"));
  expect(countsText()).toBe("1 Gemini account · 1 serving");
  expect(barGroups()).toEqual(["serving"]);

  await click(chip("All"));
  expect(countsText()).toBe("3 accounts at 2 providers · 1 needs you · 2 serving");
  expect(barGroups()).toEqual(["serving", "needs"]);
});

test("List | Cards starts from the theme's list layout, stores an Accounts-only choice, and never calls setLayout", async () => {
  const store = new Map<string, string>();
  const fakeStorage = {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
  };
  const originalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: fakeStorage });
  try {
    const calls: [string, string | undefined][] = [];
    const page = await mount([reply(account("a"), account("b"))], {
      listLayout: "cards",
      onSetLayout: (opt, val) => calls.push([opt, val]),
    });
    const container = () =>
      all(page.root, "div").find((d) => d.attributes.some((a) => a.name === "data-view"));
    expect(container()?.attributes.find((a) => a.name === "data-view")?.value).toBe("cards");

    // Clicking the already-pressed button neither writes storage nor calls setLayout.
    await click(button(page.root, "Cards"));
    expect(store.has(ACCOUNTS_VIEW_KEY)).toBe(false);
    expect(calls).toEqual([]);

    await click(button(page.root, "List"));
    expect(calls).toEqual([]);
    expect(store.get(ACCOUNTS_VIEW_KEY)).toBe("list");
    expect(container()?.attributes.find((a) => a.name === "data-view")?.value).toBe("list");
    await page.unmount();

    // A fresh mount restores the Accounts-only stored choice over the theme's list layout.
    const reloaded = await mount([reply(account("a"), account("b"))], {
      listLayout: "cards",
      onSetLayout: (opt, val) => calls.push([opt, val]),
    });
    const reloadedContainer = all(reloaded.root, "div").find((d) =>
      d.attributes.some((a) => a.name === "data-view"),
    );
    expect(reloadedContainer?.attributes.find((a) => a.name === "data-view")?.value).toBe("list");
    await click(button(reloaded.root, "Cards"));
    expect(store.get(ACCOUNTS_VIEW_KEY)).toBe("cards");
    expect(calls).toEqual([]);
    await reloaded.unmount();

    // When browser storage throws, the switch falls back to the layout option and still works in memory.
    const throwingStorage = {
      getItem: () => {
        throw new Error("storage blocked");
      },
      setItem: () => {
        throw new Error("storage blocked");
      },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: throwingStorage });
    const blocked = await mount([reply(account("a"), account("b"))], {
      listLayout: "cards",
      onSetLayout: (opt, val) => calls.push([opt, val]),
    });
    const blockedContainer = () =>
      all(blocked.root, "div").find((d) => d.attributes.some((a) => a.name === "data-view"));
    expect(blockedContainer()?.attributes.find((a) => a.name === "data-view")?.value).toBe("cards");
    await click(button(blocked.root, "List"));
    expect(blockedContainer()?.attributes.find((a) => a.name === "data-view")?.value).toBe("list");
    expect(calls).toEqual([]);
  } finally {
    if (originalStorage) Object.defineProperty(globalThis, "localStorage", originalStorage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});

test("the view keeps at most one filled primary button: the first repair when one exists, else Add an account", async () => {
  const primaries = (root: FakeNode) =>
    [...all(root, "a"), ...all(root, "button")].filter((el) =>
      el.attributes.some((a) => a.name === "data-variant" && a.value === "primary"),
    );

  const broken = await mount([
    reply(
      account("first-bad", { state: "reauth_required" }),
      account("second-bad", { state: "reauth_required" }),
      account("healthy"),
    ),
  ]);
  expect(primaries(broken.root).map((el) => el.textContent.trim())).toEqual(["Sign in again"]);
  await broken.unmount();

  const healthy = await mount([reply(account("ok-1"), account("ok-2"))]);
  expect(primaries(healthy.root).map((el) => el.textContent.trim())).toEqual(["Add an account"]);
});

test("a failed reload marks the page stale while keeping the rows", async () => {
  jest.useFakeTimers();
  const page = await mount([reply(account("work-01")), hiccup]);
  const rootDiv = () => all(page.root, "div").find((d) => d.attributes.some((a) => a.name === "data-view"));
  expect(rootDiv()?.attributes.some((a) => a.name === "data-stale")).toBe(false);
  await advance(10_000);
  expect(rootDiv()?.attributes.some((a) => a.name === "data-stale")).toBe(true);
  expect(rowNames(page.root)).toEqual(["work-01"]);
});

// ---- Import files… (the end of Add an account) -------------------------------------------------

test("privacy mode masks Import files…' lines: the file's name, core's reasons and a caught error", async () => {
  // Core makes an account's ID from its file's name, and says it back in a reason.
  const replies: (Reply<unknown> | Error)[] = [
    { status: 409, error: { code: "conflict", message: "account claude-alice-smith-example-com exists" } },
    { status: 200, body: { imported: [], skipped: ["claude-alice-smith-example-com: no token"] } },
    new Error("reading claude-alice.smith@example.com.json failed"),
  ];
  const host = {
    call: async (): Promise<Reply<unknown>> => {
      const next = replies.shift();
      if (next instanceof Error) throw next;
      return next as Reply<unknown>;
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
  } as unknown as Host;
  const kit = { host, api: api(host), unauthorized: () => {} } as unknown as Kit;
  const page = await mountIn(
    <KitContext.Provider value={kit}>
      <PrivacyContext.Provider value={privacyMasks(true)}>
        <AuthImport />
      </PrivacyContext.Provider>
    </KitContext.Provider>,
  );
  const input = all(page.root, "input").find((n) => n.attributes.some((a) => a.value === "file"));
  if (!input) throw new Error("no file input");
  const file = (name: string) => new File(["{}"], name, { type: "application/json" });
  const big = new File([new Uint8Array((1 << 20) + 1)], "claude-alice.smith@example.com-big.json");
  Object.defineProperty(input, "files", {
    configurable: true,
    value: [
      big,
      file("claude-alice.smith@example.com.json"),
      file("claude-alice.smith@example.com.json"),
      file("claude-alice.smith@example.com.json"),
    ],
  });
  await act(async () => {
    input.dispatchEvent(new Event("change"));
  });
  await settle();
  const lines = all(page.root, "li").map((n) => n.textContent);
  expect(lines).toHaveLength(4);
  for (const line of lines) expect(line).not.toMatch(/alice/);
  expect(lines[0]).toContain("is larger than 1 MB.");
  expect(lines[1]).toContain("Not added: account");
});
