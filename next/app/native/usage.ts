// Fixtures for the native test's check of the usage hub (src-tauri/src/usage_native.rs): the hub
// pluxy/agent-usage and the test-only fixture parser pluxy/usage-fixture, from next/app-tools (which
// run.ts builds), each in a signed native package; pluxy/usage-peer, routing.ts's fixture tool
// exporting its `summary`; a curated registry listing the three; the run's test trust profile; and the
// fixture parser's usage files in a root of their own (not its default, so the check must set it).
//
// The Codex parser check (usage-codex, a real package of next/app-tools, packed like the fixture parser
// but with no import) reads a Codex home written here by hand in Codex's rollout format, in the run's
// HOME: two rollouts in `sessions`, one of them copied to `archived_sessions` too, and a third, with
// prompt, reply, working folder and key canaries in the lines the parser must not carry. Its default
// roots are used, so the check sets none.
//
// Each test archive adds one page to its package's manifest: routing.ts's relay page (`page`), which
// sends the calls the check posts it on its channel. The hub's own page stays as it is; the parser has
// no page of its own. The parser's test archive also imports the peer's `summary`, so its `relay` has
// a call it may make: the positive control for the hub's `ingest` refused on the same route. Neither
// relay page nor that import is in a released package.
//
// expect.json holds what the check compares with, written out by hand here, never worked out by the
// hub's or the parser's code: the records below, in a +02:00 zone with no daylight saving, reported at
// a fixed time (2026-11-10 13:30 UTC, 15:30 local, a Tuesday). When a tool has not been built (its
// program is missing), the registry does not list it, and the check fails at its first step for it.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync, type KeyObject } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { native, PAGE_SCRIPT, pack as packPackage, summary } from "./routing";

export const HUB_ID = "pluxy/agent-usage";
export const PARSER_ID = "pluxy/usage-fixture";
export const PEER_ID = "pluxy/usage-peer";
export const CODEX_ID = "pluxy/usage-codex";
/** What the parser relays to the peer's `summary`, and its answer. */
const PEER_ITEMS = ["usage", "positive", "control"];

export interface Usage {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** Strings from the Codex home's lines that must not reach any report, the store or the app's output. */
  canaries: string[];
}

/** A record line of the fixture format, ts in unix seconds. */
function rec(
  ts: number,
  model: string,
  input: number,
  output: number,
  more: Record<string, number | string> = {},
): string {
  return JSON.stringify({ ts, model, input, output, ...more });
}

// The records, by file. Local times are UTC + 2 h.
const A = [
  // 2026-11-09 21:10 UTC, 23:10 local Monday: total 100 + 50 + 30 + 20 = 200.
  rec(1794258600, "Model-X", 100, 50, { id: "m1", cache_read: 30, cache_write: 20 }),
  // 2026-11-09 22:20 UTC, 00:20 local Tuesday 11-10: total 15, reasoning 3 (inside output).
  rec(1794262800, "model-x", 10, 5, { id: "m2", reasoning: 3 }),
  // m1 again: counted once, as first seen.
  rec(1794258600, "Model-X", 9999, 9999, { id: "m1" }),
  // 2026-11-10 08:05 UTC, 10:05 local: the source's own total, 120, wins.
  rec(1794297900, "vendor/Model-Y", 40, 10, { id: "m3", cache_read: 50, total: 120 }),
];
const B = [
  // 2026-11-04 10:00 UTC, 12:00 local Wednesday: 1,200.
  rec(1793786400, "model-y", 1000, 200),
  "not JSON",
  // 2026-10-01 10:00 UTC, 12:00 local: outside the 7 days and the 30 hours' days, in the calendar.
  rec(1790848800, "model-x", 7, 3),
];
/** Appended to A before the third sync: 2026-11-10 12:59 UTC, 14:59 local, 2; and m2 again. */
const APPEND = [
  rec(1794315540, "model-x", 1, 1, { id: "m4" }),
  rec(1794262800, "model-x", 50, 50, { id: "m2" }),
];
/** A new file before the third sync: 2026-11-08 05:00 UTC, 07:00 local Sunday: 3 + 2 + 5 = 10. */
const ADD = [rec(1794114000, "Model-Z", 3, 2, { cache_write: 5 })];
/** The file the cancelled sync reads: one part of 1,000 sent, then a long pause, then 2 more. */
const PAUSE = [
  rec(1794042000, "model-x", 500, 500),
  JSON.stringify({ pause: 120000 }),
  rec(1794045600, "model-x", 1, 1),
];

