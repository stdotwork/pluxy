// The runner's side of the signed application update scenario (update-session.ts): a native
// session of its own (private session bus, X server and desktop services, like each mode's run
// in run.ts) whose program is the scenario, which launches the test package several times on
// one profile: a test trust profile holding the test package's updater key, and managed mode.

import { spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { PROFILE_FILE } from "./admission";
import { makeProfile } from "./profile";

export interface UpdateRun {
  /** package.ts test's test-package.json. */
  packageJson: string;
  /** The updater public key compiled into the test package. */
  pubkey: string;
  /** examples/session_probe, and the smoke AppImage: what session.ts needs. */
  probe: string;
  appImage: string;
  /** The core program the app runs in managed mode. */
  core: string;
  identifier: string;
  timeoutMs: number;
  sessions: Set<number>;
  stopSession: (pid: number) => Promise<void>;
  removeRun: (root: string) => Promise<void>;
}

export async function runUpdate(o: UpdateRun): Promise<{ problems: string[]; setup?: string }> {
  const root = mkdtempSync(join(tmpdir(), "pluxy-upd-"));
  try {
    const { dirs, env } = makeProfile(root);
    const work = join(root, "work");
    const tmp = join(root, "tmp");
    for (const d of [work, tmp]) mkdirSync(d, { mode: 0o700 });
    // Where an AppImage's runtime extracts it (APPIMAGE_EXTRACT_AND_RUN), inside the run.
    env.TMPDIR = tmp;
    mkdirSync(join(dirs.config, o.identifier), { recursive: true });
    writeFileSync(
      join(dirs.config, o.identifier, "connection.json"),
      JSON.stringify({ mode: "managed", core_path: o.core }),
      { mode: 0o600 },
    );
    // The app runs only a core in its own store: adopt the core program first, as the window's
    // Adopt would (and as run.ts and instances-session.ts do for their managed runs).
    const adopted = spawnSync(o.probe, ["adopt-core", join(dirs.data, o.identifier, "cores"), o.core], {
      encoding: "utf8",
    });
    if (adopted.status !== 0)
      return { problems: [`session_probe adopt-core exited with ${adopted.status}: ${adopted.stderr}`] };
    writeFileSync(
      join(dirs.config, o.identifier, "window.json"),
      JSON.stringify({ close_behavior: "minimize-to-tray" }),
      { mode: 0o600 },
    );
    // The test trust profile: the marker (with its nonce) and the test package's updater key.
    const marker = spawnSync(o.probe, ["trust-marker", dirs.home], { encoding: "utf8" });
    const nonce = /^nonce=([0-9a-f]{64})$/m.exec(marker.stdout ?? "")?.[1];
    if (!nonce) return { problems: [`the test trust marker was not written: ${marker.stderr}`] };
    writeFileSync(
      join(dirs.home, PROFILE_FILE),
      JSON.stringify({ roots: [], updater: { kind: "test", public_key: o.pubkey } }),
      { mode: 0o600 },
    );
    env.PLUXY_TEST_TRUST_NONCE = nonce;
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      o.probe,
      o.appImage,
      randomUUID(),
      process.execPath,
      join(import.meta.dir, "update-session.ts"),
      work,
      o.packageJson,
      o.pubkey,
    ];
    const xvfb = ["xvfb-run", "-a", "-s", "-screen 0 1280x800x24 -nolisten tcp", ...session];
    const proc = spawn("dbus-run-session", ["--", ...xvfb], {
      env,
      stdio: ["ignore", "pipe", "pipe"],
      detached: true,
    });
    const pid = proc.pid ?? 0;
    o.sessions.add(pid);
    if (!proc.stderr || !proc.stdout) throw new Error("the session's output is not piped");
    const problems: string[] = [];
    const setup: string[] = [];
    let ready = false;
    let done = false;
    const log: string[] = [];
    const onLine = (line: string) => {
      if (line.startsWith("native-prereq: ")) {
        console.log(`  update | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      if (line.startsWith("update: ")) {
        console.log(`  ${line}`);
        if (line === "update: done" || line === "update: FAILED") done = true;
        return;
      }
      if (line.startsWith("update-problem: ")) {
        problems.push(line.slice("update-problem: ".length));
        return;
      }
      log.push(line);
      if (log.length > 80) log.shift();
    };
    for (const stream of [proc.stdout, proc.stderr]) createInterface({ input: stream }).on("line", onLine);
    let stopping: Promise<void> | undefined;
    const end = await new Promise<string>((resolve) => {
      const timer = setTimeout(() => {
        problems.push(`the scenario did not finish within ${o.timeoutMs / 1000}s`);
        stopping = o.stopSession(pid);
      }, o.timeoutMs);
      proc.on("close", (c, signal) => {
        clearTimeout(timer);
        resolve(signal ?? String(c));
      });
    });
    await stopping;
    o.sessions.delete(pid);
    if (!ready) {
      return {
        problems,
        setup: setup.length > 0 ? setup.join("\n") : `the session ended (${end}) before it was ready`,
      };
    }
    if (!done) problems.push(`the scenario ended (${end}) before it was done`);
    else if (end !== "0") problems.push(`the scenario was done but the session ended with ${end}`);
    if (problems.length > 0) console.log(`update: the session's last lines:\n  ${log.join("\n  ")}`);
    console.log(`update: ${problems.length === 0 ? "ok" : "FAILED"}`);
    return { problems };
  } finally {
    await o.removeRun(root);
  }
}
