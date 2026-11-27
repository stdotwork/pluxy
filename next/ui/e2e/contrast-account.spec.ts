// One account's page stays readable (WCAG AA, 4.5:1 for text; 3:1 for non-text marks) in every
// built-in theme, light and dark: text kept on screen after a failed refresh.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;

function sampleQuota(now = Date.now()) {
  return {
    fetched_at: new Date(now).toISOString(),
    quota: {
      plan: "Max 20x",
      limits: [
        {
          id: "5h",
          window_seconds: 5 * 3600,
          resets_at: Math.floor((now + 2 * 3600_000) / 1000),
          used_percent: 85,
        },
        {
          id: "day",
          window_seconds: 24 * 3600,
          resets_at: Math.floor((now + 12 * 3600_000) / 1000),
          used_percent: 50,
        },
        {
          id: "wk",
          window_seconds: 7 * 86400,
          resets_at: Math.floor((now + 3 * 86_400_000) / 1000),
          used_percent: 15,
        },
      ],
    },
  };
}

async function openStaleAccount(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await page.clock.install();
  const quota = sampleQuota();
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const reply = await route.fetch();
    const body = (await reply.json()) as { accounts: Record<string, unknown>[] };
    for (const a of body.accounts) if (a.id === "alpha-1") a.quota = quota;
    await route.fulfill({ response: reply, json: body });
  });
  await page.route("**/v0/management/accounts/alpha-1/quota", (route) => route.fulfill({ json: quota }));
  let usageCalls = 0;
  await page.route("**/v0/management/usage?*", async (route) => {
    usageCalls += 1;
    if (usageCalls === 1) {
      await route.fulfill({
        json: {
          total_requests: 5,
          success_count: 5,
          models: [{ name: "alpha-model", total_requests: 5 }],
        },
      });
      return;
    }
    await route.fulfill({ status: 502, json: { error: { code: "bad_gateway", message: "core hiccup" } } });
  });
  await openPanel(page, core, "/panel/#/accounts/alpha-1");
  await signIn(page, core);
  await expect(page.locator('[data-part="account-opening"]')).toBeVisible();
  await expect(page.locator(".account-limit-pace").first()).toBeVisible();
  // Advance past the 10-second poll interval so the second usage read fails and sets data-stale.
  await page.clock.runFor(10_500);
  await expect(page.locator('[data-part="account-page"][data-stale]')).toBeVisible();
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;

    test(`stale account page text stays readable after a failed refresh: ${name}`, async ({ page }) => {
      await openStaleAccount(page, theme, appearance);
      const parts = page.locator(
        [
          ".account-state-text",
          ".account-limit-name",
          ".account-limit-value",
          ".account-limit-reset",
          ".account-limit-pace",
          ".account-read",
          ".account-week-summary strong",
          ".account-bar-name",
          ".account-bar-count",
          ".account-about dt",
          ".account-about dd",
        ].join(", "),
      );
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      expect(measured.length).toBeGreaterThanOrEqual(12);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 40), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });
  }
}
