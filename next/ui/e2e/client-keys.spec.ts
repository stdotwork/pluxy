import { mkdir } from "node:fs/promises";
import { expect, type Page, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

// panelcore compiles core and the fixture extensions before announcing readiness.
test.setTimeout(300_000);
const core = useCore();
// These responses contain generated secrets; never retain traces or failure screenshots.
test.use({ trace: "off", screenshot: "off", video: "off" });

async function screenshot(page: Page, name: string) {
  const directory = process.env.PLUXY_KEY_SHOTS;
  if (!directory) return;
  await mkdir(directory, { recursive: true });
  await page.screenshot({ path: `${directory}/${name}.png`, fullPage: true });
}

async function goTo(page: Page, id: string, title: string) {
  await page.locator(`[data-part="nav-item"][href="#/${id}"]`).click();
  await expect(page.locator('[data-part="page-title"]')).toHaveText(title);
}

for (const theme of ["default", "terminal"]) {
  test.describe(theme, () => {
    test.beforeEach(async ({ page, context }) => {
      await context.grantPermissions(["clipboard-read", "clipboard-write"]);
      await page.addInitScript((id) => {
        localStorage.setItem("pluxy.theme", `pluxy/${id}`);
      }, theme);
    });

    test(`${theme}: client keys are revealed once, work immediately, and cannot all be deleted`, async ({
      page,
    }) => {
      await openPanel(page, core);
      await signIn(page, core);
      await goTo(page, "config", "Config Panel");
      await expect(
        page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true }),
      ).toBeVisible();
      await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
      const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
      await expect(clients.locator('[data-part="item"]')).toHaveCount(1);
      await expect(clients.getByRole("button", { name: "Copy", exact: true })).toHaveCount(0);
      await clients.getByRole("button", { name: "Add", exact: true }).click();
      const value = clients.locator('[data-part="key-value"]');
      await expect(value).toBeVisible();
      const key = (await value.textContent()) ?? "";
      expect(key.startsWith("sk-") && key.length > 16).toBe(true);
      const clientStatus = async () => {
        const response = await fetch(`${core.base}/v1/models`, {
          headers: { Authorization: `Bearer ${key}` },
        });
        return response.status;
      };
      expect(await clientStatus()).toBe(200);
      await expect(clients.locator('[data-part="item"]')).toHaveCount(2);
      await screenshot(page, `${theme}-client-key`);
      await clients.getByRole("button", { name: "Copy", exact: true }).click();
      await expect(clients.getByText("Copied", { exact: true })).toBeVisible();
      expect((await page.evaluate(() => navigator.clipboard.readText())) === key).toBe(true);
      expect(
        await page.evaluate(
          (secret) => !JSON.stringify([localStorage, sessionStorage]).includes(secret),
          key,
        ),
      ).toBe(true);
      if (theme === "default") {
        await clients.getByRole("button", { name: "Close", exact: true }).click();
        await expect(value).toHaveCount(0);
      }
      await page.getByRole("tab", { name: "Common", exact: true }).click();
      await expect(page.locator('[data-part="key-value"]')).toHaveCount(0);
      await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
      await expect(value).toHaveCount(0);
      await expect(clients.getByRole("button", { name: "Copy", exact: true })).toHaveCount(0);
      const added = clients.locator('[data-part="item"]').filter({ hasText: "#2" });
      await added.getByRole("button", { name: "Delete", exact: true }).click();
      await expect(clients.locator('[data-part="item"]')).toHaveCount(2);
      await added.getByRole("button", { name: "Yes, delete", exact: true }).click();
      await expect(clients.locator('[data-part="item"]')).toHaveCount(1);
      expect(await clientStatus()).toBe(401);
      const last = clients.locator('[data-part="item"]');
      await last.getByRole("button", { name: "Delete", exact: true }).click();
      const deletion = page.waitForResponse(
        (r) => r.request().method() === "DELETE" && r.url().includes("/client-keys/"),
      );
      await last.getByRole("button", { name: "Yes, delete", exact: true }).click();
      const refused = await deletion;
      expect(refused.status()).toBe(409);
      const error = (await refused.json()).error.message;
      await expect(clients.getByRole("alert")).toHaveText(error);
      await expect(last).toHaveCount(1);
    });

    test(`${theme}: rotating the management key keeps the panel signed in and refuses the old key`, async ({
      page,
    }) => {
      await openPanel(page, core);
      if (theme === "terminal") await page.getByLabel("Remember on this device").check();
      await signIn(page, core);
      await goTo(page, "config", "Config Panel");
      await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
      const management = page.getByRole("region", { name: "Management Key", exact: true });
      await management.getByRole("button", { name: "Rotate", exact: true }).click();
      const value = management.locator('[data-part="key-value"]');
      await expect(value).toBeVisible();
      const oldKey = core.key;
      const newKey = (await value.textContent()) ?? "";
      expect(newKey.length > 16 && newKey !== oldKey).toBe(true);
      await screenshot(page, `${theme}-management-key`);
      const remember = theme === "terminal";
      expect(
        await page.evaluate(
          ({ secret, remembered }) => {
            const kept = remembered ? localStorage : sessionStorage;
            const other = remembered ? sessionStorage : localStorage;
            return kept.getItem("pluxy.key") === secret && other.getItem("pluxy.key") === null;
          },
          { secret: newKey, remembered: remember },
        ),
      ).toBe(true);
      const old = await fetch(`${core.base}/v0/management/status`, {
        headers: { Authorization: `Bearer ${oldKey}` },
      });
      expect(old.status).toBe(401);
      core.key = newKey;
      await management.getByRole("button", { name: "Close", exact: true }).click();
      await expect(value).toHaveCount(0);
      await goTo(page, "status", "Dashboard");
      await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
      await page.reload();
      await expect(page.locator('[data-part="page-title"]')).toHaveText("Dashboard");
    });
  });
}

