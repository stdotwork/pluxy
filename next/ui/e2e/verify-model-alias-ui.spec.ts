import { expect, type Locator, type Page, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

const core = useCore();
const id = "verify-alias";

interface ModelsReply {
  models: { id: string; names: string[] | null; excluded?: boolean }[];
}

async function addAccount() {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "verify-model-alias-ui-key" },
  });
  expect(r.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
}

async function openAccount(page: Page) {
  await page.locator('nav a[href="#/accounts"]').click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  await page.getByRole("button", { name: id, exact: true }).click();
  return page.getByRole("complementary", { name: `Details: ${id}`, exact: true });
}

async function edit(page: Page, detail: Locator) {
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: `Edit ${id}`, exact: true });
  const aliases = form.getByRole("group", { name: "Account model aliases", exact: true });
  return { form, aliases };
}

async function names(model: string): Promise<string[] | null> {
  const r = await core.mgmt<ModelsReply>("GET", `/accounts/${id}/models`);
  expect(r.status).toBe(200);
  return r.body.models.find((m) => m.id === model)?.names ?? null;
}

test("an account model alias is saved, followed through GET /models, edited, and deleted", async ({
  page,
}) => {
  await addAccount();
  await openPanel(page, core);
  await signIn(page, core);
  let detail = await openAccount(page);

  let { form, aliases } = await edit(page, detail);
  await aliases.getByRole("button", { name: "Add alias", exact: true }).click();
  await aliases.getByLabel("Model name", { exact: true }).fill("fake-model");
  await aliases.getByLabel("Alias", { exact: true }).fill("panel-alias");
  await aliases.getByLabel("Display name", { exact: true }).fill("Panel Alias");
  const added = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith(`/accounts/${id}`),
  );
  await form.getByRole("button", { name: "Save", exact: true }).click();
  expect((await added).postDataJSON()).toEqual({
    model_alias: [{ name: "fake-model", alias: "panel-alias", display_name: "Panel Alias" }],
  });
  await expect(form).toHaveCount(0);

  // The saved rule is what core uses to name the account's models, rather than only text the
  // form kept in the browser.
  expect(await names("fake-model")).toEqual(["panel-alias"]);
  await expect(detail.locator('[data-part="account-settings"]')).toContainText(
    "fake-model → panel-alias",
  );
  await detail.getByRole("button", { name: "Models", exact: true }).click();
  const listed = detail.locator('[data-part="account-models"]');
  const model = listed.locator('[data-part="item"]').filter({ hasText: "fake-model" });
  await expect(model).toContainText("panel-alias");
  await expect(model).not.toContainText("fake-model, ");

  // A reload and a reopened Edit show the saved alias; editing it changes the names clients see.
  await page.reload();
  detail = await openAccount(page);
  ({ form, aliases } = await edit(page, detail));
  await expect(aliases.getByLabel("Model name", { exact: true })).toHaveValue("fake-model");
  await expect(aliases.getByLabel("Alias", { exact: true })).toHaveValue("panel-alias");
  await aliases.getByLabel("Alias", { exact: true }).fill("panel-alias-v2");
  const changed = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith(`/accounts/${id}`),
  );
  await form.getByRole("button", { name: "Save", exact: true }).click();
  expect((await changed).postDataJSON()).toEqual({
    model_alias: [{ name: "fake-model", alias: "panel-alias-v2", display_name: "Panel Alias" }],
  });
  await expect(form).toHaveCount(0);
  expect(await names("fake-model")).toEqual(["panel-alias-v2"]);

  // Deleting the alias is kept too: a refresh does not bring it back, and the original model name
  // is served again.
  ({ form, aliases } = await edit(page, detail));
  await aliases.getByRole("button", { name: "Remove alias", exact: true }).click();
  const removed = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith(`/accounts/${id}`),
  );
  await form.getByRole("button", { name: "Save", exact: true }).click();
  expect((await removed).postDataJSON()).toEqual({ model_alias: null });
  await expect(form).toHaveCount(0);
  expect(await names("fake-model")).toEqual(["fake-model"]);
  await page.reload();
  detail = await openAccount(page);
  await expect(detail.locator('[data-part="account-settings"]')).not.toContainText("panel-alias-v2");
  await expect(detail.locator('[data-part="account-settings"]')).not.toContainText("fake-model →");
});
