import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import type { LoginMethodsExtension } from "../kit/api/logins";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

test("capture sign-in states in the built-in themes", async ({ page }) => {
  const dir = process.env.ADD_ACCOUNT_SHOTS;
  test.skip(!dir, "set ADD_ACCOUNT_SHOTS to capture the sign-in states");
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  for (const theme of ["default", "terminal", "paper"]) {
    await openPanel(page, core);
    if (theme === "default") await signIn(page, core);
    await goTo(page, "Themes");
    await page
      .getByRole("radiogroup", { name: "Theme" })
      .getByRole("radio", { name: theme, exact: false })
      .check();
    await goTo(page, "Add Account");
    const shot = (state: string) =>
      page.screenshot({ path: join(dir, theme.concat("-", state, ".png")), fullPage: true });
    const tile = (method: string) =>
      page.locator('[data-part="method"]').filter({ hasText: "test/alpha" }).filter({ hasText: method });
    await expect(tile("Test browser")).toBeVisible();
    await shot("tiles");
    await tile("Test API key").getByRole("button", { name: "Test API key" }).click();
    await shot("inputs");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/v0/management/accounts", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      const response = await route.fetch();
      await gate;
      await route.fulfill({ response });
    });
    await tile("Test browser").getByRole("button", { name: "Test browser" }).click();
    await expect(page.getByText("Generating sign-in link…", { exact: true })).toBeVisible();
    await shot("generating");
    release?.();
    await expect(page.getByLabel("Callback URL")).toBeVisible();
    await page.unroute("**/v0/management/accounts");
    await shot("browser-waiting");
    await page.getByLabel("Callback URL").fill("http://localhost:1455/auth/callback?code=bad&state=bad");
    await page.getByRole("button", { name: "Submit Callback URL" }).click();
    await expect(page.getByRole("alert")).toContainText("the test provider refused this sign-in");
    await shot("failed");
    await page.getByRole("button", { name: "Start over" }).click();
    await tile("Test browser").getByRole("button", { name: "Test browser" }).click();
    await expect(page.getByLabel("Callback URL")).toBeVisible();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByRole("status")).toHaveText("Sign-in cancelled.");
    await shot("cancelled");
    await page.getByRole("button", { name: "Start over" }).click();
    await tile("Test device").getByRole("button", { name: "Test device" }).click();
    await expect(page.locator('[data-part="device-code"]')).toHaveText("TEST-1234");
    await shot("device-code");
    await expect(page.getByRole("status")).toHaveText("Authentication successful!", { timeout: 15_000 });
    await shot("success");
  }
});

async function openAddAccount(page: import("@playwright/test").Page) {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Add Account");
}

