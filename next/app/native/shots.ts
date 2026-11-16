// Screenshots of the native test's X server, for CI to keep as proof of what the app showed.
//
// run.ts and the other session starters name a PNG file for their session (shotEnv, only when
// PLUXY_APP_TEST_SHOTS names a folder); session.ts, which runs inside the session's X server,
// keeps it up to date (watchScreen). A session's windows are gone by the time it ends, so the
// root window is captured every few seconds while the app runs and the last capture that shows
// anything stays: the session's last screen. A capture that fails is logged once and never fails
// the session.

import { execFile } from "node:child_process";
import { mkdirSync, renameSync, rmSync } from "node:fs";
import { join } from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);

/** The variable that carries a session's screenshot file to session.ts. */
export const SHOT_FILE = "PLUXY_APP_TEST_SHOT";

const used = new Set<string>();

/** The session's screenshot file in the environment to give session.ts, or nothing when
 * PLUXY_APP_TEST_SHOTS is unset. A name used before gets a number, so no session overwrites
 * another's file. */
export function shotEnv(name: string): Record<string, string> {
  const dir = process.env.PLUXY_APP_TEST_SHOTS;
  if (!dir) return {};
  const base = name.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "") || "session";
  let file = base;
  for (let i = 2; used.has(file); i++) file = `${base}-${i}`;
  used.add(file);
  mkdirSync(dir, { recursive: true });
  return { [SHOT_FILE]: join(dir, `${file}.png`) };
}

/** Captures the root window to `file` every `everyMs` until the returned function is called, which
 * waits for a capture in flight. A capture that is one flat colour (an empty screen) is dropped,
 * so the file holds the last screen that showed a window. `say` gets one line when a capture
 * fails. Needs ImageMagick's `import` and `identify` and the session's DISPLAY. */
export function watchScreen(file: string, say: (line: string) => void, everyMs = 8000): () => Promise<void> {
  const part = `${file}.part`;
  let stopped = false;
  let failed = false;
  const capture = async () => {
    try {
      await run("import", ["-window", "root", `png:${part}`], { timeout: 20_000 });
      const { stdout } = await run("identify", ["-format", "%[fx:standard_deviation]", `png:${part}`], {
        timeout: 20_000,
      });
      if (Number(stdout) > 0) renameSync(part, file);
      else rmSync(part, { force: true });
    } catch (e) {
      rmSync(part, { force: true });
      if (!failed) {
        failed = true;
        say(`screenshot: ${(e as Error).message.split("\n")[0]} (the session goes on without one)`);
      }
    }
  };
  let current = Promise.resolve();
  const tick = () => {
    if (stopped) return;
    current = capture().then(() => {
      if (!stopped) timer = setTimeout(tick, everyMs);
    });
  };
  let timer: ReturnType<typeof setTimeout> | undefined = setTimeout(tick, 1000);
  return async () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    await current;
  };
}
