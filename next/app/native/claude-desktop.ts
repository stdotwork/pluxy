// Fixtures for the native test's check of the Claude Desktop setup and launcher
// (src-tauri/src/claude_desktop_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/claude-desktop (next/app-tools/claude-desktop, which run.ts builds) and pluxy/execution
//   (the real execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Claude Desktop's third-party folder as a person keeps it:
//   `~/.config/Claude-3p/claude_desktop_config.json` with their earlier sign-in choice (1p) and a
//   preference this check knows nothing of, but no `configLibrary` folder yet; the files Claude
//   Desktop saves there once that folder exists (`_meta.json` naming the person's own gateway
//   configuration, a hybrid pointer and a key this check knows nothing of, and that configuration
//   itself); beside it the standard app's `~/.config/Claude/claude_desktop_config.json` (their MCP
//   servers) and `~/.config/Claude/config.json` (holding a login canary), which no step may touch;
// - a stow layout: `~/.config/claude-desktop-stow/claude_desktop_config.json` is a relative link to
//   `~/dotfiles/claude-desktop/claude_desktop_config.json` (mode 0640), beside `keep.json` and an
//   empty `configLibrary` folder;
// - a chezmoi-style root: `~/.claude-desktop-root` is an absolute link to the empty folder
//   `~/.local/share/chezmoi/private_dot_config/private_Claude-3p`, which has no `configLibrary`;
// - files Claude Desktop would not take: `~/unknown/Claude-3p` (its `_meta.json` entries are not a
//   list), `~/broken/Claude-3p` (its `claude_desktop_config.json` has a comment);
// - bin/ goes first on the app's PATH: `claude-desktop`, a shell script that records its arguments,
//   folder and environment in records/; override/claude-desktop is another copy, for the executable
//   override.
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

export const CLAUDE_DESKTOP = "pluxy/claude-desktop";

/** pluxy's configuration in Claude Desktop's configuration library. */
const ID = "00000000-0000-4000-8000-000000831700";
/** The person's own gateway configuration, and one they add later. */
const OTHER = "3f9b6c2e-8d41-4a7f-9b0e-5c2d7a1e6f40";
const THIRD = "b7e2a9d4-1c6f-4e83-a5b0-9d8c7f6e5a41";

export interface ClaudeDesktop {
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
 * records/<name>.*, then `done`. */
function standIn(path: string, records: string, name: string) {
  const r = (x: string) => `'${join(records, `${name}.${x}`)}'`;
  put(
    path,
    `#!/bin/sh
for a in "$@"; do printf '%s\\n' "$a"; done > ${r("argv")}
pwd -P > ${r("cwd")}
env > ${r("env")}
: > ${r("done")}
`,
    0o755,
  );
}

/** The values every apply in the check gives, as the window would send them (with `targets` and
 * `key` added by the check): a Claude model as it is, a non-Claude model under an alias the core
 * serves (with 1M), and another Claude model. */
const SETTINGS = {
  base_url: "http://127.0.0.1:41999",
  models: [
    { model: "claude-opus-5-5", alias: "", one_m: false },
    { model: "gpt-6.1-sol", alias: "claude-sonnet-5-sol", one_m: true },
    { model: "claude-haiku-5-5", alias: "", one_m: false },
  ],
};

/** The gateway profile an apply of SETTINGS writes into a library that had none, by hand. */
const PROFILE =
  "{\n" +
  '  "inferenceProvider": "gateway",\n' +
  '  "inferenceGatewayBaseUrl": "http://127.0.0.1:41999",\n' +
  '  "inferenceGatewayApiKey": "@KEY@",\n' +
  '  "inferenceGatewayAuthScheme": "bearer",\n' +
  '  "inferenceCredentialKind": "static",\n' +
  '  "modelDiscoveryEnabled": false,\n' +
  '  "bootstrapEnabled": false,\n' +
  '  "disableDeploymentModeChooser": true,\n' +
  '  "inferenceModels": [\n' +
  "    {\n" +
  '      "name": "claude-opus-5-5",\n' +
  '      "anthropicFamilyTier": "opus",\n' +
  '      "isFamilyDefault": true\n' +
  "    },\n" +
  "    {\n" +
  '      "name": "claude-sonnet-5-sol",\n' +
  '      "labelOverride": "gpt-6.1-sol",\n' +
  '      "anthropicFamilyTier": "sonnet",\n' +
  '      "isFamilyDefault": true,\n' +
  '      "supports1m": true,\n' +
  '      "prefer1m": true\n' +
  "    },\n" +
  "    {\n" +
  '      "name": "claude-haiku-5-5",\n' +
  '      "anthropicFamilyTier": "haiku",\n' +
  '      "isFamilyDefault": true\n' +
  "    }\n" +
  "  ]\n" +
  "}\n";

/** `_meta.json` as an apply writes it into a library that had none. */
const META_NEW = `{\n  "appliedId": "${ID}",\n  "entries": [\n    {\n      "id": "${ID}",\n      "name": "pluxy"\n    }\n  ]\n}\n`;

/** `claude_desktop_config.json` as an apply writes it where there was none. */
const DEPLOYMENT_NEW = '{\n  "deploymentMode": "3p"\n}\n';

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const threep = join(home, ".config", "Claude-3p");
  const library = join(threep, "configLibrary");
  const deployment = join(threep, "claude_desktop_config.json");
  const standard = join(home, ".config", "Claude", "claude_desktop_config.json");
  const loginFile = join(home, ".config", "Claude", "config.json");
  const dotfiles = join(home, "dotfiles", "claude-desktop");
  const stowTarget = join(dotfiles, "claude_desktop_config.json");
  const stowLink = join(home, ".config", "claude-desktop-stow", "claude_desktop_config.json");
  const stowText = "../../dotfiles/claude-desktop/claude_desktop_config.json";
  const chezmoi = join(home, ".local", "share", "chezmoi", "private_dot_config", "private_Claude-3p");
  const rootLink = join(home, ".claude-desktop-root");
  const unknown = join(home, "unknown", "Claude-3p");
  const broken = join(home, "broken", "Claude-3p");

