import { expect, test } from "bun:test";
import type { Account } from "../kit/api/accounts";
import { filterAccounts, hasProblem } from "../kit/pages/accounts";

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
  account("cool", { cooldowns: [{ until: "2026-01-01T00:01:00Z" }] }),
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
