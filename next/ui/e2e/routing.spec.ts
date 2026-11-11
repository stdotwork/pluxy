import { expect, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import type { CoreSettings } from "../kit/api/settings";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();
async function seed(id: string, priority: number) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    priority,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "routing-test-key" },
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
test.beforeAll(async () => {
  for (const [id, priority] of [
    ["route-top", 10],
    ["route-middle", 0],
    ["route-bottom", -5],
  ] as const)
    await seed(id, priority);
});

test("routing saves strategy and priorities and shows live model cooling", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  await page.getByRole("combobox", { name: "Within a tier", exact: true }).selectOption("fill-first");
  await expect
    .poll(async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body.routing.strategy)
    .toBe("fill-first");
  const middle = page.locator('[data-part="item"]').filter({ hasText: "route-middle" });
  await middle.getByRole("button", { name: "Move up a tier: route-middle", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.find(
          (a) => a.id === "route-middle",
        )?.priority,
    )
    .toBe(10);
  await expect(page.locator('[data-part="tier"]').first()).toContainText("route-middle");
  await expect(page.locator('[data-part="tier"]').first()).toContainText("Preferred");
  await expect(page.getByText(/^Weight \d+$/)).toHaveCount(0);
  await page.getByRole("combobox", { name: "Within a tier", exact: true }).selectOption("weighted-round-robin");
  await expect(middle.getByText("Weight 1", { exact: true })).toBeVisible();
  await middle.getByRole("button", { name: "Set weight…: route-middle", exact: true }).click();
  const weight = middle.getByRole("spinbutton", { name: "Set weight…: route-middle", exact: true });
  await weight.fill("3");
  await weight.press("Tab");
  await expect(middle.getByText("Weight 3", { exact: true })).toBeVisible();
  const reply = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "rate-limited", messages: [{ role: "user", content: "Hi" }] }),
  });
  expect(reply.status).toBe(429);
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(middle.getByText(/^Cooling /)).toBeVisible();
  const configured = page.locator('[data-part="item"]').filter({ hasText: "Set in config" });
  await expect(configured.first()).toBeVisible();
  for (const row of await configured.all()) {
    await expect(row.getByRole("button", { name: /^Move up a tier: / })).toBeDisabled();
    await expect(row.getByRole("button", { name: /^Set weight…: / })).toBeDisabled();
  }
});

