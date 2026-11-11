// Drive the real watcher's StatusNotifierItem/dbusmenu with every renderer closed.
import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";

interface Snapshot {
  app_pid: number;
  window: boolean;
  webviews: number;
  view: { mode: string; core?: { state: string; pid?: number; detail?: string } };
}

export interface RecoveryResult {
  /** The owned core left running at the end, which quitting the app must stop. */
  core_pid?: number;
}

const LABELS = ["Start core", "Stop core", "Restart core"] as const;
const IDS: Record<(typeof LABELS)[number], string> = {
  "Start core": "core-start",
  "Stop core": "core-stop",
  "Restart core": "core-restart",
};
// Start, Stop, Restart enabled, as in the old app: Restart only while running.
const RUNNING = [false, true, true];
const STOPPED = [true, false, false];
const RECOVERING = [true, true, false];
const PAUSED = [true, false, false];
const OFF = [false, false, false];

/** lines: every line the app has written so far, in order. hold: while this file exists, a
 * tray action waits before it starts (a native-test fixture). problems collects the checks that
 * fail without stopping the run; a check the rest depends on throws. */
export async function checkRecovery(
  probe: string,
  stateFile: string,
  lines: string[],
  hold: string,
  problems: string[],
): Promise<RecoveryResult> {
  const read = (): Snapshot => JSON.parse(readFileSync(stateFile, "utf8"));
  const assert = (ok: unknown, why: string) => {
    if (!ok) throw new Error(why);
  };
  const soft = (ok: unknown, why: string) => {
    if (!ok) problems.push(why);
  };
  const wait = async (what: string, predicate: (s: Snapshot) => boolean) => {
    const deadline = Date.now() + 40_000;
    for (;;) {
      const s = read();
      assert(!s.window && s.webviews === 0, "tray action recreated a window/webview");
      if (predicate(s)) return s;
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}: ${JSON.stringify(s)}`);
      await Bun.sleep(50);
    }
  };
  /** The app's `native-test: <step> {...}` lines from index from on. */
  const events = (step: string, from: number): Record<string, unknown>[] => {
    const prefix = `native-test: ${step} `;
    return lines
      .slice(from)
      .flatMap((l) => (l.startsWith(prefix) ? [JSON.parse(l.slice(prefix.length))] : []));
  };
  const until = async <T>(f: () => T | undefined, ms = 5_000): Promise<T | undefined> => {
    const deadline = Date.now() + ms;
    for (;;) {
      const v = f();
      if (v !== undefined || Date.now() > deadline) return v;
      await Bun.sleep(50);
    }
  };
  const first = await wait("closed app snapshot", () => true);
  const pid = first.app_pid;
  const stat = (p: number) => {
    try {
      const s = readFileSync(`/proc/${p}/stat`, "utf8");
      const end = s.lastIndexOf(")");
      const [state, ppid] = s.slice(end + 2).split(" ");
      return { name: s.slice(s.indexOf("(") + 1, end), state, ppid: Number(ppid) };
    } catch {
      return undefined; // exited
    }
  };
  const renderers = () => {
    const procs = new Map<number, { name: string; ppid: number }>();
    for (const p of readdirSync("/proc")) {
      if (!/^[0-9]+$/.test(p)) continue;
      const s = stat(Number(p));
      if (s) procs.set(Number(p), s);
    }
    let count = 0;
    for (const [p, { name }] of procs) {
      if (!name.startsWith("WebKitWebProces")) continue;
      for (let q = procs.get(p)?.ppid; q && q > 1; q = procs.get(q)?.ppid) {
        if (q === pid) {
          count++;
          break;
        }
      }
    }
    return count;
  };
  const tray = (action: string) => {
    assert(renderers() === 0, "renderer process exists during tray-only operation");
    const r = spawnSync(probe, ["tray", String(pid), action], { encoding: "utf8", timeout: 15_000 });
    assert(r.status === 0, `tray ${action}: ${r.stderr}`);
    return r.stdout;
  };
  const menu = () => {
    const m = new Map<string, boolean>();
    for (const line of tray("inspect").split("\n")) {
      const at = line.lastIndexOf("=");
      if (at > 0) m.set(line.slice(0, at), line.slice(at + 1) === "true");
    }
    return m;
  };
  /** The tray updates on the app's main thread a moment after the state file changes. */
  const flags = async (what: string, want: boolean[]) => {
    let got: (boolean | undefined)[] = [];
    await until(() => {
      const m = menu();
      got = LABELS.map((l) => m.get(l));
      return got.every((g, i) => g === want[i]) || undefined;
    }, 5_000);
    soft(
      got.every((g, i) => g === want[i]),
      `tray while ${what}: Start/Stop/Restart enabled ${JSON.stringify(got)}, want ${JSON.stringify(want)}`,
    );
  };
  /** Clicks a menu item and waits for the app's handler to act on or refuse it. */
  const click = async (label: (typeof LABELS)[number]) => {
    const from = lines.length;
    tray(label);
    const id = IDS[label];
    type Handled = Record<string, unknown> | undefined;
    let acted: Handled;
    let refused: Handled;
    await until(() => {
      acted = events("tray-action", from).find((e) => e.action === id);
      refused = events("tray-refused", from).find((e) => e.action === id);
      return acted ?? refused;
    });
    return { acted, refused };
  };
  /** mode null: the handler refused without reading the connection, as it does while busy. */
  const refused = async (label: (typeof LABELS)[number], mode: string | null, reason: string) => {
    const r = await click(label);
    soft(
      r.refused?.mode === mode && r.refused?.reason === reason && !r.acted,
      `a click on ${label} (${mode}) was not refused for "${reason}": ` +
        `${JSON.stringify(r.refused ?? r.acted ?? "the click never reached the app's handler")}`,
    );
    return r;
  };
  const alive = (p: number) => {
    const s = stat(p);
    return s !== undefined && s.state !== "Z";
  };
  const crash = (s: Snapshot) => {
    const core = s.view.core?.pid;
    assert(core, "no owned core to crash");
    // Test-only crash fixture: signal the child the app reports, after checking that it still is
    // the app's own child and alive, so a reused PID is never signalled.
    const now = stat(core as number);
    assert(
      now && now.ppid === pid && now.state !== "Z",
      `core pid ${core} is not a live child of the app: ${JSON.stringify(now)}`,
    );
    process.kill(core as number, "SIGKILL");
    return core;
  };
  const running = (s: Snapshot) => s.view.core?.state === "running" && !!s.view.core.pid;

  if (first.view.mode !== "managed") {
    const mode = first.view.mode;
    await flags(mode, OFF);
    for (const label of LABELS) await refused(label, mode, "disabled");
    await Bun.sleep(500);
    assert(!read().view.core, "foreign mode gained an owned core");
    console.log(`native-recovery: ${mode}: tray lifecycle refused by the app's handler, zero renderers`);
    return {};
  }

  const initial = first.view.core?.pid;
  await flags("running", RUNNING);
  // Positive control for the refusals: a click on a disabled item reaches the handler.
  await refused("Start core", "managed", "disabled");
  soft(read().view.core?.pid === initial, "a click on the disabled Start core changed the core");
  await click("Stop core");
  await wait("tray Stop", (s) => s.view.core?.state === "stopped");
  assert(initial && !alive(initial), "tray Stop left the owned core alive");
  await flags("stopped", STOPPED);
  await refused("Restart core", "managed", "disabled");
  soft(read().view.core?.state === "stopped", "a click on the disabled Restart core started a core");

  // While an action runs, the tray shows it is working and refuses another click.
  writeFileSync(hold, "");
  let from = lines.length;
  tray("Start core");
  const held = await until(() => events("tray-held", from)[0]);
  soft(held, "the tray Start never reached the busy hold");
  if (held) {
    await flags("busy", OFF);
    soft(menu().has("Core working…"), "the tray does not say the core is working");
    // The busy refusal comes before the handler reads the connection, which the held action
    // may be about to lock, so it carries no mode.
    await refused("Stop core", null, "busy");
  }
  rmSync(hold, { force: true });
  let s = await wait("tray Start", running);
  await flags("running", RUNNING);
  const started = s.view.core?.pid;
  await click("Restart core");
  s = await wait("tray Restart", (v) => running(v) && v.view.core?.pid !== started);
  assert(started && !alive(started), "Restart left the old owned core alive");

  from = lines.length;
  let old = crash(s);
  await wait("scheduled recovery", (v) => v.view.core?.state === "recovering");
  await flags("recovering", RECOVERING);
  await click("Stop core");
  await wait("manual Stop cancels scheduled recovery", (v) => v.view.core?.state === "stopped");
  await Bun.sleep(2500);
  assert(read().view.core?.state === "stopped", "scheduled recovery undid manual Stop");
  // Without this, a slow Stop that lands after the recovery would pass the check above.
  const early = events("recovery-attempt", from);
  assert(early.length === 0, `recovery ran between the crash and the manual Stop: ${JSON.stringify(early)}`);
  console.log("native-recovery: pending recovery cannot undo manual tray Stop; no attempt ran before it");

  await click("Start core");
  s = await wait("tray Start after cancellation", running);
  from = lines.length;
  old = crash(s);
  s = await wait("crash fixture recovery", (v) => running(v) && v.view.core?.pid !== old);
  // Positive control for the no-attempt check above: a recovery is logged.
  soft(events("recovery-attempt", from).length === 1, "a recovery ran without its native-test log");
  console.log("native-recovery: crashed real core recovered; tray Start/Stop/Restart with zero renderers");
  for (let retry = 1; retry < 3; retry++) {
    old = crash(s);
    s = await wait("crash-loop retry", (v) => running(v) && v.view.core?.pid !== old);
  }
  crash(s);
  s = await wait("crash-loop cutoff", (v) => v.view.core?.state === "paused");
  assert(s.view.core?.detail, "paused state has no reason");
  assert(tray("inspect").includes("Core recovery paused"), "tray hides paused state");
  await flags("paused", PAUSED);
  await Bun.sleep(16_000);
  assert(read().view.core?.state === "paused", "crash loop resumed after its retry cutoff");
  console.log(
    "native-recovery: crash loop paused after three retries; reason visible in tray and connection",
  );

  // An explicit Start clears the budget: the next crash is recovered again.
  await click("Start core");
  s = await wait("resume paused core", running);
  old = crash(s);
  s = await wait("recovery after manual Start", (v) => running(v) && v.view.core?.pid !== old);
  await flags("running", RUNNING);
  console.log("native-recovery: manual Start cleared the recovery budget; the next crash was recovered");
  assert(renderers() === 0, "renderer process appeared during recovery");
  return { core_pid: s.view.core?.pid };
}
