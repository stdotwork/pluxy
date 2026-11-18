// The signed application update scenario on Linux, run inside a native session (session.ts starts
// it in place of the app; update.ts starts the session):
//
//   bun native/update-session.ts <run folder> <test package JSON> <update key's public key>
//
// The cases and their checks are update-cases.ts's, the same as on Windows and macOS
// (platform.ts). Here it serves the fake https feed, puts stand-ins for pkexec, sudo, dpkg, rpm,
// apt, dnf and zypper first on PATH that only log being run, and launches the test package
// (native-test build, PLUXY_APP_TEST_UPDATE, update_native.rs) on one profile:
//
// 1. the AppImage without its trust profile's nonce (no updater trust): refused;
// 2. its program with the bundle type a .deb and an .rpm would give it (the bundler's own patch),
//    and as built (no package): refused, the .deb one with APPIMAGE pointing at the AppImage;
// 3. the AppImage, in managed mode, which updates and restarts itself: refusals, an update its
//    new version does not accept (undone), the update with a third launch in its acceptance gap,
//    a rollback.
//
// Output: `update: ...` lines, and `update-problem: ...` for each failed check.

import { chmodSync, copyFileSync, readFileSync, writeFileSync } from "node:fs";
import { basename, join } from "node:path";
import {
  checkSteps,
  NO_ROOTS,
  pinOf,
  privateFolder,
  REFUSALS,
  refusedLaunch,
  sha256,
  standIns,
  startUpdateFeed,
  updateRoute,
  withPathFirst,
} from "./update-cases";

const [dirArg, packageJson, pubkey] = process.argv.slice(2);
if (!dirArg || !packageJson || !pubkey) {
  console.error("usage: bun native/update-session.ts <run folder> <test package JSON> <public key>");
  process.exit(2);
}
const dir: string = dirArg;
interface TestPackage {
  appimage: string;
  binary: string;
  update: { appimage: string; version: string; signature: string };
}
const pkg: TestPackage = JSON.parse(readFileSync(packageJson, "utf8"));
const TARGET = "linux-x86_64-appimage";

const problems: string[] = [];
const say = (line: string) => console.log(`update: ${line}`);
function check(ok: unknown, what: string) {
  if (!ok) {
    problems.push(what);
    console.log(`update-problem: ${what}`);
  }
}

// The installed AppImage: a copy in a folder of this user's, as a person keeps one.
const installDir = privateFolder(join(dir, "install"));
const installed = join(installDir, "pluxy-test.AppImage");
copyFileSync(pkg.appimage, installed);
chmodSync(installed, 0o755);
const v1Sha = pinOf(installed);
writeFileSync(join(dir, "v1.sha256"), v1Sha);
const v2 = readFileSync(pkg.update.appimage);
const v2Sha = sha256(v2);

// The program with the bundle type a package of each kind gives it: tauri-bundler 2.10.4
// (bundle.rs, patch_binary) replaces the first __TAURI_BUNDLE_TYPE_VAR_UNK with the type's.
function patched(kind: "DEB" | "RPM" | "UNK"): string {
  const bytes = Buffer.from(readFileSync(pkg.binary));
  const token = Buffer.from("__TAURI_BUNDLE_TYPE_VAR_UNK");
  const at = bytes.indexOf(token);
  if (at < 0) throw new Error(`${pkg.binary} has no unpatched bundle type`);
  bytes.write(`__TAURI_BUNDLE_TYPE_VAR_${kind}`, at, "latin1");
  const out = join(dir, `pluxy-app-test-${kind.toLowerCase()}`);
  writeFileSync(out, bytes, { mode: 0o755 });
  return out;
}

// Stand-ins for what installs packages with privileges: each logs its name and arguments.
const stand = standIns(dir, ["pkexec", "sudo", "dpkg", "rpm", "apt", "apt-get", "dnf", "zypper"]);
const feed = startUpdateFeed({
  dir,
  payload: v2,
  file: basename(pkg.update.appimage),
  target: TARGET,
  version: pkg.update.version,
  signature: pkg.update.signature,
  pubkey,
});
const env: Record<string, string> = {
  ...withPathFirst(process.env, stand.bin),
  PLUXY_APP_TEST_UPDATE: dir,
  PLUXY_APP_TEST_FEED: feed.url,
  PLUXY_APP_TEST_FEED_CA: feed.ca,
};
const unchanged = () => pinOf(installed) === v1Sha;
const nonce = process.env.PLUXY_TEST_TRUST_NONCE ?? "";
const seen = new Set<string>();
const add = (names: Set<string>) => {
  for (const n of names) seen.add(n);
};

// 1. No updater trust: the profile is there, but not this launch's nonce.
{
  const { PLUXY_TEST_TRUST_NONCE: _, ...noNonce } = env;
  add(
    await refusedLaunch({ refusal: NO_ROOTS, program: installed, env: noNonce, check, say, feed, unchanged }),
  );
}
// 2. The program installed by a package manager, or by no package at all.
const kinds = { deb: "DEB", rpm: "RPM", unknown: "UNK" } as const;
for (const refusal of REFUSALS.linux) {
  const kind = kinds[refusal.case as keyof typeof kinds];
  add(
    await refusedLaunch({
      refusal,
      program: patched(kind),
      env: {
        ...env,
        PLUXY_TEST_TRUST_NONCE: nonce,
        // A .deb install that says it is the AppImage: its bundle type still decides.
        ...(kind === "DEB" ? { APPIMAGE: installed, APPDIR: dir } : {}),
      },
      check,
      say,
      feed,
      unchanged,
    }),
  );
}
check(stand.ran() === "", `a package installer ran: ${stand.ran()}`);

// 3. The update route, which restarts the app four times.
add(
  await updateRoute({
    program: installed,
    installed,
    third: installed,
    env: { ...env, PLUXY_TEST_TRUST_NONCE: nonce },
    dir,
    v1: v1Sha,
    v2: v2Sha,
    v2Version: pkg.update.version,
    managed: true,
    feed,
    check,
    say,
  }),
);
checkSteps(check, "linux", seen);
check(stand.ran() === "", `a package installer ran: ${stand.ran()}`);
feed.stop();
say(`requests: ${JSON.stringify(Object.fromEntries(feed.requests))}`);
say(problems.length === 0 ? "done" : "FAILED");
process.exit(0);
