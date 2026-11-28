import { expect, test } from "bun:test";
import { api } from "../kit/api";
import type { Account } from "../kit/api/accounts";
import type { Host, Method } from "../kit/host";
import {
  addStep,
  canMove,
  changeStep,
  type DropTarget,
  draftOf,
  draftProblem,
  dropOf,
  groupTiers,
  MAX_STEPS,
  moveOf,
  movePriority,
  moveStep,
  newDraft,
  orderOf,
  providerOfModel,
  removeStep,
  skipsItself,
  stepParts,
  stepsOf,
  stepWords,
  withName,
} from "../kit/pages/routing.helpers";

const account = (
  id: string,
  priority?: number,
  extension = "pluxy/claude",
  source: "login" | "config" = "login",
): Account => ({
  id,
  priority,
  extension,
  source,
  state: "active",
  refreshable: false,
});

test("tiers group equal priorities highest first, including default zero", () => {
  const items = [account("a", -4), account("b", 8), account("c"), account("d", 8)];
  expect(groupTiers(items).map((t) => [t.priority, t.accounts.map((a) => a.id)])).toEqual([
    [8, ["b", "d"]],
    [0, ["c"]],
    [-4, ["a"]],
  ]);
  expect(items.map((a) => a.id)).toEqual(["a", "b", "c", "d"]);
  expect(groupTiers([])).toEqual([]);
});

