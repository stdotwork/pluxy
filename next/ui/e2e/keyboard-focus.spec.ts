// Keyboard use of the panel: where focus starts, where it goes when a page, a detail, a dialog or
// a confirm closes, and what a keyboard can scroll. Each test drives the keys a person would press.
import { expect, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function addAccount(id: string) {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "ak-keyboard-focus-test" },
  });
  expect(r.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
}

test.beforeAll(async () => {
  await addAccount("kb-one");
  await addAccount("kb-two");
  // One request, so Home has a day with a request.
  const r = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "fake-model", messages: [{ role: "user", content: "hi" }] }),
  });
  expect(r.status).toBe(200);
  await r.arrayBuffer();
});

async function openAccounts(page: Page) {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  await expect(page.getByRole("button", { name: "kb-one", exact: true })).toBeVisible();
}

/** A description of the focused element, for failure messages. */
const focused = (page: Page) =>
  page.evaluate(() => {
    const el = document.activeElement;
    if (!el) return "nothing";
    const name = el.getAttribute("aria-label") ?? el.textContent?.trim().slice(0, 40) ?? "";
    return `${el.tagName.toLowerCase()} "${name}"`;
  });

test("the first Tab stop is a link that skips to the main content", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.keyboard.press("Tab");
  const skip = page.getByRole("link", { name: "Skip to main content", exact: true });
  await expect(skip, `focus is on ${await focused(page)}`).toBeFocused();
  await expect(skip).toBeInViewport();
  await page.keyboard.press("Enter");
  // Focus moves into the main area, so the next Tab reaches its first control, not the navigation.
  await expect(page.locator("main")).toBeFocused();
  await page.keyboard.press("Tab");
  expect(await page.evaluate(() => document.activeElement?.closest("nav") !== null)).toBe(false);
  expect(await page.evaluate(() => document.activeElement?.closest("main") !== null)).toBe(true);
});

test("Tab goes through the frame in reading order: the navigation, the status block, the header strip, then the page", async ({
  page,
}) => {
  await openPanel(page, core, "/panel/#/routing");
  await signIn(page, core);
  await expect(page.getByRole("heading", { level: 1, name: "Routing" })).toBeVisible();
  const host = new URL(core.base).host;
  // Each stop's name, until focus is inside the page.
  const stops: string[] = [];
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press("Tab");
    const stop = await page.evaluate(() => {
      const el = document.activeElement;
      if (!el || el.closest("main")) return null;
      return el.getAttribute("aria-label") ?? el.textContent?.trim() ?? "";
    });
    if (stop === null) break;
    stops.push(stop);
  }
  expect(stops).toEqual([
    "Skip to main content",
    "pluxy",
    "Home",
    "Accounts",
    "Activity",
    "Tools",
    "Settings",
    `Running, ${host}. Open the menu`,
    "Accounts",
    "Order & fallback",
    "Limits",
    "Refresh",
  ]);
  expect(await page.evaluate(() => document.activeElement?.closest("main") !== null)).toBe(true);
});

