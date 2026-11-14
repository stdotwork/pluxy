// Fixtures for the native test's check of the execution program and the launcher
// (src-tauri/src/launcher_native.rs):
//
// - pluxy/execution: the real execution program (next/app-tools/execution, which run.ts builds),
//   packed with its own manifest's operations into a signed curated package;
// - pluxy/launch-relay: the disposable fixture tool (src-tauri/examples/program_fixture), whose
//   `relay` calls pluxy/execution's `launch`: another chain to the same launch;
// - pluxy/launch-page: a curated web package whose page imports pluxy/execution's `launch`.
//
// bin/ goes first on the app's PATH: `x-terminal-emulator` (the fixture program as a stand-in
// terminal, the first one Automatic picks) and `pluxy-agent` (the fixture program as the agent,
// found by name). override/agent is another copy, for an executable override. keys.json holds the
// key the check types into native key entry, made here at random; the app never chose it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
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
const launchImport = { package: EXECUTION, contract: [1], operations: ["launch"], required: true };

export interface Launcher {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The key: it must not show in the app's output. */
  canaries: string[];
  /** The folder to put first on the app's PATH. */
  bin: string;
}

/** Builds the fixtures in root: execution (the built execution program, if there is one) and
 * manifest (its pluxy-extension.json), program (the built fixture tool) as the relay tool, the
 * terminal and the agent, and the test trust profile in home (the app's HOME). */
export function buildLauncherFixtures(
  root: string,
  home: string,
  program: string,
  execution: string,
  manifest: string,
): Launcher {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const packages: [Package, string][] = [];
  // Before the execution program exists there is nothing to pack; the check then fails at its
  // first step, the launcher's state, which the app does not have either.
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
  }
  packages.push([
    native("pluxy/launch-relay", "1.0.0", { operations: [relay], imports: [launchImport] }),
    program,
  ]);
  packages.push([
    {
      id: "pluxy/launch-page",
      version: "1.0.0",
      tier: "web",
      page: true,
      app: { version: 0, tier: "web", host_versions: [0], imports: [launchImport] },
    },
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
  const override = join(root, "override");
  mkdirSync(bin);
  mkdirSync(override);
  for (const to of [join(bin, "x-terminal-emulator"), join(bin, "pluxy-agent"), join(override, "agent")]) {
    copyFileSync(program, to);
    chmodSync(to, 0o755);
  }
  const keys = { key: `pluxy-launcher-key-${randomBytes(16).toString("hex")}` };
  writeFileSync(join(root, "keys.json"), JSON.stringify(keys));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [keys.key], bin };
}
