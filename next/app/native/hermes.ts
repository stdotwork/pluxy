// Fixtures for the native test's check of the Hermes Agent setup and launcher
// (src-tauri/src/hermes_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/hermes-setup (next/app-tools/hermes-setup, which run.ts builds) and pluxy/execution (the
//   real execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Hermes Agent's folder `~/.hermes` as a person keeps it: `config.yaml` with
//   comments, their own endpoint selected with a key variable, an old app's custom provider and a
//   setting this check does not know; `.env` and `auth.json` hold a login canary;
// - a stow layout: `~/.hermes-stow/config.yaml` is a relative link to
//   `~/dotfiles/hermes/.hermes/config.yaml` (mode 0640), which already has a `pluxy` provider with
//   the person's own model entry, beside `keep.yml`;
// - a chezmoi-style root: `~/.hermes-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_hermes`, which has no files yet;
// - files Hermes Agent would not take: `~/unknown/config.yaml` whose `providers` is a list, and
//   `~/broken/config.yaml` with a duplicate key;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `hermes`, shell scripts that record their arguments, folder and environment in records/;
//   installed/hermes is the copy the check later puts at `~/.local/bin/hermes` (the installer's
//   place), which the recipe then names.
//
// expect.json holds what the check compares with, written out by hand here, never worked out by
// the package's code: the paths, the link texts and every file's bytes after each step, with
// `@KEY@` where the key goes. keys.json holds the key the check types into native key entry, made
// here at random. When the package has not been built (its program or manifest is missing), the
// registry does not list it and the check fails at its first step, installing it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { index, type Package, pack } from "./routing";

export const HERMES = "pluxy/hermes-setup";

export interface Hermes {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The key and the login canary: neither may show in the app's output. */
  canaries: string[];
  /** The folder to put first on the app's PATH. */
  bin: string;
}

/** Writes bytes to path, making its folder, with mode. */
function put(path: string, bytes: string, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, bytes);
  chmodSync(path, mode);
}

/** A stand-in program: records its arguments (one per line), its folder and its environment in
 * records/<name>.*, then `done`; a terminal then runs what follows its `-e`. */
function standIn(path: string, records: string, name: string, terminal: boolean) {
  const r = (x: string) => `'${join(records, `${name}.${x}`)}'`;
  put(
    path,
    `#!/bin/sh
for a in "$@"; do printf '%s\\n' "$a"; done > ${r("argv")}
pwd -P > ${r("cwd")}
env > ${r("env")}
: > ${r("done")}
${terminal ? '[ "$1" = "-e" ] && shift\nexec "$@"\n' : ""}`,
    0o755,
  );
}

/** The values every apply in the check gives, as the window would send them (with `targets` and
 * `key` added by the check). */
const SETTINGS = { base_url: "http://127.0.0.1:41999", model: "gpt-6.1-sol" };

/** The person's config.yaml: their own endpoint selected with a key variable, an old app's custom
 * provider, other settings and comments. */
const CONFIG = [
  "# The person's Hermes Agent settings.",
  "model:",
  "  provider: ollama-local # their own endpoint",
  "  default: llama3.3",
  "  key_env: OLLAMA_KEY",
  "providers:",
  "  # A local model server.",
  "  ollama-local:",
  "    api: http://127.0.0.1:11434/v1",
  "    key_env: OLLAMA_KEY",
  "    models:",
  "      llama3.3:",
  "        context_length: 131072",
  "custom_providers:",
  "  - name: cpa-gui",
  "    base_url: http://127.0.0.1:8317/v1",
  "    api_mode: chat_completions",
  "terminal:",
  "  backend: local",
  '  cwd: "."',
  "toolsets: [hermes-cli]",
  "_config_version: 53",
  "",
].join("\n");

/** CONFIG after an apply: pluxy's provider and selection, the rest kept. */
const APPLIED = [
  "# The person's Hermes Agent settings.",
  "model:",
  "  provider: custom:pluxy # their own endpoint",
  "  default: gpt-6.1-sol",
  "  api_mode: chat_completions",
  "providers:",
  "  # A local model server.",
  "  ollama-local:",
  "    api: http://127.0.0.1:11434/v1",
  "    key_env: OLLAMA_KEY",
  "    models:",
  "      llama3.3:",
  "        context_length: 131072",
  "  pluxy:",
  "    api: http://127.0.0.1:41999/v1",
  "    api_key: @KEY@",
  "    transport: chat_completions",
  "    default_model: gpt-6.1-sol",
  "custom_providers:",
  "  - name: cpa-gui",
  "    base_url: http://127.0.0.1:8317/v1",
  "    api_mode: chat_completions",
  "terminal:",
  "  backend: local",
  '  cwd: "."',
  "toolsets: [hermes-cli]",
  "_config_version: 53",
  "",
].join("\n");

