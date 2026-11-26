// What the frame's status block says in the desktop app: where pluxy runs and whether the app
// reaches it, read from the app's connection.

import type { FrameStatus } from "../../ui/kit/frame";
import type { Connection } from "./host";

/** "this computer :8317" for a core on this computer, the other machine's address for a remote one. */
function where(c: Connection): string {
  const address = c.target ?? c.url;
  let url: URL | undefined;
  try {
    url = address ? new URL(address) : undefined;
  } catch {
    url = undefined;
  }
  if (c.mode === "remote") return url?.host ?? "another computer";
  if (!c.mode) return "not set up";
  return url?.port ? `this computer :${url.port}` : "this computer";
}

export function connectionStatus(c: Connection): FrameStatus {
  const at = where(c);
  if (c.target && c.signed_in) {
    return {
      state: "running",
      word: "Running",
      where: at,
      menu: [
        { kind: "address", value: c.target },
        { kind: "link", label: "Connection", href: "#/connection" },
      ],
    };
  }
  if (c.target) return { state: "unknown", word: "Not signed in", where: at };
  if (!c.mode) return { state: "unknown", word: "Not connected", where: at };
  if (c.mode === "managed") {
    const core = c.core?.state;
    if (core === "missing") return { state: "stopped", word: "Not installed", where: at };
    if (core === "recovering") return { state: "down", word: "Restarting", where: at };
    if (core === "running") return { state: "down", word: "Not answering", where: at };
    return { state: "stopped", word: "Stopped", where: at };
  }
  return { state: "down", word: "Not answering", where: at };
}
