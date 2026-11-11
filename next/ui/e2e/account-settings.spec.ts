import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();
const secret = "sk-account-settings-panel";

interface Listed {
  id: string;
  priority?: number;
  excluded_models?: string[];
  settings?: Record<string, unknown>;
}

async function addAccount(id: string, method: string, input: Record<string, string>) {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/alpha",
    method,
    input,
  });
  expect(r.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
}

async function listed(id: string): Promise<Listed | undefined> {
  const r = await core.mgmt<{ accounts: Listed[] }>("GET", "/accounts");
  expect(JSON.stringify(r.body)).not.toContain(secret);
  return r.body.accounts.find((a) => a.id === id);
}

async function openAccount(page: Page, id: string) {
  await page.locator('nav a[href="#/accounts"]').click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  await page.getByRole("button", { name: id, exact: true }).click();
  return page.getByRole("complementary", { name: `Details: ${id}`, exact: true });
}

test("Edit sets priority and an excluded model, and asks before discarding", async ({ page }) => {
  await addAccount("settings-key", "api_key", { api_key: "account-settings-test-input" });
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "settings-key");
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Edit settings-key", exact: true });
  await form.getByLabel("Priority (priority)").fill("5");
  await form.getByLabel("Excluded models (excluded_models)").fill("rate-*");

  // Cancel with unsaved changes asks first; keeping them keeps the form as it was.
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(form.getByText("Discard unsaved changes?")).toBeVisible();
  await form.getByRole("button", { name: "Keep editing", exact: true }).click();
  await expect(form.getByLabel("Priority (priority)")).toHaveValue("5");
  const shots = process.env.ACCOUNT_SETTINGS_SHOTS;
  if (shots) {
    mkdirSync(shots, { recursive: true });
    await page.screenshot({ path: join(shots, "edit-form.png"), fullPage: true, animations: "disabled" });
  }
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form).toHaveCount(0);

  const a = await listed("settings-key");
  expect(a?.priority).toBe(5);
  expect(a?.excluded_models).toEqual(["rate-*"]);
  const settings = detail.locator('[data-part="account-settings"]');
  await expect(settings).toContainText("rate-*");
  await detail.getByRole("button", { name: "Models", exact: true }).click();
  const models = detail.locator('[data-part="account-models"]');
  await expect(models.locator('[data-part="item"]').filter({ hasText: "rate-limited" })).toContainText("Excluded");
  await expect(models.locator('[data-part="item"]').filter({ hasText: "fake-model" })).toContainText("Included");

  // Discarding drops the edit.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await form.getByLabel("Priority (priority)").fill("9");
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await form.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(form).toHaveCount(0);
  expect((await listed("settings-key"))?.priority).toBe(5);
});

test("settings show a placeholder for a secret, and saving keeps the secret", async ({ page }) => {
  await addAccount("settings-secret", "settings", {
    settings: JSON.stringify({ base_url: "https://one.example/v1", api_key: secret }),
  });
  const before = await listed("settings-secret");
  const placeholder = before?.settings?.api_key;
  expect(placeholder).toMatch(/^pluxy-redacted-[0-9a-f]{24}$/);
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "settings-secret");
  await expect(detail.locator('[data-part="account-settings"]')).toContainText(String(placeholder));
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Edit settings-secret", exact: true });
  const text = form.getByLabel("Settings (settings)");
  await expect(text).toHaveValue(new RegExp(String(placeholder)));
  await text.fill(JSON.stringify({ base_url: "https://two.example/v1", api_key: placeholder }, null, 2));
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form).toHaveCount(0);
  expect(await page.content()).not.toContain(secret);

  const after = await listed("settings-secret");
  expect(after?.settings).toEqual({ base_url: "https://two.example/v1", api_key: placeholder });
  // The extension gets the new base_url with the secret the placeholder kept.
  const r = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "account-settings", messages: [{ role: "user", content: "Hi" }] }),
  });
  expect(r.status).toBe(200);
  const reply = (await r.json()) as { choices: { message: { content: string } }[] };
  expect(JSON.parse(reply.choices[0].message.content.replace(/ done$/, ""))).toEqual({
    base_url: "https://two.example/v1",
    api_key: secret,
  });
});

test("a config account's settings are read-only", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "alpha-1");
  await expect(detail.locator('[data-part="account-settings"]')).toContainText("alpha-model");
  await expect(detail.getByRole("button", { name: "Edit", exact: true })).toHaveCount(0);
  await expect(detail.getByRole("link", { name: "Edit in Config Panel" })).toBeVisible();
});
