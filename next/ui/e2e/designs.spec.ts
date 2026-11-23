// The design directions' switch (kit/designs): ?design= picks one, the switcher moves between
// them, and with none set the panel loads nothing of them. It looks only at the switcher, which
// every direction shares, so it holds whatever a direction draws.
import { expect, type Page, test } from "@playwright/test";
import { openPanel, useCore } from "./support";

const core = useCore();

const switcher = (page: Page) => page.getByRole("navigation", { name: "Design direction" });

/** Records what would show a broken load: page errors and console errors (CSP refusals are among them). */
function problemsOf(page: Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`page error: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  return problems;
}

test("with no design set the panel is today's and asks for no design file", async ({ page }) => {
  const scripts: string[] = [];
  page.on("request", (r) => {
    if (/\.(js|css)$/.test(new URL(r.url()).pathname)) scripts.push(new URL(r.url()).pathname);
  });
  await openPanel(page, core);
  await expect(page.getByLabel("Management key")).toBeVisible();
  await expect(switcher(page)).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.dataset.design)).toBeUndefined();
  expect(scripts.filter((s) => /\/(mount|app|switcher)-/.test(s))).toEqual([]);
});

test("?design= picks a direction, the switcher moves between them and back to today's panel", async ({
  page,
}) => {
  const problems = problemsOf(page);
  await openPanel(page, core, "/panel/?design=b#/accounts");
  await expect(switcher(page)).toBeVisible();
  await expect(switcher(page).getByRole("link", { name: "B", exact: true })).toHaveAttribute(
    "aria-current",
    "true",
  );
  expect(await page.evaluate(() => document.documentElement.dataset.design)).toBe("b");
  // The parameter is taken out of the address; the page it named stays.
  expect(new URL(page.url()).search).toBe("");
  expect(new URL(page.url()).hash).toBe("#/accounts");

  // It is remembered: the next load needs no parameter.
  await page.reload();
  await expect(switcher(page).getByRole("link", { name: "B", exact: true })).toHaveAttribute(
    "aria-current",
    "true",
  );

  await switcher(page).getByRole("link", { name: "C", exact: true }).click();
  await expect(switcher(page).getByRole("link", { name: "C", exact: true })).toHaveAttribute(
    "aria-current",
    "true",
  );
  expect(await page.evaluate(() => document.documentElement.dataset.design)).toBe("c");
  expect(new URL(page.url()).hash).toBe("#/accounts");

  await switcher(page).getByRole("link", { name: "current" }).click();
  await expect(page.getByLabel("Management key")).toBeVisible();
  await expect(switcher(page)).toHaveCount(0);
  expect(await page.evaluate(() => localStorage.getItem("pluxy.design"))).toBeNull();
  expect(problems).toEqual([]);
});
