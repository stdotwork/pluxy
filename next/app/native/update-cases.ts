// The signed application update scenario's cases, the same on every system: the Linux native
// session (update-session.ts, which update.ts runs for run.ts's `update` session) and the Windows
// and macOS one (platform.ts's `app update` session) both serve the fake https feed made here, launch
// the test package on one profile (update_native.rs drives the app from inside), and check what
// the app reported against what they saw themselves with the checks here:
//
// - refusals before any download: no updater trust (`no-roots`, the first launch, which also sets
//   the feed through its native confirmation) and the crafted installs of each system (REFUSALS);
// - the update route, which restarts the app four times: a lower feed, a bad signature, a signed
//   version that is not the announced one and a cancelled download change nothing; the plugin's
//   own command is not allowed to the window; a new version that does not accept itself is undone;
//   v1→v2 for real, with a third launch while the new version holds its acceptance open (it shows
//   the running version's window and starts no app of its own); a rollback refused for a version
//   never installed and for a changed kept copy, cancelled in its native confirmation, then
//   confirmed back to v1;
// - every step the system's cases print is there (expectedSteps), and stand-ins for what installs
//   with privileges never ran.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { delimiter, dirname, join } from "node:path";
import { createInterface } from "node:readline";
import { certificates } from "./feed";
import { keyIdOf, signUpdate, updaterKey } from "./minisign";

export type Step = Record<string, unknown>;
export type Check = (ok: unknown, what: string) => void;
export type Say = (line: string) => void;
type System = "linux" | "win32" | "darwin";

/** The version the test package is built at; the update's comes from test-package.json. */
export const V1 = "0.1.0";
export const LAUNCH_MS = 240_000;
export const CHAIN_MS = 600_000;
/** How long the third launch, in the update's acceptance gap, may take to hand over and exit. */
const THIRD_MS = 20_000;

export const sha256 = (b: Uint8Array) => createHash("sha256").update(b).digest("hex");

/** An app bundle's digest, `pluxy-tree-v1` as the updater computes it (updater/app.rs): SHA-256 of
 * `pluxy-tree-v1\n` and one line per entry below the bundle folder, sorted by relative path as
 * bytes: `d <path>` for a folder, `f <x|-> <size> <sha256> <path>` for a regular file (`x` when any
 * execute bit is set). Anything else (a link) has no digest. */
export function treeDigest(bundle: string): string {
  const lines: [Buffer, string][] = [];
  const walk = (rel: string) => {
    for (const name of readdirSync(rel ? join(bundle, rel) : bundle)) {
      const r = rel ? `${rel}/${name}` : name;
      const st = lstatSync(join(bundle, r));
      if (st.isDirectory()) {
        lines.push([Buffer.from(r), `d ${r}\n`]);
        walk(r);
      } else if (st.isFile()) {
        const x = st.mode & 0o111 ? "x" : "-";
        lines.push([Buffer.from(r), `f ${x} ${st.size} ${sha256(readFileSync(join(bundle, r)))} ${r}\n`]);
      } else {
        throw new Error(`${join(bundle, r)} is neither a folder nor a regular file`);
      }
    }
  };
  walk("");
  lines.sort((a, b) => Buffer.compare(a[0], b[0]));
  const h = createHash("sha256").update("pluxy-tree-v1\n");
  for (const [, line] of lines) h.update(line);
  return h.digest("hex");
}

/** The test package's program on Windows, a part of its program set. */
export const SET_EXE = "pluxy-app-test.exe";
/** A Windows program set's parts (updater/set.rs), sorted as the app lists a folder. */
export const SET_PARTS = ["bundled", SET_EXE, "pluxy.exe"].sort();

/** A Windows program set's digest, `pluxy-set-v1` as the updater computes it (updater/set.rs):
 * SHA-256 of `pluxy-set-v1\n` and one line per entry of the set's parts only (`bundled`, a
 * folder; `pluxy.exe` and the program, files), sorted by path from the folder as bytes: `d <path>`
 * for a folder, `f <size> <sha256> <path>` for a file. Anything else (a link) has no digest. */
