// Add an account: choosing a provider's way to sign in, signing in (a browser address, a device code
// or an API key), cancelling back to the ways, and naming the account once it is added. No account
// ID is asked for or sent: core makes it.
import { expect, type Page, test } from "@playwright/test";
import type { LoginMethodsExtension } from "../kit/api/logins";
import { accountRow, goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

async function openAddAccount(page: Page) {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Add an account");
}

/** A provider's tile, as a person picks it before its ways show. */
const tile = (page: Page, extension: string) =>
  page.locator(`[data-part="provider-tile"][data-extension="${extension}"]`);

/** Add an account with the test provider picked, so its ways show. */
async function openAlpha(page: Page) {
  await openAddAccount(page);
  await tile(page, "test/alpha").click();
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
}

/** One of the test provider's ways to sign in, by its own label. */
const way = (page: Page, label: string) =>
  page.locator('[data-extension="test/alpha"] [data-part="method"]').filter({ hasText: label });

const start = (page: Page, label: string) => way(page, label).getByRole("button", { name: "Start" });

async function startBrowser(page: Page) {
  await openAlpha(page);
  await start(page, "Test browser").click();
  await expect(page.getByLabel("Address the browser ended on")).toBeVisible();
}

const loginRequests = (page: Page) => {
  const seen = { cancels: [] as string[], reads: 0 };
  page.on("request", (r) => {
    if (!r.url().includes("/management/logins/")) return;
    if (r.method() === "DELETE") seen.cancels.push(r.url());
    if (r.method() === "GET") seen.reads++;
  });
  return seen;
};

test("Add an account asks for no ID: the providers are tiles, then a provider's ways are rows with a sentence", async ({
  page,
}) => {
  await openAddAccount(page);
  await expect(page.getByRole("heading", { level: 1, name: "Add an account" })).toBeVisible();
  await expect(page.getByText("Which account do you want to add?")).toBeVisible();
  await expect(page.getByText("Choose how to add it.")).toHaveCount(0);
  await expect(page.getByLabel("Account ID")).toHaveCount(0);
  await expect(page.getByRole("main").locator("input:not([type=file])")).toHaveCount(0);
  // The test providers are not main kinds, so the first four core lists are the tiles and the rest sit under More.
  const listed = (
    await core.mgmt<{ extensions: { methods: unknown[] }[] }>("GET", "/login-methods")
  ).body.extensions.filter((extension) => extension.methods.length > 0).length;
  const tiles = page.locator('[data-part="provider-tile"]');
  await expect(tiles).toHaveCount(Math.min(listed, 4));
  await expect(tile(page, "test/alpha")).toContainText("Alpha");
  await expect(tile(page, "test/alpha")).toContainText("Browser sign-in, device code or API key");
  await expect(page.locator('[data-part="method"]')).toHaveCount(0);
  const more = page.getByRole("button", { name: /More providers/ });
  await expect(more).toHaveCount(listed > 4 ? 1 : 0);
  if (listed > 4) {
    await expect(more).toHaveAttribute("aria-expanded", "false");
    await more.click();
    await expect(more).toHaveAttribute("aria-expanded", "true");
    await expect(tiles).toHaveCount(listed);
  }
  await expect(page.getByRole("main")).not.toContainText("test/alpha");
  await expect(page.getByText("Have auth files from CLIProxyAPI?")).toBeVisible();
  await expect(page.getByText("Or bring them from CLIProxyAPI")).toBeVisible();

  // The ways of the picked provider only.
  await tile(page, "test/alpha").click();
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
  await expect(page.locator('[data-part="method-group"]')).toHaveCount(1);
  await expect(
    page.locator('[data-extension="test/alpha"]').getByRole("heading", { name: "Alpha" }),
  ).toBeVisible();
  await expect(way(page, "Test browser")).toContainText("Sign in with your account in your browser.");
  await expect(way(page, "Test browser")).toContainText("Recommended");
  await expect(way(page, "Test device")).toContainText("Get a code to enter on another device");
  await expect(way(page, "Test API key")).toContainText("Paste an API key from the service.");
  await expect(start(page, "Test browser")).toHaveAttribute("data-variant", "primary");
  await expect(start(page, "Test device")).not.toHaveAttribute("data-variant", "primary");
  await expect(page.getByText("Have auth files from CLIProxyAPI?")).toBeVisible();

  // Back to the providers, with the tile just left in focus.
  await page.getByRole("button", { name: "All providers" }).click();
  await expect(page.getByText("Which account do you want to add?")).toBeVisible();
  await expect(tile(page, "test/alpha")).toBeFocused();
});

test("an API-key account is added with no ID, named with Save name, and the name shows on the list", async ({
  page,
}) => {
  await openAlpha(page);
  const started = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/v0/management/accounts"),
  );
  await start(page, "Test API key").click();
  const key = page.getByLabel("API key", { exact: true });
  await expect(key).toBeFocused();
  await expect(key).toHaveAttribute("type", "password");
  await expect(key).toHaveAttribute("autocomplete", "new-password");
  const chosen = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Load from file" }).click();
  await (await chosen).setFiles({
    name: "key.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("test-good-key"),
  });
  await expect(key).toHaveValue("test-good-key");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  const body = (await started).postDataJSON();
  expect(body).toEqual({ extension: "test/alpha", method: "api_key", input: { api_key: "test-good-key" } });
  expect(body).not.toHaveProperty("id");

  // Added, under the name the provider gave it, ready to be renamed.
  await expect(page.getByRole("status").filter({ hasText: "Added Test account." })).toBeVisible();
  const name = page.getByLabel("Name", { exact: true });
  await expect(name).toHaveValue("Test account");
  await expect(page.getByRole("button", { name: "Save name" })).toBeDisabled();
  await name.fill("My work key");
  await page.getByRole("button", { name: "Save name" }).click();
  await expect(page.getByText("Saved.", { exact: true })).toBeVisible();

  // Core made the account's ID, and holds the name.
  const listed = await core.mgmt<{ accounts: { id: string; label?: string; kind?: string }[] }>(
    "GET",
    "/accounts",
  );
  const added = listed.body.accounts.find((a) => a.label === "My work key");
  expect(added).toMatchObject({ kind: "api_key" });
  expect(added?.id).toMatch(/^acc_[0-9a-f]{8}$/);
  const storage = await page.evaluate(() =>
    JSON.stringify([Object.entries(localStorage), Object.entries(sessionStorage)]),
  );
  expect(storage).not.toContain("test-good-key");

  // The list shows the name, not the ID, and Open account leads to its page.
  await page.getByRole("link", { name: "Open account" }).click();
  await expect(page).toHaveURL(new RegExp(`#/accounts/${added?.id}$`));
  await expect(page.getByRole("heading", { level: 1, name: "My work key" })).toBeVisible();
  await goTo(page, "Accounts");
  await expect(accountRow(page, added?.id ?? "")).toContainText("My work key");
});

