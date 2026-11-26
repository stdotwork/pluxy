// The frame fits what it holds at the window sizes the app is checked at, in the default theme
// and in Paper (the navigation on top, larger type): the status menu shows pluxy's whole address,
// port included; on top it opens below the bar; and every control in the header strip lies inside
// the strip, above its border and the current tab's line.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, signIn, useCore } from "./support";

const core = useCore();

/** The address the panel is opened at: a five-digit port, as pluxy's usually is. */
const ADDRESS = "http://127.0.0.1:39703";

/**
 * Opens the panel as if pluxy answered at ADDRESS, so its status menu names that address: each
 * request to ADDRESS is answered by the spec's core.
 */
async function openAt(page: Page) {
  await page.route(`${ADDRESS}/**`, async (route) => {
    const url = new URL(route.request().url());
    const response = await route.fetch({ url: core.base + url.pathname + url.search });
    await route.fulfill({ response });
  });
  await page.goto(`${ADDRESS}/panel/`);
  await signIn(page, core);
}

type Box = { x: number; y: number; width: number; height: number };

/** The header strip's box, its bottom border's width, and the boxes of the controls in it. */
function strip(page: Page) {
  return page.locator('[data-part="header"]').evaluate((header) => {
    const box = (el: Element): Box => {
      const r = el.getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, height: r.height };
    };
    const shown = (el: Element) => el.getBoundingClientRect().width > 0;
    const controls = [...header.querySelectorAll("a, button, input, select")].filter(shown);
    const tab = (el: Element) => el.matches('[data-part="header-tab"]');
    return {
      strip: box(header),
      border: Number.parseFloat(getComputedStyle(header).borderBottomWidth),
      controls: controls
        .filter((el) => !tab(el))
        .map((el) => ({ name: el.textContent || el.getAttribute("aria-label"), ...box(el) })),
      tabs: controls.filter(tab).map((el) => ({
        name: el.textContent,
        ...box(el),
      })),
    };
  });
}

/** Every control lies inside the strip, above its border; the current tab's line is below them. */
async function expectStripHolds(page: Page, where: string) {
  const s = await strip(page);
  const bottom = s.strip.y + s.strip.height - s.border;
  for (const c of s.controls) {
    const at = `${where}: ${c.name}`;
    expect.soft(c.x, at).toBeGreaterThanOrEqual(s.strip.x);
    expect.soft(c.x + c.width, at).toBeLessThanOrEqual(s.strip.x + s.strip.width + 0.5);
    expect.soft(c.y, at).toBeGreaterThanOrEqual(s.strip.y);
    expect.soft(c.y + c.height, at).toBeLessThanOrEqual(bottom + 0.5);
  }
  for (const tab of s.tabs) {
    const at = `${where}: tab ${tab.name}`;
    expect.soft(tab.x, at).toBeGreaterThanOrEqual(s.strip.x);
    expect.soft(tab.x + tab.width, at).toBeLessThanOrEqual(s.strip.x + s.strip.width + 0.5);
    expect.soft(tab.y, at).toBeGreaterThanOrEqual(s.strip.y);
    // The tab's line (its 2 pixel bottom border) sits on the strip's border, under every control.
    const line = tab.y + tab.height - 2;
    for (const c of s.controls)
      expect.soft(line, `${at}, under ${c.name}`).toBeGreaterThanOrEqual(c.y + c.height - 0.5);
    expect.soft(line, at).toBeGreaterThanOrEqual(bottom - 2.5);
  }
}

for (const [theme, appearance] of [
  ["", "dark"],
  ["", "light"],
  ["pluxy/paper", "light"],
] as const) {
  for (const [width, height] of [
    [1100, 760],
    [640, 480],
  ] as const) {
    const name = `${theme.replace("pluxy/", "") || "default"}-${appearance}-${width}x${height}`;
    test(`the status menu shows the whole address and the header strip holds its controls: ${name}`, async ({
      page,
    }) => {
      await core.mgmt("PUT", "/theme", { id: theme });
      await page.emulateMedia({ colorScheme: appearance });
      await page.setViewportSize({ width, height });
      await openAt(page);
      const dir = process.env.PANEL_SHOTS_DIR;
      if (dir) mkdirSync(dir, { recursive: true });

      // The status menu: the whole address beside Copy, inside the window.
      const shell = page.locator('[data-part="shell"]');
      const frame = await shell.getAttribute("data-frame");
      await page.locator('[data-part="status"] button.status-block').click();
      const menu = page.locator('[data-part="status-menu"]');
      const address = menu.locator(".status-address code");
      await expect(address).toHaveText(ADDRESS);
      const fit = await address.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
      expect
        .soft(fit.scroll, `the address is ${fit.scroll} pixels wide in ${fit.client}`)
        .toBeLessThanOrEqual(fit.client);
      await expect.soft(menu.getByRole("button", { name: "Copy" })).toBeInViewport({ ratio: 1 });
      await expect.soft(menu).toBeInViewport({ ratio: 1 });
      // On top, the menu opens below the bar and its border, over the page.
      if (frame === "top" || frame === "bar") {
        const bar = await page.locator('[data-part="nav"]').boundingBox();
        const box = await menu.boundingBox();
        if (!bar || !box) throw new Error("no bar or menu");
        expect
          .soft(box.y, `the menu's top, below the bar's bottom at ${bar.y + bar.height}`)
          .toBeGreaterThanOrEqual(bar.y + bar.height);
      }
      if (dir) await page.screenshot({ path: join(dir, `status-menu-${name}.png`), animations: "disabled" });
      await page.keyboard.press("Escape");
      await expect(menu).toHaveCount(0);

      // The header strip, on a page with tabs and actions, and on Home.
      await expectStripHolds(page, "Home");
      await goTo(page, "Accounts");
      await expect(
        page.locator('[data-part="header-actions"]').getByRole("link", { name: "Add Account" }),
      ).toBeVisible();
      await expectStripHolds(page, "Accounts");
      if (dir)
        await page.screenshot({ path: join(dir, `strip-accounts-${name}.png`), animations: "disabled" });
    });
  }
}
