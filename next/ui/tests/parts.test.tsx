// The shared parts (kit/parts): what each says and offers in each state, by the rules of the K3
// spec. Static output is read with renderToString; presses go through Preact's DOM renderer.
import { afterEach, beforeEach, expect, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { act } from "preact/test-utils";
import { renderToString } from "preact-render-to-string";
import type { Account, QuotaLimit, SetupClient, UsageEvent } from "../kit/api";
import type { Reply } from "../kit/host";
import { Meter, meterTone } from "../kit/meter";
import {
  AccountRow,
  accountCondition,
  accountReason,
  forgetWindow,
  forgetWindows,
  Limits,
  Phases,
  PlanPill,
  planTier,
  RequestRow,
  SettingRow,
  StatePane,
  ToolRow,
  useNaming,
  useSetting,
  useWindows,
  type Windows,
} from "../kit/parts";
import { PrivacyContext, privacyMasks } from "../kit/privacy";
import { failureWord, stateText } from "../kit/states";
import { ConfirmButton, type Kit, KitContext, type Loaded, Switch } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(restoreDom);
beforeEach(forgetWindows);

const NOW = Date.now();
const iso = (ms: number) => new Date(ms).toISOString();
const HOUR = 3_600_000;

const account = (fields: Partial<Account> = {}): Account => ({
  id: "acc_1234abcd",
  source: "login",
  state: "active",
  refreshable: true,
  extension: "pluxy/claude",
  ...fields,
});

const noop = () => undefined;
const row = (a: Account, extra: { windows?: Windows; running?: ReadonlySet<string>; wide?: boolean } = {}) =>
  renderToString(<AccountRow account={a} onTurnOn={noop} onClearCooldown={noop} {...extra} />);

/** The buttons and button-links in some HTML: their label, variant and where a link leads. */
function buttons(html: string) {
  return [...html.matchAll(/<(a|button)\b([^>]*)>([^<]*)</g)]
    .filter((m) => (m[2] ?? "").includes('data-part="button"'))
    .map((m) => ({
      label: m[3],
      variant: /data-variant="(\w+)"/.exec(m[2] ?? "")?.[1],
      href: /href="([^"]*)"/.exec(m[2] ?? "")?.[1],
    }));
}

/** Some HTML in privacy mode. */
const hidden = (tree: ComponentChildren) =>
  renderToString(<PrivacyContext.Provider value={privacyMasks(true)}>{tree}</PrivacyContext.Provider>);

/** What some HTML shows a person: its text, and every title and aria-label. Not its links' addresses. */
const shown = (html: string) =>
  [plain(html), ...[...html.matchAll(/(?:title|aria-label)="([^"]*)"/g)].map((m) => m[1])].join("\n");

const attr = (html: string, name: string) => new RegExp(`${name}="([^"]*)"`).exec(html)?.[1];
const plain = (html: string) => html.replace(/<[^>]+>/g, "").replaceAll("&amp;", "&");

// ---- AccountRow: the address an API-key account sends its requests to ---------------------------

test("AccountRow shows an API-key account's address under its provider, and nothing for one with none", () => {
  const keyed = account({
    extension: "pluxy/openai-compatible",
    settings: { base_url: "http://127.0.0.1:18500/v1", api_key: "pluxy-redacted-1" },
  });
  const html = row(keyed);
  expect(plain(html)).toContain("OpenAI Compatible");
  expect(plain(html)).toContain("127.0.0.1:18500");
  // The path and the scheme stay out of a row, and the key is never in it.
  expect(plain(html)).not.toContain("/v1");
  expect(html).not.toContain("pluxy-redacted");
  expect(plain(row(account()))).not.toContain("127.0.0.1");
  expect(plain(row(account({ settings: { base_url: "not an address" } })))).not.toContain("not an address");
});

// ---- AccountRow: conditions, groups and fixes --------------------------------------------------

const later = iso(NOW + 2 * HOUR);
const CASES: {
  name: string;
  a: Account;
  running?: ReadonlySet<string>;
  group: string;
  word: string;
  fix?: { label: string; variant: string; href?: string };
}[] = [
  {
    name: "needs sign-in",
    a: account({ health: { state: "needs_login" } }),
    group: "needs",
    word: "Needs sign-in",
    fix: { label: "Sign in again", variant: "primary", href: "#/accounts/acc_1234abcd/sign-in" },
  },
  {
    name: "needs sign-in, from the settings file",
    a: account({ source: "config", health: { state: "needs_login" } }),
    group: "needs",
    word: "Needs sign-in",
    fix: { label: "See what to do", variant: "primary", href: "#/accounts/acc_1234abcd" },
  },
  {
    name: "closed by provider",
    a: account({ health: { state: "banned" } }),
    group: "needs",
    word: "Closed by provider",
    fix: { label: "Turn on", variant: "quiet" },
  },
  {
    name: "cannot be read",
    a: account({ health: { state: "broken" } }),
    group: "needs",
    word: "Cannot be read",
    fix: { label: "See what to do", variant: "primary", href: "#/accounts/acc_1234abcd" },
  },
  {
    name: "provider stopped",
    a: account(),
    running: new Set(["pluxy/codex"]),
    group: "needs",
    word: "Provider stopped",
    fix: { label: "Open Extensions", variant: "primary", href: "#/extensions" },
  },
  {
    name: "cooling down",
    a: account({ cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] }),
    group: "waiting",
    word: "Cooling down",
    fix: { label: "Clear cooldown", variant: "ghost" },
  },
  {
    name: "limit used up",
    a: account({ health: { state: "exhausted", until: later } }),
    group: "waiting",
    word: "Limit used up",
  },
  {
    name: "a model cooling down",
    a: account({ cooldowns: [{ scope: "model", model: "opus-5", kind: "rate_limit", until: later }] }),
    group: "serving",
    word: "Serving",
    fix: { label: "Clear cooldown", variant: "ghost" },
  },
  { name: "serving", a: account(), group: "serving", word: "Serving" },
  {
    name: "turned off",
    a: account({ disabled: true, health: { state: "banned" } }),
    group: "off",
    word: "Turned off",
    fix: { label: "Turn on", variant: "quiet" },
  },
];

