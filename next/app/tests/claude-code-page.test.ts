// The Claude Code page's form logic (src/claudeCodeForm.ts): the form it fills from
// the tool's read-back, and when Apply is allowed.
import { describe, expect, test } from "bun:test";
import { fresh, fromFile, ready, settingsNote, toolUnavailable } from "../src/claudeCodeForm";

describe("form from the file", () => {
  test("a file set up with no startup model keeps it empty; a file never set up starts at opus", () => {
    expect(fromFile({ state: "configured", startup_model: null }).startup).toBe("");
    expect(fromFile({ state: "needs-update", startup_model: null }).startup).toBe("");
    expect(fromFile({ state: "not-configured", startup_model: null }).startup).toBe("opus");
    expect(fromFile({ state: "configured", startup_model: "sonnet" }).startup).toBe("sonnet");
  });

  test("a refused file gives the fresh form", () => {
    expect(fromFile({ state: "refused", reason: "not JSON" })).toEqual(fresh());
  });
});

describe("apply allowed", () => {
  test("the fresh form can be applied", () => {
    expect(ready(fresh())).toBe(true);
  });

  test("opus, sonnet and haiku each need a model, not only [1m]", () => {
    for (const role of ["opus", "sonnet", "haiku"] as const) {
      for (const m of ["", " ", "[1m]", " [1M] ", "[1m][1m]"]) {
        const f = fresh();
        f.models[role] = m;
        expect(ready(f)).toBe(false);
      }
    }
    const f = fresh();
    f.models.fable = "";
    expect(ready(f)).toBe(true);
    f.models.fable = "[1m]";
    expect(ready(f)).toBe(false);
  });

  test("the startup and subagent models may be empty, never only [1m]", () => {
    const f = fresh();
    f.startup = "";
    f.subagent = "";
    expect(ready(f)).toBe(true);
    f.startup = "[1m]";
    expect(ready(f)).toBe(false);
    f.startup = "";
    f.subagent = "[1M]";
    expect(ready(f)).toBe(false);
  });

  test("the window and percentage are whole numbers in range", () => {
    for (const [window, pct, ok] of [
      [100000, 1, true],
      [1000000, 100, true],
      [400000.5, 90, false],
      [200000, 90.5, false],
      [99999, 90, false],
      [1000001, 90, false],
      [200000, 0, false],
      [200000, 101, false],
      [Number.NaN, 90, false],
    ] as const) {
      const f = fresh();
      f.window = window;
      f.pct = pct;
      expect(ready(f)).toBe(ok);
    }
  });
});

describe("the settings file in the selected environment", () => {
  test("Claude Code's user settings are in the home of the WSL user, or ~ on this computer", () => {
    expect(settingsNote()).toBe("~/.claude/settings.json");
    expect(settingsNote("/home/alice")).toBe("/home/alice/.claude/settings.json");
    expect(settingsNote("/root")).toBe("/root/.claude/settings.json");
  });
});

describe("tool unavailable", () => {
  test("when the tool is not installed or refuses as the package", () => {
    expect(toolUnavailable("pluxy/claude-code is not installed on this device")).toBe(true);
    expect(toolUnavailable("pluxy/claude-code is disabled")).toBe(true);
  });

  test("not for a missing Linux build, a package that does not run in WSL, or no error", () => {
    expect(toolUnavailable("pluxy/claude-code's Linux build for linux/amd64 is not installed")).toBe(false);
    expect(toolUnavailable("pluxy/claude-code does not run in WSL")).toBe(false);
    expect(toolUnavailable(undefined)).toBe(false);
    expect(toolUnavailable("the settings file changed since it was read")).toBe(false);
  });
});
