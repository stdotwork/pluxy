// The Accounts page across its loads: before the first reply, after a failed first read, after a
// failed reload, and when core really has no accounts. It drives the real page through Preact's
// DOM renderer against a fake host.
import { afterEach, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { AccountsPage } from "../kit/pages/accounts";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(restoreDom);

const account = (id: string): Account => ({ id, source: "login", state: "active", refreshable: false });
const accounts = (...ids: string[]): Reply<unknown> => ({
  status: 200,
  body: { accounts: ids.map(account) },
});
const status: Reply<unknown> = { status: 200, body: { extensions: [] } };
const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

/** Each GET /accounts takes the next reply; "pending" never arrives. */
async function mount(replies: (Reply<unknown> | "pending")[]) {
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      if (method === "GET" && path === "/status") return status;
      if (method === "GET" && path === "/accounts") {
        const next = replies.shift();
        if (!next) throw new Error("no accounts reply left");
        return next === "pending" ? new Promise<Reply<unknown>>(() => {}) : next;
      }
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
  return {
    ...page,
    refresh: async () => {
      await act(async () => {
        button(page.root, "Refresh").dispatchEvent(new Event("click"));
      });
      await settle();
    },
  };
}

test("while the first read is still running, the page says it is loading and counts nothing", async () => {
  const page = await mount(["pending"]);
  expect(page.text()).toContain("Loading");
  expect(page.text()).not.toContain("0 credentials");
  expect(page.text()).not.toContain("No results");
  expect(page.text()).not.toContain("No accounts yet");
});

test("when the first read fails, the page shows the error and no empty state", async () => {
  const page = await mount([hiccup]);
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).not.toContain("Loading");
  expect(page.text()).not.toContain("0 credentials");
  expect(page.text()).not.toContain("No results");
  expect(page.text()).not.toContain("No accounts yet");
});

test("one bad reload keeps the credentials on screen, with the error above them", async () => {
  const page = await mount([accounts("work-01", "work-02"), hiccup]);
  expect(page.text()).toContain("2 credentials");
  expect(page.text()).toContain("work-01");
  await page.refresh();
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).toContain("2 credentials");
  expect(page.text()).toContain("work-01");
  expect(page.text()).toContain("work-02");
  expect(page.text()).not.toContain("0 credentials");
  expect(page.text()).not.toContain("No results");
});

test("when core has no accounts, the page counts zero and offers the empty state", async () => {
  const page = await mount([accounts()]);
  expect(page.text()).toContain("0 credentials");
  expect(page.text()).toContain("No accounts yet");
  expect(page.text()).not.toContain("Loading");
});

test("a search that matches nothing says no results, and the credentials are still counted", async () => {
  const page = await mount([accounts("work-01")]);
  const search = all(page.root, "input").find((node) =>
    node.attributes.some((a) => a.name === "placeholder"),
  );
  if (!search) throw new Error("no search box");
  search.value = "zzz";
  await act(async () => {
    search.dispatchEvent(new Event("input"));
  });
  expect(page.text()).toContain("No results");
  expect(page.text()).toContain("1 credential");
});
