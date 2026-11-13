// Fixtures for the native test's check of Access, the broker that lets a web package on this
// device read files and folders the person chose (src-tauri/src/access_native.rs). It packs
// page-only packages as the protocol's release archives, signs some of them with an Ed25519 key
// generated here for this run only (the private key never leaves this process), lists them in
// two local registries, and writes the files the person will choose, with what each holds in
// expect.json, written here, not by the app.

import { spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, randomUUID, sign } from "node:crypto";
import { mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** The page: says hello, then makes each channel call its parent posts as `accessCall` and posts
 * every reply it gets back as `accessReply`, including replies to calls it did not make. */
const PAGE_SCRIPT = `let port;
const queue = [];
addEventListener("message", (e) => {
  if (e.source !== parent) return;
  const d = e.data;
  if (d && d.pluxy === "host" && d.type === "welcome" && e.ports[0] && !port) {
    port = e.ports[0];
    port.onmessage = (r) => {
      const m = r.data;
      if (m && typeof m.id === "string") parent.postMessage({ accessReply: m }, "*");
    };
    port.postMessage({ v: 0, id: "ready", method: "theme.get", params: null });
    for (const c of queue.splice(0)) port.postMessage(c);
  } else if (d && d.accessCall) {
    const c = { v: 0, id: d.accessCall.id, method: d.accessCall.method, params: d.accessCall.params };
    if (port) port.postMessage(c);
    else queue.push(c);
  }
});
parent.postMessage({ pluxy: "page", v: 0, type: "hello", versions: [0] }, "*");
`;

interface Resource {
  id: string;
  kind: "file" | "subtree";
  operations: string[];
}

interface Fixture {
  file: string;
  id: string;
  version: string;
  page: string;
  resources: Resource[];
  signed: boolean;
}

function pack(root: string, f: Fixture): string {
  const dir = join(root, `pkg-${f.file}`);
  mkdirSync(join(dir, "pages"), { recursive: true });
  const marker = `${f.id.split("/")[1]}-${f.version}`;
  const manifest = {
    manifest_version: 1,
    id: f.id,
    version: f.version,
    app: {
      version: 0,
      tier: "web",
      host_versions: [0],
      ui: {
        version: 0,
        dir: "pages",
        pages: [
          { id: f.page, title: `${f.page} ${f.version}`, short: f.page.slice(0, 8), path: "index.html" },
        ],
      },
      resources: f.resources,
    },
  };
  writeFileSync(join(dir, "pluxy-extension.json"), JSON.stringify(manifest));
  writeFileSync(
    join(dir, "pages", "index.html"),
    `<!doctype html><meta charset="utf-8"><title>${marker}</title><script src="page.js"></script><p>${marker}</p>`,
  );
  writeFileSync(join(dir, "pages", "page.js"), PAGE_SCRIPT);
  const archive = join(root, f.file);
  const r = spawnSync("tar", ["-czf", archive, "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return archive;
}

/** Go's platform name for this machine, as registry artifacts are keyed. */
function platform(): string {
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch as string] ?? process.arch;
  return `${process.platform}/${arch}`;
}

/** Builds the fixtures in root and returns the registries file the app's config should hold. */
export function buildAccessFixtures(root: string): object {
  // A key for this run: its private half stays in this process, never on disk.
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const jwk = publicKey.export({ format: "jwk" });
  const publicHex = Buffer.from(jwk.x as string, "base64url").toString("hex");
  const keyId = `run-${randomUUID().slice(0, 8)}`;

  const files: Resource[] = [
    { id: "config", kind: "file", operations: ["read"] },
    { id: "tree", kind: "subtree", operations: ["list", "read"] },
  ];
  const fixtures: Record<string, Fixture> = {
    files1: {
      file: "files-1.0.0.tar.gz",
      id: "example/files",
      version: "1.0.0",
      page: "files",
      resources: files,
      signed: true,
    },
    files2: {
      file: "files-2.0.0.tar.gz",
      id: "example/files",
      version: "2.0.0",
      page: "files",
      resources: files,
      signed: true,
    },
    other: {
      file: "other-1.0.0.tar.gz",
      id: "example/other",
      version: "1.0.0",
      page: "other",
      resources: [
        { id: "config", kind: "file", operations: ["read"] },
        { id: "doc", kind: "file", operations: ["read"] },
      ],
      signed: true,
    },
    // Asks for a file, unsigned: it may display, never be granted one.
    plain: {
      file: "plain-1.0.0.tar.gz",
      id: "example/plain",
      version: "1.0.0",
      page: "plain",
      resources: [{ id: "config", kind: "file", operations: ["read"] }],
      signed: false,
    },
  };
  const digests: Record<string, string> = {};
  const artifacts: Record<string, object> = {};
  for (const [key, f] of Object.entries(fixtures)) {
    const bytes = readFileSync(pack(root, f));
    const sha256 = createHash("sha256").update(bytes).digest("hex");
    digests[key] = sha256;
    const artifact: Record<string, unknown> = { url: f.file, sha256, size: bytes.length };
    if (f.signed) {
      // The exact bytes signed and kept (app-host README, signed descriptor bytes).
      const descriptor = JSON.stringify({
        id: f.id,
        version: f.version,
        tier: "web",
        platform: platform(),
        size: bytes.length,
        sha256,
      });
      artifact.key_id = keyId;
      artifact.public_key = publicHex;
      artifact.descriptor = descriptor;
      artifact.signature = sign(null, Buffer.from(descriptor), privateKey).toString("hex");
    }
    artifacts[key] = artifact;
  }
  const index = (keys: string[]) => {
    const packages: Record<string, { releases: object[] }> = {};
    for (const key of keys) {
      const f = fixtures[key] as Fixture;
      packages[f.id] ??= { releases: [] };
      // Newest first, as the index format asks.
      packages[f.id]?.releases.unshift({ version: f.version, artifacts: { [platform()]: artifacts[key] } });
    }
    return JSON.stringify({ registry_version: 0, packages });
  };
  writeFileSync(join(root, "signed.json"), index(["files1", "files2", "other", "plain"]));
  // The same signed 1.0.0 archive from another source: the same ID, version, bytes and key.
  writeFileSync(join(root, "mirror.json"), index(["files1"]));

  // What the person chooses. Each file holds text of its own, with a random part, so a reply
  // carrying another file's bytes cannot pass for the expected one.
  const data = join(root, "data");
  const text = (name: string) => `${name}-${randomUUID()}\n`;
  const contents = {
    config: text("config"),
    sibling: text("sibling"),
    other: text("other"),
    outside: text("outside"),
    top: text("top"),
    inner: text("inner"),
    portal: text("portal"),
    replaced: text("replaced"),
  };
  mkdirSync(join(data, "stow"), { recursive: true });
  mkdirSync(join(data, "tree", "inner"), { recursive: true });
  writeFileSync(join(data, "stow", "config"), contents.config);
  // A config manager's link (stow, chezmoi): the person chooses the link, the target is read.
  symlinkSync(join("stow", "config"), join(data, "config"));
  writeFileSync(join(data, "stow", "sibling"), contents.sibling);
  writeFileSync(join(data, "other.txt"), contents.other);
  writeFileSync(join(data, "outside.txt"), contents.outside);
  writeFileSync(join(data, "tree", "top.txt"), contents.top);
  writeFileSync(join(data, "tree", "inner", "ok.txt"), contents.inner);
  // A link that stays inside the folder, and one that leaves it.
  symlinkSync(join("inner", "ok.txt"), join(data, "tree", "inside"));
  symlinkSync(join("..", "outside.txt"), join(data, "tree", "escape"));
  writeFileSync(join(data, "portal.txt"), contents.portal);

  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({ digests, key_id: keyId, public_key: publicHex, data, contents }),
  );
  return {
    version: 0,
    registries: [
      { name: "signed", url: join(root, "signed.json") },
      { name: "mirror", url: join(root, "mirror.json") },
    ],
  };
}