test("moves join adjacent groups or create one beyond the edge without renumbering", () => {
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

test("a sole member of the first or last group cannot move further out", () => {
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

const two = [
  account("c1", 5),
  account("c2", 0),
  account("c3", 0),
  // The other provider's priorities never show in the Claude moves below.
  account("g1", 3, "pluxy/gemini"),
  account("g2", -3, "pluxy/gemini"),
  account("solo", 7, "pluxy/devin"),
];

test("the order is per provider: a provider's groups come from its own accounts alone", () => {
  const { sections, single } = orderOf(two);
  expect(sections.map((s) => [s.name, s.tiers.map((t) => t.accounts.map((a) => a.id))])).toEqual([
    ["Claude", [["c1"], ["c2", "c3"]]],
    ["Gemini", [["g1"], ["g2"]]],
  ]);
  expect(single).toEqual(["Devin"]);
  expect(orderOf([account("a"), account("b", 1, "pluxy/gemini")]).sections).toEqual([]);
});

test("an arrow sets the priority within the account's provider and ignores another provider's", () => {
  // c2 goes up into c1's group (5), not into Gemini's group at 3; c1 is alone in Claude's first group.
  expect(moveOf(two, "c2", "up")).toBe(5);
  expect(moveOf(two, "c1", "up")).toBeUndefined();
  expect(moveOf(two, "c1", "down")).toBe(0);
  expect(moveOf(two, "c3", "down")).toBe(-1);
  expect(moveOf(two, "g2", "up")).toBe(3);
  expect(moveOf(two, "g1", "down")).toBe(-3);
  expect(moveOf(two, "g2", "down")).toBeUndefined();
  expect(moveOf(two, "nobody", "up")).toBeUndefined();
});

test("an account set in the settings file cannot be moved", () => {
  const list = [account("a", 1), account("file", 0, "pluxy/claude", "config")];
  expect(moveOf(list, "file", "up")).toBeUndefined();
  expect(dropOf(list, "file", { edge: "first" })).toBeUndefined();
});

test("a drop joins a group, or starts a new first or last group", () => {
  const to = (id: string, target: DropTarget) => dropOf(two, id, target);
  expect(to("c2", { group: 5 })).toBe(5);
  expect(to("c1", { group: 0 })).toBe(0);
  expect(to("c2", { group: 0 })).toBeUndefined();
  // Gemini's group is not a group of Claude.
  expect(to("c2", { group: 3 })).toBeUndefined();
  expect(to("c2", { edge: "first" })).toBe(6);
  expect(to("c1", { edge: "first" })).toBeUndefined();
  expect(to("c1", { edge: "last" })).toBe(-1);
  expect(to("c2", { edge: "last" })).toBe(-1);
  expect(to("c3", { edge: "last" })).toBe(-1);
});

test("a new fallback starts with its own model as step 1 and an empty step 2", () => {
  const d = withName(newDraft(), "claude-opus-4-7");
  expect(d.steps.map((s) => [s.model, s.provider])).toEqual([
    ["claude-opus-4-7", ""],
    ["", ""],
  ]);
  // Step 1 follows the name until it is changed itself.
  expect(withName(d, "claude-opus-4-8").steps[0]?.model).toBe("claude-opus-4-8");
  const edited = changeStep(d, 0, { model: "gpt-5.5" });
  expect(withName(edited, "claude-sonnet-4-6").steps[0]?.model).toBe("gpt-5.5");
  // An existing fallback's name cannot change, and its first step is its own.
  const existing = draftOf("m", [{ model: "x" }]);
  expect(withName(existing, "n").steps[0]?.model).toBe("x");
});

test("a fallback with nothing to try but itself says it never reaches its own model", () => {
  const d = changeStep(withName(newDraft(), "m"), 0, { model: "other" });
  expect(skipsItself(d)).toBe(true);
  expect(skipsItself(withName(newDraft(), "m"))).toBe(false);
  expect(skipsItself(newDraft())).toBe(false);
});

test("a draft is checked for the mistakes core would refuse, in order", () => {
  const ok = changeStep(withName(newDraft(), "claude-opus-4-7"), 1, {
    model: "gpt-5.5",
    provider: "pluxy/codex",
  });
  expect(draftProblem(ok, [])).toBeUndefined();
  expect(draftProblem(newDraft(), [])).toBe("Enter the model a tool asks for.");
  expect(draftProblem(withName(newDraft(), "  "), [])).toBe("Enter the model a tool asks for.");
  expect(draftProblem(ok, ["claude-opus-4-7"])).toBe(
    "There is already a fallback for claude-opus-4-7. Edit it instead.",
  );
  // Editing an existing one is not a clash with itself.
  expect(draftProblem({ ...ok, existing: true }, ["claude-opus-4-7"])).toBeUndefined();
  expect(draftProblem(removeStep(removeStep(ok, 0), 1), [])).toBe("Add at least one step.");
  expect(draftProblem(withName(newDraft(), "m"), [])).toBe("Each step needs a model.");
  const repeated = changeStep(ok, 1, { model: "claude-opus-4-7", provider: "" });
  expect(draftProblem(repeated, [])).toBe("Step 2 repeats step 1.");
  // The same model on another provider or with other accounts is not a repeat.
  expect(draftProblem(changeStep(repeated, 1, { provider: "pluxy/claude" }), [])).toBeUndefined();
  expect(draftProblem(changeStep(repeated, 1, { accounts: ["a"] }), [])).toBeUndefined();
});

test("a ninth step cannot be added, and steps move and go", () => {
  let d = withName(newDraft(), "m");
  for (let i = 0; i < 10; i++) d = addStep(d);
  expect(d.steps).toHaveLength(MAX_STEPS);
  expect(addStep(d)).toBe(d);
  const moved = moveStep(d, 0, "down");
  expect(moved.steps.map((s) => s.id).slice(0, 2)).toEqual([1, 0]);
  expect(moveStep(d, 0, "up")).toBe(d);
  expect(removeStep(d, 0).steps).toHaveLength(MAX_STEPS - 1);
});

test("steps are sent without an empty provider or account list", () => {
  const d = changeStep(
    changeStep(withName(newDraft(), " m "), 1, { model: " gpt-5.5 ", provider: "pluxy/codex" }),
    1,
    {
      accounts: ["work"],
    },
  );
  expect(stepsOf(d)).toEqual([
    { model: "m" },
    { model: "gpt-5.5", provider: "pluxy/codex", accounts: ["work"] },
  ]);
});

test("a model picked from the served list gives the provider that serves it", () => {
  const served = [{ id: "gpt-5.5", extension: "pluxy/codex", upstream: "gpt-5.5", accounts: ["a"] }];
  expect(providerOfModel("gpt-5.5", served)).toBe("pluxy/codex");
  expect(providerOfModel("gpt-5", served)).toBeUndefined();
  expect(providerOfModel("gpt-5.5", undefined)).toBeUndefined();
});

test("a fallback reads as one line of steps, and stepParts separates the model from its qualifier", () => {
  const name = (id: string) => ({ work: "Work", home: "Home" })[id] ?? id;
  expect(stepWords({ model: "claude-opus-4-7" }, name)).toBe("claude-opus-4-7");
  expect(stepWords({ model: "gpt-5.5", provider: "pluxy/codex" }, name)).toBe("gpt-5.5 (Codex)");
  expect(stepWords({ model: "gpt-5.5", provider: "pluxy/codex", accounts: ["work", "home"] }, name)).toBe(
    "gpt-5.5 (Codex, only Work, Home)",
  );
  expect(stepParts({ model: "claude-opus-4-7" }, name)).toEqual({ model: "claude-opus-4-7" });
  expect(stepParts({ model: "gpt-5.5", provider: "pluxy/codex" }, name)).toEqual({
    model: "gpt-5.5",
    detail: "Codex",
  });
  expect(stepParts({ model: "gpt-5.5", provider: "pluxy/codex", accounts: ["work", "home"] }, name)).toEqual({
    model: "gpt-5.5",
    detail: "Codex, only Work, Home",
  });
});

test("the fallback calls name the model as one path segment and send the steps as written", async () => {
  const calls: { method: Method; path: string; body?: unknown }[] = [];
  const host = {
    call: async (method: Method, path: string, body?: unknown) => {
      calls.push({ method, path, body });
      return { status: 200 };
    },
  } as unknown as Host;
  const a = api(host);
  await a.chains();
  await a.saveChain("models/gemini 3", [
    { model: "m" },
    { model: "n", provider: "pluxy/gemini", accounts: ["x"] },
  ]);
  await a.deleteChain("claude-opus-4-7");
  expect(calls).toEqual([
    { method: "GET", path: "/chains", body: undefined },
    {
      method: "PUT",
      path: "/chains/models%2Fgemini%203",
      body: { steps: [{ model: "m" }, { model: "n", provider: "pluxy/gemini", accounts: ["x"] }] },
    },
    { method: "DELETE", path: "/chains/claude-opus-4-7", body: undefined },
  ]);
});
