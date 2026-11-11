import { expect, test } from "bun:test";
import type { Account } from "../kit/api/accounts";
import { groupTiers, movePriority } from "../kit/pages/routing";

const account = (id: string, priority?: number): Account => ({
  id, priority, source: "login", state: "active", refreshable: false,
});
test("tiers group equal priorities highest first, including default zero", () => {
  const items = [account("a", -4), account("b", 8), account("c"), account("d", 8)];
  expect(groupTiers(items)).toEqual([
    { priority: 8, accounts: [items[1], items[3]] },
    { priority: 0, accounts: [items[2]] },
    { priority: -4, accounts: [items[0]] },
  ]);
  expect(items.map((a) => a.id)).toEqual(["a", "b", "c", "d"]);
  expect(groupTiers([])).toEqual([]);
});
test("moves join adjacent tiers or create one beyond the edge without renumbering", () => {
  const tiers = groupTiers([account("a", -4), account("b", 8), account("c")]);
  expect(movePriority(tiers, 0, "up")).toBe(8);
  expect(movePriority(tiers, 0, "down")).toBe(-4);
  expect(movePriority(tiers, 8, "up")).toBe(9);
  expect(movePriority(tiers, 8, "down")).toBe(0);
  expect(movePriority(tiers, -4, "up")).toBe(0);
  expect(movePriority(tiers, -4, "down")).toBe(-5);
  const single = groupTiers([account("only")]);
  expect(movePriority(single, 0, "up")).toBe(1);
  expect(movePriority(single, 0, "down")).toBe(-1);
});
