// The Hermes Agent page's form logic (src/hermesForm.ts): the keys it offers from the vault, the
// model it starts from, and which model IDs Apply takes.
import { describe, expect, test } from "bun:test";
import { keysOf, modelOf, validModel } from "../src/hermesForm";

describe("keys", () => {
  test("lists plain secrets and this package's client keys from vault_secrets' answer", () => {
    const body = {
      secrets: [
        { id: "a", label: "Plain" },
        { id: "b", label: "Ours", client_key: { client: "pluxy/hermes-setup" } },
        { id: "c", label: "Oh My Pi's", client_key: { client: "pluxy/omp-setup" } },
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

describe("model", () => {
  test("starts from pluxy's default model, and from none for a refused or unset file", () => {
    expect(modelOf({ state: "configured", model: "gpt-6.1-sol" })).toBe("gpt-6.1-sol");
    expect(modelOf({ state: "not-configured", model: null })).toBe("");
    expect(modelOf({ state: "refused", reason: "not YAML" })).toBe("");
  });

  test("takes one model ID, without spaces or ${", () => {
    for (const ok of ["gpt-6.1-sol", "qwen3:8b", "openrouter/x", "a,b"]) expect(validModel(ok)).toBe(true);
    for (const bad of ["", "a b", "x\n", "a$" + "{B}", "x".repeat(241)]) expect(validModel(bad)).toBe(false);
  });
});