export function setDigest(folder: string, exe = SET_EXE): string {
  const lines: [Buffer, string][] = [];
  const walk = (rel: string) => {
    const st = lstatSync(join(folder, rel));
    if (st.isDirectory()) {
      lines.push([Buffer.from(rel), `d ${rel}\n`]);
      for (const name of readdirSync(join(folder, rel))) walk(`${rel}/${name}`);
    } else if (st.isFile()) {
      lines.push([Buffer.from(rel), `f ${st.size} ${sha256(readFileSync(join(folder, rel)))} ${rel}\n`]);
    } else {
      throw new Error(`${join(folder, rel)} is neither a folder nor a regular file`);
    }
  };
  for (const part of ["bundled", "pluxy.exe", exe]) {
    if (lstatSync(join(folder, part)).isDirectory() !== (part === "bundled"))
      throw new Error(`${join(folder, part)} is not a part of a program set`);
    walk(part);
  }
  lines.sort((a, b) => Buffer.compare(a[0], b[0]));
  const h = createHash("sha256").update("pluxy-set-v1\n");
  for (const [, line] of lines) h.update(line);
  return h.digest("hex");
}

/** The digest an installed copy is pinned by: a Windows program set's digest, an app bundle's
 * tree digest, a file's SHA-256. */
export const pinOf = (path: string): string =>
  lstatSync(path).isDirectory()
    ? process.platform === "win32"
      ? setDigest(path)
      : treeDigest(path)
    : sha256(readFileSync(path));

/** What the folder an install leaves things in holds once nothing it made is left: the
 * installed copy's name (its folder's other entries are not the app's), or on Windows the program
 * set's parts (the installed copy is that folder) and others, the files in it that are not the
 * app's (the per-user setup's uninstall.exe). */
export const ownNames = (installed: string, others: string[] = []): string[] =>
  process.platform === "win32"
    ? [...SET_PARTS, ...others].sort()
    : [installed.split(/[\\/]/).pop() as string];

/** The crafted installs each system's updater refuses before any download, with how to update
 * instead (the reason the page and both commands give). `case` is PLUXY_APP_TEST_UPDATE_CASE; the
 * app reports it as the step `update_<case>`. */
export interface Refusal {
  label: string;
  case: string;
  reason: RegExp;
}
export const NO_ROOTS: Refusal = { label: "no roots", case: "no-roots", reason: /not configured|trust/ };
export const REFUSALS: Record<System, Refusal[]> = {
  // The program with the bundle type a .deb, an .rpm and no package gives it.
  linux: [
    { label: "deb", case: "deb", reason: /\.deb package.*package manager/s },
    { label: "rpm", case: "rpm", reason: /\.rpm package.*package manager/s },
    { label: "unknown", case: "unknown", reason: /not an AppImage|unknown/ },
  ],
  // The program with the bundle type an .msi gives it, and a folder bundle in a folder Everyone
  // may change. (A copy the per-user setup program installed is updated: the install session.)
  win32: [
    { label: "msi", case: "msi", reason: /\.msi package/ },
    { label: "shared folder", case: "shared", reason: /only you can change/ },
  ],
  // The program copied out of its app bundle, an app bundle its group may change, and the
  // installed bundle started by a path that goes through a link.
  darwin: [
    { label: "bare program", case: "bare", reason: /not in its app bundle/ },
    { label: "group-writable bundle", case: "group-writable", reason: /only you can change/ },
    { label: "linked folder", case: "linked", reason: /goes through a link.*real location/s },
  ],
};
export const stepOf = (c: Refusal) => `update_${c.case.replaceAll("-", "_")}`;

/** Every step a system's run must have seen, at least once. */
export function expectedSteps(system: System): string[] {
  return [
    stepOf(NO_ROOTS),
    ...REFUSALS[system].map(stepOf),
    ...(system === "darwin" ? ["update_open"] : []),
    ...(system === "win32" ? ["update_between_parts", "update_parts_undone"] : []),
    "update_stage",
    "update_refusals",
    "update_refused",
    "update_gap",
    "update_gap_hidden",
    "update_gap_raised",
    "update_rollback_refusals",
    "update_done",
  ];
}

// ---- stand-ins ----------------------------------------------------------------------------

export interface StandIns {
  /** The folder to put first on PATH. */
  bin: string;
  /** What ran, one line each ("" when nothing did). */
  ran(): string;
}

/** Stand-ins named names in dir/stand-ins, each logging its name and arguments and exiting 1.
 * Windows: a program (built with Go once, copied under each name, `.exe` added); elsewhere a
 * shell script. */
