// The shared parts (kit/parts) in the panel's own styles: a test-only specimen of every part in
// each of its states (e2e/specimen), run inside the signed-in panel, in each built-in theme, light
// and dark, at the two window sizes the app is checked at. Nothing scrolls sideways, every button is
// as high as the theme's controls (28 pixels on the default theme) and never under 24, and the
// parts' text reads at 4.5:1 or more.
//
// With PLUXY_PARTS_SHOTS set to a folder, the spec also saves screenshots of the whole specimen
// there, one per pane's height, for a person to look at.
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const SIZES = [
  { width: 1100, height: 760 },
  { width: 640, height: 480 },
] as const;
const AA = 4.5;
const SHOTS = process.env.PLUXY_PARTS_SHOTS;

const bundle = join(import.meta.dirname, "..", "test-results", "specimen", "specimen.js");

test.beforeAll(() => {
  mkdirSync(join(bundle, ".."), { recursive: true });
  execFileSync(
    "bun",
    [
      "build",
      join(import.meta.dirname, "specimen", "specimen.tsx"),
      "--outfile",
      bundle,
      "--target",
      "browser",
      "--format",
      "iife",
    ],
    { stdio: "inherit" },
  );
});

/** Opens the panel signed in, puts the specimen in its pane in place of the page, and waits for it. */
async function openSpecimen(
  page: Page,
  theme: string,
  appearance: string,
  size: { width: number; height: number },
) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.setViewportSize(size);
  await page.emulateMedia({ colorScheme: appearance as "light" | "dark", reducedMotion: "reduce" });
  // The bundle is served from the panel's own address, so the panel's script rules allow it.
  await page.route("**/panel/specimen.js", (route) =>
    route.fulfill({ path: bundle, contentType: "text/javascript" }),
  );
  await openPanel(page, core);
  await signIn(page, core);
  await page.evaluate(() => {
    const main = document.querySelector('[data-part="main"]');
    if (!main) throw new Error("no pane");
    // A section, not a div: the page's own renderer must not take it for the page's wrapper.
    const box = document.createElement("section");
    box.id = "specimen";
    // A page's own wrapper, so the specimen sits where a page's content does.
    box.className = "content";
    main.prepend(box);
    // The page is hidden by its own style property, kept so as the page draws again: the panel's
    // content security policy refuses a <style> element added now, and the theme engine sets the
    // document's style sheets.
    const hide = () => {
      for (const el of main.children)
        if (el !== box) (el as HTMLElement).style.setProperty("display", "none", "important");
    };
    hide();
    new MutationObserver(hide).observe(main, { childList: true });
  });
  await page.addScriptTag({ url: `${core.base}/panel/specimen.js` });
  await expect(page.locator('#specimen [data-part="account-row"]').first()).toBeVisible();
}

