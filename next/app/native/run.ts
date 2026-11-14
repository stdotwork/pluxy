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
// - in managed mode, this device's extension catalog installs, swaps, rolls back, disables and
//   removes page-only packages from a local registry and from local files through the native
//   file chooser, refusing reserved IDs and a failed candidate (device.ts, device_native.rs);
// - closing the window leaves no webview and no web process, with the app still in the tray;
// - quitting stops the app's own core, and leaves a local or remote core running;
// - in a second managed run, a core that exits before the app state exists is recovered.
// - the management key rotates natively (native_test.rs, rotation_test.rs): core_call refuses
//   it, the app keeps working on the new key as the old one was kept, the key window stays open
//   when the window closes to the tray and goes when a later rotation shows its own, no webview
//   (the window's or an extension page's) and no output line holds any key the run rotated; in
//   local mode the app then starts again on the same folders and signs in with the kept key.
//
// The core steps (src-tauri/src/native_core.rs) run in a managed session of their own, on the
// build with artifact-test-trust, which starts with no core installed: they install, update, roll
// back and adopt the app's own core through the real native confirmation, from signed test
// artifacts made here with a key generated for the run (coreSource below); the run's root joins
// the trust profile in its temporary HOME, and the private key never leaves memory. A sentinel
// core this runner starts must still run, untouched, when the app has quit. The app runs only a
// core it installed (cores.rs): the startup runs and the early-exit run adopt their core_path
// program before they start, and the other managed runs start with signed release 2.0.0
// installed, which brings the bundled test packages beside its program.
//
// The packaged app too: the test package (package.ts test, an AppImage with the test identifier,
// product name and compiled test marker) runs through the same checks in local mode, as the
// AppImage itself, not the binary it was built from. Before anything else, each run's app reports
// its updater gate (the check P8c2's updater must pass before any updater plugin call): with the
// same test trust profile in every run, the test package passes it and the normal-identifier dev
// binary is refused. Then the release command (package.ts release) must pass a clean release
// build and a clean AppImage, and refuse the test package (renamed or not) and an AppImage holding
// a test-feature binary.
// In managed mode it then runs the window checks (window.ts, native_window.rs) in two more
// sessions, one with a tray watcher and one without, and the Access checks (access-suite.ts,
// access_native.rs) in another, with the desktop portal's file chooser (GTK_USE_PORTAL=1).
// Then, in a session of its own (instances.ts, instances-session.ts), the single-instance
// scenario: a second launch on the same profile exits and raises the first's window, a launch on
// another profile runs as its own app, and a killed owner's stale lock is recovered.
// The signed native-tier admission check (admission.ts, admission_native.rs) runs in one more
// session, with the app built with artifact-test-trust and a test trust profile in the run's HOME,
// and the program host check (program.ts, program_native.rs) in one more with that build: a signed
// summary tool, started on demand, called and cancelled, stopped when idle and disabled, then
// (swap_native.rs) swapped to a new release while a call drains on the old one, kept when a
// candidate is incompatible, crashed into backoff and quarantine, and rolled back; its processes
// are gone after the app quits. The other sessions run a build without that
// feature; its device check shows the same profile admits nothing there.
// The cross-package routing check (routing.ts, routing_native.rs) runs in one more session with
// that build: signed tools calling each other's declared exports through the host, a curated web
// package's page calling native imports after their native consent, and the refusals of
// undeclared, forged, cyclic, stale, revoked and Unverified routes, each with a positive control.
// The declared jobs check (jobs_native.rs) runs after it in that session: a consumer lists the
// registrations of its kind and starts jobs whose batches the host delivers with their source,
// with the refusals of forged, duplicate and stale sources, past the window and the size and job
// limits, cancel and restart, and a disabled producer, each with a positive control.
// The vault check (vault.ts, vault_native.rs) runs in one more session with that build: a key
// typed into native key entry reaches a writer tool and a final consumer only on their pipes after
// native confirmation, an execution tool gets only references, references refused to another
// recipient, call or release, open journals reconciled after a crash without a second write, and
// the key and a backup canary absent from webviews, logs and the app's output.
// The spawn check (spawn.ts, spawn_native.rs) runs in one more session with that build: a launch
// tool asks the host to spawn the launches the person confirmed, a detached agent and managed
// runtimes, each with exactly the confirmed argv and cwd and the filtered launch environment, the
// key only in the final process's environment; the agent outlives the tool's crash and idle stop
// and the app; the runtimes stay controlled across the tool's restart, and Stop and Restart reach
// only the registered group. After the app quits, the runner checks the detached agent still runs
// and the runtimes do not, then ends every process the check named.
// The launcher check (launcher.ts, launcher_native.rs) runs in one more session with that build:
// the execution program prepares a terminal launch (Automatic, a program found on PATH) and
// direct ones, the host spawns them after native confirmation; a remembered launch starts again in
// one click from the window's launcher and from the tray's Start… menu, while a page's Start,
// another chain and a changed executable or agent ask again; the three key consumers (an agent
// given its key in its environment, a final process given key, base URL and model, a credential
// bridge given its key on stdin) get the key and the execution program only references. After
// the app quits, the runner checks the launched agents still run, then ends them.
// The Codex session reader check (sessions.ts, sessions_native.rs) runs in one more session with
// that build: the reader (next/app-tools/codex-sessions, built here) lists a copied Codex home
// exactly as a hand-written expect file says, shows a session only after a native confirmation of
// each detail call, leaves the Codex home's bytes and modification times as they were, keeps the
// config.toml and auth.json canaries out of pages, webviews and the app's output, and leaves no
// reader process when idle or disabled.
// The usage hub check (usage.ts, usage_native.rs) runs in one more session with that build: the hub
// and the test-only fixture parser (next/app-tools/agent-usage and usage-fixture, built here) sync a
// fixture root through the host's jobs, driven from the hub's page, and its report equals a
// hand-written expect file, also after a repeated sync, an appended and an added file, and a parser
// disabled mid-file; ingest is refused to everyone but the host and report to another package's page;
// no hub or parser process is left when idle.
// The privacy mode checks (privacy.ts, native_privacy.rs) run in one more session.
//
// Each run gets a fresh HOME, XDG folders and runtime folder, a session bus and an X server of its
// own (dbus-run-session, xvfb-run), and random ports. Inside that session, before the app,
// native/session.ts starts the desktop services later checks need (the portals and a tray
// watcher), checks each of them for real, and runs a disposable AppImage with
// APPIMAGE_EXTRACT_AND_RUN=1; a missing or broken service fails the run at setup (services.ts
// names the packages). Run from next/app: bun native/run.ts (needs dbus-run-session, xvfb-run,
// Go, cargo and those services).

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, type KeyObject, randomBytes, randomUUID, sign } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { buildAccessFixtures } from "./access";
import { buildAdmissionFixtures, MARKER_FILE, PROFILE_FILE } from "./admission";
import { appImageHeader, buildSmokeAppImage, packAppImage } from "./appimage";
import { buildDeviceFixtures } from "./device";
import { type Feed, standInBrowser, startFeed } from "./feed";
import { runInstances } from "./instances";
import { buildLauncherFixtures } from "./launcher";
import { descendants, webProcesses } from "./processes";
import { buildProgramFixtures } from "./program";
import { buildRoutingFixtures } from "./routing";
import { missingPrerequisites } from "./services";
import { buildSessionsFixtures } from "./sessions";
import { buildSpawnFixtures } from "./spawn";
import { buildUsageFixtures } from "./usage";
import { buildVaultFixtures } from "./vault";

const app = join(import.meta.dir, "..");
const next = join(app, "..");
const tauri = join(app, "src-tauri");
const tauriConf = JSON.parse(readFileSync(join(tauri, "tauri.conf.json"), "utf8"));
const identifier: string = tauriConf.identifier;
const appVersion: string = tauriConf.version;
/** What the test package must be built as; written here, not read from what package.ts made. */
const TEST_ID = "dev.pluxy.desktop.test";
const TEST_PRODUCT = "pluxy Test";
const commands = [
  ...readFileSync(join(tauri, "src", "command_names.rs"), "utf8").matchAll(/^\s+"(\w+)",$/gm),
].map((m) => m[1] as string);
const PAGE = "ext/test/probe/probe";
/** Tauri's command check refusing a call (debug builds say where; release builds "by ACL"). */
const REFUSED = /not allowed( on window|\. Permissions associated| by ACL)/;
const TIMEOUT = 240_000;

