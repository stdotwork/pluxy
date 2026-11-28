// The colours the look adds stay readable in every built-in theme, light and dark: text at 4.5:1
// (WCAG AA) and dots and bars at 3:1 against what they sit on (WCAG 1.4.11). The pairs: the
// navigation on the window's ground, plan pills in their four finishes, tinted notices, table
// headings, and a limit's gauge against its track.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();
const appCss = readFileSync(join(import.meta.dirname, "..", "..", "app", "src", "app.css"), "utf8");

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;
const GRAPHIC = 3;

async function open(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Home");
}

/** Puts samples of the look's parts at the top of the page, in the page's own styles. */
async function addSamples(page: Page) {
  await page.evaluate(() => {
    const host = document.createElement("div");
    host.id = "look-samples";
    const surface = document.createElement("div");
    surface.className = "card";
    surface.dataset.part = "card";
    for (const tier of ["top", "high", "mid", "free"]) {
      const pill = document.createElement("span");
      pill.className = "plan-pill";
      pill.dataset.part = "plan-pill";
      pill.dataset.planTier = tier;
      pill.textContent = `${tier} plan`;
      surface.append(pill, " ");
    }
    for (const tone of ["ok", "warn", "danger"]) {
      const meter = document.createElement("div");
      meter.className = "meter";
      meter.dataset.part = "meter";
      meter.dataset.mode = "left";
      meter.dataset.tone = tone;
      const fill = document.createElement("span");
      fill.style.width = "50%";
      meter.append(fill);
      surface.append(meter);
    }
    const table = document.createElement("table");
    table.className = "table";
    table.innerHTML = "<thead><tr><th>Heading</th></tr></thead><tbody><tr><td>Cell</td></tr></tbody>";
    surface.append(table);
    host.append(surface);
    for (const tone of ["danger", "warn", "ok", "info", "update", "muted"]) {
      const notice = document.createElement("p");
      notice.className = "notice";
      notice.dataset.tone = tone;
      notice.textContent = `${tone} notice`;
      host.append(notice);
    }
    document.querySelector("main")?.prepend(host);
  });
}

