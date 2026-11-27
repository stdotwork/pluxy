// One account's page (kit/pages/account.tsx) through Preact's DOM renderer against a fake host:
// one Sign in again, a test request to the chosen model, what an account from the settings file
// cannot change, and Rename's limit.
import { afterEach, expect, jest, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api, type QuotaLimit } from "../kit/api";
import type { Host, Method, Reply } from "../kit/host";
import { AccountPage, accountAddress } from "../kit/pages/account";
import { balanceLabel, isBalanceLimit, limitTitle, paceOf, resetLabel } from "../kit/pages/account.helpers";
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
    /** An answer to any call, ahead of the defaults. */
    reply?: (call: Call) => Reply<unknown> | undefined;
  } = {},
) {
  Object.defineProperty(globalThis, "location", { configurable: true, value: { hash } });
  const calls: Call[] = [];
  const models = more.models ?? ["m1", "m2"];
  const host = {
    call: async (method: Method, path: string, body?: unknown): Promise<Reply<unknown>> => {
      calls.push({ method, path, body });
      const given = more.reply?.({ method, path, body });
      if (given) return given;
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

/** Lets the effects of a render that no act wrapped (a read that came in) run: Preact runs them after the next paint. */
async function painted() {
  await new Promise((resolve) => setTimeout(resolve, 80));
  await settle();
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

/** Core's answer to starting a sign-in again on acc-1: pending, with a prompt. */
const pendingSignIn = (c: { method: string; path: string }) =>
  c.method === "POST" && c.path === "/accounts/acc-1/login"
    ? {
        status: 200,
        body: {
          login: {
            id: "login-1",
            account: "acc-1",
            extension: "pluxy/claude",
            method: "browser",
            state: "pending",
            prompt: { kind: "browser", url: "https://claude.example/sign-in" },
          },
        },
      }
    : c.method === "DELETE"
      ? { status: 204 }
      : undefined;

test("on its sign-in address the page shows the sign-in in place of the opening's link", async () => {
  const page = await open(account({ state: "reauth_required" }), "#/accounts/acc-1/sign-in", {
    reply: pendingSignIn,
  });
  // The link that led here is gone and the sign-in has no second `Sign in again` button: its
  // heading is all that is left of the words, and its one way to sign in has started and waits.
  await painted();
  expect(controls(page.root, "Sign in again")).toHaveLength(0);
  expect(all(page.root, "h2").filter((h) => h.textContent === "Sign in again")).toHaveLength(1);
  expect(page.text()).toContain("Waiting for sign-in…");
  expect(controls(page.root, "Open sign-in page")).toHaveLength(1);
  expect(page.calls.filter((c) => c.method === "POST" && c.path === "/accounts/acc-1/login")).toHaveLength(1);
});

test("with privacy on, a sign-in again that signed in to another account hides this account's ID and name in Details", async () => {
  const core =
    'the login signed in to a different account (its email differs from account "acc-1"\'s, alice@example.com); to use the other account, add it as a new one';
  const page = await open(
    account({ state: "reauth_required", label: "alice@example.com" }),
    "#/accounts/acc-1/sign-in",
    {
      privacy: true,
      reply: (c) =>
        c.method === "POST" && c.path === "/accounts/acc-1/login"
          ? {
              status: 200,
              body: {
                login: {
                  id: "login-1",
                  account: "acc-1",
                  extension: "pluxy/claude",
                  method: "browser",
                  state: "failed",
                  error: { code: "invalid_request", message: core },
                },
              },
            }
          : undefined,
    },
  );
  await painted();
  expect(page.text()).toContain("That was a different Claude account");
  expect(page.text()).toContain("the login signed in to a different account");
  expect(page.text()).not.toContain("acc-1");
  expect(page.text()).not.toContain("alice");
});

test("Cancel on a sign-in with one way to sign in stops it and leaves the sign-in for the account's page", async () => {
  const page = await open(account({ state: "reauth_required" }), "#/accounts/acc-1/sign-in", {
    reply: pendingSignIn,
  });
  await painted();
  await click(button(page.root, "Cancel"));
  expect(page.changes().map((c) => `${c.method} ${c.path}`)).toContain("DELETE /logins/login-1");
  expect((globalThis as unknown as { location: { hash: string } }).location.hash).toBe("#/accounts/acc-1");
  // It does not start again behind the person's back.
  expect(page.calls.filter((c) => c.method === "POST" && c.path === "/accounts/acc-1/login")).toHaveLength(1);
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

test("saving the Edit settings dialog closes it and says Saved., as Rename does", async () => {
  const page = await open(account({ settings: { api_key: "pluxy-redacted-1" } }));
  await click(button(page.root, "Edit settings"));
  const field = all(page.root, "label")
    .find((l) => l.textContent.startsWith("Priority"))
    ?.childNodes.flatMap((c) => all(c, "input"))[0];
  if (!field) throw new Error("no priority field in the Edit settings dialog");
  field.value = "5";
  await act(async () => {
    field.dispatchEvent(new Event("input"));
  });
  const form = all(page.root, "form")[0];
  if (!form) throw new Error("no Edit settings form");
  await act(async () => {
    form.dispatchEvent(new Event("submit", { cancelable: true }));
  });
  await settle();
  expect(page.changes().map((c) => `${c.method} ${c.path}`)).toContain("PATCH /accounts/acc-1");
  expect(all(page.root, "form")).toHaveLength(0);
  expect(page.text()).toContain("Saved.");
});

// ---- limits: absolute resets, pace and money balances -------------------------------------------

test("resetLabel formats weekday and clock with relative countdown, date beyond a week, and passed resets", () => {
  const now = new Date(2026, 10, 27, 12, 0, 0).getTime(); // Fri 27 Nov 2026 12:00 local
  const soon = resetLabel(
    { id: "5h", resets_at: Math.floor((now + (1 * 3600 + 52 * 60) * 1000) / 1000) },
    now,
  );
  expect(soon?.text).toBe("Resets Fri 13:52 · in 1h 52m");
  expect(soon?.at).toBeInstanceOf(Date);

  const later = resetLabel({ id: "mo", resets_at: Math.floor((now + 14 * 86_400_000) / 1000) }, now);
  expect(later?.text).toBe("Resets 11 Dec 12:00 · in 14d");

  const passed = resetLabel({ id: "5h", resets_at: Math.floor((now - 60_000) / 1000) }, now);
  expect(passed).toEqual({ text: "Reset available", passed: true });

  expect(resetLabel({ id: "5h" }, now)).toBeUndefined();
});

test("paceOf places the even-burn tick and shows a sentence only when pace runs out before reset", () => {
  const now = new Date(2026, 10, 27, 15, 0, 0).getTime();
  const fiveH = 5 * 3600;
  // 2h remaining of 5h -> evenLeft = 40%; actual 15% left -> 85% used in 3h -> runs out in ~32m (~1h 28m before reset).
  const fast = paceOf(
    { id: "5h", window_seconds: fiveH, resets_at: Math.floor((now + 2 * 3600_000) / 1000), used_percent: 85 },
    now,
  );
  expect(fast?.kind).toBe("fast");
  expect(fast?.showTick).toBe(true);
  expect(fast?.evenLeft).toBeCloseTo(40, 1);
  expect(fast?.text).toMatch(/^At this pace it runs out at 15:3[12], 1h 2[89]m before it resets\.$/);

  // 2h remaining of 5h -> evenLeft = 40%; actual 75% left -> lasts until reset: tick stays, no sentence.
  const ok = paceOf(
    { id: "5h", window_seconds: fiveH, resets_at: Math.floor((now + 2 * 3600_000) / 1000), used_percent: 25 },
    now,
  );
  expect(ok?.kind).toBe("ok");
  expect(ok?.showTick).toBe(true);
  expect(ok?.text).toBeUndefined();

  // 100% left or just started: no pace sentence.
  const unused = paceOf(
    { id: "5h", window_seconds: fiveH, resets_at: Math.floor((now + 2 * 3600_000) / 1000), used_percent: 0 },
    now,
  );
  expect(unused?.kind).toBe("even");
  expect(unused?.text).toBeUndefined();

  const justStarted = paceOf(
    {
      id: "5h",
      window_seconds: fiveH,
      resets_at: Math.floor((now + fiveH * 1000 - 60_000) / 1000),
      used_percent: 10,
    },
    now,
  );
  expect(justStarted?.kind).toBe("even");
  expect(justStarted?.text).toBeUndefined();

  // Used up or unlimited: no pace.
  expect(
    paceOf(
      {
        id: "5h",
        window_seconds: fiveH,
        resets_at: Math.floor((now + 2 * 3600_000) / 1000),
        used_percent: 100,
      },
      now,
    ),
  ).toBeUndefined();
  expect(
    paceOf(
      {
        id: "5h",
        window_seconds: fiveH,
        resets_at: Math.floor((now + 2 * 3600_000) / 1000),
        unlimited: true,
      },
      now,
    ),
  ).toBeUndefined();
});

test("limitTitle keeps scoped labels and isBalanceLimit formats USD credits without a bar", () => {
  expect(limitTitle({ id: "5h", window_seconds: 5 * 3600 })).toBe("5-hour limit");
  expect(
    limitTitle({
      id: "sonnet",
      window_seconds: 7 * 86400,
      models: ["claude-sonnet"],
      label: "Weekly · Sonnet",
    }),
  ).toBe("Weekly · Sonnet");
  const credit: QuotaLimit = { id: "credits", label: "Monthly credits", remaining: 42.1, unit: "usd" };
  expect(isBalanceLimit(credit)).toBe(true);
  expect(balanceLabel(credit)).toEqual({ title: "Monthly credits", value: "$42.10 left" });
});

test("the page renders the header mark, plan pill, full-width limits with pace and resets, and balance", async () => {
  const now = Date.now();
  const fiveH = 5 * 3600;
  const quota = {
    fetched_at: new Date(now).toISOString(),
    quota: {
      plan: "Max 20x",
      limits: [
        {
          id: "5h",
          window_seconds: fiveH,
          resets_at: Math.floor((now + 2 * 3600_000) / 1000),
          used_percent: 82,
        },
        {
          id: "wk",
          window_seconds: 7 * 86400,
          resets_at: Math.floor((now + 3 * 86_400_000) / 1000),
          used_percent: 20,
        },
        { id: "credits", label: "API credit", remaining: 42.1, unit: "usd" },
      ],
    },
  };
  const page = await open(account({ label: "studio-max", quota }));
  // Hero mark and top-tier plan pill.
  expect(
    all(page.root, "span").some((n) =>
      n.attributes.some((a) => a.name === "data-part" && a.value === "provider-mark"),
    ),
  ).toBe(true);
  const pill = all(page.root, "span").find((n) =>
    n.attributes.some((a) => a.name === "data-part" && a.value === "plan-pill"),
  );
  expect(pill?.textContent).toBe("Max 20x");
  expect(pill?.attributes.find((a) => a.name === "data-plan-tier")?.value).toBe("top");
  // Limits with reset times, pace ticks, pace lines, and USD balance without a bar.
  expect(page.text()).toContain("18% left");
  expect(page.text()).toContain("80% left");
  expect(page.text()).toContain("Resets ");
  expect(page.text()).toContain("At this pace it runs out");
  expect(page.text()).not.toContain("At this pace it lasts until reset.");
  expect(page.text()).toContain("$42.10 left");
  const paceLines = all(page.root, "p").filter((n) =>
    n.attributes.some((a) => a.name === "class" && a.value === "account-limit-pace"),
  );
  expect(paceLines).toHaveLength(1);
  const ticks = all(page.root, "span").filter((n) =>
    n.attributes.some((a) => a.name === "class" && a.value === "account-pace-tick"),
  );
  expect(ticks).toHaveLength(2);
});

test("when a provider reports no limits, the limits card names the provider and shows no meter", async () => {
  const page = await open(account({ extension: "pluxy/xai" }), undefined, {
    status: [{ status: 200, body: { extensions: [{ id: "pluxy/xai" }] } }],
    reply: (c) => (c.method === "GET" && c.path.endsWith("/quota") ? { status: 422 } : undefined),
  });
  await painted();
  expect(page.text()).toContain("Not reported by xAI");
  expect(
    all(page.root, "div").filter((n) => n.attributes.some((a) => a.name === "role" && a.value === "meter")),
  ).toHaveLength(0);
});

test("a stale refresh sets data-stale on the account page while keeping the last data visible", async () => {
  jest.useFakeTimers();
  const none = { status: 200, body: { total_requests: 0, success_count: 0, models: [] } };
  const page = await open(account(), undefined, { usage: [none, hiccup] });
  const article = all(page.root, "article")[0];
  expect(article?.attributes.some((a) => a.name === "data-stale")).toBe(false);
  await act(async () => {
    jest.advanceTimersByTime(10_000);
  });
  await settle();
  const after = all(page.root, "article")[0];
  expect(after?.attributes.some((a) => a.name === "data-stale")).toBe(true);
});
