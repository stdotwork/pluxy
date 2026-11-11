// Drive the real watcher's StatusNotifierItem/dbusmenu with every renderer closed.
import { spawnSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";

interface Snapshot {
  app_pid: number;
  window: boolean;
  webviews: number;
  view: { mode: string; core?: { state: string; pid?: number; detail?: string } };
}

export async function checkRecovery(probe: string, stateFile: string) {
  const read = (): Snapshot => JSON.parse(readFileSync(stateFile, "utf8"));
  const assert = (ok: unknown, why: string) => { if (!ok) throw new Error(why); };
  const wait = async (what: string, predicate: (s: Snapshot) => boolean) => {
    const deadline = Date.now() + 40_000;
    for (;;) {
      const s = read();
      assert(!s.window && s.webviews === 0, "tray action recreated a window/webview");
      if (predicate(s)) return s;
      if (Date.now() > deadline) throw new Error("timed out waiting for " + what + ": " + JSON.stringify(s));
      await Bun.sleep(50);
    }
  };
  const first = await wait("closed app snapshot", () => true);
  const pid = first.app_pid;
  const renderers = () => {
    const parents = new Map<number, number>();
    const names = new Map<number, string>();
    for (const p of readdirSync("/proc")) {
      if (!/^[0-9]+$/.test(p)) continue;
      try {
        const stat = readFileSync("/proc/" + p + "/stat", "utf8");
        const end = stat.lastIndexOf(")");
        parents.set(Number(p), Number(stat.slice(end + 2).split(" ")[1]));
        names.set(Number(p), stat.slice(stat.indexOf("(") + 1, end));
      } catch { /* process exited */ }
    }
    let count = 0;
    for (const [p, name] of names) {
      if (!name.startsWith("WebKitWebProces")) continue;
      for (let q = parents.get(p); q && q > 1; q = parents.get(q)) {
        if (q === pid) { count++; break; }
      }
    }
    return count;
  };
  const tray = (action: string) => {
    assert(renderers() === 0, "renderer process exists during tray-only operation");
    const r = spawnSync(probe, ["tray", String(pid), action], { encoding: "utf8", timeout: 15_000 });
    assert(r.status === 0, "tray " + action + ": " + r.stderr);
    return r.stdout;
  };
  const alive = (p: number) => {
    try { return readFileSync("/proc/" + p + "/stat", "utf8").split(") ")[1]?.[0] !== "Z"; }
    catch { return false; }
  };
  const crash = (s: Snapshot) => {
    const core = s.view.core?.pid;
    assert(core, "no owned core to crash");
    // Test-only crash fixture: signal the real child ID reported by the app's ManagedCore.
    process.kill(core as number, "SIGKILL");
    return core;
  };
  const running = (s: Snapshot) => s.view.core?.state === "running" && !!s.view.core.pid;
  if (first.view.mode !== "managed") {
    const menu = tray("inspect");
    for (const label of ["Start core", "Stop core", "Restart core"]) {
      assert(!menu.includes(label + "=true"), "foreign mode enables " + label);
      if (menu.includes(label + "=")) tray(label);
    }
    await Bun.sleep(500);
    assert(!read().view.core, "foreign mode gained an owned core");
    console.log("native-recovery: " + first.view.mode + ": tray lifecycle refused, zero renderers");
    return;
  }
  const initial = first.view.core?.pid;
  tray("Stop core");
  await wait("tray Stop", (s) => s.view.core?.state === "stopped");
  assert(initial && !alive(initial), "tray Stop left the owned core alive");
  tray("Start core");
  let s = await wait("tray Start", running);
  const started = s.view.core?.pid;
  tray("Restart core");
  s = await wait("tray Restart", (v) => running(v) && v.view.core?.pid !== started);
  assert(started && !alive(started), "Restart left the old owned core alive");
  let old = crash(s);
  await wait("scheduled recovery", (v) => v.view.core?.state === "recovering");
  tray("Stop core");
  await wait("manual Stop cancels scheduled recovery", (v) => v.view.core?.state === "stopped");
  await Bun.sleep(2500);
  assert(read().view.core?.state === "stopped", "scheduled recovery undid manual Stop");
  console.log("native-recovery: pending recovery cannot undo manual tray Stop");
  tray("Start core");
  s = await wait("tray Start after cancellation", running);
  old = crash(s);
  s = await wait("crash fixture recovery", (v) => running(v) && v.view.core?.pid !== old);
  console.log("native-recovery: crashed real core recovered; tray Start/Stop/Restart with zero renderers");
  for (let retry = 1; retry < 3; retry++) {
    old = crash(s);
    s = await wait("crash-loop retry", (v) => running(v) && v.view.core?.pid !== old);
  }
  crash(s);
  s = await wait("crash-loop cutoff", (v) => v.view.core?.state === "paused");
  assert(s.view.core?.detail, "paused state has no reason");
  assert(tray("inspect").includes("Core recovery paused"), "tray hides paused state");
  console.log("native-recovery: crash loop paused after three retries; reason visible in tray and connection");
  tray("Start core");
  s = await wait("resume paused core", running);
  crash(s);
  await wait("budget retained after manual Start", (v) => v.view.core?.state === "paused");
  console.log("native-recovery: manual Start retained the recovery budget");
  assert(renderers() === 0, "renderer process appeared during recovery");
}
