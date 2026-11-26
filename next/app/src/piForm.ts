// The Pi page's form logic (src/pi.tsx), kept apart so its tests need no window.

import { hasSpaceOrControl, length } from "./textRules";

export const PACKAGE = "pluxy/pi-setup";
/** The name the launcher keeps Pi's recent folders under. */
export const CLIENT = "Pi";

/** A model ID pi-setup takes (its `settings`): non-empty, without spaces or control characters, at
 * most 240 characters. */
export const validModel = (m: string) => m.length > 0 && !hasSpaceOrControl(m) && length(m) <= 240;

/** A plugin version as pi-setup writes it: the upstream version and pluxy's patch level
 * (`1.4.25-pluxy.1`; none is patch level 0). Undefined for any other text. */
function versionOf(v: string): number[] | undefined {
  const m = /^(\d+)\.(\d+)\.(\d+)(?:-pluxy\.(\d+))?$/.exec(v);
  return m ? [m[1], m[2], m[3], m[4] ?? "0"].map(Number) : undefined;
}

/**
 * Whether the plugin copy the add-on carries (bundled) is newer than the one installed: only then
 * is Update plugin offered. The add-on's own `update` is true for any other version, older too. An
 * installed copy whose version is not known (none in its marker, or not a version pi-setup writes)
 * can be updated, as before.
 */
export function pluginNewer(installed: string | null, bundled: string): boolean {
  if (installed === null) return true;
  const a = versionOf(installed);
  const b = versionOf(bundled);
  if (!a || !b) return installed !== bundled;
  for (let i = 0; i < a.length; i++) {
    if ((b[i] as number) !== (a[i] as number)) return (b[i] as number) > (a[i] as number);
  }
  return false;
}

/** Where the person says Pi's files are: Pi's agent folder, a selected agent folder (which may be a
 * link), or a folder whose files are links (as stow makes). */
export type Where = "default" | "folder" | "links";

/** Pi's three files, in the order the tool pins them. */
export const FILES = ["cliproxyapi.json", "settings.json", "cliproxyapi-models.json"] as const;

/** The tool's preview input for where and the folder field: nothing for the default folder, the
 * folder itself, or the three files in it selected one by one. An empty folder sends empty paths,
 * which the tool refuses, never `/<file>`. */
export function selectionFor(where: Where, folder: string): Record<string, unknown> {
  const f = folder.trim();
  if (where === "folder") return { folder: f };
  if (where === "links")
    return { targets: FILES.map((n) => ({ file: f ? `${f.replace(/\/+$/, "")}/${n}` : "" })) };
  return {};
}
