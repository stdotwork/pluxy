// The states core reports, in the words a person reads (docs/terms.md). Core names them in
// next/internal/cred/store.go (an account), next/internal/route/health.go (an account's health),
// next/internal/host/managed.go and next/internal/core/packages.go (an extension), and
// next/internal/core/setup_clients.go (a coding tool). failureWord names what ended a failed
// request, by the error codes in next/llm/llm.go.

import type { UsageEvent } from "./api/usageEvents";
import { strings } from "./strings";

const t = strings("states", {
  // An account's condition: the same words on every page (AccountRow, StateBadge).
  serving: "Serving",
  coolingDown: "Cooling down",
  limitUsedUp: "Limit used up",
  needsSignIn: "Needs sign-in",
  closedByProvider: "Closed by provider",
  cannotBeRead: "Cannot be read",
  providerStopped: "Provider stopped",
  // Turned off or on by a person: an account, an extension.
  turnedOff: "Turned off",
  turnedOn: "Turned on",
  // An extension's program.
  starting: "Starting",
  running: "Running",
  idle: "Idle",
  crashed: "Crashed",
  quarantined: "Stopped after crashes",
  draining: "Finishing requests",
  stopped: "Stopped",
  failed: "Failed",
  // A coding tool, as pluxy found it on this computer.
  connected: "Connected",
  notConnected: "Installed · not connected",
  needsUpdate: "Settings need update",
  cannotChange: "Cannot change settings",
  notInstalled: "Not installed",
  notChecked: "Not checked",
});

export type StateTone = "ok" | "warn" | "danger" | "muted";

/**
 * What a state belongs to: `active` is "Serving" for an account and "Running" for an extension;
 * a tool's states are its own.
 */
export type StateOf = "account" | "extension" | "tool";

type Key = Parameters<typeof t>[0];
type Table = Record<string, [text: Key, tone: StateTone]>;

const EXTENSION: Table = {
  starting: ["starting", "warn"],
  active: ["running", "ok"],
  idle: ["idle", "muted"],
  crashed: ["crashed", "danger"],
  quarantined: ["quarantined", "danger"],
  draining: ["draining", "warn"],
  stopped: ["stopped", "muted"],
  failed: ["failed", "danger"],
  disabled: ["turnedOff", "muted"],
  enabled: ["turnedOn", "ok"],
};

// An account: its own state, what routing holds against it (its health), and no_provider, the
// condition of an account whose provider's extension does not run (api/accounts.ts,
// providerMissing). Needs sign-in, Closed by provider, Cannot be read and Provider stopped are
// what only the person can fix: the danger tone, as AccountRow's Needs you mark.
const ACCOUNT: Table = {
  active: ["serving", "ok"],
  ok: ["serving", "ok"],
  reauth_required: ["needsSignIn", "danger"],
  needs_login: ["needsSignIn", "danger"],
  cooling: ["coolingDown", "warn"],
  exhausted: ["limitUsedUp", "warn"],
  banned: ["closedByProvider", "danger"],
  broken: ["cannotBeRead", "danger"],
  no_provider: ["providerStopped", "danger"],
  disabled: ["turnedOff", "muted"],
  enabled: ["turnedOn", "ok"],
};

// A coding tool: core's SetupClientState, not_installed for a tool core did not find, and
// unchecked for one whose setup pluxy cannot read (only the desktop app marks a tool so).
const TOOL: Table = {
  configured: ["connected", "ok"],
  "not-configured": ["notConnected", "muted"],
  "needs-update": ["needsUpdate", "warn"],
  refused: ["cannotChange", "danger"],
  not_installed: ["notInstalled", "muted"],
  unchecked: ["notChecked", "muted"],
};

const TABLES: Record<StateOf, Table> = { account: ACCOUNT, extension: EXTENSION, tool: TOOL };

const known = (state: string, of: StateOf) => {
  const table = TABLES[of];
  return Object.hasOwn(table, state) ? table[state] : undefined;
};

/**
 * A state in plain words. One this table does not know yet shows as core sent it, with spaces
 * for its underscores and a capital first letter: never the raw name.
 */
export function stateText(state: string, of: StateOf = "extension"): string {
  const entry = known(state, of);
  if (entry) return t(entry[0]);
  const words = state.replaceAll("_", " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function stateTone(state: string, of: StateOf = "extension"): StateTone {
  return known(state, of)?.[1] ?? "muted";
}

const f = strings("failures", {
  not_found: "Model not served",
  quota: "Limit used up",
  rate_limit: "Rate limited",
  auth: "Sign-in refused",
  keyRefused: "Key refused",
  account_closed: "Account closed",
  upstream: "Provider error",
  overloaded: "Overloaded",
  unavailable: "Provider unreachable",
  extension_timeout: "Timed out",
  cancelled: "Cancelled",
  invalid_request: "Request refused",
  unsupported: "Not supported",
  providerStopped: "Provider stopped",
  internal: "pluxy error",
  status: "Failed {status}",
  failed: "Failed",
});

type FailureKey = Parameters<typeof f>[0];

const FAILURES: Record<string, FailureKey> = {
  not_found: "not_found",
  quota: "quota",
  rate_limit: "rate_limit",
  auth: "auth",
  permission: "auth",
  account_closed: "account_closed",
  upstream: "upstream",
  unavailable: "unavailable",
  extension_timeout: "extension_timeout",
  cancelled: "cancelled",
  invalid_request: "invalid_request",
  unsupported: "unsupported",
  extension_crashed: "providerStopped",
  extension_protocol: "providerStopped",
  extension_unavailable: "providerStopped",
  internal: "internal",
};

/** HTTP 529: the provider said it is overloaded (Anthropic's status for it). */
const OVERLOADED = 529;

/**
 * What ended a failed request, in a word or two: by its error code, as a usage event's
 * `error_code` or an account test's `AccountError.code` gives it, and the HTTP status. A code
 * this table does not know reads `Failed {status}`. Given a usage event, a refused sign-in on a
 * request that reached no account reads "Key refused": the client's key was the one refused.
 */
export function failureWord(code: string | undefined, status?: number): string;
export function failureWord(event: Pick<UsageEvent, "error_code" | "status" | "account" | "tried">): string;
export function failureWord(
  from: string | undefined | Pick<UsageEvent, "error_code" | "status" | "account" | "tried">,
  status?: number,
): string {
  if (from !== undefined && typeof from === "object") {
    const tried = !!from.account || (from.tried?.length ?? 0) > 0;
    const code = from.error_code;
    if (!tried && (code === "auth" || code === "permission")) return f("keyRefused");
    return failureWord(code, from.status);
  }
  const key = from && Object.hasOwn(FAILURES, from) ? FAILURES[from] : undefined;
  if (key === "upstream" && status === OVERLOADED) return f("overloaded");
  if (key) return f(key);
  return status ? f("status", { status }) : f("failed");
}
