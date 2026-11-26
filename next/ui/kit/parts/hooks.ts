// The shared parts' hooks: an account's name as pages show it (useNaming), the limits of the
// accounts a list shows, read a few at a time and kept a while (useWindows), and what a fix or a
// save that a page runs came to (outcome).

import { useContext, useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem, type QuotaLimit, type QuotaReply } from "../api";
import type { Reply } from "../host";
import { accountIdentities, usePrivacy } from "../privacy";
import { KitContext, useKit } from "../ui";
import { accountCondition, accountName, resetPassed } from "./account";

/**
 * An account's name, masked in privacy mode: its label (often an e-mail address) as an
 * identifier, else its ID with any e-mail address or identity in it hidden. byId names an account
 * a usage record gives by its ID or public ID; one the list does not hold shows its ID, masked
 * the same way.
 */
export function useNaming(accounts?: Account[]) {
  const p = usePrivacy();
  const of = (a: Account) => {
    const label = a.label?.trim();
    return label ? p.id(label) : p.text(accountName(a), accountIdentities(a));
  };
  return {
    of,
    byId: (id: string) => {
      const a = accounts?.find((x) => x.id === id || x.public_id === id);
      return a ? of(a) : p.text(id, accounts?.flatMap(accountIdentities));
    },
  };
}

/** What a page's fix or save gave back: a reply, an error to show, or nothing when it worked. */
export type Outcome = Reply<unknown> | string | undefined;

/**
 * The error to show for an outcome, or undefined when it worked. A reply of 401 sends the person
 * back to signing in, as useAction does, and shows nothing.
 */
export function useOutcome() {
  const kit = useContext(KitContext);
  return (result: Outcome): string | undefined => {
    if (typeof result === "string") return result || undefined;
    if (!result) return undefined;
    if (result.status === 401) {
      kit?.unauthorized();
      return undefined;
    }
    return problem(result);
  };
}

/** One account's limits as last read. */
export interface Windows {
  /** reading: no reply yet; read: limits came; none: the provider reports none; failed: the read failed. */
  state: "reading" | "read" | "none" | "failed";
  limits: QuotaLimit[];
  plan?: string;
  error?: string;
  /** When the reply was read (ms since 1970). */
  at?: number;
}

/** How long a reply is kept before the limits are read again. */
export const WINDOWS_KEEP_MS = 5 * 60_000;
/** How many accounts' limits are read at once. */
export const WINDOWS_AT_ONCE = 3;
/** How many accounts a view reads limits for by itself; the rest are read on their own page. */
export const WINDOWS_PER_VIEW = 24;
/**
 * A reply this old whose reset time has passed is read again: younger, the provider may not have
 * moved its window on yet, and reading at once would ask it again and again.
 */
export const WINDOWS_PAST_RESET_MS = 30_000;
/** How often the hook looks for replies that went out of date. */
const LOOK_EVERY_MS = 30_000;

// The replies, kept in page memory across pages and views.
const kept = new Map<string, Windows>();
const reading = new Set<string>();
const READING: Windows = { state: "reading", limits: [] };

/** Forgets every reply kept, for tests. */
export function forgetWindows() {
  kept.clear();
  reading.clear();
}

/** Limits from a reply to GET /accounts/{id}/quota, or from the copy GET /accounts carries. */
export function windowsOf(r: Reply<QuotaReply>, at: number): Windows {
  // 422: the provider reports no limits for this kind of account. That is an answer, not a failure.
  if (r.status === 422) return { state: "none", limits: [], at };
  const why = problem(r);
  if (why !== undefined) return { state: "failed", limits: [], error: why, at };
  const quota = r.body?.quota;
  const limits = (quota?.limits ?? []).filter((l) => l.id !== "reset_credits");
  const plan = quota?.plan?.trim() || undefined;
  return { state: limits.length > 0 ? "read" : "none", limits, plan, at };
}

/** Whether a kept reply is still good at now. */
export function windowsFresh(w: Windows, now: number): boolean {
  const age = now - (w.at ?? 0);
  if (age >= WINDOWS_KEEP_MS) return false;
  return !(age >= WINDOWS_PAST_RESET_MS && w.limits.some((l) => resetPassed(l, now)));
}

/**
 * The limits of the accounts that serve or wait, by account ID: an account that needs the
 * person or is turned off takes no requests, so its limits are not read. At most
 * WINDOWS_PER_VIEW accounts, WINDOWS_AT_ONCE at a time; each reply is kept WINDOWS_KEEP_MS, or
 * until a reset it names has passed. The limits GET /accounts carries (quota) show at once and
 * are read again only when out of date. An account being read again keeps showing its last reply.
 */
export function useWindows(
  accounts: Account[] | undefined,
  running?: ReadonlySet<string>,
): Map<string, Windows> {
  const { api, unauthorized } = useKit();
  const [now, setNow] = useState(() => Date.now());
  const [, bump] = useState(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    const timer = setInterval(() => setNow(Date.now()), LOOK_EVERY_MS);
    return () => {
      mounted.current = false;
      clearInterval(timer);
    };
  }, []);
  const wanted = (accounts ?? [])
    .filter((a) => {
      const group = accountCondition(a, running, now).group;
      return group === "serving" || group === "waiting";
    })
    .slice(0, WINDOWS_PER_VIEW);
  // What GET /accounts carries counts as a reply read when core read it.
  for (const a of wanted) {
    if (!a.quota) continue;
    const at = Date.parse(a.quota.fetched_at);
    const have = kept.get(a.id);
    if (!Number.isNaN(at) && (!have || (have.at ?? 0) < at))
      kept.set(a.id, windowsOf({ status: 200, body: a.quota }, at));
  }
  const ids = wanted.map((a) => a.id);
  const key = ids.join("\n");
  useEffect(() => {
    let live = true;
    const due = (key ? key.split("\n") : []).filter((id) => {
      const have = kept.get(id);
      return !reading.has(id) && !(have && windowsFresh(have, Date.now()));
    });
    let next = 0;
    const work = async () => {
      while (live && next < due.length) {
        const id = due[next++] as string;
        reading.add(id);
        let r: Reply<QuotaReply>;
        try {
          r = await api.quota(id);
        } finally {
          reading.delete(id);
        }
        if (r.status === 401) {
          live = false;
          unauthorized();
          return;
        }
        kept.set(id, windowsOf(r, Date.now()));
        if (mounted.current) bump((n) => n + 1);
      }
    };
    for (let i = 0; i < WINDOWS_AT_ONCE; i++) void work();
    return () => {
      live = false;
    };
  }, [key, now, api, unauthorized]);
  const out = new Map<string, Windows>();
  for (const id of ids) out.set(id, kept.get(id) ?? READING);
  return out;
}
