// How direction B's screens read core: replies that stay on screen while a newer one loads or
// fails, a slow clock for "resets in", and the accounts' usage windows read a few at a time and
// kept for a while, because each reading asks the account's provider.

import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type Account, problem, type QuotaLimit, type QuotaReply } from "../../api";
import type { Reply } from "../../host";
import { accountIdentities, type Masker, usePrivacy } from "../../privacy";
import { useKit } from "../../ui";

export interface Read<T> {
  /** The last good reply; it stays while a newer one loads or fails. */
  data?: T;
  /** Why the latest load failed, when it did. */
  error?: string;
  /** True until the first reply, good or bad: the screen says it is asking, never "0 accounts". */
  loading: boolean;
  /** A load is under way. */
  busy: boolean;
  /** When the data was read (Unix ms). */
  at?: number;
  reload(): void;
}

/**
 * Reads a reply now, again every `every` ms while the window shows (0: only on reload), and when
 * key changes. A new key starts over without the old data.
 */
export function useRead<T>(load: () => Promise<Reply<T>>, every = 0, key = ""): Read<T> {
  const { unauthorized } = useKit();
  const [state, setState] = useState<{ data?: T; error?: string; loading: boolean; at?: number }>({
    loading: true,
  });
  const [busy, setBusy] = useState(false);
  const [n, setN] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  const shown = useRef(key);
  useEffect(() => {
    let live = true;
    if (shown.current !== key) {
      shown.current = key;
      setState({ loading: true });
    }
    const run = () => {
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
    const timer = every > 0 ? setInterval(() => !document.hidden && run(), every) : undefined;
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [key, every, n, unauthorized]);
  const reload = useCallback(() => setN((x) => x + 1), []);
  return { ...state, busy, reload };
}

/** The time now, read again every `every` ms while the window shows: what countdowns are counted from. */
export function useNow(every = 20_000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const tick = () => !document.hidden && setNow(Date.now());
    const timer = setInterval(tick, every);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", tick);
    };
  }, [every]);
  return now;
}

/** One account's usage windows as last read. */
export interface Windows {
  state: "reading" | "read" | "none" | "failed";
  limits: QuotaLimit[];
  plan?: string;
  /** When the provider was asked (Unix ms). */
  at?: number;
  error?: string;
}

// Kept for the life of the page, across screens: going from the first screen to Accounts asks no
// provider again. A reading older than this is read again when a screen next shows it.
const FRESH_MS = 5 * 60_000;
// How many accounts a screen reads windows for by itself; the rest are read on their own page.
export const WINDOWS_AT_ONCE = 24;
const kept = new Map<string, Windows>();

const READING: Windows = { state: "reading", limits: [] };

function windowsOf(r: Reply<QuotaReply>): Windows {
  const error = problem(r);
  // 422: the provider reports no quota for this kind of account. That is an answer, not a failure.
  if (r.status === 422) return { state: "none", limits: [], at: Date.now() };
  if (error || !r.body) return { state: "failed", limits: [], at: Date.now(), error };
  const limits = (r.body.quota?.limits ?? []).filter((l) => l.id !== "reset_credits");
  return {
    state: limits.length > 0 ? "read" : "none",
    limits,
    plan: r.body.quota?.plan,
    at: Date.parse(r.body.fetched_at) || Date.now(),
  };
}

/**
 * The usage windows of the given accounts, by account ID: three are read at a time, and a reading
 * is kept for five minutes. An account that is turned off or needs a sign-in is not asked (core
 * would refuse). refresh() reads them all again.
 */
export function useWindows(accounts: Account[] | undefined): {
  windows: Map<string, Windows>;
  refresh(): void;
  reading: boolean;
} {
  const { api } = useKit();
  const [, bump] = useState(0);
  const [round, setRound] = useState(0);
  const ids = useMemo(
    () =>
      (accounts ?? [])
        .filter((a) => !a.disabled && a.state === "active" && a.health?.state !== "broken")
        .map((a) => a.id),
    [accounts],
  );
  const key = ids.join("\n");
  const forced = useRef(0);
  useEffect(() => {
    let live = true;
    const force = forced.current !== round;
    forced.current = round;
    const now = Date.now();
    const todo = ids.filter((id) => {
      const have = kept.get(id);
      return force || !have || (have.state !== "reading" && now - (have.at ?? 0) > FRESH_MS);
    });
    for (const id of todo) if (!kept.has(id)) kept.set(id, READING);
    let next = 0;
    const worker = async () => {
      while (live && next < todo.length) {
        const id = todo[next++];
        if (id === undefined) break;
        const r = await api.quota(id);
        kept.set(id, windowsOf(r));
        if (live) bump((x) => x + 1);
      }
    };
    void Promise.all([worker(), worker(), worker()]);
    return () => {
      live = false;
      // A reading cut short is asked for again by the next screen.
      for (const id of todo) if (kept.get(id) === READING) kept.delete(id);
    };
  }, [key, round, api]);
  const windows = new Map<string, Windows>();
  for (const id of ids) {
    const have = kept.get(id);
    if (have) windows.set(id, have);
  }
  return {
    windows,
    refresh: useCallback(() => setRound((x) => x + 1), []),
    reading: ids.some((id) => kept.get(id)?.state === "reading"),
  };
}

/** How an account is shown and found: its ID as the person named it, masked in privacy mode. */
export interface Naming {
  /** What to call the account an ID (or a public ID from a usage record) names. */
  name(account: string): string;
  /** The account an ID or public ID names, when core lists it. */
  find(account: string): Account | undefined;
  /** Masks free text that may name accounts. */
  mask: Masker;
}

export function useNaming(accounts: Account[] | undefined): Naming {
  const p = usePrivacy();
  return useMemo(() => {
    const by = new Map<string, Account>();
    for (const a of accounts ?? []) {
      by.set(a.id, a);
      if (a.public_id) by.set(a.public_id, a);
    }
    // Without the accounts, privacy mode hides a text whole rather than show a name in it.
    const mask = p.masker(accounts ? accounts.flatMap(accountIdentities) : undefined);
    return {
      find: (account) => by.get(account),
      name: (account) => {
        const a = by.get(account);
        return a ? p.text(a.id, accountIdentities(a)) : mask(account);
      },
      mask,
    };
  }, [accounts, p]);
}
