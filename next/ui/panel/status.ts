// What the frame's status block says in the panel. The panel knows two things: the address it
// was served from, which is pluxy's, and whether its last call to pluxy was answered.

import type { FrameStatus } from "../kit/frame";
import type { Host } from "../kit/host";

/** Whether pluxy answered the panel's last call; unknown before the first. */
export type Reach = "unknown" | "running" | "down";

/** How often the panel asks again while its window shows and no page is asking. */
const ASK_EVERY_MS = 15_000;

/** Tells every watcher what the host's calls find, starting with the last one known. */
export class ReachWatch {
  private last: Reach = "unknown";
  private readonly watchers = new Set<(reach: Reach) => void>();

  /** For the host: called with whether a request got any reply. */
  readonly heard = (answered: boolean) => {
    const next: Reach = answered ? "running" : "down";
    if (next === this.last) return;
    this.last = next;
    for (const tell of this.watchers) tell(next);
  };

  /**
   * Calls tell now and on each change. While signed in it also asks pluxy for its status from
   * time to time, so a pluxy that stops while the page sits still is noticed. The function it
   * returns ends the watch.
   */
  watch(host: Host, tell: (reach: Reach) => void): () => void {
    this.watchers.add(tell);
    tell(this.last);
    const timer = setInterval(() => {
      if (!document.hidden && host.signedIn()) void host.call("GET", "/status");
    }, ASK_EVERY_MS);
    return () => {
      clearInterval(timer);
      this.watchers.delete(tell);
    };
  }
}

const WORDS: Record<Reach, string> = { unknown: "Checking", running: "Running", down: "Not answering" };

export function panelStatus(reach: Reach, at: { host: string; origin: string }): FrameStatus {
  return {
    state: reach,
    word: WORDS[reach],
    where: at.host,
    menu: [{ kind: "address", value: at.origin }],
  };
}
