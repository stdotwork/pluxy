// The managed-core steps' fixtures and checks (src-tauri/src/native_core.rs), shared by the Linux
// core steps session (run.ts) and the dialogs session's core run on Windows and macOS
// (dialogs.ts): signed test core releases with their index, made for one run, and what each step
// must report.

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, type KeyObject, randomBytes, randomUUID, sign } from "node:crypto";
import { cpSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";

const next = resolvePath(import.meta.dir, "..", "..");

/** Signed test core releases for the managed run, in a folder of their own. */
export interface CoreSource {
  /** The index file, which the app's connection names as its core source. */
  index: string;
  /** A core program of the runner's, which the app must adopt before it runs it. */
  custom: string;
  /** The trust profile's root for the run's key, bound to the index. */
  root: object;
}

const CORE_ID = "pluxy/core";
const PLATFORM = `${process.platform === "win32" ? "windows" : process.platform}/${process.arch === "x64" ? "amd64" : process.arch}`;
const VERSION_FLAG = "-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=";
/** The core's program, as the app's store names it (cores.rs PROGRAM). */
export const PROGRAM = process.platform === "win32" ? "pluxy.exe" : "pluxy";

function run(cmd: string, args: string[], cwd: string, env: NodeJS.ProcessEnv = process.env) {
  const r = spawnSync(cmd, args, { cwd, env, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status ?? r.signal}`);
  return r.stdout;
}

function hex(b: Buffer | Uint8Array): string {
  return Buffer.from(b).toString("hex");
}

/** The system's own tar: on Windows bsdtar in System32 (another tar on PATH, such as Git's GNU
 * tar, reads `C:` in a path as a remote host); on macOS without AppleDouble `._` entries. */
function tar(args: string[], cwd: string) {
  if (process.platform === "win32")
    return run(join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe"), args, cwd);
  return run("tar", args, cwd, { ...process.env, COPYFILE_DISABLE: "1" });
}

/** One release in the index: the archive of folder, its descriptor and a signature by key. */
function release(dir: string, version: string, folder: string, key: KeyObject, keyId: string, bad = false) {
  const name = `pluxy-core-${version}-${PLATFORM.replace("/", "-")}.tar.gz`;
  tar(["-czf", join(dir, name), "-C", folder, "."], dir);
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

/** A bad candidate core: it breaks its config (`-config <path>`) and exits 3. */
const BROKEN = `package main

import (
	"fmt"
	"os"
)

func main() {
	for i, a := range os.Args {
		if a == "-config" && i+1 < len(os.Args) {
			_ = os.WriteFile(os.Args[i+1], []byte("not json"), 0o600)
		}
	}
	fmt.Fprintln(os.Stderr, "pluxy: bad candidate broke its config")
	os.Exit(3)
}
`;

/** Builds cores 1.0.0 and 2.0.0 (with the bundled test packages of prepared, which
 * `go run ./e2e/panelcore -probe -prepare` made), a 3.0.0 that breaks its config and exits, and a
 * 2.1.0 whose signature does not verify, signed by a key made for this run, with their index. */
export function coreSource(dir: string, prepared: string): CoreSource {
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
        join(folder, PROGRAM),
        "./cmd/pluxy",
      ],
      next,
    );
    cpSync(join(prepared, "bundled"), join(folder, "bundled"), { recursive: true });
    releases.push(release(dir, version, folder, privateKey, keyId));
  }
  const broken = join(dir, "core-3.0.0");
  const src = join(dir, "broken-src");
  mkdirSync(broken);
  mkdirSync(src);
  writeFileSync(join(src, "main.go"), BROKEN);
  run("go", ["build", "-o", join(broken, PROGRAM), "main.go"], src, {
    ...process.env,
    GOFLAGS: "",
    GOWORK: "off",
  });
  releases.push(release(dir, "3.0.0", broken, privateKey, keyId));
  releases.push(release(dir, "2.1.0", join(dir, "core-2.0.0"), privateKey, keyId, true));
  const index = join(dir, "index.json");
  writeFileSync(index, JSON.stringify({ format: 1, releases }, null, 2));
  const jwk = publicKey.export({ format: "jwk" });
  return {
    index,
    custom: join(prepared, PROGRAM),
    root: {
      kind: "test",
      publisher: "pluxy",
      source: index,
      key_id: keyId,
      public_key: hex(Buffer.from(jwk.x ?? "", "base64url")),
    },
  };
}

/** What the managed run's core steps must report (native_core.rs). */
export const CORE_STEPS: [string, Record<string, unknown>][] = [
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
      pid_runs_program: true,
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
  [
    "core_changed",
    {
      reinstalled: "done",
      refused: true,
      repaired: "done",
      version: "1.0.0",
      status: 200,
      reported: "1.0.0",
    },
  ],
  [
    "core_repair_running",
    {
      outcome: "done",
      version: "1.0.0",
      new_process: true,
      old_gone: true,
      status: 200,
      reported: "1.0.0",
    },
  ],
];

/** What is wrong with the core steps' reports (step name to its JSON), for the run with core. */
// biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
export function coreProblems(steps: Map<string, any>, core: CoreSource): string[] {
  const problems: string[] = [];
  const check = (ok: unknown, why: string) => {
    if (!ok) problems.push(why);
  };
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
    installed("core_changed") === '["1.0.0","2.0.0"]',
    `installed after the repair: ${installed("core_changed")}`,
  );
  check(
    installed("core_repair_running") === '["1.0.0","2.0.0"]',
    `installed after the running version was installed again: ${installed("core_repair_running")}`,
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
  check(
    /changed since it was installed/.test(String(steps.get("core_changed")?.detail)),
    "the changed version's refusal does not say why",
  );
  return problems;
}

/** A core the runner starts, which the app never started: it must outlive the app's run, its
 * config and program untouched. */
export interface Sentinel {
  proc: ChildProcess;
  base: string;
  key: string;
  config: string;
  before: Buffer;
  program: string;
  /** The program's SHA-256, and its inode where the system keeps one per file (not Windows). */
  digest: string;
  ino?: number;
}

const digestOf = (file: string) => createHash("sha256").update(readFileSync(file)).digest("hex");

/** Starts a sentinel core from a copy of the core folder from (a release's) in dir. */
export async function startSentinel(dir: string, from: string): Promise<Sentinel> {
  cpSync(from, dir, { recursive: true });
  const key = randomBytes(32).toString("hex");
  const config = join(dir, "config.json");
  writeFileSync(
    config,
    JSON.stringify({
      listen: "127.0.0.1:0",
      client_keys: [randomBytes(32).toString("hex")],
      management_key: key,
      credentials_dir: join(dir, "credentials"),
      packages: { dir: join(dir, "packages") },
    }),
    { mode: 0o600 },
  );
  const before = readFileSync(config);
  const program = join(dir, PROGRAM);
  const proc = spawn(program, ["-config", config], { cwd: dir, stdio: ["ignore", "ignore", "pipe"] });
  const base = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`the sentinel core exited with ${code}`)));
    if (proc.stderr)
      createInterface({ input: proc.stderr }).on("line", (line) => {
        const m = /^pluxy: listening on (\S+)$/.exec(line);
        if (m?.[1]) resolve(m[1]);
      });
  });
  return {
    proc,
    base,
    key,
    config,
    before,
    program,
    digest: digestOf(program),
    ino: process.platform === "win32" ? undefined : statSync(program).ino,
  };
}

/** What is wrong with the sentinel after the app's run: it must still run and answer its own
 * management key, with its config and program as they were. Says what it found first. */
export async function sentinelProblems(s: Sentinel, say: (line: string) => void): Promise<string[]> {
  const running = s.proc.exitCode === null && s.proc.signalCode === null;
  let status = 0;
  try {
    const r = await fetch(`${s.base}/v0/management/status`, {
      headers: { Authorization: `Bearer ${s.key}` },
    });
    status = r.status;
  } catch {
    // no answer
  }
  const kept = readFileSync(s.config).equals(s.before);
  const same = digestOf(s.program) === s.digest && (s.ino === undefined || statSync(s.program).ino === s.ino);
  say(
    `managed sentinel ${JSON.stringify({ alive: running, status, config_kept: kept, program_kept: same })}`,
  );
  const problems: string[] = [];
  if (!running || status !== 200) problems.push("the sentinel core did not survive the app");
  if (!kept) problems.push("the sentinel core's config changed");
  if (!same) problems.push("the sentinel core's program changed");
  return problems;
}

/** Stops the sentinel, the process the runner started, and waits for it to exit. */
export async function stopSentinel(s: Sentinel) {
  if (s.proc.exitCode !== null || s.proc.signalCode !== null) return;
  const exited = new Promise((r) => s.proc.once("exit", r));
  s.proc.kill("SIGTERM");
  await exited;
}
