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
    input: { api_key: "ak-controls-test", label: "controls-a" },
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
  await expect(page.getByRole("link", { name: "controls-a", exact: true })).toBeVisible();
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
    const list = await heights(page);
    // An account's page, with its settings, models and test request open, has the most controls.
    await page.getByRole("link", { name: "controls-a", exact: true }).click();
    await page.getByText("Settings, models and a test request", { exact: true }).click();
    await expect(page.getByRole("button", { name: "Send a test request", exact: true })).toBeVisible();
    const all = [...list, ...(await heights(page))];
    expect(all.filter((c) => !c.main).length).toBeGreaterThan(5);
    expect(new Set(all.filter((c) => !c.main).map((c) => c.height))).toEqual(new Set([control]));
    // The page's one main action, in the header strip, is size.control-main.
    expect(all.filter((c) => c.main).map((c) => c.height)).toEqual([main]);
  });
}

test("a table wider than its pane fades out on the side with more columns", async ({ page }) => {
  // Order & fallback's first tier is wider than a phone-sized window.
  await page.setViewportSize({ width: 380, height: 480 });
  // An account whose long name makes its tier wider than the window.
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const reply = await route.fetch();
    const body = (await reply.json()) as { accounts: unknown[] };
    const wide = {
      id: "anaccountwhosenamehasnoplacetobreakandmakesitstierwiderthanthewindow",
      source: "login",
      state: "active",
      refreshable: false,
      extension: "test/alpha",
    };
    await route.fulfill({ response: reply, json: { accounts: [...body.accounts, wide] } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/routing`);
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
