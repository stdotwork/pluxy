import { readFile } from "node:fs/promises";
import { expect, type Page, test } from "@playwright/test";
import { goTo, headerTabs, openPanel, signIn, useCore } from "./support";

const core = useCore();

/** Core's own answer to a request, with the trace ID it sent back. */
async function ask(model: string, key = core.clientKey) {
  const response = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }),
  });
  await response.text();
  const trace = response.headers.get("X-CPA-TRACE-ID");
  if (!trace) throw new Error("core sent no trace ID");
  return { status: response.status, trace };
}

async function addAccount(id: string, priority: number, settings?: Record<string, unknown>) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    models: ["fake-model", "server-error"],
    priority,
    settings,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: `test-${id}`, label: id },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`)).body.login
          .state,
    )
    .toBe("done");
}

const trace: Record<string, string> = {};
const rows = (page: Page) => page.locator('[data-page="usage"] [data-part="request-row"]');
const row = (page: Page, id: string) => page.locator(`[data-trace="${id}"] [data-part="request-row"]`);
/** The side pane of the request that is open. */
const pane = (page: Page) => page.locator('[data-part="detail"]');
/** The Filter button's panel, opened. */
const openFilter = async (page: Page) => {
  if ((await page.locator(".filter-panel").count()) === 0)
    await page.getByRole("button", { name: "Filter", exact: true }).click();
};

test.beforeAll(async () => {
  await addAccount("usage-account", 1);
  trace.ok = (await ask("fake-model")).trace;
  const failed = await ask("server-error");
  expect(failed.status).toBe(500);
  trace.failed = failed.trace;
  // A second account that comes first, and a named key that asks through it.
  await addAccount("usage-second", 5);
  const made = await core.mgmt<{ value: string }>("POST", "/client-keys", { label: "Claude Code" });
  expect(made.status).toBe(201);
  trace.named = (await ask("fake-model", made.body.value)).trace;
});

test("Requests opens newest first; Failed, an account and a key filter it, and the address carries them", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity: Requests", exact: true }),
  ).toBeVisible();
  await expect(rows(page)).toHaveCount(3);
  // Newest first, with its account and its key's name.
  await expect(rows(page).first()).toContainText("usage-second");
  await expect(rows(page).first()).toContainText("Claude Code");
  await expect(page.getByRole("heading", { level: 2, name: "Today" })).toBeVisible();
  await expect(headerTabs(page).getByRole("link", { name: "Requests" })).toHaveAttribute(
    "aria-current",
    "page",
  );

  await page.getByRole("radio", { name: "Failed", exact: true }).check();
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first()).toContainText("server-error");
  expect(new URL(page.url()).hash).toBe("#/activity?failed=1");

  // A failed request through the second account does not exist: nothing matches.
  await openFilter(page);
  await page.getByLabel("Every account", { exact: true }).selectOption({ label: "usage-second" });
  // The filter is a chip, and takes itself off with a press.
  await expect(page.getByRole("button", { name: /^Account: usage-second/ })).toBeVisible();
  await expect(page.getByText("No requests match.")).toBeVisible();
  await page.getByRole("button", { name: "Clear filters", exact: true }).click();
  await expect(rows(page)).toHaveCount(3);
  expect(new URL(page.url()).hash).toBe("#/activity");

  await openFilter(page);
  await page.getByLabel("Every account", { exact: true }).selectOption({ label: "usage-second" });
  await expect(rows(page)).toHaveCount(1);
  await page.getByLabel("Every key", { exact: true }).selectOption({ label: "Claude Code" });
  await expect(rows(page)).toHaveCount(1);
  const hash = new URL(page.url()).hash;
  expect(hash).toMatch(/^#\/activity\?account=usage-second&key=/);

  // Opening that address again restores the filters, as chips and in the Filter panel.
  await page.reload();
  await expect(rows(page)).toHaveCount(1);
  await expect(page.getByRole("button", { name: /^Account: usage-second/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Key: Claude Code/ })).toBeVisible();
  await openFilter(page);
  await expect(page.getByLabel("Every account", { exact: true }).locator("option:checked")).toHaveText(
    "usage-second",
  );
  await expect(page.getByLabel("Every key", { exact: true }).locator("option:checked")).toHaveText(
    "Claude Code",
  );
  // A chip's press takes its filter off; Escape closes the panel.
  await page.keyboard.press("Escape");
  await expect(page.locator(".filter-panel")).toHaveCount(0);
  await page.getByRole("button", { name: /^Key: Claude Code/ }).click();
  await expect(page.getByRole("button", { name: /^Key: Claude Code/ })).toHaveCount(0);
  expect(new URL(page.url()).hash).toBe("#/activity?account=usage-second");
});

test("a row opens in the side pane to why it went where it did, and its trace links to its log", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity`);
  const failed = row(page, trace.failed ?? "");
  // A failed request has its failure as a word, in the list.
  await expect(failed).toContainText("Provider error");
  await failed.getByRole("button", { expanded: false }).click();
  await expect(pane(page)).toContainText("Tried usage-account: provider error. No account answered.");
  await expect(pane(page).getByRole("link", { name: "Open usage-account" })).toHaveAttribute(
    "href",
    "#/accounts/usage-account",
  );
  await expect(pane(page).getByRole("link", { name: trace.failed })).toHaveAttribute(
    "href",
    `#/logs/${trace.failed}`,
  );
  // The account first in the order says so, and links to the order.
  const named = row(page, trace.named ?? "");
  await named.getByRole("button", { expanded: false }).click();
  await expect(pane(page)).toContainText("usage-second comes first in its provider's account order.");
  await expect(pane(page).getByRole("link", { name: "Order & fallback" })).toHaveAttribute(
    "href",
    "#/routing",
  );
  // One request is open at a time.
  const ok = row(page, trace.ok ?? "");
  await ok.getByRole("button", { expanded: false }).click();
  await expect(failed.getByRole("button", { expanded: true })).toHaveCount(0);
  await expect(pane(page)).toHaveCount(1);
  // A request that succeeded says why its account answered: it was the only one then.
  await expect(pane(page)).toContainText("usage-account is the only account that serves fake-model.");
  // A request that succeeded has a log only while core logs every request.
  await expect(pane(page).getByRole("link", { name: trace.ok })).toHaveCount(0);
  await expect(pane(page).getByText(trace.ok ?? "", { exact: true })).toBeVisible();
  // Escape closes the pane and puts focus back on the row.
  await page.keyboard.press("Escape");
  await expect(pane(page)).toHaveCount(0);
  await expect(ok.getByRole("button", { expanded: false })).toBeFocused();
  try {
    await core.mgmt("PATCH", "/settings", { observability: { logs: { "request-log": true } } });
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await row(page, trace.ok ?? "")
      .getByRole("button", { expanded: false })
      .click();
    await expect(pane(page).getByRole("link", { name: trace.ok })).toHaveAttribute(
      "href",
      `#/logs/${trace.ok}`,
    );
  } finally {
    await core.mgmt("PATCH", "/settings", { observability: { logs: { "request-log": false } } });
  }
});

