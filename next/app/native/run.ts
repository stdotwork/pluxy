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
// - closing the window leaves no webview and no web process, with the app still in the tray;
// - quitting stops the app's own core, and leaves a local or remote core running.
//
// Then, in a session of its own (instances.ts, instances-session.ts), the single-instance
// scenario: a second launch on the same profile exits and raises the first's window, a launch on
// another profile runs as its own app, and a killed owner's stale lock is recovered.
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
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildSmokeAppImage } from "./appimage";
import { runInstances } from "./instances";
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
): Promise<{ proc: ChildProcess; base: string; key: string; ca?: string }> {
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

/** The processes among pid's descendants, with their names. */
function descendants(pid: number): Map<number, string> {
  const parent = new Map<number, number>();
  const name = new Map<number, string>();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const stat = readFileSync(`/proc/${d}/stat`, "utf8");
      const close = stat.lastIndexOf(")");
      name.set(Number(d), stat.slice(stat.indexOf("(") + 1, close));
      parent.set(Number(d), Number(stat.slice(close + 2).split(" ")[1]));
    } catch {
      // gone already
    }
  }
  const found = new Map<number, string>();
  for (const [p, comm] of name) {
    for (let q = parent.get(p); q !== undefined && q > 1; q = parent.get(q)) {
      if (q === pid) {
        found.set(p, comm);
        break;
      }
    }
  }
  return found;
}

/** The web processes among pid's descendants. */
function webProcesses(pid: number): number {
  return [...descendants(pid).values()].filter((comm) => comm.startsWith("WebKitWebProces")).length;
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

async function managementStatus(base: string, key: string, ca?: string): Promise<number> {
  const r = await fetch(`${base}/v0/management/status`, {
    headers: { Authorization: `Bearer ${key}` },
    ...(ca ? { tls: { ca: readFileSync(ca, "utf8") } } : {}),
  } as RequestInit);
  return r.status;
}

/** Runs the app once with connection as its saved connection, and checks what it reports. */
async function runApp(mode: Mode, bin: string, connection: object, base?: string): Promise<string[]> {
  const root = mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
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
    writeFileSync(join(dirs.config, identifier, "connection.json"), JSON.stringify(connection), {
      mode: 0o600,
    });
    const quit = join(root, "quit");
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
    };
    // The session script starts and checks the desktop services, then runs the app.
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      probe,
      appImage,
      randomUUID(),
      bin,
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
    let before = 0;
    let after = -1;
    const pid = proc.pid ?? 0;
    sessions.add(pid);
    let ready = false; // session.ts said its setup is done and the app starts
    if (!proc.stderr || !proc.stdout) throw new Error("the app's output is not piped");
    const log: string[] = []; // the app's last lines, shown when the run fails
    const setup: string[] = []; // session.ts's report of what failed at setup
    const onLine = (line: string) => {
      // The session's service checks are always shown: they say what this run stood on.
      if (line.startsWith("native-prereq: ")) {
        console.log(`  ${mode} | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
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
      steps.set(step, value);
      if (step === "frame") before = webProcesses(pid);
      if (step === "closed") {
        // Give the web processes a moment to exit, then let the app quit.
        setTimeout(() => {
          after = webProcesses(pid);
          writeFileSync(quit, "");
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
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);

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
      await Bun.sleep(1000);
      check(closed?.core_pid && !alive(closed.core_pid), "the app's core still runs after it quit");
    }
    if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
    console.log(
      `${mode}: ${problems.length === 0 ? "ok" : "FAILED"} (web processes ${before} open, ${after} after close; ` +
        `${reached.length} commands reached, all from main: ${reached.every((w) => w === "main")})`,
    );
  } finally {
    await removeRun(root);
  }
  return problems;
}

/** Removes a run's folder. A desktop service the run's bus started (such as the document portal,
 * which mounts itself in the runtime folder) can outlive the app for a moment. */
async function removeRun(root: string) {
  for (let i = 0; ; i++) {
    try {
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
  ["build", "--locked", "--features", "native-test", "--bin", "pluxy-app", "--example", "session_probe"],
  tauri,
);
const targetDir = resolvePath(tauri, process.env.CARGO_TARGET_DIR ?? "target");
const bin = join(targetDir, "debug", "pluxy-app");
const probe = join(targetDir, "debug", "examples", "session_probe");
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
    problems.push(...p.map((x) => `managed: ${x}`));

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
    try {
      const conn = { mode, url: c.base, key: c.key, ...(c.ca ? { ca_file: c.ca } : {}) };
      const p = await runApp(mode, bin, conn, c.base);
      // The app only connected: its quitting leaves this core running.
      if ((await managementStatus(c.base, c.key, c.ca)) !== 200) p.push("the core stopped when the app quit");
      problems.push(...p.map((x) => `${mode}: ${x}`));
    } finally {
      c.proc.stdin?.end();
      await new Promise((r) => c.proc.once("exit", r));
    }
  }
} catch (e) {
  if (!(e instanceof SetupError)) throw e;
  setupFailure = e.message;
} finally {
  rmSync(appImageDir, { recursive: true, force: true });
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
