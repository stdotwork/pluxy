// All accounts: Find an account, Select and its actions, and the Quota page's old address.
import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { accountRow, goTo, openPanel, pageTitle, signIn, useCore } from "./support";

const core = useCore();

const listed = async () => (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts;

async function addKeys(prefix: string, count: number) {
  for (let n = 1; n <= count; n++) {
    const suffix = String(n).padStart(2, "0");
    expect(
      (
        await core.mgmt("POST", "/accounts", {
          id: `${prefix}-${suffix}`,
          extension: "test/local",
          method: "api_key",
          // Named after its ID: the test provider calls every signed-in account "Test account".
          input: { api_key: `k-${suffix}`, label: `${prefix}-${suffix}` },
        })
      ).status,
    ).toBe(202);
  }
  await expect
    .poll(async () => (await listed()).filter((a) => a.id.startsWith(`${prefix}-`)).length)
    .toBe(count);
}

for (const theme of ["default", "terminal"]) {
  test(`Find an account, Select and its actions in ${theme}`, async ({ page }) => {
    await addKeys("bulk", 8);
    await page.addInitScript((id) => localStorage.setItem("pluxy.theme", `pluxy/${id}`), theme);
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Accounts");
    await expect(page.getByText("11 accounts at 2 providers · 11 serving", { exact: true })).toBeVisible();
    const serving = page.getByRole("region", { name: "Serving" });
    await expect(serving.locator('[data-part="account-row"]')).toHaveCount(11);

    // Find an account matches a public ID and a provider's name.
    const find = page.getByPlaceholder("Find an account");
    const bulk = (await listed()).find((a) => a.id === "bulk-05");
    await find.fill(bulk?.public_id ?? "");
    await expect(page.locator('[data-part="account-row"]')).toHaveCount(1);
    await expect(accountRow(page, "bulk-05")).toBeVisible();
    await find.fill("alpha");
    await expect(page.locator('[data-part="account-row"]')).toHaveCount(2);
    await find.fill("no-such-account");
    await expect(page.getByText('No account matches "no-such-account".')).toBeVisible();
    await page.getByRole("button", { name: "Clear search", exact: true }).click();
    await expect(page.locator('[data-part="account-row"]')).toHaveCount(11);

    // Select: tick some, turn them off, then on; the settings-file account is skipped and said so.
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const bar = page.getByRole("toolbar", { name: "Actions for the selected accounts" });
    for (const id of ["bulk-01", "bulk-02", "alpha-1"])
      await page.getByRole("checkbox", { name: `Select ${id}`, exact: true }).check();
    await expect(bar).toContainText("3 selected");
    await bar.getByRole("button", { name: "Turn off", exact: true }).click();
    await expect(page.getByText("Skipped 1 account set in pluxy's settings file.")).toBeVisible();
    await expect
      .poll(async () =>
        (await listed())
          .filter((a) => a.disabled)
          .map((a) => a.id)
          .sort(),
      )
      .toEqual(["bulk-01", "bulk-02"]);
    await bar.getByRole("button", { name: "Done", exact: true }).click();
    const off = page.getByRole("region", { name: "Turned off" });
    await expect(off.locator('[data-part="account-row"]')).toHaveCount(2);
    if (process.env.P2A_SHOTS)
      await page.screenshot({ path: `${process.env.P2A_SHOTS}/${theme}-list.png`, fullPage: true });

    await page.getByRole("button", { name: "Select", exact: true }).click();
    for (const id of ["bulk-01", "bulk-02"])
      await page.getByRole("checkbox", { name: `Select ${id}`, exact: true }).check();
    await bar.getByRole("button", { name: "Turn on", exact: true }).click();
    await expect.poll(async () => (await listed()).filter((a) => a.disabled).length).toBe(0);

    // Remove… asks first, and leaves the settings-file account alone.
    for (const id of ["bulk-03", "alpha-2"])
      await page.getByRole("checkbox", { name: `Select ${id}`, exact: true }).check();
    await bar.getByRole("button", { name: "Remove…", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Remove 4 accounts?" });
    await expect(dialog).toContainText("pluxy deletes its saved sign-in. To use it again, add it again.");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    expect((await listed()).length).toBe(11);
    await bar.getByRole("button", { name: "Remove…", exact: true }).click();
    await dialog.getByRole("button", { name: "Remove", exact: true }).click();
    await expect
      .poll(async () => (await listed()).map((a) => a.id).sort())
      .toEqual(
        ["alpha-1", "alpha-2", "local-1", ...["04", "05", "06", "07", "08"].map((n) => `bulk-${n}`)].sort(),
      );
    await expect(page.getByText("Skipped 1 account set in pluxy's settings file.")).toBeVisible();
    await bar.getByRole("button", { name: "Done", exact: true }).click();
    for (const n of ["04", "05", "06", "07", "08"])
      expect((await core.mgmt("DELETE", `/accounts/bulk-${n}`)).status).toBe(204);
  });
}

test("Select acts on one account after another and keeps every failure on screen", async ({ page }) => {
  await addKeys("failure", 4);
  let active = 0;
  let most = 0;
  await page.route("**/v0/management/accounts/failure-*", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    active++;
    most = Math.max(most, active);
    const id = route.request().url().split("/").at(-1);
    if (id === "failure-01" || id === "failure-03") {
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "conflict", message: `Update refused for ${id}` } }),
      });
    } else {
      const response = await route.fetch();
      await route.fulfill({ response });
    }
    active--;
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  await page.getByRole("button", { name: "Select", exact: true }).click();
  for (let n = 1; n <= 4; n++)
    await page.getByRole("checkbox", { name: `Select failure-0${n}`, exact: true }).check();
  await page.getByRole("toolbar").getByRole("button", { name: "Turn off", exact: true }).click();
  await expect
    .poll(async () => (await page.getByRole("alert").allTextContents()).sort())
    .toEqual(["failure-01: Update refused for failure-01", "failure-03: Update refused for failure-03"]);
  expect(most).toBe(1);
  expect(
    (await listed())
      .filter((a) => a.id.startsWith("failure-") && a.disabled)
      .map((a) => a.id)
      .sort(),
  ).toEqual(["failure-02", "failure-04"]);
  for (let n = 1; n <= 4; n++)
    expect((await core.mgmt("DELETE", `/accounts/failure-0${n}`)).status).toBe(204);
});

