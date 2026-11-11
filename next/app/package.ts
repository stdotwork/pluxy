// The app's two package commands. Run from next/app (the front end built first, bun run build):
//
//   bun package.ts test <out-dir>        the test package: a disposable Linux AppImage for checks
//   bun package.ts release [AppImage...] the release gate: builds the release app and checks it
//                                        and each AppImage given; it uploads nothing
//
// test builds with tauri build --debug, the native-test and artifact-test-trust features and a test
// config overlay given with --config: identifier dev.pluxy.desktop.test, product name "pluxy Test",
// binary pluxy-app-test, the AppImage bundle (the base config keeps bundling off) and a temporary
// updater public key made here, whose private key is never written. It checks that the AppImage is
// of type 2, copies it to <out-dir>/pluxy-test_<version>_amd64.AppImage and writes
// <out-dir>/test-package.json: {"appimage": <path>, "updater_pubkey": <key>}.
//
// Tauri's AppImage bundler runs tools it downloads when they are missing from its tools folder.
// test puts pinned copies there first (BUNDLER_TOOLS, each checked against its SHA-256), gives the
// AppImage plugin the pinned AppImage runtime (LDAI_RUNTIME_FILE), and runs the build in a cleared
// environment: a temporary HOME and cache folder, no credentials, Cargo offline after `cargo fetch`,
// and every proxy variable pointing at a closed port, so a download that is still tried fails the
// build. After the build, the tools folder must hold only the pinned tools and the two plugin
// scripts the bundler writes from its own copy.
//
// release builds with tauri build --no-bundle: the release profile, no features, no overlay and no
// bundle format. Then the release validator (src-tauri/examples/validate-release-config.rs, a debug
// tooling build with the same config and features) checks that build's own snapshots, the staged
// trust folder, the built binary and each AppImage given; it unpacks an AppImage into a private
// temporary folder and never runs it. The last line says `release-package: valid`,
// `release-package: refused: <reason>` or, when it could not check, `release-package: failed:
// <reason>`; the exit code is 0 only for valid.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync, randomBytes } from "node:crypto";
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { appImageHeader, type Pin, pinnedBytes, runtimeBytes } from "./native/appimage";

const app = import.meta.dir;
const tauriDir = join(app, "src-tauri");
const targetDir = resolve(tauriDir, process.env.CARGO_TARGET_DIR ?? "target");
const tauri = join(app, "node_modules", ".bin", "tauri");
const version: string = JSON.parse(readFileSync(join(tauriDir, "tauri.conf.json"), "utf8")).version;

