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
      await goTo(page, "status", "Status");
      await expect(page.getByRole("button", { name: "Sign in", exact: true })).toHaveCount(0);
      await page.reload();
      await expect(page.locator('[data-part="page-title"]')).toHaveText("Status");
    });
  });
}
