// What the Setup page works out from core's replies, apart from the page so it can be tested
// without a browser.

import type { Api, Package, Setup, SetupApplyResult, SetupClient, SetupPick } from "../api";
import type { Reply } from "../host";

/** What the person ticked or unticked themselves, by pick ID. A pick not in it follows core's suggestion. */
export type Choices = Record<string, boolean>;

/** The outcome of the last Start for each pick it sent, by pick ID. */
export type Results = Record<string, { status: "done" | "error"; error?: string }>;

/** The picks the page offers to install: every kind but client, whose settings the page only shows. */
export function providerPicks(setup: Setup): SetupPick[] {
  return setup.picks.filter((pick) => pick.kind !== "client");
}

/** Installed as core says, or by a Start whose result core's next reply has not caught up with. */
export function isInstalled(pick: SetupPick, results: Results): boolean {
  return pick.installed || results[pick.id]?.status === "done";
}

/** Ticked: the person's own choice when they made one, core's suggestion otherwise. Never an installed pick. */
export function isTicked(pick: SetupPick, choices: Choices, results: Results): boolean {
  return !isInstalled(pick, results) && (choices[pick.id] ?? pick.suggested);
}

/** The pick IDs Start sends: ticked providers that are not installed. */
export function toInstall(setup: Setup, choices: Choices, results: Results): string[] {
  return providerPicks(setup)
    .filter((pick) => isTicked(pick, choices, results))
    .map((pick) => pick.id);
}

/** The results after a reply: each pick it names takes its new outcome, the others keep theirs. */
export function withResults(results: Results, reply: SetupApplyResult[]): Results {
  const next = { ...results };
  for (const r of reply) next[r.id] = { status: r.status, error: r.error };
  return next;
}

/** Sends Start's picks. Returns the new results, or why the call itself failed. */
export async function start(
  api: Pick<Api, "applySetup">,
  setup: Setup,
  choices: Choices,
  results: Results,
): Promise<{ reply: Reply<{ results: SetupApplyResult[] }>; results: Results }> {
  const reply = await api.applySetup(toInstall(setup, choices, results));
  return { reply, results: reply.body ? withResults(results, reply.body.results) : results };
}

/**
 * Where the scan stands: "first" while the first one runs (nothing is known yet), "again" while
 * a later one runs over a kept scan, "never" when none has run, "done" otherwise.
 */
export function scanState(setup: Setup): "first" | "again" | "never" | "done" {
  if (setup.scanning) return setup.scanned_at ? "again" : "first";
  return setup.scanned_at ? "done" : "never";
}

/** Whether core runs a provider: an installed provider pick, or any package that is not a client pick. */
export function hasProvider(setup: Setup, results: Results, packages: Package[] = []): boolean {
  const clients = new Set(setup.picks.filter((pick) => pick.kind === "client").map((pick) => pick.id));
  return (
    providerPicks(setup).some((pick) => isInstalled(pick, results)) ||
    packages.some((p) => !clients.has(p.id))
  );
}

/** The clients found installed, and the rest. */
export function splitClients(setup: Setup): { found: SetupClient[]; others: SetupClient[] } {
  return {
    found: setup.found.clients.filter((client) => client.installed),
    others: setup.found.clients.filter((client) => !client.installed),
  };
}

/** Whether address (scheme, host and port) names the machine the browser runs on. Unknown counts as yes. */
export function onThisMachine(address?: string): boolean {
  if (!address) return true;
  try {
    const host = new URL(address).hostname.replace(/^\[|\]$/g, "");
    return host === "localhost" || host === "::1" || /^127\./.test(host);
  } catch {
    return true;
  }
}

/** The files of a paste reply with the client key's value where the placeholder stood. */
export function withKey(content: string, placeholder: string, value?: string): string {
  return value ? content.replaceAll(placeholder, value) : content;
}
