// Which agent client pages the window lists (clientList.tsx), apart from the page so its unit tests
// need no webview. The person's saved choice is a list of client page IDs, or none, which is
// automatic: the clients whose optional tool is installed on this device. Either way the client
// page the person is on stays listed, so the page they are looking at never leaves the navigation.

import type { DeviceCatalog } from "./device";

/** The host pref that keeps the saved choice (the host prefixes it). */
export const PREF = "agent-clients";

/** What the list needs to know of a client page: its ID, its tool's package and its title. */
export interface Client {
  id: string;
  package: string;
  title: string;
}

/** The IDs of the clients whose tool is installed on this device, enabled or not: not one a registry
 * only offers, which Beginner Mode is for. */
export function detected(clients: Client[], catalog?: DeviceCatalog): Set<string> {
  const have = new Set(catalog?.packages.map((p) => p.id));
  return new Set(clients.filter((c) => have.has(c.package)).map((c) => c.id));
}

/** The saved choice as the pref holds it: the known IDs in it, or undefined (automatic) when the
 * pref is missing, damaged or names none this window knows. */
export function parse(raw: string | null, clients: Client[]): string[] | undefined {
  if (raw === null) return undefined;
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (!Array.isArray(value)) return undefined;
  const known = clients.filter((c) => value.includes(c.id)).map((c) => c.id);
  return known.length > 0 ? known : undefined;
}

/** The pref's value for a choice: null (the pref is removed) for automatic. */
export const encode = (saved: string[] | undefined): string | null =>
  saved === undefined ? null : JSON.stringify(saved);

/** Installed clients first; within each group the window's order stays. */
export function ordered(clients: Client[], installed: Set<string>): Client[] {
  return [...clients].sort((a, b) => Number(installed.has(b.id)) - Number(installed.has(a.id)));
}

/** The automatic choice: the installed clients. */
export const automatic = (clients: Client[], installed: Set<string>): string[] =>
  ordered(clients, installed)
    .filter((c) => installed.has(c.id))
    .map((c) => c.id);

/** The clients the navigation lists, in its order: the choice (saved, else automatic), and the one
 * the person is on, if it is a client. */
export function listed(
  clients: Client[],
  installed: Set<string>,
  saved: string[] | undefined,
  current?: string,
): Client[] {
  const chosen = saved ?? automatic(clients, installed);
  return ordered(clients, installed).filter((c) => chosen.includes(c.id) || c.id === current);
}

/** The clients whose title holds the search text, in the list's order. */
export function matching(clients: Client[], installed: Set<string>, query: string): Client[] {
  const q = query.trim().toLowerCase();
  return ordered(clients, installed).filter((c) => c.title.toLowerCase().includes(q));
}

/** Whether a choice may be saved: at least one client stays visible. Automatic (undefined) is not
 * refused: it can show none, when no tool is installed, and the Manage page is still there. */
export const savable = (draft: string[] | undefined): boolean => draft === undefined || draft.length > 0;

/** The choice after the checkbox of one client is set. Starting from automatic, it is the
 * automatic list that changes. */
export function toggled(
  draft: string[] | undefined,
  automaticIds: string[],
  id: string,
  on: boolean,
): string[] {
  const now = draft ?? automaticIds;
  return on ? (now.includes(id) ? now : [...now, id]) : now.filter((x) => x !== id);
}

/** The client whose page the address (the window's hash) names, if it names one. */
export function openClient(hash: string, clients: Client[]): string | undefined {
  const path = hash.replace(/^#\/?/, "");
  return clients.find((c) => path === c.id || path.startsWith(`${c.id}/`))?.id;
}