const specimen = (page: Page) => page.locator("#specimen");

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    for (const size of SIZES) {
      const name = `${theme || "pluxy/default"} ${appearance} ${size.width}x${size.height}`;
      test(`the shared parts fit and read well: ${name}`, async ({ page }) => {
        await openSpecimen(page, theme, appearance, size);
        const s = specimen(page);

        // The states a press brings: a fix that failed, and a question asked.
        await s
          .getByRole("button", { name: /^Turn on: old/ })
          .first()
          .click();
        await expect(s.locator(".fix-error").first()).toBeVisible();
        await s.getByRole("button", { name: "Replace key", exact: true }).click();
        await expect(s.getByText("Replace the key? Every other window signs out.")).toBeVisible();

        // Nothing scrolls sideways: not the window, not the pane, not a part.
        const wide = await page.evaluate(() => {
          const over = (el: Element) => el.scrollWidth > el.clientWidth + 1;
          const out: string[] = [];
          if (document.documentElement.scrollWidth > window.innerWidth + 1) out.push("the window");
          const main = document.querySelector('[data-part="main"]');
          if (main && over(main)) out.push("the pane");
          for (const el of document.querySelectorAll(
            '#specimen [data-part="account-row"], #specimen [data-part="request-row"], #specimen [data-part="setting-row"], #specimen [data-part="tool-row"], #specimen [data-part="state-pane"]',
          )) {
            if (over(el)) out.push(`${el.getAttribute("data-part")}: ${(el.textContent ?? "").slice(0, 40)}`);
          }
          return out;
        });
        expect(wide).toEqual([]);

        // The pane shows the specimen alone, and the page's renderer kept out of it.
        const shown = await page.evaluate(() =>
          [...(document.querySelector('[data-part="main"]')?.children ?? [])]
            .filter((el) => getComputedStyle(el).display !== "none")
            .map((el) => `${el.tagName.toLowerCase()}#${el.id}.${el.className}`),
        );
        expect(shown).toEqual(["section#specimen.content"]);
        expect(await s.locator(":scope > *").count()).toBe(1);

        // No name, setting text or model is squeezed below what can be read: 4em of its own text.
        const squeezed = await page.evaluate(() =>
          [
            ...document.querySelectorAll(
              "#specimen .account-name, #specimen .setting-about, #specimen .request-model, #specimen .tool-name",
            ),
          ]
            .filter((el) => {
              const em = Number.parseFloat(getComputedStyle(el).fontSize);
              // scrollWidth is a whole number of pixels: a pixel's difference is rounding, not a cut.
              return el.getBoundingClientRect().width + 1 < Math.min(4 * em, el.scrollWidth);
            })
            .map(
              (el) =>
                `${el.className}: ${(el.textContent ?? "").slice(0, 30)} ${el.getBoundingClientRect().width}px`,
            ),
        );
        expect(squeezed).toEqual([]);

        // Every button is as high as the theme's controls (size.control: 28 pixels on the default
        // theme, 36 on Paper, 24 on Terminal, the compact one), and never under 24 pixels.
        const small = await page.evaluate(() => {
          const probe = document.createElement("div");
          probe.style.height = "var(--px-size-control)";
          document.body.append(probe);
          const least = Math.max(24, probe.getBoundingClientRect().height);
          probe.remove();
          return [
            ...document.querySelectorAll(
              '#specimen button, #specimen a[data-part="button"], #specimen a.request-row',
            ),
          ]
            .map((el) => ({ el, box: el.getBoundingClientRect() }))
            .filter(({ box }) => box.width > 0 && box.height < least - 0.5)
            .map(
              ({ el, box }) =>
                `${(el.textContent || el.getAttribute("aria-label") || "").slice(0, 30)}: ${box.height}px < ${least}px`,
            );
        });
        expect(small).toEqual([]);

        // The parts' text reads at 4.5:1 or more on what it sits on.
        const texts = s.locator(
          [
            ".account-name",
            ".account-provider",
            ".account-reason",
            ".plan-pill",
            ".limit-name",
            ".limit-left",
            ".limit-reset",
            '.limits:not([data-state="read"])',
            ".account-week",
            ".fix-error",
            ".request-time",
            ".request-model",
            ".request-account",
            ".request-key",
            ".request-tokens",
            ".request-taken",
            ".request-result > span",
            ".tool-name",
            ".tool-version",
            ".tool-path",
            '[data-part="badge"]',
            ".setting-name",
            ".setting-description",
            ".setting-key",
            ".setting-saving",
            ".setting-why",
            ".confirm-question",
            ".state-pane-title",
            ".state-pane-text p",
            ".state-pane-details summary",
            ".notice",
          ].join(", "),
        );
        const measured = await measureContrast(texts);
        const labels = await texts.evaluateAll((els) =>
          els.map((el) => `${el.className || el.tagName}: ${(el.textContent ?? "").trim().slice(0, 30)}`),
        );
        expect(measured.length).toBeGreaterThan(50);
        const low = measured
          .map((m, i) => ({ label: labels[i], ...m }))
          .filter((m) => m.ratio < AA)
          .map((m) => `${m.label} ${m.ratio.toFixed(2)}:1 (${m.fg} on ${m.bg})`);
        expect(low).toEqual([]);

        if (SHOTS) {
          await page.mouse.move(0, size.height - 1);
          const slug = name.replaceAll("/", "-").replaceAll(" ", "_");
          const pane = page.locator('[data-part="main"]');
          const { scrollHeight, clientHeight } = await pane.evaluate((el) => ({
            scrollHeight: el.scrollHeight,
            clientHeight: el.clientHeight,
          }));
          for (let top = 0, n = 1; top < scrollHeight; top += clientHeight, n++) {
            await pane.evaluate((el, y) => el.scrollTo(0, y), top);
            await page.screenshot({ path: join(SHOTS, `${slug}_${String(n).padStart(2, "0")}.png`) });
          }
        }
      });
    }
  }
}
