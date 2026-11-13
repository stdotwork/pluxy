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
// In managed mode it then runs the window checks (window.ts, native_window.rs) in two more
// sessions, one with a tray watcher and one without.
//
// Each run gets a fresh HOME, XDG folders and runtime folder, a session bus and an X server of its
// own (dbus-run-session, xvfb-run), and random ports. Inside that session, before the app,
// native/session.ts starts the desktop services later checks need (the portals and a tray
// watcher), checks each of them for real, and runs a disposable AppImage with
// APPIMAGE_EXTRACT_AND_RUN=1; a missing or broken service fails the run at setup (services.ts
// names the packages). Run from next/app: bun native/run.ts (needs dbus-run-session, xvfb-run,
// Go, cargo and those services).

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildSmokeAppImage } from "./appimage";
import { type Feed, standInBrowser, startFeed } from "./feed";
import { descendants, webProcesses } from "./processes";
import { missingPrerequisites } from "./services";

const app = join(import.meta.dir, "..");
const next = join(app, "..");
const tauri = join(app, "src-tauri");
const tauriConf = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8"));
const identifier: string = tauriConf.identifier;
const appVersion: string = tauriConf.version;
const commands = [
  ...readFileSync(join(tauri, "src", "command_names.rs"), "utf8").matchAll(/^\s+"(\w+)",$/gm),
].map((m) => m[1] as string);
const PAGE = "ext/test/probe/probe";
/** Tauri's command check refusing a call (debug builds say where; release builds "by ACL"). */
const REFUSED = /not allowed( on window|\. Permissions associated| by ACL)/;
const TIMEOUT = 240_000;

type Mode = "managed" | "local" | "remote";
/** The session's setup failed (session.ts said what is missing or broken, and never said it was
 * ready): the app never ran, and the other modes would fail the same way. */
class SetupError extends Error {}
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

/** Sends signal to the process group pid leads (each session runs in a group of its own). */
function signalGroup(pid: number, signal: NodeJS.Signals) {
  try {
    process.kill(-pid, signal);
  } catch {
    // gone already
  }
}

/** The sessions still running, so an interrupted run can stop them. */
const sessions = new Set<number>();
for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => {
    // Bounded: TERM, a grace period, then KILL for each session, before the runner exits.
    void Promise.all([...sessions].map(stopSession)).finally(() => process.exit(130));
  });
}

/** Stops a session that did not finish: TERM first, so session.ts stops the services it started,
 * then KILL for its whole group and every process still below it. */
async function stopSession(pid: number) {
  signalGroup(pid, "SIGTERM");
  await Bun.sleep(10_000);
  signalGroup(pid, "SIGKILL");
  for (let i = 0; i < 3 && descendants(pid).size > 0; i++) {
    killTree(pid);
    await Bun.sleep(500);
  }
}

