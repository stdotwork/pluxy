// The native runner's single-instance step (native/instances.ts) reads its session's end right:
// the session here is a stand-in `dbus-run-session` on PATH that prints what a real one would.

import { afterAll, expect, spyOn, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runInstances } from "../native/instances";

const bin = mkdtempSync(join(tmpdir(), "pluxy-inst-test-"));
const path = process.env.PATH;
afterAll(() => {
  process.env.PATH = path;
  rmSync(bin, { recursive: true, force: true });
});

/** Runs the step with a session that prints `lines` and then ends with `end` (shell). */
async function session(lines: string[], end: string) {
  const script = join(bin, "dbus-run-session");
  writeFileSync(script, `#!/bin/sh\n${lines.map((l) => `echo '${l}'`).join("\n")}\n${end}\n`);
  chmodSync(script, 0o755);
  process.env.PATH = `${bin}:${path}`;
  return runInstances({
    bin: "unused",
    probe: "unused",
    appImage: "unused",
    core: "unused",
    identifier: "dev.pluxy.test",
    timeoutMs: 10_000,
    sessions: new Set(),
    stopSession: async () => {},
    removeRun: async (root) => rmSync(root, { recursive: true, force: true }),
  });
}

const finished = ["native-prereq: ready", "instances: done"];

/**
 * Runs the session with console.log captured. runInstances announces its outcome and the session's
 * last lines there; the tests check those lines instead of printing them into the test output.
 */
async function captured(end: string) {
  const logs = spyOn(console, "log").mockImplementation(() => {});
  try {
    const result = await session(finished, end);
    return { result, logged: logs.mock.calls.map((args) => args.join(" ")) };
  } finally {
    logs.mockRestore();
  }
}

test("a session that finished and exited 0 passes", async () => {
  const { result, logged } = await captured("exit 0");
  expect(result).toEqual({ problems: [] });
  expect(logged).toContain("instances: ok");
});

test("a session that finished but exited nonzero fails", async () => {
  const { result: r, logged } = await captured("exit 3");
  expect(r.setup).toBeUndefined();
  expect(r.problems.length).toBe(1);
  expect(r.problems[0]).toContain("3");
  expect(logged).toContain("instances: FAILED");
});

test("a session that finished but was killed by a signal fails", async () => {
  const { result: r, logged } = await captured("kill -9 $$");
  expect(r.problems.length).toBe(1);
  expect(r.problems[0]).toContain("SIGKILL");
  expect(logged).toContain("instances: FAILED");
});
