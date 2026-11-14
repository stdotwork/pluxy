// Fixtures for the native test's check of the Grok Build setup and launcher
// (src-tauri/src/grok_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/grok-setup (next/app-tools/grok-setup, which run.ts builds) and pluxy/execution (the real
//   execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Grok Build's user config `~/.grok/config.toml` as a person keeps it: a
//   comment, their default model, a model entry of their own and a table this check knows nothing
//   of; beside it `~/.grok/auth.json`, holding a login canary, which no step may touch;
// - a stow layout: `~/.config/grok-stow/config.toml` is a relative link to
//   `~/dotfiles/grok/.grok/config.toml` (mode 0640), beside `keep.toml`;
// - a chezmoi-style root: `~/.grok-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_grok`, which has no config.toml yet;
// - for the backup browser's step, the person's edits after a Manual Backup and the diff a restore
//   of it shows, by hand;
// - files Grok Build would not take: `~/broken/config.toml` is not TOML, `~/unknown/config.toml`
//   holds `models` as a string rather than a table;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `grok`, shell scripts that record their arguments, folder and environment in records/;
//   override/grok is another copy, for the executable override, and installed/grok one more, which
//   the check copies to `~/.grok/bin/grok` (where Grok Build's install script puts it) for the
//   recipe to prefer.
//
// expect.json holds what the check compares with, written out by hand here, never worked out by
// the package's code: the paths, the link texts, every file's bytes after each step, with `@KEY@`
// where the key goes, and what the preview reads back. keys.json holds the key the check types
// into native key entry, made here at random. When the package has not been built (its program or
// manifest is missing), the registry does not list it and the check fails at its first step,
// installing it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { index, type Package, pack } from "./routing";

export const GROK = "pluxy/grok-setup";

export interface Grok {
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
 * `key` added by the check): two of the core's models, the first the default. */
const SETTINGS = {
  base_url: "http://127.0.0.1:41999",
  model: "gpt-6.1-sol",
  models: [
    { model: "gpt-6.1-sol", name: "GPT-6.1 Sol", context_window: 400000 },
    { model: "claude-opus-5-5", name: "", context_window: 200000 },
  ],
};

