// Fixtures for the native test's checks of the program host (src-tauri/src/program_native.rs, then
// src-tauri/src/swap_native.rs in the same session): the disposable summary tool
// (src-tauri/examples/program_fixture, which run.ts builds) in native app packages with a page,
// signed with a publisher key generated for this run, a curated registry listing them, and the
// run's test trust profile in its HOME. The private key never leaves this process. The releases:
// 1.0.0 and 1.1.0 behave; 1.2.0 describes an operation its manifest does not declare, so it is
// incompatible. expect.json holds what the checks compare with, worked out here and not by the
// app: each archive's SHA-256, the summary's input and the count and longest item it must answer.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync, type KeyObject } from "node:crypto";
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";

export const PROGRAM_ID = "pluxy/summary";
const ITEMS = ["one", "three", "seventeen", "four"];
/** Each release, and the fixture.json that makes it misbehave (none: it behaves). */
const RELEASES: { version: string; mode?: object }[] = [
  { version: "1.0.0" },
  { version: "1.1.0" },
  { version: "1.2.0", mode: { extra_operation: "more" } },
];

export interface Program {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

const object = { type: "object" };
const OPERATIONS = [
  {
    id: "summary",
    description: "Count the items and name the longest",
    effect: "read",
    input_schema: {
      type: "object",
      properties: {
        items: { type: "array", items: { type: "string", maxLength: 64 }, maxItems: 16 },
      },
      required: ["items"],
      additionalProperties: false,
    },
    output_schema: {
      type: "object",
      properties: { count: { type: "integer" }, longest: { type: "string" } },
      required: ["count", "longest"],
    },
  },
  {
    id: "wait",
    description: "Wait until cancelled",
    effect: "read",
    input_schema: { type: "object", additionalProperties: false },
    output_schema: object,
  },
  {
    id: "slow",
    description: "Answer after a while",
    effect: "read",
    input_schema: {
      type: "object",
      properties: { ms: { type: "integer", minimum: 0, maximum: 60000 } },
      required: ["ms"],
      additionalProperties: false,
    },
    output_schema: object,
  },
  {
    id: "crash",
    description: "Exit at once without answering",
    effect: "read",
    input_schema: { type: "object", additionalProperties: false },
    output_schema: object,
  },
];

/** Packs, signs and lists one release of the tool in root. */
function release(
  root: string,
  program: string,
  version: string,
  mode: object | undefined,
  publicKey: KeyObject,
  privateKey: KeyObject,
) {
  const f: Fixture = { file: `summary-${version}.tar.gz`, id: PROGRAM_ID, version, tier: "native" };
  const dir = join(root, `pkg-summary-${version}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  mkdirSync(join(dir, "pages"));
  writeFileSync(
    join(dir, "pluxy-extension.json"),
    JSON.stringify({
      manifest_version: 1,
      id: f.id,
      version: f.version,
      app: {
        version: 0,
        tier: "native",
        host_versions: [0],
        programs: { [platform()]: "bin/summary" },
        operations: OPERATIONS,
        state: { version: 0, readable_versions: [0] },
        ui: {
          version: 0,
          dir: "pages",
          pages: [{ id: "page", title: "Summary", short: "Summary", path: "index.html" }],
        },
      },
    }),
  );
  if (mode) writeFileSync(join(dir, "fixture.json"), JSON.stringify(mode));
  copyFileSync(program, join(dir, "bin", "summary"));
  chmodSync(join(dir, "bin", "summary"), 0o755);
  writeFileSync(join(dir, "pages", "index.html"), "<!doctype html><title>Summary</title><p>Summary</p>");
  const r = spawnSync("tar", ["-czf", join(root, f.file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return artifact(root, f.file, f, publicKey, privateKey);
}

/** Builds the fixtures in root, with program (the built fixture tool) as the packages' program,
 * and the test trust profile in home (the app's HOME). */
export function buildProgramFixtures(root: string, home: string, program: string): Program {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const signed = RELEASES.map((r) => ({
    version: r.version,
    ...release(root, program, r.version, r.mode, publicKey, privateKey),
  }));
  const curated = join(root, "curated.json");
  writeFileSync(
    curated,
    JSON.stringify({
      registry_version: 0,
      packages: {
        [PROGRAM_ID]: {
          releases: signed.map((s) => ({ version: s.version, artifacts: { [platform()]: s.entry } })),
        },
      },
    }),
  );
  const longest = ITEMS.reduce((a, b) => (b.length > a.length ? b : a), "");
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      sha256: signed[0]?.sha256,
      releases: Object.fromEntries(signed.map((s) => [s.version, s.sha256])),
      items: ITEMS,
      count: ITEMS.length,
      longest,
    }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce };
}