for (const c of CASES) {
  test(`AccountRow, ${c.name}: group ${c.group}, mark "${c.word}", ${c.fix ? c.fix.label : "no button"}`, () => {
    const html = row(c.a, { running: c.running });
    expect(attr(html, "data-group")).toBe(c.group);
    // The mark's colour group matches the condition's own group (a model cooldown stays serving).
    expect(html).toContain(`class="group-mark" data-group="${c.group}"`);
    // The mark's accessible name is the condition word, the same as stateText's.
    expect(html).toContain(`role="img" aria-label="${c.word}"`);
    const found = buttons(html);
    expect(found).toEqual(c.fix ? [{ ...c.fix, href: c.fix.href }] : []);
  });
}

test("AccountRow's run fixes call the page once a press, and Clear cooldown asks nothing", async () => {
  const pressed: string[] = [];
  const press = async (a: Account, label: string) => {
    const m = await mountIn(
      <AccountRow
        account={a}
        onTurnOn={(id) => {
          pressed.push(`on ${id}`);
        }}
        onClearCooldown={(id) => {
          pressed.push(`clear ${id}`);
        }}
      />,
    );
    const b = all(m.root, "button").find((n) => n.textContent === label);
    if (!b) throw new Error(`no ${label}`);
    await act(async () => {
      b.dispatchEvent(new Event("click"));
    });
    await settle();
    // No question, no second press: nothing that says Yes.
    expect(m.text()).not.toContain("Yes,");
    await m.unmount();
  };
  await press(
    account({ id: "cool", cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] }),
    "Clear cooldown",
  );
  await press(
    account({ id: "model", cooldowns: [{ scope: "model", model: "m", kind: "rate_limit", until: later }] }),
    "Clear cooldown",
  );
  await press(account({ id: "closed", health: { state: "banned" } }), "Turn on");
  await press(account({ id: "off", disabled: true }), "Turn on");
  expect(pressed).toEqual(["clear cool", "clear model", "on closed", "on off"]);
});

test("a fix that fails says why under its button, and can be pressed again", async () => {
  let calls = 0;
  const m = await mountIn(
    <AccountRow
      account={account({ disabled: true })}
      onTurnOn={async () => {
        calls++;
        return {
          status: 500,
          error: { code: "internal", message: "could not write alice@example.com" },
        } as Reply<unknown>;
      }}
      onClearCooldown={noop}
    />,
  );
  const b = all(m.root, "button").find((n) => n.textContent === "Turn on");
  await act(async () => {
    b?.dispatchEvent(new Event("click"));
  });
  await settle();
  expect(m.text()).toContain("could not write alice@example.com");
  await act(async () => {
    b?.dispatchEvent(new Event("click"));
  });
  await settle();
  expect(calls).toBe(2);
  await m.unmount();
});

test("the reasons say what happened and when it is back", () => {
  const now = NOW;
  const r = (a: Account) => accountReason(a, accountCondition(a, undefined, now), now);
  expect(r(account({ health: { state: "needs_login" } }))).toBe("Claude no longer accepts its sign-in.");
  expect(r(account({ source: "config", health: { state: "needs_login" } }))).toBe(
    "Claude refused its key. Change it in pluxy's settings file.",
  );
  expect(r(account({ health: { state: "banned" } }))).toBe(
    "Claude closed this account. Check it with Claude, then turn it back on.",
  );
  expect(r(account({ health: { state: "broken" } }))).toBe("pluxy cannot read this account's saved sign-in.");
  const two = iso(now + 2 * HOUR);
  expect(r(account({ cooldowns: [{ scope: "account", kind: "rate_limit", until: two }] }))).toMatch(
    /^Claude rate-limited it\. Back (at|tomorrow at) \d\d:\d\d, in 2h\.$/,
  );
  expect(
    r(
      account({
        cooldowns: [{ scope: "account", kind: "cooldown", until: two, error: { code: "auth", message: "" } }],
      }),
    ),
  ).toMatch(/^Claude refused its sign-in\. Trying again (at|tomorrow at) \d\d:\d\d\.$/);
  expect(r(account({ cooldowns: [{ scope: "account", kind: "cooldown", until: two }] }))).toMatch(
    /^Claude kept failing\. Trying again /,
  );
  expect(r(account({ health: { state: "exhausted", until: two } }))).toMatch(
    /^Its limit is used up\. Back .+, in 2h\.$/,
  );
  expect(
    r(account({ cooldowns: [{ scope: "model", model: "opus-5", kind: "rate_limit", until: two }] })),
  ).toBe("Cooling down on opus-5, back in 2h.");
  expect(
    r(
      account({
        cooldowns: [
          { scope: "model", model: "a", kind: "rate_limit", until: two },
          { scope: "model", model: "b", kind: "rate_limit", until: iso(now + HOUR) },
        ],
      }),
    ),
  ).toBe("Cooling down on 2 models, back in 2h.");
  expect(r(account({ disabled: true }))).toBe("You turned it off.");
  expect(r(account())).toBeUndefined();
});

// ---- names ------------------------------------------------------------------------------------

test("an account's name is its label, else its ID, never shown twice, and masked in privacy mode", () => {
  const labelled = account({ label: "alice@example.com" });
  const open = row(labelled);
  expect(plain(open).split("alice@example.com").length - 1).toBe(1);
  expect(attr(open, "title")).toBe("alice@example.com");
  expect(plain(row(account()))).toContain("acc_1234abcd");

  const hidden = (a: Account) =>
    renderToString(
      <PrivacyContext.Provider value={privacyMasks(true)}>
        <AccountRow account={a} onTurnOn={noop} onClearCooldown={noop} />
      </PrivacyContext.Provider>,
    );
  const masked = hidden(labelled);
  expect(masked).not.toContain("alice@example.com");
  expect(plain(masked)).toContain("al***@e***.com");
  // An ID made from an imported file's name holds the address too.
  const old = hidden(account({ id: "claude-alice@example.com.json" }));
  expect(old).not.toContain("alice@example.com");
});