test("the end of the list says how far back pluxy keeps requests", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity`);
  await expect(rows(page)).toHaveCount(3);
  await expect(page.getByText(/^That is every request pluxy keeps, back to \w+ \d\d:\d\d\./)).toBeVisible();
  await expect(page.getByRole("button", { name: "Show older" })).toHaveCount(0);
});

test("Requests opens one request from an address, and says when it is no longer kept", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity?trace=${trace.ok}`);
  const line = row(page, trace.ok ?? "").getByRole("button", { expanded: true });
  await expect(line).toBeVisible();
  await expect(line).toBeFocused();
  await expect(page.getByText("That request is no longer kept.")).toHaveCount(0);
  await page.goto(`${core.base}/panel/#/activity?trace=no-such-request`);
  await expect(page.getByText("That request is no longer kept.")).toBeVisible();
  await expect(rows(page)).toHaveCount(3);
  // The old address still works.
  await page.goto(`${core.base}/panel/#/usage`);
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity: Requests", exact: true }),
  ).toBeVisible();
  await expect(rows(page)).toHaveCount(3);
});

test("a request that fell back says how many accounts failed, and its row what each attempt came to", async ({
  page,
}) => {
  // Two new accounts for one model, the first already out of its usage window.
  await addAccount("tries-first", 90, { exhausted_until: Math.floor(Date.now() / 1000) + 3600 });
  await addAccount("tries-second", 80);
  const answered = await ask("fake-model");
  expect(answered.status).toBe(200);
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity`);
  const request = row(page, answered.trace);
  await expect(request).toContainText("tries-second");
  // The list says it in two words; the chip's title is the longer note.
  await expect(request.locator(".word-chip")).toHaveText("2nd");
  await expect(request.locator(".word-chip")).toHaveAttribute("title", "after 1 other");
  await request.getByRole("button", { expanded: false }).click();
  await expect(pane(page)).toContainText(/Tried tries-first: .+\. Then tries-second answered in /);
  await expect(pane(page)).toContainText(
    "tries-second is a backup in its provider's account order: no account above it could take the request then.",
  );
  const attempts = pane(page).locator(".request-attempts li");
  await expect(attempts).toHaveCount(2);
  await expect(attempts.nth(0)).toContainText("tries-first");
  await expect(attempts.nth(1)).toContainText("tries-second");
  await expect(attempts.nth(1)).toContainText("Succeeded");
});

test("Usage adds the requests up, by range and by name; a name filters, and its requests are one link away", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Usage");
  await expect(page.locator(".usage-figures")).toContainText(/\d+ requests · \d+(\.\d)?% succeeded/);
  const bars = page.locator('[data-part="usage-chart"] svg g[role="img"]');
  await expect(bars).toHaveCount(7);
  await page.getByRole("radio", { name: "30 days", exact: true }).check();
  await expect(bars).toHaveCount(30);
  await page.getByRole("radio", { name: "24 hours", exact: true }).check();
  await expect.poll(async () => bars.count()).toBeGreaterThanOrEqual(24);
  await page.getByRole("radio", { name: "7 days", exact: true }).check();
  await expect(bars).toHaveCount(7);
  // The bar for today says its figures.
  await expect(bars.last()).toHaveAttribute(
    "aria-label",
    /^\w{3},? (\w{3} \d+|\d+ \w{3}): \d+ requests?, \d+ failed$/,
  );

  await page.getByRole("button", { name: "Show the numbers" }).click();
  const numbers = page.locator('[data-part="usage-numbers"]');
  await expect(numbers.getByRole("columnheader", { name: "Day" })).toBeVisible();
  await expect(numbers.getByRole("columnheader", { name: "Tokens" })).toBeVisible();
  await page.getByRole("button", { name: "Hide the numbers" }).click();
  await expect(numbers).toHaveCount(0);

  await page.getByRole("radio", { name: "Account", exact: true }).check();
  await page.getByRole("button", { name: "usage-second", exact: true }).click();
  const chip = page.getByRole("button", { name: /^Account: usage-second/ });
  await expect(chip).toBeVisible();
  await expect(page.locator(".usage-figures")).toContainText("1 request");
  await page.getByRole("link", { name: "See these requests" }).click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Activity: Requests", exact: true }),
  ).toBeVisible();
  await expect(rows(page)).toHaveCount(1);
  await expect(page.getByRole("button", { name: /^Account: usage-second/ })).toBeVisible();
  // The chip comes off.
  await page.goto(`${core.base}/panel/#/activity/usage?account=nobody`);
  await expect(page.getByText("No requests in the last 7 days.")).toBeVisible();
  await page.getByRole("button", { name: /^Account: nobody/ }).click();
  await expect(page.locator(".usage-figures")).toContainText("requests");
});

