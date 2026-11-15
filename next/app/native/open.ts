// Fixtures for the native test's check of opening a file in the person's own app
// (src-tauri/src/open_native.rs):
//
// - pluxy/execution: the real execution program (next/app-tools/execution, which run.ts builds),
//   packed with its own manifest's operations into a signed curated package;
// - pluxy/open-relay: the disposable fixture tool (src-tauri/examples/program_fixture), whose
//   `relay` calls pluxy/execution's `prepare` and `launch`: a stand-in for a tool such as the Codex
//   session reader asking to open one of its files.
//
// bin/xdg-open is the fixture program as the stand-in opener: it writes what it was started with
// to the file FIXTURE_OPENER_RECORD names and exits. codex/ is the folder the tool may open files
// in, with a session file in it; outside/ holds files it may not open, and codex/ links to one.

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
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
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

/** Builds the fixtures in root: execution (the built execution program, if there is one) and
 * manifest (its pluxy-extension.json), program (the built fixture tool) as the relay tool and the
 * opener, the files, and the test trust profile in home (the app's HOME). */
export function buildOpenFixtures(
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
        app: { ...m.app, programs: { [platform()]: "bin/tool" } },
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

  const bin = join(root, "bin");
  mkdirSync(bin);
  copyFileSync(program, join(bin, "xdg-open"));
  chmodSync(join(bin, "xdg-open"), 0o755);

  const sessions = join(root, "codex", "sessions", "2026", "11", "15");
  mkdirSync(sessions, { recursive: true });
  writeFileSync(join(sessions, "rollout-2026-11-15T03-00-00-session.jsonl"), '{"type":"session_meta"}\n', {
    mode: 0o600,
  });
  // An executable file inside the folder: never opened (a desktop may run what it opens).
  writeFileSync(join(sessions, "run.sh"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const outside = join(root, "outside");
  mkdirSync(outside);
  writeFileSync(join(outside, "private.txt"), "not the tool's\n", { mode: 0o600 });
  // A link inside the folder to a file outside it: refused as the file outside.
  symlinkSync(join(outside, "private.txt"), join(sessions, "rollout-link.jsonl"));
  mkdirSync(join(root, "records"));

  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce };
}
