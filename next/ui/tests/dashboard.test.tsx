// Home (kit/pages/home.tsx): what its rules decide, apart from the page, and the page itself driven
// through Preact's DOM renderer against a fake host. The file keeps its old name; the page's ID is
// still `status`.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { act } from "preact/test-utils";
import { type Account, api, type UsageBucket, type UsageGroup } from "../kit/api";
import type { SetupClient } from "../kit/api/setup";
import type { Host, Method, Reply } from "../kit/host";
import { HomePage } from "../kit/pages/home";
import {
  type Counts,
  countsLine,
  dayText,
  groupAccounts,
  hotspotOf,
  localDays,
  servingOrder,
  toolLine,
  usageQuery,
  verdictOf,
  weekTotals,
} from "../kit/pages/home.helpers";
import type { Windows } from "../kit/parts/hooks";
import { forgetWindows } from "../kit/parts/hooks";
import { type Kit, KitContext } from "../kit/ui";
import { all, button, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

const counts = (c: Partial<Counts>): Counts => {
  const n = { needs: 0, waiting: 0, serving: 0, off: 0, ...c };
  return { ...n, total: n.needs + n.waiting + n.serving + n.off };
};

describe("the verdict", () => {
  test("is the first of six that matches, in order", () => {
    const safe = { safe_mode: true };
    const normal = { safe_mode: false };
    expect(verdictOf(normal, counts({})).headline).toBe("pluxy has no accounts yet.");
    expect(verdictOf(normal, counts({ needs: 1, serving: 2 })).headline).toBe("1 account needs you.");
    expect(verdictOf(normal, counts({ needs: 3 })).headline).toBe("3 accounts need you.");
    expect(verdictOf(normal, counts({ waiting: 1, off: 1 })).headline).toBe("Nothing is serving.");
    expect(verdictOf(normal, counts({ waiting: 1, serving: 1 })).headline).toBe("Nothing needs you.");
    expect(verdictOf(normal, counts({ serving: 2, off: 1 })).headline).toBe("Everything is serving.");
    expect(verdictOf(safe, counts({ serving: 2 })).headline).toBe("pluxy is in safe mode.");
  });

  test("accounts that serve are not called everything when most of the week's requests failed", () => {
    const normal = { safe_mode: false };
    const now = Date.now();
    // Two of three failed: the walk's own numbers.
    for (const c of [counts({ serving: 1 }), counts({ serving: 1, waiting: 1 })]) {
      const v = verdictOf(normal, c, undefined, now, { total: 3, failed: 2 });
      expect(v.kind).toBe("most-failed");
      expect(v.headline).toBe("Accounts are serving, but most requests failed.");
      expect(v.note).toBe("Activity lists the failed requests, with the reason for each.");
    }
    // Half is not most; a week with no requests says nothing of failure; an old core's missing week too.
    expect(verdictOf(normal, counts({ serving: 1 }), undefined, now, { total: 4, failed: 2 }).headline).toBe(
      "Everything is serving.",
    );
    expect(verdictOf(normal, counts({ serving: 1 }), undefined, now, { total: 0, failed: 0 }).kind).toBe(
      "serving",
    );
    expect(verdictOf(normal, counts({ serving: 1 })).kind).toBe("serving");
    // What needs the person, or serves nothing, still comes first; so does safe mode.
    expect(
      verdictOf(normal, counts({ needs: 1, serving: 1 }), undefined, now, { total: 3, failed: 3 }).kind,
    ).toBe("needs-you");
    expect(verdictOf(normal, counts({ off: 1 }), undefined, now, { total: 3, failed: 3 }).kind).toBe(
      "nothing-serving",
    );
    expect(
      verdictOf({ safe_mode: true }, counts({ serving: 1 }), undefined, now, { total: 3, failed: 3 }).kind,
    ).toBe("safe-mode");
  });

  test("safe mode comes before every other, also with accounts that need the person or no accounts", () => {
    expect(verdictOf({ safe_mode: true }, counts({ needs: 2, serving: 1 })).kind).toBe("safe-mode");
    expect(verdictOf({ safe_mode: true }, counts({})).kind).toBe("safe-mode");
    // Without GET /status the verdict is worked out without safe mode.
    expect(verdictOf(undefined, counts({ needs: 2 })).kind).toBe("needs-you");
  });

  test("needing the person comes before nothing serving, and nothing serving before waiting", () => {
    expect(verdictOf({ safe_mode: false }, counts({ needs: 1, waiting: 2 })).kind).toBe("needs-you");
    expect(verdictOf({ safe_mode: false }, counts({ waiting: 2 })).kind).toBe("nothing-serving");
  });

  test("its notes say what safe mode and nothing serving mean, and when the first is back", () => {
    const now = new Date(2026, 10, 25, 12, 0).getTime();
    expect(verdictOf({ safe_mode: true }, counts({ serving: 1 })).note).toBe(
      "No provider runs in safe mode, so every request fails. Restart pluxy without safe mode to serve again.",
    );
    const back = new Date(2026, 10, 25, 14, 5).getTime();
    const nothing = verdictOf({ safe_mode: false }, counts({ waiting: 2 }), back, now);
    expect(nothing.note).toBe(
      "Requests from your coding tools fail until an account serves again. The first is back at 14:05.",
    );
    expect(verdictOf({ safe_mode: false }, counts({ off: 2 }), undefined, now).note).toBe(
      "Requests from your coding tools fail until an account serves again.",
    );
  });
});

describe("the counts line", () => {
  test("holds only the parts that are not zero, and the requests while the record is on", () => {
    expect(countsLine(counts({ waiting: 1, serving: 7, off: 1 }), { total: 453, succeeded: 411 })).toBe(
      "1 waiting · 7 serving · 1 turned off · 453 requests in 7 days, 90.7% succeeded",
    );
    expect(countsLine(counts({ serving: 3 }))).toBe("3 serving");
    expect(countsLine(counts({ serving: 3 }), { total: 0, succeeded: 0 })).toBe("3 serving");
    expect(countsLine(counts({ serving: 1 }), { total: 1, succeeded: 1 })).toBe(
      "1 serving · 1 request in 7 days, 100% succeeded",
    );
  });
});

describe("the hotspot among the failures", () => {
  const models = (...failures: number[]): UsageGroup[] =>
    failures.map((n, i) => ({
      name: `model-${i}`,
      total_requests: 10,
      success_count: 10 - n,
      failure_count: n,
      success_rate: 0,
    }));
  const total = (m: UsageGroup[]) => m.reduce((n, g) => n + g.failure_count, 0);

  test("two failures are not a pattern", () => {
    expect(hotspotOf(models(2), 2)).toBeUndefined();
  });

  test("three of four on one model is one; two of four is not", () => {
    const found = hotspotOf(models(3, 1), 4);
    expect(found?.text).toBe("3 of the 4 failed requests asked for model-0.");
    expect(found?.model).toBe("model-0");
    expect(hotspotOf(models(2, 1, 1), 4)).toBeUndefined();
    expect(hotspotOf(models(2, 2), 4)).toBeUndefined();
  });

  test("every failure on one model says all of them", () => {
    const m = models(5);
    expect(hotspotOf(m, total(m))?.text).toBe("All 5 failed requests asked for model-0.");
  });
});

describe("the accounts", () => {
  const account = (id: string, more: Partial<Account> = {}): Account => ({
    id,
    source: "login",
    state: "active",
    refreshable: false,
    ...more,
  });
  const read = (left: number): Windows => ({
    state: "read",
    limits: [{ id: "five_hour", label: "5h", window_seconds: 18_000, used_percent: 100 - left }],
  });

  test("they fall in the kit's four groups, and the first waiting end is kept", () => {
    const now = Date.now();
    const back = new Date(now + 3_600_000).toISOString();
    const later = new Date(now + 7_200_000).toISOString();
    const g = groupAccounts(
      [
        account("a"),
        account("b", { disabled: true }),
        account("c", { state: "reauth_required" }),
        account("d", { health: { state: "exhausted", until: later } }),
        account("e", { health: { state: "exhausted", until: back } }),
      ],
      undefined,
      now,
    );
    expect(g.counts).toEqual({ needs: 1, waiting: 2, serving: 1, off: 1, total: 5 });
    expect(g.firstBack).toBe(Date.parse(back));
  });

  test("serving ones with the least left come first, those with no limits known last in core's order", () => {
    const list = [account("none-1"), account("plenty"), account("tight"), account("none-2"), account("half")];
    const windows = new Map<string, Windows>([
      ["plenty", read(90)],
      ["tight", read(7)],
      ["half", read(50)],
      ["none-1", { state: "none", limits: [] }],
    ]);
    expect(servingOrder(list, windows).map((a) => a.id)).toEqual([
      "tight",
      "half",
      "plenty",
      "none-1",
      "none-2",
    ]);
  });
});

describe("the last seven days", () => {
  // Sydney is 11 hours ahead of UTC in November. A UTC day is not the person's day there.
  const zone = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = "Australia/Sydney";
  });
  afterAll(() => {
    if (zone === undefined) Reflect.deleteProperty(process.env, "TZ");
    else process.env.TZ = zone;
  });

  // Wednesday 25 November 2026, 14:30 in Sydney.
  const now = () => new Date("2026-11-25T03:30:00Z");
  const bucket = (start: string, requests: number, failed: number): UsageBucket => ({
    start,
    requests,
    failure_count: failed,
    total_tokens: 0,
  });
  const series = [
    bucket("2026-11-18T13:00:00Z", 4, 1), // 00:00 Thu 19, the first hour of the window
    bucket("2026-11-24T12:00:00Z", 6, 0), // 23:00 Tue 24
    bucket("2026-11-24T13:00:00Z", 5, 2), // 00:00 Wed 25
    bucket("2026-11-25T02:00:00Z", 9, 3), // 13:00 Wed 25
    bucket("2026-11-23T20:00:00Z", 7, 0), // 07:00 Tue 24
  ];

  test("the query starts at local midnight six days ago, in hourly steps", () => {
    const query = new URLSearchParams(usageQuery(now()));
    expect(query.get("since")).toBe("2026-11-18T13:00:00.000Z");
    expect(query.get("bucket")).toBe("1h");
  });

  test("hourly steps land in the person's local days, not in UTC days", () => {
    const days = localDays(series, now());
    expect(days).toHaveLength(7);
    expect(days.map((d) => dayText(d).split(":")[0])).toEqual([
      "Thu 19 Nov",
      "Fri 20 Nov",
      "Sat 21 Nov",
      "Sun 22 Nov",
      "Mon 23 Nov",
      "Tue 24 Nov",
      "Wed 25 Nov",
    ]);
    expect(days.map((d) => d.total)).toEqual([4, 0, 0, 0, 0, 6 + 7, 5 + 9]);
    expect(days.map((d) => d.failed)).toEqual([1, 0, 0, 0, 0, 0, 5]);
    // In UTC the 13:00 step would count in the day before; Wednesday here holds 14 requests.
    expect(dayText(days[6] as (typeof days)[number])).toBe("Wed 25 Nov: 14 requests, 5 failed");
  });

  test("the counts line says what the bars add up to", () => {
    const days = localDays(series, now());
    const week = weekTotals(days);
    expect(week).toEqual({ total: 31, failed: 6, succeeded: 25 });
    expect(week.total).toBe(days.reduce((n, d) => n + d.total, 0));
    expect(countsLine(counts({ serving: 1 }), week)).toBe(
      "1 serving · 31 requests in 7 days, 80.6% succeeded",
    );
  });

  test("a step outside the window counts in no day", () => {
    const days = localDays([bucket("2026-11-18T12:00:00Z", 50, 50)], now());
    expect(weekTotals(days).total).toBe(0);
  });
});

