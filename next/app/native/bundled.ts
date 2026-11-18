// The runner's side of the bundled core scenario on Linux (bundled-session.ts): a native session
// of its own (private session bus, X server and desktop services, like each mode's run in run.ts)
// whose program is the scenario, which starts the test package several times.

import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { makeProfile } from "./profile";
import { shotEnv } from "./shots";

export interface BundledSessionRun {
  /** The test package's test-package.json. */
  packageJson: string;
  /** examples/session_probe, and the smoke AppImage: what session.ts needs. */
  probe: string;
  appImage: string;
  timeoutMs: number;
  /** run.ts's bookkeeping for a session that has to be stopped. */
  sessions: Set<number>;
  stopSession: (pid: number) => Promise<void>;
  removeRun: (root: string) => Promise<void>;
}

/** The scenario's problems, or `setup` when session.ts failed before the scenario could start. */
export async function runBundled(o: BundledSessionRun): Promise<{ problems: string[]; setup?: string }> {
  const root = mkdtempSync(join(tmpdir(), "pluxy-bundled-"));
  try {
    const { env } = makeProfile(root);
    Object.assign(env, shotEnv("bundled"));
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      o.probe,
      o.appImage,
      randomUUID(),
      process.execPath,
      join(import.meta.dir, "bundled-session.ts"),
      o.packageJson,
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
        console.log(`  bundled | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      if (line.startsWith("bundled: ")) {
        console.log(`  ${line}`);
        if (line === "bundled: done") done = true;
        return;
      }
      if (line.startsWith("bundled-problem: ")) {
        problems.push(line.slice("bundled-problem: ".length));
        return;
      }
      log.push(line);
      if (log.length > 60) log.shift();
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
    // Done is not enough: whatever ended the session after that (a nonzero status, a signal) fails.
    if (!done) problems.push(`the scenario ended (${end}) before it was done`);
    else if (end !== "0" && problems.length === 0)
      problems.push(`the scenario was done but the session ended with ${end}`);
    if (problems.length > 0) console.log(`bundled: the session's last lines:\n  ${log.join("\n  ")}`);
    console.log(`bundled: ${problems.length === 0 ? "ok" : "FAILED"}`);
    return { problems };
  } finally {
    await o.removeRun(root);
  }
}
