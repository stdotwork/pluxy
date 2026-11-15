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
//
// These write what a real run writes: the user's registry or LaunchAgents folder, and on Windows
// the app's own folder in the user's profile. So it runs only on a GitHub-hosted runner, whose user
// is thrown away, and removes what it made.
//
// Output: `platform: ...` lines, `platform-problem: ...` for each failed check, and a summary with
// the counts. A run that checked nothing fails.

import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  appendFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { Launch, type Step } from "./launch";
import { SESSIONS, selectShard } from "./shards";

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
function makeProfile(): Profile {
  if (mac) {
    const root = mkdtempSync(join(tmpdir(), "pluxy-plat-"));
    const home = join(root, "home");
    const dir = join(home, "Library", "Application Support", identifier);
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
  const dir = join(appdata, identifier);
  const webview = join(local, identifier);
  if (existsSync(dir))
    throw new Error(`${dir} exists: this is not a fresh runner, and the test would write over it`);
  mkdirSync(dir, { recursive: true });
  return {
    env: { ...process.env },
    dir,
    agents: "",
    remove: () => {
      rmSync(dir, { recursive: true, force: true });
      rmSync(webview, { recursive: true, force: true });
    },
  };
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

// ---- the run --------------------------------------------------------------------------------

async function main() {
  const sessions: [string, () => Promise<void>][] = [
    ["single instance", singleInstance],
    ["login item", loginItem],
    ["window and tray", windowAndTray],
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
