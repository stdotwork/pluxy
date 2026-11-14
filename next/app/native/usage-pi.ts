// Fixtures for the native test's check of the Pi usage parser (usage-pi, next/app-tools), run by
// usage_native.rs after the Oh My Pi parser's: a Pi home written here by hand, in the run's HOME, in the
// format Pi itself writes (docs/session-format.md of earendil-works/pi): session files under both of the
// parser's default roots, `~/.pi/agent/sessions/--<path>--/<time>_<id>.jsonl` and
// `~/.pi/sessions/--<path>--/…`, each beginning with the session header and then entries. Times are UTC;
// the check's zone is +02:00 (local = UTC + 2 h).
//
//   --home-work-alpha--, in both roots: the copy in `sessions` is the newer and has one more record than
//     the one in `agent/sessions`, so it is one session, read once, from the newer copy (never summed);
//     the extra record names a virtual model and the model that answered (`responseModel`), which counts;
//   --home-work-beta--, in `agent/sessions` only: a `usage` entry (Pi's cache warming) and, appended before
//     the third sync, one more record;
//   --home-work-gamma--, in `sessions` only: an earlier date, a model name in other letters, a line that
//     is not JSON.
//
// The token-details cases: reasoning is part of output (never added), cache read and write are their own
// fields (input is already net of them; the one-hour cache write is a part of the write), and a total of
// the source's own wins over the sum. Prompt, reply, folder and key canaries sit where the parser must not
// carry them. Every expected number is worked out here by hand, not by the parser or the hub.

import { mkdirSync, utimesSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Shared } from "./usage-omp";

export const PI_ID = "pluxy/usage-pi";

type T = [number, number, number, number, number, number, number];
/** Tokens in the order input, output, cache read, cache write, reasoning, total, samples. */
function t(v: T) {
  const [input, output, cache_read, cache_write, reasoning, total, samples] = v;
  return { input, output, cache_read, cache_write, reasoning, total, samples };
}
const Z: T = [0, 0, 0, 0, 0, 0, 0];

const C_CWD = "CANARY-PI-CWD-3b8e";
const C_ONE = "CANARY-PI-PROMPT-ONE-a07c";
const C_REPLY = "CANARY-PI-REPLY-5d12";
const C_TWO = "CANARY-PI-PROMPT-TWO-e946";
const C_TOOL = "CANARY-PI-TOOL-OUTPUT-71bf";
const C_THREE = "CANARY-PI-PROMPT-THREE-0d3a";
const C_KEY = "CANARY-PI-KEY-c2f9";
const C_ADDED = "CANARY-PI-PROMPT-ADDED-84e1";
export const PI_CANARIES = [C_CWD, C_ONE, C_REPLY, C_TWO, C_TOOL, C_THREE, C_KEY, C_ADDED];

const ONE = "--home-work-alpha--/2026-11-09T21-00-00-000Z_019c1000-0000-7000-8000-000000000001.jsonl";
const TWO = "--home-work-beta--/2026-11-10T09-00-00-000Z_019c1000-0000-7000-8000-000000000002.jsonl";
const THREE = "--home-work-gamma--/2026-11-04T10-00-00-000Z_019c1000-0000-7000-8000-000000000003.jsonl";
const FOUR = "--home-work-delta--/2026-11-10T11-00-00-000Z_019c1000-0000-7000-8000-000000000004.jsonl";

const header = (at: string, id: string) =>
  JSON.stringify({ type: "session", version: 3, id, timestamp: at, cwd: `/home/${C_CWD}`, apiKey: C_KEY });

let n = 0;
const entry = (at: string, type: string, body: object) =>
  JSON.stringify({ type, id: (++n).toString(16).padStart(8, "0"), parentId: null, timestamp: at, ...body });
const user = (at: string, text: string) =>
  entry(at, "message", { message: { role: "user", content: text, timestamp: Date.parse(at) } });
const cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0.5 };
const assistant = (at: string, model: string, usage: object, extra: object = {}, text = "done") =>
  entry(at, "message", {
    message: {
      role: "assistant",
      api: "anthropic-messages",
      provider: "anthropic",
      model,
      content: [{ type: "text", text }],
      usage: { ...usage, cost },
      stopReason: "stop",
      timestamp: Date.parse(at),
      ...extra,
    },
  });

