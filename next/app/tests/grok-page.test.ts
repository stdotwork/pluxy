// The Grok Build page's form logic (src/grokForm.ts): the catalog it starts
// from, and when Apply is held back.
import { describe, expect, test } from "bun:test";
import { blocked, MODELS_MAX, modelOf, type Row, rowsOf, withServed } from "../src/grokForm";

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

  test("adds the core's models once, keeping the rows already there", () => {
    const rows: Row[] = [{ model: "a", name: "A", window: 1000, on: false }];
    expect(withServed(rows, [{ id: "a" }, { id: "c", display_name: "C" }])).toEqual([
      { model: "a", name: "A", window: 1000, on: false },
      { model: "c", name: "C", window: 200000, on: true },
    ]);
  });
});

describe("apply", () => {
  const row = (model: string, on = true, window = 200000): Row => ({ model, name: model, window, on });

  test("is held back past the tool's limit of models", () => {
    const many = Array.from({ length: MODELS_MAX + 1 }, (_, i) => row(`m${i}`));
    expect(blocked(many, "m0")).toBe("too-many");
    many[MODELS_MAX].on = false;
    expect(blocked(many, "m0")).toBeUndefined();
  });

  test("needs a selected default model, a selected row and whole windows", () => {
    expect(blocked([row("a")], "a")).toBeUndefined();
    expect(blocked([row("a", false)], "a")).toBe("none");
    expect(blocked([row("a"), row("b", false)], "b")).toBe("model");
    expect(blocked([row("a", true, 0)], "a")).toBe("window");
    expect(blocked([row("a", true, 1.5)], "a")).toBe("window");
  });
});
