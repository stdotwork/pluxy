// Direction C's data: what the shell reads once and every page shares (the accounts, core's
// status, the client keys), the flow of requests from key to model to account that the map draws,
// and each account's usage windows. Everything comes from the management API core already has.

import { type ComponentChildren, createContext } from "preact";
import { useCallback, useContext, useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  type Account,
  type Chain,
  type ClientKey,
  problem,
  type QuotaReply,
  runningExtensions,
  type Status,
  type UsageSummary,
} from "../../api";
import type { Reply } from "../../host";
import { accountIdentities, type Masker, usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { type Condition, conditionOf, type Filters, filterQuery, Providers, type Range } from "./lib";

// ---- asking core ---------------------------------------------------------------------------

/** Thrown when core refused the key: the shell is already going back to signing in. */
class SignedOut extends Error {}

/** Turns a reply into its body, or throws its problem as one line for people. */
export function useAsk() {
  const { unauthorized } = useKit();
  return useCallback(
    async <T,>(call: Promise<Reply<T>>): Promise<T> => {
      const r = await call;
      if (r.status === 401) {
        unauthorized();
        throw new SignedOut("signed out");
      }
      const why = problem(r);
      if (why) throw new Error(why);
      return r.body as T;
    },
    [unauthorized],
  );
}

export interface Resource<T> {
  data?: T;
  /** Why the last read failed; data, when there, is what an earlier read returned. */
  error?: string;
  /** True until the first read of this key ends, and while a new key is read. */
  loading: boolean;
  /** When data was read, in milliseconds. */
  at?: number;
  reload(): void;
}

/**
 * Reads something from core and keeps what it read while it reads again: a new key (another
 * range or filter) or a refresh never empties the page. With poll, it reads again that often
 * while the window is visible.
 */
export function useResource<T>(key: string, load: () => Promise<T>, poll?: number): Resource<T> {
  const [state, setState] = useState<{ data?: T; error?: string; key?: string; at?: number }>({});
  const [n, setN] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    let live = true;
    loadRef.current().then(
      (data) => live && setState({ data, key, at: Date.now() }),
      (e: unknown) => {
        if (!live || e instanceof SignedOut) return;
        const error = e instanceof Error ? e.message : String(e);
        setState((s) => ({ ...s, error, key }));
      },
    );
    return () => {
      live = false;
    };
  }, [key, n]);
  const reload = useCallback(() => setN((x) => x + 1), []);
  useEffect(() => {
    if (!poll) return;
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") reload();
    }, poll);
    return () => clearInterval(timer);
  }, [poll, reload]);
  return { data: state.data, error: state.error, loading: state.key !== key, at: state.at, reload };
}

/** The time now, read again every interval milliseconds, for countdowns. */
export function useNow(interval = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), interval);
    return () => clearInterval(timer);
  }, [interval]);
  return now;
}

// ---- the fleet: what every page shares -------------------------------------------------------

export const REFRESH_MS = 60_000;

export interface Fleet {
  accounts?: Account[];
  status?: Status;
  keys?: ClientKey[];
  chains?: Chain[];
  /** The extensions core runs, when its status is read. */
  running?: ReadonlySet<string>;
  providers: Providers;
  /** Why the accounts could not be read. */
  error?: string;
  loading: boolean;
  at?: number;
  reload(): void;
  /** Hides what names an account's owner while privacy mode is on. */
  mask: Masker;
  /** An account by its ID or its public ID (a request's tries name it by the public one). */
  account(id: string | undefined): Account | undefined;
  /** What a client key is called on screen: its masked value. */
  keyName(id: string): string;
}

const FleetContext = createContext<Fleet | null>(null);

export function useFleet(): Fleet {
  const fleet = useContext(FleetContext);
  if (!fleet) throw new Error("useFleet outside direction C's shell");
  return fleet;
}

interface FleetData {
  accounts: Account[];
  status?: Status;
  keys?: ClientKey[];
  chains?: Chain[];
}

