// The Start section's terminal choice once its terminal list reloads (src/startCli.ts): the choice
// stays while the launcher still lists it, and goes back to Automatic when the launcher does not.
// And its words for a launcher that is not installed.
import { describe, expect, test } from "bun:test";
import { keptTerminal, launcherProblem } from "../src/startCli";

const listed = [
  { id: "automatic", label: "Automatic" },
  { id: "x-terminal-emulator", label: "Terminal emulator" },
];

describe("terminal choice after the list reloads", () => {
  test("keeps the chosen terminal while the launcher still lists it", () => {
    expect(keptTerminal("x-terminal-emulator", listed)).toBe("x-terminal-emulator");
  });

  test("goes back to Automatic when the launcher no longer lists the chosen terminal", () => {
    expect(keptTerminal("gone-terminal", listed)).toBe("automatic");
  });

  test("keeps the choice while the list has not loaded", () => {
    expect(keptTerminal("x-terminal-emulator", undefined)).toBe("x-terminal-emulator");
  });
});

describe("a launcher error", () => {
  test("says what is missing and where to install it when the launcher is not installed", () => {
    // The native side's own line for a tool this device does not have.
    const shown = launcherProblem("extension_unavailable: pluxy/execution is not installed on this device");
    expect(shown).toBe("The Launcher add-on is not installed on this computer. Install it in Add-ons.");
  });

  test("stays as it came for any other error, another tool's included", () => {
    for (const error of [
      "extension_unavailable: pluxy/claude-code is not installed on this device",
      "refused: the working directory does not exist",
    ]) {
      expect(launcherProblem(error)).toBe(error);
    }
  });
});
