// Order & fallback against a real core with fake providers: a provider's accounts in groups that an
// arrow, a drag or a weight changes at once, the fallbacks an editor makes and removes, and the rules.
import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import type { Chain } from "../kit/api/chains";
import type { CoreSettings } from "../kit/api/settings";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function seed(id: string, priority: number) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    priority,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "routing-test-key", label: id },
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
const account = async (id: string) =>
  (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.find((a) => a.id === id);
const priority = async (id: string) => (await account(id))?.priority ?? 0;
const chains = async () => (await core.mgmt<{ chains: Chain[] }>("GET", "/chains")).body.chains;
const settings = async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body;

const LONG = "route-an-account-whose-name-has-no-place-to-break-and-is-far-too-long-for-a-narrow-window";
test.beforeAll(async () => {
  for (const [id, priority] of [
    ["route-top", 10],
    ["route-middle", 0],
    ["route-bottom", -5],
    ["route-focus", 400],
    ["route-weight", 500],
    ["route-edit", 600],
    ["route-drag", 700],
    ["route-once", 800],
    [LONG, 900],
  ] as const)
    await seed(id, priority);
});

// The rules every test starts from, and no fallbacks left over from another test.
test.beforeEach(async () => {
  await core.mgmt("PATCH", "/settings", {
    routing: {
      strategy: "round-robin",
      "session-affinity": false,
      cooldown: { "disable-cooling": false, "quota-response-mode": "retryable" },
    },
  });
  for (const chain of await chains()) await core.mgmt("DELETE", `/chains/${encodeURIComponent(chain.name)}`);
});

/** The page is idle: nothing is being saved. */
const settled = (page: Page) => expect(page.locator(".order")).not.toHaveAttribute("aria-busy", "true");

/** Opens Order & fallback the way a person does, and waits until the accounts are drawn and idle. */
async function open(page: Page) {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Order & fallback");
  await expect(page.getByRole("region", { name: "Alpha", exact: true })).toBeVisible();
  await settled(page);
}

const row = (page: Page, id: string) => page.locator(`[data-account="${id}"]`);
const group = (page: Page, priority: number) =>
  page.locator(`[data-part="tier"][data-priority="${priority}"]`);
const arrow = (page: Page, id: string, direction: "up" | "down") =>
  page.getByRole("button", { name: `Move ${id} ${direction} a group`, exact: true });
const weightButton = (page: Page, id: string) =>
  page.getByRole("button", { name: `Change ${id}'s weight`, exact: true });
const weightInput = (page: Page, id: string) =>
  page.getByRole("spinbutton", { name: `Change ${id}'s weight`, exact: true });

/** The next read of the accounts, which follows a save: waited for, so a test never races it. */
const accountsRead = (page: Page) =>
  page.waitForResponse((r) => r.url().endsWith("/v0/management/accounts") && r.request().method() === "GET");

test("an account moves up a group and back, keeps keyboard focus, and its priority survives a reload", async ({
  page,
}) => {
  await open(page);
  const read = accountsRead(page);
  await arrow(page, "route-middle", "up").click();
  await expect.poll(async () => priority("route-middle")).toBe(10);
  await read;
  await settled(page);
  await expect(group(page, 10)).toContainText("route-middle");
  await expect(group(page, 10)).toContainText("route-top");
  await expect(arrow(page, "route-middle", "up")).toBeFocused();
  // Back down: it joins the group below, where the config account is, at priority 0.
  await arrow(page, "route-middle", "down").focus();
  await page.keyboard.press("Enter");
  await expect.poll(async () => priority("route-middle")).toBe(0);
  await settled(page);
  await expect(group(page, 0)).toContainText("route-middle");
  await expect(arrow(page, "route-middle", "down")).toBeFocused();
  await page.reload();
  await expect(group(page, 0)).toContainText("route-middle");
  await expect(group(page, 10)).not.toContainText("route-middle");
});

test("an account set in the settings file has no handle, arrows or weight, and links to its entry", async ({
  page,
}) => {
  await core.mgmt("PATCH", "/settings", { routing: { strategy: "weighted-round-robin" } });
  await open(page);
  const file = row(page, "alpha-1");
  await expect(file).toBeVisible();
  await expect(file.getByRole("button")).toHaveCount(0);
  await expect(file.locator("[draggable]")).toHaveCount(0);
  await expect(file.getByRole("link", { name: "In the settings file", exact: true })).toHaveAttribute(
    "href",
    "#/config/source?account=alpha-1",
  );
});

test("a sole account of the first group cannot go up and one of the last cannot go down", async ({
  page,
}) => {
  await open(page);
  await expect(arrow(page, LONG, "up")).toBeDisabled();
  await expect(arrow(page, LONG, "up")).toHaveAccessibleDescription("Already first");
  await expect(arrow(page, LONG, "down")).toBeEnabled();
  await expect(arrow(page, "route-bottom", "down")).toBeDisabled();
  await expect(arrow(page, "route-bottom", "down")).toHaveAccessibleDescription("Already last");
  await expect(arrow(page, "route-bottom", "up")).toBeEnabled();
});

test("weight shows only under Split by weight, saves on Enter once, and Escape cancels", async ({ page }) => {
  await open(page);
  await expect(page.getByRole("button", { name: /^Change .*'s weight$/ })).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "How a group shares requests", exact: true })
    .selectOption("weighted-round-robin");
  await expect.poll(async () => (await settings()).routing.strategy).toBe("weighted-round-robin");
  await expect(weightButton(page, "route-edit")).toHaveText("Weight 1");
  // Escape leaves the weight as it was and sends nothing.
  const patches: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH" && r.url().includes("/accounts/")) patches.push(r.url());
  });
  await weightButton(page, "route-edit").click();
  await weightInput(page, "route-edit").fill("7");
  await expect(
    page.getByText("This account takes 7 shares of its group's requests. 0 takes none; empty is 1."),
  ).toBeVisible();
  await weightInput(page, "route-edit").press("Escape");
  await expect(weightInput(page, "route-edit")).toHaveCount(0);
  await expect(weightButton(page, "route-edit")).toBeFocused();
  await page.waitForTimeout(300);
  expect(patches).toEqual([]);
  expect((await account("route-edit"))?.weight).toBeUndefined();
  // Enter saves it, once.
  await weightButton(page, "route-once").click();
  await weightInput(page, "route-once").fill("2");
  await weightInput(page, "route-once").press("Enter");
  await expect.poll(async () => (await account("route-once"))?.weight).toBe(2);
  await expect(weightButton(page, "route-once")).toHaveText("Weight 2");
  await settled(page);
  expect(patches).toHaveLength(1);
  // Leaving the field saves it too, and a click on another control is not lost.
  await weightButton(page, "route-edit").click();
  await weightInput(page, "route-edit").fill("4");
  await arrow(page, "route-edit", "down").click();
  await expect.poll(async () => (await account("route-edit"))?.weight).toBe(4);
  await expect.poll(async () => (await account("route-edit"))?.priority).toBeLessThan(600);
  // Emptied, the weight goes back to its default.
  await weightButton(page, "route-edit").click();
  await weightInput(page, "route-edit").fill("");
  await weightInput(page, "route-edit").press("Enter");
  await expect.poll(async () => (await account("route-edit"))?.weight).toBeUndefined();
});

test("keyboard focus stays on the same account's button after a move, and the weight field takes typing at once", async ({
  page,
}) => {
  await core.mgmt("PATCH", "/settings", { routing: { strategy: "weighted-round-robin" } });
  await open(page);
  const down = arrow(page, "route-focus", "down");
  await down.focus();
  const read = accountsRead(page);
  await page.keyboard.press("Enter");
  await expect.poll(async () => (await account("route-focus"))?.priority).toBeLessThan(400);
  // The row has moved to its new group and the page has read the accounts back: nothing is still on its way.
  await read;
  await settled(page);
  await expect(down).toBeEnabled();
  await expect(down).toBeFocused();
  await weightButton(page, "route-focus").focus();
  await page.keyboard.press("Enter");
  await weightInput(page, "route-focus").fill("5");
  await page.keyboard.press("Enter");
  await expect.poll(async () => (await account("route-focus"))?.weight).toBe(5);
  await expect(weightButton(page, "route-focus")).toBeFocused();
});

test("a row dragged onto a group joins it", async ({ page }) => {
  await open(page);
  await row(page, "route-drag").locator(".order-handle").dragTo(group(page, 10));
  await expect.poll(async () => (await account("route-drag"))?.priority).toBe(10);
  await settled(page);
  await expect(group(page, 10)).toContainText("route-drag");
});

test("a fallback is made with its own model first, steps reordered, saved, read back and removed after a question", async ({
  page,
}) => {
  await open(page);
  await expect(page.getByText("No fallbacks yet. When every account for a model is out")).toBeVisible();
  await page.getByRole("button", { name: "+ Add a fallback", exact: true }).click();
  await page.getByLabel("When a tool asks for").fill("fake-model");
  await expect(page.getByRole("combobox", { name: "Model, step 1", exact: true })).toHaveValue("fake-model");
  await expect(page.getByRole("combobox", { name: "Provider, step 1", exact: true })).toHaveValue("");
  // Picking a model from the list sets its provider.
  await page.getByRole("combobox", { name: "Model, step 2", exact: true }).fill("server-error");
  await expect(page.getByRole("combobox", { name: "Provider, step 2", exact: true })).toHaveValue(
    "test/alpha",
  );
  await expect(page.getByText("Only some accounts", { exact: true }).first()).toBeVisible();
  // Reorder: step 2 goes first.
  await page.getByRole("button", { name: "Move step 2 up", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Model, step 1", exact: true })).toHaveValue(
    "server-error",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator('[data-part="fallback-editor"]')).toHaveCount(0);
  const line = page.locator('[data-part="fallback"]');
  await expect(line).toContainText("fake-model");
  await expect(line).toContainText("server-error (Alpha) → fake-model");
  expect(await chains()).toEqual([
    {
      name: "fake-model",
      steps: [{ model: "server-error", provider: "test/alpha" }, { model: "fake-model" }],
    },
  ]);
  await page.reload();
  await expect(page.locator('[data-part="fallback"]')).toContainText("server-error (Alpha) → fake-model");
  // Remove asks first, and saying no keeps it.
  await page.getByRole("button", { name: "Remove the fallback for fake-model…", exact: true }).click();
  await expect(
    page.getByText(
      "Remove the fallback for fake-model? Requests for fake-model go back to its own accounts only.",
    ),
  ).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(await chains()).toHaveLength(1);
  await page.getByRole("button", { name: "Remove the fallback for fake-model…", exact: true }).click();
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByText("No fallbacks yet. When every account for a model is out")).toBeVisible();
  expect(await chains()).toEqual([]);
});

test("core's refusal of a fallback shows its message and leaves the editor open", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "+ Add a fallback", exact: true }).click();
  await page.getByLabel("When a tool asks for").fill("no-such-model");
  await page.getByRole("combobox", { name: "Model, step 2", exact: true }).fill("also-missing");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "pluxy could not save this fallback: chain no-such-model:" }),
  ).toBeVisible();
  await expect(page.locator('[data-part="fallback-editor"]')).toBeVisible();
  expect(await chains()).toEqual([]);
});

