// Text on the badges and the Themes page's segment buttons, theme cards and setting rows is readable (WCAG AA, 4.5:1 for text
// this size) in every built-in theme, in light and in dark.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

// One request, so Home draws its chart of the last 7 days.
test.beforeAll(async () => {
  const r = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "fake-model", messages: [{ role: "user", content: "hi" }] }),
  });
  expect(r.status).toBe(200);
  await r.arrayBuffer();
});

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const TONES = ["ok", "warn", "danger", "muted", "info", "update"] as const;
// The surfaces a badge sits on: the page, a card or table, a raised or selected row.
const SURFACES = ["bg", "surface", "surface-raised", "selected"] as const;
const AA = 4.5;

async function open(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core);
  await signIn(page, core);
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;
    test(`badge text is readable on every surface: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      // Put one badge of each tone on each surface, in the page's own styles.
      await page.evaluate(
        ({ tones, surfaces }) => {
          const host = document.createElement("div");
          host.id = "badge-samples";
          for (const surface of surfaces) {
            const box = document.createElement("div");
            box.dataset.surface = surface;
            box.style.background = `var(--px-color-${surface})`;
            box.style.padding = "8px";
            for (const tone of tones) {
              const badge = document.createElement("span");
              badge.className = "badge";
              badge.dataset.part = "badge";
              badge.dataset.tone = tone;
              badge.textContent = `${tone} sample`;
              box.append(badge, " ");
            }
            host.append(box);
          }
          document.querySelector("main")?.prepend(host);
        },
        { tones: [...TONES], surfaces: [...SURFACES] },
      );
      const badges = page.locator("#badge-samples .badge");
      const all = await measureContrast(badges);
      expect(all).toHaveLength(TONES.length * SURFACES.length);
      const low = all
        .map((m, i) => ({
          tone: TONES[i % TONES.length],
          surface: SURFACES[Math.floor(i / TONES.length)],
          ...m,
        }))
        .filter((m) => m.ratio < AA)
        .map((m) => `${m.tone} on ${m.surface}: ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });

    test(`Home's chart labels, counts and group names are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await goTo(page, "Home");
      await expect(page.locator('[data-part="days"]')).toBeVisible();
      // A bar's readout shows on hover: it is text too.
      await page.locator('[data-part="days"] .day').last().hover();
      await expect(page.locator(".days-readout")).not.toBeEmpty();
      const parts = page.locator(
        ".day-name, .days-top, .days-readout, .home-counts, .home-group-head, .home-address-label, .home-section-head a",
      );
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      // Seven day names, the top figure, the readout, the counts line, a group name, the address label, two links.
      expect(measured.length).toBeGreaterThanOrEqual(13);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });

    test(`segment labels, theme names and setting words are readable on the Themes page: ${name}`, async ({
      page,
    }) => {
      expect((await core.mgmt("PUT", "/themes/me/broken", { name: "Broken Thing" })).status).toBe(200);
      try {
        await open(page, theme, appearance);
        await goTo(page, "Themes");
        const spans = page.locator(".segment span");
        expect(await spans.count()).toBeGreaterThan(0);
        await expect(page.locator(".theme-card[data-unusable] .theme-about")).toBeVisible();
        // The text of each segment: its label, and the note under it where it has one; each theme's
        // name and description (including an unusable theme's reason); each setting's name and sentence.
        const parts = page.locator(
          ".segment span, .segment span small, .theme-name, .theme-about, .setting-name, .setting-description",
        );
        const measured = await measureContrast(parts);
        const texts = await parts.allTextContents();
        const low = measured
          .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
          .filter((m) => m.ratio < AA)
          .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
        expect(low).toEqual([]);

        // Every palette dot keeps a hairline ring at 3:1+ against the active card, while its fill
        // stays the previewed theme's own colour.
        const dots = await page.locator(".theme-dots i").evaluateAll((elements) => {
          type Rgba = [number, number, number, number];
          const ctx = document.createElement("canvas").getContext("2d");
          const rgb = (c: string): Rgba => {
            if (!ctx) return [0, 0, 0, 255];
            ctx.clearRect(0, 0, 1, 1);
            ctx.fillStyle = "#000";
            ctx.fillStyle = c;
            ctx.fillRect(0, 0, 1, 1);
            const d = ctx.getImageData(0, 0, 1, 1).data;
            return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0, d[3] ?? 255];
          };
          const over = (top: Rgba, below: Rgba): Rgba => {
            const a = top[3] / 255;
            return [
              top[0] * a + below[0] * (1 - a),
              top[1] * a + below[1] * (1 - a),
              top[2] * a + below[2] * (1 - a),
              255,
            ];
          };
          const lum = ([r, g, b]: Rgba) => {
            const l = (v: number) => {
              const s = v / 255;
              return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
            };
            return 0.2126 * l(r) + 0.7152 * l(g) + 0.0722 * l(b);
          };
          const ratio = (a: Rgba, b: Rgba) => {
            const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x) as [number, number];
            return (hi + 0.05) / (lo + 0.05);
          };
          const bgOf = (el: Element | null) => {
            const layers: Rgba[] = [];
            for (let cur = el; cur; cur = cur.parentElement) {
              const c = rgb(getComputedStyle(cur).backgroundColor);
              if (c[3] > 0) layers.push(c);
            }
            let b: Rgba = [255, 255, 255, 255];
            for (const l of layers.reverse()) b = over(l, b);
            return b;
          };
          return elements.map((dot) => {
            const style = getComputedStyle(dot);
            const card = dot.closest(".theme-card");
            const ground = bgOf(card);
            const ring = over(rgb(style.borderTopColor), ground);
            const cardName = card?.querySelector(".theme-name")?.textContent ?? "";
            return {
              cardName,
              borderWidth: Number.parseFloat(style.borderTopWidth),
              ringRatio: ratio(ring, ground),
              fill: style.backgroundColor,
            };
          });
        });
        expect(dots.length).toBe(15);
        expect(dots.filter((d) => d.borderWidth < 1 || d.ringRatio < 3)).toEqual([]);
        // Default's page dot (dot 2) keeps Default's light page colour (#fcfcfc) on every theme.
        expect(dots.filter((d) => d.cardName === "Default")[1]?.fill).toBe("rgb(252, 252, 252)");
      } finally {
        await core.mgmt("DELETE", "/themes/me/broken");
      }
    });
  }
}
