// The Grok Build page's form logic (src/grokForm.ts): the catalog it starts
// from, and every reason Connect is held.
import { describe, expect, test } from "bun:test";
import { heldOf, MODELS_MAX, modelOf, type Row, rowsOf, withServed } from "../src/grokForm";

describe("catalog", () => {
  test("starts from pluxy's entries, and from none for a refused file", () => {
    const r = {
      state: "configured" as const,
      model: "a",
      models: [
        { model: "a", name: "A", context_window: 400000 },
        { model: "b", name: null, context_window: null },
      ],
    };
    expect(rowsOf(r)).toEqual([
      { model: "a", name: "A", window: 400000, on: true },
      { model: "b", name: "b", window: 200000, on: true },
    ]);
    expect(modelOf(r)).toBe("a");
    expect(rowsOf({ state: "refused", reason: "not TOML" })).toEqual([]);
    expect(modelOf({ state: "refused", reason: "not TOML" })).toBe("");
  });

  test("adds pluxy's models once, keeping the rows already there", () => {
    const rows: Row[] = [{ model: "a", name: "A", window: 1000, on: false }];
    expect(withServed(rows, [{ id: "a" }, { id: "c", display_name: "C" }])).toEqual([
      { model: "a", name: "A", window: 1000, on: false },
      { model: "c", name: "C", window: 200000, on: true },
    ]);
  });
});

describe("Connect", () => {
  const row = (model: string, on = true, window = 200000): Row => ({ model, name: model, window, on });

  test("is held back past the tool's limit of models", () => {
    const many = Array.from({ length: MODELS_MAX + 1 }, (_, i) => row(`m${i}`));
    expect(heldOf(many, "m0")).toEqual(["too-many"]);
    many[MODELS_MAX].on = false;
    expect(heldOf(many, "m0")).toEqual([]);
  });

  test("needs a selected default model, a selected row and whole windows", () => {
    expect(heldOf([row("a")], "a")).toEqual([]);
    expect(heldOf([row("a", false)], "a")).toEqual(["none"]);
    expect(heldOf([row("a"), row("b", false)], "b")).toEqual(["model"]);
    expect(heldOf([row("a", true, 0)], "a")).toEqual(["window"]);
    expect(heldOf([row("a", true, 1.5)], "a")).toEqual(["window"]);
  });

  test("is held for model text the add-on refuses: spaces or control characters in an ID, control characters in a name, an ID listed twice once trimmed", () => {
    expect(heldOf([row(" a ")], "a")).toEqual([]);
    expect(heldOf([row("a b")], "a b")).toEqual(["text"]);
    expect(heldOf([row("a\u0007")], "a\u0007")).toEqual(["text"]);
    expect(heldOf([row("x".repeat(241))], "x".repeat(241))).toEqual(["text"]);
    expect(heldOf([{ model: "a", name: "A\u0000", window: 1000, on: true }], "a")).toEqual(["text"]);
    expect(heldOf([{ model: "a", name: "x".repeat(241), window: 1000, on: true }], "a")).toEqual(["text"]);
    // A name with spaces is fine; only the ID is bare.
    expect(heldOf([{ model: "a", name: "Model A", window: 1000, on: true }], "a")).toEqual([]);
    expect(heldOf([row("a"), row("a ")], "a")).toEqual(["twice"]);
    // A row turned off is not written, so it holds nothing.
    expect(heldOf([row("a"), row("a b", false)], "a")).toEqual([]);
  });

  test("says every reason at once", () => {
    const many = Array.from({ length: MODELS_MAX + 1 }, (_, i) => row(`m${i}`, true, i === 0 ? 0 : 200000));
    expect(heldOf(many, "elsewhere")).toEqual(["too-many", "window", "model"]);
  });
});
