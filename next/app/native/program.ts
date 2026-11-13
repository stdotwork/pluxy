// Fixtures for the native test's check of the program host (src-tauri/src/program_native.rs): the
// disposable summary tool (src-tauri/examples/program_fixture, which run.ts builds) in a native app
// package with a page, signed with a publisher key generated for this run, a curated registry
// listing it, and the run's test trust profile in its HOME. The private key never leaves this
// process. expect.json holds what the check compares with, worked out here and not by the app:
// the archive's SHA-256, the summary's input and the count and longest item it must answer.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync } from "node:crypto";
import { chmodSync, copyFileSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";

export const PROGRAM_ID = "pluxy/summary";
const ITEMS = ["one", "three", "seventeen", "four"];

export interface Program {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

/** Builds the fixtures in root, with program (the built fixture tool) as the package's program,
 * and the test trust profile in home (the app's HOME). */
export function buildProgramFixtures(root: string, home: string, program: string): Program {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const f: Fixture = { file: "summary-1.0.0.tar.gz", id: PROGRAM_ID, version: "1.0.0", tier: "native" };
  const dir = join(root, "pkg-summary");
  mkdirSync(join(dir, "bin"), { recursive: true });
  mkdirSync(join(dir, "pages"));
  const object = { type: "object" };
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
        operations: [
          {
            id: "summary",
            description: "Count the items and name the longest",
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
            input_schema: { type: "object", additionalProperties: false },
            output_schema: object,
          },
        ],
        state: { version: 0, readable_versions: [0] },
        ui: {
          version: 0,
          dir: "pages",
          pages: [{ id: "page", title: "Summary", short: "Summary", path: "index.html" }],
        },
      },
    }),
  );
  copyFileSync(program, join(dir, "bin", "summary"));
  chmodSync(join(dir, "bin", "summary"), 0o755);
  writeFileSync(join(dir, "pages", "index.html"), "<!doctype html><title>Summary</title><p>Summary</p>");
  const r = spawnSync("tar", ["-czf", join(root, f.file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });

  const signed = artifact(root, f.file, f, publicKey, privateKey);
  const curated = join(root, "curated.json");
  writeFileSync(
    curated,
    JSON.stringify({
      registry_version: 0,
      packages: {
        [f.id]: { releases: [{ version: f.version, artifacts: { [platform()]: signed.entry } }] },
      },
    }),
  );
  const longest = ITEMS.reduce((a, b) => (b.length > a.length ? b : a), "");
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({ sha256: signed.sha256, items: ITEMS, count: ITEMS.length, longest }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce };
}