function run(cmd: string, args: string[], cwd: string, env?: Record<string, string>) {
  const r = spawnSync(cmd, args, { cwd, env: env ?? process.env, stdio: ["ignore", "inherit", "inherit"] });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status}`);
}

/** A temporary updater public key in the format Tauri's updater reads (base64 of minisign's
 * public key file). Its private key stays in this process and is dropped. */
function temporaryUpdaterKey(): string {
  const { publicKey } = generateKeyPairSync("ed25519");
  const raw = Buffer.from(publicKey.export({ format: "jwk" }).x as string, "base64url");
  const keyId = randomBytes(8);
  const id = Buffer.from(keyId).reverse().toString("hex").toUpperCase();
  const blob = Buffer.concat([Buffer.from("Ed"), keyId, raw]).toString("base64");
  return Buffer.from(`untrusted comment: minisign public key: ${id}\n${blob}\n`).toString("base64");
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

/** The build's environment: nothing of the caller's but the toolchain's paths. */
function isolatedEnv(work: string, runtime: string): Record<string, string> {
  const closed = "http://127.0.0.1:9";
  const env: Record<string, string> = {
    PATH: process.env.PATH ?? "/usr/bin:/bin",
    LANG: "C.UTF-8",
    HOME: join(work, "home"),
    XDG_CACHE_HOME: join(work, "cache"),
    XDG_CONFIG_HOME: join(work, "config"),
    XDG_DATA_HOME: join(work, "data"),
    TMPDIR: join(work, "tmp"),
    CARGO_HOME: process.env.CARGO_HOME ?? join(homedir(), ".cargo"),
    RUSTUP_HOME: process.env.RUSTUP_HOME ?? join(homedir(), ".rustup"),
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
  for (const dir of ["home", "cache", "config", "data", "tmp"]) {
    mkdirSync(join(work, dir), { recursive: true, mode: 0o700 });
  }
  return env;
}

async function testPackage(outDir: string) {
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new Error(
      `the test package is an x86_64 Linux AppImage; this is ${process.platform} ${process.arch}`,
    );
  }
  mkdirSync(outDir, { recursive: true });
  const work = mkdtempSync(join(outDir, "config-"));
  try {
    const runtime = join(work, "runtime-x86_64");
    writeFileSync(runtime, await runtimeBytes(), { mode: 0o600 });
    const env = isolatedEnv(work, runtime);
    const tools = join(env.XDG_CACHE_HOME as string, "tauri");
    mkdirSync(tools, { mode: 0o700 });
    for (const tool of BUNDLER_TOOLS) {
      writeFileSync(join(tools, tool.file), await pinnedBytes(tool), { mode: 0o700 });
    }
    // What the build needs from the network, fetched here by the lockfile; the build is offline.
    run("cargo", ["fetch", "--locked"], tauriDir);
    const pubkey = temporaryUpdaterKey();
    const overlay = join(work, "tauri.test.conf.json");
    writeFileSync(
      overlay,
      JSON.stringify({
        identifier: "dev.pluxy.desktop.test",
        productName: "pluxy Test",
        mainBinaryName: "pluxy-app-test",
        bundle: { active: true, targets: ["appimage"] },
        plugins: { updater: { pubkey } },
      }),
    );
    run(
      tauri,
      ["build", "--ci", "--debug", "--features", "native-test,artifact-test-trust", "--config", overlay],
      app,
      env,
    );
    const expected = [...BUNDLER_TOOLS.map((t) => t.file), ...BUNDLER_SCRIPTS].sort();
    const found = readdirSync(tools).sort();
    if (JSON.stringify(found) !== JSON.stringify(expected)) {
      throw new Error(
        `the bundler's tools folder holds ${found.join(", ")}, not only ${expected.join(", ")}`,
      );
    }
    const built = join(targetDir, "debug", "bundle", "appimage", `pluxy Test_${version}_amd64.AppImage`);
    const header = appImageHeader(built);
    if (!header.elf || header.type !== 2) {
      throw new Error(`${built} is not a type-2 AppImage (first bytes ${header.hex})`);
    }
    const appimage = join(outDir, `pluxy-test_${version}_amd64.AppImage`);
    copyFileSync(built, appimage);
    writeFileSync(join(outDir, "test-package.json"), JSON.stringify({ appimage, updater_pubkey: pubkey }));
    console.log(`test package: ${appimage} (type-2 AppImage, first bytes ${header.hex})`);
    console.log(
      `test package: bundler tools pinned: ${BUNDLER_TOOLS.map((t) => `${t.file} ${t.sha256.slice(0, 12)}`).join(", ")}; runtime from LDAI_RUNTIME_FILE; downloads blocked`,
    );
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
  run(tauri, ["build", "--ci", "--no-bundle"], app);
  const binary = join(targetDir, "release", "pluxy-app");
  const out = snapshots(binary);
  run("cargo", ["build", "--locked", "--example", "validate-release-config"], tauriDir);
  const validator = spawnSync(
    join(targetDir, "debug", "examples", "validate-release-config"),
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
    console.error("usage: bun package.ts test <out-dir> | bun package.ts release [AppImage...]");
    process.exitCode = 2;
  }
} catch (e) {
  // Not a verdict: the gate did not get to check anything.
  if (command === "release") console.log(`release-package: failed: ${(e as Error).message}`);
  console.error(`package ${command}: ${(e as Error).message}`);
  process.exitCode = 1;
}
