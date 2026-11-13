// The native runner's single-instance step (native/instances.ts) reads its session's end right:
// the session here is a stand-in `dbus-run-session` on PATH that prints what a real one would.

import { afterAll, expect, test } from "bun:test";
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

test("a session that finished and exited 0 passes", async () => {
  expect(await session(finished, "exit 0")).toEqual({ problems: [] });
});

test("a session that finished but exited nonzero fails", async () => {
  const r = await session(finished, "exit 3");
  expect(r.setup).toBeUndefined();
  expect(r.problems.length).toBe(1);
  expect(r.problems[0]).toContain("3");
});

test("a session that finished but was killed by a signal fails", async () => {
  const r = await session(finished, "kill -9 $$");
  expect(r.problems.length).toBe(1);
  expect(r.problems[0]).toContain("SIGKILL");
});
