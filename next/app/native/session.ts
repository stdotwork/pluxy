// One native run's session. run.ts starts this inside the run's private session bus and X server:
//
//   dbus-run-session -- xvfb-run ... bun native/session.ts <session_probe> <AppImage> <nonce> <app> [args...]
//
// It starts the desktop services the app's later checks need (the portal frontend with its gtk
// backend, which start the document portal and the permission store, and the tray watcher),
// checks each of them for real before anything else, and only then runs the app. A service that
// is missing or does not work fails the setup with a message naming its package; nothing is
// skipped. When the app has exited, it stops what it started.
//
// The checks, each printed as `native-prereq: ...`:
// - the portal frontend answers and serves the gtk backend this session started (portals.conf in
//   the run's own config folder selects it);
// - a real temporary file goes to the document portal over D-Bus with a file descriptor
//   (session_probe), and its bytes read back through the portal's mount;
// - org.kde.StatusNotifierWatcher is owned by the tray watcher service started here, and an item
//   registered with it shows in RegisteredStatusNotifierItems (session_probe);
// - the disposable AppImage runs with APPIMAGE_EXTRACT_AND_RUN=1, in this cleared environment.
//
// Exit code 78 means the setup failed; any other is the app's. run.ts goes by the "ready" line,
// not by the code, since the app could exit with 78 too.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { smokeProblem } from "./appimage";
import { busNames, documentPortal, gtkBackend, portalFrontend, type Service, trayWatcher } from "./services";

const [probe, appImage, nonce, app, ...appArgs] = process.argv.slice(2);
if (!probe || !appImage || !nonce || !app) {
  console.error("usage: bun native/session.ts <session_probe> <AppImage> <nonce> <app> [args...]");
  process.exit(2);
}

const SETUP_FAILED = 78;
const READY_MS = 30_000;

class SetupError extends Error {}

const say = (line: string) => console.log(`native-prereq: ${line}`);

interface Running {
  service: Service;
  proc: ChildProcess;
  /** The service's last lines, shown when the setup fails. */
  log: string[];
  /** Why it is no longer running. */
  ended?: string;
}
const running: Running[] = [];
let appProc: ChildProcess | undefined;

function setting(name: string): string {
  const value = process.env[name];
  if (!value) throw new SetupError(`${name} is not set in the session`);
  return value;
}

/** Starts a service in this session: it inherits the session's bus, display and folders. */
function start(service: Service): Running {
  const proc = spawn(service.file, [], { stdio: ["ignore", "pipe", "pipe"] });
  const r: Running = { service, proc, log: [] };
  proc.once("error", (e) => {
    r.ended = `could not be started from ${service.file}: ${e.message}`;
  });
  proc.once("exit", (code, signal) => {
    r.ended = `exited with ${code ?? signal}`;
  });
  for (const stream of [proc.stdout, proc.stderr]) {
    if (!stream) continue;
    createInterface({ input: stream }).on("line", (line) => {
      r.log.push(line);
      if (r.log.length > 40) r.log.shift();
    });
  }
  running.push(r);
  return r;
}

/** Runs session_probe; its stdout is `key=value` lines, its stderr says what is wrong. */
function ask(...args: string[]): Record<string, string> {
  const r = spawnSync(probe as string, args, { encoding: "utf8", timeout: 40_000 });
  if (r.status !== 0) throw new Error((r.stderr || r.error?.message || `exited with ${r.status}`).trim());
  const out: Record<string, string> = {};
  for (const line of (r.stdout ?? "").split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) out[line.slice(0, i)] = line.slice(i + 1);
  }
  return out;
}

function field(out: Record<string, string>, key: string): string {
  const value = out[key];
  if (value === undefined) throw new Error(`session_probe printed no ${key}`);
  return value;
}

/** Tries `attempt` until it stops throwing, for up to READY_MS; a service in `watching` that
 * exits meanwhile fails it at once. */
async function until<T>(what: string, attempt: () => T, watching: Running[]): Promise<T> {
  const deadline = Date.now() + READY_MS;
  for (;;) {
    for (const r of watching) {
      if (r.ended) throw new SetupError(`${r.service.what} (package ${r.service.package}) ${r.ended}`);
    }
    try {
      return attempt();
    } catch (e) {
      if (Date.now() > deadline) throw new SetupError(`${what}: ${(e as Error).message}`);
    }
    await Bun.sleep(250);
  }
}

/** Waits until the service this session started owns the bus name (not one the bus started). */
function owns(name: string, r: Running) {
  return until(`${r.service.what} (package ${r.service.package}) does not own ${name}`, () => {
    const pid = Number(field(ask("owner", name), "pid"));
    if (pid !== r.proc.pid) throw new Error(`pid ${pid} owns it, not the one started here (${r.proc.pid})`);
  }, [r]);
}

