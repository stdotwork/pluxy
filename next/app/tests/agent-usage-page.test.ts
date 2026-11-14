// The Agent usage page's rendering (next/app-tools/agent-usage/pages/view.js) of a fixed report:
// every card, cell, bar and heat level below is counted by hand from the report, which is the one
// the usage native check expects first (native/usage.ts), with its calendar and hours written out.
import { describe, expect, test } from "bun:test";
// @ts-expect-error: a plain ES module of the page, without types.
import * as v from "../../app-tools/agent-usage/pages/view.js";

const T = (
  input: number,
  output: number,
  cr: number,
  cw: number,
  re: number,
  total: number,
  samples: number,
) => ({
  input,
  output,
  cache_read: cr,
  cache_write: cw,
  reasoning: re,
  total,
  samples,
});
const Z = T(0, 0, 0, 0, 0, 0, 0);
const DAYS = [
  "2026-11-04",
  "2026-11-05",
  "2026-11-06",
  "2026-11-07",
  "2026-11-08",
  "2026-11-09",
  "2026-11-10",
];

function calendar() {
  // 2025-11-10 to 2026-11-10: 366 days; 2026-10-01 is day 325, 11-04 day 359, 11-09 364, 11-10 365.
  const totals = new Array(366).fill(0);
  totals[325] = 10;
  totals[359] = 1200;
  totals[364] = 200;
  totals[365] = 135;
  return { start: "2025-11-10", totals };
}

function hours() {
  const h = Array.from({ length: 7 }, () => new Array(24).fill(0));
  h[2][12] = 1200;
  h[0][23] = 200;
  h[1][0] = 15;
  h[1][10] = 120;
  return h;
}

function report(over: Record<string, unknown> = {}) {
  return {
    range: "7d",
    granularity: "day",
    buckets: DAYS,
    totals: T(1150, 265, 80, 20, 3, 1535, 4),
    previous: Z,
    active_days: 3,
    series: [
      T(1000, 200, 0, 0, 0, 1200, 1),
      Z,
      Z,
      Z,
      Z,
      T(100, 50, 30, 20, 0, 200, 1),
      T(50, 15, 50, 0, 3, 135, 2),
    ],
    by_source: { keys: ["pluxy/usage-fixture"], values: [[1200], [0], [0], [0], [0], [200], [135]] },
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
    sources: [{ id: "pluxy/usage-fixture", label: "Fixture", status: "active", total: 1535, active_days: 3 }],
    calendar: calendar(),
    hours: hours(),
    sync: { running: false, last_sync: null, interval_minutes: 5, sources: [] },
    ...over,
  };
}

describe("cards", () => {
  test("total tokens and cache hit rate", () => {
    const c = v.cards(report());
    expect([c.tokens.value, c.tokens.title, c.tokens.subtitle]).toEqual([
      "1.5K",
      "1,535",
      "No usage in previous period",
    ]);
    // Shares of 80 + 20 + 1,150 + 265 = 1,515.
    expect(c.tokens.mix.map((m: { label: string; text: string }) => `${m.label} ${m.text}`)).toEqual([
      "Cache read 5%",
      "Cache write 1%",
      "Input 76%",
      "Output 17%",
    ]);
    // 80 / (1,150 + 80 + 20).
    expect([c.cache.value, c.cache.subtitle]).toEqual(["6.4%", "80 read · 20 written"]);
  });

  test("the change from the previous period, or active days without one", () => {
    const down = v.cards(report({ previous: T(0, 0, 0, 0, 0, 2000, 1) })).tokens;
    expect([down.subtitle, down.tone]).toEqual(["−23% vs previous period", "down"]);
    // (1,535 − 1,200) / 1,200 = 27.9%.
    const up = v.cards(report({ previous: T(0, 0, 0, 0, 0, 1200, 1) })).tokens;
    expect([up.subtitle, up.tone]).toEqual(["+28% vs previous period", "up"]);
    expect(v.cards(report({ previous: T(0, 0, 0, 0, 0, 1534, 1) })).tokens.subtitle).toBe(
      "Same as previous period",
    );
    expect(v.cards(report({ previous: null })).tokens.subtitle).toBe("3 active days");
  });
});

