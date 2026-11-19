// The update scenario's check that each stage ran as a process of its own (native/update-cases.ts):
// it goes by the value each process made at its start, since a process id is reused.
import { describe, expect, test } from "bun:test";
import { stagesAreProcesses } from "../native/update-cases";

const hex = (c: string) => c.repeat(64);

describe("each stage is a process of its own", () => {
  test("passes when every stage reports its own start value", () => {
    expect(
      stagesAreProcesses([
        { pid: 7832, run: hex("a") },
        { pid: 8280, run: hex("b") },
        { pid: 6628, run: hex("c") },
      ]),
    ).toBe(true);
  });

  test("passes when a later process was given an earlier one's id", () => {
    // The Windows failure that made this check go by more than the id: stages 1 and 3 had 7832.
    expect(
      stagesAreProcesses([
        { pid: 7832, run: hex("a") },
        { pid: 8280, run: hex("b") },
        { pid: 7832, run: hex("c") },
        { pid: 6628, run: hex("d") },
      ]),
    ).toBe(true);
  });

  test("fails when two stages report the same start value (the same process)", () => {
    expect(
      stagesAreProcesses([
        { pid: 7832, run: hex("a") },
        { pid: 8280, run: hex("b") },
        { pid: 8280, run: hex("a") },
      ]),
    ).toBe(false);
  });

  test("fails when a stage reports no start value or no id", () => {
    expect(stagesAreProcesses([{ pid: 1, run: hex("a") }, { pid: 2 }])).toBe(false);
    expect(
      stagesAreProcesses([
        { pid: 1, run: hex("a") },
        { pid: 2, run: "" },
      ]),
    ).toBe(false);
    expect(stagesAreProcesses([{ pid: 1, run: hex("a") }, { run: hex("b") }])).toBe(false);
  });
});
