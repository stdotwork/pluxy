// The single-instance scenario, run inside a native session (session.ts starts it in place of the
// app; instances.ts starts the session):
//
//   bun native/instances-session.ts <app> <core program> <app identifier>
//
// It launches the real app (built with the native-test feature, PLUXY_APP_TEST_INSTANCES=1 so the
// app answers commands on stdin: native_test.rs) several times and checks, from outside it:
//
// - a second launch with the same HOME and XDG folders exits 0 within a bound, starts no second
//   app, window or core, and the first's window is shown again: after it was hidden, and after it
//   was closed to the tray;
// - a launch with other HOME and XDG folders is an instance of its own, with its own core;
// - after the owner is killed (SIGKILL) its lock and socket are left behind, and the next launch
//   takes over: it starts normally, and a launch after that raises it;
// - when the owner quits, its socket is gone.
//
// Output: `instances: ...` lines, and `instances-problem: ...` for each failed check.

import { type ChildProcess, spawn } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { createInterface } from "node:readline";
import { makeProfile } from "./profile";

const [bin, core, identifier] = process.argv.slice(2);
if (!bin || !core || !identifier) {
  console.error("usage: bun native/instances-session.ts <app> <core program> <app identifier>");
  process.exit(2);
}

const SECOND_EXIT_MS = 10_000;
const UP_MS = 60_000;
const coreComm = basename(core).slice(0, 15);
const appComm = "pluxy-app";

const problems: string[] = [];
const say = (line: string) => console.log(`instances: ${line}`);
function check(ok: unknown, what: string) {
  if (!ok) {
    problems.push(what);
    console.log(`instances-problem: ${what}`);
  }
}

type Step = Record<string, unknown>;

/** One launch of the app: its `native-test:` steps by name, and its exit. */
class Launch {
  readonly child: ChildProcess;
  readonly exited: Promise<number | null>;
  private readonly steps = new Map<string, Step[]>();
  private waiting = new Map<string, (s: Step) => void>();
  readonly log: string[] = [];
  readonly pid: number;

  constructor(
    readonly label: string,
    env: Record<string, string | undefined>,
  ) {
    this.child = spawn(bin as string, [], { env, stdio: ["pipe", "pipe", "pipe"] });
    this.pid = this.child.pid ?? 0;
    this.exited = new Promise((resolve) => this.child.once("close", (code) => resolve(code)));
    this.child.stdin?.on("error", () => {});
    for (const stream of [this.child.stdout, this.child.stderr]) {
      if (stream) createInterface({ input: stream }).on("line", (l) => this.onLine(l));
    }
  }

  private onLine(line: string) {
    this.log.push(line);
    if (this.log.length > 40) this.log.shift();
    const m = /^native-test: (\w+) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string) as Step;
    const wake = this.waiting.get(m[1] as string);
    if (wake) {
      this.waiting.delete(m[1] as string);
      wake(value);
    } else {
      const queue = this.steps.get(m[1] as string) ?? [];
      queue.push(value);
      this.steps.set(m[1] as string, queue);
    }
  }

  /** Whether the app printed this step (without taking it). */
  printed(step: string): boolean {
    return (this.steps.get(step)?.length ?? 0) > 0;
  }

  /** The app's next step of this name, or undefined if it does not come in time. */
  next(step: string, ms: number): Promise<Step | undefined> {
    const queued = this.steps.get(step)?.shift();
    if (queued) return Promise.resolve(queued);
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.waiting.delete(step);
        resolve(undefined);
      }, ms);
      this.waiting.set(step, (s) => {
        clearTimeout(timer);
        resolve(s);
      });
    });
  }

  /** Sends a command to the app and returns the window's state it answers with. */
  async command(cmd: string): Promise<Step | undefined> {
    this.send(cmd);
    return this.next("state", 15_000);
  }

  send(cmd: string) {
    this.child.stdin?.write(`${cmd}\n`);
  }

  kill(signal: NodeJS.Signals = "SIGKILL") {
    try {
      this.child.kill(signal);
    } catch {
      // gone already
    }
  }

  async exitWithin(ms: number): Promise<number | null | "timeout"> {
    return Promise.race([this.exited, Bun.sleep(ms).then(() => "timeout" as const)]);
  }

  dump(): string {
    return this.log.join(" | ");
  }
}

