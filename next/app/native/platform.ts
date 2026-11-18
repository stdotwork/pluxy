// The native test on Windows and macOS, on the GitHub-hosted runners of those systems
// (.github/workflows/next-app-platforms.yml). Run from next/app, after the app is built with the
// native-test feature (`cargo build --locked --features native-test` in src-tauri):
//
//   bun native/platform.ts
//
// It launches the real app, as the person would, and checks it from outside, with the operating
// system's own tools (reg, plutil, launchctl, PowerShell) for what the app leaves in the system.
// Its sessions are named in native/shards.ts (PLATFORM_SHARDS), and each asks runsSession first,
// as run.ts's do:
//
// - single instance: a second launch raises the first app's window (also when it was hidden or
//   closed to the tray, and for a login launch), starts no second app, and a first app that was
//   killed leaves a lock and an address the next launch takes over; the lock, the address and the
//   pipe (Windows) or socket (macOS) are the user's own, and go when the app quits;
// - login item: saving the preference writes the user's own login item (the `Run` key on Windows,
//   a LaunchAgent on macOS) running the app with `--autostart`, nothing machine-wide, the system's
//   own tools read it back, running what it names starts the app as a login does, and removing it
//   leaves nothing;
// - window and tray: the app has a tray icon; closing the window leaves the app in the tray, or
//   quits it, as the preference says; the window's size and place are kept across a restart.
// - bundle: the test package (package.ts test, made before this run and named by
//   PLUXY_APP_TEST_PACKAGE: a folder bundle holding the program set on Windows, an app bundle on
//   macOS) has the test
//   identity in its own files, starts from its bundle and passes its updater gate with its test
//   trust profile, and quits leaving no process; the same bundle without the profile, and the dev
//   app (normal identifier, no test trust) with it, are refused at the gate;
// - bundle release gate: the release command (package.ts release) passes a clean release build and
//   a clean bundle of this system's kind around it, and refuses a bundle holding the test-feature
//   dev app, the test package renamed and the test package itself.
// - app update (the update shard): the test package, installed from its bundle into a folder of
//   this user's and connected to a test core (`go run ./e2e/panelcore`), updates itself to its
//   signed newer version and back,
//   and refuses the rest, with update-cases.ts's cases (the same as Linux's update session): no
//   updater trust; this system's crafted installs (Windows: an .msi-patched program, a folder
//   Everyone may change; macOS: the program outside its bundle, a bundle its
//   group may change, the installed bundle started through a link on its path); macOS: the
//   update route starts the bundle by its real path, and a launch through LaunchServices (`/usr/bin/open`) is offered the
//   update; Windows: an update ended (by the session, which started it) between two parts of the
//   program set, and the earlier version, started again on the mixed folder, undoes it and keeps
//   its own set as kept; the update route with its third launch in the acceptance gap and its rollback; stand-ins
//   for installers and elevation first on PATH never ran; macOS: no quarantine on the installed
//   bundle.
// - install (the update shard; Windows only, macOS records a skip until it has an installer): the
//   test package's per-user setup program (`/S` throughout) installs into the user's local
//   application data folder with a per-user uninstall entry, nothing machine-wide, and a manifest
//   that asks for no elevation; the installed app reads the setup's bundle type, turns its login
//   item on, updates itself in place to its newer version and rolls back (update-cases.ts's
//   route), the uninstall entry's DisplayVersion following each version that accepted itself; the
//   setup and the uninstaller each refuse while the app runs and leave it running; once it quits,
//   uninstalling removes the folder (with what the app left there, queuing nothing for Windows to
//   delete at the next restart), the login item and the uninstall entry and keeps the app's data; and a junction in bundled\ is removed without
//   removing what it points to.
//
// These write what a real run writes: the user's registry or LaunchAgents folder, and on Windows
// the app's own folder in the user's profile. So it runs only on a GitHub-hosted runner, whose user
// is thrown away, and removes what it made.
//
// Output: `platform: ...` lines, `platform-problem: ...` for each failed check, and a summary with
// the counts. A run that checked nothing fails.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import {
  appendFileSync,
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { createInterface } from "node:readline";
import { MARKER_FILE, PROFILE_FILE } from "./admission";
import { Launch, type Step } from "./launch";
import { SESSIONS, selectShard } from "./shards";
import {
  checkSteps,
  LAUNCH_MS,
  launchChain,
  NO_ROOTS,
  ownNames,
  pinOf,
  privateFolder,
  REFUSALS,
  type Refusal,
  refusedLaunch,
  SET_PARTS,
  setDigest,
  standIns,
  startUpdateFeed,
  treeDigest,
  updateRoute,
  V1,
  withPathFirst,
} from "./update-cases";

const windows = process.platform === "win32";
const mac = process.platform === "darwin";
if (!windows && !mac) {
  console.error("native test setup failed: this is the Windows and macOS test (the Linux one is run.ts)");
  process.exit(1);
}
if (process.env.GITHUB_ACTIONS !== "true" || process.env.RUNNER_ENVIRONMENT !== "github-hosted") {
  console.error(
    "native test setup failed: it writes the user's registry or LaunchAgents folder and app folder, so it runs only on a GitHub-hosted runner (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted)",
  );
  process.exit(1);
}

const app = join(import.meta.dir, "..");
const tauri = join(app, "src-tauri");
const identifier: string = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8")).identifier;
const targetDir = process.env.CARGO_TARGET_DIR ?? join(tauri, "target");
const bin = join(targetDir, "debug", windows ? "pluxy-app.exe" : "pluxy-app");
if (!existsSync(bin)) {
  console.error(
    `native test setup failed: ${bin} is not built (cargo build --locked --features native-test)`,
  );
  process.exit(1);
}

const shard = process.env.PLUXY_APP_NATIVE_SHARD || "platform";
let selected: ReadonlySet<string>;
try {
  selected = selectShard(shard);
} catch (e) {
  console.error(`native test setup failed: ${(e as Error).message}`);
  process.exit(1);
}
const started = new Set<string>();
/** Whether this run runs the session called name, which every session asks before it starts. */
function runsSession(name: string): boolean {
  if (!SESSIONS.has(name))
    throw new Error(`the native test session ${name} is in no shard of native/shards.ts`);
  if (selected.has(name)) started.add(name);
  return selected.has(name);
}

let checks = 0;
const problems: string[] = [];
const skipped: string[] = [];
let session = "";
const say = (line: string) => console.log(`platform: ${session}: ${line}`);
function check(ok: unknown, what: string) {
  checks++;
  if (!ok) {
    problems.push(`${session}: ${what}`);
    console.log(`platform-problem: ${session}: ${what}`);
  }
}
function skip(why: string) {
  skipped.push(`${session}: ${why}`);
  console.log(`platform-skipped: ${session}: ${why}`);
}

/** Whether two pixel counts are the same, give or take what rounding between scales does. */
const near = (a: number | undefined, b: number) => a !== undefined && Math.abs(a - b) <= 3;

const UP_MS = 120_000;
const SECOND_EXIT_MS = 20_000;

// ---- a profile ------------------------------------------------------------------------------

interface Profile {
  /** The environment the app runs in. */
  env: Record<string, string | undefined>;
  /** The app's own folder (window.json, startup.json, instance/). */
  dir: string;
  /** Where the macOS login item goes: the user's LaunchAgents folder. */
  agents: string;
  /** Removes what the session made. */
  remove: () => void;
}

/** A user's folders for one session. macOS: a home of its own under a temporary folder. Windows:
 * the app's folder in the runner user's profile, which no folder variable moves (the system
 * knows it): it must not exist before, and is removed after. */
function makeProfile(id = identifier): Profile {
  if (mac) {
    const root = mkdtempSync(join(tmpdir(), "pluxy-plat-"));
    const home = join(root, "home");
    const dir = join(home, "Library", "Application Support", id);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    return {
      env: { ...process.env, HOME: home },
      dir,
      agents: join(home, "Library", "LaunchAgents"),
      remove: () => rmSync(root, { recursive: true, force: true }),
    };
  }
  const appdata = process.env.APPDATA as string;
  const local = process.env.LOCALAPPDATA as string;
  const dir = join(appdata, id);
  const webview = join(local, id);
  if (existsSync(dir))
    throw new Error(`${dir} exists: this is not a fresh runner, and the test would write over it`);
  mkdirSync(dir, { recursive: true });
  return {
    env: { ...process.env },
    dir,
    agents: "",
    remove: () => {
      removeWhenFree(dir, webview);
      removeWhenFree(webview, webview);
    },
  };
}

/** How long a profile's folders may stay in use once its app has quit. */
const FREE_MS = 30_000;

/** The WebView2 processes (msedgewebview2.exe) whose command line names folder, which is where an
 * app's WebView2 keeps its data: they can outlive the app that started them for a moment. As
 * "<pid> <name>" each. */
function webviewHolders(folder: string): string[] {
  // The folder itself, not a longer name it starts (the test package's id extends the app's).
  const like = `*${folder.replaceAll("'", "''")}\\*`;
  const r = powershell(
    `Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" | Where-Object { $_.CommandLine -like '${like}' } | ForEach-Object { "$($_.ProcessId) $($_.Name)" }`,
  );
  if (r.status !== 0) throw new Error(`listing the WebView2 processes: ${r.out}`);
  return r.out.split(/\r?\n/).filter(Boolean);
}

/** Windows: removes path once the WebView2 processes using webview (the app's WebView2 data
 * folder) have exited, retrying while the folder is busy (EBUSY, EPERM, ENOTEMPTY), for at most
 * FREE_MS. Then it fails, naming what still holds the folder. */
function removeWhenFree(path: string, webview: string) {
  const deadline = Date.now() + FREE_MS;
  let holders: string[] = [];
  for (;;) {
    holders = webviewHolders(webview);
    if (holders.length === 0) {
      try {
        rmSync(path, { recursive: true, force: true });
        return;
      } catch (e) {
        const code = (e as NodeJS.ErrnoException).code ?? "";
        if (!["EBUSY", "EPERM", "ENOTEMPTY"].includes(code)) throw e;
        if (Date.now() >= deadline)
          throw new Error(
            `${path} is still in use ${FREE_MS / 1000} s after the app quit: ${(e as Error).message}`,
          );
      }
    } else if (Date.now() >= deadline) {
      throw new Error(
        `${path} is still in use ${FREE_MS / 1000} s after the app quit, by ${holders.join(", ")}`,
      );
    }
    Bun.sleepSync(250);
  }
}

