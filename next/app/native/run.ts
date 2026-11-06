// The app's native test: the real app (built with the native-test feature) on a private X server,
// against a real core with fake extensions (next/e2e/panelcore -probe), once in each mode:
// managed (the app runs the core), local (a core already running here) and remote (that core
// behind https with its own certificate authority). In each it checks that
//
// - the app connects, and the window's webview reaches every app command and gets core's status;
// - at the top of an extension page's webview every app command and Tauri plugin command is
//   refused by Tauri's command check, and the app never reaches a command from that webview;
// - from inside the page's sandboxed frame nothing reaches the app (test/probe reports through
//   its channel);
// - closing the window leaves no webview and no web process, with the app still in the tray;
// - quitting stops the app's own core, and leaves a local or remote core running.
//
// Each run gets a fresh HOME, XDG folders and runtime folder, a session bus and an X server of its
// own (dbus-run-session, xvfb-run), and random ports. Run from next/app: bun native/run.ts (needs
// dbus-run-session, xvfb-run, Go and cargo).

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const app = join(import.meta.dir, "..");
const next = join(app, "..");
const tauri = join(app, "src-tauri");
const identifier: string = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8")).identifier;
const commands = [
  ...readFileSync(join(tauri, "src", "command_names.rs"), "utf8").matchAll(/^\s+"(\w+)",$/gm),
].map((m) => m[1] as string);
const PAGE = "ext/test/probe/probe";
/** Tauri's command check refusing a call (debug builds say where; release builds "by ACL"). */
const REFUSED = /not allowed( on window|\. Permissions associated| by ACL)/;
const TIMEOUT = 240_000;

type Mode = "managed" | "local" | "remote";
type Step = Record<string, unknown>;
interface Call {
  ok: boolean;
  detail: string;
}
interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}
interface Probe {
  internals: boolean;
  calls: Record<string, Call>;
  label?: string;
  url?: string;
  area?: Box;
  placed?: Box;
}

function run(cmd: string, args: string[], cwd: string) {
  const r = spawnSync(cmd, args, { cwd, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
  return r.stdout;
}

/** A core from panelcore, running until its stdin closes. */
async function startCore(
  tls: boolean,
): Promise<{ proc: ChildProcess; base: string; key: string; ca?: string }> {
  const proc = spawn("go", ["run", "./e2e/panelcore", "-probe", ...(tls ? ["-tls"] : [])], {
    cwd: next,
    stdio: ["pipe", "pipe", "inherit"],
  });
  const line = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`panelcore exited with ${code}`)));
    if (proc.stdout) createInterface({ input: proc.stdout }).once("line", resolve);
  });
  return { proc, ...JSON.parse(line) };
}

/** The web processes among pid's descendants. */
function webProcesses(pid: number): number {
  const parent = new Map<number, number>();
  const name = new Map<number, string>();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const stat = readFileSync(`/proc/${d}/stat`, "utf8");
      const close = stat.lastIndexOf(")");
      name.set(Number(d), stat.slice(stat.indexOf("(") + 1, close));
      parent.set(Number(d), Number(stat.slice(close + 2).split(" ")[1]));
    } catch {
      // gone already
    }
  }
  let n = 0;
  for (const [p, comm] of name) {
    if (!comm.startsWith("WebKitWebProces")) continue;
    for (let q = parent.get(p); q !== undefined && q > 1; q = parent.get(q)) {
      if (q === pid) {
        n++;
        break;
      }
    }
  }
  return n;
}

function alive(pid: number): boolean {
  try {
    const state = readFileSync(`/proc/${pid}/stat`, "utf8").split(") ")[1]?.[0];
    return state !== "Z";
  } catch {
    return false;
  }
}

async function managementStatus(base: string, key: string, ca?: string): Promise<number> {
  const r = await fetch(`${base}/v0/management/status`, {
    headers: { Authorization: `Bearer ${key}` },
    ...(ca ? { tls: { ca: readFileSync(ca, "utf8") } } : {}),
  } as RequestInit);
  return r.status;
}

