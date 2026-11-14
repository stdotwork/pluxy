// The signed application update scenario, run inside a native session (session.ts starts it in
// place of the app; update.ts starts the session):
//
//   bun native/update-session.ts <run folder> <test package JSON> <update key's public key>
//
// It serves a fake https release feed whose releases the app's check switches (by the word it
// writes into <run folder>/feed-variant), puts stand-ins for pkexec, sudo, dpkg, rpm, apt, dnf and
// zypper first on PATH that only log being run, and launches the test package (native-test
// build, PLUXY_APP_TEST_UPDATE, update_native.rs) on one profile:
//
// 1. the AppImage without its trust profile's nonce (no updater trust): refused;
// 2. its program with the bundle type a .deb and an .rpm would give it (the bundler's own patch),
//    and as built (no package): refused, the .deb one with APPIMAGE pointing at the AppImage;
// 3. the AppImage, in managed mode, which updates and restarts itself: refusals, an update its
//    new version does not accept (undone), the update, a rollback.
//
// Then it checks what the app reported against what it saw itself: its feed's requests per
// launch, the installed AppImage's digest, the stand-ins' log, and the versions that ran.
// Output: `update: ...` lines, and `update-problem: ...` for each failed check.

import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { certificates } from "./feed";
import { keyIdOf, signUpdate, updaterKey } from "./minisign";

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
const LAUNCH_MS = 240_000;
const CHAIN_MS = 600_000;
const TARGET = "linux-x86_64-appimage";

const problems: string[] = [];
const say = (line: string) => console.log(`update: ${line}`);
function check(ok: unknown, what: string) {
  if (!ok) {
    problems.push(what);
    console.log(`update-problem: ${what}`);
  }
}
const sha256 = (b: Uint8Array) => createHash("sha256").update(b).digest("hex");

// The installed AppImage: a copy in a folder of this user's, as a person keeps one.
const installDir = join(dir, "install");
mkdirSync(installDir, { mode: 0o700 });
const installed = join(installDir, "pluxy-test.AppImage");
copyFileSync(pkg.appimage, installed);
chmodSync(installed, 0o755);
const v1Sha = sha256(readFileSync(installed));
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
const fakeBin = join(dir, "fake-bin");
const privileged = join(dir, "privileged.log");
mkdirSync(fakeBin, { mode: 0o700 });
for (const name of ["pkexec", "sudo", "dpkg", "rpm", "apt", "apt-get", "dnf", "zypper"]) {
  writeFileSync(join(fakeBin, name), `#!/bin/sh\necho "${name} $*" >> '${privileged}'\nexit 1\n`, {
    mode: 0o755,
  });
}

// The feed. Its update entries name artifacts on this server; each variant is one feed.
const certs = certificates(join(dir, "feed"));
const requests = new Map<string, number>();
const count = (path: string) => requests.get(path) ?? 0;
let base = "";
const other = updaterKey(keyIdOf(pubkey)); // not trusted by the app; the trusted key's ID
const badSignature = signUpdate(other, v2, "pluxy-test_0.2.0_amd64.AppImage", pkg.update.version);
const app = (version: string, channel: string, update?: { url: string; signature: string }) => ({
  product: "app",
  version,
  channel,
  published: "2026-11-01T09:00:00Z",
  notes: `Release ${version}.`,
  ...(update ? { update: { [TARGET]: { ...update, size: v2.length } } } : {}),
});
function feed(variant: string) {
  const good = { url: `${base}/artifacts/v2.AppImage`, signature: pkg.update.signature };
  const releases = {
    lower: [app("0.0.9", "stable"), app("0.1.0", "stable")],
    good: [app("0.1.0", "stable"), app(pkg.update.version, "stable", good)],
    slow: [
      app("0.1.0", "stable"),
      app(pkg.update.version, "stable", { ...good, url: `${base}/artifacts/v2-slow.AppImage` }),
    ],
    badsig: [app("0.1.0", "stable"), app(pkg.update.version, "stable", { ...good, signature: badSignature })],
    // 0.2.0's artifact and signature announced as 0.3.0.
    mismatch: [app("0.1.0", "stable"), app("0.3.0", "stable", good)],
  }[variant];
  if (!releases) throw new Error(`no feed variant ${variant}`);
  return { format: 1, releases };
}
const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  tls: { cert: readFileSync(certs.cert, "utf8"), key: readFileSync(certs.key, "utf8") },
  fetch(req): Response {
    const path = new URL(req.url).pathname;
    requests.set(path, count(path) + 1);
    if (req.method !== "GET") return new Response("no", { status: 405 });
    if (path === "/feed.json") {
      const variantFile = join(dir, "feed-variant");
      const variant = existsSync(variantFile) ? readFileSync(variantFile, "utf8").trim() : "good";
      return Response.json(feed(variant));
    }
    if (path === "/artifacts/v2.AppImage") {
      return new Response(v2, { headers: { "content-length": String(v2.length) } });
    }
    if (path === "/artifacts/v2-slow.AppImage") {
      // 64 KiB every 200 ms: the check cancels it long before the end.
      let at = 0;
      let open = true;
      const body = new ReadableStream({
        async pull(c) {
          await Bun.sleep(200);
          if (!open) return;
          const end = Math.min(at + 65536, v2.length);
          c.enqueue(v2.subarray(at, end));
          at = end;
          if (at >= v2.length) c.close();
        },
        cancel() {
          open = false;
        },
      });
      return new Response(body, { headers: { "content-length": String(v2.length) } });
    }
    return new Response("not here", { status: 404 });
  },
});
base = `https://127.0.0.1:${server.port}`;
const artifactRequests = () => count("/artifacts/v2.AppImage") + count("/artifacts/v2-slow.AppImage");

