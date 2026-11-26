// The Grok Build page's form logic (grok.tsx), apart from the page so its unit tests need no
// webview: the catalog rows from what the config holds and from the models pluxy serves, and why
// the form cannot be written yet.

export const PACKAGE = "pluxy/grok-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Grok Build";
/** The context window an entry gets until the person sets another (the old app's default). */
export const WINDOW = 200000;
/** The most models one apply writes (the tool's limit). */
export const MODELS_MAX = 256;

/** One of pluxy's model entries as the preview reads it back. */
export interface Entry {
  model: string;
  name: string | null;
  context_window: number | null;
}

/** What the tool's preview reads back of the config; `refused` (with `reason`) for a file Grok
 * Build would not take, which apply and clear refuse and Restore backup still reaches. */
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

/** rows with the models pluxy serves added (selected, as the old app wrote them all); rows already
 * there keep their window and choice. */
export function withServed(rows: Row[], served: { id: string; display_name?: string }[]): Row[] {
  return [
    ...rows,
    ...served
      .filter((m) => !rows.some((r) => r.model === m.id))
      .map((m) => ({ model: m.id, name: m.display_name || m.id, window: WINDOW, on: true })),
  ];
}

/** The largest context window the tool takes. */
export const WINDOW_MAX = 100000000;

/** Why the selected rows and default model cannot be written yet, every reason at once: no model
 * selected, more than the tool takes, a window that is not a whole number from 1 to WINDOW_MAX, a
 * default that is not one of the selected models. Empty when they can be written. */
export type Held = "none" | "too-many" | "window" | "model";

export function heldOf(rows: Row[], model: string): Held[] {
  const on = rows.filter((r) => r.on);
  if (on.length === 0) return ["none"];
  const held: Held[] = [];
  if (on.length > MODELS_MAX) held.push("too-many");
  if (!on.every((r) => Number.isInteger(r.window) && r.window >= 1 && r.window <= WINDOW_MAX))
    held.push("window");
  if (!on.some((r) => r.model === model.trim())) held.push("model");
  return held;
}
