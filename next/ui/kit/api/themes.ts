// The themes core keeps (next/management/README.md, "Themes"). Core checks a stored theme file is
// one UTF-8 JSON object of at most 256 KiB and takes its `name` for the list; the kit's reader
// (theme/validate.ts) is the one validator of everything else.

import type { Host } from "../host";
import { seg } from "./path";

/** One stored theme, as core lists it. */
export interface StoredTheme {
  /** publisher/name. */
  id: string;
  /** The file's `name` field; empty when the file has none. */
  name: string;
  bytes: number;
}

export interface StoredThemes {
  /** The ID of the theme that is on; empty for the UI's default. */
  current: string;
  themes: StoredTheme[];
}

export const themesApi = (host: Host) => ({
  storedThemes: () => host.call<StoredThemes>("GET", "/themes"),
  /** One stored theme's file. */
  themeFile: (id: string) => host.call<unknown>("GET", `/themes/${seg(id)}`),
  /** Stores file under id, replacing a theme with that ID; 400 for an ID core does not take, 422 past 64 themes. */
  storeTheme: (id: string, file: unknown) => host.call<StoredTheme>("PUT", `/themes/${seg(id)}`, file),
  /** Removes a stored theme; if it was on, core goes back to the default. */
  removeStoredTheme: (id: string) => host.call<void>("DELETE", `/themes/${seg(id)}`),
  /** Turns a theme on by ID, a built-in theme's too; an empty ID is the UI's default. */
  selectTheme: (id: string) => host.call<{ id: string }>("PUT", "/theme", { id }),
});
