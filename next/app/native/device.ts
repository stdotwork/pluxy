// Fixtures for the native test's check of this device's extension catalog
// (src-tauri/src/device_native.rs): page-only app packages packed as the protocol's release
// archives (gzip-compressed tar, manifest at the top), a local registry index listing some of
// them, and expect.json with each archive's SHA-256, computed here from the archive bytes.

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** The page: says hello, then reports marker to its parent after each channel reply. */
function pageScript(marker: string): string {
  return `const marker = ${JSON.stringify(marker)};
let port;
const done = [];
setInterval(() => {
  for (const m of done) parent.postMessage({ deviceMarker: m }, "*");
}, 200);
addEventListener("message", (e) => {
  if (e.source !== parent) return;
  const d = e.data;
  if (d && d.pluxy === "host" && d.type === "welcome" && e.ports[0] && !port) {
    port = e.ports[0];
    port.onmessage = (r) => {
      const m = r.data;
      if (m && typeof m.id === "string" && m.result && !done.includes(m.id)) done.push(m.id);
    };
    port.postMessage({ v: 0, id: marker, method: "theme.get", params: null });
  } else if (d && typeof d.deviceLoad === "string") {
    // Asks the asset scheme for another file, as an image: no navigation, a plain request.
    const img = new Image();
    img.src = d.deviceLoad;
  } else if (d && typeof d.deviceProbe === "string" && port) {
    port.postMessage({ v: 0, id: marker + "-" + d.deviceProbe, method: "theme.get", params: null });
  }
});
parent.postMessage({ pluxy: "page", v: 0, type: "hello", versions: [0] }, "*");
`;
}

interface Fixture {
  file: string;
  id: string;
  version: string;
  page: string;
  hostVersions?: number[];
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
      host_versions: f.hostVersions ?? [0],
      ui: {
        version: 0,
        dir: "pages",
        pages: [
          { id: f.page, title: `${f.page} ${f.version}`, short: f.page.slice(0, 8), path: "index.html" },
        ],
      },
    },
  };
  writeFileSync(join(dir, "pluxy-extension.json"), JSON.stringify(manifest));
  writeFileSync(
    join(dir, "pages", "index.html"),
    `<!doctype html><meta charset="utf-8"><title>${marker}</title><script src="page.js"></script><p>${marker}</p>`,
  );
  writeFileSync(join(dir, "pages", "page.js"), pageScript(marker));
  const archive = join(root, f.file);
  const r = spawnSync("tar", ["-czf", archive, "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return archive;
}

function digest(path: string): { sha256: string; size: number } {
  const bytes = readFileSync(path);
  return { sha256: createHash("sha256").update(bytes).digest("hex"), size: bytes.length };
}

/** Go's platform name for this machine, as registry artifacts are keyed. */
function platform(): string {
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch as string] ?? process.arch;
  return `${process.platform}/${arch}`;
}

/** Builds the fixtures in root and returns the registries file the app's config should hold. */
export function buildDeviceFixtures(root: string): object {
  const fixtures: Record<string, Fixture> = {
    notes1: { file: "notes-1.0.0.tar.gz", id: "example/notes", version: "1.0.0", page: "notes" },
    notes2: { file: "notes-2.0.0.tar.gz", id: "example/notes", version: "2.0.0", page: "notes" },
    // A candidate this app cannot host: its host contract is 1, the app's is 0.
    notes3: {
      file: "notes-3.0.0.tar.gz",
      id: "example/notes",
      version: "3.0.0",
      page: "notes",
      hostVersions: [1],
    },
    // Claims the reserved publisher pluxy from a registry that is not curated.
    spoof: { file: "spoof-1.0.0.tar.gz", id: "pluxy/notes", version: "1.0.0", page: "notes" },
    local: { file: "local-1.0.0.tar.gz", id: "example/local", version: "1.0.0", page: "local" },
    pluxy_local: { file: "pluxy-local-1.0.0.tar.gz", id: "pluxy/local", version: "1.0.0", page: "local" },
  };
  const expect: Record<string, string> = {};
  const packages: Record<string, { releases: object[] }> = {};
  for (const [key, f] of Object.entries(fixtures)) {
    const d = digest(pack(root, f));
    expect[key] = d.sha256;
    if (key === "local" || key === "pluxy_local") continue;
    // Newest first, as the index format asks.
    packages[f.id] ??= { releases: [] };
    packages[f.id]?.releases.unshift({
      version: f.version,
      artifacts: { [platform()]: { url: f.file, sha256: d.sha256, size: d.size } },
    });
  }
  writeFileSync(join(root, "index.json"), JSON.stringify({ registry_version: 0, packages }));
  writeFileSync(join(root, "expect.json"), JSON.stringify(expect));
  return { version: 0, registries: [{ name: "fixture", url: join(root, "index.json") }] };
}
