// What the Accounts list works out before it draws (kit/pages/accounts.helpers.ts): the groups and
// their order, the counts line, the search, the batch's targets, and an account's facts.
import { expect, test } from "bun:test";
import type { Account } from "../kit/api";
import {
  batchTargets,
  countsLine,
  groupAccounts,
  matchesSearch,
  nameProblem,
  orderWord,
  providerCounts,
  signsInWith,
  stateCounts,
} from "../kit/pages/accounts.helpers";
import { accountCondition } from "../kit/parts";

const NOW = Date.parse("2026-11-27T12:00:00Z");
const at = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString();

const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});

const names = (rows: { account: Account }[]) => rows.map((r) => r.account.label ?? r.account.id);

test("accounts fall in four groups; those that need you or wait are ordered by soonest back, the rest by name", () => {
  const list = [
    account("zeta"),
    account("alpha", { label: "Work" }),
    account("cool-late", { cooldowns: [{ scope: "account", kind: "rate_limit", until: at(50) }] }),
    account("cool-soon", { cooldowns: [{ scope: "account", kind: "rate_limit", until: at(5) }] }),
    account("closed", { health: { state: "banned" } }),
    account("signed-out", { state: "reauth_required" }),
    account("off-b", { disabled: true }),
    account("off-a", { disabled: true }),
  ];
  const g = groupAccounts(list, undefined, NOW);
  expect(names(g.needs)).toEqual(["closed", "signed-out"]);
  expect(names(g.waiting)).toEqual(["cool-soon", "cool-late"]);
  // By the name a person sees: the label "Work", not the ID "alpha".
  expect(names(g.serving)).toEqual(["Work", "zeta"]);
  expect(names(g.off)).toEqual(["off-a", "off-b"]);
  // The same list read again in another order groups the same way: rows do not jump.
  expect(groupAccounts([...list].reverse(), undefined, NOW)).toEqual(g);
});

test("the counts line leaves out every zero count", () => {
  expect(
    countsLine(
      [
        account("a"),
        account("b", { extension: "pluxy/gemini" }),
        account("c", { state: "reauth_required" }),
        account("d", { disabled: true }),
      ],
      undefined,
      NOW,
    ),
  ).toBe("4 accounts at 2 providers · 1 needs you · 2 serving · 1 turned off");
  expect(countsLine([account("a")], undefined, NOW)).toBe("1 account at 1 provider · 1 serving");
  expect(
    countsLine(
      [
        account("a", { state: "reauth_required" }),
        account("b", { health: { state: "banned" } }),
        account("c", { cooldowns: [{ scope: "account", kind: "rate_limit", until: at(5) }] }),
      ],
      undefined,
      NOW,
    ),
  ).toBe("3 accounts at 1 provider · 2 need you · 1 waiting");
  expect(
    countsLine([account("a"), account("b", { state: "reauth_required" })], undefined, NOW, false, "Claude"),
  ).toBe("2 Claude accounts · 1 needs you · 1 serving");
  expect(countsLine([account("a")], undefined, NOW, false, "Gemini")).toBe("1 Gemini account · 1 serving");
});

test("Find an account matches the public ID, the provider's name, the name and the condition word", () => {
  const a = account("work-01", {
    public_id: "acc_1a2b3c4d",
    label: "me@example.com",
    extension: "pluxy/codex",
  });
  const c = accountCondition(a, undefined, NOW);
  expect(matchesSearch(a, c, "acc_1A2B")).toBe(true);
  expect(matchesSearch(a, c, "codex")).toBe(true);
  expect(matchesSearch(a, c, "EXAMPLE")).toBe(true);
  expect(matchesSearch(a, c, "work-01")).toBe(true);
  expect(matchesSearch(a, c, "serving")).toBe(true);
  expect(matchesSearch(a, c, "gemini")).toBe(false);
  expect(matchesSearch(a, c, "   ")).toBe(true);
  const failing = account("x", {
    last_error: { code: "auth", message: "token revoked" },
    state: "reauth_required",
  });
  expect(matchesSearch(failing, accountCondition(failing, undefined, NOW), "revoked")).toBe(true);
  expect(matchesSearch(failing, accountCondition(failing, undefined, NOW), "needs sign-in")).toBe(true);
});

