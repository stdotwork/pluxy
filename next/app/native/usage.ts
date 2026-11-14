// Fixtures for the native test's check of the usage hub (src-tauri/src/usage_native.rs): the hub
// pluxy/agent-usage and the test-only fixture parser pluxy/usage-fixture, from next/app-tools (which
// run.ts builds), each in a signed native package; a curated registry listing them; the run's test
// trust profile; and the fixture parser's usage files in a root of their own (not its default, so the
// check must set it).
//
// Each test archive adds one page to its package's manifest: routing.ts's relay page (`page`), which
// sends the calls the check posts it on its channel. The hub's own page stays as it is; the parser has
// no page of its own. Neither relay page is in a released package.
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
import { PAGE_SCRIPT } from "./routing";

export const HUB_ID = "pluxy/agent-usage";
export const PARSER_ID = "pluxy/usage-fixture";

export interface Usage {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
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

/** Packs and signs tool name from tools (next/app-tools) with program, the relay page added; undefined
 * when the tool was not built. */
function pack(
  root: string,
  tools: string,
  name: string,
  program: string,
  publicKey: KeyObject,
  privateKey: KeyObject,
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

/** Builds the fixtures in root and the test trust profile in home (the app's HOME), with the built hub
 * and parser programs in target (the app tools' target folder, debug) from tools (next/app-tools). */
export function buildUsageFixtures(root: string, home: string, tools: string, target: string): Usage {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const hub = pack(root, tools, "agent-usage", join(target, "agent-usage"), publicKey, privateKey);
  const parser = pack(root, tools, "usage-fixture", join(target, "usage-fixture"), publicKey, privateKey);
  const curated = join(root, "curated.json");
  const packages: Record<string, object> = {};
  for (const p of [hub, parser]) {
    if (p) packages[p.id] = { releases: [{ version: p.version, artifacts: { [platform()]: p.entry } }] };
  }
  writeFileSync(curated, JSON.stringify({ registry_version: 0, packages }));
  const usageRoot = join(root, "usage-root");
  mkdirSync(join(usageRoot, "a"), { recursive: true });
  writeFileSync(join(usageRoot, "a", "2026-11.jsonl"), `${A.join("\n")}\n`);
  writeFileSync(join(usageRoot, "b.jsonl"), `${B.join("\n")}\n`);
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      hub: HUB_ID,
      parser: PARSER_ID,
      kind: "pluxy/agent-usage/usage",
      versions: { [HUB_ID]: hub?.version ?? "0.1.0", [PARSER_ID]: parser?.version ?? "0.1.0" },
      sha256: { [HUB_ID]: hub?.sha256 ?? null, [PARSER_ID]: parser?.sha256 ?? null },
      bin: { [HUB_ID]: hub?.bin ?? "bin/agent-usage", [PARSER_ID]: parser?.bin ?? "bin/usage-fixture" },
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
    }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce };
}
