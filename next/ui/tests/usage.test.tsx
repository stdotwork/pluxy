import { afterEach, expect, test } from "bun:test";
import type { Account, ClientKey, UsageBucket, UsageEvent, UsageEventsReply, UsageSummary } from "../kit/api";
import type { Reply } from "../kit/host";
import {
  accountValue,
  byDay,
  coreFilters,
  csvName,
  csvOf,
  dayHeading,
  FIND_PAGES,
  figuresOf,
  filtersOf,
  hotspotOf,
  localDays,
  NO_FILTERS,
  queryOf,
  readDownTo,
  readNewest,
  SAVE_LIMIT,
  traceMode,
  usageSince,
  whyOf,
} from "../kit/pages/activity";
import { keyName, timeTaken } from "../kit/parts";
import { currentId, type PageDef } from "../kit/shell";

const event = (fields: Partial<UsageEvent> = {}): UsageEvent => ({
  seq: 1,
  timestamp: "2026-01-01T00:00:00Z",
  model: "fake-model",
  failed: false,
  status: 200,
  latency_ms: 12,
  tokens: { total_tokens: 3 },
  ...fields,
});

const name = (id: string) => ({ a: "aurora", b: "borealis", c: "cirrus" })[id] ?? id;
const story = (e: UsageEvent) => whyOf(e, name, Date.parse("2026-01-01T12:00:00Z"));

// ---- the story --------------------------------------------------------------------------------

test("a request that succeeded on its one attempt has no story", () => {
  const s = story(
    event({ account: "a", attempts: 1, tried: [{ account: "a", provider: "pluxy/claude", result: "ok" }] }),
  );
  expect(s.lines).toEqual([]);
  expect(s.note).toBeUndefined();
  expect(s.next).toBeUndefined();
});

test("a request that succeeded after others failed says how many, and the row says after N others", () => {
  const tried = (n: number) => [
    ...Array.from({ length: n }, () => ({ account: "b", provider: "pluxy/claude", result: "rate_limit" })),
    { account: "a", provider: "pluxy/claude", result: "ok" },
  ];
  expect(story(event({ account: "a", tried: tried(1) })).lines).toEqual([
    "aurora succeeded after 1 other account failed.",
  ]);
  const two = story(event({ account: "a", tried: tried(2) }));
  expect(two.lines).toEqual(["aurora succeeded after 2 other accounts failed."]);
  expect(two.note).toBe("after 2 others");
  expect(two.next).toBeUndefined();
});

test("a request that went to a fallback says so before the line about the accounts", () => {
  const e = event({
    account: "a",
    chain_step: 2,
    tried: [
      { account: "b", provider: "pluxy/claude", model: "fake-model", result: "quota" },
      { account: "a", provider: "pluxy/codex", model: "fallback-model", result: "ok" },
    ],
  });
  const s = story(e);
  expect(s.lines).toEqual([
    "fake-model did not work, so pluxy used its fallback fallback-model (step 2).",
    "aurora succeeded after 1 other account failed.",
  ]);
  expect(s.note).toBe("after 1 other, fallback (step 2)");
  // One attempt, on the fallback: only the fallback line.
  expect(
    story(event({ chain_step: 3, tried: [{ account: "a", provider: "p", model: "m2", result: "ok" }] }))
      .lines,
  ).toEqual(["fake-model did not work, so pluxy used its fallback m2 (step 3)."]);
});

test("a request that failed on its one attempt names the account and the failure, and links the account", () => {
  const s = story(
    event({
      failed: true,
      status: 429,
      error_code: "rate_limit",
      tried: [{ account: "a", provider: "pluxy/claude", result: "rate_limit" }],
    }),
  );
  expect(s.lines).toEqual(["aurora failed: Rate limited."]);
  expect(s.next).toEqual({ kind: "account", account: "a" });
});