  const deploymentOriginal =
    '{\n  "deploymentMode": "1p",\n  "preferences": {\n    "menuBarEnabled": false\n  }\n}\n';
  const standardBytes = '{\n  "mcpServers": {}\n}\n';
  const loginBytes = `{"oauth:tokenCache":"${login}"}\n`;
  // What Claude Desktop saves in the library once the person sets up their own gateway there.
  const metaOriginal =
    `{\n  "appliedId": "${OTHER}",\n  "entries": [\n    {\n      "id": "${OTHER}",\n      "name": "Work gateway"\n    }\n  ],\n` +
    '  "hybridPointer": {\n    "bootstrapUrl": "https://config.example.invalid"\n  },\n' +
    '  "futureKey": 12345678901234567890\n}\n';
  const otherBytes =
    '{\n  "inferenceProvider": "gateway",\n  "inferenceGatewayBaseUrl": "https://gateway.example.invalid",\n' +
    '  "inferenceGatewayApiKey": "fixture-work-gateway-key"\n}\n';
  const stowOriginal = '{\n  "deploymentMode": "any"\n}\n';
  const keep = '{"keep": true}\n';
  const unknownMeta = '{\n  "appliedId": "x",\n  "entries": {\n    "id": "x"\n  }\n}\n';
  const brokenBytes = '{\n  // a comment Claude Desktop would refuse\n  "deploymentMode": "1p"\n}\n';

  put(deployment, deploymentOriginal);
  put(standard, standardBytes);
  put(loginFile, loginBytes, 0o600);
  put(stowTarget, stowOriginal, 0o640);
  put(join(dotfiles, "keep.json"), keep);
  mkdirSync(join(dotfiles, "configLibrary"), { recursive: true });
  mkdirSync(dirname(stowLink), { recursive: true });
  symlinkSync(stowText, stowLink);
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(join(unknown, "configLibrary", "_meta.json"), unknownMeta);
  put(join(broken, "claude_desktop_config.json"), brokenBytes);
  mkdirSync(join(broken, "configLibrary"), { recursive: true });

