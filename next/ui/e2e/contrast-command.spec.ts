// The command menu's text stays readable (WCAG AA, 4.5:1) in every built-in theme, light and dark:
// the Search field in the navigation, and in the menu the group headings, names, hints, the row the
// keys are on, the keys' own labels and the empty state.
import { expect, type Page, test } from "@playwright/test";
import { measureContrast } from "./contrast";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const AA = 4.5;

async function open(page: Page, theme: string, appearance: (typeof APPEARANCES)[number]) {
  await core.mgmt("PUT", "/theme", { id: theme });
  await page.emulateMedia({ colorScheme: appearance });
  await openPanel(page, core);
  await signIn(page, core);
}

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    test(`the Search field and the menu are readable: ${theme || "pluxy/default"} in ${appearance}`, async ({
      page,
    }) => {
      await open(page, theme, appearance);
      await page.keyboard.press("Control+k");
      await expect(page.getByRole("combobox")).toBeFocused();
      // Names, hints, headings and keys of the empty menu; the selected row is the first.
      const parts = page.locator(
        [
          '[data-part="search"] .nav-label',
          '[data-part="search"] kbd',
          ".command-heading",
          ".command-label",
          ".command-hint",
          ".command-field kbd",
          ".command-foot",
        ].join(", "),
      );
      const measured = await measureContrast(parts);
      const texts = await parts.allTextContents();
      expect(measured.length).toBeGreaterThanOrEqual(12);
      const low = measured
        .map((m, i) => ({ text: texts[i]?.trim().slice(0, 30), ...m }))
        .filter((m) => m.ratio < AA)
        .map((m) => `${m.text}: ${m.ratio.toFixed(2)} (${m.fg} on ${m.bg})`);
      expect(low).toEqual([]);

      // Typing a name marks what was found, and the empty state is plain text.
      await page.keyboard.type("acc");
      const marked = await measureContrast(page.locator(".command mark"));
      expect(marked.length).toBeGreaterThan(0);
      expect(marked.filter((m) => m.ratio < AA).map((m) => m.ratio.toFixed(2))).toEqual([]);
      await page.keyboard.type("zzzq");
      const empty = await measureContrast(page.locator(".command-empty b, .command-empty span"));
      expect(empty).toHaveLength(2);
      expect(empty.filter((m) => m.ratio < AA).map((m) => m.ratio.toFixed(2))).toEqual([]);
    });
  }
}
