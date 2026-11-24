// The Accounts page across its loads: before the first reply, after a failed first read, after a
// failed reload, and when core really has no accounts. It drives the real page through Preact's
// DOM renderer against a fake host.
import { afterEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { AccountsPage } from "../kit/pages/accounts";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
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
  ...more,
});
const accounts = (...ids: string[]): Reply<unknown> => ({
  status: 200,
  body: { accounts: ids.map((id) => account(id)) },
});
const accountsReply = (...items: Account[]): Reply<unknown> => ({
  status: 200,
  body: { accounts: items },
});
const status: Reply<unknown> = { status: 200, body: { extensions: [] } };
const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

/** Each GET /accounts takes the next reply; "pending" never arrives. */
async function mount(
  replies: (Reply<unknown> | "pending")[],
  other?: (method: Method, path: string) => Promise<Reply<unknown>> | Reply<unknown>,
) {
  let accountReads = 0;
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      if (method === "GET" && path === "/status") return status;
      if (method === "GET" && path === "/accounts") {
        accountReads++;
        const next = replies.shift();
        if (!next) throw new Error("no accounts reply left");
        return next === "pending" ? new Promise<Reply<unknown>>(() => {}) : next;
      }
      if (other) return other(method, path);
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
    reads: () => accountReads,
    refresh: async () => {
      await act(async () => {
        button(page.root, "Refresh").dispatchEvent(new Event("click"));
      });
      await settle();
    },
  };
}

async function advance(ms: number) {
  await act(async () => {
    jest.advanceTimersByTime(ms);
  });
  await settle();
}

test("while the first read is still running, the page says it is loading and counts nothing", async () => {
  const page = await mount(["pending"]);
  expect(page.text()).toContain("Loading");
  expect(page.text()).not.toContain("0 credentials");
  expect(page.text()).not.toContain("0 accounts");
  expect(page.text()).not.toContain("No matching accounts");
  expect(page.text()).not.toContain("No accounts yet");
});

test("when the first read fails, the page shows the error and no empty state", async () => {
  const page = await mount([hiccup]);
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).not.toContain("Loading");
  expect(page.text()).not.toContain("0 credentials");
  expect(page.text()).not.toContain("0 accounts");
  expect(page.text()).not.toContain("No matching accounts");
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
  expect(page.text()).not.toContain("No matching accounts");
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
  expect(page.text()).toContain("No matching accounts");
  expect(page.text()).toContain("1 credential");
});

test("a changed Requests now count appears after a poll tick without any action", async () => {
  jest.useFakeTimers();
  const page = await mount([
    accountsReply(account("work-01", { active_requests: 0 })),
    accountsReply(account("work-01", { active_requests: 7 })),
  ]);
  const lastCell = () => {
    const cells = all(page.root, "td");
    return cells[cells.length - 1]?.textContent;
  };
  expect(lastCell()).toBe("0");
  await advance(5_000);
  expect(lastCell()).toBe("7");
  expect(page.text()).not.toContain("Loading");
});

test("a poll tick keeps the selection and the typed search text", async () => {
  jest.useFakeTimers();
  const page = await mount([
    accountsReply(account("work-01", { active_requests: 1 }), account("work-02", { active_requests: 0 })),
    accountsReply(account("work-01", { active_requests: 4 }), account("work-02", { active_requests: 0 })),
  ]);
  const checkbox = all(page.root, "input").find((node) =>
    node.attributes.some((a) => a.name === "type" && a.value === "checkbox"),
  );
  if (!checkbox) throw new Error("no selection checkbox");
  checkbox.checked = true;
  await act(async () => {
    checkbox.dispatchEvent(new Event("change"));
  });
  const search = all(page.root, "input").find((node) =>
    node.attributes.some((a) => a.name === "placeholder"),
  );
  if (!search) throw new Error("no search box");
  search.value = "work-01";
  await act(async () => {
    search.dispatchEvent(new Event("input"));
  });
  expect(page.text()).toContain("1 selected");
  expect(page.text()).toContain("work-01");
  expect(page.text()).not.toContain("work-02");

  await advance(5_000);
  const cells = all(page.root, "td");
  expect(cells[cells.length - 1]?.textContent).toBe("4");
  expect(page.text()).toContain("1 selected");
  expect(search.value).toBe("work-01");
  expect(page.text()).toContain("work-01");
  expect(page.text()).not.toContain("work-02");
});

test("a failed poll tick keeps the rows and shows the error", async () => {
  jest.useFakeTimers();
  const page = await mount([accounts("work-01", "work-02"), hiccup]);
  expect(page.text()).toContain("work-01");
  expect(page.text()).toContain("work-02");
  await advance(5_000);
  expect(page.text()).toContain("core hiccup");
  expect(page.text()).toContain("work-01");
  expect(page.text()).toContain("work-02");
  expect(page.text()).toContain("2 credentials");
});

test("a poll tick is skipped while a batch action is in flight, and resumes after", async () => {
  jest.useFakeTimers();
  let finishAction!: (reply: Reply<unknown>) => void;
  const page = await mount(
    [
      accountsReply(account("work-01", { active_requests: 1 })),
      accountsReply(account("work-01", { active_requests: 2 })),
      accountsReply(account("work-01", { active_requests: 5 })),
    ],
    () =>
      new Promise<Reply<unknown>>((resolve) => {
        finishAction = resolve;
      }),
  );
  const checkbox = all(page.root, "input").find((node) =>
    node.attributes.some((a) => a.name === "type" && a.value === "checkbox"),
  );
  if (!checkbox) throw new Error("no selection checkbox");
  checkbox.checked = true;
  await act(async () => {
    checkbox.dispatchEvent(new Event("change"));
  });
  await act(async () => {
    button(page.root, "Disable").dispatchEvent(new Event("click"));
  });
  expect(page.reads()).toBe(1);

  await advance(10_000);
  expect(page.reads()).toBe(1);

  await act(async () => {
    finishAction({ status: 200, body: {} });
  });
  await settle();
  expect(page.reads()).toBe(2);

  await advance(5_000);
  expect(page.reads()).toBe(3);
  const cells = all(page.root, "td");
  expect(cells[cells.length - 1]?.textContent).toBe("5");
});
