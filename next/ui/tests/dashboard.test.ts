import { describe, expect, test } from "bun:test";
import type { UsageBucket } from "../kit/api";
import { usageQuery, verdictOf, windowLabel } from "../kit/pages/status";
import { axisMax, bucketRows, successPercent } from "../kit/trafficChart";

describe("the verdict", () => {
  test("no reply yet is waiting, no requests is quiet", () => {
    expect(verdictOf(undefined)).toBe("offline");
    expect(verdictOf({ success: 0, total: 0 })).toBe("idle");
  });

  test("95% and above is steady, 80% to below 95% is minor, below 80% needs attention", () => {
    expect(verdictOf({ success: 19, total: 20 })).toBe("good");
    expect(verdictOf({ success: 100, total: 100 })).toBe("good");
    expect(verdictOf({ success: 94, total: 100 })).toBe("warning");
    expect(verdictOf({ success: 4, total: 5 })).toBe("warning");
    expect(verdictOf({ success: 79, total: 100 })).toBe("critical");
    expect(verdictOf({ success: 3, total: 4 })).toBe("critical");
    expect(verdictOf({ success: 0, total: 3 })).toBe("critical");
  });
});

describe("success percent", () => {
  test("is exact, one decimal at most, and is 100% only when nothing failed", () => {
    expect(successPercent(3, 4)).toBe("75%");
    expect(successPercent(1, 3)).toBe("33.3%");
    expect(successPercent(2, 3)).toBe("66.6%");
    expect(successPercent(999, 1000)).toBe("99.9%");
    expect(successPercent(9999, 10000)).toBe("99.9%");
    expect(successPercent(7, 7)).toBe("100%");
    expect(successPercent(0, 5)).toBe("0%");
  });

  test("no requests has no percent", () => {
    expect(successPercent(0, 0)).toBeUndefined();
  });
});

describe("the window", () => {
  // A reply to the usage call as core gives it: one step per 10 minutes, oldest first, quiet steps
  // included. Each pair is [requests, failure_count]; `drawn` is the same steps as [success, failure].
  const asked: [number, number][] = [
    [0, 0],
    [0, 0],
    [3, 0],
    [12, 1],
    [7, 7],
    [0, 0],
    [1, 0],
    [25, 4],
    [40, 0],
    [9, 2],
    [0, 0],
    [0, 0],
    [2, 1],
    [18, 0],
    [33, 3],
    [5, 5],
    [0, 0],
    [14, 2],
    [6, 0],
    [21, 1],
  ];
  const drawn: [number, number][] = [
    [0, 0],
    [0, 0],
    [3, 0],
    [11, 1],
    [0, 7],
    [0, 0],
    [1, 0],
    [21, 4],
    [40, 0],
    [7, 2],
    [0, 0],
    [0, 0],
    [1, 1],
    [18, 0],
    [30, 3],
    [0, 5],
    [0, 0],
    [12, 2],
    [6, 0],
    [20, 1],
  ];
  const at = (i: number) => new Date(Date.UTC(2026, 10, 11, 7, 10 * i)).toISOString();

  test("the chart gets as many rows as the series core answers the query with, with the right values", () => {
    const series: UsageBucket[] = asked.map(([requests, failures], i) => ({
      start: at(i),
      requests,
      failure_count: failures,
      total_tokens: 100 * i,
    }));
    const rows = bucketRows(series);
    expect(rows).toHaveLength(20);
    expect(rows.map((r) => [r.success, r.failure])).toEqual(drawn);
    expect(rows.map((r) => r.total)).toEqual(asked.map(([requests]) => requests));
    expect(rows.map((r) => r.start)).toEqual(series.map((b) => b.start));
    // And the query asks core for exactly that many.
    const query = new URLSearchParams(usageQuery());
    expect(Number(query.get("buckets"))).toBe(rows.length);
    expect(query.get("bucket")).toBe("10m");
    expect(query.get("since")).toBe("200m");
  });

  test("is named in hours and minutes", () => {
    expect(windowLabel(200)).toBe("3h 20m");
    expect(windowLabel(45)).toBe("45m");
    expect(windowLabel(120)).toBe("2h");
  });
});

describe("buckets", () => {
  const series: UsageBucket[] = [
    { start: "2026-11-11T10:00:00Z", requests: 5, failure_count: 2, total_tokens: 0 },
    { start: "2026-11-11T10:10:00Z", requests: 0, failure_count: 0, total_tokens: 0 },
    { start: "2026-11-11T10:20:00Z", requests: 4, failure_count: 1, total_tokens: 90 },
  ];

  test("split each step into success and failure and keep core's order", () => {
    expect(bucketRows(series)).toEqual([
      { start: "2026-11-11T10:00:00Z", success: 3, failure: 2, total: 5 },
      { start: "2026-11-11T10:10:00Z", success: 0, failure: 0, total: 0 },
      { start: "2026-11-11T10:20:00Z", success: 3, failure: 1, total: 4 },
    ]);
  });

  test("the chart's top is a whole number of equal steps above the peak", () => {
    expect(axisMax(0, 4)).toBe(4);
    expect(axisMax(4, 4)).toBe(4);
    expect(axisMax(5, 4)).toBe(8);
    expect(axisMax(112, 4)).toBe(120);
    expect(axisMax(3, 2)).toBe(4);
  });
});
