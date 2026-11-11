// Runs inside session.ts's disposable D-Bus/Xvfb/HOME. Each launch is a fresh app process.
import { spawn, spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";

const [bin, identifier, probe] = process.argv.slice(2);
if (!bin || !identifier) throw new Error("startup test needs app and identifier");
const config = process.env.XDG_CONFIG_HOME as string;
const entry = join(config, "autostart", identifier + ".desktop");
const settings = join(config, identifier, "startup.json");
const mode = JSON.parse(readFileSync(join(config, identifier, "connection.json"), "utf8")).mode;
function check(ok: unknown, message: string): asserts ok {
  if (!ok) throw new Error(message);
}
async function launch(stage: string, argv: string[] = [bin as string]) {
  const proceed = join(config, "startup-continue-" + stage + "-" + Date.now());
  const child = spawn(argv[0] as string, argv.slice(1), {
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
        if (stage === "silent") {
          const tray = spawnSync(probe as string, ["tray-pid", String(report.app_pid)], { encoding: "utf8" });
          if (tray.status !== 0) lines.push("native-test: failed tray watcher: " + tray.stderr);
          else console.log("startup tray registered: " + tray.stdout.trim());
          // No renderer may exist, including an accidentally hidden page.
          for (const pid of readdirSync("/proc").filter((p) => /^\d+$/.test(p))) {
            try {
              const stat = readFileSync("/proc/" + pid + "/stat", "utf8");
              const tail = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
              if (Number(tail[1]) === report.app_pid && stat.includes("WebKitWebProces")) {
                lines.push("native-test: failed silent renderer exists");
              }
            } catch {
              /* process exited */
            }
          }
        }
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
    "startup stage failed: " + stage,
  );
  const line = lines.find((l) => l.startsWith("native-test: startup "));
  check(line, "no startup report: " + stage);
  return JSON.parse(line.slice("native-test: startup ".length));
}

await launch("enable");
check(existsSync(entry), "enabling did not write the XDG autostart entry");
const text = readFileSync(entry, "utf8");
const exec = text
  .split("\n")
  .find((l) => l.startsWith("Exec="))
  ?.slice(5);
check(exec && exec.endsWith(" --silent-start"), "autostart entry has no silent flag");
// Parse the generated Desktop Entry quoted argument, never a shell command.
check(exec.startsWith('"') && exec.endsWith('" --silent-start'), "unexpected Exec quoting");
const executable = exec.slice(1, -'" --silent-start'.length).replaceAll("%%", "%");
check(executable === bin, "Exec does not name this app");
const silent = await launch("silent", [executable, "--silent-start"]);
check(
  silent.windows === 0 && silent.webviews === 0 && silent.tray === true,
  "silent launch created a window or missed its tray",
);
check(
  (mode === "managed") === (silent.core_pid !== null),
  "start-core preference did not respect connection ownership",
);
const prefs = JSON.parse(readFileSync(settings, "utf8"));
writeFileSync(
  settings,
  JSON.stringify({ ...prefs, silent_start_enabled: true, start_core_on_launch: false }),
);
const stopped = await launch("silent");
check(
  stopped.windows === 0 && stopped.webviews === 0 && stopped.core_pid === null,
  "saved silent/core-off preferences were ignored",
);
writeFileSync(settings, JSON.stringify({ ...prefs, silent_start_enabled: false }));
await launch("disable");
check(!existsSync(entry), "disabling left the autostart entry behind");
console.log(
  "startup preferences passed: " +
    mode +
    "; Exec silent, zero windows/webviews, native tray, managed-only core, registration removed",
);
