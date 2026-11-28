// The colours the look adds stay readable in every built-in theme, light and dark: text at 4.5:1
// (WCAG AA) and dots and bars at 3:1 against what they sit on (WCAG 1.4.11). The pairs: the
// navigation on the window's ground, plan pills in their four finishes, tinted notices, table
// headings, and a limit's gauge against its track.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

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
    for (const series of ["1", "2", "3", "4", "5", "6"]) {
      const mark = document.createElement("span");
      mark.className = "provider-mark";
      mark.dataset.part = "provider-mark";
      mark.dataset.initial = "";
      mark.dataset.series = series;
      mark.textContent = "M";
      surface.append(mark, " ");
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
    const stale = document.createElement("div");
    stale.className = "home-body";
    stale.dataset.stale = "";
    stale.innerHTML =
      '<div class="home-fade"><div class="card"><span class="account-sub"><span class="account-word">Serving</span></span><p class="pool-foot"><span>2 of 3 serving</span></p></div></div>';
    host.append(stale);
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
    const effOpacity = (el) => {
      let o = 1;
      for (let e = el; e; e = e.parentElement) o *= Number.parseFloat(getComputedStyle(e).opacity);
      return o;
    };
    const withOpacity = (c, o) => [c[0], c[1], c[2], c[3] * o];
    // The colour stops of a gradient (computed background-image): its top-level arguments, less the
    // angle or direction and any stop position, each resolved by the browser on a probe.
    const gradientStops = (image) => {
      const open = image.indexOf("(");
      if (!/^(repeating-)?(linear|radial|conic)-gradient\\(/.test(image) || open < 0) return [];
      const args = [];
      let depth = 0, start = open + 1;
      for (let i = start; i < image.length; i++) {
        const c = image[i];
        if (c === "(") depth++;
        else if (c === ")") { if (depth === 0) { args.push(image.slice(start, i)); break; } depth--; }
        else if (c === "," && depth === 0) { args.push(image.slice(start, i)); start = i + 1; }
      }
      const probe = document.createElement("span");
      document.body.append(probe);
      const stops = [];
      for (const arg of args.map((a) => a.trim())) {
        if (/^(to\\s|[-\\d.]+(deg|turn|rad|grad)$)/.test(arg)) continue;
        // A stop position follows the colour at the top level: "color-mix(...) 55%".
        let colour = arg;
        const end = arg.lastIndexOf(")");
        if (end >= 0) colour = arg.slice(0, end + 1);
        else colour = arg.split(/\\s+/)[0];
        probe.style.backgroundColor = "";
        probe.style.backgroundColor = colour;
        if (probe.style.backgroundColor) stops.push(getComputedStyle(probe).backgroundColor);
      }
      probe.remove();
      return stops;
    };
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
          "#look-samples .provider-mark[data-initial]",
          "#look-samples .home-body[data-stale] .home-fade .account-word",
          "#look-samples .home-body[data-stale] .home-fade .pool-foot span",
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
          // The finish is a gradient: check the text against each of its colour stops. A finish whose
          // stops cannot be read counts as a failure, not as the card behind it.
          const stops = gradientStops(style.backgroundImage);
          out.push({ tier, stop: "stops read", ratio: stops.length >= 2 ? 99 : 0 });
          const text = over(rgb(style.color), base);
          for (const stop of stops) out.push({ tier, stop, ratio: ratio(text, over(rgb(stop), base)) });
        }
        return out;
      })()`);
      const list = results as { tier: string; stop: string; ratio: number }[];
      // Per tier: the "stops read" row and at least two stops.
      expect(list.length).toBeGreaterThanOrEqual(6);
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
        const host = document.querySelector("#look-samples");
        for (const tone of ["ok", "warn", "danger", "info", "update"]) {
          const badge = document.createElement("span");
          badge.className = "badge";
          badge.dataset.part = "badge";
          badge.dataset.tone = tone;
          badge.textContent = tone;
          box?.append(badge, " ");
        }
        const offRow = document.createElement("div");
        offRow.className = "account-row";
        offRow.dataset.group = "off";
        offRow.innerHTML =
          '<span class="account-mark"><span class="provider-mark">A</span><span class="group-mark" data-group="off" data-dot=""></span></span>';
        box?.append(offRow);
        const bar = document.createElement("div");
        bar.className = "home-state-bar";
        bar.innerHTML = '<span data-group="off"></span>';
        host?.append(bar);
        const poolBar = document.createElement("div");
        poolBar.className = "pool-bar";
        poolBar.innerHTML =
          '<span class="pool-segment" data-kind="unknown"></span><span class="pool-segment" data-kind="out"></span>';
        box?.append(poolBar);
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
        const offDot = document.querySelector('#look-samples .account-row[data-group="off"] .group-mark[data-dot]');
        const offDotColor = over(withOpacity(rgb(getComputedStyle(offDot).color), effOpacity(offDot)), ground);
        out.push({ what: "turned-off dot on the row", ratio: ratio(offDotColor, ground) });
        const offBar = document.querySelector('#look-samples .home-state-bar [data-group="off"]');
        const pageGround = bgOf(offBar.parentElement);
        const offBarColor = over(withOpacity(rgb(getComputedStyle(offBar).backgroundColor), effOpacity(offBar)), pageGround);
        out.push({ what: "turned-off bar on the page", ratio: ratio(offBarColor, pageGround) });
        for (const seg of document.querySelectorAll("#look-samples .pool-segment")) {
          const style = getComputedStyle(seg);
          const track = over(rgb(style.backgroundColor), ground);
          const stops = (style.backgroundImage.match(/(?:rgba?|color|oklab|oklch|lab|lch)\\([^()]*\\)/g) ?? [])
            .map(rgb)
            .filter((c) => c[3] > 0);
          const stroke = stops[0] ? over(stops[0], track) : track;
          out.push({ what: "pool hatch " + seg.dataset.kind + " on its track", ratio: ratio(stroke, track) });
        }
        return out;
      })()`);
      const list = results as { what: string; ratio: number }[];
      expect(list).toHaveLength(12);
      const low = list.filter((r) => r.ratio < GRAPHIC).map((r) => `${r.what}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });

    test(`selected text, switches, inputs and chart series are clear: ${name}`, async ({ page }) => {
      await open(page, theme, appearance);
      await addSamples(page);
      await page.evaluate(() => {
        const box = document.querySelector("#look-samples .card");
        const track = document.createElement("span");
        track.className = "track";
        const input = document.createElement("input");
        input.type = "text";
        input.dataset.slot = "input";
        const words = document.createElement("p");
        words.id = "look-selected";
        words.textContent = "Selected words";
        box?.append(track, input, words);
        for (const n of [1, 2, 3, 4, 5, 6]) {
          const bar = document.createElement("span");
          bar.dataset.series = String(n);
          // Only these probes are measured: the samples also hold marks that carry a series.
          bar.dataset.probe = "series";
          bar.style.display = "inline-block";
          bar.style.width = "12px";
          bar.style.height = "12px";
          box?.append(bar);
          const onPage = bar.cloneNode() as HTMLElement;
          onPage.dataset.onPage = "";
          document.querySelector("#look-samples")?.append(onPage);
        }
      });
      const results = await page.evaluate(`(() => {
        ${rgbScript()}
        const out = [];
        const card = document.querySelector("#look-samples .card");
        const ground = bgOf(card);
        // Selected text: the ::selection colours over the card.
        const words = document.getElementById("look-selected");
        const sel = getComputedStyle(words, "::selection");
        const selBg = over(rgb(sel.backgroundColor), ground);
        out.push({ what: "selected text", ratio: ratio(over(rgb(sel.color), selBg), selBg), min: 4.5 });
        // An off switch: its track against the card, and its knob against the track.
        const trackEl = card.querySelector(".track");
        const track = over(rgb(getComputedStyle(trackEl).backgroundColor), ground);
        const knob = over(rgb(getComputedStyle(trackEl, "::after").backgroundColor), track);
        out.push({ what: "off switch track on the card", ratio: ratio(track, ground), min: 3 });
        out.push({ what: "off switch knob on its track", ratio: ratio(knob, track), min: 3 });
        // An input's border against the card and against the input's own fill.
        const input = card.querySelector("input");
        const fill = over(rgb(getComputedStyle(input).backgroundColor), ground);
        const edge = over(rgb(getComputedStyle(input).borderTopColor), ground);
        out.push({ what: "input border on the card", ratio: ratio(edge, ground), min: 3, inputs: true });
        out.push({ what: "input border on its fill", ratio: ratio(edge, fill), min: 3, inputs: true });
        // Chart series on a card and on the page.
        for (const bar of document.querySelectorAll('#look-samples [data-probe="series"]')) {
          const under = bgOf(bar.parentElement);
          const colour = over(rgb(getComputedStyle(bar).backgroundColor), under);
          const where = "onPage" in bar.dataset ? "the page" : "the card";
          out.push({ what: "series " + bar.dataset.series + " on " + where, ratio: ratio(colour, under), min: 3 });
        }
        return out;
      })()`);
      const list = results as { what: string; ratio: number; min: number; inputs?: boolean }[];
      expect(list).toHaveLength(17);
      // Paper and Terminal keep their own faint input borders, as before: the 3:1 floor is the
      // default theme's.
      const low = list
        .filter((r) => !(r.inputs && theme !== ""))
        .filter((r) => r.ratio < r.min)
        .map((r) => `${r.what}: ${r.ratio.toFixed(2)}:1`);
      expect(low).toEqual([]);
    });
  }
}
