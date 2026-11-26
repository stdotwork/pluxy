// The kit's shared controls take their sizes from the theme's tokens, and a table wider than its
// pane says which side has more columns instead of cutting them off.
import { expect, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

test.beforeAll(async () => {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: "controls-a",
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "ak-controls-test" },
  });
  expect(r.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
});

async function openAccounts(page: Page) {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  await expect(page.getByRole("button", { name: "controls-a", exact: true })).toBeVisible();
}

/** The rendered heights of the page's buttons, one-line inputs and selects. */
function heights(page: Page) {
  return page.evaluate(() =>
    [
      ...document.querySelectorAll<HTMLElement>(
        '.btn:not([data-size]), input[type="search"], input[type="text"], select',
      ),
    ]
      .filter((el) => el.getBoundingClientRect().height > 0)
      .map((el) => ({
        main: !!el.closest('[data-part="header-actions"]') && el.dataset.variant === "primary",
        height: Math.round(el.getBoundingClientRect().height),
      })),
  );
}

for (const [theme, control, main] of [
  ["", 28, 32],
  ["pluxy/terminal", 24, 28],
] as const) {
  test(`buttons, inputs and selects are size.control high: ${theme || "pluxy/default"}`, async ({ page }) => {
    await core.mgmt("PUT", "/theme", { id: theme });
    await openAccounts(page);
    const all = await heights(page);
    expect(all.filter((c) => !c.main).length).toBeGreaterThan(5);
    expect(new Set(all.filter((c) => !c.main).map((c) => c.height))).toEqual(new Set([control]));
    // The page's one main action, in the header strip, is size.control-main.
    expect(all.filter((c) => c.main).map((c) => c.height)).toEqual([main]);
  });
}

test("a table wider than its pane fades out on the side with more columns", async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 480 });
  await openAccounts(page);
  const frame = page.locator('[data-part="list-frame"]').first();
  const cue = (side: "::before" | "::after") =>
    frame.evaluate((el, s) => getComputedStyle(el, s).opacity, side);
  await expect(frame).toHaveAttribute("data-more", "end");
  await expect.poll(() => cue("::after")).toBe("1");
  expect(await cue("::before")).toBe("0");
  await frame.locator(".table-wrap").evaluate((w) => w.scrollTo({ left: w.scrollWidth }));
  await expect(frame).toHaveAttribute("data-more", "start");
  await expect.poll(() => cue("::before")).toBe("1");
  expect(await cue("::after")).toBe("0");
  // The headings are in sentence case, as written.
  const heading = frame.locator("thead th").first();
  expect(await heading.evaluate((th) => getComputedStyle(th).textTransform)).toBe("none");
  expect(await heading.evaluate((th) => getComputedStyle(th).letterSpacing)).toBe("normal");
});