test("the Usage tiles are the chart's tabs: the chart shows the figure that is chosen", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity/usage`);
  const tiles = page.locator('[data-part="usage-tiles"]');
  const bars = page.locator('[data-part="usage-chart"] svg g[role="img"]');
  await expect(bars).toHaveCount(7);
  await expect(tiles.getByRole("radio", { name: /^Requests/ })).toBeChecked();
  await expect(bars.last()).toHaveAttribute("aria-label", /: \d+ requests?, \d+ failed$/);
  await tiles.getByRole("radio", { name: /^Went through/ }).check();
  await expect(page.locator('[data-part="usage-chart"]')).toHaveAttribute("data-metric", "through");
  await expect(bars.last()).toHaveAttribute(
    "aria-label",
    /: \d+(\.\d)?% went through, of \d+ requests?$|: no requests$/,
  );
  await tiles.getByRole("radio", { name: /^Tokens/ }).check();
  await expect(bars.last()).toHaveAttribute("aria-label", /: [\d,]+ tokens$/);
  // The time a request took has no figure per bar, so it is read, not chosen.
  const time = tiles.locator(".usage-tile[data-static]");
  await expect(time).toContainText("Avg time");
  await expect(time.getByRole("radio")).toHaveCount(0);
  // The arrow keys move between the tiles, as in any group of radio buttons.
  await tiles.getByRole("radio", { name: /^Tokens/ }).focus();
  await page.keyboard.press("ArrowUp");
  await expect(tiles.getByRole("radio", { name: /^Went through/ })).toBeChecked();
});

test("with the record turned off, both tabs say so and link to the switch", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  try {
    await core.mgmt("PATCH", "/settings", {
      observability: { usage: { "usage-statistics-enabled": false } },
    });
    await page.goto(`${core.base}/panel/#/activity`);
    const pane = page.locator('[data-part="state-pane"]');
    await expect(pane).toContainText("pluxy is not keeping a record of requests.");
    await expect(pane.getByRole("link", { name: "Turn it on in Config" })).toHaveAttribute(
      "href",
      "#/config/logging",
    );
    const save = page.getByRole("button", { name: "Save as spreadsheet", exact: true });
    await expect(save).toBeDisabled();
    await expect(save.locator("xpath=..")).toHaveAttribute(
      "title",
      "pluxy is not keeping a record of requests.",
    );
    await page.goto(`${core.base}/panel/#/activity/usage`);
    await expect(pane).toContainText("pluxy is not keeping a record of requests.");
  } finally {
    await core.mgmt("PATCH", "/settings", { observability: { usage: { "usage-statistics-enabled": true } } });
  }
});

