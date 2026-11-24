// What the client setup pages share (src/clientPage.tsx): the keys a page offers from the vault.
import { describe, expect, test } from "bun:test";
import { keysOf } from "../src/clientPage";

describe("keys", () => {
  test("lists plain secrets and this package's client keys from vault_secrets' answer", () => {
    const body = {
      secrets: [
        { id: "a", label: "Plain" },
        { id: "b", label: "Ours", client_key: { client: "pluxy/claude-code" } },
        { id: "c", label: "Codex's", client_key: { client: "pluxy/codex-setup" } },
      ],
    };
    expect(keysOf("pluxy/claude-code", body).map((s) => s.id)).toEqual(["a", "b"]);
    expect(keysOf("pluxy/codex-setup", body).map((s) => s.id)).toEqual(["a", "c"]);
  });

  test("lists nothing while the answer is missing or empty", () => {
    expect(keysOf("pluxy/claude-code", undefined)).toEqual([]);
    expect(keysOf("pluxy/claude-code", { secrets: [] })).toEqual([]);
    expect(keysOf("pluxy/claude-code", {})).toEqual([]);
  });
});
