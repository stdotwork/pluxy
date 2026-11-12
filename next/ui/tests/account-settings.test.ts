import { expect, test } from "bun:test";
import type { Account } from "../kit/api";
import { accountChange, draftOf } from "../kit/pages/accountSettings.helpers";

// An account as core's reply reads after the browser parsed it.
const account = (settingsJSON: string, inexact?: boolean): Account => ({
  id: "a",
  source: "login",
  state: "active",
  refreshable: false,
  settings: JSON.parse(settingsJSON),
  ...(inexact ? { settings_inexact: true } : {}),
});

test("a number the browser cannot keep exactly is refused, not changed", () => {
  const a = account(`{"base_url":"https://one.example"}`);
  for (const n of ["9007199254740993", "1e-400", "1e400", "0.10000000000000001"]) {
    const d = { ...draftOf(a), settings: `{"base_url":"https://one.example","n":${n}}` };
    expect(accountChange(a, d)).toEqual({ invalid: "settingsNumber" });
  }
  for (const [n, kept] of [
    ["1.0", 1],
    ["0.1", 0.1],
    ["-0", -0],
    ["2e3", 2000],
    ["9007199254740991", 9007199254740991],
  ] as const) {
    const d = { ...draftOf(a), settings: `{"base_url":"https://one.example","n":${n}}` };
    expect(accountChange(a, d)).toEqual({
      change: { settings: { base_url: "https://one.example", n: kept } },
    });
  }
  // Digits inside a string are text, not a number.
  const d = { ...draftOf(a), settings: `{"base_url":"https://one.example","s":"9007199254740993"}` };
  expect(accountChange(a, d)).toEqual({
    change: { settings: { base_url: "https://one.example", s: "9007199254740993" } },
  });
});

test("settings holding a number the browser did not keep cannot be edited here", () => {
  // Core marks them: the browser shows these numbers rounded, and would write them back so.
  for (const stored of [
    `{"big":9007199254740993}`,
    `{"huge":1e400}`,
    `{"n":0.10000000000000001}`,
    `{"tiny":1e-400}`,
  ]) {
    const a = account(stored, true);
    // Another field: settings are not sent, so the stored number stays as it is.
    expect(accountChange(a, { ...draftOf(a), priority: "5" })).toEqual({ change: { priority: 5 } });
    // The settings text: refused, since the number shown is not the one core holds.
    const edited = { ...draftOf(a), settings: draftOf(a).settings.replace("{", `{"websockets": true,`) };
    expect(accountChange(a, edited)).toEqual({ invalid: "settingsNumber" });
  }
  // Unmarked settings can be edited, a large integer the browser keeps exactly included.
  const a = account(`{"big":9007199254740994}`);
  const edited = { ...draftOf(a), settings: draftOf(a).settings.replace("{", `{"websockets": true,`) };
  expect(accountChange(a, edited)).toEqual({
    change: { settings: { websockets: true, big: 9007199254740994 } },
  });
});
