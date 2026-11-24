// The Claude Desktop page's form logic (claudeDesktop.tsx), apart from the page so its unit tests
// need no webview: the model rows from what the files hold, whether the rows can be applied, and when to offer creating Claude Desktop's library folder.

export const PACKAGE = "pluxy/claude-desktop";
export const PROFILE_ID = "00000000-0000-4000-8000-000000831700";
/** The three files inside a selected third-party folder, in the tool's order. */
export const FILES = [
  "claude_desktop_config.json",
  `configLibrary/${PROFILE_ID}.json`,
  "configLibrary/_meta.json",
] as const;
/** The most rows the tool takes. */
export const MODELS_MAX = 16;
/** What the tool's default preview says when Claude Desktop's library folder is not there yet,
 * which its `folders` operation creates (next/app-tools/claude-desktop, MISSING_LIBRARY). */
export const MISSING_LIBRARY = "Claude Desktop's configuration library does not exist yet";

/** One model row: the model the core serves, the name Claude Desktop uses (empty: the model's
 * own) and whether its 1M-context variant is offered. */
export interface Row {
  model: string;
  alias: string;
  one_m: boolean;
}

/** What the tool's preview reads back of the three files; `refused` (with `reason`) when Claude
 * Desktop would not take one of them, which apply and clear refuse and Restore Backup still
 * reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  models?: Row[];
}

/** The old app's three starting rows: Claude models, which need no alias. */
export const fresh = (): Row[] => [
  { model: "claude-opus-5-5", alias: "", one_m: false },
  { model: "claude-sonnet-5-5", alias: "", one_m: false },
  { model: "claude-haiku-5-5", alias: "", one_m: false },
];

/** The rows from what the files hold, or the starting rows when they hold none (or are refused). */
export function rowsFrom(r: ReadBack): Row[] {
  if (r.state === "refused" || !r.models || r.models.length === 0) return fresh();
  return r.models.map((m) => ({ model: m.model, alias: m.alias, one_m: m.one_m === true }));
}

/** Whether the rows can be applied: 1 to 16 of them, each naming a model with no spaces. Claude
 * Desktop's name rule is the tool's, which says which row it refuses. */
export function ready(rows: Row[]): boolean {
  const id = (s: string) => /^\S*$/.test(s.trim());
  return (
    rows.length >= 1 &&
    rows.length <= MODELS_MAX &&
    rows.every((r) => r.model.trim() !== "" && id(r.model) && id(r.alias))
  );
}

/** Whether a preview's error is the default location's missing library folder, the one case the
 * page offers to create. */
export const missingLibrary = (error: string | undefined) => !!error?.includes(MISSING_LIBRARY);

/** What a row's alias needs from core, which serves the names Claude Desktop sends: nothing
 * (no alias, or the model's own name), nothing more (core lists it), creating it in core (core
 * does not list it), or unknown (core's list was not read). */
export type AliasState = "none" | "served" | "missing" | "unknown";

/** name with ASCII letters in lower case: core matches a model's name so, as the tool's Rust
 * (`eq_ignore_ascii_case`) does. */
const folded = (name: string) => name.replace(/[A-Z]/g, (c) => c.toLowerCase());

export function aliasState(row: Row, served: ReadonlySet<string> | undefined): AliasState {
  const alias = row.alias.trim();
  // An alias that is the model's own name in another case renames nothing in core.
  if (alias === "" || folded(alias) === folded(row.model.trim())) return "none";
  if (!served) return "unknown";
  return served.has(alias) ? "served" : "missing";
}

/** Whether `core_alias_create`'s refusal is the person's cancel of its confirmation: exactly the
 * app's "cancelled", not any message that mentions the word. */
export const aliasCancelled = (error: string | undefined) => error === "cancelled";