test("a request that failed on every attempt says so, and links the last account tried", () => {
  const s = story(
    event({
      failed: true,
      error_code: "upstream",
      tried: [
        { account: "a", provider: "p", result: "rate_limit" },
        { account: "b", provider: "p", result: "upstream" },
      ],
    }),
  );
  expect(s.lines).toEqual(["Every account pluxy tried failed."]);
  expect(s.next).toEqual({ kind: "account", account: "b" });
});

test("a request no account could take lists each account left out, and why", () => {
  const soon = new Date(Date.parse("2026-01-01T12:00:00Z") + 3_600_000).toISOString();
  const s = story(
    event({
      failed: true,
      error_code: "unavailable",
      left_out: [
        { account: "a", state: "cooling", until: soon },
        { account: "b", state: "needs_login" },
        { account: "c", state: "exhausted" },
        { account: "x", state: "disabled" },
        { account: "y", state: "banned" },
        { account: "z", state: "busy" },
        { account: "w", state: "skipped" },
        { account: "v", state: "ok" },
      ],
    }),
  );
  expect(s.lines).toEqual(["No account could take it."]);
  const text = s.leftOut.map((l) => `${l.account}: ${l.text}`);
  expect(text[0]).toMatch(/^aurora: cooling down, back (at|tomorrow at) \d\d:\d\d$/);
  expect(text.slice(1)).toEqual([
    "borealis: needs sign-in",
    "cirrus: limit used up",
    "x: turned off",
    "y: closed by provider",
    "z: busy with other requests",
    "w: not reached before the request gave up",
    "v: not tried",
  ]);
  expect(s.next).toEqual({ kind: "log" });
});

test("a request with no attempt and nothing left out says what went wrong, by its code", () => {
  expect(story(event({ failed: true, error_code: "not_found" }))).toMatchObject({
    lines: ["No account serves fake-model."],
    noServe: true,
    next: { kind: "log" },
  });
  expect(story(event({ failed: true, error_code: "auth" })).lines).toEqual([
    "pluxy did not accept the key this request came with.",
  ]);
  expect(story(event({ failed: true, error_code: "permission" })).lines).toEqual([
    "pluxy did not accept the key this request came with.",
  ]);
  expect(story(event({ failed: true, error_code: "unsupported" })).lines).toEqual([
    "pluxy could not send it to any account: Not supported.",
  ]);
});

// ---- the trace rule ---------------------------------------------------------------------------

test("a failed request's trace links to its log; one that succeeded links only while every request is logged", () => {
  expect(traceMode({ failed: true }, false)).toBe("link");
  expect(traceMode({ failed: true }, undefined)).toBe("link");
  expect(traceMode({ failed: false }, true)).toBe("link");
  expect(traceMode({ failed: false }, false)).toBe("text");
  expect(traceMode({ failed: false }, undefined)).toBe("text");
});

// ---- keys -------------------------------------------------------------------------------------

test("a key is shown by its label, else as Key sk…48", () => {
  const keys: ClientKey[] = [
    { id: "k1", masked: "sk******48", label: "Claude Code" },
    { id: "k2", masked: "sk******77" },
  ];
  expect(keyName("k1", keys)).toBe("Claude Code");
  expect(keyName("k2", keys)).toBe("Key sk…77");
  // The event's own copy of the name wins when the key is gone from the list.
  expect(keyName("k3", keys, "Old tool")).toBe("Old tool");
  expect(keyName(undefined, keys)).toBeUndefined();
});

// ---- the address ------------------------------------------------------------------------------

test("filters go into the address and come back out of it", () => {
  const f = {
    ...NO_FILTERS,
    failed: true,
    provider: "pluxy/claude",
    account: "acc_0123abcd",
    model: "opus",
    key: "k1",
  };
  const q = queryOf(f);
  expect(q).toBe("failed=1&provider=pluxy%2Fclaude&account=acc_0123abcd&model=opus&key=k1");
  expect(filtersOf(new URLSearchParams(q))).toEqual(f);
  expect(queryOf(NO_FILTERS)).toBe("");
  expect(queryOf(NO_FILTERS, "trace-1")).toBe("trace=trace-1");
  expect(filtersOf(new URLSearchParams("failed=true")).failed).toBe(true);
  expect(filtersOf(new URLSearchParams("failed=0")).failed).toBe(false);
});

