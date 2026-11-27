// What the Tools page works out from core's replies, apart from the page so it can be tested without
// a browser.

import type { ClientKey, Setup, SetupClient, UsageGroup } from "../api";
import { keyName, seriesOf } from "../parts";

/**
 * The tools core has text to paste for: GET /setup/clients/{id}/paste answers 404 for any other
 * (ClientSnippetTools in next/internal/core/setup_clients.go).
 */
const PASTE_TOOLS: ReadonlySet<string> = new Set(["claude-code", "codex", "opencode", "pi", "omp"]);

export const hasPasteText = (client: Pick<SetupClient, "id">) => PASTE_TOOLS.has(client.id);

/**
 * One name for a tool on every page, where core's own differs: its row, the app's page for it and
 * that page's breadcrumb read the same.
 */
const TOOL_NAMES: Readonly<Record<string, string>> = { codex: "Codex" };

export const toolName = (client: Pick<SetupClient, "id" | "name">) => TOOL_NAMES[client.id] ?? client.name;

/** How a coding tool's mark is drawn beside its name on Tools and on its own page. */
export type ToolMarkKind =
  | { kind: "provider"; extension: string }
  | { kind: "opencode" }
  | { kind: "pi" }
  | { kind: "monogram"; letter: string; series: number };

const TOOL_PROVIDERS: Readonly<Record<string, string>> = {
  "claude-code": "pluxy/claude",
  "claude-desktop": "pluxy/claude",
  codex: "pluxy/codex",
  "grok-build": "pluxy/xai",
};

/** The mark for a coding tool: its provider's mark when it belongs to one, else a tool glyph or initial. */
export function toolMarkKind(id: string, name?: string): ToolMarkKind {
  if (Object.hasOwn(TOOL_PROVIDERS, id)) {
    return { kind: "provider", extension: TOOL_PROVIDERS[id] as string };
  }
  if (id === "opencode") return { kind: "opencode" };
  if (id === "pi" || id === "omp") return { kind: "pi" };
  const letter = (name ?? id).trim().charAt(0).toUpperCase() || "?";
  return { kind: "monogram", letter, series: seriesOf(id || letter) };
}

/** The tools the app sets up that core's scan may not list, by ID (RULINGS §1.4). */
export const APP_TOOL_IDS = ["claude-desktop", "grok-build", "hermes"] as const;

/**
 * Whether pluxy cannot see how the tool is set up: core's scan says so for the three tools it does
 * not read, and the app marks the rows it cannot read the same way.
 */
export const isUnchecked = (client: Pick<SetupClient, "state">) => client.state === "unchecked";

/** The tools a page lists: connected ones first, then the other installed ones, then the rest. */
export interface ToolGroups {
  connected: SetupClient[];
  installed: SetupClient[];
  /** Not installed, or not checked: neither counts as installed. */
  other: SetupClient[];
}

/**
 * Groups the tools core found with the ones the host adds. A host's row stands in for core's row
 * with the same ID: it is the host's own reading. Each group keeps the order the tools came in.
 */
export function groupTools(scanned: SetupClient[], extra: SetupClient[] = []): ToolGroups {
  const own = new Set(extra.map((c) => c.id));
  const all = [...scanned.filter((c) => !own.has(c.id)), ...extra];
  const found = (c: SetupClient) => c.installed && !isUnchecked(c);
  return {
    connected: all.filter((c) => found(c) && c.state === "configured"),
    installed: all.filter((c) => found(c) && c.state !== "configured"),
    other: all.filter((c) => !found(c)),
  };
}

/** Whether the three tools only the app sets up are all among the rows already. */
export const listsAppTools = (groups: ToolGroups) => {
  const ids = new Set([...groups.connected, ...groups.installed, ...groups.other].map((c) => c.id));
  return APP_TOOL_IDS.every((id) => ids.has(id));
};

/** The address programs reach pluxy at: the host's, else the one core listens on. */
export const toolsAddress = (hostAddress: string | undefined, setup?: Setup) =>
  hostAddress ?? setup?.found.core.listen;

/**
 * A key's name as the Tools page shows it: its label; in the app, then the tool the app made it
 * for (owners, by key ID, else by masked key); else the masked key ("Key sk…48"). mask is for the
 * names a person or a tool gave, not for the masked key. The second value says whether a name is
 * known, as opposed to the masked key.
 */
export function toolKeyName(
  key: ClientKey,
  owners: ReadonlyMap<string, string> | undefined,
  mask: (name: string) => string = (n) => n,
): { text: string; named: boolean } {
  const label = key.label?.trim();
  if (label) return { text: mask(label), named: true };
  const owner = (owners?.get(key.id) ?? owners?.get(key.masked))?.trim();
  if (owner) return { text: mask(owner), named: true };
  return { text: keyName(key.id, [key]) ?? key.masked, named: false };
}

/** How many requests each key made this week, by key ID. */
export function requestsByKey(groups: UsageGroup[] | undefined): Map<string, number> {
  return new Map((groups ?? []).map((g) => [g.name, g.total_requests]));
}

/** The text of a file with the key's value where core's placeholder stood. */
export function withKey(content: string, placeholder: string, value?: string): string {
  return value ? content.replaceAll(placeholder, () => value) : content;
}

/** What core's text holds where a model's name goes (next/internal/core/setup_clients.go). */
export const MODEL_PLACEHOLDER = "<model-id>";