const ONE_OLD = [
  header("2026-11-09T21:00:00.000Z", "019c1000-0000-7000-8000-000000000001"),
  user("2026-11-09T21:00:01.000Z", C_ONE),
  // 2026-11-09 21:10 UTC, 23:10 local Monday: 100 + 50 + 30 + 20 = 200; 5 of the 20 written for an hour.
  assistant(
    "2026-11-09T21:10:00.000Z",
    "claude-sonnet-4-5",
    {
      input: 100,
      output: 50,
      cacheRead: 30,
      cacheWrite: 20,
      cacheWrite1h: 5,
      reasoning: 0,
      totalTokens: 200,
    },
    {},
    C_REPLY,
  ),
  // 2026-11-09 22:20 UTC, 00:20 local Tuesday 11-10: output 25 includes 5 of reasoning; the input is
  // net of the 40 read from the cache: 10 + 25 + 40 = 75.
  assistant("2026-11-09T22:20:00.000Z", "claude-sonnet-4-5", {
    input: 10,
    output: 25,
    cacheRead: 40,
    cacheWrite: 0,
    reasoning: 5,
    totalTokens: 75,
  }),
  entry("2026-11-09T22:20:05.000Z", "message", {
    message: {
      role: "toolResult",
      toolCallId: "call_1",
      toolName: "bash",
      content: [{ type: "text", text: C_TOOL }],
    },
  }),
];
/** Only in the newer copy: 2026-11-10 08:05 UTC, 10:05 local, a virtual model whose answer came from
 * GPT-5.5: 40 + 10 + 50 = 100, and the source's own total, 120, wins. */
const ONE_NEW = [
  ...ONE_OLD,
  assistant(
    "2026-11-10T08:05:00.000Z",
    "virtual-fast",
    { input: 40, output: 10, cacheRead: 50, cacheWrite: 0, totalTokens: 120 },
    { responseModel: "GPT-5.5" },
  ),
];
const TWO_LINES = [
  header("2026-11-10T09:00:00.000Z", "019c1000-0000-7000-8000-000000000002"),
  user("2026-11-10T09:00:01.000Z", C_TWO),
  // 2026-11-10 09:15 UTC, 11:15 local: a usage entry, cache read 800.
  entry("2026-11-10T09:15:00.000Z", "usage", {
    kind: "cache_warm",
    provider: "openai",
    model: "gpt-5.5-mini",
    usage: { input: 0, output: 0, cacheRead: 800, cacheWrite: 0, totalTokens: 800, cost },
  }),
];
/** Appended to the beta session before the third sync: 2026-11-10 12:20 UTC, 14:20 local: 400 + 100 +
 * 100 = 600, reasoning 30 inside the 100. */
const TWO_APPEND = [
  assistant("2026-11-10T12:20:00.000Z", "gpt-5.5-mini", {
    input: 400,
    output: 100,
    cacheRead: 100,
    cacheWrite: 0,
    reasoning: 30,
    totalTokens: 600,
  }),
];
const THREE_LINES = [
  header("2026-11-04T10:00:00.000Z", "019c1000-0000-7000-8000-000000000003"),
  user("2026-11-04T10:00:01.000Z", C_THREE),
  "not JSON",
  // 2026-11-04 10:00 UTC, 12:00 local Wednesday: 1,200, the model in other letters.
  assistant("2026-11-04T10:00:00.000Z", "Claude-Sonnet-4-5", {
    input: 1000,
    output: 200,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: 1200,
  }),
];
/** A new session written while the parser is disabled: nothing must read it. */
const FOUR_LINES = [
  header("2026-11-10T11:00:00.000Z", "019c1000-0000-7000-8000-000000000004"),
  user("2026-11-10T11:00:01.000Z", C_ADDED),
  assistant("2026-11-10T11:30:00.000Z", "gpt-5.5", {
    input: 5000,
    output: 5000,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: 10000,
  }),
];

const text = (lines: string[]) => `${lines.join("\n")}\n`;

