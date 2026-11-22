// Setup: what core found on its machine, the packages it offers a new install, and the text
// that points a client at core.

import type { Host } from "../host";
import type { Package } from "./packages";
import { one } from "./path";

/** How a client's own settings stand towards this core. */
export type SetupClientState = "not-configured" | "configured" | "needs-update" | "refused";

/** A coding tool core looked for on its machine. */
export interface SetupClient {
  id: string;
  name: string;
  installed: boolean;
  version?: string;
  /** The client's settings file, from the home folder. */
  config: string;
  state: SetupClientState;
}

/** A sign-in core saw on its machine. Core reads no token from it. */
export interface SetupSignIn {
  provider: string;
  from: string;
  label: string;
}

/** A CLIProxyAPI setup core saw on its machine. */
export interface SetupImport {
  kind: string;
  config: string;
  auth_dir: string;
  accounts: number;
  /** The last import made from this config on this core, when there was one. */
  last_import?: SetupLastImport;
}

/**
 * What core kept of an import: when it ran, and how many rows each list of its report held. A row
 * is a key of the config or a file of the auth folder, so these do not count accounts.
 */
export interface SetupLastImport {
  at: string;
  brought: number;
  changed: number;
  not_brought: number;
}

export interface SetupFound {
  clients: SetupClient[];
  sign_ins: SetupSignIn[];
  imports: SetupImport[];
  core: { listen: string; reachable_from_network: boolean };
}

/** A package core offers at setup. */
export interface SetupPick {
  id: string;
  kind: string;
  title: string;
  summary: string;
  installed: boolean;
  /** Something found on the machine points to this pick; because says what. */
  suggested: boolean;
  because?: string;
}

export interface Setup {
  first_run: boolean;
  scanning: boolean;
  /** When the kept scan ended; absent until the first scan has. */
  scanned_at?: string;
  found: SetupFound;
  picks: SetupPick[];
  /** How many extension registries core reads from; absent on older cores. */
  registries?: number;
}

/** One pick's outcome in the reply to an apply. */
export interface SetupApplyResult {
  id: string;
  status: "done" | "error";
  package?: Package;
  error?: string;
}

/** What to paste into a client's settings files to point it at core. */
export interface ClientPaste {
  tool: string;
  name: string;
  /** Core's address as a program on core's machine reaches it. */
  base_url: string;
  /** The placeholder the files hold where a client key goes; never a key's value. */
  key: string;
  files: { path: string; content: string }[];
  /** What the files need besides themselves. */
  note?: string;
}

/**
 * One item of an import report: a key of the CLIProxyAPI config (what is its path in the file) or
 * a file of its auth folder (what is "auth file", name its name).
 */
export interface ImportRow {
  what: string;
  name?: string;
  /** What it became in pluxy. */
  as?: string;
  from?: string;
  to?: string;
  why?: string;
  /** How to keep what the old setup did, where pluxy changed it. */
  keep_with?: string;
}

/** What an import of a CLIProxyAPI setup did, or with dry_run would do. */
export interface ImportReport {
  dry_run: boolean;
  brought: ImportRow[];
  changed: ImportRow[];
  not_brought: ImportRow[];
  /** The provider packages the imported accounts need that core has not installed. */
  needs: { pick: string; why: string }[];
}

export const setupApi = (host: Host) => ({
  setup: () => host.call<Setup>("GET", "/setup"),
  scanSetup: () => host.call<Setup>("POST", "/setup/scan"),
  applySetup: (picks: string[]) =>
    host.call<{ results: SetupApplyResult[] }>("POST", "/setup/apply", { picks }),
  /**
   * Brings a CLIProxyAPI setup on core's machine across; with dryRun core writes nothing and
   * reports what it would do. authDir, when left out, is the folder the config names.
   */
  importSetup: (config: string, authDir: string | undefined, dryRun: boolean) =>
    host.call<ImportReport>("POST", "/setup/import", { config, auth_dir: authDir, dry_run: dryRun }),
  finishSetup: () => host.call<{ first_run: boolean }>("POST", "/setup/done"),
  /** keyId names a client key from clientKeys(); the placeholder then names it. */
  clientPaste: (id: string, keyId?: string) =>
    host.call<ClientPaste>("GET", `/setup/clients/${one(id)}/paste${keyId ? `?key_id=${one(keyId)}` : ""}`),
});
