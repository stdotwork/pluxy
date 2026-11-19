// The tools and open shards of the Windows and macOS native test (platform.ts, PLATFORM_SHARDS in
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
// - launcher: the execution program and the launcher, with this system's terminal kinds, detached
//   and managed launches and Windows' program rules (launcher_platform_native.rs; launcher.ts),
//   with the fixtures' bin first on PATH and a canary variable no launch may get. On Windows the
//   app runs outside the job the runner's steps run in (outsideJobRun), so that its detached
//   launches can leave it. After the app quit, a file named stop in the fixtures' work folder and
//   in each folder in it ends every agent left running, and each agent's heartbeat must stop; on
//   macOS Terminal, when the session's launches started it, is quit;
// - vault: the vault, secret delivery and references and journal recovery, then client-key capture
//   and entry (vault_native.rs, client_key_native.rs; vault.ts). Client keys are made on the test
//   core, whose config file core.json in the fixtures names: the app's own core, which run.ts's
//   session uses on Linux, does not run on these systems yet. Neither the vault's key and canary,
//   the test core's management key nor a client key the check learned may appear in the app's
//   output;
// - open (the open shard, a job of its own): a tool's opening of a file in the system's own app,
//   and the app's opening of an address in the default browser (open_platform_native.rs; open.ts,
//   buildPlatformOpenFixtures), with the session's own https server (browser.ts). On macOS the
//   session trusts that server's test authority for the run and deletes it afterwards, checking
//   that macOS trusts the server's certificate only meanwhile; on Windows the app runs outside the
//   runner's job as in the launcher session, as its confirmed opens are detached launches, and the
//   session removes the check's .jsonl file type if the app left it, and reports that.
//
// Each session gets a fresh app state and a private temporary HOME holding the test trust profile
// (the app's updater gate and trust store read it from HOME). On macOS the app's folders come from
// that HOME too. On Windows they are the account's known folders, which no environment variable
// moves: a session refuses to start when the app's roaming or local folder exists, and removes only
// the folders it made. HOME is the only folder variable set there: the program check compares the
// tool's APPDATA and LOCALAPPDATA with the app's own.
//
// This test never kills or signals a process by its ID on Windows or macOS: the app it started is
// stopped through its own child object (on Windows' launcher and open sessions, through the
// starter's, which started it), and a tool process left running is reported, not stopped.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  realpathSync,
  rmdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildAdmissionFixtures, EXE, PROFILE_FILE } from "./admission";
import { macTrusts, type Served, serveHttps, trustOnMac } from "./browser";
import { buildLauncherFixtures } from "./launcher";
import { buildPlatformOpenFixtures, platformOpenSessions } from "./open";
import { buildProgramFixtures } from "./program";
import { buildRoutingFixtures } from "./routing";
import { buildVaultFixtures } from "./vault";

