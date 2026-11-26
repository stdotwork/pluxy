// One account's page (kit/pages/account.tsx) through Preact's DOM renderer against a fake host:
// one Sign in again, a test request to the chosen model, what an account from the settings file
// cannot change, and Rename's limit.
import { afterEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { AccountPage, accountAddress } from "../kit/pages/account";
import { forgetWindows } from "../kit/parts";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const originalLocation = Object.getOwnPropertyDescriptor(globalThis, "location");

afterEach(() => {
  restoreDom();
  forgetWindows();
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
  if (originalLocation) Object.defineProperty(globalThis, "location", originalLocation);
  else Reflect.deleteProperty(globalThis, "location");
});

const account = (more: Partial<Account> = {}): Account => ({
  id: "acc-1",
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  kind: "oauth",
  ...more,
});

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };

/** Replies taken in turn, the last one kept; none: undefined, for the default. */
const turn = (list?: Reply<unknown>[]) => (list && list.length > 1 ? list.shift() : list?.[0]);

async function open(
  a: Account,
  hash = `#/accounts/${a.id}`,
  more: {
    status?: Reply<unknown>[];
    usage?: Reply<unknown>[];
    events?: Reply<unknown>[];
    models?: string[];
    privacy?: boolean;
  } = {},
) {
  Object.defineProperty(globalThis, "location", { configurable: true, value: { hash } });
  const calls: Call[] = [];
  const models = more.models ?? ["m1", "m2"];
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      calls.push({ method, path, body });
      if (method === "GET" && path === "/status")
        return turn(more.status) ?? { status: 200, body: { extensions: [{ id: "pluxy/claude" }] } };
      if (method === "GET" && path.startsWith("/usage?"))
        return turn(more.usage) ?? { status: 200, body: { total_requests: 0, success_count: 0, models: [] } };
      if (method === "GET" && path.startsWith("/usage/events?"))
        return turn(more.events) ?? { status: 200, body: { events: [], more: false } };
      if (method === "GET" && path === "/accounts") return { status: 200, body: { accounts: [a] } };
      if (method === "GET" && path === `/accounts/${a.id}/models`)
        return {
          status: 200,
          body: {
            models: models.map((id) => ({ id, names: null })),
          },
        };
      if (method === "GET" && path === "/login-methods")
        return {
          status: 200,
          body: {
            extensions: [
              { extension: a.extension, methods: [{ method: "browser", kind: "browser", label: "Browser" }] },
            ],
          },
        };
      if (method === "POST" && path === `/accounts/${a.id}/test`)
        return {
          status: 200,
          body: {
            result: { ok: true, model: (body as { model?: string })?.model ?? models[0], latency_ms: 420 },
          },
        };
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
      <PrivacyContext.Provider value={privacyMasks(more.privacy ?? false)}>
        <AccountPage />
      </PrivacyContext.Provider>
    </KitContext.Provider>,
  );
  const changes = () => calls.filter((c) => c.method !== "GET");
  return { ...page, calls, changes };
}

/** The buttons and links that read exactly label. */
const controls = (root: FakeNode, label: string) =>
  [...all(root, "button"), ...all(root, "a")].filter((n) => n.textContent === label);

async function click(node: FakeNode) {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
}

test("the address names the account and the part of its page", () => {
  expect(accountAddress("#/accounts/acc_1a2b3c4d")).toEqual({ id: "acc_1a2b3c4d", part: "" });
  expect(accountAddress("#/accounts/a%2Fb/sign-in")).toEqual({ id: "a/b", part: "sign-in" });
  expect(accountAddress("#/accounts/x?y=1")).toEqual({ id: "x", part: "" });
});

test("an account that needs signing in again offers Sign in again once, in its opening", async () => {
  const page = await open(account({ state: "reauth_required", refreshable: true }));
  expect(page.text()).toContain("Claude no longer accepts its sign-in.");
  const signIn = controls(page.root, "Sign in again");
  expect(signIn).toHaveLength(1);
  expect(signIn[0]?.attributes.find((a) => a.name === "href")?.value).toBe("#/accounts/acc-1/sign-in");
  // The tools under the disclosure renew; they do not sign in a second way.
  expect(controls(page.root, "Renew sign-in now")).toHaveLength(1);
});

test("on its sign-in address the page shows the sign-in in place of the opening's link", async () => {
  const page = await open(account({ state: "reauth_required" }), "#/accounts/acc-1/sign-in");
  // The one control left is the sign-in's own start button; the link that led here is gone.
  const signIn = controls(page.root, "Sign in again");
  expect(signIn.map((n) => n.localName)).toEqual(["button"]);
  expect(all(page.root, "h2").some((h) => h.textContent === "Sign in again")).toBe(true);
});

test("Send a test request sends the model chosen in Test with", async () => {
  const page = await open(account());
  const select = all(page.root, "select")[0];
  if (!select) throw new Error("no Test with select");
  expect(page.text()).toContain("Uses one request.");
  select.value = "m2";
  await act(async () => {
    select.dispatchEvent(new Event("change"));
  });
  await click(button(page.root, "Send a test request"));
  const sent = page.changes().find((c) => c.path === "/accounts/acc-1/test");
  expect(sent?.body).toEqual({ model: "m2" });
  expect(page.text()).toContain("Succeeded");
  expect(page.text()).toContain("420 ms");
});