type Step = Record<string, unknown>;
/** One launch and the processes it starts: their `native-test:` steps, until all have ended. */
async function launch(label: string, program: string, extra: Record<string, string | undefined>, ms: number) {
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries({ ...process.env, ...extra })) if (v !== undefined) env[k] = v;
  env.PATH = `${fakeBin}:${process.env.PATH ?? "/usr/bin:/bin"}`;
  env.PLUXY_APP_TEST_UPDATE = dir;
  env.PLUXY_APP_TEST_FEED = `${base}/feed.json`;
  env.PLUXY_APP_TEST_FEED_CA = certs.ca;
  const child = spawn(program, [], { env, stdio: ["ignore", "pipe", "pipe"] });
  const steps: [string, Step][] = [];
  const tail: string[] = [];
  for (const stream of [child.stdout, child.stderr]) {
    createInterface({ input: stream }).on("line", (line) => {
      const m = /^native-test: (\S+) (.*)$/.exec(line);
      if (m) {
        try {
          steps.push([m[1] as string, JSON.parse(m[2] as string)]);
        } catch {
          steps.push([m[1] as string, { raw: m[2] }]);
        }
        if ((m[1] as string).startsWith("update") || m[1] === "failed")
          say(`${label} | ${line.slice(0, 1500)}`);
      }
      tail.push(line);
      if (tail.length > 40) tail.shift();
    });
  }
  // "close": every process holding its output has ended, the restarted ones too.
  const ended = await new Promise<string>((resolve) => {
    const timer = setTimeout(() => {
      check(false, `${label}: did not end within ${ms / 1000}s`);
      child.kill("SIGKILL");
    }, ms);
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      resolve(signal ?? String(code));
    });
  });
  const failed = steps.find(([s]) => s === "failed");
  if (failed) check(false, `${label}: the app's check failed: ${JSON.stringify(failed[1]).slice(0, 2000)}`);
  if (failed) say(`${label}: last lines:\n  ${tail.join("\n  ")}`);
  const step = (name: string) => steps.filter(([s]) => s === name).map(([, v]) => v);
  return { ended, step };
}

const refusedBy = (label: string, s: Step | undefined, reason: RegExp, before: number) => {
  check(s, `${label}: no report`);
  if (!s) return;
  const call = s.call as { ok?: boolean; error?: string } | undefined;
  check(
    reason.test(String(s.reason)),
    `${label}: the page's reason ${JSON.stringify(s.reason)} is not ${reason}`,
  );
  check(s.offered === false, `${label}: Update Now was offered`);
  check(
    call?.ok === false && reason.test(String(call.error)),
    `${label}: app_update_start: ${JSON.stringify(call)}`,
  );
  check(sha256(readFileSync(installed)) === v1Sha, `${label}: the installed AppImage changed`);
  check(artifactRequests() === before, `${label}: the update was downloaded`);
};

const nonce = process.env.PLUXY_TEST_TRUST_NONCE;
// 1. No updater trust: the profile is there, but not this launch's nonce.
{
  const before = artifactRequests();
  const r = await launch(
    "no roots",
    installed,
    { PLUXY_APP_TEST_UPDATE_CASE: "no-roots", PLUXY_TEST_TRUST_NONCE: undefined },
    LAUNCH_MS,
  );
  refusedBy("no roots", r.step("update_no_roots")[0], /not configured|trust/, before);
}
// 2. The program installed by a package manager, or by no package at all.
for (const [kind, label, reason] of [
  ["DEB", "deb", /\.deb package.*package manager/s],
  ["RPM", "rpm", /\.rpm package.*package manager/s],
  ["UNK", "unknown", /not an AppImage|unknown/],
] as const) {
  const before = artifactRequests();
  const r = await launch(
    label,
    patched(kind),
    {
      PLUXY_APP_TEST_UPDATE_CASE: label,
      PLUXY_TEST_TRUST_NONCE: nonce,
      // A .deb install that says it is the AppImage: its bundle type still decides.
      ...(kind === "DEB" ? { APPIMAGE: installed, APPDIR: dir } : {}),
    },
    LAUNCH_MS,
  );
  refusedBy(label, r.step(`update_${label}`)[0], reason, before);
}
check(
  !existsSync(privileged),
  `a package installer ran: ${existsSync(privileged) ? readFileSync(privileged, "utf8") : ""}`,
);

