// The app's native test: the real app (built with the native-test feature) on a private X server,
// against a real core with fake extensions (next/e2e/panelcore -probe), once in each mode:
// managed (the app runs the core), local (a core already running here) and remote (that core
// behind https with its own certificate authority). In each it checks that
//
// - the app connects, and the window's webview reaches every app command and gets core's status;
// - at the top of an extension page's webview every app command and Tauri plugin command is
//   refused by Tauri's command check, and the app never reaches a command from that webview;
// - from inside the page's sandboxed frame nothing reaches the app (test/probe reports through
//   its channel);
// - in managed mode, this device's extension catalog installs, swaps, rolls back, disables and
//   removes page-only packages from a local registry and from local files through the native
//   file chooser, refusing reserved IDs and a failed candidate (device.ts, device_native.rs);
// - closing the window leaves no webview and no web process, with the app still in the tray;
// - quitting stops the app's own core, and leaves a local or remote core running;
// - in a second managed run, a core that exits before the app state exists is recovered.
// - the management key rotates natively (native_test.rs, rotation_test.rs): core_call refuses
//   it, the app keeps working on the new key as the old one was kept, the key window stays open
//   when the window closes to the tray and goes when a later rotation shows its own, no webview
//   (the window's or an extension page's) and no output line holds any key the run rotated; in
//   local mode the app then starts again on the same folders and signs in with the kept key.
//
// In managed mode it then runs the window checks (window.ts, native_window.rs) in two more
// sessions, one with a tray watcher and one without.
// Then, in a session of its own (instances.ts, instances-session.ts), the single-instance
// scenario: a second launch on the same profile exits and raises the first's window, a launch on
// another profile runs as its own app, and a killed owner's stale lock is recovered.
// The signed native-tier admission check (admission.ts, admission_native.rs) runs in one more
// session, with the app built with artifact-test-trust and a test trust profile in the run's HOME,
// and the program host check (program.ts, program_native.rs) in one more with that build: a signed
// summary tool, started on demand, called and cancelled, stopped when idle and disabled, then
// (swap_native.rs) swapped to a new release while a call drains on the old one, kept when a
// candidate is incompatible, crashed into backoff and quarantine, and rolled back; its processes
// are gone after the app quits. The other sessions run a build without that
// feature; its device check shows the same profile admits nothing there.
// The privacy mode checks (privacy.ts, native_privacy.rs) run in one more session.
//
// Each run gets a fresh HOME, XDG folders and runtime folder, a session bus and an X server of its
// own (dbus-run-session, xvfb-run), and random ports. Inside that session, before the app,
// native/session.ts starts the desktop services later checks need (the portals and a tray
// watcher), checks each of them for real, and runs a disposable AppImage with
// APPIMAGE_EXTRACT_AND_RUN=1; a missing or broken service fails the run at setup (services.ts
// names the packages). Run from next/app: bun native/run.ts (needs dbus-run-session, xvfb-run,
// Go, cargo and those services).

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildAdmissionFixtures } from "./admission";
import { buildSmokeAppImage } from "./appimage";
import { buildDeviceFixtures } from "./device";
import { runInstances } from "./instances";
import { descendants, webProcesses } from "./processes";
import { buildProgramFixtures } from "./program";
import { missingPrerequisites } from "./services";

const app = join(import.meta.dir, "..");
const next = join(app, "..");
const tauri = join(app, "src-tauri");
const identifier: string = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8")).identifier;
const commands = [
  ...readFileSync(join(tauri, "src", "command_names.rs"), "utf8").matchAll(/^\s+"(\w+)",$/gm),
].map((m) => m[1] as string);
const PAGE = "ext/test/probe/probe";
/** Tauri's command check refusing a call (debug builds say where; release builds "by ACL"). */
const REFUSED = /not allowed( on window|\. Permissions associated| by ACL)/;
const TIMEOUT = 240_000;

type Mode = "managed" | "local" | "remote";
/** The session's setup failed (session.ts said what is missing or broken, and never said it was
 * ready): the app never ran, and the other modes would fail the same way. */
class SetupError extends Error {}
type Step = Record<string, unknown>;
interface Call {
  ok: boolean;
  detail: string;
}
interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}
interface Probe {
  internals: boolean;
  calls: Record<string, Call>;
  label?: string;
  url?: string;
  area?: Box;
  placed?: Box;
}

