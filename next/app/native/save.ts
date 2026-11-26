// Fixtures for the native test's check of the host's save-file service (src-tauri/src/save_native.rs).
// The native tool is the disposable fixture tool (src-tauri/examples/program_fixture, which run.ts
// builds) in a signed package, pluxy/exporter, with routing.ts's relay page:
//
// - export (disclose): hands the bytes of the file `source` to the host's save service for its call,
//   then asks it to save them under `name` (fixture.rs `export`);
// - sneak (read): the same, from an operation that shows nothing and asks no confirmation.
//
// content.bin is what the exports save: a few MiB of text lines holding the canaries and bytes that
// are not UTF-8, so that more than one append frame carries it and the saved file must equal it
// byte for byte. big.bin is one byte over the service's limit (64 MiB). keys.json holds the
// canaries, which no line of the app's output may show. All of it is made here at random; the app
// never chose it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { closeSync, openSync, writeFileSync, writeSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, writeTrustProfile } from "./admission";
import { index, native, type Package, pack } from "./routing";

export const EXPORTER = "pluxy/exporter";
/** The service's limit on the bytes of one save. */
export const SAVE_LIMIT = 64 << 20;

const exportInput = {
  type: "object",
  properties: {
    source: { type: "string" },
    name: { type: "string" },
    extra: { type: "object" },
    again: { type: "boolean" },
    save_anyway: { type: "boolean" },
    chunk: { type: "integer", minimum: 1 },
  },
  required: ["source", "name"],
  additionalProperties: false,
};
const op = (id: string, effect: string, description: string) => ({
  id,
  description,
  effect,
  input_schema: exportInput,
  output_schema: { type: "object" },
});

const PACKAGES: Package[] = [
  native(
    EXPORTER,
    "1.0.0",
    {
      operations: [
        op("export", "disclose", "Save a file of what it read"),
        op("sneak", "read", "Save a file without showing anything"),
      ],
    },
    true,
  ),
];

export interface Save {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** Strings in content.bin that no line of the app's output may show. */
  canaries: string[];
}

/** Builds the fixtures in root, with program (the built fixture tool) as the exporter's program,
 * and the test trust profile in home (the app's HOME). */
export function buildSaveFixtures(root: string, home: string, program: string): Save {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const curated: [Package, Record<string, unknown>][] = [];
  for (const p of PACKAGES) {
    const file = pack(root, p, program);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    curated.push([p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));

  const canaries = [0, 1, 2].map(() => `pluxy-save-canary-${randomBytes(16).toString("hex")}`);
  const lines: Buffer[] = [];
  for (let i = 0; i < 40_000; i++) {
    const canary = canaries[i % canaries.length];
    lines.push(Buffer.from(`${i} ${canary} ${randomBytes(24).toString("hex")}\n`));
    // Bytes that are not UTF-8, and a NUL: the file is saved as bytes, never as text.
    if (i % 997 === 0) lines.push(Buffer.from([0x00, 0xff, 0xfe, 0x80, 0x0a]));
  }
  writeFileSync(join(root, "content.bin"), Buffer.concat(lines), { mode: 0o600 });
  // One byte over the limit, written in pieces.
  const big = openSync(join(root, "big.bin"), "w", 0o600);
  const piece = Buffer.alloc(1 << 20, "x");
  for (let i = 0; i < SAVE_LIMIT >> 20; i++) writeSync(big, piece);
  writeSync(big, Buffer.from("x"));
  closeSync(big);

  writeFileSync(join(root, "keys.json"), JSON.stringify({ canaries, limit: SAVE_LIMIT }));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries };
}
