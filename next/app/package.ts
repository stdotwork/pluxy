// The app's three package commands. Run from next/app (the front end built first, bun run build):
//
//   bun package.ts test <out-dir>        the test package: a disposable Linux AppImage, Windows
//                                        folder bundle and per-user setup program, or macOS app
//                                        bundle and disk image, for checks only
//   bun package.ts local <out-dir>       the local build: a Linux app a person can open and use,
//                                        with a core and the real providers inside; not a release
//   bun package.ts release [artifact...] the release gate: builds the release app and checks it
//                                        and each artifact given (an AppImage, a Windows folder
//                                        bundle or a macOS app bundle); it uploads nothing
//
// test builds with tauri build --debug, the native-test and artifact-test-trust features and a test
// config overlay given with --config: identifier dev.pluxy.desktop.test, product name "pluxy Test",
// binary pluxy-app-test, the AppImage bundle (the base config keeps bundling off) and a temporary
// updater key made here, whose private key stays in this process and is never written. It builds
// the package twice: at the app's version, and at UPDATE_VERSION, the update the native test
// installs, which it signs with that key (native/minisign.ts). It checks that each AppImage is of
// type 2, copies them to <out-dir>/pluxy-test_<version>_amd64.AppImage, copies the first build's
// program as built (before the bundler patches its bundle type) to <out-dir>/pluxy-app-test,
// and writes <out-dir>/test-package.json:
// {"appimage": <path>, "updater_pubkey": <key>, "binary": <path>,
//  "update": {"appimage": <path>, "version": <version>, "signature": <signature>}}.
//
// Each build comes with a core of its own (src-tauri/src/bundled_core.rs): test builds the app's
// version with the core version the app names (core-release.json) and UPDATE_VERSION with
// UPDATE_CORE_VERSION, each a test-signed release made here (a core built with Go at that version,
// with the test packages of `go run ./e2e/panelcore -probe -prepare` as its bundled/ packages),
// signed with a publisher key made here whose private key stays in this process. Before each build
// the bundle-core tool (src-tauri/examples/bundle-core.rs) checks the release against its signed
// descriptor with that key and unpacks it; the build compiles the manifest it writes
// (PLUXY_BUNDLED_CORE) and carries the files: on Linux and macOS the program as Tauri's
// externalBin (beside the app's program) and bundled/ as the resource core/bundled (the AppImage's
// usr/lib/<product>/core/bundled, the app bundle's Contents/Resources/core/bundled); on Windows
// pluxy.exe beside the app's program and bundled\ beside it, as externalBin and the resource
// bundled in the setup program, copied there in the folder bundle. test-package.json names them:
// "bundled_core": {"version", "packages": <the IDs bundled/index.json lists>}, and the same for
// the update in "update".
//
// On Windows and macOS test makes the same two debug builds with the same overlay: on macOS the
// `app` bundle, and for the first build also the `dmg` (Tauri's disk image: the bundle and a link
// to /Applications on an HFS+ volume, unsigned), copied to
// <out-dir>/pluxy-test_<version>_<arch>.dmg; on Windows the first build is also bundled as the
// per-user NSIS setup program (`nsis`, installMode currentUser, with the hooks in
// src-tauri/nsis/hooks.nsh, no code signing), copied to
// <out-dir>/pluxy-test_<version>_x64-setup.exe, and the second build is not bundled. The setup
// installs the same program set, the core through Tauri's externalBin (pluxy.exe) and resources
// (bundled\); the bundler's NSIS tools are put in its local tools folder (<target>/.tauri/NSIS)
// first, from pinned copies (NSIS_TOOLS, each checked against its SHA-256), so it downloads
// nothing; and the copies of the core Tauri's build script leaves beside the program in
// <target>/debug are removed again. Windows: each build's program goes into a folder of its own,
// <out-dir>/pluxy Test/ and <out-dir>/update/pluxy Test/, a plain folder bundle holding the program
// set the app's updater replaces (src-tauri/src/updater/set.rs): pluxy-app-test.exe, pluxy.exe
// and bundled\, the build's core; the update is the second folder's set as a .tar.gz made here
// (its parts at the top), signed. macOS: each build's app bundle is copied
// to <out-dir>/pluxy Test.app and <out-dir>/update/pluxy Test.app; the update is the second
// bundle as a .app.tar.gz the test makes itself (one top folder, no macOS metadata), signed; the
// first bundle's program is also copied out of it to <out-dir>/pluxy-app-test. Both write
// <out-dir>/test-package.json: {"bundle": <folder or .app>, "program": <the app executable in
// it>, "binary": <a copy of it outside the bundle on macOS, the program on Windows>,
// "updater_pubkey": <key>, "update": {"bundle": <path>, "payload": <path>, "target": <the feed's
// target name: windows-x86_64-set, darwin-aarch64-app>, "version": <version>, "signature":
// <signature>}}, on Windows "setup": <the setup program> and on macOS "dmg": <the disk image>. The
// builds need nothing from the network there (this command fetches the pinned NSIS tools itself),
// so they run in the caller's environment, Cargo offline after `cargo fetch` and every proxy
// variable pointing at a closed port.
//
// Tauri's AppImage bundler runs tools it downloads when they are missing from its tools folder.
// test puts pinned copies there first (BUNDLER_TOOLS, each checked against its SHA-256), gives the
// AppImage plugin the pinned AppImage runtime (LDAI_RUNTIME_FILE), and runs the build in a cleared
// environment: a temporary HOME, cache folder and Cargo home (links to the dependency caches only),
// no credentials, Cargo offline after `cargo fetch`,
// and every proxy variable pointing at a closed port, so a download that is still tried fails the
// build. After the build, the tools folder must hold only the pinned tools and the two plugin
// scripts the bundler writes from its own copy.
//
// local builds the app for this Linux computer with tauri build --no-bundle: the release profile,
// the local-build feature (the window's title says "local build", and the program carries the
// test build marker the release gate refuses) and an overlay: identifier dev.pluxy.desktop.local,
// product name "pluxy Local", binary pluxy-app-local, so its settings and data folders are its
// own. No native-test feature: it runs no checks and stays open. Its core is a release made here
// as the test package's are, but with the real provider extensions of ./extensions as its
// bundled/ packages (`go run ./cmd/pluxy-pack`), signed with a key made here and dropped when the
// command ends. It writes the installed layout bundled_core.rs `place` expects:
// <out-dir>/bin/pluxy-app-local and <out-dir>/bin/pluxy, <out-dir>/lib/pluxy Local/core/bundled/,
// and <out-dir>/local-build.json naming the program and the core's packages. An <out-dir> that is
// not empty is replaced only if it holds a local-build.json. It has no updater key, so it never
// updates itself. It also comes with the coding-tool add-ons (the native tools of ../app-tools):
// each one's Linux program built with Cargo, packed by `pluxy-pack -app-tools`, and each archive's
// descriptor signed with another key made here and dropped when the command ends, into
// <out-dir>/lib/pluxy Local/app-tools/ with the signed index and publisher.json (that key's ID
// and public key). The app is built with the artifact-test-trust feature too, so it accepts that
// key from a trust profile in its HOME, which <out-dir>/start (local-start.sh) writes before it
// starts the app. Run it with <out-dir>/start.
//
// release builds the release validator (src-tauri/examples/validate-release-config.rs, a debug
// tooling build with the same config and features), then the app with tauri build --no-bundle: the
// release profile, no features, no overlay and no bundle format. The validator checks that build's
// own snapshots, the staged trust folder, the built binary (pluxy-app.exe on Windows) and each
// artifact given; it unpacks an AppImage into a private temporary folder, reads every file of a
// bundle folder, and never runs either. The last line says `release-package: valid`,
// `release-package: refused: <reason>` or, when it could not check, `release-package: failed:
// <reason>`; the exit code is 0 only for valid.

