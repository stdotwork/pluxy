// Fixtures for the native test's check of the Claude Code setup and launcher
// (src-tauri/src/claude_code_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/claude-code (next/app-tools/claude-code, which run.ts builds) and pluxy/execution (the
//   real execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Claude Code's user settings `~/.claude/settings.json` as a person keeps
//   it: its `$schema`, permissions, an `env` with their own API key and timeout, their model and a
//   setting this check knows nothing of; beside it the login files `~/.claude/.credentials.json`
//   and `~/.claude.json`, holding a login canary, which no step may touch;
// - a stow layout: `~/.config/claude-stow/settings.json` is a relative link to
//   `~/dotfiles/claude/.claude/settings.json` (mode 0640), beside `keep.json`;
// - a chezmoi-style root: `~/.claude-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_claude`, which has no settings.json yet;
// - for the backup browser's step, the person's edits after a Manual Backup and the diff a restore
//   of it shows, by hand;
// - settings Claude Code would not take as its own: `~/unknown/settings.json` names another
//   schema, `~/broken/settings.json` is not strict JSON (it has a comment);
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `claude`, shell scripts that record their arguments, folder and environment in
//   records/; override/claude is another copy, for the executable override.
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

export const CLAUDE_CODE = "pluxy/claude-code";

export interface ClaudeCode {
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
const SETTINGS = {
  base_url: "http://127.0.0.1:41999",
  opus: "claude-opus-5-5",
  opus_1m: false,
  sonnet: "gpt-6.1-sol",
  sonnet_1m: true,
  haiku: "claude-haiku-5-5",
  haiku_1m: false,
  fable: "",
  fable_1m: false,
  startup_model: "opus",
  subagent_model: "haiku",
  max_context_tokens: 1000000,
  auto_compact_pct: 80,
  disable_auto_compact: false,
};

/** The `env` entries an apply of SETTINGS adds, in the order the package writes them, indented
 * for an `env` object at the top level. Fable is empty, so it follows Sonnet. */
const MANAGED_ENV = [
  '"ANTHROPIC_BASE_URL": "http://127.0.0.1:41999"',
  '"ANTHROPIC_AUTH_TOKEN": "@KEY@"',
  '"ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5-5"',
  '"ANTHROPIC_DEFAULT_SONNET_MODEL": "gpt-6.1-sol[1m]"',
  '"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-5-5"',
  '"ANTHROPIC_DEFAULT_FABLE_MODEL": "gpt-6.1-sol[1m]"',
  '"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "Opus · claude-opus-5-5"',
  '"ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION": "Auto-compact window: 1M"',
  '"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "Sonnet · gpt-6.1-sol[1m]"',
  '"ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION": "1M enabled · Auto-compact window: 1M"',
  '"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "Haiku · claude-haiku-5-5"',
  '"ANTHROPIC_DEFAULT_HAIKU_MODEL_DESCRIPTION": "Auto-compact window: 1M"',
  '"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "Fable · gpt-6.1-sol[1m]"',
  '"ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION": "1M enabled · Auto-compact window: 1M"',
  '"ANTHROPIC_CUSTOM_MODEL_OPTION": "gpt-6.1-sol[1m]"',
  '"ANTHROPIC_CUSTOM_MODEL_OPTION_NAME": "gpt-6.1-sol[1m]"',
  '"ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION": "1M enabled · Auto-compact window: 1M"',
  '"CLAUDE_CODE_SUBAGENT_MODEL": "haiku"',
  '"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"',
  '"CLAUDE_CODE_AUTO_MODE_SERVER": "0"',
  '"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1000000"',
  '"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "1000000"',
  '"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "80"',
  '"DISABLE_AUTO_COMPACT": "0"',
];

/** An `env` object at the top level with these entries first, then the managed ones. */
function env(first: string[]): string {
  return `  "env": {\n${[...first, ...MANAGED_ENV].map((l) => `    ${l}`).join(",\n")}\n  }`;
}

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const claude = join(home, ".claude");
  const settings = join(claude, "settings.json");
  const credentials = join(claude, ".credentials.json");
  const claudeJson = join(home, ".claude.json");
  const dotfiles = join(home, "dotfiles", "claude", ".claude");
  const stowTarget = join(dotfiles, "settings.json");
  const stowLink = join(home, ".config", "claude-stow", "settings.json");
  const stowText = "../../dotfiles/claude/.claude/settings.json";
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_claude");
  const rootLink = join(home, ".claude-root");
  const unknown = join(home, "unknown", "settings.json");
  const broken = join(home, "broken", "settings.json");

