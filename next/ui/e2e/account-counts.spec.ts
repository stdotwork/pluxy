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

test("Accounts and Home give the same accounts the same numbers", async ({ page }) => {
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

  await goTo(page, "Home");
  // Home sorts the same six: banned-1 and ghost-1 need the person, cool-1 waits (it serves other
  // models, but its cooldown is on the whole account), ok-1 and err-1 serve, off-1 is turned off.
  const group = (name: string) => page.getByRole("region", { name, exact: true });
  await expect(page.getByRole("heading", { level: 2, name: "2 accounts need you." })).toBeVisible();
  await expect(page.locator(".home-counts")).toHaveText("1 waiting · 2 serving · 1 turned off");
  for (const [name, ids] of [
    ["Needs you", ["banned-1", "ghost-1"]],
    ["Waiting", ["cool-1"]],
    ["Serving", ["ok-1", "err-1"]],
    ["Turned off", ["off-1"]],
  ] as const) {
    await expect(group(name).locator('[data-part="account-row"]')).toHaveCount(ids.length);
    for (const id of ids)
      await expect(group(name).getByRole("link", { name: id, exact: true })).toBeVisible();
  }
  await expect(group("Needs you").locator('[data-part="account-row"]', { hasText: "ghost-1" })).toContainText(
    "Open Extensions",
  );
});
