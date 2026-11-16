// The Grok Build page's form logic (grok.tsx), apart from the page so its unit tests need no
// webview: which vault keys the page offers, the catalog rows from what the config holds and from
// the core's models, and whether the form can be applied.

export const PACKAGE = "pluxy/grok-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Grok Build";
/** The context window an entry gets until the person sets another (the old app's default). */
export const WINDOW = 200000;
/** The most models one apply writes (the tool's limit). */
export const MODELS_MAX = 256;

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

/** One of pluxy's model entries as the preview reads it back. */
export interface Entry {
  model: string;
  name: string | null;
  context_window: number | null;
}

/** What the tool's preview reads back of the config; `refused` (with `reason`) for a file Grok
 * Build would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  default?: string | null;
  model?: string | null;
  models?: Entry[];
  legacy?: number;
}

/** One row of the catalog: a model, the name Grok Build shows, its window and whether it is kept. */
export interface Row {
  model: string;
  name: string;
  window: number;
  on: boolean;
}

/** The catalog the form starts from: pluxy's entries in the config, none for a refused file. */
export function rowsOf(r: ReadBack): Row[] {
  if (r.state === "refused") return [];
  return (r.models ?? []).map((m) => ({
    model: m.model,
    name: m.name ?? m.model,
    window: m.context_window ?? WINDOW,
    on: true,
  }));
}

/** The default model the form starts from: pluxy's default in the config, else none. */
export const modelOf = (r: ReadBack): string => (r.state === "refused" ? "" : (r.model ?? ""));

/** rows with the core's served models added (selected, as the old app wrote them all); rows already
 * there keep their window and choice. */
export function withServed(rows: Row[], served: { id: string; display_name?: string }[]): Row[] {
  return [
    ...rows,
    ...served
      .filter((m) => !rows.some((r) => r.model === m.id))
      .map((m) => ({ model: m.id, name: m.display_name || m.id, window: WINDOW, on: true })),
  ];
}

/** Why the selected rows and default model cannot be applied, or undefined when they can. */
export function blocked(rows: Row[], model: string): "none" | "too-many" | "window" | "model" | undefined {
  const on = rows.filter((r) => r.on);
  if (on.length === 0) return "none";
  if (on.length > MODELS_MAX) return "too-many";
  if (!on.every((r) => Number.isInteger(r.window) && r.window >= 1 && r.window <= 100000000)) return "window";
  if (!on.some((r) => r.model === model.trim())) return "model";
  return undefined;
}