const launches: Launch[] = [];
function launch(p: Profile, label: string, extra: Record<string, string> = {}, args: string[] = []): Launch {
  const l = new Launch(bin, label, { ...p.env, ...extra }, args);
  launches.push(l);
  return l;
}

/** An app that stays up and answers commands on stdin (native_test.rs, `instances`). */
async function owner(p: Profile, label: string) {
  const l = launch(p, label, { PLUXY_APP_TEST_INSTANCES: "1" });
  const up = await l.next("up", UP_MS);
  check(up, `${label}: the app never came up: ${l.dump()}`);
  if (up) {
    const window = up.window as Step;
    check(window.exists && window.visible, `${label}: no window shown at start: ${JSON.stringify(window)}`);
    check(up.tray, `${label}: the app has no tray icon`);
  }
  return { app: l, up };
}

/** A later launch with the same profile: exits 0 in time, and starts nothing of its own. */
async function second(p: Profile, label: string, args: string[] = []) {
  const t0 = Date.now();
  const s = launch(p, label, { PLUXY_APP_TEST_INSTANCES: "1" }, args);
  const code = await s.exitWithin(SECOND_EXIT_MS);
  if (code === "timeout") {
    check(false, `${label}: the second launch did not exit within ${SECOND_EXIT_MS / 1000}s: ${s.dump()}`);
    s.kill();
    await s.exited;
  } else {
    check(code === 0, `${label}: the second launch exited with ${code}: ${s.dump()}`);
  }
  check(!s.printed("up"), `${label}: the second launch came up as an app of its own`);
  say(`${label}: exited ${code} after ${Date.now() - t0} ms`);
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** A run of the app's startup stage (native_test.rs): it does what the stage says, reports, quits. */
async function stage(p: Profile, which: string, args: string[] = [], via?: string[]) {
  const env = { ...p.env, PLUXY_APP_TEST_STARTUP: which };
  const l = via
    ? new Launch(via[0] as string, which, env, via.slice(1), windows)
    : new Launch(bin, which, env, args);
  launches.push(l);
  const report = await l.next("startup", UP_MS);
  const code = await l.exitWithin(60_000);
  if (code === "timeout") l.kill();
  return { report, code, dump: l.dump() };
}

function run(cmd: string, args: string[], opts: { verbatim?: boolean } = {}) {
  const r = spawnSync(cmd, args, { encoding: "utf8", windowsVerbatimArguments: opts.verbatim });
  return { status: r.status, out: `${r.stdout ?? ""}${r.stderr ?? ""}`.trim() };
}

const powershell = (script: string) =>
  run("powershell.exe", ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script]);

/** The app's own saved files in the profile. */
const readJson = (file: string) => JSON.parse(readFileSync(file, "utf8"));

// ---- single instance ------------------------------------------------------------------------

async function singleInstance() {
  const p = makeProfile();
  try {
    // Closing leaves the app in the tray (closing asks by default).
    writeFileSync(join(p.dir, "window.json"), JSON.stringify({ close_behavior: "minimize-to-tray" }));
    const lock = join(p.dir, "instance", "lock");
    const address = join(p.dir, "instance", "address");
    const first = await owner(p, "first");
    check(existsSync(lock), "first: no lock file in the app's data folder");
    check(existsSync(address), "first: no address in the app's data folder");
    const where = existsSync(address) ? readFileSync(address, "utf8") : "";
    if (windows) {
      check(
        /^\\\\\.\\pipe\\pluxy-app-[0-9a-f]+$/.test(where),
        `first: the address names ${JSON.stringify(where)}, not a pluxy pipe`,
      );
      const leaf = where.slice(where.lastIndexOf("\\") + 1);
      const listed = powershell(
        "[System.IO.Directory]::GetFiles('\\\\.\\pipe\\') | ForEach-Object { $_.Substring($_.LastIndexOf('\\') + 1) }",
      );
      check(
        listed.status === 0 && listed.out.split(/\r?\n/).includes(leaf),
        `first: the pipe ${leaf} is not among the system's pipes: ${listed.out.slice(0, 300)}`,
      );
      // The pipe's access list, read from outside (positive control: this user connects to it).
      const acl = powershell(
        `$c = New-Object System.IO.Pipes.NamedPipeClientStream('.', '${leaf}', [System.IO.Pipes.PipeDirection]::InOut, [System.IO.Pipes.PipeOptions]::None, [System.Security.Principal.TokenImpersonationLevel]::Identification); ` +
          "$c.Connect(10000); $a = $c.GetAccessControl(); " +
          "$rules = @($a.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])); " +
          "$me = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value; " +
          "@{ me = $me; protected = $a.AreAccessRulesProtected; rules = @($rules | ForEach-Object { @{ sid = $_.IdentityReference.Value; type = $_.AccessControlType.ToString() } }) } | ConvertTo-Json -Compress -Depth 4; " +
          "$c.Dispose()",
      );
      check(acl.status === 0, `first: this user could not connect to its own pipe: ${acl.out.slice(0, 400)}`);
      if (acl.status === 0) {
        const got = JSON.parse(acl.out.split(/\r?\n/).pop() as string) as {
          me: string;
          protected: boolean;
          rules: { sid: string; type: string }[];
        };
        check(got.protected, "first: the pipe inherits access from its parent");
        check(
          got.rules.length === 1 && got.rules[0]?.sid === got.me && got.rules[0]?.type === "Allow",
          `first: the pipe's access list is ${JSON.stringify(got.rules)}, not an Allow for ${got.me} alone`,
        );
      }
    } else {
      const st = existsSync(where) ? statSync(where) : undefined;
      check(st?.isSocket(), `first: the address names ${JSON.stringify(where)}, which is not a socket`);
      check(
        st && (st.mode & 0o777) === 0o600 && st.uid === process.getuid?.(),
        "first: the socket is not this user's alone (mode 600)",
      );
      const folder = statSync(dirname(where));
      check(
        (folder.mode & 0o777) === 0o700 && folder.uid === process.getuid?.(),
        "first: the socket's folder is not this user's alone (mode 700)",
      );
      const lockStat = statSync(lock);
      check((lockStat.mode & 0o777) === 0o600, "first: the lock file is not mode 600");
    }

    // 1. The window is open: a second launch leaves one app, and the window shown.
    await second(p, "second");
    let state = await first.app.command("state");
    check(state?.exists && state.visible, `second: the first's window is ${JSON.stringify(state)}`);
    check(alive(first.app.pid), "second: the first app is gone");

    // 2. Hidden: the second launch shows it.
    state = await first.app.command("hide");
    check(state?.exists && state.visible === false, `hide: the window is ${JSON.stringify(state)}`);
    await second(p, "second after hide");
    state = await first.app.command("state");
    check(state?.exists && state.visible, `after hide: the first's window is ${JSON.stringify(state)}`);

    // 3. Closed (the app stays in the tray): the second launch opens a new one.
    state = await first.app.command("close");
    check(state?.exists === false, `close: the window is ${JSON.stringify(state)}`);
    check(alive(first.app.pid), "close: the app ended with its window, not left in the tray");
    await second(p, "second after close");
    state = await first.app.command("state");
    check(state?.exists && state.visible, `after close: the first's window is ${JSON.stringify(state)}`);
    say(`window after close: ${JSON.stringify(state)}`);

    // 3b. A login launch is a second launch too, with Silent Launch on (which hides the window of a
    // login launch that is the first): the first's window is shown, hidden and closed.
    writeFileSync(
      join(p.dir, "startup.json"),
      JSON.stringify({ autostart_enabled: true, silent_start_enabled: true, start_core_on_launch: true }),
    );
    for (const [how, cmd, closed] of [
      ["hidden", "hide", false],
      ["closed", "close", true],
    ] as const) {
      state = await first.app.command(cmd);
      check(
        closed ? state?.exists === false : state?.exists && state.visible === false,
        `autostart, ${how}: the window is ${JSON.stringify(state)}`,
      );
      await second(p, `autostart launch, window ${how}`, ["--autostart"]);
      state = await first.app.command("state");
      check(
        state?.exists && state.visible,
        `autostart, ${how}: the first's window is ${JSON.stringify(state)}`,
      );
    }

    // 4. The first app is killed with no cleanup: its lock and address stay on disk, and the next
    // launch takes over, starts normally, and is then the one a launch after it raises.
    first.app.kill("SIGKILL");
    await first.app.exited;
    check(existsSync(lock), "kill: no stale lock was left to recover from");
    check(existsSync(address), "kill: no stale address was left to recover from");
    const taken = await owner(p, "after kill");
    await second(p, "second after takeover");
    state = await taken.app.command("state");
    check(
      state?.exists && state.visible,
      `after takeover: the new owner's window is ${JSON.stringify(state)}`,
    );

    // 5. The owner quits: its address, and its socket or pipe, go with it.
    taken.app.send("quit");
    const code = await taken.app.exitWithin(30_000);
    check(code === 0, `quit: the owner exited with ${code}`);
    check(!existsSync(address), "quit: the address stays after the owner quit");
    if (mac) check(!existsSync(where), "quit: the socket stays after the owner quit");
    if (windows) {
      const leaf = where.slice(where.lastIndexOf("\\") + 1);
      const listed = powershell(
        "[System.IO.Directory]::GetFiles('\\\\.\\pipe\\') | ForEach-Object { $_.Substring($_.LastIndexOf('\\') + 1) }",
      );
      check(!listed.out.split(/\r?\n/).includes(leaf), "quit: the pipe stays after the owner quit");
    }
    // The positive control of the whole scenario: with nothing running, a launch is a first.
    const again = await owner(p, "after quit");
    again.app.send("quit");
    check((await again.app.exitWithin(30_000)) === 0, "after quit: the new owner did not quit cleanly");
  } finally {
    for (const l of launches) l.kill("SIGKILL");
    await Bun.sleep(500);
    p.remove();
  }
}

// ---- login item -----------------------------------------------------------------------------

const RUN_KEY = "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run";
const APPROVED_KEY = "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\StartupApproved\\Run";

