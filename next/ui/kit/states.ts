// The states core reports, in the words a person reads (docs/terms.md). Core names them in
// next/internal/cred/store.go (an account), next/internal/route/health.go (an account's health),
// next/internal/host/managed.go and next/internal/core/packages.go (an extension).

import { strings } from "./strings";

const t = strings("states", {
  available: "Available",
  needsSignIn: "Needs sign-in",
  coolingDown: "Cooling down",
  exhausted: "Exhausted",
  banned: "Banned",
  disabled: "Disabled",
  enabled: "Enabled",
  broken: "Broken",
  starting: "Starting",
  running: "Running",
  idle: "Idle",
  crashed: "Crashed",
  quarantined: "Stopped after crashes",
  draining: "Finishing requests",
  stopped: "Stopped",
  failed: "Failed",
});

export type StateTone = "ok" | "warn" | "danger" | "muted";

/** What a state belongs to: `active` is "Available" for an account and "Running" for an extension. */
export type StateOf = "account" | "extension";

type Key = Parameters<typeof t>[0];

const STATES: Record<string, [text: Key, tone: StateTone]> = {
  // An extension's program.
  starting: ["starting", "warn"],
  active: ["running", "ok"],
  idle: ["idle", "muted"],
  crashed: ["crashed", "danger"],
  quarantined: ["quarantined", "danger"],
  draining: ["draining", "warn"],
  stopped: ["stopped", "muted"],
  failed: ["failed", "danger"],
  // An account, and what routing holds against it.
  reauth_required: ["needsSignIn", "warn"],
  ok: ["available", "ok"],
  cooling: ["coolingDown", "warn"],
  exhausted: ["exhausted", "warn"],
  needs_login: ["needsSignIn", "danger"],
  banned: ["banned", "danger"],
  broken: ["broken", "danger"],
  // Turned off or on by a person.
  disabled: ["disabled", "muted"],
  enabled: ["enabled", "ok"],
};

const ACCOUNT: typeof STATES = { active: ["available", "ok"] };

const known = (state: string, of: StateOf) =>
  (of === "account" && Object.hasOwn(ACCOUNT, state) ? ACCOUNT : STATES)[state];

/**
 * A state in plain words. One this table does not know yet shows as core sent it, with spaces
 * for its underscores and a capital first letter: never the raw name.
 */
export function stateText(state: string, of: StateOf = "extension"): string {
  const entry = Object.hasOwn(STATES, state) ? known(state, of) : undefined;
  if (entry) return t(entry[0]);
  const words = state.replaceAll("_", " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function stateTone(state: string, of: StateOf = "extension"): StateTone {
  return (Object.hasOwn(STATES, state) ? known(state, of)?.[1] : undefined) ?? "muted";
}