test("an account set in the settings file offers no Rename, Turn off or Remove…, and says where it is changed", async () => {
  const page = await open(account({ source: "config", kind: "api_key" }));
  for (const label of ["Rename", "Turn off", "Remove…", "Edit settings"])
    expect(controls(page.root, label)).toHaveLength(0);
  expect(page.text()).toContain("Set in pluxy's settings file.");
  expect(
    controls(page.root, "Open the settings file")[0]?.attributes.find((a) => a.name === "href")?.value,
  ).toBe("#/config/source?account=acc-1");
});

test("Rename refuses a name over 64 characters before sending, and sends one of 64 as the label", async () => {
  const page = await open(account({ label: "old" }));
  await click(button(page.root, "Rename"));
  const input = all(page.root, "input").find((n) => n.value === "old");
  const form = all(page.root, "form")[0];
  if (!input || !form) throw new Error("no rename form");
  const submit = async (value: string) => {
    input.value = value;
    await act(async () => {
      input.dispatchEvent(new Event("input"));
    });
    await act(async () => {
      form.dispatchEvent(new Event("submit"));
    });
    await settle();
  };
  await submit("x".repeat(65));
  expect(page.text()).toContain("Use at most 64 characters.");
  expect(page.changes()).toEqual([]);
  await submit(` ${"y".repeat(64)} `);
  expect(page.changes().map((c) => [c.method, c.path, c.body])).toEqual([
    ["PATCH", "/accounts/acc-1", { label: "y".repeat(64) }],
  ]);
});

test("an account pluxy cannot read says what to do, and offers only Remove…", async () => {
  const page = await open(account({ health: { state: "broken", why: "bad file x.json" } }));
  expect(page.text()).toContain("Remove it and add it again.");
  expect(page.text()).toContain("bad file x.json");
  expect(controls(page.root, "Remove…")).toHaveLength(1);
  expect(controls(page.root, "Rename")).toHaveLength(0);
  expect(controls(page.root, "See what to do")).toHaveLength(0);
});

test("an account not in pluxy says so and leads back to the list", async () => {
  const page = await open(account(), "#/accounts/gone");
  expect(page.text()).toContain("This account is not in pluxy.");
  expect(controls(page.root, "Back to accounts")).toHaveLength(1);
});

// ---- reads that did not work --------------------------------------------------------------------

test("when the status cannot be read, the opening does not say Serving and the page says why", async () => {
  const page = await open(account(), undefined, { status: [hiccup] });
  expect(page.text()).not.toContain("Serving.");
  expect(page.text()).toContain("pluxy could not check whether Claude is running.");
  expect(page.text()).toContain("Could not check which providers are running.");
  expect(controls(page.root, "Try again")).toHaveLength(1);
});

test("a week and latest requests never read each say so, with Try again; core's error is not the text", async () => {
  const page = await open(account(), undefined, { usage: [hiccup], events: [hiccup] });
  expect(page.text()).toContain("Could not read this week's requests.");
  expect(page.text()).toContain("Could not read the latest requests.");
  expect(page.text()).not.toContain("No requests in the last 7 days.");
  expect(page.text()).not.toContain("core hiccup");
  expect(controls(page.root, "Try again")).toHaveLength(2);
});

test("a week with no requests whose refresh fails is shown under the stale notice", async () => {
  jest.useFakeTimers();
  const none = { status: 200, body: { total_requests: 0, success_count: 0, models: [] } };
  const page = await open(account(), undefined, { usage: [none, hiccup] });
  expect(page.text()).toContain("No requests in the last 7 days.");
  expect(page.text()).not.toContain("Could not refresh");
  await act(async () => {
    jest.advanceTimersByTime(10_000);
  });
  await settle();
  expect(page.text()).toContain("No requests in the last 7 days.");
  expect(page.text()).toContain("Could not refresh. Showing what pluxy said at");
});

test("privacy mode masks the owner's address in model names: the week's bars, the latest requests, the models it serves, Test with and the result", async () => {
  const model = "ft:alice.smith@example.com:custom";
  const event = {
    seq: 1,
    id: "t1",
    timestamp: "2026-11-27T10:00:00Z",
    model,
    account: "acc-1",
    latency_ms: 300,
    failed: false,
  };
  const page = await open(account({ label: "alice.smith@example.com" }), undefined, {
    privacy: true,
    models: [model],
    usage: [
      {
        status: 200,
        body: { total_requests: 1, success_count: 1, models: [{ name: model, total_requests: 1 }] },
      },
    ],
    events: [{ status: 200, body: { events: [event], more: false } }],
  });
  await click(button(page.root, "Send a test request"));
  expect(page.text()).toContain("Succeeded");
  const titles = all(page.root, "span").flatMap((n) => n.attributes.filter((x) => x.name === "title"));
  const shown = `${page.text()}\n${titles.map((x) => x.value).join("\n")}`;
  expect(shown).not.toContain("alice.smith");
  expect(shown).toContain("ft:al***@e***.com:custom");
  // The choice sent is the real model.
  const select = all(page.root, "select")[0];
  expect(all(select as FakeNode, "option").map((o) => o.value)).toContain(model);
});
