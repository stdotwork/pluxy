// Direction A's rules: how an account stands, how a usage window is paced, what path a request
// took, the compact figures, and the keys. The expected values are worked out by hand here.
import { expect, test } from "bun:test";
import type { Account, UsageEvent } from "../kit/api";
import { type Command, rankCommands } from "../kit/designs/a/command";
import { type KeyBinding, KeyRegistry } from "../kit/designs/a/keys";
import {
  compact,
  countBoard,
  fellBack,
  latency,
  matchScore,
  paceOf,
  pathOf,
  percent,
  rate,
  standingOf,
  verdict,
  wait,
  waitCoarse,
  windowName,
} from "../kit/designs/a/lib";
import { parsePlace, placeAddress } from "../kit/designs/a/requests";

const NOW = Date.parse("2026-11-24T03:00:00Z");
const at = (seconds: number) => new Date(NOW + seconds * 1000).toISOString();
const account = (extra: Partial<Account> = {}): Account => ({
  id: "a1",
  source: "login",
  extension: "test/alpha",
  state: "active",
  refreshable: false,
  health: { state: "ok" },
  ...extra,
});

test("figures stay short and never round up past what happened", () => {
  expect(compact(1284)).toBe("1,284");
  expect(compact(12_449)).toBe("12.4k");
  expect(compact(99_960)).toBe("99.9k");
  expect(compact(4_219_000)).toBe("4.21M");
  expect(compact(1_200_000_000)).toBe("1.20B");
  expect(percent(0.9996)).toBe("99.9%");
  expect(percent(1)).toBe("100%");
  expect(percent(0.5)).toBe("50.0%");
  expect(percent(0)).toBe("0%");
  expect(latency(812)).toBe("812ms");
  expect(latency(1840)).toBe("1.8s");
  expect(latency(64_000)).toBe("1m 04s");
  expect(rate(4757, 86_400)).toBe("3.3/min");
  expect(rate(145, 86_400)).toBe("6.0/h");
  expect(rate(3, 604_800)).toBe("0.4/day");
  expect(rate(0, 3600)).toBe("0/min");
});

test("a wait shows its two largest units and never reads zero while it lasts", () => {
  expect(wait(500)).toBe("1s");
  expect(wait(42_000)).toBe("42s");
  expect(wait(252_000)).toBe("4m 12s");
  expect(wait(8_040_000)).toBe("2h 14m");
  expect(wait((3 * 86_400 + 4 * 3600) * 1000)).toBe("3d 4h");
  expect(waitCoarse(41 * 60_000)).toBe("41m");
  expect(waitCoarse(8_040_000)).toBe("2h 14m");
  expect(waitCoarse(100 * 86_400_000)).toBe("99d+");
});

test("an account serves, waits on a clock, needs the person or is off", () => {
  expect(standingOf(account(), NOW)).toMatchObject({ state: "serving", tone: "ok" });
  expect(standingOf(account({ disabled: true, health: { state: "banned" } }), NOW)).toMatchObject({
    state: "off",
    fix: "enable",
  });
  expect(
    standingOf(
      account({ health: { state: "exhausted", until: at(3600), why: "usage window used up" } }),
      NOW,
    ),
  ).toMatchObject({ state: "exhausted", tone: "warn", until: NOW + 3_600_000 });
  expect(
    standingOf(
      account({ state: "reauth_required", reason: "token revoked", health: { state: "needs_login" } }),
      NOW,
    ),
  ).toMatchObject({ state: "sign-in", tone: "danger", fix: "sign-in", why: "token revoked" });
  expect(standingOf(account({ health: { state: "banned" } }), NOW)).toMatchObject({
    state: "banned",
    fix: "enable",
  });
  expect(standingOf(account({ health: { state: "broken", why: "x.json: bad" } }), NOW)).toMatchObject({
    state: "broken",
    fix: "remove",
  });
  // Its extension is not among those core runs.
  expect(standingOf(account(), NOW, new Set(["test/local"]))).toMatchObject({
    state: "no-provider",
    fix: "extensions",
  });
  expect(standingOf(account(), NOW, new Set(["test/alpha"]))).toMatchObject({ state: "serving" });
});

test("an account with nothing wrong offers no fix", () => {
  expect(standingOf(account(), NOW).fix).toBeUndefined();
  expect(standingOf(account({ health: { state: "exhausted", until: at(60) } }), NOW).fix).toBeUndefined();
  expect(
    standingOf(account({ cooldowns: [{ scope: "credential", kind: "cooldown", until: at(-5) }] }), NOW).fix,
  ).toBeUndefined();
});