test("the Quota page's old address lands on All accounts", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/quota`);
  await expect(pageTitle(page)).toHaveText("Accounts");
  await expect(
    page.locator('[data-part="header-tabs"]').getByRole("link", { name: "All accounts" }),
  ).toHaveAttribute("aria-current", "page");
  await expect(page.locator('[data-part="account-row"]').first()).toBeVisible();
});

test("an account set in the settings file offers no Remove… on its page; one added here asks first", async ({
  page,
}) => {
  await addKeys("gone", 1);
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts/alpha-1`);
  await expect(page.getByRole("main")).toContainText("Set in pluxy's settings file.");
  await expect(page.getByRole("button", { name: "Remove…", exact: true })).toHaveCount(0);
  await page.goto(`${core.base}/panel/#/accounts/gone-01`);
  const remove = page.getByRole("button", { name: "Remove…", exact: true });
  await remove.click();
  const dialog = page.getByRole("dialog", { name: "Remove gone-01?" });
  await expect(dialog).toContainText("pluxy deletes its saved sign-in. To use it again, add it again.");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(remove).toBeFocused();
  expect((await listed()).some((a) => a.id === "gone-01")).toBe(true);
  await remove.click();
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page).toHaveURL(/#\/accounts$/);
  await expect.poll(async () => (await listed()).some((a) => a.id === "gone-01")).toBe(false);
});

test("provider chips filter with a pool card, and List | Cards stores an Accounts-only choice without changing other lists", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");

  // Two providers (Local and Alpha): chips show counts and filter the list with a pool card.
  const chips = page.getByRole("group", { name: "Filter by provider" });
  await expect(chips.getByRole("button", { name: /^All\s+3$/ })).toHaveAttribute("aria-pressed", "true");
  await expect(chips.getByRole("button", { name: /Alpha\s+2/ })).toBeVisible();
  await expect(chips.getByRole("button", { name: /Local\s+1/ })).toBeVisible();
  await expect(page.locator('[data-part="pool-card"]')).toHaveCount(0);

  await chips.getByRole("button", { name: /Alpha/ }).click();
  await expect(chips.getByRole("button", { name: /Alpha/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".accounts-counts")).toHaveText("2 Alpha accounts · 2 serving");
  await expect(page.locator('[data-part="pool-card"][data-provider="alpha"]')).toContainText(
    "2 of 2 serving",
  );
  await expect(page.locator('[data-part="account-row"]')).toHaveCount(2);
  await expect(accountRow(page, "alpha-1")).toBeVisible();
  await expect(accountRow(page, "alpha-2")).toBeVisible();

  await chips.getByRole("button", { name: /^All/ }).click();
  await expect(page.locator(".accounts-counts")).toHaveText("3 accounts at 2 providers · 3 serving");
  await expect(page.locator('[data-part="pool-card"]')).toHaveCount(0);
  await expect(page.locator('[data-part="account-row"]')).toHaveCount(3);

  // List | Cards switches the Accounts view, persists in Accounts-only storage across reload, and
  // leaves the kit-wide list layout (html[data-list] and other pages' tables) untouched.
  const viewSwitch = page.getByRole("group", { name: "Account view" });
  const listBtn = viewSwitch.getByRole("button", { name: "List", exact: true });
  const cardsBtn = viewSwitch.getByRole("button", { name: "Cards", exact: true });
  await expect(listBtn).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", "list");
  await expect(page.locator("html")).toHaveAttribute("data-list", "table");

  await cardsBtn.click();
  await expect(cardsBtn).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", "cards");
  await expect(page.locator("html")).toHaveAttribute("data-list", "table");
  await expect(accountRow(page, "alpha-1")).toBeVisible();

  // Other pages that use List stay tables (System's models list).
  await page.goto(`${core.base}/panel/#/system`);
  await expect(page.locator("html")).toHaveAttribute("data-list", "table");
  await expect(page.locator("table.table").first()).toBeVisible();

  // Returning to Accounts and reloading restores the stored Accounts-only Cards choice.
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", "cards");
  await page.reload();
  await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", "cards");
  await expect(cardsBtn).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("html")).toHaveAttribute("data-list", "table");

  // Narrow viewport in Cards view fits without horizontal scroll.
  await page.setViewportSize({ width: 560, height: 800 });
  await expect(accountRow(page, "alpha-1")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);

  await listBtn.click();
  await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", "list");
  await expect(page.locator("html")).toHaveAttribute("data-list", "table");
});