type Mode = "managed" | "local" | "remote";
/** What package.ts test wrote into its folder's test-package.json. */
interface TestPackage {
  /** The AppImage, under its -test file name. */
  appimage: string;
  /** The temporary updater public key compiled into it (no private key is kept). */
  updater_pubkey: string;
}
/** Which app a run starts, and what its updater gate must say. */
interface Artifact {
  /** How the run's lines are labelled. */
  label: string;
  /** The program the session starts: the dev binary, or the AppImage itself. */
  bin: string;
  /** The identifier the app's config folder is named after. */
  identifier: string;
  /** The updater gate passes (the test package) or refuses (anything else), with the same
   * test trust profile in place either way. */
  gate: "passes" | "refuses";
  /** What a refusal must say. A build with artifact-test-trust refuses a normal identifier for
   * that identifier; one without it never had updater trust at all. */
  refusal?: RegExp;
}
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

function run(cmd: string, args: string[], cwd: string, env?: NodeJS.ProcessEnv) {
  const r = spawnSync(cmd, args, { cwd, env, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
  return r.stdout;
}

/** Runs a command whose output is only shown (a build, which can print a lot). */
function runShown(cmd: string, args: string[], cwd: string) {
  const r = spawnSync(cmd, args, { cwd, stdio: ["ignore", "inherit", "inherit"] });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
}

/** A core from panelcore, running until its stdin closes. */
async function startCore(
  tls: boolean,
): Promise<{ proc: ChildProcess; base: string; key: string; config: string; ca?: string }> {
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
    void Promise.all([...sessions].map(stopSession)).finally(() => {
      removeTemporary();
      process.exit(130);
    });
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

/** A process the spawn check started (spawn_native.rs writes them to known.json): its ID and a
 * string only its command line holds. */
interface Known {
  pid: number;
  marker: string;
}

function readKnown(dir: string): { detached: Known[]; managed: Known[] } {
  try {
    const k = JSON.parse(readFileSync(join(dir, "known.json"), "utf8"));
    return { detached: k.detached ?? [], managed: k.managed ?? [] };
  } catch {
    return { detached: [], managed: [] };
  }
}

/** Whether k's process runs, as that process: its command line still holds k's marker. */
function owned(k: Known): boolean {
  try {
    return alive(k.pid) && readFileSync(`/proc/${k.pid}/cmdline`, "utf8").includes(k.marker);
  } catch {
    return false;
  }
}

/** Kills each process the spawn check named that still runs as itself. */
function cleanKnown(dir: string) {
  const { detached, managed } = readKnown(dir);
  for (const k of [...detached, ...managed])
    if (owned(k))
      try {
        process.kill(k.pid, "SIGKILL");
      } catch {
        // gone already
      }
}

/** Whether pid runs the program at path (and is not some later process with its number). */
function runs(pid: number, path: string): boolean {
  try {
    return alive(pid) && readlinkSync(`/proc/${pid}/exe`).startsWith(path);
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

/** Signed test core releases for the managed run, in a folder of their own. */
interface CoreSource {
  /** The index file, which the app's connection names as its core source. */
  index: string;
  /** A core program of the runner's, which the app must adopt before it runs it. */
  custom: string;
  /** The trust profile's root for the run's key, bound to the index. */
  root: object;
}

const CORE_ID = "pluxy/core";
const PLATFORM = `${process.platform}/${process.arch === "x64" ? "amd64" : process.arch}`;
const VERSION_FLAG = "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=";

function hex(b: Buffer | Uint8Array): string {
  return Buffer.from(b).toString("hex");
}

/** One release in the index: the archive of folder, its descriptor and a signature by key. */
function release(dir: string, version: string, folder: string, key: KeyObject, keyId: string, bad = false) {
  const name = `pluxy-core-${version}-${PLATFORM.replace("/", "-")}.tar.gz`;
  run("tar", ["-czf", join(dir, name), "-C", folder, "."], dir);
  const bytes = readFileSync(join(dir, name));
  const descriptor = JSON.stringify({
    id: CORE_ID,
    version,
    tier: "native",
    platform: PLATFORM,
    size: bytes.length,
    sha256: createHash("sha256").update(bytes).digest("hex"),
  });
  const signature = sign(null, Buffer.from(descriptor), key);
  if (bad) signature[0] = (signature[0] ?? 0) ^ 0xff;
  return {
    product: "core",
    version,
    channel: "stable",
    published: "2026-11-01T00:00:00Z",
    artifacts: [{ platform: PLATFORM, url: name, descriptor, signature: hex(signature), key_id: keyId }],
  };
}

/** Builds cores 1.0.0 and 2.0.0 (with the bundled test packages of prepared), a 3.0.0 that
 * breaks its config and exits, and a 2.1.0 whose signature does not verify, signed by a key made
 * for this run, with their index. */
function coreSource(dir: string, prepared: string): CoreSource {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const keyId = `native-test-${randomUUID()}`;
  const releases = [];
  for (const version of ["1.0.0", "2.0.0"]) {
    const folder = join(dir, `core-${version}`);
    mkdirSync(folder);
    run(
      "go",
      [
        "build",
        "-buildvcs=false",
        "-ldflags",
        `${VERSION_FLAG}${version}`,
        "-o",
        join(folder, "pluxy"),
        "./cmd/pluxy",
      ],
      next,
    );
    cpSync(join(prepared, "bundled"), join(folder, "bundled"), { recursive: true });
    releases.push(release(dir, version, folder, privateKey, keyId));
  }
  const broken = join(dir, "core-3.0.0");
  mkdirSync(broken);
  writeFileSync(
    join(broken, "pluxy"),
    "#!/bin/sh\nset -e\n# A bad candidate: it breaks its config (-config <path>) and exits.\nprintf 'not json' > \"$2\"\necho 'pluxy: bad candidate broke its config' >&2\nexit 3\n",
  );
  chmodSync(join(broken, "pluxy"), 0o755);
  releases.push(release(dir, "3.0.0", broken, privateKey, keyId));
  releases.push(release(dir, "2.1.0", join(dir, "core-2.0.0"), privateKey, keyId, true));
  const index = join(dir, "index.json");
  writeFileSync(index, JSON.stringify({ format: 1, releases }, null, 2));
  const jwk = publicKey.export({ format: "jwk" });
  return {
    index,
    custom: join(prepared, "pluxy"),
    root: {
      kind: "test",
      publisher: "pluxy",
      source: index,
      key_id: keyId,
      public_key: hex(Buffer.from(jwk.x ?? "", "base64url")),
    },
  };
}

/** A core this runner starts, which the app never started: it must outlive the app's run. */
async function startSentinel(dir: string, from: string) {
  cpSync(from, dir, { recursive: true });
  const key = hex(randomBytes(32));
  const config = join(dir, "config.json");
  writeFileSync(
    config,
    JSON.stringify({
      listen: "127.0.0.1:0",
      client_keys: [hex(randomBytes(32))],
      management_key: key,
      credentials_dir: join(dir, "credentials"),
      packages: { dir: join(dir, "packages") },
    }),
    { mode: 0o600 },
  );
  const before = readFileSync(config);
  const proc = spawn(join(dir, "pluxy"), ["-config", config], {
    cwd: dir,
    stdio: ["ignore", "ignore", "pipe"],
  });
  const base = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`the sentinel core exited with ${code}`)));
    if (proc.stderr)
      createInterface({ input: proc.stderr }).on("line", (line) => {
        const m = /^pluxy: listening on (\S+)$/.exec(line);
        if (m?.[1]) resolve(m[1]);
      });
  });
  return { proc, base, key, config, before, exe: statSync(join(dir, "pluxy")).ino };
}

/** What the managed run's core steps must report (native_core.rs). */
const CORE_STEPS: [string, Record<string, unknown>][] = [
  ["core_missing", { state: "missing", connected: false }],
  ["core_no_roots", { refused: true, dialog: false }],
  [
    "core_install",
    {
      outcome: "done",
      version: "1.0.0",
      connected: true,
      signed_in: true,
      status: 200,
      reported: "1.0.0",
      exe_in_app: true,
    },
  ],
  ["core_bad_signature", { refused: true, running: "1.0.0" }],
  [
    "core_update",
    {
      outcome: "done",
      version: "2.0.0",
      status: 200,
      reported: "2.0.0",
      old_alive: false,
      new_pid: true,
      config_kept: true,
    },
  ],
  [
    "core_bad_start",
    {
      outcome: "failed",
      version: "2.0.0",
      status: 200,
      reported: "2.0.0",
      old_alive: false,
      running: true,
      config_restored: true,
      failed: "3.0.0",
    },
  ],
  [
    "core_rollback",
    {
      unknown_refused: true,
      cancel: "cancelled",
      cancel_kept: true,
      outcome: "done",
      version: "1.0.0",
      status: 200,
      reported: "1.0.0",
      config_kept: true,
    },
  ],
  [
    "core_adopt",
    {
      custom_not_run: true,
      running_before: "1.0.0",
      outcome: "done",
      status: 200,
      exe_in_app: true,
      custom_running: false,
      back: "1.0.0",
      back_outcome: "done",
    },
  ],
];

