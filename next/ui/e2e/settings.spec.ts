// Settings: it opens on General, its topics are listed in three groups with a tip each, and General
// and Themes fit every built-in theme, light and dark, in a wide and a small window: nothing
// scrolls sideways, every button is as high as the theme's controls, and the theme cards are one
// size with one gap.
//
// With PANEL_SHOTS_DIR set to a folder, the spec also saves a screenshot of each screen and state
// there, for a person to look at.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, pageTitle, signIn, topics, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const SIZES = [
  { width: 1100, height: 760 },
  { width: 640, height: 480 },
] as const;
const SHOTS = process.env.PANEL_SHOTS_DIR;
const fixtures = join(import.meta.dirname, "..", "tests", "fixtures");

async function open(page: Page, theme: string, appearance: string, size: { width: number; height: number }) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.setViewportSize(size);
  await page.emulateMedia({ colorScheme: appearance as "light" | "dark" });
  await openPanel(page, core);
  await signIn(page, core);
}

/** Scrolls only the pane that scrolls, as a person's wheel would, until the heading is at its top. */
const scrollToHeading = (page: Page, heading: string) =>
  page.getByRole("heading", { level: 2, name: heading, exact: true }).evaluate((e) => {
    let pane = e.parentElement;
    while (pane && !/(auto|scroll)/.test(getComputedStyle(pane).overflowY)) pane = pane.parentElement;
    if (pane) pane.scrollTop += e.getBoundingClientRect().top - pane.getBoundingClientRect().top;
  });

const name = (theme: string) => (theme || "pluxy/default").replace("pluxy/", "");

