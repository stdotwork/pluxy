// Fixtures for the native test's check of the Pi setup and launcher (src-tauri/src/pi_native.rs),
// the shared acceptance flow of an integration:
//
// - pluxy/pi-setup (next/app-tools/pi-setup, which run.ts builds) and pluxy/execution (the real
//   execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Pi's agent folder `~/.pi/agent` as a person keeps it: `settings.json` with
//   their theme, their default provider and model, their packages (the legacy npm entry of the old
//   adapter among them) and a setting this check knows nothing of; `cliproxyapi.json` and
//   `cliproxyapi-models.json`, the adapter's files; and `auth.json`, holding a login canary;
// - a stow layout: `~/.pi-stow/settings.json` and `~/.pi-stow/cliproxyapi.json` are relative links
//   to `~/dotfiles/pi/.pi/agent/`, beside `keep.json`;
// - a chezmoi-style root: `~/.pi-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_pi/agent`, which has no files yet;
// - folders Pi would not take: `~/unknown/settings.json` whose packages is an object,
//   `~/broken/settings.json` with a trailing comma, and `~/foreign/`, whose adapter folder is not
//   pluxy's;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `pi`, shell scripts that record their arguments, folder and environment in records/;
//   installed/pi is the copy the check later puts at `~/.local/bin/pi` and then at
//   `~/.pi/agent/bin/pi` (which comes first).
//
// expect.json holds what the check compares with, written out by hand here, never worked out by
// the package's code: the paths, the link texts and every file's bytes after each step, with
// `@KEY@` where the key goes. The adapter's own files are read from next/app-tools/pi-setup/adapter,
// the bundle the package installs. keys.json holds the key the check types into native key entry,
// made here at random. When the package has not been built (its program or manifest is missing),
// the registry does not list it and the check fails at its first step, installing it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { index, type Package, pack } from "./routing";

export const PI = "pluxy/pi-setup";
/** The adapter's folder in Pi's agent folder, which settings.json lists as ./<folder>. */
const ADAPTER = "pluxy-cliproxyapi-provider";

export interface Pi {
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

/** The files under dir, by their path relative to it, as text (none if dir does not exist). */
function files(dir: string, base = dir): Record<string, string> {
  if (!existsSync(dir)) return {};
  const out: Record<string, string> = {};
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, e.name);
    if (e.isDirectory()) Object.assign(out, files(path, base));
    else out[relative(base, path)] = readFileSync(path, "utf8");
  }
  return out;
}

/** A stand-in program: records its arguments (one per line), its folder and its environment in
 * records/<name>.*, then `done`; a terminal then runs what follows its `-e`.
 * `--version` only prints a version and records nothing: core's setup scan asks it. */
