// The panel in a real browser, served by a real core with fake extensions (next/e2e/panelcore).
// Run it with `bun run test:browser`, which builds the panel first.
import { mkdirSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

interface Seen {
  url: string;
  auth?: string;
}

/** Opens the panel, records every request the page makes and every CSP report or page error. */
async function open(page: Page, path = "/panel/") {
  const requests: Seen[] = [];
  const problems: string[] = [];
  page.on("request", (r) => {
    requests.push({ url: r.url(), auth: r.headers().authorization });
  });
  page.on("pageerror", (e) => {
    problems.push(`page error: ${e.message}`);
  });
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  await openPanel(page, core, path);
  return { requests, problems };
}

const root = (page: Page) => page.locator("html");

test("panel shows core, switches layout and theme live, and recovers from custom CSS", async ({ page }) => {
  const { requests, problems } = await open(page);
  await signIn(page, core);

  // Shared pages appear in their navigation groups.
  await expect(
    nav(page).getByRole("group", { name: "Operate" }).getByRole("link", { name: "Dashboard" }),
  ).toBeVisible();
  await expect(
    nav(page).getByRole("group", { name: "Control" }).getByRole("link", { name: "Themes" }),
  ).toBeVisible();

  // Dashboard: the extensions core runs, from its status.
  const main = page.locator('[data-part="main"]');
  await expect(main.getByText("test/local", { exact: true })).toBeVisible();
  await expect(main.getByText("test/alpha", { exact: true })).toBeVisible();

  // Accounts: config accounts of both extensions.
  await goTo(page, "Accounts");
  const accounts = page.locator('[data-part="list"]');
  for (const id of ["local-1", "alpha-1", "alpha-2"]) {
    await expect(accounts.locator('[data-part="item"]', { hasText: id })).toBeVisible();
  }

  // Extensions: the installed packages, one enabled and running, one disabled.
  await goTo(page, "Extensions");
  const packages = page.locator('[data-part="list"][aria-label="Installed extensions"]');
  const alpha = packages.locator('[data-part="item"]', { hasText: "test/alpha" });
  const beta = packages.locator('[data-part="item"]', { hasText: "test/beta" });
  await expect(alpha).toBeVisible();
  await expect(alpha.locator('[data-part="badge"]')).toHaveText("active");
  await expect(beta.locator('[data-part="badge"]')).toHaveText("disabled");

  // Themes: switching a layout option applies at once, without reloading the page.
  await goTo(page, "Themes");
  await page.evaluate(() => {
    (window as unknown as { marker: number }).marker = 1;
  });
  await expect(root(page)).toHaveAttribute("data-nav", "sidebar");
  const sideBox = await nav(page).boundingBox();
  expect(sideBox && sideBox.height > sideBox.width).toBe(true);

  await page.getByRole("radiogroup", { name: "Navigation" }).getByRole("radio", { name: "Top" }).check();
  await expect(root(page)).toHaveAttribute("data-nav", "top");
  const topBox = await nav(page).boundingBox();
  expect(topBox && topBox.width > topBox.height && topBox.y < 10).toBe(true);
  expect(await page.evaluate(() => (window as unknown as { marker?: number }).marker)).toBe(1);

  await page.getByRole("radiogroup", { name: "Lists" }).getByRole("radio", { name: "Cards" }).check();
  await expect(root(page)).toHaveAttribute("data-list", "cards");

  // Another theme changes the tokens; the person's layout choice stays.
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const before = await bg();
  await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Terminal" }).check();
  await expect.poll(bg).not.toBe(before);
  await expect(root(page)).toHaveAttribute("data-nav", "top");

  // Custom CSS applies live; safe mode turns it off and on again.
  const title = page.locator('[data-part="page-title"]');
  const titleColor = () => title.evaluate((e) => getComputedStyle(e).color);
  await page.getByLabel("Your CSS").fill('[data-part="page-title"] { color: rgb(255, 0, 0); }');
  await expect.poll(titleColor).toBe("rgb(255, 0, 0)");
  const safe = page.getByRole("switch", { name: "Safe mode" });
  await safe.check();
  await expect.poll(titleColor).not.toBe("rgb(255, 0, 0)");
  await safe.uncheck();
  await expect.poll(titleColor).toBe("rgb(255, 0, 0)");

  // Custom CSS cannot load anything from elsewhere: the CSP refuses it, so nothing is sent.
  // A canary server on another origin counts what reaches it.
  let reached = 0;
  const canary = createServer((_, res) => {
    reached++;
    res.end();
  });
  await new Promise<void>((resolve) => canary.listen(0, "127.0.0.1", resolve));
  const outside = `http://127.0.0.1:${(canary.address() as AddressInfo).port}/leak.png`;
  await page.getByLabel("Your CSS").fill(`[data-part="main"] { background-image: url(${outside}); }`);
  await expect
    .poll(() => problems.some((p) => p.includes("Content Security Policy") && p.includes(outside)))
    .toBe(true);
  const refused = problems.filter((p) => p.includes(outside));
  problems.splice(0, problems.length, ...problems.filter((p) => !p.includes(outside)));
  expect(refused.length).toBeGreaterThan(0);
  expect(reached).toBe(0);
  canary.close();

  // CSS that hides the navigation: ?safe in the address brings it back, and safe mode stays on.
  await page.getByLabel("Your CSS").fill('[data-part="nav"] { display: none; }');
  await expect(nav(page)).toBeHidden();
  await openPanel(page, core, "/panel/?safe");
  await expect(nav(page)).toBeVisible();
  await page.reload();
  await expect(nav(page)).toBeVisible();
  await goTo(page, "Themes");
  await expect(page.getByRole("switch", { name: "Safe mode" })).toBeChecked();

  // The management key went to core's own management API and nowhere else, and the page
  // reached no other origin.
  const keyed = requests.filter((r) => r.auth !== undefined);
  expect(keyed.length).toBeGreaterThan(0);
  for (const r of keyed) {
    expect(r.url.startsWith(`${core.base}/v0/management/`), r.url).toBe(true);
    expect(r.auth).toBe(`Bearer ${core.key}`);
  }
  for (const r of requests) {
    if (r.url !== outside) expect(new URL(r.url).origin, r.url).toBe(core.base);
  }
  expect(requests.some((r) => r.url.includes(core.key))).toBe(false);
  expect(problems).toEqual([]);
});

test("screenshots in two themes", async ({ page }) => {
  const dir = process.env.PANEL_SHOTS_DIR;
  test.skip(!dir, "set PANEL_SHOTS_DIR to take screenshots");
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await open(page);
  await signIn(page, core);
  for (const [theme, file] of [
    ["Default", "panel-default.png"],
    ["Terminal", "panel-terminal.png"],
  ] as const) {
    await goTo(page, "Themes");
    await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: theme }).check();
    await goTo(page, "Accounts");
    await page.locator('[data-part="item"]', { hasText: "alpha-1" }).click();
    await expect(page.locator('[data-part="detail"]')).toBeVisible();
    await page.screenshot({ path: join(dir, file), fullPage: true, animations: "disabled" });
  }
  await goTo(page, "Themes");
  await page.screenshot({ path: join(dir, "panel-themes.png"), fullPage: true, animations: "disabled" });
});