describe("the line about coding tools", () => {
  const client = (name: string, more: Partial<SetupClient> = {}): SetupClient => ({
    id: name.toLowerCase().replace(/\W+/g, "-"),
    name,
    installed: true,
    config: "",
    state: "not-configured",
    ...more,
  });

  test("says which are connected, then which are only installed, else that none is", () => {
    expect(toolLine([client("Claude Code", { state: "configured" })])).toEqual({
      state: "connected",
      text: "Claude Code is connected to pluxy.",
    });
    expect(toolLine([client("Claude Code")])).toEqual({
      state: "installed",
      link: "connect",
      text: "Claude Code is installed but not connected to pluxy.",
    });
    expect(toolLine([client("Claude Code", { installed: false })])).toEqual({
      state: "none",
      link: "connectTool",
      text: "No coding tool is connected to pluxy yet.",
    });
    expect(toolLine([])).toMatchObject({ state: "none" });
  });

  test("names two tools, and three or more as the first and a count", () => {
    const on = { state: "configured" } as const;
    expect(toolLine([client("Claude Code", on), client("Codex", on)]).text).toBe(
      "Claude Code and Codex are connected to pluxy.",
    );
    expect(toolLine([client("Claude Code", on), client("Codex", on), client("Gemini CLI", on)]).text).toBe(
      "Claude Code and 2 other tools are connected to pluxy.",
    );
  });

  test("connected wins over installed, and a tool core cannot read says nothing", () => {
    expect(toolLine([client("Claude Code", { state: "configured" }), client("Codex")]).text).toBe(
      "Claude Code is connected to pluxy.",
    );
    expect(toolLine([client("Hermes Agent", { state: "unchecked" })]).state).toBe("none");
  });
});

