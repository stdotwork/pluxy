// The single-instance scenario, run inside a native session (session.ts starts it in place of the
// app; instances.ts starts the session):
//
//   bun native/instances-session.ts <app> <core program> <app identifier> <session_probe>
//
// It launches the real app (built with the native-test feature, PLUXY_APP_TEST_INSTANCES=1 so the
// app answers commands on stdin: native_test.rs) several times and checks, from outside it:
//
// - a second launch with the same HOME and XDG folders exits 0 within a bound, starts no second
//   app, window or core, and the first's window is shown again: after it was hidden, and after it
//   was closed to the tray;
// - so is a login launch (`--autostart`, as the autostart entry runs it, with Silent Launch on):
//   the first's window is shown, after it was hidden and after it was closed to the tray;
// - a launch with other HOME and XDG folders is an instance of its own, with its own core; its
//   data folder is too long for a socket path there, and a second launch of it still raises it;
// - the socket is in the runtime folder, where the app's `instance/address` says;
// - after the owner is killed (SIGKILL) its lock and socket are left behind, and the next launch
//   takes over: it starts normally, and a launch after that raises it;
// - when the owner quits, its socket and address are gone.
//
// The app runs a copy of the core program adopted into each profile (session_probe adopt-core),
// as the window's Adopt would: the copy runs from an open file, so a core is told by the program
// it runs, not by its name.
//
// Output: `instances: ...` lines, and `instances-problem: ...` for each failed check.

import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { Launch, type Step } from "./launch";
import { makeProfile } from "./profile";

const [bin, core, identifier, probe] = process.argv.slice(2);
if (!bin || !core || !identifier || !probe) {
  console.error(
    "usage: bun native/instances-session.ts <app> <core program> <app identifier> <session_probe>",
  );
  process.exit(2);
}

const SECOND_EXIT_MS = 10_000;
const END_MS = 15_000;
const UP_MS = 60_000;
const coreName = basename(core);
const appComm = "pluxy-app";

const problems: string[] = [];
const say = (line: string) => console.log(`instances: ${line}`);
function check(ok: unknown, what: string) {
  if (!ok) {
    problems.push(what);
    // One line: the runner reads a problem as one line, and a stuck app's report has several.
    console.log(`instances-problem: ${what.replace(/\s*\n\s*/g, " | ")}`);
  }
}

/** Every process on the machine: pid to its parent, name and program (when readable). */
function processes(): Map<number, { ppid: number; comm: string; exe: string }> {
  const out = new Map<number, { ppid: number; comm: string; exe: string }>();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const stat = readFileSync(`/proc/${d}/stat`, "utf8");
      const close = stat.lastIndexOf(")");
      if (stat.slice(close + 2, close + 3) === "Z") continue;
      let exe = "";
      try {
        exe = readlinkSync(`/proc/${d}/exe`);
      } catch {
        // another user's, or gone
      }
      out.set(Number(d), {
        comm: stat.slice(stat.indexOf("(") + 1, close),
        ppid: Number(stat.slice(close + 2).split(" ")[1]),
        exe,
      });
    } catch {
      // gone already
    }
  }
  return out;
}

/** Adopts the core program into the app's cores in the data folder data, as the window's Adopt
 * would: the app runs only a core in its own store. */
function adopt(data: string) {
  const r = spawnSync(
    probe as string,
    ["adopt-core", join(data, identifier as string, "cores"), core as string],
    {
      stdio: ["ignore", "ignore", "inherit"],
    },
  );
  if (r.status !== 0) throw new Error(`session_probe adopt-core exited with ${r.status}`);
}

/** The running core programs and apps below this script. */
function counts(): { cores: number[]; apps: number[]; coresOf: (app: number) => number[] } {
  const all = processes();
  const below = (pid: number): boolean => {
    for (let q = all.get(pid)?.ppid; q !== undefined && q > 1; q = all.get(q)?.ppid) {
      if (q === process.pid) return true;
    }
    return false;
  };
  const cores = [...all].filter(([p, v]) => basename(v.exe) === coreName && below(p));
  return {
    cores: cores.map(([p]) => p),
    apps: [...all].filter(([p, v]) => v.comm === appComm && below(p)).map(([p]) => p),
    coresOf: (app) => cores.filter(([, v]) => v.ppid === app).map(([p]) => p),
  };
}

async function gone(pid: number, ms: number): Promise<boolean> {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (!processes().has(pid)) return true;
    await Bun.sleep(100);
  }
  return !processes().has(pid);
}

const launches: Launch[] = [];
const roots: string[] = [];
function launch(label: string, env: Record<string, string | undefined>, args: string[] = []): Launch {
  const l = new Launch(bin as string, label, { ...env, PLUXY_APP_TEST_INSTANCES: "1" }, args);
  launches.push(l);
  return l;
}