test("a key the provider refuses is said, and Try again goes back to the ways", async ({ page }) => {
  await openAlpha(page);
  await start(page, "Test API key").click();
  await page.getByLabel("API key", { exact: true }).fill("bad");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Sign-in did not finish: the test provider refused this key",
  );
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
  await expect(start(page, "Test API key")).toBeFocused();
});

test("a browser sign-in takes the address and says why the provider refused it", async ({ page }) => {
  await startBrowser(page);
  await expect(page.getByRole("button", { name: "Open sign-in page" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Copy link" })).toBeVisible();
  await expect(page.getByText(/^Time left: \d+:\d{2}$/)).toBeVisible();
  await page
    .getByLabel("Address the browser ended on")
    .fill("http://localhost:1455/auth/callback?code=bad&state=bad");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("the test provider refused this sign-in");
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(start(page, "Test browser")).toBeVisible();

  await start(page, "Test browser").click();
  await page
    .getByLabel("Address the browser ended on")
    .fill("http://localhost:1455/auth/callback?code=ok&state=ok");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: /^Added / })).toBeVisible();
  await page.getByRole("button", { name: "Add another" }).click();
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
});

test("Cancel stops a browser sign-in and goes back to the ways to sign in", async ({ page }) => {
  await openAlpha(page);
  const started = page.waitForResponse(
    (r) => r.url().includes("/v0/management/accounts") && r.request().method() === "POST",
  );
  await start(page, "Test browser").click();
  await expect(page.getByLabel("Address the browser ended on")).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  // Back on the ways, with the one just left ready to start again: no dead end between.
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
  await expect(start(page, "Test browser")).toBeFocused();
  await expect(page.getByText("Sign-in cancelled.")).toHaveCount(0);
  const login = ((await (await started).json()) as { login: { id: string } }).login;
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${login.id}`)).body.login.state,
    )
    .toBe("cancelled");
});

for (const action of ["Continue", "Cancel"]) {
  test(`a refused ${action} says why, and the next poll does not clear it`, async ({ page }) => {
    await startBrowser(page);
    await page.route("**/v0/management/logins/**", async (route) => {
      if (route.request().method() === "GET") return route.continue();
      await route.fulfill({
        status: 400,
        json: { error: { code: "invalid_request", message: "Action refused" } },
      });
    });
    if (action === "Continue") await page.getByLabel("Address the browser ended on").fill("invalid-callback");
    await page.getByRole("button", { name: action, exact: true }).click();
    await expect(page.getByRole("alert")).toHaveText("Action refused");
    await page.waitForResponse((r) => r.request().method() === "GET" && /\/logins\/[^/]+$/.test(r.url()));
    await expect(page.getByRole("alert")).toHaveText("Action refused");
  });
}

for (const status of [404, 503]) {
  test(`a poll failure ${status} stops the polling and offers Try again`, async ({ page }) => {
    await startBrowser(page);
    let reads = 0;
    await page.route("**/v0/management/logins/*", async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      reads++;
      await route.fulfill({ status, json: { error: { code: "unavailable", message: "Login unavailable" } } });
    });
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible({ timeout: 20_000 });
    expect(reads).toBe(status === 404 ? 1 : 3);
    await expect(page.getByRole("button", { name: "Try again" })).toBeFocused();
    await expect(page.getByRole("alert")).toContainText(
      status === 404
        ? "Sign-in did not finish: Login unavailable"
        : "Could not check the sign-in three times in a row. Login unavailable",
    );
    const stopped = reads;
    await page.waitForTimeout(2500);
    expect(reads).toBe(stopped);
  });
}

test("the paste field takes its words from the provider's prompt and names no address", async ({ page }) => {
  await startBrowser(page);
  const field = page.getByLabel("Address the browser ended on");
  await expect(field).toHaveAttribute("type", "text");
  expect((await field.getAttribute("placeholder")) ?? "").not.toMatch(/localhost|1455|auth\/callback/);
  await expect(page.getByText("Callback URL")).toHaveCount(0);
  // The provider's sign-in address is not printed or linked: it is only opened or copied.
  await expect(page.getByText("login.example.test")).toHaveCount(0);
  await expect(page.locator('a[href*="login.example.test"]')).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeVisible();
});

test("leaving a pending login cancels it and stops polling", async ({ page }) => {
  await startBrowser(page);
  const seen = loginRequests(page);
  await goTo(page, "Accounts");
  await expect.poll(() => seen.cancels.length).toBe(1);
  await page.waitForTimeout(4500);
  expect(seen.reads).toBe(0);
  expect(seen.cancels.length).toBe(1);
});

test("closing the page cancels a pending login once", async ({ page }) => {
  await startBrowser(page);
  const seen = loginRequests(page);
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await expect.poll(() => seen.cancels.length).toBe(1);
  await goTo(page, "Accounts");
  await page.waitForTimeout(500);
  expect(seen.cancels.length).toBe(1);
});

test("a device sign-in shows its code, adds the account, and is not cancelled when the page is left", async ({
  page,
}) => {
  await openAlpha(page);
  await start(page, "Test device").click();
  await expect(page.locator('[data-part="device-code"]')).toHaveText("TEST-1234");
  await expect(page.locator('[data-part="device-code"]')).toHaveAttribute("aria-live", "polite");
  await expect(page.getByRole("button", { name: "Copy code" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open sign-in page" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await expect(page.getByText(/^Time left: \d+:\d{2}$/)).toBeVisible();
  await expect(page.getByText("Waiting for sign-in…", { exact: true })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Added Test account." })).toBeVisible({
    timeout: 15_000,
  });
  const cancels: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "DELETE") cancels.push(r.url());
  });
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  await goTo(page, "Accounts");
  await page.waitForTimeout(500);
  expect(cancels).toEqual([]);
});

test("a second tap on a way starts one login, and a good address is cleared once sent", async ({ page }) => {
  await openAlpha(page);
  let starts = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/management/accounts")) starts++;
  });
  await start(page, "Test browser").evaluate((element) => {
    (element as HTMLButtonElement).click();
    (element as HTMLButtonElement).click();
  });
  const callback = page.getByLabel("Address the browser ended on");
  await expect(callback).toBeVisible();
  expect(starts).toBe(1);
  await page.route("**/v0/management/logins/*/input", (route) => route.fulfill({ status: 204 }));
  await callback.fill("http://localhost/callback?code=ok");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(callback).toHaveValue("");
});

test("signing in again while a sign-in for that account runs says so in plain words", async ({ page }) => {
  const made = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: "again-running",
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "ak-again-running" },
  });
  expect(made.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${made.body.login.id}`)).body.login
          .state,
    )
    .toBe("done");
  const running = await core.mgmt<{ login: { id: string } }>("POST", "/accounts/again-running/login", {
    method: "browser",
  });
  expect(running.status).toBeLessThan(300);
  try {
    await openPanel(page, core);
    await signIn(page, core);
    await page.goto(`${core.base}/panel/#/accounts/again-running/sign-in`);
    await page.getByRole("button", { name: "Test browser", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("A sign-in for this account is already running.");
    await expect(page.getByRole("alert")).not.toContainText("conflict");
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
  } finally {
    await core.mgmt("DELETE", `/logins/${running.body.login.id}`);
  }
});

