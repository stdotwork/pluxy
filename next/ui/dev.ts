// Panel edit loop: watches next/ui, rebuilds the panel into next/internal/panel/dist, and
// restarts the demo panelcore on the same loopback port so a browser reload shows each change.
// Run from next/ with: make dev (or make dev PORT=8331).

import { type FSWatcher, mkdirSync, mkdtempSync, rmSync, watch } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const uiDir = import.meta.dir;
const nextDir = join(uiDir, "..");
const watchDirs = ["kit", "panel", "themes", "extension-pages"];
const defaultPort = 8330;
const debounceMs = 100;
const stopTimeoutMs = 12_000;
const exe = process.platform === "win32" ? ".exe" : "";

interface ReadyLine {
  base: string;
  key: string;
}

interface RunningCore {
  proc: Bun.Subprocess<"pipe", "pipe", "pipe">;
  stopping: boolean;
}

function parsePort(argv: string[]): number {
  let raw = process.env.PORT ?? String(defaultPort);
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--port") {
      const next = argv[i + 1];
      if (!next) {
        console.error("dev: --port requires a port number");
        process.exit(2);
      }
      raw = next;
      i++;
    } else if (arg?.startsWith("--port=")) {
      raw = arg.slice("--port=".length);
    } else {
      console.error(`dev: unknown argument ${arg}`);
      process.exit(2);
    }
  }
  if (!/^\d+$/.test(raw)) {
    console.error(`dev: invalid port ${raw}`);
    process.exit(2);
  }
  const port = Number(raw);
  if (port < 1 || port > 65535) {
    console.error(`dev: port must be between 1 and 65535, got ${port}`);
    process.exit(2);
  }
  return port;
}

async function goEnvValues(): Promise<Record<string, string>> {
  const proc = Bun.spawn(["go", "env", "GOCACHE", "GOPATH", "GOMODCACHE"], {
    cwd: nextDir,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [out, code] = await Promise.all([new Response(proc.stdout).text(), proc.exited]);
  if (code !== 0) return {};
  const lines = out.trim().split(/\r?\n/);
  const result: Record<string, string> = {};
  if (lines[0]) result.GOCACHE = lines[0];
  if (lines[1]) result.GOPATH = lines[1];
  if (lines[2]) result.GOMODCACHE = lines[2];
  return result;
}

function makeChildEnv(homeDir: string, goEnv: Record<string, string>): Record<string, string> {
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v === undefined) continue;
    const upper = k.toUpperCase();
    if (upper === "HOME" || upper === "USERPROFILE" || upper.startsWith("XDG_")) continue;
    env[k] = v;
  }
  env.HOME = homeDir;
  env.USERPROFILE = homeDir;
  for (const [k, v] of Object.entries(goEnv)) {
    if (!env[k]) env[k] = v;
  }
  return env;
}

async function runPanelBuild(): Promise<{ ok: boolean; output: string }> {
  const proc = Bun.spawn([process.execPath, join(uiDir, "build.ts")], {
    cwd: uiDir,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  const output = [stderr.trim(), stdout.trim()].filter(Boolean).join("\n");
  return { ok: code === 0, output };
}

async function buildPanelcore(binPath: string, env: Record<string, string>): Promise<void> {
  const proc = Bun.spawn(["go", "build", "-buildvcs=false", "-o", binPath, "./e2e/panelcore"], {
    cwd: nextDir,
    env,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stderr, code] = await Promise.all([new Response(proc.stderr).text(), proc.exited]);
  if (code !== 0) {
    throw new Error(`building panelcore failed:\n${stderr.trim()}`);
  }
}

async function readFirstLine(stream: ReadableStream<Uint8Array>): Promise<string | null> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) return buf.length > 0 ? buf : null;
      buf += decoder.decode(value, { stream: true });
      const nl = buf.indexOf("\n");
      if (nl !== -1) return buf.slice(0, nl).trim();
    }
  } finally {
    reader.releaseLock();
  }
}

async function drainLines(stream: ReadableStream<Uint8Array>, tail: string[]): Promise<void> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let nl = buf.indexOf("\n");
      while (nl !== -1) {
        tail.push(buf.slice(0, nl));
        if (tail.length > 40) tail.shift();
        buf = buf.slice(nl + 1);
        nl = buf.indexOf("\n");
      }
    }
    if (buf.trim().length > 0) {
      tail.push(buf.trim());
      if (tail.length > 40) tail.shift();
    }
  } catch {
    // Stream closed during shutdown.
  } finally {
    reader.releaseLock();
  }
}

async function stopCore(running: RunningCore | null): Promise<void> {
  if (!running) return;
  running.stopping = true;
  try {
    running.proc.stdin.end();
  } catch {
    // Already closed.
  }
  try {
    running.proc.kill("SIGTERM");
  } catch {
    // Already exited.
  }
  const timer = setTimeout(() => {
    try {
      running.proc.kill("SIGKILL");
    } catch {
      // Already exited.
    }
  }, stopTimeoutMs);
  try {
    await running.proc.exited;
  } finally {
    clearTimeout(timer);
  }
}

