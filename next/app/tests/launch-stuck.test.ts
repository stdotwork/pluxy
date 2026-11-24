// Launch.stuck (native/launch.ts) shows where an app that prints nothing is stuck: here the "app"
// is `sleep 300`, which never prints a step.

import { expect, test } from "bun:test";
import { Launch } from "../native/launch";

const win = process.platform === "win32";

test("a silent app is reported alive, with its counters and its process", async () => {
  const l = new Launch(
    win ? "ping" : "sleep",
    "silent",
    { ...process.env },
    win ? ["-n", "300", "127.0.0.1"] : ["300"],
  );
  try {
    expect(await l.next("bundled", 300)).toBeUndefined();
    const text = await l.stuck();
    console.log(text);
    expect(text).toContain(`app pid ${l.pid}: still running`);
    expect(text).toContain("printed 0 lines, 0 bytes");
    if (!win) {
      expect(text).toContain("sleep 300");
      expect(text).toContain("children: none");
    }
  } finally {
    l.kill();
    await l.exited;
  }
});

test("a gone app is reported gone, and nothing throws", async () => {
  const l = new Launch("sh", "gone", { ...process.env }, ["-c", "echo hi; echo err >&2"]);
  await l.exited;
  const text = await l.stuck();
  expect(text).toContain("gone (exit 0");
  expect(text).toContain("printed 2 lines, 7 bytes");
});
