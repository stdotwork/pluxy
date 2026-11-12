// Runs inside session.ts's disposable D-Bus/Xvfb/HOME. gio consumes the Desktop Entry.
import { spawn, spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { webProcesses } from "./processes";

const [bin, identifier, probe] = process.argv.slice(2);
if (!bin || !identifier || !probe) throw new Error("startup test needs app, identifier and probe");
const config = process.env.XDG_CONFIG_HOME as string;
const entry = join(config, "autostart", `${identifier}.desktop`);
const settings = join(config, identifier, "startup.json");
const mode = JSON.parse(readFileSync(join(config, identifier, "connection.json"), "utf8")).mode;
const noTray = process.env.PLUXY_APP_TEST_NO_TRAY === "1";
function check(ok: unknown, message: string): asserts ok {
  if (!ok) throw new Error(message);
}
function prefs(silent: boolean, core = true) {
  writeFileSync(
    settings,
    JSON.stringify({ autostart_enabled: true, silent_start_enabled: silent, start_core_on_launch: core }),
  );
}
interface Report {
  app_pid: number;
  windows: number;
  webviews: number;
  tray: boolean;
  core_pid: number | null;
}
/** during runs once the launch has reported, before it may quit; lines grows meanwhile. */
async function launch(
  stage: string,
  desktop = false,
  during?: (report: Report, lines: string[]) => Promise<void>,
) {
  const proceed = join(config, `startup-continue-${stage}-${Date.now()}`);
  const child = spawn(desktop ? "gio" : (bin as string), desktop ? ["launch", entry] : [], {
    env: { ...process.env, PLUXY_APP_TEST_STARTUP: stage, PLUXY_APP_TEST_STARTUP_CONTINUE: proceed },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const lines: string[] = [];
  for (const stream of [child.stdout, child.stderr]) {
    createInterface({ input: stream }).on("line", (line) => {
      lines.push(line);
      console.log(line);
      if (line.startsWith("native-test: startup ")) {
        const report = JSON.parse(line.slice("native-test: startup ".length));
        const renderers = webProcesses(report.app_pid);
        if (report.webviews === 0 && renderers !== 0)
          lines.push("native-test: failed silent renderer exists");
        if (report.webviews > 0 && renderers === 0)
          lines.push("native-test: failed renderer positive control missing");
        if (report.windows === 0 && !noTray) {
          const tray = spawnSync(probe as string, ["tray-pid", String(report.app_pid)], { encoding: "utf8" });
          if (tray.status !== 0) lines.push(`native-test: failed tray watcher: ${tray.stderr}`);
          else console.log(`startup tray registered: ${tray.stdout.trim()}`);
        }
        console.log(
          `startup real consumer: ${desktop ? "gio launch" : "app menu"}; windows=${report.windows}, webviews=${report.webviews}, renderers=${renderers}, core=${report.core_pid}`,
        );
        void (async () => {
          try {
            await during?.(report, lines);
          } catch (e) {
            lines.push(`native-test: failed ${String(e)}`);
          }
          writeFileSync(proceed, "");
        })();
      }
    });
  }
  const code = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("startup launch timed out"));
    }, 60_000);
    child.once("error", reject);
    child.once("close", (c) => {
      clearTimeout(timer);
      resolve(c);
    });
  });
  const failed = lines.filter((l) => l.startsWith("native-test: failed"));
  check(code === 0 && failed.length === 0, `startup stage failed: ${stage} ${failed.join("; ")}`);
  const line = lines.find((l) => l.startsWith("native-test: startup "));
  check(line, `no startup report: ${stage}`);
  return JSON.parse(line.slice("native-test: startup ".length));
}
/** The parent pid of a live process, or undefined once it has exited. */
function liveParent(pid: number): number | undefined {
  try {
    const s = readFileSync(`/proc/${pid}/stat`, "utf8");
    const [state, ppid] = s.slice(s.lastIndexOf(")") + 2).split(" ");
    return state === "Z" ? undefined : Number(ppid);
  } catch {
    return undefined; // exited
  }
}
let offCore: number | undefined; // the core coreOff left running, which must not outlive the app
/** With Start Core on Launch off the app starts no core and recovers none. Positive controls on
 * the same launch: the tray's Start core starts one, and that core is recovered after a crash. */