/** The sessions of these shards, by their names in shards.ts. */
export const TOOL_SESSIONS = ["admission", "program", "routing", "launcher", "vault", "open"] as const;
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
/** One app run: 10 minutes (4 minutes on Linux), with room for slower runners. */
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
async function startCore(): Promise<{ proc: ChildProcess; base: string; key: string; config: string }> {
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

/** A run of the app: its output, its end, and a way to stop it. */
interface AppRun {
  /** Calls f with each line of the app's stdout and stderr. */
  lines(f: (line: string) => void): void;
  /** Its exit code, once it ended and its output was read; null when it was stopped. */
  closed: Promise<number | null>;
  /** Stops it: only the process this session started. */
  stop(): void;
}

/** The app as this session's child. */
function childRun(bin: string, cwd: string, env: Record<string, string | undefined>): AppRun {
  const child = spawn(bin, [], { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
  // "close", not "exit": it comes after the output is read.
  const closed = new Promise<number | null>((resolve) => child.once("close", (c) => resolve(c)));
  return {
    lines(f) {
      for (const s of [child.stdout, child.stderr]) if (s) createInterface({ input: s }).on("line", f);
    },
    closed,
    stop() {
      if (child.exitCode === null && child.signalCode === null) child.kill();
    },
  };
}

/** Windows: the app started outside the job the runner's steps run in, which lets no process
 * leave it (CREATE_BREAKAWAY_FROM_JOB is refused there), so no detached launch of the app's could
 * start. WMI's Win32_Process.Create starts the fixture program's starter (`pluxy-fixture-run`,
 * src-tauri/examples/program_fixture) outside it, as the workflow's "Detached launch outside the
 * runner's job" step starts the unit tests; the starter starts the app as its own child with env
 * and its output in files in a private folder, which this reads as they grow, and stops that
 * child once asked. The app must be in this Windows session, where its windows and tray show. */
async function outsideJobRun(
  ctx: Context,
  fixture: string,
  cwd: string,
  env: Record<string, string | undefined>,
  folder: string,
): Promise<AppRun> {
  const at = (n: string) => join(folder, n);
  const files = {
    stdout: at("out.txt"),
    stderr: at("err.txt"),
    pid: at("pid"),
    exit: at("exit"),
    kill: at("kill"),
  };
  // The environment as a block holds names, not the per-drive folders (`=C:`) some list.
  const whole = Object.fromEntries(
    Object.entries(env).filter(([k, v]) => v !== undefined && k !== "" && !k.startsWith("=")),
  );
  writeFileSync(at("spec.json"), JSON.stringify({ program: ctx.bin, args: [], cwd, env: whole, ...files }));
  const q = (s: string) => `'${s.replaceAll("'", "''")}'`;
  writeFileSync(
    at("start.ps1"),
    [
      `$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = ${q(`"${fixture}" pluxy-fixture-run "${at("spec.json")}"`)}; CurrentDirectory = ${q(cwd)} }`,
      "Write-Output $r.ReturnValue",
    ].join("\r\n"),
  );
  const started = ctx.powershell(`& ${q(at("start.ps1"))}`);
  if (started.status !== 0 || started.out.trim() !== "0")
    throw new Error(`WMI did not start the app outside the runner's job: ${started.out}`);
  const deadline = Date.now() + 30_000;
  while (!existsSync(files.pid) && !existsSync(files.exit) && Date.now() < deadline) await Bun.sleep(200);
  if (!existsSync(files.pid)) {
    // Should the starter still start it, it stops it at once.
    writeFileSync(files.kill, "");
    throw new Error(
      `the starter did not start the app within 30 s: ${existsSync(files.exit) ? readFileSync(files.exit, "utf8") : "no word from it"}`,
    );
  }
  const pid = Number(readFileSync(files.pid, "utf8"));
  // Its Windows session, and this one's (WMI's process host runs in session 0).
  const sessions = ctx.powershell(
    `(Get-Process -Id ${pid}).SessionId; [System.Diagnostics.Process]::GetCurrentProcess().SessionId`,
  );
  const [theirs, ours] = sessions.out.split(/\r?\n/).map((l) => l.trim());
  const listeners: ((line: string) => void)[] = [];
  const tails = [files.stdout, files.stderr].map((path) => ({ path, offset: 0, pending: Buffer.alloc(0) }));
  // Reads what each file has gained, line by line (a line is whole once its line break is there),
  // once there is someone to give the lines to.
  const pump = () => {
    if (listeners.length === 0) return;
    for (const t of tails) {
      if (!existsSync(t.path)) continue;
      const fd = openSync(t.path, "r");
      try {
        const chunk = Buffer.alloc(1 << 16);
        for (;;) {
          const n = readSync(fd, chunk, 0, chunk.length, t.offset);
          if (n <= 0) break;
          t.offset += n;
          t.pending = Buffer.concat([t.pending, chunk.subarray(0, n)]);
        }
      } finally {
        closeSync(fd);
      }
      for (let i = t.pending.indexOf(10); i >= 0; i = t.pending.indexOf(10)) {
        const line = t.pending.subarray(0, i).toString("utf8").replace(/\r$/, "");
        t.pending = t.pending.subarray(i + 1);
        for (const f of listeners) f(line);
      }
    }
  };
  let asked: number | undefined;
  const closed = (async () => {
    while (!existsSync(files.exit)) {
      pump();
      if (asked !== undefined && Date.now() - asked > 60_000)
        throw new Error(
          `the app started outside the runner's job (process ${pid}) did not stop within 60 s of being asked; it is left running (this test signals no process by its ID)`,
        );
      await Bun.sleep(250);
    }
    // The starter writes the exit file once the app ended: its output is all in the files.
    pump();
    for (const t of tails) if (t.pending.length > 0) for (const f of listeners) f(t.pending.toString("utf8"));
    const exit = readFileSync(files.exit, "utf8").trim();
    return /^-?\d+$/.test(exit) ? Number(exit) : null;
  })();
  const run: AppRun = {
    lines: (f) => listeners.push(f),
    closed,
    stop: () => {
      asked ??= Date.now();
      try {
        writeFileSync(files.kill, "");
      } catch {
        // Its folder is gone (the session cleaned up): closed has said whether it stopped.
      }
    },
  };
  if (!theirs || theirs !== ours) {
    run.stop();
    await closed.catch(() => null);
    throw new Error(
      `the app started outside the runner's job is in Windows session ${theirs}, not this session's ${ours}: ${sessions.out}`,
    );
  }
  return run;
}

/** The agents' heartbeat files under work (`<record>.<process ID>.beat`, which each rewrites
 * every 50 ms until its stop file is there), with what each holds now. */
function beats(work: string): Map<string, string> {
  const out = new Map<string, string>();
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith(".beat")) {
        try {
          out.set(p, readFileSync(p, "utf8"));
        } catch {
          // Replaced while read: it beats.
          out.set(p, "");
        }
      }
    }
  };
  walk(work);
  return out;
}

