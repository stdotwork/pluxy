// Fixtures for the native test's check of signed native-tier admission
// (src-tauri/src/admission_native.rs): a native app package signed with a publisher key generated
// for this run, three local registry indexes (two the run's trust profile names as curated
// sources, one it does not), and the run's test trust profile in its HOME. The private key never
// leaves this process: only the public key is written, into the trust profile. expect.json holds
// each archive's SHA-256, computed here from the archive bytes, and the native program's.

import { spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, type KeyObject, randomBytes, sign } from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** The trust profile's file names (src-tauri/src/trust.rs). */
export const MARKER_FILE = ".pluxy-test-trust-profile.json";
export const PROFILE_FILE = ".pluxy-test-trust-roots.json";
const KEY_ID = "fixture";
/** The native fixtures' program (bin/tool). */
const PROGRAM = "#!/bin/sh\nexit 0\n";

/** Go's platform name for this machine, as registry artifacts are keyed. */
function platform(): string {
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch as string] ?? process.arch;
  return `${process.platform}/${arch}`;
}

interface Fixture {
  file: string;
  id: string;
  version: string;
  tier: "native" | "web";
}

function pack(root: string, f: Fixture): string {
  const dir = join(root, `pkg-${f.file}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  const app =
    f.tier === "native"
      ? {
          version: 0,
          tier: "native",
          host_versions: [0],
          programs: { [platform()]: "bin/tool" },
          operations: [
            {
              id: "inspect",
              description: "Report the fixture's state",
              input_schema: { type: "object" },
              output_schema: { type: "object" },
            },
          ],
          state: { version: 0, readable_versions: [0] },
        }
      : {
          version: 0,
          tier: "web",
          host_versions: [0],
          ui: {
            version: 0,
            dir: "pages",
            pages: [{ id: "page", title: "Page", short: "Page", path: "index.html" }],
          },
        };
  writeFileSync(
    join(dir, "pluxy-extension.json"),
    JSON.stringify({ manifest_version: 1, id: f.id, version: f.version, app }),
  );
  if (f.tier === "native") {
    // Never run by this slice: admission starts no process.
    writeFileSync(join(dir, "bin", "tool"), PROGRAM);
    chmodSync(join(dir, "bin", "tool"), 0o755);
  } else {
    mkdirSync(join(dir, "pages"));
    writeFileSync(join(dir, "pages", "index.html"), "<!doctype html><title>page</title><p>page</p>");
  }
  const archive = join(root, f.file);
  const r = spawnSync("tar", ["-czf", archive, "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return archive;
}

function hex(b: Uint8Array): string {
  return Buffer.from(b).toString("hex");
}

function publicHex(key: KeyObject): string {
  const jwk = key.export({ format: "jwk" });
  return hex(Buffer.from(jwk.x as string, "base64url"));
}

/** An index artifact for archive, signed with key when given. */
function artifact(root: string, file: string, f: Fixture, key?: KeyObject) {
  const bytes = readFileSync(join(root, file));
  const sha256 = createHash("sha256").update(bytes).digest("hex");
  const entry: Record<string, unknown> = { url: file, sha256, size: bytes.length };
  if (key) {
    // The publisher fixes these exact bytes, then signs them.
    const descriptor = JSON.stringify({
      id: f.id,
      version: f.version,
      tier: f.tier,
      platform: platform(),
      size: bytes.length,
      sha256,
    });
    entry.key_id = KEY_ID;
    entry.descriptor = descriptor;
    entry.signature = hex(sign(null, Buffer.from(descriptor, "utf8"), key));
  }
  return { sha256, entry };
}

export interface Admission {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

/** Builds the fixtures in root and the test trust profile in home (the app's HOME). */
export function buildAdmissionFixtures(root: string, home: string): Admission {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  // Signs pluxy/forged: a key no trust root names.
  const other = generateKeyPairSync("ed25519").privateKey;
  const fixtures: Record<string, Fixture> = {
    tool: { file: "tool-1.0.0.tar.gz", id: "pluxy/tool", version: "1.0.0", tier: "native" },
    forged: { file: "forged-1.0.0.tar.gz", id: "pluxy/forged", version: "1.0.0", tier: "native" },
    unsigned: { file: "unsigned-1.0.0.tar.gz", id: "pluxy/unsigned", version: "1.0.0", tier: "native" },
    lookalike: { file: "lookalike-1.0.0.tar.gz", id: "example/native", version: "1.0.0", tier: "native" },
    page: { file: "page-1.0.0.tar.gz", id: "example/page", version: "1.0.0", tier: "web" },
  };
  for (const f of Object.values(fixtures)) pack(root, f);
  const a = (k: string, key?: KeyObject) => {
    const f = fixtures[k] as Fixture;
    return artifact(root, f.file, f, key);
  };
  const tool = a("tool", privateKey);
  const forged = a("forged", other);
  const unsigned = a("unsigned");
  const lookalike = a("lookalike");
  const page = a("page");
  const index = (entries: Record<string, { entry: object }>) => ({
    registry_version: 0,
    packages: Object.fromEntries(
      Object.entries(entries).map(([id, x]) => [
        id,
        { releases: [{ version: "1.0.0", artifacts: { [platform()]: x.entry } }] },
      ]),
    ),
  });
  const curated = join(root, "curated.json");
  const mirror = join(root, "mirror.json");
  const untrusted = join(root, "untrusted.json");
  writeFileSync(
    curated,
    JSON.stringify(
      index({ "pluxy/tool": tool, "pluxy/forged": forged, "pluxy/unsigned": unsigned, "example/page": page }),
    ),
  );
  writeFileSync(mirror, JSON.stringify(index({ "pluxy/tool": tool })));
  // The same signed entry, digest and all, from a source no root names.
  writeFileSync(
    untrusted,
    JSON.stringify(index({ "pluxy/tool": tool, "example/native": lookalike, "example/page": page })),
  );
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      tool: tool.sha256,
      forged: forged.sha256,
      unsigned: unsigned.sha256,
      lookalike: lookalike.sha256,
      page: page.sha256,
      public_key: publicHex(publicKey),
      key_id: KEY_ID,
      program: createHash("sha256").update(PROGRAM).digest("hex"),
    }),
  );

  // The test trust profile: roots for the curated and mirror sources, and the marker.
  const root_ = (source: string) => ({
    kind: "test",
    publisher: "pluxy",
    source,
    key_id: KEY_ID,
    public_key: publicHex(publicKey),
  });
  writeFileSync(join(home, PROFILE_FILE), JSON.stringify({ roots: [root_(curated), root_(mirror)] }), {
    mode: 0o600,
  });
  const nonce = hex(randomBytes(32));
  writeFileSync(
    join(home, MARKER_FILE),
    JSON.stringify({ format: 1, home: realpathSync(home), purpose: "pluxy-artifact-test-trust", nonce }),
    { mode: 0o600 },
  );
  return {
    registries: [
      { name: "curated", url: curated },
      { name: "mirror", url: mirror },
      { name: "untrusted", url: untrusted },
    ],
    nonce,
  };
}
