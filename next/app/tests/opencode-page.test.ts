// The OpenCode page's form logic (src/opencodeForm.ts): the keys it offers from the vault, the
// models it lists and when Apply is allowed.
import { describe, expect, test } from "bun:test";
import { attachmentDefault, keysOf, ready, withModels } from "../src/opencodeForm";

describe("keys", () => {
  test("lists plain secrets and this package's client keys from vault_secrets' answer", () => {
    const body = {
      secrets: [
        { id: "a", label: "Plain" },
        { id: "b", label: "Ours", client_key: { client: "pluxy/opencode" } },
        { id: "c", label: "Claude Code's", client_key: { client: "pluxy/claude-code" } },
      ],
    };
    expect(keysOf(body).map((s) => s.id)).toEqual(["a", "b"]);
  });

  test("lists nothing while the answer is missing or empty", () => {
    expect(keysOf(undefined)).toEqual([]);
    expect(keysOf({ secrets: [] })).toEqual([]);
    expect(keysOf({})).toEqual([]);
  });
});

describe("models", () => {
  const m = (id: string) => ({ id, name: "", attachment: false });

  test("imported models add only IDs not listed yet, once each", () => {
    expect(withModels([m("a")], [m("a"), m("b"), m("b")]).map((x) => x.id)).toEqual(["a", "b"]);
  });

  test("attachments start on for model names the old app knew take images", () => {
    expect(attachmentDefault("Claude-Sonnet-5-5")).toBe(true);
    expect(attachmentDefault("deepseek-v4")).toBe(false);
  });

  test("apply needs a listed default and IDs the tool takes", () => {
    expect(ready([m("a"), m("b")], "b")).toBe(true);
    expect(ready([], "")).toBe(false);
    expect(ready([m("a")], "b")).toBe(false);
    expect(ready([m("a#high")], "a#high")).toBe(false);
    expect(ready([m("x".repeat(241))], "x".repeat(241))).toBe(false);
  });
});
