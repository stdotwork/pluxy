// Activity's own text is readable (WCAG AA, 4.5:1) in every built-in theme, in light and in dark:
// the day headings and the end of the Requests list, the failure and detour chips, the Usage
// tiles, the Usage chart's labels, readout and legend, and the Logs warning and error lines.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;

test.beforeAll(async () => {
  for (const model of ["fake-model", "server-error"]) {
    const r = await fetch(`${core.base}/v1/chat/completions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }),
    });
    await r.arrayBuffer();
  }
});

async function open(page: Page, theme: string, appearance: (typeof APPEARANCES)[number], hash: string) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core, `/panel/#/${hash}`);
  await signIn(page, core);
}

async function unreadable(page: Page, selector: string) {
  const found = page.locator(selector);
  await expect(found.first()).toBeVisible();
  const measured = await measureContrast(found);
  const texts = await found.allTextContents();
  return measured
    .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
    .filter((m) => m.ratio < AA)
    .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;
    test(`the Requests list's day headings and end are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance, "activity");
      expect(await unreadable(page, ".activity-day-heading, .activity-end")).toEqual([]);
    });

    test(`the Usage chart's labels, readout and legend are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance, "activity/usage");
      expect(
        await unreadable(
          page,
          ".usage-chart .chart-label, .usage-readout, .usage-chart .chart-legend, .usage-step",
        ),
      ).toEqual([]);
    });

    test(`the Usage tiles and the breakdown are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance, "activity/usage");
      expect(
        await unreadable(
          page,
          ".usage-tile-label, .usage-tile-value, .usage-tile-sub, .usage-percent, .usage-breakdown td[data-failed]",
        ),
      ).toEqual([]);
    });

    test(`the Requests list's chips and headings are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance, "activity");
      expect(await unreadable(page, ".word-chip, .requests-table thead th, .req-time")).toEqual([]);
    });

    test(`the Logs warning and error lines are readable: ${name}`, async ({ page }) => {
      await page.route("**/v0/management/logs*", (route) =>
        route.fulfill({
          json: {
            lines: [
              "[2026-11-27 12:00:00.000] [info ] server started",
              "[2026-11-27 12:00:01.000] [warn ] account cooling down",
              "[2026-11-27 12:00:02.000] [error] upstream failed",
            ],
            "line-count": 3,
            "latest-timestamp": 3,
          },
        }),
      );
      await open(page, theme, appearance, "logs");
      expect(
        await unreadable(
          page,
          '.logs-core [data-part="log-line"][data-level="warn"], .logs-core [data-part="log-line"][data-level="error"]',
        ),
      ).toEqual([]);
    });
  }
}
