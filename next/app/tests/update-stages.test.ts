// The update scenario's check that each stage ran as a process of its own (native/update-cases.ts):
// it goes by the value each process made at its start, since a process id is reused.
import { describe, expect, test } from "bun:test";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { outputOf, sameProcess, stagesAreProcesses, startOf, stillRuns } from "../native/update-cases";

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

  const at = (t: string | undefined) => async () => t;

  test("has ended when its id is gone", async () => {
    expect(await stillRuns(7832, "100", gone, at("100"))).toBe(false);
  });

  test("has ended when its id went to a later process (another start time)", async () => {
    expect(await stillRuns(7832, "100", here, at("250"))).toBe(false);
  });

  test("still runs when the id has the start time it had", async () => {
    expect(await stillRuns(7832, "100", here, at("100"))).toBe(true);
  });

  test("goes by the id alone when a start time cannot be read", async () => {
    expect(await stillRuns(7832, undefined, here, at("250"))).toBe(true);
    expect(await stillRuns(7832, "100", here, at(undefined))).toBe(true);
    expect(await stillRuns(7832, undefined, gone, at(undefined))).toBe(false);
  });
});

describe("start time of a process", () => {
  test.skipIf(process.platform === "win32")(
    "is the same on each read and differs for another process",
    async () => {
      const child = spawn("sleep", ["30"], { stdio: "ignore" });
      try {
        const pid = child.pid as number;
        const first = await startOf(pid);
        expect(first).toBeTruthy();
        expect(await startOf(pid)).toBe(first);
        expect(await stillRuns(pid, first)).toBe(true);
        expect(await stillRuns(pid, `${first}-other`)).toBe(false);
      } finally {
        child.kill("SIGKILL");
      }
      await new Promise((r) => child.on("close", r));
      expect(await stillRuns(child.pid as number, "x")).toBe(false);
      expect(await startOf(child.pid as number)).toBeUndefined();
    },
  );

  test.skipIf(process.platform !== "linux")(
    "reads field 22 when the command holds spaces and brackets",
    async () => {
      // The kernel's own line for this process: the command name field is unchanged, so the value
      // matches what a direct parse of the whole line gives.
      const stat = readFileSync("/proc/self/stat", "utf8");
      const after = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
      expect(await startOf(process.pid)).toBe(after[19]);
      expect(after[19]).toMatch(/^\d+$/);
    },
  );
});

describe("reading a start time", () => {
  // The session serves the update feed from its own process: while a system tool runs (a cold
  // PowerShell took close to 20 s on a loaded Windows runner), the feed must still answer.
  test.skipIf(process.platform === "win32")("leaves this process free while the tool runs", async () => {
    let ticks = 0;
    const timer = setInterval(() => ticks++, 50);
    try {
      expect(await outputOf(["sh", "-c", "sleep 1; echo 5"], 10_000)).toBe("5");
    } finally {
      clearInterval(timer);
    }
    expect(ticks).toBeGreaterThan(5);
  });

  test.skipIf(process.platform === "win32")("is unread when the tool fails or runs too long", async () => {
    expect(await outputOf(["sh", "-c", "exit 3"], 10_000)).toBeUndefined();
    expect(await outputOf(["sh", "-c", "sleep 5; echo 5"], 200)).toBeUndefined();
  });
});
