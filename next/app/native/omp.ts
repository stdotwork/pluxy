// Fixtures for the native test's check of the Oh My Pi setup and launcher
// (src-tauri/src/omp_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/omp-setup (next/app-tools/omp-setup, which run.ts builds) and pluxy/execution (the real
//   execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Oh My Pi's agent folder `~/.omp/agent` as a person keeps it: `models.yml`
//   with comments, their own provider and its model list, an old app's provider and a setting this
//   check knows nothing of; `config.yml` with their theme, their own model roles and other
//   settings; the credential store `agent.db` holds a login canary;
// - a stow layout: `~/.omp-stow/models.yml` and `~/.omp-stow/config.yml` are relative links to
//   `~/dotfiles/omp/.omp/agent/`, whose models.yml (mode 0640) already has a `pluxy` provider with
//   the person's own model entry, beside `keep.yml`;
// - a chezmoi-style root: `~/.omp-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_omp/agent`, which has no files yet;
// - files Oh My Pi would not take: `~/unknown/models.yml` whose `providers` is a list,
//   `~/broken/models.yml` with a duplicate key; and `~/legacy/models.json`, a models file Oh My Pi
//   has not moved to models.yml yet;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `omp`, shell scripts that record their arguments, folder and environment in records/;
//   installed/omp is the copy the check later puts at `~/.local/bin/omp` (the install script's
//   place) and then at `~/.bun/bin/omp` (bun's global place, which comes first).
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

export const OMP = "pluxy/omp-setup";

