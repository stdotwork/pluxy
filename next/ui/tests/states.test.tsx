import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToString } from "preact-render-to-string";
import type { Account } from "../kit/api";
import { AccountState } from "../kit/pages/accountDetail";
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
  expect(words("active")).toEqual(["Available", "ok"]);
  expect(words("reauth_required")).toEqual(["Needs sign-in", "warn"]);
  expect(words("ok")).toEqual(["Available", "ok"]);
  expect(words("cooling")).toEqual(["Cooling down", "warn"]);
  expect(words("exhausted")).toEqual(["Exhausted", "warn"]);
  expect(words("needs_login")).toEqual(["Needs sign-in", "danger"]);
  expect(words("banned")).toEqual(["Banned", "danger"]);
  expect(words("disabled")).toEqual(["Disabled", "muted"]);
  expect(words("broken")).toEqual(["Broken", "danger"]);
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
  expect(words("disabled")).toEqual(["Disabled", "muted"]);
  expect(words("failed")).toEqual(["Failed", "danger"]);
  expect(words("enabled")).toEqual(["Enabled", "ok"]);
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
    '<span class="badge" data-part="badge" data-tone="warn" data-state="reauth_required" ' +
      'title="reauth_required">Needs sign-in</span>',
  );
  expect(renderToString(<StateBadge state="quarantined" />)).toContain(">Stopped after crashes</span>");
});

test("an account's badge is its plain state: turned off first, then what keeps requests away", () => {
  const account = (fields: Partial<Account>): Account => ({
    id: "a",
    source: "login",
    state: "active",
    refreshable: false,
    extension: "test/alpha",
    ...fields,
  });
  const badge = (fields: Partial<Account>, running?: ReadonlySet<string>) =>
    renderToString(<AccountState a={account(fields)} running={running} />).replace(/<[^>]+>/g, "");
  expect(badge({})).toBe("Available");
  expect(badge({ health: { state: "cooling" } })).toBe("Available");
  expect(badge({ state: "reauth_required" })).toBe("Needs sign-in");
  expect(badge({ health: { state: "needs_login" } })).toBe("Needs sign-in");
  expect(badge({ health: { state: "exhausted" } })).toBe("Exhausted");
  expect(badge({ health: { state: "banned" } })).toBe("Banned");
  expect(badge({ health: { state: "broken" } })).toBe("Broken");
  expect(badge({ disabled: true, health: { state: "banned" } })).toBe("Disabled");
  expect(badge({}, new Set(["test/other"]))).toBe("No provider");
});
