// One launch of the real app (built with the native-test feature), for the scenarios that start it
// several times (instances-session.ts on Linux, platform.ts on Windows and macOS): the app's
// `native-test:` steps by name, its stdin for the commands it answers, and its exit.

import { type ChildProcess, execFile, spawn } from "node:child_process";
import { readdirSync, readFileSync, readlinkSync } from "node:fs";
import { createInterface } from "node:readline";

export type Step = Record<string, unknown>;

export class Launch {
  readonly child: ChildProcess;
  /** The app's exit code, once it has exited and every process holding its stdout and stderr has
   * let them go: its webview's helpers do, so this can come later than the app's exit (`stuck`
   * names them). */
  readonly exited: Promise<number | null>;
  private readonly steps = new Map<string, Step[]>();
  private waiting = new Map<string, (s: Step) => void>();
  readonly log: string[] = [];
  readonly pid: number;
  /** What the app has printed so far on stdout and stderr, in lines and bytes (never trimmed). */
  lines = 0;
  bytes = 0;
  /** Final lines of its output that were cut off in the middle of a `native-test:` step's JSON:
   * the app was killed (or died) while printing it. Never a step. */
  readonly cutOff: string[] = [];
  /** Linux: what its stdout and stderr are (`pipe:[inode]`), read once it has printed, for
   * `stuck`: its `close` waits for every process holding them, not only for the app. */
  private pipes: string[] = [];

  constructor(
    bin: string,
    readonly label: string,
    env: Record<string, string | undefined>,
    args: string[] = [],
    /** Windows: pass args to the program as they are, for a command line that is not the usual quoting's (cmd /c). */
    verbatim = false,
  ) {
    this.child = spawn(bin, args, {
      env,
      stdio: ["pipe", "pipe", "pipe"],
      windowsVerbatimArguments: verbatim,
    });
    this.pid = this.child.pid ?? 0;
    this.exited = new Promise((resolve) => this.child.once("close", (code) => resolve(code)));
    this.child.stdin?.on("error", () => {});
    for (const stream of [this.child.stdout, this.child.stderr]) {
      if (!stream) continue;
      stream.on("data", (chunk: Buffer | string) => {
        this.bytes += Buffer.byteLength(chunk);
      });
      // Set before readline's own end handler runs, which emits a last line that has no newline.
      let ended = false;
      stream.once("end", () => {
        ended = true;
      });
      createInterface({ input: stream }).on("line", (l) => this.onLine(l, ended));
    }
  }

  /** final: the last line of a stream that ended, which has no newline after it when the app was
   * killed in the middle of printing it. */
  private onLine(line: string, final = false) {
    this.lines++;
    this.log.push(line);
    if (this.log.length > 40) this.log.shift();
    if (this.pipes.length === 0 && process.platform === "linux") {
      // The app has printed, so it has run its program: its fds are the pipes it was given.
      for (const fd of [1, 2]) {
        try {
          this.pipes.push(readlinkSync(`/proc/${this.pid}/fd/${fd}`));
        } catch {
          // gone already
        }
      }
    }
    const m = /^native-test: (\w+) (.*)$/.exec(line);
    if (!m) return;
    let value: Step;
    try {
      value = JSON.parse(m[2] as string) as Step;
    } catch (e) {
      // A step cut off at the end of the output was never completed, so nothing waits for it as
      // printed (a `next` that wanted it times out and says so). A line that is complete and
      // not JSON is a failure of the app's output, and still throws.
      if (!final) throw e;
      this.cutOff.push(line);
      return;
    }
    const wake = this.waiting.get(m[1] as string);
    if (wake) {
      this.waiting.delete(m[1] as string);
      wake(value);
    } else {
      const queue = this.steps.get(m[1] as string) ?? [];
      queue.push(value);
      this.steps.set(m[1] as string, queue);
    }
  }

  /** Whether the app printed this step (without taking it). */
  printed(step: string): boolean {
    return (this.steps.get(step)?.length ?? 0) > 0;
  }