import { spawnSync } from "node:child_process";
import { createHash, generateKeyPairSync, randomUUID, sign } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { arch, homedir } from "node:os";
import { basename, join, resolve } from "node:path";
import { hex, publicHex } from "./native/admission";
import { appImageHeader, type Pin, pinnedBytes, runtimeBytes } from "./native/appimage";
import { release as coreRelease } from "./native/core-steps";
import { signUpdate, updaterKey } from "./native/minisign";
import { setArchive } from "./native/set-archive";

const app = import.meta.dir;
const tauriDir = join(app, "src-tauri");
const targetDir = resolve(tauriDir, process.env.CARGO_TARGET_DIR || "target");
const tauri = join(app, "node_modules", ".bin", "tauri");
/** The Tauri CLI as a command: the package's own script, which Windows cannot start by itself, is
 * run by this Bun on Windows and macOS. */
const tauriCommand: string[] =
  process.platform === "linux"
    ? [tauri]
    : [process.execPath, join(app, "node_modules", "@tauri-apps", "cli", "tauri.js")];
const version: string = JSON.parse(readFileSync(join(tauriDir, "tauri.conf.json"), "utf8")).version;

function run(cmd: string, args: string[], cwd: string, env?: Record<string, string>) {
  const r = spawnSync(cmd, args, { cwd, env: env ?? process.env, stdio: ["ignore", "inherit", "inherit"] });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
}

/** The version of the update the test package is built at too (newer than the app's). */
const UPDATE_VERSION = "0.2.0";
/** The core version the app names (core-release.json), which it comes with. */
const CORE_VERSION: string = JSON.parse(readFileSync(join(app, "core-release.json"), "utf8")).version;
/** The core version the update build comes with: a test version, as the update is. */
const UPDATE_CORE_VERSION = "0.2.0";
const next = resolve(app, "..");
const coreProgram = process.platform === "win32" ? "pluxy.exe" : "pluxy";

/** One test build's core: its manifest, the checked files, and the packages its index lists. */
interface BundledCore {
  version: string;
  manifest: string;
  /** The program and bundled/, as the release unpacked. */
  dir: string;
  packages: string[];
}

/** The test-signed core releases the two test builds come with, checked and unpacked by the
 * bundle-core tool into work (see the top of this file), by core version. */
function bundledCores(work: string): Record<string, BundledCore> {
  const prepared = join(work, "core-prepared");
  run("go", ["run", "./e2e/panelcore", "-probe", "-prepare", prepared], next);
  return signedCores(work, [CORE_VERSION, UPDATE_CORE_VERSION], "test-package", (bundled, v) => {
    cpSync(join(prepared, "bundled"), bundled, { recursive: true });
    // Both versions bundle the same test packages: a file naming the version makes each part of
    // the program set differ between them, as two releases' do (the update session ends an
    // update after its first part, bundled\, and checks that the folder is then neither set).
    writeFileSync(join(bundled, "test-core-version.txt"), `${v}\n`);
  });
}

/** Core releases of versions, each a core built with Go at that version with the packages fill
 * puts in its bundled/ folder, signed with a publisher key made here whose private key stays in
 * this process (its ID starts with label), then checked and unpacked by the bundle-core tool into
 * work, by core version. */
