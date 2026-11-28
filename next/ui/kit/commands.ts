// What the command menu offers and how it finds it, worked out without drawing anything: the
// commands (pages, accounts, actions, settings topics), the match of a typed query against them,
// the groups they are shown in, and the recents kept on this device. command.tsx draws them.

import type { Account } from "./api/accounts";
import { DESTINATIONS, type Destination, destinationHome, type PageDef } from "./destinations";
import type { IconName } from "./icons";
import { accountCondition, accountHref, accountName, conditionWord, providerName } from "./parts/account";

/** What a command is: a page to open, an account to open, something to do, or a settings topic. */
export type CommandKind = "page" | "account" | "action" | "setting";

/**
 * What an action says when it ran. done: a note to show on its row for a moment ("Copied"), then
 * the menu closes. failed: why it did not work, shown in the menu, which stays open. stay: the
 * menu stays open and nothing is said. Nothing: the menu closes at once.
 */
export type Outcome = { done: string } | { failed: string } | { stay: true } | undefined;

export interface Command {
  /** Stable across openings, so a recent can find it again: "page:<id>", "account:<id>", "action:<name>". */
  id: string;
  kind: CommandKind;
  label: string;
  /** Where it is or what it is, in a few words under or beside the name: "Activity", "Claude · Serving". */
  hint?: string;
  /** Words that find it besides its label and hint. */
  keywords?: string[];
  icon: IconName;
  /** Shown while nothing is typed, after the recents: the destinations and the actions. */
  suggested?: boolean;
  /** The keys that do it without the menu, shown at the row's right where there are any. */
  shortcut?: string[];
  /** A page or an account opens here. */
  href?: string;
  /** An action does this. */
  run?: () => Promise<Outcome> | Outcome;
}

/** The groups the menu shows, in the order they take when two are equally good a match. */
export type GroupId = "recent" | "actions" | "pages" | "accounts" | "settings";
const GROUP_OF: Record<CommandKind, GroupId> = {
  page: "pages",
  account: "accounts",
  action: "actions",
  setting: "settings",
};
const TIE_ORDER: GroupId[] = ["pages", "accounts", "actions", "settings"];

export interface Group {
  id: GroupId;
  items: Command[];
  /** Matches left out because the group shows at most MAX_PER_GROUP of them. */
  more: number;
}

/** A typed query shows at most this many matches of one group, the rest as a count. */
export const MAX_PER_GROUP = 8;
/** How many recents are kept and shown. */
export const MAX_RECENTS = 5;

// ---- the commands -----------------------------------------------------------------------------

/**
 * The pages the menu opens: every destination (its first page), and every tab and topic a
 * destination lists. A topic of Settings is a settings command; every other is a page. A page
 * reached from another page (one account, Add an account) is not listed: Add an account is an
 * action and accounts are listed by name.
 */
export function pageCommands(
  pages: PageDef[],
  icons: Record<Destination, IconName>,
  destinationName: (d: Destination) => string,
): Command[] {
  const out: Command[] = [];
  for (const dest of DESTINATIONS) {
    const home = destinationHome(pages, dest);
    if (!home) continue;
    out.push({
      id: `page:${home.id}:${dest}`,
      kind: "page",
      label: destinationName(dest),
      suggested: true,
      icon: icons[dest],
      href: `#/${home.id}`,
    });
  }
  for (const p of pages) {
    if (p.kind === undefined) continue;
    const setting = p.dest === "settings" && p.kind === "topic";
    out.push({
      id: `page:${p.id}`,
      kind: setting ? "setting" : "page",
      label: p.label ?? p.title,
      hint: destinationName(p.dest),
      keywords: p.label !== undefined && p.label !== p.title ? [p.title] : undefined,
      icon: setting ? "settings" : icons[p.dest],
      href: `#/${p.id}`,
    });
  }
  return out;
}

/**
 * The accounts as commands, by the name pages show (name, masked in privacy mode as the caller
 * passes it), with the provider's name and the account's condition under it.
 */
export function accountCommands(
  accounts: Account[],
  show: (a: Account) => string,
  now = Date.now(),
): Command[] {
  return accounts.map((a) => ({
    id: `account:${a.id}`,
    kind: "account" as const,
    label: show(a),
    hint: `${providerName(a.extension)} · ${conditionWord(accountCondition(a, undefined, now))}`,
    // The real name finds it too, as search does on every page, though only the shown one is drawn.
    keywords: [accountName(a)],
    icon: "accounts" as const,
    href: accountHref(a.id),
  }));
}

// ---- matching ---------------------------------------------------------------------------------

/** Lower case without accents, so "cafe" finds "Café". */
export function fold(text: string): string {
  return text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
}

const words = (text: string) => fold(text).split(/\s+/).filter(Boolean);

/**
 * How well a query finds a command, lower is better, or undefined when it does not. Every word
 * of the query must be found: at the start of the label (0), at the start of a word of the label
 * (1), inside the label (2), or only in its hint or keywords (3). A command ranks as its worst word.
 */
