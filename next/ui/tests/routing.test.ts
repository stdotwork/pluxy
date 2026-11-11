import { expect, test } from "bun:test";
import type { Account } from "../kit/api/accounts";
import { canMove, groupTiers, movePriority } from "../kit/pages/routing";

const account = (id: string, priority?: number): Account => ({
  id,
  priority,
  source: "login",
  state: "active",
  refreshable: false,
});
test("tiers group equal priorities highest first, including default zero", () => {
  const items = [account("a", -4), account("b", 8), account("c"), account("d", 8)];
  expect(groupTiers(items)).toEqual([
    { priority: 8, accounts: [account("b", 8), account("d", 8)] },
    { priority: 0, accounts: [account("c")] },
    { priority: -4, accounts: [account("a", -4)] },
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
test("a sole member of the top or bottom tier cannot move further out", () => {
  const tiers = groupTiers([account("a", 8), account("b", 0), account("c", 0), account("d", -4)]);
  expect(canMove(tiers, 8, "up")).toBe(false);
  expect(canMove(tiers, 8, "down")).toBe(true);
  expect(canMove(tiers, 0, "up")).toBe(true);
  expect(canMove(tiers, 0, "down")).toBe(true);
  expect(canMove(tiers, -4, "up")).toBe(true);
  expect(canMove(tiers, -4, "down")).toBe(false);
  const shared = groupTiers([account("a", 8), account("b", 8)]);
  expect(canMove(shared, 8, "up")).toBe(true);
  expect(canMove(shared, 8, "down")).toBe(true);
});
