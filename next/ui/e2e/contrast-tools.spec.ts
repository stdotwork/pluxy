// Tools' text stays readable (WCAG AA, 4.5:1) under a stale notice in every built-in theme, in
// light and in dark: only the tool marks fade (opacity 0.6), never the words.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;

async function open(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Tools");
  await expect(page.locator('[data-part="tool-row"]').filter({ hasText: "Claude Code" })).toBeVisible();
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;
    test(`stale Tools rows keep text readable and fade only marks: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await page.route("**/setup/scan", (route) =>
        route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "internal", message: "scan failed" } }),
        }),
      );
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      const stale = page.locator(".tools-fade[data-stale]");
      await expect(stale).toBeVisible();
      await expect(page.locator(".tools-stale-wrap")).toBeVisible();

      // Words stay at full opacity; only the tool mark fades to 0.6.
      expect(await stale.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
      expect(
        await stale
          .locator(".tool-mark-tile")
          .first()
          .evaluate((el) => getComputedStyle(el).opacity),
      ).toBe("0.6");

      const parts = page.locator(
        ".tools-fade[data-stale] .tool-name, .tools-fade[data-stale] .tool-version, .tools-fade[data-stale] .badge, .tools-fade[data-stale] .tools-row-path, .tools-foot-link",
      );
      await expect(parts.first()).toBeVisible();
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      expect(measured.length).toBeGreaterThanOrEqual(5);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });
  }
}