// ---- the page ------------------------------------------------------------------------------------

afterEach(() => {
  restoreDom();
  forgetWindows();
});

const hiccup: Reply<unknown> = { status: 502, error: { code: "bad_gateway", message: "core hiccup" } };
const down: Reply<unknown> = { status: 0 };
const ok = (body: unknown): Reply<unknown> => ({ status: 200, body });
const statusBody = {
  safe_mode: false,
  extensions: [],
  desired: { revision: 1 },
  active: { revision: 1, at: "" },
};
const usageBody = (requests: number) => ({
  enabled: true,
  total_requests: requests,
  success_count: requests,
  failure_count: 0,
  models: [],
  providers: [],
  accounts: [],
  keys: [],
  bucket_seconds: 3600,
  series:
    requests === 0 ? [] : [{ start: new Date().toISOString(), requests, failure_count: 0, total_tokens: 0 }],
});

/** One reply for each of the page's four reads in a round; the next round's replace them. */
async function mount(
  rounds: Record<"accounts" | "status" | "usage" | "events", Reply<unknown>>[],
  once: { keys?: unknown[]; clients?: unknown[] } = {},
) {
  let round = -1;
  const reads: string[] = [];
  const host = {
    call: async (method: Method, path: string): Promise<Reply<unknown>> => {
      reads.push(path);
      if (method === "GET" && path === "/accounts") round++;
      const now = rounds[Math.min(round, rounds.length - 1)];
      if (method === "GET" && path === "/accounts") return now?.accounts ?? hiccup;
      if (method === "GET" && path === "/status") return now?.status ?? hiccup;
      if (method === "GET" && path.startsWith("/usage/events")) return now?.events ?? hiccup;
      if (method === "GET" && path.startsWith("/usage")) return now?.usage ?? hiccup;
      if (path.endsWith("/quota")) return { status: 422 };
      if (path === "/client-keys") return once.keys ? ok({ keys: once.keys }) : hiccup;
      return ok({ found: { clients: once.clients ?? [] } });
    },
    signedIn: () => true,
    prefs: { get: () => null, set: () => {} },
    features: new Set(),
    address: "http://127.0.0.1:8317",
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
      <HomePage />
    </KitContext.Provider>,
  );
  await settle();
  return {
    ...page,
    reads,
    refresh: async () => {
      await act(async () => {
        button(page.root, "Refresh").dispatchEvent(new Event("click"));
      });
      await settle();
    },
  };
}