export function FleetProvider(props: { children: ComponentChildren }) {
  const { api } = useKit();
  const ask = useAsk();
  const privacy = usePrivacy();
  const res = useResource<FleetData>(
    "fleet",
    async () => {
      // The accounts are the read that matters: the rest fills in around them when it arrives.
      const [accounts, status, keys, chains] = await Promise.all([
        ask(api.accounts()),
        ask(api.status()).catch(() => undefined),
        ask(api.clientKeys()).catch(() => undefined),
        ask(api.chains()).catch(() => undefined),
      ]);
      return { accounts: accounts.accounts, status, keys: keys?.keys, chains: chains?.chains };
    },
    REFRESH_MS,
  );
  const fleet = useMemo<Fleet>(() => {
    const d = res.data;
    const accounts = d?.accounts;
    const byId = new Map<string, Account>();
    for (const a of accounts ?? []) {
      byId.set(a.id, a);
      if (a.public_id) byId.set(a.public_id, a);
    }
    const keyNames = new Map((d?.keys ?? []).map((k) => [k.id, k.masked]));
    return {
      accounts,
      status: d?.status,
      keys: d?.keys,
      chains: d?.chains,
      running: runningExtensions(d?.status),
      providers: new Providers(
        (accounts ?? []).map((a) => a.extension ?? ""),
        (d?.status?.extensions ?? []).map((e) => e.id),
      ),
      error: res.error,
      loading: res.loading,
      at: res.at,
      reload: res.reload,
      mask: privacy.masker(accounts ? accounts.flatMap(accountIdentities) : undefined),
      account: (id) => (id ? byId.get(id) : undefined),
      keyName: (id) => keyNames.get(id) ?? (id ? `${id.slice(0, 6)}…` : "No key"),
    };
  }, [res.data, res.error, res.loading, res.at, res.reload, privacy]);
  return <FleetContext.Provider value={fleet}>{props.children}</FleetContext.Provider>;
}

/** Each account with its condition, for the pages that list them. */
export function useConditions(fleet: Fleet, now: number): Map<string, Condition> {
  // To the second: a cooldown ends between two refreshes, and its account then reads Available.
  const second = Math.floor(now / 1000);
  return useMemo(() => {
    const out = new Map<string, Condition>();
    for (const a of fleet.accounts ?? []) out.set(a.id, conditionOf(a, fleet.running, now));
    return out;
  }, [fleet.accounts, fleet.running, second]);
}

// ---- the flow of requests ------------------------------------------------------------------

/** Requests that went from one node of the map to the next. */
export interface Link {
  from: string;
  to: string;
  requests: number;
  failed: number;
}

export interface Flow {
  summary: UsageSummary;
  /** Client key to model, and model to account. */
  keyModel: Link[];
  modelAccount: Link[];
  /** Reads that failed or were left out, in words: the map is then not whole. */
  gaps: string[];
}

/** How many keys and models the links are read for; the rest are drawn without links. */
const MAX_KEYS = 8;
export const MAX_MODELS = 14;

/** Runs the jobs a few at a time. */
async function pooled<T>(jobs: (() => Promise<T>)[], width = 6): Promise<T[]> {
  const out: T[] = new Array(jobs.length);
  let next = 0;
  const worker = async () => {
    while (next < jobs.length) {
      const i = next++;
      out[i] = await (jobs[i] as () => Promise<T>)();
    }
  };
  await Promise.all(Array.from({ length: Math.min(width, jobs.length) }, worker));
  return out;
}

/**
 * Reads the flow for a range and filters. Core adds up requests by key, by model and by account,
 * but not by pairs of them, so a link's weight is read by asking again for one key (its models)
 * and for one model (its accounts): a handful of small reads whatever the number of requests.
 */
