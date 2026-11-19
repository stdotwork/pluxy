// The tools shard of the Windows and macOS native test (platform.ts, PLATFORM_SHARDS in
// shards.ts): the sessions run.ts runs on Linux for signed tools, in the real app built with
// native-test and artifact-test-trust, against a test core in local mode
// (`go run ./e2e/panelcore -probe`, as dialogs.ts starts it):
//
// - admission: signed native-tier admission (admission_native.rs, fixtures from admission.ts), and
//   the updater gate of this dev app, which refuses it for its identifier;
// - program: the program host, then the swap (program_native.rs, swap_native.rs; program.ts): and
//   after the app quit, none of the tool processes each names still runs;
// - routing: cross-package routing, then declared jobs (routing_native.rs, jobs_native.rs;
//   routing.ts);
// - launcher: the execution program and the launcher with managed launches
//   (launcher_platform_native.rs; launcher.ts), with the fixtures' bin first on PATH and a canary
//   variable no launch may get; after the app quit, a file named stop in the fixtures' work folder
//   ends any agent left running.
//
// Each session gets a fresh app state and a private temporary HOME holding the test trust profile
// (the app's updater gate and trust store read it from HOME). On macOS the app's folders come from
// that HOME too. On Windows they are the account's known folders, which no environment variable
// moves: a session refuses to start when the app's roaming or local folder exists, and removes only
// the folders it made. HOME is the only folder variable set there: the program check compares the
// tool's APPDATA and LOCALAPPDATA with the app's own.
//
// This test never kills or signals a process by its ID on Windows or macOS: the app it started is
// stopped through its own child object, and a tool process left running is reported, not stopped.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildAdmissionFixtures, EXE, PROFILE_FILE } from "./admission";
import { buildLauncherFixtures } from "./launcher";
import { buildProgramFixtures } from "./program";
import { buildRoutingFixtures } from "./routing";

/** The sessions of this shard, by their names in shards.ts. */
export const TOOL_SESSIONS = ["admission", "program", "routing", "launcher"] as const;
export type ToolSession = (typeof TOOL_SESSIONS)[number];

/** What platform.ts gives these sessions. */
export interface Context {
  /** The app built with native-test and artifact-test-trust. */
  bin: string;
  /** Cargo's target folder, which holds the fixture tool and, under app-tools, the execution
   * program. */
  targetDir: string;
  identifier: string;
  check: (ok: unknown, what: string) => void;
  say: (line: string) => void;
  /** Windows: a new folder whose access list lets in only this user, SYSTEM and Administrators. */
  privateFolder: () => string;
  /** Windows: removes path once the WebView2 processes using webview have exited. */
  removeWhenFree: (path: string, webview: string) => void;
  powershell: (script: string) => { status: number | null; out: string };
}

const windows = process.platform === "win32";
const next = resolvePath(import.meta.dir, "..", "..");
const PAGE = "ext/test/probe/probe";
/** The updater key in the trust profile: this dev app has no test package to trust. */
const UPDATER_KEY = "no-test-package";
/** One app run: four minutes on Linux, with room for slower runners. */
const TIMEOUT = 600_000;
/** The test core's start: `go run` builds it first, once a minute or two on a runner. */
const CORE_START = 180_000;
/** How long a process this session stopped, or asked to stop, may take to end. */
const STOP_MS = 30_000;

/** Waits at most ms for proc's event (`exit`, or `close` once its output is read too); whether it
 * came. */
function within(proc: ChildProcess, event: "exit" | "close", ms: number): Promise<boolean> {
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(t);
      resolve(true);
    };
    const t = setTimeout(() => {
      proc.off(event, done);
      resolve(false);
    }, ms);
    proc.once(event, done);
  });
}

/** Ends the test core: closes its input, which ends it once it runs, and waits up to STOP_MS for
 * `go` to exit; then stops it (only the process this session started) and waits up to STOP_MS
 * more. Whether it exited. */