/** Kills pid and everything it started: its session's bus, X server and services too. */
function killTree(pid: number) {
  for (const p of [...descendants(pid).keys(), pid]) {
    try {
      process.kill(p, "SIGKILL");
    } catch {
      // gone already
    }
  }
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

/** The window checks (window.ts), with or without a tray watcher in the session. */
type Suite = "window" | "window-no-tray";

/** Runs the app once with connection as its saved connection, and checks what it reports. With
 * suite, the session runs window.ts, which runs the app through the window checks' stages. */
async function runApp(
  mode: Mode,
  bin: string,
  connection: object,
  base?: string,
  startup = false,
  noTray = false,
  key?: string,
  suite?: Suite,
): Promise<string[]> {
  const root = mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
  const problems: string[] = [];
  let feed: Feed | undefined;
  let offlineWatch: ReturnType<typeof setInterval> | undefined;
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
    // The main checks close the window to the tray; the window suite starts with Ask every time.
    if (!suite)
      writeFileSync(
        join(dirs.config, identifier, "window.json"),
        JSON.stringify({ close_behavior: "minimize-to-tray" }),
        { mode: 0o600 },
      );
    const quit = join(root, "quit");
    // The fake release feed, and the stand-in browser that notes links open in.
    feed = startFeed(join(root, "feed"));
    const browserLog = standInBrowser(root, dirs);
    const offline = join(root, "feed-offline");
    offlineWatch = setInterval(() => {
      if (feed && existsSync(offline) && !existsSync(`${offline}-done`)) {
        feed.stop();
        writeFileSync(`${offline}-done`, "");
      }
    }, 100);
    const keyFile = join(root, "key-input");
    const wrongKey = randomUUID();
    if (key) writeFileSync(keyFile, JSON.stringify({ key, wrong: wrongKey }), { mode: 0o600 });
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
      // The FUSE-free way to run an AppImage, which the session's smoke AppImage uses.
      APPIMAGE_EXTRACT_AND_RUN: "1",
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
      PLUXY_APP_TEST_FEED: feed.url,
      PLUXY_APP_TEST_FEED_OTHER: feed.other,
      PLUXY_APP_TEST_FEED_CA: feed.ca,
      PLUXY_APP_TEST_FEED_OFFLINE: offline,
      PLUXY_APP_TEST_BROWSER_LOG: browserLog,
      ...(startup ? { PLUXY_APP_TEST_STARTUP_SUITE: identifier } : {}),
      ...(noTray ? { PLUXY_APP_TEST_NO_TRAY: "1" } : {}),
      ...(key ? { PLUXY_APP_TEST_KEY_FILE: keyFile } : {}),
      ...(suite
        ? {
            PLUXY_APP_TEST_WINDOW_BIN: bin,
            PLUXY_APP_TEST_PROBE: probe,
            PLUXY_APP_TEST_IDENTIFIER: identifier,
          }
        : {}),
    };
    // The session script starts and checks the desktop services, then runs the app.
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      probe,
      appImage,
      randomUUID(),
      suite ? join(import.meta.dir, "window.ts") : bin,
    ];
    const xvfb = ["xvfb-run", "-a", "-s", "-screen 0 1280x800x24 -nolisten tcp", ...session];
    const proc = spawn("dbus-run-session", ["--", ...xvfb], {
      env,
      // Some xvfb-run versions send the program's stderr to stdout: read both.
      stdio: ["ignore", "pipe", "pipe"],
      // A process group of its own, so a run that hangs can be stopped as a whole.
      detached: true,
    });
    const steps = new Map<string, Step>();
    const reached: string[] = [];
    let before = 0;
    let after = -1;
    const pid = proc.pid ?? 0;
    sessions.add(pid);
    let ready = false; // session.ts said its setup is done and the app starts
    if (!proc.stderr || !proc.stdout) throw new Error("the app's output is not piped");
    const log: string[] = []; // the app's last lines, shown when the run fails
    const setup: string[] = []; // session.ts's report of what failed at setup
    let suitePassed = false;
    const onLine = (line: string) => {
      if (key && [key, wrongKey].some((canary) => line.includes(canary))) {
        problems.push("management key appeared on stdout/stderr");
        return;
      }
      // window.ts's checks, each shown.
      if (line.startsWith("window: ")) {
        console.log(`  ${mode} | ${line}`);
        if (line.startsWith("window: suite passed")) suitePassed = true;
        return;
      }
      // The session's service checks are always shown: they say what this run stood on.
      if (line.startsWith("native-prereq: ")) {
        console.log(`  ${mode} | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      const m = /^native-test: (\w+) (.*)$/.exec(line);
      if (!m) {
        if (process.env.PLUXY_APP_TEST_VERBOSE) console.error(`  ${mode} | ${line}`);
        log.push(line);
        if (log.length > 60) log.shift();
        return;
      }
      const [, step, json] = m as unknown as [string, string, string];
      const value = JSON.parse(json);
      if (step === "reached") {
        reached.push(value.webview);
        return;
      }
      steps.set(step, value);
      if (step === "confirmations") console.log(`  ${mode} | native confirmations: ${JSON.stringify(value)}`);
      if (step === "frame") before = webProcesses(pid);
      if (step === "closed") {
        // Give the web processes a moment to exit, then let the app quit.
        setTimeout(() => {
          after = webProcesses(pid);
          writeFileSync(quit, "");
        }, 3000);
      }
    };
    for (const stream of [proc.stdout, proc.stderr]) createInterface({ input: stream }).on("line", onLine);
    let stopping: Promise<void> | undefined;
    const code = await new Promise<number | null>((resolve) => {
      const timer = setTimeout(() => {
        problems.push(`the app did not finish within ${TIMEOUT / 1000}s`);
        stopping = stopSession(pid);
      }, TIMEOUT);
      // "close", not "exit": it comes after the output is read, so the "ready" line is not missed.
      proc.on("close", (c) => {
        clearTimeout(timer);
        resolve(c);
      });
    });
    await stopping;
    sessions.delete(pid);

    // Only session.ts's own "ready" line says the app ran: an exit code alone could be the app's.
    if (!ready) {
      throw new SetupError(
        setup.length > 0 ? setup.join("\n") : `the session ended (${code}) before it was ready`,
      );
    }
    check(code === 0, `the app exited with ${code}`);
    if (suite) {
      check(suitePassed, `the ${suite} checks failed`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode}: ${suite} ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);

    if (startup) {
      check(
        log.some((l) => l.startsWith("startup preferences passed:")),
        "startup suite gave no success report",
      );
      console.log(`${mode}: startup ${problems.length === 0 ? "ok" : "FAILED"}`);
      for (const line of log) console.log(line);
      return problems;
    }

    if (key) {
      const entry = steps.get("keyentry");
      for (const field of [
        "good",
        "bad_preserved",
        "cancel_preserved",
        "session",
        "remember",
        "webviews_clean",
        "events_clean",
      ])
        check(entry?.[field] === true, `native key entry did not prove ${field}`);
      console.log(`  ${mode} | native key entry: ${JSON.stringify(entry ?? {})}`);
    }

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

    for (const [step, expected] of [
      ["ingress_site", { attempts: 1, arrivals: 0, blocked: true }],
      ["ingress_recovery", { error: "unavailable", control: true }],
      [
        "ingress_navigation",
        { native_closed: true, wrapper_closed_suppressed: true, status: 409, window_closed: true },
      ],
      ["ingress_closed", { closed: 204, subsequent_call: 409 }],
      [
        "http_navigation",
        {
          native_closed: true,
          status: 409,
          wrapper_closed_suppressed: true,
          window_closed: true,
          fragment_open: true,
        },
      ],
    ] as const) {
      const value = steps.get(step);
      check(
        value && Object.entries(expected).every(([key, want]) => value[key] === want),
        `${step} evidence missing or wrong: ${JSON.stringify(value)}`,
      );
      console.log(`${mode} ${step} ${JSON.stringify(value)}`);
    }

    const ingress = steps.get("ingress");
    check(ingress?.control === true, "the native ingress check did not finish");
    if (ingress) console.log(`${mode} native ingress ${JSON.stringify(ingress)}`);

    check(steps.get("confirmations")?.passed === true, "native confirmation checks did not complete");

    checkRelease(steps.get("release"), feed, browserLog, check);
    console.log(`${mode} release ${JSON.stringify(steps.get("release"))}`);

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
    if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
    console.log(
      `${mode}: ${problems.length === 0 ? "ok" : "FAILED"} (web processes ${before} open, ${after} after close; ` +
        `${reached.length} commands reached, all from main: ${reached.every((w) => w === "main")})`,
    );
  } finally {
    clearInterval(offlineWatch);
    feed?.stop();
    await removeRun(root);
  }
  return problems;
}

/** The release feed check's evidence (release_native.rs), against the feed fixture and what the
 * feed server and the stand-in browser saw, which the app cannot change. */
function checkRelease(
  // biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
  release: Record<string, any> | undefined,
  feed: Feed,
  browserLog: string,
  check: (ok: unknown, what: string) => void,
) {
  check(release, "the release feed check did not finish");
  if (!release) return;
  const good = `${feed.base}/notes/0.3.0-preview.1`;
  const full = `${feed.base}/releases/0.3.0-preview.1`;
  // The fixture's `/escaped/a[1]?q=x|y`, as the notes document writes it (and WebKit navigates to it).
  const escaped = `${feed.base}/escaped/a%5B1%5D?q=x%7Cy`;
  const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
  check(release.no_default === true, "release settings existed before any feed was confirmed");
  check(release.confirmed === true, "the confirmed feed was not kept in release.json");
  check(
    same(release.stable, { app: "0.2.0", core: "1.4.0", installed_app: appVersion }),
    `the stable channel showed ${JSON.stringify(release.stable)}`,
  );
  check(release.cancel_kept === true, "cancelling a feed change did not keep the old feed and its copy");
  const vo = release.voided ?? {};
  check(
    vo.channel_disabled === true &&
      vo.channel_set === true &&
      /settings changed while the feed change waited/.test(vo.error ?? "") &&
      vo.kept === true,
    `a feed change voided while it waited: ${JSON.stringify(vo)}`,
  );
  check(
    same(release.preview, { app: "0.3.0-preview.1", core: "1.5.0-rc.1" }),
    `Preview (latest) showed ${JSON.stringify(release.preview)}`,
  );
  const n = release.notes ?? {};
  check(n.href === "pluxy-notes://localhost/", `the notes webview shows ${n.href}`);
  check(n.internals === true, "the notes webview has no IPC, so its probe proves nothing");
  for (const k of ["scripts", "images", "embedded", "handlers"] as const)
    check(n[k] === 0, `the notes document has ${n[k]} ${k}`);
  check(n.ran === null, `a script in the notes ran: ${n.ran}`);
  check(n.heading === "What changed", `the notes' Markdown was not rendered: ${n.heading}`);
  check(n.html_left_out === true, "raw HTML in the notes was not left out, or the text was lost");
  check(
    same(n.links, [good, escaped]),
    `the notes link to ${JSON.stringify(n.links)}, not ${good} and ${escaped}`,
  );
  check(
    release.control?.ok === true && release.control.source === feed.url,
    `the window's release_get control failed: ${JSON.stringify(release.control)}`,
  );
  const probe = release.notes_probe as Probe | undefined;
  check(probe?.internals, "the notes webview's probe found no IPC");
  for (const cmd of commands) check(probe?.calls[cmd], `the notes webview's probe did not try ${cmd}`);
  for (const [cmd, c] of Object.entries(probe?.calls ?? {}))
    check(!c.ok && REFUSED.test(c.detail), `${cmd} from the notes webview: ${JSON.stringify(c)}`);
  const nav = release.navigation ?? {};
  check(same(nav.unlinked_opened, []), `an unlinked address opened: ${JSON.stringify(nav.unlinked_opened)}`);
  check(same(nav.opened, [good]), `the notes link opened ${JSON.stringify(nav.opened)}`);
  check(same(nav.full, [good, full]), `View full release notes opened ${JSON.stringify(nav.full)}`);
  check(
    same(nav.escaped, [good, full, escaped]),
    `the link with escaped characters opened ${JSON.stringify(nav.escaped)}`,
  );
  check(nav.stayed === "pluxy-notes://localhost/", `the notes webview navigated to ${nav.stayed}`);
  const dl = release.downloads ?? {};
  check(
    dl.refused >= 1 && dl.saved === false && dl.right_clicks >= 1 && dl.menu_shown === false,
    `downloads and context menu in the notes webview: ${JSON.stringify(dl)}`,
  );
  const ex = release.expand ?? {};
  check(
    ex.first_follows === true && ex.collapse_closed === true && ex.expand_follows === true,
    `Expand/Collapse: ${JSON.stringify(ex)}`,
  );
  const sc = release.scrolled ?? {};
  const g = (step: string) => sc[step]?.geometry ?? {};
  check(
    sc.unscrolled?.placed &&
      sc.back?.placed &&
      sc.partly?.placed &&
      g("partly").top < g("partly").nav &&
      Math.abs(sc.partly.placed.y - g("partly").nav) <= 2 &&
      sc.hidden?.want === null &&
      sc.hidden?.placed === null,
    `the notes webview on a scrolled page: ${JSON.stringify(sc)}`,
  );
  const off = release.offline ?? {};
  check(
    off.kept === true && off.same_time === true && /Could not reach the release feed/.test(off.message ?? ""),
    `offline: ${JSON.stringify(off)}`,
  );
  // The notice moved the notes area (so the check below is not vacuous), and the webview followed.
  check(off.area_moved === true, `the offline notice did not move the notes area: ${JSON.stringify(off)}`);
  check(release.closed === true, "notes webviews stayed open after leaving the page");
  // What the feed server and the stand-in browser saw, apart from the app's own report.
  const seen = Object.fromEntries(feed.requests);
  check(same(seen, { "/feed.json": 1 }), `the feed server saw ${JSON.stringify(seen)}, not one feed read`);
  const opened = existsSync(browserLog) ? readFileSync(browserLog, "utf8").trim().split("\n") : [];
  check(same(opened, [good, full, escaped]), `the stand-in browser opened ${JSON.stringify(opened)}`);
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
      if (i === 10) {
        for (const tool of ["fusermount3", "fusermount"])
          spawnSync(tool, ["-u", "-z", join(root, "runtime", "doc")]);
      }
      await Bun.sleep(500);
    }
  }
}

