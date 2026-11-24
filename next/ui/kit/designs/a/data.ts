// How direction A's screens read core: polling that keeps the last good reply on screen, a clock
// for countdowns, and the usage windows of many accounts read a few at a time and kept a while.

import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem, type QuotaReply } from "../../api";
import type { Reply } from "../../host";
import { useKit } from "../../ui";

export interface Polled<T> {
  /** The last good reply; it stays while a newer one loads or fails. */
  data?: T;
  /** Why the latest load failed, when it did. */
  error?: string;
  /** True until the first reply, good or bad: the screen shows its skeleton, not zeros. */
  loading: boolean;
  /** A load is under way. */
  busy: boolean;
  /** When the data was read (Unix ms). */
  at?: number;
  reload(): void;
}

/**
 * Loads a reply now and again every `every` ms while the window is visible (0: only on reload and
 * when key changes). key names what is loaded: a new key starts over without the old data.
 */
export function usePoll<T>(load: () => Promise<Reply<T>>, every: number, key = ""): Polled<T> {
  const { unauthorized } = useKit();
  const [state, setState] = useState<Omit<Polled<T>, "reload" | "busy">>({ loading: true });
  const [busy, setBusy] = useState(false);
  const [n, setN] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  const shownKey = useRef(key);
  useEffect(() => {
    let live = true;
    if (shownKey.current !== key) {
      shownKey.current = key;
      setState({ loading: true });
    }
    const run = () => {
      if (document.hidden) return;
      setBusy(true);
      loadRef.current().then((r) => {
        if (!live) return;
        setBusy(false);
        if (r.status === 401) return unauthorized();
        const error = problem(r);
        setState((old) =>
          error ? { ...old, error, loading: false } : { data: r.body, at: Date.now(), loading: false },
        );
      });
    };
    run();
    const timer = every > 0 ? setInterval(run, every) : undefined;
    const wake = () => {
      if (!document.hidden && every > 0) run();
    };
    document.addEventListener("visibilitychange", wake);
    return () => {
      live = false;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", wake);
    };
  }, [n, key, every, unauthorized]);
  return { ...state, busy, reload: useCallback(() => setN((x) => x + 1), []) };
}

/** The time now, again every `every` ms while the window is visible: countdowns tick with it. */
export function useNow(every = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const tick = () => {
      if (!document.hidden) setNow(Date.now());
    };
    const timer = setInterval(tick, every);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", tick);
    };
  }, [every]);
  return now;
}

/** An account's usage windows as last read, or why there are none. */
export interface Windows {
  reply?: QuotaReply;
  /** The provider has no usage windows to report for this kind of account. */
  unsupported?: boolean;
  error?: string;
  /** When it was read (Unix ms). */
  at: number;
}

// Kept for the page's life, so moving between screens does not ask every provider again.
const kept = new Map<string, Windows>();
const KEEP_MS = 5 * 60_000;
const AT_ONCE = 3;

/**
 * The usage windows of the accounts that can have them (not off, not unreadable), by account ID.
 * Core asks the provider for each, so they are read three at a time and kept five minutes;
 * refresh() reads them all again, or only the IDs given.
 */
export function useWindows(accounts: Account[] | undefined): {
  windows: ReadonlyMap<string, Windows>;
  pending: ReadonlySet<string>;
  refresh(ids?: string[]): void;
} {
  const { api } = useKit();
  const [, bump] = useState(0);
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const [force, setForce] = useState<{ n: number; ids?: string[] }>({ n: 0 });
  const wanted = (accounts ?? [])
    .filter((a) => !a.disabled && a.health?.state !== "broken" && a.extension)
    .map((a) => a.id);
  const wantedKey = wanted.join("\n");
  useEffect(() => {
    let live = true;
    const now = Date.now();
    const forced = force.ids ? new Set(force.ids) : undefined;
    const queue = wanted.filter((id) => {
      const have = kept.get(id);
      if (force.n > 0 && (!forced || forced.has(id))) return true;
      return !have || now - have.at > KEEP_MS;
    });
    if (queue.length === 0) return;
    setPending(new Set(queue));
    let cursor = 0;
    const worker = async () => {
      while (live && cursor < queue.length) {
        const id = queue[cursor++] as string;
        const r = await api.quota(id);
        if (!live) return;
        const at = Date.now();
        if (r.status === 200 && r.body?.quota) kept.set(id, { reply: r.body, at });
        else if (r.status === 422 || (r.status === 200 && !r.body?.quota))
          kept.set(id, { unsupported: true, at });
        else kept.set(id, { error: problem(r) ?? "no reply", at, reply: kept.get(id)?.reply });
        setPending((old) => {
          const next = new Set(old);
          next.delete(id);
          return next;
        });
        bump((x) => x + 1);
      }
    };
    void Promise.all(Array.from({ length: Math.min(AT_ONCE, queue.length) }, worker));
    return () => {
      live = false;
    };
  }, [wantedKey, force, api]);
  // Once a forced read has been taken up, later runs go back to the keep time.
  useEffect(() => {
    if (force.n > 0) setForce((f) => (f.n > 0 ? { n: 0 } : f));
  }, [force]);
  return {
    windows: kept,
    pending,
    refresh: useCallback((ids?: string[]) => setForce((f) => ({ n: f.n + 1, ids })), []),
  };
}

/** Forgets every kept window: a test's or a sign-out's clean start. */
export function forgetWindows() {
  kept.clear();
}
