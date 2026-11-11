#!/usr/bin/env bun
// The window checks' driver. run.ts gives it to session.ts in place of the app, so it runs inside
// one run's session (its HOME, X server, bus and, unless PLUXY_APP_TEST_NO_TRAY is set, tray
// watcher). It starts the app (PLUXY_APP_TEST_WINDOW_BIN, built with native-test) once per stage
// with PLUXY_APP_TEST_WINDOW=<stage>; the stages themselves are src-tauri/src/native_window.rs.
// Between stages it checks what the app left: its exit code, the saved window.json, the app's own
// core stopped, and whether the app asked the window what closing does.
//
// It prints `window: ok <check>` or `window: FAILED <check>` for every check, then
// `window: suite passed` or `window: suite FAILED`, and exits 0 only when every check passed.

import { spawn } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";

const bin = process.env.PLUXY_APP_TEST_WINDOW_BIN;
const identifier = process.env.PLUXY_APP_TEST_IDENTIFIER;
const configHome = process.env.XDG_CONFIG_HOME;
if (!bin || !identifier || !configHome) {
  console.error("window.ts needs PLUXY_APP_TEST_WINDOW_BIN, PLUXY_APP_TEST_IDENTIFIER and XDG_CONFIG_HOME");
  process.exit(2);
}
const noTray = process.env.PLUXY_APP_TEST_NO_TRAY === "1";
const settings = join(configHome, identifier, "window.json");
const STAGE_TIMEOUT = 150_000;

let failed = 0;
function check(ok: unknown, what: string) {
  console.log(`window: ${ok ? "ok" : "FAILED"} ${what}`);
  if (!ok) failed++;
}

interface Stage {
  code: number | null;
  report?: { stage: string; failed: number; core_pid?: number | null };
  prompts: number;
}

let child: ReturnType<typeof spawn> | undefined;
for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => {
    child?.kill("SIGKILL");
    process.exit(128);
  });
}

/** Runs the app through one stage and reports what it printed. */
async function stage(name: string): Promise<Stage> {
  const proc = spawn(bin as string, [], {
    env: { ...process.env, PLUXY_APP_TEST_WINDOW: name },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child = proc;
  const result: Stage = { code: null, prompts: 0 };
  const lines: string[] = [];
  const onLine = (line: string) => {
    lines.push(line);
    if (lines.length > 80) lines.shift();
    const m = /^native-test: (window_\w+) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string);
    if (m[1] === "window_check") check(value.ok, `${name}: ${value.what}`);
    if (m[1] === "window_report") result.report = value;
    if (m[1] === "window_prompt") result.prompts++;
  };
  for (const stream of [proc.stdout, proc.stderr]) createInterface({ input: stream }).on("line", onLine);
  result.code = await new Promise<number | null>((resolve) => {
    const timer = setTimeout(() => {
      check(false, `${name}: the app finished within ${STAGE_TIMEOUT / 1000}s`);
      proc.kill("SIGKILL");
    }, STAGE_TIMEOUT);
    proc.on("close", (code) => {
      clearTimeout(timer);
      resolve(code);
    });
  });
  child = undefined;
  check(result.code === 0, `${name}: the app exited with 0 (${result.code})`);
  check(result.report?.stage === name, `${name}: the stage reached its last close`);
  if (failed > 0) console.log(`window: ${name}: the app's last lines:\n  ${lines.join("\n  ")}`);
  return result;
}

function alive(pid: number): boolean {
  try {
    return readFileSync(`/proc/${pid}/stat`, "utf8").split(") ")[1]?.[0] !== "Z";
  } catch {
    return false;
  }
}

/** The app quit: its own core is stopped too. */
async function coreStopped(name: string, s: Stage) {
  const pid = s.report?.core_pid;
  check(typeof pid === "number", `${name}: the app ran its own core (${pid})`);
  await Bun.sleep(1000);
  check(typeof pid === "number" && !alive(pid), `${name}: quitting stopped the app's own core`);
}

function saved(): { close_behavior?: string; geometry?: Record<string, number> } {
  try {
    return JSON.parse(readFileSync(settings, "utf8"));
  } catch {
    return {};
  }
}

if (noTray) {
  for (const choice of ["minimize-to-tray", "ask"]) {
    writeFileSync(settings, JSON.stringify({ close_behavior: choice }), { mode: 0o600 });
    const s = await stage("no-tray");
    check(s.prompts === 0, `no-tray (${choice}): closing quit without asking (${s.prompts} prompts)`);
    await coreStopped(`no-tray (${choice})`, s);
  }
} else {
  const first = await stage("first");
  check(
    first.prompts === 3,
    `first: Ask every time asked at each of its three closes (${first.prompts} prompts)`,
  );
  await coreStopped("first", first);
  const kept = saved();
  check(kept.close_behavior === "exit", `first: window.json keeps Exit App (${JSON.stringify(kept)})`);
  check(kept.geometry !== undefined, `first: window.json keeps the window's size and place`);

  const second = await stage("second");
  check(second.prompts === 1, `second: Ask every time asked once (${second.prompts} prompts)`);
  await coreStopped("second", second);
  check(
    saved().close_behavior === "exit",
    `second: Remember this choice kept Exit App (${saved().close_behavior})`,
  );

  const third = await stage("third");
  check(third.prompts === 0, `third: the remembered Exit App quit without asking (${third.prompts} prompts)`);
  await coreStopped("third", third);
}

console.log(
  `window: suite ${failed === 0 ? "passed" : "FAILED"} (${noTray ? "no tray" : "tray"}, ${failed} failed)`,
);
process.exit(failed === 0 ? 0 : 1);