const NOW = 1794317400;
const OFFSETS = [[0, 120]];

type T = [number, number, number, number, number, number, number];
/** Tokens in the order input, output, cache read, cache write, reasoning, total, samples. */
function t(v: T) {
  const [input, output, cache_read, cache_write, reasoning, total, samples] = v;
  return { input, output, cache_read, cache_write, reasoning, total, samples };
}
const Z: T = [0, 0, 0, 0, 0, 0, 0];
const DAYS = [
  "2026-11-04",
  "2026-11-05",
  "2026-11-06",
  "2026-11-07",
  "2026-11-08",
  "2026-11-09",
  "2026-11-10",
];
const HOURS = Array.from({ length: 16 }, (_, h) => `2026-11-10T${String(h).padStart(2, "0")}`);

/** What `report` must answer, by hand, with the calendar and hours as their nonzero cells
 * ("weekday:hour", Monday 0). */
function expected() {
  const source = (total: number, days: number, status: string) => [
    { id: PARSER_ID, label: "Fixture", status, total, active_days: days },
  ];
  const calendar = (extra: Record<string, number>) => ({
    start: "2025-11-10",
    days: 366,
    nonzero: { "2026-10-01": 10, "2026-11-04": 1200, "2026-11-09": 200, ...extra },
  });
  const sevenDays = (input: object) => ({
    range: "7d",
    granularity: "day",
    sources: [],
    offsets: OFFSETS,
    now: NOW,
    ...input,
  });
  // Before the append: A and B.
  const first = {
    input: sevenDays({}),
    output: {
      range: "7d",
      granularity: "day",
      buckets: DAYS,
      totals: t([1150, 265, 80, 20, 3, 1535, 4]),
      previous: t(Z),
      active_days: 3,
      series: [
        t([1000, 200, 0, 0, 0, 1200, 1]),
        t(Z),
        t(Z),
        t(Z),
        t(Z),
        t([100, 50, 30, 20, 0, 200, 1]),
        t([50, 15, 50, 0, 3, 135, 2]),
      ],
      by_source: { keys: [PARSER_ID], values: [[1200], [0], [0], [0], [0], [200], [135]] },
      by_model: {
        keys: ["model-y", "model-x"],
        values: [
          [1200, 0],
          [0, 0],
          [0, 0],
          [0, 0],
          [0, 0],
          [0, 200],
          [120, 15],
        ],
      },
      models: [
        { model: "model-y", total: 1320 },
        { model: "model-x", total: 215 },
      ],
      sources: source(1535, 3, "active"),
      calendar: calendar({ "2026-11-10": 135 }),
      hours: { "2:12": 1200, "0:23": 200, "1:0": 15, "1:10": 120 },
    },
  };
  // After the append and the new file.
  const sevenAfter = (status: string) => ({
    input: sevenDays({}),
    output: {
      range: "7d",
      granularity: "day",
      buckets: DAYS,
      totals: t([1154, 268, 80, 25, 3, 1547, 6]),
      previous: t(Z),
      active_days: 4,
      series: [
        t([1000, 200, 0, 0, 0, 1200, 1]),
        t(Z),
        t(Z),
        t(Z),
        t([3, 2, 0, 5, 0, 10, 1]),
        t([100, 50, 30, 20, 0, 200, 1]),
        t([51, 16, 50, 0, 3, 137, 3]),
      ],
      by_source: { keys: [PARSER_ID], values: [[1200], [0], [0], [0], [10], [200], [137]] },
      by_model: {
        keys: ["model-y", "model-x", "model-z"],
        values: [
          [1200, 0, 0],
          [0, 0, 0],
          [0, 0, 0],
          [0, 0, 0],
          [0, 0, 10],
          [0, 200, 0],
          [120, 17, 0],
        ],
      },
      models: [
        { model: "model-y", total: 1320 },
        { model: "model-x", total: 217 },
        { model: "model-z", total: 10 },
      ],
      sources: source(1547, 4, status),
      calendar: calendar({ "2026-11-08": 10, "2026-11-10": 137 }),
      hours: { "2:12": 1200, "0:23": 200, "1:0": 15, "1:10": 120, "1:14": 2, "6:7": 10 },
    },
  });
  const today = (status: string) => {
    const series = HOURS.map(() => t(Z));
    series[0] = t([10, 5, 0, 0, 3, 15, 1]);
    series[10] = t([40, 10, 50, 0, 0, 120, 1]);
    series[14] = t([1, 1, 0, 0, 0, 2, 1]);
    const byModel = HOURS.map(() => [0, 0]);
    byModel[0] = [0, 15];
    byModel[10] = [120, 0];
    byModel[14] = [0, 2];
    return {
      // No granularity: today's is the hour.
      input: { range: "today", sources: [PARSER_ID], offsets: OFFSETS, now: NOW },
      output: {
        range: "today",
        granularity: "hour",
        buckets: HOURS,
        totals: t([51, 16, 50, 0, 3, 137, 3]),
        previous: t(Z),
        active_days: 1,
        series,
        by_source: { keys: [PARSER_ID], values: series.map((s) => [s.total]) },
        by_model: { keys: ["model-y", "model-x"], values: byModel },
        models: [
          { model: "model-y", total: 120 },
          { model: "model-x", total: 17 },
        ],
        // A source's total and days are the range's, whatever the filter.
        sources: source(137, 1, status),
        calendar: calendar({ "2026-11-08": 10, "2026-11-10": 137 }),
        hours: { "2:12": 1200, "0:23": 200, "1:0": 15, "1:10": 120, "1:14": 2, "6:7": 10 },
      },
    };
  };
  return {
    first: [first],
    second: [sevenAfter("active"), today("active")],
    // The cancelled file left nothing, and the disabled parser is archived with its rows.
    cancelled: [sevenAfter("archived"), today("archived")],
  };
}