test("an account in the address is asked of core by its ID, whichever ID the address holds", () => {
  const accounts = [{ id: "work", public_id: "acc_aaaa1111" } as Account, { id: "acc_bbbb2222" } as Account];
  expect(accountValue("work", accounts)).toBe("work");
  expect(accountValue("acc_aaaa1111", accounts)).toBe("work");
  expect(accountValue("acc_bbbb2222", accounts)).toBe("acc_bbbb2222");
  expect(accountValue("gone", accounts)).toBe("gone");
  const asked = coreFilters({ ...NO_FILTERS, failed: true, key: "k1", account: "acc_aaaa1111" }, accounts);
  expect(asked).toEqual({
    since: "",
    model: "",
    provider: "",
    account: "work",
    key_id: "k1",
    failed: "true",
  });
});

const page = (id: string, dest: PageDef["dest"]): PageDef => ({
  id,
  dest,
  title: id,
  render: () => null as never,
});
const pages = [
  page("status", "home"),
  page("usage", "activity"),
  page("activity/usage", "activity"),
  page("config", "settings"),
];
const at = (hash: string) => {
  (globalThis as { location?: unknown }).location = { hash };
  return currentId(pages);
};

test("the shell opens the page an address names, ignoring its query, and a destination's first page for its name", () => {
  const had = (globalThis as { location?: unknown }).location;
  try {
    expect(at("#/usage")).toBe("usage");
    expect(at("#/usage?failed=1")).toBe("usage");
    expect(at("#/activity")).toBe("usage");
    expect(at("#/activity?trace=abc")).toBe("usage");
    expect(at("#/activity/usage")).toBe("activity/usage");
    expect(at("#/activity/usage?account=acc_1")).toBe("activity/usage");
    expect(at("#/nowhere")).toBe("status");
  } finally {
    (globalThis as { location?: unknown }).location = had;
  }
});

// ---- opening one request ----------------------------------------------------------------------

/** A record of n requests, seq n down to 1, read the way core reads it: newest first, by before. */
function record(n: number, calls: { before: number; limit: number }[] = []) {
  return async (before: number, limit: number): Promise<Reply<UsageEventsReply>> => {
    calls.push({ before, limit });
    const top = before === 0 ? n : before - 1;
    const events: UsageEvent[] = [];
    for (let seq = top; seq >= 1 && events.length < limit; seq--) events.push(event({ seq, id: `t${seq}` }));
    const last = events[events.length - 1]?.seq ?? before;
    return { status: 200, body: { events, next: last, more: last > 1, lost: false } };
  };
}

test("opening a request reads down to it and shows the list as far as it", async () => {
  const calls: { before: number; limit: number }[] = [];
  const found = await readDownTo("t1500", 50, record(3000, calls));
  if (!("rows" in found)) throw new Error("not read");
  expect(found.found).toBe(true);
  expect(found.rows).toHaveLength(1501);
  expect(found.rows[0]?.id).toBe("t3000");
  expect(found.rows[1500]?.id).toBe("t1500");
  expect(found.more).toBe(true);
  expect(calls).toEqual([
    { before: 0, limit: 1000 },
    { before: 2001, limit: 1000 },
  ]);
});

test("a request that is not kept is not found, and the list shows its first 50", async () => {
  const calls: { before: number; limit: number }[] = [];
  const missing = await readDownTo("nope", 50, record(25_000, calls));
  if (!("rows" in missing)) throw new Error("not read");
  expect(missing.found).toBe(false);
  expect(missing.rows).toHaveLength(50);
  expect(missing.more).toBe(true);
  // At most ten reads of 1,000.
  expect(calls).toHaveLength(FIND_PAGES);
  const small = await readDownTo("nope", 50, record(20));
  if (!("rows" in small)) throw new Error("not read");
  expect(small).toMatchObject({ found: false, more: false });
  expect(small.rows).toHaveLength(20);
});