/** The model entries an apply of SETTINGS writes, at the end of the file. */
const ENTRIES =
  '[model."pluxy/gpt-6.1-sol"]\nmodel = "gpt-6.1-sol"\nbase_url = "http://127.0.0.1:41999/v1"\n' +
  'name = "GPT-6.1 Sol"\napi_key = "@KEY@"\napi_backend = "chat_completions"\ncontext_window = 400000\n\n' +
  '[model."pluxy/claude-opus-5-5"]\nmodel = "claude-opus-5-5"\nbase_url = "http://127.0.0.1:41999/v1"\n' +
  'name = "claude-opus-5-5"\napi_key = "@KEY@"\napi_backend = "chat_completions"\ncontext_window = 200000\n';

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const grok = join(home, ".grok");
  const config = join(grok, "config.toml");
  const auth = join(grok, "auth.json");
  const dotfiles = join(home, "dotfiles", "grok", ".grok");
  const stowTarget = join(dotfiles, "config.toml");
  const stowLink = join(home, ".config", "grok-stow", "config.toml");
  const stowText = "../../dotfiles/grok/.grok/config.toml";
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_grok");
  const rootLink = join(home, ".grok-root");
  const broken = join(home, "broken", "config.toml");
  const unknown = join(home, "unknown", "config.toml");

  const original =
    "# Grok Build config, kept by hand\n" +
    '[models]\ndefault = "grok-4.6" # my default\n\n' +
    '[model.my-local]\nmodel = "qwen-coder"\nbase_url = "http://127.0.0.1:8080/v1"\napi_backend = "responses"\n\n' +
    "[ui]\nfuture_flag = true\n";
  const authBytes = `{"access_token":"${login}"}\n`;
  const stowOriginal = '[ui]\ntheme = "dark"\n';
  const keep = "keep = true\n";
  const brokenBytes = '[models\ndefault = "grok-4.6"\n';
  const unknownBytes = 'models = "grok-4.6"\n';

  put(config, original, 0o600);
  put(auth, authBytes, 0o600);
  put(stowTarget, stowOriginal, 0o640);
  put(join(dotfiles, "keep.toml"), keep);
  mkdirSync(dirname(stowLink), { recursive: true });
  symlinkSync(stowText, stowLink);
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(broken, brokenBytes);
  put(unknown, unknownBytes);

  const models = [
    { model: "gpt-6.1-sol", name: "GPT-6.1 Sol", context_window: 400000 },
    { model: "claude-opus-5-5", name: "claude-opus-5-5", context_window: 200000 },
  ];
  return {
    home,
    config,
    auth,
    stow: { link: stowLink, text: stowText, target: stowTarget, folder: dotfiles },
    root: { link: rootLink, text: chezmoi, file: join(chezmoi, "config.toml") },
    refused: [
      { file: broken, bytes: "broken", says: "not TOML" },
      { file: unknown, bytes: "unknown", says: "models is not a table" },
    ],
    settings_input: SETTINGS,
    // What a preview reads back: of the person's file, and once applied (never the key).
    read_original: {
      state: "not-configured",
      base_url: null,
      key_set: false,
      default: "grok-4.6",
      model: null,
      models: [],
      legacy: 0,
    },
    read_applied: {
      state: "configured",
      base_url: "http://127.0.0.1:41999/v1",
      key_set: true,
      default: "pluxy/gpt-6.1-sol",
      model: "gpt-6.1-sol",
      models,
      legacy: 0,
    },
    bytes: {
      original,
      auth: authBytes,
      keep,
      broken: brokenBytes,
      unknown: unknownBytes,
      stow: stowOriginal,
      // The default model becomes pluxy's (its comment kept); pluxy's entries come after the
      // person's own; the table the check does not know stays where it was.
      applied:
        "# Grok Build config, kept by hand\n" +
        '[models]\ndefault = "pluxy/gpt-6.1-sol" # my default\n\n' +
        '[model.my-local]\nmodel = "qwen-coder"\nbase_url = "http://127.0.0.1:8080/v1"\napi_backend = "responses"\n\n' +
        `${ENTRIES}\n` +
        "[ui]\nfuture_flag = true\n",
      // The person's edit of their own model entry.
      edited_own: ['model = "qwen-coder"', 'model = "qwen-coder-2"'],
      // Their edit of a model entry Apply wrote, too.
      edited_written: ["context_window = 400000", "context_window = 300000"],
      // Clear after both edits: their default model comes back, their own entry keeps its edit,
      // the written entry keeps only the value they changed, the other entry goes.
      cleared:
        "# Grok Build config, kept by hand\n" +
        '[models]\ndefault = "grok-4.6" # my default\n\n' +
        '[model.my-local]\nmodel = "qwen-coder-2"\nbase_url = "http://127.0.0.1:8080/v1"\napi_backend = "responses"\n\n' +
        '[model."pluxy/gpt-6.1-sol"]\ncontext_window = 300000\n\n' +
        "[ui]\nfuture_flag = true\n",
      stow_applied: `[ui]\ntheme = "dark"\n\n[models]\ndefault = "pluxy/gpt-6.1-sol"\n\n${ENTRIES}`,
      // A file the root did not have, created.
      root_applied: `[models]\ndefault = "pluxy/gpt-6.1-sol"\n\n${ENTRIES}`,
    },
    // The backup browser's step, from the cleared config: the person's edits after a Manual
    // Backup, the diff a restore of it shows (setting names in the backup's order, then those only
    // the file has now; never a value), and the values no answer may hold.
    backups: {
      edits: [
        ['default = "grok-4.6"', 'default = "pluxy-model-canary"'],
        ["future_flag = true", 'future_flag = true\nstyle = "pluxy-style-canary"'],
      ],
      diff: [
        { path: ["models", "default"], restore: "changes" },
        { path: ["ui", "style"], restore: "removes" },
      ],
      values: ["grok-4.6", "pluxy-model-canary", "pluxy-style-canary", "qwen-coder-2"],
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
 * (the built pluxy/grok-setup and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildGrokFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): Grok {
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
  standIn(join(bin, "grok"), records, "grok", false);
  standIn(join(root, "override", "grok"), records, "override", false);
  standIn(join(root, "installed", "grok"), records, "installed", false);

  const key = `pluxy-grok-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-grok-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: GROK,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      grok: join(bin, "grok"),
      override: join(root, "override", "grok"),
      installed: { from: join(root, "installed", "grok"), to: join(home, ".grok", "bin", "grok") },
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
