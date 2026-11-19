// The native dialogs session on Windows and macOS (.github/workflows/next-app-platforms.yml):
// the app, built with the native-test feature, in local mode against a test core with the probe
// page (`go run ./e2e/panelcore -probe`), runs native_test.rs's main run there: the window's and
// the page's command probes, the native confirmations (confirmation_native.rs), native key entry
// (key_entry_test.rs), management-key rotation (rotation_test.rs), the page ingress checks, then
// closes the window and quits. Its dialogs are worked through the UI driver (ui_driver.rs); this
// runner checks the app's step reports as run.ts checks them on Linux, and that no key reached the
// app's output. run.ts's sessions need Xvfb, a session bus and the AppImage, so this is a runner
// of its own; on Linux run.ts's local and remote sessions run the same checks.
//
// It runs only on a disposable CI runner (native/runner.ts). On Windows the app's folders come
// from the account's known folders, which no environment variable redirects: it refuses to start
// when the app's roaming or local folder already exists, and afterwards removes only the folders
// this run made. On macOS the app gets a temporary HOME.
//
// Usage, from next/app after `bun run build`: bun native/dialogs.ts

import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  appendFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";
import { DISPOSABLE_RUNNER, onDisposableRunner } from "./runner";

const app = resolvePath(import.meta.dir, "..");
const next = resolvePath(app, "..");
const tauri = join(app, "src-tauri");
const identifier = "dev.pluxy.desktop";
const PAGE = "ext/test/probe/probe";
/** Tauri's command check refusing a call (debug builds say where; release builds "by ACL"). */
const REFUSED = /not allowed( on window|\. Permissions associated| by ACL)/;
/** The whole app run: the main run takes about three minutes on Linux. */
const TIMEOUT = 900_000;
const platform =
  process.platform === "win32" ? "windows" : process.platform === "darwin" ? "macos" : undefined;

/**
 * Checks that are expected to fail on a platform until the slice named fixes them: each one is
 * reported by name, with what it saw, and counted, and passing one is a problem too (the
 * expectation is stale). Each points at the plan row that owns it, and only two may: P8p4a (the
 * tray, single instance and window preferences on Windows and macOS) and P8p7 (extension page
 * isolation on Windows and macOS). Whichever of this change and the owner's fix lands second
 * removes the entry.
 */
const EXPECTED: Record<string, { platforms: string[]; owner: string; why: string }> = {};

/**
 * Known Windows limitation, the one exception to the frame checks: wry adds every Tauri
 * initialization script to all frames on WebView2, main-frame-only ones too, so the page's frame
 * has a Tauri bridge of its own there ("own tauri internals" succeeds). The frame-commands and
 * reached checks below show it reaches nothing. Drop this once wry keeps main-frame-only scripts
 * out of frames on WebView2: https://github.com/tauri-apps/wry/pull/1874
 */
const FRAME_BRIDGE_ON_WINDOWS = { platform: "windows", check: "own tauri internals", status: "succeeded" };

/**
 * Windows only: when the page document's frame-src blocks a frame's off-site address, WebView2
 * shows its own error page in the frame even though the app's navigation hook cancelled the
 * navigation (it ends unsuccessfully, not cancelled). The page's document is then gone, so the app
 * closes the page's channel (page_frames.rs); the off-site address itself never loads. This is
 * accepted there only with the hook's or the frame-src stop recorded, the replacement recorded and
 * the channel closed.
 */
const ERROR_PAGE_ON_WINDOWS = { platform: "windows" };

/** Every command of the plugins the build registers, from the ACL manifests Tauri's build
 * writes (as lib.rs plugin_commands reads them); read once the app is built. */
