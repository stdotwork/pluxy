// Fixtures for the native test's check of the OpenCode setup and launcher
// (src-tauri/src/opencode_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/opencode (next/app-tools/opencode, which run.ts builds) and pluxy/execution (the real
//   execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, OpenCode's global config folder `~/.config/opencode` as a person keeps it:
//   `opencode.jsonc` with comments, trailing commas, their own provider and model and settings this
//   check knows nothing of, and beside it an older `opencode.json`, which OpenCode reads first and
//   so loses to the jsonc file: the default preview must pick `opencode.jsonc` and no step may
//   touch `opencode.json`; the login file `~/.local/share/opencode/auth.json` holds a login canary;
// - a stow layout: `~/.config/opencode-stow/opencode.json` is a relative link to
//   `~/dotfiles/opencode/.config/opencode/opencode.json` (strict JSON, mode 0640), beside
//   `keep.json`;
// - a chezmoi-style root: `~/.opencode-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_config/opencode`, which has no config yet;
// - configs OpenCode would not take: `~/unknown/opencode.json` names another schema,
//   `~/broken/opencode.json` uses single quotes;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `opencode`, shell scripts that record their arguments, folder and environment in records/;
//   installed/opencode is the copy the check later puts at `~/.opencode/bin/opencode` (where
//   OpenCode's install script puts it) and override/ai.opencode stands in for the desktop app.
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

export const OPENCODE = "pluxy/opencode";

export interface OpenCode {
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
 * `key` added by the check). The second model has no name: it is shown by its ID. */
const SETTINGS = {
  base_url: "http://127.0.0.1:41999",
  model: "gpt-6.1-sol",
  models: [
    { id: "gpt-6.1-sol", name: "GPT-6.1 Sol", attachment: true },
    { id: "deepseek-v4", name: "", attachment: false },
  ],
};

/** The provider an apply of SETTINGS writes, as lines at the indentation of a member of
 * `provider` at the top level: with trailing commas (in a file that uses them), or without (a
 * comma right before a closing brace or bracket dropped). */
