// Design direction A (kit/designs/a) in a real browser on a real core: it loads under the panel's
// CSP with its own styles and font, its three screens read core, the command menu and the keys
// move around, and a page it has not redesigned shows inside its shell.
import { expect, type Page, test } from "@playwright/test";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

/** Page errors and console errors: a CSP refusal of a style, font or script is among them. */
function problemsOf(page: Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

test("direction A shows core's accounts and requests, and is driven from the keyboard", async ({ page }) => {
  const problems = problemsOf(page);
  const sent = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "fake-model", messages: [{ role: "user", content: "hello" }] }),
  });
  expect(sent.status).toBe(200);

  await openPanel(page, core, "/panel/?design=a");
  await signIn(page, core);
  expect(await page.evaluate(() => document.documentElement.dataset.design)).toBe("a");
  // Its own stylesheet applies (the rail is a narrow column) and its font file loaded.
  expect((await nav(page).boundingBox())?.width).toBe(48);
  await expect
    .poll(() =>
      page.evaluate(() =>
        [...document.fonts].some((f) => f.family.includes("Plex") && f.status === "loaded"),
      ),
    )
    .toBe(true);

  // Overview: the board lists core's accounts with their state, the tail the request just sent.
  const board = page.getByRole("table", { name: "Accounts" });
  await expect(board.getByRole("row", { name: /local-1/ })).toContainText("serving");
  await expect(page.getByRole("region", { name: "Recent requests" })).toContainText("fake-model");
  // The account's usage windows are read once its row shows.
  await expect(
    board
      .getByRole("row", { name: /local-1/ })
      .getByRole("meter")
      .first(),
  ).toBeVisible();

  // The command menu finds an account by a few letters and opens it.
  await page.keyboard.press("Control+k");
  const menu = page.getByRole("dialog", { name: "Command menu" });
  await expect(menu).toBeVisible();
  await page.keyboard.type("alpha-2");
  await expect(menu.getByRole("option").first()).toContainText("alpha-2");
  await page.keyboard.press("Enter");
  await expect(menu).toHaveCount(0);
  await expect(page).toHaveURL(/#\/accounts\/alpha-2$/);
  await expect(page.getByRole("complementary", { name: "Account alpha-2" })).toContainText("Serving.");

  // Keys: Escape closes the account, g r goes to Requests, 2 is its list view.
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/#\/accounts$/);
  await page.keyboard.press("g");
  await page.keyboard.press("r");
  await expect(page.getByRole("region", { name: "By model" })).toContainText("fake-model");
  await page.keyboard.press("2");
  await expect(page.getByRole("table", { name: "Requests, newest first" })).toContainText("fake-model");

  // A page that is not redesigned is today's, inside this shell.
  await nav(page).getByRole("link", { name: "Routing" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Routing", exact: true })).toBeVisible();
  await expect(nav(page).getByRole("link", { name: "Routing" })).toHaveAttribute("aria-current", "page");

  expect(problems).toEqual([]);
});
