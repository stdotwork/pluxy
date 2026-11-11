import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();
async function addAccount(id: string, method = "api_key") {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/alpha",
    method,
    input: { api_key: "account-tools-test-input" },
  });
  expect(r.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
}
async function openAccount(page: Page, id: string) {
  await page.locator('nav a[href="#/accounts"]').click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  await page.getByRole("button", { name: id, exact: true }).click();
  return page.getByRole("complementary", { name: `Details: ${id}`, exact: true });
}

test("account health, cooldown reset, models and a paid test", async ({ page }) => {
  await addAccount("tools-key");
  const request = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "rate-limited", messages: [{ role: "user", content: "Hi" }] }),
  });
  expect(request.status).toBe(429);
  const accounts = await core.mgmt<{ accounts: { id: string; cooldowns?: { model: string }[] }[] }>(
    "GET",
    "/accounts",
  );
  expect(accounts.body.accounts.find((a) => a.id === "tools-key")?.cooldowns).toEqual(
    expect.arrayContaining([expect.objectContaining({ model: "rate-limited" })]),
  );
  await openPanel(page, core);
  await signIn(page, core);
  let detail = await openAccount(page, "tools-key");
  await expect(detail.getByText("Cooling", { exact: true })).toBeVisible();
  await expect(detail.locator('[data-part="account-cooldowns"]')).toContainText("rate-limited");
  await expect(detail.locator('[data-part="account-check"]')).toContainText("the test provider answered");
  await expect(detail.getByRole("button", { name: "Refresh OAuth credential", exact: true })).toHaveCount(0);
  await expect(detail.getByRole("button", { name: "Sign in again", exact: true })).toHaveCount(0);
  const shots = process.env.ACCOUNT_DETAIL_SHOTS;
  if (shots) mkdirSync(shots, { recursive: true });
  for (const theme of ["default", "terminal"]) {
    await page.locator('nav a[href="#/themes"]').click();
    await page
      .getByRole("radiogroup", { name: "Theme" })
      .getByRole("radio", { name: theme, exact: false })
      .check();
    detail = await openAccount(page, "tools-key");
    await expect(detail.getByText("Cooling", { exact: true })).toBeVisible();
    if (shots) await page.screenshot({ path: join(shots, `${theme}-cooldown.png`), fullPage: true });
    await detail.getByRole("button", { name: "Models", exact: true }).click();
    const models = detail.locator('[data-part="account-models"]');
    await expect(models).toContainText("Supported models");
    for (const id of ["fake-model", "rate-limited", "server-error"]) await expect(models).toContainText(id);
    await detail.getByRole("button", { name: "Test (spends one request)", exact: true }).click();
    const result = detail.locator('[data-part="account-test-result"]');
    await expect(result).toContainText("v=0.2.0 pid=");
    await expect(result).toContainText("fake-model");
    await expect(result).toContainText(/[0-9]+ ms/);
    if (shots) await page.screenshot({ path: join(shots, `${theme}-test.png`), fullPage: true });
    await expect(detail.getByText("Cooling", { exact: true })).toBeVisible();
  }
  await detail.getByRole("button", { name: "Clear cooldown", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Clear cooldown", exact: true });
  await expect(dialog).toContainText("It does not restore upstream quota.");
  await dialog.getByRole("button", { name: "Clear cooldown", exact: true }).click();
  await dialog.getByRole("button", { name: "Yes, clear cooldown", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(detail.getByText("Cooling", { exact: true })).toHaveCount(0);
  const r = await core.mgmt<{ accounts: { id: string; cooldowns?: unknown[] }[] }>("GET", "/accounts");
  expect(r.body.accounts.find((a) => a.id === "tools-key")?.cooldowns ?? []).toEqual([]);
  await detail.getByLabel("Test model").selectOption("server-error");
  await detail.getByRole("button", { name: "Test (spends one request)", exact: true }).click();
  await expect(detail.locator('[data-part="account-test-result"]')).toContainText(
    "the test provider failed this request",
  );
});

test("config models and credential tools follow core metadata", async ({ page }) => {
  await addAccount("tools-oauth", "device");
  await openPanel(page, core);
  await signIn(page, core);
  let detail = await openAccount(page, "alpha-1");
  await expect(detail.getByRole("link", { name: "Edit in Config Panel" })).toHaveAttribute(
    "href",
    "#/config",
  );
  await expect(detail.getByRole("button", { name: "Refresh OAuth credential" })).toHaveCount(0);
  await expect(detail.getByRole("button", { name: "Sign in again", exact: true })).toHaveCount(0);
  await detail.getByRole("button", { name: "Models", exact: true }).click();
  await expect(detail.locator('[data-part="account-models"]')).toContainText("alpha-model");
  detail = await openAccount(page, "tools-oauth");
  await detail.getByRole("button", { name: "Refresh OAuth credential", exact: true }).click();
  await expect(detail.getByRole("status")).toContainText("OAuth credential refreshed.");
  await expect(detail.getByRole("button", { name: "Sign in again", exact: true })).toHaveCount(0);
  // The fake has no auth-failure model. Supply core's reauth state at the HTTP edge;
  // sign-in still goes through the real core and provider.
  await page.route("**/v0/management/accounts", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    for (const account of body.accounts) if (account.id === "tools-oauth") account.state = "reauth_required";
    await route.fulfill({ response, json: body });
  });
  await goTo(page, "Add Account");
  detail = await openAccount(page, "tools-oauth");
  await detail.getByRole("button", { name: "Sign in again", exact: true }).click();
  await detail.getByRole("button", { name: "Test browser", exact: true }).click();
  await expect(detail.getByLabel("Callback URL")).toBeVisible();
  await detail.getByLabel("Callback URL").fill("http://localhost/callback?code=ok");
  await detail.getByRole("button", { name: "Submit Callback URL" }).click();
  await expect(detail.getByRole("status")).toContainText("Authentication successful!");
  await expect(detail.getByRole("button", { name: "Log in another account" })).toHaveCount(0);
});