async function stopCore(proc: ChildProcess): Promise<boolean> {
  const ended = () => proc.exitCode !== null || proc.signalCode !== null;
  proc.stdin?.end();
  if (ended() || (await within(proc, "exit", STOP_MS))) return true;
  proc.kill();
  return ended() || within(proc, "exit", STOP_MS);
}

/** The account's known folders on Windows, as the app finds them (SHGetKnownFolderPath). */
function knownFolders(ctx: Context): { roaming: string; local: string } {
  const r = ctx.powershell(
    "[Environment]::GetFolderPath('ApplicationData'); [Environment]::GetFolderPath('LocalApplicationData')",
  );
  const out = r.out
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  if (r.status !== 0 || out.length !== 2)
    throw new Error(`PowerShell gave the known folders as ${JSON.stringify(r.out)}`);
  return { roaming: out[0] as string, local: out[1] as string };
}

/** Starts the test core and waits at most CORE_START for its first line. Past that, it ends the
 * core (`stopCore`) and fails. */
async function startCore(): Promise<{ proc: ChildProcess; base: string; key: string }> {
  const proc = spawn("go", ["run", "./e2e/panelcore", "-probe"], {
    cwd: next,
    stdio: ["pipe", "pipe", "inherit"],
  });
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const line = await new Promise<string>((resolve, reject) => {
      proc.once("error", reject);
      proc.once("exit", (code) => reject(new Error(`panelcore exited with ${code}`)));
      if (proc.stdout) createInterface({ input: proc.stdout }).once("line", resolve);
      timer = setTimeout(
        () => reject(new Error(`the test core did not start within ${CORE_START / 1000} s`)),
        CORE_START,
      );
    });
    return { proc, ...JSON.parse(line) };
  } catch (e) {
    if (!(await stopCore(proc))) throw new Error(`${(e as Error).message}; and it did not exit once stopped`);
    throw e;
  } finally {
    clearTimeout(timer);
  }
}

/** env with PATH's value replaced, under the name the environment has it (`Path` on Windows). */
function withPath(
  env: Record<string, string | undefined>,
  first: string,
): Record<string, string | undefined> {
  const name = Object.keys(env).find((k) => k.toUpperCase() === "PATH") ?? "PATH";
  return { ...env, [name]: `${first}${delimiter}${env[name] ?? ""}` };
}

/** The program path of process pid, if it runs: Windows from PowerShell's Get-Process (no output
 * when there is no such process), macOS from `ps -p <pid> -o comm=`. On macOS this relies on ps
 * giving a process's full program path as `comm`, as it does for one started by that path (the
 * host starts a tool by its path, and the tool its child by `current_exe`); a process started by
 * a bare name would show only that name and not match. */
function programOf(ctx: Context, pid: number): string | undefined {
  if (!Number.isInteger(pid) || pid <= 0) return undefined;
  if (windows) {
    const r = ctx.powershell(
      `$p = Get-Process -Id ${pid} -ErrorAction SilentlyContinue; if ($p) { $p.Path }`,
    );
    const path = r.out.trim();
    return r.status === 0 && path ? path : undefined;
  }
  const r = spawnSync("ps", ["-p", String(pid), "-o", "comm="], { encoding: "utf8" });
  const path = (r.stdout ?? "").trim();
  return r.status === 0 && path ? path : undefined;
}

/** Whether two program paths name the same file: on Windows without case or a `\\?\` prefix; on
 * macOS also after resolving links (/var is /private/var). */
function samePath(a: string, b: string): boolean {
  const norm = (p: string) => {
    let s = windows
      ? p
          .replace(/^\\\\\?\\/, "")
          .replaceAll("/", "\\")
          .toLowerCase()
      : p;
    if (!windows && existsSync(s)) s = realpathSync.native(s);
    return s;
  };
  return a === b || norm(a) === norm(b);
}

/** The app keeps the packages it installed read-only (device.rs `freeze`): on macOS each folder
 * is 0500; on Windows each file and folder has a protected access list allowing no write or
 * delete. The app removes them by making each writable first (device.rs `remove_tree`); this does
 * the same for the package store in data, an app data folder this session made, so it can be
 * removed. Windows: icacls gives each file and folder back the list it inherits (the store holds
 * no links: device.rs refuses them when it unpacks). */