test("the editor refuses a missing step model before it asks core", async ({ page }) => {
  await open(page);
  let puts = 0;
  page.on("request", (r) => {
    if (r.method() === "PUT" && r.url().includes("/chains/")) puts++;
  });
  await page.getByRole("button", { name: "+ Add a fallback", exact: true }).click();
  await page.getByLabel("When a tool asks for").fill("fake-model");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Each step needs a model.");
  expect(puts).toBe(0);
});

test("a settings file that cannot hold fallbacks gets the config.yaml notice, from core's refusal", async ({
  page,
}) => {
  await page.route("**/v0/management/chains/*", (route) =>
    route.request().method() === "PUT"
      ? route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "conflict", message: "a config.yaml cannot hold fallback chains" },
          }),
        })
      : route.fallback(),
  );
  await open(page);
  await page.getByRole("button", { name: "+ Add a fallback", exact: true }).click();
  await page.getByLabel("When a tool asks for").fill("fake-model");
  await page.getByRole("combobox", { name: "Model, step 2", exact: true }).fill("server-error");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByText("pluxy's settings file is CLIProxyAPI's config.yaml, which cannot hold fallbacks."),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "+ Add a fallback" })).toHaveCount(0);
});

test("a settings file that is a config.yaml shows the notice at once", async ({ page }) => {
  await page.route("**/v0/management/config", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ format: "yaml", hash: "x", content: "" }),
        })
      : route.fallback(),
  );
  await open(page);
  await expect(page.getByText("Move it to pluxy's own format with pluxy import cpa")).toBeVisible();
  await expect(page.getByRole("button", { name: "+ Add a fallback" })).toHaveCount(0);
});