test("Settings opens on General, and its topics are in three groups with a tip each", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await nav(page).getByRole("link", { name: "Settings", exact: true }).click();
  await expect(pageTitle(page)).toHaveText("General");
  await expect(page).toHaveURL(/#\/settings$/);

  const groups = topics(page).locator('[data-part="nav-group"]');
  const labels = await groups.evaluateAll((gs) =>
    gs.map((g) => ({
      group: g.getAttribute("data-group"),
      links: [...g.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("title")]),
    })),
  );
  // panelcore's demo extensions bring pages of their own, listed last.
  expect(labels.map((g) => g.group)).toEqual(["device", "core", "extensions"]);
  expect(labels[0]?.links).toEqual([
    ["General", "Look, hiding names and signing out"],
    ["Themes", "Theme, layout and custom CSS"],
  ]);
  // The pages' titles are their own slices' to change; the tips are Settings'.
  expect(labels[1]?.links).toEqual([
    ["Config Panel", "Network, logging, keys and pluxy's settings file"],
    ["Extensions", "The programs that connect pluxy to each provider"],
    ["Logs Viewer", "pluxy's log and failed requests"],
    ["System", "Version, models and addresses"],
    ["Set up pluxy", "The guided setup"],
  ]);
  await expect(topics(page).getByRole("link", { name: "General", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(page.getByText("Light, dark, or whatever your system uses.")).toBeVisible();
  // Signed in through the panel, so Sign out is a row of General.
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
});

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    for (const size of SIZES) {
      const where = `${name(theme)}, ${appearance}, ${size.width}x${size.height}`;
      const shot = async (page: Page, file: string) => {
        if (!SHOTS) return;
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({
          path: join(SHOTS, `${file}-${name(theme)}-${appearance}-${size.width}.png`),
          // Switches and segments slide: the picture shows where they end.
          animations: "disabled",
        });
      };
      /** Nothing is wider than the window, and every button is at least as high as the theme's controls. */
      const fits = async (page: Page) => {
        expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
        const wide = await page.evaluate(() => {
          const control = Number.parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue("--px-size-control"),
          );
          const main = document.querySelector("main");
          return [...document.querySelectorAll<HTMLElement>("main .btn, header .btn")]
            .filter((b) => b.getBoundingClientRect().height > 0)
            .filter((b) => b.getBoundingClientRect().height < control - 0.5)
            .map((b) => `${b.textContent}: ${b.getBoundingClientRect().height} < ${control}`)
            .concat(main && main.scrollWidth > main.clientWidth ? ["the page area scrolls sideways"] : []);
        });
        expect(wide).toEqual([]);
      };

      test(`General fits: ${where}`, async ({ page }) => {
        await open(page, theme, appearance, size);
        await goTo(page, "General");
        await fits(page);
        await shot(page, "general");
        // The same page with names hidden: the header strip says so.
        await page.getByRole("switch", { name: "Hide names" }).check();
        await expect(page.getByRole("button", { name: "Names hidden", exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "general-hidden");
      });

      test(`Themes fits, and its cards are one size with one gap: ${where}`, async ({ page }) => {
        await open(page, theme, appearance, size);
        await goTo(page, "Themes");
        await expect(page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio")).toHaveCount(3);
        await fits(page);
        const boxes = await page.locator(".theme-grid .theme-card").evaluateAll((cards) =>
          cards.map((c) => {
            const r = c.getBoundingClientRect();
            return { x: r.x, y: r.y, w: r.width, h: r.height };
          }),
        );
        expect(boxes).toHaveLength(3);
        for (const b of boxes) {
          expect(Math.abs(b.w - (boxes[0]?.w ?? 0))).toBeLessThan(1);
          expect(Math.abs(b.h - (boxes[0]?.h ?? 0))).toBeLessThan(1);
        }
        // One gap between neighbours in a row, and one between rows.
        const gaps = new Set<number>();
        for (const [i, b] of boxes.entries()) {
          const next = boxes[i + 1];
          if (!next) continue;
          gaps.add(Math.round(next.y === b.y ? next.x - (b.x + b.w) : next.y - (b.y + b.h)));
        }
        expect(gaps.size).toBe(1);
        await shot(page, "themes");
        if (SHOTS) {
          // The rest of the page, a section at a time.
          for (const [heading, file] of [
            ["Layout", "themes-layout"],
            ["Add a theme", "themes-add"],
          ] as const) {
            await scrollToHeading(page, heading);
            await shot(page, file);
          }
        }

        // A theme someone added, on: Save theme file and Remove under the cards, and a warning notice.
        await page
          .getByLabel("Theme file", { exact: true })
          .setInputFiles(join(fixtures, "t3-docs-nightfall.json"));
        await expect(page.getByText("Added and turned on Nightfall.")).toBeVisible();
        await expect(page.getByRole("button", { name: "Save theme file", exact: true })).toBeVisible();
        await expect(page.getByRole("button", { name: "Remove Nightfall", exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "themes-added");
        await page.getByRole("button", { name: "Remove Nightfall", exact: true }).click();
        await expect(page.getByRole("button", { name: "Yes, remove Nightfall", exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "themes-removing");
        await page.getByRole("button", { name: "Yes, remove Nightfall", exact: true }).click();
        await expect(page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio")).toHaveCount(3);
      });
    }
  }
}

test("Themes: a stored file it cannot use, a load that failed, and a refused add show their notices", async ({
  page,
}) => {
  const size = SIZES[0];
  await open(page, "", "light", size);
  const shot = async (file: string) => {
    if (!SHOTS) return;
    mkdirSync(SHOTS, { recursive: true });
    await page.screenshot({ path: join(SHOTS, `${file}.png`), animations: "disabled" });
  };
  await goTo(page, "Themes");

  // A refused add says why, under Add.
  const paste = page.getByLabel("Or paste a theme file");
  await paste.fill('{"foo": 1}');
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Not added:");
  await shot("themes-not-added");

  // A stored file the page cannot use, turned on from elsewhere.
  expect((await core.mgmt("PUT", "/themes/me/broken", { name: "Broken Thing" })).status).toBe(200);
  await core.mgmt("PUT", "/theme", { id: "me/broken" });
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("status").filter({ hasText: "cannot be used" })).toBeVisible();
  await scrollToHeading(page, "Theme");
  await shot("themes-unusable");
  await core.mgmt("DELETE", "/themes/me/broken");

  // Core cannot be read: the notice has its own Try again, and it works.
  let down = true;
  await page.route("**/v0/management/themes", (route) =>
    down
      ? route.fulfill({
          status: 500,
          contentType: "application/json",
          body: '{"error":{"code":"internal","message":"core is down"}}',
        })
      : route.continue(),
  );
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  const lost = page.getByRole("alert").filter({ hasText: "Could not load pluxy's themes" });
  await expect(lost).toBeVisible();
  await scrollToHeading(page, "Theme");
  await shot("themes-load-failed");
  down = false;
  await lost.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(lost).toHaveCount(0);
});

test("the topics are a select in a small window, on the current topic", async ({ page }) => {
  await open(page, "", "light", SIZES[1]);
  await nav(page).getByRole("link", { name: "Settings", exact: true }).click();
  const select = page.getByRole("combobox", { name: "Settings topics" });
  await expect(select.locator("option:checked")).toHaveText("General");
  await expect(select.locator("optgroup")).toHaveCount(3);
  await select.selectOption({ label: "Themes" });
  await expect(pageTitle(page)).toHaveText("Themes");
});