describe("charts", () => {
  test("the trend by model stacks each bucket's models", () => {
    const m = v.trend(report(), "model");
    expect(m.keys.map((k: { label: string }) => k.label)).toEqual(["model-y", "model-x"]);
    expect(m.bars.map((b: { total: number }) => b.total)).toEqual([1200, 0, 0, 0, 0, 200, 135]);
    expect(
      m.bars[6].segments.map((s: { value: number; y0: number; y1: number }) => [s.value, s.y0, s.y1]),
    ).toEqual([
      [120, 0, 120],
      [15, 120, 135],
    ]);
    expect([m.max, m.ticks, m.labels, m.bars[6].label.axis]).toEqual([
      1200,
      [0, 500, 1000, 1500],
      true,
      "11-10",
    ]);
  });

  test("the trend by source names sources by their label with the package ID, and Other", () => {
    const m = v.trend(report(), "source");
    expect(m.keys.map((k: { label: string; title: string | null }) => [k.label, k.title])).toEqual([
      ["Fixture", "Fixture · pluxy/usage-fixture"],
    ]);
    expect(v.trend(report(), "model").keys[0].title).toBe(null);
    const other = v.trend(
      report({ by_source: { keys: ["a", "__other__"], values: DAYS.map(() => [1, 2]) } }),
      "source",
    );
    expect(other.keys.map((k: { label: string; title: string | null }) => [k.label, k.title])).toEqual([
      ["a", "a · a"],
      ["Other", null],
    ]);
  });

  test("the token mix stacks cache read, cache write, input and output", () => {
    const m = v.tokenMix(report());
    expect(m.keys.map((k: { label: string }) => k.label)).toEqual([
      "Cache read",
      "Cache write",
      "Input",
      "Output",
    ]);
    // 11-10: 50 cache read, 0 cache write, 50 input, 15 output.
    expect(
      m.bars[6].segments.map((s: { value: number; y0: number; y1: number }) => [s.value, s.y0, s.y1]),
    ).toEqual([
      [50, 0, 50],
      [0, 50, 50],
      [50, 50, 100],
      [15, 100, 115],
    ]);
    expect(v.tokenMix(report({ series: DAYS.map(() => Z) })).empty).toBe(true);
  });

  test("hour buckets read as dates and hours", () => {
    expect(v.bucketLabel("2026-11-10T05", "hour")).toEqual({ axis: "11-10 05:00", full: "2026-11-10 05:00" });
    expect(v.bucketLabel("2026-11", "month")).toEqual({ axis: "2026-11", full: "2026-11" });
  });
});

describe("comparing sources", () => {
  // Claude Code and Codex together, as the usage native check's combined report has them, and the
  // fixture parser archived.
  const CLAUDE = "pluxy/usage-claude";
  const CODEX = "pluxy/usage-codex";
  const FIXTURE = "pluxy/usage-fixture";
  const sources = [
    { id: CODEX, label: "Codex", status: "active", total: 5930, active_days: 2 },
    { id: CLAUDE, label: "Claude Code", status: "active", total: 5535, active_days: 2 },
    { id: FIXTURE, label: "Fixture", status: "archived", total: 1547, active_days: 4 },
  ];
  const both = report({
    sources,
    by_source: {
      keys: [CODEX, CLAUDE],
      values: [
        [0, 0],
        [0, 0],
        [0, 0],
        [0, 0],
        [0, 0],
        [1300, 1270],
        [4630, 4265],
      ],
    },
  });
  // The same sources ranked the other way, with Claude Code alone selected.
  const flipped = report({
    sources: [sources[1], sources[0], sources[2]].map((s) => (s.id === CLAUDE ? { ...s, total: 8555 } : s)),
    by_source: { keys: [CLAUDE], values: [[0], [0], [0], [0], [0], [1270], [7285]] },
  });
  // The old page's palette by place among the IDs sorted: usage-claude, usage-codex, usage-fixture.
  const color = { [CLAUDE]: "#3b82f6", [CODEX]: "#10b981", [FIXTURE]: "#8b5cf6" };

  test("each source keeps its colour in the trend, the chips and the Sources table", () => {
    for (const r of [both, flipped]) {
      for (const k of v.trend(r, "source").keys) expect(k.color).toBe(color[k.key as keyof typeof color]);
      for (const c of v.chips(r, []).slice(1)) expect(c.color).toBe(color[c.id as keyof typeof color]);
      for (const row of v.sourcesTable(r, [CLAUDE]).rows)
        expect(row.color).toBe(color[row.id as keyof typeof color]);
    }
    expect(v.trend(both, "source").keys.map((k: { color: string }) => k.color)).toEqual([
      "#10b981",
      "#3b82f6",
    ]);
    // Models keep the series colours by rank.
    expect(v.trend(both, "model").keys.map((k: { color: string }) => k.color)).toEqual([
      "#3b82f6",
      "#f59e0b",
    ]);
  });

  test("the Sources table gives each source's share for its bar", () => {
    // 5,930, 5,535 and 1,547 of 13,012.
    const rows = v.sourcesTable(both, []).rows;
    expect(rows.map((r: { cells: string[] }) => r.cells[1])).toEqual(["45.6%", "42.5%", "11.9%"]);
    expect(rows[0].share).toBeCloseTo(0.4557, 4);
    expect(rows[1].share).toBeCloseTo(0.4254, 4);
    expect(rows[2].share).toBeCloseTo(0.1189, 4);
  });

  test("a bar's hover text breaks its total down, top part first", () => {
    const bars = v.trend(both, "source").bars;
    expect(bars[6].tip).toBe("2026-11-10: 8,895\nClaude Code: 4,265\nCodex: 4,630");
    expect(bars[5].tip).toBe("2026-11-09: 2,570\nClaude Code: 1,270\nCodex: 1,300");
    expect(bars[1].tip).toBe("2026-11-05: 0");
    // The token mix: 11-10 of the fixture report, 50 cache read, no cache write, 50 input, 15 output.
    expect(v.tokenMix(report()).bars[6].tip).toBe("2026-11-10: 115\nOutput: 15\nInput: 50\nCache read: 50");
  });
});