// ---- The Codex home: rollout files in Codex's format, times UTC (local = UTC + 2 h).
const C_ONE = "CANARY-PROMPT-ONE-7f3a";
const C_REPLY = "CANARY-REPLY-ONE-2b91";
const C_TWO = "CANARY-PROMPT-TWO-c04d";
const C_THREE = "CANARY-PROMPT-THREE-58e6";
const C_FOUR = "CANARY-PROMPT-FOUR-a1d7";
const C_FIVE = "CANARY-PROMPT-FIVE-93bc";
const C_CWD = "CANARY-CWD-d2e8";
const C_KEY = "CANARY-KEY-6f10";
const CODEX_CANARIES = [C_ONE, C_REPLY, C_TWO, C_THREE, C_FOUR, C_FIVE, C_CWD, C_KEY];
const S1 = "rollout-2026-11-09T21-00-00-0198a000-0000-7000-8000-000000000001.jsonl";
const S2 = "rollout-2026-11-10T08-00-00-0198a000-0000-7000-8000-000000000002.jsonl";
const S3 = "rollout-2026-11-10T10-00-00-0198a000-0000-7000-8000-000000000003.jsonl";

function line(timestamp: string, type: string, payload: object): string {
  return JSON.stringify({ timestamp, type, payload });
}
const meta = (at: string, id: string) =>
  line(at, "session_meta", {
    id,
    cwd: `/home/${C_CWD}`,
    originator: "codex_cli_rs",
    cli_version: "0.99.0",
    instructions: `${C_KEY}`,
  });
const context = (at: string, model: string) =>
  line(at, "turn_context", { cwd: `/home/${C_CWD}`, model, effort: "high", summary: "auto" });
const message = (at: string, role: string, text: string) =>
  line(at, "response_item", {
    type: "message",
    role,
    content: [{ type: role === "user" ? "input_text" : "output_text", text }],
  });
/** A token_count with Codex's cumulative counters (its total is input + output). */
const count = (at: string, input: number, cached: number, output: number, reasoning: number) => {
  const usage = {
    input_tokens: input,
    cached_input_tokens: cached,
    output_tokens: output,
    reasoning_output_tokens: reasoning,
    total_tokens: input + output,
  };
  return line(at, "event_msg", {
    type: "token_count",
    info: { total_token_usage: usage, last_token_usage: usage, model_context_window: 272000 },
    rate_limits: { primary: null, secondary: null },
  });
};
const FIRST_COUNT = line("2026-11-09T21:00:03.000Z", "event_msg", {
  type: "token_count",
  info: null,
  rate_limits: { primary: null, secondary: null },
});

