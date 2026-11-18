// The core that comes with the app, on the real test package (package.ts test), in managed mode:
// the scenario shared by the Linux session (bundled-session.ts, inside a native session) and the
// Windows and macOS one (platform.ts, the package shard). The app reports what it sees with
// PLUXY_APP_TEST_BUNDLED (src-tauri/src/bundled_native.rs) and quits; this checks:
//
// - a fresh profile in managed mode runs the core the app came with, with no confirmation: the
//   running core's program is in the app's own cores (not beside the app), as a version that came
//   with the app, and the packages its bundled/index.json lists are installed;
// - the update build (another app version, with another core) moves the selection the app made
//   to its own core; the person then rolls back to the earlier core through the native
//   confirmation; that choice stays when the earlier app runs again and when the update build
//   runs again, though each is a change of the app's version;
// - a file in bundled/ that the app does not list is not copied into its cores;
// - a changed bundled/index.json is refused with the damaged message, and nothing is installed;
// - a version in the app's cores with the bundled core's number but other files gives the
//   conflict message, and stays as it was.
//
// Each case starts from a fresh profile. Nothing here is specific to one system: the session
// gives how to make a profile and a changeable copy of the package.

import { createHash } from "node:crypto";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Launch, Step } from "./launch";

/** One build of the test package and the core it comes with (test-package.json). */
export interface BundledApp {
  label: string;
  /** The program to start: the AppImage, the folder bundle's exe, or the app bundle's program. */
  program: string;
  core: string;
  /** The package IDs its bundled/index.json lists. */
  packages: string[];
}

/** A changeable copy of a package: its program, and the folder its bundled/ files are in. */
export interface PackageCopy {
  program: string;
  bundled: string;
}

/** A fresh profile of the test package's identifier. */
export interface BundledProfile {
  env: Record<string, string | undefined>;
  /** Where the app keeps connection.json. */
  secrets: string;
  /** The app's own cores. */
  cores: string;
  remove: () => void;
}

export interface BundledRun {
  first: BundledApp;
  update: BundledApp;
  profile: (label: string) => BundledProfile;
  copy: (app: BundledApp, label: string) => PackageCopy;
  launch: (program: string, label: string, env: Record<string, string | undefined>) => Launch;
  check: (ok: unknown, what: string) => void;
  say: (line: string) => void;
}

const REPORT_MS = 240_000;
const EXIT_MS = 60_000;
/** The core's program in a version's folder (cores.rs PROGRAM). */
const PROGRAM = process.platform === "win32" ? "pluxy.exe" : "pluxy";

/** Starts program on profile in managed mode, takes its report (and, with rollback, the report
 * after rolling back to that version), and waits for it to quit by itself. */
async function report(
  o: BundledRun,
  program: string,
  label: string,
  p: BundledProfile,
  packages: number,
  rollback?: string,
): Promise<{ first?: Step; after?: Step }> {
  const l = o.launch(program, label, {
    ...p.env,
    PLUXY_APP_TEST_BUNDLED: rollback ? `rollback:${rollback}` : "report",
    PLUXY_APP_TEST_BUNDLED_PACKAGES: String(packages),
  });
  const first = await l.next("bundled", REPORT_MS);
  o.check(first, `${label}: the app never reported its core: ${l.dump()}`);
  const after = rollback && first ? await l.next("bundled_rollback", REPORT_MS) : undefined;
  if (rollback) o.check(after, `${label}: the app never reported the rollback: ${l.dump()}`);
  const code = await l.exitWithin(EXIT_MS);
  if (code === "timeout") l.kill();
  o.check(code === 0, `${label}: the app exited with ${code}: ${l.dump()}`);
  if (first) o.say(`${label}: ${JSON.stringify(summary(first))}`);
  if (after) o.say(`${label}, after the rollback: ${JSON.stringify(summary(after))}`);
  return { first, after };
}