describe("activity", () => {
  test("calendar cells by week and weekday, with quartile levels", () => {
    const c = v.calendar(report());
    expect(c.weeks).toBe(53);
    const pick = (i: number) => {
      const x = c.cells[i];
      return [x.date, x.value, x.level, x.col, x.row];
    };
    // Positive days 10, 135, 200, 1,200: quartiles 10, 135, 200.
    expect([pick(325), pick(359), pick(364), pick(365), pick(0)]).toEqual([
      ["2026-10-01", 10, 1, 46, 3],
      ["2026-11-04", 1200, 4, 51, 2],
      ["2026-11-09", 200, 3, 52, 0],
      ["2026-11-10", 135, 2, 52, 1],
      ["2025-11-10", 0, 0, 0, 0],
    ]);
    expect(c.months[0]).toEqual({ col: 0, month: "2025-11" });
  });

  test("hours by weekday and hour", () => {
    const cells = v.hours(report());
    const at = (row: number, hour: number) =>
      cells.find((c: { row: number; hour: number }) => c.row === row && c.hour === hour);
    // Positive hours 15, 120, 200, 1,200: quartiles 15, 120, 200.
    expect(
      [at(2, 12), at(0, 23), at(1, 0), at(1, 10), at(6, 6)].map((c) => [c.weekday, c.value, c.level]),
    ).toEqual([
      ["Wed", 1200, 4],
      ["Mon", 200, 3],
      ["Tue", 15, 1],
      ["Tue", 120, 2],
      ["Sun", 0, 0],
    ]);
    expect(cells.length).toBe(168);
  });
});

