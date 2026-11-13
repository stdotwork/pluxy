// The AppImage smoke fixture: a disposable AppImage built from a known runtime, which each native
// run's session runs (session.ts) with APPIMAGE_EXTRACT_AND_RUN=1, the FUSE-free way to run an
// AppImage. Later checks of the app's own package rely on that way working on this machine.
// The release gate's checks (run.ts) pack AppImages the same way around a given app binary, as
// cases with a known content for the gate to pass or refuse.
//
// The runtime is the x86_64 asset of AppImage/type2-runtime's release 20251108. It is downloaded
// when needed, kept in the user's cache folder, and checked against the SHA-256 below every time
// it is used. GitHub does not mark the release immutable, so the digest is what pins it. It was
// checked when pinned: GitHub's own digest for the asset (releases API, `digest`) equals the
// sha256sum of the downloaded file, and the release's detached signature (runtime-x86_64.sig)
// verifies with gpg against the public key the release ships (signing-pubkey.asc, fingerprint
// 570C 77AC EA40 C0F1 B758 902C BF96 CCA5 6490 F695). To move to another release, repeat those
// checks and change TAG and the digest together.

import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  closeSync,
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  readSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";

/** A file downloaded from a fixed address and pinned by its SHA-256. */
export interface Pin {
  /** Its file name in the cache folder. */
  cache: string;
  url: string;
  sha256: string;
}

const TAG = "20251108";
const ASSET = "runtime-x86_64";
/** The AppImage type 2 runtime (see the top of this file). */
export const RUNTIME: Pin = {
  cache: `type2-runtime-${TAG}-${ASSET}`,
  url: `https://github.com/AppImage/type2-runtime/releases/download/${TAG}/${ASSET}`,
  sha256: "2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d",
};

/** What the AppRun script prints; the runtime sets APPIMAGE and APPDIR. */
const APP_RUN = `#!/bin/sh
echo "pluxy-appimage-smoke arg=$1 appimage=\${APPIMAGE:+set} appdir=$APPDIR"
`;

const sha256 = (data: Uint8Array) => createHash("sha256").update(data).digest("hex");

/** A pinned file's bytes, from the cache folder or downloaded, checked against its digest every
 * time. The caller uses these bytes, never the cache file again, so nothing unchecked runs. */
export async function pinnedBytes(pin: Pin): Promise<Uint8Array> {
  const dir = join(process.env.XDG_CACHE_HOME ?? join(homedir(), ".cache"), "pluxy-app-native");
  mkdirSync(dir, { recursive: true });
  const file = join(dir, pin.cache);
  if (existsSync(file)) {
    const cached = new Uint8Array(readFileSync(file));
    if (sha256(cached) === pin.sha256) return cached;
  }
  rmSync(file, { force: true });

  const response = await fetch(pin.url);
  if (!response.ok) throw new Error(`downloading ${pin.url}: HTTP ${response.status}`);
  const bytes = new Uint8Array(await response.arrayBuffer());
  const got = sha256(bytes);
  if (got !== pin.sha256) throw new Error(`${pin.url} has SHA-256 ${got}, not the pinned ${pin.sha256}`);
  const part = `${file}.${randomUUID()}`;
  writeFileSync(part, bytes, { flag: "wx", mode: 0o600 });
  renameSync(part, file);
  return bytes;
}

/** The runtime's bytes, checked against the pinned digest. */
export async function runtimeBytes(): Promise<Uint8Array> {
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new Error(`the AppImage smoke test needs Linux on x86_64, not ${process.platform} ${process.arch}`);
  }
  return pinnedBytes(RUNTIME);
}

/** Builds the smoke AppImage in dir (an existing, empty folder) and returns its path. */
export async function buildSmokeAppImage(dir: string): Promise<string> {
  return packAppImage(dir, "smoke.AppImage", { AppRun: Buffer.from(APP_RUN) });
}

/** Packs an AppImage named name in dir (created if missing, otherwise empty) from the pinned
 * runtime and a squashfs image of files: each a path in the image, with its bytes or the file to
 * copy them from. Every file is executable. Returns the AppImage's path. */
export async function packAppImage(
  dir: string,
  name: string,
  files: Record<string, string | Uint8Array>,
): Promise<string> {
  const runtime = await runtimeBytes();
  const appDir = join(dir, "AppDir");
  mkdirSync(appDir, { recursive: true });
  for (const [path, from] of Object.entries(files)) {
    mkdirSync(dirname(join(appDir, path)), { recursive: true });
    writeFileSync(join(appDir, path), typeof from === "string" ? readFileSync(from) : from, { mode: 0o755 });
  }
  const image = join(dir, `${name.replace(/\.AppImage$/, "")}.squashfs`);
  const made = spawnSync(
    "mksquashfs",
    [appDir, image, "-root-owned", "-noappend", "-no-progress", "-quiet", "-comp", "gzip"],
    { encoding: "utf8" },
  );
  if (made.status !== 0) {
    throw new Error(`mksquashfs (package squashfs-tools) exited with ${made.status}: ${made.stderr}`);
  }
  const appImage = join(dir, name);
  writeFileSync(appImage, Buffer.concat([runtime, readFileSync(image)]), { mode: 0o755 });
  return appImage;
}

/** A file's first 16 bytes, read without running it: an ELF file is an AppImage of type 2 when
 * `AI\x02` follows at offset 8 (the AppImage specification's magic; type 1 has `AI\x01`). */
export function appImageHeader(path: string): { hex: string; elf: boolean; type?: number } {
  const bytes = Buffer.alloc(16);
  const fd = openSync(path, "r");
  try {
    readSync(fd, bytes, 0, 16, 0);
  } finally {
    closeSync(fd);
  }
  const elf = bytes.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]));
  const ai = bytes[8] === 0x41 && bytes[9] === 0x49;
  return { hex: bytes.toString("hex"), elf, ...(elf && ai ? { type: bytes[10] } : {}) };
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
