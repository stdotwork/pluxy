// What the Setup page works out from core's replies, apart from the page so it can be tested
// without a browser.

import type {
  Account,
  Api,
  Package,
  Setup,
  SetupApplyResult,
  SetupClient,
  SetupPick,
  SetupSignIn,
} from "../api";
import type { Reply } from "../host";
import { accountCondition } from "../parts";

/** What the person ticked or unticked themselves, by pick ID. A pick not in it follows core's suggestion. */
export type Choices = Record<string, boolean>;

/** The outcome of the last Add for each pick it sent, by pick ID. */
export type Results = Record<string, { status: "done" | "error"; error?: string }>;

/**
 * The four kinds of account v0.1 serves fully, in the order step 1 offers them, each under the
 * plain name a person knows it by.
 */
export const MAIN_KINDS: readonly { id: string; title: string }[] = [
  { id: "pluxy/claude", title: "Claude" },
  { id: "pluxy/codex", title: "ChatGPT (Codex)" },
  { id: "pluxy/gemini", title: "Gemini" },
  { id: "pluxy/openai-compatible", title: "Another service with an API key" },
];

/** A kind of account's name: the plain name of a main kind, else the pick's own title. */
export function kindTitle(pick: Pick<SetupPick, "id" | "title">): string {
  return MAIN_KINDS.find((kind) => kind.id === pick.id)?.title ?? (pick.title || pick.id);
}

/** The picks step 1 offers: providers only. A tool package (kind client) never shows there. */
export function providerPicks(setup: Setup): SetupPick[] {
  return setup.picks.filter((pick) => pick.kind === "provider");
}

/**
 * Step 1's kinds in their three groups: what was found on this computer (core's suggestions and
 * what an import needs), then the main kinds not already shown, then every other one, for More.
 */
export function kindGroups(
  setup: Setup,
  needed: ReadonlySet<string> = new Set(),
): { found: SetupPick[]; main: SetupPick[]; more: SetupPick[] } {
  const picks = providerPicks(setup);
  const found = picks.filter((pick) => pick.suggested || needed.has(pick.id));
  const shown = new Set(found.map((pick) => pick.id));
  const main = MAIN_KINDS.flatMap((kind) =>
    picks.filter((pick) => pick.id === kind.id && !shown.has(pick.id)),
  );
  for (const pick of main) shown.add(pick.id);
  return { found, main, more: picks.filter((pick) => !shown.has(pick.id)) };
}

/** Step 1's kinds in the order it shows them. */
export function orderedKinds(setup: Setup, needed?: ReadonlySet<string>): SetupPick[] {
  const { found, main, more } = kindGroups(setup, needed);
  return [...found, ...main, ...more];
}

/** Installed as core says, or by an Add whose result core's next reply has not caught up with. */
export function isInstalled(pick: SetupPick, results: Results): boolean {
  return pick.installed || results[pick.id]?.status === "done";
}

/** Ticked: the person's own choice when they made one, core's suggestion otherwise. Never an installed pick. */
export function isTicked(pick: SetupPick, choices: Choices, results: Results): boolean {
  return !isInstalled(pick, results) && (choices[pick.id] ?? pick.suggested);
}

/** The pick IDs Add sends: ticked providers that are not installed. */
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

