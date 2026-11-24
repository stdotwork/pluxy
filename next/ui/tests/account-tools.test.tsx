import { expect, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { renderToString } from "preact-render-to-string";
import type { Account } from "../kit/api";
import { AccountsPage } from "../kit/pages/accounts";
import { AccountTools } from "../kit/pages/accountTools";
import { credentialTools } from "../kit/pages/accountTools.helpers";
import { type Kit, KitContext } from "../kit/ui";

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

const hooks = (html: string) => new Set([...html.matchAll(/data-part="([^"]+)"/g)].map((m) => m[1]));

/** A kit that answers nothing: the first render of a page reads nothing yet, as in the browser. */
const kit = {
  host: {},
  api: {},
  themes: { resolved: { layout: { tabs: "top" } }, subscribe: () => () => {} },
  unauthorized: () => {},
  extensionsChanged: () => {},
} as unknown as Kit;

const inKit = (page: ComponentChildren) =>
  renderToString(<KitContext.Provider value={kit}>{page}</KitContext.Provider>);

const account = (more: Partial<Account> = {}): Account => ({
  id: "acc-1",
  source: "config",
  state: "active",
  refreshable: false,
  ...more,
});

test("an account's tools carry the theme hooks the contract documents for them", () => {
  const found = hooks(
    inKit(
      <AccountTools
        account={account({
          cooldowns: [
            { scope: "model", model: "fake-model", kind: "rate_limit", until: "2026-01-01T00:00:00Z" },
          ],
        })}
        onChanged={() => {}}
      />,
    ),
  );
  for (const hook of [
    "account-tools",
    "account-check",
    "account-cooldowns",
    "account-tool-actions",
    "account-config-link",
    "account-test-hint",
  ])
    expect(found).toContain(hook);
});

test("the config link is offered only for an account that came from the config file", () => {
  expect(
    hooks(inKit(<AccountTools account={account({ source: "login" })} onChanged={() => {}} />)),
  ).not.toContain("account-config-link");
});

test("the accounts page carries the theme hooks the contract documents for its list controls", () => {
  const found = hooks(inKit(<AccountsPage />));
  for (const hook of ["account-meta", "tabs", "tab", "toolbar", "batch", "pager"])
    expect(found).toContain(hook);
});
