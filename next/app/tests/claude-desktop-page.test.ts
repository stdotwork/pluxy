// The Claude Desktop page's form logic (src/claudeDesktopForm.ts): the rows it fills from
// the tool's read-back, when Apply is allowed, and when it offers to create Claude Desktop's library
// folder.
import { describe, expect, test } from "bun:test";
import {
  aliasCancelled,
  aliasState,
  fresh,
  MISSING_LIBRARY,
  missingLibrary,
  ready,
  rowsFrom,
} from "../src/claudeDesktopForm";

describe("rows from the files", () => {
  test("the files' rows, or the three starting rows when they hold none or are refused", () => {
    const models = [{ model: "gpt-6.1-sol", alias: "claude-sonnet-5-sol", one_m: true }];
    expect(rowsFrom({ state: "configured", models })).toEqual(models);
    expect(rowsFrom({ state: "not-configured", models: [] })).toEqual(fresh());
    expect(rowsFrom({ state: "refused", reason: "not JSON" })).toEqual(fresh());
  });
});

describe("apply allowed", () => {
  test("the starting rows can be applied", () => {
    expect(ready(fresh())).toBe(true);
  });

  test("no rows, an empty model, a model or alias with a space, or more than 16 rows cannot", () => {
    expect(ready([])).toBe(false);
    expect(ready([{ model: " ", alias: "", one_m: false }])).toBe(false);
    expect(ready([{ model: "claude opus", alias: "", one_m: false }])).toBe(false);
    expect(ready([{ model: "gpt-6.1-sol", alias: "claude sonnet", one_m: false }])).toBe(false);
    const many = Array.from({ length: 17 }, (_, i) => ({
      model: `claude-opus-${i}`,
      alias: "",
      one_m: false,
    }));
    expect(ready(many)).toBe(false);
  });
});

describe("missing library", () => {
  test("only the tool's own missing-library refusal offers to create the folder", () => {
    expect(
      missingLibrary(`${MISSING_LIBRARY}: /home/x/.config/Claude-3p/configLibrary; create it here`),
    ).toBe(true);
    expect(missingLibrary("/home/x/.config/Claude-3p/configLibrary: No such file or directory")).toBe(false);
    expect(missingLibrary(undefined)).toBe(false);
  });
});

describe("aliasState", () => {
  const row = (model: string, alias: string) => ({ model, alias, one_m: false });
  const served = new Set(["gpt-6", "claude-opus-5-x"]);
  test("a row with no alias, or its model's own name, needs nothing from core", () => {
    expect(aliasState(row("gpt-6", ""), served)).toBe("none");
    expect(aliasState(row("gpt-6", " gpt-6 "), served)).toBe("none");
    // Core matches the model's name whatever the case of its ASCII letters.
    expect(aliasState(row("gpt-6", "GPT-6"), served)).toBe("none");
  });
  test("an alias core lists is served; one it does not list is offered for creating", () => {
    expect(aliasState(row("gpt-6", "claude-opus-5-x"), served)).toBe("served");
    expect(aliasState(row("gpt-6", " claude-sonnet-5-x "), served)).toBe("missing");
  });
  test("without core's list nothing is offered", () => {
    expect(aliasState(row("gpt-6", "claude-sonnet-5-x"), undefined)).toBe("unknown");
  });
});

describe("aliasCancelled", () => {
  test("only the app's own cancel is a cancel", () => {
    expect(aliasCancelled("cancelled")).toBe(true);
    expect(aliasCancelled("core did not add the alias to a: the request was cancelled")).toBe(false);
    expect(aliasCancelled(undefined)).toBe(false);
  });
});
