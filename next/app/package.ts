// The app's two package commands. Run from next/app (the front end built first, bun run build):
//
//   bun package.ts test <out-dir>        the test package: a disposable Linux AppImage, Windows
//                                        folder bundle and per-user setup program, or macOS app
//                                        bundle, for checks only
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
// On Windows and macOS test makes the same two debug builds with the same overlay: on macOS the
// `app` bundle and no disk image; on Windows the first build is also bundled as the per-user NSIS
// setup program (`nsis`, installMode currentUser, with the hooks in src-tauri/nsis/hooks.nsh, no
// code signing), copied to <out-dir>/pluxy-test_<version>_x64-setup.exe, and the second build is
// not bundled. The setup installs the same program set, its stubs through Tauri's externalBin
// (pluxy.exe) and resources (bundled\); the bundler's NSIS tools are put in its local tools
// folder (<target>/.tauri/NSIS) first, from pinned copies (NSIS_TOOLS, each checked against its
// SHA-256), so it downloads nothing; and the copies of the stubs Tauri's build script leaves
// beside the program in <target>/debug are removed again. Windows: each build's program goes into
// a folder of its own, <out-dir>/pluxy Test/ and <out-dir>/update/pluxy Test/, a plain folder
// bundle holding the program
// set the app's updater replaces (src-tauri/src/updater/set.rs): pluxy-app-test.exe, and until the
// bundled core exists a stub pluxy.exe and bundled\ (text files saying the version, which nothing
// runs); the update is the second folder's set as a .tar.gz made here (its parts at the top),
// signed. macOS: each build's app bundle is copied
// to <out-dir>/pluxy Test.app and <out-dir>/update/pluxy Test.app; the update is the second
// bundle as a .app.tar.gz the test makes itself (one top folder, no macOS metadata), signed; the
// first bundle's program is also copied out of it to <out-dir>/pluxy-app-test. Both write
// <out-dir>/test-package.json: {"bundle": <folder or .app>, "program": <the app executable in
// it>, "binary": <a copy of it outside the bundle on macOS, the program on Windows>,
// "updater_pubkey": <key>, "update": {"bundle": <path>, "payload": <path>, "target": <the feed's
// target name: windows-x86_64-set, darwin-aarch64-app>, "version": <version>, "signature":
// <signature>}}, and on Windows "setup": <the setup program>. The builds need nothing from the
// network there (this command fetches the pinned NSIS tools itself), so they run in the caller's
// environment, Cargo offline after `cargo fetch` and every proxy variable pointing at a closed
// port.
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
// release builds with tauri build --no-bundle: the release profile, no features, no overlay and no
// bundle format. Then the release validator (src-tauri/examples/validate-release-config.rs, a debug
// tooling build with the same config and features) checks that build's own snapshots, the staged
// trust folder, the built binary (pluxy-app.exe on Windows) and each artifact given; it unpacks an
// AppImage into a private temporary folder, reads every file of a bundle folder, and never runs
// either. The last line says `release-package: valid`,
// `release-package: refused: <reason>` or, when it could not check, `release-package: failed:
// <reason>`; the exit code is 0 only for valid.

