import { expect, test } from "bun:test";
import { type Account, countAccounts, leftOut, runningExtensions, standing } from "../kit/api/accounts";
import { batchTargets, filterAccounts, hasProblem } from "../kit/pages/accounts";

const now = Date.parse("2026-01-01T00:00:00Z");
const account = (id: string, fields: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  ...fields,
});
const list = [
  account("work-01", { label: "Alice", extension: "test/alpha", kind: "api_key" }),
  account("work-02", { disabled: true, source: "config", priority: 10 }),
  account("login", { state: "reauth_required" }),
  account("check", { check: { state: "failed", error: { code: "invalid", message: "Invalid key" } } }),
  account("error", { last_error: { code: "rate_limit", message: "429 too many requests" } }),
  account("cool", { cooldowns: [{ scope: "account", kind: "rate_limit", until: "2026-01-01T00:01:00Z" }] }),
  account("expired", { cooldown_until: "2025-12-31T23:59:00Z" }),
];

test("account search treats * as wildcard and other punctuation literally", () => {
  expect(filterAccounts(list, { search: "WORK*01" }, now).map((a) => a.id)).toEqual(["work-01"]);
  expect(filterAccounts(list, { search: "alice*" }, now).map((a) => a.id)).toEqual(["work-01"]);
  expect(filterAccounts(list, { search: "test/alpha" }, now).map((a) => a.id)).toEqual(["work-01"]);
  expect(filterAccounts(list, { search: "api_key" }, now).map((a) => a.id)).toEqual(["work-01"]);
  expect(filterAccounts(list, { search: "429" }, now).map((a) => a.id)).toEqual(["error"]);
  expect(filterAccounts(list, { search: "invalid" }, now).map((a) => a.id)).toEqual(["check"]);
  expect(filterAccounts(list, { search: "work.01" }, now)).toEqual([]);
});

test("problems include needs-login, failed check, last error and live cooldown", () => {
  expect(list.map((a) => hasProblem(a, now))).toEqual([false, false, true, true, true, true, false]);
  expect(filterAccounts(list, { status: "problem" }, now).map((a) => a.id)).toEqual([
    "login",
    "check",
    "error",
    "cool",
  ]);
  expect(filterAccounts(list, { status: "disabled" }, now).map((a) => a.id)).toEqual(["work-02"]);
  expect(filterAccounts(list, { source: "config", sort: "priority" }, now).map((a) => a.id)).toEqual([
    "work-02",
  ]);
  expect(filterAccounts(list, { extension: "test/alpha" }, now).map((a) => a.id)).toEqual(["work-01"]);
});

test("sort orders and combined filters have exact results", () => {
  const rows = [
    account("z", { priority: 1 }),
    account("a", { priority: 3 }),
    account("m", { priority: 2, disabled: true }),
  ];
  expect(filterAccounts(rows, {}).map((a) => a.id)).toEqual(["z", "a", "m"]);
  expect(filterAccounts(rows, { sort: "name" }).map((a) => a.id)).toEqual(["a", "m", "z"]);
  expect(filterAccounts(rows, { sort: "priority" }).map((a) => a.id)).toEqual(["a", "m", "z"]);
  expect(filterAccounts(rows, { status: "enabled", search: "z" }).map((a) => a.id)).toEqual(["z"]);
});

test("search finds HTTP errors whose message does not repeat their status", () => {
  const rows = [account("denied", { last_error: { code: "auth", message: "Access denied", status: 403 } })];
  expect(filterAccounts(rows, { search: "403" }).map((a) => a.id)).toEqual(["denied"]);
});

test("an account core leaves out of routing is a problem, unless a person turned it off", () => {
  const health = (state: string, fields: Partial<Account> = {}) =>
    account(state, { health: { state }, ...fields });
  const rows = [
    health("ok"),
    health("cooling"),
    health("banned"),
    health("exhausted"),
    health("needs_login"),
    health("broken", { state: "", extension: "" }),
    health("disabled", { disabled: true }),
    account("banned-then-off", { disabled: true, health: { state: "banned" } }),
    account("old-core"),
  ];
  expect(rows.map(leftOut)).toEqual([
    undefined,
    undefined,
    "banned",
    "exhausted",
    "needs_login",
    "broken",
    undefined,
    undefined,
    undefined,
  ]);
  expect(rows.map((a) => hasProblem(a, now))).toEqual([
    false,
    false,
    true,
    true,
    true,
    true,
    false,
    false,
    false,
  ]);
  expect(filterAccounts(rows, { status: "problem" }, now).map((a) => a.id)).toEqual([
    "banned",
    "exhausted",
    "needs_login",
    "broken",
  ]);
});

test("batch Enable and Clear cooldown reach config accounts; Disable and Delete leave them out", () => {
  const selected = [
    account("signed-in"),
    account("from-config", { source: "config", health: { state: "banned" } }),
  ];
  expect(batchTargets(selected, "enable").map((a) => a.id)).toEqual(["signed-in", "from-config"]);
  expect(batchTargets(selected, "cooldown").map((a) => a.id)).toEqual(["signed-in", "from-config"]);
  expect(batchTargets(selected, "disable").map((a) => a.id)).toEqual(["signed-in"]);
  expect(batchTargets(selected, "delete").map((a) => a.id)).toEqual(["signed-in"]);
});

test("one rule counts accounts: cooling and a last error still serve, a missing provider does not", () => {
  const running = runningExtensions({ extensions: [{ id: "test/alpha" }] });
  const rows = [
    account("ok", { extension: "test/alpha" }),
    account("cool", {
      extension: "test/alpha",
      cooldowns: [{ scope: "account", kind: "rate_limit", until: "2026-01-01T00:01:00Z" }],
    }),
    account("err", { extension: "test/alpha", last_error: { code: "rate_limit", message: "429" } }),
    account("banned", { extension: "test/alpha", health: { state: "banned" } }),
    account("login", { extension: "test/alpha", state: "reauth_required" }),
    account("off", { extension: "test/alpha", disabled: true }),
    account("ghost", { extension: "ghost/gone" }),
    account("ghost-off", { extension: "ghost/gone", disabled: true }),
  ];
  expect(rows.map((a) => standing(a, running))).toEqual([
    "active",
    "active",
    "active",
    "unavailable",
    "unavailable",
    "disabled",
    "unavailable",
    "disabled",
  ]);
  expect(countAccounts(rows, running)).toEqual({ active: 3, unavailable: 3, disabled: 2 });
  // Before the running extensions are read, no account is held against its provider.
  expect(standing(rows[6] as Account)).toBe("active");
  expect(hasProblem(rows[6] as Account, now, running)).toBe(true);
});