const list = (...ids: string[]) =>
  ok({ accounts: ids.map((id) => ({ id, source: "login", state: "active", refreshable: false })) });
const good = {
  accounts: list("work-1", "work-2"),
  status: ok(statusBody),
  usage: ok(usageBody(5)),
  events: ok({ events: [], next: 0, more: false, lost: false }),
};
const count = (text: string, part: string) => text.split(part).length - 1;
const attr = (n: FakeNode, name: string) => n.attributes.find((a) => a.name === name)?.value;
/** The elements under root whose data-part (or, with by "class", one of whose classes) is name. */
function parts(root: FakeNode, name: string, by: "data-part" | "class" = "data-part"): FakeNode[] {
  const own =
    by === "class" ? (attr(root, "class") ?? "").split(" ").includes(name) : attr(root, by) === name;
  return [...(own ? [root] : []), ...root.childNodes.flatMap((c) => parts(c, name, by))];
}

describe("the page", () => {
  test("says it is checking before the first reply, and counts nothing", async () => {
    const page = await mountIn(
      <KitContext.Provider
        value={
          {
            host: { call: () => new Promise(() => {}), address: undefined, features: new Set() },
            api: api({ call: () => new Promise(() => {}) } as unknown as Host),
            themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
            unauthorized: () => {},
          } as unknown as Kit
        }
      >
        <HomePage />
      </KitContext.Provider>,
    );
    expect(page.text()).toContain("Checking pluxy…");
    expect(page.text()).not.toContain("0 ");
    expect(page.text()).not.toContain("no accounts");
  });

  test("a first load that fails in all four reads is one pane, never a notice for each", async () => {
    const page = await mount([{ accounts: hiccup, status: hiccup, usage: hiccup, events: hiccup }]);
    expect(count(page.text(), "pluxy could not list its accounts.")).toBe(1);
    expect(page.text()).toContain("core hiccup");
    expect(page.text()).toContain("Try again");
    expect(page.text()).not.toContain("Could not refresh");
    expect(page.text()).not.toContain("Could not read the last 7 days.");
  });

  test("a core that does not answer says so", async () => {
    const page = await mount([{ accounts: down, status: down, usage: down, events: down }]);
    expect(page.text()).toContain("Cannot reach pluxy.");
    expect(page.text()).toContain("Check that pluxy is running, then try again.");
  });

  test("four reads failing in one refresh give one stale notice, and the rows stay", async () => {
    const page = await mount([good, { accounts: hiccup, status: hiccup, usage: hiccup, events: hiccup }]);
    expect(page.text()).toContain("work-1");
    expect(page.text()).not.toContain("Could not refresh");
    await page.refresh();
    expect(count(page.text(), "Could not refresh.")).toBe(1);
    expect(page.text()).toContain("work-1");
    expect(page.text()).toContain("work-2");
    expect(page.text()).toContain("Everything is serving.");
  });

  test("with no accounts it says what to do next, the kinds of account and how to point a tool, and no groups or chart", async () => {
    const page = await mount([{ ...good, accounts: list() }]);
    const text = page.text();
    expect(text).toContain("pluxy has no accounts yet.");
    expect(text).toContain("Add an account");
    expect(text).toContain("Guided setup");
    expect(text).toContain("Next: connect a coding tool.");
    // The four kinds and More, each leading to Add an account.
    const kinds = parts(page.root, "home-kind", "class");
    expect(kinds.map((k) => all(k, "span").at(-1)?.textContent)).toEqual([
      "Claude",
      "ChatGPT (Codex)",
      "Gemini",
      "Another service with an API key",
      "More",
    ]);
    expect(kinds.every((k) => attr(k, "href") === "#/add-account")).toBe(true);
    expect(text).toContain("Point a tool at pluxy");
    expect(parts(page.root, "pool-card")).toHaveLength(0);
    expect(text).not.toContain("Last 7 days");
    expect(text).not.toContain("Serving");
  });

  test("with accounts it leads with the verdict, the address and the groups", async () => {
    const page = await mount([good]);
    const text = page.text();
    expect(text).toContain("Everything is serving.");
    expect(text).toContain("2 serving · 5 requests in 7 days, 100% succeeded");
    expect(text).toContain("Point a tool at pluxy");
    expect(text).toContain("Base URL");
    expect(text).toContain("http://127.0.0.1:8317/v1");
    expect(text).toContain("Serving");
    // Nothing needs repair, so Refresh is the view's one filled button.
    expect(attr(button(page.root, "Refresh"), "data-variant")).toBe("primary");
  });

  test("Refresh is filled when the only band fix is Turn on, and quiet on the empty state", async () => {
    const banned = ok({
      accounts: [
        {
          id: "closed-1",
          extension: "pluxy/claude",
          source: "login",
          state: "active",
          refreshable: true,
          health: { state: "banned" },
        },
      ],
    });
    const page = await mount([{ ...good, accounts: banned }]);
    expect(attr(button(page.root, "Refresh"), "data-variant")).toBe("primary");
    expect(attr(button(page.root, "Turn on"), "data-variant")).toBe("quiet");
    await page.unmount();

    const empty = await mount([{ ...good, accounts: list() }]);
    expect(attr(button(empty.root, "Refresh"), "data-variant")).toBe("quiet");
  });

  test("the connection card gives the base URL of the API a tool speaks, a key's masked value and the tools set up", async () => {
    const page = await mount([good], {
      keys: [{ id: "k1", masked: "sk-pluxy-****3f9a", label: "Claude Code" }],
      clients: [
        { id: "claude-code", name: "Claude Code", installed: true, config: "", state: "configured" },
        { id: "opencode", name: "OpenCode", installed: true, config: "", state: "not-configured" },
        { id: "pi", name: "Pi", installed: false, config: "", state: "not-configured" },
        { id: "hermes", name: "Hermes", installed: true, config: "", state: "unchecked" },
      ],
    });
    const card = parts(page.root, "connection-card")[0];
    if (!card) throw new Error("no connection card");
    expect(all(card, "code")[0]?.textContent).toBe("http://127.0.0.1:8317/v1");
    // OpenAI, Claude, Gemini, in that order.
    const claude = all(card, "input")[1];
    await act(async () => {
      claude?.dispatchEvent(new Event("change"));
    });
    await settle();
    expect(all(card, "code")[0]?.textContent).toBe("http://127.0.0.1:8317");
    expect(card.textContent).toContain("Key · Claude Code");
    expect(card.textContent).toContain("sk-pluxy-****3f9a");
    const keysLink = all(card, "a").find((a) => a.textContent === "Keys");
    expect(keysLink && attr(keysLink, "href")).toBe("#/tools");
    expect(keysLink && all(keysLink, "svg")).toHaveLength(1);
    // Set up, and installed but not set up; a tool not installed or unchecked has no chip.
    const chips = parts(card, "tool-chip", "class");
    expect(chips.map((c) => [c.textContent, attr(c, "data-state"), attr(c, "href")])).toEqual([
      ["Claude Code", "configured", "#/tools"],
      ["OpenCode", "not-configured", "#/tools"],
    ]);
  });

  test("accounts that need the person are in the band with one filled fix; the rest are by provider, each with a pool", async () => {
    const accounts = ok({
      accounts: [
        {
          id: "c-out",
          extension: "pluxy/claude",
          source: "login",
          state: "reauth_required",
          refreshable: true,
          quota: { fetched_at: new Date().toISOString(), quota: { plan: "Max 20x", limits: [] } },
        },
        { id: "c-ok", extension: "pluxy/claude", source: "login", state: "active", refreshable: true },
        {
          id: "x-out",
          extension: "pluxy/codex",
          source: "login",
          state: "reauth_required",
          refreshable: true,
        },
        { id: "x-ok", extension: "pluxy/codex", source: "login", state: "active", refreshable: true },
        { id: "x-off", extension: "pluxy/codex", source: "login", state: "active", disabled: true },
      ],
    });
    const running = { ...statusBody, extensions: [{ id: "pluxy/claude" }, { id: "pluxy/codex" }] };
    const page = await mount([{ ...good, accounts, status: ok(running) }]);
    const band = parts(page.root, "needs-band")[0];
    if (!band) throw new Error("no band");
    expect(attr(band, "aria-label")).toBe("Needs you");
    const fixes = parts(band, "button").map((b) => [b.textContent, attr(b, "data-variant")]);
    expect(fixes).toEqual([
      ["Sign in again", "primary"],
      ["Sign in again", "quiet"],
    ]);
    expect(parts(band, "plan-pill").map((p) => [p.textContent, attr(p, "data-plan-tier")])).toEqual([
      ["Max 20x", "top"],
    ]);
    expect(attr(button(page.root, "Refresh"), "data-variant")).toBe("quiet");
    const groups = parts(page.root, "home-group", "class");
    expect(groups.map((g) => attr(g, "aria-label"))).toEqual(["Claude", "Codex"]);
    const ids = (g: FakeNode) => parts(g, "account-name", "class").map((a) => a.textContent);
    expect(groups.map(ids)).toEqual([["c-ok"], ["x-ok", "x-off"]]);
    const pools = parts(page.root, "pool-card");
    expect(pools.map((p) => attr(p, "data-provider"))).toEqual(["claude", "codex"]);
    // Every account of the provider counts in its pool, including turned-off and needs-you ones.
    expect(pools.map((p) => p.textContent?.match(/\d of \d serving/)?.[0])).toEqual([
      "1 of 2 serving",
      "1 of 3 serving",
    ]);
  });

  test("lists every account under a provider without a six-row cap, and keeps the All accounts link", async () => {
    const ten = Array.from({ length: 10 }, (_, i) => ({
      id: `c-${i + 1}`,
      extension: "pluxy/claude",
      source: "login",
      state: "active",
      refreshable: true,
    }));
    const running = { ...statusBody, extensions: [{ id: "pluxy/claude" }] };
    const page = await mount([{ ...good, accounts: ok({ accounts: ten }), status: ok(running) }]);
    const group = parts(page.root, "home-group", "class")[0];
    if (!group) throw new Error("no provider group");
    expect(parts(group, "account-name", "class").map((a) => a.textContent)).toEqual(ten.map((a) => a.id));
    expect(page.text()).not.toContain("And 4 more");
    const allLink = all(page.root, "a").find((a) => a.textContent === "All accounts");
    expect(allLink && attr(allLink, "href")).toBe("#/accounts");
  });

  test("when two of three requests failed the headline does not say everything is serving", async () => {
    const failed = {
      ...usageBody(3),
      success_count: 1,
      failure_count: 2,
      series: [{ start: new Date().toISOString(), requests: 3, failure_count: 2, total_tokens: 0 }],
    };
    const page = await mount([{ ...good, usage: ok(failed) }]);
    const text = page.text();
    expect(text).toContain("Accounts are serving, but most requests failed.");
    expect(text).not.toContain("Everything is serving.");
    expect(text).toContain("33.3% succeeded");
  });

  test("when only the seven days cannot be read, the accounts stay and one line offers Try again", async () => {
    const page = await mount([{ ...good, usage: hiccup }]);
    expect(page.text()).toContain("work-1");
    expect(page.text()).toContain("Could not read the last 7 days.");
    expect(page.text()).not.toContain("Could not refresh");
  });

  test("a status that cannot be read leaves the verdict without safe mode and shows no refused notice", async () => {
    const page = await mount([{ ...good, status: hiccup }]);
    expect(page.text()).toContain("Everything is serving.");
    expect(page.text()).not.toContain("refused");
  });

  test("when status fails with no prior read, accounts with a provider read Not checked and stay out of the serving count and pool mean", async () => {
    const accounts = ok({
      accounts: [
        {
          id: "c-1",
          extension: "pluxy/claude",
          source: "login",
          state: "active",
          refreshable: true,
          quota: {
            fetched_at: new Date().toISOString(),
            quota: { limits: [{ id: "five", window_seconds: 18_000, used_percent: 20 }] },
          },
        },
        {
          id: "c-wait",
          extension: "pluxy/claude",
          source: "login",
          state: "active",
          refreshable: true,
          cooldowns: [
            { scope: "account", kind: "rate_limit", until: new Date(Date.now() + 3_600_000).toISOString() },
          ],
        },
      ],
    });
    const page = await mount([{ ...good, accounts, status: hiccup }]);
    const text = page.text();
    expect(text).toContain("Could not check which providers are running.");
    expect(text).not.toContain("Everything is serving.");
    expect(text).toContain("2 not checked");
    expect(text).toContain("pluxy could not check whether Claude is running.");
    const rows = parts(page.root, "account-row");
    expect(rows).toHaveLength(2);
    for (const r of rows) expect(attr(r, "data-condition")).toBe("unchecked");
    const pool = parts(page.root, "pool-card")[0];
    if (!pool) throw new Error("no pool card");
    expect(pool.textContent).toContain("Not checked");
    expect(pool.textContent).not.toContain("80%");
    expect(pool.textContent).not.toContain("serving");
  });

  test("a refused settings change shows its error, once", async () => {
    const refused = {
      ...statusBody,
      desired: { revision: 2, error: "unknown key" },
      active: { revision: 1, at: "2026-11-25T12:00:00Z" },
    };
    const page = await mount([{ ...good, status: ok(refused) }]);
    expect(count(page.text(), "pluxy refused the last change to its settings: unknown key.")).toBe(1);
    expect(page.text()).toContain("Open Config");
  });

  test("with the record off it says so, with a link to the setting, and shows neither section", async () => {
    const off = { ...usageBody(0), enabled: false };
    const page = await mount([{ ...good, usage: ok(off) }]);
    expect(page.text()).toContain(
      "pluxy is not keeping a record of requests, so there is nothing to show here.",
    );
    expect(page.text()).toContain("Turn it on in Config");
    expect(page.text()).not.toContain("Latest requests");
    expect(page.text()).not.toContain("requests in 7 days");
  });

  test("Refresh reads the limits of the accounts it shows from core again, though GET /accounts carries a fresh copy", async () => {
    const copy = {
      quota: { limits: [{ id: "five", window_seconds: 18000, used_percent: 10 }] },
      // Read by core a minute ago: fresh, so nothing is read from the provider at first.
      fetched_at: new Date(Date.now() - 60_000).toISOString(),
    };
    const fresh = ok({
      accounts: [{ id: "work-1", source: "login", state: "active", refreshable: false, quota: copy }],
    });
    const page = await mount([{ ...good, accounts: fresh }]);
    const quotaReads = () => page.reads.filter((p) => p.endsWith("/quota"));
    expect(page.text()).toContain("work-1");
    expect(quotaReads()).toEqual([]);
    await page.refresh();
    expect(quotaReads()).toEqual(["/accounts/work-1/quota"]);
  });

  test("with no requests in 7 days it says so and reads the coding tools once", async () => {
    const page = await mount([{ ...good, usage: ok(usageBody(0)) }]);
    expect(page.text()).toContain("No requests in the last 7 days.");
    expect(page.text()).toContain("No coding tool is connected to pluxy yet.");
    expect(page.text()).toContain("Connect a tool");
    const connect = all(page.root, "a").find((a) => a.textContent === "Connect a tool");
    expect(connect?.attributes.find((a) => a.name === "href")?.value).toBe("#/tools");
    expect(page.reads.filter((p) => p === "/setup")).toHaveLength(1);
    await page.refresh();
    expect(page.reads.filter((p) => p === "/setup")).toHaveLength(1);
  });
});