const CODEX_S1 = [
  meta("2026-11-09T21:00:00.000Z", "0198a000-0000-7000-8000-000000000001"),
  context("2026-11-09T21:00:01.000Z", "gpt-5.5-codex"),
  message("2026-11-09T21:00:02.000Z", "user", C_ONE),
  FIRST_COUNT,
  message("2026-11-09T21:09:58.000Z", "assistant", C_REPLY),
  // 23:10 local Monday 11-09: net input 800, output 300, cache read 200, reasoning 50, total 1,300.
  count("2026-11-09T21:10:00.000Z", 1000, 200, 300, 50),
  // The same counters again: nothing new, but a sample.
  count("2026-11-09T21:10:05.000Z", 1000, 200, 300, 50),
  context("2026-11-09T22:20:00.000Z", "gpt-5.5-codex"),
  message("2026-11-09T22:20:01.000Z", "user", C_TWO),
  // 00:30 local Tuesday 11-10: net input 1,800 - 800 = 1,000, output 300, cache read 500,
  // reasoning 100, total 3,100 - 1,300 = 1,800.
  count("2026-11-09T22:30:00.000Z", 2500, 700, 600, 150),
];
const CODEX_S2 = [
  meta("2026-11-10T08:00:00.000Z", "0198a000-0000-7000-8000-000000000002"),
  context("2026-11-10T08:00:01.000Z", "GPT-5.5"),
  message("2026-11-10T08:00:02.000Z", "user", C_THREE),
  // 10:05 local: net input 300, output 50, cache read 100, total 450.
  count("2026-11-10T08:05:00.000Z", 400, 100, 50, 0),
  context("2026-11-10T09:00:00.000Z", "gpt-5.5-mini"),
  // 11:15 local, the new model: input 1,000 - 300 = 700, output 100, cache read 300, reasoning 20,
  // total 1,550 - 450 = 1,100.
  count("2026-11-10T09:15:00.000Z", 1400, 400, 150, 20),
];
/** Appended to S2 before the third sync: 14:20 local, input 1,400 - 1,000 = 400, output 100, cache
 * read 100, reasoning 30, total 2,150 - 1,550 = 600. */
const CODEX_S2_APPEND = [
  message("2026-11-10T12:19:00.000Z", "user", C_FOUR),
  count("2026-11-10T12:20:00.000Z", 1900, 500, 250, 50),
];
const CODEX_S3 = [
  meta("2026-11-10T10:00:00.000Z", "0198a000-0000-7000-8000-000000000003"),
  context("2026-11-10T10:00:01.000Z", "gpt-5.5-codex"),
  message("2026-11-10T10:00:02.000Z", "user", C_FIVE),
  // 12:30 local: net input 500, output 80, cache read 100, reasoning 10, total 680.
  count("2026-11-10T10:30:00.000Z", 600, 100, 80, 10),
  // 13:10 local, rewound away before the third sync: input 750 - 500 = 250, output 40, cache read 50,
  // reasoning 10, total 340.
  count("2026-11-10T11:10:00.000Z", 900, 150, 120, 20),
];
/** S3's lines kept by the rewind: everything but its last token_count. */
const CODEX_S3_KEPT = 4;

/** Makes the Codex home in home/.codex; gives the writes the check does later. */
function codexHome(home: string) {
  const sessions = join(home, ".codex", "sessions");
  const archived = join(home, ".codex", "archived_sessions");
  mkdirSync(join(sessions, "2026", "11", "09"), { recursive: true });
  mkdirSync(join(sessions, "2026", "11", "10"), { recursive: true });
  mkdirSync(archived, { recursive: true });
  const s1 = join(sessions, "2026", "11", "09", S1);
  writeFileSync(s1, `${CODEX_S1.join("\n")}\n`);
  writeFileSync(join(sessions, "2026", "11", "10", S2), `${CODEX_S2.join("\n")}\n`);
  writeFileSync(join(sessions, "2026", "11", "10", S3), `${CODEX_S3.join("\n")}\n`);
  // An archived copy of S1: the same session (its file name), counted once.
  copyFileSync(s1, join(archived, S1));
  return {
    append: { path: join(sessions, "2026", "11", "10", S2), lines: CODEX_S2_APPEND },
    rewind: { path: join(sessions, "2026", "11", "10", S3), keep: CODEX_S3_KEPT },
  };
}

