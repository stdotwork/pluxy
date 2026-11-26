// Launch (native/launch.ts) reads the app's `native-test:` steps. An app killed in the middle of
// printing one leaves a last line with no newline and a cut-off JSON: that is a symptom of the
// kill, not a crash of the session reading it. A complete line that is not JSON still throws.
// The "apps" here are `sh` printing what a real one would.

import { expect, test } from "bun:test";
import { Launch } from "../native/launch";

const win = process.platform === "win32";

/** An app that prints `script`'s output and exits. */
function app(script: string): Launch {
  return new Launch("sh", "cutoff", { ...process.env }, ["-c", script]);
}

test("a step cut off at the end of the output is not parsed, and does not throw", async () => {
  if (win) return;
  // The app wrote "native-test: reached " and was killed before the JSON: the value is empty.
  const l = app(`printf 'native-test: up {"n":1}\\nnative-test: reached '`);
  expect(await l.exited).toBe(0);
  expect(await l.next("up", 1000)).toEqual({ n: 1 });
  expect(l.printed("reached")).toBe(false);
  expect(l.cutOff).toEqual(["native-test: reached "]);
  expect(l.dump()).toContain("cut off at the end");
});

test("a step cut off in the middle of its JSON is not parsed either", async () => {
  if (win) return;
  const l = app(`printf 'native-test: state {"exists":tr'`);
  expect(await l.exited).toBe(0);
  expect(l.printed("state")).toBe(false);
  expect(l.cutOff).toEqual(['native-test: state {"exists":tr']);
});

test("a last step with no newline but complete JSON is still a step", async () => {
  if (win) return;
  const l = app(`printf 'native-test: state {"exists":true}'`);
  expect(await l.exited).toBe(0);
  expect(await l.next("state", 1000)).toEqual({ exists: true });
  expect(l.cutOff).toEqual([]);
});

test("a complete line that is not JSON still throws", async () => {
  if (win) return;
  const l = new Launch("sleep", "bad", { ...process.env }, ["300"]);
  try {
    // Not the last line of a stream that ended: the app is still running.
    const feed = l as unknown as { onLine(line: string, final?: boolean): void };
    expect(() => feed.onLine("native-test: state {not json")).toThrow(SyntaxError);
    expect(() => feed.onLine("native-test: state ")).toThrow(SyntaxError);
  } finally {
    l.kill();
    await l.exited;
  }
});

test("stuck names the helper that keeps an app's output open after the app is gone", async () => {
  if (win || process.platform !== "linux") return;
  // The app prints, lingers briefly, and exits; its helper (what a webview's process is) holds the pipes for 2 s.
  const l = app("echo hi; sleep 2 & sleep 0.5");
  await new Promise((r) => l.child.once("exit", r));
  const text = await l.stuck();
  expect(text).toContain("gone (exit 0");
  expect(text).toMatch(/processes holding its stdout or stderr open: .*:fd[12]:sleep:[A-Z]/);
  expect(await l.exitWithin(100)).toBe("timeout");
  expect(await l.exited).toBe(0);
});
