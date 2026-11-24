// Text on the badges and the Themes page's segment buttons is readable (WCAG AA, 4.5:1 for text
// this size) in every built-in theme, in light and in dark.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const TONES = ["ok", "warn", "danger", "muted"] as const;
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

    test(`segment labels are readable on the Themes page: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await goTo(page, "Themes");
      const spans = page.locator(".segment span");
      expect(await spans.count()).toBeGreaterThan(0);
      // The text of each segment: its label, and the note under it where it has one.
      const parts = page.locator(".segment span, .segment span small");
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });
  }
}