async function loginItem() {
  const p = makeProfile();
  try {
    if (windows) {
      check(
        run("reg", ["query", RUN_KEY, "/v", identifier]).status !== 0,
        "before: a login item of the app's is in the Run key already",
      );
    } else {
      check(
        !existsSync(join(p.agents, `${identifier}.plist`)),
        "before: a login item of the app's is in the LaunchAgents folder already",
      );
    }

    // Saving the preference, through the app's own command from its own window.
    const enabled = await stage(p, "enable");
    check(
      enabled.code === 0 && enabled.report,
      `enable: the app did not report and quit: ${enabled.code} ${enabled.dump}`,
    );
    check(
      existsSync(join(p.dir, "startup.json")) &&
        readJson(join(p.dir, "startup.json")).autostart_enabled === true,
      "enable: startup.json does not say autostart is on",
    );
    let command: string[] = [];
    if (windows) {
      const q = run("reg", ["query", RUN_KEY, "/v", identifier]);
      check(q.status === 0, `enable: no value ${identifier} in the user's Run key: ${q.out}`);
      const m = /REG_SZ\s+(.*)$/m.exec(q.out);
      const value = m?.[1]?.trim() ?? "";
      check(
        value.toLowerCase() === `"${bin}" --autostart`.toLowerCase(),
        `enable: the Run value is ${JSON.stringify(value)}, not "${bin}" --autostart`,
      );
      // Nothing machine-wide.
      for (const hive of [
        "HKLM\\Software\\Microsoft\\Windows\\CurrentVersion\\Run",
        "HKLM\\Software\\WOW6432Node\\Microsoft\\Windows\\CurrentVersion\\Run",
      ]) {
        check(
          run("reg", ["query", hive, "/v", identifier]).status !== 0,
          `enable: a value ${identifier} is in ${hive}`,
        );
      }
      // What the shell does with the value at login: runs it. The positive control of the checks
      // above: the app comes up with the argument, and quits when its stage is done.
      const consumer = await stage(p, "observe", [], ["cmd.exe", "/d", "/s", "/c", `"${value}"`]);
      check(
        consumer.report?.autostart === true,
        `consumer: the app run from the Run value did not see --autostart: ${consumer.dump}`,
      );
      check(
        consumer.report?.windows === 1,
        `consumer: the login launch opened ${consumer.report?.windows} windows, not one`,
      );
      check(consumer.code === 0, `consumer: exited with ${consumer.code}`);
      command = [value];
    } else {
      const plist = join(p.agents, `${identifier}.plist`);
      check(existsSync(plist), `enable: no ${plist}`);
      if (existsSync(plist)) {
        const lint = run("plutil", ["-lint", plist]);
        check(lint.status === 0, `enable: plutil -lint says ${lint.out}`);
        const json = JSON.parse(run("plutil", ["-convert", "json", "-o", "-", plist]).out) as {
          Label: string;
          ProgramArguments: string[];
          RunAtLoad: boolean;
          LimitLoadToSessionType: string;
        };
        check(json.Label === identifier, `enable: Label is ${json.Label}`);
        check(
          json.ProgramArguments.length === 2 &&
            realpathSync(json.ProgramArguments[0] as string) === realpathSync(bin) &&
            json.ProgramArguments[1] === "--autostart",
          `enable: ProgramArguments is ${JSON.stringify(json.ProgramArguments)}, not [${bin}, --autostart]`,
        );
        check(json.RunAtLoad === true, "enable: RunAtLoad is not true");
        check(
          json.LimitLoadToSessionType === "Aqua",
          `enable: LimitLoadToSessionType is ${json.LimitLoadToSessionType}`,
        );
        check(
          (statSync(plist).mode & 0o022) === 0,
          "enable: the plist can be written by others (launchd would refuse it)",
        );
        // Nothing machine-wide.
        for (const d of ["/Library/LaunchAgents", "/Library/LaunchDaemons"]) {
          check(!existsSync(join(d, `${identifier}.plist`)), `enable: ${identifier}.plist is in ${d}`);
        }
        // launchd accepts it: a copy that does not run at load (and has a label of its own) is loaded
        // into this user's graphical domain, read back, and removed.
        const uid = process.getuid?.() ?? 0;
        const domain = `gui/${uid}`;
        if (run("launchctl", ["print", domain]).status !== 0) {
          skip(`launchd has no ${domain} domain on this machine, so it was not asked to load the agent`);
        } else {
          const label = `${identifier}.p8p4a-${randomUUID().slice(0, 8)}`;
          const copy = join(dirname(plist), `${label}.plist`);
          const text = run("plutil", ["-convert", "xml1", "-o", "-", plist])
            .out.replace(`<string>${identifier}</string>`, `<string>${label}</string>`)
            .replace("<key>RunAtLoad</key>\n\t<true/>", "<key>RunAtLoad</key>\n\t<false/>");
          writeFileSync(copy, text, { mode: 0o600 });
          try {
            const boot = run("launchctl", ["bootstrap", domain, copy]);
            check(boot.status === 0, `launchd: bootstrap of the agent failed: ${boot.out}`);
            const printed = run("launchctl", ["print", `${domain}/${label}`]);
            check(
              printed.status === 0 && printed.out.includes("--autostart"),
              `launchd: it does not list the agent with --autostart: ${printed.out.slice(0, 600)}`,
            );
          } finally {
            run("launchctl", ["bootout", `${domain}/${label}`]);
            rmSync(copy, { force: true });
          }
        }
        // What launchd does at login: runs ProgramArguments. The positive control of the checks
        // above: the app comes up with the argument, and quits when its stage is done.
        command = json.ProgramArguments;
        const consumer = await stage(p, "observe", [], command);
        check(
          consumer.report?.autostart === true,
          `consumer: the app run from ProgramArguments did not see --autostart: ${consumer.dump}`,
        );
        check(
          consumer.report?.windows === 1,
          `consumer: the login launch opened ${consumer.report?.windows} windows, not one`,
        );
        check(consumer.code === 0, `consumer: exited with ${consumer.code}`);
      }
    }
    say(`the login item runs ${JSON.stringify(command)}`);

    // Turning it off removes it, and what the system keeps beside it.
    const disabled = await stage(p, "disable");
    check(
      disabled.code === 0 && disabled.report,
      `disable: the app did not report and quit: ${disabled.code} ${disabled.dump}`,
    );
    check(
      readJson(join(p.dir, "startup.json")).autostart_enabled === false,
      "disable: startup.json still says autostart is on",
    );
    if (windows) {
      check(
        run("reg", ["query", RUN_KEY, "/v", identifier]).status !== 0,
        "disable: the Run value is still there",
      );
      check(
        run("reg", ["query", APPROVED_KEY, "/v", identifier]).status !== 0,
        "disable: a StartupApproved value is left",
      );
    } else {
      check(!existsSync(join(p.agents, `${identifier}.plist`)), "disable: the LaunchAgent is still there");
    }
  } finally {
    for (const l of launches) l.kill("SIGKILL");
    if (windows) {
      run("reg", ["delete", RUN_KEY, "/v", identifier, "/f"]);
      run("reg", ["delete", APPROVED_KEY, "/v", identifier, "/f"]);
      if (run("reg", ["query", RUN_KEY, "/v", identifier]).status === 0)
        console.log("platform: the Run value could not be removed");
    }
    await Bun.sleep(500);
    p.remove();
  }
}

// ---- window and tray ------------------------------------------------------------------------

async function windowAndTray() {
  const p = makeProfile();
  const file = join(p.dir, "window.json");
  try {
    // Minimize to Tray: closing the window destroys it and the app stays, with its tray icon.
    writeFileSync(file, JSON.stringify({ close_behavior: "minimize-to-tray" }));
    const tray = await owner(p, "minimize to tray");
    let state = await tray.app.command("close");
    check(state?.exists === false, `minimize to tray: the window is ${JSON.stringify(state)}`);
    await Bun.sleep(2000);
    check(alive(tray.app.pid), "minimize to tray: the app ended with its window");
    tray.app.send("quit");
    await tray.app.exitWithin(30_000);

    // Exit App: closing the window quits the app.
    writeFileSync(file, JSON.stringify({ close_behavior: "exit" }));
    const exit = await owner(p, "exit");
    exit.app.send("close");
    const code = await exit.app.exitWithin(30_000);
    check(code === 0, `exit: closing the window ended the app with ${code}, not 0`);
    check(!alive(exit.app.pid), "exit: the app is still running after its window was closed");

    // The window's size and place are kept (window.json) and used at the next start. A size and
    // place that fit the smallest runner screen.
    rmSync(file, { force: true });
    const a = await owner(p, "geometry");
    state = await a.app.command("resize 900 640");
    state = await a.app.command("move 20 20");
    check(
      JSON.stringify(state?.size) === JSON.stringify([900, 640]),
      `geometry: the window is ${JSON.stringify(state?.size)}, not 900x640`,
    );
    a.app.send("quit");
    check((await a.app.exitWithin(30_000)) === 0, "geometry: the app did not quit cleanly");
    const saved = readJson(file).geometry as
      | { width: number; height: number; x: number; y: number }
      | undefined;
    check(
      saved && saved.width === 900 && saved.height === 640,
      `geometry: window.json keeps ${JSON.stringify(saved)}, not 900x640`,
    );
    const b = await owner(p, "geometry restart");
    const now = await b.app.command("state");
    const size = now?.size as number[] | undefined;
    const position = now?.position as number[] | undefined;
    check(
      saved && size && near(size[0], saved.width) && near(size[1], saved.height),
      `geometry: the window opened ${JSON.stringify(size)}, saved ${JSON.stringify(saved)}`,
    );
    check(
      saved && position && near(position[0], saved.x) && near(position[1], saved.y),
      `geometry: the window opened at ${JSON.stringify(position)}, saved at ${JSON.stringify(saved && [saved.x, saved.y])}`,
    );
    b.app.send("quit");
    await b.app.exitWithin(30_000);
    // The positive control of the two checks above: with nothing saved the window is not the saved
    // one (a screen may be too small for the default size, so only that is asked).
    writeFileSync(file, JSON.stringify({ close_behavior: "minimize-to-tray" }));
    const c = await owner(p, "nothing saved");
    const def = (await c.app.command("state"))?.size as number[] | undefined;
    check(
      def && !(near(def[0], 900) && near(def[1], 640)),
      `geometry: with nothing saved the window is ${JSON.stringify(def)}, as it was saved`,
    );
    c.app.send("quit");
    await c.app.exitWithin(30_000);
  } finally {
    for (const l of launches) l.kill("SIGKILL");
    await Bun.sleep(500);
    p.remove();
  }
}

// ---- the test package ----------------------------------------------------------------------

/** What the test package must be built as; written here, not read from what package.ts made. */
const TEST_ID = "dev.pluxy.desktop.test";
const TEST_PRODUCT = "pluxy Test";
const appVersion: string = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8")).version;