test("a cooldown of the whole account holds it back; a model's cooldown leaves it serving", () => {
  const whole = account({
    health: { state: "cooling", until: at(252), why: "3 server errors in a row" },
    cooldown_until: at(252),
    cooldowns: [{ scope: "credential", kind: "cooldown", until: at(252) }],
  });
  expect(standingOf(whole, NOW)).toMatchObject({
    state: "cooling",
    tone: "warn",
    until: NOW + 252_000,
    fix: "clear-cooldown",
    coolingModels: 0,
  });
  // Core calls the account "cooling" here too, though only one model is held back.
  const oneModel = account({
    health: { state: "cooling", until: at(300), why: "model m: rate limited" },
    cooldowns: [
      {
        scope: "model",
        model: "m",
        kind: "rate_limit",
        until: at(300),
        error: { code: "rate_limit", message: "slow down" },
      },
    ],
  });
  expect(standingOf(oneModel, NOW)).toMatchObject({
    state: "serving",
    tone: "ok",
    coolingModels: 1,
    modelsUntil: NOW + 300_000,
    why: "slow down",
    fix: "clear-cooldown",
  });
  // A cooldown that is over holds nothing.
  const over = account({ cooldowns: [{ scope: "credential", kind: "cooldown", until: at(-5) }] });
  expect(standingOf(over, NOW)).toMatchObject({ state: "serving", coolingModels: 0 });
});

test("the board's counts split accounts into serving, waiting, needing the person and off", () => {
  const standings = [
    account(),
    account({ disabled: true }),
    account({ health: { state: "exhausted" } }),
    account({ cooldown_until: at(60) }),
    account({ health: { state: "banned" } }),
    account({ health: { state: "broken" } }),
  ].map((a) => standingOf(a, NOW));
  expect(countBoard(standings)).toEqual({ total: 6, serving: 1, waiting: 2, needYou: 2, off: 1 });
});

test("a usage window is paced against the clock", () => {
  const window = (used_percent: number, resetsIn: number) => ({
    id: "five_hour",
    used_percent,
    window_seconds: 18_000,
    resets_at: NOW / 1000 + resetsIn,
  });
  // Half the window's time has passed.
  expect(paceOf(window(40, 9000), NOW)).toMatchObject({
    used: 0.4,
    elapsed: 0.5,
    course: "steady",
    resetsIn: 9_000_000,
  });
  expect(paceOf(window(80, 9000), NOW)).toMatchObject({ course: "ahead" });
  // Ahead of the clock, but early and low: not worth a warning yet.
  expect(paceOf(window(30, 16_200), NOW)).toMatchObject({ course: "steady" });
  expect(paceOf(window(100, 3600), NOW)).toMatchObject({ used: 1, course: "full" });
  // A reset further away than the window is long says nothing of the clock.
  expect(paceOf(window(42, 200_000_000), NOW)).toMatchObject({
    used: 0.42,
    elapsed: undefined,
    course: "steady",
  });
  expect(paceOf({ id: "x", used: 30, limit: 120 }, NOW)).toMatchObject({ used: 0.25, elapsed: undefined });
  expect(paceOf({ id: "x", remaining: 3, unit: "resets" }, NOW)).toBeUndefined();
  expect(paceOf({ id: "x", used_percent: 50, unlimited: true }, NOW)).toBeUndefined();
  expect(windowName({ id: "five_hour", window_seconds: 18_000 })).toBe("5h");
  expect(windowName({ id: "weekly", window_seconds: 604_800 })).toBe("wk");
  expect(windowName({ id: "daily", window_seconds: 86_400 })).toBe("1d");
  expect(windowName({ id: "m", label: "Monthly limit" })).toBe("Monthly");
});

const event = (extra: Partial<UsageEvent> = {}): UsageEvent => ({
  seq: 1,
  timestamp: at(0),
  model: "m",
  provider: "test/alpha",
  account: "a1",
  failed: false,
  status: 200,
  latency_ms: 10,
  tokens: {},
  ...extra,
});

test("a request's path is every try in order, and one try when core recorded none", () => {
  expect(pathOf(event())).toEqual([
    { account: "a1", provider: "test/alpha", model: undefined, result: "ok", ok: true },
  ]);
  const fell = event({
    attempts: 2,
    tried: [
      { account: "acc_1", provider: "test/alpha", model: "m", result: "quota" },
      { account: "acc_2", provider: "test/alpha", model: "m", result: "ok" },
    ],
  });
  expect(pathOf(fell).map((h) => [h.account, h.ok])).toEqual([
    ["acc_1", false],
    ["acc_2", true],
  ]);
  expect(fellBack(fell)).toBe(true);
  expect(fellBack(event())).toBe(false);
  expect(fellBack(event({ chain_step: 1 }))).toBe(false);
  // No try was recorded, but it was served at the chain's second step.
  expect(fellBack(event({ chain_step: 2 }))).toBe(true);
  expect(pathOf(event({ account: undefined, failed: true, status: 404 }))).toEqual([]);
  expect(verdict(event())).toBe("Served by a1 at the first try.");
  expect(verdict(fell, (a) => a.toUpperCase())).toBe("Fell back once: ACC_1 (quota). Served by ACC_2.");
  const failed = event({
    failed: true,
    status: 429,
    tried: [
      { account: "acc_1", provider: "p", result: "rate_limit" },
      { account: "acc_2", provider: "p", result: "rate_limit" },
    ],
  });
  expect(verdict(failed)).toBe("Failed after 2 tries: acc_1 (rate limit), then acc_2 (rate limit).");
  expect(verdict(event({ account: undefined, failed: true }))).toBe("No account could take this request.");
});

