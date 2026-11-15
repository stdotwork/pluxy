// The Codex page's form logic (src/codexForm.ts): the keys it offers from the vault and the form it
// fills from the tool's read-back.
import { describe, expect, test } from "bun:test";
import { fresh, fromFile, keysOf } from "../src/codexForm";

describe("keys", () => {
  test("lists plain secrets and this package's client keys from vault_secrets' answer", () => {
    const body = {
      secrets: [
        { id: "a", label: "Plain" },
        { id: "b", label: "Ours", client_key: { client: "pluxy/codex-setup" } },
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

describe("form from the files", () => {
  test("files Codex would not take give the fresh form", () => {
    expect(fromFile({ state: "refused", reason: "the file is not TOML" })).toEqual(fresh());
  });

  test("files read back fill the form", () => {
    const agents = { enabled: false, max_threads: 6, model: "m", effort: "low", interrupt: false };
    const f = fromFile({
      state: "configured",
      key_set: true,
      provider: "pluxy",
      model: null,
      profile: null,
      remote_compaction: true,
      agents,
      models: [],
      approval_model: "a",
    });
    expect(f).toEqual({ model: "", approval: "a", compaction: true, agents, models: [] });
  });
});