async function drainStream(stream: ReadableStream<Uint8Array>): Promise<void> {
  const reader = stream.getReader();
  try {
    while (true) {
      const { done } = await reader.read();
      if (done) break;
    }
  } catch {
    // Stream closed during shutdown.
  } finally {
    reader.releaseLock();
  }
}

async function startCore(
  binPath: string,
  port: number,
  keyFile: string,
  env: Record<string, string>,
): Promise<{ core: RunningCore; ready: ReadyLine }> {
  const proc = Bun.spawn([binPath, "-demo", "-listen", `127.0.0.1:${port}`, "-key-file", keyFile], {
    cwd: nextDir,
    env,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  });
  const running: RunningCore = { proc, stopping: false };
  const stderrTail: string[] = [];
  const stderrDone = drainLines(proc.stderr, stderrTail);

  const line = await readFirstLine(proc.stdout);
  if (!line) {
    await Promise.all([proc.exited, stderrDone]);
    const detail = stderrTail.join("\n").trim();
    throw new Error(detail ? `panelcore exited before ready:\n${detail}` : "panelcore exited before ready");
  }
  let parsed: Partial<ReadyLine>;
  try {
    parsed = JSON.parse(line) as Partial<ReadyLine>;
  } catch (err) {
    await stopCore(running);
    throw new Error(`panelcore printed unexpected output: ${line} (${String(err)})`);
  }
  if (!parsed.base || !parsed.key) {
    await stopCore(running);
    throw new Error(`panelcore ready line missing base or key: ${line}`);
  }
  // Keep draining stdout so the child never blocks if anything else is written.
  void drainStream(proc.stdout);
  return { core: running, ready: { base: parsed.base, key: parsed.key } };
}

function isIgnoredName(filename: string | null): boolean {
  if (!filename) return false;
  const base = filename.split(/[/\\]/).pop() ?? filename;
  return base.endsWith("~") || base.endsWith(".swp") || base.endsWith(".tmp") || base.startsWith(".#");
}

async function main(): Promise<void> {
  const port = parsePort(process.argv.slice(2));
  const tmp = mkdtempSync(join(tmpdir(), "pluxy-dev-"));
  const homeDir = join(tmp, "home");
  mkdirSync(homeDir, { recursive: true });

  const watchers: FSWatcher[] = [];
  let current: RunningCore | null = null;
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;
  let rebuilding = false;
  let pending = false;
  let shuttingDown = false;
  let shutdownPromise: Promise<void> | null = null;

  const cleanup = async (): Promise<void> => {
    if (shutdownPromise) return shutdownPromise;
    shuttingDown = true;
    shutdownPromise = (async () => {
      if (debounceTimer) {
        clearTimeout(debounceTimer);
        debounceTimer = null;
      }
      for (const w of watchers) w.close();
      await stopCore(current);
      current = null;
      rmSync(tmp, { recursive: true, force: true });
    })();
    return shutdownPromise;
  };

  const onSignal = (): void => {
    void cleanup().finally(() => process.exit(0));
  };
  process.on("SIGINT", onSignal);
  process.on("SIGTERM", onSignal);

  try {
    const goEnv = await goEnvValues();
    const childEnv = makeChildEnv(homeDir, goEnv);
    const panelcoreBin = join(tmp, `panelcore${exe}`);
    const keyFile = join(tmp, "mgmt.key");

    const [buildRes] = await Promise.all([runPanelBuild(), buildPanelcore(panelcoreBin, childEnv)]);
    if (!buildRes.ok) {
      console.error(`panel build failed:\n${buildRes.output}`);
      await cleanup();
      process.exit(1);
    }

    const started = await startCore(panelcoreBin, port, keyFile, childEnv);
    current = started.core;
    console.log(`URL: ${started.ready.base}/panel/`);
    console.log(`Key: ${started.ready.key}`);

    const rebuild = async (): Promise<void> => {
      if (shuttingDown) return;
      if (rebuilding) {
        pending = true;
        return;
      }
      rebuilding = true;
      do {
        pending = false;
        const t0 = performance.now();
        const res = await runPanelBuild();
        if (shuttingDown) break;
        if (!res.ok) {
          console.error(`build failed:\n${res.output}`);
          continue;
        }
        await stopCore(current);
        current = null;
        if (shuttingDown) break;
        try {
          const next = await startCore(panelcoreBin, port, keyFile, childEnv);
          if (shuttingDown) {
            await stopCore(next.core);
            break;
          }
          current = next.core;
          const elapsed = ((performance.now() - t0) / 1000).toFixed(2);
          console.log(`rebuilt in ${elapsed}s`);
        } catch (err) {
          console.error(`restart failed: ${err instanceof Error ? err.message : String(err)}`);
        }
      } while (pending && !shuttingDown);
      rebuilding = false;
    };

    const scheduleRebuild = (_event: string, filename: string | null): void => {
      if (shuttingDown || isIgnoredName(filename)) return;
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        void rebuild();
      }, debounceMs);
    };

    for (const dir of watchDirs) {
      watchers.push(watch(join(uiDir, dir), { recursive: true }, scheduleRebuild));
    }
  } catch (err) {
    console.error(err instanceof Error ? err.message : String(err));
    await cleanup();
    process.exit(1);
  }
}

void main();