/** The first launch of a profile: waits until its window and core are up. */
async function owner(label: string, env: Record<string, string | undefined>) {
  const app = launch(label, env);
  const up = await app.next("up", UP_MS);
  check(up, `${label}: the app never came up: ${app.dump()}`);
  if (up) {
    const window = up.window as Step;
    check(window.exists && window.visible, `${label}: no window shown at start: ${JSON.stringify(window)}`);
    check(up.tray, `${label}: the app has no tray icon`);
    check(typeof up.core_pid === "number", `${label}: the app ran no core`);
  }
  return { app, corePid: up?.core_pid as number | undefined };
}

/** Waits (bounded) for a launch to end, after it was asked to quit or was killed: its process has
 * exited and its output has closed, which its webview's helper processes can hold open after the
 * app itself is gone. A launch that does not end in time is a problem that says which it was, and
 * is killed. */
async function ended(l: Launch, what: string): Promise<number | null> {
  const code = await l.exitWithin(END_MS);
  if (code !== "timeout") return code;
  check(
    false,
    `${what}: the app did not end within ${END_MS / 1000}s: ${await l.stuck()}\nits output: ${l.dump()}`,
  );
  l.kill("SIGKILL");
  return null;
}

/** A second launch with the same profile: exits 0 in time, and starts nothing of its own. */
async function second(
  label: string,
  env: Record<string, string | undefined>,
  owners: number[],
  args: string[] = [],
) {
  const t0 = Date.now();
  const s = launch(label, env, args);
  const code = await s.exitWithin(SECOND_EXIT_MS);
  if (code === "timeout") {
    check(
      false,
      `${label}: the second launch did not exit within ${SECOND_EXIT_MS / 1000}s (it ran: ${s.printed("up")}): ${s.dump()}`,
    );
    s.kill();
    await ended(s, `${label} (after it was killed)`);
  } else {
    check(code === 0, `${label}: the second launch exited with ${code}: ${s.dump()}`);
  }
  check(!s.printed("up"), `${label}: the second launch came up as an app of its own`);
  const now = counts();
  check(
    JSON.stringify(now.apps.sort()) === JSON.stringify([...owners].sort()),
    `${label}: apps running ${now.apps}, expected only ${owners}`,
  );
  say(`${label}: second launch exited ${code} after ${Date.now() - t0} ms`);
  return now;
}

const env = (extra: Record<string, string | undefined> = {}) => ({ ...process.env, ...extra });