function makeRemovable(data: string) {
  const store = join(data, "device-packages");
  if (!existsSync(store)) return;
  if (windows) {
    const icacls = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "icacls.exe");
    const r = spawnSync(icacls, [store, "/reset", "/T", "/C", "/Q"], { encoding: "utf8" });
    if (r.status !== 0)
      throw new Error(`icacls ${store} /reset exited with ${r.status}: ${r.stdout ?? ""}${r.stderr ?? ""}`);
    return;
  }
  const walk = (dir: string) => {
    const st = lstatSync(dir);
    if (!st.isDirectory()) return;
    if ((st.mode & 0o700) !== 0o700) chmodSync(dir, 0o700);
    for (const e of readdirSync(dir)) walk(join(dir, e));
  };
  walk(store);
}

/** One session: its fixtures, a fresh app state, the app run, and run.ts's checks of its report. */
export async function toolSession(name: ToolSession, ctx: Context) {
  const { check, say } = ctx;
  const root = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-tools-"));
  const fixtures = join(root, name);
  let home = join(root, "home");
  let configDir: string;
  let localDir: string;
  // The app's folders this session made, the only ones it removes (Windows).
  const made: string[] = [];
  // The app's data folder (its package store's), once this session made it.
  let data: string | undefined;
  let core: Awaited<ReturnType<typeof startCore>> | undefined;
  let proc: ChildProcess | undefined;
  try {
    if (windows) {
      home = ctx.privateFolder();
      const k = knownFolders(ctx);
      configDir = join(k.roaming, ctx.identifier);
      localDir = join(k.local, ctx.identifier);
      for (const d of [configDir, localDir])
        if (existsSync(d))
          throw new Error(`the app's folder ${d} exists already; this test removes only folders it made`);
      made.push(configDir, localDir);
    } else {
      mkdirSync(home, { mode: 0o700 });
      // macOS: the app's folders come from HOME (Application Support holds config and data).
      configDir = join(home, "Library", "Application Support", ctx.identifier);
      localDir = configDir;
    }
    mkdirSync(configDir, { recursive: true });
    mkdirSync(localDir, { recursive: true });
    data = configDir;
    mkdirSync(fixtures);

    // The session's fixtures, and the test trust profile they write in HOME.
    const programFixture = join(ctx.targetDir, "debug", "examples", `program_fixture${EXE}`);
    const appToolsTarget = join(ctx.targetDir, "app-tools");
    let registries: { name: string; url: string }[];
    let nonce: string;
    let canaries: string[] = [];
    let launcherBin: string | undefined;
    if (name === "admission") {
      ({ registries, nonce } = buildAdmissionFixtures(fixtures, home));
    } else if (name === "program") {
      ({ registries, nonce } = buildProgramFixtures(fixtures, home, programFixture));
    } else if (name === "routing") {
      ({ registries, nonce } = buildRoutingFixtures(fixtures, home, programFixture));
    } else {
      const execution = join(appToolsTarget, "debug", `execution${EXE}`);
      check(existsSync(execution), `the execution program ${execution} is not built`);
      const f = buildLauncherFixtures(
        fixtures,
        home,
        programFixture,
        execution,
        join(next, "app-tools", "execution", "pluxy-extension.json"),
      );
      ({ registries, nonce, canaries } = f);
      launcherBin = f.bin;
    }
    // The profile with its roots and the updater key, as run.ts writes it.
    const roots = JSON.parse(readFileSync(join(home, PROFILE_FILE), "utf8")).roots;
    writeFileSync(
      join(home, PROFILE_FILE),
      JSON.stringify({ roots, updater: { kind: "test", public_key: UPDATER_KEY } }),
      { mode: 0o600 },
    );
    writeFileSync(join(configDir, "device-registries.json"), JSON.stringify({ version: 0, registries }));

    say("starting the test core");
    core = await startCore();
    // The connection the app starts with: local mode, signed in with a kept key.
    writeFileSync(
      join(localDir, "connection.json"),
      JSON.stringify({ mode: "local", url: core.base, key: core.key }),
      { mode: 0o600 },
    );
    writeFileSync(join(configDir, "window.json"), JSON.stringify({ close_behavior: "minimize-to-tray" }), {
      mode: 0o600,
    });

    const quit = join(root, "quit");
    let env: Record<string, string | undefined> = {
      ...process.env,
      HOME: home,
      PLUXY_TEST_TRUST_NONCE: nonce,
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
    };
    if (name === "admission") env.PLUXY_APP_TEST_ADMISSION = fixtures;
    // In the app's environment, never in the tool's (program_native.rs checks its names).
    if (name === "program") {
      env.PLUXY_APP_TEST_PROGRAM = fixtures;
      env.PLUXY_TEST_PROGRAM_CANARY = randomUUID();
    }
    if (name === "routing") env.PLUXY_APP_TEST_ROUTING = fixtures;
    if (launcherBin) {
      env.PLUXY_APP_TEST_LAUNCHER = fixtures;
      // In the app's environment, never in a launch's (launcher_platform_native.rs checks the
      // agent's).
      const canary = randomUUID();
      env.PLUXY_TEST_LAUNCHER_CANARY = canary;
      canaries = [...canaries, canary];
      // The agent the launcher finds by name comes first.
      env = withPath(env, launcherBin);
    }

    say(`running ${ctx.bin}`);
    const child = spawn(ctx.bin, [], { cwd: root, env, stdio: ["ignore", "pipe", "pipe"] });
    proc = child;
    // biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
    const steps = new Map<string, any>();
    const order: string[] = [];
    const log: string[] = [];
    const onLine = (line: string) => {
      if (canaries.some((c) => line.includes(c))) {
        check(false, "the launcher check's key or canary appeared on the app's stdout/stderr");
        return;
      }
      log.push(line);
      if (log.length > 60) log.shift();
      const m = /^native-test: (\S+) (.*)$/.exec(line);
      if (!m) return;
      const [, step, json] = m as unknown as [string, string, string];
      if (step === "reached" || step === "asset") return;
      let value: unknown;
      try {
        value = JSON.parse(json);
      } catch {
        value = json;
      }
      steps.set(step, value);
      order.push(step);
      // The window has closed: the app may quit once its checks are done.
      if (step === "closed") writeFileSync(quit, "");
      if (step === "stalled") {
        check(false, `the app stalled: ${json}`);
        child.kill();
      }
    };
    if (child.stdout) createInterface({ input: child.stdout }).on("line", onLine);
    if (child.stderr) createInterface({ input: child.stderr }).on("line", onLine);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      // Only the process this session started.
      child.kill();
    }, TIMEOUT);
    // "close", not "exit": it comes after the output is read. Once stopped, it has STOP_MS for
    // that.
    let late: ReturnType<typeof setTimeout> | undefined;
    const code = await new Promise<number | null | "open">((resolve) => {
      child.once("close", (c) => resolve(c));
      late = setTimeout(() => resolve("open"), TIMEOUT + STOP_MS);
    });
    clearTimeout(timer);
    clearTimeout(late);
    check(code !== "open", `the app's output did not close within ${STOP_MS / 1000} s after it was stopped`);
    if (code !== "open") proc = undefined;

    const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
    check(!timedOut, `the app did not finish within ${TIMEOUT / 1000} s; stopped it`);
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);
    if (code !== "open") check(code === 0, `the app exited with ${code}`);
    check(steps.has("quit"), "the app did not reach its quit step");

    if (name === "admission") {
      // The updater gate, reported before any other step: this dev app (the normal identifier)
      // with the profile's updater key is refused for its identifier.
      const gate = steps.get("updater_gate") as
        | { passed?: boolean; marker?: boolean; identifier?: string; error?: string }
        | undefined;
      say(`updater_gate ${JSON.stringify(gate)}`);
      check(order[0] === "updater_gate", `the app's first report is ${order[0]}, not its updater gate`);
      check(
        gate?.marker === true,
        `the updater gate did not find the compiled test marker: ${JSON.stringify(gate)}`,
      );
      check(
        gate?.passed === false && gate.identifier === ctx.identifier && /identifier/.test(gate.error ?? ""),
        `the ${ctx.identifier} app's updater gate was not refused for its identifier: ${JSON.stringify(gate)}`,
      );
      const a = steps.get("admission");
      check(a?.passed === true, `the native admission check did not complete: ${failure}`);
      say(`admission ${JSON.stringify(a ?? {})}`);
    }

    if (name === "program") {
      const p = steps.get("program") as
        | { passed?: boolean; program?: string; quit_pids?: number[] }
        | undefined;
      check(p?.passed === true, `the program host check did not complete: ${failure}`);
      // The swap check runs on from where the program check left (swap_native.rs).
      const w = steps.get("swap") as typeof p;
      check(w?.passed === true, `the swap check did not complete: ${failure}`);
      // After the app quit, neither tool the checks left running nor its child runs: each process,
      // by its ID and program path together.
      await Bun.sleep(1000);
      for (const [what, r] of [
        ["program", p],
        ["swap", w],
      ] as const) {
        check(
          r?.quit_pids?.length === 2,
          `the ${what} check named no processes to watch: ${JSON.stringify(r)}`,
        );
        const left = (r?.quit_pids ?? []).filter((pid) => {
          const running = programOf(ctx, pid);
          return running !== undefined && r?.program !== undefined && samePath(running, r.program);
        });
        // Reported only: this test signals no process by its ID here.
        check(
          left.length === 0,
          `the ${what} check's tool processes ${left.join(", ")} (${r?.program}) still run after the app quit`,
        );
      }
      say(`program ${JSON.stringify(p ?? {})}`);
      say(`swap ${JSON.stringify(w ?? {})}`);
    }

    if (name === "routing") {
      const r = steps.get("routing");
      check(r?.passed === true, `the routing check did not complete: ${failure}`);
      const j = steps.get("jobs");
      check(j?.passed === true, `the jobs check did not complete: ${failure}`);
      say(`routing ${JSON.stringify(r ?? {})}`);
      say(`jobs ${JSON.stringify(j ?? {})}`);
    }

    if (name === "launcher") {
      const r = steps.get("launcher");
      check(r?.passed === true, `the launcher check did not complete: ${failure}`);
      say(`launcher ${JSON.stringify(r ?? {})}`);
    }

    if (timedOut || code !== 0 || steps.has("failed")) say(`the app's last lines:\n  ${log.join("\n  ")}`);
  } finally {
    // An app still running here (the session failed before it quit): only the one it started.
    if (proc && proc.exitCode === null && proc.signalCode === null) {
      const p = proc;
      p.kill();
      if (!(await within(p, "close", STOP_MS)))
        check(false, `the app's output did not close within ${STOP_MS / 1000} s after it was stopped`);
    }
    // The launcher's agent ends once this file is in its working folder.
    if (name === "launcher" && existsSync(join(fixtures, "work"))) {
      writeFileSync(join(fixtures, "work", "stop"), "");
      await Bun.sleep(2000);
    }
    if (core && !(await stopCore(core.proc)))
      check(false, `the test core did not exit within ${(2 * STOP_MS) / 1000} s of being stopped`);
    if (data) {
      try {
        makeRemovable(data);
      } catch (e) {
        check(false, `could not make the app's package store removable: ${(e as Error).message}`);
      }
    }
    // Windows: its folders may stay in use a moment after the app quit (its WebView2 processes,
    // the agent the launcher started). Each is tried, and one left behind is a problem.
    const webview = made[1];
    for (const d of windows ? [...made, root, home] : [root]) {
      try {
        if (webview) ctx.removeWhenFree(d, webview);
        else rmSync(d, { recursive: true, force: true });
      } catch (e) {
        check(false, `could not remove ${d}: ${(e as Error).message}`);
      }
    }
  }
}
