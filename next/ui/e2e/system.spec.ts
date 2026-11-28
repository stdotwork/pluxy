// System: which pluxy this is, the models its tools can ask for and the address each kind of
// program needs. Against a real core with fake extensions.
//
// With PANEL_SHOTS_DIR set to a folder, the spec also saves a screenshot of each screen and state
// there (Default, Terminal and Paper, light and dark, wide and small), for a person to look at.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goToId, openPanel, signIn, useCore } from "./support";

test.setTimeout(300_000);
const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const SIZES = [
  { width: 1100, height: 760 },
  { width: 640, height: 480 },
] as const;
const SHOTS = process.env.PANEL_SHOTS_DIR;

const addresses = (page: Page) => page.getByRole("region", { name: "Addresses for other programs" });

test("System shows the build, the served models and the addresses for other programs", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "system");
  await expect(page.locator('[data-part="page-title"]')).toHaveText("System");

  const status = await core.mgmt<{ version: string; commit: string }>("GET", "/status");
  const about = page.getByRole("region", { name: "About" });
  await expect(about).toContainText("Version");
  await expect(about).toContainText(status.body.version);
  await expect(about).toContainText("Built");
  await expect(about).toContainText("Settings file");
  await expect(about).toContainText("pluxy is using it as it is saved.");
  await expect(about).toContainText(core.base);

  const models = page.getByRole("region", { name: "Models your tools can ask for" });
  const alpha = models.locator('[data-part="model-group"]').filter({ hasText: "test/alpha" });
  await expect(alpha.getByText("alpha-model", { exact: true })).toBeVisible();
  await expect(alpha).toContainText("alpha-1, alpha-2");
  await expect(alpha).toContainText("1 model");
  const local = models.locator('[data-part="model-group"]').filter({ hasText: "test/local" });
  await expect(local.getByText("fake-model", { exact: true })).toBeVisible();
  await expect(local).toContainText("local-1");

  // Refresh in the header asks core for the status and the models again.
  const again = page.waitForRequest((r) => r.url().endsWith("/v0/management/models"));
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await again;
  await expect(alpha.getByText("alpha-model", { exact: true })).toBeVisible();

  // Each kind of program has the base URL for the API it speaks.
  const rows = addresses(page).locator('[data-part="item"]');
  await expect(rows).toHaveText([
    new RegExp(`^OpenAI API${core.base}/v1`),
    new RegExp(`^Anthropic \\(Claude\\) API${core.base}Copy`),
    new RegExp(`^Gemini API${core.base}Copy`),
    new RegExp(`^Codex with an API key${core.base}/v1`),
    new RegExp(`^Codex signed in with ChatGPT${core.base}/backend-api/codex`),
  ]);
  await expect(page.getByText("chatgpt_base_url")).toHaveCount(0);
  await addresses(page).getByRole("link", { name: "Tools", exact: true }).click();
  await expect(page.locator('[data-part="page-title"]')).toHaveText("Tools");
});

test("Copy puts an address and a model's name on the clipboard", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "system");
  const copy = addresses(page).getByRole("button", { name: `Copy ${core.base}/backend-api/codex` });
  await copy.click();
  await expect(copy).toHaveText("Copied");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${core.base}/backend-api/codex`);
  await page.getByRole("button", { name: "Copy alpha-model", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("alpha-model");
});

test("a model list core cannot give says so and offers Try again", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.route("**/v0/management/models", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "internal", message: "no model list today" } }),
    }),
  );
  await goToId(page, "system");
  const models = page.getByRole("region", { name: "Models your tools can ask for" });
  await expect(models).toContainText("Could not read the models");
  await expect(models).not.toContainText("No models yet");
  await expect(models.getByRole("button", { name: "Try again", exact: true })).toBeVisible();
});

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    for (const size of SIZES) {
      const where = `${theme || "pluxy/default"}, ${appearance}, ${size.width}x${size.height}`.replace(
        "pluxy/",
        "",
      );
      const open = async (page: Page) => {
        await core.mgmt("PUT", "/theme", { id: theme });
        await page.setViewportSize(size);
        await page.emulateMedia({ colorScheme: appearance });
        await openPanel(page, core);
        await signIn(page, core);
      };
      const shot = async (page: Page, file: string) => {
        if (!SHOTS) return;
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({
          path: join(
            SHOTS,
            `${file}-${(theme || "default").replace("pluxy/", "")}-${appearance}-${size.width}.png`,
          ),
          animations: "disabled",
        });
      };
      /** Nothing is wider than the window, every button is as high as the theme's controls, and no base URL is broken inside. */
      const fits = async (page: Page) => {
        expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
        const wrong = await page.evaluate(() => {
          const control = Number.parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue("--px-size-control"),
          );
          const main = document.querySelector("main");
          const small = [...document.querySelectorAll<HTMLElement>("main .btn, header .btn")]
            .filter((b) => b.getBoundingClientRect().height > 0)
            .filter((b) => b.getBoundingClientRect().height < control - 0.5)
            .map((b) => `${b.textContent}: ${b.getBoundingClientRect().height} < ${control}`);
          // A base URL stays on one line: a line's height is the font's, so two lines are taller.
          const broken = [...document.querySelectorAll<HTMLElement>(".system-url")]
            .filter(
              (u) =>
                u.getBoundingClientRect().height >
                Number.parseFloat(getComputedStyle(u).lineHeight || "0") * 1.6 + 4,
            )
            .map((u) => `${u.textContent} is broken inside`);
          return small
            .concat(broken)
            .concat(main && main.scrollWidth > main.clientWidth ? ["the page area scrolls sideways"] : []);
        });
        expect(wrong).toEqual([]);
      };

      test(`System fits: ${where}`, async ({ page, context }) => {
        await context.grantPermissions(["clipboard-read", "clipboard-write"]);
        await open(page);
        await goToId(page, "system");
        await expect(page.getByText("alpha-model", { exact: true })).toBeVisible();
        await expect(addresses(page).locator('[data-part="item"]')).toHaveCount(5);
        await fits(page);
        await shot(page, "system");
        // A Copy that worked says so.
        await page.getByRole("button", { name: "Copy alpha-model", exact: true }).click();
        await shot(page, "system-copied");
      });

      test(`System's other states fit: ${where}`, async ({ page }) => {
        const status = await core.mgmt<{ desired: object }>("GET", "/status");
        await page.route("**/v0/management/status", (route) =>
          route.fulfill({
            json: {
              ...status.body,
              safe_mode: true,
              desired: { revision: 9, at: "2026-11-25T22:00:00Z", error: "line 3: unknown key" },
            },
          }),
        );
        await page.route("**/v0/management/models", (route) => route.fulfill({ json: { models: [] } }));
        await open(page);
        await goToId(page, "system");
        await expect(page.getByText("No models yet", { exact: true })).toBeVisible();
        await expect(page.getByText("pluxy was started in safe mode", { exact: false })).toBeVisible();
        await fits(page);
        await shot(page, "system-empty-safe-mode");
        await page.unroute("**/v0/management/models");
        await page.route("**/v0/management/models", (route) =>
          route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: { code: "internal", message: "no model list today" } }),
          }),
        );
        await page.getByRole("button", { name: "Refresh", exact: true }).click();
        await expect(page.getByText("Could not read the models", { exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "system-models-failed");
      });
    }
  }
}
