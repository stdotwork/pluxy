// Times and states as a person reads them: relative times that keep themselves current, plain
// words for core's states, and the next step for an account that gets no requests.
import { expect, test } from "@playwright/test";
import { accountRow, goTo, openPanel, signIn, useCore } from "./support";

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

const mark = (page: import("@playwright/test").Page, id: string) =>
  accountRow(page, id).locator(".group-mark").getByRole("img");

test("a state shows in plain words, and core's own word for it nowhere", async ({ page }) => {
  const states = {
    "relogin-1": "Needs sign-in",
    "used-up-1": "Limit used up",
    "off-1": "Turned off",
    "fine-1": "Serving",
  };
  for (const [id, words] of Object.entries(states)) await expect(mark(page, id)).toHaveAccessibleName(words);
  await expect(accountRow(page, "relogin-1")).toHaveAttribute("data-condition", "needs_login");
  await expect(page.getByRole("main")).not.toContainText(/reauth_required|needs_login/);
});

test("a time reads by its distance, keeps the exact time as its title and stays current", async ({
  page,
}) => {
  await accountRow(page, "relogin-1").getByRole("link", { name: "relogin-1", exact: true }).click();
  const added = page.getByRole("region", { name: "About" }).locator('time[datetime="2026-11-24T15:24:30Z"]');
  await expect(added).toHaveText("5m ago");
  await expect(added).toHaveAttribute("title", /\d{4}.*\d:24:30/);
  // An hour on, the same element says so: nothing was drawn again from scratch.
  await added.evaluate((el) => {
    (el as HTMLElement).dataset.same = "yes";
  });
  await page.clock.fastForward("01:00:00");
  await expect(added).toHaveText("1h ago");
  await expect(added).toHaveAttribute("data-same", "yes");
});

test("an account that gets no requests says why on its page, with the control that fixes it", async ({
  page,
}) => {
  const opening = page.locator('[data-part="account-opening"]');
  await accountRow(page, "relogin-1").getByRole("link", { name: "relogin-1", exact: true }).click();
  await expect(opening).toContainText("Alpha no longer accepts its sign-in.");
  await expect(page.getByRole("region", { name: "Sign in again", exact: true })).toHaveCount(0);
  await opening.getByRole("link", { name: "Sign in again", exact: true }).click();
  await expect(page.getByRole("region", { name: "Sign in again", exact: true })).toBeVisible();

  await page.goto(`${core.base}/panel/#/accounts/used-up-1`);
  await expect(opening).toContainText(/Its limit is used up\. Back at \d\d:\d\d, in 2h 5m\./);
  await expect(page.getByRole("region", { name: "Sign in again", exact: true })).toHaveCount(0);

  await page.goto(`${core.base}/panel/#/accounts/off-1`);
  await expect(opening).toContainText("You turned it off.");
  await expect(opening.getByRole("button", { name: "Turn on", exact: true })).toBeVisible();
});
