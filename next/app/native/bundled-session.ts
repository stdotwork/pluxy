// The core that comes with the app on Linux (bundled-steps.ts), run inside a native session
// (session.ts starts it in place of the app; bundled.ts starts the session):
//
//   bun native/bundled-session.ts <test package JSON>
//
// It starts the test package's AppImages (package.ts test) as a person runs them, each profile a
// HOME and XDG folders of its own. A changeable copy of a package is the AppImage extracted
// (--appimage-extract) and started through its AppRun, so the app runs from the same tree, with
// its bundled packages in usr/lib/pluxy Test/core/bundled.
//
// Output: `bundled: ...` lines, and `bundled-problem: ...` for each failed check.

import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { type BundledApp, bundledScenario } from "./bundled-steps";
import { Launch } from "./launch";
import { makeProfile } from "./profile";

const [packageJson] = process.argv.slice(2);
if (!packageJson) {
  console.error("usage: bun native/bundled-session.ts <test package JSON>");
  process.exit(2);
}
const ID = "dev.pluxy.desktop.test";
const PRODUCT = "pluxy Test";

interface TestPackage {
  appimage: string;
  bundled_core: { version: string; packages: string[] };
  update: { appimage: string; version: string; bundled_core: { version: string; packages: string[] } };
}
const pkg: TestPackage = JSON.parse(readFileSync(packageJson, "utf8"));

let problems = 0;
const say = (line: string) => console.log(`bundled: ${line}`);
function check(ok: unknown, what: string) {
  if (!ok) {
    problems++;
    console.log(`bundled-problem: ${what}`);
  }
}

const roots: string[] = [];
const launches: Launch[] = [];
const root = (label: string) => {
  const r = mkdtempSync(join(tmpdir(), `pluxy-bundled-${label}-`));
  roots.push(r);
  return r;
};

const first: BundledApp = {
  label: `app ${JSON.parse(readFileSync(join(import.meta.dir, "..", "src-tauri", "tauri.conf.json"), "utf8")).version}`,
  program: pkg.appimage,
  core: pkg.bundled_core.version,
  packages: pkg.bundled_core.packages,
};
const update: BundledApp = {
  label: `app ${pkg.update.version}`,
  program: pkg.update.appimage,
  core: pkg.update.bundled_core.version,
  packages: pkg.update.bundled_core.packages,
};

try {
  await bundledScenario({
    first,
    update,
    profile: (label) => {
      const r = root(label);
      const { dirs, env } = makeProfile(r);
      return {
        // The session's display and bus, the profile's own folders.
        env: { ...process.env, ...env, XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR, TMPDIR: join(r, "tmp") },
        secrets: join(dirs.config, ID),
        cores: join(dirs.data, ID, "cores"),
        remove: () => rmSync(r, { recursive: true, force: true }),
      };
    },
    copy: (app, label) => {
      const r = root(`copy-${label}`);
      const x = spawnSync(app.program, ["--appimage-extract"], {
        cwd: r,
        env: { ...process.env, APPIMAGE_EXTRACT_AND_RUN: "" },
        stdio: ["ignore", "ignore", "inherit"],
      });
      const tree = join(r, "squashfs-root");
      if (x.status !== 0 || !existsSync(join(tree, "AppRun")))
        throw new Error(`${app.program} --appimage-extract exited with ${x.status}`);
      return { program: join(tree, "AppRun"), bundled: join(tree, "usr", "lib", PRODUCT, "core", "bundled") };
    },
    launch: (program, label, env) => {
      const l = new Launch(program, label, env);
      launches.push(l);
      return l;
    },
    check,
    say,
  });
} catch (e) {
  check(false, `the scenario failed: ${e instanceof Error ? e.stack : String(e)}`);
} finally {
  for (const l of launches) l.kill("SIGKILL");
  for (const r of roots) rmSync(r, { recursive: true, force: true });
}
say("done");
process.exit(problems > 0 ? 1 : 0);