/** Runs the app once with connection as its saved connection, and checks what it reports. */
async function runApp(mode: Mode, bin: string, connection: object, base?: string): Promise<string[]> {
  const root = mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
  const problems: string[] = [];
  const check = (ok: unknown, what: string) => {
    if (!ok) problems.push(what);
  };
  try {
    const dirs = {
      home: join(root, "home"),
      config: join(root, "config"),
      data: join(root, "data"),
      cache: join(root, "cache"),
      state: join(root, "state"),
      runtime: join(root, "runtime"),
    };
    for (const d of Object.values(dirs)) mkdirSync(d, { recursive: true, mode: 0o700 });
    mkdirSync(join(dirs.config, identifier), { recursive: true });
    writeFileSync(join(dirs.config, identifier, "connection.json"), JSON.stringify(connection), {
      mode: 0o600,
    });
    const quit = join(root, "quit");
    // Only what the app needs: no display or session bus but the run's own.
    const env: Record<string, string> = {
      PATH: process.env.PATH ?? "/usr/bin:/bin",
      LANG: "C.UTF-8",
      HOME: dirs.home,
      XDG_CONFIG_HOME: dirs.config,
      XDG_DATA_HOME: dirs.data,
      XDG_CACHE_HOME: dirs.cache,
      XDG_STATE_HOME: dirs.state,
      XDG_RUNTIME_DIR: dirs.runtime,
      WEBKIT_DISABLE_DMABUF_RENDERER: "1",
      LIBGL_ALWAYS_SOFTWARE: "1",
      NO_AT_BRIDGE: "1",
      GTK_USE_PORTAL: "0",
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
    };
    const xvfb = ["xvfb-run", "-a", "-s", "-screen 0 1280x800x24 -nolisten tcp", bin];
    const proc = spawn("dbus-run-session", ["--", ...xvfb], {
      env,
      stdio: ["ignore", "inherit", "pipe"],
    });
    const steps = new Map<string, Step>();
    const reached: string[] = [];
    let before = 0;
    let after = -1;
    const pid = proc.pid ?? 0;
    if (!proc.stderr) throw new Error("the app's stderr is not piped");
    const lines = createInterface({ input: proc.stderr });
    lines.on("line", (line) => {
      const m = /^native-test: (\w+) (.*)$/.exec(line);
      if (!m) {
        if (process.env.PLUXY_APP_TEST_VERBOSE) console.error(`  ${mode} | ${line}`);
        return;
      }
      const [, step, json] = m as unknown as [string, string, string];
      const value = JSON.parse(json);
      if (step === "reached") {
        reached.push(value.webview);
        return;
      }
      steps.set(step, value);
      if (step === "frame") before = webProcesses(pid);
      if (step === "closed") {
        // Give the web processes a moment to exit, then let the app quit.
        setTimeout(() => {
          after = webProcesses(pid);
          writeFileSync(quit, "");
        }, 3000);
      }
    });
    const code = await new Promise<number | null>((resolve) => {
      const timer = setTimeout(() => {
        problems.push(`the app did not finish within ${TIMEOUT / 1000}s`);
        proc.kill("SIGKILL");
      }, TIMEOUT);
      proc.on("exit", (c) => {
        clearTimeout(timer);
        resolve(c);
      });
    });

    check(code === 0, `the app exited with ${code}`);
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);

    // The window: connected in this mode, every command reached, core's status through the app.
    const main = steps.get("main") as Probe | undefined;
    check(main?.internals, "the window's webview has no IPC");
    const got = main?.calls.connection_get;
    const view = got?.ok ? JSON.parse(got.detail) : undefined;
    check(view?.mode === mode, `the window's connection is ${JSON.stringify(view)}`);
    check(view?.signed_in === true, "the app is not signed in");
    check(base === undefined || view?.target === base, `the app's target is ${view?.target}, not ${base}`);
    for (const [cmd, c] of Object.entries(main?.calls ?? {})) {
      if (cmd !== "connection_get")
        check(!/not allowed/i.test(c.detail), `the window was refused ${cmd}: ${c.detail}`);
    }
    check((steps.get("status") as { status?: number })?.status === 200, "core's status did not come through");

    // The page webview: Tauri refuses every command, and the app reaches none from it.
    const page = steps.get("page") as Probe | undefined;
    check(page?.internals, "the page webview has no IPC, so the probe proves nothing");
    check(page?.url === "pluxy-page://localhost/", `the page webview shows ${page?.url}`);
    // The page webview covers the page area the window drew for it, not some other place.
    const area = page?.area;
    const placed = page?.placed;
    const near = (a?: number, b?: number) => a !== undefined && b !== undefined && Math.abs(a - b) <= 2;
    check(
      area && placed && (["x", "y", "width", "height"] as const).every((k) => near(area[k], placed[k])),
      `the page webview is at ${JSON.stringify(placed)}, the page area at ${JSON.stringify(area)}`,
    );
    for (const cmd of commands) check(page?.calls[cmd], `the page webview's probe did not try ${cmd}`);
    for (const [cmd, c] of Object.entries(page?.calls ?? {})) {
      check(!c.ok && REFUSED.test(c.detail), `${cmd} from the page webview: ${JSON.stringify(c)}`);
    }
    check(reached.length > 0, "no command was reached at all");
    check(
      reached.every((w) => w === "main"),
      `commands were reached from ${[...new Set(reached)].join(", ")}`,
    );

    // The page's frame, from its own report.
    const frame = steps.get("frame") as Record<string, { status: string; detail: string }> | undefined;
    check(frame && Object.keys(frame).length >= 10, "the page sent no report");
    for (const [what, r] of Object.entries(frame ?? {})) {
      const fine = r.status === "blocked" || (r.status === "attempted" && what === "native message handler");
      check(fine, `from the page's frame, "${what}" ${r.status}: ${r.detail}`);
    }

    // Closing the window frees its webviews; the app stays in the tray.
    const closed = steps.get("closed") as
      | { webviews: string[]; tray: boolean; core_pid?: number }
      | undefined;
    check(closed?.tray, "the app has no tray icon");
    check(closed?.webviews.length === 0, `webviews left after closing: ${closed?.webviews}`);
    check(before > 0, "no web process seen while the window was open");
    check(after === 0, `${after} web processes left after closing the window`);
    if (mode === "managed") {
      check(closed?.core_pid, "the app ran no core");
      await Bun.sleep(1000);
      check(closed?.core_pid && !alive(closed.core_pid), "the app's core still runs after it quit");
    }
    console.log(
      `${mode}: ${problems.length === 0 ? "ok" : "FAILED"} (web processes ${before} open, ${after} after close; ` +
        `${reached.length} commands reached, all from main: ${reached.every((w) => w === "main")})`,
    );
  } finally {
    await removeRun(root);
  }
  return problems;
}

