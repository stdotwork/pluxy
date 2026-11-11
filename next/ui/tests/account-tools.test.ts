import { expect, test } from "bun:test";
import { credentialTools } from "../kit/pages/accountTools.helpers";
import { PARTS } from "../kit/theme/contract";

test("credential tools follow source, renewal capability and login state", () => {
  expect(credentialTools({ source: "config", state: "reauth_required", refreshable: true })).toEqual({
    refresh: false,
    signIn: false,
  });
  expect(credentialTools({ source: "login", state: "active", refreshable: false })).toEqual({
    refresh: false,
    signIn: false,
  });
  expect(credentialTools({ source: "login", state: "active", refreshable: true })).toEqual({
    refresh: true,
    signIn: false,
  });
  expect(credentialTools({ source: "login", state: "reauth_required", refreshable: false })).toEqual({
    refresh: false,
    signIn: true,
  });
});

test("account tools expose theme hooks", () => {
  for (const hook of [
    "account-tools",
    "account-check",
    "account-cooldowns",
    "account-cooldown-confirm",
    "account-tool-actions",
    "account-config-link",
    "account-test-hint",
    "account-models",
    "account-test-result",
    "account-test-reply",
    "account-sign-in",
  ] as const)
    expect(PARTS).toContain(hook);
});