/** The person's edits after the first apply: the terminal backend changed, and a per-model entry
 * added under pluxy's provider. Clear takes out pluxy's values and keeps these. */
const CLEARED = [
  "# The person's Hermes Agent settings.",
  "model:",
  "  provider: ollama-local # their own endpoint",
  "  default: llama3.3",
  "  key_env: OLLAMA_KEY",
  "providers:",
  "  # A local model server.",
  "  ollama-local:",
  "    api: http://127.0.0.1:11434/v1",
  "    key_env: OLLAMA_KEY",
  "    models:",
  "      llama3.3:",
  "        context_length: 131072",
  "  pluxy:",
  "    models:",
  "      gpt-6.1-sol:",
  "        context_length: 400000",
  "custom_providers:",
  "  - name: cpa-gui",
  "    base_url: http://127.0.0.1:8317/v1",
  "    api_mode: chat_completions",
  "terminal:",
  "  backend: docker",
  '  cwd: "."',
  "toolsets: [hermes-cli]",
  "_config_version: 53",
  "",
].join("\n");

/** An apply over CLEARED: pluxy's values join its model entry, and the person's edits stay. */
const APPLIED_WITH_EDITS = [
  "# The person's Hermes Agent settings.",
  "model:",
  "  provider: custom:pluxy # their own endpoint",
  "  default: gpt-6.1-sol",
  "  api_mode: chat_completions",
  "providers:",
  "  # A local model server.",
  "  ollama-local:",
  "    api: http://127.0.0.1:11434/v1",
  "    key_env: OLLAMA_KEY",
  "    models:",
  "      llama3.3:",
  "        context_length: 131072",
  "  pluxy:",
  "    models:",
  "      gpt-6.1-sol:",
  "        context_length: 400000",
  "    api: http://127.0.0.1:41999/v1",
  "    api_key: @KEY@",
  "    transport: chat_completions",
  "    default_model: gpt-6.1-sol",
  "custom_providers:",
  "  - name: cpa-gui",
  "    base_url: http://127.0.0.1:8317/v1",
  "    api_mode: chat_completions",
  "terminal:",
  "  backend: docker",
  '  cwd: "."',
  "toolsets: [hermes-cli]",
  "_config_version: 53",
  "",
].join("\n");

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const hermes = join(home, ".hermes");
  const config = join(hermes, "config.yaml");
  const env = join(hermes, ".env");
  const auth = join(hermes, "auth.json");
  const dotfiles = join(home, "dotfiles", "hermes", ".hermes");
  const stowFolder = join(home, ".hermes-stow");
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_hermes");
  const rootLink = join(home, ".hermes-root");

  // stow: a pluxy provider with the person's own model entry already there.
  const stowConfig = "providers:\n  pluxy:\n    models:\n      my-model:\n        context_length: 32768\n";
  const stowApplied = [
    "providers:",
    "  pluxy:",
    "    models:",
    "      my-model:",
    "        context_length: 32768",
    "    api: http://127.0.0.1:41999/v1",
    "    api_key: @KEY@",
    "    transport: chat_completions",
    "    default_model: gpt-6.1-sol",
    "model:",
    "  provider: custom:pluxy",
    "  default: gpt-6.1-sol",
    "  api_mode: chat_completions",
    "",
  ].join("\n");
  // Files Hermes Agent would not take.
  const unknownBytes = "providers:\n  - pluxy\n";
  const brokenBytes = "model:\n  default: a\n  default: b\n";
  // The root: created with pluxy's values only.
  const rootBytes = [
    "providers:",
    "  pluxy:",
    "    api: http://127.0.0.1:41999/v1",
    "    api_key: @KEY@",
    "    transport: chat_completions",
    "    default_model: gpt-6.1-sol",
    "model:",
    "  provider: custom:pluxy",
    "  default: gpt-6.1-sol",
    "  api_mode: chat_completions",
    "",
  ].join("\n");

  put(config, CONFIG, 0o600);
  put(env, `OPENROUTER_API_KEY=${login}\n`, 0o600);
  put(auth, `{"token":"${login}"}\n`, 0o600);
  put(join(dotfiles, "config.yaml"), stowConfig, 0o640);
  put(join(dotfiles, "keep.yml"), "keep: true\n");
  mkdirSync(stowFolder, { recursive: true });
  symlinkSync("../dotfiles/hermes/.hermes/config.yaml", join(stowFolder, "config.yaml"));
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(join(home, "unknown", "config.yaml"), unknownBytes);
  put(join(home, "broken", "config.yaml"), brokenBytes);

  return {
    home,
    config,
    env,
    auth,
    settings_input: SETTINGS,
    stow: {
      config: join(stowFolder, "config.yaml"),
      config_text: "../dotfiles/hermes/.hermes/config.yaml",
      target_config: join(dotfiles, "config.yaml"),
      target_folder: dotfiles,
    },
    root: {
      link: rootLink,
      text: chezmoi,
      config: join(chezmoi, "config.yaml"),
    },
    unknown: join(home, "unknown"),
    broken: join(home, "broken"),
    bytes: {
      original: CONFIG,
      env: `OPENROUTER_API_KEY=${login}\n`,
      auth: `{"token":"${login}"}\n`,
      applied: APPLIED,
      cleared: CLEARED,
      applied_with_edits: APPLIED_WITH_EDITS,
      unknown: unknownBytes,
      broken: brokenBytes,
      keep: "keep: true\n",
      stow: stowConfig,
      stow_applied: stowApplied,
      root: rootBytes,
    },
    // The person's edits, each from and to a text that occurs once in the file it edits.
    edits: {
      // The terminal backend, changed by the person after an apply and after a restore.
      backend: ["backend: local", "backend: docker"],
      // The person's per-model entry under pluxy's provider, added after an apply.
      models: [
        "    default_model: gpt-6.1-sol\n",
        "    default_model: gpt-6.1-sol\n    models:\n      gpt-6.1-sol:\n        context_length: 400000\n",
      ],
      // The backup browser's edit, after a Manual Backup.
      backup: ["backend: docker", "backend: pluxy-backend-canary"],
    },
    // The backup browser's step: the diff a restore of it shows (setting names, never a value),
    // and the values no answer may hold.
    backups: {
      diff: [{ path: ["terminal", "backend"], restore: "changes" }],
      values: ["docker", "pluxy-backend-canary"],
    },
  };
}