export function standIns(dir: string, names: string[]): StandIns {
  const bin = join(dir, "stand-ins");
  const log = join(dir, "stand-ins.log");
  mkdirSync(bin, { recursive: true, mode: 0o700 });
  if (process.platform === "win32") {
    const src = join(dir, "stand-in-src");
    mkdirSync(src, { recursive: true });
    writeFileSync(
      join(src, "main.go"),
      `package main

import (
	"os"
	"path/filepath"
	"strings"
)

func main() {
	exe, _ := os.Executable()
	f, err := os.OpenFile(filepath.Join(filepath.Dir(filepath.Dir(exe)), "stand-ins.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString(strings.Join(os.Args, " ") + "\\n")
		_ = f.Close()
	}
	os.Exit(1)
}
`,
    );
    const built = join(src, "stand-in.exe");
    const env = { ...process.env, GOFLAGS: "", GO111MODULE: "on" };
    const r = Bun.spawnSync(["go", "build", "-o", built, "main.go"], { cwd: src, env, stderr: "pipe" });
    if (r.exitCode !== 0) throw new Error(`go build of the stand-in: ${r.stderr.toString()}`);
    for (const name of names) copyFileSync(built, join(bin, `${name}.exe`));
    rmSync(src, { recursive: true, force: true });
  } else {
    for (const name of names)
      writeFileSync(join(bin, name), `#!/bin/sh\necho "${name} $*" >> '${log}'\nexit 1\n`, { mode: 0o755 });
  }
  return { bin, ran: () => (existsSync(log) ? readFileSync(log, "utf8") : "") };
}

/** env with folder first on its PATH (whatever case the system's own name has). */
export function withPathFirst(env: Record<string, string | undefined>, folder: string) {
  const out: Record<string, string> = {};
  let path = "";
  for (const [k, v] of Object.entries(env)) {
    if (v === undefined) continue;
    if (k.toUpperCase() === "PATH") path = v;
    else out[k] = v;
  }
  out.PATH = `${folder}${delimiter}${path || (process.platform === "win32" ? "" : "/usr/bin:/bin")}`;
  return out;
}

// ---- the feed -----------------------------------------------------------------------------

export interface UpdateFeed {
  /** The feed's address, for PLUXY_APP_TEST_FEED. */
  url: string;
  /** Its authority's certificate, for PLUXY_APP_TEST_FEED_CA. */
  ca: string;
  /** Requests by path. */
  requests: Map<string, number>;
  count(path: string): number;
  /** Requests for the update's artifact, the slow one too. */
  downloads(): number;
  stop(): void;
}
export const ARTIFACT = "/artifacts/v2";
export const SLOW_ARTIFACT = "/artifacts/v2-slow";

/** Serves a feed whose releases follow the word in dir/feed-variant (written by the app's check):
 * lower (nothing newer), good (version with the signed payload for target), slow (the same,
 * served slowly), badsig (signed by a key the app does not trust, with the trusted key's ID),
 * mismatch (the payload and signature announced as 0.3.0). */
export function startUpdateFeed(o: {
  dir: string;
  payload: Buffer<ArrayBuffer>;
  /** The payload's file name, as its signature's trusted comment has it. */
  file: string;
  target: string;
  version: string;
  signature: string;
  pubkey: string;
}): UpdateFeed {
  const certs = certificates(join(o.dir, "feed"));
  const requests = new Map<string, number>();
  const count = (path: string) => requests.get(path) ?? 0;
  let base = "";
  const other = updaterKey(keyIdOf(o.pubkey));
  const badSignature = signUpdate(other, o.payload, o.file, o.version);
  const app = (version: string, channel: string, update?: { url: string; signature: string }) => ({
    product: "app",
    version,
    channel,
    published: "2026-11-01T09:00:00Z",
    notes: `Release ${version}.`,
    ...(update ? { update: { [o.target]: { ...update, size: o.payload.length } } } : {}),
  });
  const feed = (variant: string) => {
    const good = { url: `${base}${ARTIFACT}`, signature: o.signature };
    const releases = {
      lower: [app("0.0.9", "stable"), app(V1, "stable")],
      good: [app(V1, "stable"), app(o.version, "stable", good)],
      slow: [app(V1, "stable"), app(o.version, "stable", { ...good, url: `${base}${SLOW_ARTIFACT}` })],
      badsig: [app(V1, "stable"), app(o.version, "stable", { ...good, signature: badSignature })],
      mismatch: [app(V1, "stable"), app("0.3.0", "stable", good)],
    }[variant];
    if (!releases) throw new Error(`no feed variant ${variant}`);
    return { format: 1, releases };
  };
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    tls: { cert: readFileSync(certs.cert, "utf8"), key: readFileSync(certs.key, "utf8") },
    fetch(req): Response {
      const path = new URL(req.url).pathname;
      requests.set(path, count(path) + 1);
      if (req.method !== "GET") return new Response("no", { status: 405 });
      if (path === "/feed.json") {
        const variantFile = join(o.dir, "feed-variant");
        const variant = existsSync(variantFile) ? readFileSync(variantFile, "utf8").trim() : "good";
        return Response.json(feed(variant));
      }
      if (path === ARTIFACT) {
        return new Response(o.payload, { headers: { "content-length": String(o.payload.length) } });
      }
      if (path === SLOW_ARTIFACT) {
        // 64 KiB every 200 ms: the check cancels it long before the end.
        let at = 0;
        let open = true;
        const body = new ReadableStream({
          async pull(c) {
            await Bun.sleep(200);
            if (!open) return;
            const end = Math.min(at + 65536, o.payload.length);
            c.enqueue(o.payload.subarray(at, end));
            at = end;
            if (at >= o.payload.length) c.close();
          },
          cancel() {
            open = false;
          },
        });
        return new Response(body, { headers: { "content-length": String(o.payload.length) } });
      }
      return new Response("not here", { status: 404 });
    },
  });
  base = `https://127.0.0.1:${server.port}`;
  return {
    url: `${base}/feed.json`,
    ca: certs.ca,
    requests,
    count,
    downloads: () => count(ARTIFACT) + count(SLOW_ARTIFACT),
    stop: () => server.stop(true),
  };
}

