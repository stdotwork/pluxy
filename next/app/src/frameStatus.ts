// What the frame's status block says in the desktop app: where pluxy runs and whether the app
// reaches it, read from the app's connection. The word is Running only for the app's own pluxy,
// which the app watches; for a pluxy it only connects to, the word is Connected, because the app
// does not check that one answers (a page that cannot reach it says so itself).

import type { FrameStatus } from "../../ui/kit/frame";
import { noAnswer, WHERE } from "./connectionCopy";
import { type Connection, coreRestart } from "./host";

/** "this computer :8317" for a pluxy on this computer, the other computer's address for a remote one. */
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

/**
 * The status block for a connection. onChange gets the connection a Restart from its menu ends in
 * (also a failed one, which carries its error); a Restart the native side refuses throws, and the
 * menu shows why.
 */
export function connectionStatus(c: Connection, onChange: (c: Connection) => void = () => {}): FrameStatus {
  const at = where(c);
  const open = "#/connection";
  if (c.target && c.signed_in) {
    const menu: FrameStatus["menu"] = [{ kind: "address", value: c.target }];
    if (c.mode === "managed")
      menu.push({
        kind: "action",
        label: "Restart pluxy",
        async run() {
          onChange(await coreRestart());
        },
      });
    menu.push({ kind: "link", label: WHERE.title, href: open });
    return c.mode === "managed"
      ? { state: "running", word: "Running", where: at, menu }
      : { state: "unknown", word: "Connected", where: at, menu };
  }
  // Not running: a click opens Where pluxy runs.
  if (c.target)
    return noAnswer(c.error)
      ? { state: "down", word: "Not answering", where: at, href: open }
      : { state: "unknown", word: "Not signed in", where: at, href: open };
  if (!c.mode) return { state: "unknown", word: "Not connected", where: at };
  if (c.mode === "managed") {
    const core = c.core?.state;
    if (core === "missing") return { state: "stopped", word: "Not installed", where: at, href: open };
    if (core === "recovering") return { state: "down", word: "Restarting", where: at, href: open };
    if (core === "running") return { state: "down", word: "Not answering", where: at, href: open };
    return { state: "stopped", word: "Stopped", where: at, href: open };
  }
  return { state: "down", word: "Not connected", where: at, href: open };
}
