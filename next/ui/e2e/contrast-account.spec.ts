// One account's page stays readable (WCAG AA, 4.5:1 for text; 3:1 for non-text marks) in every
// built-in theme, light and dark: text kept on screen after a failed refresh, the waiting
// header's reason sentence on its warning band, and the pace tick over every meter fill.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;
const GRAPHIC = 3;

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
      // Words stay at full opacity; only bars and marks fade to 0.6.
      const opacities = await page.evaluate(() => ({
        fade: getComputedStyle(document.querySelector(".account-fade") as Element).opacity,
        mark: getComputedStyle(document.querySelector(".account-hero-mark") as Element).opacity,
        limitBar: getComputedStyle(document.querySelector(".account-limit-bar") as Element).opacity,
        weekBar: getComputedStyle(document.querySelector(".account-bar-track") as Element).opacity,
      }));
      expect(opacities).toEqual({ fade: "1", mark: "0.6", limitBar: "0.6", weekBar: "0.6" });
    });

    test(`waiting header reason text is readable on the warning band: ${name}`, async ({ page }) => {
      await core.mgmt("PUT", "/theme", { id: theme });
      await page.emulateMedia({ colorScheme: appearance });
      const until = new Date(Date.now() + 20 * 60_000).toISOString();
      await page.route("**/v0/management/accounts", async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        const reply = await route.fetch();
        const body = (await reply.json()) as { accounts: Record<string, unknown>[] };
        for (const a of body.accounts) {
          if (a.id === "alpha-1") {
            a.cooldowns = [{ scope: "account", kind: "rate_limit", until }];
            a.last_error = { code: "rate_limit", message: "too many requests" };
          }
        }
        await route.fulfill({ response: reply, json: body });
      });
      await openPanel(page, core, "/panel/#/accounts/alpha-1");
      await signIn(page, core);
      const header = page.locator('[data-part="account-opening"][data-group="waiting"]');
      await expect(header).toBeVisible();
      const parts = header.locator(".account-state-text, .account-said");
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      expect(measured.length).toBeGreaterThanOrEqual(2);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 40), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });

    test(`pace tick stands out over every meter fill and track: ${name}`, async ({ page }) => {
      await core.mgmt("PUT", "/theme", { id: theme });
      await page.emulateMedia({ colorScheme: appearance });
      const quota = sampleQuota();
      await page.route("**/v0/management/accounts", async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        const reply = await route.fetch();
        const body = (await reply.json()) as { accounts: Record<string, unknown>[] };
        for (const a of body.accounts) if (a.id === "alpha-1") a.quota = quota;
        await route.fulfill({ response: reply, json: body });
      });
      await page.route("**/v0/management/accounts/alpha-1/quota", (route) => route.fulfill({ json: quota }));
      await openPanel(page, core, "/panel/#/accounts/alpha-1");
      await signIn(page, core);
      await expect(page.locator(".account-pace-tick")).toHaveCount(3);
      const results = await page.evaluate(() => {
        const ctx = document.createElement("canvas").getContext("2d");
        if (!ctx) throw new Error("no 2d canvas");
        const rgba = (c: string): [number, number, number, number] => {
          ctx.clearRect(0, 0, 1, 1);
          ctx.fillStyle = "rgba(0, 0, 0, 0)";
          ctx.fillStyle = c;
          ctx.fillRect(0, 0, 1, 1);
          const [r = 0, g = 0, b = 0, a = 0] = ctx.getImageData(0, 0, 1, 1).data;
          return [r, g, b, a];
        };
        const over = (
          top: [number, number, number, number],
          below: [number, number, number, number],
        ): [number, number, number, number] => {
          const a = top[3] / 255;
          return [
            top[0] * a + below[0] * (1 - a),
            top[1] * a + below[1] * (1 - a),
            top[2] * a + below[2] * (1 - a),
            255,
          ];
        };
        const lum = ([r, g, b]: [number, number, number, number]) => {
          const lin = (v: number) => {
            const s = v / 255;
            return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
          };
          return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
        };
        const ratio = (a: [number, number, number, number], b: [number, number, number, number]) => {
          const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
          return (hi + 0.05) / (lo + 0.05);
        };
        const bgOf = (el: Element | null): [number, number, number, number] => {
          const layers: [number, number, number, number][] = [];
          for (let e = el; e; e = e.parentElement) {
            const c = rgba(getComputedStyle(e).backgroundColor);
            if (c[3] > 0) layers.push(c);
          }
          let b: [number, number, number, number] = [255, 255, 255, 255];
          for (const l of layers.reverse()) b = over(l, b);
          return b;
        };
        const shadowColor = (shadow: string): [number, number, number, number] => {
          const m = shadow.match(/(?:rgba?|color|oklab|oklch|lab|lch)\([^)]*\)/);
          return m ? rgba(m[0]) : [0, 0, 0, 0];
        };
        const out: { what: string; ratio: number }[] = [];
        for (const bar of document.querySelectorAll(".account-limit-bar")) {
          const meter = bar.querySelector(".meter");
          const fillEl = meter?.firstElementChild;
          const tick = bar.querySelector(".account-pace-tick");
          if (!meter || !fillEl || !tick) continue;
          const tone = meter.getAttribute("data-tone") ?? "unknown";
          const ground = bgOf(bar);
          const track = over(rgba(getComputedStyle(meter).backgroundColor), ground);
          const fill = over(rgba(getComputedStyle(fillEl).backgroundColor), track);
          const tickStyle = getComputedStyle(tick);
          const tickColor = rgba(tickStyle.backgroundColor);
          const ringColor = shadowColor(tickStyle.boxShadow);
          const overFill = Math.max(ratio(over(tickColor, fill), fill), ratio(over(ringColor, fill), fill));
          const overTrack = Math.max(
            ratio(over(tickColor, track), track),
            ratio(over(ringColor, track), track),
          );
          out.push({ what: `tick on ${tone} fill`, ratio: overFill });
          out.push({ what: `tick on ${tone} track`, ratio: overTrack });
        }
        return out;
      });
      expect(results).toHaveLength(6);
      const low = results.filter((r) => r.ratio < GRAPHIC).map((r) => `${r.what}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });
  }
}
