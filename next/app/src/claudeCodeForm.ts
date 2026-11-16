// The Claude Code page's form logic (claudeCode.tsx), apart from the page so its unit tests need no
// webview: which vault keys the page offers, the form from what the settings file holds, and
// whether the form can be applied.

import { inHome, linuxBuildMissing, notInWsl } from "./environment";

/** Claude Code's own models (its documentation's alias table), suggested until the model picker
 * lists the core's. */
export const MODELS = [
  "claude-opus-5-5",
  "claude-sonnet-5-5",
  "claude-haiku-5-5",
  "claude-fable-5-2",
] as const;
export const ROLES = ["opus", "sonnet", "haiku", "fable"] as const;
export type Role = (typeof ROLES)[number];
export const PACKAGE = "pluxy/claude-code";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Claude Code";
export const WINDOW_DEFAULT = 200000;
export const WINDOW_1M = 1000000;

/** A vault secret as `vault_secrets` lists it: never its value. */
export interface Secret {
  id: string;
  label: string;
  /** Set for a captured client key: the package it may be given to. */
  client_key?: { client: string };
}

/** The keys the page offers from `vault_secrets`'s answer (`{secrets: [...]}`): plain secrets, and
 * client keys bound to this package; a client key bound to another package would be refused. */
export function keysOf(body: { secrets?: Secret[] } | undefined): Secret[] {
  return (body?.secrets ?? []).filter((s) => !s.client_key || s.client_key.client === PACKAGE);
}

/** What the tool's preview reads back of the settings file; `refused` (with `reason`) for a file
 * Claude Code would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  startup_model?: string | null;
  subagent_model?: string;
  max_context_tokens?: number | null;
  auto_compact_pct?: number | null;
  disable_auto_compact?: boolean;
  [k: string]: unknown;
}

export interface Form {
  models: Record<Role, string>;
  oneM: Record<Role, boolean>;
  startup: string;
  subagent: string;
  window: number;
  pct: number;
  disable: boolean;
}

export const fresh = (): Form => ({
  models: { opus: MODELS[0], sonnet: MODELS[1], haiku: MODELS[2], fable: "" },
  oneM: { opus: false, sonnet: false, haiku: false, fable: false },
  startup: "opus",
  subagent: "",
  window: WINDOW_DEFAULT,
  pct: 90,
  disable: false,
});

/** The form from what the file holds. A file pluxy has set up keeps an empty startup model empty;
 * only a file never set up starts from Opus. */
export function fromFile(r: ReadBack): Form {
  const f = fresh();
  if (r.state === "refused") return f;
  for (const role of ROLES) {
    const m = r[role];
    if (typeof m === "string" && m) f.models[role] = m;
    f.oneM[role] = r[`${role}_1m`] === true;
  }
  f.startup = r.startup_model ?? (r.state === "not-configured" ? "opus" : "");
  f.subagent = r.subagent_model ?? "";
  if (r.max_context_tokens) f.window = r.max_context_tokens;
  if (r.auto_compact_pct) f.pct = r.auto_compact_pct;
  f.disable = r.disable_auto_compact === true;
  return f;
}

/** model without trailing `[1m]` suffixes. */
const bare = (model: string) => model.trim().replace(/(\s*\[1m\])+$/i, "");

/** Whether the form can be applied: Opus, Sonnet and Haiku name models, no field is only `[1m]`,
 * and the window and percentage are whole numbers in Claude Code's ranges. */
export function ready(f: Form): boolean {
  const fields = [...ROLES.map((r) => f.models[r]), f.startup, f.subagent];
  return (
    (["opus", "sonnet", "haiku"] as const).every((r) => bare(f.models[r]) !== "") &&
    fields.every((m) => m.trim() === "" || bare(m) !== "") &&
    Number.isInteger(f.window) &&
    f.window >= 100000 &&
    f.window <= 1000000 &&
    Number.isInteger(f.pct) &&
    f.pct >= 1 &&
    f.pct <= 100
  );
}

/** Where Claude Code keeps its user settings: in the home of the selected WSL user, else `~`. */
export const settingsNote = (home?: string) => inHome(home ?? "~", ".claude/settings.json");

/** Whether a refusal says the tool cannot be used here at all (not installed, not enabled), rather
 * than that its Linux build for WSL is missing or that it does not run in WSL. */
export const toolUnavailable = (e?: string) =>
  !!e && !linuxBuildMissing(e) && !notInWsl(e) && (e.includes(PACKAGE) || e.includes("not installed"));