/** Every process on the machine: pid to its parent and name. */
function processes(): Map<number, { ppid: number; comm: string }> {
  const out = new Map<number, { ppid: number; comm: string }>();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const stat = readFileSync(`/proc/${d}/stat`, "utf8");
      const close = stat.lastIndexOf(")");
      if (stat.slice(close + 2, close + 3) === "Z") continue;
      out.set(Number(d), {
        comm: stat.slice(stat.indexOf("(") + 1, close),
        ppid: Number(stat.slice(close + 2).split(" ")[1]),
      });
    } catch {
      // gone already
    }
  }
  return out;
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
  const named = (comm: string) => [...all].filter(([p, v]) => v.comm === comm && below(p));
  const cores = named(coreComm);
  return {
    cores: cores.map(([p]) => p),
    apps: named(appComm).map(([p]) => p),
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
function launch(label: string, env: Record<string, string | undefined>): Launch {
  const l = new Launch(label, { ...env, PLUXY_APP_TEST_INSTANCES: "1" });
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

/** A second launch with the same profile: exits 0 in time, and starts nothing of its own. */
async function second(label: string, env: Record<string, string | undefined>, owners: number[]) {
  const t0 = Date.now();
  const s = launch(label, env);
  const code = await s.exitWithin(SECOND_EXIT_MS);
  if (code === "timeout") {
    check(
      false,
      `${label}: the second launch did not exit within ${SECOND_EXIT_MS / 1000}s (it ran: ${s.printed("up")}): ${s.dump()}`,
    );
    s.kill();
    await s.exited;
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
  const dataDir = join(process.env.XDG_DATA_HOME as string, identifier as string);
  const socket = join(dataDir, "instance", "activate.sock");
  const lock = join(dataDir, "instance", "lock");

  const first = await owner("first", home);
  const owners = [first.app.pid];
  check(first.corePid, "first: no core");
  check(
    counts().coresOf(first.app.pid).length === 1,
    `first: ${counts().coresOf(first.app.pid).length} cores, expected 1`,
  );
  check(existsSync(lock), "first: no lock file in the app's data folder");
  check(existsSync(socket), "first: no activation socket in the app's data folder");

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

  // 4. Another profile (other HOME and XDG folders) is an instance of its own, with its own core.
  const rootB = mkdtempSync(join(tmpdir(), "pluxy-inst-"));
  roots.push(rootB);
  const b = makeProfile(rootB);
  mkdirSync(join(b.dirs.config, identifier as string), { recursive: true });
  writeFileSync(
    join(b.dirs.config, identifier as string, "connection.json"),
    JSON.stringify({ mode: "managed", core_path: core }),
    { mode: 0o600 },
  );
  const other = env({ ...b.env, XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR });
  const control = await owner("other profile", other);
  now = counts();
  check(now.apps.length === 2, `other profile: ${now.apps.length} apps run, expected 2`);
  check(now.cores.length === 2, `other profile: ${now.cores.length} cores run, expected 2`);
  check(control.corePid && control.corePid !== first.corePid, "other profile: it shares the first's core");
  // It is its own instance: a second launch of it raises it, not the first.
  await second("second of other profile", other, [first.app.pid, control.app.pid]);
  control.app.send("quit");
  await control.app.exited;
  check(
    !existsSync(join(b.dirs.data, identifier as string, "instance", "activate.sock")),
    "other profile: its socket stays after it quit",
  );

  // 5. The owner dies without any cleanup: its lock and socket stay on disk, and the next launch
  // takes over, starts normally, and is then the one a launch after it raises.
  first.app.kill("SIGKILL");
  await first.app.exited;
  check(first.corePid && (await gone(first.corePid, 15_000)), "kill: the first's core still runs");
  check(existsSync(socket), "kill: no stale socket was left to recover from");
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
  const code = await taken.app.exitWithin(15_000);
  check(code === 0, `quit: the owner exited with ${code}`);
  check(!existsSync(socket), "quit: the socket stays after the owner quit");
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
