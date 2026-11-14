// Fixtures for the native test's check of the Oh My Pi usage parser (usage-omp, next/app-tools), run by
// usage_native.rs after the Codex parser's: an Oh My Pi home written here by hand, in the run's HOME, in
// the format Oh My Pi itself writes (docs/session.md of can1357/oh-my-pi): session files under both of
// the parser's default roots, `~/.omp/agent/sessions/<folder>/<time>_<id>.jsonl` and
// `~/.omp/sessions/<folder>/…`, each beginning with the 256-byte `title` line, then the header and the
// entries. Times are UTC; the check's zone is +02:00 (local = UTC + 2 h).
//
//   -work-alpha, in both roots: the copy in `sessions` is the newer and has one more record than the one
//     in `agent/sessions`, so it is one session, read once, from the newer copy (never summed);
//   -work-beta, in `agent/sessions` only: a `model_usage` entry, a `task` result whose `details.usage`
//     is its child's (counted in the child's own file, never here) and, appended before the third
//     sync, one more record;
//   -work-gamma, in `sessions` only: an earlier date, a model name in other letters, a line that is not
//     JSON;
//   -work-alpha-fork, in `agent/sessions` only: a fork of the -work-alpha session, Oh My Pi's `forkFrom`,
//     which copies the parent's entries unchanged (same `id` and `timestamp`) into a new file, and one
//     entry of its own: the copied entries count once, not in each file.
//
// The token-details cases are in -work-alpha and -work-beta: reasoning is part of output (never added),
// cache read and write are their own fields (input is already net of them), and a total of the
// source's own (which holds provider-side tokens the other fields do not) wins over the sum. Prompt,
// reply, folder, title and key canaries sit where the parser must not carry them. Every expected
// number is worked out here by hand, not by the parser or the hub.

import { mkdirSync, utimesSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export const OMP_ID = "pluxy/usage-omp";

/** What usage.ts shares with this file (its own constants, passed so the two files do not import each other). */
export interface Shared {
  now: number;
  offsets: number[][];
  /** The seven days of the 7-day report, oldest first. */
  days: string[];
  /** The other sources a report lists, each as the hub gives it. */
  others: { id: string; label: string; status: string; total: number; active_days: number }[];
}

type T = [number, number, number, number, number, number, number];
/** Tokens in the order input, output, cache read, cache write, reasoning, total, samples. */
function t(v: T) {
  const [input, output, cache_read, cache_write, reasoning, total, samples] = v;
  return { input, output, cache_read, cache_write, reasoning, total, samples };
}
const Z: T = [0, 0, 0, 0, 0, 0, 0];

const C_TITLE = "CANARY-OMP-TITLE-4c1e";
const C_CWD = "CANARY-OMP-CWD-9a07";
const C_ONE = "CANARY-OMP-PROMPT-ONE-e3b5";
const C_REPLY = "CANARY-OMP-REPLY-1d68";
const C_TWO = "CANARY-OMP-PROMPT-TWO-72fa";
const C_TOOL = "CANARY-OMP-TOOL-OUTPUT-b904";
const C_THREE = "CANARY-OMP-PROMPT-THREE-0c5d";
const C_KEY = "CANARY-OMP-KEY-58ae";
const C_ADDED = "CANARY-OMP-PROMPT-ADDED-d4f3";
export const OMP_CANARIES = [C_TITLE, C_CWD, C_ONE, C_REPLY, C_TWO, C_TOOL, C_THREE, C_KEY, C_ADDED];

const ONE = "-work-alpha/2026-11-09T21-00-00-000Z_019c0000-0000-7000-8000-000000000001.jsonl";
const TWO = "-work-beta/2026-11-10T09-00-00-000Z_019c0000-0000-7000-8000-000000000002.jsonl";
const THREE = "-work-gamma/2026-11-04T10-00-00-000Z_019c0000-0000-7000-8000-000000000003.jsonl";
const FORK = "-work-alpha-fork/2026-11-10T13-00-00-000Z_019c0000-0000-7000-8000-000000000005.jsonl";
const FOUR = "-work-delta/2026-11-10T11-00-00-000Z_019c0000-0000-7000-8000-000000000004.jsonl";

/** Oh My Pi's first line: a fixed-width 256-byte slot (its newline included) holding the title. */
const titleSlot = JSON.stringify({ type: "title", title: C_TITLE, titleSource: "user" }).padEnd(255, " ");
const header = (at: string, id: string) =>
  JSON.stringify({
    type: "session",
    version: 3,
    id,
    timestamp: at,
    cwd: `/home/${C_CWD}`,
    title: C_TITLE,
    providerPromptCacheKey: C_KEY,
  });

let n = 0;
const entry = (at: string, type: string, body: object) =>
  JSON.stringify({ type, id: (++n).toString(16).padStart(8, "0"), parentId: null, timestamp: at, ...body });
const user = (at: string, text: string) =>
  entry(at, "message", { message: { role: "user", content: text, timestamp: Date.parse(at) } });
const cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0.5 };
const assistant = (at: string, model: string, usage: object, text = "done") =>
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
    },
  });
