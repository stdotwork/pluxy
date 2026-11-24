// Times and states as a person reads them: relative times that keep themselves current, plain
// words for core's states, and the next step for an account that gets no requests.
import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

const NOW = "2026-11-24T15:30:00Z";
const login = { source: "login", refreshable: false, extension: "test/alpha", kind: "oauth" };
const accounts = [
  { ...login, id: "relogin-1", state: "reauth_required", created_at: "2026-11-24T15:24:30Z" },
  {
    ...login,
    id: "used-up-1",
    state: "active",
    health: { state: "exhausted", until: "2026-11-24T17:34:50Z", why: "the weekly limit ran out" },
  },
  { ...login, id: "off-1", state: "active", disabled: true, health: { state: "disabled" } },
  { ...login, id: "fine-1", state: "active", health: { state: "ok" } },
];

test.beforeEach(async ({ page }) => {
  await page.clock.install({ time: new Date(NOW) });
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { accounts } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
});

const row = (page: import("@playwright/test").Page, id: string) =>
  page.locator('[data-part="item"]').filter({ hasText: id });

test("a state shows in plain words and keeps what core sent for support", async ({ page }) => {
  const states = {
    "relogin-1": "Needs sign-in",
    "used-up-1": "Exhausted",
    "off-1": "Disabled",
    "fine-1": "Available",
  };
  for (const [id, words] of Object.entries(states)) {
    await expect(row(page, id).locator('[data-part="badge"]')).toHaveText(words);
  }
  const badge = row(page, "relogin-1").locator('[data-part="badge"]');
  await expect(badge).toHaveAttribute("data-state", "reauth_required");
  await expect(badge).toHaveAttribute("title", "reauth_required");
  await expect(page.getByRole("main")).not.toContainText(/reauth_required|needs_login/);
});

test("a time reads by its distance, keeps the exact time as its title and stays current", async ({
  page,
}) => {
  await row(page, "relogin-1").getByRole("button", { name: "relogin-1", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: relogin-1" });
  const added = detail.locator('time[datetime="2026-11-24T15:24:30Z"]');
  await expect(added).toHaveText("5m ago");
  await expect(added).toHaveAttribute("title", /\d{4}.*\d:24:30/);
  // An hour on, the same element says so: nothing was loaded again and the detail stayed open.
  await added.evaluate((el) => {
    (el as HTMLElement).dataset.same = "yes";
  });
  await page.clock.fastForward("01:00:00");
  await expect(added).toHaveText("1h ago");
  await expect(added).toHaveAttribute("data-same", "yes");
});

test("an account that gets no requests says what to do, beside the control that does it", async ({
  page,
}) => {
  await row(page, "relogin-1").getByRole("button", { name: "relogin-1", exact: true }).click();
  const relogin = page.getByRole("complementary", { name: "Details: relogin-1" });
  const step = relogin.getByRole("status").filter({ hasText: "Sign in again to use it." });
  await expect(step).toContainText("The provider refused this account's sign-in, so it gets no requests.");
  await expect(relogin.getByRole("region", { name: "Sign in again", exact: true })).toHaveCount(0);
  await step.getByRole("button", { name: "Sign in again", exact: true }).click();
  await expect(relogin.getByRole("region", { name: "Sign in again", exact: true })).toBeInViewport();

  await row(page, "used-up-1").getByRole("button", { name: "used-up-1", exact: true }).click();
  const usedUp = page.getByRole("complementary", { name: "Details: used-up-1" });
  await expect(usedUp).toContainText(
    "This account's limit is used up, so it gets no requests until the limit resets (in 2h 5m).",
  );
  // The sign-in form belongs to the account it was opened for.
  await expect(usedUp.getByRole("region", { name: "Sign in again", exact: true })).toHaveCount(0);

  await row(page, "off-1").getByRole("button", { name: "off-1", exact: true }).click();
  const off = page.getByRole("complementary", { name: "Details: off-1" });
  await expect(off).toContainText(
    "This account is disabled, so it gets no requests. Enable it to use it again.",
  );
  await expect(off.getByRole("button", { name: "Enable", exact: true })).toBeVisible();
});