  /** The app's next step of this name, or undefined if it does not come in time. */
  next(step: string, ms: number): Promise<Step | undefined> {
    const queued = this.steps.get(step)?.shift();
    if (queued) return Promise.resolve(queued);
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.waiting.delete(step);
        resolve(undefined);
      }, ms);
      this.waiting.set(step, (s) => {
        clearTimeout(timer);
        resolve(s);
      });
    });
  }

  /** Sends a command to the app and returns the window's state it answers with. */
  async command(cmd: string): Promise<Step | undefined> {
    this.send(cmd);
    return this.next("state", 15_000);
  }

  send(cmd: string) {
    this.child.stdin?.write(`${cmd}\n`);
  }

  kill(signal: NodeJS.Signals = "SIGKILL") {
    try {
      this.child.kill(signal);
    } catch {
      // gone already
    }
  }

  async exitWithin(ms: number): Promise<number | null | "timeout"> {
    return Promise.race([this.exited, Bun.sleep(ms).then(() => "timeout" as const)]);
  }

  dump(): string {
    const cut = this.cutOff.length > 0 ? ` | (cut off at the end: ${this.cutOff.join(" | ")})` : "";
    return this.log.join(" | ") + cut;
  }

  /** Linux: the processes (pid:fd:name) that hold the app's stdout or stderr open now, the app
   * itself among them while it runs; undefined when the app printed nothing (its pipes are not
   * known) or off Linux. A helper the app started (a webview's) that outlives it keeps `exited`
   * from resolving, since that waits for the pipes to close. */
  holders(): string | undefined {
    if (this.pipes.length === 0) return undefined;
    const out: string[] = [];
    try {
      for (const d of readdirSync("/proc")) {
        if (!/^\d+$/.test(d)) continue;
        for (const fd of [1, 2]) {
          try {
            if (!this.pipes.includes(readlinkSync(`/proc/${d}/fd/${fd}`))) continue;
            const stat = readFileSync(`/proc/${d}/stat`, "utf8");
            const state = stat.slice(stat.lastIndexOf(")") + 2, stat.lastIndexOf(")") + 3);
            out.push(`${d}:fd${fd}:${stat.slice(stat.indexOf("(") + 1, stat.lastIndexOf(")"))}:${state}`);
          } catch {
            // another user's, or gone
          }
        }
      }
    } catch (e) {
      return `unreadable: ${e}`;
    }
    return out.join(" ");
  }

  /** Where a silent app is stuck: whether it is alive, what it printed, and (not on Windows) its
   * process, its children and, on macOS, a 3 s sample. Each command gets 10 s; errors become text,
   * nothing throws. Call it before kill(). */
  async stuck(): Promise<string> {
    const alive = this.pid > 0 && this.child.exitCode === null && this.child.signalCode === null;
    const out = [
      `app pid ${this.pid}: ${alive ? "still running" : `gone (exit ${this.child.exitCode}, signal ${this.child.signalCode})`}`,
      `printed ${this.lines} lines, ${this.bytes} bytes on stdout and stderr`,
    ];
    const held = this.holders();
    if (held !== undefined) out.push(`processes holding its stdout or stderr open: ${held || "none"}`);
    if (process.platform !== "win32" && alive) {
      try {
        const psCols = ["-o", "pid,ppid,stat,etime,pcpu,rss,command"];
        out.push(await tool("ps", [...psCols, "-p", String(this.pid)]));
        const kids = (await tool("pgrep", ["-P", String(this.pid)], false))
          .split("\n")
          .filter((x) => /^\d+$/.test(x.trim()))
          .map((x) => x.trim());
        out.push(`children: ${kids.length ? kids.join(" ") : "none"}`);
        if (kids.length) out.push(await tool("ps", [...psCols, "-p", kids.join(",")]));
        if (process.platform === "darwin") {
          const sample = (await tool("sample", [String(this.pid), "3"])).split("\n");
          out.push(sample.slice(0, 80).join("\n"));
        }
      } catch (e) {
        out.push(`diagnostics failed: ${e}`);
      }
    }
    return out.join("\n");
  }
}

/** Runs a command with a 10 s timeout and returns its output, or what went wrong, as text. */
function tool(cmd: string, args: string[], label = true): Promise<string> {
  return new Promise((resolve) => {
    try {
      execFile(cmd, args, { timeout: 10_000, maxBuffer: 4 << 20 }, (err, stdout, stderr) => {
        const text = `${stdout}${stderr}`.trimEnd();
        const head = label ? `$ ${cmd} ${args.join(" ")}\n` : "";
        resolve(err && !text ? `${head}${cmd} failed: ${err.message}` : `${head}${text}`);
      });
    } catch (e) {
      resolve(`${cmd} failed: ${e}`);
    }
  });
}