async function main() {
  // Profile A is this session's own (the runner wrote its connection.json: managed mode).
  const home = env();
  adopt(process.env.XDG_DATA_HOME as string);
  const dataDir = join(process.env.XDG_DATA_HOME as string, identifier as string);
  const address = join(dataDir, "instance", "address");
  const lock = join(dataDir, "instance", "lock");
  const runtime = join(process.env.XDG_RUNTIME_DIR as string, "pluxy-app");

  const first = await owner("first", home);
  const owners = [first.app.pid];
  check(first.corePid, "first: no core");
  check(
    counts().coresOf(first.app.pid).length === 1,
    `first: ${counts().coresOf(first.app.pid).length} cores, expected 1`,
  );
  check(existsSync(lock), "first: no lock file in the app's data folder");
  check(existsSync(address), "first: no address in the app's data folder");
  const socket = existsSync(address) ? readFileSync(address, "utf8") : "";
  check(
    dirname(socket) === runtime && existsSync(socket),
    `first: the address names ${JSON.stringify(socket)}, not a socket in ${runtime}`,
  );

  // 1. The window is open: a second launch leaves one app, one core, one window.
  let now = await second("second", home, owners);
  check(now.cores.length === 1, `second: ${now.cores.length} cores run, expected 1`);
  check(first.corePid && now.cores[0] === first.corePid, "second: the core is not the first's");
  let state = await first.app.command("state");
  check(state?.exists && state.visible, `second: the first's window is ${JSON.stringify(state)}`);

  // 2. The window is hidden: the second launch shows it again.
  state = await first.app.command("hide");
  check(state?.exists && state.visible === false, `hide: the window is ${JSON.stringify(state)}`);
  await second("second after hide", home, owners);
  state = await first.app.command("state");
  check(state?.exists && state.visible, `after hide: the first's window is ${JSON.stringify(state)}`);
  say(`window after hide: ${JSON.stringify(state)}`);

  // 3. The window is closed (the app stays in the tray): the second launch opens a new one.
  state = await first.app.command("close");
  check(state?.exists === false, `close: the window is ${JSON.stringify(state)}`);
  await second("second after close", home, owners);
  state = await first.app.command("state");
  check(state?.exists && state.visible, `after close: the first's window is ${JSON.stringify(state)}`);
  say(`window after close: ${JSON.stringify(state)}`);
  check(counts().cores.length === 1, `after close: ${counts().cores.length} cores run, expected 1`);

  // 3b. A login launch is a second launch too: with the autostart entry's argument and Silent
  // Launch on (which hides the window only of a login launch that is the first), the first's
  // window is shown, after it was hidden and after it was closed to the tray. The plain launches
  // above are the same route's positive control.
  const config = join(process.env.XDG_CONFIG_HOME as string, identifier as string);
  writeFileSync(
    join(config, "startup.json"),
    JSON.stringify({ autostart_enabled: true, silent_start_enabled: true, start_core_on_launch: true }),
    { mode: 0o600 },
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
    now = await second(`autostart launch, window ${how}`, home, owners, ["--autostart"]);
    check(now.cores.length === 1, `autostart, ${how}: ${now.cores.length} cores run, expected 1`);
    state = await first.app.command("state");
    check(
      state?.exists && state.visible,
      `autostart, ${how}: the first's window is ${JSON.stringify(state)}`,
    );
    say(`window after autostart launch, ${how}: ${JSON.stringify(state)}`);
  }

  // 4. Another profile (other HOME and XDG folders) is an instance of its own, with its own core.
  const rootB = mkdtempSync(join(tmpdir(), "pluxy-inst-"));
  roots.push(rootB);
  const b = makeProfile(rootB);
  // A data folder too long for a socket path in it: the socket goes in the runtime folder.
  b.env.XDG_DATA_HOME = join(rootB, "d".repeat(100));
  b.dirs.data = b.env.XDG_DATA_HOME;
  mkdirSync(b.dirs.data, { mode: 0o700 });
  mkdirSync(join(b.dirs.config, identifier as string), { recursive: true });
  writeFileSync(
    join(b.dirs.config, identifier as string, "connection.json"),
    JSON.stringify({ mode: "managed", core_path: core }),
    { mode: 0o600 },
  );
  adopt(b.dirs.data);
  const other = env({ ...b.env, XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR });
  const control = await owner("other profile", other);
  now = counts();
  check(now.apps.length === 2, `other profile: ${now.apps.length} apps run, expected 2`);
  check(now.cores.length === 2, `other profile: ${now.cores.length} cores run, expected 2`);
  check(control.corePid && control.corePid !== first.corePid, "other profile: it shares the first's core");
  // It is its own instance: a second launch of it raises it, not the first.
  await second("second of other profile", other, [first.app.pid, control.app.pid]);
  control.app.send("quit");
  await ended(control.app, "other profile: quit");
  const addressB = join(b.dirs.data, identifier as string, "instance", "address");
  check(!existsSync(addressB), "other profile: its address stays after it quit");
  check(
    readdirSync(runtime).length === 1,
    `other profile: sockets left in the runtime folder after it quit: ${readdirSync(runtime)}`,
  );

  // 5. The owner dies without any cleanup: its lock and socket stay on disk, and the next launch
  // takes over, starts normally, and is then the one a launch after it raises.
  first.app.kill("SIGKILL");
  await ended(first.app, "kill: the first");
  check(first.corePid && (await gone(first.corePid, 15_000)), "kill: the first's core still runs");
  check(existsSync(socket), "kill: no stale socket was left to recover from");
  check(existsSync(address), "kill: no stale address was left to recover from");
  check(existsSync(lock), "kill: no stale lock file was left to recover from");
  const taken = await owner("after kill", home);
  check(taken.corePid && taken.corePid !== first.corePid, "after kill: the new owner has no core of its own");
  check(
    counts().coresOf(taken.app.pid).length === 1 && counts().cores.length === 1,
    `after kill: ${counts().cores.length} cores run, expected 1`,
  );
  await second("second after takeover", home, [taken.app.pid]);
  state = await taken.app.command("state");
  check(state?.exists && state.visible, `after takeover: the new owner's window is ${JSON.stringify(state)}`);

  // 6. The owner quits: its socket goes with it.
  taken.app.send("quit");
  const code = await ended(taken.app, "quit: the owner");
  check(code === 0, `quit: the owner exited with ${code}`);
  check(!existsSync(socket), "quit: the socket stays after the owner quit");
  check(!existsSync(address), "quit: the address stays after the owner quit");
  check(taken.corePid && (await gone(taken.corePid, 10_000)), "quit: the owner's core still runs");
  say("done");
}

try {
  await main();
} catch (e) {
  check(false, `the scenario failed: ${e instanceof Error ? e.stack : String(e)}`);
} finally {
  for (const l of launches) l.kill("SIGKILL");
  for (const r of roots) rmSync(r, { recursive: true, force: true });
}
process.exit(problems.length > 0 ? 1 : 0);
