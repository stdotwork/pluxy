import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToString } from "preact-render-to-string";
import type { Account } from "../kit/api";
import { accountCondition, conditionState, conditionUnchecked, conditionWord, GroupMark } from "../kit/parts";
import { stateText, stateTone } from "../kit/states";
import { StateBadge } from "../kit/ui";

/** The values of the string constants named like `prefix...` in one of core's Go files. */
function coreStates(file: string, prefix: string): string[] {
  const go = readFileSync(join(import.meta.dir, "../..", file), "utf8");
  return [...go.matchAll(new RegExp(`^\\s*${prefix}\\w+\\s*= "(\\w+)"`, "gm"))].map((m) => m[1] as string);
}

test("every account and health state core can send has plain words", () => {
  expect(coreStates("internal/cred/store.go", "State").sort()).toEqual(["active", "reauth_required"]);
  expect(coreStates("internal/route/health.go", "Health").sort()).toEqual(
    ["banned", "broken", "cooling", "disabled", "exhausted", "needs_login", "ok"].sort(),
  );
  const words = (state: string) => [stateText(state, "account"), stateTone(state, "account")];
  // The condition words AccountRow shows; what only the person can fix is the danger tone.
  expect(words("active")).toEqual(["Serving", "ok"]);
  expect(words("reauth_required")).toEqual(["Needs sign-in", "danger"]);
  expect(words("ok")).toEqual(["Serving", "ok"]);
  expect(words("cooling")).toEqual(["Cooling down", "warn"]);
  expect(words("exhausted")).toEqual(["Limit used up", "warn"]);
  expect(words("needs_login")).toEqual(["Needs sign-in", "danger"]);
  expect(words("banned")).toEqual(["Closed by provider", "danger"]);
  expect(words("disabled")).toEqual(["Turned off", "muted"]);
  expect(words("broken")).toEqual(["Cannot be read", "danger"]);
  expect(words("no_provider")).toEqual(["Provider stopped", "danger"]);
  expect(words("enabled")).toEqual(["Turned on", "ok"]);
});

test("every extension state core can send has plain words", () => {
  expect(coreStates("internal/host/managed.go", "State").sort()).toEqual(
    ["active", "crashed", "draining", "idle", "quarantined", "starting", "stopped"].sort(),
  );
  const words = (state: string) => [stateText(state), stateTone(state)];
  expect(words("starting")).toEqual(["Starting", "warn"]);
  expect(words("active")).toEqual(["Running", "ok"]);
  expect(words("idle")).toEqual(["Idle", "muted"]);
  expect(words("crashed")).toEqual(["Crashed", "danger"]);
  expect(words("quarantined")).toEqual(["Stopped after crashes", "danger"]);
  expect(words("draining")).toEqual(["Finishing requests", "warn"]);
  expect(words("stopped")).toEqual(["Stopped", "muted"]);
  // An installed extension that is turned off, or that could not start (core/packages.go).
  expect(words("disabled")).toEqual(["Turned off", "muted"]);
  expect(words("failed")).toEqual(["Failed", "danger"]);
  expect(words("enabled")).toEqual(["Turned on", "ok"]);
});

test("a state the kit does not know never shows raw", () => {
  expect(stateText("not_checked")).toBe("Not checked");
  expect(stateText("some_new_state", "account")).toBe("Some new state");
  expect(stateTone("some_new_state")).toBe("muted");
  // A name every object has is not a state.
  expect(stateText("constructor")).toBe("Constructor");
  expect(stateTone("toString", "account")).toBe("muted");
});

test("the badge keeps what core sent, for support", () => {
  expect(renderToString(<StateBadge state="reauth_required" of="account" />)).toBe(
    '<span class="badge" data-part="badge" data-tone="danger" data-state="reauth_required" ' +
      'title="reauth_required">Needs sign-in</span>',
  );
  expect(renderToString(<StateBadge state="quarantined" />)).toContain(">Stopped after crashes</span>");
});

