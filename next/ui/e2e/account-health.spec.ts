// Accounts that core leaves out of routing, on All accounts and on their own pages, and All
// accounts in a narrow window.
import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { accountRow, goTo, openAccount, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function addAccount(id: string) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/local",
    method: "api_key",
    input: { api_key: `key-${id}`, label: id },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(async () => {
      const r = await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`);
      return r.body.login.state;
    })
    .toBe("done");
}

const health = async (id: string) =>
  (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.find((a) => a.id === id)
    ?.health?.state;

const condition = (row: ReturnType<typeof accountRow>) => row.locator(".group-mark").getByRole("img");

test("an account its provider closed waits in Needs you with its reason, and Turn on brings it back", async ({
  page,
}) => {
  await addAccount("shut-out-1");
  // The test provider closes the account on this model; core then leaves it out of every request.
  const refused = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${core.clientKey}`,
      "Content-Type": "application/json",
      "X-Pluxy-Account": "shut-out-1",
    },
    body: JSON.stringify({ model: "account-closed", messages: [{ role: "user", content: "hi" }] }),
  });
  expect(refused.status).toBe(403);
  await refused.text();
  expect(await health("shut-out-1")).toBe("banned");

  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  const needs = page.getByRole("region", { name: "Needs you" });
  const row = accountRow(needs, "shut-out-1");
  await expect(condition(row)).toHaveAccessibleName("Closed by provider");
  await expect(row).toContainText("Local closed this account. Check it with Local, then turn it back on.");
  await expect(page.getByText(/· 1 needs you ·/)).toBeVisible();
  // Turning it on is a choice, not a repair: the quiet button.
  const turnOn = row.getByRole("button", { name: "Turn on: shut-out-1", exact: true });
  await expect(turnOn).toHaveAttribute("data-variant", "quiet");
  await turnOn.click();
  await expect(
    condition(accountRow(page.getByRole("region", { name: "Serving" }), "shut-out-1")),
  ).toHaveAccessibleName("Serving");
  expect(await health("shut-out-1")).toBe("ok");

  // Home puts it among the accounts that need the person while it is out.
  const again = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${core.clientKey}`,
      "Content-Type": "application/json",
      "X-Pluxy-Account": "shut-out-1",
    },
    body: JSON.stringify({ model: "account-closed", messages: [{ role: "user", content: "hi" }] }),
  });
  await again.text();
  await goTo(page, "Home");
  const homeNeeds = page.getByRole("region", { name: "Needs you", exact: true });
  await expect(homeNeeds.getByRole("link", { name: "shut-out-1", exact: true })).toBeVisible();
  await expect(homeNeeds.getByRole("button", { name: "Turn on: shut-out-1", exact: true })).toBeVisible();
});

test("a closed account set in the settings file can be turned on from its row, its page and Select", async ({
  page,
}) => {
  // The test provider closes only signed-in accounts, so the list is made to say the config
  // account local-1 is closed until the panel has asked core to turn it on. Core's own answer to
  // that request is the real one.
  const enabled: unknown[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH" && r.url().endsWith("/v0/management/accounts/local-1"))
      enabled.push(r.postDataJSON());
  });
  let banned = true;
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET" || !banned) return route.fallback();
    const reply = await route.fetch();
    const body = (await reply.json()) as { accounts: Account[] };
    const accounts = body.accounts.map((a) =>
      a.id === "local-1" ? { ...a, health: { state: "banned", why: "the provider closed this account" } } : a,
    );
    await route.fulfill({ response: reply, json: { ...body, accounts } });
  });
  page.on("response", (r) => {
    if (r.request().method() === "PATCH" && r.url().endsWith("/v0/management/accounts/local-1") && r.ok())
      banned = false;
  });
  await openPanel(page, core);
  await signIn(page, core);
  await openAccount(page, "local-1");
  const main = page.getByRole("main");
  await expect(main).toContainText("Local closed this account.");
  await expect(main).toContainText("Set in pluxy's settings file.");
  await expect(main.getByRole("button", { name: /^(Turn off|Rename|Edit settings|Remove…)$/ })).toHaveCount(
    0,
  );
  await main
    .locator('[data-part="account-opening"]')
    .getByRole("button", { name: "Turn on", exact: true })
    .click();
  await expect(main).toContainText("Serving.");
  expect(enabled).toEqual([{ disabled: false }]);

  banned = true;
  await goTo(page, "Accounts");
  await expect(condition(accountRow(page, "local-1"))).toHaveAccessibleName("Closed by provider");
  await page.getByRole("button", { name: "Select", exact: true }).click();
  await page.getByRole("checkbox", { name: "Select local-1", exact: true }).check();
  await page.getByRole("toolbar").getByRole("button", { name: "Turn on", exact: true }).click();
  await expect(page.getByRole("toolbar")).toContainText("1 selected");
  await page.getByRole("button", { name: "Done", exact: true }).click();
  await expect(condition(accountRow(page, "local-1"))).toHaveAccessibleName("Serving");
  expect(enabled).toEqual([{ disabled: false }, { disabled: false }]);
});

test("an account whose saved sign-in cannot be read says what to do and offers only its removal", async ({
  page,
}) => {
  // What core lists for such a file (management README, "Accounts"): the file's name as the ID, no
  // extension, kind or state, and the file and the error in the health's why.
  const why = "/home/a/pluxy/state/accounts/lost-1.json: unexpected end of JSON input";
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const reply = await route.fetch();
    const body = (await reply.json()) as { accounts: Account[] };
    const broken = {
      id: "lost-1",
      public_id: "acc_00000001",
      source: "login",
      health: { state: "broken", why },
    };
    await route.fulfill({ response: reply, json: { ...body, accounts: [...body.accounts, broken] } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  const row = accountRow(page.getByRole("region", { name: "Needs you" }), "lost-1");
  await expect(condition(row)).toHaveAccessibleName("Cannot be read");
  await row.getByRole("link", { name: "See what to do: lost-1" }).click();
  const main = page.getByRole("main");
  await expect(main).toContainText("pluxy cannot read this account's saved sign-in.");
  await expect(main).toContainText(
    "Remove it and add it again. If you have a copy of its file, put it back and restart pluxy instead.",
  );
  await main.getByText("Details", { exact: true }).click();
  await expect(main).toContainText(why);
  await expect(main.getByRole("button", { name: "Remove…", exact: true })).toBeVisible();
  await expect(main.getByRole("button", { name: /^(Turn on|Turn off|Rename|Edit settings)$/ })).toHaveCount(
    0,
  );
  // Its limits and week are not shown: there is nothing to read.
  await expect(main.getByRole("region", { name: "Limits" })).toHaveCount(0);
});

test("All accounts fits a narrow window without scrolling sideways", async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 480 });
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.locator('[data-part="account-row"]').first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);
  await page.getByRole("link", { name: "local-1", exact: true }).click();
  await expect(page.getByRole("region", { name: "About" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);
});