// ---- the spreadsheet --------------------------------------------------------------------------

test("a save reads the newest requests first and stops at 10,000", async () => {
  const calls: { before: number; limit: number }[] = [];
  const r = await readNewest(record(25_000, calls));
  if (!("events" in r)) throw new Error("not read");
  expect(r.events).toHaveLength(SAVE_LIMIT);
  expect(r.capped).toBe(true);
  expect(r.events[0]?.seq).toBe(25_000);
  expect(r.events[SAVE_LIMIT - 1]?.seq).toBe(25_000 - SAVE_LIMIT + 1);
  expect(calls).toHaveLength(10);
  expect(calls.every((c) => c.limit === 1000)).toBe(true);
  const all = await readNewest(record(1500));
  if (!("events" in all)) throw new Error("not read");
  expect(all).toMatchObject({ capped: false });
  expect(all.events).toHaveLength(1500);
});

test("a save that cannot read says so, and a refused key is not an error to show", async () => {
  const failing = async (): Promise<Reply<UsageEventsReply>> => ({
    status: 500,
    error: { code: "internal", message: "disk gone" },
  });
  expect(await readNewest(failing)).toEqual({ error: "disk gone" });
  expect(await readNewest(async () => ({ status: 401 }))).toEqual({ unauthorized: true });
});

test("the file keeps its columns, newest first, and is named by the local date", () => {
  const csv = csvOf([
    event({
      seq: 2,
      id: "t2",
      account: "a",
      provider: "p",
      key_id: "k1",
      failed: true,
      status: 500,
      latency_ms: 40,
    }),
    event({ seq: 1, id: "t1", model: "=bad" }),
  ]);
  const lines = csv.trim().split("\r\n");
  expect(lines[0]).toBe("Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID");
  expect(lines[1]).toBe("2026-01-01T00:00:00Z,fake-model,p,a,k1,true,500,40,3,t2");
  expect(lines[2]).toContain("'=bad");
  expect(csvName(new Date(2026, 10, 5))).toBe("pluxy-requests-2026-11-05.csv");
});

// ---- Usage ------------------------------------------------------------------------------------

const hourly = (start: string, requests: number, failure_count: number): UsageBucket => ({
  start,
  requests,
  failure_count,
  total_tokens: requests * 10,
});

const zone = process.env.TZ;
afterEach(() => {
  if (zone === undefined) delete process.env.TZ;
  else process.env.TZ = zone;
});

test("30 days in Sydney add hourly steps into local days, not UTC days", () => {
  process.env.TZ = "Australia/Sydney";
  // 2026-11-26 10:00 in Sydney (UTC+11).
  const now = new Date("2026-11-25T23:00:00Z");
  const series = [
    // 2026-11-25 00:00 in Sydney, still 24 November in UTC.
    hourly("2026-11-24T13:00:00Z", 4, 1),
    // 23:00 on the 25th in Sydney, and in UTC.
    hourly("2026-11-25T12:00:00Z", 3, 0),
    // 00:00 on the 26th in Sydney, still 25 November in UTC.
    hourly("2026-11-25T13:00:00Z", 5, 2),
    // The first hour of the 30 days: 29 days before the 26th, 00:00 local.
    hourly("2026-10-27T13:00:00Z", 7, 0),
  ];
  const days = localDays(series, 30, now);
  expect(days).toHaveLength(30);
  const day = (i: number) => days[i];
  expect(day(29)?.start.getDate()).toBe(26);
  expect(day(29)).toMatchObject({ requests: 5, failed: 2, tokens: 50 });
  expect(day(28)?.start.getDate()).toBe(25);
  expect(day(28)).toMatchObject({ requests: 7, failed: 1 });
  expect(day(27)?.requests).toBe(0);
  expect(day(0)?.start.getDate()).toBe(28);
  expect(day(0)?.requests).toBe(7);
  expect(days.reduce((n, d) => n + d.requests, 0)).toBe(19);
  // 30 days start at local midnight 29 days back, as an instant.
  expect(usageSince("30d", now)).toBe("2026-10-27T13:00:00.000Z");
  expect(usageSince("7d", now)).toBe("2026-11-19T13:00:00.000Z");
  expect(usageSince("24h", now)).toBe("24h");
});