const modelUsage = (at: string, model: string, usage: object) =>
  entry(at, "model_usage", {
    purpose: "title",
    role: "smol",
    api: "openai-responses",
    provider: "openai",
    model,
    usage: { ...usage, cost },
    stopReason: "stop",
  });

const ONE_OLD = [
  titleSlot,
  header("2026-11-09T21:00:00.000Z", "019c0000-0000-7000-8000-000000000001"),
  user("2026-11-09T21:00:01.000Z", C_ONE),
  // 2026-11-09 21:10 UTC, 23:10 local Monday: 100 + 50 + 30 + 20 = 200.
  assistant(
    "2026-11-09T21:10:00.000Z",
    "claude-sonnet-4-5",
    { input: 100, output: 50, cacheRead: 30, cacheWrite: 20, totalTokens: 200 },
    C_REPLY,
  ),
  // 2026-11-09 22:20 UTC, 00:20 local Tuesday 11-10: output 25 includes 5 of reasoning; the input is
  // net of the 40 read from the cache: 10 + 25 + 40 = 75.
  assistant("2026-11-09T22:20:00.000Z", "claude-sonnet-4-5", {
    input: 10,
    output: 25,
    cacheRead: 40,
    cacheWrite: 0,
    reasoningTokens: 5,
    totalTokens: 75,
  }),
  entry("2026-11-09T22:20:05.000Z", "message", {
    message: { role: "toolResult", toolName: "bash", content: [{ type: "text", text: C_TOOL }] },
  }),
];
/** Only in the newer copy: 2026-11-10 08:05 UTC, 10:05 local; the source's own total, 120, holds 20 of
 * provider-side tokens: 40 + 10 + 50 + 20. */
const ONE_NEW = [
  ...ONE_OLD,
  assistant("2026-11-10T08:05:00.000Z", "gpt-5.5", {
    input: 40,
    output: 10,
    cacheRead: 50,
    cacheWrite: 0,
    totalTokens: 120,
    orchestration: { input: 20 },
  }),
];
const TWO_LINES = [
  titleSlot,
  header("2026-11-10T09:00:00.000Z", "019c0000-0000-7000-8000-000000000002"),
  user("2026-11-10T09:00:01.000Z", C_TWO),
  // 2026-11-10 09:15 UTC, 11:15 local: 700 + 100 + 300 = 1,100, reasoning 20 inside the 100.
  modelUsage("2026-11-10T09:15:00.000Z", "gpt-5.5-mini", {
    input: 700,
    output: 100,
    cacheRead: 300,
    cacheWrite: 0,
    reasoningTokens: 20,
    totalTokens: 1100,
  }),
  // A task result: its child's usage is in `details`, in the child's own session file; not counted here.
  entry("2026-11-10T09:40:00.000Z", "message", {
    message: {
      role: "toolResult",
      toolName: "task",
      content: [{ type: "text", text: C_TOOL }],
      details: {
        usage: { input: 9999, output: 9999, cacheRead: 9999, cacheWrite: 9999, totalTokens: 39996 },
      },
    },
  }),
];
/** Appended to two.jsonl before the third sync: 2026-11-10 12:20 UTC, 14:20 local: 400 + 100 + 100 = 600,
 * reasoning 30 inside the 100. */