test("an account that needs the person or is turned off shows no limits, even when some were read, but keeps its plan pill", () => {
  const windows: Windows = {
    state: "read",
    plan: "Max 20x",
    limits: [{ id: "five", window_seconds: 18000, used_percent: 10 }],
  };
  const banned = row(account({ health: { state: "banned" } }), { windows });
  expect(banned).not.toContain('data-part="limits"');
  expect(banned).toContain('data-part="plan-pill"');
  expect(banned).toContain("Max 20x");
  // Also reads a stored plan carried on the account when no windows were passed.
  const stored = row(
    account({
      health: { state: "needs_login" },
      quota: { fetched_at: iso(NOW), quota: { plan: "Pro", limits: [] } },
    }),
  );
  expect(stored).not.toContain('data-part="limits"');
  expect(stored).toContain('data-plan-tier="mid"');
  expect(stored).toContain("Pro");
  expect(row(account({ disabled: true }), { windows })).not.toContain('data-part="limits"');
  expect(row(account(), { windows })).toContain('data-part="limits"');
});

/** Mounts useWindows over accounts with a fake core; calls lists each quota read. */
async function mountWindows(accounts: Account[], running?: ReadonlySet<string>) {
  const calls: string[] = [];
  const kit = {
    api: {
      quota: (id: string) => {
        calls.push(id);
        return Promise.resolve({
          status: 200,
          body: {
            quota: { plan: "Pro", limits: [{ id: "five", window_seconds: 18000, used_percent: 40 }] },
            fetched_at: iso(Date.now()),
          },
        });
      },
    },
    unauthorized: noop,
  } as unknown as Kit;
  let seen = new Map<string, Windows>();
  function Probe() {
    seen = useWindows(accounts, running);
    return <p>{[...seen.keys()].join(",")}</p>;
  }
  const m = await mountIn(
    <KitContext.Provider value={kit}>
      <Probe />
    </KitContext.Provider>,
  );
  return { calls, seen: () => seen, m };
}

test("limits are read only for accounts that serve or wait", async () => {
  const w = await mountWindows(
    [
      account({ id: "serving" }),
      account({ id: "cooling", cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] }),
      account({ id: "used-up", health: { state: "exhausted" } }),
      account({ id: "signin", health: { state: "needs_login" } }),
      account({ id: "closed", health: { state: "banned" } }),
      account({ id: "broken", health: { state: "broken" } }),
      account({ id: "off", disabled: true }),
      account({ id: "stopped", extension: "pluxy/gone" }),
    ],
    new Set(["pluxy/claude"]),
  );
  expect(w.calls.sort()).toEqual(["cooling", "serving", "used-up"]);
  expect(w.seen().get("serving")?.plan).toBe("Pro");
  await w.m.unmount();
});

// ---- plan and gauge ---------------------------------------------------------------------------

test("the plan pill's tier follows the plan word, and there is no pill without a plan", () => {
  // The largest plans (Max 20x, Ultra, an API key's) are top; the next (Max 5x) are high.
  // Claude: Max 20x ($200) gold, Max 5x and Max ($100) silver, Pro ($20) and Team an outline.
  expect(planTier("Max 20x", "pluxy/claude")).toBe("top");
  expect(planTier("Max 5x", "pluxy/claude")).toBe("high");
  expect(planTier("Max", "pluxy/claude")).toBe("high");
  expect(planTier("Pro", "pluxy/claude")).toBe("mid");
  expect(planTier("Team", "pluxy/claude")).toBe("mid");
  expect(planTier("Free", "pluxy/claude")).toBe("free");
  // ChatGPT (Codex): pro is its $200 plan, gold; plus and the business plans an outline.
  expect(planTier("pro", "pluxy/codex")).toBe("top");
  expect(planTier("plus", "pluxy/codex")).toBe("mid");
  expect(planTier("team", "pluxy/codex")).toBe("mid");
  expect(planTier("enterprise", "pluxy/codex")).toBe("mid");
  expect(planTier("free", "pluxy/codex")).toBe("free");
  // Gemini: Ultra gold, Pro an outline. xAI: SuperGrok Heavy gold, SuperGrok an outline.
  expect(planTier("Ultra", "pluxy/gemini")).toBe("top");
  expect(planTier("Pro", "pluxy/gemini")).toBe("mid");
  expect(planTier("SuperGrok Heavy", "pluxy/xai")).toBe("top");
  expect(planTier("SuperGrok", "pluxy/xai")).toBe("mid");
  // An OpenAI-compatible key: paid (pay-as-you-go API use) is with the largest plans.
  expect(planTier("paid", "pluxy/openai-compatible")).toBe("top");
  expect(planTier("free", "pluxy/openai-compatible")).toBe("free");
  // No provider, or one with no table: the words of the largest plans anywhere.
  expect(planTier("Max 20x")).toBe("top");
  expect(planTier("Max 5x")).toBe("high");
  expect(planTier("Ultra")).toBe("top");
  expect(planTier("API")).toBe("top");
  expect(planTier("Pro")).toBe("mid");
  expect(planTier("Plus", "acme/other")).toBe("mid");
  expect(planTier("Free")).toBe("free");
  expect(renderToString(<PlanPill plan=" Max 20x " />)).toBe(
    '<span class="plan-pill" data-part="plan-pill" data-plan-tier="top">Max 20x</span>',
  );
  const serving = account();
  const limits: QuotaLimit[] = [{ id: "five", window_seconds: 18000, used_percent: 10 }];
  expect(row(serving, { windows: { state: "read", limits, plan: "Team" } })).toContain(
    'data-plan-tier="mid"',
  );
  expect(row(serving, { windows: { state: "read", limits } })).not.toContain("plan-pill");
});

const limitsOf = (limits: QuotaLimit[], now = NOW) =>
  renderToString(<Limits windows={{ state: "read", limits }} account="alpha" now={now} />);

