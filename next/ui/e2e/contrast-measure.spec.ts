import { expect, test } from "@playwright/test";
import { measureContrast } from "./contrast";

test("measureContrast reads a background reported as oklab() mid-transition", async ({ page }) => {
  await page.setContent(`
    <div style="background: rgb(250 250 250)">
      <div style="background: oklab(0.957881 0.0131752 0.00357771 / 0.338903)">
        <span style="color: rgb(196 43 49)">Needs sign-in</span>
      </div>
    </div>`);
  const [measured] = await measureContrast(page.locator("span"));
  expect(measured?.bg).toBe("rgb(250 246 246)");
  expect(measured?.ratio).toBeGreaterThan(4.5);
});

test("measureContrast reads rgb() and color(srgb) as before", async ({ page }) => {
  await page.setContent(`
    <div style="background: color(srgb 0.98102 0.931765 0.933333)">
      <span style="color: rgba(0, 0, 0, 0.5)">text</span>
    </div>`);
  const [measured] = await measureContrast(page.locator("span"));
  expect(measured?.bg).toBe("rgb(250 238 238)");
  expect(measured?.fg).toBe("rgb(125 119 119)");
});