/** The management key in core's config file: its current one, also after a rotation. */
function managementKey(config: string): string {
  return JSON.parse(readFileSync(config, "utf8")).management_key;
}

/** A signed release from a core source, installed before the app starts. */
interface Release {
  from: CoreSource;
  version: string;
}

/** The window checks (window.ts), with or without a tray watcher in the session, the privacy
 * mode checks (privacy.ts), and the Access checks (access-suite.ts). */
type Suite = "window" | "window-no-tray" | "privacy" | "access";

/** For a local or remote core, rotation.coreConfig is its config file. rotation.root keeps the
 * run's folders for the caller to remove; with rotation.restart the app starts again on them,
 * with the connection an earlier run left. */
interface Rotation {
  coreConfig?: string;
  root?: string;
  restart?: boolean;
}

/** Runs the app once with connection as its saved connection, and checks what it reports.
 * updaterKey goes into the run's test trust profile; with key, the run also drives native key
 * entry. With suite, the session runs window.ts, which runs the app through the window checks'
 * stages. earlyExit: the app's setup waits for its core's exit callback before the app state
 * exists (PLUXY_APP_TEST_EARLY_EXIT), and only the recovery of that core is checked. admission:
 * the session checks only signed native-tier admission; program: only the program host and swap
 * checks; routing: only the cross-package routing check; vault: only the vault check. core:
 * the session runs only the core steps, against that core source. release: the app starts with
 * that release installed and selected. spawnCheck: only the spawn check. reader: only the session
 * reader check. launcherCheck: only the launcher check. */