/** macOS: whether Terminal runs (asked without starting it or sending it an Apple event). */
function terminalRuns(): boolean {
  const r = spawnSync("osascript", ["-e", 'application "Terminal" is running'], { encoding: "utf8" });
  return r.status === 0 && r.stdout.trim() === "true";
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
  // The app's run, until it ended.
  let app: AppRun | undefined;
  // Windows' launcher and open sessions: the private folder of their starter's files.
  let runFolder: string | undefined;
  // macOS' launcher session: whether Terminal ran before the app did.
  let terminalBefore: boolean | undefined;
  // The open session's https server, and on macOS what removes its trusted test authority.
  let served: Served | undefined;
  let untrust: (() => void) | undefined;
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
    } else if (name === "vault") {
      ({ registries, nonce, canaries } = buildVaultFixtures(fixtures, home, programFixture));
    } else if (name === "open") {
      const execution = join(appToolsTarget, "debug", `execution${EXE}`);
      check(existsSync(execution), `the execution program ${execution} is not built`);
      ({ registries, nonce } = buildPlatformOpenFixtures(
        fixtures,
        home,
        programFixture,
        execution,
        join(next, "app-tools", "execution", "pluxy-extension.json"),
      ));
      // The session's own https address for the app's browser step, which macOS' browser asks for
      // only once it trusts the server. A failure here is reported, and that step then fails.
      served = serveHttps(fixtures);
      if (!windows) {
        try {
          untrust = trustOnMac(served.certs, fixtures);
          const t = macTrusts(served.certs, fixtures);
          check(
            t.ok,
            `macOS does not trust the session's server after trusting its test authority: ${t.out}`,
          );
        } catch (e) {
          check(false, `could not trust the session's test authority: ${(e as Error).message}`);
        }
      }
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
    if (name === "vault") {
      // The core whose config file the client-key check reads the keys core makes from.
      writeFileSync(join(fixtures, "core.json"), JSON.stringify({ config: core.config }));
      // Its management key, which the check types when it signs in again off Linux.
      canaries = [...canaries, core.key];
    }
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
    if (name === "vault") env.PLUXY_APP_TEST_VAULT = fixtures;
    if (name === "open") env.PLUXY_APP_TEST_OPEN = fixtures;
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
    if (launcherBin && !windows) terminalBefore = terminalRuns();
    let run: AppRun;
    if ((launcherBin || name === "open") && windows) {
      // The launcher's detached launches and the open session's confirmed opens (detached launches
      // too) must be able to leave the app's job.
      say("starting it outside the runner's job");
      runFolder = ctx.privateFolder();
      run = await outsideJobRun(ctx, programFixture, root, env, runFolder);
    } else {
      run = childRun(ctx.bin, root, env);
    }
    app = run;
    // biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
    const steps = new Map<string, any>();
    const order: string[] = [];
    const log: string[] = [];
    // Every line, for the client keys the check learns as it runs (vault).
    const output: string[] = [];
    const onLine = (line: string) => {
      if (canaries.some((c) => line.includes(c))) {
        check(false, `the ${name} check's key or canary appeared on the app's stdout/stderr`);
        return;
      }
      output.push(line);
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
        run.stop();
      }
    };
    run.lines(onLine);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      run.stop();
    }, TIMEOUT);
    // Once stopped, the app has STOP_MS for its output to close ("close", not "exit": it comes
    // after the output is read).
    let late: ReturnType<typeof setTimeout> | undefined;
    const code = await Promise.race([
      run.closed,
      new Promise<"open">((resolve) => {
        late = setTimeout(() => resolve("open"), TIMEOUT + STOP_MS);
      }),
    ]);
    clearTimeout(timer);
    clearTimeout(late);
    check(code !== "open", `the app's output did not close within ${STOP_MS / 1000} s after it was stopped`);
    if (code !== "open") app = undefined;

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

    if (name === "vault") {
      const v = steps.get("vault");
      check(v?.passed === true, `the vault check did not complete: ${failure}`);
      const c = steps.get("client_keys");
      check(c?.passed === true, `the client-key check did not complete: ${failure}`);
      // The client keys the check captured, as it learned them (client_key_native.rs).
      const file = join(fixtures, "client-keys");
      const clientKeys = existsSync(file)
        ? readFileSync(file, "utf8")
            .split("\n")
            .filter((k) => k.length > 0)
        : [];
      check(clientKeys.length === 2, `the client-key check noted ${clientKeys.length} keys, not 2`);
      check(
        !output.some((line) => clientKeys.some((k) => line.includes(k))),
        "a client key from the client-key check appeared on the app's stdout/stderr",
      );
      say(`vault ${JSON.stringify(v ?? {})}`);
      say(`client keys ${JSON.stringify(c ?? {})}`);
    }

    if (name === "open") {
      const o = steps.get("open");
      check(o?.passed === true, `the open check did not complete: ${failure}`);
      say(`open ${JSON.stringify(o ?? {})}`);
    }

    if (timedOut || code !== 0 || steps.has("failed")) say(`the app's last lines:\n  ${log.join("\n  ")}`);
  } finally {
    // An app still running here (the session failed before it quit): only the one it started.
    if (app) {
      const a = app;
      a.stop();
      try {
        const closed = await Promise.race([a.closed.then(() => true), Bun.sleep(STOP_MS).then(() => false)]);
        if (!closed)
          check(false, `the app's output did not close within ${STOP_MS / 1000} s after it was stopped`);
      } catch (e) {
        check(false, (e as Error).message);
      }
    }
    // The launcher's agents: each ends once a file named stop is in its record's folder (work, or
    // the launch's own folder in it), and until then rewrites its heartbeat file every 50 ms.
    const work = join(fixtures, "work");
    if (name === "launcher" && existsSync(work)) {
      for (const d of [
        work,
        ...readdirSync(work, { withFileTypes: true })
          .filter((e) => e.isDirectory())
          .map((e) => join(work, e.name)),
      ])
        writeFileSync(join(d, "stop"), "");
      // Still for a second within 15 s: every agent ended.
      let last = beats(work);
      let still = [...last.keys()];
      for (const deadline = Date.now() + 15_000; still.length > 0 && Date.now() < deadline; ) {
        await Bun.sleep(1000);
        const now = beats(work);
        still = [...now].filter(([p, v]) => last.get(p) !== v).map(([p]) => p);
        last = now;
      }
      check(
        still.length === 0,
        `the launcher's agents still run after their stop files: ${still.join(", ")}`,
      );
    }
    // macOS: Terminal, started by this session's terminal launches, quit (its windows' programs
    // ended above); one that ran before is left as it was.
    if (terminalBefore === false && terminalRuns()) {
      const r = spawnSync("osascript", ["-e", 'tell application "Terminal" to quit'], {
        encoding: "utf8",
        timeout: 30_000,
      });
      // It ends a moment after it answered.
      for (const deadline = Date.now() + 10_000; terminalRuns() && Date.now() < deadline; )
        await Bun.sleep(250);
      check(
        r.status === 0 && !terminalRuns(),
        `Terminal, which this session's launches started, did not quit: ${r.status} ${r.stdout ?? ""}${r.stderr ?? ""}`,
      );
    }
    if (terminalBefore === true) say("Terminal ran before this session: its windows from this session stay");
    if (name === "open") {
      served?.stop();
      if (untrust && served) {
        try {
          untrust();
          const t = macTrusts(served.certs, fixtures);
          check(!t.ok, "macOS still trusts the session's server after its test authority was deleted");
        } catch (e) {
          check(false, `could not delete the session's test authority: ${(e as Error).message}`);
        }
      }
      // The check removes its .jsonl file type itself; one left (the app ended first) is removed
      // here, and reported.
      if (windows) {
        const left = ctx.powershell(
          [
            "$c = 'HKCU:\\Software\\Classes'",
            "$left = @()",
            'if (Test-Path -LiteralPath "$c\\PluxyTest.jsonl") { Remove-Item -LiteralPath "$c\\PluxyTest.jsonl" -Recurse; $left += \'PluxyTest.jsonl\' }',
            '$k = Get-Item -LiteralPath "$c\\.jsonl" -ErrorAction SilentlyContinue',
            "if ($k -and $k.GetValue('') -eq 'PluxyTest.jsonl') { $k.Dispose(); Remove-Item -LiteralPath \"$c\\.jsonl\" -Recurse; $left += '.jsonl' }",
            "$left -join ','",
          ].join("; "),
        );
        check(
          left.status === 0 && left.out === "",
          `the open check left its .jsonl file type registered, removed now: ${left.status} ${left.out}`,
        );
        // The junction, not what it leads to, before its folder is removed.
        const junction = join(platformOpenSessions(home), "linked");
        try {
          rmdirSync(junction);
        } catch (e) {
          if ((e as NodeJS.ErrnoException).code !== "ENOENT")
            check(false, `could not remove the junction ${junction}: ${(e as Error).message}`);
        }
      }
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
    for (const d of windows ? [...made, root, home, ...(runFolder ? [runFolder] : [])] : [root]) {
      try {
        if (webview) ctx.removeWhenFree(d, webview);
        else rmSync(d, { recursive: true, force: true });
      } catch (e) {
        check(false, `could not remove ${d}: ${(e as Error).message}`);
      }
    }
  }
}
