import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

test("quota shows when the subscription ends", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Quota Management");
  const card = page.locator('[data-part="item"]').filter({ hasText: "quota-a" });
  await card.getByRole("button", { name: "Refresh Quota", exact: true }).click();
  await expect(card.getByText(/^Subscription ends /)).toBeVisible();
  if (process.env.RENEWAL_SHOTS) {
    await card.screenshot({ path: `${process.env.RENEWAL_SHOTS}/default.png` });
  }
});

test.beforeAll(async () => {
  for (const id of ["quota-a", "quota-b"]) {
    const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
      id,
      extension: "test/alpha",
      method: "api_key",
      input: { api_key: `test-${id}` },
    });
    expect(started.status).toBe(202);
    await expect
      .poll(async () => {
        const r = await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`);
        return r.body.login.state;
      })
      .toBe("done");
  }
});

test("provider errors stay on the card and unsupported accounts disappear", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Quota Management");
  const a = page.locator('[data-part="item"]').filter({ hasText: "quota-a" });
  let unsupported = false;
  await page.route("**/v0/management/accounts/quota-a/quota", async (route) => {
    await route.fulfill({
      status: unsupported ? 422 : 502,
      contentType: "application/json",
      body: JSON.stringify({
        error: unsupported
          ? { code: "unsupported", message: "This provider reads no quota" }
          : { code: "provider_unavailable", message: "The provider could not read this plan" },
      }),
    });
  });
  await a.getByRole("button", { name: "Refresh Quota", exact: true }).click();
  await expect(a.getByRole("alert")).toHaveText("The provider could not read this plan");
  await expect(a.getByRole("button", { name: "Refresh Quota", exact: true })).toBeEnabled();
  unsupported = true;
  await a.getByRole("button", { name: "Refresh Quota", exact: true }).click();
  await expect(a).toHaveCount(0);
});

test("quota is read on request and shows plan, limits and reset times", async ({ page }) => {
  const reads: string[] = [];
  page.on("request", (r) => {
    if (/\/accounts\/[^/]+\/quota$/.test(r.url())) reads.push(r.url());
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Quota Management");
  const items = page.locator('[data-part="item"]');
  const a = items.filter({ hasText: "quota-a" });
  await expect(a.getByRole("button", { name: "Refresh Quota", exact: true })).toBeVisible();
  expect(reads).toEqual([]);
  await a.getByRole("button", { name: "Refresh Quota", exact: true }).click();
  await expect(a.getByText("Test plan", { exact: true })).toBeVisible();
  await expect(a.getByRole("meter", { name: "5-hour limit", exact: true })).toHaveAttribute(
    "aria-valuenow",
    "42",
  );
  await expect(a.getByText("42%", { exact: true })).toBeVisible();
  await expect(a.locator('time[datetime="2033-05-18T03:33:20.000Z"]')).toBeVisible();
  await expect(a.getByText(/resets in/)).toHaveCount(2);
  expect(reads).toHaveLength(1);
  await page.getByRole("button", { name: "Refresh all credentials", exact: true }).click();
  const b = items.filter({ hasText: "quota-b" });
  await expect(b.getByText("Test plan", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Refresh all credentials" })).toBeEnabled();
  await page.getByLabel("Sort", { exact: true }).selectOption("soonest");
  // testext gives both accounts the same reset times: ties retain the default account order.
  const ids = await items.allTextContents();
  expect(ids.findIndex((text) => text.includes("quota-a"))).toBeLessThan(
    ids.findIndex((text) => text.includes("quota-b")),
  );
  await page.getByLabel("Search accounts", { exact: true }).fill("quota-b");
  await expect(items).toHaveCount(1);
  await expect(items.first()).toContainText("quota-b");
  await page.getByLabel("Search accounts", { exact: true }).fill("quota-");

  if (process.env.QUOTA_SHOTS) {
    for (const theme of ["default", "terminal", "paper"]) {
      await page.locator('nav a[href="#/themes"]').click();
      await page.getByRole("radio", { name: new RegExp(`^${theme}`, "i") }).check();
      await page.locator('nav a[href="#/quota"]').click();
      await page.getByRole("button", { name: "Refresh all credentials", exact: true }).click();
      await expect(page.getByRole("button", { name: "Refresh all credentials" })).toBeEnabled();
      await page.getByLabel("Search accounts", { exact: true }).fill("quota-");
      await expect(page.getByText("Test plan", { exact: true })).toHaveCount(2);
      await page.screenshot({ path: `${process.env.QUOTA_SHOTS}/${theme}.png`, fullPage: true });
    }
  }
});
