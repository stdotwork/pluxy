import { expect, test } from "bun:test";
import type { VNode } from "preact";
import { renderToString } from "preact-render-to-string";
import {
  compactTime,
  fullTime,
  LONGEST_COUNTDOWN_DAYS,
  parseTime,
  relativeTime,
  shortDuration,
  when,
} from "../kit/time";

// A fixed clock in a fixed zone and locale: Tuesday 24 November 2026, 15:30:00 UTC.
process.env.TZ = "UTC";
const NOW = Date.UTC(2026, 10, 24, 15, 30, 0);
const ago = (seconds: number) => relativeTime(new Date(NOW - seconds * 1000), NOW, "en-GB");
const at = (iso: string) => relativeTime(new Date(iso), NOW, "en-GB");

test("a time from core is seconds as a number or a date as text; a missing one is nothing", () => {
  expect(parseTime(1795534200)?.toISOString()).toBe("2026-11-24T15:30:00.000Z");
  expect(parseTime("2026-11-24T15:30:00Z")?.getTime()).toBe(NOW);
  for (const none of [undefined, 0, "", "not a date"]) expect(parseTime(none)).toBeUndefined();
});

test("a past time reads by its distance: just now, minutes, hours, yesterday, then the date", () => {
  expect(ago(0)).toBe("just now");
  expect(ago(59)).toBe("just now");
  expect(ago(60)).toBe("1m ago");
  expect(ago(5 * 60 + 59)).toBe("5m ago");
  expect(ago(3599)).toBe("59m ago");
  expect(ago(3600)).toBe("1h ago");
  expect(ago(3 * 3600 + 1800)).toBe("3h ago");
  // Midnight today is still hours ago; the second before it is yesterday.
  expect(at("2026-11-24T00:00:00Z")).toBe("15h ago");
  expect(at("2026-11-23T23:59:59Z")).toBe("yesterday 23:59");
  expect(at("2026-11-23T14:02:00Z")).toBe("yesterday 14:02");
  expect(at("2026-11-23T00:00:00Z")).toBe("yesterday 00:00");
  expect(at("2026-11-22T23:59:59Z")).toBe("22 Nov");
  expect(at("2026-01-03T08:00:00Z")).toBe("3 Jan");
  expect(at("2025-12-31T23:00:00Z")).toBe("31 Dec 2025");
});

test("just after midnight, the last hours of yesterday still count as hours ago", () => {
  const night = Date.UTC(2026, 10, 24, 1, 0, 0);
  const then = (iso: string) => relativeTime(new Date(iso), night, "en-GB");
  expect(then("2026-11-23T23:00:00Z")).toBe("2h ago");
  expect(then("2026-11-23T19:00:01Z")).toBe("5h ago");
  expect(then("2026-11-23T19:00:00Z")).toBe("yesterday 19:00");
});

test("a coming time counts down in short units, and shows its date when too far to count", () => {
  expect(ago(-1)).toBe("in 1s");
  expect(ago(-45)).toBe("in 45s");
  expect(ago(-(2 * 3600 + 5 * 60))).toBe("in 2h 5m");
  expect(ago(-(3 * 86400 + 4 * 3600))).toBe("in 3d 4h");
  expect(ago(-LONGEST_COUNTDOWN_DAYS * 86400)).toBe("in 99d");
  expect(ago(-(LONGEST_COUNTDOWN_DAYS * 86400 + 1))).toBe("3 Mar 2027");
  expect(at("2033-05-18T03:33:20Z")).toBe("18 May 2033");
  // A clock a moment ahead of this one is not a countdown.
  expect(ago(-0.5)).toBe("just now");
});

test("a length of time is at most two units, rounded up", () => {
  const cases: [number, string][] = [
    [-5, "0s"],
    [0, "0s"],
    [0.2, "1s"],
    [59, "59s"],
    [60, "1m"],
    [61, "2m"],
    [3540, "59m"],
    [3541, "1h"],
    [3600, "1h"],
    [7500, "2h 5m"],
    [86340, "23h 59m"],
    [86341, "1d"],
    [86400, "1d"],
    [86401, "1d 1h"],
    [273600, "3d 4h"],
    [400 * 86400, "400d"],
  ];
  for (const [seconds, text] of cases) expect([seconds, shortDuration(seconds)]).toEqual([seconds, text]);
});

test("the compact exact time leaves the date out today and the year out this year", () => {
  const exact = (iso: string) => compactTime(new Date(iso), NOW, "en-GB");
  expect(exact("2026-11-24T00:00:00Z")).toBe("00:00:00");
  expect(exact("2026-11-24T22:58:38Z")).toBe("22:58:38");
  expect(exact("2026-11-23T23:59:59Z")).toBe("23 Nov, 23:59:59");
  expect(exact("2026-11-25T00:00:00Z")).toBe("25 Nov, 00:00:00");
  expect(exact("2025-11-24T22:58:38Z")).toBe("24 Nov 2025, 22:58:38");
  expect(compactTime(new Date("2026-11-24T22:58:38Z"), NOW, "en-US")).toBe("10:58:38 PM");
});

test("a page's time is a <time> with the machine's time and the exact local time as its title", () => {
  const d = new Date("2020-02-03T04:05:06Z");
  expect(renderToString(when("2020-02-03T04:05:06Z") as VNode)).toBe(
    `<time datetime="2020-02-03T04:05:06.000Z" title="${fullTime(d)}" data-part="time">` +
      `${relativeTime(d, Date.now())}</time>`,
  );
  expect(fullTime(d, "en-GB")).toBe("03/02/2020, 04:05:06");
  expect(renderToString(when(1580702706, "exact") as VNode)).toContain('datetime="2020-02-03T04:05:06.000Z"');
  for (const none of [undefined, 0, "", "not a date"]) expect(when(none)).toBeUndefined();
});
