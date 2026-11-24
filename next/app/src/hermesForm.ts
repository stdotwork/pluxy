// The Hermes Agent page's form logic (hermes.tsx), apart from the page so its unit tests need no
// webview: what the tool's preview reads back, and whether a model ID can be applied.

export const PACKAGE = "pluxy/hermes-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Hermes Agent";

/** What the tool's preview reads back of the config file; `refused` (with `reason`) for a file
 * Hermes Agent would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  provider?: string | null;
  model?: string | null;
  legacy?: boolean;
  [k: string]: unknown;
}

/** The model the form starts from: pluxy's default model in the file, else none. */
export const modelOf = (r: ReadBack): string => (r.state === "refused" ? "" : (r.model ?? ""));

/** A model ID the tool can write: 1 to 240 characters, no spaces or control characters, no `${` (Hermes
 * expands it). The tool also refuses an ID YAML 1.1 reads as something other than text, such as `1.5`. */
export const validModel = (m: string): boolean =>
  m.length > 0 && [...m].length <= 240 && !/[\s\p{Cc}]/u.test(m) && !m.includes("${");