  const metaApplied =
    `{\n  "appliedId": "${ID}",\n  "entries": [\n    {\n      "id": "${OTHER}",\n      "name": "Work gateway"\n    },\n` +
    `    {\n      "id": "${ID}",\n      "name": "pluxy"\n    }\n  ],\n` +
    '  "futureKey": 12345678901234567890\n}\n';
  return {
    home,
    threep,
    library,
    deployment,
    profile: join(library, `${ID}.json`),
    meta: join(library, "_meta.json"),
    other: join(library, `${OTHER}.json`),
    standard,
    login_file: loginFile,
    stow: {
      link: stowLink,
      text: stowText,
      target: stowTarget,
      folder: dotfiles,
      profile: join(dotfiles, "configLibrary", `${ID}.json`),
      meta: join(dotfiles, "configLibrary", "_meta.json"),
    },
    root: { link: rootLink, text: chezmoi, library: join(chezmoi, "configLibrary") },
    unknown,
    broken,
    settings_input: SETTINGS,
    bytes: {
      deployment: deploymentOriginal,
      standard: standardBytes,
      login: loginBytes,
      meta: metaOriginal,
      other: otherBytes,
      keep,
      stow: stowOriginal,
      unknown_meta: unknownMeta,
      broken: brokenBytes,
      // The earlier sign-in choice becomes 3p, the preference stays; the person's own
      // configuration stays listed, the hybrid pointer goes (it would win over appliedId), the key
      // the check does not know keeps its digits.
      deployment_applied:
        '{\n  "deploymentMode": "3p",\n  "preferences": {\n    "menuBarEnabled": false\n  }\n}\n',
      profile_applied: PROFILE,
      meta_applied: metaApplied,
      // The person's edit of the applied deployment file: their preference.
      edited_preference: ['"menuBarEnabled": false', '"menuBarEnabled": true'],
      // Claude Desktop sealing the key in place at its next launch (its keystore).
      sealed: [
        '"inferenceGatewayApiKey": "@KEY@"',
        '"inferenceGatewayApiKey": "pluxy-sealed-v1:AAAAfixture"',
      ],
      // The person adding a configuration of their own in Claude Desktop's window.
      added_entry: [
        `      "name": "pluxy"\n    }`,
        `      "name": "pluxy"\n    },\n    {\n      "id": "${THIRD}",\n      "name": "Personal"\n    }`,
      ],
      // Clear after those three: the sign-in choice comes back, the preference stays; the profile
      // (untouched but for the sealing) goes; appliedId comes back, pluxy's entry goes and the
      // person's new one stays, the hybrid pointer comes back (last, after the keys the file has).
      deployment_cleared:
        '{\n  "deploymentMode": "1p",\n  "preferences": {\n    "menuBarEnabled": true\n  }\n}\n',
      meta_cleared:
        `{\n  "appliedId": "${OTHER}",\n  "entries": [\n    {\n      "id": "${OTHER}",\n      "name": "Work gateway"\n    },\n` +
        `    {\n      "id": "${THIRD}",\n      "name": "Personal"\n    }\n  ],\n` +
        '  "futureKey": 12345678901234567890,\n' +
        '  "hybridPointer": {\n    "bootstrapUrl": "https://config.example.invalid"\n  }\n}\n',
      stow_applied: '{\n  "deploymentMode": "3p"\n}\n',
      // Files the stow and root cases create, the library having none.
      meta_new: META_NEW,
      deployment_new: DEPLOYMENT_NEW,
    },
    // The backup browser's step, from the cleared files: the person's edit after a Manual Backup
    // and the change a restore of it shows (a setting's name, never its value).
    backups: {
      edit: ['"menuBarEnabled": true', '"menuBarEnabled": "pluxy-backup-canary"'],
      diff: [{ path: ["preferences", "menuBarEnabled"], restore: "changes" }],
      values: ["pluxy-backup-canary", "fixture-work-gateway-key"],
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
 * (the built pluxy/claude-desktop and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildClaudeDesktopFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): ClaudeDesktop {
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
  mkdirSync(records);
  standIn(join(bin, "claude-desktop"), records, "app");
  standIn(join(root, "override", "claude-desktop"), records, "override");

  const key = `pluxy-claude-desktop-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-claude-desktop-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: CLAUDE_DESKTOP,
      profile_id: ID,
      versions,
      login,
      records,
      app: join(bin, "claude-desktop"),
      override: join(root, "override", "claude-desktop"),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
