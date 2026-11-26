// Routes what the native side passes on from page webviews to the page that opened each webview.
// A webview can say hello before the window has its label (page_open answers after the webview
// started loading), so messages for a label not claimed yet wait for it, within limits.

export interface PageMessage {
  label: string;
  generation: string;
  type: "hello" | "call" | "closed";
  data?: unknown;
}

type Listener = (m: PageMessage) => void;

/** The most labels with waiting messages, and the most messages waiting for one label. */
export const MAX_LABELS = 16;
export const MAX_WAITING = 16;

export class PageRouter {
  private listeners = new Map<string, Listener>();
  private waiting = new Map<string, PageMessage[]>();

  deliver(m: PageMessage) {
    const fn = this.listeners.get(m.label);
    if (fn) {
      fn(m);
      return;
    }
    const list = this.waiting.get(m.label);
    if (list) {
      if (list.length < MAX_WAITING) list.push(m);
    } else if (this.waiting.size < MAX_LABELS) {
      this.waiting.set(m.label, [m]);
    }
  }

  /** Sends label's messages to fn, first those that waited. Returns the way to stop. */
  claim(label: string, fn: Listener): () => void {
    this.listeners.set(label, fn);
    const early = this.waiting.get(label) ?? [];
    this.waiting.delete(label);
    for (const m of early) fn(m);
    return () => this.drop(label);
  }

  /** Forgets label: its webview closed. */
  drop(label: string) {
    this.listeners.delete(label);
    this.waiting.delete(label);
  }
}