export function rank(query: string, c: Command): number | undefined {
  const label = fold(c.label);
  const labelWords = label.split(/[\s\-_/.·]+/).filter(Boolean);
  const other = [c.hint ?? "", ...(c.keywords ?? [])].map(fold);
  let worst = 0;
  for (const word of words(query)) {
    let best: number | undefined;
    if (label.startsWith(word)) best = 0;
    else if (labelWords.some((w) => w.startsWith(word))) best = 1;
    else if (label.includes(word)) best = 2;
    else if (other.some((o) => o.includes(word))) best = 3;
    if (best === undefined) return undefined;
    worst = Math.max(worst, best);
  }
  return worst;
}

/**
 * Where the query is found in text, as [start, end) ranges to mark; none when text is not the
 * same length once folded (an accent that folds to more or fewer letters) or the query is empty.
 */
export function marks(text: string, query: string): [number, number][] {
  const folded = fold(text);
  if (folded.length !== text.length) return [];
  const found: [number, number][] = [];
  for (const word of words(query)) {
    const at = folded.indexOf(word);
    if (at >= 0) found.push([at, at + word.length]);
  }
  found.sort((a, b) => a[0] - b[0]);
  const merged: [number, number][] = [];
  for (const [start, end] of found) {
    const last = merged[merged.length - 1];
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }
  return merged;
}

// ---- the groups -------------------------------------------------------------------------------

/**
 * What the menu shows for a query. With none: the recents (the commands still there), then the
 * destinations and the actions; the tabs, the settings and the accounts appear once something is
 * typed, as there may be hundreds of them. With a query: each group's matches, best first, the groups with the best match
 * first, so Enter opens what was most likely meant.
 */
export function arrange(query: string, commands: Command[], recents: string[]): Group[] {
  if (fold(query).trim() === "") {
    const byId = new Map(commands.map((c) => [c.id, c]));
    const recent = recents.flatMap((id) => byId.get(id) ?? []).slice(0, MAX_RECENTS);
    const kept = new Set(recent.map((c) => c.id));
    const groups: Group[] = [{ id: "recent", items: recent, more: 0 }];
    for (const id of ["pages", "actions"] as const) {
      groups.push({
        id,
        items: commands.filter((c) => c.suggested && GROUP_OF[c.kind] === id && !kept.has(c.id)),
        more: 0,
      });
    }
    return groups.filter((g) => g.items.length > 0);
  }
  const best = new Map<GroupId, number>();
  const found = new Map<GroupId, { c: Command; r: number; at: number }[]>();
  commands.forEach((c, at) => {
    const r = rank(query, c);
    if (r === undefined) return;
    const id = GROUP_OF[c.kind];
    const list = found.get(id) ?? [];
    list.push({ c, r, at });
    found.set(id, list);
    best.set(id, Math.min(best.get(id) ?? r, r));
  });
  return [...found.keys()]
    .sort((a, b) => (best.get(a) ?? 0) - (best.get(b) ?? 0) || TIE_ORDER.indexOf(a) - TIE_ORDER.indexOf(b))
    .map((id) => {
      const list = (found.get(id) ?? []).sort((a, b) => a.r - b.r || a.at - b.at);
      return {
        id,
        items: list.slice(0, MAX_PER_GROUP).map((x) => x.c),
        more: Math.max(0, list.length - MAX_PER_GROUP),
      };
    });
}

/** The row after or before index in a list of count rows, going round at the ends. */
export function step(index: number, by: 1 | -1, count: number): number {
  if (count <= 0) return 0;
  return (index + by + count) % count;
}

// ---- recents ----------------------------------------------------------------------------------

export const RECENTS_PREF = "command.recents";

/** The recents kept on this device: IDs, newest first. A value that is not a list of texts is none. */
export function readRecents(stored: string | null): string[] {
  if (!stored) return [];
  try {
    const value: unknown = JSON.parse(stored);
    if (!Array.isArray(value)) return [];
    return value.filter((v): v is string => typeof v === "string").slice(0, MAX_RECENTS);
  } catch {
    return [];
  }
}

/** The recents after opening id: it goes first, once. */
export function pushRecent(recents: string[], id: string): string[] {
  return [id, ...recents.filter((r) => r !== id)].slice(0, MAX_RECENTS);
}

// ---- the shortcut -----------------------------------------------------------------------------

/** Whether this is an Apple computer, where the menu's key is Command K and not Control K. */
export function isApple(nav: { platform?: string; userAgent?: string } = navigator): boolean {
  return /Mac|iPhone|iPad/i.test(nav.platform || nav.userAgent || "");
}

/** The keys that open the menu, as the Search field shows them. */
export function openKeys(apple = isApple()): string[] {
  return apple ? ["⌘", "K"] : ["Ctrl", "K"];
}

/** Whether a key press opens the menu: Command K on an Apple computer, Control K elsewhere. */
export function opensMenu(
  e: { key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean; shiftKey: boolean },
  apple = isApple(),
): boolean {
  if (e.key.toLowerCase() !== "k" || e.altKey || e.shiftKey) return false;
  return apple ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}