// The services and tools the session needs, before anything is built.
const missing = missingPrerequisites();
if (missing.length > 0) {
  console.error(
    `native test setup failed: services or tools it needs are not installed (Ubuntu 24.04 package names):\n- ${missing.join("\n- ")}`,
  );
  process.exit(1);
}

run("bun", ["run", "build"], app);
// The app, and the probe the session uses to check the desktop services (examples/session_probe.rs).
run(
  "cargo",
  ["build", "--locked", "--features", "native-test", "--bin", "pluxy-app", "--example", "session_probe"],
  tauri,
);
const targetDir = resolvePath(tauri, process.env.CARGO_TARGET_DIR ?? "target");
const bin = join(targetDir, "debug", "pluxy-app");
const probe = join(targetDir, "debug", "examples", "session_probe");
const appImageDir = mkdtempSync(join(tmpdir(), "pluxy-app-native-appimage-"));
const problems: string[] = [];
let appImage = ""; // the smoke AppImage the sessions run
let setupFailure: string | undefined;

try {
  appImage = await buildSmokeAppImage(appImageDir);
  const prepared = mkdtempSync(join(tmpdir(), "pluxy-app-native-core-"));
  try {
    const { core } = JSON.parse(run("go", ["run", "./e2e/panelcore", "-probe", "-prepare", prepared], next));
    const p = await runApp("managed", bin, { mode: "managed", core_path: core });
    p.push(...(await runApp("managed", bin, { mode: "managed", core_path: core }, undefined, true)));
    p.push(...(await runApp("managed", bin, { mode: "managed", core_path: core }, undefined, true, true)));
    for (const suite of ["window", "window-no-tray"] as const)
      p.push(
        ...(await runApp(
          "managed",
          bin,
          { mode: "managed", core_path: core },
          undefined,
          false,
          suite === "window-no-tray",
          undefined,
          suite,
        )),
      );
    problems.push(...p.map((x) => `managed: ${x}`));
  } finally {
    rmSync(prepared, { recursive: true, force: true });
  }

  for (const mode of ["local", "remote"] as const) {
    const c = await startCore(mode === "remote");
    try {
      const conn = { mode, url: c.base, key: c.key, ...(c.ca ? { ca_file: c.ca } : {}) };
      const p = await runApp(mode, bin, conn, c.base, false, false, c.key);
      p.push(...(await runApp(mode, bin, conn, c.base, true)));
      // The app only connected: its quitting leaves this core running.
      if ((await managementStatus(c.base, c.key, c.ca)) !== 200) p.push("the core stopped when the app quit");
      problems.push(...p.map((x) => `${mode}: ${x}`));
    } finally {
      c.proc.stdin?.end();
      await new Promise((r) => c.proc.once("exit", r));
    }
  }
} catch (e) {
  if (!(e instanceof SetupError)) throw e;
  setupFailure = e.message;
} finally {
  rmSync(appImageDir, { recursive: true, force: true });
}

if (setupFailure !== undefined) {
  console.error(`\nnative test setup failed:\n${setupFailure}`);
  process.exit(1);
}
if (problems.length > 0) {
  console.error(`\n${problems.length} problems:\n- ${problems.join("\n- ")}`);
  process.exit(1);
}
console.log("native test passed in all three modes");
