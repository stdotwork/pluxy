// Fixtures for the native test's check of the Codex setup and launcher
// (src-tauri/src/codex_native.rs), the shared acceptance flow of an integration:
//
// - pluxy/codex-setup (next/app-tools/codex, which run.ts builds) and pluxy/execution (the real
//   execution program), packed with their own manifests into signed curated packages;
// - in the run's HOME, Codex's config `~/.codex/config.toml` as a person keeps it: comments, their
//   own provider and model, a table this check knows nothing of; beside it the login file
//   `~/.codex/auth.json`, holding a login canary, which no step may touch; no model catalog yet;
// - a stow layout: `~/.config/codex-stow/config.toml` is a relative link to
//   `~/dotfiles/codex/.codex/config.toml` (mode 0640), beside `keep.toml`; the catalog is
//   selected beside the link;
// - a chezmoi-style root: `~/.codex-root` is an absolute link to the folder
//   `~/.local/share/chezmoi/dot_codex`, which has neither file yet;
// - files Codex would not take: `~/broken/config.toml` is not TOML, `~/unknown/config.toml` asks
//   pluxy's provider for the removed "chat" wire API, `~/badcat/pluxy-model-catalog.json` has no
//   models list;
// - bin/ goes first on the app's PATH: `x-terminal-emulator` (the first terminal Automatic picks)
//   and `codex`, shell scripts that record their arguments, folder and environment in records/;
//   override/codex is another copy, for the executable override, and app/codex-app stands in for
//   the Codex App program a person names (Codex has no Linux app).
//
// expect.json holds what the check compares with, written out by hand here, never worked out by
// the package's code: the paths, the link texts and every file's bytes after each step, with
// `@KEY@` where the key goes. The catalog entry is the template of gpt-6-luna in pluxy's Codex
// client templates (next/extensions/codex/client_models.json) with the fields this file lists by
// hand. keys.json holds the key the check types into native key entry, made here at random. When
// the package has not been built (its program or manifest is missing), the registry does not
// list it and the check fails at its first step, installing it.