function signedCores(
  work: string,
  versions: string[],
  label: string,
  fill: (bundled: string, version: string) => void,
): Record<string, BundledCore> {
  const releases = join(work, "core-releases");
  mkdirSync(releases);
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const keyId = `${label}-${randomUUID()}`;
  const entries = [];
  for (const v of versions) {
    const folder = join(releases, `core-${v}`);
    mkdirSync(folder);
    // As a release builds it (next/Dockerfile): no cgo, no local paths, the version linked in.
    run(
      "go",
      [
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags",
        `-X github.com/stdotwork/pluxy/next/internal/buildinfo.Version=${v}`,
        "-o",
        join(folder, coreProgram),
        "./cmd/pluxy",
      ],
      next,
      { ...(process.env as Record<string, string>), CGO_ENABLED: "0" },
    );
    fill(join(folder, "bundled"), v);
    entries.push(coreRelease(releases, v, folder, privateKey, keyId));
  }
  const index = join(releases, "index.json");
  writeFileSync(index, JSON.stringify({ format: 1, releases: entries }, null, 2));
  const jwk = publicKey.export({ format: "jwk" });
  const pub = Buffer.from(jwk.x ?? "", "base64url").toString("hex");
  run("cargo", ["build", "--locked", "--example", "bundle-core"], tauriDir);
  const exe = process.platform === "win32" ? ".exe" : "";
  const tool = join(targetDir, "debug", "examples", `bundle-core${exe}`);
  const cores: Record<string, BundledCore> = {};
  for (const v of versions) {
    const out = join(work, `bundled-${v}`);
    mkdirSync(out);
    run(tool, [index, v, keyId, pub, out], app);
    const dir = join(out, "core");
    const listed = JSON.parse(readFileSync(join(dir, "bundled", "index.json"), "utf8")).packages ?? {};
    cores[v] = {
      version: v,
      manifest: join(out, "bundled-core.json"),
      dir,
      packages: Object.keys(listed).sort(),
    };
  }
  return cores;
}

/** This Rust host's target triple, which Tauri's externalBin names its programs by. */
function hostTriple(): string {
  const r = spawnSync("rustc", ["-vV"], { encoding: "utf8" });
  const triple = /^host: (\S+)$/m.exec(r.stdout ?? "")?.[1];
  if (r.status !== 0 || !triple) throw new Error(`rustc -vV says no host triple: ${r.stdout}${r.stderr}`);
  return triple;
}

/** The Tauri config that carries core in the bundle (Linux, macOS, and Windows' setup program): its
 * program as externalBin, named with the target triple in a folder of its own, and bundled/ as the
 * resource core/bundled (on Windows bundled, beside the program: bundled_core.rs `place`). */
function bundledConfig(core: BundledCore, work: string, triple: string) {
  const windows = process.platform === "win32";
  const bin = join(work, `sidecar-${core.version}`);
  mkdirSync(bin, { recursive: true });
  copyFileSync(join(core.dir, coreProgram), join(bin, `pluxy-${triple}${windows ? ".exe" : ""}`));
  return {
    externalBin: [join(bin, "pluxy")],
    resources: { [join(core.dir, "bundled")]: windows ? "bundled" : "core/bundled" },
  };
}

/** Tauri copies an externalBin program beside the program it builds in target/debug: removed
 * after each build, so no later run finds a core program beside a dev build. */
function removeSidecarCopy() {
  rmSync(join(targetDir, "debug", coreProgram), { force: true });
}

/** The tools tauri-bundler 2.10.4's AppImage bundler (src/bundle/linux/appimage/linuxdeploy.rs,
 * prepare_tools) downloads unless a file of that name is in its tools folder, `$XDG_CACHE_HOME/tauri`.
 * AppRun and linuxdeploy come from the same fixed tags the bundler names; GitHub's own digest
 * for each asset (releases API) equals the SHA-256 here. The bundler would take the AppImage
 * plugin from the moving `continuous` tag: this pins the fixed tag 1-alpha-20250213-1 instead,
 * whose asset has no GitHub digest; the SHA-256 is of the file downloaded when it was pinned. To
 * move one, download it, check it the same way and change its URL and digest together. */
const BUNDLER_TOOLS: (Pin & { file: string })[] = [
  {
    file: "AppRun-x86_64",
    cache: "tauri-apprun-old-AppRun-x86_64",
    url: "https://github.com/tauri-apps/binary-releases/releases/download/apprun-old/AppRun-x86_64",
    sha256: "f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f",
  },
  {
    file: "linuxdeploy-07333c6-x86_64.AppImage",
    cache: "tauri-linuxdeploy-07333c6-x86_64.AppImage",
    url: "https://github.com/tauri-apps/binary-releases/releases/download/linuxdeploy-07333c6/linuxdeploy-x86_64.AppImage",
    sha256: "36a2d7e274d12e1050d0e9ecfe11d339ed54720b2bec464c286d53f8b07f5c62",
  },
  {
    file: "linuxdeploy-plugin-appimage.AppImage",
    cache: "linuxdeploy-plugin-appimage-1-alpha-20250213-1-x86_64.AppImage",
    url: "https://github.com/linuxdeploy/linuxdeploy-plugin-appimage/releases/download/1-alpha-20250213-1/linuxdeploy-plugin-appimage-x86_64.AppImage",
    sha256: "992d502a248e14ab185448ddf6f6e7d25558cb84d4623c354c3af350c25fccb3",
  },
];
/** What the bundler writes into its tools folder from its own embedded copies. */
const BUNDLER_SCRIPTS = ["linuxdeploy-plugin-gtk.sh", "linuxdeploy-plugin-gstreamer.sh"];

