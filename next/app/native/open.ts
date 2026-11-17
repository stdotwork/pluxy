// Fixtures for the native test's check of opening a file in the person's own app
// (src-tauri/src/open_native.rs; on Windows and macOS src-tauri/src/open_platform_native.rs,
// buildPlatformOpenFixtures):
//
// - pluxy/execution: the real execution program (next/app-tools/execution, which run.ts builds),
//   packed with its own manifest's operations into a signed curated package;
// - pluxy/open-relay: the disposable fixture tool (src-tauri/examples/program_fixture), whose
//   `relay` calls pluxy/execution's `prepare` and `launch`: a stand-in for a tool such as the Codex
//   session reader asking to open one of its files.
//
// bin/xdg-open is the fixture program as the stand-in opener (the app accepts an opener from bin/
// only in a native-test build, PLUXY_APP_TEST_OPENERS): it appends a line of what it was started
// with to bin/opened.jsonl and exits. codex/ is the folder the tool may open files in, with a
// session file in it, and files it may not open: an HTML file named .jsonl, a .desktop file, an
// executable file and a link to a file in outside/.

import { generateKeyPairSync, type KeyObject } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { artifact, EXE, type Fixture, platform, TOOL, writeTrustProfile } from "./admission";
import { index, native, type Package, pack } from "./routing";

const object = { type: "object" };

const relay = {
  id: "relay",
  description: "Call another package's operation through the host",
  effect: "read",
  input_schema: object,
  output_schema: object,
};

const EXECUTION = "pluxy/execution";

export interface Open {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

/** Packs the two packages into a signed curated index in root: execution (the built execution
 * program, if there is one) with manifest (its pluxy-extension.json), and program (the built
 * fixture tool) as the relay tool; writes the test trust profile in home (the app's HOME). */
function packOpenTools(
  root: string,
  home: string,
  program: string,
  execution: string,
  manifest: string,
): Open {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const packages: [Package, string][] = [];
  if (existsSync(execution) && existsSync(manifest)) {
    const m = JSON.parse(readFileSync(manifest, "utf8"));
    packages.push([
      {
        id: m.id,
        version: m.version,
        tier: "native",
        app: { ...m.app, programs: { [platform()]: TOOL } },
      },
      execution,
    ]);
    // The check installs the release its manifest names.
    writeFileSync(join(root, "execution-version"), m.version);
  }
  // One import of both operations (a package is imported once), optional: the tool is enabled
  // whether or not the execution program exports `prepare`; when it does not, each call through
  // the import is refused.
  packages.push([
    native("pluxy/open-relay", "1.0.0", {
      operations: [relay],
      imports: [{ package: EXECUTION, contract: [1], operations: ["prepare", "launch"], required: false }],
    }),
    program,
  ]);
  const curated: [Package, Record<string, unknown>][] = [];
  for (const [p, prog] of packages) {
    const file = pack(root, p, prog);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    curated.push([p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce };
}

/** Builds the fixtures in root: the two packages (packOpenTools), program (the built fixture
 * tool) as the opener, the files, and the test trust profile in home (the app's HOME). */
export function buildOpenFixtures(
  root: string,
  home: string,
  program: string,
  execution: string,
  manifest: string,
): Open {
  const open = packOpenTools(root, home, program, execution, manifest);

  const bin = join(root, "bin");
  mkdirSync(bin);
  copyFileSync(program, join(bin, "xdg-open"));
  chmodSync(join(bin, "xdg-open"), 0o755);

  const sessions = join(root, "codex", "sessions", "2026", "11", "15");
  // Private: the app opens only a file and folder no one else may write.
  mkdirSync(sessions, { recursive: true, mode: 0o700 });
  writeFileSync(join(sessions, "rollout-2026-11-15T03-00-00-session.jsonl"), '{"type":"session_meta"}\n', {
    mode: 0o600,
  });
  // An executable file inside the folder: never opened (a desktop may run what it opens).
  writeFileSync(join(sessions, "run.sh"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  // HTML named .jsonl, and a .desktop file: the desktop would open them in apps that run them.
  writeFileSync(join(sessions, "rollout-page.jsonl"), "<html><body><script>x</script></body></html>\n", {
    mode: 0o600,
  });
  writeFileSync(join(sessions, "app.desktop"), "[Desktop Entry]\nType=Application\nExec=true\n", {
    mode: 0o600,
  });
  const outside = join(root, "outside");
  mkdirSync(outside);
  writeFileSync(join(outside, "private.txt"), "not the tool's\n", { mode: 0o600 });
  // A link inside the folder to a file outside it: refused as the file outside.
  symlinkSync(join(outside, "private.txt"), join(sessions, "rollout-link.jsonl"));
  return open;
}

/** The session folder buildPlatformOpenFixtures makes in home: on Windows its junction `linked`
 * leads outside it, so the session removes that junction itself before the folder. */
export function platformOpenSessions(home: string): string {
  return join(home, "open-files", "codex", "sessions", "2026", "11", "18");
}

/** Windows and macOS (src-tauri/src/open_platform_native.rs): the two packages in root
 * (packOpenTools), and the files in home/open-files, a folder only this user may write (home is
 * the session's private folder): codex/sessions/2026/11/18 holds notes.txt, the same text as
 * page.html and shared.txt, and a session file; outside/private.txt is not the tool's, and a link
 * (macOS: linked.txt) or junction (Windows: linked) in the session folder leads to it. On Windows
 * bin/handler.exe is the fixture program, the stand-in app the check's .jsonl file type runs.
 * root/open.json names the files' folder and the execution program as built. */
export function buildPlatformOpenFixtures(
  root: string,
  home: string,
  program: string,
  execution: string,
  manifest: string,
): Open {
  const open = packOpenTools(root, home, program, execution, manifest);
  const files = join(home, "open-files");
  const sessions = platformOpenSessions(home);
  mkdirSync(sessions, { recursive: true, mode: 0o700 });
  for (const name of ["notes.txt", "page.html", "shared.txt"])
    writeFileSync(join(sessions, name), "pluxy's open check\n", { mode: 0o600 });
  writeFileSync(join(sessions, "rollout-2026-11-18T03-00-00-session.jsonl"), '{"type":"session_meta"}\n', {
    mode: 0o600,
  });
  const outside = join(files, "outside");
  mkdirSync(outside, { mode: 0o700 });
  writeFileSync(join(outside, "private.txt"), "not the tool's\n", { mode: 0o600 });
  if (process.platform === "win32") {
    // A junction needs no privilege, unlike a link.
    symlinkSync(outside, join(sessions, "linked"), "junction");
    mkdirSync(join(files, "bin"));
    copyFileSync(program, join(files, "bin", `handler${EXE}`));
  } else {
    symlinkSync(join(outside, "private.txt"), join(sessions, "linked.txt"));
  }
  writeFileSync(join(root, "open.json"), JSON.stringify({ files, execution }));
  return open;
}