function pluginCommands(): string[] {
  const manifests = JSON.parse(readFileSync(join(tauri, "gen", "schemas", "acl-manifests.json"), "utf8"));
  const all = new Set<string>();
  for (const [plugin, manifest] of Object.entries<{
    permissions?: Record<string, { commands?: { allow?: string[]; deny?: string[] } }>;
  }>(manifests)) {
    if (plugin === "__app-acl__") continue;
    const name = plugin.replace(/^core:/, "");
    for (const p of Object.values(manifest.permissions ?? {}))
      for (const c of [...(p.commands?.allow ?? []), ...(p.commands?.deny ?? [])])
        all.add(`plugin:${name}|${c}`);
  }
  return [...all];
}

function refuse(why: string): never {
  console.error(`dialogs: refused to start: ${why}`);
  process.exit(2);
}

function run(cmd: string, args: string[], cwd: string, env = process.env): string {
  const r = spawnSync(cmd, args, { cwd, env, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status ?? r.signal}`);
  return r.stdout;
}

/** The account's known folders on Windows, as the app finds them (SHGetKnownFolderPath). */
function knownFolders(): { roaming: string; local: string } {
  const out = run(
    "powershell.exe",
    [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      "[Environment]::GetFolderPath('ApplicationData'); [Environment]::GetFolderPath('LocalApplicationData')",
    ],
    app,
  )
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  if (out.length !== 2) throw new Error(`PowerShell gave the known folders as ${JSON.stringify(out)}`);
  return { roaming: out[0] as string, local: out[1] as string };
}

async function startCore(): Promise<{ proc: ChildProcess; base: string; key: string; config: string }> {
  const proc = spawn("go", ["run", "./e2e/panelcore", "-probe"], {
    cwd: next,
    stdio: ["pipe", "pipe", "inherit"],
  });
  const line = await new Promise<string>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`panelcore exited with ${code}`)));
    if (proc.stdout) createInterface({ input: proc.stdout }).once("line", resolve);
  });
  return { proc, ...JSON.parse(line) };
}

type Probe = { internals?: boolean; calls: Record<string, { ok: boolean; detail: string }> } & Record<
  string,
  // biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
  any
>;

/**
 * Prints the stack of every thread of process pid (the app this run started), on Windows with the
 * console debugger when the runner has it, attached without stopping the process for good (`-pv`,
 * noninvasive); symbols from the app's folder, where the debug build leaves its PDB. Only names
 * and addresses come out, never memory.
 */
function stacks(pid: number, symbols: string) {
  if (platform !== "windows") return;
  const cdb = [
    "C:\\Program Files (x86)\\Windows Kits\\10\\Debuggers\\x64\\cdb.exe",
    "C:\\Program Files\\Windows Kits\\10\\Debuggers\\x64\\cdb.exe",
  ].find((p) => existsSync(p));
  if (!cdb) {
    console.log("dialogs: no console debugger (cdb.exe) on this runner, so no thread stacks");
    return;
  }
  const r = spawnSync(cdb, ["-pv", "-p", String(pid), "-y", symbols, "-c", "~*kc 40; qd"], {
    encoding: "utf8",
    timeout: 120_000,
  });
  console.log(`dialogs: the app's threads (cdb, status ${r.status}):`);
  for (const line of `${r.stdout ?? ""}${r.stderr ?? ""}`.split("\n")) console.log(`  stack | ${line}`);
}

async function main(): Promise<number> {
  if (!platform)
    refuse("this session runs on Windows and macOS; on Linux, run.ts's local session runs these checks");
  if (!onDisposableRunner())
    refuse(`it runs only on ${DISPOSABLE_RUNNER}: on Windows it would use this account's own app folders`);

  const root = mkdtempSync(join(tmpdir(), "pluxy-app-dialogs-"));
  const home = join(root, "home");
  mkdirSync(home, { mode: 0o700 });
  // Where the app keeps what this runner writes and checks.
  let configDir: string;
  let localDir: string;
  // The app's folders this run made, the only ones it removes.
  const made: string[] = [];
  if (platform === "windows") {
    const k = knownFolders();
    configDir = join(k.roaming, identifier);
    localDir = join(k.local, identifier);
    for (const d of [configDir, localDir])
      if (existsSync(d))
        refuse(`the app's folder ${d} exists already; this run removes only folders it made`);
    made.push(configDir, localDir);
  } else {
    // macOS: the app's folders come from HOME (Application Support holds config and data).
    configDir = join(home, "Library", "Application Support", identifier);
    localDir = configDir;
  }

  const problems: string[] = [];
  const expected: string[] = [];
  let passed = 0;
  const check = (name: string, ok: unknown, detail: string) => {
    const e = EXPECTED[name];
    const expect = e?.platforms.includes(platform as string);
    if (ok) {
      passed++;
      if (expect) problems.push(`${name}: expected to fail on ${platform} until ${e?.owner}, but passed`);
    } else if (expect) {
      expected.push(`${name} (expected until ${e?.owner}: ${e?.why}): ${detail}`);
    } else {
      problems.push(`${name}: ${detail}`);
    }
  };

  console.log("dialogs: building the app with the native-test feature");
  run("cargo", ["build", "--locked", "--features", "native-test", "--bin", "pluxy-app"], tauri);
  const targetDir = resolvePath(tauri, process.env.CARGO_TARGET_DIR ?? "target");
  const bin = join(targetDir, "debug", platform === "windows" ? "pluxy-app.exe" : "pluxy-app");

  const output: string[] = [];
  let exit: number | null = null;
  let core: Awaited<ReturnType<typeof startCore>> | undefined;
  try {
    console.log("dialogs: starting the test core");
    core = await startCore();
    mkdirSync(configDir, { recursive: true });
    mkdirSync(localDir, { recursive: true });
    // The connection the app starts with: local mode, signed in with a kept key.
    writeFileSync(
      join(localDir, "connection.json"),
      JSON.stringify({ mode: "local", url: core.base, key: core.key }),
      { mode: 0o600 },
    );
    // The rotation check closes the window to the tray and opens it again.
    writeFileSync(join(configDir, "window.json"), JSON.stringify({ close_behavior: "minimize-to-tray" }), {
      mode: 0o600,
    });
    const wrong = randomUUID();
    const keyFile = join(root, "key-input");
    writeFileSync(keyFile, JSON.stringify({ key: core.key, wrong }), { mode: 0o600 });
    const rotatedFile = join(root, "rotated-keys");
    const quit = join(root, "quit");
    const env: Record<string, string> = {
      ...(process.env as Record<string, string>),
      PLUXY_APP_TEST_PAGE: PAGE,
      PLUXY_APP_TEST_QUIT: quit,
      PLUXY_APP_TEST_KEY_FILE: keyFile,
      PLUXY_APP_TEST_ROTATED_FILE: rotatedFile,
      PLUXY_APP_TEST_CORE_CONFIG: core.config,
    };
    if (platform === "macos") env.HOME = home;

    console.log(`dialogs: running ${bin}`);
    const proc = spawn(bin, [], { cwd: root, env, stdio: ["ignore", "pipe", "pipe"] });
    // biome-ignore lint/suspicious/noExplicitAny: the app's JSON evidence, checked field by field below
    const steps = new Map<string, any>();
    const order: string[] = [];
    // The webview of each command the app reached (lib.rs trusted, native-test builds).
    const reached: string[] = [];
    const onLine = (line: string) => {
      output.push(line);
      if ([core?.key ?? "", wrong].some((k) => k && line.includes(k))) {
        problems.push("a management key appeared on the app's stdout/stderr");
        return;
      }
      console.log(`  app | ${line}`);
      const m = /^native-test: (\S+) (.*)$/.exec(line);
      if (!m) return;
      const [, step, json] = m as unknown as [string, string, string];
      if (step === "reached") {
        try {
          reached.push(JSON.parse(json).webview);
        } catch {
          reached.push(json);
        }
      }
      try {
        steps.set(step, JSON.parse(json));
      } catch {
        steps.set(step, json);
      }
      order.push(step);
      // The window has closed: the app may quit once its checks are done.
      if (step === "closed") writeFileSync(quit, "");
      // The app's test thread gave no sign of life: what each of its threads is doing, then
      // stop it rather than wait out the time limit.
      if (step === "stalled") {
        problems.push(`the app stalled: ${json}`);
        if (proc.pid !== undefined) stacks(proc.pid, dirname(bin));
        proc.kill();
      }
    };
    if (proc.stdout) createInterface({ input: proc.stdout }).on("line", onLine);
    if (proc.stderr) createInterface({ input: proc.stderr }).on("line", onLine);
    const timer = setTimeout(() => {
      problems.push(`the app did not finish within ${TIMEOUT / 1000} s; stopped it`);
      if (proc.pid !== undefined) stacks(proc.pid, dirname(bin));
      // Only the process this run started.
      proc.kill();
    }, TIMEOUT);
    exit = await new Promise<number | null>((resolve) => proc.once("exit", (code) => resolve(code)));
    clearTimeout(timer);

    // The app's own report, step by step, as run.ts checks it on Linux.
    check(
      "no failed step",
      !steps.has("failed"),
      `the app's test steps failed: ${JSON.stringify(steps.get("failed"))}`,
    );
    check("exit", exit === 0, `the app exited with ${exit}`);
    check("quit", steps.has("quit"), "the app did not reach its quit step");

    const main = steps.get("main") as Probe | undefined;
    check("main ipc", main?.internals, "the window's webview has no IPC");
    const got = main?.calls?.connection_get;
    const view = got?.ok ? JSON.parse(got.detail) : undefined;
    check(
      "main connection",
      view?.mode === "local" && view?.signed_in === true && view?.target === core?.base,
      `the window's connection is ${JSON.stringify(view)}`,
    );
    for (const [cmd, c] of Object.entries(main?.calls ?? {}))
      if (cmd !== "connection_get")
        check(`main ${cmd}`, !/not allowed/i.test(c.detail), `the window was refused ${cmd}: ${c.detail}`);
    check("status", steps.get("status")?.status === 200, "core's status did not come through");

    const commands = [
      ...readFileSync(join(tauri, "src", "command_names.rs"), "utf8").matchAll(/^\s+"(\w+)",$/gm),
    ].map((m) => m[1] as string);
    const page = steps.get("page") as Probe | undefined;
    check("page ipc", page?.internals, "the page webview has no IPC, so the probe proves nothing");
    const pageUrl = platform === "windows" ? "http://pluxy-page.localhost/" : "pluxy-page://localhost/";
    check("page url", page?.url === pageUrl, `the page webview shows ${page?.url}`);
    const near = (a?: number, b?: number) => a !== undefined && b !== undefined && Math.abs(a - b) <= 2;
    check(
      "page area",
      page?.area &&
        page?.placed &&
        (["x", "y", "width", "height"] as const).every((k) => near(page.area[k], page.placed[k])),
      `the page webview is at ${JSON.stringify(page?.placed)}, the page area at ${JSON.stringify(page?.area)}`,
    );
    for (const cmd of [...commands, ...pluginCommands()])
      check(`page tried ${cmd}`, page?.calls?.[cmd], `the page's probe did not try ${cmd}`);
    for (const [cmd, c] of Object.entries(page?.calls ?? {}))
      check(
        `page refused ${cmd}`,
        !c.ok && REFUSED.test(c.detail),
        `${cmd} from the page webview: ${JSON.stringify(c)}`,
      );

    const frame = steps.get("frame") as Record<string, { status: string; detail: string }> | undefined;
    check("frame report", frame && Object.keys(frame).length >= 10, "the page sent no report");
    // The frame has a Tauri bridge on Windows only (FRAME_BRIDGE_ON_WINDOWS).
    const bridged = platform === FRAME_BRIDGE_ON_WINDOWS.platform;
    for (const [what, r] of Object.entries(frame ?? {}))
      check(
        `frame ${what}`,
        r.status === "blocked" ||
          (r.status === "attempted" && what === "native message handler") ||
          (bridged && what === FRAME_BRIDGE_ON_WINDOWS.check && r.status === FRAME_BRIDGE_ON_WINDOWS.status),
        `from the page's frame, "${what}" ${r.status}: ${r.detail}`,
      );
    // The frame calls every command through its own bridge: none succeeds.
    const invoked = steps.get("frame_invoke") as Probe | undefined;
    check(
      "frame bridge",
      invoked?.internals === bridged,
      `the page's frame ${invoked?.internals ? "has" : "has no"} Tauri bridge: ${JSON.stringify(invoked)}`,
    );
    const plugins = pluginCommands();
    check("plugin commands", plugins.length > 0, "no plugin commands in Tauri's ACL manifests");
    for (const cmd of [...commands, ...plugins])
      check(`frame tried ${cmd}`, invoked?.calls?.[cmd], `the page's frame did not try ${cmd}`);
    for (const [cmd, c] of Object.entries(invoked?.calls ?? {}))
      check(`frame refused ${cmd}`, !c.ok, `${cmd} from the page's frame: ${JSON.stringify(c)}`);
    // And the app reached commands from its window only.
    check("reached", reached.length > 0, "no command was reached at all");
    check(
      "reached only from main",
      reached.every((w) => w === "main"),
      `commands were reached from ${[...new Set(reached)].join(", ")}`,
    );
    // The frame's navigations off its site, each from a page of its own (native_test.rs
    // frame_offsite): each stopped where the evidence shows, refused by the app's navigation
    // handler or first by the app document's frame-src; none allowed, nothing reaching the page
    // scheme's probe address, the frame still there and the channel open, but for
    // ERROR_PAGE_ON_WINDOWS.
    type OffsiteTry = {
      url: string;
      error?: unknown;
      stopped?: string | null;
      refused?: boolean;
      allowed?: unknown[];
      replaced?: unknown[];
      stayed?: boolean;
      open?: boolean;
    };
    const offsite = steps.get("frame_offsite") as { tried?: OffsiteTry[]; probe_hits?: number } | undefined;
    check(
      "frame offsite tried",
      offsite?.tried?.length === 4,
      `the page's frame did not try to leave its site: ${JSON.stringify(offsite)}`,
    );
    check(
      "frame offsite probe",
      offsite?.probe_hits === 0,
      `an off-site try reached the page scheme: ${JSON.stringify(offsite)}`,
    );
    for (const t of offsite?.tried ?? []) {
      const seen = JSON.stringify(t);
      check(
        `frame offsite ${t.url} error`,
        t.error == null,
        `the page's frame could not try ${t.url}: ${seen}`,
      );
      check(
        `frame offsite ${t.url} stopped`,
        (t.stopped === "hook" && t.refused === true) || t.stopped === "frame-src",
        `nothing shows where ${t.url} stopped: ${seen}`,
      );
      check(
        `frame offsite ${t.url} allowed`,
        t.allowed?.length === 0,
        `the app allowed the frame to ${t.url}: ${seen}`,
      );
      const held = t.stayed === true && t.open === true;
      const errorPage =
        platform === ERROR_PAGE_ON_WINDOWS.platform &&
        t.stopped !== null &&
        t.stayed === false &&
        t.open === false &&
        (t.replaced?.length ?? 0) > 0;
      check(
        `frame offsite ${t.url} held`,
        held || errorPage,
        `the page's frame or channel did not hold for ${t.url}: ${seen}`,
      );
    }
    console.log(`frame_offsite ${JSON.stringify(offsite)}`);
    for (const [step, want] of [
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
        step,
        value && Object.entries(want).every(([k, w]) => value[k] === w),
        `${step} evidence missing or wrong: ${JSON.stringify(value)}`,
      );
    }
    check("ingress", steps.get("ingress")?.control === true, "the native ingress check did not finish");

    check(
      "confirmations",
      steps.get("confirmations")?.passed === true,
      "native confirmation checks did not complete",
    );
    check(
      "confirmation close box",
      steps.get("confirmations")?.close_box === true,
      "the confirmation's close box was not checked",
    );
    // Key entry whose window goes while it is open: the cancel answer, and key entry free again.
    const gone = steps.get("owner_gone");
    for (const field of ["entry_closed", "free_again", "connection_unchanged"])
      check(
        `owner gone ${field}`,
        gone?.[field] === true,
        `key entry with its window gone: ${JSON.stringify(gone)}`,
      );

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
      check(`keyentry ${field}`, entry?.[field] === true, `native key entry did not prove ${field}`);
    if (platform === "windows") {
      // Another process, through UI Automation, sees a password field and not what was typed.
      const uia = steps.get("uia");
      // Where UI Automation found the entry, from the desktop down.
      console.log(`dialogs: UI Automation path to the key entry: ${(uia?.path ?? []).join(" > ")}`);
      check(
        "uia password field",
        uia?.found === true && uia?.is_edit === true && uia?.is_password === true,
        `UI Automation saw ${JSON.stringify(uia)}`,
      );
      check(
        "uia no key",
        uia?.found === true && uia?.leaked === false,
        `UI Automation read the typed key: ${JSON.stringify(uia)}`,
      );
    }

    const rotation = steps.get("rotation");
    const wanted: Record<string, unknown> = {
      refused: true,
      control: true,
      cancelled: true,
      rotated: true,
      old_refused: true,
      new_works: true,
      kept: "device",
      session: true,
      recovery: true,
      webviews_clean: true,
      page_audited: true,
      outlived_window: true,
      replaced: true,
      connection_shown: true,
      no_launch_notice: true,
    };
    for (const [k, want] of Object.entries(wanted))
      check(
        `rotation ${k}`,
        rotation?.[k] === want,
        `native rotation evidence ${k}: ${JSON.stringify(rotation?.[k])}`,
      );
    const rotated = existsSync(rotatedFile)
      ? readFileSync(rotatedFile, "utf8").split("\n").filter(Boolean)
      : [];
    // The key the app started with and the three it rotated to.
    check("rotated keys", rotated.length === 4, `the rotation check recorded ${rotated.length} keys`);
    for (const k of rotated)
      if (output.some((line) => line.includes(k)))
        problems.push("a rotated management key appeared on the app's output");

    const input = steps.get("input") ?? {};
    const inputs = Object.entries(input).filter(
      ([k, v]) => k !== "platform" && typeof v === "number" && v > 0,
    );
    check("native input", inputs.length > 0, `no native input was sent: ${JSON.stringify(input)}`);

    const closed = steps.get("closed");
    check("closed tray", closed?.tray === true, "the app has no tray icon");
    check(
      "closed webviews",
      Array.isArray(closed?.webviews) && closed.webviews.length === 0,
      `webviews left after closing: ${JSON.stringify(closed?.webviews)}`,
    );

    const summary =
      `dialogs (${platform}): ${passed} checks passed, ${expected.length} expected failures, ` +
      `${problems.length} problems; steps ${order.length}; native input ${JSON.stringify(input)}`;
    console.log(`\n${summary}`);
    for (const x of expected) console.log(`expected failure: ${x}`);
    if (process.env.GITHUB_STEP_SUMMARY) {
      appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${summary}\n\n`);
      for (const x of expected) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `- expected failure: ${x}\n`);
    }
  } finally {
    if (core) {
      const proc = core.proc;
      proc.stdin?.end();
      if (proc.exitCode === null) await new Promise((r) => proc.once("exit", r));
    }
    rmSync(root, { recursive: true, force: true });
    for (const d of made) rmSync(d, { recursive: true, force: true });
  }
  if (problems.length > 0) {
    console.error(`\n${problems.length} problems:\n- ${problems.join("\n- ")}`);
    return 1;
  }
  if (passed === 0) {
    console.error("no check ran");
    return 1;
  }
  return 0;
}

process.exit(await main());
