import { expect, test } from "@playwright/test";
import { accountRow, goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

// One list for both pages. test/alpha runs in this core; ghost/gone is a provider that is not
// installed (an account imported before its provider was installed).
const later = new Date(Date.now() + 3_600_000).toISOString();
const base = { source: "login", state: "active", refreshable: false, extension: "test/alpha" };
const accounts = [
  { ...base, id: "ok-1" },
  { ...base, id: "cool-1", cooldowns: [{ scope: "account", kind: "rate_limit", until: later }] },
  { ...base, id: "banned-1", health: { state: "banned", why: "the provider closed this account" } },
  { ...base, id: "off-1", disabled: true },
  { ...base, id: "err-1", last_error: { code: "rate_limit", message: "429 too many requests" } },
  { ...base, id: "ghost-1", extension: "ghost/gone" },
];

test.beforeEach(async ({ page }) => {
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { accounts } });
  });
});

test("Accounts and the Dashboard give the same accounts the same numbers", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  // Serving: ok-1 and err-1. Waiting: cool-1 (it comes back by itself). Needs you: banned-1, and
  // ghost-1, whose provider is not running. Turned off: off-1.
  await expect(
    page.getByText("6 accounts at 2 providers · 2 need you · 2 serving · 1 waiting · 1 turned off"),
  ).toBeVisible();
  const ghost = accountRow(page.getByRole("region", { name: "Needs you" }), "ghost-1");
  await expect(ghost.getByRole("img", { name: "Provider stopped" })).toBeVisible();
  await expect(ghost).toContainText("Gone is not running, so nothing reaches this account.");
  await ghost.getByRole("link", { name: "ghost-1", exact: true }).click();
  await expect(page.getByRole("main")).toContainText("Gone is not running, so nothing reaches this account.");
  await goTo(page, "Dashboard");
  const health = page.getByRole("region", { name: "Credential status", exact: true });
  await expect(health.locator("dl")).toContainText("Available3");
  await expect(health.locator("dl")).toContainText("Unavailable2");
  await expect(health.locator("dl")).toContainText("Disabled1");
  const credentials = page.locator('[data-part="stat"]').filter({ hasText: "Credentials" });
  await expect(credentials).toContainText("3 available · 3 not serving");
  await expect(page.getByRole("link", { name: "3 not serving" })).toHaveAttribute("href", "#/accounts");
});