test("the gauge shows what is left: ok from 70%, warn under 70%, danger under 30% and used up", () => {
  const seven = limitsOf([
    { id: "five", window_seconds: 18000, used_percent: 93, resets_at: (NOW + 85 * 60_000) / 1000 },
  ]);
  expect(seven).toContain('data-tone="danger"');
  expect(seven).toContain('data-mode="left"');
  expect(seven).toContain("width:7%");
  expect(plain(seven)).toContain("7% left");
  expect(attr(seven, "title")).toBe("5-hour limit: 7% left, resets in 1h 25m");
  const half = limitsOf([{ id: "wk", window_seconds: 7 * 86400, used_percent: 42 }]);
  expect(half).toContain('data-tone="warn"');
  expect(half).toContain("width:58%");
  expect(plain(half)).toBe("wk58% left");
  expect(limitsOf([{ id: "wk", window_seconds: 7 * 86400, used_percent: 30 }])).toContain('data-tone="ok"');
  expect(limitsOf([{ id: "wk", window_seconds: 7 * 86400, used_percent: 31 }])).toContain('data-tone="warn"');
  expect(limitsOf([{ id: "wk", window_seconds: 7 * 86400, used_percent: 70 }])).toContain('data-tone="warn"');
  expect(limitsOf([{ id: "wk", window_seconds: 7 * 86400, used_percent: 71 }])).toContain(
    'data-tone="danger"',
  );
  const out = limitsOf([{ id: "five", window_seconds: 18000, used_percent: 100 }]);
  expect(out).toContain('data-tone="danger"');
  expect(plain(out)).toContain("Used up");
  // The two shortest request windows only: not a feature's, not a model's.
  const two = limitsOf([
    { id: "month", window_seconds: 30 * 86400, used_percent: 1 },
    { id: "review", window_seconds: 3600, used_percent: 1, feature: true },
    { id: "opus", window_seconds: 3600, used_percent: 1, models: ["opus"] },
    { id: "week", window_seconds: 7 * 86400, used_percent: 1 },
    { id: "five", window_seconds: 18000, used_percent: 1 },
  ]);
  expect([...two.matchAll(/class="limit-name">([^<]+)</g)].map((m) => m[1])).toEqual(["5h", "wk"]);
});

test("a theme moves where the gauge changes colour with meter.mid-below and meter.low-below", () => {
  expect(meterTone(55, { mid: 50, low: 15 })).toBe("ok");
  expect(meterTone(14, { mid: 50, low: 15 })).toBe("danger");
  const themes = {
    resolved: { tokens: { "meter.mid-below": "50", "meter.low-below": "15" } },
    subscribe: () => () => {},
  };
  const kit = { themes } as unknown as Kit;
  const gauge = (percent: number) =>
    renderToString(
      <KitContext.Provider value={kit}>
        <Meter label="g" percent={percent} />
      </KitContext.Provider>,
    );
  expect(gauge(55)).toContain('data-tone="ok"');
  expect(gauge(30)).toContain('data-tone="warn"');
  expect(gauge(14)).toContain('data-tone="danger"');
  // Outside the kit's shell, the contract's defaults: 55% left is under 60.
  expect(renderToString(<Meter label="g" percent={55} />)).toContain('data-tone="warn"');
});

test("the limits' other states are said in words", () => {
  const say = (w: Windows) => plain(renderToString(<Limits windows={w} account="a" />));
  expect(say({ state: "reading", limits: [] })).toBe("Reading limits…");
  expect(say({ state: "none", limits: [] })).toBe("No limits reported");
  const failed = renderToString(
    <Limits windows={{ state: "failed", limits: [], error: "core said no" }} account="a" />,
  );
  expect(plain(failed)).toBe("Limits unavailable");
  expect(attr(failed, "title")).toBe("core said no");
});

test("a reset that has passed shows no countdown, and the limits are asked for again", async () => {
  const past = limitsOf([
    { id: "five", window_seconds: 18000, used_percent: 50, resets_at: (NOW - 60_000) / 1000 },
  ]);
  expect(past).not.toContain("limit-reset");
  expect(attr(past, "title")).toBe("5-hour limit: 50% left");

  // The limits GET /accounts carries, read two minutes ago: one with a reset gone by, one not.
  const reply = (resetsAt: number) => ({
    quota: { limits: [{ id: "five", window_seconds: 18000, used_percent: 50, resets_at: resetsAt / 1000 }] },
    fetched_at: iso(Date.now() - 2 * 60_000),
  });
  const stale = await mountWindows([account({ id: "stale", quota: reply(Date.now() - 60_000) })]);
  expect(stale.calls).toEqual(["stale"]);
  await stale.m.unmount();
  forgetWindows();
  const fresh = await mountWindows([account({ id: "fresh", quota: reply(Date.now() + HOUR) })]);
  expect(fresh.calls).toEqual([]);
  expect(fresh.seen().get("fresh")?.state).toBe("read");
  await fresh.m.unmount();
});

test("an account page's Refresh reads its limits from core again, though GET /accounts carries a fresh copy", async () => {
  const calls: string[] = [];
  const kit = {
    api: {
      quota: (id: string) => {
        calls.push(id);
        return Promise.resolve({
          status: 200,
          body: { quota: { limits: [{ id: "five", window_seconds: 18000, used_percent: 70 }] } },
        });
      },
    },
    unauthorized: noop,
  } as unknown as Kit;
  // The copy GET /accounts carries, read by core a minute ago: fresh, so nothing is read at first.
  const a = account({
    id: "a",
    quota: {
      quota: { limits: [{ id: "five", window_seconds: 18000, used_percent: 10 }] },
      fetched_at: iso(Date.now() - 60_000),
    },
  });
  let refresh = noop;
  let seen: Windows | undefined;
  function Probe() {
    const [n, setN] = useState(0);
    // As the account page's Refresh does it.
    refresh = () => {
      forgetWindow("a");
      setN((x) => x + 1);
    };
    seen = useWindows([a], undefined, n).get("a");
    return <p>{seen?.state}</p>;
  }
  const m = await mountIn(
    <KitContext.Provider value={kit}>
      <Probe />
    </KitContext.Provider>,
  );
  expect(calls).toEqual([]);
  expect(seen?.limits[0]?.used_percent).toBe(10);
  await act(async () => {
    refresh();
  });
  await settle();
  expect(calls).toEqual(["a"]);
  expect(seen?.limits[0]?.used_percent).toBe(70);
  await m.unmount();
});

