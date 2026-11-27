// Home (page ID `status`, file name kept): a new install says what to do next; a core with
// accounts and requests shows the verdict, the accounts by group with their limits, the last
// seven days and the latest requests; and each way it can fail says so once.
import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const verdict = (page: Page, name: string | RegExp) =>
  page.getByRole("heading", { level: 2, name, exact: typeof name === "string" });

test.describe("a new install", () => {
  const core = useCore({ firstRun: true });

  test("says there are no accounts and offers the next step, with no chart", async ({ page }) => {
    // A first run opens Setup by itself unless the address names a page.
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    await expect(page.getByRole("heading", { level: 1, name: "Home", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { level: 2, name: "pluxy has no accounts yet." })).toBeVisible();
    await expect(
      page.getByText(
        "Add the account you use for Claude, ChatGPT, Gemini or another service, and your coding tools can use it through pluxy.",
      ),
    ).toBeVisible();
    await expect(page.getByRole("link", { name: "Add an account", exact: true })).toHaveAttribute(
      "href",
      "#/add-account",
    );
    await expect(page.getByRole("link", { name: "Guided setup", exact: true })).toHaveAttribute(
      "href",
      "#/setup",
    );
    await expect(page.locator('[data-part="days"]')).toHaveCount(0);
    await expect(page.locator('[data-part="account-row"]')).toHaveCount(0);
    await expect(page.getByText("Address for tools")).toHaveCount(0);
    await expect(page.getByText("0 requests")).toHaveCount(0);
  });

  test("names the coding tools core found, when it found some", async ({ page }) => {
    await page.route("**/v0/management/setup", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const reply = await route.fetch();
      const body = await reply.json();
      body.found.clients = [
        { id: "claude-code", name: "Claude Code", installed: true, config: "", state: "not-configured" },
        { id: "codex", name: "Codex", installed: true, config: "", state: "not-configured" },
        { id: "grok", name: "Grok Build", installed: false, config: "", state: "unchecked" },
      ];
      await route.fulfill({ response: reply, json: body });
    });
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    await expect(page.getByText("Found: Claude Code, Codex.", { exact: true })).toBeVisible();
    await expect(page.getByText("Next: connect a coding tool.")).toHaveCount(0);
  });

  test("the new-install screen fits a small window", async ({ page }) => {
    await page.setViewportSize({ width: 640, height: 480 });
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    await expect(page.getByRole("link", { name: "Add an account", exact: true })).toBeInViewport();
    const main = page.locator('[data-part="main"]');
    expect(await main.evaluate((m) => m.scrollWidth <= m.clientWidth)).toBe(true);
  });
});

