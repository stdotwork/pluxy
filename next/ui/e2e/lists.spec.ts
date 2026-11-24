// The kit's lists and buttons: a long table keeps its header in view, numbers line up, a button
// shows that it is working without changing size, an empty list says how to fill it, and a trace
// ID has a copy button.
import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

const account = (n: number) => ({
  id: `many-${String(n).padStart(2, "0")}`,
  source: "login",
  refreshable: false,
  extension: "test/alpha",
  kind: "api_key",
  state: "active",
  active_requests: n,
});

/** Answers GET /accounts with these accounts, in place of core's. */
const accountsAre = (page: Page, accounts: unknown[]) =>
  page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { accounts } });
  });

const event = (n: number) => ({
  seq: n,
  id: `trace-${n}`,
  timestamp: "2026-04-15T10:05:38Z",
  endpoint: "/v1/chat/completions",
  stream: false,
  model: "fake-model",
  provider: "test/alpha",
  account: "usage-account",
  failed: false,
  status: 200,
  latency_ms: 10 + n,
  tokens: { total_tokens: 1000 * n },
});

for (const navLayout of ["sidebar", "rail", "top"]) {
  test(`a long table keeps its header in view under the ${navLayout} navigation`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 600 });
    await accountsAre(
      page,
      Array.from({ length: 25 }, (_, i) => account(i + 1)),
    );
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Accounts");
    // The layout a theme picks is an attribute on the page.
    await page.evaluate((nav) => {
      document.documentElement.dataset.nav = nav;
    }, navLayout);
    const rows = page.locator('[data-part="list"] tbody tr');
    await expect(rows).toHaveCount(25);
    const header = page.locator('[data-part="list"] thead th').first();

    // Scrolled down as far as it takes to see the last row.
    await rows.last().evaluate((row) => row.scrollIntoView({ block: "end" }));
    await expect(rows.last()).toBeInViewport();
    await expect(header).toBeInViewport({ ratio: 1 });
    // In view and not under the navigation or a row: the header is what a click there would hit.
    const onTop = await header.evaluate((th) => {
      const box = th.getBoundingClientRect();
      const at = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
      return th.contains(at);
    });
    expect(onTop).toBe(true);
    // The header sits at the top of the table's own frame, with the last row below it.
    const frame = await page.locator(".table-wrap").boundingBox();
    const head = await header.boundingBox();
    const last = await rows.last().boundingBox();
    expect(Math.abs((head?.y ?? 0) - (frame?.y ?? -100))).toBeLessThanOrEqual(1);
    expect(last?.y ?? 0).toBeGreaterThan((head?.y ?? 0) + (head?.height ?? 0) - 1);
  });
}

test("numbers line up: digits of one width, and a numeric column set against the end edge", async ({
  page,
}) => {
  await accountsAre(page, [account(7), account(123)]);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  const table = page.locator('[data-part="list"]');
  const column = await table.locator("thead th").allTextContents();
  const requests = column.indexOf("Requests now");
  expect(requests).toBeGreaterThan(0);
  const style = (el: Element) => {
    const s = getComputedStyle(el);
    return [s.textAlign, s.fontVariantNumeric];
  };
  expect(await table.locator("thead th").nth(requests).evaluate(style)).toEqual(["end", "tabular-nums"]);
  const cell = table.locator("tbody tr").first().locator("th, td").nth(requests);
  await expect(cell).toHaveText("7");
  expect(await cell.evaluate(style)).toEqual(["end", "tabular-nums"]);
  // A column of words stays at the start edge.
  expect((await table.locator("thead th").nth(2).evaluate(style))[0]).toBe("start");
  // Counters and labels outside tables have digits of one width too.
  expect((await page.locator('[data-part="page-title"]').evaluate(style))[1]).toBe("tabular-nums");
});