test("failureWord names every code core sends, from a code alone and from a request", () => {
  const words: Record<string, string> = {
    not_found: "Model not served",
    quota: "Limit used up",
    rate_limit: "Rate limited",
    auth: "Sign-in refused",
    permission: "Sign-in refused",
    account_closed: "Account closed",
    upstream: "Provider error",
    unavailable: "Provider unreachable",
    extension_timeout: "Timed out",
    cancelled: "Cancelled",
    invalid_request: "Request refused",
    unsupported: "Not supported",
    extension_crashed: "Provider stopped",
    extension_protocol: "Provider stopped",
    extension_unavailable: "Provider stopped",
    internal: "pluxy error",
  };
  for (const [code, word] of Object.entries(words)) {
    expect(failureWord(code)).toBe(word);
    expect(failureWord({ error_code: code, status: 500, account: "acc_1" })).toBe(word);
  }
  expect(failureWord("upstream", 529)).toBe("Overloaded");
  expect(failureWord({ error_code: "upstream", status: 529, account: "a" })).toBe("Overloaded");
  expect(failureWord("teapot", 418)).toBe("Failed 418");
  expect(failureWord(undefined, 502)).toBe("Failed 502");
  expect(failureWord(undefined)).toBe("Failed");
  // A refused sign-in that reached no account: the client's key was refused.
  expect(failureWord({ error_code: "auth", status: 401 })).toBe("Key refused");
  expect(
    failureWord({
      error_code: "auth",
      status: 401,
      tried: [{ account: "a", provider: "p", result: "auth" }],
    }),
  ).toBe("Sign-in refused");
});

test("stateText gives an account's condition words, and an extension that is off reads Turned off", () => {
  const words = [
    "active",
    "cooling",
    "exhausted",
    "needs_login",
    "banned",
    "broken",
    "no_provider",
    "disabled",
  ].map((s) => stateText(s, "account"));
  expect(words).toEqual([
    "Serving",
    "Cooling down",
    "Limit used up",
    "Needs sign-in",
    "Closed by provider",
    "Cannot be read",
    "Provider stopped",
    "Turned off",
  ]);
  expect(stateText("disabled", "extension")).toBe("Turned off");
  expect(stateText("enabled", "extension")).toBe("Turned on");
  expect(stateText("active")).toBe("Running");
});

// ---- ToolRow ----------------------------------------------------------------------------------

const tool = (fields: Partial<SetupClient>): SetupClient => ({
  id: "claude-code",
  name: "Claude Code",
  installed: true,
  version: "2.1.0",
  config: "~/.claude/settings.json",
  state: "configured",
  ...fields,
});

test("ToolRow says each of the six states, and Not checked only for a row the host marks", () => {
  const badge = (html: string) => ({
    word: /data-part="badge"[^>]*>([^<]+)</.exec(html)?.[1],
    tone:
      /data-part="badge"[^>]*data-tone="(\w+)"/.exec(html)?.[1] ??
      attr(html.slice(html.indexOf("badge")), "data-tone"),
    title: /data-part="badge"[^>]*title="([^"]*)"/.exec(html)?.[1],
  });
  const of = (c: SetupClient, unchecked?: boolean, reason?: string) =>
    badge(renderToString(<ToolRow client={c} unchecked={unchecked} reason={reason} />));
  expect(of(tool({ installed: false }))).toMatchObject({ word: "Not installed", tone: "muted" });
  expect(of(tool({ state: "configured" }))).toMatchObject({ word: "Connected", tone: "ok" });
  expect(of(tool({ state: "not-configured" }))).toMatchObject({
    word: "Installed · not connected",
    tone: "muted",
  });
  expect(of(tool({ state: "needs-update" }))).toMatchObject({ word: "Settings need update", tone: "warn" });
  expect(of(tool({ state: "refused" }), false, "The file is not JSON.")).toMatchObject({
    word: "Cannot change settings",
    tone: "danger",
    title: "The file is not JSON.",
  });
  expect(of(tool({ name: "Claude Desktop", state: "not-configured" }), true)).toMatchObject({
    word: "Not checked",
    tone: "muted",
    title: "pluxy cannot see how Claude Desktop is set up.",
  });
  // Core's scan never reads Not checked, whatever it found.
  for (const state of ["configured", "not-configured", "needs-update", "refused"] as const) {
    for (const installed of [true, false])
      expect(of(tool({ state, installed })).word).not.toBe("Not checked");
  }
});

test("ToolRow shows the page's action and what the page opens under it", () => {
  const html = renderToString(
    <ToolRow client={tool({})} action={<button type="button">Set up</button>}>
      <p>paste this</p>
    </ToolRow>,
  );
  expect(plain(html)).toContain("Set up");
  expect(html).toContain('<div class="tool-more"><p>paste this</p></div>');
  // A connected tool's state needs no explaining.
  expect(html).not.toMatch(/data-part="badge"[^>]*title=/);
});

// ---- RequestRow -------------------------------------------------------------------------------

const event = (fields: Partial<UsageEvent> = {}): UsageEvent => ({
  seq: 1,
  id: "trace-abc",
  timestamp: iso(NOW),
  model: "opus-5",
  provider: "pluxy/claude",
  account: "acc_1234abcd",
  failed: false,
  status: 200,
  latency_ms: 2200,
  tokens: { input_tokens: 1200, output_tokens: 300, cached_tokens: 1000, total_tokens: 1500 },
  ...fields,
});

test("the compact request row links to the request on Activity", () => {
  const html = renderToString(<RequestRow event={event()} compact />);
  expect(attr(html, "href")).toBe("#/activity?trace=trace-abc");
  expect(html).toContain('aria-label="Succeeded"');
  expect(html).not.toContain("<button");
  const failed = renderToString(
    <RequestRow event={event({ failed: true, error_code: "rate_limit", status: 429 })} compact />,
  );
  expect(plain(failed)).toContain("Rate limited");
});

test("privacy mode masks an address in a request's model names, on the compact row and in the open one", () => {
  const model = "ft:alice.smith@example.com:custom";
  const e = event({
    model,
    served_model: `${model}-2026`,
    tried: [
      { account: "acc_1", provider: "pluxy/claude", model, result: "upstream" },
      { account: "acc_1234abcd", provider: "pluxy/claude", model, result: "ok" },
    ],
  });
  for (const html of [hidden(<RequestRow event={e} compact />), hidden(<RequestRow event={e} open />)]) {
    expect(shown(html)).not.toContain("alice.smith");
    expect(shown(html)).toContain("ft:al***@e***.com:custom");
  }
  // Off: the model as core sent it.
  expect(plain(renderToString(<RequestRow event={e} compact />))).toContain(model);
});