/** A CSS colour (any syntax the browser knows) as sRGB bytes, read back from a canvas. */
function rgbScript() {
  return `
    const ctx = document.createElement("canvas").getContext("2d");
    const rgb = (c) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = "#000"; ctx.fillStyle = c; ctx.fillRect(0, 0, 1, 1); return [...ctx.getImageData(0, 0, 1, 1).data]; };
    const lum = ([r, g, b]) => { const l = (v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4; }; return 0.2126 * l(r) + 0.7152 * l(g) + 0.0722 * l(b); };
    const ratio = (a, b) => { const [h, o] = [lum(a), lum(b)].sort((x, y) => y - x); return (h + 0.05) / (o + 0.05); };
    const over = (top, below) => { const a = top[3] / 255; return [0, 1, 2].map((i) => top[i] * a + below[i] * (1 - a)).concat(255); };
    const bgOf = (el) => {
      const layers = [];
      for (let e = el; e; e = e.parentElement) { const c = rgb(getComputedStyle(e).backgroundColor); if (c[3] > 0) layers.push(c); }
      let b = [255, 255, 255, 255];
      for (const l of layers.reverse()) b = over(l, b);
      return b;
    };
  `;
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    const name = `${theme || "pluxy/default"} in ${appearance}`;

    test(`navigation, headings, notices and plain pills are readable: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await addSamples(page);
      const parts = page.locator(
        [
          '[data-part="nav-item"] .nav-label',
          ".status-text b",
          ".status-text span",
          '[data-part="page-title"]',
          "#look-samples .notice",
          "#look-samples thead th",
          '#look-samples .plan-pill[data-plan-tier="mid"]',
          '#look-samples .plan-pill[data-plan-tier="free"]',
        ].join(", "),
      );
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      // Five destinations, the status block's two lines, the title, six notices, a heading, two pills.
      expect(measured.length).toBeGreaterThanOrEqual(16);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `"${m.text}": ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);
    });

    test(`metal plan pills are readable across their whole finish: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await addSamples(page);
      const results = await page.evaluate(`(() => {
        ${rgbScript()}
        const out = [];
        for (const tier of ["top", "high"]) {
          const pill = document.querySelector('#look-samples .plan-pill[data-plan-tier="' + tier + '"]');
          const style = getComputedStyle(pill);
          const card = bgOf(pill.parentElement);
          const base = over(rgb(style.backgroundColor), card);
          // The finish is a gradient: check the text against each of its colour stops.
          const stops = style.backgroundImage.match(/(?:rgba?|color|oklab|oklch|lab|lch)\\([^()]*\\)/g) ?? [];
          if (stops.length === 0) stops.push(style.backgroundColor);
          const text = over(rgb(style.color), base);
          for (const stop of stops) out.push({ tier, stop, ratio: ratio(text, over(rgb(stop), base)) });
        }
        return out;
      })()`);
      const list = results as { tier: string; stop: string; ratio: number }[];
      expect(list.length).toBeGreaterThanOrEqual(2);
      const low = list
        .filter((r) => r.ratio < AA)
        .map((r) => `${r.tier} on ${r.stop}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });

    test(`a gauge's fill and a badge's dot stand out from what they sit on: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await addSamples(page);
      await page.evaluate(() => {
        const box = document.querySelector("#look-samples .card");
        for (const tone of ["ok", "warn", "danger", "info", "update"]) {
          const badge = document.createElement("span");
          badge.className = "badge";
          badge.dataset.part = "badge";
          badge.dataset.tone = tone;
          badge.textContent = tone;
          box?.append(badge, " ");
        }
      });
      const results = await page.evaluate(`(() => {
        ${rgbScript()}
        const out = [];
        const card = document.querySelector("#look-samples .card");
        const ground = bgOf(card);
        for (const meter of document.querySelectorAll("#look-samples .meter")) {
          const track = over(rgb(getComputedStyle(meter).backgroundColor), ground);
          const fill = over(rgb(getComputedStyle(meter.firstElementChild).backgroundColor), track);
          out.push({ what: "gauge " + meter.dataset.tone + " on its track", ratio: ratio(fill, track) });
        }
        for (const badge of document.querySelectorAll("#look-samples .badge")) {
          const dot = over(rgb(getComputedStyle(badge, "::before").backgroundColor), ground);
          out.push({ what: "dot " + badge.dataset.tone + " on the card", ratio: ratio(dot, ground) });
        }
        return out;
      })()`);
      const list = results as { what: string; ratio: number }[];
      expect(list).toHaveLength(8);
      const low = list.filter((r) => r.ratio < GRAPHIC).map((r) => `${r.what}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });

    test(`the first-launch starting bar clears 3:1 with reduced motion: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await page.emulateMedia({ colorScheme: appearance, reducedMotion: "reduce" });
      await page.evaluate((css) => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(css);
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
        const card = document.createElement("div");
        card.id = "first-progress-sample";
        card.className = "focus-card";
        card.innerHTML = '<div class="first-progress-bar"><span></span></div>';
        document.querySelector("main")?.prepend(card);
      }, appCss);
      const result = await page.evaluate(`(() => {
        ${rgbScript()}
        const card = document.querySelector("#first-progress-sample");
        const bar = card.querySelector(".first-progress-bar");
        const fill = bar.firstElementChild;
        const ground = bgOf(card);
        const track = over(rgb(getComputedStyle(bar).backgroundColor), ground);
        const style = getComputedStyle(fill);
        const raw = rgb(style.backgroundColor);
        raw[3] = Math.round(raw[3] * Number(style.opacity));
        const painted = over(raw, track);
        return { ratio: ratio(painted, track), animation: style.animationName };
      })()`);
      const { ratio, animation } = result as { ratio: number; animation: string };
      expect(animation).toBe("none");
      expect(ratio).toBeGreaterThanOrEqual(GRAPHIC);
    });

    test(`the Setup stepper connectors clear 3:1 against the focused ground: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await page.evaluate(() => {
        const focus = document.createElement("div");
        focus.id = "stepper-sample";
        focus.className = "focus";
        focus.innerHTML =
          '<ol class="setup-stepper"><li data-state="done"><button type="button">1</button></li><li data-state="open"><button type="button">2</button></li><li data-state="todo"><button type="button">3</button></li></ol>';
        document.querySelector("main")?.prepend(focus);
      });
      const results = await page.evaluate(`(() => {
        ${rgbScript()}
        const focus = document.querySelector("#stepper-sample");
        const ground = bgOf(focus);
        const out = [];
        for (const li of focus.querySelectorAll(".setup-stepper > li:not(:last-child)")) {
          const line = over(rgb(getComputedStyle(li, "::after").backgroundColor), ground);
          out.push({ state: li.dataset.state, ratio: ratio(line, ground) });
        }
        return out;
      })()`);
      const list = results as { state: string; ratio: number }[];
      expect(list).toHaveLength(2);
      const low = list.filter((r) => r.ratio < GRAPHIC).map((r) => `${r.state}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });
  }
}