test("Escape closes the account detail and focus returns to the row that opened it", async ({ page }) => {
  await openAccounts(page);
  const row = page.getByRole("button", { name: "kb-one", exact: true });
  await row.focus();
  await page.keyboard.press("Enter");
  const detail = page.getByRole("complementary", { name: "Details: kb-one", exact: true });
  await expect(detail).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(detail).toHaveCount(0);
  await expect(row, `focus is on ${await focused(page)}`).toBeFocused();

  // Also with focus on a control inside the detail.
  await page.keyboard.press("Enter");
  await expect(detail).toBeFocused();
  await detail.getByRole("button", { name: "Disable", exact: true }).focus();
  await page.keyboard.press("Escape");
  await expect(detail).toHaveCount(0);
  await expect(row, `focus is on ${await focused(page)}`).toBeFocused();

  // The Close button returns focus the same way.
  await page.keyboard.press("Enter");
  await detail.getByRole("button", { name: "Close details", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(detail).toHaveCount(0);
  await expect(row, `focus is on ${await focused(page)}`).toBeFocused();
});

test("Escape in a dialog opened from the detail does not close the detail", async ({ page }) => {
  await openAccounts(page);
  await page.getByRole("button", { name: "kb-one", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: kb-one", exact: true });
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Edit kb-one", exact: true });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(detail).toBeVisible();
});

test("closing the Edit dialog returns focus to the Edit button", async ({ page }) => {
  await openAccounts(page);
  await page.getByRole("button", { name: "kb-one", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: kb-one", exact: true });
  await expect(detail).toBeFocused();
  const edit = detail.getByRole("button", { name: "Edit", exact: true });
  await edit.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Edit kb-one", exact: true });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(edit, `focus is on ${await focused(page)}`).toBeFocused();

  // Closed with its Close button too.
  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Close", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(edit, `focus is on ${await focused(page)}`).toBeFocused();
});

test("closing the remove confirm returns focus to the Remove button", async ({ page }) => {
  await openAccounts(page);
  await page.getByRole("button", { name: "Select filtered", exact: true }).click();
  const del = page.getByRole("button", { name: "Remove", exact: true });
  await del.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Are you sure you want to remove");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(del, `focus is on ${await focused(page)}`).toBeFocused();

  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(del, `focus is on ${await focused(page)}`).toBeFocused();
});

test("cancelling the inline Remove confirm returns focus to Remove", async ({ page }) => {
  await openAccounts(page);
  await page.getByRole("button", { name: "kb-two", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: kb-two", exact: true });
  await expect(detail).toBeFocused();
  const remove = detail.getByRole("button", { name: "Remove", exact: true });
  await remove.focus();
  await page.keyboard.press("Enter");
  await detail.getByRole("button", { name: "Cancel", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(remove, `focus is on ${await focused(page)}`).toBeFocused();
});

// Focus may reach the browser's own controls (the document's body) when it leaves the first or last
// control, which the platform does for any modal dialog; it must never land on the page behind it.
test("Tab and Shift+Tab never reach the page behind an open dialog", async ({ page }) => {
  await openAccounts(page);
  await page.getByRole("button", { name: "kb-one", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: kb-one", exact: true });
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Edit kb-one", exact: true });
  await expect(dialog).toBeVisible();
  const outside: string[] = [];
  for (const key of ["Tab", "Shift+Tab"]) {
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press(key);
      const inside = await page.evaluate(
        () => document.activeElement === document.body || document.activeElement?.closest("dialog") !== null,
      );
      if (!inside) outside.push(`${key} ${i + 1}: ${await focused(page)}`);
    }
  }
  expect(outside).toEqual([]);
});

test("a page change moves focus to the page heading and announces the title", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  const link = page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Activity", exact: true });
  await link.focus();
  await page.keyboard.press("Enter");
  const heading = page.getByRole("heading", { level: 1, name: "Activity: Requests", exact: true });
  await expect(heading).toBeVisible();
  await expect(heading, `focus is on ${await focused(page)}`).toBeFocused();
  await expect(page.locator('[data-part="page-announcer"]')).toHaveText("Requests · pluxy");
  await expect(page).toHaveTitle(/^Requests · /);

  // A second change announces the new title in the same place.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Accounts", exact: true })
    .click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts", exact: true })).toBeFocused();
  await expect(page.locator('[data-part="page-announcer"]')).toHaveText("Accounts · pluxy");
  await expect(page.locator('[data-part="page-announcer"]')).toHaveAttribute("aria-live", "polite");
});

test("opening the panel is not a page change, so it moves no focus", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  // Opening the panel is not a page change: the skip link is still the first Tab stop.
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to main content", exact: true })).toBeFocused();
});

test("Home's day bars take focus and say their day, and the log text scrolls from the keyboard", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Home");
  const bars = page.locator('[data-part="days"] .day');
  await expect(bars).toHaveCount(7);
  await bars.last().focus();
  await expect(bars.last()).toBeFocused();
  await expect(page.locator(".days-readout")).toContainText("1 request, 0 failed");
  await bars.first().focus();
  await expect(page.locator(".days-readout")).toContainText(": 0 requests, 0 failed");

  await goTo(page, "Logs Viewer");
  const coreLog = page.getByRole("region", { name: "Core logs", exact: true });
  // Its lines load after the page opens: wait for them, so no later render replaces the region.
  await expect(coreLog.locator('[data-part="log-line"]').first()).toBeVisible();
  await expect(coreLog).toHaveAttribute("tabindex", "0");
  await coreLog.focus();
  await expect(coreLog).toBeFocused();
});
