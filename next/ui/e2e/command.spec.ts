// The command menu: the Search field in the navigation and Control K open it; it lists pages,
// accounts, actions and settings; the arrow keys, Enter and Escape work and focus goes back; it
// opens at once when the keyboard opens it; and names are masked in it as everywhere else.
import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { goTo, nav, openPanel, pageTitle, signIn, useCore } from "./support";

const core = useCore();

/** The Search field in the navigation. */
const field = (page: Page) => nav(page).getByRole("button", { name: "Search" });
const menu = (page: Page) => page.getByRole("dialog", { name: "Search pluxy" });
const input = (page: Page) => menu(page).getByRole("combobox");
const rows = (page: Page) => menu(page).getByRole("option");
const active = (page: Page) => menu(page).locator('[role="option"][aria-selected="true"]');

/** Opens the menu with Control K and waits until what is typed next goes into its field. */
async function open(page: Page) {
  await page.keyboard.press("Control+k");
  await expect(input(page)).toBeFocused();
}

const EMAIL = "command.owner@example.com";

const LONG = "alpha-shared-research-workspace-account-with-a-deliberately-very-long-name-0123456789";

async function addAccount(id: string, label: string) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/local",
    method: "api_key",
    input: { api_key: `k-${id}`, label },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`)).body.login
          .state,
    )
    .toBe("done");
}

test.beforeAll(async () => {
  await addAccount("command-mail", EMAIL);
  await addAccount("command-long", LONG);
});

test.beforeEach(async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
});

test("the Search field opens it, Escape closes it and focus goes back to the field", async ({ page }) => {
  await expect(field(page)).toBeVisible();
  await expect(field(page)).toContainText("Ctrl K");
  await field(page).click();
  await expect(menu(page)).toBeVisible();
  await expect(input(page)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu(page)).toHaveCount(0);
  await expect(field(page)).toBeFocused();
});

test("Control K opens it from anywhere, and the same keys close it with focus back where it was", async ({
  page,
}) => {
  const refresh = page.locator('[data-part="header-actions"]').getByRole("button", { name: "Refresh" });
  await refresh.focus();
  await page.keyboard.press("Control+k");
  await expect(menu(page)).toBeVisible();
  await expect(input(page)).toBeFocused();
  await page.keyboard.press("Control+k");
  await expect(menu(page)).toHaveCount(0);
  await expect(refresh).toBeFocused();
});

test("a click on a heading or the footer leaves focus in the field, so Escape still closes it", async ({
  page,
}) => {
  await open(page);
  await menu(page).getByText("Pages", { exact: true }).click();
  await expect(input(page)).toBeFocused();
  await menu(page)
    .locator(".command-foot")
    .click({ position: { x: 4, y: 4 } });
  await expect(input(page)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(menu(page)).toHaveCount(0);
});

test("the shortcut held down opens the menu once and does not close it with its repeats", async ({
  page,
}) => {
  await page.keyboard.press("Control+k");
  await expect(input(page)).toBeFocused();
  // What the platform sends while the keys stay down: the same key again, marked as a repeat.
  const closed = await page.evaluate(() => {
    const repeat = () =>
      document.activeElement?.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "k",
          ctrlKey: true,
          repeat: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    repeat();
    repeat();
    return !document.querySelector('[data-part="command"]');
  });
  expect(closed).toBe(false);
  await expect(menu(page)).toBeVisible();
  // A fresh press still closes it.
  await page.keyboard.press("Control+k");
  await expect(menu(page)).toHaveCount(0);
});

test("a menu opened by the keyboard is there at once, and one opened by the pointer fades in", async ({
  page,
}) => {
  const animation = () => menu(page).evaluate((el) => getComputedStyle(el).animationName);
  await page.keyboard.press("Control+k");
  await expect(menu(page)).toBeVisible();
  expect(await animation()).toBe("none");
  await page.keyboard.press("Escape");
  await field(page).click();
  await expect(menu(page)).toBeVisible();
  expect(await animation()).toBe("command-in");
  await page.keyboard.press("Escape");
  // Enter on the focused field is the keyboard's, not the pointer's.
  await field(page).focus();
  await page.keyboard.press("Enter");
  await expect(menu(page)).toBeVisible();
  expect(await animation()).toBe("none");
});

test("with nothing typed it offers the destinations and the actions", async ({ page }) => {
  await open(page);
  await expect(rows(page).first()).toBeVisible();
  const names = await rows(page).locator(".command-label").allInnerTexts();
  expect(names).toEqual([
    "Home",
    "Accounts",
    "Activity",
    "Tools",
    "Settings",
    "Copy address for tools",
    "Add an account",
    "Refresh this page",
    "Switch to dark appearance",
  ]);
  await expect(menu(page).getByRole("group", { name: "Pages" })).toBeVisible();
  await expect(menu(page).getByRole("group", { name: "Actions" })).toBeVisible();
});

test("arrow keys move through the rows and round the ends, and Enter opens the one they are on", async ({
  page,
}) => {
  await page.keyboard.press("Control+k");
  await expect(active(page)).toHaveText(/Home/);
  await page.keyboard.press("ArrowDown");
  await expect(active(page)).toHaveText(/Accounts/);
  await expect(input(page)).toHaveAttribute(
    "aria-activedescendant",
    (await active(page).getAttribute("id")) ?? "",
  );
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("ArrowUp");
  await expect(active(page)).toHaveText(/Switch to dark appearance/);
  await page.keyboard.press("ArrowDown");
  await expect(active(page)).toHaveText(/Home/);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(menu(page)).toHaveCount(0);
  await expect(pageTitle(page)).toHaveText("Activity");
  await expect(page).toHaveURL(/#\/usage$/);
});

test("typing finds pages, tabs and settings topics, with what was found marked", async ({ page }) => {
  await open(page);
  await page.keyboard.type("them");
  // The appearance switch is found by "theme" too, after the page it is named for.
  await expect(rows(page).first()).toContainText("Themes");
  await expect(rows(page).first().locator("mark")).toHaveText("Them");
  await expect(menu(page).getByRole("group", { name: "Settings" })).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(pageTitle(page)).toHaveText("Themes");

  await open(page);
  await page.keyboard.type("order");
  await expect(rows(page).first()).toContainText("Order & fallback");
  await expect(rows(page).first()).toContainText("Accounts");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "Order & fallback" })).toBeVisible();
});

test("an account is found by name and opens its page", async ({ page }) => {
  const accounts = (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts;
  const first = accounts.find((a) => a.id !== "command-mail" && a.label);
  const name = first?.label as string;
  expect(name).toBeTruthy();
  await open(page);
  await page.keyboard.type(name);
  await expect(menu(page).getByRole("group", { name: "Accounts" })).toBeVisible();
  const row = rows(page).filter({ hasText: name }).first();
  await expect(row).toContainText("Serving");
  await row.click();
  await expect(menu(page)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`#/accounts/${encodeURIComponent(first?.id as string)}$`));
});