/** Sends the picks to add (ids). Returns the new results, or why the call itself failed. */
export async function start(
  api: Pick<Api, "applySetup">,
  ids: string[],
  results: Results,
): Promise<{ reply: Reply<{ results: SetupApplyResult[] }>; results: Results }> {
  const reply = await api.applySetup(ids);
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

/** Package states where the package is installed but its program did not run. */
const failedStates = new Set(["failed", "crashed", "quarantined"]);

/** Whether a package runs: it is enabled and has not failed. */
const packageRuns = (p: Package) => p.enabled && !failedStates.has(p.state);

/**
 * What a provider pick is, in the one answer the page gives to "does this provider run?". Not
 * installed: offered, or its Add failed. Installed: running, or stopped, because its Add
 * failed, its package is turned off, or its package failed.
 */
export type PickStatus =
  | { state: "offered" }
  | { state: "install-failed"; error?: string }
  | { state: "running" }
  | { state: "stopped"; why: "disabled" }
  | { state: "stopped"; why: "start" | "failed"; error?: string };

/** A provider pick with its status. */
export interface PickWithStatus {
  pick: SetupPick;
  status: PickStatus;
}

/**
 * What a provider pick is. Installed is core's word or an Add that worked (see isInstalled). Once
 * the package is listed, its state decides whether an installed pick runs, over an older Add
 * result. Without the package, a failed Add decides.
 */
export function pickStatus(pick: SetupPick, results: Results, pkg?: Package): PickStatus {
  const result = results[pick.id];
  const startError = result?.status === "error" ? result.error : undefined;
  if (!isInstalled(pick, results)) {
    return result?.status === "error" ? { state: "install-failed", error: startError } : { state: "offered" };
  }
  if (!pkg) {
    return result?.status === "error"
      ? { state: "stopped", why: "start", error: startError }
      : { state: "running" };
  }
  if (failedStates.has(pkg.state)) return { state: "stopped", why: "failed", error: startError ?? pkg.error };
  if (!pkg.enabled) return { state: "stopped", why: "disabled" };
  return { state: "running" };
}

/** The provider picks, each with its status, in the order core offers them. */
export function pickStatuses(setup: Setup, results: Results, packages: Package[] = []): PickWithStatus[] {
  return providerPicks(setup).map((pick) => ({
    pick,
    status: pickStatus(
      pick,
      results,
      packages.find((p) => p.id === pick.id),
    ),
  }));
}

/**
 * The kinds of account pluxy runs, as { id, title } in step 1's order: the provider picks whose
 * status says running, then any running package that is none of the picks (added elsewhere, such
 * as on Extensions), under its own ID. AddAccountPage leaves out one that has no way to sign in.
 */
export function runningKinds(
  setup: Setup,
  results: Results,
  packages: Package[] = [],
  needed?: ReadonlySet<string>,
): { id: string; title?: string }[] {
  const picked = new Set(setup.picks.map((pick) => pick.id));
  const statuses = new Map(pickStatuses(setup, results, packages).map((s) => [s.pick.id, s.status]));
  return [
    ...orderedKinds(setup, needed)
      .filter((pick) => statuses.get(pick.id)?.state === "running")
      .map((pick) => ({ id: pick.id, title: kindTitle(pick) })),
    ...packages.filter((p) => !picked.has(p.id) && packageRuns(p)).map((p) => ({ id: p.id })),
  ];
}

/**
 * Whether core runs a provider: a provider pick's status says running, or a package that is none
 * of the picks, is enabled and has not failed.
 */
export function hasProvider(setup: Setup, results: Results, packages: Package[] = []): boolean {
  return runningKinds(setup, results, packages).length > 0;
}

/** The sign-ins found whose provider runs: the ones signing in on this page can use. */
export function signInsToName(setup: Setup, running: { id: string }[]): SetupSignIn[] {
  const ids = new Set(running.map((kind) => kind.id));
  return setup.found.sign_ins.filter((signIn) => ids.has(signIn.provider));
}

/** The tools that use pluxy: core's found clients and the host's own, Connected. */
export function connectedTools(setup: Setup | undefined, extra: SetupClient[] = []): SetupClient[] {
  return [...(setup?.found.clients ?? []), ...extra].filter(
    (client) => client.installed && client.state === "configured",
  );
}

/** Names in a sentence: "A", "A and B", "A, B and C". */
export function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** An account a test request can go through: not turned off, and its provider runs. */
export function canTest(account: Account, running?: ReadonlySet<string>): boolean {
  const kind = accountCondition(account, running).kind;
  return kind !== "off" && kind !== "no_provider";
}

/**
 * The account step 3 tests by default: the one added in step 1 on this visit, else the first
 * Serving one, else the first that can take a test. None when no account can.
 */
export function defaultTestAccount(
  accounts: Account[],
  running?: ReadonlySet<string>,
  added?: string,
): Account | undefined {
  const usable = accounts.filter((a) => canTest(a, running));
  return (
    usable.find((a) => a.id === added) ??
    usable.find((a) => accountCondition(a, running).kind === "serving") ??
    usable[0]
  );
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