// 3. The update route, which restarts the app four times.
{
  const r = await launch(
    "update",
    installed,
    { PLUXY_APP_TEST_UPDATE_CASE: "update", PLUXY_TEST_TRUST_NONCE: nonce },
    CHAIN_MS,
  );
  const stages = r.step("update_stage");
  const seen = stages.map((s) => `${s.stage}@${s.version}`);
  say(`versions that ran: ${seen.join(", ")}`);
  check(
    JSON.stringify(seen) ===
      JSON.stringify(["first@0.1.0", "refused@0.1.0", "updated@0.2.0", "rolled-back@0.1.0"]),
    `the update route ran ${JSON.stringify(seen)}`,
  );
  const at = (name: string) => stages.find((s) => s.stage === name) ?? {};
  for (const [name, sha] of [
    ["first", v1Sha],
    ["refused", v1Sha],
    ["updated", v2Sha],
    ["rolled-back", v1Sha],
  ] as const)
    check(
      at(name).installed_sha === sha,
      `${name}: the installed AppImage is not ${name === "updated" ? "v2" : "v1"}`,
    );
  const pids = stages.map((s) => s.core_pid);
  check(
    new Set(pids).size === pids.length && pids.every((p) => typeof p === "number"),
    `the core was not stopped and started again across restarts: ${JSON.stringify(pids)}`,
  );
  const refusals = r.step("update_refusals")[0] ?? {};
  const lower = refusals.lower as Step | undefined;
  check(
    lower?.offered === false && (lower.call as Step)?.ok === false,
    `lower feed: ${JSON.stringify(lower)}`,
  );
  for (const k of ["badsig", "mismatch", "cancelled"]) {
    check((refusals[k] as Step)?.installed_sha === v1Sha, `${k}: the installed AppImage changed`);
  }
  const plugin = refusals.plugin as Step | undefined;
  check(
    plugin?.ok === false && /not allowed/.test(String(plugin.error)),
    `the window reached the updater plugin: ${JSON.stringify(plugin)}`,
  );
  check(
    /signature/i.test(String((refusals.badsig as Step)?.error)),
    `bad signature: ${JSON.stringify(refusals.badsig)}`,
  );
  check(
    /version/i.test(String((refusals.mismatch as Step)?.error)),
    `signed version: ${JSON.stringify(refusals.mismatch)}`,
  );
  check(
    /did not start|not accept/i.test(String((at("refused").last as Step)?.detail)) &&
      (at("refused").last as Step)?.result === "failed",
    `the refused update was not reported undone: ${JSON.stringify(at("refused").last)}`,
  );
  check(r.step("update_refused")[0]?.hook_left === false, "the new version never reached its acceptance");
  check(
    (at("updated").last as Step)?.result === "installed",
    `the update was not reported installed: ${JSON.stringify(at("updated").last)}`,
  );
  const offered = (at("updated").rollback as Step[] | undefined) ?? [];
  check(
    offered.some((o) => o.version === "0.1.0" && o.sha256 === v1Sha),
    `0.1.0 (${v1Sha}) is not offered to roll back to: ${JSON.stringify(offered)}`,
  );
  const rb = r.step("update_rollback_refusals")[0] ?? {};
  check(
    (rb.unknown as Step)?.ok === false && rb.no_dialog === true,
    `rollback to a version never installed: ${JSON.stringify(rb.unknown)}`,
  );
  check(
    (rb.tampered as Step)?.ok === false && rb.no_dialog_tampered === true,
    `rollback to a changed kept copy: ${JSON.stringify(rb.tampered)}`,
  );
  check(
    /0\.1\.0/.test(String(rb.dialog)) && String(rb.dialog).includes(v1Sha.slice(0, 16)),
    `the rollback confirmation does not show 0.1.0 and its digest: ${rb.dialog}`,
  );
  check(
    (rb.cancelled as Step)?.installed_sha === v2Sha,
    "a cancelled rollback changed the installed AppImage",
  );
  check(
    (at("rolled-back").last as Step)?.result === "rolled-back",
    `the rollback was not reported: ${JSON.stringify(at("rolled-back").last)}`,
  );
  check(r.step("update_done").length === 1, "the update route did not finish");
  check(sha256(readFileSync(installed)) === v1Sha, "the AppImage installed at the end is not v1");
  // Downloads: one each for the bad signature, the signed version, the refused and the real
  // update; the slow one, cancelled.
  check(
    count("/artifacts/v2.AppImage") === 4,
    `v2 was downloaded ${count("/artifacts/v2.AppImage")} times, not 4`,
  );
  check(
    count("/artifacts/v2-slow.AppImage") === 1,
    `the slow download was requested ${count("/artifacts/v2-slow.AppImage")} times`,
  );
}
check(!existsSync(privileged), "a package installer ran");
server.stop(true);
say(`requests: ${JSON.stringify(Object.fromEntries(requests))}`);
say(problems.length === 0 ? "done" : "FAILED");
process.exit(0);