function run(cmd: string, args: string[], cwd: string) {
  const r = spawnSync(cmd, args, { cwd, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
  return r.stdout;
}

/** A core from panelcore, running until its stdin closes. */
async function startCore(
  tls: boolean,
): Promise<{ proc: ChildProcess; base: string; key: string; config: string; ca?: string }> {
  const proc = spawn("go", ["run", "./e2e/panelcore", "-probe", ...(tls ? ["-tls"] : [])], {
    cwd: next,
    stdio: ["pipe", "pipe", "inherit"],
  });
  const line = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`panelcore exited with ${code}`)));
    if (proc.stdout) createInterface({ input: proc.stdout }).once("line", resolve);
  });
  return { proc, ...JSON.parse(line) };
}

/** Sends signal to the process group pid leads (each session runs in a group of its own). */
function signalGroup(pid: number, signal: NodeJS.Signals) {
  try {
    process.kill(-pid, signal);
  } catch {
    // gone already
  }
}

/** The sessions still running, so an interrupted run can stop them. */
const sessions = new Set<number>();
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => {
    // Bounded: TERM, a grace period, then KILL for each session, before the runner exits.
    void Promise.all([...sessions].map(stopSession)).finally(() => process.exit(130));
  });
}

/** Stops a session that did not finish: TERM first, so session.ts stops the services it started,
 * then KILL for its whole group and every process still below it. */
async function stopSession(pid: number) {
  signalGroup(pid, "SIGTERM");
  await Bun.sleep(10_000);
  signalGroup(pid, "SIGKILL");
  for (let i = 0; i < 3 && descendants(pid).size > 0; i++) {
    killTree(pid);
    await Bun.sleep(500);
  }
}

/** Kills pid and everything it started: its session's bus, X server and services too. */
function killTree(pid: number) {
  for (const p of [...descendants(pid).keys(), pid]) {
    try {
      process.kill(p, "SIGKILL");
    } catch {
      // gone already
    }
  }
}

function alive(pid: number): boolean {
  try {
    const state = readFileSync(`/proc/${pid}/stat`, "utf8").split(") ")[1]?.[0];
    return state !== "Z";
  } catch {
    return false;
  }
}

/** Whether pid runs the program at path (and is not some later process with its number). */
function runs(pid: number, path: string): boolean {
  try {
    return alive(pid) && readlinkSync(`/proc/${pid}/exe`).startsWith(path);
  } catch {
    return false;
  }
}

async function managementStatus(base: string, key: string, ca?: string): Promise<number> {
  const r = await fetch(`${base}/v0/management/status`, {
    headers: { Authorization: `Bearer ${key}` },
    ...(ca ? { tls: { ca: readFileSync(ca, "utf8") } } : {}),
  } as RequestInit);
  return r.status;
}

/** The management key in core's config file: its current one, also after a rotation. */
function managementKey(config: string): string {
  return JSON.parse(readFileSync(config, "utf8")).management_key;
}

/** The window checks (window.ts), with or without a tray watcher in the session, and the privacy
 * mode checks (privacy.ts). */
type Suite = "window" | "window-no-tray" | "privacy";

/** For a local or remote core, rotation.coreConfig is its config file. rotation.root keeps the
 * run's folders for the caller to remove; with rotation.restart the app starts again on them,
 * with the connection an earlier run left. */
interface Rotation {
  coreConfig?: string;
  root?: string;
  restart?: boolean;
}

/** Runs the app once with connection as its saved connection, and checks what it reports. With
 * suite, the session runs window.ts, which runs the app through the window checks' stages.
 * earlyExit: the app's setup waits for its core's exit callback before the app state exists
 * (PLUXY_APP_TEST_EARLY_EXIT), and only the recovery of that core is checked. admission: the
 * session checks only signed native-tier admission. */
