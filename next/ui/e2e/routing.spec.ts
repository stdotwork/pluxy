import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import type { CoreSettings } from "../kit/api/settings";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();
test.beforeAll(async () => {
  for (const [id, priority] of [["route-top", 10], ["route-middle", 0], ["route-bottom", -5]] as const) {
    const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
      id, priority, extension: "test/alpha", method: "api_key", input: { api_key: "routing-test-key" },
    });
    expect(started.status).toBe(202);
    await expect.poll(async () =>
      (await core.mgmt<{ login: { state: string } }>("GET", "/logins/" + started.body.login.id)).body.login.state,
    ).toBe("done");
  }
});

test("routing saves strategy and priorities and shows live model cooling", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  await page.getByRole("combobox", { name: "Within a tier", exact: true }).selectOption("fill-first");
  await expect.poll(async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body.routing.strategy).toBe("fill-first");
  const middle = page.locator('[data-part="item"]').filter({ hasText: "route-middle" });
  await middle.getByRole("button", { name: "Move up a tier", exact: true }).click();
  await expect.poll(async () => (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.find((a) => a.id === "route-middle")?.priority).toBe(10);
  await expect(page.locator('[data-part="tier"]').first()).toContainText("route-middle");
  await expect(page.locator('[data-part="tier"]').first()).toContainText("Preferred");
  await middle.getByRole("button", { name: "Set weight…", exact: true }).click();
  const weight = middle.getByRole("spinbutton", { name: "Set weight…", exact: true });
  await weight.fill("3");
  await weight.press("Tab");
  await expect(middle.getByText("Weight 3", { exact: true })).toBeVisible();
  const reply = await fetch(core.base + "/v1/chat/completions", {
    method: "POST", headers: { Authorization: "Bearer " + core.clientKey, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "rate-limited", messages: [{ role: "user", content: "Hi" }] }),
  });
  expect(reply.status).toBe(429);
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(middle.getByText(/^Cooling /)).toBeVisible();
  const configured = page.locator('[data-part="item"]').filter({ hasText: "Set in config" });
  await expect(configured.first()).toBeVisible();
  for (const row of await configured.all()) {
    await expect(row.getByRole("button", { name: "Move up a tier", exact: true })).toBeDisabled();
    await expect(row.getByRole("button", { name: "Set weight…", exact: true })).toBeDisabled();
  }
});

for (const theme of ["default", "terminal"]) {
  test("routing three tiers and cooling in " + theme, async ({ page }) => {
    await page.addInitScript((id) => localStorage.setItem("pluxy.theme", "pluxy/" + id), theme);
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Routing");
    await expect(page.locator('[data-part="tier"]')).toHaveCount(3);
    await expect(page.getByText(/^Cooling /).first()).toBeVisible();
    if (process.env.ROUTING_SHOTS) await page.screenshot({ path: process.env.ROUTING_SHOTS + "/" + theme + ".png", fullPage: true });
  });
}