test("More rules is closed at first; a rule saves at once, and a refused one goes back with core's message", async ({
  page,
}) => {
  await open(page);
  const more = page.locator("details.routing-more");
  await expect(more).not.toHaveAttribute("open", "");
  await expect(page.getByRole("switch", { name: "Rest an account after it fails" })).toBeHidden();
  await page
    .getByRole("combobox", { name: "How a group shares requests", exact: true })
    .selectOption("soonest-reset");
  await expect(
    page.getByText("Spends the limit that resets soonest first, so less of it goes unused."),
  ).toBeVisible();
  await expect.poll(async () => (await settings()).routing.strategy).toBe("soonest-reset");
  await page.reload();
  await expect(page.getByRole("combobox", { name: "How a group shares requests", exact: true })).toHaveValue(
    "soonest-reset",
  );
  await more.locator("summary").click();
  // Resting is on while cooling is not disabled; turning it off disables cooling.
  const rest = page.getByRole("switch", { name: "Rest an account after it fails" });
  await expect(rest).toBeChecked();
  await rest.click();
  await expect.poll(async () => (await settings()).routing.cooldown["disable-cooling"]).toBe(true);
  await rest.click();
  await expect.poll(async () => (await settings()).routing.cooldown["disable-cooling"]).toBe(false);
  // The limit message is on when the quota response is usage-limit.
  await page.getByRole("switch", { name: "Show tools when a limit resets" }).click();
  await expect
    .poll(async () => (await settings()).routing.cooldown["quota-response-mode"])
    .toBe("usage-limit");
  // A refusal puts the switch back.
  await page.route("**/v0/management/settings", (route) =>
    route.request().method() === "PATCH"
      ? route.fulfill({
          status: 400,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "invalid", message: "core would not take that" } }),
        })
      : route.fallback(),
  );
  const affinity = page.getByRole("switch", { name: "Keep a conversation on one account" });
  await affinity.click();
  await expect(page.getByRole("alert").filter({ hasText: "core would not take that" })).toBeVisible();
  await expect(affinity).not.toBeChecked();
  expect((await settings()).routing["session-affinity"]).toBe(false);
});

