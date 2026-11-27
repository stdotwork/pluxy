// The Oh My Pi page's form logic (omp.tsx), apart from the page so its unit tests need no webview:
// what the tool's preview reads back, and whether a model ID can be applied.

import { hasSpaceOrControl, length } from "./textRules";

export const PACKAGE = "pluxy/omp-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Oh My Pi";

/** What the tool's preview reads back of the two files; `refused` (with `reason`) for files Oh My
 * Pi would not take, which apply and clear refuse and Restore Backup still reaches. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  model?: string | null;
  default_role?: string | null;
  legacy?: boolean;
}

/** The model the form starts from: pluxy's default model in the files, else none. */
export const modelOf = (r: ReadBack): string => (r.state === "refused" ? "" : (r.model ?? ""));

/** A model ID Oh My Pi's default role takes as one model (omp-setup's `settings`): 1 to 240
 * characters, no spaces, commas (a list of fallbacks) or control characters, not starting with `@`
 * (a role) or `*`. */
export const validModel = (m: string): boolean =>
  m.length > 0 && length(m) <= 240 && !hasSpaceOrControl(m) && !m.includes(",") && !/^[@*]/.test(m);