async function coreOff(report: Report, lines: string[]) {
  const stateFile = process.env.PLUXY_APP_TEST_STATE as string;
  type Core = { state?: string; pid?: number; detail?: string };
  const core = (): Core | undefined => JSON.parse(readFileSync(stateFile, "utf8")).view?.core;
  const attempts = () => lines.filter((l) => l.startsWith("native-test: recovery-attempt ")).length;
  const wait = async (what: string, ok: (c?: Core) => boolean) => {
    const deadline = Date.now() + 20_000;
    for (;;) {
      const c = core();
      if (ok(c)) return c as Core;
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}: ${JSON.stringify(c)}`);
      await Bun.sleep(100);
    }
  };
  const running = (c?: Core) => c?.state === "running" && !!c.pid;
  // Past the first recovery delay, 2 seconds.
  await Bun.sleep(3000);
  const idle = core();
  check(
    idle?.state === "stopped" && !idle.pid && attempts() === 0,
    `Start Core on Launch off: a core started or was recovered: ${JSON.stringify(idle)}, ${attempts()} recovery attempts`,
  );
  const click = spawnSync(probe as string, ["tray", String(report.app_pid), "Start core"], {
    encoding: "utf8",
    timeout: 15_000,
  });
  check(click.status === 0, `tray Start core: ${click.stderr}`);
  const started = (await wait("tray Start core", running)).pid as number;
  // Test-only crash fixture, as in recovery.ts: only the app's own live child is signalled.
  check(liveParent(started) === report.app_pid, `core pid ${started} is not a live child of the app`);
  process.kill(started, "SIGKILL");
  offCore = (await wait("recovery of the core the tray started", (c) => running(c) && c?.pid !== started))
    .pid;
  check(attempts() === 1, `${attempts()} recovery attempts after one crash, want 1`);
  console.log(
    "native-recovery: Start Core on Launch off: no core started or recovered; tray Start core started one, and it was recovered after a crash",
  );
}

await launch("enable");
check(existsSync(entry), "enabling did not write the XDG autostart entry");
prefs(true);
if (noTray) {
  const visible = await launch("observe", true);
  check(
    visible.windows === 1 && visible.webviews === 1,
    "silent login without tray host must open exactly one window/webview",
  );
  console.log("startup no-host fallback passed: exactly one window/webview");
} else {
  // Silent Launch keeps the app's own core in the tray; local and remote modes open normally.
  const hidden = mode === "managed";
  const silent = await launch("observe", true);
  check(
    hidden
      ? silent.windows === 0 && silent.webviews === 0 && silent.tray === true
      : silent.windows === 1 && silent.webviews === 1,
    hidden
      ? "silent login created a window or missed its tray"
      : `silent login in ${mode} mode must show one window`,
  );
  check(
    (mode === "managed") === (silent.core_pid !== null),
    "start-core preference did not respect connection ownership",
  );
  const menu = await launch("observe");
  check(
    menu.windows === 1 && menu.webviews === 1,
    "app-menu launch must show one window even with Silent Launch on",
  );
  prefs(false);
  const visible = await launch("observe", true);
  check(visible.windows === 1 && visible.webviews === 1, "login with Silent Launch off must show one window");
  prefs(true, false);
  const stopped = await launch("observe", true, hidden ? coreOff : undefined);
  if (hidden) {
    check(offCore, "the core-off launch left no recovered core");
    await Bun.sleep(1000);
    check(liveParent(offCore) === undefined, `the recovered core ${offCore} still runs after the app quit`);
  }
  check(
    stopped.windows === (hidden ? 0 : 1) &&
      stopped.webviews === (hidden ? 0 : 1) &&
      stopped.core_pid === null,
    "core-off silent login ignored preferences",
  );
}
prefs(false);
await launch("disable");
check(!existsSync(entry), "disabling left the autostart entry behind");
console.log(
  `startup preferences passed: ${mode}; ${noTray ? "no host fallback" : "gio launch, login/menu choices, managed-only core and silence, real tray"}; registration removed`,
);