for (const theme of ["default", "terminal"]) {
  test(`routing three tiers and cooling in ${theme}`, async ({ page }) => {
    await page.addInitScript((id) => localStorage.setItem("pluxy.theme", `pluxy/${id}`), theme);
    const limited = await fetch(`${core.base}/v1/chat/completions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model: "rate-limited", messages: [{ role: "user", content: "Hi" }] }),
    });
    expect(limited.status).toBe(429);
    await openPanel(page, core);
    await signIn(page, core);
    await nav(page).locator('a[href="#/routing"]').click();
    await expect(page.getByRole("heading", { level: 1, name: /Routing$/ })).toBeVisible();
    await expect(page.locator('[data-part="tier"]')).toHaveCount(3);
    await expect(page.getByText(/^Cooling /).first()).toBeVisible();
    for (const badge of await page.locator('[data-part="tier"] [data-part="badge"]').all()) {
      expect
        .soft(
          await badge.evaluate((element) => {
            const range = document.createRange();
            range.selectNodeContents(element);
            return (
              range.getBoundingClientRect().height <=
              Number.parseFloat(getComputedStyle(element).lineHeight) + 1
            );
          }),
        )
        .toBe(true);
    }
    if (process.env.ROUTING_SHOTS)
      await page.screenshot({ path: `${process.env.ROUTING_SHOTS}/${theme}.png`, fullPage: true });
  });
}

test("failed settings and account saves show core errors and reload current values", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const interval = page.getByRole("spinbutton", { name: "Max Retry Interval (seconds)", exact: true });
  const original = (await core.mgmt<CoreSettings>("GET", "/settings")).body.routing.retry[
    "max-retry-interval"
  ];
  await interval.fill("0.75");
  await interval.press("Tab");
  await expect(page.getByRole("alert")).toContainText("cannot unmarshal number");
  await expect(interval).toHaveValue(String(original));
  const bottom = page.locator('[data-part="item"]').filter({ hasText: "route-bottom" });
  await page.route("**/v0/management/accounts/route-bottom", (route) =>
    route.fulfill({
      status: 400,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "invalid_account", message: "The account could not be saved" } }),
    }),
  );
  const reloaded = page.waitForResponse(
    (r) => r.url().endsWith("/v0/management/accounts") && r.request().method() === "GET",
  );
  await bottom.getByRole("button", { name: "Move down a tier: route-bottom", exact: true }).click();
  await reloaded;
  await expect(page.getByRole("alert")).toHaveText("The account could not be saved");
  await expect(page.locator('[data-part="tier"]').last()).toContainText("Priority -5");
});

test("row actions are named by account, and a sole member cannot leave the edge tier", async ({ page }) => {
  await seed("route-lead", 300);
  await seed("route-last", -300);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const names = await page.locator('[data-part="tier"] [data-part="button"]').evaluateAll((buttons) =>
    buttons.map((b) => b.getAttribute("aria-label") ?? b.textContent),
  );
  expect(names.length).toBeGreaterThan(6);
  expect(new Set(names).size).toBe(names.length);
  await expect(page.getByRole("button", { name: "Move up a tier: route-lead", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Move down a tier: route-lead", exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Move down a tier: route-last", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Move up a tier: route-last", exact: true })).toBeEnabled();
});

test("keyboard focus stays on the same account's button after a move", async ({ page }) => {
  await seed("route-focus", 400);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const down = page.getByRole("button", { name: "Move down a tier: route-focus", exact: true });
  await down.focus();
  await page.keyboard.press("Enter");
  await expect.poll(async () => (await account("route-focus"))?.priority).toBeLessThan(400);
  await expect(down).toBeEnabled();
  await expect(down).toBeFocused();
  const setWeight = page.getByRole("button", { name: "Set weight…: route-focus", exact: true });
  await setWeight.focus();
  await page.keyboard.press("Enter");
  const input = page.getByRole("spinbutton", { name: "Set weight…: route-focus", exact: true });
  await input.fill("5");
  await page.keyboard.press("Enter");
  await expect.poll(async () => (await account("route-focus"))?.weight).toBe(5);
  await expect(setWeight).toBeFocused();
});

test("weight and unavailable show only under weighted round-robin", async ({ page }) => {
  await seed("route-zero", 500);
  await core.mgmt("PATCH", "/accounts/route-zero", { weight: 0 });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const row = page.locator('[data-part="item"]').filter({ hasText: "route-zero" });
  const strategy = page.getByRole("combobox", { name: "Within a tier", exact: true });
  await strategy.selectOption("round-robin");
  await expect(strategy).toHaveValue("round-robin");
  await expect(row).toBeVisible();
  await expect(row.getByText("Unavailable", { exact: true })).toHaveCount(0);
  await expect(row.getByText(/^Weight /)).toHaveCount(0);
  await strategy.selectOption("weighted-round-robin");
  await expect(row.getByText("Weight 0", { exact: true })).toBeVisible();
  await expect(row.getByText("Unavailable", { exact: true })).toBeVisible();
});

test("the weight editor cancels on Escape and does not lose a click on another action", async ({ page }) => {
  await seed("route-edit", 600);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const open = () => page.getByRole("button", { name: "Set weight…: route-edit", exact: true });
  const input = () => page.getByRole("spinbutton", { name: "Set weight…: route-edit", exact: true });
  await open().click();
  await input().fill("7");
  await input().press("Escape");
  await expect(input()).toHaveCount(0);
  await page.waitForTimeout(500);
  expect((await account("route-edit"))?.weight).toBeUndefined();
  await open().click();
  await input().fill("4");
  await page.getByRole("button", { name: "Move down a tier: route-edit", exact: true }).click();
  await expect.poll(async () => (await account("route-edit"))?.weight).toBe(4);
  await expect.poll(async () => (await account("route-edit"))?.priority).toBeLessThan(600);
});

test("a rejected value in a policy field is restored on blur", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Routing");
  const credentials = page.getByRole("spinbutton", { name: "Credentials per round", exact: true });
  const saved = await credentials.inputValue();
  await credentials.fill("-3");
  await credentials.press("Tab");
  await expect(credentials).toHaveValue(saved);
  expect(
    (await core.mgmt<CoreSettings>("GET", "/settings")).body.routing.retry["max-retry-credentials"],
  ).toBe(Number(saved));
});
