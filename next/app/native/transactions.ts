// Fixtures for the native test's check of the transaction library (src-tauri/src/transactions_native.rs):
// the fixture writer (next/app-tools/lib/transactions, example fixture-writer, which run.ts builds)
// in a signed native package, a curated registry listing it, the run's test trust profile, and, in
// the run's HOME, config files managed the way stow and chezmoi manage them:
//
// - stow: ~/.config/fixture/settings.json is a relative link to
//   ~/dotfiles/fixture/.config/fixture/settings.json (mode 0640), beside other.json; ~/dotfiles/alt
//   holds the file the check retargets the link to;
// - chezmoi: ~/.fixture-root is an absolute link to the folder ~/.local/share/chezmoi/fixture-root,
//   which holds a.json (mode 0644), no b.json, and inner-link.json, a link to ~/outside.json that no
//   write may follow.
//
// expect.json holds what the check compares with, written out by hand here, never worked out by the
// library's code: the paths, the link texts and every file's expected bytes after each step. When
// the fixture writer has not been built (its program or manifest is missing), the registry lists
// nothing and the check fails at its first step, installing the writer.

import { spawnSync } from "node:child_process";
import { generateKeyPairSync, type KeyObject } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";

export const WRITER_ID = "pluxy/fixture-writer";

export interface Transactions {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
}

/** Writes bytes to path, making its folder, with mode. */
function put(path: string, bytes: string, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, bytes);
  chmodSync(path, mode);
}

/** The config files and links in home, and what the check expects of them, by hand. */
function configs(home: string) {
  const dotfiles = join(home, "dotfiles", "fixture", ".config", "fixture");
  const stowTarget = join(dotfiles, "settings.json");
  const stowLink = join(home, ".config", "fixture", "settings.json");
  const stowText = "../../dotfiles/fixture/.config/fixture/settings.json";
  const alt = join(home, "dotfiles", "alt", "settings.json");
  const chezmoi = join(home, ".local", "share", "chezmoi", "fixture-root");
  const rootLink = join(home, ".fixture-root");
  const outside = join(home, "outside.json");

  const settings = '{\n  "theme": "light",\n  "keep": {"a": 1}\n}\n';
  const a = '{ "name": "a" }\n';
  put(stowTarget, settings, 0o640);
  put(join(dotfiles, "other.json"), '{"other": true}\n');
  put(alt, '{"alt": true}\n');
  mkdirSync(dirname(stowLink), { recursive: true });
  symlinkSync(stowText, stowLink);
  put(join(chezmoi, "a.json"), a);
  put(outside, '{"outside": true}\n');
  symlinkSync(outside, join(chezmoi, "inner-link.json"));
  symlinkSync(chezmoi, rootLink);

  return {
    home,
    stow: { link: stowLink, text: stowText, target: stowTarget, alt, folder: dotfiles },
    root: {
      link: rootLink,
      text: chezmoi,
      folder: chezmoi,
      a: join(chezmoi, "a.json"),
      b: join(chezmoi, "b.json"),
    },
    outside,
    // Every file's bytes as the check expects them, step by step.
    bytes: {
      settings,
      a,
      outside: '{"outside": true}\n',
      other: '{"other": true}\n',
      alt: '{"alt": true}\n',
      // apply {"model": "fixture-model", "proxy": "http://127.0.0.1:9"} to settings.json.
      settingsApplied:
        '{\n  "keep": {\n    "a": 1\n  },\n  "model": "fixture-model",\n  "proxy": "http://127.0.0.1:9",\n  "theme": "light"\n}\n',
      // apply {"managed": true} to a.json and the missing b.json.
      aApplied: '{\n  "managed": true,\n  "name": "a"\n}\n',
      bApplied: '{\n  "managed": true\n}\n',
      // The person's edit of settings.json between a preview and its apply: a key added.
      settingsEdited:
        '{\n  "keep": {\n    "a": 1\n  },\n  "model": "fixture-model",\n  "proxy": "http://127.0.0.1:9",\n  "theme": "light",\n  "editor": "vim"\n}\n',
      // Then the person's own model; clear keeps both edits and drops the untouched proxy.
      settingsEditedAgain:
        '{\n  "keep": {\n    "a": 1\n  },\n  "model": "user-model",\n  "proxy": "http://127.0.0.1:9",\n  "theme": "light",\n  "editor": "vim"\n}\n',
      settingsCleared:
        '{\n  "editor": "vim",\n  "keep": {\n    "a": 1\n  },\n  "model": "user-model",\n  "theme": "light"\n}\n',
      // The person's edit of the applied a.json, which restore replaces with the original.
      aEdited: '{\n  "managed": true,\n  "name": "a",\n  "user": 1\n}\n',
      // A competing write to a.json after a crash, which recovery must keep.
      competing: '{"competing": true}\n',
      // apply {"crash": 3} to the missing b.json, recovered as committed.
      bCrash: '{\n  "crash": 3\n}\n',
    },
  };
}

/** Packs and signs the fixture writer from tools (next/app-tools); undefined when it was not built. */
function packWriter(
  root: string,
  tools: string,
  program: string,
  publicKey: KeyObject,
  privateKey: KeyObject,
) {
  const manifestPath = join(
    tools,
    "lib",
    "transactions",
    "examples",
    "fixture-writer",
    "pluxy-extension.json",
  );
  if (!existsSync(program) || !existsSync(manifestPath)) return undefined;
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const dir = join(root, "pkg-fixture-writer");
  mkdirSync(join(dir, "bin"), { recursive: true });
  const bin = "bin/fixture-writer";
  manifest.app.programs = { [platform()]: bin };
  copyFileSync(program, join(dir, bin));
  chmodSync(join(dir, bin), 0o755);
  writeFileSync(join(dir, "pluxy-extension.json"), JSON.stringify(manifest));
  const f: Fixture = {
    file: `fixture-writer-${manifest.version}.tar.gz`,
    id: WRITER_ID,
    version: manifest.version,
    tier: "native",
  };
  const r = spawnSync("tar", ["-czf", join(root, f.file), "-C", dir, "."], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`tar exited with ${r.status}`);
  rmSync(dir, { recursive: true, force: true });
  return { version: manifest.version as string, bin, ...artifact(root, f.file, f, publicKey, privateKey) };
}

/** Builds the fixtures in root, the config files and the test trust profile in home (the app's
 * HOME), with program (the built fixture writer) from tools (next/app-tools). */
export function buildTransactionsFixtures(
  root: string,
  home: string,
  tools: string,
  program: string,
): Transactions {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const writer = packWriter(root, tools, program, publicKey, privateKey);
  const curated = join(root, "curated.json");
  writeFileSync(
    curated,
    JSON.stringify({
      registry_version: 0,
      packages: writer
        ? {
            [WRITER_ID]: {
              releases: [{ version: writer.version, artifacts: { [platform()]: writer.entry } }],
            },
          }
        : {},
    }),
  );
  writeFileSync(
    join(root, "expect.json"),
    JSON.stringify({
      id: WRITER_ID,
      version: writer?.version ?? "0.1.0",
      sha256: writer?.sha256 ?? null,
      bin: writer?.bin ?? "bin/fixture-writer",
      ...configs(home),
    }),
  );
  const nonce = writeTrustProfile(home, [curated], publicKey);
  return { registries: [{ name: "curated", url: curated }], nonce };
}