type Report = {
  core: { state?: string; version?: string; detail?: string; pid?: number };
  versions: {
    installed: { id: string; kind: string }[];
    bundled?: { version: string; problem?: string };
    outcome?: unknown;
  };
  exe_in_store: boolean;
  store_files: string[];
  packages: string[];
  state: { selected?: string; chosen_by?: string; bundled_for?: string } | null;
};

const summary = (s: Step) => {
  const r = s as unknown as Report;
  return {
    state: r.core?.state,
    version: r.core?.version,
    detail: r.core?.detail,
    exe_in_store: r.exe_in_store,
    installed: r.versions?.installed,
    bundled: r.versions?.bundled,
    selection: r.state,
    packages: r.packages?.length,
  };
};

function connection(p: BundledProfile) {
  mkdirSync(p.secrets, { recursive: true });
  writeFileSync(join(p.secrets, "connection.json"), JSON.stringify({ mode: "managed" }), { mode: 0o600 });
}

/** The runs on one profile: a fresh start, the update build, the person's rollback, and both app
 * versions again. */
async function versions(o: BundledRun) {
  const p = o.profile("versions");
  try {
    connection(p);
    const { first } = await report(
      o,
      o.first.program,
      `${o.first.label}, fresh profile`,
      p,
      o.first.packages.length,
    );
    if (first) {
      const r = first as unknown as Report;
      o.check(
        r.core.state === "running" && r.core.version === o.first.core,
        `a fresh profile in managed mode does not run the bundled core ${o.first.core}: ${JSON.stringify(r.core)}`,
      );
      o.check(r.exe_in_store, "the running core's program is not in the app's own cores");
      o.check(
        r.versions.installed.some((i) => i.id === o.first.core && i.kind === "bundled"),
        `core ${o.first.core} is not installed as the core that came with the app: ${JSON.stringify(r.versions.installed)}`,
      );
      o.check(r.versions.outcome === undefined, "a core change was asked of the person");
      o.check(
        o.first.packages.length > 0 && o.first.packages.every((id) => r.packages.includes(id)),
        `the running core has not installed its bundled packages ${o.first.packages.join(", ")}: it has ${r.packages.join(", ")}`,
      );
      o.check(
        r.state?.chosen_by === "app",
        `the selection is not recorded as the app's: ${JSON.stringify(r.state)}`,
      );
    }
    // The update build: another app version, which moves the app's own selection to its core;
    // then the person rolls back to the earlier one.
    const { first: moved, after } = await report(
      o,
      o.update.program,
      `${o.update.label}, then a rollback`,
      p,
      0,
      o.first.core,
    );
    if (moved) {
      const r = moved as unknown as Report;
      o.check(
        r.core.state === "running" && r.core.version === o.update.core,
        `the other app version did not move the app's selection to its core ${o.update.core}: ${JSON.stringify(r.core)}`,
      );
    }
    if (after) {
      const r = after as unknown as Report;
      o.check(
        r.core.version === o.first.core && r.state?.chosen_by === "person",
        `the rollback to ${o.first.core} is not the person's selection: ${JSON.stringify(summary(after))}`,
      );
    }
    // Both app versions again, each a change of the app's version: the person's choice stays.
    for (const a of [o.first, o.update]) {
      const { first: again } = await report(o, a.program, `${a.label} again`, p, 0);
      if (again) {
        const r = again as unknown as Report;
        o.check(
          r.core.state === "running" && r.core.version === o.first.core && r.state?.chosen_by === "person",
          `${a.label}: the core the person chose (${o.first.core}) did not stay selected: ${JSON.stringify(summary(again))}`,
        );
      }
    }
  } finally {
    p.remove();
  }
}