test("a request row says its result, and opens to its facts", async () => {
  const ok = renderToString(<RequestRow event={event()} />);
  expect(ok).toContain('aria-label="Succeeded"');
  expect(plain(ok)).toContain("2.2 s");
  const failed = renderToString(
    <RequestRow event={event({ failed: true, error_code: "upstream", status: 529 })} />,
  );
  expect(plain(failed)).toContain("Overloaded");

  const m = await mountIn(
    <RequestRow event={event({ served_model: "opus-5-20261101", endpoint: "/v1/messages" })} />,
  );
  expect(m.text()).not.toContain("Model used");
  const line = all(m.root, "button")[0] as FakeNode;
  await act(async () => {
    line.dispatchEvent(new Event("click"));
  });
  await settle();
  expect(m.text()).toContain("Model usedopus-5-20261101");
  expect(m.text()).toContain("Path/v1/messages");
  expect(m.text()).toContain("Tokensin 1,200, out 300, cached 1,000");
  await m.unmount();
  const same = await mountIn(<RequestRow event={event({ served_model: "opus-5" })} open />);
  expect(same.text()).not.toContain("Model used");
  await same.unmount();
});

// ---- ConfirmButton's question -----------------------------------------------------------------

test("ConfirmButton shows its question while it asks, and is unchanged without one", async () => {
  const before = renderToString(<ConfirmButton onConfirm={noop}>Delete</ConfirmButton>);
  expect(
    renderToString(
      <ConfirmButton onConfirm={noop} question="Delete the key?">
        Delete
      </ConfirmButton>,
    ),
  ).toBe(before);
  const ask = async (question?: string) => {
    const m = await mountIn(
      <ConfirmButton onConfirm={noop} question={question}>
        Delete
      </ConfirmButton>,
    );
    const b = all(m.root, "button")[0] as FakeNode;
    await act(async () => {
      b.dispatchEvent(new Event("click"));
    });
    await settle();
    const text = m.text();
    await m.unmount();
    return text;
  };
  expect(await ask("Delete the key Claude Code? Tools using it stop working.")).toBe(
    "Delete the key Claude Code? Tools using it stop working.Yes, deleteCancel",
  );
  expect(await ask()).toBe("Yes, deleteCancel");
});

// ---- StatePane and the phase rule -------------------------------------------------------------

const loaded = <T,>(state: Partial<Loaded<T>>): Loaded<T> =>
  ({ loading: false, reload: noop, ...state }) as Loaded<T>;

test("a pane never says it is empty while it loads or failed", () => {
  const pane = (load: Loaded<string[]>) =>
    plain(
      renderToString(
        <Phases
          load={load}
          loading="Loading accounts…"
          failed={{ title: "pluxy did not answer" }}
          isEmpty={(d) => d.length === 0}
          empty={<p>No accounts yet</p>}
        >
          {(d) => <p>{d.join(" and ")}</p>}
        </Phases>,
      ),
    );
  const loading = pane(loaded({ phase: "loading", loading: true }));
  expect(loading).toContain("Loading accounts…");
  expect(loading).not.toContain("No accounts yet");
  const failed = pane(loaded({ phase: "failed", error: "connection refused" }));
  expect(failed).toContain("pluxy did not answer");
  expect(failed).toContain("Try again");
  expect(failed).toContain("connection refused");
  expect(failed).not.toContain("No accounts yet");
  const stale = pane(loaded({ phase: "failed", error: "timeout", data: ["alpha", "beta"] }));
  expect(stale).toMatch(/Could not refresh\. Showing what pluxy said at .+\./);
  expect(stale).toContain("alpha and beta");
  expect(stale).not.toContain("No accounts yet");
  // A refresh that failed over an empty list: what is there now is not known, so not "No … yet".
  const failedEmpty = pane(loaded({ phase: "failed", error: "timeout", data: [] }));
  expect(failedEmpty).not.toContain("No accounts yet");
  expect(failedEmpty).toContain("pluxy did not answer");
  expect(failedEmpty).toContain("Try again");
  expect(pane(loaded({ phase: "ready", data: [] }))).toBe("No accounts yet");
  expect(pane(loaded({ phase: "ready", data: ["alpha"] }))).toBe("alpha");
});

test("a loading pane shows its placeholder only after 150 ms", async () => {
  const m = await mountIn(<StatePane kind="loading" title="Loading accounts…" />);
  const bars = () => all(m.root, "i").length;
  expect(m.text()).toBe("Loading accounts…");
  expect(bars()).toBe(0);
  await Bun.sleep(100);
  await settle();
  expect(bars()).toBe(0);
  await Bun.sleep(100);
  await settle();
  expect(bars()).toBe(3);
  await m.unmount();
});

test("a failed pane holds core's error, masked, under Details", () => {
  const html = renderToString(
    <PrivacyContext.Provider value={privacyMasks(true)}>
      <StatePane kind="failed" title="Could not read the accounts" details="bob@example.com refused" />
    </PrivacyContext.Provider>,
  );
  expect(html).toContain("<summary>Details</summary>");
  expect(html).not.toContain("bob@example.com");
});

test("a pane button can wait: disabled, with its label saying what it waits for", async () => {
  const m = await mountIn(
    <StatePane
      kind="empty"
      title="No pluxy is installed"
      primary={{ label: "Answer the confirmation window…", onClick: noop, disabled: true }}
      secondary={{ label: "Change where pluxy runs", href: "#/connection" }}
    />,
  );
  const wait = all(m.root, "button")[0] as FakeNode;
  expect(wait.textContent).toBe("Answer the confirmation window…");
  expect(wait.disabled).toBe(true);
  const link = all(m.root, "a")[0] as FakeNode;
  expect(link.attributes.find((a) => a.name === "href")?.value).toBe("#/connection");
  await m.unmount();
});

// ---- SettingRow -------------------------------------------------------------------------------

test("a setting that saves on change says Saving…, then goes back with a notice when the save fails", async () => {
  let answer: (r: Reply<unknown>) => void = noop;
  function Probe() {
    const s = useSetting(false, () => new Promise<Reply<unknown>>((resolve) => (answer = resolve)));
    return (
      <SettingRow
        name="Log every request"
        description="Keeps each request's log."
        saving={s.saving}
        error={s.error}
      >
        <Switch label="Log every request" checked={s.value} onChange={s.set} />
      </SettingRow>
    );
  }
  const m = await mountIn(<Probe />);
  const input = all(m.root, "input")[0] as FakeNode;
  expect(m.text()).not.toContain("Saving…");
  input.checked = true;
  await act(async () => {
    input.dispatchEvent(new Event("change"));
  });
  await settle();
  expect(m.text()).toContain("Saving…");
  expect(input.checked).toBe(true);
  await act(async () => {
    answer({ status: 500, error: { code: "internal", message: "the disk is full" } });
  });
  await settle();
  expect(m.text()).not.toContain("Saving…");
  expect(m.text()).toContain("the disk is full");
  expect(input.checked).toBe(false);
  await m.unmount();
});