async function openOwnAccount(page: Page, id: string) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/local",
    method: "api_key",
    input: { api_key: `key-${id}` },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(async () => {
      const r = await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`);
      return r.body.login.state;
    })
    .toBe("done");
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  await page.getByRole("button", { name: id, exact: true }).click();
  return page.getByRole("complementary", { name: `Details: ${id}` });
}

/** Records every time a button in the page gets the busy mark. */
const watchBusyMarks = (page: Page) =>
  page.evaluate(() => {
    const w = window as unknown as { busyMarks: number };
    w.busyMarks = 0;
    new MutationObserver((changes) => {
      for (const c of changes) if ((c.target as Element).hasAttribute("data-busy")) w.busyMarks++;
    }).observe(document.body, { subtree: true, attributes: true, attributeFilter: ["data-busy"] });
  });
const busyMarks = (page: Page) => page.evaluate(() => (window as unknown as { busyMarks: number }).busyMarks);

test("a working button keeps its label and width, shows a mark after a moment and takes one press", async ({
  page,
}) => {
  const detail = await openOwnAccount(page, "busy-1");
  let changes = 0;
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v0/management/accounts/busy-1", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    changes++;
    await held;
    await route.fallback();
  });
  await watchBusyMarks(page);
  const button = detail.getByRole("button", { name: "Disable", exact: true });
  const before = await button.boundingBox();
  await button.click();
  await expect(button).toHaveAttribute("aria-busy", "true");
  // The mark comes after a moment, and the button is otherwise as it was: same label, same
  // size, not dimmed.
  await expect(button).toHaveAttribute("data-busy", "");
  await expect(button).toHaveText("Disable");
  expect(await button.evaluate((b) => getComputedStyle(b).opacity)).toBe("1");
  const working = await button.boundingBox();
  // Within a hundredth of a pixel: the same box can differ in the last digits between two reads.
  expect(working?.width).toBeCloseTo(before?.width ?? -1, 2);
  expect(working?.height).toBeCloseTo(before?.height ?? -1, 2);
  const mark = await button.evaluate((b) => {
    const s = getComputedStyle(b, "::after");
    return { content: s.content, position: s.position, animation: s.animationName };
  });
  expect(mark).toEqual({ content: '""', position: "absolute", animation: "px-spin" });
  // The other buttons of the action wait; a second press of this one sends nothing.
  await expect(detail.getByRole("button", { name: "Remove", exact: true })).toBeDisabled();
  await button.dispatchEvent("click");
  expect(changes).toBe(1);

  release();
  await expect(detail.getByRole("button", { name: "Enable", exact: true })).toBeVisible();
  await expect(detail.getByRole("button", { name: "Enable", exact: true })).not.toHaveAttribute(
    "aria-busy",
    "true",
  );
  expect(changes).toBe(1);
});

test("a button whose action answers at once never shows the mark", async ({ page }) => {
  const detail = await openOwnAccount(page, "busy-2");
  await page.route("**/v0/management/accounts/busy-2", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    // Answered here, without core: at once.
    await route.fulfill({
      json: { account: { id: "busy-2", source: "login", state: "active", refreshable: false } },
    });
  });
  await watchBusyMarks(page);
  const changed = page.waitForResponse((r) => r.request().method() === "PATCH");
  await detail.getByRole("button", { name: "Disable", exact: true }).click();
  await changed;
  await expect(detail.getByRole("button", { name: "Disable", exact: true })).not.toHaveAttribute(
    "aria-busy",
    "true",
  );
  // Longer than the mark's wait: it would have shown by now.
  await page.waitForTimeout(400);
  expect(await busyMarks(page)).toBe(0);
});

test("with reduced motion the busy mark stands still", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const detail = await openOwnAccount(page, "busy-3");
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v0/management/accounts/busy-3", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    await held;
    await route.fallback();
  });
  const button = detail.getByRole("button", { name: "Disable", exact: true });
  await button.click();
  await expect(button).toHaveAttribute("data-busy", "");
  expect(await button.evaluate((b) => getComputedStyle(b, "::after").animationDuration)).toBe("0s");
  release();
  await expect(detail.getByRole("button", { name: "Enable", exact: true })).toBeVisible();
});

test("Accounts: an empty list leads to Add Account, a filter with no match can be cleared", async ({
  page,
}) => {
  await accountsAre(page, []);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  const empty = page.getByRole("main").locator('[data-part="empty"]');
  await expect(empty).toHaveAttribute("data-variant", "none");
  await expect(empty).toContainText("No accounts yet.");
  await empty.getByRole("link", { name: "Add Account", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add Account" })).toBeVisible();

  await page.unroute("**/v0/management/accounts");
  await accountsAre(page, [account(1), account(2)]);
  await goTo(page, "Accounts");
  const rows = page.locator('[data-part="list"] [data-part="item"]');
  await expect(rows).toHaveCount(2);
  const search = page.getByLabel("Search credentials by name, account, type, provider, or error message");
  await search.fill("nothing-like-this");
  await expect(empty).toHaveAttribute("data-variant", "filter");
  await expect(empty).toContainText("No matching accounts.");
  await empty.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(rows).toHaveCount(2);
  await expect(search).toHaveValue("");
  await expect(empty).toHaveCount(0);
});

test("a list that failed to load shows the failure, not an empty state", async ({ page }) => {
  const fail = (route: import("@playwright/test").Route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 500,
          json: { error: { code: "internal", message: "core could not read it" } },
        })
      : route.fallback();
  await page.route("**/v0/management/accounts", fail);
  await page.route("**/v0/management/usage/events?*", fail);
  await page.route("**/v0/management/packages", fail);
  await openPanel(page, core);
  await signIn(page, core);
  const main = page.getByRole("main");
  for (const name of ["Accounts", "Quota Management", "Usage", "Extensions"]) {
    await nav(page).getByRole("link", { name, exact: true }).click();
    await expect(page.getByRole("heading", { level: 1, name, exact: true })).toBeVisible();
    await expect(main.getByRole("alert").filter({ hasText: "core could not read it" }).first()).toBeVisible();
    await expect(main.locator('[data-part="empty"]')).toHaveCount(0);
  }
});

test("Quota: no account leads to Add Account, a search with no match can be cleared", async ({ page }) => {
  await accountsAre(page, []);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Quota Management");
  const empty = page.getByRole("main").locator('[data-part="empty"]');
  await expect(empty).toContainText("No accounts report quota yet.");
  await expect(empty.getByRole("link", { name: "Add Account", exact: true })).toHaveAttribute(
    "href",
    "#/add-account",
  );
  await page.unroute("**/v0/management/accounts");
  await accountsAre(page, [account(1)]);
  await goTo(page, "Accounts");
  await goTo(page, "Quota Management");
  await expect(page.locator('[data-part="item"]')).toHaveCount(1);
  await page.getByLabel("Search accounts").fill("nothing-like-this");
  await expect(empty).toHaveAttribute("data-variant", "filter");
  await empty.getByRole("button", { name: "Clear search", exact: true }).click();
  await expect(page.locator('[data-part="item"]')).toHaveCount(1);
});

test("Usage: no request leads to setup, filters with no match can be cleared, a trace ID copies", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.route("**/v0/management/usage/events?*", (route) => {
    const filtered = new URL(route.request().url()).searchParams.get("model");
    const events = filtered === null ? [event(2), event(1)] : [];
    return route.fulfill({ json: { events, next: 3, more: false, lost: false } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await nav(page).getByRole("link", { name: "Usage", exact: true }).click();
  const rows = page.locator('[data-page="usage"] [data-part="item"]');
  await expect(rows).toHaveCount(2);

  // The copy button copies the ID and does not open the row.
  const row = rows.filter({ hasText: "trace-2" });
  const trace = row.locator('[data-part="trace-id"]');
  await expect(trace.getByRole("link", { name: "trace-2", exact: true })).toBeVisible();
  await trace.getByRole("button", { name: "Copy trace ID trace-2", exact: true }).click();
  await expect(trace.getByRole("button")).toHaveText("Copied");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("trace-2");
  await expect(page.getByRole("complementary")).toHaveCount(0);
  // Latency and tokens are numbers: set against the end edge.
  const ends = await row.locator("td.num").allTextContents();
  expect(ends).toEqual(["12 ms", "2,000"]);

  const empty = page.getByRole("main").locator('[data-part="empty"]');
  await page.getByLabel("Model", { exact: true }).fill("no-such-model");
  await expect(empty).toHaveAttribute("data-variant", "filter");
  await empty.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(rows).toHaveCount(2);
  await expect(page.getByLabel("Model", { exact: true })).toHaveValue("");

  await page.unroute("**/v0/management/usage/events?*");
  await page.route("**/v0/management/usage/events?*", (route) =>
    route.fulfill({ json: { events: [], next: 0, more: false, lost: false } }),
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(empty).toHaveAttribute("data-variant", "none");
  await empty.getByRole("link", { name: "Set up pluxy", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
});

test("Logs: a search with no match can be cleared", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Logs Viewer");
  const log = page.getByRole("region", { name: "Core logs", exact: true });
  const lines = log.locator('[data-part="log-line"]');
  await expect(lines.first()).toBeVisible();
  await page.getByLabel("Search logs by content or keyword").fill("nothing-like-this");
  await page.getByLabel("Log level", { exact: true }).selectOption("warn");
  const empty = log.locator('[data-part="empty"]');
  await expect(empty).toHaveAttribute("data-variant", "filter");
  await empty.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(lines.first()).toBeVisible();
  await expect(page.getByLabel("Search logs by content or keyword")).toHaveValue("");
  await expect(page.getByLabel("Log level", { exact: true })).toHaveValue("");
});

test("Extensions: with none installed, the empty list leads to what can be installed", async ({ page }) => {
  await page.route("**/v0/management/packages", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { packages: [] } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Extensions");
  const empty = page.getByRole("main").locator('[data-part="empty"]').first();
  await expect(empty).toContainText("No extensions installed.");
  await empty.getByRole("button", { name: "Not installed", exact: true }).click();
  const section = page.getByRole("region", { name: "Not installed", exact: true });
  await expect(section).toBeInViewport();
  expect(await section.evaluate((s) => s.parentElement === document.activeElement)).toBe(true);
});
