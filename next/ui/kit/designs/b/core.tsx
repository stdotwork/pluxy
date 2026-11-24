// What every screen of direction B knows about core without asking again: the accounts, core's
// status, each account's condition and the verdict they add up to. The shell reads them once a
// minute and the screens share the reading, so the wordmark's full stop, the tab's icon and the
// first screen's sentence always agree.

import { createContext } from "preact";
import { useCallback, useContext, useMemo } from "preact/hooks";
import { type Account, runningExtensions, type Status } from "../../api";
import { useKit } from "../../ui";
import { type Naming, type Read, useNaming, useNow, useRead } from "./data";
import {
  type Condition,
  type Counts,
  condition,
  countGroups,
  type Tone,
  type Verdict,
  verdictOf,
} from "./lib";

export interface Core {
  accounts: Read<{ accounts: Account[] }>;
  status: Read<Status>;
  /** The accounts, once read. */
  list: Account[] | undefined;
  running: ReadonlySet<string> | undefined;
  now: number;
  conditionOf(a: Account): Condition;
  counts: Counts | undefined;
  /** The verdict on the accounts, once they are read. */
  verdict: Verdict | undefined;
  /** What the wordmark's full stop shows. */
  tone: Tone;
  naming: Naming;
  /** Reads the accounts and the status again, after something changed. */
  reload(): void;
}

const CoreContext = createContext<Core | null>(null);

export const CoreProvider = CoreContext.Provider;

export function useCore(): Core {
  const core = useContext(CoreContext);
  if (!core) throw new Error("useCore outside direction B's shell");
  return core;
}

const EVERY_MS = 60_000;

/** Reads what the screens share. Called once, by the shell. */
export function useCoreState(): Core {
  const { api } = useKit();
  const accounts = useRead(api.accounts, EVERY_MS);
  const status = useRead(api.status, EVERY_MS);
  const now = useNow();
  const list = accounts.data?.accounts;
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const conditions = useMemo(() => {
    const by = new Map<string, Condition>();
    for (const a of list ?? []) by.set(a.id, condition(a, running, now));
    return by;
  }, [list, running, now]);
  const counts = useMemo(
    () => (list ? countGroups([...conditions.values()]) : undefined),
    [list, conditions],
  );
  const verdict = useMemo(
    () => (counts ? verdictOf(counts, status.data?.safe_mode) : undefined),
    [counts, status.data?.safe_mode],
  );
  const naming = useNaming(list);
  const reloadAccounts = accounts.reload;
  const reloadStatus = status.reload;
  const reload = useCallback(() => {
    reloadAccounts();
    reloadStatus();
  }, [reloadAccounts, reloadStatus]);
  // A reading that failed says nothing about the accounts: the full stop goes back to ink.
  const tone: Tone = accounts.error
    ? list
      ? (verdict?.tone ?? "unknown")
      : "needs"
    : (verdict?.tone ?? "unknown");
  return {
    accounts,
    status,
    list,
    running,
    now,
    conditionOf: (a) => conditions.get(a.id) ?? condition(a, running, now),
    counts,
    verdict,
    tone,
    naming,
    reload,
  };
}

/** The address of an account's own page. */
export const accountHref = (id: string, part?: string) =>
  `#/accounts/${encodeURIComponent(id)}${part ? `/${part}` : ""}`;
