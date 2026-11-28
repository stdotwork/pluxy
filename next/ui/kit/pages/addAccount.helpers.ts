// What Add an account works out without drawing anything: which providers are tiles and which sit
// under More, the series colour of a provider's mark, and which of a provider's ways is the one to
// suggest.

import type { LoginMethod } from "../api";
import { MAIN_KINDS } from "./setup.helpers";

/** How many provider tiles the page shows before More. */
export const TILES = 4;

/** The short ID of an extension: "pluxy/claude" is "claude". */
export const shortId = (extension: string) => extension.slice(extension.lastIndexOf("/") + 1);

/**
 * The providers shown as tiles and the ones under More. The tiles are the main kinds that run
 * (Claude, ChatGPT, Gemini, another service with an API key), in that order, and when fewer than four
 * of them run, the other providers in the order core lists them, up to four. Everything else is under
 * More, in core's order. With four or fewer providers there is no More.
 */
export function splitKinds<T extends { extension: string }>(groups: T[]): { tiles: T[]; more: T[] } {
  if (groups.length <= TILES) return { tiles: groups, more: [] };
  const main = MAIN_KINDS.flatMap((kind) => groups.filter((g) => g.extension === kind.id).slice(0, 1));
  const rest = groups.filter((g) => !main.includes(g));
  const tiles = [...main, ...rest].slice(0, TILES);
  return { tiles, more: groups.filter((g) => !tiles.includes(g)) };
}

/**
 * The way to suggest when a provider has several: its first sign-in in the browser. A provider with
 * one way, or none that opens a browser, suggests none.
 */
export function recommendedWay(methods: LoginMethod[]): string | undefined {
  if (methods.length < 2) return undefined;
  return methods.find((method) => method.kind === "browser")?.method;
}