test("an optional input sits under More options, and what is typed there is sent", async ({ page }) => {
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
  await openAlpha(page);
  await start(page, "Test API key").click();
  const more = page.locator('[data-part="more-options"]');
  await expect(more.locator("summary")).toHaveText("More options");
  await expect(page.getByRole("textbox", { name: "Region", exact: true })).toBeHidden();
  await more.locator("summary").click();
  const region = page.getByRole("textbox", { name: "Region", exact: true });
  await expect(region).not.toHaveAttribute("required");
  await region.fill("test-region");
  await page.getByLabel("API key", { exact: true }).fill("test-metadata-key");
  const request = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/v0/management/accounts"),
  );
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  expect((await request).postDataJSON().input).toEqual({
    api_key: "test-metadata-key",
    region: "test-region",
  });
  await expect(page.getByRole("status").filter({ hasText: /^Added / })).toBeVisible();
});

test("a secret file clears the file control and a file over one MiB is refused", async ({ page }) => {
  await openAlpha(page);
  await start(page, "Test API key").click();
  const secret = page.getByLabel("API key", { exact: true });
  await expect(secret).toBeFocused();
  const file = page.getByLabel("Load from file", { exact: true });
  await file.setInputFiles({ name: "key.txt", mimeType: "text/plain", buffer: Buffer.from("file-key") });
  await expect(secret).toHaveValue("file-key");
  await expect(file).toHaveValue("");
  await file.setInputFiles({
    name: "large.txt",
    mimeType: "text/plain",
    buffer: Buffer.alloc(1024 * 1024 + 1, "x"),
  });
  await expect(page.getByRole("alert")).toHaveText("Could not read the file. Choose it again.");
  await expect(secret).toHaveValue("file-key");
  await expect(file).toHaveValue("");
  // A file that is not text (here a PNG header) is refused before it reaches the field.
  await file.setInputFiles({
    name: "picture.png",
    mimeType: "image/png",
    buffer: Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe]),
  });
  await expect(page.getByRole("alert")).toHaveText("That file is not text. Choose a text file.");
  await expect(secret).toHaveValue("file-key");
  await expect(file).toHaveValue("");
});