const summary = (fields: Partial<UsageSummary> = {}): UsageSummary => ({
  enabled: true,
  total_requests: 1234,
  success_count: 1148,
  failure_count: 86,
  success_rate: 0.93,
  average_latency_ms: 420,
  tokens: {
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cached_tokens: 1_500_000,
    cache_creation_tokens: 0,
    total_tokens: 4_200_000,
  },
  models: [],
  providers: [],
  accounts: [],
  keys: [],
  bucket_seconds: 3600,
  series: [],
  ...fields,
});

test("the figures say requests, share that succeeded, tokens, cache and average time; a part core lacks is left out", () => {
  expect(figuresOf(summary(), timeTaken)).toBe(
    "1,234 requests · 93% succeeded · 4.2M tokens, 1.5M from cache · 420 ms avg time",
  );
  expect(figuresOf(summary({ tokens: undefined, average_latency_ms: undefined }), timeTaken)).toBe(
    "1,234 requests · 93% succeeded",
  );
  // Only all of them is 100%.
  expect(
    figuresOf(summary({ total_requests: 1000, success_count: 999, failure_count: 1 }), timeTaken),
  ).toContain("99.9% succeeded");
  expect(
    figuresOf(summary({ total_requests: 1, success_count: 1, failure_count: 0 }), timeTaken),
  ).toStartWith("1 request · 100% succeeded");
});

test("the hotspot needs 3 failures and one model with half of them", () => {
  const model = (n: string, failure_count: number) => ({
    name: n,
    total_requests: 10,
    success_count: 10 - failure_count,
    failure_count,
    success_rate: 0,
  });
  expect(hotspotOf({ failure_count: 2, models: [model("a", 2)] })).toBeUndefined();
  expect(hotspotOf({ failure_count: 4, models: [model("a", 3), model("b", 1)] })?.text).toBe(
    "3 of the 4 failed requests asked for a.",
  );
  expect(hotspotOf({ failure_count: 4, models: [model("a", 2), model("b", 2)] })?.model).toBe("a");
  expect(
    hotspotOf({ failure_count: 4, models: [model("a", 1), model("b", 1), model("c", 1), model("d", 1)] }),
  ).toBeUndefined();
  expect(hotspotOf({ failure_count: 3, models: [model("a", 3)] })?.text).toBe(
    "All 3 failed requests asked for a.",
  );
});

test("requests are grouped under Today, Yesterday and the day's name", () => {
  const now = new Date(2026, 10, 27, 15, 0).getTime();
  const at = (d: number, h: number) => new Date(2026, 10, d, h, 5).toISOString();
  expect(dayHeading(at(27, 9), now)).toBe("Today");
  expect(dayHeading(at(26, 23), now)).toBe("Yesterday");
  expect(dayHeading(at(20, 9), now)).toMatch(/^Friday,? (20 November|November 20)$/);
  const groups = byDay(
    [
      event({ seq: 3, timestamp: at(27, 9) }),
      event({ seq: 2, timestamp: at(27, 8) }),
      event({ seq: 1, timestamp: at(26, 8) }),
    ],
    now,
  );
  expect(groups.map((g) => [g.day, g.events.length])).toEqual([
    ["Today", 2],
    ["Yesterday", 1],
  ]);
});