async function runApp(
  mode: Mode,
  artifact: Artifact,
  updaterKey: string,
  connection: object,
  base?: string,
  startup = false,
  noTray = false,
  key?: string,
  suite?: Suite,
  rotation: Rotation = {},
  admission = false,
  program = false,
  earlyExit = false,
  routing = false,
  vault = false,
  core?: CoreSource,
  release?: Release,
  spawnCheck = false,
  reader = false,
  usage = false,
  launcherCheck = false,
): Promise<string[]> {
  const { label, bin } = artifact;
  const root = rotation.root ?? mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
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
      // Where an AppImage's runtime extracts it (APPIMAGE_EXTRACT_AND_RUN), inside the run.
      tmp: join(root, "tmp"),
    };
    for (const d of Object.values(dirs)) mkdirSync(d, { recursive: true, mode: 0o700 });
    mkdirSync(join(dirs.config, artifact.identifier), { recursive: true });
    if (!rotation.restart)
      writeFileSync(join(dirs.config, artifact.identifier, "connection.json"), JSON.stringify(connection), {
        mode: 0o600,
      });
    // A managed run with a core_path and no core source starts with that program adopted, as the
    // person would adopt it in the window (core_adopt), so the app runs its own core.
    const corePath = (connection as { core_path?: string }).core_path;
    if (mode === "managed" && !core && corePath)
      run(probe, ["adopt-core", join(dirs.data, artifact.identifier, "cores"), corePath], app);
    // A release is installed the way a confirmed core_install would, trusting the run's root from
    // a HOME of its own: the profile in the app's HOME has no publisher roots.
    if (mode === "managed" && !core && release) {
      const trustHome = join(root, "install-home");
      mkdirSync(trustHome, { mode: 0o700 });
      const nonce = /^nonce=([0-9a-f]{64})$/m.exec(run(probe, ["trust-marker", trustHome], app))?.[1] ?? "";
      writeFileSync(
        join(trustHome, PROFILE_FILE),
        JSON.stringify({ roots: [release.from.root], updater: null }),
        {
          mode: 0o600,
        },
      );
      run(
        probe,
        ["install-core", join(dirs.data, artifact.identifier, "cores"), release.from.index, release.version],
        app,
        { ...process.env, HOME: trustHome, PLUXY_TEST_TRUST_NONCE: nonce },
      );
      rmSync(trustHome, { recursive: true, force: true });
    }
    // The main checks close the window to the tray; the window suite starts with Ask every time.
    if (!suite)
      writeFileSync(
        join(dirs.config, artifact.identifier, "window.json"),
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
    // This device's extension catalog is the same in every mode: check it once, in managed mode.
    const device =
      mode === "managed" &&
      !admission &&
      !program &&
      !routing &&
      !vault &&
      !core &&
      !spawnCheck &&
      !launcherCheck &&
      !reader &&
      !usage &&
      suite !== "access"
        ? join(root, "device")
        : undefined;
    const access = suite === "access" ? join(root, "access") : undefined;
    if (access) {
      mkdirSync(access);
      writeFileSync(
        join(dirs.config, identifier, "device-registries.json"),
        JSON.stringify(buildAccessFixtures(access)),
      );
      // No recent files for the session's GTK programs, the portal's file chooser among them:
      // with recent files, a folder chooser opens in its Recent view, where a typed folder
      // cannot be chosen (GTK 3.24 gtkfilechooserwidget.c gtk_file_chooser_widget_get_files);
      // without, it opens in a folder (startup mode falls back to the current folder).
      mkdirSync(join(dirs.config, "gtk-3.0"), { recursive: true });
      writeFileSync(
        join(dirs.config, "gtk-3.0", "settings.ini"),
        "[Settings]\ngtk-recent-files-enabled=false\n",
      );
    }
    // A test trust profile in HOME naming curated registries: the admission session's build
    // honours it; the device check's normal build must not.
    const curated = device || admission ? join(root, "curated") : undefined;
    // The test trust profile in HOME: its marker and nonce, and the updater key, the same in
    // every run. With curated registries or the program host's fixtures, they write the marker
    // and the publisher roots; otherwise the app's own code writes the marker (session_probe
    // trust-marker), with no roots.
    let nonce = "";
    let roots: object[] = [];
    // The program host check's signed summary tool, its curated registry and trust profile.
    const programDir = program ? join(root, "program") : undefined;
    if (programDir) {
      mkdirSync(programDir);
      const f = buildProgramFixtures(programDir, dirs.home, programFixture);
      nonce = f.nonce;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The routing check's signed tools and web packages, their registries and trust profile.
    const routingDir = routing ? join(root, "routing") : undefined;
    if (routingDir) {
      mkdirSync(routingDir);
      const f = buildRoutingFixtures(routingDir, dirs.home, programFixture);
      nonce = f.nonce;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The vault check's signed tools, their registry and trust profile, and its canaries.
    const vaultDir = vault ? join(root, "vault") : undefined;
    let canaries: string[] = [];
    if (vaultDir) {
      mkdirSync(vaultDir);
      const f = buildVaultFixtures(vaultDir, dirs.home, programFixture);
      nonce = f.nonce;
      canaries = f.canaries;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The spawn check's signed tools, agent, registry and trust profile, and its canaries.
    const spawnDir = spawnCheck ? join(root, "spawn") : undefined;
    let spawnEnv: Record<string, string> = {};
    if (spawnDir) {
      mkdirSync(spawnDir);
      const f = buildSpawnFixtures(spawnDir, dirs.home, programFixture);
      nonce = f.nonce;
      canaries = f.canaries;
      spawnEnv = f.env;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The launcher check's signed packages, terminal, agent, registry and trust profile, and its key.
    const launcherDir = launcherCheck ? join(root, "launcher") : undefined;
    let launcherBin: string | undefined;
    if (launcherDir) {
      mkdirSync(launcherDir);
      const f = buildLauncherFixtures(
        launcherDir,
        dirs.home,
        programFixture,
        executionProgram,
        executionManifest,
      );
      nonce = f.nonce;
      canaries = f.canaries;
      launcherBin = f.bin;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The session reader check's signed reader, its registry and trust profile, the Codex home
    // and its canaries.
    const sessionsDir = reader ? join(root, "sessions") : undefined;
    if (sessionsDir) {
      mkdirSync(sessionsDir);
      const f = buildSessionsFixtures(sessionsDir, dirs.home, appTools, sessionsReader);
      nonce = f.nonce;
      canaries = f.canaries;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    // The usage hub check's signed hub and fixture parser, their registry and trust profile.
    const usageDir = usage ? join(root, "usage") : undefined;
    if (usageDir) {
      mkdirSync(usageDir);
      const f = buildUsageFixtures(
        usageDir,
        dirs.home,
        appTools,
        join(appToolsTarget, "debug"),
        programFixture,
      );
      nonce = f.nonce;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries: f.registries }),
      );
    }
    if (curated) {
      mkdirSync(curated);
      if (device) mkdirSync(device);
      const a = buildAdmissionFixtures(curated, dirs.home);
      nonce = a.nonce;
      roots = JSON.parse(readFileSync(join(dirs.home, PROFILE_FILE), "utf8")).roots;
      const registries = admission
        ? a.registries
        : [
            ...(buildDeviceFixtures(device as string) as { registries: object[] }).registries,
            ...a.registries.filter((r) => r.name === "curated"),
          ];
      writeFileSync(
        join(dirs.config, artifact.identifier, "device-registries.json"),
        JSON.stringify({ version: 0, registries }),
      );
    } else if (rotation.restart) {
      // The app starts again on the first run's HOME: its marker and profile stay, and so does
      // the nonce the marker holds.
      nonce = JSON.parse(readFileSync(join(dirs.home, MARKER_FILE), "utf8")).nonce;
    } else if (
      !programDir &&
      !routingDir &&
      !vaultDir &&
      !spawnDir &&
      !sessionsDir &&
      !launcherDir &&
      !usageDir
    ) {
      try {
        nonce = /^nonce=([0-9a-f]{64})$/m.exec(run(probe, ["trust-marker", dirs.home], app))?.[1] ?? "";
        if (!nonce) throw new Error("it printed no nonce");
      } catch (e) {
        check(false, `the test trust marker was not written: ${(e as Error).message}`);
      }
    }
    if (!rotation.restart)
      writeFileSync(
        join(dirs.home, PROFILE_FILE),
        JSON.stringify({ roots, updater: { kind: "test", public_key: updaterKey } }),
        { mode: 0o600 },
      );
    const keyFile = join(root, "key-input");
    // Every key the rotation check sees, for the log audit below; it is native-test output.
    const rotatedFile = join(root, "rotated-keys");
    const wrongKey = randomUUID();
    if (key) writeFileSync(keyFile, JSON.stringify({ key, wrong: wrongKey }), { mode: 0o600 });
    // Only what the app needs: no display or session bus but the run's own.
    const env: Record<string, string> = {
      // The launcher check's stand-in terminal and agent come first.
      PATH: `${launcherBin ? `${launcherBin}:` : ""}${process.env.PATH ?? "/usr/bin:/bin"}`,
      LANG: "C.UTF-8",
      HOME: dirs.home,
      XDG_CONFIG_HOME: dirs.config,
      XDG_DATA_HOME: dirs.data,
      XDG_CACHE_HOME: dirs.cache,
      XDG_STATE_HOME: dirs.state,
      XDG_RUNTIME_DIR: dirs.runtime,
      TMPDIR: dirs.tmp,
      PLUXY_TEST_TRUST_NONCE: nonce,
      WEBKIT_DISABLE_DMABUF_RENDERER: "1",
      LIBGL_ALWAYS_SOFTWARE: "1",
      NO_AT_BRIDGE: "1",
      // GTK's own file chooser, except for the Access checks, which use the desktop portal's.
      GTK_USE_PORTAL: access ? "1" : "0",
      // The FUSE-free way to run an AppImage, which the session's smoke AppImage uses.
      APPIMAGE_EXTRACT_AND_RUN: "1",
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
      PLUXY_APP_TEST_FEED: feed.url,
      PLUXY_APP_TEST_FEED_OTHER: feed.other,
      PLUXY_APP_TEST_FEED_CA: feed.ca,
      PLUXY_APP_TEST_FEED_OFFLINE: offline,
      PLUXY_APP_TEST_BROWSER_LOG: browserLog,
      PLUXY_APP_TEST_STATE: join(root, "state.json"),
      PLUXY_APP_TEST_TRAY_HOLD: join(root, "tray-hold"),
      ...(device ? { PLUXY_APP_TEST_DEVICE: device, PLUXY_APP_TEST_CURATED: curated as string } : {}),
      ...(access ? { PLUXY_APP_TEST_ACCESS: access, PLUXY_APP_TEST_ACCESS_HOLD: join(access, "hold") } : {}),
      ...(admission ? { PLUXY_APP_TEST_ADMISSION: curated as string } : {}),
      // In the app's environment, never in the tool's (program_native.rs checks its names).
      ...(programDir ? { PLUXY_APP_TEST_PROGRAM: programDir, PLUXY_TEST_PROGRAM_CANARY: randomUUID() } : {}),
      ...(routingDir ? { PLUXY_APP_TEST_ROUTING: routingDir } : {}),
      ...(vaultDir ? { PLUXY_APP_TEST_VAULT: vaultDir } : {}),
      // The canaries no spawned process may inherit, and the variable a launch inherits by name.
      ...(spawnDir ? { PLUXY_APP_TEST_SPAWN: spawnDir, ...spawnEnv } : {}),
      // The probe clicks the tray's Start… items through the real watcher's dbusmenu.
      ...(launcherDir ? { PLUXY_APP_TEST_LAUNCHER: launcherDir, PLUXY_APP_TEST_PROBE: probe } : {}),
      ...(sessionsDir ? { PLUXY_APP_TEST_SESSIONS: sessionsDir } : {}),
      ...(usageDir ? { PLUXY_APP_TEST_USAGE: usageDir } : {}),
      ...(startup ? { PLUXY_APP_TEST_STARTUP_SUITE: artifact.identifier } : {}),
      ...(noTray ? { PLUXY_APP_TEST_NO_TRAY: "1" } : {}),
      ...(key ? { PLUXY_APP_TEST_KEY_FILE: keyFile } : {}),
      PLUXY_APP_TEST_ROTATED_FILE: rotatedFile,
      ...(rotation.coreConfig ? { PLUXY_APP_TEST_CORE_CONFIG: rotation.coreConfig } : {}),
      ...(rotation.restart ? { PLUXY_APP_TEST_ROTATION_RESTART: "1" } : {}),
      ...(suite
        ? {
            PLUXY_APP_TEST_WINDOW_BIN: bin,
            PLUXY_APP_TEST_PROBE: probe,
            PLUXY_APP_TEST_IDENTIFIER: artifact.identifier,
          }
        : {}),
      ...(earlyExit ? { PLUXY_APP_TEST_EARLY_EXIT: "1" } : {}),
    };
    const trustReady = join(root, "trust-ready");
    if (core) {
      // No roots at first (the profile above has none): the first install must be refused for
      // that. The run's root joins the profile once it has been (core_no_roots).
      env.PLUXY_APP_TEST_CORE_SOURCE = core.index;
      env.PLUXY_APP_TEST_CORE_CUSTOM = core.custom;
      env.PLUXY_APP_TEST_TRUST_READY = trustReady;
    }
    // The session script starts and checks the desktop services, then runs the app.
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      probe,
      appImage,
      randomUUID(),
      suite
        ? join(
            import.meta.dir,
            suite === "access" ? "access-suite.ts" : suite === "privacy" ? "privacy.ts" : "window.ts",
          )
        : bin,
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
    const order: string[] = []; // the app's steps in the order it reported them
    const reached: string[] = [];
    const assets: { label: string; path: string; status: number }[] = [];
    let before = 0;
    let after = -1;
    const pid = proc.pid ?? 0;
    sessions.add(pid);
    let ready = false; // session.ts said its setup is done and the app starts
    let attempts = 0; // the app's recovery attempts
    if (!proc.stderr || !proc.stdout) throw new Error("the app's output is not piped");
    const log: string[] = []; // the app's last lines, shown when the run fails
    const setup: string[] = []; // session.ts's report of what failed at setup
    const output: string[] = []; // every line, for the rotated-key audit once the run ends
    let suitePassed = false;
    const onLine = (line: string) => {
      output.push(line);
      if (key && [key, wrongKey].some((canary) => line.includes(canary))) {
        problems.push("management key appeared on stdout/stderr");
        return;
      }
      if (canaries.some((canary) => line.includes(canary))) {
        problems.push(
          reader
            ? "the session reader check's canary appeared on stdout/stderr"
            : "the vault or spawn check's key or a canary appeared on stdout/stderr",
        );
        return;
      }
      // window.ts's, privacy.ts's and access-suite.ts's checks, each shown.
      const driver = /^(window|privacy|access): /.exec(line)?.[1];
      if (driver) {
        console.log(`  ${mode} | ${line}`);
        if (line.startsWith(`${driver}: suite passed`)) suitePassed = true;
        return;
      }
      // The session's service checks are always shown: they say what this run stood on.
      if (line.startsWith("native-recovery: ")) {
        console.log(`  ${mode} | ${line}`);
        return;
      }
      if (line.startsWith("native-prereq: ")) {
        console.log(`  ${label} | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      if (line.startsWith("native-test: recovery-attempt ")) attempts++;
      const m = /^native-test: (\w+) (.*)$/.exec(line);
      if (!m) {
        if (process.env.PLUXY_APP_TEST_VERBOSE) console.error(`  ${label} | ${line}`);
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
      if (step === "asset") {
        assets.push(value);
        return;
      }
      steps.set(step, value);
      order.push(step);
      if (step === "core_no_roots" && core) {
        writeFileSync(
          join(dirs.home, PROFILE_FILE),
          JSON.stringify({ roots: [core.root], updater: { kind: "test", public_key: updaterKey } }),
          { mode: 0o600 },
        );
        writeFileSync(trustReady, "");
      }
      if (step.startsWith("core_")) console.log(`  ${label} | ${step} ${json}`);
      if (step === "confirmations")
        console.log(`  ${label} | native confirmations: ${JSON.stringify(value)}`);
      if (step === "frame") before = webProcesses(pid);
      if (step === "closed") {
        // Give the web processes a moment to exit, then let the app quit.
        setTimeout(() => {
          after = webProcesses(pid);
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
    const rotated = existsSync(rotatedFile)
      ? readFileSync(rotatedFile, "utf8")
          .split("\n")
          .filter((k) => k.length > 0)
      : [];
    check(
      !output.some((line) => rotated.some((k) => line.includes(k))),
      "a management key from the rotation check appeared on stdout/stderr",
    );
    if (suite) {
      check(suitePassed, `the ${suite} checks failed`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode}: ${suite} ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }
    check(!steps.has("failed"), `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`);
    if (earlyExit) {
      // Without this the run would not test the early exit: the callback could have found the state.
      const held = steps.get("early_exit_held");
      check(
        held?.callback_ran === true,
        `the core's exit callback did not run first: ${JSON.stringify(held)}`,
      );
      const end = steps.get("early_exit") as
        | { view?: { core?: { state?: string; pid?: number } } }
        | undefined;
      const core = end?.view?.core;
      check(
        core?.state === "running" && core.pid,
        `a core that exited before the app state existed was not recovered: ${JSON.stringify(end)}`,
      );
      check(attempts === 1, `${attempts} recovery attempts, want 1`);
      await Bun.sleep(1000);
      // Only a recovered core can outlive the app; its absence is the check above.
      check(!core?.pid || !alive(core.pid), `the recovered core ${core?.pid} still runs after the app quit`);
      if (problems.length > 0)
        console.log(`${mode} early exit: the app's last lines:\n  ${log.join("\n  ")}`);
      else
        console.log(
          `  ${mode} | native-recovery: a core that exited before the app state existed was recovered`,
        );
      return problems;
    }

    if (rotation.restart) {
      const restarted = steps.get("rotation_restart");
      check(
        restarted?.signed_in === true && restarted?.status === 200 && Number(restarted?.refused) > 0,
        `the restart after rotation did not sign in on the kept key: ${JSON.stringify(restarted)}`,
      );
      console.log(`  ${mode} | after restart: ${JSON.stringify(restarted ?? {})}`);
      console.log(`${mode}: restart ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (program) {
      const p = steps.get("program") as
        | { passed?: boolean; program?: string; quit_pids?: number[] }
        | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(p?.passed === true, `the program host check did not complete: ${failure}`);
      // The swap check runs on from where the program check left (swap_native.rs).
      const w = steps.get("swap") as typeof p;
      check(w?.passed === true, `the swap check did not complete: ${failure}`);
      // After the app quit, neither tool the checks left running nor its child runs.
      await Bun.sleep(1000);
      for (const [what, r] of [
        ["program", p],
        ["swap", w],
      ] as const) {
        const left = (r?.quit_pids ?? []).filter((pid) => runs(pid, r?.program ?? ""));
        check(
          r?.quit_pids?.length === 2,
          `the ${what} check named no processes to watch: ${JSON.stringify(r)}`,
        );
        check(left.length === 0, `the tool's processes ${left.join(", ")} still run after the app quit`);
        for (const pid of left) killTree(pid);
      }
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} program ${JSON.stringify(p ?? {})}`);
      console.log(`${mode} swap ${JSON.stringify(w ?? {})}`);
      console.log(`${mode}: program ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (routing) {
      const r = steps.get("routing") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the routing check did not complete: ${failure}`);
      const j = steps.get("jobs") as { passed?: boolean } | undefined;
      check(j?.passed === true, `the jobs check did not complete: ${failure}`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} routing ${JSON.stringify(r ?? {})}`);
      console.log(`${mode} jobs ${JSON.stringify(j ?? {})}`);
      console.log(`${mode}: routing ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (spawnDir) {
      const r = steps.get("spawn") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the spawn check did not complete: ${failure}`);
      // After the app quit: its detached agent runs on, its managed runtimes do not.
      await Bun.sleep(1000);
      const known = readKnown(spawnDir);
      if (r?.passed === true) {
        check(known.detached.length > 0, "the spawn check named no detached agent to watch");
        check(known.managed.length > 0, "the spawn check named no managed runtime to watch");
        for (const k of known.detached)
          check(owned(k), `the detached agent ${k.pid} did not outlive the app`);
        for (const k of known.managed)
          check(!owned(k), `the managed runtime's process ${k.pid} still runs after the app quit`);
      }
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} spawn ${JSON.stringify(r ?? {})}`);
      console.log(`${mode}: spawn ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (launcherDir) {
      const r = steps.get("launcher") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the launcher check did not complete: ${failure}`);
      // After the app quit: the agents it launched run on.
      await Bun.sleep(1000);
      const known = readKnown(launcherDir);
      if (r?.passed === true) {
        check(known.detached.length > 0, "the launcher check named no launched agent to watch");
        for (const k of known.detached)
          check(owned(k), `the launched agent ${k.pid} did not outlive the app`);
      }
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} launcher ${JSON.stringify(r ?? {})}`);
      console.log(`${mode}: launcher ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (usage) {
      const r = steps.get("usage") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the usage hub check did not complete: ${failure}`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} usage ${JSON.stringify(r ?? {})}`);
      console.log(`${mode}: usage ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (reader) {
      const r = steps.get("sessions") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the session reader check did not complete: ${failure}`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} sessions ${JSON.stringify(r ?? {})}`);
      console.log(`${mode}: sessions ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (vault) {
      const r = steps.get("vault") as { passed?: boolean } | undefined;
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(r?.passed === true, `the vault check did not complete: ${failure}`);
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} vault ${JSON.stringify(r ?? {})}`);
      console.log(`${mode}: vault ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    // The updater gate, reported before any other step: it read the compiled marker, identifier
    // and product name, and with the same profile passes only the test package.
    const checkGate = () => {
      const gate = steps.get("updater_gate") as
        | { passed?: boolean; marker?: boolean; identifier?: string; product?: string; error?: string }
        | undefined;
      console.log(`${label} updater_gate ${JSON.stringify(gate)}`);
      check(order[0] === "updater_gate", `the app's first report is ${order[0]}, not its updater gate`);
      check(
        gate?.marker === true,
        `the updater gate did not find the compiled test marker: ${JSON.stringify(gate)}`,
      );
      if (artifact.gate === "passes") {
        check(
          gate?.passed === true && gate.identifier === TEST_ID && gate.product === TEST_PRODUCT,
          `the test package's updater gate did not pass as ${TEST_ID}/${TEST_PRODUCT}: ${JSON.stringify(gate)}`,
        );
      } else {
        check(
          gate?.passed === false &&
            gate.identifier === identifier &&
            (artifact.refusal ?? /identifier/).test(gate.error ?? ""),
          `the ${identifier} app's updater gate was not refused as ${artifact.refusal ?? "for its identifier"}: ${JSON.stringify(gate)}`,
        );
      }
    };

    if (admission) {
      checkGate();
      const a = steps.get("admission");
      check(a?.passed === true, "the native admission check did not complete");
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode} admission ${JSON.stringify(a ?? {})}`);
      console.log(`${mode}: admission ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (core) {
      checkGate();
      for (const [step, expected] of CORE_STEPS) {
        const value = steps.get(step);
        check(
          value && Object.entries(expected).every(([key, want]) => value[key] === want),
          `${step} evidence missing or wrong: ${JSON.stringify(value)} (want ${JSON.stringify(expected)})`,
        );
      }
      const installed = (step: string) => JSON.stringify(steps.get(step)?.installed);
      check(
        installed("core_no_roots") === "[]",
        `installed after the refused install: ${installed("core_no_roots")}`,
      );
      check(
        installed("core_bad_signature") === '["1.0.0"]',
        `installed after the bad signature: ${installed("core_bad_signature")}`,
      );
      check(
        installed("core_bad_start") === '["1.0.0","2.0.0"]',
        `installed after the bad start: ${installed("core_bad_start")}`,
      );
      check(
        /trust not configured/.test(String(steps.get("core_no_roots")?.detail)),
        "the no-roots refusal does not say why",
      );
      check(
        /signature/.test(String(steps.get("core_bad_signature")?.detail)),
        "the bad-signature refusal does not say why",
      );
      check(
        /not installed/.test(String(steps.get("core_rollback")?.unknown_detail)),
        "the unknown rollback refusal does not say why",
      );
      check(
        /bad candidate broke its config/.test(String(steps.get("core_bad_start")?.detail)),
        "the bad start's reason is missing",
      );
      check(
        steps.get("core_adopt")?.pending === core.custom,
        `the custom program is not pending adoption: ${JSON.stringify(steps.get("core_adopt"))}`,
      );
      check(
        /^custom-[0-9a-f]{64}$/.test(String(steps.get("core_adopt")?.version)),
        "the adopted core has no app-owned version",
      );
      if (problems.length > 0) console.log(`${mode}: the app's last lines:\n  ${log.join("\n  ")}`);
      console.log(`${mode}: core steps ${problems.length === 0 ? "ok" : "FAILED"}`);
      return problems;
    }

    if (startup) {
      check(
        log.some((l) => l.startsWith("startup preferences passed:")),
        "startup suite gave no success report",
      );
      console.log(`${mode}: startup ${problems.length === 0 ? "ok" : "FAILED"}`);
      for (const line of log) console.log(line);
      return problems;
    }

    checkGate();
    check(
      steps.get("recovery")?.ok === true,
      `tray/recovery check: ${JSON.stringify(steps.get("recovery"))}`,
    );

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
      console.log(`  ${label} | native key entry: ${JSON.stringify(entry ?? {})}`);
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
    // The page webview keeps exactly its area after the window shrinks, at the top and partly
    // scrolled (native_test.rs shrunk_page; its report says what it saw).
    const allocation = steps.get("page_allocation") as
      | { ok?: boolean; top?: { ok?: boolean }; scrolled?: { ok?: boolean } }
      | undefined;
    check(
      allocation?.ok === true && allocation.top?.ok === true && allocation.scrolled?.ok === true,
      `the page webview after the window shrank: ${JSON.stringify(allocation)}`,
    );
    console.log(`${mode} page_allocation ${JSON.stringify(allocation)}`);
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
      console.log(`${label} ${step} ${JSON.stringify(value)}`);
    }

    const ingress = steps.get("ingress");
    check(ingress?.control === true, "the native ingress check did not finish");
    if (ingress) console.log(`${label} native ingress ${JSON.stringify(ingress)}`);

    check(steps.get("confirmations")?.passed === true, "native confirmation checks did not complete");

    checkRelease(steps.get("release"), feed, browserLog, check);
    console.log(`${mode} release ${JSON.stringify(steps.get("release"))}`);
    const rotationStep = steps.get("rotation");
    const managed = mode === "managed";
    const wanted = {
      refused: true,
      control: true,
      cancelled: true,
      rotated: true,
      old_refused: true,
      new_works: true,
      kept: managed ? "core" : "device",
      session: !managed,
      recovery: !managed,
      webviews_clean: true,
      page_audited: !managed,
      outlived_window: !managed,
      replaced: !managed,
      connection_shown: !managed,
      no_launch_notice: true,
    };
    check(
      rotationStep && Object.entries(wanted).every(([k, want]) => rotationStep[k] === want),
      `native rotation evidence missing or wrong: ${JSON.stringify(rotationStep)}`,
    );
    // The key the app started with and each rotated one (two in managed mode, four otherwise).
    check(rotated.length === (managed ? 2 : 4), `the rotation check recorded ${rotated.length} keys`);
    console.log(`  ${mode} | native rotation: ${JSON.stringify(rotationStep ?? {})}`);
    // This device's catalog: the driver's own checks passed, and the asset scheme served the
    // swapped-in release to its page webview and refused that webview the replaced one's files.
    if (device) {
      const d = steps.get("device") as
        | { passed?: boolean; v2_label?: string; digests?: Record<string, string>; normal_build?: string }
        | undefined;
      // Without its line, the app's failed step says where the check stopped and why.
      const failure = (steps.get("failed") as { error?: string } | undefined)?.error ?? "no report";
      check(d?.passed === true, `the device catalog check did not complete: ${failure}`);
      // This normal build refused the curated registry's signed native tool despite the HOME's
      // valid test trust profile, for want of roots.
      check(
        d?.normal_build?.includes("not configured"),
        `the normal build did not refuse the curated native tool: ${JSON.stringify(d?.normal_build)}`,
      );
      const dg = d?.digests ?? {};
      const served = (label: string | undefined, digest: string | undefined, status: number) =>
        assets.some((a) => a.label === label && a.path.includes(`/${digest}/`) && a.status === status);
      check(
        dg.notes2 && served(d?.v2_label, dg.notes2, 200),
        `the asset scheme did not serve 2.0.0 to ${d?.v2_label}`,
      );
      check(
        dg.notes1 && served(d?.v2_label, dg.notes1, 404),
        `the asset scheme did not refuse 1.0.0 to ${d?.v2_label}`,
      );
      for (const k of ["spoof", "pluxy_local", "notes3"])
        check(
          dg[k] && !assets.some((a) => a.path.includes(`/${dg[k]}/`) && a.status === 200),
          `the asset scheme served refused package ${k}`,
        );
      console.log(
        `${mode} device ${JSON.stringify(d ?? { failed: failure })} (${assets.length} asset requests)`,
      );
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
      // The tray checks killed that core; the one they left running must not outlive the app.
      const last = (steps.get("recovery") as { core_pid?: number } | undefined)?.core_pid;
      check(last, "the tray checks left no owned core running before quit");
      await Bun.sleep(1000);
      check(last && !alive(last), `the app's core ${last} still runs after it quit`);
    }
    if (problems.length > 0) console.log(`${label}: the app's last lines:\n  ${log.join("\n  ")}`);
    console.log(
      `${label}: ${problems.length === 0 ? "ok" : "FAILED"} (web processes ${before} open, ${after} after close; ` +
        `${reached.length} commands reached, all from main: ${reached.every((w) => w === "main")})`,
    );
  } finally {
    clearInterval(offlineWatch);
    feed?.stop();
    // Every process the spawn check named, still running as itself: nothing else would end them.
    if (spawnCheck) cleanKnown(join(root, "spawn"));
    if (launcherCheck) cleanKnown(join(root, "launcher"));
    // A folder left behind is a problem of its own; it must not hide this run's report.
    if (!rotation.root)
      await removeRun(root).catch((e) => problems.push(`the run folder ${root} was not removed: ${e}`));
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

/** Lets this user remove the device's package store the app left in its run folder, after the
 * app has quit: the store keeps its installed folders read-only, so they cannot be emptied as
 * they are. Only folders under the app's `device-packages` change. They are path-based changes,
 * safe here because the app made that tree, holds no links or mounts in it (archives with links
 * are refused), and no longer runs. */
function makeRemovable(root: string) {
  const walk = (dir: string) => {
    let entries: string[];
    try {
      const st = lstatSync(dir);
      if (!st.isDirectory()) return;
      if ((st.mode & 0o700) !== 0o700) chmodSync(dir, 0o700);
      entries = readdirSync(dir);
    } catch {
      return; // gone: rmSync reports what it cannot remove
    }
    for (const e of entries) walk(join(dir, e));
  };
  walk(join(root, "data", identifier, "device-packages"));
}

/** Removes a run's folder. A desktop service the run's bus started (such as the document portal,
 * which mounts itself in the runtime folder) can outlive the app for a moment. */
async function removeRun(root: string) {
  for (let i = 0; ; i++) {
    try {
      makeRemovable(root);
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

const targetDir = resolvePath(tauri, process.env.CARGO_TARGET_DIR ?? "target");
/** The probe the session uses to check the desktop services. */
const probe = join(targetDir, "debug", "examples", "session_probe");
/** The disposable summary tool the program host check packs into a signed package. */
const programFixture = join(targetDir, "debug", "examples", "program_fixture");
/** The app tools' workspace, and the Codex session reader the session reader check packs. */
const appTools = join(next, "app-tools");
const appToolsTarget = join(targetDir, "app-tools");
const sessionsReader = join(appToolsTarget, "debug", "codex-sessions");
/** The execution program (next/app-tools/execution) and its manifest, for the launcher check. */
const executionManifest = join(appTools, "execution", "pluxy-extension.json");
const executionProgram = join(appToolsTarget, "debug", "execution");
let appImage = ""; // the smoke AppImage the sessions run
const problems: string[] = [];

/** This run's own temporary folders (each app run removes its own), removed when it ends,
 * however it ends. */
const temporary: string[] = [];
function tempDir(prefix: string): string {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  temporary.push(dir);
  return dir;
}
/** Removes each folder, keeping those it could not remove for a later try; says whether all went. */
function removeTemporary(): boolean {
  const failed: string[] = [];
  for (const dir of temporary.splice(0)) {
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch (e) {
      failed.push(dir);
      console.error(`could not remove ${dir}: ${(e as Error).message}`);
    }
  }
  temporary.push(...failed);
  return failed.length === 0;
}

let exitCode = 1;
try {
  exitCode = await main();
} finally {
  if (!removeTemporary()) exitCode = 1;
}
process.exit(exitCode);

async function main(): Promise<number> {
  run("bun", ["run", "build"], app);

  // The test package first: its Tauri build renames the binary it makes in target/debug, and the
  // dev build below puts the dev binary back.
  const packageDir = tempDir("pluxy-app-native-package-");
  let testPackage: TestPackage | undefined;
  try {
    runShown("bun", ["run", "package:test", packageDir], app);
    const made: TestPackage = JSON.parse(readFileSync(join(packageDir, "test-package.json"), "utf8"));
    const name = basename(made.appimage);
    const header = appImageHeader(made.appimage);
    console.log(
      `package: ${name}: first bytes ${header.hex}; ELF ${header.elf}, AppImage type ${header.type ?? "none"}`,
    );
    if (!/-test_[^/]*\.AppImage$/.test(name))
      problems.push(`the test package's file name ${name} has no -test`);
    if (!header.elf || header.type !== 2) problems.push(`the test package ${name} is not a type-2 AppImage`);
    if (!made.updater_pubkey) problems.push("the test package names no updater public key");
    testPackage = made;
  } catch (e) {
    problems.push(`the test package was not built: ${(e as Error).message}`);
  }

  // The dev app, the probe the session uses to check the desktop services
  // (examples/session_probe.rs) and the program host check's summary tool.
  run(
    "cargo",
    [
      "build",
      "--locked",
      "--features",
      "native-test",
      "--bin",
      "pluxy-app",
      "--example",
      "session_probe",
      "--example",
      "program_fixture",
    ],
    tauri,
  );
  // The execution program the launcher check installs, from its own workspace.
  if (existsSync(executionManifest))
    run("cargo", ["build", "--locked", "-p", "execution", "--target-dir", appToolsTarget], appTools);
  // The build without artifact-test-trust, kept aside before the build with it replaces it. The
  // device check needs the one (a normal build refuses the curated native tool), the admission
  // session and the updater gate's identifier check the other (its features are the test
  // package's, so the two differ only in the identifier and product name). The probe is built
  // again with it, since installing a core release (install-core) checks it with the test roots.
  const binDir = tempDir("pluxy-app-native-bin-");
  const bin = join(binDir, "pluxy-app");
  copyFileSync(join(targetDir, "debug", "pluxy-app"), bin);
  run(
    "cargo",
    [
      "build",
      "--locked",
      "--features",
      "native-test,artifact-test-trust",
      "--bin",
      "pluxy-app",
      "--example",
      "session_probe",
    ],
    tauri,
  );
  const trustBin = join(targetDir, "debug", "pluxy-app");
  // The Codex session reader, from the app tools' workspace, when it is there; and the usage hub
  // and its fixture parser, each when its folder is there.
  if (existsSync(join(appTools, "Cargo.toml"))) {
    run("cargo", ["build", "--locked", "-p", "codex-sessions", "--target-dir", appToolsTarget], appTools);
    const usageTools = ["agent-usage", "usage-fixture"].filter((t) =>
      existsSync(join(appTools, t, "Cargo.toml")),
    );
    if (usageTools.length > 0) {
      run(
        "cargo",
        ["build", "--locked", ...usageTools.flatMap((t) => ["-p", t]), "--target-dir", appToolsTarget],
        appTools,
      );
    }
  }
  const appImageDir = tempDir("pluxy-app-native-appimage-");
  // The same test trust profile in every run; without a test package, a key no build carries.
  const updaterKey = testPackage?.updater_pubkey ?? "no-test-package";
  let setupFailure: string | undefined;
  // Without artifact-test-trust the app has no updater trust at all, which is what its gate says.
  const dev = (mode: Mode): Artifact => ({
    label: mode,
    bin,
    identifier,
    gate: "refuses",
    refusal: /updater trust not configured/,
  });
  const trusted: Artifact = { label: "managed", bin: trustBin, identifier, gate: "refuses" };

  try {
    appImage = await buildSmokeAppImage(appImageDir);
    const prepared = mkdtempSync(join(tmpdir(), "pluxy-app-native-core-"));
    try {
      const { core } = JSON.parse(
        run("go", ["run", "./e2e/panelcore", "-probe", "-prepare", prepared], next),
      );
      const releases = join(prepared, "releases");
      mkdirSync(releases);
      const source = coreSource(releases, prepared);
      // The core steps, on the build with artifact-test-trust, in a session of their own. A core
      // the app did not start must outlive it, its config and program untouched.
      const sentinel = await startSentinel(join(prepared, "sentinel"), join(releases, "core-1.0.0"));
      try {
        const c = await runApp(
          "managed",
          trusted,
          updaterKey,
          { mode: "managed", core_source: source.index },
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          false,
          source,
        );
        const status = await managementStatus(sentinel.base, sentinel.key);
        const kept = readFileSync(sentinel.config).equals(sentinel.before);
        console.log(
          `managed sentinel ${JSON.stringify({ alive: alive(sentinel.proc.pid ?? 0), status, config_kept: kept })}`,
        );
        if (!alive(sentinel.proc.pid ?? 0) || status !== 200)
          c.push("the sentinel core did not survive the app");
        if (!kept) c.push("the sentinel core's config changed");
        if (statSync(join(prepared, "sentinel", "pluxy")).ino !== sentinel.exe)
          c.push("the sentinel core's program changed");
        problems.push(...c.map((x) => `managed, core steps: ${x}`));
      } finally {
        sentinel.proc.kill("SIGTERM");
        await new Promise((r) => sentinel.proc.once("exit", r));
      }
      // The other runs need a core in the app's own store. The startup runs adopt the core_path
      // program before each run, as the window's Adopt would. The rest use the bundled test
      // packages beside the core's program, which an adopted program is copied without, so they
      // start with signed release 2.0.0 (panelcore's packages) installed.
      const adopted = { mode: "managed", core_path: core };
      const managed = { mode: "managed", core_source: source.index };
      const release: Release = { from: source, version: "2.0.0" };
      const p = await runApp(
        "managed",
        dev("managed"),
        updaterKey,
        managed,
        undefined,
        false,
        false,
        undefined,
        undefined,
        {},
        false,
        false,
        false,
        false,
        false,
        undefined,
        release,
      );
      p.push(...(await runApp("managed", dev("managed"), updaterKey, adopted, undefined, true)));
      p.push(...(await runApp("managed", dev("managed"), updaterKey, adopted, undefined, true, true)));
      // The signed native-tier admission check, then the program host check and the routing
      // check, on the build with artifact-test-trust.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          true,
          false,
          false,
          false,
          false,
          undefined,
          release,
        )),
      );
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          true,
          false,
          false,
          false,
          undefined,
          release,
        )),
      );
      // The cross-package routing check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          true,
          false,
          undefined,
          release,
        )),
      );
      // The vault check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          true,
          undefined,
          release,
        )),
      );
      // The spawn check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          false,
          undefined,
          release,
          true,
        )),
      );
      // The Codex session reader check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          false,
          undefined,
          release,
          false,
          true,
        )),
      );
      // The usage hub check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          false,
          undefined,
          release,
          false,
          false,
          true,
        )),
      );
      // The launcher check, on the same build.
      p.push(
        ...(await runApp(
          "managed",
          trusted,
          updaterKey,
          managed,
          undefined,
          false,
          false,
          undefined,
          undefined,
          {},
          false,
          false,
          false,
          false,
          false,
          undefined,
          release,
          false,
          false,
          false,
          true,
        )),
      );
      for (const suite of ["window", "window-no-tray", "privacy", "access"] as const)
        p.push(
          ...(await runApp(
            "managed",
            dev("managed"),
            updaterKey,
            managed,
            undefined,
            false,
            suite === "window-no-tray",
            undefined,
            suite,
            {},
            false,
            false,
            false,
            false,
            false,
            undefined,
            release,
          )),
        );
      problems.push(...p.map((x) => `managed: ${x}`));
      // The first start runs a stand-in that says where it listens, answers the app's readiness
      // check (conn.rs ready), then exits; the next start, recovery's, runs the real core. The
      // app runs the adopted copy of this script from an open file, so its mark is not beside $0.
      const early = join(prepared, "early-exit.sh");
      const standIn = join(prepared, "early-exit.ts");
      writeFileSync(
        standIn,
        `const s = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch() { setTimeout(() => process.exit(7), 500); return Response.json({}); } });\nconsole.error(\`pluxy: listening on http://127.0.0.1:\${s.port}\`);\n`,
      );
      writeFileSync(
        early,
        `#!/bin/sh\nif [ ! -e '${early}.ran' ]; then\n  : > '${early}.ran'\n  exec '${process.execPath}' '${standIn}'\nfi\nexec '${core}' "$@"\n`,
        { mode: 0o755 },
      );
      const e = await runApp(
        "managed",
        dev("managed"),
        updaterKey,
        { mode: "managed", core_path: early },
        undefined,
        false,
        false,
        undefined,
        undefined,
        {},
        false,
        false,
        true,
      );
      problems.push(...e.map((x) => `managed, early exit: ${x}`));

      // Two launches of the app on one profile: one app, one core, the first one's window raised.
      const inst = await runInstances({
        bin,
        probe,
        appImage,
        core,
        identifier,
        timeoutMs: TIMEOUT,
        sessions,
        stopSession,
        removeRun,
      });
      if (inst.setup !== undefined) throw new SetupError(inst.setup);
      problems.push(...inst.problems.map((x) => `instances: ${x}`));
    } finally {
      rmSync(prepared, { recursive: true, force: true });
    }

    for (const mode of ["local", "remote"] as const) {
      const c = await startCore(mode === "remote");
      const kept = mkdtempSync(join(tmpdir(), `pluxy-app-native-${mode}-`));
      try {
        const conn = { mode, url: c.base, key: c.key, ...(c.ca ? { ca_file: c.ca } : {}) };
        const rotation = { coreConfig: c.config, root: kept };
        const p = await runApp(
          mode,
          dev(mode),
          updaterKey,
          conn,
          c.base,
          false,
          false,
          c.key,
          undefined,
          rotation,
        );
        // Rotated and kept: the app starts again on the same folders, signed in on the new key.
        if (mode === "local")
          p.push(
            ...(await runApp(mode, dev(mode), updaterKey, conn, c.base, false, false, undefined, undefined, {
              ...rotation,
              restart: true,
            })),
          );
        const current = { ...conn, key: managementKey(c.config) };
        p.push(...(await runApp(mode, dev(mode), updaterKey, current, c.base, true)));
        // The app only connected: its quitting leaves this core running.
        if ((await managementStatus(c.base, current.key, c.ca)) !== 200)
          p.push("the core stopped when the app quit");
        problems.push(...p.map((x) => `${mode}: ${x}`));
      } finally {
        await removeRun(kept);
        c.proc.stdin?.end();
        await new Promise((r) => c.proc.once("exit", r));
      }
    }

    // The packaged app: the AppImage itself, against a test core of the runner's.
    if (testPackage) {
      const packaged: Artifact = {
        label: "packaged",
        bin: testPackage.appimage,
        identifier: TEST_ID,
        gate: "passes",
      };
      const c = await startCore(false);
      try {
        const conn = { mode: "local", url: c.base, key: c.key };
        // The same checks as the dev app's local run, including the key rotation, which needs
        // the core's config file; the key it rotated to is the one that must still work.
        const p = await runApp("local", packaged, updaterKey, conn, c.base, false, false, c.key, undefined, {
          coreConfig: c.config,
        });
        if ((await managementStatus(c.base, managementKey(c.config))) !== 200)
          p.push("the core stopped when the app quit");
        problems.push(...p.map((x) => `packaged: ${x}`));
      } finally {
        c.proc.stdin?.end();
        await new Promise((r) => c.proc.once("exit", r));
      }
    } else {
      problems.push("packaged: there is no test package to run");
    }
  } catch (e) {
    if (!(e instanceof SetupError)) throw e;
    setupFailure = e.message;
  }

  if (setupFailure !== undefined) {
    console.error(`\nnative test setup failed:\n${setupFailure}`);
    return 1;
  }

  // The release gate (package.ts release): it builds the release app and checks it with every
  // AppImage given, unpacking each, never running it. Each refusal has a passing case on the same
  // route: an AppImage packed the same way around a clean binary.
  const releaseDir = tempDir("pluxy-app-native-release-");
  {
    const releaseGate = (what: string, images: string[], want: "valid" | RegExp) => {
      const r = spawnSync("bun", ["run", "package:release", ...images], {
        cwd: app,
        encoding: "utf8",
        stdio: ["ignore", "pipe", "inherit"],
      });
      const verdict = /^release-package: .*$/m.exec(r.stdout ?? "")?.[0] ?? "no verdict";
      console.log(`release gate, ${what}: exit ${r.status}: ${verdict}`);
      const ok =
        want === "valid"
          ? r.status === 0 && verdict.startsWith("release-package: valid")
          : r.status === 1 && verdict.startsWith("release-package: refused") && want.test(verdict);
      if (!ok) problems.push(`release gate, ${what}: exit ${r.status}: ${verdict}`);
    };
    releaseGate("a clean release build", [], "valid");
    const releaseBin = join(targetDir, "release", "pluxy-app");
    try {
      const clean = await packAppImage(join(releaseDir, "clean"), "pluxy.AppImage", {
        "usr/bin/pluxy-app": releaseBin,
      });
      releaseGate("a clean AppImage", [clean], "valid");
    } catch (e) {
      problems.push(`release gate: no clean AppImage: ${(e as Error).message}`);
    }
    const marked = await packAppImage(join(releaseDir, "marked"), "pluxy.AppImage", {
      "usr/bin/pluxy-app": bin,
    });
    releaseGate(
      "an AppImage holding the test-feature dev binary",
      [marked],
      /pluxy\.AppImage!usr\/bin\/pluxy-app: test build marker\/identity in artifact/,
    );
    if (testPackage) {
      const renamed = join(releaseDir, "candidate.AppImage");
      copyFileSync(testPackage.appimage, renamed);
      releaseGate("the test package, renamed", [renamed], /candidate\.AppImage!/);
      releaseGate("the test package", [testPackage.appimage], /test\/private release input or artifact path/);
    } else {
      problems.push("release gate: there is no test package to refuse");
    }
  }

  if (problems.length > 0) {
    console.error(`\n${problems.length} problems:\n- ${problems.join("\n- ")}`);
    return 1;
  }
  console.log(
    "native test passed in all three modes, the single-instance scenario, as the packaged app, and at the release gate",
  );
  return 0;
}