test("a pending login is polled every two seconds only while visible, and not after Cancel", async ({
  page,
}) => {
  await openAlpha(page);
  await page.clock.install();
  const reads: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET" && /\/v0\/management\/logins\/[^/]+$/.test(request.url()))
      reads.push(request.url());
  });
  await start(page, "Test browser").click();
  await expect(page.getByRole("button", { name: "Open sign-in page" })).toBeVisible();
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 100));
  await page.clock.runFor(100); // Flush Preact's after-paint effects before checking the poll interval.
  expect(reads).toHaveLength(0);
  const poll = () =>
    page.waitForResponse(
      (response) => /\/logins\/[^/]+$/.test(response.url()) && response.request().method() === "GET",
    );
  const initialPoll = poll();
  await page.evaluate(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await (await initialPoll).finished();
  await page.evaluate(() => new Promise<void>((resolve) => queueMicrotask(resolve)));
  expect(reads).toHaveLength(1);
  await page.clock.runFor(1999);
  expect(reads).toHaveLength(1);
  const firstPoll = poll();
  await page.clock.runFor(1);
  await firstPoll;
  expect(reads).toHaveLength(2);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.runFor(6000);
  expect(reads).toHaveLength(2);
  const visiblePoll = poll();
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await visiblePoll;
  expect(reads).toHaveLength(3);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByText("Choose how to add it.")).toBeVisible();
  const count = reads.length;
  await page.clock.runFor(6000);
  expect(reads).toHaveLength(count);
});

