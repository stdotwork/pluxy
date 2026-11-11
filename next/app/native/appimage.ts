// The AppImage smoke fixture: a disposable AppImage built from a known runtime, which each native
// run's session runs (session.ts) with APPIMAGE_EXTRACT_AND_RUN=1, the FUSE-free way to run an
// AppImage. Later checks of the app's own package rely on that way working on this machine.
//
// The runtime is the x86_64 asset of AppImage/type2-runtime's release 20251108. It is downloaded
// when needed, kept in the user's cache folder, and checked against the SHA-256 below every time
// it is used. GitHub does not mark the release immutable, so the digest is what pins it. It was
// checked when pinned: GitHub's own digest for the asset (releases API, `digest`) equals the
// sha256sum of the downloaded file, and the release's detached signature (runtime-x86_64.sig)
// verifies with gpg against the public key the release ships (signing-pubkey.asc, fingerprint
// 570C 77AC EA40 C0F1 B758 902C BF96 CCA5 6490 F695). To move to another release, repeat those
// checks and change TAG and SHA256 together.

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const TAG = "20251108";
const ASSET = "runtime-x86_64";
const SHA256 = "2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d";
const URL = `https://github.com/AppImage/type2-runtime/releases/download/${TAG}/${ASSET}`;

/** What the AppRun script prints; the runtime sets APPIMAGE and APPDIR. */
const APP_RUN = `#!/bin/sh
echo "pluxy-appimage-smoke arg=$1 appimage=\${APPIMAGE:+set} appdir=$APPDIR"
`;

const sha256 = (data: Uint8Array) => createHash("sha256").update(data).digest("hex");

/** The runtime's path in the cache, downloaded and checked against the pinned digest. */
export async function runtimeFile(): Promise<string> {
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new Error(`the AppImage smoke test needs Linux on x86_64, not ${process.platform} ${process.arch}`);
  }
  const dir = join(process.env.XDG_CACHE_HOME ?? join(homedir(), ".cache"), "pluxy-app-native");
  mkdirSync(dir, { recursive: true });
  const file = join(dir, `type2-runtime-${TAG}-${ASSET}`);
  if (existsSync(file) && sha256(readFileSync(file)) === SHA256) return file;
  rmSync(file, { force: true });

  const response = await fetch(URL);
  if (!response.ok) throw new Error(`downloading ${URL}: HTTP ${response.status}`);
  const bytes = new Uint8Array(await response.arrayBuffer());
  const got = sha256(bytes);
  if (got !== SHA256) throw new Error(`${URL} has SHA-256 ${got}, not the pinned ${SHA256}`);
  const part = `${file}.${process.pid}`;
  writeFileSync(part, bytes);
  renameSync(part, file);
  return file;
}

/** Builds the smoke AppImage in dir (an existing, empty folder) and returns its path. */
export async function buildSmokeAppImage(dir: string): Promise<string> {
  const runtime = await runtimeFile();
  const appDir = join(dir, "AppDir");
  mkdirSync(appDir);
  writeFileSync(join(appDir, "AppRun"), APP_RUN, { mode: 0o755 });
  const image = join(dir, "smoke.squashfs");
  const made = spawnSync(
    "mksquashfs",
    [appDir, image, "-root-owned", "-noappend", "-no-progress", "-quiet", "-comp", "gzip"],
    { encoding: "utf8" },
  );
  if (made.status !== 0) {
    throw new Error(`mksquashfs (package squashfs-tools) exited with ${made.status}: ${made.stderr}`);
  }
  const appImage = join(dir, "smoke.AppImage");
  writeFileSync(appImage, Buffer.concat([readFileSync(runtime), readFileSync(image)]), { mode: 0o755 });
  return appImage;
}

/** What the smoke AppImage's output must say for a run with this nonce: it ran, from the
 * runtime, in a folder it extracted itself (a FUSE mount is `.mount_…`). */
export function smokeProblem(output: string, nonce: string): string | undefined {
  const m = /^pluxy-appimage-smoke arg=(\S*) appimage=(\S*) appdir=(\S*)$/m.exec(output);
  if (!m) return `its output has no marker line: ${JSON.stringify(output)}`;
  if (m[1] !== nonce) return `its marker carries ${JSON.stringify(m[1])}, not the nonce ${nonce}`;
  if (m[2] !== "set") return "the runtime did not set APPIMAGE";
  if (!m[3]?.includes("/appimage_extracted_")) return `it ran from ${m[3]}, not from a folder it extracted`;
  return undefined;
}
