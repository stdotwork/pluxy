// Design directions: whole alternative versions of the panel (shell, navigation and pages) that
// run side by side while one is being chosen. A direction is picked with ?design=a|b|c in the
// address and remembered on this device; ?design=off goes back to today's panel. With none set
// the panel is today's and loads none of this folder but this file.
//
// Each direction lives wholly in its own folder here (designs/<x>/), entered through
// designs/<x>/app.tsx, and shares the kit's API calls, host, privacy mode and theme tokens. See
// README.md beside this file for what a direction's app.tsx exports.

import type { VNode } from "preact";
import type { Host } from "../host";
import type { PrivacyState } from "../privacy";
import type { PageDef } from "../shell";
import type { ThemeState } from "../theme/state";

export const DESIGNS = ["a", "b", "c"] as const;
export type Design = (typeof DESIGNS)[number];

/** Where the chosen direction is remembered (localStorage). */
export const DESIGN_KEY = "pluxy.design";
/** The address parameter that picks a direction, or "off" for none. */
export const DESIGN_PARAM = "design";

const isDesign = (value: string | null): value is Design => DESIGNS.includes(value as Design);

type Store = Pick<Storage, "getItem" | "setItem" | "removeItem">;

/** localStorage, which may be missing or refuse (private windows, blocked site data). */
function localStore(): Store | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

/**
 * The direction to show, or undefined for today's panel. The address's ?design= wins and is
 * remembered: a, b or c picks one, off forgets it. Any other value is ignored. Without the
 * parameter it is the remembered one. search is the address's query string.
 */
export function currentDesign(
  search: string = globalThis.location?.search ?? "",
  store: Store | undefined = localStore(),
): Design | undefined {
  const asked = new URLSearchParams(search).get(DESIGN_PARAM);
  try {
    if (isDesign(asked)) {
      store?.setItem(DESIGN_KEY, asked);
      return asked;
    }
    if (asked === "off") {
      store?.removeItem(DESIGN_KEY);
      return undefined;
    }
    const kept = store?.getItem(DESIGN_KEY) ?? null;
    return isDesign(kept) ? kept : undefined;
  } catch {
    // Storage refused: the address still picks a direction, for this load only.
    return isDesign(asked) ? asked : undefined;
  }
}

/** href with ?design= set to a direction or "off", keeping the page (hash) it is on. */
export function designAddress(href: string, design: Design | "off"): string {
  const url = new URL(href);
  url.searchParams.set(DESIGN_PARAM, design);
  return url.toString();
}

/** What the panel hands a direction: the same as it hands today's Shell. */
export interface DesignProps {
  host: Host;
  themes: ThemeState;
  privacy: PrivacyState;
  /** Today's pages. A direction renders the ones it has not redesigned inside its own shell. */
  pages: PageDef[];
}

/** What designs/<x>/app.tsx exports. */
export interface DesignModule {
  /** The whole panel in this direction: sign-in, navigation and pages. */
  default: (props: DesignProps) => VNode;
  /**
   * The direction's stylesheets, each imported `with { type: "file" }` so it is a file of its own
   * that only this direction loads. They are linked, in order, before the first render.
   */
  styles?: string[];
}

const LOADERS: Record<Design, () => Promise<DesignModule>> = {
  a: () => import("./a/app"),
  b: () => import("./b/app"),
  c: () => import("./c/app"),
};

export const loadDesign = (design: Design): Promise<DesignModule> => LOADERS[design]();