// ---- launches -----------------------------------------------------------------------------

/**
 * What each thread of process pid (a launch this session started) is doing, said line by line,
 * without stopping it for good: on Windows the console debugger attached noninvasively (`-pv`),
 * symbols from folder when a PDB is there; on macOS `sample`. Only names and addresses come out,
 * never memory. Nothing on Linux, whose sessions have their own report.
 */
export function stacks(pid: number, folder: string, say: Say) {
  let r: ReturnType<typeof spawnSync> | undefined;
  if (process.platform === "win32") {
    const cdb = [
      "C:\\Program Files (x86)\\Windows Kits\\10\\Debuggers\\x64\\cdb.exe",
      "C:\\Program Files\\Windows Kits\\10\\Debuggers\\x64\\cdb.exe",
    ].find((p) => existsSync(p));
    if (!cdb) {
      say("no console debugger (cdb.exe) on this runner, so no thread stacks");
      return;
    }
    r = spawnSync(cdb, ["-pv", "-p", String(pid), "-y", folder, "-c", "~*kc 40; qd"], {
      encoding: "utf8",
      timeout: 120_000,
    });
  } else if (process.platform === "darwin") {
    r = spawnSync("/usr/bin/sample", [String(pid), "3"], { encoding: "utf8", timeout: 60_000 });
  } else {
    return;
  }
  say(`the threads of process ${pid} (status ${r.status}):`);
  for (const line of `${r.stdout ?? ""}${r.stderr ?? ""}`.split("\n")) say(`  stack | ${line}`);
}

export interface Chain {
  ended: string;
  /** Every step of that name, in order. */
  step(name: string): Step[];
  /** The names of every step seen. */
  names: Set<string>;
}

/** One launch and the processes it starts (an update's new version inherits its output): their
 * `native-test:` steps, until every process holding its output has ended. onStep sees each step
 * as it comes; the launch is done once the processes and what onStep started have ended. */