  const original =
    '{\n  "$schema": "https://json.schemastore.org/claude-code-settings.json",\n' +
    '  "permissions": {\n    "allow": [\n      "Bash(npm run lint)"\n    ]\n  },\n' +
    '  "env": {\n    "API_TIMEOUT_MS": "1200000",\n    "ANTHROPIC_API_KEY": "fixture-own-api-key"\n  },\n' +
    '  "model": "sonnet",\n' +
    '  "futureSetting": {\n    "nested": [\n      1,\n      2.50,\n      12345678901234567890\n    ]\n  }\n}\n';
  const credentialsBytes = `{"claudeAiOauth":{"accessToken":"${login}"}}\n`;
  const claudeJsonBytes = `{"oauthAccount":{"accountUuid":"${login}"},"projects":{}}\n`;
  const stowOriginal = '{\n  "env": {\n    "API_TIMEOUT_MS": "1"\n  }\n}\n';
  const keep = '{"keep": true}\n';
  const unknownBytes = '{\n  "$schema": "https://example.invalid/agent-settings.json",\n  "env": {}\n}\n';
  const brokenBytes = '{\n  // a comment Claude Code would refuse\n  "env": {}\n}\n';

  put(settings, original);
  put(credentials, credentialsBytes, 0o600);
  put(claudeJson, claudeJsonBytes, 0o600);
  put(stowTarget, stowOriginal, 0o640);
  put(join(dotfiles, "keep.json"), keep);
  mkdirSync(dirname(stowLink), { recursive: true });
  symlinkSync(stowText, stowLink);
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(unknown, unknownBytes);
  put(broken, brokenBytes);

  const tail = '  "autoCompactWindow": 1000000,\n  "autoCompactEnabled": true\n}\n';
  return {
    home,
    settings,
    credentials,
    claude_json: claudeJson,
    stow: { link: stowLink, text: stowText, target: stowTarget, folder: dotfiles },
    root: { link: rootLink, text: chezmoi, file: join(chezmoi, "settings.json") },
    unknown,
    broken,
    settings_input: SETTINGS,
    bytes: {
      original,
      credentials: credentialsBytes,
      claude_json: claudeJsonBytes,
      keep,
      unknown: unknownBytes,
      broken: brokenBytes,
      stow: stowOriginal,
      // The person's API key goes (it would win over the bearer token), their timeout stays, the
      // startup model replaces theirs, the setting the check does not know keeps its digits.
      applied:
        '{\n  "$schema": "https://json.schemastore.org/claude-code-settings.json",\n' +
        '  "permissions": {\n    "allow": [\n      "Bash(npm run lint)"\n    ]\n  },\n' +
        `${env(['"API_TIMEOUT_MS": "1200000"'])},\n` +
        '  "model": "opus",\n' +
        '  "futureSetting": {\n    "nested": [\n      1,\n      2.50,\n      12345678901234567890\n    ]\n  },\n' +
        tail,
      // The person's edit of the applied file: their timeout.
      edited_timeout: ['"API_TIMEOUT_MS": "1200000"', '"API_TIMEOUT_MS": "600000"'],
      // Their own model, too.
      edited_model: ['"model": "opus"', '"model": "fable"'],
      // Clear after both edits: the managed values go, their API key comes back (last, after the
      // keys the file has now), their timeout and model stay.
      cleared:
        '{\n  "$schema": "https://json.schemastore.org/claude-code-settings.json",\n' +
        '  "permissions": {\n    "allow": [\n      "Bash(npm run lint)"\n    ]\n  },\n' +
        '  "env": {\n    "API_TIMEOUT_MS": "600000",\n    "ANTHROPIC_API_KEY": "fixture-own-api-key"\n  },\n' +
        '  "model": "fable",\n' +
        '  "futureSetting": {\n    "nested": [\n      1,\n      2.50,\n      12345678901234567890\n    ]\n  }\n}\n',
      stow_applied: `{\n${env(['"API_TIMEOUT_MS": "1"'])},\n  "model": "opus",\n${tail}`,
      // A file the root did not have, created.
      root_applied: `{\n${env([])},\n  "model": "opus",\n${tail}`,
    },
    // The backup browser's step (backups_native.rs), from the cleared settings: the person's
    // edits after a Manual Backup, the diff a restore of it shows (setting names in the backup's
    // order, then those only the file has now; never a value), and the values of the files no
    // answer and no file in the tool's own data may hold.
    backups: {
      edits: [
        ['"API_TIMEOUT_MS": "600000"', '"API_TIMEOUT_MS": "900000"'],
        ['"model": "fable"', '"model": "pluxy-model-canary",\n  "outputStyle": "pluxy-style-canary"'],
      ],
      diff: [
        { path: ["env", "API_TIMEOUT_MS"], restore: "changes" },
        { path: ["model"], restore: "changes" },
        { path: ["outputStyle"], restore: "removes" },
      ],
      values: ["fixture-own-api-key", "pluxy-model-canary", "pluxy-style-canary"],
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
 * (the built pluxy/claude-code and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildClaudeCodeFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): ClaudeCode {
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
  standIn(join(bin, "claude"), records, "claude", false);
  standIn(join(root, "override", "claude"), records, "override", false);

  const key = `pluxy-claude-code-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-claude-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: CLAUDE_CODE,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      claude: join(bin, "claude"),
      override: join(root, "override", "claude"),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