/** What `report` must answer for the Codex source alone, by hand, before and after the third sync. */
function codexExpected() {
  const sources = (total: number) => [
    { id: CODEX_ID, label: "Codex", status: "active", total, active_days: 2 },
    // The fixture parser, disabled by the cancel step: its rows kept.
    { id: PARSER_ID, label: "Fixture", status: "archived", total: 1547, active_days: 4 },
  ];
  const report = (
    day10: T,
    totals: T,
    models: [string, number, number][],
    total: number,
    hours: Record<string, number>,
  ) => ({
    input: {
      range: "7d",
      granularity: "day",
      sources: [CODEX_ID],
      offsets: OFFSETS,
      now: NOW,
    },
    output: {
      range: "7d",
      granularity: "day",
      buckets: DAYS,
      totals: t(totals),
      previous: t(Z),
      active_days: 2,
      series: [t(Z), t(Z), t(Z), t(Z), t(Z), t([800, 300, 200, 0, 50, 1300, 2]), t(day10)],
      by_source: {
        keys: [CODEX_ID],
        values: [[0], [0], [0], [0], [0], [1300], [day10[5]]],
      },
      by_model: {
        keys: models.map((m) => m[0]),
        values: [
          ...[0, 0, 0, 0, 0].map(() => models.map(() => 0)),
          models.map((m) => (m[0] === "gpt-5.5-codex" ? 1300 : 0)),
          models.map((m) => m[2]),
        ],
      },
      models: models.map((m) => ({ model: m[0], total: m[1] })),
      sources: sources(total),
      calendar: {
        start: "2025-11-10",
        days: 366,
        nonzero: { "2026-11-09": 1300, "2026-11-10": day10[5] },
      },
      hours,
    },
  });
  return {
    // S1 (once, though copied), S2 until its first two counts, S3 whole.
    first: [
      report(
        [2750, 570, 1050, 0, 140, 4370, 5],
        [3550, 870, 1250, 0, 190, 5670, 7],
        [
          ["gpt-5.5-codex", 4120, 2820],
          ["gpt-5.5-mini", 1100, 1100],
          ["gpt-5.5", 450, 450],
        ],
        5670,
        { "0:23": 1300, "1:0": 1800, "1:10": 450, "1:11": 1100, "1:12": 680, "1:13": 340 },
      ),
    ],
    // S2 appended, S3 rewound.
    after: [
      report(
        [2900, 630, 1100, 0, 160, 4630, 5],
        [3700, 930, 1300, 0, 210, 5930, 7],
        [
          ["gpt-5.5-codex", 3780, 2480],
          ["gpt-5.5-mini", 1700, 1700],
          ["gpt-5.5", 450, 450],
        ],
        5930,
        { "0:23": 1300, "1:0": 1800, "1:10": 450, "1:11": 1100, "1:12": 680, "1:14": 600 },
      ),
    ],
  };
}

/** Packs and signs tool name from tools (next/app-tools) with program, the relay page and any imports
 * added; undefined when the tool was not built. */
