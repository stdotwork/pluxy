import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();
const searchText = "Search name, account, type, provider, or error (403, 429, invalid). Use * as a wildcard";

for (const theme of ["default", "terminal"]) {
  test(`accounts search, paging and batch actions in ${theme}`, async ({ page }) => {
    for (let n = 1; n <= 30; n++) {
      const suffix = String(n).padStart(2, "0");
      expect(
        (
          await core.mgmt("POST", "/accounts", {
            id: `bulk-${suffix}`,
            extension: "test/local",
            method: "api_key",
            input: { api_key: `k-${suffix}` },
          })
        ).status,
      ).toBe(202);
    }
    await expect
      .poll(async () => (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.length)
      .toBe(33);
    await page.addInitScript((id) => localStorage.setItem("pluxy.theme", `pluxy/${id}`), theme);
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Accounts");
    await page.getByLabel("Per page", { exact: true }).selectOption("10");
    await expect(page.getByText("Page 1 / 4 · 33 accounts", { exact: true })).toBeVisible();
    await expect(page.locator('[data-part="item"]')).toHaveCount(10);
    await expect(page.getByRole("tab", { name: "test/local (31)", exact: true })).toBeVisible();
    if (process.env.P7_SHOTS) {
      await page.getByLabel("Per page", { exact: true }).selectOption("50");
      await page.screenshot({ path: `${process.env.P7_SHOTS}/${theme}-list.png`, fullPage: true });
      await page.getByLabel("Per page", { exact: true }).selectOption("10");
    }
    await page.getByRole("tab", { name: "test/alpha (2)", exact: true }).click();
    await expect(page.locator('[data-part="item"]')).toHaveCount(2);
    await page.getByRole("tab", { name: "All (33)", exact: true }).click();
    await page.getByLabel("Source", { exact: true }).selectOption("config");
    await expect(page.locator('[data-part="item"]')).toHaveCount(3);
    await page.getByLabel("Source", { exact: true }).selectOption("login");
    await expect(page.getByText("Page 1 / 3 · 30 accounts", { exact: true })).toBeVisible();
    await page.getByLabel("Source", { exact: true }).selectOption("");
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await expect(page.getByText("Page 2 / 4 · 33 accounts", { exact: true })).toBeVisible();
    await page.getByPlaceholder(searchText).fill("bulk-17");
    await expect(page.locator('[data-part="item"]')).toHaveCount(1);
    await expect(page.getByRole("button", { name: "bulk-17", exact: true })).toBeVisible();
    if (process.env.P7_SHOTS)
      await page.screenshot({ path: `${process.env.P7_SHOTS}/${theme}-filtered.png`, fullPage: true });
    await page.getByPlaceholder(searchText).fill("bulk-*");
    await expect(page.getByText("Page 1 / 3 · 30 accounts", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Select filtered", exact: true }).click();
    await expect(page.getByText("30 selected", { exact: true })).toBeVisible();
    if (process.env.P7_SHOTS)
      await page.screenshot({ path: `${process.env.P7_SHOTS}/${theme}-batch.png`, fullPage: true });
    await page.getByRole("button", { name: "Disable", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.filter(
            (a) => a.id.startsWith("bulk-") && a.disabled,
          ).length,
      )
      .toBe(30);
    await page.getByLabel("Status Filter", { exact: true }).selectOption("disabled");
    await expect(page.getByText("Page 1 / 3 · 30 accounts", { exact: true })).toBeVisible();
    await expect(page.locator('[data-part="item"]')).toHaveCount(10);
    await page.getByRole("button", { name: "Enable", exact: true }).click();
    await expect(page.getByText("No results", { exact: true })).toBeVisible();
    expect(
      (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts
        .filter((a) => a.id.startsWith("bulk-"))
        .every((a) => !a.disabled),
    ).toBe(true);
    await page.getByLabel("Status Filter", { exact: true }).selectOption("all");
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Are you sure you want to delete 30 accounts?");
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await expect
      .poll(async () =>
        (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.map((a) => a.id).sort(),
      )
      .toEqual(["alpha-1", "alpha-2", "local-1"]);
    await expect(page.getByText("No results", { exact: true })).toBeVisible();
    await page.getByPlaceholder(searchText).fill("");
    await page.getByLabel("Status Filter", { exact: true }).selectOption("all");
    await page.getByRole("button", { name: "Select filtered", exact: true }).click();
    await page.getByRole("button", { name: "Disable", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Skipped 3 config accounts");
    expect(
      (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.every((a) => !a.disabled),
    ).toBe(true);
    await page.getByRole("button", { name: "Clear cooldown", exact: true }).click();
    await expect(page.getByRole("status")).toHaveText("3 accounts updated");
  });
}

test("batch actions limit concurrency and keep every failure visible", async ({ page }) => {
  for (let n = 1; n <= 7; n++) {
    expect(
      (
        await core.mgmt("POST", "/accounts", {
          id: `failure-${n}`,
          extension: "test/local",
          method: "api_key",
          input: { api_key: `k-${n}` },
        })
      ).status,
    ).toBe(202);
  }
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.filter((a) =>
          a.id.startsWith("failure-"),
        ).length,
    )
    .toBe(7);
  let active = 0;
  let maximum = 0;
  let entered = 0;
  let release!: () => void;
  const firstThree = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v0/management/accounts/failure-*", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    active++;
    entered++;
    maximum = Math.max(maximum, active);
    if (entered === 3) release();
    await firstThree;
    const id = route.request().url().split("/").at(-1);
    if (id === "failure-1" || id === "failure-5") {
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
  await page.getByPlaceholder(searchText).fill("failure-*");
  await page.getByRole("button", { name: "Select filtered", exact: true }).click();
  await page.getByRole("button", { name: "Disable", exact: true }).click();
  await expect(page.getByRole("status")).toHaveText("5 accounts updated");
  await expect
    .poll(async () => (await page.getByRole("alert").allTextContents()).sort())
    .toEqual(["failure-1: Update refused for failure-1", "failure-5: Update refused for failure-5"]);
  expect(entered).toBe(7);
  expect(maximum).toBe(3);
  const result = (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts;
  expect(
    result
      .filter((a) => a.id.startsWith("failure-") && a.disabled)
      .map((a) => a.id)
      .sort(),
  ).toEqual(["failure-2", "failure-3", "failure-4", "failure-6", "failure-7"]);
  for (let n = 1; n <= 7; n++) expect((await core.mgmt("DELETE", `/accounts/failure-${n}`)).status).toBe(204);
});
