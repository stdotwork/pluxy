// Accounts that core leaves out of routing, and the Accounts page in a narrow window.
import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function addAccount(id: string) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/local",
    method: "api_key",
    input: { api_key: `key-${id}` },
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

test("an account its provider closed says so, and Enable brings it back", async ({ page }) => {
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
  const row = page.locator('[data-part="item"]').filter({ hasText: "shut-out-1" });
  await expect(row.locator('[data-part="badge"]')).toHaveText("Closed by provider");
  await expect(page.getByText(/· 1 problem$/)).toBeVisible();
  await row.getByRole("button", { name: "shut-out-1", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: shut-out-1" });
  await expect(detail.locator('[data-part="badge"]').first()).toHaveText("Closed by provider");
  await expect(detail).toContainText("the test provider closed this account");
  await expect(detail).toContainText("pluxy skips this account until you enable it again.");
  await detail.getByRole("button", { name: "Enable", exact: true }).click();
  await expect(row.locator('[data-part="badge"]')).toHaveText("Serving");
  await expect(detail.getByRole("button", { name: "Disable", exact: true })).toBeVisible();
  expect(await health("shut-out-1")).toBe("ok");

  // The Dashboard counts it among the accounts that are not serving while it is out.
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
  await goTo(page, "Dashboard");
  const status = page.getByRole("region", { name: "Credential status", exact: true });
  await expect(status.locator("dl")).toContainText("Unavailable1");
});

test("a banned account set in core's config can be enabled from its details and by batch", async ({
  page,
}) => {
  // The test provider closes only signed-in accounts, so the list is made to say the config
  // account local-1 is banned until the panel has asked core to enable it. Core's own answer to
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
  await goTo(page, "Accounts");
  const row = page.locator('[data-part="item"]').filter({ hasText: "local-1" });
  await expect(row.locator('[data-part="badge"]')).toHaveText("Closed by provider");
  await row.getByRole("button", { name: "local-1", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: local-1" });
  await expect(detail).toContainText("This account is set in core's config; change it there.");
  await expect(detail.getByRole("button", { name: /^(Disable|Edit|Remove)$/ })).toHaveCount(0);
  if (process.env.P7_SHOTS)
    await page.screenshot({ path: `${process.env.P7_SHOTS}/config-account-banned.png` });
  await detail.getByRole("button", { name: "Enable", exact: true }).click();
  await expect(row.locator('[data-part="badge"]')).toHaveText("Serving");
  await expect(detail.getByRole("button", { name: "Enable", exact: true })).toHaveCount(0);
  expect(enabled).toEqual([{ disabled: false }]);

  banned = true;
  await page.reload();
  await expect(row.locator('[data-part="badge"]')).toHaveText("Closed by provider");
  await row.getByRole("checkbox").check();
  await page
    .getByRole("group", { name: "Batch actions" })
    .getByRole("button", { name: "Enable", exact: true })
    .click();
  await expect(page.getByRole("status")).toHaveText("1 account updated");
  await expect(row.locator('[data-part="badge"]')).toHaveText("Serving");
  expect(enabled).toEqual([{ disabled: false }, { disabled: false }]);
});

test("an account whose credential file cannot be read says so and offers only its removal", async ({
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
  const row = page.locator('[data-part="item"]').filter({ hasText: "lost-1" });
  await expect(row.locator('[data-part="badge"]')).toHaveText("Cannot be read");
  await page.getByLabel("Status filter").selectOption({ label: "Problem" });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "lost-1", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: lost-1" });
  await expect(detail.locator('[data-part="badge"]').first()).toHaveText("Cannot be read");
  await expect(detail).toContainText(why);
  await expect(detail).toContainText("pluxy cannot read this account's auth file");
  await expect(detail.getByRole("button", { name: "Remove", exact: true })).toBeVisible();
  await expect(detail.getByRole("button", { name: /^(Enable|Disable|Edit)$/ })).toHaveCount(0);
});

test("the accounts table scrolls inside its frame in a narrow window", async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 800 });
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.locator('[data-part="item"]').first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);
});