export async function launchChain(o: {
  label: string;
  program: string;
  args?: string[];
  env: Record<string, string>;
  ms: number;
  check: Check;
  say: Say;
  /** child is the launch's own process, which the session started and holds. */
  onStep?: (name: string, value: Step, child: ChildProcess) => Promise<void> | void;
}): Promise<Chain> {
  const child = spawn(o.program, o.args ?? [], { env: o.env, stdio: ["ignore", "pipe", "pipe"] });
  const steps: [string, Step][] = [];
  const pending: Promise<void>[] = [];
  const tail: string[] = [];
  for (const stream of [child.stdout, child.stderr]) {
    createInterface({ input: stream }).on("line", (line) => {
      const m = /^native-test: (\S+) (.*)$/.exec(line);
      if (m) {
        const name = m[1] as string;
        let value: Step;
        try {
          value = JSON.parse(m[2] as string);
        } catch {
          value = { raw: m[2] };
        }
        steps.push([name, value]);
        // The app's own stall report (native_test.rs) says what its test thread last did.
        if (name.startsWith("update") || name === "failed" || name === "stalled")
          o.say(`${o.label} | ${line.slice(0, 1500)}`);
        if (o.onStep) {
          pending.push(
            Promise.resolve()
              .then(() => o.onStep?.(name, value, child))
              .catch((e) =>
                o.check(false, `${o.label}: handling ${name}: ${e instanceof Error ? e.stack : e}`),
              ),
          );
        }
      }
      tail.push(line);
      if (tail.length > 40) tail.shift();
    });
  }
  // "close": every process holding its output has ended, the restarted ones too.
  const ended = await new Promise<string>((resolve) => {
    const timer = setTimeout(() => {
      o.check(false, `${o.label}: did not end within ${o.ms / 1000}s`);
      // What the app was doing: its last lines (a stall report among them, if its test thread
      // went quiet), then its threads' stacks, before it is stopped.
      o.say(`${o.label}: the app's last lines:\n  ${tail.join("\n  ")}`);
      if (child.pid !== undefined) stacks(child.pid, dirname(o.program), o.say);
      child.kill("SIGKILL");
    }, o.ms);
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      resolve(signal ?? String(code));
    });
    child.on("error", (e) => {
      clearTimeout(timer);
      o.check(false, `${o.label}: could not start ${o.program}: ${e.message}`);
      resolve("error");
    });
  });
  await Promise.all(pending);
  const failed = steps.find(([s]) => s === "failed");
  if (failed) {
    o.check(false, `${o.label}: the app's check failed: ${JSON.stringify(failed[1]).slice(0, 2000)}`);
    o.say(`${o.label}: last lines:\n  ${tail.join("\n  ")}`);
  }
  return {
    ended,
    step: (name) => steps.filter(([s]) => s === name).map(([, v]) => v),
    names: new Set(steps.map(([s]) => s)),
  };
}

/** A launch the updater must refuse before any download (NO_ROOTS, REFUSALS): the page's reason,
 * Update now not offered, and both commands refused for that reason; the installed copy unchanged
 * and nothing downloaded. */
export async function refusedLaunch(o: {
  refusal: Refusal;
  program: string;
  env: Record<string, string>;
  check: Check;
  say: Say;
  feed: UpdateFeed;
  /** Whether the installed copy is still v1 now. */
  unchanged: () => boolean;
}): Promise<Set<string>> {
  const { label, reason } = o.refusal;
  const before = o.feed.downloads();
  const r = await launchChain({
    label,
    program: o.program,
    env: { ...o.env, PLUXY_APP_TEST_UPDATE_CASE: o.refusal.case },
    ms: LAUNCH_MS,
    check: o.check,
    say: o.say,
  });
  const s = r.step(stepOf(o.refusal))[0];
  o.check(s, `${label}: no report`);
  if (s) {
    const call = s.call as { ok?: boolean; error?: string } | undefined;
    o.check(
      reason.test(String(s.reason)),
      `${label}: the page's reason ${JSON.stringify(s.reason)} is not ${reason}`,
    );
    o.check(s.offered === false, `${label}: Update now was offered`);
    o.check(
      call?.ok === false && reason.test(String(call.error)),
      `${label}: app_update_start: ${JSON.stringify(call)}`,
    );
    const rollback = s.rollback as { ok?: boolean; error?: string } | undefined;
    o.check(
      rollback?.ok === false && reason.test(String(rollback.error)),
      `${label}: app_update_rollback: ${JSON.stringify(rollback)}`,
    );
  }
  o.check(o.unchanged(), `${label}: the installed copy changed`);
  o.check(o.feed.downloads() === before, `${label}: the update was downloaded`);
  return r.names;
}

export function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** When process `pid` started, as text that differs between two processes that had the same id
 * one after the other: the kernel's start tick on Linux (`/proc/<pid>/stat`), `ps`'s start time on
 * macOS, the start time PowerShell reads on Windows. `undefined` when it cannot be read (the
 * process is gone, or the system's tool failed). */
export async function startOf(
  pid: number,
  platform: NodeJS.Platform = process.platform,
): Promise<string | undefined> {
  try {
    if (platform === "linux") {
      // Field 22 of the stat line; the command (field 2) may hold spaces and brackets, so count
      // from the last ")": what follows starts at field 3.
      const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
      return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19] || undefined;
    }
    const command =
      platform === "darwin"
        ? ["/bin/ps", "-o", "lstart=", "-p", String(pid)]
        : platform === "win32"
          ? [
              "powershell.exe",
              "-NoProfile",
              "-NonInteractive",
              "-Command",
              `(Get-Process -Id ${pid} -ErrorAction Stop).StartTime.ToUniversalTime().Ticks`,
            ]
          : undefined;
    return command ? await outputOf(command, 20_000) : undefined;
  } catch {
    return undefined;
  }
}