test("nothing matching says so and offers no row to open", async ({ page }) => {
  await open(page);
  await page.keyboard.type("zzzqx");
  await expect(menu(page)).toContainText("Nothing matches “zzzqx”.");
  await expect(rows(page)).toHaveCount(0);
  await expect(input(page)).not.toHaveAttribute("aria-activedescendant", /.+/);
  await page.keyboard.press("Enter");
  await expect(menu(page)).toBeVisible();
});

test("while accounts are read, a wait that is noticed shows rows in their shape, and the pages already answer", async ({
  page,
}) => {
  await page.route(/\/v0\/management\/accounts$/, async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1200));
    await route.continue();
  });
  await open(page);
  await page.keyboard.type("acc");
  await expect(menu(page).locator('[data-part="command-loading"]')).toBeVisible();
  await expect(rows(page).first()).toContainText("Accounts");
  await expect(menu(page).locator('[data-part="command-loading"]')).toHaveCount(0, { timeout: 5000 });
});

test("accounts that cannot be read say so, and the menu offers to try again", async ({ page }) => {
  let fail = true;
  await page.route(/\/v0\/management\/accounts$/, (route) =>
    fail ? route.fulfill({ status: 500, json: { error: { message: "boom" } } }) : route.continue(),
  );
  await open(page);
  await page.keyboard.type("xyz");
  await expect(menu(page)).toContainText("Couldn't read accounts.");
  fail = false;
  await page.keyboard.press("Enter");
  await expect(menu(page)).toBeVisible();
  await expect(menu(page)).toContainText("Nothing matches");
  await expect(menu(page)).not.toContainText("Couldn't read accounts.");
});

test("what was opened is offered first the next time", async ({ page }) => {
  await open(page);
  await page.keyboard.type("system");
  await page.keyboard.press("Enter");
  await expect(pageTitle(page)).toHaveText("System");
  await page.keyboard.press("Control+k");
  await expect(menu(page).getByRole("group", { name: "Recent" })).toBeVisible();
  await expect(rows(page).first()).toContainText("System");
  // It stays after a reload: it is kept on this device.
  await page.reload();
  await expect(nav(page)).toBeVisible();
  await open(page);
  await expect(rows(page).first()).toContainText("System");
});

test("Switch to dark appearance changes the appearance, and the menu then offers to switch back", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light" });
  await open(page);
  await page.keyboard.type("dark");
  await page.keyboard.press("Enter");
  await expect(menu(page)).toHaveCount(0);
  await expect(page.locator("html")).toHaveAttribute("data-appearance", "dark");
  await open(page);
  await page.keyboard.type("light");
  await expect(rows(page).first()).toContainText("Switch to light appearance");
  await page.keyboard.press("Enter");
  await expect(page.locator("html")).toHaveAttribute("data-appearance", "light");
});