test("a number rule checks itself and saves on Enter", async ({ page }) => {
  await open(page);
  await page.locator("details.routing-more summary").click();
  const perRound = page.getByRole("spinbutton", { name: "Accounts to try per round", exact: true });
  await perRound.fill("-3");
  await perRound.press("Enter");
  await expect(page.getByText("Enter 0 or more.")).toBeVisible();
  await expect(perRound).toHaveValue("0");
  await perRound.fill("2");
  await perRound.press("Enter");
  await expect.poll(async () => (await settings()).routing.retry["max-retry-credentials"]).toBe(2);
});

for (const size of [
  { width: 640, height: 480 },
  { width: 1100, height: 760 },
]) {
  test(`nothing scrolls sideways at ${size.width} by ${size.height}, with a long name and the editor open`, async ({
    page,
  }) => {
    await page.setViewportSize(size);
    await core.mgmt("PATCH", "/settings", { routing: { strategy: "weighted-round-robin" } });
    await open(page);
    await page.getByRole("button", { name: "+ Add a fallback", exact: true }).click();
    await page.getByRole("button", { name: "+ Add a step", exact: true }).click();
    await page.getByRole("combobox", { name: "Provider, step 1", exact: true }).selectOption("test/alpha");
    await expect(page.getByText("Only some accounts", { exact: true }).first()).toBeVisible();
    const overflow = await page.evaluate(() =>
      [
        document.documentElement,
        ...document.querySelectorAll("main, .routing-layout, .order, .fallback-editor"),
      ].map((el) => el.scrollWidth - el.clientWidth),
    );
    expect(
      overflow.every((n) => n <= 0),
      `sideways overflow: ${overflow}`,
    ).toBe(true);
    // The arrows stay at their size, and the long name is cut with its full text in the tooltip.
    const box = await arrow(page, LONG, "down").boundingBox();
    expect(Math.min(box?.width ?? 0, box?.height ?? 0)).toBeGreaterThanOrEqual(24);
    await expect(row(page, LONG).locator(".order-name")).toHaveAttribute("title", LONG);
  });
}
