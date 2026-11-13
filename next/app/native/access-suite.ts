#!/usr/bin/env bun
// The native test's Access checks (src-tauri/src/access_native.rs), inside one session from
// run.ts: runs the app through the stage `grant`, then launches it again for `relaunch` with
// the same HOME and folders, and checks each stage's report. It also checks every file the
// broker opened (the app's `native-test: access_io` lines): the files a page was refused, and
// the document portal's host file, were never opened. Prints `access: ...` lines; the last is
// `access: suite passed` when every check held.

import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";

const bin = process.env.PLUXY_APP_TEST_WINDOW_BIN;
const fixtures = process.env.PLUXY_APP_TEST_ACCESS;
if (!bin || !fixtures) {
  console.error("access-suite.ts needs PLUXY_APP_TEST_WINDOW_BIN and PLUXY_APP_TEST_ACCESS");
  process.exit(2);
}
const expect = JSON.parse(readFileSync(join(fixtures, "expect.json"), "utf8"));
const data: string = expect.data;
const STAGE_TIMEOUT = 300_000;

let failed = 0;
function check(ok: unknown, what: string) {
  console.log(`access: ${ok ? "ok" : "FAILED"} ${what}`);
  if (!ok) failed++;
}

let child: ReturnType<typeof spawn> | undefined;
for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => {
    child?.kill("SIGKILL");
    process.exit(128);
  });
}

/** Every path the broker opened, in both stages. */
const opened: string[] = [];

async function stage(name: string): Promise<Record<string, unknown> | undefined> {
  const proc = spawn(bin as string, [], {
    env: { ...process.env, PLUXY_APP_TEST_ACCESS_STAGE: name },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child = proc;
  let report: Record<string, unknown> | undefined;
  let failure: unknown;
  const lines: string[] = [];
  const onLine = (line: string) => {
    lines.push(line);
    if (lines.length > 80) lines.shift();
    const m = /^native-test: (\w+) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string);
    if (m[1] === "access") report = value;
    if (m[1] === "access_io") opened.push(value.path);
    if (m[1] === "failed") failure = value;
  };
  for (const stream of [proc.stdout, proc.stderr]) createInterface({ input: stream }).on("line", onLine);
  const code = await new Promise<number | null>((resolve) => {
    const timer = setTimeout(() => {
      check(false, `${name}: the app finished within ${STAGE_TIMEOUT / 1000}s`);
      proc.kill("SIGKILL");
    }, STAGE_TIMEOUT);
    proc.on("close", (c) => {
      clearTimeout(timer);
      resolve(c);
    });
  });
  child = undefined;
  check(code === 0, `${name}: the app exited with 0 (${code})`);
  check(
    report?.passed === true,
    `${name}: every check passed (${JSON.stringify(report ?? failure ?? "no report")})`,
  );
  if (report?.passed !== true) console.log(`access: ${name}: the app's last lines:\n  ${lines.join("\n  ")}`);
  return report;
}

await stage("grant");
await stage("relaunch");

// The broker opened what was granted, and never what a page was refused.
check(
  opened.some((p) => p === join(data, "stow", "config")),
  `the broker opened the granted link's target (${opened.length} opens)`,
);
for (const refusedFile of [
  join(data, "outside.txt"),
  join(data, "stow", "sibling"),
  join(data, "portal.txt"),
])
  check(!opened.includes(refusedFile), `the broker never opened ${refusedFile}`);

console.log(failed === 0 ? "access: suite passed" : `access: ${failed} checks failed`);
process.exit(failed === 0 ? 0 : 1);
