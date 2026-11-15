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
// It runs only on a disposable GitHub-hosted runner (GITHUB_ACTIONS=true and
// RUNNER_ENVIRONMENT=github-hosted). On Windows the app's folders come from the account's known
// folders, which no environment variable redirects: it refuses to start when the app's roaming or
// local folder already exists, and afterwards removes only the folders this run made. On macOS
// the app gets a temporary HOME.
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
import { join, resolve as resolvePath } from "node:path";
import { createInterface } from "node:readline";

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
const FRAME_NAVIGATION =
  "WebView2 does not report a navigation inside the page's frame (wry hooks only the top-level " +
  "NavigationStarting), so the app does not close the page's channel when its frame navigates";
const EXPECTED: Record<string, { platforms: string[]; owner: string; why: string }> = {
  http_navigation: { platforms: ["windows"], owner: "P8p7", why: FRAME_NAVIGATION },
  ingress_navigation: { platforms: ["windows"], owner: "P8p7", why: FRAME_NAVIGATION },
  "frame own tauri internals": {
    platforms: ["windows"],
    owner: "P8p7",
    why: "the Tauri bridge (window.__TAURI_INTERNALS__) is present in the page's frame, though every call from it is refused",
  },
};

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

async function main(): Promise<number> {
  if (!platform)
    refuse("this session runs on Windows and macOS; on Linux, run.ts's local session runs these checks");
  if (process.env.GITHUB_ACTIONS !== "true" || process.env.RUNNER_ENVIRONMENT !== "github-hosted")
    refuse(
      "it runs only on a disposable GitHub-hosted runner (GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted): " +
        "on Windows it would use this account's own app folders",
    );

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
      try {
        steps.set(step, JSON.parse(json));
      } catch {
        steps.set(step, json);
      }
      order.push(step);
      // The window has closed: the app may quit once its checks are done.
      if (step === "closed") writeFileSync(quit, "");
    };
    if (proc.stdout) createInterface({ input: proc.stdout }).on("line", onLine);
    if (proc.stderr) createInterface({ input: proc.stderr }).on("line", onLine);
    const timer = setTimeout(() => {
      problems.push(`the app did not finish within ${TIMEOUT / 1000} s; stopped it`);
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
    for (const cmd of commands)
      check(`page tried ${cmd}`, page?.calls?.[cmd], `the page's probe did not try ${cmd}`);
    for (const [cmd, c] of Object.entries(page?.calls ?? {}))
      check(
        `page refused ${cmd}`,
        !c.ok && REFUSED.test(c.detail),
        `${cmd} from the page webview: ${JSON.stringify(c)}`,
      );

    const frame = steps.get("frame") as Record<string, { status: string; detail: string }> | undefined;
    check("frame report", frame && Object.keys(frame).length >= 10, "the page sent no report");
    for (const [what, r] of Object.entries(frame ?? {}))
      check(
        `frame ${what}`,
        r.status === "blocked" || (r.status === "attempted" && what === "native message handler"),
        `from the page's frame, "${what}" ${r.status}: ${r.detail}`,
      );
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
      check(
        "uia password field",
        uia?.found === true && uia?.is_password === true,
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