test("Turn off and Remove leave out accounts set in the settings file; Turn on and Clear cooldown reach every one", () => {
  const selected = [account("login-1"), account("config-1", { source: "config" }), account("login-2")];
  const ids = (list: Account[]) => list.map((a) => a.id);
  expect(ids(batchTargets(selected, "turnOff"))).toEqual(["login-1", "login-2"]);
  expect(ids(batchTargets(selected, "remove"))).toEqual(["login-1", "login-2"]);
  expect(ids(batchTargets(selected, "turnOn"))).toEqual(["login-1", "config-1", "login-2"]);
  expect(ids(batchTargets(selected, "clearCooldown"))).toEqual(["login-1", "config-1", "login-2"]);
});

test("an account's place in its provider's order, and how it signs in", () => {
  const all = [
    account("a", { priority: 10 }),
    account("b", { priority: 10 }),
    account("c", { priority: 5 }),
    account("d", { priority: 0 }),
    account("other", { extension: "pluxy/gemini", priority: 100 }),
  ];
  const find = (id: string) => all.find((a) => a.id === id) as Account;
  expect(orderWord(find("a"), all)).toBe("First among Claude accounts");
  expect(orderWord(find("b"), all)).toBe("First among Claude accounts");
  expect(orderWord(find("c"), all)).toBe("Backup 1");
  expect(orderWord(find("d"), all)).toBe("Backup 2");
  expect(orderWord(find("other"), all)).toBe("First among Gemini accounts");

  expect(signsInWith(account("x", { kind: "oauth" }))).toBe("a sign-in with Claude");
  expect(signsInWith(account("x", { kind: "api_key" }))).toBe("an API key");
  expect(signsInWith(account("x", { kind: "api_key", source: "config" }))).toBe(
    "an API key, set in pluxy's settings file",
  );
  expect(signsInWith(account("x", { kind: "cookie" }))).toBe("cookie");
});

test("a name over 64 characters, trimmed, is refused", () => {
  expect(nameProblem("a".repeat(64))).toBeUndefined();
  expect(nameProblem(`  ${"a".repeat(64)}  `)).toBeUndefined();
  expect(nameProblem("a".repeat(65))).toBe("Use at most 64 characters.");
  expect(nameProblem("")).toBeUndefined();
});

test("a used-up limit on one model leaves the account serving; one on the whole account waits", () => {
  // Core's health reads exhausted for a block on one model too; the cooldowns it lists say which.
  const oneModel = account("a", {
    health: { state: "exhausted", until: at(30) },
    cooldowns: [{ scope: "model", model: "m", kind: "quota", until: at(30) }],
  });
  const c = accountCondition(oneModel, undefined, NOW);
  expect(c.kind).toBe("serving");
  expect(c.resting.map((r) => r.model)).toEqual(["m"]);
  const whole = account("b", {
    health: { state: "exhausted", until: at(30) },
    cooldowns: [{ scope: "account", kind: "quota", until: at(30) }],
  });
  expect(accountCondition(whole, undefined, NOW).kind).toBe("exhausted");
  // A core that does not list its cooldowns is taken at its word.
  expect(accountCondition(account("c", { health: { state: "exhausted" } }), undefined, NOW).kind).toBe(
    "exhausted",
  );
});

test("providerCounts preserves first-seen provider order, names each, and skips accounts with no provider", () => {
  const list = [
    account("g1", { extension: "pluxy/gemini" }),
    account("c1", { extension: "pluxy/claude" }),
    account("broken", { extension: undefined }),
    account("g2", { extension: "pluxy/gemini" }),
    account("x1", { extension: "pluxy/xai" }),
  ];
  expect(providerCounts(list)).toEqual([
    { extension: "pluxy/gemini", name: "Gemini", count: 2 },
    { extension: "pluxy/claude", name: "Claude", count: 1 },
    { extension: "pluxy/xai", name: "xAI", count: 1 },
  ]);
});

test("stateCounts places accounts into needs, waiting, serving, off, and unchecked", () => {
  const list = [
    account("ok"),
    account("wait", { cooldowns: [{ scope: "account", kind: "rate_limit", until: at(10) }] }),
    account("need", { state: "reauth_required" }),
    account("off", { disabled: true }),
  ];
  expect(stateCounts(list, undefined, NOW, false)).toEqual({
    needs: 1,
    waiting: 1,
    serving: 1,
    off: 1,
    unchecked: 0,
  });
  expect(stateCounts(list, undefined, NOW, true)).toEqual({
    needs: 1,
    waiting: 0,
    serving: 0,
    off: 1,
    unchecked: 2,
  });
});