function standIn(path: string, records: string, name: string, terminal: boolean) {
  const r = (x: string) => `'${join(records, `${name}.${x}`)}'`;
  put(
    path,
    `#!/bin/sh
[ "$1" = "--version" ] && { echo "0.0.0 (stand-in)"; exit 0; }
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

/** The adapter's marker file, written by pluxy at the given version. */
const marker = (version: string) =>
  `{
  "package": "pluxy/pi-setup",
  "adapter": "@router-for-me/pi-cliproxyapi-provider",
  "version": "${version}"
}
`;

/** The person's settings before pluxy: their packages include the old adapter's npm entry. */
const SETTINGS_ORIGINAL = `{
  "theme": "dark",
  "defaultProvider": "anthropic",
  "defaultModel": "claude-sonnet-5-5",
  "packages": [
    "npm:pi-subagents",
    "npm:@router-for-me/pi-cliproxyapi-provider@1.4.20",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    }
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
`;

/** Applied: the defaults are pluxy's, the legacy npm entry is gone, pluxy's entry is last. */
const SETTINGS_APPLIED = `{
  "theme": "dark",
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    },
    "./pluxy-cliproxyapi-provider"
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
`;

/** Uninstalled: applied, without pluxy's entry. */
const SETTINGS_UNINSTALLED = `{
  "theme": "dark",
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    }
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
`;

/** Cleared, after the person's edits: pluxy's values go, their own entries stay. */
const SETTINGS_CLEARED = `{
  "theme": "dark",
  "quietStartup": true,
  "defaultProvider": "anthropic",
  "defaultModel": "claude-sonnet-5-5",
  "packages": [
    "npm:pi-subagents",
    {
      "source": "git:github.com/example/pi-tools@v1",
      "skills": []
    },
    "npm:pi-mcp",
    "npm:@router-for-me/pi-cliproxyapi-provider@1.4.20"
  ],
  "futureSetting": [
    1,
    2.50,
    12345678901234567890
  ]
}
`;

const CLIPROXY_ORIGINAL = `{
  "baseUrl": "http://127.0.0.1:8317",
  "fast": true,
  "pause": false
}
`;

const CLIPROXY_APPLIED = `{
  "baseUrl": "http://127.0.0.1:41999",
  "fast": true,
  "pause": false,
  "apiKey": "@KEY@"
}
`;

const CACHE_ORIGINAL = `{"baseUrl":"http://127.0.0.1:8317","models":[],"fetchedAt":1}\n`;

const STOW_SETTINGS = `{
  "theme": "dark"
}
`;

const STOW_SETTINGS_APPLIED = `{
  "theme": "dark",
  "packages": [
    "./pluxy-cliproxyapi-provider"
  ],
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol"
}
`;

const STOW_CLIPROXY = `{
  "fast": false
}
`;

const STOW_CLIPROXY_APPLIED = `{
  "fast": false,
  "baseUrl": "http://127.0.0.1:41999",
  "apiKey": "@KEY@"
}
`;

const ROOT_SETTINGS = `{
  "packages": [
    "./pluxy-cliproxyapi-provider"
  ],
  "defaultProvider": "cliproxyapi",
  "defaultModel": "gpt-6.1-sol"
}
`;

const ROOT_CLIPROXY = `{
  "baseUrl": "http://127.0.0.1:41999",
  "apiKey": "@KEY@"
}
`;

const UNKNOWN = `{
  "packages": {
    "npm:x": true
  }
}
`;

const BROKEN = `{
  "theme": "dark",
}
`;

const KEEP = `{"keep": true}\n`;

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const agent = join(home, ".pi", "agent");
  const settings = join(agent, "settings.json");
  const cliproxy = join(agent, "cliproxyapi.json");
  const cache = join(agent, "cliproxyapi-models.json");
  const auth = join(agent, "auth.json");
  const dotfiles = join(home, "dotfiles", "pi", ".pi", "agent");
  const stow = join(home, ".pi-stow");
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_pi", "agent");
  const rootLink = join(home, ".pi-root");
  const foreign = join(home, "foreign");
  const authText = `{"credential": "${login}"}\n`;

  // The person's files, before pluxy.
  put(settings, SETTINGS_ORIGINAL);
  put(cliproxy, CLIPROXY_ORIGINAL, 0o600);
  put(cache, CACHE_ORIGINAL);
  put(auth, authText, 0o600);
  // Stow: the targets in the dotfiles, and relative links to them from the stow folder.
  put(join(dotfiles, "settings.json"), STOW_SETTINGS, 0o640);
  put(join(dotfiles, "cliproxyapi.json"), STOW_CLIPROXY);
  put(join(dotfiles, "keep.json"), KEEP);
  mkdirSync(stow, { recursive: true });
  symlinkSync("../dotfiles/pi/.pi/agent/settings.json", join(stow, "settings.json"));
  symlinkSync("../dotfiles/pi/.pi/agent/cliproxyapi.json", join(stow, "cliproxyapi.json"));
  // Root: an absolute link to an empty folder.
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  // Folders Pi would not take, and one whose adapter folder is not pluxy's.
  put(join(home, "unknown", "settings.json"), UNKNOWN);
  put(join(home, "broken", "settings.json"), BROKEN);
  put(join(foreign, "settings.json"), "{}\n");
  put(join(foreign, ADAPTER, "package.json"), '{"name":"someone-else"}\n');

  return {
    home,
    agent,
    settings,
    cliproxy,
    cache,
    auth,
    adapter: join(agent, ADAPTER),
    stow: {
      folder: stow,
      settings: join(stow, "settings.json"),
      cliproxy: join(stow, "cliproxyapi.json"),
      settings_text: "../dotfiles/pi/.pi/agent/settings.json",
      cliproxy_text: "../dotfiles/pi/.pi/agent/cliproxyapi.json",
      target_settings: join(dotfiles, "settings.json"),
      target_cliproxy: join(dotfiles, "cliproxyapi.json"),
      target_folder: dotfiles,
    },
    root: { link: rootLink, folder: chezmoi },
    unknown: join(home, "unknown"),
    broken: join(home, "broken"),
    foreign: {
      folder: foreign,
      settings: join(foreign, "settings.json"),
      cliproxy: join(foreign, "cliproxyapi.json"),
      package: join(foreign, ADAPTER, "package.json"),
    },
    settings_input: SETTINGS,
    // The person's edits: [what to find, what to put in its place], in the file as it now stands.
    edits: {
      theme: ['"theme": "dark"', '"theme": "light"'],
      quiet: ['"theme": "dark",', '"theme": "dark",\n  "quietStartup": true,'],
      mcp: ['"./pluxy-cliproxyapi-provider"\n', '"./pluxy-cliproxyapi-provider",\n    "npm:pi-mcp"\n'],
      backup: ['"theme": "dark"', '"theme": "pi-theme-canary"'],
      corrupt: ['"theme": "dark",', '"theme": "dark",,'],
      model_shape: ['"defaultModel": "gpt-6.1-sol",', '"defaultModel": 5,'],
      provider_shape: ['"fast": true,', '"fast": true,\n  "providerId": 7,'],
    },
    // The backup browser's step, from the cleared files: the diff a restore of a backup taken
    // before the backup edit shows (setting names, never a value), and the values no answer may hold.
    backups: {
      diff: [{ path: ["theme"], restore: "changes" }],
      values: ["pi-theme-canary"],
    },
    bytes: {
      settings_original: SETTINGS_ORIGINAL,
      settings_applied: SETTINGS_APPLIED,
      settings_uninstalled: SETTINGS_UNINSTALLED,
      settings_cleared: SETTINGS_CLEARED,
      cliproxy_original: CLIPROXY_ORIGINAL,
      cliproxy_applied: CLIPROXY_APPLIED,
      cache_original: CACHE_ORIGINAL,
      auth: authText,
      keep: KEEP,
      unknown: UNKNOWN,
      broken: BROKEN,
      foreign_settings: "{}\n",
      foreign_package: '{"name":"someone-else"}\n',
      stow_settings: STOW_SETTINGS,
      stow_cliproxy: STOW_CLIPROXY,
      stow_settings_applied: STOW_SETTINGS_APPLIED,
      stow_cliproxy_applied: STOW_CLIPROXY_APPLIED,
      root_settings: ROOT_SETTINGS,
      root_cliproxy: ROOT_CLIPROXY,
      marker: marker("1.4.25-pluxy.1"),
      marker_stale: marker("1.4.24-pluxy.1"),
      stale: "// stale\n",
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
 * (the built pluxy/pi-setup and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildPiFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): Pi {
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
  standIn(join(bin, "pi"), records, "pi", false);
  standIn(join(root, "installed", "pi"), records, "installed", false);

  const key = `pluxy-pi-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-pi-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: PI,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      pi: join(bin, "pi"),
      installed: join(root, "installed", "pi"),
      local_bin: join(home, ".local", "bin", "pi"),
      agent_bin: join(home, ".pi", "agent", "bin", "pi"),
      adapter_files: files(join(import.meta.dir, "..", "..", "app-tools", "pi-setup", "adapter")),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