import { spawnSync } from "node:child_process";
import {
  copyFileSync,
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
import { appImageHeader, type Pin, pinnedBytes, runtimeBytes } from "./native/appimage";
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
      `the test package is an x86_64 Linux AppImage, a Windows folder bundle or a macOS app bundle; this is ${process.platform} ${process.arch}`,
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
    // One build at each version; the second rebuilds only the app's own crate.
    const build = (v: string, bundle = true): string => {
      const overlay = join(work, `tauri.test.${v}.conf.json`);
      writeFileSync(
        overlay,
        JSON.stringify({
          version: v,
          identifier: "dev.pluxy.desktop.test",
          productName: "pluxy Test",
          mainBinaryName: "pluxy-app-test",
          bundle: { active: true, targets: ["appimage"] },
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
        env,
      );
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
    const binary = join(outDir, "pluxy-app-test");
    copyFileSync(build(version, false), binary);
    const appimage = build(version);
    const update = build(UPDATE_VERSION);
    const signature = signUpdate(key, readFileSync(update), basename(update), UPDATE_VERSION);
    writeFileSync(
      join(outDir, "test-package.json"),
      JSON.stringify({
        appimage,
        updater_pubkey: key.pubkey,
        binary,
        update: { appimage: update, version: UPDATE_VERSION, signature },
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

/** The Windows folder bundle or macOS app bundle test package (see the top of this file). */
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
    // Stand-ins for the core and its packages until the bundled core exists: text files saying
    // the version, which nothing runs. Made per version in the work folder; the folder bundle and
    // the setup program get the same.
    const stubs = (v: string) => {
      const dir = join(work, `core-${v}`);
      mkdirSync(join(dir, "bundled"), { recursive: true });
      // Tauri's name for an external program built for this target: <name>-<target triple>.exe.
      writeFileSync(join(dir, "pluxy-x86_64-pc-windows-msvc.exe"), `stub core ${v}\n`);
      writeFileSync(join(dir, "bundled", "stub.txt"), `stub core packages ${v}\n`);
      return { core: join(dir, "pluxy-x86_64-pc-windows-msvc.exe"), packages: join(dir, "bundled") };
    };
    if (windows) await placeNsisTools(work);
    // One build at each version, its bundle copied into folder; the second rebuilds only the
    // app's own crate. With setup (Windows), the build is also bundled as the setup program.
    const build = (v: string, folder: string, setup = false): string => {
      const overlay = join(work, `tauri.test.${v}.conf.json`);
      const stub = windows ? stubs(v) : undefined;
      writeFileSync(
        overlay,
        JSON.stringify({
          version: v,
          identifier: "dev.pluxy.desktop.test",
          productName: name,
          mainBinaryName: "pluxy-app-test",
          bundle: !windows
            ? { active: true, targets: ["app"] }
            : setup && stub
              ? {
                  active: true,
                  targets: ["nsis"],
                  useLocalToolsDir: true,
                  externalBin: [stub.core.replace(/-x86_64-pc-windows-msvc\.exe$/, "")],
                  resources: { [join(stub.packages, "stub.txt")]: "bundled/stub.txt" },
                  windows: {
                    nsis: { installMode: "currentUser", installerHooks: join(tauriDir, "nsis", "hooks.nsh") },
                  },
                }
              : { active: false },
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
          ...(windows && !setup ? ["--no-bundle"] : []),
        ],
        app,
        env,
      );
      mkdirSync(folder, { recursive: true });
      if (windows && stub) {
        // Tauri's build script copied the setup's external program and resources beside the
        // program in target/debug, where the dev app is built later: removed again.
        if (setup) {
          rmSync(join(targetDir, "debug", "pluxy.exe"), { force: true });
          rmSync(join(targetDir, "debug", "bundled"), { recursive: true, force: true });
        }
        // The program as built: the bundler restores it after patching the setup's copy.
        const bundle = join(folder, name);
        mkdirSync(join(bundle, "bundled"), { recursive: true });
        copyFileSync(join(targetDir, "debug", exe), join(bundle, exe));
        copyFileSync(stub.core, join(bundle, "pluxy.exe"));
        copyFileSync(join(stub.packages, "stub.txt"), join(bundle, "bundled", "stub.txt"));
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
    const bundle = build(version, outDir, windows);
    let setup: string | undefined;
    if (windows) {
      const built = join(targetDir, "debug", "bundle", "nsis", `${name}_${version}_x64-setup.exe`);
      setup = join(outDir, `pluxy-test_${version}_x64-setup.exe`);
      copyFileSync(built, setup);
    }
    const updateBundle = build(UPDATE_VERSION, join(outDir, "update"));
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
        update: { bundle: updateBundle, payload, target, version: UPDATE_VERSION, signature },
        ...(setup ? { setup } : {}),
      }),
    );
    const pinned = Object.values(NSIS_TOOLS).map((t) => `${t.cache} ${t.sha256.slice(0, 12)}`);
    console.log(
      `test package: ${bundle} (${windows ? "folder bundle" : "app bundle"}); update ${payload} for ${target}`,
    );
    if (setup) console.log(`test package: setup ${setup}; NSIS tools pinned: ${pinned.join(", ")}`);
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
  run(tauriCommand[0] as string, [...tauriCommand.slice(1), "build", "--ci", "--no-bundle"], app);
  const exe = process.platform === "win32" ? ".exe" : "";
  const binary = join(targetDir, "release", `pluxy-app${exe}`);
  const out = snapshots(binary);
  run("cargo", ["build", "--locked", "--example", "validate-release-config"], tauriDir);
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
  } else if (command === "release") {
    process.exitCode = releasePackage(args) ? 0 : 1;
  } else {
    console.error("usage: bun package.ts test <out-dir> | bun package.ts release [artifact...]");
    process.exitCode = 2;
  }
} catch (e) {
  // Not a verdict: the gate did not get to check anything.
  if (command === "release") console.log(`release-package: failed: ${(e as Error).message}`);
  console.error(`package ${command}: ${(e as Error).message}`);
  process.exitCode = 1;
}
