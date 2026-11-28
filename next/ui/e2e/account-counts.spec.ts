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
  // Home sorts the same six: banned-1 and ghost-1 need the person and are in the band with their
  // fixes; the others are under their provider, Alpha: cool-1 first (it waits: its cooldown is on
  // the whole account), then ok-1 and err-1, which serve, then off-1, turned off.
  await expect(page.getByRole("heading", { level: 2, name: "2 accounts need you." })).toBeVisible();
  await expect(page.locator(".home-counts")).toHaveText("1 waiting · 2 serving · 1 turned off");
  const band = page.locator('[data-part="needs-band"]');
  await expect(band).toHaveAttribute("aria-label", "Needs you");
  await expect(band.locator('[data-part="account-row"]')).toHaveCount(2);
  for (const id of ["banned-1", "ghost-1"])
    await expect(band.getByRole("link", { name: id, exact: true })).toBeVisible();
  await expect(band.locator('[data-part="account-row"]', { hasText: "ghost-1" })).toContainText(
    "Open Extensions",
  );
  const alpha = page.locator(".home-group").and(page.getByRole("region", { name: "Alpha", exact: true }));
  const rows = alpha.locator('[data-part="account-row"]');
  await expect(rows).toHaveCount(4);
  const expected = [
    ["cool-1", "waiting"],
    ["ok-1", "serving"],
    ["err-1", "serving"],
    ["off-1", "off"],
  ] as const;
  for (const [i, [id, group]] of expected.entries()) {
    await expect(rows.nth(i).getByRole("link", { name: id, exact: true })).toBeVisible();
    await expect(rows.nth(i)).toHaveAttribute("data-group", group);
  }
  // Each provider's pool counts all of its accounts (including turned-off as unable to serve):
  // two of Alpha's five serve, and Gone's one cannot.
  await expect(page.locator('[data-part="pool-card"][data-provider="alpha"]')).toContainText(
    "2 of 5 serving",
  );
  await expect(page.locator('[data-part="pool-card"][data-provider="gone"]')).toContainText(
    "Nothing is serving",
  );
});