export interface Omp {
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

/** pluxy's own values in the provider, as lines at the indentation of a provider's members
 * (four spaces). */
const OWN = [
  "    baseUrl: http://127.0.0.1:41999/v1",
  "    apiKey: @KEY@",
  "    api: openai-responses",
  "    discovery:",
  "      type: openai-models-list",
].join("\n");

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, login: string) {
  const agent = join(home, ".omp", "agent");
  const models = join(agent, "models.yml");
  const config = join(agent, "config.yml");
  const db = join(agent, "agent.db");
  const dotfiles = join(home, "dotfiles", "omp", ".omp", "agent");
  const stowFolder = join(home, ".omp-stow");
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_omp", "agent");
  const rootLink = join(home, ".omp-root");

  // The person's models: their own provider, an old app's provider, a setting this check does not
  // know, and comments.
  const ollama = [
    "providers:",
    "  # A local model server.",
    "  ollama:",
    "    baseUrl: http://127.0.0.1:11434/v1 # local",
    "    api: openai-completions",
    "    auth: none",
    "    models:",
    "      - id: llama3.3",
    "        contextWindow: 131072",
    "  easy-cliproxyapi:",
    "    baseUrl: http://127.0.0.1:8317/v1",
    "    api: openai-responses",
  ].join("\n");
  const modelsHead = "# The person's Oh My Pi models.\n";
  const modelsTail = "futureSetting: [1, 2.50, 12345678901234567890]\n";
  const original = `${modelsHead}${ollama}\n${modelsTail}`;
  // The person's settings.
  const configOriginal = [
    "# The person's Oh My Pi settings.",
    "theme:",
    "  dark: tokyo-night",
    "modelRoles:",
    "  default: anthropic/claude-sonnet-5-5:high # their default",
    "  smol: anthropic/claude-haiku-5-5",
    "defaultThinkingLevel: high",
    "",
  ].join("\n");
  const configApplied = configOriginal.replace(
    "default: anthropic/claude-sonnet-5-5:high",
    "default: pluxy/gpt-6.1-sol",
  );
  const dbBytes = `pluxy-fixture-credentials ${login}\n`;

  // stow: a pluxy provider with the person's own model entry already there.
  const stowModels = "providers:\n  pluxy:\n    models:\n      - id: my-own-model\n";
  const stowConfig = "theme:\n  dark: nord\n";
  const keep = "keep: true\n";
  // Files Oh My Pi would not take, and one it has not migrated yet.
  const unknownBytes = "providers:\n  - ollama\n  - pluxy\n";
  const brokenBytes = "providers:\n  pluxy:\n    api: openai-responses\n    api: openai-completions\n";
  const legacyBytes = '{"providers": {"ollama": {"api": "openai-completions"}}}\n';

  put(models, original);
  put(config, configOriginal);
  put(db, dbBytes, 0o600);
  put(join(dotfiles, "models.yml"), stowModels, 0o640);
  put(join(dotfiles, "config.yml"), stowConfig);
  put(join(dotfiles, "keep.yml"), keep);
  mkdirSync(stowFolder, { recursive: true });
  symlinkSync("../dotfiles/omp/.omp/agent/models.yml", join(stowFolder, "models.yml"));
  symlinkSync("../dotfiles/omp/.omp/agent/config.yml", join(stowFolder, "config.yml"));
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(join(home, "unknown", "models.yml"), unknownBytes);
  put(join(home, "broken", "models.yml"), brokenBytes);
  put(join(home, "legacy", "models.json"), legacyBytes);

  // pluxy's provider follows the person's providers, inside `providers`.
  const applied = `${modelsHead}${ollama}\n  pluxy:\n${OWN}\n${modelsTail}`;
  // The person's independent model entry under pluxy's provider, added after an apply.
  const ownEntry = "    models:\n      - id: their-model\n        contextWindow: 65536\n";
  const appliedWithEntry = `${modelsHead}${ollama}\n  pluxy:\n${OWN}\n${ownEntry}${modelsTail}`;
  return {
    home,
    models,
    config,
    db,
    stow: {
      folder: stowFolder,
      models: join(stowFolder, "models.yml"),
      config: join(stowFolder, "config.yml"),
      models_text: "../dotfiles/omp/.omp/agent/models.yml",
      config_text: "../dotfiles/omp/.omp/agent/config.yml",
      target_models: join(dotfiles, "models.yml"),
      target_config: join(dotfiles, "config.yml"),
      target_folder: dotfiles,
    },
    root: {
      link: rootLink,
      text: chezmoi,
      models: join(chezmoi, "models.yml"),
      config: join(chezmoi, "config.yml"),
    },
    unknown: join(home, "unknown"),
    broken: join(home, "broken"),
    legacy: join(home, "legacy"),
    settings_input: SETTINGS,
    reapply_model: "deepseek-v4",
    bytes: {
      original,
      config_original: configOriginal,
      db: dbBytes,
      keep,
      unknown: unknownBytes,
      broken: brokenBytes,
      legacy: legacyBytes,
      stow_models: stowModels,
      stow_config: stowConfig,
      // Comments, the person's providers and their order, the setting the check does not know
      // and its digits stay as they are; pluxy's provider follows theirs.
      applied,
      // The default role is pluxy's; its comment, the other role and settings stay.
      config_applied: configApplied,
      // The person's edits of the applied files: their own model entry under pluxy's provider,
      // their theme, their smol role.
      edit_entry: [`${OWN}\n${modelsTail}`, `${OWN}\n${ownEntry}${modelsTail}`],
      edit_theme: ["dark: tokyo-night", "dark: dracula"],
      edit_smol: ["smol: anthropic/claude-haiku-5-5", "smol: anthropic/claude-opus-5-5"],
      // Applying again with another model keeps their model entry, theme and smol role.
      applied_with_entry: appliedWithEntry,
      config_reapplied: configOriginal
        .replace("dark: tokyo-night", "dark: dracula")
        .replace("default: anthropic/claude-sonnet-5-5:high", "default: pluxy/deepseek-v4")
        .replace("smol: anthropic/claude-haiku-5-5", "smol: anthropic/claude-opus-5-5"),
      // Clear after the edits: pluxy's values go; their model entry stays under the provider,
      // their theme and smol role stay, and their own default comes back.
      cleared: `${modelsHead}${ollama}\n  pluxy:\n${ownEntry}${modelsTail}`,
      config_cleared: configOriginal
        .replace("dark: tokyo-night", "dark: dracula")
        .replace("smol: anthropic/claude-haiku-5-5", "smol: anthropic/claude-opus-5-5"),
      // pluxy's values join the person's existing pluxy provider, after their models.
      stow_models_applied: `providers:\n  pluxy:\n    models:\n      - id: my-own-model\n${OWN}\n`,
      stow_config_applied: "theme:\n  dark: nord\nmodelRoles:\n  default: pluxy/gpt-6.1-sol\n",
      // Files the root did not have, created.
      root_models: `providers:\n  pluxy:\n${OWN}\n`,
      root_config: "modelRoles:\n  default: pluxy/gpt-6.1-sol\n",
    },
    // The backup browser's step, from the cleared files: the person's edit after a Manual
    // Backup, the diff a restore of it shows (setting names, never a value), and the values no
    // answer may hold.
    backups: {
      edit: ["dark: dracula", "dark: pluxy-theme-canary"],
      diff: [{ path: ["theme", "dark"], restore: "changes" }],
      values: ["dracula", "pluxy-theme-canary", "anthropic/claude-opus-5-5", "their-model"],
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
 * (the built pluxy/omp-setup and its pluxy-extension.json), execution and executionManifest
 * likewise. */
export function buildOmpFixtures(
  root: string,
  home: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): Omp {
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
  standIn(join(bin, "omp"), records, "omp", false);
  standIn(join(root, "installed", "omp"), records, "installed", false);

  const key = `pluxy-omp-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-omp-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: OMP,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      omp: join(bin, "omp"),
      installed: join(root, "installed", "omp"),
      local_bin: join(home, ".local", "bin", "omp"),
      bun_bin: join(home, ".bun", "bin", "omp"),
      ...configs(home, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