/** The package whose manifest is at manifest and program at program, to pack, when both exist. */
function packed(manifest: string, program: string): [Package, string] | undefined {
  if (!existsSync(manifest) || !existsSync(program)) return undefined;
  const m = JSON.parse(readFileSync(manifest, "utf8"));
  return [
    {
      id: m.id,
      version: m.version,
      tier: "native",
      app: { ...m.app, programs: { [platform()]: "bin/tool" } },
    },
    program,
  ];
}

/** Builds the fixtures in root and the files in home (the app's HOME): tool and toolManifest
 * (the built pluxy/hermes-setup and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildHermesFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): Hermes {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const versions: Record<string, string> = {};
  const curated: [Package, Record<string, unknown>][] = [];
  for (const p of [packed(toolManifest, tool), packed(executionManifest, execution)]) {
    if (!p) continue;
    const file = pack(root, p[0], p[1]);
    const f: Fixture = { file, id: p[0].id, version: p[0].version, tier: "native" };
    curated.push([p[0], artifact(root, file, f, publicKey, privateKey as KeyObject).entry]);
    versions[p[0].id] = p[0].version;
  }
  const curatedFile = join(root, "curated.json");
  writeFileSync(curatedFile, JSON.stringify(index(curated)));

  const records = join(root, "records");
  const bin = join(root, "bin");
  const work = join(root, "work");
  mkdirSync(records);
  mkdirSync(work);
  standIn(join(bin, "x-terminal-emulator"), records, "terminal", true);
  standIn(join(bin, "hermes"), records, "hermes", false);
  standIn(join(root, "installed", "hermes"), records, "installed", false);

  const key = `pluxy-hermes-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-hermes-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: HERMES,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      hermes: join(bin, "hermes"),
      installed: join(root, "installed", "hermes"),
      local_bin: join(home, ".local", "bin", "hermes"),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