test("device sign-in shows its code and adds the account", async ({ page }) => {
  await openAddAccount(page);
  await page.getByLabel("Account ID", { exact: true }).fill("device-added");
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test device" });
  await tile.getByRole("button", { name: "Test device" }).click();
  await expect(page.locator('[data-part="device-code"]')).toHaveText("TEST-1234");
  await expect(page.getByText(/^Time left: \d+:\d{2}$/)).toBeVisible();
  await expect(page.getByText("Waiting for authentication…", { exact: true })).toBeVisible();
  await expect(page.getByRole("status")).toContainText("Authentication successful!", { timeout: 15_000 });
  await page.getByRole("button", { name: "View accounts" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  await expect(page.getByRole("button", { name: "device-added", exact: true })).toBeVisible();
});

test("browser sign-in accepts a callback and reports provider failures", async ({ page }) => {
  await openAddAccount(page);
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test browser" });
  await tile.getByRole("button", { name: "Test browser" }).click();
  await expect(page.getByRole("button", { name: "Open Link" })).toBeVisible();
  await page.getByLabel("Callback URL").fill("http://localhost:1455/auth/callback?code=ok&state=ok");
  await page.getByRole("button", { name: "Submit Callback URL" }).click();
  await expect(page.getByRole("status")).toContainText("Authentication successful!");

  await page.getByRole("button", { name: "Log in another account" }).click();
  await tile.getByRole("button", { name: "Test browser" }).click();
  await page.getByLabel("Callback URL").fill("http://localhost:1455/auth/callback?code=bad&state=bad");
  await page.getByRole("button", { name: "Submit Callback URL" }).click();
  await expect(page.getByRole("alert")).toContainText("the test provider refused this sign-in");
});

test("api key adds an account and rejects a bad key", async ({ page }) => {
  await openAddAccount(page);
  await page.getByLabel("Account ID", { exact: true }).fill("panel-api-key");
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test API key" });
  await tile.getByRole("button", { name: "Test API key" }).click();
  await expect(tile.getByLabel("API key", { exact: true })).toHaveAttribute("type", "password");
  const chosen = page.waitForEvent("filechooser");
  await tile.getByRole("button", { name: "Choose File" }).click();
  await (await chosen).setFiles({
    name: "key.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("test-good-key"),
  });
  await expect(tile.getByLabel("API key", { exact: true })).toHaveValue("test-good-key");
  await tile.getByRole("button", { name: "Add account" }).click();
  await expect(page.getByRole("status")).toContainText("Authentication successful!");
  const accounts = await core.mgmt<{ accounts: { id: string; extension: string; kind: string }[] }>(
    "GET",
    "/accounts",
  );
  expect(accounts.body.accounts).toContainEqual(
    expect.objectContaining({ id: "panel-api-key", extension: "test/alpha", kind: "api_key" }),
  );
  const storage = await page.evaluate(() =>
    JSON.stringify([Object.entries(localStorage), Object.entries(sessionStorage)]),
  );
  expect(storage).not.toContain("test-good-key");

  await page.getByRole("button", { name: "Log in another account" }).click();
  await tile.getByRole("button", { name: "Test API key" }).click();
  await tile.getByLabel("API key", { exact: true }).fill("bad");
  await tile.getByRole("button", { name: "Add account" }).click();
  await expect(page.getByRole("alert")).toContainText("the test provider refused this key");
});

test("cancel cancels a pending login", async ({ page }) => {
  await openAddAccount(page);
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test browser" });
  const response = page.waitForResponse(
    (r) => r.url().includes("/v0/management/accounts") && r.request().method() === "POST",
  );
  await tile.getByRole("button", { name: "Test browser" }).click();
  await expect(page.getByRole("button", { name: "Cancel" })).toBeVisible();
  await page.getByRole("button", { name: "Cancel" }).click();
  const started = await response;
  const body = (await started.json()) as { login: { id: string } };
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${body.login.id}`)).body.login.state,
    )
    .toBe("cancelled");
  await expect(page.getByRole("status")).toHaveText("Sign-in cancelled.");
});

test("method inputs follow metadata, including optional non-secret fields", async ({ page }) => {
  await page.route("**/v0/management/login-methods", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as { extensions: LoginMethodsExtension[] };
    const method = body.extensions
      .find((extension) => extension.extension === "test/alpha")
      ?.methods.find((candidate) => candidate.method === "api_key");
    if (!method) throw new Error("test/alpha has no API-key method");
    method.inputs = [...(method.inputs ?? []), { name: "region", label: "Region", optional: true }];
    await route.fulfill({ response, json: body });
  });
  await openAddAccount(page);
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test API key" });
  await tile.getByRole("button", { name: "Test API key" }).click();
  const region = tile.getByRole("textbox", { name: "Region", exact: true });
  await expect(region).not.toHaveAttribute("required");
  await expect(tile.getByText("Region (optional)", { exact: true })).toBeVisible();
  await region.fill("test-region");
  await tile.getByLabel("API key", { exact: true }).fill("test-metadata-key");
  const request = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().endsWith("/v0/management/accounts"),
  );
  await tile.getByRole("button", { name: "Add account" }).click();
  expect((await request).postDataJSON().input).toEqual({
    api_key: "test-metadata-key",
    region: "test-region",
  });
  await expect(page.getByRole("status")).toHaveText("Authentication successful!");
});

test("pending login polls every two seconds only while visible, and stops after cancel", async ({ page }) => {
  await openAddAccount(page);
  await page.clock.install();
  const reads: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET" && /\/v0\/management\/logins\/[^/]+$/.test(request.url()))
      reads.push(request.url());
  });
  const tile = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .filter({ hasText: "Test browser" });
  await tile.getByRole("button", { name: "Test browser" }).click();
  await expect(page.getByRole("button", { name: "Open Link" })).toBeVisible();
  await page.clock.pauseAt(new Date());
  await page.clock.runFor(100); // Flush Preact's after-paint effects before checking the poll interval.
  expect(reads).toHaveLength(0);
  const initialPoll = page.waitForResponse(
    (response) => /\/logins\/[^/]+$/.test(response.url()) && response.request().method() === "GET",
  );
  await page.evaluate(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await (await initialPoll).finished();
  await page.evaluate(() => new Promise<void>((resolve) => queueMicrotask(resolve)));
  expect(reads).toHaveLength(1);
  await page.clock.runFor(1999);
  expect(reads).toHaveLength(1);
  const firstPoll = page.waitForResponse(
    (response) => /\/logins\/[^/]+$/.test(response.url()) && response.request().method() === "GET",
  );
  await page.clock.runFor(1);
  await firstPoll;
  expect(reads).toHaveLength(2);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.runFor(6000);
  expect(reads).toHaveLength(2);
  const visiblePoll = page.waitForResponse(
    (response) => /\/logins\/[^/]+$/.test(response.url()) && response.request().method() === "GET",
  );
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await visiblePoll;
  expect(reads).toHaveLength(3);
  await page.getByRole("button", { name: "Cancel" }).click();
  const cancelledPoll = page.waitForResponse(
    (response) => /\/logins\/[^/]+$/.test(response.url()) && response.request().method() === "GET",
  );
  await page.clock.runFor(2000);
  await cancelledPoll;
  await expect(page.getByRole("status")).toHaveText("Sign-in cancelled.");
  const count = reads.length;
  await page.clock.runFor(6000);
  expect(reads).toHaveLength(count);
});