export function useFlow(range: Range, filters: Filters, enabled = true): Resource<Flow | undefined> {
  const { api } = useKit();
  const ask = useAsk();
  const q = filterQuery(filters);
  return useResource<Flow | undefined>(
    enabled ? `${range.id}${q}` : "off",
    async () => {
      if (!enabled) return undefined;
      const base = `since=${range.id}`;
      const summary = await ask(api.usage(`${base}&${range.series}${q}`));
      const gaps: string[] = [];
      const keys = summary.keys.filter((k) => k.name).slice(0, MAX_KEYS);
      const models = summary.models.slice(0, MAX_MODELS);
      if (summary.keys.length > MAX_KEYS) gaps.push(`links are read for the ${MAX_KEYS} busiest client keys`);
      if (summary.models.length > MAX_MODELS)
        gaps.push(`links are read for the ${MAX_MODELS} busiest models`);
      const without = (name: string) => filterQuery({ ...filters, [name]: undefined });
      const keyReads = keys.map((k) => async (): Promise<Link[]> => {
        if (filters.key && filters.key !== k.name) return [];
        try {
          const s = await ask(
            api.usage(`${base}&buckets=1${without("key")}&key=${encodeURIComponent(k.name)}`),
          );
          return s.models.map((m) => ({
            from: k.name,
            to: m.name,
            requests: m.total_requests,
            failed: m.failure_count,
          }));
        } catch (e) {
          if (e instanceof SignedOut) throw e;
          gaps.push("some links from client keys could not be read");
          return [];
        }
      });
      const modelReads = models.map((m) => async (): Promise<Link[]> => {
        if (filters.model && filters.model !== m.name) return [];
        try {
          const s = await ask(
            api.usage(`${base}&buckets=1${without("model")}&model=${encodeURIComponent(m.name)}`),
          );
          return s.accounts.map((a) => ({
            from: m.name,
            to: a.name,
            requests: a.total_requests,
            failed: a.failure_count,
          }));
        } catch (e) {
          if (e instanceof SignedOut) throw e;
          gaps.push("some links to accounts could not be read");
          return [];
        }
      });
      const links = await pooled([...keyReads, ...modelReads]);
      return {
        summary,
        keyModel: links.slice(0, keyReads.length).flat(),
        modelAccount: links.slice(keyReads.length).flat(),
        gaps: [...new Set(gaps)],
      };
    },
    REFRESH_MS,
  );
}

// ---- usage windows -------------------------------------------------------------------------

export type Windows =
  | { state: "loading" }
  | { state: "none"; why?: string }
  | { state: "ok"; reply: QuotaReply };

const QUOTA_TTL_MS = 5 * 60_000;
const quotaCache = new Map<string, { at: number; value: Windows }>();
const quotaListeners = new Set<() => void>();
const quotaQueue: string[] = [];
let quotaRunning = 0;

/** Forgets what was read for an account (or all), so the next look reads it again. */
export function forgetWindows(id?: string) {
  if (id) quotaCache.delete(id);
  else quotaCache.clear();
  for (const fn of quotaListeners) fn();
}

/**
 * The usage windows of the accounts named, read from core three at a time and kept for five
 * minutes. Core asks the account's provider for them, so they are read only for the accounts a
 * page shows, and only for accounts requests can reach.
 */
export function useWindows(ids: string[]): Map<string, Windows> {
  const { api } = useKit();
  const [, bump] = useState(0);
  useEffect(() => {
    const fn = () => bump((n) => n + 1);
    quotaListeners.add(fn);
    return () => {
      quotaListeners.delete(fn);
    };
  }, []);
  const wanted = ids.join("\n");
  useEffect(() => {
    const pump = () => {
      while (quotaRunning < 3 && quotaQueue.length > 0) {
        const id = quotaQueue.shift() as string;
        quotaRunning++;
        api.quota(id).then((r) => {
          quotaRunning--;
          const value: Windows =
            r.status >= 200 && r.status < 300 && r.body?.quota
              ? { state: "ok", reply: r.body }
              : { state: "none", why: r.status === 422 ? undefined : problem(r) };
          quotaCache.set(id, { at: Date.now(), value });
          for (const fn of quotaListeners) fn();
          pump();
        });
      }
    };
    for (const id of ids) {
      const kept = quotaCache.get(id);
      if (kept && Date.now() - kept.at < QUOTA_TTL_MS) continue;
      if (kept?.value.state === "loading" || quotaQueue.includes(id)) continue;
      quotaCache.set(id, { at: Date.now(), value: { state: "loading" } });
      quotaQueue.push(id);
    }
    pump();
  }, [wanted, api]);
  const out = new Map<string, Windows>();
  for (const id of ids) out.set(id, quotaCache.get(id)?.value ?? { state: "loading" });
  return out;
}