test("two providers are two tiles named for people, and each one's ways sit under its own name", async ({
  page,
}) => {
  const apiKey = { method: "api_key", kind: "api_key", label: "API key", inputs: [] };
  await page.route("**/v0/management/login-methods", (route) =>
    route.fulfill({
      json: {
        extensions: [
          {
            extension: "pluxy/claude",
            methods: [{ method: "browser", kind: "browser", label: "Claude subscription" }, apiKey],
          },
          { extension: "pluxy/codex", methods: [apiKey] },
          { extension: "pluxy/gemini", methods: [apiKey] },
        ],
      },
    }),
  );
  await openAddAccount(page);
  const tiles = page.locator('[data-part="provider-tile"]');
  await expect(tiles).toHaveCount(3);
  await expect(tiles.nth(0)).toContainText("Claude");
  await expect(tiles.nth(1)).toContainText("ChatGPT (Codex)");
  await expect(tiles.nth(2)).toContainText("Gemini");
  await expect(page.getByRole("button", { name: /More providers/ })).toHaveCount(0);
  await tiles.nth(0).click();
  const groups = page.locator('[data-part="method-group"]');
  await expect(groups).toHaveCount(1);
  await expect(groups.getByRole("heading", { name: "Claude" })).toBeVisible();
  await expect(groups.locator("strong")).toHaveText(["Claude subscription Recommended", "API key"]);
  await page.getByRole("button", { name: "All providers" }).click();
  await tiles.nth(2).click();
  await expect(groups.getByRole("heading", { name: "Gemini" })).toBeVisible();
  await expect(groups.locator("strong")).toHaveText(["API key"]);
  await expect(page.getByRole("main")).not.toContainText("pluxy/");
});

test("with no provider, Add an account says so and Open Extensions leads on", async ({ page }) => {
  await page.route("**/v0/management/login-methods", (route) => route.fulfill({ json: { extensions: [] } }));
  await openAddAccount(page);
  const pane = page.locator('main [data-part="state-pane"]');
  await expect(pane).toContainText("There is no provider to sign in with yet.");
  await expect(pane).toContainText("A provider is what pluxy talks to, such as Claude or ChatGPT.");
  await pane.getByRole("link", { name: "Open Extensions", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Extensions" })).toBeVisible();
});
