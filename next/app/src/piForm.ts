// The Pi page's form logic (src/pi.tsx), kept apart so its tests need no window.

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