/** What package.ts test wrote into its folder's test-package.json on Windows and macOS. */
interface BundlePackage {
  /** The folder bundle (Windows) or app bundle (macOS). */
  bundle: string;
  /** The app executable inside it. */
  program: string;
  updater_pubkey: string;
  update: { bundle: string; payload: string; target: string; version: string; signature: string };
  /** Windows: the per-user setup program, at the app's version. */
  setup?: string;
}

function testPackage(): BundlePackage | undefined {
  const dir = process.env.PLUXY_APP_TEST_PACKAGE;
  if (!dir) {
    check(false, "PLUXY_APP_TEST_PACKAGE names no test package (bun run package:test <dir> first)");
    return undefined;
  }
  return readJson(join(dir, "test-package.json")) as BundlePackage;
}

/** A private folder of this user's for the test trust profile, the app's HOME (its updater gate
 * reads the profile from HOME). macOS: the profile's own home, made owner-only. Windows: a folder
 * under the runner's temporary folder whose access list lets in only this user, SYSTEM and
 * Administrators, and is inherited by the files made in it. */
function privateHome(p: Profile): string {
  if (mac) {
    const home = p.env.HOME as string;
    chmodSync(home, 0o700);
    return home;
  }
  return ownFolder(mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-trust-")));
}

/** Windows: makes folder (if need be) one whose access list lets in only this user, SYSTEM and
 * Administrators, inherited by what is made in it. Elsewhere: mode 700. */
function ownFolder(folder: string): string {
  if (!windows) return privateFolder(folder);
  mkdirSync(folder, { recursive: true });
  const user = run("whoami", []).out;
  const set = run("icacls", [
    folder,
    "/inheritance:r",
    "/grant:r",
    `${user}:(OI)(CI)F`,
    "*S-1-5-18:(OI)(CI)F",
    "*S-1-5-32-544:(OI)(CI)F",
  ]);
  if (set.status !== 0) throw new Error(`icacls ${folder}: ${set.out}`);
  return folder;
}

/** Writes a test trust profile in home trusting pubkey for the app's own update, and its marker.
 * Returns the marker's nonce, PLUXY_TEST_TRUST_NONCE for the app. */
function writeUpdaterTrust(home: string, pubkey: string): string {
  writeFileSync(
    join(home, PROFILE_FILE),
    JSON.stringify({ roots: [], updater: { kind: "test", public_key: pubkey } }),
    {
      mode: 0o600,
    },
  );
  const nonce = randomBytes(32).toString("hex");
  writeFileSync(
    join(home, MARKER_FILE),
    JSON.stringify({
      format: 1,
      home: realpathSync.native(home),
      purpose: "pluxy-artifact-test-trust",
      nonce,
    }),
    { mode: 0o600 },
  );
  return nonce;
}

/** Starts program as the person would, waits for its updater gate's report and its window, then
 * quits it: it must exit 0 and leave no process. Returns the gate's report. */
async function gateRun(program: string, label: string, env: Record<string, string | undefined>) {
  const l = new Launch(program, label, { ...env, PLUXY_APP_TEST_INSTANCES: "1" });
  launches.push(l);
  const gate = await l.next("updater_gate", UP_MS);
  const up = await l.next("up", UP_MS);
  check(up, `${label}: the app never came up: ${l.dump()}`);
  say(`${label}: updater_gate ${JSON.stringify(gate)}`);
  l.send("quit");
  const code = await l.exitWithin(60_000);
  if (code === "timeout") l.kill();
  check(code === 0, `${label}: quit exited with ${code}: ${l.dump()}`);
  await Bun.sleep(500);
  check(!alive(l.pid), `${label}: the app's process ${l.pid} is still running after it quit`);
  return gate;
}

async function bundle() {
  const made = testPackage();
  if (!made) return;
  const exe = windows ? "pluxy-app-test.exe" : "pluxy-app-test";
  // What the bundle is, read with the system's own tools.
  if (windows) {
    const files = readdirSync(made.bundle).sort();
    check(
      JSON.stringify(files) === JSON.stringify(SET_PARTS),
      `the folder bundle holds ${files.join(", ")}, not only the program set ${SET_PARTS.join(", ")}`,
    );
    check(made.program === join(made.bundle, exe), `the program ${made.program} is not the bundle's ${exe}`);
    // The update is the second build's set as a .tar.gz, its parts at the top. Read with Windows'
    // own tar: the one first on the runner's PATH (Git's GNU tar) takes `D:` for a remote host.
    const tar = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe");
    const read = run(tar, ["-tzf", made.update.payload]);
    check(read.status === 0, `${tar} could not list the update archive: ${read.out}`);
    const listed = read.out.split(/\r?\n/).filter(Boolean);
    const tops = [...new Set(listed.map((e) => e.replace(/\/.*$/, "")))].sort();
    check(
      JSON.stringify(tops) === JSON.stringify(SET_PARTS),
      `the update archive's top entries are ${tops.join(", ")}, not the program set's`,
    );
    check(made.update.target === "windows-x86_64-set", `the update's target is ${made.update.target}`);
    say(`update archive: ${listed.length} entries`);
  } else {
    const plist = join(made.bundle, "Contents", "Info.plist");
    const key = (k: string) => run("plutil", ["-extract", k, "raw", "-o", "-", plist]).out;
    check(made.bundle.endsWith(`${TEST_PRODUCT}.app`), `the app bundle is ${made.bundle}`);
    check(key("CFBundleIdentifier") === TEST_ID, `Info.plist names ${key("CFBundleIdentifier")}`);
    check(key("CFBundleExecutable") === "pluxy-app-test", `Info.plist runs ${key("CFBundleExecutable")}`);
    check(
      key("CFBundleShortVersionString") === appVersion,
      `Info.plist says version ${key("CFBundleShortVersionString")}`,
    );
    check(
      run("xattr", ["-p", "com.apple.quarantine", made.bundle]).status !== 0,
      "the app bundle is quarantined",
    );
    const listed = run("tar", ["-tzf", made.update.payload]).out.split(/\r?\n/).filter(Boolean);
    check(
      listed.length > 0 && listed.every((e) => e.startsWith(`${TEST_PRODUCT}.app/`) && !e.includes("/._")),
      `the update archive holds more than one app bundle: ${listed.slice(0, 5).join(", ")}`,
    );
    say(`update archive: ${listed.length} entries under ${TEST_PRODUCT}.app/`);
  }
  check(
    made.update.version !== appVersion && made.update.signature.length > 0,
    "the update has no newer signed version",
  );

  // The test package, from its bundle, with its trust profile: the gate passes.
  let p = makeProfile(TEST_ID);
  try {
    const home = privateHome(p);
    const nonce = writeUpdaterTrust(home, made.updater_pubkey);
    const gate = await gateRun(made.program, "test package", {
      ...p.env,
      HOME: home,
      PLUXY_TEST_TRUST_NONCE: nonce,
    });
    check(
      gate?.passed === true &&
        gate.marker === true &&
        gate.identifier === TEST_ID &&
        gate.product === TEST_PRODUCT,
      `the test package's updater gate did not pass as ${TEST_ID}/${TEST_PRODUCT}: ${JSON.stringify(gate)}`,
    );
    if (windows) rmSync(home, { recursive: true, force: true });
  } finally {
    p.remove();
  }
  // The same bundle with the profile in a HOME other users can use: refused (Windows: an access
  // list letting Everyone change it; macOS: a mode letting others read it).
  p = makeProfile(TEST_ID);
  try {
    const home = privateHome(p);
    const nonce = writeUpdaterTrust(home, made.updater_pubkey);
    if (windows) {
      const opened = run("icacls", [home, "/grant", "*S-1-1-0:(OI)(CI)M"]);
      if (opened.status !== 0) throw new Error(`icacls ${home}: ${opened.out}`);
    } else {
      chmodSync(home, 0o755);
    }
    const gate = await gateRun(made.program, "test package, shared HOME", {
      ...p.env,
      HOME: home,
      PLUXY_TEST_TRUST_NONCE: nonce,
    });
    check(
      gate?.passed === false && /private owned directory/.test(String(gate.error)),
      `the test package's trust profile in a shared HOME was not refused: ${JSON.stringify(gate)}`,
    );
    if (windows) rmSync(home, { recursive: true, force: true });
  } finally {
    p.remove();
  }
  // The same bundle, its HOME private but without the profile: refused.
  p = makeProfile(TEST_ID);
  try {
    const home = privateHome(p);
    const gate = await gateRun(made.program, "test package, no profile", { ...p.env, HOME: home });
    check(
      gate?.passed === false && gate.identifier === TEST_ID && /not configured/.test(String(gate.error)),
      `the test package without its trust profile was not refused: ${JSON.stringify(gate)}`,
    );
    if (windows) rmSync(home, { recursive: true, force: true });
  } finally {
    p.remove();
  }
  // The dev app (the normal identifier, no test trust compiled in) with the same profile: refused.
  p = makeProfile();
  try {
    const home = privateHome(p);
    const nonce = writeUpdaterTrust(home, made.updater_pubkey);
    const gate = await gateRun(bin, "dev app", { ...p.env, HOME: home, PLUXY_TEST_TRUST_NONCE: nonce });
    check(
      gate?.passed === false &&
        gate.marker === true &&
        gate.identifier === identifier &&
        /not configured/.test(String(gate.error)),
      `the dev app's updater gate was not refused for ${identifier}: ${JSON.stringify(gate)}`,
    );
    if (windows) rmSync(home, { recursive: true, force: true });
  } finally {
    p.remove();
  }
}

/** A bundle of this system's kind around program, named for release: a folder holding
 * pluxy-app.exe (Windows), or pluxy.app with its Info.plist and Contents/MacOS/pluxy-app. */
function releaseBundle(dir: string, program: string): string {
  mkdirSync(dir, { recursive: true });
  if (windows) {
    const folder = join(dir, "pluxy");
    mkdirSync(folder);
    copyFileSync(program, join(folder, "pluxy-app.exe"));
    return folder;
  }
  const bundle = join(dir, "pluxy.app");
  mkdirSync(join(bundle, "Contents", "MacOS"), { recursive: true });
  copyFileSync(program, join(bundle, "Contents", "MacOS", "pluxy-app"));
  writeFileSync(
    join(bundle, "Contents", "Info.plist"),
    `<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict>\n<key>CFBundleExecutable</key><string>pluxy-app</string>\n<key>CFBundleIdentifier</key><string>${identifier}</string>\n<key>CFBundleName</key><string>pluxy</string>\n</dict></plist>\n`,
  );
  return bundle;
}

async function bundleReleaseGate() {
  const made = testPackage();
  const dir = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-release-"));
  try {
    const gate = (what: string, artifacts: string[], want: "valid" | RegExp) => {
      const r = spawnSync(process.execPath, ["run", "package:release", ...artifacts], {
        cwd: app,
        encoding: "utf8",
        stdio: ["ignore", "pipe", "inherit"],
      });
      const verdict = /^release-package: .*$/m.exec(r.stdout ?? "")?.[0] ?? "no verdict";
      say(`${what}: exit ${r.status}: ${verdict}`);
      check(
        want === "valid"
          ? r.status === 0 && verdict.startsWith("release-package: valid")
          : r.status === 1 && verdict.startsWith("release-package: refused") && want.test(verdict),
        `${what}: exit ${r.status}: ${verdict}`,
      );
    };
    gate("a clean release build", [], "valid");
    const release = join(targetDir, "release", windows ? "pluxy-app.exe" : "pluxy-app");
    gate("a clean bundle", [releaseBundle(join(dir, "clean"), release)], "valid");
    gate(
      "a bundle holding the test-feature dev app",
      [releaseBundle(join(dir, "marked"), bin)],
      windows
        ? /pluxy!pluxy-app\.exe: test build marker\/identity in artifact/
        : /pluxy\.app!Contents\/MacOS\/pluxy-app: test build marker\/identity in artifact/,
    );
    if (made) {
      const renamed = join(dir, windows ? "candidate" : "candidate.app");
      if (windows) {
        cpSync(made.bundle, renamed, { recursive: true });
      } else {
        const copied = run("ditto", [made.bundle, renamed]);
        if (copied.status !== 0) throw new Error(`ditto: ${copied.out}`);
      }
      // Refused for what is in it, not its name: the test program's name (Windows), or the test
      // identity in its Info.plist, which the gate reads first (macOS).
      gate(
        "the test package, renamed",
        [renamed],
        windows
          ? /candidate!pluxy-app-test\.exe: test file name in package/
          : /candidate\.app!Contents\/Info\.plist: test build marker\/identity in artifact/,
      );
      gate("the test package", [made.bundle], /test\/private release input or artifact path/);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// ---- the update -----------------------------------------------------------------------------

/** The test core, `go run ./e2e/panelcore` (as dialogs.ts starts it): its address and key. It
 * runs until its stdin ends. */
async function startCore(): Promise<{ proc: ChildProcess; base: string; key: string }> {
  const proc = spawn("go", ["run", "./e2e/panelcore"], {
    cwd: join(app, ".."),
    stdio: ["pipe", "pipe", "inherit"],
  });
  const line = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`panelcore exited with ${code}`)));
    proc.once("error", reject);
    if (proc.stdout) createInterface({ input: proc.stdout }).once("line", resolve);
  });
  return { proc, ...JSON.parse(line) };
}