/** What command writes to stdout, trimmed, if it exits 0 within ms; `undefined` otherwise. It runs
 * without holding up this process: the session serves the update feed from it, and a cold
 * PowerShell on a loaded Windows runner took close to 20 s, longer than the app waits to connect
 * to the feed (10 s). */
export async function outputOf(command: string[], ms: number): Promise<string | undefined> {
  const p = Bun.spawn(command, { stdout: "pipe", stderr: "ignore", stdin: "ignore", timeout: ms });
  const out = new Response(p.stdout).text();
  // A tool ended for its time may leave a child holding its output: not waited for then.
  if ((await p.exited) !== 0) return undefined;
  return (await out).trim() || undefined;
}

/** Whether the process that was `pid` and started at `started` (`startOf`, read while it was known
 * to run) still runs. An id given to a later process does not count: it started at another time.
 * Without a start time (`started` unread) or while it cannot be read again, the id alone says. */
export async function stillRuns(
  pid: number,
  started: string | undefined,
  isAlive: (pid: number) => boolean = alive,
  now: (pid: number) => Promise<string | undefined> = startOf,
): Promise<boolean> {
  if (!isAlive(pid)) return false;
  if (started === undefined) return true;
  const at = await now(pid);
  return at === undefined || at === started;
}

/** A launch of program on the same profile while the running version holds its acceptance open:
 * as a second launch, it hands over to the running app and exits. It runs as an `instances` app
 * (native_test.rs) would, so one that wrongly came up as an app of its own says `up` and waits;
 * this launch was started here and is ended here then. */
