// Saving a file a page made (a theme file, a log, a CSV). In a browser it is a download. A host
// whose window has no downloads (the desktop app) saves it itself and says where.

import { useState } from "preact/hooks";
import type { Host } from "./host";
import { usePrivacy } from "./privacy";
import { strings } from "./strings";
import { Notice, useKit } from "./ui";

const t = strings("save", {
  saved: "Saved to {path}",
  notSaved: "Not saved: {error}",
});

/**
 * Saves text as a file called name, of the media type given. Returns where the host saved it, or
 * undefined after a browser download, which the browser shows itself. Throws when the host could
 * not save it.
 */
export async function saveFile(
  host: Pick<Host, "saveFile">,
  name: string,
  text: string,
  type: string,
): Promise<string | undefined> {
  if (host.saveFile) return host.saveFile(name, text);
  const url = URL.createObjectURL(new Blob([text], { type }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  // Allow the browser to start consuming the Blob before releasing it.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return undefined;
}

/**
 * A saved file's path as the page shows it. In privacy mode only the file and its folder are named:
 * the folders above them usually hold the person's user name.
 */
export function shownPath(path: string, privacy: boolean): string {
  if (!privacy) return path;
  const parts = path.split(/[\\/]/).filter(Boolean);
  if (parts.length <= 2) return path;
  const separator = path.includes("\\") && !path.includes("/") ? "\\" : "/";
  return ["…", ...parts.slice(-2)].join(separator);
}

/**
 * A page's saves: save() saves a file, and notice says where the host put the last one or why it
 * could not, until the next save or clear().
 */
export function useSave() {
  const { host } = useKit();
  const privacy = usePrivacy();
  const [result, setResult] = useState<{ path: string } | { error: string }>();
  return {
    async save(name: string, text: string, type: string) {
      setResult(undefined);
      try {
        const path = await saveFile(host, name, text, type);
        if (path !== undefined) setResult({ path });
      } catch (e) {
        setResult({ error: e instanceof Error ? e.message : String(e) });
      }
    },
    clear: () => setResult(undefined),
    notice:
      result &&
      ("path" in result ? (
        <Notice tone="ok">{t("saved", { path: shownPath(result.path, privacy.on) })}</Notice>
      ) : (
        <Notice tone="danger">{t("notSaved", { error: result.error })}</Notice>
      )),
  };
}
