import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

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
  // Active: ok-1, cool-1 (cooling is a short block, it still serves) and err-1. Problem: banned-1 and
  // ghost-1, whose provider is not running. Disabled: off-1.
  await expect(page.locator('[data-part="account-meta"]')).toHaveText(
    "6 credentials · 3 available · 2 problems",
  );
  const ghost = page.locator('[data-part="item"]').filter({ hasText: "ghost-1" });
  await expect(ghost).toContainText("no provider");
  await ghost.click();
  await expect(page.getByText("ghost/gone is not running, so this account gets no requests.")).toBeVisible();
  await goTo(page, "Dashboard");
  const health = page.getByRole("region", { name: "Credential status", exact: true });
  await expect(health.locator("dl")).toContainText("Available3");
  await expect(health.locator("dl")).toContainText("Unavailable2");
  await expect(health.locator("dl")).toContainText("Disabled1");
  const credentials = page.locator('[data-part="stat"]').filter({ hasText: "Credentials" });
  await expect(credentials).toContainText("3 available · 3 not serving");
  await expect(page.getByRole("link", { name: "3 not serving" })).toHaveAttribute("href", "#/accounts");
});
