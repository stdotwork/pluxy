// Accounts page contrast across every built-in theme, light and dark: text at 4.5:1 (WCAG AA)
// and non-text graphics at 3:1 (WCAG 1.4.11).
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;

const SAMPLE_ACCOUNTS = [
  {
    id: "needs-1",
    label: "Aurora broken",
    extension: "test/alpha",
    source: "login",
    state: "reauth_required",
    refreshable: false,
  },
  {
    id: "waiting-1",
    label: "Aurora cooling",
    extension: "test/alpha",
    source: "login",
    state: "active",
    refreshable: false,
    cooldowns: [
      {
        scope: "account",
        kind: "rate_limit",
        until: new Date(Date.now() + 15 * 60_000).toISOString(),
      },
    ],
  },
  {
    id: "serving-1",
    label: "Alpha live",
    extension: "test/alpha",
    source: "login",
    state: "active",
    refreshable: false,
  },
  {
    id: "off-1",
    label: "Local paused",
    extension: "test/local",
    source: "login",
    state: "active",
    disabled: true,
    refreshable: false,
  },
];

const SAMPLE_USAGE = {
  total_requests: 99,
  accounts: [
    { name: "needs-1", total_requests: 46 },
    { name: "waiting-1", total_requests: 20 },
    { name: "serving-1", total_requests: 18 },
    { name: "off-1", total_requests: 15 },
  ],
  models: [],
};

async function openAccounts(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;

    test(`stale Accounts text stays at 4.5:1 while bars and marks fade to 0.6: ${name}`, async ({ page }) => {
      let failAccounts = false;
      await page.route("**/v0/management/accounts", async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        if (!failAccounts) {
          return route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ accounts: SAMPLE_ACCOUNTS }),
          });
        }
        return route.fulfill({
          status: 502,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "bad_gateway", message: "core hiccup" } }),
        });
      });
      await page.route("**/v0/management/accounts/off-1", async (route) => {
        if (route.request().method() !== "PATCH") return route.continue();
        return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
      });
      await page.route("**/v0/management/usage*", async (route) => {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(SAMPLE_USAGE),
        });
      });

      await openAccounts(page, theme, appearance);
      await expect(page.locator(".accounts-page")).toBeVisible();

      // Trigger a second read immediately via Turn on; GET /accounts fails over the cached list.
      failAccounts = true;
      await page.getByRole("button", { name: "Turn on: Local paused" }).click();
      await expect(page.locator(".accounts-page[data-stale]")).toBeVisible();

      // Open the Alpha chip too so the pool card's bar and mark are checked in stale state.
      await page
        .getByRole("group", { name: "Filter by provider" })
        .getByRole("button", { name: /Alpha/ })
        .click();
      await expect(page.locator('[data-part="pool-card"]')).toBeVisible();

      const textParts = page.locator(
        [
          ".accounts-counts",
          ".accounts-group-head",
          ".accounts-group-count",
          ".account-name",
          ".account-word",
          '.accounts-group:not([data-group="needs"]) .account-week',
          ".pool-name",
          ".pool-window",
          ".pool-foot",
        ].join(", "),
      );
      const measured = await measureContrast(textParts);
      const texts = await textParts.allTextContents();
      expect(measured.length).toBeGreaterThanOrEqual(10);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);

      const faded = await page
        .locator(
          ".accounts-state-bar, .accounts-fade .pool-bar, .accounts-fade .account-mark, .accounts-fade .pool-head .provider-mark",
        )
        .evaluateAll((els) => els.map((el) => Number.parseFloat(getComputedStyle(el).opacity)));
      expect(faded.length).toBeGreaterThanOrEqual(4);
      for (const opacity of faded) expect(opacity).toBeCloseTo(0.6, 2);
    });
  }
}