/** Removes a run's folder. A desktop service the run's bus started (such as the document portal,
 * which mounts itself in the runtime folder) can outlive the app for a moment. */
async function removeRun(root: string) {
  for (let i = 0; ; i++) {
    try {
      rmSync(root, { recursive: true, force: true });
      return;
    } catch (e) {
      if (i === 20) throw e;
      if (i === 10) spawnSync("fusermount", ["-u", "-z", join(root, "runtime", "doc")]);
      await Bun.sleep(500);
    }
  }
}

run("bun", ["run", "build"], app);
run("cargo", ["build", "--locked", "--features", "native-test"], tauri);
const bin = join(tauri, "target", "debug", "pluxy-app");
const problems: string[] = [];

const prepared = mkdtempSync(join(tmpdir(), "pluxy-app-native-core-"));
try {
  const { core } = JSON.parse(run("go", ["run", "./e2e/panelcore", "-probe", "-prepare", prepared], next));
  const p = await runApp("managed", bin, { mode: "managed", core_path: core });
  problems.push(...p.map((x) => `managed: ${x}`));
} finally {
  rmSync(prepared, { recursive: true, force: true });
}

for (const mode of ["local", "remote"] as const) {
  const c = await startCore(mode === "remote");
  try {
    const conn = { mode, url: c.base, key: c.key, ...(c.ca ? { ca_file: c.ca } : {}) };
    const p = await runApp(mode, bin, conn, c.base);
    // The app only connected: its quitting leaves this core running.
    if ((await managementStatus(c.base, c.key, c.ca)) !== 200) p.push("the core stopped when the app quit");
    problems.push(...p.map((x) => `${mode}: ${x}`));
  } finally {
    c.proc.stdin?.end();
    await new Promise((r) => c.proc.once("exit", r));
  }
}

if (problems.length > 0) {
  console.error(`\n${problems.length} problems:\n- ${problems.join("\n- ")}`);
  process.exit(1);
}
console.log("native test passed in all three modes");
