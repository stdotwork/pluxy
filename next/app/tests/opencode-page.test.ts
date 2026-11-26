// The OpenCode page's form logic (src/opencodeForm.ts): the models it lists and
// every reason Connect is held.
import { describe, expect, test } from "bun:test";
import { attachmentDefault, heldOf, MODELS_MAX, withModels } from "../src/opencodeForm";

describe("models", () => {
  const m = (id: string, name = "") => ({ id, name, attachment: false });

  test("imported models add only IDs not listed yet, once each", () => {
    expect(withModels([m("a")], [m("a"), m("b"), m("b")]).map((x) => x.id)).toEqual(["a", "b"]);
  });

  test("attachments start on for model names the old app knew take images", () => {
    expect(attachmentDefault("Claude-Sonnet-5-5")).toBe(true);
    expect(attachmentDefault("deepseek-v4")).toBe(false);
  });

  test("Connect needs a listed default and IDs the tool takes, and says every reason", () => {
    expect(heldOf([m("a"), m("b")], "b")).toEqual([]);
    expect(heldOf([], "")).toEqual(["none"]);
    expect(heldOf([m("a")], "b")).toEqual(["model"]);
    expect(heldOf([m("a#high")], "a#high")).toEqual(["text"]);
    expect(heldOf([m("x".repeat(241))], "x".repeat(241))).toEqual(["text"]);
    const many = Array.from({ length: MODELS_MAX + 1 }, (_, i) => m(`m${i}#`));
    expect(heldOf(many, "elsewhere")).toEqual(["too-many", "text", "model"]);
  });

  test("Connect is held for what the add-on refuses: control characters in an ID or a name, an ID listed twice once trimmed", () => {
    // The add-on trims IDs and names before it checks them, as here.
    expect(heldOf([m(" a "), m("b")], "a")).toEqual([]);
    expect(heldOf([m("a\u0085b")], "a\u0085b")).toEqual(["text"]);
    expect(heldOf([m("a", "A\u0007")], "a")).toEqual(["text"]);
    expect(heldOf([m("a", "A\u009f")], "a")).toEqual(["text"]);
    expect(heldOf([m("a", "x".repeat(241))], "a")).toEqual(["text"]);
    expect(heldOf([m("a"), m("a ")], "a")).toEqual(["twice"]);
  });
});
