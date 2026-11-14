// The runner's side of the single-instance scenario (instances-session.ts): a native session of
// its own (private session bus, X server and desktop services, like each mode's run in run.ts)
// whose program is the scenario, which launches the app several times.

import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { makeProfile } from "./profile";

export interface InstancesRun {
  bin: string;
  /** examples/session_probe, and the smoke AppImage: what session.ts needs. */
  probe: string;
  appImage: string;
  /** The core program the app runs in managed mode. */
  core: string;
  identifier: string;
  timeoutMs: number;
  /** run.ts's bookkeeping for a session that has to be stopped. */
  sessions: Set<number>;
  stopSession: (pid: number) => Promise<void>;
  removeRun: (root: string) => Promise<void>;
}

/** The scenario's problems, or `setup` when session.ts failed before the scenario could start. */
export async function runInstances(o: InstancesRun): Promise<{ problems: string[]; setup?: string }> {
  const root = mkdtempSync(join(tmpdir(), "pluxy-inst-"));
  try {
    const { dirs, env } = makeProfile(root);
    mkdirSync(join(dirs.config, o.identifier), { recursive: true });
    writeFileSync(
      join(dirs.config, o.identifier, "connection.json"),
      JSON.stringify({ mode: "managed", core_path: o.core }),
      { mode: 0o600 },
    );
    // The scenario closes the first's window to the tray (closing asks by default), as the
    // runner's main checks do.
    writeFileSync(
      join(dirs.config, o.identifier, "window.json"),
      JSON.stringify({ close_behavior: "minimize-to-tray" }),
      { mode: 0o600 },
    );
    const session = [
      process.execPath,
      join(import.meta.dir, "session.ts"),
      o.probe,
      o.appImage,
      randomUUID(),
      process.execPath,
      join(import.meta.dir, "instances-session.ts"),
      o.bin,
      o.core,
      o.identifier,
      o.probe,
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
        console.log(`  instances | ${line}`);
        if (line === "native-prereq: ready") ready = true;
        if (line.startsWith("native-prereq: FAILED "))
          setup.push(line.slice("native-prereq: FAILED ".length));
        return;
      }
      if (line.startsWith("instances: ")) {
        console.log(`  ${line}`);
        if (line === "instances: done") done = true;
        return;
      }
      if (line.startsWith("instances-problem: ")) {
        problems.push(line.slice("instances-problem: ".length));
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
    else if (end !== "0") problems.push(`the scenario was done but the session ended with ${end}`);
    if (problems.length > 0) console.log(`instances: the session's last lines:\n  ${log.join("\n  ")}`);
    console.log(`instances: ${problems.length === 0 ? "ok" : "FAILED"}`);
    return { problems };
  } finally {
    await o.removeRun(root);
  }
}
