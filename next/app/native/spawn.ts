// Fixtures for the native test's check of the host's spawn service, managed runtime registry and
// launch environment (src-tauri/src/spawn_native.rs). Both native tools are the disposable fixture
// tool (src-tauri/examples/program_fixture, which run.ts builds) in their own signed packages:
//
// - pluxy/launcher: start (launch; its `key` is a secret passed as a reference only), runtime
//   (read) and crash (read: it exits at once);
// - pluxy/other: runtime (read), to try to control the launcher's runtimes.
//
// The agent the launches start is that fixture program too, copied to `agent` (it is the agent
// when its first argument says so). keys.json holds the key the check types into native key
// entry and the canaries the run puts in the app's environment, which no spawned process may
// inherit, all made here at random; the app never chose them.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, copyFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, writeTrustProfile } from "./admission";
import { index, native, type Package, pack } from "./routing";

const object = { type: "object" };
const str = { type: "string" };

const start = {
  id: "start",
  description: "Start an agent, or a managed runtime",
  effect: "launch",
  secrets: { key: "reference" },
  input_schema: {
    type: "object",
    properties: {
      executable: str,
      argv: { type: "array", items: str },
      cwd: str,
      lifetime: { type: "string", enum: ["detached", "managed"] },
      environment: object,
      key: str,
      record: str,
    },
    required: ["executable", "argv", "cwd"],
    additionalProperties: false,
  },
  output_schema: object,
};
const runtime = {
  id: "runtime",
  description: "Look at, stop or restart a managed runtime",
  effect: "read",
  input_schema: {
    type: "object",
    properties: {
      op: { type: "string", enum: ["status", "stop", "restart", "list"] },
      runtime: str,
    },
    required: ["op"],
    additionalProperties: false,
  },
  output_schema: object,
};
const crash = {
  id: "crash",
  description: "Exit at once",
  effect: "read",
  input_schema: { type: "object", additionalProperties: false },
  output_schema: object,
};

const PACKAGES: Package[] = [
  native("pluxy/launcher", "1.0.0", { operations: [start, runtime, crash] }),
  native("pluxy/other", "1.0.0", { operations: [runtime] }),
];

export interface Spawn {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The key and the environment canaries: none may show in the app's output. */
  canaries: string[];
  /** Variables for the app's environment: the canaries no spawned process may inherit, and the
   * one a launch asks to inherit. */
  env: Record<string, string>;
}

/** Builds the fixtures in root, with program (the built fixture tool) as every package's program
 * and as the agent, and the test trust profile in home (the app's HOME). */
export function buildSpawnFixtures(root: string, home: string, program: string): Spawn {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const curated: [Package, Record<string, unknown>][] = [];
  for (const p of PACKAGES) {
    const file = pack(root, p, program);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    curated.push([p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));
  const agent = join(root, "agent");
  copyFileSync(program, agent);
  chmodSync(agent, 0o755);
  const keys = {
    key: `pluxy-spawn-key-${randomBytes(16).toString("hex")}`,
    // In the app's environment under a provider key's name and a test variable's: never inherited.
    provider: `pluxy-spawn-provider-${randomBytes(16).toString("hex")}`,
    test: `pluxy-spawn-test-${randomBytes(16).toString("hex")}`,
    // In the app's environment too; a launch asks for it by name.
    inherit: `pluxy-spawn-inherit-${randomBytes(8).toString("hex")}`,
  };
  writeFileSync(join(root, "keys.json"), JSON.stringify(keys));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return {
    registries: [{ name: "curated", url: curatedFile }],
    nonce,
    canaries: [keys.key, keys.provider, keys.test],
    env: {
      OPENAI_API_KEY: keys.provider,
      PLUXY_TEST_SPAWN_CANARY: keys.test,
      FIXTURE_INHERIT: keys.inherit,
    },
  };
}
