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
async function launch(stage: string, desktop = false) {
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
        writeFileSync(proceed, "");
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
  check(
    code === 0 && !lines.some((l) => l.startsWith("native-test: failed")),
    `startup stage failed: ${stage}`,
  );
  const line = lines.find((l) => l.startsWith("native-test: startup "));
  check(line, `no startup report: ${stage}`);
  return JSON.parse(line.slice("native-test: startup ".length));
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
  const stopped = await launch("observe", true);
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