describe("tables", () => {
  test("models: share of the listed models and compact totals", () => {
    const m = v.modelsTable(report(), false);
    expect(m.header).toEqual(["Models", "Share", "Total tokens"]);
    // 1,320 / 1,535 and 215 / 1,535.
    expect(m.rows.map((r: { cells: string[] }) => r.cells)).toEqual([
      ["model-y", "86.0%", "1.3K"],
      ["model-x", "14.0%", "215"],
    ]);
    expect(m.more).toBe(null);
    const nine = Array.from({ length: 9 }, (_, i) => ({ model: i === 8 ? "" : `m${i}`, total: 9 - i }));
    const few = v.modelsTable(report({ models: nine }), false);
    expect([few.rows.length, few.more]).toEqual([8, "Show all 9 models"]);
    const all = v.modelsTable(report({ models: nine }), true);
    expect([all.rows.length, all.more, all.rows[8].cells[0]]).toEqual([9, "Show fewer", "Unknown model"]);
    const capped = v.modelsTable(
      report({
        models: [
          { model: "m", total: 9 },
          { model: "__other__", total: 1 },
        ],
      }),
      false,
    );
    expect(capped.rows.map((r: { cells: string[] }) => r.cells[0])).toEqual(["m", "Other"]);
  });

  test("sources: share, total and active days; the package ID, archived and dimmed", () => {
    const s = v.sourcesTable(report(), []);
    expect(s.header).toEqual(["Agent sources", "Share", "Total tokens", "Active days"]);
    expect(
      s.rows.map((r: { cells: string[]; note: string | null; dimmed: boolean }) => [
        ...r.cells,
        r.note,
        r.dimmed,
      ]),
    ).toEqual([["Fixture", "100.0%", "1.5K", "3", "pluxy/usage-fixture", false]]);
    const archived = report({
      sources: [
        { id: "pluxy/usage-fixture", label: "Fixture", status: "archived", total: 1535, active_days: 3 },
        { id: "pluxy/other", label: "Other agent", status: "active", total: 1465, active_days: 1 },
      ],
    });
    // 1,535 / 3,000 and 1,465 / 3,000.
    expect(
      v
        .sourcesTable(archived, ["pluxy/other"])
        .rows.map((r: { cells: string[]; note: string | null; dimmed: boolean }) => [
          r.cells[1],
          r.note,
          r.dimmed,
        ]),
    ).toEqual([
      ["51.2%", "pluxy/usage-fixture · Uninstalled · history kept", true],
      ["48.8%", "pluxy/other", false],
    ]);
  });

  test("details: buckets with tokens, newest first", () => {
    const d = v.detailsTable(report());
    expect(d.header).toEqual([
      "Date",
      "Input tokens",
      "Output tokens",
      "Cache read",
      "Cache write",
      "Total tokens",
    ]);
    expect(d.rows).toEqual([
      ["2026-11-10", "50", "15", "50", "0", "135"],
      ["2026-11-09", "100", "50", "30", "20", "200"],
      ["2026-11-04", "1,000", "200", "0", "0", "1,200"],
    ]);
    expect(v.detailsTable(report({ granularity: "week" })).header[0]).toBe("Week of");
  });
});

describe("controls and status", () => {
  test("source chips: All, then each source with tokens, titled with its package ID", () => {
    expect(
      v
        .chips(report(), [])
        .map((c: { label: string; total?: string; active: boolean; title?: string }) => [
          c.label,
          c.total,
          c.active,
          c.title,
        ]),
    ).toEqual([
      ["All", undefined, true, undefined],
      ["Fixture", "1.5K", false, "Fixture · pluxy/usage-fixture"],
    ]);
  });

  test("hour cannot be chosen for All", () => {
    const g = v.granularities("all");
    expect(g.map((x: { label: string; disabled: boolean }) => [x.label, x.disabled])).toEqual([
      ["Hour", true],
      ["Day", false],
      ["Week", false],
      ["Month", false],
    ]);
    expect(v.granularities("7d")[0].disabled).toBe(false);
  });

  test("status lines", () => {
    const running = {
      running: true,
      sources: [{ label: "Fixture", phase: "reading", changed: 2, read: 1, failed: 0 }],
    };
    expect(v.statusLine(running)).toBe("Syncing Fixture (1/2)");
    expect(v.statusLine({ running: true, sources: [] })).toBe("Syncing…");
    expect(v.statusLine({ running: false, last_sync: null })).toBe("Not synced yet");
    expect(v.statusLine({ running: false, last_sync: 1794317400 }).startsWith("Last synced ")).toBe(true);
    const nothing = report({ totals: Z });
    expect([v.emptyText(report()), v.emptyText(nothing), v.emptyText({ ...nothing, sync: running })]).toEqual(
      [null, "No local Agent usage yet", "Building the history database; data will appear as it is read"],
    );
  });

  test("the zone's offsets change where its daylight saving does", () => {
    const before = process.env.TZ;
    process.env.TZ = "Europe/Berlin";
    try {
      // 2026-01-01 to 2026-12-31 UTC: +01:00, +02:00 from 03-29 01:00 UTC, +01:00 from 10-25 01:00 UTC.
      expect(v.utcOffsets(1767225600000, 1798675200000)).toEqual([
        [1767225600, 60],
        [1774746000, 120],
        [1792890000, 60],
      ]);
    } finally {
      process.env.TZ = before;
    }
  });
});
