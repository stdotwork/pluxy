#!/usr/bin/env bun
// The privacy mode checks' driver. run.ts gives it to session.ts in place of the app, so it runs
// inside one run's session (its HOME, X server and bus). It starts the app
// (PLUXY_APP_TEST_WINDOW_BIN, built with native-test) once per stage with
// PLUXY_APP_TEST_PRIVACY=<stage>; the stages themselves are src-tauri/src/native_privacy.rs.
// Between stages it checks the choice the app keeps natively (privacy-mode.json beside
// connection.json), and before the last stage it damages that file.
//
// It prints `privacy: ok <check>` or `privacy: FAILED <check>` for every check, then
// `privacy: suite passed` or `privacy: suite FAILED`, and exits 0 only when every check passed.

import { spawn } from "node:child_process";
import { readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";

const bin = process.env.PLUXY_APP_TEST_WINDOW_BIN;
const identifier = process.env.PLUXY_APP_TEST_IDENTIFIER;
const configHome = process.env.XDG_CONFIG_HOME;
if (!bin || !identifier || !configHome) {
  console.error("privacy.ts needs PLUXY_APP_TEST_WINDOW_BIN, PLUXY_APP_TEST_IDENTIFIER and XDG_CONFIG_HOME");
  process.exit(2);
}
const saved = join(configHome, identifier, "privacy-mode.json");
const STAGE_TIMEOUT = 150_000;

let failed = 0;
function check(ok: unknown, what: string) {
  console.log(`privacy: ${ok ? "ok" : "FAILED"} ${what}`);
  if (!ok) failed++;
}

let child: ReturnType<typeof spawn> | undefined;
for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => {
    child?.kill("SIGKILL");
    process.exit(128);
  });
}

/** Runs the app through one stage. */
async function stage(name: string) {
  const proc = spawn(bin as string, [], {
    env: { ...process.env, PLUXY_APP_TEST_PRIVACY: name },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child = proc;
  let report: { stage?: string; failed?: number } | undefined;
  const lines: string[] = [];
  const onLine = (line: string) => {
    lines.push(line);
    if (lines.length > 80) lines.shift();
    const m = /^native-test: (privacy_\w+|failed) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string);
    if (m[1] === "privacy_check") check(value.ok, `${name}: ${value.what}`);
    if (m[1] === "privacy_report") report = value;
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
  if (failed > 0) console.log(`privacy: ${name}: the app's last lines:\n  ${lines.join("\n  ")}`);
}

/** The saved choice, as the file holds it. */
function choice(): string {
  try {
    return readFileSync(saved, "utf8").trim();
  } catch (e) {
    return `unreadable: ${e}`;
  }
}

await stage("first");
check(choice() === "true", `first: privacy-mode.json keeps privacy mode on (${choice()})`);
let mode = -1;
try {
  mode = statSync(saved).mode & 0o777;
} catch {}
check(mode === 0o600, `first: privacy-mode.json is readable only by this user (${mode.toString(8)})`);

await stage("second");
check(choice() === "false", `second: privacy-mode.json keeps privacy mode off (${choice()})`);

writeFileSync(saved, "not json", { mode: 0o600 });
await stage("damaged");

console.log(`privacy: suite ${failed === 0 ? "passed" : "FAILED"} (${failed} failed)`);
process.exit(failed === 0 ? 0 : 1);
