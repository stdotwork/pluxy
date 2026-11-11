import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

/** One chat request to the client API with the client key; the model picks what the fake does. */
async function chat(model: string): Promise<number> {
  const r = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }),
  });
  await r.arrayBuffer();
  return r.status;
}

const stat = (page: Page, name: string) =>
  page.locator('[data-part="stat"]').filter({ has: page.getByText(name, { exact: true }) });

test.beforeAll(async () => {
  // A sign-in account serves the fake's models fake-model and server-error.
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: "dash-a",
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "test-dash-a" },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`)).body.login
          .state,
    )
    .toBe("done");
  for (const model of ["fake-model", "fake-model", "fake-model"]) expect(await chat(model)).toBe(200);
  expect(await chat("server-error")).toBeGreaterThanOrEqual(500);
});

test("the Dashboard sums up the requests core recorded and the accounts it holds", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await expect(nav(page).getByRole("link", { name: "Status" })).toHaveCount(0);
  await goTo(page, "Dashboard");

  await expect(page.getByRole("heading", { level: 2, name: "Needs attention" })).toBeVisible();
  await expect(stat(page, "Requests handled").locator("strong")).toHaveText("4");
  await expect(page.getByText("Rolling 3h 20m window · 10-minute buckets")).toBeVisible();
  await expect(stat(page, "Success rate").locator("strong")).toHaveText("75%");
  await expect(stat(page, "Success rate")).toContainText("Across 4 requests in the window");

  // The current bucket is the table's first row, and holds all four requests: 3 and 1.
  const table = page.getByRole("table", { name: "Requests in each 10-minute bucket" });
  await expect(table.locator("tbody tr").first().locator("td")).toHaveText(["3", "1"]);
  const chart = page.locator('svg[data-part="chart"]');
  await expect(chart.locator('[data-outcome="success"]')).toHaveCount(1);
  await expect(chart.locator('[data-outcome="failure"]')).toHaveCount(1);

  // Credentials: what GET /accounts reports.
  const accounts = (
    await core.mgmt<{ accounts: { disabled?: boolean; state: string }[] }>("GET", "/accounts")
  ).body.accounts;
  const active = accounts.filter((a) => !a.disabled && a.state === "active").length;
  expect(accounts.length).toBeGreaterThan(0);
  const credentials = stat(page, "Credentials");
  await expect(credentials.locator("strong")).toHaveText(String(accounts.length));
  await expect(credentials).toContainText(`${active} active · ${accounts.length - active} not serving`);

  // Fleet: requests and success rate per extension; Health and Runtime link on.
  await expect(
    page.getByRole("table", { name: "Every provider, side by side" }).locator("tbody tr"),
  ).not.toHaveCount(0);
  await expect(page.getByRole("link", { name: "Open auth files" })).toHaveAttribute("href", "#/accounts");
  await expect(page.getByRole("link", { name: "Edit configuration" })).toHaveAttribute("href", "#/config");
  await expect(page.getByText(/^Updated /)).toBeVisible();

  if (process.env.DASHBOARD_SHOTS) {
    for (const theme of ["default", "terminal", "paper"]) {
      await page.locator('nav a[href="#/themes"]').click();
      await page.getByRole("radio", { name: new RegExp(`^${theme}`, "i") }).check();
      await page.locator('nav a[href="#/status"]').click();
      await expect(stat(page, "Requests handled").locator("strong")).toHaveText("4");
      await page.screenshot({ path: `${process.env.DASHBOARD_SHOTS}/${theme}.png`, fullPage: true });
    }
  }
});

test("the Dashboard refreshes every minute, and only while the document is visible", async ({ page }) => {
  const reads: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.endsWith("/v0/management/usage")) reads.push(r.url());
  });
  await page.clock.install();
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Dashboard");
  await expect.poll(() => reads.length).toBe(1);

  await page.clock.fastForward(61_000);
  await expect.poll(() => reads.length).toBe(2);

  const setVisibility = (state: "hidden" | "visible") =>
    page.evaluate((s) => {
      Object.defineProperty(document, "visibilityState", { value: s, configurable: true });
      document.dispatchEvent(new Event("visibilitychange"));
    }, state);
  await setVisibility("hidden");
  await page.clock.fastForward(180_000);
  expect(reads.length).toBe(2);

  // Coming back after more than a minute reads again at once.
  await setVisibility("visible");
  await expect.poll(() => reads.length).toBe(3);
});