const TWO_APPEND = [
  assistant("2026-11-10T12:20:00.000Z", "gpt-5.5-mini", {
    input: 400,
    output: 100,
    cacheRead: 100,
    cacheWrite: 0,
    reasoningTokens: 30,
    totalTokens: 600,
  }),
];
const THREE_LINES = [
  titleSlot,
  header("2026-11-04T10:00:00.000Z", "019c0000-0000-7000-8000-000000000003"),
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
/** The fork of the alpha session: its parent's entries as they are, then its own: 2026-11-10 13:05 UTC,
 * 15:05 local: 200 + 50 + 100 = 350. */
const FORK_LINES = [
  titleSlot,
  header("2026-11-10T13:00:00.000Z", "019c0000-0000-7000-8000-000000000005"),
  ...ONE_NEW.slice(2),
  assistant("2026-11-10T13:05:00.000Z", "gpt-5.5-mini", {
    input: 200,
    output: 50,
    cacheRead: 100,
    cacheWrite: 0,
    totalTokens: 350,
  }),
];
/** A new session written while the parser is disabled: nothing must read it. */
const FOUR_LINES = [
  titleSlot,
  header("2026-11-10T11:00:00.000Z", "019c0000-0000-7000-8000-000000000004"),
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

/** Makes the Oh My Pi home in home/.omp; gives the writes the check does later. */
export function ompHome(home: string) {
  const agent = join(home, ".omp", "agent", "sessions");
  const plain = join(home, ".omp", "sessions");
  const put = (root: string, rel: string, lines: string[]) => {
    const path = join(root, rel);
    mkdirSync(join(path, ".."), { recursive: true });
    writeFileSync(path, text(lines));
    return path;
  };
  // The older copy of one.jsonl is a day older, so the newer is the one a scan lists first.
  const old = put(agent, ONE, ONE_OLD);
  const yesterday = new Date(Date.now() - 86_400_000);
  utimesSync(old, yesterday, yesterday);
  put(plain, ONE, ONE_NEW);
  put(agent, TWO, TWO_LINES);
  put(plain, THREE, THREE_LINES);
  put(agent, FORK, FORK_LINES);
  return {
    append: { path: join(agent, TWO), lines: TWO_APPEND },
    add: { path: join(plain, FOUR), lines: [...FOUR_LINES] },
  };
}

/** What `report` must answer for this source alone, by hand: before and after the append, and once the
 * parser is disabled (its rows kept, the new session unread). The calendar and hours are their nonzero
 * cells ("weekday:hour", Monday 0). */
export function ompExpected(s: Shared) {
  // The hub lists sources by their total in the range, most first, then by ID.
  const sources = (total: number, status: string) =>
    [...s.others, { id: OMP_ID, label: "Oh My Pi", status, total, active_days: 3 }].sort(
      (a, b) => b.total - a.total || a.id.localeCompare(b.id),
    );
  const report = (
    day10: T,
    totals: T,
    models: [string, number[]][],
    status: string,
    hours: Record<string, number>,
  ) => {
    // Per day: 11-04, then 11-09, then 11-10; the other four days are empty.
    const days = (a: number, b: number, c: number) => [a, 0, 0, 0, 0, b, c];
    return {
      input: { range: "7d", granularity: "day", sources: [OMP_ID], offsets: s.offsets, now: s.now },
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
        by_source: { keys: [OMP_ID], values: days(1200, 200, day10[5]).map((v) => [v]) },
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
    [950, 185, 490, 0, 25, 1645, 4],
    [2050, 435, 520, 20, 25, 3045, 6],
    [
      ["claude-sonnet-4-5", [1200, 0, 0, 0, 0, 200, 75]],
      ["gpt-5.5-mini", [0, 0, 0, 0, 0, 0, 1450]],
      ["gpt-5.5", [0, 0, 0, 0, 0, 0, 120]],
    ],
    "active",
    { "2:12": 1200, "0:23": 200, "1:0": 75, "1:10": 120, "1:11": 1100, "1:15": 350 },
  );
  const after = (status: string) =>
    report(
      [1350, 285, 590, 0, 55, 2245, 5],
      [2450, 535, 620, 20, 55, 3645, 7],
      [
        ["gpt-5.5-mini", [0, 0, 0, 0, 0, 0, 2050]],
        ["claude-sonnet-4-5", [1200, 0, 0, 0, 0, 200, 75]],
        ["gpt-5.5", [0, 0, 0, 0, 0, 0, 120]],
      ],
      status,
      { "2:12": 1200, "0:23": 200, "1:0": 75, "1:10": 120, "1:11": 1100, "1:14": 600, "1:15": 350 },
    );
  return { first: [first], after: [after("active")], disabled: [after("archived")] };
}
