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
  await expect(row.locator('[data-part="badge"]')).toHaveText("banned");
  await expect(page.getByText(/· 1 problem$/)).toBeVisible();
  await row.getByRole("button", { name: "shut-out-1", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: shut-out-1" });
  await expect(detail.locator('[data-part="badge"]').first()).toHaveText("banned");
  await expect(detail).toContainText("the test provider closed this account");
  await expect(detail).toContainText("No request is sent to this account until you enable it again.");
  await detail.getByRole("button", { name: "Enable", exact: true }).click();
  await expect(row.locator('[data-part="badge"]')).toHaveText("active");
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

test("the accounts table scrolls inside its frame in a narrow window", async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 800 });
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.locator('[data-part="item"]').first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);
});
