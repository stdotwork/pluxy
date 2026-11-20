// Fixtures for the native test's check of the execution program and the launcher
// (src-tauri/src/launcher_native.rs on Linux; on Windows and macOS
// src-tauri/src/launcher_platform_native.rs, which uses pluxy/execution, `pluxy-agent` and work/
// only):
//
// - pluxy/execution: the real execution program (next/app-tools/execution, which run.ts builds,
//   and off Linux the workflow), packed with its own manifest's operations into a signed curated
//   package;
// - pluxy/launch-relay: the disposable fixture tool (src-tauri/examples/program_fixture), whose
//   `relay` calls pluxy/execution's `launch`: another chain to the same launch;
// - pluxy/launch-page: a curated web package whose page imports pluxy/execution's `launch`.
//
// bin/ goes first on the app's PATH: `x-terminal-emulator` (the fixture program as a stand-in
// terminal, the first one Automatic picks) and `pluxy-agent` (`pluxy-agent.exe` on Windows: the
// fixture program as the agent, found by name). override/agent is another copy, for an executable override. keys.json holds the
// key the check types into native key entry, made here at random; the app never chose it.
//
// On Windows bin/ also holds `pluxy-node-agent.cmd`, an npm command shim (npm 9's cmd-shim layout,
// which the execution program reads) of `pluxy-node-agent.js`, a Node script that writes what it
// was started with to the file its first argument names; `pluxy-batch.cmd`, a batch file that is
// no shim; and `pluxy-not-a-program.exe`, a text file named as a program.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, EXE, type Fixture, platform, TOOL, writeTrustProfile } from "./admission";
import { index, native, type Package, pack } from "./routing";

const object = { type: "object" };

const relay = {
  id: "relay",
  description: "Call another package's operation through the host",
  effect: "read",
  input_schema: object,
  output_schema: object,
};

const EXECUTION = "pluxy/execution";
const launchImport = { package: EXECUTION, contract: [1], operations: ["launch"], required: true };

/** npm 9's command shim of a Node script at rel in the shim's folder, as cmd-shim 9.0.2 writes it
 * (next/app-tools/execution/src/shim_tests.rs, NPM9_NODE). */
function npmShim(rel: string): string {
  return [
    "@ECHO off",
    "GOTO start",
    ":find_dp0",
    "SET dp0=%~dp0",
    "EXIT /b",
    ":start",
    "SETLOCAL",
    "CALL :find_dp0",
    "",
    'IF EXIST "%dp0%\\node.exe" (',
    '  SET "_prog=%dp0%\\node.exe"',
    ") ELSE (",
    '  SET "_prog=node"',
    ")",
    "",
    `endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & "%_prog%"  "%dp0%\\${rel}" %*`,
    "",
  ].join("\r\n");
}

/** The Node agent: what it was started with (its argv, folder, the program running it, whether
 * that is Bun, and whether its output is a console), written whole to the file its first argument
 * names. */
const NODE_AGENT = `"use strict";
const fs = require("node:fs");
const record = process.argv[2];
const seen = {
  pid: process.pid,
  argv: process.argv,
  cwd: process.cwd(),
  exec_path: process.execPath,
  bun: process.versions.bun ?? null,
  tty: process.stdout.isTTY === true,
};
fs.writeFileSync(record + ".tmp", JSON.stringify(seen));
fs.renameSync(record + ".tmp", record);
`;

export interface Launcher {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The key: it must not show in the app's output. */
  canaries: string[];
  /** The folder to put first on the app's PATH. */
  bin: string;
}

/** Builds the fixtures in root: execution (the built execution program, if there is one) and
 * manifest (its pluxy-extension.json), program (the built fixture tool) as the relay tool, the
 * terminal and the agent, and the test trust profile in home (the app's HOME). */
export function buildLauncherFixtures(
  root: string,
  home: string,
  program: string,
  execution: string,
  manifest: string,
): Launcher {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const packages: [Package, string][] = [];
  // Before the execution program exists there is nothing to pack; the check then fails at its
  // first step, the launcher's state, which the app does not have either.
  if (existsSync(execution) && existsSync(manifest)) {
    const m = JSON.parse(readFileSync(manifest, "utf8"));
    packages.push([
      {
        id: m.id,
        version: m.version,
        tier: "native",
        app: { ...m.app, programs: { [platform()]: TOOL } },
      },
      execution,
    ]);
    // The check installs the release its manifest names.
    writeFileSync(join(root, "execution-version"), m.version);
  }
  packages.push([
    native("pluxy/launch-relay", "1.0.0", { operations: [relay], imports: [launchImport] }),
    program,
  ]);
  packages.push([
    {
      id: "pluxy/launch-page",
      version: "1.0.0",
      tier: "web",
      page: true,
      app: { version: 0, tier: "web", host_versions: [0], imports: [launchImport] },
    },
    program,
  ]);
  const curated: [Package, Record<string, unknown>][] = [];
  for (const [p, prog] of packages) {
    const file = pack(root, p, prog);
    const f: Fixture = { file, id: p.id, version: p.version, tier: p.tier };
    curated.push([p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));
  const bin = join(root, "bin");
  const override = join(root, "override");
  mkdirSync(bin);
  mkdirSync(override);
  for (const to of [
    join(bin, "x-terminal-emulator"),
    join(bin, `pluxy-agent${EXE}`),
    join(override, "agent"),
  ]) {
    copyFileSync(program, to);
    chmodSync(to, 0o755);
  }
  if (process.platform === "win32") {
    writeFileSync(join(bin, "pluxy-node-agent.cmd"), npmShim("pluxy-node-agent.js"));
    writeFileSync(join(bin, "pluxy-node-agent.js"), NODE_AGENT);
    // A plain batch file: cmd.exe would run it, and read its arguments its own way.
    writeFileSync(join(bin, "pluxy-batch.cmd"), '@echo off\r\nnode "%~dp0\\pluxy-node-agent.js" %*\r\n');
    writeFileSync(join(bin, "pluxy-not-a-program.exe"), "not a program\r\n");
  }
  // Off Linux, the agents' working folders: each agent exits once a file named stop is in the
  // folder of its record (the check makes a folder for each launch inside work).
  if (process.platform !== "linux") mkdirSync(join(root, "work"));
  const keys = { key: `pluxy-launcher-key-${randomBytes(16).toString("hex")}` };
  writeFileSync(join(root, "keys.json"), JSON.stringify(keys));
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [keys.key], bin };
}