/** The test package's program with the bundle type an .msi gives it: tauri-bundler patches the
 * first __TAURI_BUNDLE_TYPE_VAR_UNK with the type's (`MSI`). */
function patchedBundleType(program: string, kind: "MSI"): Buffer {
  const bytes = Buffer.from(readFileSync(program));
  const at = bytes.indexOf(Buffer.from("__TAURI_BUNDLE_TYPE_VAR_UNK"));
  if (at < 0) throw new Error(`${program} has no unpatched bundle type`);
  bytes.write(`__TAURI_BUNDLE_TYPE_VAR_${kind}`, at, "latin1");
  return bytes;
}

async function update() {
  const made = testPackage();
  if (!made) return;
  const system = windows ? "win32" : "darwin";
  const exe = windows ? "pluxy-app-test.exe" : "pluxy-app-test";
  const p = makeProfile(TEST_ID);
  const root = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-update-"));
  // Windows: the app keeps its connection in its local folder, which makeProfile's remove takes.
  const local = windows ? join(process.env.LOCALAPPDATA as string, TEST_ID) : p.dir;
  let core: Awaited<ReturnType<typeof startCore>> | undefined;
  let feed: ReturnType<typeof startUpdateFeed> | undefined;
  // The trust profile's home: on Windows a folder of its own under the runner's temporary folder,
  // removed below; on macOS the profile's own, removed with it.
  let home: string | undefined;
  try {
    if (windows && existsSync(local))
      throw new Error(`${local} exists: this is not a fresh runner, and the test would write over it`);
    home = privateHome(p);
    const nonce = writeUpdaterTrust(home, made.updater_pubkey);
    const work = privateFolder(join(root, "work"));

    // The installed test package, a copy in a folder of this user's: Windows, its folder bundle
    // in a folder only this user (and SYSTEM and Administrators) may change; macOS, its app
    // bundle in the profile's Applications folder, started by its real path.
    let installed: string;
    let program: string;
    // macOS: the program by the path the bundle was put at, which goes through a link (the
    // system's temporary folder is under /var, a link to /private/var): the "linked" refusal.
    let linkedProgram = "";
    if (windows) {
      // The folder bundle's program set: the folder is the installed copy.
      installed = join(ownFolder(join(root, "install")), basename(made.bundle));
      cpSync(made.bundle, installed, { recursive: true });
      program = join(installed, exe);
    } else {
      const put = join(privateFolder(join(home, "Applications")), basename(made.bundle));
      const copied = run("ditto", ["--norsrc", "--noextattr", "--noqtn", made.bundle, put]);
      if (copied.status !== 0) throw new Error(`ditto: ${copied.out}`);
      installed = realpathSync(put);
      program = join(installed, "Contents", "MacOS", exe);
      linkedProgram = join(put, "Contents", "MacOS", exe);
      check(
        installed !== put,
        `the installed copy's path ${put} has no link in it, so the linked case does not start the app through one`,
      );
    }
    const v1 = pinOf(installed);
    writeFileSync(join(work, "v1.sha256"), v1);
    const payload = readFileSync(made.update.payload);
    // The new version's pin: the program set or the tree of the bundle the archive holds.
    const v2 = windows ? setDigest(made.update.bundle) : treeDigest(made.update.bundle);
    say(`installed ${installed}: v1 ${v1}, v2 ${v2}`);

    say("starting the test core");
    core = await startCore();
    mkdirSync(local, { recursive: true });
    writeFileSync(
      join(local, "connection.json"),
      JSON.stringify({ mode: "local", url: core.base, key: core.key }),
      { mode: 0o600 },
    );
    writeFileSync(join(p.dir, "window.json"), JSON.stringify({ close_behavior: "minimize-to-tray" }), {
      mode: 0o600,
    });

    const served = startUpdateFeed({
      dir: work,
      payload,
      file: basename(made.update.payload),
      target: made.update.target,
      version: made.update.version,
      signature: made.update.signature,
      pubkey: made.updater_pubkey,
    });
    feed = served;
    // Installers and elevation: never run by the app; each stand-in logs if it is.
    const stand = standIns(
      root,
      windows ? ["msiexec", "runas", "schtasks", "sc"] : ["osascript", "sudo", "installer", "authopen"],
    );
    const env = {
      ...withPathFirst({ ...p.env, HOME: home }, stand.bin),
      PLUXY_APP_TEST_UPDATE: work,
      PLUXY_APP_TEST_FEED: served.url,
      PLUXY_APP_TEST_FEED_CA: served.ca,
    };
    const trusted = { ...env, PLUXY_TEST_TRUST_NONCE: nonce };
    const unchanged = () => pinOf(installed) === v1;
    const seen = new Set<string>();
    const add = (names: Set<string>) => {
      for (const n of names) seen.add(n);
    };
    const refuse = async (refusal: Refusal, prog: string, e: Record<string, string>) =>
      add(await refusedLaunch({ refusal, program: prog, env: e, check, say, feed: served, unchanged }));

    // No updater trust (no nonce); the first launch, which sets the feed.
    await refuse(NO_ROOTS, program, env);

    // This system's crafted installs, each in a folder of its own.
    for (const refusal of REFUSALS[system]) {
      // The installed bundle itself, started through the link on its path.
      if (refusal.case === "linked") {
        await refuse(refusal, linkedProgram, trusted);
        continue;
      }
      const folder = ownFolder(join(root, `crafted-${refusal.case}`));
      let crafted: string;
      if (windows) {
        const bundle = join(folder, basename(made.bundle));
        mkdirSync(bundle);
        crafted = join(bundle, exe);
        if (refusal.case === "msi") {
          writeFileSync(crafted, patchedBundleType(made.program, "MSI"));
        } else {
          copyFileSync(made.program, crafted);
          // Everyone may change the bundle's folder (and, inherited, the program).
          const opened = run("icacls", [bundle, "/grant", "*S-1-1-0:(OI)(CI)M"]);
          if (opened.status !== 0) throw new Error(`icacls ${bundle}: ${opened.out}`);
        }
      } else if (refusal.case === "bare") {
        crafted = join(folder, exe);
        copyFileSync(program, crafted);
        chmodSync(crafted, 0o755);
      } else {
        const bundle = join(folder, basename(made.bundle));
        const copied = run("ditto", ["--norsrc", "--noextattr", "--noqtn", installed, bundle]);
        if (copied.status !== 0) throw new Error(`ditto: ${copied.out}`);
        chmodSync(bundle, 0o775);
        crafted = join(bundle, "Contents", "MacOS", exe);
      }
      await refuse(refusal, crafted, trusted);
    }

    // macOS: the installed bundle started once by LaunchServices, as Finder or the Dock would:
    // its provenance holds, and the update is offered.
    if (mac) {
      const out = join(root, "open.log");
      const args = ["-n", "-W", "--stdout", out, "--stderr", out];
      for (const [k, v] of Object.entries({ ...trusted, PLUXY_APP_TEST_UPDATE_CASE: "open" }))
        if (k === "HOME" || k === "PATH" || k.startsWith("PLUXY_")) args.push("--env", `${k}=${v}`);
      const opened = spawnSync("/usr/bin/open", [...args, installed], {
        encoding: "utf8",
        timeout: LAUNCH_MS,
      });
      const lines = existsSync(out) ? readFileSync(out, "utf8").split("\n") : [];
      const step = (name: string) => {
        const l = lines.find((x) => x.startsWith(`native-test: ${name} `));
        return l ? (JSON.parse(l.slice(`native-test: ${name} `.length)) as Step) : undefined;
      };
      const s = step("update_open");
      if (s) seen.add("update_open");
      say(`open: exit ${opened.status}: ${JSON.stringify(s ?? step("failed") ?? lines.slice(-20))}`);
      check(opened.status === 0, `open: /usr/bin/open exited with ${opened.status}: ${opened.stderr ?? ""}`);
      check(
        s?.available === "true" &&
          s.installed_sha === v1 &&
          String(s.installed).endsWith(basename(installed)),
        `open: the app started by LaunchServices was not offered the update from its bundle: ${JSON.stringify(s)}`,
      );
      check(unchanged(), "open: the installed copy changed");
    }
    check(stand.ran() === "", `an installer or elevation ran: ${stand.ran()}`);

    // Windows: the update ended between two parts of the program set, then the earlier version
    // started again on the mixed folder.
    if (windows) {
      const updates = join(local, "updates");
      let between: Record<string, unknown> | undefined;
      let mixed = "";
      let asides: string[] = [];
      const parts = await launchChain({
        label: "parts",
        program,
        env: { ...trusted, PLUXY_APP_TEST_UPDATE_CASE: "parts" },
        ms: LAUNCH_MS,
        check,
        say,
        onStep: (name, v, child) => {
          if (name !== "update_between_parts") return;
          between = v;
          // What the folder holds as the update waits there, read before it is ended.
          try {
            mixed = pinOf(installed);
          } catch (e) {
            mixed = `unreadable: ${(e as Error).message}`;
          }
          asides = readdirSync(installed).filter((n) => n.startsWith(".pluxy-old-"));
          check(
            v.pid === child.pid,
            `the process waiting between parts is ${v.pid}, not the launch ${child.pid}`,
          );
          child.kill("SIGKILL");
        },
      });
      for (const n of parts.names) seen.add(n);
      say(`between parts: ${JSON.stringify(between)}; asides ${asides.join(", ")}; ended ${parts.ended}`);
      check(
        between?.part === "bundled",
        `the update did not wait after its first part: ${JSON.stringify(between)}`,
      );
      check(
        mixed !== v1 && mixed !== v2,
        `the folder was not half replaced when the update was ended: ${mixed}`,
      );
      check(
        asides.length === 1 && asides[0]?.endsWith("-bundled"),
        `asides when the update was ended: ${asides.join(", ")}`,
      );
      check(existsSync(join(updates, "pending.json")), "the ended update left no pending.json");
      const undone = await launchChain({
        label: "parts undone",
        program,
        env: { ...trusted, PLUXY_APP_TEST_UPDATE_CASE: "parts-undone" },
        ms: LAUNCH_MS,
        check,
        say,
      });
      for (const n of undone.names) seen.add(n);
      const u = undone.step("update_parts_undone")[0];
      say(`the start after: ${JSON.stringify(u)}`);
      check(u?.version === V1, `the earlier version did not start on the mixed folder: ${JSON.stringify(u)}`);
      check(
        u?.installed_sha === v1 && pinOf(installed) === v1,
        `the start after did not undo it: ${JSON.stringify(u)}`,
      );
      check(u?.pending === false, "pending.json is still there after the undo");
      check(u?.kept_sha === v1, `the earlier set is not kept as ${V1}: ${JSON.stringify(u)}`);
      check(
        JSON.stringify(u?.beside) === JSON.stringify(SET_PARTS) &&
          JSON.stringify(readdirSync(installed).sort()) === JSON.stringify(SET_PARTS),
        `in the program's folder after the undo: ${readdirSync(installed).join(", ")}`,
      );
    }

    // The update route.
    add(
      await updateRoute({
        program,
        installed,
        third: program,
        env: trusted,
        dir: work,
        v1,
        v2,
        v2Version: made.update.version,
        managed: false,
        feed: served,
        check,
        say,
      }),
    );
    checkSteps(check, system, seen);
    check(stand.ran() === "", `an installer or elevation ran: ${stand.ran()}`);
    // Nothing an install made is left next to the installed copy (on Windows, in the set's
    // folder).
    const left = readdirSync(windows ? installed : dirname(installed)).sort();
    check(
      JSON.stringify(left) === JSON.stringify(ownNames(installed)),
      `next to the installed copy at the end: ${left.join(", ")}`,
    );
    if (mac)
      check(
        run("xattr", ["-p", "com.apple.quarantine", installed]).status !== 0,
        "the installed app bundle is quarantined",
      );
    say(`requests: ${JSON.stringify(Object.fromEntries(served.requests))}`);
  } finally {
    feed?.stop();
    if (core) {
      const proc = core.proc;
      proc.stdin?.end();
      if (proc.exitCode === null) await new Promise((r) => proc.once("exit", r));
    }
    await Bun.sleep(500);
    if (windows) {
      removeWhenFree(root, local);
      if (home) removeWhenFree(home, local);
    } else rmSync(root, { recursive: true, force: true });
    p.remove();
  }
}

