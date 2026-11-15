// The Oh My Pi page's form logic (omp.tsx), apart from the page so its unit tests need no webview:
// which vault keys the page offers, what the tool's preview reads back, and whether a model ID can
// be applied.

export const PACKAGE = "pluxy/omp-setup";
/** The client label the tool's recipe gives its launches, which the launcher keys recent folders by. */
export const CLIENT = "Oh My Pi";

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

/** A model ID Oh My Pi's default role takes as one model: no spaces or commas (a list of
 * fallbacks), not starting with `@` (a role) or `*`. */
export const validModel = (m: string): boolean => m.length > 0 && !/[\s,]/.test(m) && !/^[@*]/.test(m);