test("a setting that worked keeps its value; a disabled one says why", async () => {
  function Probe() {
    const s = useSetting("a", () => ({ status: 200, body: {} }) as Reply<unknown>);
    return (
      <SettingRow name="Mode" saving={s.saving} error={s.error}>
        <button type="button" onClick={() => s.set("b")}>
          {s.value}
        </button>
      </SettingRow>
    );
  }
  const m = await mountIn(<Probe />);
  const b = all(m.root, "button")[0] as FakeNode;
  await act(async () => {
    b.dispatchEvent(new Event("click"));
  });
  await settle();
  expect(b.textContent).toBe("b");
  expect(all(m.root, "p")).toHaveLength(0);
  await m.unmount();
  const off = renderToString(
    <SettingRow name="Port" configKey="port" disabled="pluxy runs as a service, so its port is set there.">
      <input />
    </SettingRow>,
  );
  expect(off).toContain("<fieldset disabled");
  expect(plain(off)).toContain("pluxy runs as a service, so its port is set there.");
});

// ---- privacy: everything a part shows about a person is masked ---------------------------------

test("in privacy mode an unlabelled account's ID is masked wherever the row shows it", () => {
  const a = account({
    id: "work-laptop",
    cooldowns: [{ scope: "account", kind: "rate_limit", until: later }],
  });
  const windows: Windows = {
    state: "read",
    limits: [{ id: "five", window_seconds: 18000, used_percent: 10 }],
  };
  const html = hidden(<AccountRow account={a} windows={windows} onTurnOn={noop} onClearCooldown={noop} />);
  expect(shown(html)).not.toContain("work-laptop");
  expect(plain(html)).toContain("wo***op");
  // The fix's and the gauge's names say it the same way.
  expect(attr(html, "aria-label")).not.toContain("work-laptop");
  expect(html).toContain('aria-label="Clear cooldown: wo***op"');
});

function Named(props: { accounts?: Account[]; id: string }) {
  return <p>{useNaming(props.accounts).byId(props.id)}</p>;
}

test("in privacy mode an account a list does not hold is named by its ID, masked", () => {
  const listed = [account({ id: "acc_1", label: "alice@example.com" })];
  expect(plain(hidden(<Named accounts={listed} id="work-laptop" />))).toBe("wo***op");
  expect(plain(hidden(<Named id="work-laptop" />))).toBe("wo***op");
  expect(plain(hidden(<Named accounts={listed} id="acc_1" />))).toBe("al***@e***.com");
});

test("in privacy mode a request row masks its accounts and its key's label, never the key's masked value", () => {
  const e = event({
    account: "work-laptop",
    key_id: "k1",
    label: "Alice laptop key",
    tried: [
      { account: "home-desktop", provider: "pluxy/claude", model: "opus-5", result: "rate_limit" },
      { account: "work-laptop", provider: "pluxy/claude", model: "opus-5", result: "ok" },
    ],
  });
  const html = hidden(<RequestRow event={e} open />);
  for (const real of ["work-laptop", "home-desktop", "Alice laptop key"])
    expect(shown(html)).not.toContain(real);
  expect(plain(html)).toContain("Al***ey");
  const unnamed = hidden(
    <RequestRow event={event({ key_id: "k1" })} keys={[{ id: "k1", masked: "sk-****48" }]} open />,
  );
  expect(plain(unnamed)).toContain("Key sk-…48");
});

test("in privacy mode a failed limits read keeps the account's name out of its title", () => {
  const html = hidden(
    <AccountRow
      account={account({ label: "Work laptop" })}
      windows={{ state: "failed", limits: [], error: "Work laptop: the provider did not answer" }}
      onTurnOn={noop}
      onClearCooldown={noop}
    />,
  );
  expect(shown(html)).not.toContain("Work laptop");
  expect(attr(html.slice(html.indexOf('data-part="limits"')), "title")).toBe(
    "Wo***op: the provider did not answer",
  );
});

test("in privacy mode a fix that fails names the account masked", async () => {
  const m = await mountIn(
    <PrivacyContext.Provider value={privacyMasks(true)}>
      <AccountRow
        account={account({ label: "Work laptop", disabled: true })}
        onTurnOn={() => "could not turn on Work laptop"}
        onClearCooldown={noop}
      />
    </PrivacyContext.Provider>,
  );
  const b = all(m.root, "button").find((n) => n.textContent === "Turn on");
  await act(async () => {
    b?.dispatchEvent(new Event("click"));
  });
  await settle();
  expect(m.text()).toContain("could not turn on Wo***op");
  expect(m.text()).not.toContain("Work laptop");
  await m.unmount();
});

test("in privacy mode a tool's refusal and its settings path are masked", () => {
  const html = hidden(
    <ToolRow
      client={tool({ state: "refused", config: "/home/alice/.claude/settings.json" })}
      reason="The file belongs to alice@example.com."
    />,
  );
  expect(shown(html)).not.toContain("alice@example.com");
  expect(shown(html)).not.toContain("/home/alice");
  expect(plain(html)).toContain("…/.claude/settings.json");
  // Shown as it is while privacy mode is off.
  expect(
    plain(renderToString(<ToolRow client={tool({ config: "/home/alice/.claude/settings.json" })} />)),
  ).toContain("/home/alice/.claude/settings.json");
});

test("in privacy mode a setting's disabled reason is masked", () => {
  const html = hidden(
    <SettingRow name="Port" disabled="Set by the service file of bob@example.com.">
      <input />
    </SettingRow>,
  );
  expect(shown(html)).not.toContain("bob@example.com");
  expect(plain(html)).toContain("Set by the service file of b***@e***.com.");
});

// ---- RequestRow: attempts and the trace ---------------------------------------------------------

