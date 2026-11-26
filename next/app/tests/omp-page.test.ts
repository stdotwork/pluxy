// The Oh My Pi page's form logic (src/ompForm.ts): the model it starts from,
// and which model IDs Apply takes.
import { describe, expect, test } from "bun:test";
import { modelOf, validModel } from "../src/ompForm";

describe("model", () => {
  test("starts from pluxy's default model, and from none for a refused or unset file", () => {
    expect(modelOf({ state: "configured", model: "gpt-6.1-sol" })).toBe("gpt-6.1-sol");
    expect(modelOf({ state: "not-configured", model: null })).toBe("");
    expect(modelOf({ state: "refused", reason: "not YAML" })).toBe("");
  });

  test("takes one model ID, never a list, a role or spaces", () => {
    for (const ok of ["gpt-6.1-sol", "qwen3:8b", "openrouter/x"]) expect(validModel(ok)).toBe(true);
    for (const bad of ["", "a,b", "a b", "@slow", "*", "x\n"]) expect(validModel(bad)).toBe(false);
  });
});