// ---- the install (Windows: the per-user setup program) ---------------------------------------

/** The setup's uninstall entry for the test product (Tauri's NSIS template: per user, HKCU). */
const UNINSTALL_KEY = `HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${TEST_PRODUCT}`;
/** The same entry machine-wide, in either registry view: a per-user setup writes neither. */
const MACHINE_UNINSTALL_KEYS = [
  `HKLM\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${TEST_PRODUCT}`,
  `HKLM\\Software\\WOW6432Node\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\${TEST_PRODUCT}`,
];

/** A registry value's data as reg prints it, or undefined when the key or the value is not there. */
function regValue(key: string, name: string): string | undefined {
  const q = run("reg", ["query", key, "/v", name]);
  if (q.status !== 0) return undefined;
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^\\s*${escaped}\\s+REG_\\w+\\s+(.*)$`, "m").exec(q.out)?.[1]?.trimEnd();
}

const regKeyExists = (key: string) => run("reg", ["query", key]).status === 0;

/** Where the setup records the folder it installed into, which a later setup installs into again
 * and the uninstaller keeps (the template's MANUPRODUCTKEY, `Software\<manufacturer>\<product>`;
 * with no publisher set the manufacturer is the identifier's second part). */
const SETUP_RECORD = `HKCU\\Software\\pluxy\\${TEST_PRODUCT}`;

/** Runs the setup program silently; its exit code and what it wrote. */
function runSetup(setup: string) {
  const r = spawnSync(setup, ["/S"], { encoding: "utf8", timeout: 300_000 });
  return { status: r.status, out: `${r.stdout ?? ""}${r.stderr ?? ""}`.trim() };
}

/** Runs the setup's uninstaller silently, as NSIS runs it itself: a copy outside the folder, told
 * the folder with `_?=` (the last argument, unquoted), so this waits for it and gets its exit code.
 * The folder's own uninstall.exe started there would copy itself to a temporary folder, start that
 * copy and exit at once. */
function runUninstaller(folder: string) {
  const dir = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-un-"));
  try {
    const copy = join(dir, "uninstall.exe");
    copyFileSync(join(folder, "uninstall.exe"), copy);
    const r = spawnSync(copy, ["/S", `_?=${folder}`], {
      encoding: "utf8",
      timeout: 300_000,
      windowsVerbatimArguments: true,
    });
    return { status: r.status, out: `${r.stdout ?? ""}${r.stderr ?? ""}`.trim() };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** The execution level a program's manifest asks for, read from its bytes (NSIS embeds the
 * manifest as plain text), and whether it names a higher one anywhere. */
function executionLevel(program: string): { level?: string; higher: boolean } {
  const text = readFileSync(program).toString("latin1");
  return {
    level: /<requestedExecutionLevel\s+level="(\w+)"/.exec(text)?.[1],
    higher: /requireAdministrator|highestAvailable/.test(text),
  };
}

/** The shortcuts named for the test product: on the user's desktop and in the user's Start menu,
 * and the machine-wide ones (the Public desktop, the all-users Start menu); and the two places a
 * silent install puts the user's (Tauri's template: `$DESKTOP` and `$SMPROGRAMS`, no Start menu
 * folder set). */
function shortcuts(): { user: string[]; machine: string[]; expected: string[] } {
  const r = powershell(
    "$d = [Environment]::GetFolderPath('Desktop'); $s = [Environment]::GetFolderPath('Programs'); " +
      "$cd = [Environment]::GetFolderPath('CommonDesktopDirectory'); $cs = [Environment]::GetFolderPath('CommonPrograms'); " +
      "@{ user = @($d, $s); machine = @($cd, $cs) } | ConvertTo-Json -Compress",
  );
  if (r.status !== 0) throw new Error(`the shortcut folders: ${r.out}`);
  const dirs = JSON.parse(r.out.split(/\r?\n/).pop() as string) as { user: string[]; machine: string[] };
  const found = (folders: string[]) => {
    const out: string[] = [];
    const walk = (dir: string, depth: number) => {
      if (!existsSync(dir) || depth > 2) return;
      for (const e of readdirSync(dir, { withFileTypes: true })) {
        if (e.isDirectory()) walk(join(dir, e.name), depth + 1);
        else if (e.name.toLowerCase() === `${TEST_PRODUCT}.lnk`.toLowerCase()) out.push(join(dir, e.name));
      }
    };
    for (const d of folders) walk(d, 0);
    return out;
  };
  return {
    user: found(dirs.user),
    machine: found(dirs.machine),
    expected: dirs.user.map((d) => join(d, `${TEST_PRODUCT}.lnk`)),
  };
}

/** What Windows will rename or delete at the next restart under folder (NSIS's /REBOOTOK
 * fallback, MoveFileEx with MOVEFILE_DELAY_UNTIL_REBOOT, writes these machine-wide), or "". */
function restartDeletes(folder: string): string {
  const q = run("reg", [
    "query",
    "HKLM\\SYSTEM\\CurrentControlSet\\Control\\Session Manager",
    "/v",
    "PendingFileRenameOperations",
  ]);
  if (q.status !== 0) return "";
  return q.out
    .split(/\\0|\r?\n/)
    .filter((l) => l.toLowerCase().includes(folder.toLowerCase()))
    .join("; ");
}

/** Folders a machine-wide install would have made. */
const machineFolders = () =>
  [process.env.ProgramFiles, process.env["ProgramFiles(x86)"]]
    .filter((d): d is string => Boolean(d))
    .map((d) => join(d, TEST_PRODUCT));

async function install() {
  if (mac) {
    skip("no installer on macOS yet: the dmg is a later slice, so there is nothing to install");
    return;
  }
  const made = testPackage();
  if (!made) return;
  check(made.setup && existsSync(made.setup), `the test package has no setup program: ${made.setup}`);
  if (!made.setup) return;
  const setup = made.setup;
  const exe = "pluxy-app-test.exe";
  const localAppData = process.env.LOCALAPPDATA as string;
  const folder = join(localAppData, TEST_PRODUCT);
  const program = join(folder, exe);
  const local = join(localAppData, TEST_ID);
  for (const [what, there] of [
    [folder, existsSync(folder)],
    [local, existsSync(local)],
    [UNINSTALL_KEY, regKeyExists(UNINSTALL_KEY)],
    [SETUP_RECORD, regKeyExists(SETUP_RECORD)],
  ] as const)
    if (there)
      throw new Error(`${what} exists: this is not a fresh runner, and the test would write over it`);
  const p = makeProfile(TEST_ID);
  const root = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-install-"));
  let core: Awaited<ReturnType<typeof startCore>> | undefined;
  let feed: ReturnType<typeof startUpdateFeed> | undefined;
  let home: string | undefined;
  let running: Launch | undefined;
  let outside: string | undefined;
  const runCommand = `"${program}" --autostart`;
  try {
    // 1. Install, silently, as this user.
    const before = shortcuts();
    check(
      before.user.length === 0 && before.machine.length === 0,
      `shortcuts before the install: ${JSON.stringify(before)}`,
    );
    const installed = runSetup(setup);
    say(`setup /S: exit ${installed.status}${installed.out ? `: ${installed.out}` : ""}`);
    check(installed.status === 0, `the setup exited with ${installed.status}: ${installed.out}`);

    // 2. Where it went, and nothing machine-wide.
    const files = existsSync(folder) ? readdirSync(folder).sort() : [];
    check(
      JSON.stringify(files) === JSON.stringify([...SET_PARTS, "uninstall.exe"].sort()),
      `the install folder ${folder} holds ${files.join(", ")}, not the program set and uninstall.exe`,
    );
    check(
      existsSync(join(folder, "pluxy.exe")) &&
        readFileSync(join(folder, "pluxy.exe"), "utf8") === `stub core ${appVersion}\n`,
      "the installed pluxy.exe is not the set's (the stub of this version)",
    );
    check(
      existsSync(join(folder, "bundled", "stub.txt")),
      `the installed bundled\\ holds ${existsSync(join(folder, "bundled")) ? readdirSync(join(folder, "bundled")).join(", ") : "nothing"}`,
    );
    const entry = {
      DisplayName: regValue(UNINSTALL_KEY, "DisplayName"),
      DisplayVersion: regValue(UNINSTALL_KEY, "DisplayVersion"),
      InstallLocation: regValue(UNINSTALL_KEY, "InstallLocation"),
      UninstallString: regValue(UNINSTALL_KEY, "UninstallString"),
    };
    say(`uninstall entry: ${JSON.stringify(entry)}`);
    check(entry.DisplayName === TEST_PRODUCT, `the uninstall entry's DisplayName is ${entry.DisplayName}`);
    check(
      entry.DisplayVersion === appVersion,
      `the uninstall entry's DisplayVersion is ${entry.DisplayVersion}`,
    );
    check(
      entry.InstallLocation?.toLowerCase() === `"${folder}"`.toLowerCase(),
      `the uninstall entry's InstallLocation is ${entry.InstallLocation}, not "${folder}"`,
    );
    check(
      entry.UninstallString?.toLowerCase() === `"${join(folder, "uninstall.exe")}"`.toLowerCase(),
      `the uninstall entry's UninstallString is ${entry.UninstallString}`,
    );
    for (const key of MACHINE_UNINSTALL_KEYS) check(!regKeyExists(key), `the setup wrote ${key}`);
    for (const d of machineFolders()) check(!existsSync(d), `the setup made ${d}`);
    const placed = shortcuts();
    say(`shortcuts: ${JSON.stringify(placed)}`);
    const profile = (process.env.USERPROFILE as string).toLowerCase();
    check(placed.machine.length === 0, `machine-wide shortcuts: ${placed.machine.join(", ")}`);
    const lower = (l: string[]) => l.map((x) => x.toLowerCase()).sort();
    check(
      JSON.stringify(lower(placed.user)) === JSON.stringify(lower(placed.expected)),
      `the user's shortcuts are ${JSON.stringify(placed.user)}, not ${JSON.stringify(placed.expected)}`,
    );
    check(
      placed.user.every((s) => s.toLowerCase().startsWith(`${profile}\\`)),
      `shortcuts outside the user's profile: ${placed.user.join(", ")}`,
    );
    // No elevation: the setup's manifest asks to run as the user who started it, and so does the
    // uninstaller it wrote (the runner's user is an administrator with UAC off, so running is not
    // proof of that by itself).
    for (const prog of [setup, join(folder, "uninstall.exe")]) {
      const level = existsSync(prog) ? executionLevel(prog) : { higher: true };
      say(`${basename(prog)}: requestedExecutionLevel ${level.level}`);
      check(
        level.level === "asInvoker" && !level.higher,
        `${prog} asks for ${JSON.stringify(level)}, not asInvoker alone`,
      );
    }
    // The setup's copy of the program carries the bundle type it patched in (the app reads it
    // below, at its first stage).
    check(
      existsSync(program) && readFileSync(program).includes(Buffer.from("__TAURI_BUNDLE_TYPE_VAR_NSS")),
      `${program} does not carry the setup's bundle type`,
    );
    const acl = run("icacls", [folder]);
    say(`the install folder's access list: ${acl.out.replace(/\s+/g, " ").slice(0, 600)}`);

    // 3. The installed app: its login item turned on, through its own command.
    const enabled = await stage(p, "enable", [], [program]);
    check(
      enabled.code === 0 && enabled.report,
      `enable: the app did not report and quit: ${enabled.code} ${enabled.dump}`,
    );
    const runValue = regValue(RUN_KEY, TEST_ID);
    check(
      runValue?.toLowerCase() === runCommand.toLowerCase(),
      `enable: the Run value is ${JSON.stringify(runValue)}, not ${runCommand}`,
    );

    // 4. The installed copy updates itself in place to 0.2.0 and rolls back, its uninstall entry's
    // version following each version that accepts itself. The updater refuses a copy whose program
    // or folder others could change (win_acl), so an update that installs shows the folder passed.
    home = privateHome(p);
    const nonce = writeUpdaterTrust(home, made.updater_pubkey);
    const work = privateFolder(join(root, "work"));
    const v1 = pinOf(folder);
    writeFileSync(join(work, "v1.sha256"), v1);
    const v2 = setDigest(made.update.bundle);
    say(`installed ${folder}: v1 ${v1}, v2 ${v2}`);
    core = await startCore();
    mkdirSync(local, { recursive: true });
    writeFileSync(
      join(local, "connection.json"),
      JSON.stringify({ mode: "local", url: core.base, key: core.key }),
      {
        mode: 0o600,
      },
    );
    writeFileSync(join(p.dir, "window.json"), JSON.stringify({ close_behavior: "minimize-to-tray" }), {
      mode: 0o600,
    });
    const served = startUpdateFeed({
      dir: work,
      payload: readFileSync(made.update.payload),
      file: basename(made.update.payload),
      target: made.update.target,
      version: made.update.version,
      signature: made.update.signature,
      pubkey: made.updater_pubkey,
    });
    feed = served;
    const stand = standIns(root, ["msiexec", "runas", "schtasks", "sc"]);
    const env = {
      ...withPathFirst({ ...p.env, HOME: home }, stand.bin),
      PLUXY_APP_TEST_UPDATE: work,
      PLUXY_APP_TEST_FEED: served.url,
      PLUXY_APP_TEST_FEED_CA: served.ca,
    };
    // The first launch sets the feed (without updater trust, so it is refused an update).
    await refusedLaunch({
      refusal: NO_ROOTS,
      program,
      env,
      check,
      say,
      feed: served,
      unchanged: () => pinOf(folder) === v1,
    });
    const display: Record<string, string | undefined> = {};
    const bundleType: Record<string, unknown> = {};
    await updateRoute({
      program,
      installed: folder,
      third: program,
      env: { ...env, PLUXY_TEST_TRUST_NONCE: nonce },
      dir: work,
      v1,
      v2,
      v2Version: made.update.version,
      managed: false,
      feed: served,
      check,
      say,
      others: ["uninstall.exe"],
      onStage: (stage, v) => {
        display[stage] = regValue(UNINSTALL_KEY, "DisplayVersion");
        bundleType[stage] = v.bundle_type;
      },
    });
    say(
      `DisplayVersion at each stage: ${JSON.stringify(display)}; bundle type: ${JSON.stringify(bundleType)}`,
    );
    const wantDisplay = { first: V1, refused: V1, updated: made.update.version, "rolled-back": V1 };
    check(
      JSON.stringify(display) === JSON.stringify(wantDisplay),
      `the uninstall entry's DisplayVersion at each stage was ${JSON.stringify(display)}, not ${JSON.stringify(wantDisplay)}`,
    );
    // The setup's program reads as installed by the setup; the update's program, as built, reads as
    // a folder bundle; the rollback (and the put-back of the refused update) bring the setup's back.
    const wantType = { first: "Nsis", refused: "Nsis", updated: null, "rolled-back": "Nsis" };
    check(
      JSON.stringify(bundleType) === JSON.stringify(wantType),
      `the app's bundle type at each stage was ${JSON.stringify(bundleType)}, not ${JSON.stringify(wantType)}`,
    );
    check(stand.ran() === "", `an installer or elevation ran: ${stand.ran()}`);
    check(
      regValue(UNINSTALL_KEY, "InstallLocation")?.toLowerCase() === `"${folder}"`.toLowerCase(),
      "the uninstall entry's InstallLocation changed",
    );

    // 5. The setup, run again while the app runs, refuses and leaves it running.
    running = new Launch(program, "running", { ...p.env, PLUXY_APP_TEST_INSTANCES: "1" });
    launches.push(running);
    const up = await running.next("up", UP_MS);
    check(up, `the installed app never came up: ${running.dump()}`);
    const pinned = pinOf(folder);
    const again = runSetup(setup);
    say(`setup /S while the app runs: exit ${again.status}: ${again.out}`);
    check(
      again.status === 2,
      `the setup run while the app runs exited with ${again.status}, not 2 (aborted)`,
    );
    check(
      /is running from/.test(again.out),
      `the setup did not say the app is running: ${JSON.stringify(again.out)}`,
    );
    check(alive(running.pid), "the setup ended the running app");
    check(
      (await running.command("state"))?.exists === true,
      "the running app no longer answers after the setup",
    );
    check(pinOf(folder) === pinned, "the refused setup changed the program set");
    check(
      regValue(UNINSTALL_KEY, "DisplayVersion") === V1,
      "the refused setup changed the uninstall entry's version",
    );

    // 6. The uninstaller, run while the app runs, refuses and leaves it running.
    const refusedUninstall = runUninstaller(folder);
    say(`uninstall /S while the app runs: exit ${refusedUninstall.status}: ${refusedUninstall.out}`);
    check(
      refusedUninstall.status === 2,
      `the uninstaller run while the app runs exited with ${refusedUninstall.status}, not 2 (aborted)`,
    );
    check(
      /is running from/.test(refusedUninstall.out),
      `the uninstaller did not say the app is running: ${JSON.stringify(refusedUninstall.out)}`,
    );
    check(alive(running.pid), "the uninstaller ended the running app");
    check(
      (await running.command("state"))?.exists === true,
      "the running app no longer answers after the uninstaller",
    );
    check(
      pinOf(folder) === pinned && existsSync(join(folder, "uninstall.exe")),
      "the refused uninstaller removed files of the install",
    );
    check(
      regValue(RUN_KEY, TEST_ID)?.toLowerCase() === runCommand.toLowerCase(),
      "the refused uninstaller removed the login item",
    );
    check(regKeyExists(UNINSTALL_KEY), "the refused uninstaller removed the uninstall entry");

    // 7. Quit, then uninstall: no folder, no login item and nothing Task Manager kept for it, no
    // uninstall entry or shortcut; the app's data stays. What the app itself leaves in the folder,
    // which the uninstaller (the first version's) does not know, goes too: an update's staging
    // folder and asides, and a file of a later version in bundled\.
    running.send("quit");
    const quit = await running.exitWithin(60_000);
    check(quit === 0, `quit exited with ${quit}: ${running.dump()}`);
    running = undefined;
    // The setup also refuses while another program of the app's in the folder runs: pluxy.exe, or
    // a program an interrupted update set aside. Each is a copy of Windows' ping, which runs until
    // it is ended, under that name.
    const ping = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "PING.EXE");
    const core0 = readFileSync(join(folder, "pluxy.exe"));
    for (const name of ["pluxy.exe", ".pluxy-old-0123456789abcdef-pluxy-app-test.exe"]) {
      const holder = join(folder, name);
      copyFileSync(ping, holder);
      const proc = spawn(holder, ["-n", "600", "127.0.0.1"], { stdio: "ignore" });
      try {
        await Bun.sleep(1000);
        check(proc.exitCode === null, `${name}: the stand-in did not keep running`);
        const r = runSetup(setup);
        say(`setup /S while ${name} runs: exit ${r.status}: ${r.out}`);
        check(r.status === 2, `the setup run while ${name} runs exited with ${r.status}, not 2 (aborted)`);
        check(
          /is running from/.test(r.out),
          `the setup did not say ${name} is running: ${JSON.stringify(r.out)}`,
        );
        check(proc.exitCode === null && alive(proc.pid as number), `the setup ended ${name}`);
      } finally {
        proc.kill();
        if (proc.exitCode === null) await new Promise((r) => proc.once("exit", r));
      }
      if (name === "pluxy.exe") writeFileSync(holder, core0);
      else rmSync(holder, { force: true });
    }
    check(pinOf(folder) === pinned, "the program set changed after the stand-ins");
    const leftovers = [
      join(folder, ".pluxy-update-0123456789abcdef", "bundled", "stub.txt"),
      join(folder, ".pluxy-old-0123456789abcdef-bundled", "stub.txt"),
      join(folder, ".pluxy-old-0123456789abcdef-pluxy.exe"),
      join(folder, "bundled", "packages-9.9.9.txt"),
    ];
    for (const f of leftovers) {
      mkdirSync(dirname(f), { recursive: true });
      writeFileSync(f, "left by the app\n");
    }
    // Task Manager's mark for the item (on: an even first byte).
    const mark = run("reg", [
      "add",
      APPROVED_KEY,
      "/v",
      TEST_ID,
      "/t",
      "REG_BINARY",
      "/d",
      "020000000000000000000000",
      "/f",
    ]);
    check(mark.status === 0, `writing Task Manager's mark: ${mark.out}`);
    await Bun.sleep(1000);
    const removed = runUninstaller(folder);
    say(`uninstall /S: exit ${removed.status}${removed.out ? `: ${removed.out}` : ""}`);
    check(removed.status === 0, `the uninstaller exited with ${removed.status}: ${removed.out}`);
    check(
      !existsSync(folder),
      `the install folder is still there, holding ${existsSync(folder) ? readdirSync(folder).join(", ") : ""}`,
    );
    check(regValue(RUN_KEY, TEST_ID) === undefined, "the uninstaller left the login item's Run value");
    check(
      regValue(APPROVED_KEY, TEST_ID) === undefined,
      "the uninstaller left Task Manager's StartupApproved value",
    );
    check(!regKeyExists(UNINSTALL_KEY), "the uninstaller left the uninstall entry");
    // The hook emptied bundled\ (with its later file) before Tauri's RMDir /REBOOTOK of it, so
    // nothing was left for Windows to delete at the next restart.
    check(!restartDeletes(folder), `the uninstall queued a delete at restart: ${restartDeletes(folder)}`);
    const after = shortcuts();
    check(
      after.user.length === 0 && after.machine.length === 0,
      `shortcuts after the uninstall: ${JSON.stringify(after)}`,
    );
    check(
      existsSync(p.dir) && existsSync(local),
      `the app's data was removed: ${p.dir} ${existsSync(p.dir)}, ${local} ${existsSync(local)}`,
    );

    // 8. Junctions to a folder outside, inside bundled\ and inside an update's leftover folder:
    // uninstalling removes the junctions, not what they point to.
    const reinstalled = runSetup(setup);
    check(reinstalled.status === 0, `the second setup exited with ${reinstalled.status}: ${reinstalled.out}`);
    outside = mkdtempSync(join(process.env.RUNNER_TEMP ?? tmpdir(), "pluxy-outside-"));
    const kept = join(outside, "keep.txt");
    writeFileSync(kept, "not the app's\n");
    mkdirSync(join(folder, ".pluxy-update-0123456789abcdef"), { recursive: true });
    for (const junction of [
      join(folder, "bundled", "link"),
      join(folder, ".pluxy-update-0123456789abcdef", "link"),
    ]) {
      const made8 = run("cmd.exe", ["/d", "/c", "mklink", "/J", junction, outside]);
      check(
        made8.status === 0 && existsSync(join(junction, "keep.txt")),
        `making the junction ${junction}: ${made8.out}`,
      );
    }
    const removed8 = runUninstaller(folder);
    say(
      `uninstall /S with junctions in bundled\\ and .pluxy-update-*: exit ${removed8.status}${removed8.out ? `: ${removed8.out}` : ""}; ${kept} ${existsSync(kept) ? "kept" : "removed"}`,
    );
    check(removed8.status === 0, `the uninstaller exited with ${removed8.status}: ${removed8.out}`);
    check(existsSync(kept), `the uninstaller followed a junction and removed ${kept}`);
    check(!restartDeletes(folder), `the uninstall queued a delete at restart: ${restartDeletes(folder)}`);
    check(
      !existsSync(folder),
      `the install folder is still there, holding ${existsSync(folder) ? readdirSync(folder).join(", ") : ""}`,
    );
  } finally {
    running?.kill();
    for (const l of launches) l.kill("SIGKILL");
    feed?.stop();
    if (core) {
      const proc = core.proc;
      proc.stdin?.end();
      if (proc.exitCode === null) await new Promise((r) => proc.once("exit", r));
    }
    await Bun.sleep(500);
    // What the run made outside the profile, if a check above stopped it early.
    if (existsSync(join(folder, "uninstall.exe"))) runUninstaller(folder);
    if (existsSync(folder)) removeWhenFree(folder, local);
    run("reg", ["delete", RUN_KEY, "/v", TEST_ID, "/f"]);
    run("reg", ["delete", APPROVED_KEY, "/v", TEST_ID, "/f"]);
    run("reg", ["delete", UNINSTALL_KEY, "/f"]);
    // The setup's record of where it installed, which its uninstaller keeps, and its manufacturer's
    // key once that is empty.
    run("reg", ["delete", SETUP_RECORD, "/f"]);
    const maker = SETUP_RECORD.slice(0, SETUP_RECORD.lastIndexOf("\\"));
    const left = run("reg", ["query", maker]);
    if (left.status === 0 && left.out.split(/\r?\n/).filter(Boolean).length <= 1)
      run("reg", ["delete", maker, "/f"]);
    for (const s of [...shortcuts().user]) rmSync(s, { force: true });
    if (outside) rmSync(outside, { recursive: true, force: true });
    removeWhenFree(root, local);
    if (home) removeWhenFree(home, local);
    p.remove();
  }
}