async function thirdLaunch(program: string, env: Record<string, string>) {
  const child = spawn(program, [], {
    env: { ...env, PLUXY_APP_TEST_INSTANCES: "1", PLUXY_APP_TEST_UPDATE_CASE: "third" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const steps: string[] = [];
  for (const stream of [child.stdout, child.stderr])
    createInterface({ input: stream }).on("line", (line) => {
      const m = /^native-test: (\S+) /.exec(line);
      if (m) steps.push(m[1] as string);
    });
  const t0 = Date.now();
  const closed = new Promise<number | null>((resolve) => child.on("close", (c) => resolve(c)));
  const code = await Promise.race([closed, Bun.sleep(THIRD_MS).then(() => "timeout" as const)]);
  if (code === "timeout") {
    child.kill("SIGKILL");
    await closed;
  }
  return { code, steps, ms: Date.now() - t0 };
}

/** What the update route needs to know of the system it runs on. */
export interface Route {
  /** The installed test package's program, which the route starts first (v1). */
  program: string;
  /** The installed copy the updater replaces (the AppImage, the program or the app bundle). */
  installed: string;
  /** The program a third launch starts (the installed copy's, v2 by then). */
  third: string;
  env: Record<string, string>;
  /** The folder the session and the app share (PLUXY_APP_TEST_UPDATE). */
  dir: string;
  v1: string;
  v2: string;
  v2Version: string;
  /** Whether the app runs its own core (managed mode): it is stopped and started across restarts. */
  managed: boolean;
  feed: UpdateFeed;
  check: Check;
  say: Say;
  /** Files in the installed copy's folder that are not the app's (Windows: the per-user setup's
   * uninstall.exe), expected there at each stage. */
  others?: string[];
  /** Called as each stage reports, with its step, before the route goes on. */
  onStage?: (stage: string, v: Step) => void;
}

/** Whether the stage reports come from as many different processes. A process id is reused (on
 * Windows a later stage can get an earlier stage's), so each report carries a value its process made
 * once, at its start (`run`): two reports with the same one come from the same process. */
export function stagesAreProcesses(stages: Step[]): boolean {
  const runs = stages.map((s) => s.run);
  return (
    stages.every((s) => typeof s.pid === "number") &&
    runs.every((r) => typeof r === "string" && r !== "") &&
    new Set(runs).size === runs.length
  );
}

/** Whether two reports come from the same process: both carry the same start value (`run`), which
 * the process id, reused by later processes, cannot say. */
export function sameProcess(a: Step | undefined, b: Step | undefined): boolean {
  return typeof a?.run === "string" && a.run !== "" && a.run === b?.run;
}

/** The update route (see the top of this file), and its checks. Returns the steps it saw. */
export async function updateRoute(o: Route): Promise<Set<string>> {
  const { check, say } = o;
  // Downloads before the route (Windows: the update ended between parts downloaded it once).
  const fetched = { artifact: o.feed.count(ARTIFACT), slow: o.feed.count(SLOW_ARTIFACT) };
  // The installed copy's pin, read here when each stage reported.
  const pins: Record<string, string> = {};
  const pids: Record<string, number> = {};
  // The refused version's start time, read while it runs (startOf).
  let refusedStart: Promise<string | undefined> = Promise.resolve(undefined);
  let earlierEnded: boolean | undefined;
  let third: Awaited<ReturnType<typeof thirdLaunch>> | undefined;
  const onStep = async (name: string, v: Step) => {
    if (name === "update_stage") {
      const stage = String(v.stage);
      try {
        pins[stage] = pinOf(o.installed);
      } catch (e) {
        pins[stage] = `unreadable: ${(e as Error).message}`;
      }
      pids[stage] = Number(v.pid);
      o.onStage?.(stage, v);
      // Read while it runs: a process id is reused, so the wait below also compares this.
      if (stage === "refused") refusedStart = startOf(pids.refused as number);
      // The version that installed the update ends once the new one has accepted itself.
      if (stage === "updated" && pids.refused) {
        const started = await refusedStart;
        if (started === undefined)
          say(`start time of the refused version's process ${pids.refused} unread: waiting on its id alone`);
        const until = Date.now() + 30_000;
        while ((await stillRuns(pids.refused, started)) && Date.now() < until) await Bun.sleep(200);
        earlierEnded = !(await stillRuns(pids.refused, started));
      }
    }
    if (name === "update_gap_hidden") {
      try {
        third = await thirdLaunch(o.third, o.env);
        say(`third launch in the gap: ${JSON.stringify(third)}`);
      } finally {
        rmSync(join(o.dir, "hold-gap"), { force: true });
      }
    }
  };
  const r = await launchChain({
    label: "update",
    program: o.program,
    env: { ...o.env, PLUXY_APP_TEST_UPDATE_CASE: "update" },
    ms: CHAIN_MS,
    check,
    say,
    onStep,
  });
  rmSync(join(o.dir, "hold-gap"), { force: true });

  const stages = r.step("update_stage");
  const seen = stages.map((s) => `${s.stage}@${s.version}`);
  say(`versions that ran: ${seen.join(", ")}`);
  check(
    JSON.stringify(seen) ===
      JSON.stringify([`first@${V1}`, `refused@${V1}`, `updated@${o.v2Version}`, `rolled-back@${V1}`]),
    `the update route ran ${JSON.stringify(seen)}`,
  );
  const at = (name: string) => stages.find((s) => s.stage === name) ?? {};
  const want = { first: o.v1, refused: o.v1, updated: o.v2, "rolled-back": o.v1 } as const;
  for (const [name, sha] of Object.entries(want)) {
    const which = sha === o.v2 ? "v2" : "v1";
    check(
      at(name).installed_sha === sha,
      `${name}: the app says the installed copy is ${at(name).installed_sha}, not ${which}`,
    );
    check(pins[name] === sha, `${name}: the installed copy read here is ${pins[name]}, not ${which}`);
    // What an install leaves next to the installed copy (on Windows, in the set's folder) is
    // gone once the earlier version ended.
    const beside = at(name).beside as string[] | undefined;
    const own = ownNames(o.installed, o.others);
    check(
      JSON.stringify(beside) === JSON.stringify(own),
      `${name}: next to the installed copy: ${JSON.stringify(beside)}, not only ${own.join(", ")}`,
    );
  }
  check(
    stagesAreProcesses(stages),
    `each stage is not a process of its own: ${JSON.stringify(stages.map((s) => [s.pid, s.run]))}`,
  );
  check(
    earlierEnded === true,
    "the version that installed the update had not ended 30 s after the new one reported",
  );
  if (o.managed) {
    const cores = stages.map((s) => s.core_pid);
    check(
      new Set(cores).size === cores.length && cores.every((p) => typeof p === "number"),
      `the core was not stopped and started again across restarts: ${JSON.stringify(cores)}`,
    );
  }
  const refusals = r.step("update_refusals")[0] ?? {};
  const lower = refusals.lower as Step | undefined;
  check(
    lower?.offered === false && (lower.call as Step)?.ok === false,
    `lower feed: ${JSON.stringify(lower)}`,
  );
  for (const k of ["badsig", "mismatch", "cancelled"])
    check((refusals[k] as Step)?.installed_sha === o.v1, `${k}: the installed copy changed`);
  const plugin = refusals.plugin as Step | undefined;
  check(
    plugin?.ok === false && /not allowed/.test(String(plugin.error)),
    `the window reached the updater plugin: ${JSON.stringify(plugin)}`,
  );
  check(
    /signature/i.test(String((refusals.badsig as Step)?.error)),
    `bad signature: ${JSON.stringify(refusals.badsig)}`,
  );
  check(
    /version/i.test(String((refusals.mismatch as Step)?.error)),
    `signed version: ${JSON.stringify(refusals.mismatch)}`,
  );
  check(
    /did not start|not accept/i.test(String((at("refused").last as Step)?.detail)) &&
      (at("refused").last as Step)?.result === "failed",
    `the refused update was not reported undone: ${JSON.stringify(at("refused").last)}`,
  );
  check(r.step("update_refused")[0]?.hook_left === false, "the new version never reached its acceptance");

  // The gap: the new version held its acceptance open, a third launch reached it and exited.
  const gap = r.step("update_gap")[0];
  check(
    sameProcess(gap, at("updated")),
    `the new version did not hold its acceptance open: ${JSON.stringify(gap)}`,
  );
  check(
    third?.code === 0 && third.steps.length === 0,
    `the third launch in the gap did not hand over and exit 0 within ${THIRD_MS / 1000}s with no step of its own: ${JSON.stringify(third)}`,
  );
  check(
    r.step("update_gap_raised")[0]?.visible === true,
    `the third launch did not show the running version's window: ${JSON.stringify(r.step("update_gap_raised")[0])}`,
  );

  check(
    (at("updated").last as Step)?.result === "installed",
    `the update was not reported installed: ${JSON.stringify(at("updated").last)}`,
  );
  const offered = (at("updated").rollback as Step[] | undefined) ?? [];
  check(
    offered.some((x) => x.version === V1 && x.sha256 === o.v1),
    `${V1} (${o.v1}) is not offered to roll back to: ${JSON.stringify(offered)}`,
  );
  const rb = r.step("update_rollback_refusals")[0] ?? {};
  check(
    (rb.unknown as Step)?.ok === false && rb.no_dialog === true,
    `rollback to a version never installed: ${JSON.stringify(rb.unknown)}`,
  );
  check(rb.kept_found === true, "the kept copy of v1 is not where the updater keeps it");
  check(
    (rb.tampered as Step)?.ok === false && rb.no_dialog_tampered === true,
    `rollback to a changed kept copy: ${JSON.stringify(rb.tampered)}`,
  );
  check(
    String(rb.dialog).includes(V1) && String(rb.dialog).includes(o.v1.slice(0, 16)),
    `the rollback confirmation does not show ${V1} and its digest: ${rb.dialog}`,
  );
  check((rb.cancelled as Step)?.installed_sha === o.v2, "a cancelled rollback changed the installed copy");
  check(
    (at("rolled-back").last as Step)?.result === "rolled-back",
    `the rollback was not reported: ${JSON.stringify(at("rolled-back").last)}`,
  );
  check(r.step("update_done").length === 1, "the update route did not finish");
  check(pinOf(o.installed) === o.v1, "the copy installed at the end is not v1");
  // Downloads: one each for the bad signature, the signed version, the refused and the real
  // update; the slow one, cancelled.
  const artifact = o.feed.count(ARTIFACT) - fetched.artifact;
  const slow = o.feed.count(SLOW_ARTIFACT) - fetched.slow;
  check(artifact === 4, `v2 was downloaded ${artifact} times on the route, not 4`);
  check(slow === 1, `the slow download was requested ${slow} times on the route`);
  return r.names;
}

/** Every expected step was seen. */
export function checkSteps(check: Check, system: System, seen: Set<string>) {
  const missing = expectedSteps(system).filter((s) => !seen.has(s));
  check(missing.length === 0, `steps that never came: ${missing.join(", ")}`);
}

/** Makes a private folder of this user's (mode 700 off Windows). */
export function privateFolder(path: string) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  if (process.platform !== "win32") chmodSync(path, 0o700);
  return path;
}
