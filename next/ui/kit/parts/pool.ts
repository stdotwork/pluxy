// What one provider's accounts add up to, for Home's pool card, worked out without drawing
// anything: the limit they share, the share of it left on average over the accounts that can
// serve, one segment per account, how many serve, and what comes back next.

import type { Account } from "../api/accounts";
import type { QuotaLimit } from "../api/quota";
import { accountCondition, leftPercent, providerName, resetsIn, rowWindows, windowName } from "./account";
import type { Windows } from "./hooks";

/** One account in a pool's bar. */
export interface PoolSegment {
  id: string;
  /** left: filled to its share left; unknown: it serves, its limit is not known; out: it cannot serve now. */
  kind: "left" | "unknown" | "out";
  /** 0 to 100, for kind left. */
  left?: number;
}

export interface Pool {
  extension: string;
  provider: string;
  /** The limit the pool counts, by its whole name ("5-hour limit"); none when no account reports one. */
  window?: string;
  /** Its length in seconds. */
  windowSeconds?: number;
  /** The share left, 0 to 100, on average over the accounts that serve and report it. */
  left?: number;
  /** A money balance the serving accounts report in place of a limit (unit usd), added up. */
  balance?: number;
  segments: PoolSegment[];
  serving: number;
  /** The accounts counted: every one that is not turned off. */
  total: number;
  /** The next limit to start again among those counted: what it adds to the average, and when (ms from now). */
  back?: { gain: number; in: number };
  /** When the first account that cannot serve is back by itself (ms from now), when none serves. */
  firstBack?: number;
}

const isWindow = (l: QuotaLimit) => !l.feature && !l.models?.length && (l.window_seconds ?? 0) > 0;
const isBalance = (l: QuotaLimit) =>
  !isWindow(l) && !l.feature && l.unit?.toLowerCase() === "usd" && l.remaining !== undefined;

/**
 * The window the pool counts: the shortest each serving account shows on its row, the one most of
 * them share (the shorter on a tie). undefined when no serving account reports one.
 */
function poolWindow(serving: Account[], windows: ReadonlyMap<string, Windows>): number | undefined {
  const count = new Map<number, number>();
  for (const a of serving) {
    const w = windows.get(a.id);
    if (w?.state !== "read") continue;
    const s = rowWindows(w.limits)[0]?.window_seconds;
    if (s) count.set(s, (count.get(s) ?? 0) + 1);
  }
  let best: number | undefined;
  for (const [s, n] of count) {
    const bestN = best === undefined ? 0 : (count.get(best) ?? 0);
    if (n > bestN || (n === bestN && best !== undefined && s < best)) best = s;
  }
  return best;
}

/**
 * One provider's pool from its accounts (any order; segments keep it). Accounts turned off are not
 * counted. running is core's running extensions (accountCondition).
 */
export function poolOf(
  extension: string,
  accounts: Account[],
  windows: ReadonlyMap<string, Windows>,
  running?: ReadonlySet<string>,
  now = Date.now(),
): Pool {
  const members = accounts
    .map((a) => ({ a, c: accountCondition(a, running, now) }))
    .filter((m) => m.c.group !== "off");
  const serving = members.filter((m) => m.c.group === "serving").map((m) => m.a);
  const seconds = poolWindow(serving, windows);
  const limitOf = (a: Account): QuotaLimit | undefined => {
    const w = windows.get(a.id);
    if (w?.state !== "read" || seconds === undefined) return undefined;
    return w.limits.find((l) => isWindow(l) && l.window_seconds === seconds);
  };
  const segments: PoolSegment[] = members.map(({ a, c }) => {
    if (c.group !== "serving") return { id: a.id, kind: "out" };
    const l = limitOf(a);
    const left = l && leftPercent(l);
    return left === undefined ? { id: a.id, kind: "unknown" } : { id: a.id, kind: "left", left };
  });
  const known = segments.filter((s) => s.kind === "left").map((s) => s.left as number);
  const left = known.length > 0 ? Math.round(known.reduce((x, y) => x + y, 0) / known.length) : undefined;

  // What comes back next: the soonest reset among the counted accounts with something used.
  let back: Pool["back"];
  if (left !== undefined) {
    for (const a of serving) {
      const l = limitOf(a);
      const l0 = l && leftPercent(l);
      const at = l && resetsIn(l, now);
      if (l0 === undefined || at === undefined || l0 >= 100) continue;
      const gain = Math.round((100 - l0) / known.length);
      if (gain >= 1 && (!back || at < back.in)) back = { gain, in: at };
    }
  }

  let balance: number | undefined;
  if (seconds === undefined) {
    for (const a of serving) {
      const w = windows.get(a.id);
      const b = w?.state === "read" ? w.limits.find(isBalance) : undefined;
      if (b?.remaining !== undefined) balance = (balance ?? 0) + b.remaining;
    }
  }

  let firstBack: number | undefined;
  if (serving.length === 0) {
    for (const { c } of members) {
      if (c.until !== undefined && c.until > now)
        firstBack = Math.min(firstBack ?? Number.POSITIVE_INFINITY, c.until - now);
    }
  }

  const named = serving.map(limitOf).find((l) => l !== undefined);
  return {
    extension,
    provider: providerName(extension),
    window: named ? windowName(named) : undefined,
    windowSeconds: named?.window_seconds,
    left,
    balance,
    segments,
    serving: serving.length,
    total: members.length,
    back,
    firstBack,
  };
}

/**
 * The providers of a list in the order core lists their first account, each with its accounts;
 * accounts with no provider are left out (they have nothing to pool).
 */
export function byProvider(list: Account[]): { extension: string; accounts: Account[] }[] {
  const out = new Map<string, Account[]>();
  for (const a of list) {
    if (!a.extension) continue;
    out.set(a.extension, [...(out.get(a.extension) ?? []), a]);
  }
  return [...out].map(([extension, accounts]) => ({ extension, accounts }));
}
