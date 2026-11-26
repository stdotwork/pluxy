// Fixtures for the native test's check of the vault, secret delivery and references, and journal
// recovery (src-tauri/src/vault_native.rs). Every native tool is the disposable fixture tool
// (src-tauri/examples/program_fixture, which run.ts builds) in its own signed package:
//
// - pluxy/writer, releases 1.0.0 and 1.1.0, with a page: apply (write; its `key` is a secret
//   delivered as a value) and report (read); imports pluxy/exec's run (secret references allowed)
//   and peek (none allowed);
// - pluxy/exec: run and peek (read; `key` is a secret passed as a reference only), exported;
//   imports pluxy/consumer's deliver (secret references allowed);
// - pluxy/consumer: deliver (write; `key` is a secret delivered as a value), exported;
// - pluxy/agent: serve (read; `key` is a secret delivered as a value), the client-key check's
//   agent (src-tauri/src/client_key_native.rs): it asks core with the client key it is given.
//
// The curated registry lists them, each signed with a publisher key generated for this run.
// keys.json holds the key the check types into native key entry and the canary it puts in the
// file the writer backs up, both made here at random; the app never chose them.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, writeTrustProfile } from "./admission";
import { index, native, type Package, pack } from "./routing";

const object = { type: "object" };
const str = { type: "string" };
const key = { key: str };

const apply = {
  id: "apply",
  description: "Write a client key into a config file",
  effect: "write",
  secrets: { key: "value" },
  input_schema: {
    type: "object",
    properties: {
      ...key,
      file: str,
      crash: { type: "boolean" },
      crash_before: { type: "boolean" },
      leave_open: { type: "boolean" },
      leak: str,
      forward: object,
    },
    required: ["key", "file"],
    additionalProperties: false,
  },
  output_schema: object,
};
const report = {
  id: "report",
  description: "Say what the last start reconciled",
  effect: "read",
  input_schema: { type: "object", additionalProperties: false },
  output_schema: object,
};
const runInput = {
  type: "object",
  properties: { ...key, record: str, steal: str, out: str, consumer: object },
  required: ["key"],
  additionalProperties: false,
};
const run = {
  id: "run",
  description: "Prepare a launch that needs a key",
  effect: "read",
  secrets: { key: "reference" },
  input_schema: runInput,
  output_schema: object,
};
const peek = { ...run, id: "peek", description: "Look at a key reference" };
const deliver = {
  id: "deliver",
  description: "Hand a key to its final consumer",
  effect: "write",
  secrets: { key: "value" },
  input_schema: {
    type: "object",
    properties: { ...key, out: str },
    required: ["key", "out"],
    additionalProperties: false,
  },
  output_schema: object,
};

const serve = {
  id: "serve",
  description: "Ask core with a client key",
  effect: "read",
  secrets: { key: "value" },
  input_schema: {
    type: "object",
    properties: { ...key, base: str, out: str },
    required: ["key", "base", "out"],
    additionalProperties: false,
  },
  output_schema: object,
};

const writer = (version: string) =>
  native(
    "pluxy/writer",
    version,
    {
      operations: [apply, report],
      imports: [
        {
          package: "pluxy/exec",
          contract: [1],
          operations: ["run", "peek"],
          secrets: ["run"],
          required: true,
        },
      ],
    },
    true,
  );

const PACKAGES: Package[] = [
  writer("1.0.0"),
  writer("1.1.0"),
  native("pluxy/exec", "1.0.0", {
    operations: [run, peek],
    exports: { contract: 1, operations: ["run", "peek"] },
    imports: [
      {
        package: "pluxy/consumer",
        contract: [1],
        operations: ["deliver"],
        secrets: ["deliver"],
        required: true,
      },
    ],
  }),
  native("pluxy/consumer", "1.0.0", {
    operations: [deliver],
    exports: { contract: 1, operations: ["deliver"] },
  }),
  native("pluxy/agent", "1.0.0", { operations: [serve] }),
];

export interface Vault {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The key and the backup canary: neither may show in the app's output. */
  canaries: string[];
}

/** Builds the fixtures in root, with program (the built fixture tool) as every package's program,
 * and the test trust profile in home (the app's HOME). */
export function buildVaultFixtures(root: string, home: string, program: string): Vault {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const curated: [Package, Record<string, unknown>][] = [];
  for (const p of PACKAGES) {
    const file = pack(root, p, program);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    curated.push([p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));
  const keys = {
    key: `pluxy-vault-key-${randomBytes(16).toString("hex")}`,
    backup: `pluxy-vault-backup-${randomBytes(16).toString("hex")}`,
  };
  writeFileSync(join(root, "keys.json"), JSON.stringify(keys));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [keys.key, keys.backup] };
}