import { generateKeyPairSync, type KeyObject, randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { index, type Package, pack } from "./routing";

export const CODEX = "pluxy/codex-setup";

export interface Codex {
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

/** The models the core serves, as the window's import sends them: one with a Codex template, one
 * without. */
const SERVED = [
  { slug: "gpt-6-luna", upstream: "gpt-6-luna" },
  { slug: "claude-fixture", upstream: "claude-fixture" },
];

/** The values every apply in the check gives, as the window would send them (with `targets`,
 * `key` and the catalog's `models` added by the check from the import's answer). */
const SETTINGS = {
  base_url: "http://127.0.0.1:41999",
  model: "gpt-6-luna",
  approval_model: "gpt-6-luna",
  remote_compaction: false,
  agents: { enabled: true, max_threads: 3, model: "", effort: "low", interrupt: true },
};
/** What the window changes in the imported model before Apply. */
const EDITS = { display_name: "Luna via pluxy", context_window: 200000 };

/** The provider and subagent tables an apply of SETTINGS writes, after the person's tables. */
const PROVIDER =
  '[model_providers.pluxy]\nname = "pluxy"\nbase_url = "http://127.0.0.1:41999/v1"\nwire_api = "responses"\nexperimental_bearer_token = "@KEY@"\n';
const AGENTS =
  '[agents]\nenabled = true\ninterrupt_message = true\nmax_concurrent_threads_per_session = 3\ndefault_subagent_reasoning_effort = "low"\n';

/** The catalog an apply writes: gpt-6-luna's template, the fields a new entry takes, by hand, in
 * the template's order, with the window's edits and the approval model. */
function catalog(next: string): string {
  const all = JSON.parse(readFileSync(join(next, "extensions", "codex", "client_models.json"), "utf8"));
  const t = all.models.find((m: { slug: string }) => m.slug === "gpt-6-luna");
  const entry = {
    slug: "gpt-6-luna",
    display_name: EDITS.display_name,
    description: t.description,
    default_reasoning_level: t.default_reasoning_level,
    supported_reasoning_levels: t.supported_reasoning_levels,
    shell_type: t.shell_type,
    visibility: "list",
    supported_in_api: t.supported_in_api,
    priority: 0,
    support_verbosity: t.support_verbosity,
    default_verbosity: t.default_verbosity,
    apply_patch_tool_type: t.apply_patch_tool_type,
    web_search_tool_type: t.web_search_tool_type,
    truncation_policy: t.truncation_policy,
    supports_image_detail_original: t.supports_image_detail_original,
    context_window: EDITS.context_window,
    max_context_window: t.max_context_window,
    // auto_compact_token_limit is null in the template and blank in the form: left out.
    experimental_supported_tools: t.experimental_supported_tools,
    input_modalities: t.input_modalities,
    supports_search_tool: t.supports_search_tool,
    tool_mode: t.tool_mode,
    default_reasoning_summary: t.default_reasoning_summary,
    model_messages: { instructions_template: t.model_messages.instructions_template },
    auto_review_model_override: "gpt-6-luna",
  };
  return `${JSON.stringify({ models: [entry] }, null, 2)}\n`;
}

/** The files and links in home, and what the check expects of them, by hand. */
function configs(home: string, next: string, login: string) {
  const codex = join(home, ".codex");
  const config = join(codex, "config.toml");
  const cat = join(codex, "pluxy-model-catalog.json");
  const auth = join(codex, "auth.json");
  const dotfiles = join(home, "dotfiles", "codex", ".codex");
  const stowTarget = join(dotfiles, "config.toml");
  const stowDir = join(home, ".config", "codex-stow");
  const stowLink = join(stowDir, "config.toml");
  const stowText = "../../dotfiles/codex/.codex/config.toml";
  const chezmoi = join(home, ".local", "share", "chezmoi", "dot_codex");
  const rootLink = join(home, ".codex-root");
  const broken = join(home, "broken", "config.toml");
  const unknown = join(home, "unknown", "config.toml");
  const badcat = join(home, "badcat");

  const original =
    "# Codex, as a person keeps it\n" +
    'model = "gpt-5.5" # their default\n' +
    'model_provider = "openai"\n' +
    'approval_policy = "on-request"\n' +
    "\n" +
    "[model_providers.own]\n" +
    'name = "Own"\n' +
    'base_url = "https://example.invalid/v1"\n' +
    "\n" +
    "[future_table]\n" +
    "ratio = 2.50\n" +
    "list = [1, 2]\n";
  const authBytes = `{"auth_mode":"chatgpt","tokens":{"access_token":"${login}"}}\n`;
  const stowOriginal = 'model = "gpt-5.5"\n\n[tui]\nnotifications = true\n';
  const keep = "keep = true\n";
  const brokenBytes = 'model = "unterminated\n';
  const unknownBytes = '[model_providers.pluxy]\nwire_api = "chat"\n';
  const badcatConfig = 'model = "gpt-5.5"\n';
  const badcatCatalog = '{\n  "model": []\n}\n';

  put(config, original);
  put(auth, authBytes, 0o600);
  put(stowTarget, stowOriginal, 0o640);
  put(join(dotfiles, "keep.toml"), keep);
  mkdirSync(stowDir, { recursive: true });
  symlinkSync(stowText, stowLink);
  mkdirSync(chezmoi, { recursive: true });
  symlinkSync(chezmoi, rootLink);
  put(broken, brokenBytes);
  put(unknown, unknownBytes);
  put(join(badcat, "config.toml"), badcatConfig);
  put(join(badcat, "pluxy-model-catalog.json"), badcatCatalog);

  const head = (model: string) =>
    `model_provider = "pluxy"\nmodel = "${model}"\nmodel_catalog_json = "pluxy-model-catalog.json"\n`;
  return {
    home,
    config,
    catalog: cat,
    auth,
    stow: {
      link: stowLink,
      text: stowText,
      target: stowTarget,
      folder: dotfiles,
      catalog: join(stowDir, "pluxy-model-catalog.json"),
    },
    root: {
      link: rootLink,
      text: chezmoi,
      config: join(chezmoi, "config.toml"),
      catalog: join(chezmoi, "pluxy-model-catalog.json"),
    },
    broken,
    unknown,
    badcat,
    served: SERVED,
    missing: ["claude-fixture"],
    edits: EDITS,
    settings_input: SETTINGS,
    bytes: {
      original,
      auth: authBytes,
      keep,
      broken: brokenBytes,
      unknown: unknownBytes,
      badcat_config: badcatConfig,
      badcat_catalog: badcatCatalog,
      stow: stowOriginal,
      catalog: catalog(next),
      // The model and provider change in place, the comment on the model stays, the catalog
      // setting joins the top-level keys; the provider table follows the person's provider, the
      // subagent table goes last; the table this check does not know keeps its digits.
      applied:
        "# Codex, as a person keeps it\n" +
        'model = "gpt-6-luna" # their default\n' +
        'model_provider = "pluxy"\n' +
        'approval_policy = "on-request"\n' +
        'model_catalog_json = "pluxy-model-catalog.json"\n' +
        "\n" +
        "[model_providers.own]\n" +
        'name = "Own"\n' +
        'base_url = "https://example.invalid/v1"\n' +
        "\n" +
        PROVIDER +
        "\n" +
        "[future_table]\n" +
        "ratio = 2.50\n" +
        "list = [1, 2]\n" +
        "\n" +
        AGENTS,
      // The person's edit before Restore Backup.
      edited_policy: ['approval_policy = "on-request"', 'approval_policy = "never"'],
      // Their edits before Clear: their own model, and a setting of their own in pluxy's provider.
      edited_model: ['model = "gpt-6-luna"', 'model = "their-model"'],
      edited_retries: ['wire_api = "responses"\n', 'wire_api = "responses"\nstream_max_retries = 7\n'],
      // Clear after both edits: the values Apply wrote go (their provider comes back), their model
      // and their setting stay; the catalog Apply made, untouched, goes.
      cleared:
        "# Codex, as a person keeps it\n" +
        'model = "their-model" # their default\n' +
        'model_provider = "openai"\n' +
        'approval_policy = "on-request"\n' +
        "\n" +
        "[model_providers.own]\n" +
        'name = "Own"\n' +
        'base_url = "https://example.invalid/v1"\n' +
        "\n" +
        "[model_providers.pluxy]\n" +
        "stream_max_retries = 7\n" +
        "\n" +
        "[future_table]\n" +
        "ratio = 2.50\n" +
        "list = [1, 2]\n",
      stow_applied:
        'model = "gpt-6-luna"\n' +
        'model_provider = "pluxy"\n' +
        'model_catalog_json = "pluxy-model-catalog.json"\n' +
        "\n" +
        "[tui]\n" +
        "notifications = true\n" +
        "\n" +
        PROVIDER +
        "\n" +
        AGENTS,
      // Files the root did not have, made.
      root_applied: `${head("gpt-6-luna")}\n${PROVIDER}\n${AGENTS}`,
    },
    // The backup browser's step, from the cleared config: the person's edit after a Manual Backup
    // and the settings a restore of it changes, by name (never a value).
    backups: {
      edit: ['approval_policy = "on-request"', 'approval_policy = "pluxy-policy-canary"'],
      diff: [{ path: ["approval_policy"], restore: "changes" }],
      values: ["pluxy-policy-canary"],
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
 * (the built pluxy/codex-setup and its pluxy-extension.json), execution and executionManifest
 * likewise; next is the next/ folder (for the Codex client templates). */
export function buildCodexFixtures(
  root: string,
  home: string,
  next: string,
  tool: string,
  toolManifest: string,
  execution: string,
  executionManifest: string,
): Codex {
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
  standIn(join(bin, "codex"), records, "codex", false);
  standIn(join(root, "override", "codex"), records, "override", false);
  standIn(join(root, "app", "codex-app"), records, "app", false);

  const key = `pluxy-codex-key-${randomBytes(16).toString("hex")}`;
  const login = `pluxy-codex-login-${randomBytes(16).toString("hex")}`;
  writeFileSync(join(root, "keys.json"), JSON.stringify({ key }));
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: CODEX,
      versions,
      login,
      records,
      work,
      terminal: join(bin, "x-terminal-emulator"),
      codex: join(bin, "codex"),
      override: join(root, "override", "codex"),
      app: join(root, "app", "codex-app"),
      ...configs(home, next, login),
    }),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, canaries: [key, login], bin };
}