// ---- the run --------------------------------------------------------------------------------

async function main() {
  const sessions: [string, () => Promise<void>][] = [
    ["single instance", singleInstance],
    ["login item", loginItem],
    ["window and tray", windowAndTray],
    ["bundle", bundle],
    ["bundle release gate", bundleReleaseGate],
    ["app update", update],
    ["install", install],
  ];
  for (const [name, body] of sessions) {
    if (!runsSession(name)) continue;
    session = name;
    const before = problems.length;
    const t0 = Date.now();
    try {
      await body();
    } catch (e) {
      check(false, `the session failed: ${e instanceof Error ? e.stack : String(e)}`);
    }
    console.log(
      `platform: ${name}: ${problems.length === before ? "ok" : "FAILED"} (${Math.round((Date.now() - t0) / 1000)} s)`,
    );
  }
  const missing = [...selected].filter((n) => !started.has(n));
  if (missing.length > 0) problems.push(`sessions that never started: ${missing.join(", ")}`);
  const summary = `${process.platform}: ${started.size} sessions, ${checks} checks, ${problems.length} failed, ${skipped.length} skipped${skipped.length > 0 ? ` (${skipped.join("; ")})` : ""}`;
  console.log(`platform: ${summary}`);
  if (process.env.GITHUB_STEP_SUMMARY)
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `Native test, ${summary}\n`);
  if (checks === 0) problems.push("the run checked nothing");
  process.exit(problems.length > 0 ? 1 : 0);
}

await main();
