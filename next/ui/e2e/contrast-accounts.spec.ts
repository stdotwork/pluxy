// Accounts page contrast across every built-in theme, light and dark: text at 4.5:1 (WCAG AA)
// and non-text graphics at 3:1 (WCAG 1.4.11).
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;
const GRAPHIC = 3;

/** Measures every segment of .accounts-state-bar against the ground behind it (solid fill or ring). */
async function measureStateBar(page: Page) {
  return page.locator(".accounts-state-bar").evaluate((bar) => {
    const ctx = document.createElement("canvas").getContext("2d");
    if (!ctx) throw new Error("no 2d context");
    const rgb = (c: string) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = "#000";
      ctx.fillStyle = c;
      ctx.fillRect(0, 0, 1, 1);
      return [...ctx.getImageData(0, 0, 1, 1).data];
    };
    const lum = ([r = 0, g = 0, b = 0]: number[]) => {
      const lin = (v: number) => {
        const s = v / 255;
        return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
    };
    const ratio = (a: number[], b: number[]) => {
      const [hi = 0, lo = 0] = [lum(a), lum(b)].sort((x, y) => y - x);
      return (hi + 0.05) / (lo + 0.05);
    };
    const over = (top: number[], below: number[]) => {
      const a = (top[3] ?? 255) / 255;
      return [0, 1, 2].map((i) => (top[i] ?? 0) * a + (below[i] ?? 0) * (1 - a)).concat(255);
    };
    const layers: number[][] = [];
    for (let e: Element | null = bar; e; e = e.parentElement) {
      const c = rgb(getComputedStyle(e).backgroundColor);
      if ((c[3] ?? 0) > 0) layers.push(c);
    }
    let ground = [255, 255, 255, 255];
    for (const l of layers.reverse()) ground = over(l, ground);
    return [...bar.querySelectorAll<HTMLElement>("[data-group]")].map((seg) => {
      const style = getComputedStyle(seg);
      const fill = rgb(style.backgroundColor);
      const borderWidth = Number.parseFloat(style.borderTopWidth);
      const paint = (fill[3] ?? 0) > 0 ? over(fill, ground) : over(rgb(style.borderTopColor), ground);
      return {
        group: seg.dataset.group ?? "",
        ratio: ratio(paint, ground),
        kind: (fill[3] ?? 0) > 0 ? "solid" : borderWidth > 0 ? "ring" : "empty",
      };
    });
  });
}

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
          ".account-week",
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

    test(`summary state bar segments clear 3:1 and distinguish unchecked from turned off: ${name}`, async ({
      page,
    }) => {
      let failStatus = false;
      await page.route("**/v0/management/accounts", async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ accounts: SAMPLE_ACCOUNTS }),
        });
      });
      await page.route("**/v0/management/status", async (route) => {
        if (!failStatus) return route.continue();
        return route.fulfill({
          status: 502,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "bad_gateway", message: "status down" } }),
        });
      });

      await openAccounts(page, theme, appearance);
      await expect(page.locator(".accounts-state-bar")).toBeVisible();
      const normal = await measureStateBar(page);
      expect(normal.map((s) => s.group).sort()).toEqual(["needs", "off", "serving", "waiting"]);
      const lowNormal = normal
        .filter((s) => s.ratio < GRAPHIC || s.kind === "empty")
        .map((s) => `${s.group} (${s.kind}): ${s.ratio.toFixed(2)}:1`);
      expect(lowNormal).toEqual([]);

      // Reload with status unread so unchecked and turned off show side by side.
      failStatus = true;
      await page.reload();
      await expect(page.locator('.accounts-state-bar [data-group="unchecked"]')).toBeVisible();
      const unread = await measureStateBar(page);
      expect(unread.map((s) => s.group).sort()).toEqual(["needs", "off", "unchecked"]);
      const lowUnread = unread
        .filter((s) => s.ratio < GRAPHIC || s.kind === "empty")
        .map((s) => `${s.group} (${s.kind}): ${s.ratio.toFixed(2)}:1`);
      expect(lowUnread).toEqual([]);
      const offSeg = unread.find((s) => s.group === "off");
      const uncheckedSeg = unread.find((s) => s.group === "unchecked");
      expect(uncheckedSeg?.kind).not.toBe(offSeg?.kind);
    });

    test(`Needs you rows stay at 4.5:1 in both List and Cards views: ${name}`, async ({ page }) => {
      await page.route("**/v0/management/accounts", async (route) => {
        if (route.request().method() !== "GET") return route.continue();
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ accounts: SAMPLE_ACCOUNTS }),
        });
      });
      await page.route("**/v0/management/usage*", async (route) => {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(SAMPLE_USAGE),
        });
      });

      await openAccounts(page, theme, appearance);
      const selector = [
        '.accounts-group[data-group="needs"] .accounts-group-head',
        '.accounts-group[data-group="needs"] .accounts-group-count',
        '.accounts-group[data-group="needs"] .account-name',
        '.accounts-group[data-group="needs"] .account-word',
        '.accounts-group[data-group="needs"] .account-reason',
        '.accounts-group[data-group="needs"] .account-week',
      ].join(", ");
      const viewSwitch = page.getByRole("group", { name: "Account view" });

      for (const mode of ["list", "cards"] as const) {
        await viewSwitch
          .getByRole("button", { name: mode === "list" ? "List" : "Cards", exact: true })
          .click();
        await expect(page.locator(".accounts-page")).toHaveAttribute("data-view", mode);
        const parts = page.locator(selector);
        const measured = await measureContrast(parts);
        const texts = await parts.allTextContents();
        expect(measured.length).toBeGreaterThanOrEqual(6);
        const low = measured
          .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
          .filter((m) => m.ratio < AA)
          .map((m) => `${mode} "${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
        expect(low).toEqual([]);
      }
    });
  }
}