/** Makes the Pi home in home/.pi; gives the writes the check does later. */
export function piHome(home: string) {
  const agent = join(home, ".pi", "agent", "sessions");
  const plain = join(home, ".pi", "sessions");
  const put = (root: string, rel: string, lines: string[]) => {
    const path = join(root, rel);
    mkdirSync(join(path, ".."), { recursive: true });
    writeFileSync(path, text(lines));
    return path;
  };
  // The older copy of the alpha session is a day older, so the newer is the one a scan lists first.
  const old = put(agent, ONE, ONE_OLD);
  const yesterday = new Date(Date.now() - 86_400_000);
  utimesSync(old, yesterday, yesterday);
  put(plain, ONE, ONE_NEW);
  put(agent, TWO, TWO_LINES);
  put(plain, THREE, THREE_LINES);
  return {
    append: { path: join(agent, TWO), lines: TWO_APPEND },
    add: { path: join(plain, FOUR), lines: [...FOUR_LINES] },
  };
}

/** What `report` must answer for this source alone, by hand: before and after the append, and once the
 * parser is disabled (its rows kept, the new session unread). The calendar and hours are their nonzero
 * cells ("weekday:hour", Monday 0). */
export function piExpected(s: Shared) {
  // The hub lists sources by their total in the range, most first, then by ID.
  const sources = (total: number, status: string) =>
    [...s.others, { id: PI_ID, label: "Pi", status, total, active_days: 3 }].sort(
      (a, b) => b.total - a.total || a.id.localeCompare(b.id),
    );
  const report = (
    day10: T,
    totals: T,
    models: [string, number[]][],
    status: string,
    hours: Record<string, number>,
  ) => {
    const days = (a: number, b: number, c: number) => [a, 0, 0, 0, 0, b, c];
    return {
      input: { range: "7d", granularity: "day", sources: [PI_ID], offsets: s.offsets, now: s.now },
      output: {
        range: "7d",
        granularity: "day",
        buckets: s.days,
        totals: t(totals),
        previous: t(Z),
        active_days: 3,
        series: [
          t([1000, 200, 0, 0, 0, 1200, 1]),
          t(Z),
          t(Z),
          t(Z),
          t(Z),
          t([100, 50, 30, 20, 0, 200, 1]),
          t(day10),
        ],
        by_source: { keys: [PI_ID], values: days(1200, 200, day10[5]).map((v) => [v]) },
        by_model: {
          keys: models.map((m) => m[0]),
          values: [0, 1, 2, 3, 4, 5, 6].map((d) => models.map((m) => m[1][d])),
        },
        models: models.map((m) => ({ model: m[0], total: m[1].reduce((a, b) => a + b, 0) })),
        sources: sources(totals[5], status),
        calendar: {
          start: "2025-11-10",
          days: 366,
          nonzero: { "2026-11-04": 1200, "2026-11-09": 200, "2026-11-10": day10[5] },
        },
        hours,
      },
    };
  };
  // Each model's total on each of the seven days, models most tokens first.
  const first = report(
    [50, 35, 890, 0, 5, 995, 3],
    [1150, 285, 920, 20, 5, 2395, 5],
    [
      ["claude-sonnet-4-5", [1200, 0, 0, 0, 0, 200, 75]],
      ["gpt-5.5-mini", [0, 0, 0, 0, 0, 0, 800]],
      ["gpt-5.5", [0, 0, 0, 0, 0, 0, 120]],
    ],
    "active",
    { "2:12": 1200, "0:23": 200, "1:0": 75, "1:10": 120, "1:11": 800 },
  );
  const after = (status: string) =>
    report(
      [450, 135, 990, 0, 35, 1595, 4],
      [1550, 385, 1020, 20, 35, 2995, 6],
      [
        ["claude-sonnet-4-5", [1200, 0, 0, 0, 0, 200, 75]],
        ["gpt-5.5-mini", [0, 0, 0, 0, 0, 0, 1400]],
        ["gpt-5.5", [0, 0, 0, 0, 0, 0, 120]],
      ],
      status,
      { "2:12": 1200, "0:23": 200, "1:0": 75, "1:10": 120, "1:11": 800, "1:14": 600 },
    );
  return { first: [first], after: [after("active")], disabled: [after("archived")] };
}
