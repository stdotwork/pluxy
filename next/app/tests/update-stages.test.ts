// The update scenario's check that each stage ran as a process of its own (native/update-cases.ts):
// it goes by the value each process made at its start, since a process id is reused.
import { describe, expect, test } from "bun:test";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { sameProcess, stagesAreProcesses, startOf, stillRuns } from "../native/update-cases";

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

describe("the held acceptance is the new version's own process", () => {
  test("passes for the same start value, even when the ids differ", () => {
    expect(sameProcess({ pid: 1, run: hex("a") }, { pid: 2, run: hex("a") })).toBe(true);
  });

  test("fails for another start value, even when the ids are the same", () => {
    expect(sameProcess({ pid: 7832, run: hex("a") }, { pid: 7832, run: hex("b") })).toBe(false);
  });

  test("fails when a report is missing or has no start value", () => {
    expect(sameProcess(undefined, { run: hex("a") })).toBe(false);
    expect(sameProcess({ pid: 1 }, { pid: 1 })).toBe(false);
    expect(sameProcess({ run: "" }, { run: "" })).toBe(false);
  });
});

describe("the version that installed the update has ended", () => {
  const gone = () => false;
  const here = () => true;

  test("has ended when its id is gone", () => {
    expect(stillRuns(7832, "100", gone, () => "100")).toBe(false);
  });

  test("has ended when its id went to a later process (another start time)", () => {
    expect(stillRuns(7832, "100", here, () => "250")).toBe(false);
  });

  test("still runs when the id has the start time it had", () => {
    expect(stillRuns(7832, "100", here, () => "100")).toBe(true);
  });

  test("goes by the id alone when a start time cannot be read", () => {
    expect(stillRuns(7832, undefined, here, () => "250")).toBe(true);
    expect(stillRuns(7832, "100", here, () => undefined)).toBe(true);
    expect(stillRuns(7832, undefined, gone, () => undefined)).toBe(false);
  });
});

describe("start time of a process", () => {
  test.skipIf(process.platform === "win32")(
    "is the same on each read and differs for another process",
    async () => {
      const child = spawn("sleep", ["30"], { stdio: "ignore" });
      try {
        const pid = child.pid as number;
        const first = startOf(pid);
        expect(first).toBeTruthy();
        expect(startOf(pid)).toBe(first);
        expect(stillRuns(pid, first)).toBe(true);
        expect(stillRuns(pid, `${first}-other`)).toBe(false);
      } finally {
        child.kill("SIGKILL");
      }
      await new Promise((r) => child.on("close", r));
      expect(stillRuns(child.pid as number, "x")).toBe(false);
      expect(startOf(child.pid as number)).toBeUndefined();
    },
  );

  test.skipIf(process.platform !== "linux")(
    "reads field 22 when the command holds spaces and brackets",
    () => {
      // The kernel's own line for this process: the command name field is unchanged, so the value
      // matches what a direct parse of the whole line gives.
      const stat = readFileSync("/proc/self/stat", "utf8");
      const after = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
      expect(startOf(process.pid)).toBe(after[19]);
      expect(after[19]).toMatch(/^\d+$/);
    },
  );
});