/** The NSIS tools tauri-bundler 2.10.4's NSIS bundler (src/bundle/windows/nsis/mod.rs,
 * bundle_project) downloads unless its tools folder holds them: NSIS 3.13, unpacked as the
 * folder NSIS, and the nsis_tauri_utils 0.5.3 plugin in it. The bundler pins both by SHA-1 and
 * checks the folder's NSIS.exe and the plugin against theirs (NSIS_SHA1, NSIS_EXE_SHA1,
 * NSIS_TAURI_UTILS_SHA1 there); the files here have those SHA-1s, and GitHub's own digest for each
 * asset (releases API) equals the SHA-256 here. To move one, check it the same way against the
 * bundler's pins and change its URL and digest together. */
const NSIS_TOOLS: { zip: Pin; plugin: Pin } = {
  zip: {
    cache: "tauri-nsis-3.13.zip",
    url: "https://github.com/tauri-apps/binary-releases/releases/download/nsis-3.13/nsis-3.13.zip",
    sha256: "ba63dffc4410ee89193e1cb5a41989991bd77c61068da17e3156d136b7b0b3d8",
  },
  plugin: {
    cache: "nsis_tauri_utils-v0.5.3.dll",
    url: "https://github.com/tauri-apps/nsis-tauri-utils/releases/download/nsis_tauri_utils-v0.5.3/nsis_tauri_utils.dll",
    sha256: "5ba143b5db4a87d32d6e7802e033330aae56cbceabe0d1e3ba41948385ad4709",
  },
};

/** Windows' own tar (bsdtar, which reads zip files): the one first on a runner's PATH (Git's GNU
 * tar) takes `D:` for a remote host. */
const windowsTar = () => join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe");

/** Puts the pinned NSIS tools in the bundler's local tools folder (`bundle.useLocalToolsDir`:
 * <target>/.tauri), replacing what is there, so the bundler finds them and downloads nothing. */
async function placeNsisTools(work: string) {
  const tools = join(targetDir, ".tauri");
  const nsis = join(tools, "NSIS");
  rmSync(nsis, { recursive: true, force: true });
  rmSync(join(tools, "nsis-3.13"), { recursive: true, force: true });
  mkdirSync(tools, { recursive: true });
  const zip = join(work, "nsis-3.13.zip");
  writeFileSync(zip, await pinnedBytes(NSIS_TOOLS.zip));
  run(windowsTar(), ["-xf", zip, "-C", tools], app);
  renameSync(join(tools, "nsis-3.13"), nsis);
  const plugins = join(nsis, "Plugins", "x86-unicode", "additional");
  mkdirSync(plugins, { recursive: true });
  writeFileSync(join(plugins, "nsis_tauri_utils.dll"), await pinnedBytes(NSIS_TOOLS.plugin));
}

/** The build's environment: nothing of the caller's but the toolchain's paths. Its Cargo home is a
 * temporary folder holding only links to the caller's dependency caches (registry and git, when
 * there), so the offline build finds the crates but not the caller's Cargo config or credentials.
 * callerCargo is the caller's Cargo home as an absolute path, so the links lead to it. */
function isolatedEnv(work: string, runtime: string, callerCargo: string): Record<string, string> {
  const closed = "http://127.0.0.1:9";
  const env: Record<string, string> = {
    PATH: process.env.PATH ?? "/usr/bin:/bin",
    LANG: "C.UTF-8",
    HOME: join(work, "home"),
    XDG_CACHE_HOME: join(work, "cache"),
    XDG_CONFIG_HOME: join(work, "config"),
    XDG_DATA_HOME: join(work, "data"),
    TMPDIR: join(work, "tmp"),
    CARGO_HOME: join(work, "cargo"),
    RUSTUP_HOME: process.env.RUSTUP_HOME || join(homedir(), ".rustup"),
    CARGO_TARGET_DIR: targetDir,
    CARGO_NET_OFFLINE: "true",
    LDAI_RUNTIME_FILE: runtime,
    NO_PROXY: "",
    no_proxy: "",
  };
  for (const name of ["http_proxy", "https_proxy", "all_proxy"]) {
    env[name] = closed;
    env[name.toUpperCase()] = closed;
  }
  for (const name of ["CARGO_BUILD_JOBS", "RUSTUP_TOOLCHAIN", "PKG_CONFIG_PATH"]) {
    const value = process.env[name];
    if (value) env[name] = value;
  }
  for (const dir of ["home", "cache", "config", "data", "tmp", "cargo"]) {
    mkdirSync(join(work, dir), { recursive: true, mode: 0o700 });
  }
  for (const cache of ["registry", "git"]) {
    if (existsSync(join(callerCargo, cache)))
      symlinkSync(join(callerCargo, cache), join(work, "cargo", cache));
  }
  return env;
}