function pack(
  root: string,
  tools: string,
  name: string,
  program: string,
  publicKey: KeyObject,
  privateKey: KeyObject,
  imports?: object[],
) {
  const source = join(tools, name);
  const manifestPath = join(source, "pluxy-extension.json");
  if (!existsSync(program) || !existsSync(manifestPath)) return undefined;
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const dir = join(root, `pkg-${name}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  const ui = manifest.app.ui ?? { version: 0, dir: "pages", pages: [] };
  if (manifest.app.ui) cpSync(join(source, ui.dir), join(dir, ui.dir), { recursive: true });
  else mkdirSync(join(dir, ui.dir));
  ui.pages = [...ui.pages, { id: "page", title: "Probe", short: "Probe", path: "probe.html" }];
  manifest.app.ui = ui;
  if (imports) manifest.app.imports = imports;
  writeFileSync(
    join(dir, ui.dir, "probe.html"),
    '<!doctype html><meta charset="utf-8"><title>Probe</title><script src="probe.js"></script><p>Probe</p>',
  );
  writeFileSync(join(dir, ui.dir, "probe.js"), PAGE_SCRIPT);
  const bin = `bin/${name}`;
  manifest.app.programs = { [platform()]: bin };
  copyFileSync(program, join(dir, bin));
  chmodSync(join(dir, bin), 0o755);
  writeFileSync(join(dir, "pluxy-extension.json"), JSON.stringify(manifest));
  const f: Fixture = {
    file: `${name}-${manifest.version}.tar.gz`,
    id: manifest.id,
    version: manifest.version,
    tier: "native",
  };
  const r = spawnSync("tar", ["-czf", join(root, f.file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return { id: f.id, version: f.version, bin, ...artifact(root, f.file, f, publicKey, privateKey) };
}

/** Packs and signs the peer with program (the built fixture tool); undefined when it was not built. */
function packPeer(root: string, program: string, publicKey: KeyObject, privateKey: KeyObject) {
  if (!existsSync(program)) return undefined;
  const p = native(PEER_ID, "1.0.0", {
    operations: [summary],
    exports: { contract: 1, operations: ["summary"] },
  });
  const file = packPackage(root, p, program);
  const f: Fixture = { file, id: p.id, version: p.version, tier: "native" };
  return { id: p.id, version: p.version, bin: "bin/tool", ...artifact(root, file, f, publicKey, privateKey) };
}

/** Builds the fixtures in root and the test trust profile in home (the app's HOME), with the built hub
 * and parser programs in target (the app tools' target folder, debug) from tools (next/app-tools), and
 * the peer's program peerProgram (the native test's built fixture tool). */
export function buildUsageFixtures(
  root: string,
  home: string,
  tools: string,
  target: string,
  peerProgram: string,
): Usage {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const hub = pack(root, tools, "agent-usage", join(target, "agent-usage"), publicKey, privateKey);
  const parser = pack(root, tools, "usage-fixture", join(target, "usage-fixture"), publicKey, privateKey, [
    { package: PEER_ID, contract: [1], operations: ["summary"], required: true },
  ]);
  const peer = packPeer(root, peerProgram, publicKey, privateKey);
  const codex = pack(root, tools, "usage-codex", join(target, "usage-codex"), publicKey, privateKey);
  const curated = join(root, "curated.json");
  const packages: Record<string, object> = {};
  for (const p of [hub, parser, peer, codex]) {
    if (p) packages[p.id] = { releases: [{ version: p.version, artifacts: { [platform()]: p.entry } }] };
  }
  writeFileSync(curated, JSON.stringify({ registry_version: 0, packages }));
  const usageRoot = join(root, "usage-root");
  mkdirSync(join(usageRoot, "a"), { recursive: true });
  writeFileSync(join(usageRoot, "a", "2026-11.jsonl"), `${A.join("\n")}\n`);
  writeFileSync(join(usageRoot, "b.jsonl"), `${B.join("\n")}\n`);
  const codexWrites = codexHome(home);
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      hub: HUB_ID,
      parser: PARSER_ID,
      peer: PEER_ID,
      peer_call: { items: PEER_ITEMS, count: 3, longest: "positive" },
      kind: "pluxy/agent-usage/usage",
      versions: {
        [HUB_ID]: hub?.version ?? "0.1.0",
        [PARSER_ID]: parser?.version ?? "0.1.0",
        [PEER_ID]: peer?.version ?? "1.0.0",
        [CODEX_ID]: codex?.version ?? "0.1.0",
      },
      sha256: {
        [HUB_ID]: hub?.sha256 ?? null,
        [PARSER_ID]: parser?.sha256 ?? null,
        [PEER_ID]: peer?.sha256 ?? null,
        [CODEX_ID]: codex?.sha256 ?? null,
      },
      bin: {
        [HUB_ID]: hub?.bin ?? "bin/agent-usage",
        [PARSER_ID]: parser?.bin ?? "bin/usage-fixture",
        [PEER_ID]: peer?.bin ?? "bin/tool",
        [CODEX_ID]: codex?.bin ?? "bin/usage-codex",
      },
      root: usageRoot,
      writes: {
        append: { path: join(usageRoot, "a", "2026-11.jsonl"), lines: APPEND },
        add: { path: join(usageRoot, "c", "new.jsonl"), lines: ADD },
        pause: { path: join(usageRoot, "d", "pause.jsonl"), lines: PAUSE },
      },
      // Each sync's counts for the fixture source: files listed, changed (to read), read, failed.
      syncs: {
        first: { listed: 2, changed: 2, read: 2, failed: 0 },
        repeat: { listed: 2, changed: 0, read: 0, failed: 0 },
        append: { listed: 3, changed: 2, read: 2, failed: 0 },
      },
      reports: expected(),
      // The Codex parser, enabled after the others: its home is in the run's HOME, its roots default.
      codex: {
        id: CODEX_ID,
        writes: codexWrites,
        canaries: CODEX_CANARIES,
        // Each sync's counts for the source: listed (S1 twice), changed (to read), read, failed.
        syncs: {
          first: { listed: 4, changed: 3, read: 3, failed: 0 },
          repeat: { listed: 4, changed: 0, read: 0, failed: 0 },
          after: { listed: 4, changed: 2, read: 2, failed: 0 },
        },
        reports: codexExpected(),
      },
    }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce, canaries: CODEX_CANARIES };
}
