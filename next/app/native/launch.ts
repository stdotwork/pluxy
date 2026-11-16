// One launch of the real app (built with the native-test feature), for the scenarios that start it
// several times (instances-session.ts on Linux, platform.ts on Windows and macOS): the app's
// `native-test:` steps by name, its stdin for the commands it answers, and its exit.

import { type ChildProcess, spawn } from "node:child_process";
import { createInterface } from "node:readline";

export type Step = Record<string, unknown>;

export class Launch {
  readonly child: ChildProcess;
  readonly exited: Promise<number | null>;
  private readonly steps = new Map<string, Step[]>();
  private waiting = new Map<string, (s: Step) => void>();
  readonly log: string[] = [];
  readonly pid: number;

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
      if (stream) createInterface({ input: stream }).on("line", (l) => this.onLine(l));
    }
  }

  private onLine(line: string) {
    this.log.push(line);
    if (this.log.length > 40) this.log.shift();
    const m = /^native-test: (\w+) (.*)$/.exec(line);
    if (!m) return;
    const value = JSON.parse(m[2] as string) as Step;
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
    return this.log.join(" | ");
  }
}