async function setup() {
  const config = setting("XDG_CONFIG_HOME");
  const cache = setting("XDG_CACHE_HOME");
  const runtime = setting("XDG_RUNTIME_DIR");
  if (process.env.APPIMAGE_EXTRACT_AND_RUN !== "1") {
    throw new SetupError("APPIMAGE_EXTRACT_AND_RUN=1 is not in the session's environment");
  }

  // The gtk backend is the one the frontend uses, chosen in the run's own config folder.
  mkdirSync(join(config, "xdg-desktop-portal"), { recursive: true });
  writeFileSync(join(config, "xdg-desktop-portal", "portals.conf"), "[preferred]\ndefault=gtk\n");

  // The backend first and ready: the frontend looks for its backends when it starts.
  const gtk = start(gtkBackend);
  const tray = start(trayWatcher);
  await owns(busNames.gtkBackend, gtk);
  const frontend = start(portalFrontend);
  await owns(busNames.desktop, frontend);

  const portal =
    await until(`${portalFrontend.what} (package ${portalFrontend.package}) does not work with the gtk backend`, () => {
      const out = ask("portal");
      const served = [Number(field(out, "frontend_pid")), Number(field(out, "gtk_backend_pid"))];
      if (served[0] !== frontend.proc.pid || served[1] !== gtk.proc.pid) {
        throw new Error(
          `it is served by pids ${served}, not those started here (${frontend.proc.pid}, ${gtk.proc.pid})`,
        );
      }
      return out;
    }, [frontend, gtk]);
  say(
    `portal ok: frontend pid ${field(portal, "frontend_pid")} with the gtk backend pid ${field(portal, "gtk_backend_pid")}` +
      " (portals.conf default=gtk)",
  );

  // A real file, through the document portal and back through its mount.
  const dir = mkdtempSync(join(cache, "portal-"));
  const file = join(dir, "document.bin");
  const bytes = randomBytes(64 * 1024);
  writeFileSync(file, bytes);
  const docs =
    await until(`${documentPortal.what} (package ${documentPortal.package}; it needs /dev/fuse and fusermount3 from the fuse3 package) gave no readable copy of a file`, () => {
      const out = ask("documents", file);
      const mount = field(out, "mount");
      if (mount !== join(runtime, "doc"))
        throw new Error(`its mount point is ${mount}, not ${join(runtime, "doc")}`);
      const back = readFileSync(join(mount, field(out, "doc"), field(out, "name")));
      if (!back.equals(bytes))
        throw new Error("the bytes read back through the mount differ from the file's");
      return { mount, doc: field(out, "doc"), size: back.length };
    }, [frontend]);
  say(
    `documents ok: ${docs.size} bytes added over D-Bus with a file descriptor, read back through ${docs.mount}/${docs.doc}/`,
  );

  // A real item, through the watcher.
  const watcher =
    await until(`${trayWatcher.what} (package ${trayWatcher.package}) does not serve ${busNames.watcher}`, () => {
      const out = ask("watcher");
      const pid = Number(field(out, "watcher_pid"));
      if (pid !== tray.proc.pid)
        throw new Error(`pid ${pid} owns the name, not the service started here (${tray.proc.pid})`);
      return out;
    }, [tray]);
  say(
    `watcher ok: ${busNames.watcher} owned by pid ${field(watcher, "watcher_pid")}; registered ${field(watcher, "item")}` +
      ` and found it in RegisteredStatusNotifierItems (${field(watcher, "property_reads")} property reads)`,
  );

  // The disposable AppImage, in this session's cleared environment, in a folder of the run's own.
  const out = spawnSync(appImage as string, [nonce as string], {
    encoding: "utf8",
    env: { ...process.env, TMPDIR: mkdtempSync(join(cache, "appimage-")) },
    timeout: 60_000,
  });
  const problem =
    out.status === 0 ? smokeProblem(out.stdout, nonce as string) : `exited with ${out.status}: ${out.stderr}`;
  if (problem)
    throw new SetupError(`the smoke AppImage did not run with APPIMAGE_EXTRACT_AND_RUN=1: ${problem}`);
  say(`appimage ok: ${out.stdout.trim()}`);
  say("ready");
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Stops what this session started: the services it ran, and the ones the bus started for it
 * (the document portal unmounts its folder when it ends). */
async function stopServices() {
  appProc?.kill("SIGKILL");
  const pids = new Set<number>();
  for (const name of [busNames.documents, busNames.permissionStore]) {
    try {
      pids.add(Number(field(ask("owner", name), "pid")));
    } catch {
      // not running
    }
  }
  for (const r of running) if (r.proc.pid && !r.ended) pids.add(r.proc.pid);
  for (const pid of pids) {
    try {
      process.kill(pid, "SIGTERM");
    } catch {
      // gone already
    }
  }
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline && [...pids].some(alive)) await Bun.sleep(100);
  for (const pid of pids) if (alive(pid)) process.kill(pid, "SIGKILL");
  if (pids.size > 0) say(`stopped ${pids.size} services`);
}

for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => void stopServices().then(() => process.exit(128)));
}

let code = SETUP_FAILED;
try {
  await setup();
  code = await new Promise<number>((resolve, reject) => {
    appProc = spawn(app, appArgs, { stdio: "inherit" });
    appProc.once("error", reject);
    appProc.once("exit", (c) => resolve(c ?? 1));
  });
} catch (e) {
  say(`FAILED ${e instanceof SetupError ? e.message : String(e)}`);
  for (const r of running)
    for (const line of r.log) console.log(`native-prereq: ${r.service.package} | ${line}`);
} finally {
  await stopServices();
}
process.exit(code);
