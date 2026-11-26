// The All accounts page through Preact's DOM renderer against a fake host: its loads (before the
// first reply, a failed first read, a failed reload, no accounts), its groups and counts, Find an
// account, Select's bar, and its 10-second refresh.
import { afterEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { AccountsPage } from "../kit/pages/accounts";
import { forgetWindows } from "../kit/parts";
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

/** Each GET /accounts takes the next reply (the last one is kept); "pending" never arrives. */
async function mount(replies: (Reply<unknown> | "pending")[]) {
  const calls: Call[] = [];
  let reads = 0;
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      calls.push({ method, path, body });
      if (method === "GET" && path === "/status")
        return { status: 200, body: { extensions: [{ id: "pluxy/claude" }, { id: "pluxy/gemini" }] } };
      if (method === "GET" && path.startsWith("/usage?"))
        return { status: 200, body: { total_requests: 0, accounts: [], models: [] } };
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
  const kit = {
    host,
    api: api(host),
    themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
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