/** A file the app does not list, beside its bundled packages: not copied. */
async function extraFile(o: BundledRun) {
  const p = o.profile("extra");
  try {
    connection(p);
    const c = o.copy(o.first, "extra");
    writeFileSync(join(c.bundled, "extra-not-listed.txt"), "not listed\n");
    const { first } = await report(o, c.program, "an extra file in bundled/", p, 0);
    if (first) {
      const r = first as unknown as Report;
      o.check(
        r.core.state === "running" && r.core.version === o.first.core,
        `with an extra file in bundled/ the bundled core does not run: ${JSON.stringify(r.core)}`,
      );
      o.check(
        r.store_files.includes(PROGRAM) && r.store_files.includes("bundled/index.json"),
        `the store's copy lacks the listed files: ${r.store_files.join(", ")}`,
      );
      o.check(
        !r.store_files.some((f) => f.includes("extra-not-listed")),
        `the extra file was copied into the app's cores: ${r.store_files.join(", ")}`,
      );
    }
  } finally {
    p.remove();
  }
}

/** A changed bundled/index.json: the damaged message, nothing installed. */
async function damaged(o: BundledRun) {
  const p = o.profile("damaged");
  try {
    connection(p);
    const c = o.copy(o.first, "damaged");
    appendFileSync(join(c.bundled, "index.json"), " ");
    const { first } = await report(o, c.program, "a changed bundled/index.json", p, 0);
    if (first) {
      const r = first as unknown as Report;
      const damage = new RegExp(
        `The core that came with the app \\(core ${o.first.core.replaceAll(".", "\\.")}\\) is damaged .*bundled/index\\.json.*reinstall or update the app`,
      );
      o.check(
        r.core.state === "missing" && damage.test(r.core.detail ?? ""),
        `a changed bundled/index.json was not refused with the damaged message: ${JSON.stringify(r.core)}`,
      );
      o.check(
        damage.test(r.versions.bundled?.problem ?? ""),
        `the versions do not say the bundled core is damaged: ${JSON.stringify(r.versions.bundled)}`,
      );
      o.check(
        r.versions.installed.length === 0,
        `something was installed: ${JSON.stringify(r.versions.installed)}`,
      );
    }
  } finally {
    p.remove();
  }
}

/** A version in the app's cores with the bundled core's number and other files: the conflict. */
async function conflict(o: BundledRun) {
  const p = o.profile("conflict");
  try {
    connection(p);
    // As the store records a version it installed (cores.rs Record): its folder and its record.
    const program = Buffer.from("#!/bin/sh\nexit 3\n");
    const folder = join(p.cores, "versions", o.first.core);
    mkdirSync(folder, { recursive: true, mode: 0o700 });
    mkdirSync(join(p.cores, "records"), { recursive: true, mode: 0o700 });
    writeFileSync(join(folder, PROGRAM), program, { mode: 0o500 });
    writeFileSync(
      join(p.cores, "records", `${o.first.core}.json`),
      JSON.stringify({
        format: 1,
        id: o.first.core,
        kind: "signed",
        version: o.first.core,
        files: [
          {
            path: PROGRAM,
            size: program.length,
            executable: true,
            sha256: createHash("sha256").update(program).digest("hex"),
          },
        ],
      }),
      { mode: 0o600 },
    );
    const { first } = await report(o, o.first.program, "a store version with other files", p, 0);
    if (first) {
      const r = first as unknown as Report;
      const problem = r.versions.bundled?.problem ?? "";
      o.check(
        problem.includes(
          `hold a core ${o.first.core} with other files than the core ${o.first.core} that came with the app`,
        ) && !problem.includes("damaged"),
        `the store's other ${o.first.core} did not give the conflict message: ${JSON.stringify(r.versions.bundled)}`,
      );
      o.check(r.core.state !== "running", `a core runs: ${JSON.stringify(r.core)}`);
      o.check(
        JSON.stringify(r.store_files) === JSON.stringify([PROGRAM]) &&
          readFileSync(join(folder, PROGRAM)).equals(program),
        `the store's own ${o.first.core} was changed: ${r.store_files.join(", ")}`,
      );
    }
  } finally {
    p.remove();
  }
}

export async function bundledScenario(o: BundledRun) {
  for (const [name, step] of [
    ["versions", versions],
    ["extra file", extraFile],
    ["damaged", damaged],
    ["conflict", conflict],
  ] as const) {
    o.say(`${name}: start`);
    await step(o);
  }
}