test("an account's condition word: turned off first, then what keeps requests away", () => {
  const account = (fields: Partial<Account>): Account => ({
    id: "a",
    source: "login",
    state: "active",
    refreshable: false,
    extension: "test/alpha",
    ...fields,
  });
  const badge = (fields: Partial<Account>, running?: ReadonlySet<string>) =>
    conditionWord(accountCondition(account(fields), running));
  const later = new Date(Date.now() + 3_600_000).toISOString();
  expect(badge({})).toBe("Serving");
  // A whole account cooling down waits; one cooling down on some models still serves.
  expect(badge({ cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] })).toBe("Cooling down");
  expect(badge({ health: { state: "cooling", until: later } })).toBe("Cooling down");
  expect(badge({ cooldowns: [{ scope: "model", model: "m", kind: "rate_limit", until: later }] })).toBe(
    "Serving",
  );
  // Core's health reads cooling for a block on one model too: the cooldowns it lists say which.
  expect(
    badge({
      health: { state: "cooling", until: later },
      cooldowns: [{ scope: "model", model: "m", kind: "rate_limit", until: later }],
    }),
  ).toBe("Serving");
  expect(badge({ state: "reauth_required" })).toBe("Needs sign-in");
  expect(badge({ health: { state: "needs_login" } })).toBe("Needs sign-in");
  expect(badge({ health: { state: "exhausted" } })).toBe("Limit used up");
  expect(badge({ health: { state: "banned" } })).toBe("Closed by provider");
  expect(badge({ health: { state: "broken" } })).toBe("Cannot be read");
  expect(badge({ disabled: true, health: { state: "banned" } })).toBe("Turned off");
  expect(badge({}, new Set(["test/other"]))).toBe("Provider stopped");
  // The tone a StateBadge gives the condition: what only the person can fix is danger, a wait warn.
  const tone = (fields: Partial<Account>, running?: ReadonlySet<string>) =>
    stateTone(conditionState(accountCondition(account(fields), running)), "account");
  expect(tone({})).toBe("ok");
  expect(tone({ cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] })).toBe("warn");
  expect(tone({ health: { state: "exhausted" } })).toBe("warn");
  expect(tone({ state: "reauth_required" })).toBe("danger");
  expect(tone({}, new Set(["test/other"]))).toBe("danger");
  expect(tone({ disabled: true })).toBe("muted");
  // The mark says the same word, in its group's colour.
  const mark = (fields: Partial<Account>, running?: ReadonlySet<string>) =>
    renderToString(<GroupMark c={accountCondition(account(fields), running)} />);
  expect(mark({}, new Set(["test/other"]))).toContain('data-group="needs"');
  expect(mark({}, new Set(["test/other"]))).toContain('aria-label="Provider stopped"');
  expect(mark({ cooldowns: [{ scope: "model", model: "m", kind: "rate_limit", until: later }] })).toContain(
    'data-group="waiting"',
  );
});

test("while the status is not read, a condition that waits on it is not settled", () => {
  const account = (fields: Partial<Account>): Account => ({
    id: "a",
    source: "login",
    state: "active",
    refreshable: false,
    extension: "test/alpha",
    ...fields,
  });
  const later = new Date(Date.now() + 3_600_000).toISOString();
  const unchecked = (fields: Partial<Account>, running?: ReadonlySet<string>) => {
    const a = account(fields);
    return conditionUnchecked(a, accountCondition(a, running), running);
  };
  // Serving and waiting hold only if the provider runs.
  expect(unchecked({})).toBe(true);
  expect(unchecked({ cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] })).toBe(true);
  // Read: settled either way.
  expect(unchecked({}, new Set(["test/alpha"]))).toBe(false);
  expect(unchecked({}, new Set(["test/other"]))).toBe(false);
  // Turned off, or needing the person, does not wait on the provider.
  expect(unchecked({ disabled: true })).toBe(false);
  expect(unchecked({ state: "reauth_required" })).toBe(false);
  expect(unchecked({ health: { state: "broken" }, extension: undefined })).toBe(false);
  const mark = renderToString(<GroupMark c={accountCondition(account({}))} unchecked />);
  expect(mark).toContain('data-group="unchecked"');
  expect(mark).toContain('aria-label="Not checked"');
});