test.describe("a core with accounts and requests", () => {
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
          (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`)).body
            .login.state,
      )
      .toBe("done");
    for (let i = 0; i < 11; i++) expect(await chat("fake-model")).toBe(200);
    for (let i = 0; i < 3; i++) expect(await chat("server-error")).toBeGreaterThanOrEqual(500);
  });

  /** The limits core would carry on each account, so the rows show 5-hour and weekly limits. */
  async function withLimits(page: Page) {
    await page.route("**/v0/management/accounts", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const reply = await route.fetch();
      const body = (await reply.json()) as { accounts: Record<string, unknown>[] };
      const reset = Math.floor(Date.now() / 1000);
      const accounts = body.accounts.map((a) => ({
        ...a,
        quota: {
          fetched_at: new Date().toISOString(),
          quota: {
            plan: "Pro",
            limits: [
              {
                id: "five_hour",
                label: "5h",
                window_seconds: 18_000,
                used_percent: 42,
                resets_at: reset + 5_400,
              },
              {
                id: "weekly",
                label: "wk",
                window_seconds: 604_800,
                used_percent: 7,
                resets_at: reset + 200_000,
              },
            ],
          },
        },
      }));
      await route.fulfill({ response: reply, json: { ...body, accounts } });
    });
  }

  test("shows the verdict, the groups with limits, 7 bars and 10 latest rows, and All requests opens Activity", async ({
    page,
  }) => {
    await withLimits(page);
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    await expect(page.getByRole("heading", { level: 1, name: "Home", exact: true })).toBeVisible();
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
    await expect(page.locator(".home-counts")).toHaveText(
      /^\d+ serving · 14 requests in 7 days, 78\.5% succeeded$/,
    );

    const serving = page.getByRole("region", { name: "Serving", exact: true });
    await expect(serving.getByRole("link", { name: "Test account", exact: true })).toBeVisible();
    const limits = serving.locator('[data-part="account-row"]').first().locator('[data-part="limits"]');
    await expect(limits).toContainText("5h");
    await expect(limits).toContainText("58% left");
    await expect(limits).toContainText("wk");
    await expect(limits).toContainText("93% left");
    await expect(page.getByRole("button", { name: "Reload config" })).toHaveCount(0);

    const days = page.locator('[data-part="days"]');
    await expect(days).toHaveAttribute("aria-label", "14 requests in the last 7 days, 3 failed");
    await expect(days.locator(".day")).toHaveCount(7);
    await expect(days.locator(".day-name").last()).toHaveText("Today");
    await expect(days.locator(".days-top")).toHaveText("14");
    await days.locator(".day").last().hover();
    await expect(page.locator(".days-readout")).toHaveText(/^\w{3} \d{1,2} \w{3}: 14 requests, 3 failed$/);

    await expect(page.getByText("All 3 failed requests asked for server-error.")).toBeVisible();
    await expect(page.getByRole("link", { name: "See them", exact: true })).toHaveAttribute(
      "href",
      "#/activity?failed=1&model=server-error",
    );

    const rows = page.locator('[data-part="request-row"]');
    await expect(rows).toHaveCount(10);
    for (const href of await rows.evaluateAll((els) => els.map((e) => e.getAttribute("href"))))
      expect(href).toMatch(/^#\/activity\?trace=/);

    await expect(page.getByRole("link", { name: "Usage", exact: true })).toHaveAttribute(
      "href",
      "#/activity/usage",
    );
    await page.getByRole("link", { name: "All requests", exact: true }).click();
    await expect(nav(page).getByRole("link", { name: "Activity", exact: true })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(await page.evaluate(() => location.hash)).toBe("#/activity");
  });

  test("a latest request is a link that opens Activity", async ({ page }) => {
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    const first = page.locator('[data-part="request-row"]').first();
    await expect(first).toBeVisible();
    await first.focus();
    await page.keyboard.press("Enter");
    await expect(nav(page).getByRole("link", { name: "Activity", exact: true })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(await page.evaluate(() => location.hash)).toMatch(/^#\/activity\?trace=/);
  });

  test("with core stopped it shows one pane that says so, and Try again reads again", async ({ page }) => {
    await openPanel(page, core);
    await signIn(page, core);
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
    const refuse = (route: { abort: (code: string) => Promise<void> }) => route.abort("connectionrefused");
    await page.route("**/v0/management/**", refuse);
    await page.reload();
    await expect(page.getByRole("heading", { name: "Cannot reach pluxy." })).toBeVisible();
    await expect(page.getByText("Check that pluxy is running, then try again.")).toBeVisible();
    await expect(page.locator('[data-part="state-pane"]')).toHaveCount(1);
    await expect(page.getByText("Could not refresh")).toHaveCount(0);
    await expect(page.getByText("Could not read the last 7 days.")).toHaveCount(0);
    await expect(page.getByText("0 serving")).toHaveCount(0);

    await page.unroute("**/v0/management/**", refuse);
    await page.getByRole("button", { name: "Try again", exact: true }).click();
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
  });

  test("a refresh that fails keeps the rows and says so once", async ({ page }) => {
    await openPanel(page, core);
    await signIn(page, core);
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
    for (const path of ["accounts", "status", "usage", "usage/events"]) {
      await page.route(`**/v0/management/${path}?*`, (route) => route.abort("connectionrefused"));
      await page.route(`**/v0/management/${path}`, (route) => route.abort("connectionrefused"));
    }
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(page.getByText("Could not refresh.")).toHaveCount(1);
    await expect(page.locator('[data-part="account-row"]').first()).toBeVisible();
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
  });

  test("a refused settings change shows its error and a link to Config", async ({ page }) => {
    await page.route("**/v0/management/status", async (route) => {
      const reply = await route.fetch();
      const body = await reply.json();
      body.desired = {
        revision: body.active.revision + 1,
        at: body.active.at,
        error: "unknown key log-level",
      };
      await route.fulfill({ response: reply, json: body });
    });
    await openPanel(page, core);
    await signIn(page, core);
    await expect(
      page.getByText(
        /^pluxy refused the last change to its settings: unknown key log-level\. The settings from \d\d:\d\d still serve\./,
      ),
    ).toBeVisible();
    await expect(page.getByRole("link", { name: "Open Config", exact: true })).toHaveAttribute(
      "href",
      "#/config",
    );
  });

  test("with the record off it says so and links to the setting", async ({ page }) => {
    await page.route("**/v0/management/usage?*", async (route) => {
      const reply = await route.fetch();
      await route.fulfill({ response: reply, json: { ...(await reply.json()), enabled: false } });
    });
    await openPanel(page, core);
    await signIn(page, core);
    await expect(
      page.getByText("pluxy is not keeping a record of requests, so there is nothing to show here."),
    ).toBeVisible();
    await expect(page.getByRole("link", { name: "Turn it on in Config", exact: true })).toHaveAttribute(
      "href",
      "#/config/logging",
    );
    await expect(page.locator('[data-part="days"]')).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Latest requests" })).toHaveCount(0);
    await expect(page.locator(".home-counts")).not.toContainText("requests in 7 days");
    await page.getByRole("link", { name: "Turn it on in Config", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Config Panel" })).toBeVisible();
  });

  test("with no requests in 7 days it says so and names the coding tool state", async ({ page }) => {
    await page.route("**/v0/management/usage?*", async (route) => {
      const reply = await route.fetch();
      const body = await reply.json();
      await route.fulfill({
        response: reply,
        json: { ...body, total_requests: 0, success_count: 0, failure_count: 0, models: [], series: [] },
      });
    });
    await page.route("**/v0/management/setup", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const reply = await route.fetch();
      const body = await reply.json();
      body.found.clients = [
        { id: "claude-code", name: "Claude Code", installed: true, config: "", state: "not-configured" },
      ];
      await route.fulfill({ response: reply, json: body });
    });
    await openPanel(page, core, "/panel/#/status");
    await signIn(page, core);
    await expect(page.getByText("No requests in the last 7 days.", { exact: true })).toBeVisible();
    await expect(page.getByText("Claude Code is installed but not connected to pluxy.")).toBeVisible();
    await expect(page.getByRole("link", { name: "Connect it", exact: true })).toHaveAttribute(
      "href",
      "#/tools",
    );
    await expect(page.locator('[data-part="days"]')).toHaveCount(0);
  });

  test("Copy puts the address for tools on the clipboard", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await openPanel(page, core);
    await signIn(page, core);
    await expect(page.getByText("Address for tools", { exact: true })).toBeVisible();
    await expect(page.locator(".home-address code")).toHaveText(core.base);
    await page.getByRole("button", { name: "Copy", exact: true }).click();
    await expect(page.getByRole("button", { name: "Copied", exact: true })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(core.base);
  });

  test("in a small window it is one column: the verdict, the accounts, then the side sections", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 640, height: 480 });
    await openPanel(page, core);
    await signIn(page, core);
    await expect(verdict(page, "Everything is serving.")).toBeVisible();
    const main = page.locator('[data-part="main"]');
    expect(await main.evaluate((m) => m.scrollWidth <= m.clientWidth)).toBe(true);
    const lastRow = await page.locator('[data-part="account-row"]').last().boundingBox();
    const side = await page.getByRole("heading", { name: "Last 7 days" }).boundingBox();
    expect(lastRow && side && side.y > lastRow.y).toBe(true);
  });

  test("it reads again every minute, and only while the document is visible", async ({ page }) => {
    const reads: string[] = [];
    page.on("request", (r) => {
      if (new URL(r.url()).pathname.endsWith("/v0/management/usage")) reads.push(r.url());
    });
    await page.clock.install();
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Home");
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
});