test("Save as spreadsheet writes the header row and the requests, newest first", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/activity`);
  await expect(rows(page).first()).toBeVisible();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Save as spreadsheet", exact: true }).click();
  const file = await download;
  expect(file.suggestedFilename()).toMatch(/^pluxy-requests-\d{4}-\d\d-\d\d\.csv$/);
  const lines = (await readFile(await file.path(), "utf8")).trim().split(/\r?\n/);
  expect(lines[0]).toBe("Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID");
  const ids = lines.slice(1).map((l) => l.split(",").at(-1));
  expect(ids).toContain(trace.ok);
  expect(ids.indexOf(trace.named)).toBeLessThan(ids.indexOf(trace.failed));
  expect(ids.indexOf(trace.failed)).toBeLessThan(ids.indexOf(trace.ok));
});

test("a load that failed says so, and a save that could not read says that, not the same thing", async ({
  page,
}) => {
  const fail = (route: import("@playwright/test").Route) =>
    route.fulfill({ status: 500, json: { error: { code: "internal", message: "core could not read it" } } });
  await openPanel(page, core);
  await signIn(page, core);
  await page.route("**/v0/management/usage/events?*", fail);
  await page.goto(`${core.base}/panel/#/activity`);
  await expect(
    page.getByRole("heading", { name: "Could not read the requests.", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Not saved")).toHaveCount(0);
  await expect(page.getByText("Could not read the requests to save.")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Try again", exact: true })).toBeVisible();
  // Reading again works once core does.
  await page.unroute("**/v0/management/usage/events?*");
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(rows(page).first()).toBeVisible();
  // Now only the save's read fails: the list stays, and the save has its own words.
  await page.route("**/v0/management/usage/events?*", fail);
  await page.getByRole("button", { name: "Save as spreadsheet", exact: true }).click();
  await expect(page.getByText("Could not read the requests to save. Try again.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Could not read the requests.", exact: true })).toHaveCount(
    0,
  );
  await expect(rows(page).first()).toBeVisible();
});