function pluxy(trailing: boolean): string {
  const lines = [
    '    "pluxy": {',
    '      "npm": "@ai-sdk/openai-compatible",',
    '      "name": "pluxy",',
    '      "options": {',
    '        "baseURL": "http://127.0.0.1:41999/v1",',
    '        "apiKey": "@KEY@",',
    "      },",
    '      "models": {',
    '        "gpt-6.1-sol": {',
    '          "name": "GPT-6.1 Sol",',
    '          "attachment": true,',
    '          "modalities": {',
    '            "input": ["text", "image", "pdf"],',
    '            "output": ["text"],',
    "          },",
    "        },",
    '        "deepseek-v4": {',
    '          "name": "deepseek-v4",',
    '          "modalities": {',
    '            "input": ["text"],',
    '            "output": ["text"],',
    "          },",
    "        },",
    "      },",
    "    },",
  ].join("\n");
  return trailing ? lines : `${lines.replace(/,(\n\s*[}\]])/g, "$1").replace(/,$/, "")}`;
}

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const folder = join(home, ".config", "opencode");
  const jsonc = join(folder, "opencode.jsonc");
  const json = join(folder, "opencode.json");
  const auth = join(home, ".local", "share", "opencode", "auth.json");
  const dotfiles = join(home, "dotfiles", "opencode", ".config", "opencode");
  const stowTarget = join(dotfiles, "opencode.json");
  const stowLink = join(home, ".config", "opencode-stow", "opencode.json");
  const stowText = "../../dotfiles/opencode/.config/opencode/opencode.json";
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_config", "opencode");
  const rootLink = join(home, ".opencode-root");
  const unknown = join(home, "unknown", "opencode.json");
  const broken = join(home, "broken", "opencode.json");

  const head =
    '{\n  // The person\'s own OpenCode config.\n  "$schema": "https://opencode.ai/config.json",\n';
  const anthropic = '  "provider": {\n    "anthropic": {\n      "options": { "timeout": 600000 },\n    },\n';
  const tail = '  "futureSetting": [1, 2.50, 12345678901234567890],\n}\n';
  const original = `${head}  "theme": "tokyonight", /* their theme */\n${anthropic}  },\n  "model": "anthropic/claude-sonnet-5-5",\n${tail}`;
  const olderJson = '{\n  "theme": "from-the-json-file"\n}\n';
  const authBytes = `{"anthropic":{"type":"oauth","access":"${login}"}}\n`;
  const stowOriginal = '{\n  "theme": "stow"\n}\n';
  const keep = '{"keep": true}\n';
  const unknownBytes = '{\n  "$schema": "https://example.invalid/agent-config.json",\n  "theme": "x"\n}\n';
  const brokenBytes = "{\n  'theme': 'single quotes OpenCode refuses'\n}\n";

  put(jsonc, original);
  put(json, olderJson);
  put(auth, authBytes, 0o600);
  put(stowTarget, stowOriginal, 0o640);
  put(join(dotfiles, "keep.json"), keep);
  mkdirSync(dirname(stowLink), { recursive: true });
  symlinkSync(stowText, stowLink);
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(unknown, unknownBytes);
  put(broken, brokenBytes);

  const applied = (theme: string, model: string) =>
    `${head}  "theme": "${theme}", /* their theme */\n${anthropic}${pluxy(true)}\n  },\n  "model": "${model}",\n${tail}`;
  return {
    home,
    jsonc,
    json,
    auth,
    stow: { link: stowLink, text: stowText, target: stowTarget, folder: dotfiles },
    root: { link: rootLink, text: chezmoi, file: join(chezmoi, "opencode.jsonc") },
    unknown,
    broken,
    settings_input: SETTINGS,
    bytes: {
      original,
      json: olderJson,
      auth: authBytes,
      keep,
      unknown: unknownBytes,
      broken: brokenBytes,
      stow: stowOriginal,
      // Comments, trailing commas, the person's provider, the setting the check does not know and
      // its digits stay where they are; pluxy's provider follows theirs and the default model is
      // pluxy's.
      applied: applied("tokyonight", "pluxy/gpt-6.1-sol"),
      // The person's edits of the applied file: their theme, then their own default model.
      edited_theme: ['"theme": "tokyonight"', '"theme": "nord"'],
      edited_model: ['"model": "pluxy/gpt-6.1-sol"', '"model": "anthropic/claude-opus-5-5"'],
      // Clear after both edits: pluxy's provider goes, their theme and model stay.
      cleared: `${head}  "theme": "nord", /* their theme */\n${anthropic}  },\n  "model": "anthropic/claude-opus-5-5",\n${tail}`,
      // A strict JSON file gets `$schema` first and no trailing commas.
      stow_applied: `{\n  "$schema": "https://opencode.ai/config.json",\n  "theme": "stow",\n  "provider": {\n${pluxy(false)}\n  },\n  "model": "pluxy/gpt-6.1-sol"\n}\n`,
      // A file the root did not have, created.
      root_applied: `{\n  "$schema": "https://opencode.ai/config.json",\n  "provider": {\n${pluxy(false)}\n  },\n  "model": "pluxy/gpt-6.1-sol"\n}\n`,
    },
    // The backup browser's step, from the cleared config: the person's edit after a Manual
    // Backup, the diff a restore of it shows (setting names, never a value), and the values no
    // answer may hold.
    backups: {
      edit: ['"theme": "nord"', '"theme": "pluxy-theme-canary"'],
      diff: [{ path: ["theme"], restore: "changes" }],
      values: ["nord", "pluxy-theme-canary", "anthropic/claude-opus-5-5"],
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
 * (the built pluxy/opencode and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildOpenCodeFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): OpenCode {
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
  standIn(join(bin, "opencode"), records, "opencode", false);
  standIn(join(root, "installed", "opencode"), records, "installed", false);
  standIn(join(root, "override", "ai.opencode"), records, "desktop", false);

  const key = `pluxy-opencode-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-opencode-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: OPENCODE,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      opencode: join(bin, "opencode"),
      installed: join(root, "installed", "opencode"),
      installed_at: join(home, ".opencode", "bin", "opencode"),
      desktop: "/opt/OpenCode/ai.opencode",
      override: join(root, "override", "ai.opencode"),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
