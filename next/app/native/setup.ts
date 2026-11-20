#!/usr/bin/env bun
// The Setup checks' driver. run.ts gives it to session.ts in place of the app, so it runs inside
// one run's session (its HOME, X server and bus). It starts the app (PLUXY_APP_TEST_WINDOW_BIN,
// built with native-test and artifact-test-trust) once per stage with PLUXY_APP_TEST_SETUP=<stage>;
// the stages themselves are src-tauri/src/native_setup.rs. The session's managed core is a first
// run: nothing installed, no accounts, a bundled registry that offers pluxy/claude as a pick
// (core-steps.ts firstRunSource), and a stand-in claude first on the PATH it inherits from the app.
//
// When a stage prints `native-test: setup_shot {"name":...}` (after holding a screen long enough
// for session.ts's next capture), it copies the session's screenshot so far beside it as
// <session>-<name>.png (PLUXY_APP_TEST_SETUP_SHOT names it; unset when the run takes none).
//
// It prints `setup: ok <check>` or `setup: FAILED <check>` for every check, then
// `setup: suite passed` or `setup: suite FAILED`, and exits 0 only when every check passed.

import { spawn } from "node:child_process";
import { copyFileSync, existsSync } from "node:fs";
import { createInterface } from "node:readline";

const bin = process.env.PLUXY_APP_TEST_WINDOW_BIN;
if (!bin) {
  console.error("setup.ts needs PLUXY_APP_TEST_WINDOW_BIN");
  process.exit(2);
}
const shot = process.env.PLUXY_APP_TEST_SETUP_SHOT;
const STAGE_TIMEOUT = 180_000;

let failed = 0;
function check(ok: unknown, what: string) {
  console.log(`setup: ${ok ? "ok" : "FAILED"} ${what}`);
  if (!ok) failed++;
}

let child: ReturnType<typeof spawn> | undefined;
for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => {
    child?.kill("SIGKILL");
    process.exit(128);
  });
}

/** Keeps the session's last screen as it is now, under name. */
function keepShot(name: string) {
  if (!shot || !existsSync(shot)) return;
  try {
    copyFileSync(shot, shot.replace(/\.png$/, `-${name}.png`));
  } catch (e) {
    console.log(`setup: screenshot ${name} not kept: ${(e as Error).message}`);
  }
}

/** Runs the app through one stage of the Setup checks. */
async function stage(name: string) {
  const proc = spawn(bin as string, [], {
    env: { ...process.env, PLUXY_APP_TEST_SETUP: name },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child = proc;
  let report: { stage?: string; failed?: number } | undefined;
  const lines: string[] = [];
  const before = failed;
  const onLine = (line: string) => {
    lines.push(line);
    if (lines.length > 80) lines.shift();
    const m = /^native-test: (setup_\w+|failed) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string);
    if (m[1] === "setup_check") check(value.ok, `${name}: ${value.what}`);
    if (m[1] === "setup_shot") keepShot(`${name}-${value.name}`);
    if (m[1] === "setup_report") report = value;
    if (m[1] === "failed") check(false, `${name}: the stage failed: ${JSON.stringify(value).slice(0, 2000)}`);
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
  check(report?.stage === name, `${name}: the stage reported`);
  if (failed > before) console.log(`setup: ${name}: the app's last lines:\n  ${lines.join("\n  ")}`);
}

await stage("first");
await stage("second");

console.log(`setup: suite ${failed === 0 ? "passed" : "FAILED"} (${failed} failed)`);
process.exit(failed === 0 ? 0 : 1);
