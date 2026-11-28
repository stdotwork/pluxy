// Fixtures for the native test's Setup checks (native/setup.ts, src-tauri/src/native_setup.rs),
// beside the session's first-run core (core-steps.ts firstRunSource):
//
// - bin/claude, a stand-in Claude Code that prints `2.4.1 (Claude Code)` when asked its version,
//   as panelcore -first-run's does; bin/ goes first on the app's PATH, which its core inherits,
//   so core's setup scan finds it;
// - home/.claude/.credentials.json, a Claude Code sign-in (never read, only seen to exist), so the
//   scan suggests the Claude pick: an installed Claude Code alone does not;
// - a curated registry offering pluxy/claude-code, the Claude Code page's package, signed by a key
//   made here, with the test trust profile naming it: so the app's Setup page offers to install it
//   for Claude Code's row (agentPages.ts rowAction), and the check installs and turns it on. It
//   is a valid native package whose program is a placeholder, which no check starts.

import { generateKeyPairSync, type KeyObject } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { artifact, type Fixture, platform, writeTrustProfile } from "./admission";
import { CLAUDE_CODE } from "./claude-code";
import { index, type Package, pack } from "./routing";

export interface FirstRun {
  /** The registries file the app's config should hold. */
  registries: { name: string; url: string }[];
  /** PLUXY_TEST_TRUST_NONCE for the app: the marker's nonce. */
  nonce: string;
  /** The folder to put first on the app's PATH. */
  bin: string;
}

/** Builds the fixtures in root and the trust profile in home (the app's HOME). */
export function buildFirstRunFixtures(root: string, home: string): FirstRun {
  const bin = join(root, "bin");
  mkdirSync(bin, { recursive: true });
  writeFileSync(join(bin, "claude"), "#!/bin/sh\necho '2.4.1 (Claude Code)'\n", { mode: 0o755 });
  mkdirSync(join(home, ".claude"), { recursive: true });
  writeFileSync(join(home, ".claude", ".credentials.json"), '{"claudeAiOauth":"opaque"}', { mode: 0o600 });

  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const placeholder = join(root, "placeholder");
  writeFileSync(placeholder, "#!/bin/sh\nexit 1\n", { mode: 0o755 });
  const p: Package = {
    id: CLAUDE_CODE,
    version: "0.1.0",
    tier: "native",
    app: {
      version: 0,
      tier: "native",
      host_versions: [0],
      programs: { [platform()]: "bin/tool" },
      operations: [
        {
          id: "inspect",
          description: "Report the fixture's state",
          input_schema: { type: "object" },
          output_schema: { type: "object" },
        },
      ],
      state: { version: 0, readable_versions: [0] },
    },
  };
  const file = pack(root, p, placeholder);
  const f: Fixture = { file, id: p.id, version: p.version, tier: "native" };
  const curatedFile = join(root, "curated.json");
  writeFileSync(
    curatedFile,
    JSON.stringify(index([[p, artifact(root, file, f, publicKey, privateKey as KeyObject).entry]])),
  );
  const nonce = writeTrustProfile(home, [curatedFile], publicKey);
  return { registries: [{ name: "curated", url: curatedFile }], nonce, bin };
}