async function testPackage(outDir: string) {
  if (process.platform === "win32" || process.platform === "darwin") return bundleTestPackage(outDir);
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new Error(
      `the test package is an x86_64 Linux AppImage, a Windows folder bundle and setup program or a macOS app bundle; this is ${process.platform} ${process.arch}`,
    );
  }
  mkdirSync(outDir, { recursive: true });
  const work = mkdtempSync(join(outDir, "config-"));
  try {
    const runtime = join(work, "runtime-x86_64");
    writeFileSync(runtime, await runtimeBytes(), { mode: 0o600 });
    // What the build needs from the network, fetched here by the lockfile with the caller's
    // Cargo; the build is offline.
    // One absolute Cargo home for the fetch and the build's links; Cargo reads a relative
    // CARGO_HOME from its working folder, and treats an empty one as unset.
    const callerCargo = resolve(tauriDir, process.env.CARGO_HOME || join(homedir(), ".cargo"));
    run("cargo", ["fetch", "--locked"], tauriDir, { ...process.env, CARGO_HOME: callerCargo } as Record<
      string,
      string
    >);
    const env = isolatedEnv(work, runtime, callerCargo);
    const tools = join(env.XDG_CACHE_HOME as string, "tauri");
    mkdirSync(tools, { mode: 0o700 });
    for (const tool of BUNDLER_TOOLS) {
      writeFileSync(join(tools, tool.file), await pinnedBytes(tool), { mode: 0o700 });
    }
    const key = updaterKey();
    const cores = bundledCores(work);
    const triple = hostTriple();
    // One build at each version, each with its core; the second rebuilds only the app's own crate.
    const build = (v: string, core: BundledCore, bundle = true): string => {
      const overlay = join(work, `tauri.test.${v}.conf.json`);
      writeFileSync(
        overlay,
        JSON.stringify({
          version: v,
          identifier: "dev.pluxy.desktop.test",
          productName: "pluxy Test",
          mainBinaryName: "pluxy-app-test",
          bundle: { active: true, targets: ["appimage"], ...bundledConfig(core, work, triple) },
          plugins: { updater: { pubkey: key.pubkey } },
        }),
      );
      run(
        tauri,
        [
          "build",
          "--ci",
          "--debug",
          "--features",
          "native-test,artifact-test-trust",
          "--config",
          overlay,
          ...(bundle ? [] : ["--no-bundle"]),
        ],
        app,
        { ...env, PLUXY_BUNDLED_CORE: core.manifest },
      );
      removeSidecarCopy();
      if (!bundle) return join(targetDir, "debug", "pluxy-app-test");
      const expected = [...BUNDLER_TOOLS.map((t) => t.file), ...BUNDLER_SCRIPTS].sort();
      const found = readdirSync(tools).sort();
      if (JSON.stringify(found) !== JSON.stringify(expected)) {
        throw new Error(
          `the bundler's tools folder holds ${found.join(", ")}, not only ${expected.join(", ")}`,
        );
      }
      const built = join(targetDir, "debug", "bundle", "appimage", `pluxy Test_${v}_amd64.AppImage`);
      const header = appImageHeader(built);
      if (!header.elf || header.type !== 2) {
        throw new Error(`${built} is not a type-2 AppImage (first bytes ${header.hex})`);
      }
      const appimage = join(outDir, `pluxy-test_${v}_amd64.AppImage`);
      copyFileSync(built, appimage);
      console.log(`test package: ${appimage} (type-2 AppImage, first bytes ${header.hex})`);
      return appimage;
    };
    // The program first, as built: bundling then patches its bundle type in place
    // (__TAURI_BUNDLE_TYPE_VAR_UNK to _APP), with nothing left to build.
    const core = cores[CORE_VERSION] as BundledCore;
    const updateCore = cores[UPDATE_CORE_VERSION] as BundledCore;
    const binary = join(outDir, "pluxy-app-test");
    copyFileSync(build(version, core, false), binary);
    const appimage = build(version, core);
    const update = build(UPDATE_VERSION, updateCore);
    const signature = signUpdate(key, readFileSync(update), basename(update), UPDATE_VERSION);
    writeFileSync(
      join(outDir, "test-package.json"),
      JSON.stringify({
        appimage,
        updater_pubkey: key.pubkey,
        binary,
        bundled_core: { version: core.version, packages: core.packages },
        update: {
          appimage: update,
          version: UPDATE_VERSION,
          signature,
          bundled_core: { version: updateCore.version, packages: updateCore.packages },
        },
      }),
    );
    console.log(
      `test package: bundler tools pinned: ${BUNDLER_TOOLS.map((t) => `${t.file} ${t.sha256.slice(0, 12)}`).join(", ")}; runtime from LDAI_RUNTIME_FILE; downloads blocked`,
    );
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

/** The feed's target name for the update on Windows and macOS (the app's updater, src-tauri/src/updater/). */
function bundleTarget(): string {
  if (process.platform === "win32" && arch() === "x64") return "windows-x86_64-set";
  if (process.platform === "darwin" && arch() === "arm64") return "darwin-aarch64-app";
  if (process.platform === "darwin" && arch() === "x64") return "darwin-x86_64-app";
  throw new Error(`no test package for ${process.platform} ${arch()}`);
}

/** The Windows folder bundle and setup program, or macOS app bundle, test package (see the top of
 * this file). */
async function bundleTestPackage(outDir: string) {
  const windows = process.platform === "win32";
  const target = bundleTarget();
  mkdirSync(outDir, { recursive: true });
  const work = mkdtempSync(join(outDir, "config-"));
  try {
    const callerCargo = resolve(tauriDir, process.env.CARGO_HOME || join(homedir(), ".cargo"));
    run("cargo", ["fetch", "--locked"], tauriDir, { ...process.env, CARGO_HOME: callerCargo } as Record<
      string,
      string
    >);
    const closed = "http://127.0.0.1:9";
    const env: Record<string, string> = {
      ...(process.env as Record<string, string>),
      CARGO_HOME: callerCargo,
      CARGO_TARGET_DIR: targetDir,
      CARGO_NET_OFFLINE: "true",
      NO_PROXY: "",
      no_proxy: "",
    };
    for (const name of ["http_proxy", "https_proxy", "all_proxy"]) {
      env[name] = closed;
      env[name.toUpperCase()] = closed;
    }
    const key = updaterKey();
    const name = "pluxy Test";
    const exe = windows ? "pluxy-app-test.exe" : "pluxy-app-test";
    const cores = bundledCores(work);
    const triple = hostTriple();
    if (windows) await placeNsisTools(work);
    // One build at each version, each with its core, its bundle copied into folder; the second
    // rebuilds only the app's own crate. With installer, the build is also bundled as this
    // system's installer: the disk image on macOS, the per-user setup program on Windows.
    const build = (v: string, core: BundledCore, folder: string, installer = false): string => {
      const overlay = join(work, `tauri.test.${v}.conf.json`);
      writeFileSync(
        overlay,
        JSON.stringify({
          version: v,
          identifier: "dev.pluxy.desktop.test",
          productName: name,
          mainBinaryName: "pluxy-app-test",
          bundle: windows
            ? installer
              ? {
                  active: true,
                  targets: ["nsis"],
                  useLocalToolsDir: true,
                  ...bundledConfig(core, work, triple),
                  windows: {
                    nsis: { installMode: "currentUser", installerHooks: join(tauriDir, "nsis", "hooks.nsh") },
                  },
                }
              : { active: false }
            : {
                active: true,
                targets: installer ? ["app", "dmg"] : ["app"],
                ...bundledConfig(core, work, triple),
              },
          plugins: { updater: { pubkey: key.pubkey } },
        }),
      );
      run(
        tauriCommand[0] as string,
        [
          ...tauriCommand.slice(1),
          "build",
          "--ci",
          "--debug",
          "--features",
          "native-test,artifact-test-trust",
          "--config",
          overlay,
          ...(windows && !installer ? ["--no-bundle"] : []),
        ],
        app,
        { ...env, PLUXY_BUNDLED_CORE: core.manifest },
      );
      removeSidecarCopy();
      mkdirSync(folder, { recursive: true });
      if (windows) {
        // Tauri's build script copied the setup's resources beside the program in target/debug,
        // where the dev app is built later: removed again (removeSidecarCopy took the core).
        if (installer) rmSync(join(targetDir, "debug", "bundled"), { recursive: true, force: true });
        // The folder bundle: the program as built (the bundler restores it after patching the
        // setup's copy), and its core beside it.
        const bundle = join(folder, name);
        mkdirSync(bundle);
        copyFileSync(join(targetDir, "debug", exe), join(bundle, exe));
        copyFileSync(join(core.dir, coreProgram), join(bundle, coreProgram));
        cpSync(join(core.dir, "bundled"), join(bundle, "bundled"), { recursive: true });
        return bundle;
      }
      const bundle = join(folder, `${name}.app`);
      // ditto keeps the bundle as it is (modes, the program's signature), without extended
      // attributes, resource forks or quarantine.
      run(
        "ditto",
        [
          "--norsrc",
          "--noextattr",
          "--noqtn",
          join(targetDir, "debug", "bundle", "macos", `${name}.app`),
          bundle,
        ],
        app,
      );
      return bundle;
    };
    const core = cores[CORE_VERSION] as BundledCore;
    const updateCore = cores[UPDATE_CORE_VERSION] as BundledCore;
    const bundle = build(version, core, outDir, true);
    let dmg: string | undefined;
    if (!windows) {
      // Tauri's name for it: <product>_<version>_<aarch64 or x64>.dmg.
      const built = join(
        targetDir,
        "debug",
        "bundle",
        "dmg",
        `${name}_${version}_${arch() === "arm64" ? "aarch64" : "x64"}.dmg`,
      );
      dmg = join(outDir, `pluxy-test_${version}_${arch()}.dmg`);
      copyFileSync(built, dmg);
    }
    let setup: string | undefined;
    if (windows) {
      const built = join(targetDir, "debug", "bundle", "nsis", `${name}_${version}_x64-setup.exe`);
      setup = join(outDir, `pluxy-test_${version}_x64-setup.exe`);
      copyFileSync(built, setup);
    }
    const updateBundle = build(UPDATE_VERSION, updateCore, join(outDir, "update"));
    const program = windows ? join(bundle, exe) : join(bundle, "Contents", "MacOS", exe);
    const binary = windows ? program : join(outDir, exe);
    if (!windows) copyFileSync(program, binary);
    let payload: string;
    if (windows) {
      payload = join(outDir, `pluxy-test_${UPDATE_VERSION}_x64.set.tar.gz`);
      writeFileSync(payload, setArchive(updateBundle, exe));
    } else {
      payload = join(outDir, `pluxy-test_${UPDATE_VERSION}_${arch()}.app.tar.gz`);
      run("tar", ["--no-mac-metadata", "-czf", payload, "-C", join(outDir, "update"), `${name}.app`], app, {
        ...(process.env as Record<string, string>),
        COPYFILE_DISABLE: "1",
      });
    }
    const signature = signUpdate(key, readFileSync(payload), basename(payload), UPDATE_VERSION);
    writeFileSync(
      join(outDir, "test-package.json"),
      JSON.stringify({
        bundle,
        program,
        binary,
        updater_pubkey: key.pubkey,
        bundled_core: { version: core.version, packages: core.packages },
        update: {
          bundle: updateBundle,
          payload,
          target,
          version: UPDATE_VERSION,
          signature,
          bundled_core: { version: updateCore.version, packages: updateCore.packages },
        },
        ...(dmg ? { dmg } : {}),
        ...(setup ? { setup } : {}),
      }),
    );
    const pinned = Object.values(NSIS_TOOLS).map((t) => `${t.cache} ${t.sha256.slice(0, 12)}`);
    console.log(
      `test package: ${bundle} (${windows ? "folder bundle" : "app bundle"}); update ${payload} for ${target}`,
    );
    if (dmg) console.log(`test package: disk image ${dmg}`);
    if (setup) console.log(`test package: setup ${setup}; NSIS tools pinned: ${pinned.join(", ")}`);
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

/** The local build's names (see the top of this file): never the release's or the test package's. */
const LOCAL = {
  identifier: "dev.pluxy.desktop.local",
  productName: "pluxy Local",
  mainBinaryName: "pluxy-app-local",
};
/** The file a local build's folder holds, which marks the folder as one this command may replace. */
const LOCAL_RECORD = "local-build.json";

/** The local build's coding-tool add-ons (see the top of this file), into dest: every app tool
 * with a Linux program, built as next-app-tools-pack.yml builds them and packed by pluxy-pack, each
 * archive's descriptor signed (the app host README, "Signed descriptor bytes") with a publisher key
 * made here, whose private key never leaves this process. Returns the tools' IDs and the key's ID. */
function localTools(work: string, dest: string): { packages: string[]; keyId: string } {
  const tools = join(next, "app-tools");
  const names: string[] = [];
  for (const folder of readdirSync(tools).sort()) {
    // pluxy-pack never packs the app's own test fixtures (appToolFixtures in cmd/pluxy-pack).
    const manifest = join(tools, folder, "pluxy-extension.json");
    if (folder === "usage-fixture" || !existsSync(manifest)) continue;
    const program = JSON.parse(readFileSync(manifest, "utf8")).app?.programs?.["linux/amd64"];
    if (typeof program === "string") names.push(basename(program));
  }
  run("cargo", ["build", "--release", "--locked", ...names.flatMap((n) => ["--bin", n])], tools);
  const built = join(resolve(tools, process.env.CARGO_TARGET_DIR || "target"), "release");
  const programs = join(work, "programs");
  mkdirSync(join(programs, "linux-amd64"), { recursive: true });
  for (const n of names) copyFileSync(join(built, n), join(programs, "linux-amd64", n));
  const packed = join(work, "tools");
  run(
    "go",
    ["run", "./cmd/pluxy-pack", "-out", packed, "-app-tools", programs, "-platforms", "linux/amd64"],
    next,
  );

  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const keyId = `local-build-${randomUUID()}`;
  const index = JSON.parse(readFileSync(join(packed, "app-tools", "index.json"), "utf8")) as {
    packages: Record<
      string,
      { releases: { version: string; artifacts: Record<string, Record<string, unknown>> }[] }
    >;
  };
  mkdirSync(dest, { recursive: true });
  for (const [id, p] of Object.entries(index.packages)) {
    for (const r of p.releases) {
      for (const [platform, a] of Object.entries(r.artifacts)) {
        const bytes = readFileSync(join(packed, "app-tools", a.url as string));
        const sha256 = createHash("sha256").update(bytes).digest("hex");
        if (sha256 !== a.sha256 || bytes.length !== a.size) {
          throw new Error(`${a.url}: its size or SHA-256 differs from the index pluxy-pack wrote`);
        }
        // The exact bytes signed, in the field order the app's verifier reads.
        const descriptor = JSON.stringify({
          id,
          version: r.version,
          tier: "native",
          platform,
          size: bytes.length,
          sha256,
        });
        a.key_id = keyId;
        a.public_key = publicHex(publicKey);
        a.descriptor = descriptor;
        a.signature = hex(sign(null, Buffer.from(descriptor, "utf8"), privateKey));
        writeFileSync(join(dest, a.url as string), bytes);
      }
    }
  }
  writeFileSync(join(dest, "index.json"), `${JSON.stringify(index, null, 2)}\n`);
  // What start reads to write the trust profile: the key's ID and public key, never the private key.
  writeFileSync(
    join(dest, "publisher.json"),
    `${JSON.stringify({ key_id: keyId, public_key: publicHex(publicKey) }, null, 2)}\n`,
  );
  return { packages: Object.keys(index.packages).sort(), keyId };
}

/** The local build (see the top of this file). */
function localBuild(outDir: string) {
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new Error(`the local build is made on x86_64 Linux; this is ${process.platform} ${process.arch}`);
  }
  if (existsSync(outDir) && readdirSync(outDir).length > 0) {
    if (!existsSync(join(outDir, LOCAL_RECORD))) {
      throw new Error(`${outDir} is not empty and holds no ${LOCAL_RECORD}: not replacing it`);
    }
    rmSync(outDir, { recursive: true, force: true });
  }
  mkdirSync(outDir, { recursive: true });
  const work = mkdtempSync(join(outDir, "work-"));
  try {
    // The real provider extensions, as the bundled registry of the core the build comes with.
    const packs = join(work, "packs");
    run("go", ["run", "./cmd/pluxy-pack", "-out", packs], next);
    const cores = signedCores(work, [CORE_VERSION], "local-build", (bundled) => {
      cpSync(join(packs, "bundled"), bundled, { recursive: true });
    });
    const core = cores[CORE_VERSION] as BundledCore;
    const lib = join(outDir, "lib", LOCAL.productName);
    const tools = localTools(work, join(lib, "app-tools"));
    const overlay = join(work, "tauri.local.conf.json");
    writeFileSync(overlay, JSON.stringify(LOCAL));
    run(
      tauri,
      [
        "build",
        "--ci",
        "--no-bundle",
        "--features",
        "local-build,artifact-test-trust",
        "--config",
        overlay,
        "--",
        "--locked",
      ],
      app,
      { ...(process.env as Record<string, string>), PLUXY_BUNDLED_CORE: core.manifest },
    );
    // The installed layout bundled_core.rs `place` reads on Linux: the core's program beside the
    // app's, and its bundled/ in the app's resource folder, lib/<product name>/core/bundled.
    const bin = join(outDir, "bin");
    mkdirSync(bin);
    mkdirSync(join(lib, "core"), { recursive: true });
    const program = join(bin, LOCAL.mainBinaryName);
    copyFileSync(join(targetDir, "release", LOCAL.mainBinaryName), program);
    copyFileSync(join(core.dir, coreProgram), join(bin, coreProgram));
    cpSync(join(core.dir, "bundled"), join(lib, "core", "bundled"), { recursive: true });
    const start = join(outDir, "start");
    copyFileSync(join(app, "local-start.sh"), start);
    chmodSync(start, 0o755);
    writeFileSync(
      join(outDir, LOCAL_RECORD),
      `${JSON.stringify(
        {
          program,
          identifier: LOCAL.identifier,
          product_name: LOCAL.productName,
          bundled_core: { version: core.version, packages: core.packages },
          start,
          app_tools: {
            index: join(lib, "app-tools", "index.json"),
            key_id: tools.keyId,
            packages: tools.packages,
          },
        },
        null,
        2,
      )}\n`,
    );
    console.log(
      `local build: core ${core.version} with ${core.packages.length} providers: ${core.packages.join(", ")}`,
    );
    console.log(`local build: ${tools.packages.length} coding-tool add-ons: ${tools.packages.join(", ")}`);
    console.log(`local build: ${outDir}`);
    console.log(`local build: run it with ${start}; its settings and data go to ${join(outDir, "home")}`);
    console.log(`local build: ${start} --real-home runs it on your own home folder instead`);
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

/** The build's OUT_DIR whose receipt the built binary carries: its snapshots describe that build. */
function snapshots(binary: string): string {
  const bytes = readFileSync(binary);
  const build = join(targetDir, "release", "build");
  for (const name of readdirSync(build)) {
    const out = join(build, name, "out");
    if (!name.startsWith("pluxy-app-") || !existsSync(join(out, "build_receipt.rs"))) continue;
    const receipt = /PLUXY_BUILD_RECEIPT_V1:[0-9a-f]{64}/.exec(
      readFileSync(join(out, "build_receipt.rs"), "utf8"),
    );
    if (receipt && bytes.includes(receipt[0])) return out;
  }
  throw new Error(`no build snapshot in ${build} matches ${binary}'s build receipt`);
}

function releasePackage(appImages: string[]): boolean {
  // The validator first: on a fresh checkout, the first build of the app writes
  // src-tauri/permissions/autogenerated (tauri-build's command permissions) while its build script
  // runs, and the next build of that profile reruns the script because the folder changed after it
  // started. Built in this order, that once is the validator's quick debug build; the release build
  // then finds the folder unchanged, and the gate's later calls find it up to date.
  run("cargo", ["build", "--locked", "--example", "validate-release-config"], tauriDir);
  run(tauriCommand[0] as string, [...tauriCommand.slice(1), "build", "--ci", "--no-bundle"], app);
  const exe = process.platform === "win32" ? ".exe" : "";
  const binary = join(targetDir, "release", `pluxy-app${exe}`);
  const out = snapshots(binary);
  const validator = spawnSync(
    join(targetDir, "debug", "examples", `validate-release-config${exe}`),
    [
      join(out, "app-tauri-config.json"),
      join(out, "app-package-trust.json"),
      join(tauriDir, "trust", "production.json"),
      join(out, "app-cargo-features.json"),
      join(tauriDir, "trust"),
      binary,
      ...appImages.map((p) => resolve(p)),
    ],
    { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  );
  const said = `${validator.stdout}${validator.stderr}`.trim();
  const refused = /^release configuration refused: (.*)$/m.exec(said);
  if (validator.status === 0 && /^release configuration valid/m.test(said)) {
    console.log(`release-package: valid: ${binary}${appImages.length ? ` and ${appImages.join(", ")}` : ""}`);
    return true;
  }
  if (refused) console.log(`release-package: refused: ${refused[1]}`);
  else console.log(`release-package: failed: the validator exited with ${validator.status}: ${said}`);
  return false;
}

const [command, ...args] = process.argv.slice(2);
try {
  if (command === "test" && args.length === 1) {
    await testPackage(resolve(args[0] as string));
  } else if (command === "local" && args.length === 1) {
    localBuild(resolve(args[0] as string));
  } else if (command === "release") {
    process.exitCode = releasePackage(args) ? 0 : 1;
  } else {
    console.error(
      "usage: bun package.ts test <out-dir> | bun package.ts local <out-dir> | bun package.ts release [artifact...]",
    );
    process.exitCode = 2;
  }
} catch (e) {
  // Not a verdict: the gate did not get to check anything.
  if (command === "release") console.log(`release-package: failed: ${(e as Error).message}`);
  console.error(`package ${command}: ${(e as Error).message}`);
  process.exitCode = 1;
}