test("finding matches every typed word, and a match at a word's start ranks higher", () => {
  expect(matchScore("team-seat-4 test/alpha serving", "seat")).toBeGreaterThan(0);
  expect(matchScore("team-seat-4 test/alpha serving", "seat alpha")).toBeGreaterThan(0);
  expect(matchScore("team-seat-4", "seat gamma")).toBe(0);
  expect(matchScore("Seat", "seat")).toBeGreaterThan(matchScore("backseat", "seat"));
  expect(matchScore("anything", "  ")).toBeGreaterThan(0);
  const commands: Command[] = [
    { id: "1", group: "Go to", label: "Settings", run() {} },
    { id: "2", group: "Accounts", label: "team-seat-3", detail: "test/alpha · serving", run() {} },
    {
      id: "3",
      group: "Accounts",
      label: "spare",
      detail: "test/alpha · disabled",
      words: "seat9@team.example",
      run() {},
    },
    { id: "4", group: "Actions", label: "Refresh", keys: "r", run() {} },
  ];
  // The label match comes before the one found only in the hidden words.
  expect(rankCommands(commands, "seat").map((c) => c.id)).toEqual(["2", "3"]);
  expect(rankCommands(commands, "serving").map((c) => c.id)).toEqual(["2"]);
  expect(rankCommands(commands, "").map((c) => c.id)).toEqual(["1", "2", "3", "4"]);
  expect(rankCommands(commands, "zzz")).toEqual([]);
});

test("the Requests address holds the view, the open request and the filters", () => {
  expect(parsePlace("")).toEqual({ view: "totals", seq: undefined, filters: {} });
  expect(parsePlace("list/42?model=x&failed=true&nonsense=1")).toEqual({
    view: "list",
    seq: 42,
    filters: { model: "x", failed: "true" },
  });
  expect(parsePlace("list/abc")).toMatchObject({ view: "list", seq: undefined });
  expect(parsePlace("?account=a%2Fb")).toMatchObject({ view: "totals", filters: { account: "a/b" } });
  expect(placeAddress({ view: "list", seq: 42, filters: { model: "x", failed: "true" } })).toBe(
    "#/usage/list/42?model=x&failed=true",
  );
  expect(placeAddress({ view: "totals", filters: {} })).toBe("#/usage");
  expect(placeAddress({ view: "totals", seq: 7, filters: { key: "k 1" } })).toBe("#/usage?key=k+1");
});

const press = (key: string, extra: Partial<KeyboardEvent> = {}) =>
  ({
    key,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    isComposing: false,
    target: null,
    ...extra,
  }) as KeyboardEvent;

test("keys: a screen's key wins over the shell's, two keys in a row work, typing is left alone", () => {
  const ran: string[] = [];
  const binding = (keys: string, label: string): KeyBinding => ({ keys, label, run: () => ran.push(label) });
  const registry = new KeyRegistry();
  const shell = Symbol("shell");
  const screen = Symbol("screen");
  let heard = 0;
  registry.subscribe(() => heard++);
  registry.set(shell, [binding("g a", "go accounts"), binding("r", "shell r")], true);
  registry.set(screen, [binding("r", "screen r"), binding("j", "next")]);
  expect(heard).toBe(2);
  // The same keys and labels again: what they do is replaced, nobody is told.
  registry.set(screen, [binding("r", "screen r"), binding("j", "next")]);
  expect(heard).toBe(2);

  expect(registry.handle(press("r"))).toBe(true);
  expect(registry.handle(press("R"))).toBe(true);
  expect(ran).toEqual(["screen r", "screen r"]);
  expect(registry.handle(press("g"), 1000)).toBe(true);
  expect(registry.handle(press("a"), 1500)).toBe(true);
  expect(ran.at(-1)).toBe("go accounts");
  // Too long after g, a is only a.
  expect(registry.handle(press("g"), 5000)).toBe(true);
  expect(registry.handle(press("a"), 9000)).toBe(false);
  expect(registry.handle(press("x"))).toBe(false);
  expect(registry.handle(press("r", { ctrlKey: true }))).toBe(false);
  const field = { tagName: "INPUT", type: "text", isContentEditable: false } as unknown as EventTarget;
  expect(registry.handle(press("j", { target: field }))).toBe(false);
  const box = { tagName: "INPUT", type: "checkbox", isContentEditable: false } as unknown as EventTarget;
  expect(registry.handle(press("j", { target: box }))).toBe(true);
  expect(ran.at(-1)).toBe("next");

  registry.remove(screen);
  expect(heard).toBe(3);
  registry.handle(press("r"));
  expect(ran.at(-1)).toBe("shell r");
  ran.length = 0;
  registry.run("g a");
  expect(ran).toEqual(["go accounts"]);
});
