import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import type { LoginMethodsExtension } from "../kit/api/logins";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function startBrowser(page: Page) {
  await openAddAccount(page);
  await page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .getByRole("button", { name: "Test browser", exact: true })
    .click();
  await expect(page.getByLabel("Callback URL")).toBeVisible();
}

for (const action of ["Submit Callback URL", "Cancel"]) {
  test("action error survives a following poll: " + action, async ({ page }) => {
    await startBrowser(page);
    await page.route("**/v0/management/logins/**", async (route) => {
      if (route.request().method() === "GET") return route.continue();
      await route.fulfill({
        status: 400,
        json: { error: { code: "invalid_request", message: "Action refused" } },
      });
    });
    if (action === "Submit Callback URL") await page.getByLabel("Callback URL").fill("invalid-callback");
    await page.getByRole("button", { name: action, exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText("Action refused");
    await page.waitForResponse((r) => r.request().method() === "GET" && /\/logins\/[^/]+$/.test(r.url()));
    await expect(page.getByRole("alert")).toHaveText("Action refused");
  });
}

for (const status of [404, 503]) {
  test("poll failure offers Start over: " + status, async ({ page }) => {
    await startBrowser(page);
    let reads = 0;
    await page.route("**/v0/management/logins/*", async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      reads++;
      await route.fulfill({ status, json: { error: { code: "unavailable", message: "Login unavailable" } } });
    });
    await expect(page.getByRole("button", { name: "Start over" })).toBeVisible({ timeout: 20_000 });
    expect(reads).toBe(status === 404 ? 1 : 3);
    await expect(page.getByRole("button", { name: "Start over" })).toBeFocused();
    const stopped = reads;
    await page.waitForTimeout(2500);
    expect(reads).toBe(stopped);
  });
}

test("leaving a pending login stops polling without cancelling it", async ({ page }) => {
  await startBrowser(page);
  let requests = 0;
  page.on("request", (r) => {
    if (r.url().includes("/management/logins/")) requests++;
  });
  await goTo(page, "Accounts");
  await page.waitForTimeout(4500);
  expect(requests).toBe(0);
});

test("double click starts one login and successful submit clears the redirect", async ({ page }) => {
  await openAddAccount(page);
  let starts = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/management/accounts")) starts++;
  });
  const button = page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .getByRole("button", { name: "Test browser", exact: true });
  await button.evaluate((element) => {
    (element as HTMLButtonElement).click();
    (element as HTMLButtonElement).click();
  });
  const callback = page.getByLabel("Callback URL");
  await expect(callback).toBeVisible();
  expect(starts).toBe(1);
  await page.route("**/v0/management/logins/*/input", (route) => route.fulfill({ status: 204 }));
  await callback.fill("http://localhost/callback?code=ok");
  await page.getByRole("button", { name: "Submit Callback URL" }).click();
  await expect(callback).toHaveValue("");
});

test("invalid account IDs explain disabled methods and typed IDs survive restart", async ({ page }) => {
  await openAddAccount(page);
  const id = page.getByLabel("Account ID", { exact: true });
  await id.fill("typed.");
  await expect(id).toHaveAttribute("aria-invalid", "true");
  await expect(id).toHaveAccessibleDescription(/must not end with/);
  await id.fill("typed-account");
  await page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .getByRole("button", { name: "Test browser", exact: true })
    .click();
  await expect(page.getByLabel("Callback URL")).toBeFocused();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("button", { name: "Start over" }).click();
  await expect(id).toHaveValue("typed-account");
});

test("secret file selection clears the file control and refuses files over one MiB", async ({ page }) => {
  await openAddAccount(page);
  await page
    .locator('[data-part="method"]')
    .filter({ hasText: "test/alpha" })
    .getByRole("button", { name: "Test API key", exact: true })
    .click();
  const secret = page.getByLabel("API key", { exact: true });
  await expect(secret).toBeFocused();
  await expect(secret).toHaveAttribute("autocomplete", "new-password");
  const file = page.locator('input[type="file"]');
  await file.setInputFiles({ name: "key.txt", mimeType: "text/plain", buffer: Buffer.from("file-key") });
  await expect(secret).toHaveValue("file-key");
  await expect(file).toHaveValue("");
  await file.setInputFiles({
    name: "large.txt",
    mimeType: "text/plain",
    buffer: Buffer.alloc(1024 * 1024 + 1, "x"),
  });
  await expect(page.getByRole("alert")).toHaveText("The file could not be read. Choose it again.");
  await expect(secret).toHaveValue("file-key");
  await expect(file).toHaveValue("");
});

async function captureSignInStates(page: Page) {
  const dir = process.env.ADD_ACCOUNT_SHOTS;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  for (const theme of ["default", "terminal", "paper"]) {
    await openPanel(page, core);
    await page.locator('nav a[href="#/themes"]').click();
    await page
      .getByRole("radiogroup", { name: "Theme" })
      .getByRole("radio", { name: theme, exact: false })
      .check();
    await page.locator('nav a[href="#/add-account"]').click();
    await expect(page.getByRole("heading", { level: 1, name: "Add Account" })).toBeVisible();
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
}

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
  await captureSignInStates(page);
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
  const region = tile.getByRole("textbox", { name: "Region (optional)", exact: true });
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