async function runApp(
  mode: Mode,
  bin: string,
  connection: object,
  base?: string,
  startup = false,
  noTray = false,
  key?: string,
  suite?: Suite,
  rotation: Rotation = {},
  admission = false,
  program = false,
  earlyExit = false,
): Promise<string[]> {
  const root = rotation.root ?? mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
  const problems: string[] = [];
  const check = (ok: unknown, what: string) => {
    if (!ok) problems.push(what);
  };
  try {
    const dirs = {
      home: join(root, "home"),
      config: join(root, "config"),
      data: join(root, "data"),
      cache: join(root, "cache"),
      state: join(root, "state"),
      runtime: join(root, "runtime"),
    };
    for (const d of Object.values(dirs)) mkdirSync(d, { recursive: true, mode: 0o700 });
    mkdirSync(join(dirs.config, identifier), { recursive: true });
    if (!rotation.restart)
      writeFileSync(join(dirs.config, identifier, "connection.json"), JSON.stringify(connection), {
        mode: 0o600,
      });
    // The main checks close the window to the tray; the window suite starts with Ask every time.
    if (!suite)
      writeFileSync(
        join(dirs.config, identifier, "window.json"),
        JSON.stringify({ close_behavior: "minimize-to-tray" }),
        { mode: 0o600 },
      );
    const quit = join(root, "quit");
    // This device's extension catalog is the same in every mode: check it once, in managed mode.
    const device = mode === "managed" && !admission && !program ? join(root, "device") : undefined;
    // A test trust profile in HOME naming curated registries: the admission session's build
    // honours it; the device check's normal build must not.
    const curated = device || admission ? join(root, "curated") : undefined;
    let nonce: string | undefined;
    // The program host check's signed summary tool, its curated registry and trust profile.
    const programDir = program ? join(root, "program") : undefined;
    if (programDir) {
      mkdirSync(programDir);
      const f = buildProgramFixtures(programDir, dirs.home, programFixture);
      nonce = f.nonce;
      writeFileSync(
        join(dirs.config, identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    if (curated) {
      mkdirSync(curated);
      if (device) mkdirSync(device);
      const a = buildAdmissionFixtures(curated, dirs.home);
      nonce = a.nonce;
      const registries = admission
        ? a.registries
        : [
            ...(buildDeviceFixtures(device as string) as { registries: object[] }).registries,
            ...a.registries.filter((r) => r.name === "curated"),
          ];
      writeFileSync(
        join(dirs.config, identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries }),
      );
    }
    const keyFile = join(root, "key-input");
    // Every key the rotation check sees, for the log audit below; it is native-test output.
    const rotatedFile = join(root, "rotated-keys");
    const wrongKey = randomUUID();
    if (key) writeFileSync(keyFile, JSON.stringify({ key, wrong: wrongKey }), { mode: 0o600 });
    // Only what the app needs: no display or session bus but the run's own.
    const env: Record<string, string> = {
      PATH: process.env.PATH ?? "/usr/bin:/bin",
      LANG: "C.UTF-8",
      HOME: dirs.home,
      XDG_CONFIG_HOME: dirs.config,
      XDG_DATA_HOME: dirs.data,
      XDG_CACHE_HOME: dirs.cache,
      XDG_STATE_HOME: dirs.state,
      XDG_RUNTIME_DIR: dirs.runtime,
      WEBKIT_DISABLE_DMABUF_RENDERER: "1",
      LIBGL_ALWAYS_SOFTWARE: "1",
      NO_AT_BRIDGE: "1",
      GTK_USE_PORTAL: "0",
      // The FUSE-free way to run an AppImage, which the session's smoke AppImage uses.
      APPIMAGE_EXTRACT_AND_RUN: "1",
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
      PLUXY_APP_TEST_STATE: join(root, "state.json"),
      PLUXY_APP_TEST_TRAY_HOLD: join(root, "tray-hold"),
      ...(device ? { PLUXY_APP_TEST_DEVICE: device, PLUXY_APP_TEST_CURATED: curated as string } : {}),
      ...(admission ? { PLUXY_APP_TEST_ADMISSION: curated as string } : {}),
      // In the app's environment, never in the tool's (program_native.rs checks its names).
      ...(programDir ? { PLUXY_APP_TEST_PROGRAM: programDir, PLUXY_TEST_PROGRAM_CANARY: randomUUID() } : {}),
      ...(nonce ? { PLUXY_TEST_TRUST_NONCE: nonce } : {}),
      ...(startup ? { PLUXY_APP_TEST_STARTUP_SUITE: identifier } : {}),
      ...(noTray ? { PLUXY_APP_TEST_NO_TRAY: "1" } : {}),
      ...(key ? { PLUXY_APP_TEST_KEY_FILE: keyFile } : {}),
      PLUXY_APP_TEST_ROTATED_FILE: rotatedFile,
      ...(rotation.coreConfig ? { PLUXY_APP_TEST_CORE_CONFIG: rotation.coreConfig } : {}),
      ...(rotation.restart ? { PLUXY_APP_TEST_ROTATION_RESTART: "1" } : {}),
      ...(suite
        ? {
            PLUXY_APP_TEST_WINDOW_BIN: bin,
            PLUXY_APP_TEST_PROBE: probe,
            PLUXY_APP_TEST_IDENTIFIER: identifier,
          }
        : {}),
      ...(earlyExit ? { PLUXY_APP_TEST_EARLY_EXIT: "1" } : {}),
    };
    // The session script starts and checks the desktop services, then runs the app.
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      probe,
      appImage,
      randomUUID(),
      suite ? join(import.meta.dir, suite === "privacy" ? "privacy.ts" : "window.ts") : bin,
    ];
    const xvfb = ["xvfb-run", "-a", "-s", "-screen 0 1280x800x24 -nolisten tcp", ...session];
    const proc = spawn("dbus-run-session", ["--", ...xvfb], {
      env,
      // Some xvfb-run versions send the program's stderr to stdout: read both.
      stdio: ["ignore", "pipe", "pipe"],
      // A process group of its own, so a run that hangs can be stopped as a whole.
      detached: true,
    });
    const steps = new Map<string, Step>();
    const reached: string[] = [];
    const assets: { label: string; path: string; status: number }[] = [];
    let before = 0;
    let after = -1;
    const pid = proc.pid ?? 0;
    sessions.add(pid);
    let ready = false; // session.ts said its setup is done and the app starts
    let attempts = 0; // the app's recovery attempts
    if (!proc.stderr || !proc.stdout) throw new Error("the app's output is not piped");
    const log: string[] = []; // the app's last lines, shown when the run fails
    const setup: string[] = []; // session.ts's report of what failed at setup
    const output: string[] = []; // every line, for the rotated-key audit once the run ends
    let suitePassed = false;
    const onLine = (line: string) => {
      output.push(line);
      if (key && [key, wrongKey].some((canary) => line.includes(canary))) {
        problems.push("management key appeared on stdout/stderr");
        return;
      }
      // window.ts's and privacy.ts's checks, each shown.
      const driver = /^(window|privacy): /.exec(line)?.[1];
      if (driver) {
        console.log(`  ${mode} | ${line}`);
        if (line.startsWith(`${driver}: suite passed`)) suitePassed = true;
        return;
      }
      // The session's service checks are always shown: they say what this run stood on.
      if (line.startsWith("native-recovery: ")) {
        console.log(`  ${mode} | ${line}`);
        return;
      }
      if (line.startsWith("native-prereq: ")) {
        console.log(`  ${mode} | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      if (line.startsWith("native-test: recovery-attempt ")) attempts++;
      const m = /^native-test: (\w+) (.*)$/.exec(line);
      if (!m) {
        if (process.env.PLUXY_APP_TEST_VERBOSE) console.error(`  ${mode} | ${line}`);
        log.push(line);
        if (log.length > 60) log.shift();
        return;
      }
      const [, step, json] = m as unknown as [string, string, string];
      const value = JSON.parse(json);
      if (step === "reached") {
        reached.push(value.webview);
        return;
      }
      if (step === "asset") {
        assets.push(value);
        return;
      }
      steps.set(step, value);
      if (step === "confirmations") console.log(`  ${mode} | native confirmations: ${JSON.stringify(value)}`);
      if (step === "frame") before = webProcesses(pid);
      if (step === "closed") {
        // Give the web processes a moment to exit, then let the app quit.
        setTimeout(() => {
          after = webProcesses(pid);
        }, 3000);
      }
    };
    for (const stream of [proc.stdout, proc.stderr]) createInterface({ input: stream }).on("line", onLine);
    let stopping: Promise<void> | undefined;
    const code = await new Promise<number | null>((resolve) => {
      const timer = setTimeout(() => {
        problems.push(`the app did not finish within ${TIMEOUT / 1000}s`);
        stopping = stopSession(pid);
      }, TIMEOUT);
      // "close", not "exit": it comes after the output is read, so the "ready" line is not missed.
      proc.on("close", (c) => {
        clearTimeout(timer);
        resolve(c);
      });
    });
    await stopping;
    sessions.delete(pid);

    // Only session.ts's own "ready" line says the app ran: an exit code alone could be the app's.
    if (!ready) {
      throw new SetupError(
        setup.length > 0 ? setup.join("\n") : `the session ended (${code}) before it was ready`,
      );
    }
    check(code === 0, `the app exited with ${code}`);
    const rotated = existsSync(rotatedFile)
      ? readFileSync(rotatedFile, "utf8")
          .split("\n")
          .filter((k) => k.length > 0)
      : [];
    check(
      !output.some((line) => rotated.some((k) => line.includes(k))),
      "a management key from the rotation check appeared on stdout/stderr",
    );
    if (suite) {
      check(suitePassed, `the ${suite} checks failed`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode}: ${suite} ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);
    if (earlyExit) {
      // Without this the run would not test the early exit: the callback could have found the state.
      const held = steps.get("early_exit_held");
      check(
        held?.callback_ran === true,
        `the core's exit callback did not run first: ${JSON.stringify(held)}`,
      );
      const end = steps.get("early_exit") as
        | { view?: { core?: { state?: string; pid?: number } } }
        | undefined;
      const core = end?.view?.core;
      check(
        core?.state === "running" && core.pid,
        `a core that exited before the app state existed was not recovered: ${JSON.stringify(end)}`,
      );
      check(attempts === 1, `${attempts} recovery attempts, want 1`);
      await Bun.sleep(1000);
      // Only a recovered core can outlive the app; its absence is the check above.
      check(!core?.pid || !alive(core.pid), `the recovered core ${core?.pid} still runs after the app quit`);
      if (problems.length > 0)
        console.log(`${mode} early exit: the app's last lines:\n  ${log.join("\n  ")}`);
      else
        console.log(
          `  ${mode} | native-recovery: a core that exited before the app state existed was recovered`,
        );
      return problems;
    }

    if (rotation.restart) {
      const restarted = steps.get("rotation_restart");
      check(
        restarted?.signed_in === true && restarted?.status === 200 && Number(restarted?.refused) > 0,
        `the restart after rotation did not sign in on the kept key: ${JSON.stringify(restarted)}`,
      );
      console.log(`  ${mode} | after restart: ${JSON.stringify(restarted ?? {})}`);
      console.log(`${mode}: restart ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (program) {
      const p = steps.get("program") as
        | { passed?: boolean; program?: string; quit_pids?: number[] }
        | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(p?.passed === true, `the program host check did not complete: ${failure}`);
      // The swap check runs on from where the program check left (swap_native.rs).
      const w = steps.get("swap") as typeof p;
      check(w?.passed === true, `the swap check did not complete: ${failure}`);
      // After the app quit, neither tool the checks left running nor its child runs.
      await Bun.sleep(1000);
      for (const [what, r] of [
        ["program", p],
        ["swap", w],
      ] as const) {
        const left = (r?.quit_pids ?? []).filter((pid) => runs(pid, r?.program ?? ""));
        check(
          r?.quit_pids?.length === 2,
          `the ${what} check named no processes to watch: ${JSON.stringify(r)}`,
        );
        check(left.length === 0, `the tool's processes ${left.join(", ")} still run after the app quit`);
        for (const pid of left) killTree(pid);
      }
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} program ${JSON.stringify(p ?? {})}`);
      console.log(`${mode} swap ${JSON.stringify(w ?? {})}`);
      console.log(`${mode}: program ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (admission) {
      const a = steps.get("admission");
      check(a?.passed === true, "the native admission check did not complete");
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} admission ${JSON.stringify(a ?? {})}`);
      console.log(`${mode}: admission ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (startup) {
      check(
        log.some((l) => l.startsWith("startup preferences passed:")),
        "startup suite gave no success report",
      );
      console.log(`${mode}: startup ${problems.length === 0 ? "ok" : "FAILED"}`);
      for (const line of log) console.log(line);
      return problems;
    }

    check(
      steps.get("recovery")?.ok === true,
      `tray/recovery check: ${JSON.stringify(steps.get("recovery"))}`,
    );

    if (key) {
      const entry = steps.get("keyentry");
      for (const field of [
        "good",
        "bad_preserved",
        "cancel_preserved",
        "session",
        "remember",
        "webviews_clean",
        "events_clean",
      ])
        check(entry?.[field] === true, `native key entry did not prove ${field}`);
      console.log(`  ${mode} | native key entry: ${JSON.stringify(entry ?? {})}`);
    }

    // The window: connected in this mode, every command reached, core's status through the app.
    const main = steps.get("main") as Probe | undefined;
    check(main?.internals, "the window's webview has no IPC");
    const got = main?.calls.connection_get;
    const view = got?.ok ? JSON.parse(got.detail) : undefined;
    check(view?.mode === mode, `the window's connection is ${JSON.stringify(view)}`);
    check(view?.signed_in === true, "the app is not signed in");
    check(base === undefined || view?.target === base, `the app's target is ${view?.target}, not ${base}`);
    for (const [cmd, c] of Object.entries(main?.calls ?? {})) {
      if (cmd !== "connection_get")
        check(!/not allowed/i.test(c.detail), `the window was refused ${cmd}: ${c.detail}`);
    }
    check((steps.get("status") as { status?: number })?.status === 200, "core's status did not come through");

    // The page webview: Tauri refuses every command, and the app reaches none from it.
    const page = steps.get("page") as Probe | undefined;
    check(page?.internals, "the page webview has no IPC, so the probe proves nothing");
    check(page?.url === "pluxy-page://localhost/", `the page webview shows ${page?.url}`);
    // The page webview covers the page area the window drew for it, not some other place.
    const area = page?.area;
    const placed = page?.placed;
    const near = (a?: number, b?: number) => a !== undefined && b !== undefined && Math.abs(a - b) <= 2;
    check(
      area && placed && (["x", "y", "width", "height"] as const).every((k) => near(area[k], placed[k])),
      `the page webview is at ${JSON.stringify(placed)}, the page area at ${JSON.stringify(area)}`,
    );
    for (const cmd of commands) check(page?.calls[cmd], `the page webview's probe did not try ${cmd}`);
    for (const [cmd, c] of Object.entries(page?.calls ?? {})) {
      check(!c.ok && REFUSED.test(c.detail), `${cmd} from the page webview: ${JSON.stringify(c)}`);
    }
    check(reached.length > 0, "no command was reached at all");
    check(
      reached.every((w) => w === "main"),
      `commands were reached from ${[...new Set(reached)].join(", ")}`,
    );

    // The page's frame, from its own report.
    const frame = steps.get("frame") as Record<string, { status: string; detail: string }> | undefined;
    check(frame && Object.keys(frame).length >= 10, "the page sent no report");
    for (const [what, r] of Object.entries(frame ?? {})) {
      const fine = r.status === "blocked" || (r.status === "attempted" && what === "native message handler");
      check(fine, `from the page's frame, "${what}" ${r.status}: ${r.detail}`);
    }

    for (const [step, expected] of [
      ["ingress_site", { attempts: 1, arrivals: 0, blocked: true }],
      ["ingress_recovery", { error: "unavailable", control: true }],
      [
        "ingress_navigation",
        { native_closed: true, wrapper_closed_suppressed: true, status: 409, window_closed: true },
      ],
      ["ingress_closed", { closed: 204, subsequent_call: 409 }],
      [
        "http_navigation",
        {
          native_closed: true,
          status: 409,
          wrapper_closed_suppressed: true,
          window_closed: true,
          fragment_open: true,
        },
      ],
    ] as const) {
      const value = steps.get(step);
      check(
        value && Object.entries(expected).every(([key, want]) => value[key] === want),
        `${step} evidence missing or wrong: ${JSON.stringify(value)}`,
      );
      console.log(`${mode} ${step} ${JSON.stringify(value)}`);
    }

    const ingress = steps.get("ingress");
    check(ingress?.control === true, "the native ingress check did not finish");
    if (ingress) console.log(`${mode} native ingress ${JSON.stringify(ingress)}`);

    check(steps.get("confirmations")?.passed === true, "native confirmation checks did not complete");

    const rotationStep = steps.get("rotation");
    const managed = mode === "managed";
    const wanted = {
      refused: true,
      control: true,
      cancelled: true,
      rotated: true,
      old_refused: true,
      new_works: true,
      kept: managed ? "core" : "device",
      session: !managed,
      recovery: !managed,
      webviews_clean: true,
      page_audited: !managed,
      outlived_window: !managed,
      replaced: !managed,
      connection_shown: !managed,
      no_launch_notice: true,
    };
    check(
      rotationStep && Object.entries(wanted).every(([k, want]) => rotationStep[k] === want),
      `native rotation evidence missing or wrong: ${JSON.stringify(rotationStep)}`,
    );
    // The key the app started with and each rotated one (two in managed mode, four otherwise).
    check(rotated.length === (managed ? 2 : 4), `the rotation check recorded ${rotated.length} keys`);
    console.log(`  ${mode} | native rotation: ${JSON.stringify(rotationStep ?? {})}`);
    // This device's catalog: the driver's own checks passed, and the asset scheme served the
    // swapped-in release to its page webview and refused that webview the replaced one's files.
    if (device) {
      const d = steps.get("device") as
        | { passed?: boolean; v2_label?: string; digests?: Record<string, string>; normal_build?: string }
        | undefined;
      // Without its line, the app's failed step says where the check stopped and why.
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(d?.passed === true, `the device catalog check did not complete: ${failure}`);
      // This normal build refused the curated registry's signed native tool despite the HOME's
      // valid test trust profile, for want of roots.
      check(
        d?.normal_build?.includes("not configured"),
        `the normal build did not refuse the curated native tool: ${JSON.stringify(d?.normal_build)}`,
      );
      const dg = d?.digests ?? {};
      const served = (label: string | undefined, digest: string | undefined, status: number) =>
        assets.some((a) => a.label === label && a.path.includes(`/${digest}/`) && a.status === status);
      check(
        dg.notes2 && served(d?.v2_label, dg.notes2, 200),
        `the asset scheme did not serve 2.0.0 to ${d?.v2_label}`,
      );
      check(
        dg.notes1 && served(d?.v2_label, dg.notes1, 404),
        `the asset scheme did not refuse 1.0.0 to ${d?.v2_label}`,
      );
      for (const k of ["spoof", "pluxy_local", "notes3"])
        check(
          dg[k] && !assets.some((a) => a.path.includes(`/${dg[k]}/`) && a.status === 200),
          `the asset scheme served refused package ${k}`,
        );
      console.log(
        `${mode} device ${JSON.stringify(d ?? { failed: failure })} (${assets.length} asset requests)`,
      );
    }

    // Closing the window frees its webviews; the app stays in the tray.
    const closed = steps.get("closed") as
      | { webviews: string[]; tray: boolean; core_pid?: number }
      | undefined;
    check(closed?.tray, "the app has no tray icon");
    check(closed?.webviews.length === 0, `webviews left after closing: ${closed?.webviews}`);
    check(before > 0, "no web process seen while the window was open");
    check(after === 0, `${after} web processes left after closing the window`);
    if (mode === "managed") {
      check(closed?.core_pid, "the app ran no core");
      // The tray checks killed that core; the one they left running must not outlive the app.
      const last = (steps.get("recovery") as { core_pid?: number } | undefined)?.core_pid;
      check(last, "the tray checks left no owned core running before quit");
      await Bun.sleep(1000);
      check(last && !alive(last), `the app's core ${last} still runs after it quit`);
    }
    if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
    console.log(
      `${mode}: ${problems.length === 0 ? "ok" : "FAILED"} (web processes ${before} open, ${after} after close; ` +
        `${reached.length} commands reached, all from main: ${reached.every((w) => w === "main")})`,
    );
  } finally {
    // A folder left behind is a problem of its own; it must not hide this run's report.
    if (!rotation.root)
      await removeRun(root).catch((e) => problems.push(`the run folder ${root} was not removed: ${e}`));
  }
  return problems;
}

/** Lets this user remove the device's package store the app left in its run folder, after the
 * app has quit: the store keeps its installed folders read-only, so they cannot be emptied as
 * they are. Only folders under the app's `device-packages` change. They are path-based changes,
 * safe here because the app made that tree, holds no links or mounts in it (archives with links
 * are refused), and no longer runs. */
function makeRemovable(root: string) {
  const walk = (dir: string) => {
    let entries: string[];
    try {
      const st = lstatSync(dir);
      if (!st.isDirectory()) return;
      if ((st.mode & 0o700) !== 0o700) chmodSync(dir, 0o700);
      entries = readdirSync(dir);
    } catch {
      return; // gone: rmSync reports what it cannot remove
    }
    for (const e of entries) walk(join(dir, e));
  };
  walk(join(root, "data", identifier, "device-packages"));
}

/** Removes a run's folder. A desktop service the run's bus started (such as the document portal,
 * which mounts itself in the runtime folder) can outlive the app for a moment. */
async function removeRun(root: string) {
  for (let i = 0; ; i++) {
    try {
      makeRemovable(root);
      rmSync(root, { recursive: true, force: true });
      return;
    } catch (e) {
      if (i === 20) throw e;
      if (i === 10) {
        for (const tool of ["fusermount3", "fusermount"])
          spawnSync(tool, ["-u", "-z", join(root, "runtime", "doc")]);
      }
      await Bun.sleep(500);
    }
  }
}

// The services and tools the session needs, before anything is built.
const missing = missingPrerequisites();
if (missing.length > 0) {
  console.error(
    `native test setup failed: services or tools it needs are not installed (Ubuntu 24.04 package names):\n- ${missing.join("\n- ")}`,
  );
  process.exit(1);
}

run("bun", ["run", "build"], app);
// The app, and the probe the session uses to check the desktop services (examples/session_probe.rs).
run(
  "cargo",
  [
    "build",
    "--locked",
    "--features",
    "native-test",
    "--bin",
    "pluxy-app",
    "--example",
    "session_probe",
    "--example",
    "program_fixture",
  ],
  tauri,
);
const targetDir = resolvePath(tauri, process.env.CARGO_TARGET_DIR ?? "target");
const probe = join(targetDir, "debug", "examples", "session_probe");
// The disposable summary tool the program host check packs into a signed package.
const programFixture = join(targetDir, "debug", "examples", "program_fixture");
// The build without artifact-test-trust, kept aside before the build with it replaces it.
const binDir = mkdtempSync(join(tmpdir(), "pluxy-app-native-bin-"));
const bin = join(binDir, "pluxy-app");
try {
  copyFileSync(join(targetDir, "debug", "pluxy-app"), bin);
  run(
    "cargo",
    ["build", "--locked", "--features", "native-test,artifact-test-trust", "--bin", "pluxy-app"],
    tauri,
  );
} catch (e) {
  rmSync(binDir, { recursive: true, force: true });
  throw e;
}
const trustBin = join(targetDir, "debug", "pluxy-app");
const appImageDir = mkdtempSync(join(tmpdir(), "pluxy-app-native-appimage-"));
const problems: string[] = [];
let appImage = ""; // the smoke AppImage the sessions run
let setupFailure: string | undefined;

try {
  appImage = await buildSmokeAppImage(appImageDir);
  const prepared = mkdtempSync(join(tmpdir(), "pluxy-app-native-core-"));
  try {
    const { core } = JSON.parse(run("go", ["run", "./e2e/panelcore", "-probe", "-prepare", prepared], next));
    const p = await runApp("managed", bin, { mode: "managed", core_path: core });
    p.push(...(await runApp("managed", bin, { mode: "managed", core_path: core }, undefined, true)));
    p.push(...(await runApp("managed", bin, { mode: "managed", core_path: core }, undefined, true, true)));
    p.push(
      ...(await runApp(
        "managed",
        trustBin,
        { mode: "managed", core_path: core },
        undefined,
        false,
        false,
        undefined,
        undefined,
        {},
        true,
      )),
    );
    p.push(
      ...(await runApp(
        "managed",
        trustBin,
        { mode: "managed", core_path: core },
        undefined,
        false,
        false,
        undefined,
        undefined,
        {},
        false,
        true,
      )),
    );
    for (const suite of ["window", "window-no-tray", "privacy"] as const)
      p.push(
        ...(await runApp(
          "managed",
          bin,
          { mode: "managed", core_path: core },
          undefined,
          false,
          suite === "window-no-tray",
          undefined,
          suite,
        )),
      );
    problems.push(...p.map((x) => `managed: ${x}`));
    // The first start runs a stand-in that says where it listens and exits at once; the next
    // start, recovery's, runs the real core.
    const early = join(prepared, "early-exit.sh");
    writeFileSync(
      early,
      `#!/bin/sh\nif [ ! -e "$0.ran" ]; then\n  : > "$0.ran"\n  echo 'pluxy: listening on http://127.0.0.1:1' >&2\n  exit 7\nfi\nexec '${core}' "$@"\n`,
      { mode: 0o755 },
    );
    const e = await runApp(
      "managed",
      bin,
      { mode: "managed", core_path: early },
      undefined,
      false,
      false,
      undefined,
      undefined,
      {},
      false,
      false,
      true,
    );
    problems.push(...e.map((x) => `managed, early exit: ${x}`));

    // Two launches of the app on one profile: one app, one core, the first one's window raised.
    const inst = await runInstances({
      bin,
      probe,
      appImage,
      core,
      identifier,
      timeoutMs: TIMEOUT,
      sessions,
      stopSession,
      removeRun,
    });
    if (inst.setup !== undefined) throw new SetupError(inst.setup);
    problems.push(...inst.problems.map((x) => `instances: ${x}`));
  } finally {
    rmSync(prepared, { recursive: true, force: true });
  }

  for (const mode of ["local", "remote"] as const) {
    const c = await startCore(mode === "remote");
    const kept = mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
    try {
      const conn = { mode, url: c.base, key: c.key, ...(c.ca ? { ca_file: c.ca } : {}) };
      const rotation = { coreConfig: c.config, root: kept };
      const p = await runApp(mode, bin, conn, c.base, false, false, c.key, undefined, rotation);
      // Rotated and kept: the app starts again on the same folders, signed in on the new key.
      if (mode === "local")
        p.push(
          ...(await runApp(mode, bin, conn, c.base, false, false, undefined, undefined, {
            ...rotation,
            restart: true,
          })),
        );
      const current = { ...conn, key: managementKey(c.config) };
      p.push(...(await runApp(mode, bin, current, c.base, true)));
      // The app only connected: its quitting leaves this core running.
      if ((await managementStatus(c.base, current.key, c.ca)) !== 200)
        p.push("the core stopped when the app quit");
      problems.push(...p.map((x) => `${mode}: ${x}`));
    } finally {
      await removeRun(kept);
      c.proc.stdin?.end();
      await new Promise((r) => c.proc.once("exit", r));
    }
  }
} catch (e) {
  if (!(e instanceof SetupError)) throw e;
  setupFailure = e.message;
} finally {
  rmSync(appImageDir, { recursive: true, force: true });
  rmSync(binDir, { recursive: true, force: true });
}

if (setupFailure !== undefined) {
  console.error(`\nnative test setup failed:\n${setupFailure}`);
  process.exit(1);
}
if (problems.length > 0) {
  console.error(`\n${problems.length} problems:\n- ${problems.join("\n- ")}`);
  process.exit(1);
}
console.log("native test passed in all three modes and the single-instance scenario");
