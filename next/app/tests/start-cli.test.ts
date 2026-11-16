// The Start CLI's terminal choice once its terminal list reloads (src/startCli.ts): the choice
// stays while the launcher still lists it, and goes back to Automatic when the launcher does not.
import { describe, expect, test } from "bun:test";
import { keptTerminal } from "../src/startCli";

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
