import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

test.setTimeout(300_000);
const core = useCore();

for (const theme of ["default", "terminal"]) {
  test(`System shows the build, the served models and the addresses clients use (${theme})`, async ({
    page,
  }) => {
    await page.addInitScript((id) => {
      localStorage.setItem("pluxy.theme", `pluxy/${id}`);
    }, theme);
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "System");

    const status = await core.mgmt<{ version: string; active: { revision: number } }>("GET", "/status");
    const about = page.getByRole("region", { name: "About" });
    await expect(about.getByText("Core version")).toBeVisible();
    await expect(about.locator('[data-part="tile"]').filter({ hasText: "Core version" })).toContainText(
      status.body.version,
    );
    await expect(about.getByText("Build Time")).toBeVisible();
    await expect(about.locator('[data-part="tile"]').filter({ hasText: "Connection Status:" })).toContainText(
      "Connected",
    );
    await expect(about.locator('[data-part="tile"]').filter({ hasText: "Config revision" })).toContainText(
      String(status.body.active.revision),
    );
    await expect(about.locator('[data-part="tile"]').filter({ hasText: "Safe mode" })).toContainText("Off");

    const models = page.getByRole("region", { name: "Available Models" });
    const alpha = models.locator('[data-part="model-group"]').filter({ hasText: "test/alpha" });
    await expect(alpha.getByText("alpha-model", { exact: true })).toBeVisible();
    await expect(alpha).toContainText("alpha-1, alpha-2");
    await expect(alpha).toContainText("1 model");
    const local = models.locator('[data-part="model-group"]').filter({ hasText: "test/local" });
    await expect(local.getByText("fake-model", { exact: true })).toBeVisible();
    await expect(local).toContainText("local-1");

    // Refresh asks core again.
    const again = page.waitForRequest((r) => r.url().endsWith("/v0/management/models"));
    await models.getByRole("button", { name: "Refresh" }).click();
    await again;
    await expect(alpha.getByText("alpha-model", { exact: true })).toBeVisible();

    const connect = page.getByRole("region", { name: "Connect a client" });
    await expect(connect.getByText(`${core.base}/v1`, { exact: true }).first()).toBeVisible();
    await expect(connect.getByText(core.base, { exact: true }).first()).toBeVisible();
    for (const client of ["OpenAI", "Claude", "Gemini", "Codex"]) {
      await expect(connect.getByText(client, { exact: true })).toBeVisible();
    }
    if (process.env.SYSTEM_SHOTS) {
      await page.screenshot({ path: `${process.env.SYSTEM_SHOTS}/${theme}.png`, fullPage: true });
    }
    await connect.getByRole("link", { name: "Config Panel" }).click();
    await expect(page.locator('[data-part="page-title"]')).toHaveText("Config Panel");
  });
}

test("a model list core cannot give shows the error", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.route("**/v0/management/models", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "internal", message: "no model list today" } }),
    }),
  );
  await goTo(page, "System");
  await expect(page.getByRole("alert")).toHaveText("no model list today");
});