test("Add an account opens that page", async ({ page }) => {
  await open(page);
  await page.keyboard.type("add an");
  await page.keyboard.press("Enter");
  await expect(pageTitle(page)).toHaveText("Add an account");
});

test("Copy address for tools puts pluxy's address on the clipboard and says Copied", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await open(page);
  await page.keyboard.type("copy addr");
  await page.keyboard.press("Enter");
  await expect(menu(page).getByRole("status").filter({ hasText: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(core.base);
  await expect(menu(page)).toHaveCount(0);
});

test("Refresh this page shows the page again, and is offered only on a page that shows what core holds", async ({
  page,
}) => {
  // What the Accounts page reads besides the accounts (the menu reads those itself when it opens).
  let reads = 0;
  await page.route(/\/v0\/management\/usage\?since=7d/, (route) => {
    reads++;
    return route.continue();
  });
  await goTo(page, "Accounts");
  await expect(page.locator('[data-part="account-row"]').first()).toBeVisible();
  const before = reads;
  await open(page);
  await page.keyboard.type("refresh");
  await page.keyboard.press("Enter");
  await expect(menu(page)).toHaveCount(0);
  await expect.poll(() => reads, { timeout: 5000 }).toBeGreaterThan(before);
  // The page that makes a new account is a form: nothing to read again, nothing to lose.
  await page.goto(`${core.base}/panel/#/add-account`);
  await open(page);
  await page.keyboard.type("refresh");
  await expect(menu(page)).toContainText("Nothing matches");
});

test("Refresh this page is not offered on an account's Sign in again, where a sign-in may be under way", async ({
  page,
}) => {
  await page.goto(`${core.base}/panel/#/accounts/command-mail/sign-in`);
  await expect(pageTitle(page)).toBeVisible();
  await open(page);
  await page.keyboard.type("refresh");
  await expect(menu(page)).toContainText("Nothing matches");
  // The same account's own page shows what core holds, and offers it.
  await page.keyboard.press("Escape");
  await page.goto(`${core.base}/panel/#/accounts/command-mail`);
  await open(page);
  await page.keyboard.type("refresh");
  await expect(rows(page)).toHaveCount(1);
});

test("names are masked in it while names are hidden, and the real name still finds the account", async ({
  page,
}) => {
  await goTo(page, "General");
  await page.getByRole("switch", { name: "Hide names" }).check();
  await open(page);
  await page.keyboard.type("command.owner");
  await expect(rows(page)).toHaveCount(1);
  const shown = await menu(page).evaluate((el) =>
    [
      el.textContent,
      ...[...el.querySelectorAll("[title], [aria-label]")].flatMap((e) => [
        e.getAttribute("title"),
        e.getAttribute("aria-label"),
      ]),
    ].join("\n"),
  );
  expect(shown).not.toContain(EMAIL);
  expect(shown).not.toContain("command.owner");
  await expect(rows(page).first()).toContainText("***");
});

test("it does not open over a question that waits for an answer", async ({ page }) => {
  await goTo(page, "Config");
  await page.evaluate(() => {
    const dialog = document.createElement("dialog");
    dialog.dataset.part = "dialog";
    dialog.textContent = "A question";
    document.body.append(dialog);
    dialog.showModal();
  });
  await page.keyboard.press("Control+k");
  await expect(menu(page)).toHaveCount(0);
});

for (const width of [800, 540]) {
  test(`at ${width} pixels wide the Search field is an icon with a name, and it opens the menu`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await expect(field(page)).toBeVisible();
    const box = await field(page).boundingBox();
    expect(box?.width).toBeLessThan(60);
    await field(page).click();
    await expect(menu(page)).toBeVisible();
    const m = await menu(page).boundingBox();
    expect(m?.x).toBeGreaterThanOrEqual(0);
    expect((m?.x ?? 0) + (m?.width ?? 0)).toBeLessThanOrEqual(width);
    await page.keyboard.type("themes");
    await page.keyboard.press("Enter");
    await expect(pageTitle(page)).toHaveText("Themes");
  });
}

test("a very long account name stays inside the menu", async ({ page }) => {
  await open(page);
  await page.keyboard.type("alpha-shared");
  await expect(rows(page).filter({ hasText: LONG }).first()).toBeVisible();
  const overflow = await menu(page).evaluate((el) => el.scrollWidth > el.clientWidth);
  expect(overflow).toBe(false);
  const list = await menu(page)
    .locator('[data-part="command-list"]')
    .evaluate((el) => el.scrollWidth > el.clientWidth);
  expect(list).toBe(false);
});