test("an opened request lists each attempt's account, provider, model and result", () => {
  const html = renderToString(
    <RequestRow
      event={event({
        account: "acc_b",
        tried: [
          { account: "acc_a", provider: "pluxy/codex", model: "gpt-5", result: "rate_limit" },
          { account: "acc_b", provider: "pluxy/claude", model: "opus-5", result: "ok" },
        ],
      })}
      name={(id) => (id === "acc_a" ? "Alpha" : "Beta")}
      open
    />,
  );
  const attempts = html.slice(html.indexOf('class="request-attempts"'), html.indexOf("</ol>"));
  const items = attempts.split("<li").slice(1);
  expect(items).toHaveLength(2);
  expect(items[0]).toContain('href="#/accounts/acc_a"');
  expect(plain(`<li${items[0]}`)).toMatch(/^Alpha\W*Codex\W*gpt-5\W*Rate limited$/);
  expect(items[1]).toContain('href="#/accounts/acc_b"');
  expect(plain(`<li${items[1]}`)).toMatch(/^Beta\W*Claude\W*opus-5\W*Succeeded$/);
});

test("a request's trace is a link to its log, or copyable text only when the page says so", () => {
  const linked = renderToString(<RequestRow event={event()} open />);
  expect(linked).toContain('href="#/logs/trace-abc"');
  const text = renderToString(<RequestRow event={event()} open trace="text" />);
  expect(text).not.toContain('href="#/logs/');
  expect(plain(text)).toContain("trace-abc");
  expect(text).toContain('data-part="trace-id"');
});

test("a compact failed row says its failure once to a screen reader", () => {
  const html = renderToString(
    <RequestRow event={event({ failed: true, error_code: "rate_limit", status: 429 })} compact />,
  );
  expect(shown(html).split("Rate limited").length - 1).toBe(1);
  // A request that worked still names its mark.
  expect(renderToString(<RequestRow event={event()} compact />)).toContain('aria-label="Succeeded"');
});

// ---- SettingRow: one save at a time ------------------------------------------------------------

test("a setting changed again while it saves sends the newer value only after the first save ends", async () => {
  const sent: boolean[] = [];
  const answers: ((r: Reply<unknown>) => void)[] = [];
  let set: (v: boolean) => Promise<void> = async () => {};
  function Probe() {
    const s = useSetting(false, (v) => {
      sent.push(v);
      return new Promise<Reply<unknown>>((resolve) => answers.push(resolve));
    });
    set = s.set;
    return (
      <SettingRow name="Mode" saving={s.saving} error={s.error}>
        <span>{String(s.value)}</span>
      </SettingRow>
    );
  }
  const m = await mountIn(<Probe />);
  await act(async () => {
    void set(true);
  });
  await act(async () => {
    void set(false);
  });
  await act(async () => {
    void set(true);
  });
  await settle();
  // The first save is on its way; the later ones wait for it, and only the newest is sent.
  expect(sent).toEqual([true]);
  expect(m.text()).toContain("Saving…");
  await act(async () => answers[0]?.({ status: 200, body: {} }));
  await settle();
  expect(sent).toEqual([true, true]);
  expect(m.text()).toContain("Saving…");
  await act(async () => answers[1]?.({ status: 200, body: {} }));
  await settle();
  expect(sent).toEqual([true, true]);
  expect(m.text()).not.toContain("Saving…");
  expect(m.text()).toContain("true");
  await m.unmount();
});

// ---- ConfirmButton: the question is announced, focus comes back ---------------------------------

test("ConfirmButton's answers are described by its question, and focus comes back after a confirm", async () => {
  let confirmed = 0;
  const m = await mountIn(
    <ConfirmButton onConfirm={() => confirmed++} question="Delete the key Claude Code?">
      Delete
    </ConfirmButton>,
  );
  const press = async (label: string) => {
    const b = all(m.root, "button").find((n) => n.textContent === label);
    if (!b) throw new Error(`no ${label}`);
    await act(async () => {
      b.dispatchEvent(new Event("click"));
    });
    await settle();
  };
  await press("Delete");
  const question = all(m.root, "span").find((n) => n.textContent === "Delete the key Claude Code?");
  const id = question?.attributes.find((a) => a.name === "id")?.value;
  expect(id).toBeTruthy();
  for (const answer of ["Yes, delete", "Cancel"]) {
    const b = all(m.root, "button").find((n) => n.textContent === answer);
    expect(b?.attributes.find((a) => a.name === "aria-describedby")?.value).toBe(id);
  }
  expect(m.document.activeElement.textContent).toBe("Cancel");
  // A confirm that returns at once: focus goes back to the button that asked, not to the page.
  await press("Yes, delete");
  expect(confirmed).toBe(1);
  expect(m.document.activeElement.textContent).toBe("Delete");
  expect(m.document.activeElement.localName).toBe("button");
  await m.unmount();
});

// ---- limits not read, and read again on Refresh -------------------------------------------------

test("a row whose limits were not read says so, rather than nothing", () => {
  const html = row(account());
  expect(plain(html.slice(html.indexOf('data-part="limits"')))).toContain("Limits not read");
});

test("a failed limits read is read again when the page refreshes, and not before", async () => {
  const calls: string[] = [];
  let fail = true;
  const kit = {
    api: {
      quota: (id: string) => {
        calls.push(id);
        return Promise.resolve(
          fail
            ? { status: 500, error: { code: "internal", message: "no answer" } }
            : {
                status: 200,
                body: {
                  quota: { limits: [{ id: "five", window_seconds: 18000, used_percent: 40 }] },
                  fetched_at: iso(Date.now()),
                },
              },
        );
      },
    },
    unauthorized: noop,
  } as unknown as Kit;
  const accounts = [account({ id: "one" })];
  let refresh: () => void = noop;
  let seen = new Map<string, Windows>();
  function Probe() {
    const [n, setN] = useState(0);
    refresh = () => setN((x) => x + 1);
    seen = useWindows(accounts, undefined, n);
    return <p>{n}</p>;
  }
  const m = await mountIn(
    <KitContext.Provider value={kit}>
      <Probe />
    </KitContext.Provider>,
  );
  expect(calls).toEqual(["one"]);
  expect(seen.get("one")?.state).toBe("failed");
  fail = false;
  await act(async () => refresh());
  await settle();
  expect(calls).toEqual(["one", "one"]);
  expect(seen.get("one")?.state).toBe("read");
  // A reply that worked is kept: Refresh does not read it again.
  await act(async () => refresh());
  await settle();
  expect(calls).toEqual(["one", "one"]);
  await m.unmount();
});