test.describe("review regressions", () => {
  test.beforeEach(async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "config", "Config Panel");
  });

  test("search preserves notices, filters cards without false empty results, and avoids unrelated key reads", async ({
    page,
  }) => {
    const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
    await clients.getByRole("button", { name: "Add", exact: true }).click();
    const notice = clients.locator('[data-part="key-notice"]');
    const key = await notice.locator('[data-part="key-value"]').textContent();
    await expect(notice).toBeFocused();
    expect(await notice.getAttribute("aria-live")).not.toBe("assertive");
    const search = page.getByRole("searchbox");
    for (const query of ["key", "api-keys", "management"]) {
      await search.fill(query);
      await expect(page.getByText("No results", { exact: true })).toHaveCount(0);
    }
    await search.fill("key");
    expect((await notice.locator('[data-part="key-value"]').textContent()) === key).toBe(true);
    await notice.getByRole("button", { name: "Copy", exact: true }).click();
    expect((await page.evaluate(() => navigator.clipboard.readText())) === key).toBe(true);
    await search.fill("");
    const removed = clients.locator('[data-part="item"]').last();
    await removed.getByRole("button", { name: "Delete", exact: true }).click();
    await removed.getByRole("button", { name: "Yes, delete", exact: true }).click();
    await expect(clients.getByRole("button", { name: "Add", exact: true })).toBeFocused();
    expect((await notice.locator('[data-part="key-value"]').textContent()) === key).toBe(true);
    await notice.getByRole("button", { name: "Close", exact: true }).click();
    await page.getByRole("tab", { name: "Network", exact: true }).click();
    let reads = 0;
    page.on("request", (request) => {
      if (request.method() === "GET" && request.url().endsWith("/client-keys")) reads++;
    });
    await search.fill("retry");
    await expect(page.getByRole("spinbutton", { name: "Request Retry Count", exact: true })).toBeVisible();
    expect(reads).toBe(0);
  });

  test("a second Add preserves both client-key notices", async ({ page }) => {
    const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
    const notices = clients.locator('[data-part="key-notice"]');
    await clients.getByRole("button", { name: "Add", exact: true }).click();
    await expect(notices).toHaveCount(1);
    const first = await notices.locator('[data-part="key-value"]').textContent();
    await clients.getByRole("button", { name: "Add", exact: true }).click();
    await expect(notices).toHaveCount(2);
    expect((await notices.first().locator('[data-part="key-value"]').textContent()) === first).toBe(true);
    const keys = await core.mgmt<{ keys: { id: string }[] }>("GET", "/client-keys");
    for (const key of keys.body.keys.slice(1)) await core.mgmt("DELETE", `/client-keys/${key.id}`);
  });

  test("search alone preserves the Common notice", async ({ page }) => {
    const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
    await clients.getByRole("button", { name: "Add", exact: true }).click();
    const value = clients.locator('[data-part="key-value"]');
    await expect(value).toBeVisible();
    const secret = await value.textContent();
    await page.getByRole("searchbox").fill("key");
    await expect(value).toBeVisible();
    expect((await value.textContent()) === secret).toBe(true);
    const keys = await core.mgmt<{ keys: { id: string }[] }>("GET", "/client-keys");
    for (const key of keys.body.keys.slice(1)) await core.mgmt("DELETE", `/client-keys/${key.id}`);
  });

  test("matching key searches do not show No results", async ({ page }) => {
    for (const query of ["key", "api-keys", "management"]) {
      await page.getByRole("searchbox").fill(query);
      await expect(page.getByText("No results", { exact: true })).toHaveCount(0);
    }
  });

  test("unrelated search makes no client-key request", async ({ page }) => {
    await page.getByRole("tab", { name: "Network", exact: true }).click();
    let reads = 0;
    page.on("request", (request) => {
      if (request.method() === "GET" && request.url().endsWith("/client-keys")) reads++;
    });
    await page.getByRole("searchbox").fill("retry");
    await expect(page.getByRole("spinbutton", { name: "Request Retry Count", exact: true })).toBeVisible();
    expect(reads).toBe(0);
  });

  test("Add keeps focus while its request is pending", async ({ page }) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/client-keys", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      await gate;
      await route.continue();
    });
    const add = page.getByRole("button", { name: "Add", exact: true });
    await add.click();
    await expect(add).toBeFocused();
    await expect(add).toHaveAttribute("aria-disabled", "true");
    release();
    await expect(page.locator('[data-part="key-notice"]')).toBeFocused();
    const keys = await core.mgmt<{ keys: { id: string }[] }>("GET", "/client-keys");
    for (const key of keys.body.keys.slice(1)) await core.mgmt("DELETE", `/client-keys/${key.id}`);
  });

  test("a rotation reply arriving after leaving its tab remains available on return", async ({ page }) => {
    await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let received!: () => void;
    const responseReady = new Promise<void>((resolve) => {
      received = resolve;
    });
    await page.route("**/management-key/rotate", async (route) => {
      const response = await route.fetch();
      core.key = (await response.json()).value;
      received();
      await gate;
      await route.fulfill({ response });
    });
    await page.getByRole("button", { name: "Rotate", exact: true }).click();
    await responseReady;
    await page.getByRole("tab", { name: "Network", exact: true }).click();
    release();
    await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
    const notice = page
      .getByRole("region", { name: "Management Key", exact: true })
      .locator('[data-part="key-notice"]');
    await expect(notice).toBeVisible();
    expect((await notice.locator('[data-part="key-value"]').textContent()) === core.key).toBe(true);
    await expect(notice).toBeFocused();
  });

  test("a lost rotation reply explains config-file recovery", async ({ page }) => {
    await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
    await page.route("**/management-key/rotate", async (route) => {
      const response = await route.fetch();
      core.key = (await response.json()).value;
      await route.abort("failed");
    });
    await page.getByRole("button", { name: "Rotate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText(
      "The management key may have changed. Find the current key in core's config file",
    );
  });

  test("a failed Delete reloads stale IDs", async ({ page }) => {
    const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
    await expect(clients.locator('[data-part="item"]')).toHaveCount(1);
    let reloads = 0;
    page.on("request", (request) => {
      if (request.method() === "GET" && request.url().endsWith("/client-keys")) reloads++;
    });
    await page.route("**/client-keys/*", (route) =>
      route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "not_found", message: "no client key in the config file has this ID" },
        }),
      }),
    );
    await clients.getByRole("button", { name: "Delete", exact: true }).click();
    await clients.getByRole("button", { name: "Yes, delete", exact: true }).click();
    await expect(clients.getByRole("alert")).toBeVisible();
    await expect.poll(() => reloads).toBe(1);
  });
});
