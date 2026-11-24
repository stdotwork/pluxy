// The Codex page's form logic (src/codexForm.ts): the form it fills from
// the tool's read-back.
import { describe, expect, test } from "bun:test";
import { fresh, fromFile } from "../src/codexForm";

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
