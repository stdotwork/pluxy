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
  test("is 20 buckets of 10 minutes, asked for by duration", () => {
    expect(usageQuery()).toBe("since=200m&bucket=10m&buckets=20");
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
