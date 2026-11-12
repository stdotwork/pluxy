import { createHash } from "node:crypto";
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
  proxy_url?: string;
  headers?: Record<string, string>;
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
    // Tall enough for the whole form, so the shot shows every field.
    await page.setViewportSize({ width: 1280, height: 2000 });
    await form.screenshot({ path: join(shots, "edit-form.png"), animations: "disabled" });
    await page.setViewportSize({ width: 1280, height: 800 });
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
  await expect(models.locator('[data-part="item"]').filter({ hasText: "rate-limited" })).toContainText(
    "Excluded",
  );
  await expect(models.locator('[data-part="item"]').filter({ hasText: "fake-model" })).toContainText(
    "Included",
  );

  // Discarding drops the edit.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await form.getByLabel("Priority (priority)").fill("9");
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await form.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(form).toHaveCount(0);
  expect((await listed("settings-key"))?.priority).toBe(5);
});

test("the unsaved-changes question stays on while a save runs, and a failed save keeps the edit", async ({
  page,
}) => {
  await addAccount("settings-slow", "api_key", { api_key: "account-settings-test-input" });
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v0/management/accounts/settings-slow", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    await held;
    await route.fulfill({ status: 500, json: { error: { code: "internal", message: "the save failed" } } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "settings-slow");
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Edit settings-slow", exact: true });
  await form.getByLabel("Priority (priority)").fill("7");
  await form.getByRole("button", { name: "Save", exact: true }).click();
  // Leaving while the save runs asks first, and stays put.
  await page.evaluate(() => {
    location.hash = "#/status";
  });
  await expect(form.getByText("Discard unsaved changes?")).toBeVisible();
  await expect(page).toHaveURL(/#\/accounts$/);
  release();
  await expect(form.getByText("the save failed")).toBeVisible();
  await form.getByRole("button", { name: "Keep editing", exact: true }).click();
  await expect(form.getByLabel("Priority (priority)")).toHaveValue("7");
  expect((await listed("settings-slow"))?.priority).toBeUndefined();
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
  expect(JSON.parse((reply.choices[0]?.message.content ?? "").replace(/ done$/, ""))).toEqual({
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

test("Proxy URL shows without its password, and saving another field keeps the password", async ({
  page,
}) => {
  const password = "proxy-pass-7731";
  const full = `socks5://proxy-user:${password}@proxy.example:1080`;
  // The extension says the SHA-256 of the proxy URL it got: core removes the password from any
  // text an extension sends back.
  const digest = (url: string) => createHash("sha256").update(url).digest("hex");
  await addAccount("settings-proxy", "api_key", { api_key: "account-settings-test-input" });
  // Only this account serves the model that makes the extension say which proxy it was given.
  expect(
    (await core.mgmt("PATCH", "/accounts/settings-proxy", { models: [{ name: "account-proxy" }] })).status,
  ).toBe(200);
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "settings-proxy");
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Edit settings-proxy", exact: true });
  const proxy = form.getByLabel("Proxy URL (proxy_url)");
  await expect(proxy).toHaveValue("");

  // A bad URL is core's 400, shown in the form like any other field's error; nothing is saved.
  await proxy.fill("ftp://proxy.example:21");
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form.getByRole("alert")).toContainText("proxy_url: value");
  await expect(form.getByRole("alert")).toContainText("must use http://, https://, socks5:// or socks5h://");
  await expect(form).toBeVisible();
  expect((await listed("settings-proxy"))?.proxy_url).toBeUndefined();

  await proxy.fill(full);
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form).toHaveCount(0);

  const shown = async () => {
    const r = await core.mgmt<{ accounts: Listed[] }>("GET", "/accounts");
    expect(JSON.stringify(r.body)).not.toContain(password);
    return r.body.accounts.find((a) => a.id === "settings-proxy")?.proxy_url;
  };
  expect(await shown()).toBe("socks5://redacted@proxy.example:1080");
  const asked = async () => {
    const r = await fetch(`${core.base}/v1/chat/completions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model: "account-proxy", messages: [{ role: "user", content: "Hi" }] }),
    });
    expect(r.status).toBe(200);
    const reply = (await r.json()) as { choices: { message: { content: string } }[] };
    return (reply.choices[0]?.message.content ?? "").replace(/ done$/, "");
  };
  // The extension got the whole URL, password included.
  expect(await asked()).toBe(digest(full));

  // Reopened, the field shows the redacted URL; saving another field sends no proxy_url, and the
  // password stays.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(proxy).toHaveValue("socks5://redacted@proxy.example:1080");
  const shots = process.env.ACCOUNT_SETTINGS_SHOTS;
  if (shots) {
    mkdirSync(shots, { recursive: true });
    await page.setViewportSize({ width: 1280, height: 2000 });
    await form.screenshot({ path: join(shots, "edit-form-proxy.png"), animations: "disabled" });
    await page.setViewportSize({ width: 1280, height: 800 });
  }
  await form.getByLabel("Priority (priority)").fill("3");
  const patch = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith("/accounts/settings-proxy"),
  );
  await form.getByRole("button", { name: "Save", exact: true }).click();
  expect((await patch).postDataJSON()).toEqual({ priority: 3 });
  await expect(form).toHaveCount(0);
  expect(await shown()).toBe("socks5://redacted@proxy.example:1080");
  expect(await asked()).toBe(digest(full));

  // Clearing the field sets the default: no proxy.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await proxy.fill("");
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form).toHaveCount(0);
  expect(await shown()).toBeUndefined();
  expect(await asked()).toBe("none");
});

test("Custom Headers show secrets as placeholders, and saving another header keeps the secret", async ({
  page,
}) => {
  const token = "headers-secret-e2e-5519";
  const authorization = `Bearer ${token}`;
  const digest = (v: string) => createHash("sha256").update(v).digest("hex");
  await addAccount("settings-headers", "api_key", { api_key: "account-settings-test-input" });
  // This account serves the model that makes the extension say which headers it was given, and
  // its priority puts it ahead of the other accounts of this core, which serve any model.
  expect(
    (
      await core.mgmt("PATCH", "/accounts/settings-headers", {
        models: [{ name: "account-headers" }],
        priority: 10,
      })
    ).status,
  ).toBe(200);
  await openPanel(page, core);
  await signIn(page, core);
  const detail = await openAccount(page, "settings-headers");
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Edit settings-headers", exact: true });
  const save = form.getByRole("button", { name: "Save", exact: true });
  const headers = form.getByLabel("Custom Headers (headers)");
  await expect(headers).toHaveValue("");

  // Text that is not a JSON object of text values is refused in the form, and nothing is sent.
  let sent = 0;
  page.on("request", (r) => {
    if (r.method() === "PATCH" && r.url().endsWith("/accounts/settings-headers")) sent++;
  });
  await headers.fill("{");
  await save.click();
  await expect(form.getByRole("alert")).toHaveText("Custom headers must be valid JSON.");
  await headers.fill('{"X-Title": 1}');
  await save.click();
  await expect(form.getByRole("alert")).toHaveText("Each custom header value must be a string.");
  expect(sent).toBe(0);

  // A name core refuses is its 400, shown in the form like any other field's error; nothing is saved.
  await headers.fill('{"Bad Name": "x"}');
  await save.click();
  await expect(form.getByRole("alert")).toContainText(
    'headers: value: "Bad Name" is not a valid header name',
  );
  await expect(form).toBeVisible();
  // A placeholder that stands for no value the account has is a 400 too.
  await headers.fill('{"Authorization": "pluxy-redacted-000000000000000000000000"}');
  await save.click();
  await expect(form.getByRole("alert")).toContainText("stands for none of the account's header values now");
  expect((await listed("settings-headers"))?.headers).toBeUndefined();

  await headers.fill(JSON.stringify({ Authorization: authorization, "X-Title": "pluxy one" }, null, 2));
  await save.click();
  await expect(form).toHaveCount(0);

  const shown = async () => (await listed("settings-headers"))?.headers;
  const asked = async () => {
    const r = await fetch(`${core.base}/v1/chat/completions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model: "account-headers", messages: [{ role: "user", content: "Hi" }] }),
    });
    expect(r.status).toBe(200);
    const reply = (await r.json()) as { choices: { message: { content: string } }[] };
    return (reply.choices[0]?.message.content ?? "").replace(/ done$/, "");
  };
  // Core shows a placeholder for the secret value and the other as it is.
  const first = await shown();
  const placeholder = first?.Authorization ?? "";
  expect(placeholder).toMatch(/^pluxy-redacted-[0-9a-f]{24}$/);
  expect(first).toEqual({ Authorization: placeholder, "X-Title": "pluxy one" });
  expect(JSON.stringify((await core.mgmt("GET", "/accounts")).body)).not.toContain(token);
  // The extension got the real values: a digest is all that shows it, since core removes a secret
  // value from any text an extension sends back.
  expect(await asked()).toBe(`Authorization=${digest(authorization)},X-Title=${digest("pluxy one")}`);

  // Reopened, the form shows the placeholder, never the value. Changing only the plain header
  // sends the whole map, the secret as its placeholder, and core keeps the secret.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(headers).toHaveValue(new RegExp(placeholder));
  expect(await page.content()).not.toContain(token);
  expect(JSON.parse(await headers.inputValue())).toEqual({
    Authorization: placeholder,
    "X-Title": "pluxy one",
  });
  const shots = process.env.ACCOUNT_SETTINGS_SHOTS;
  if (shots) {
    mkdirSync(shots, { recursive: true });
    await page.setViewportSize({ width: 1280, height: 2000 });
    await form.screenshot({ path: join(shots, "edit-form-headers.png"), animations: "disabled" });
    await page.setViewportSize({ width: 1280, height: 800 });
  }
  await headers.fill(JSON.stringify({ Authorization: placeholder, "X-Title": "pluxy two" }, null, 2));
  const patch = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith("/accounts/settings-headers"),
  );
  await save.click();
  expect((await patch).postDataJSON()).toEqual({
    headers: { Authorization: placeholder, "X-Title": "pluxy two" },
  });
  await expect(form).toHaveCount(0);
  expect(await shown()).toEqual({ Authorization: placeholder, "X-Title": "pluxy two" });
  expect(await asked()).toBe(`Authorization=${digest(authorization)},X-Title=${digest("pluxy two")}`);

  // Saving another field sends no headers, and the secret stays.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await form.getByLabel("Priority (priority)").fill("3");
  const other = page.waitForRequest(
    (r) => r.method() === "PATCH" && r.url().endsWith("/accounts/settings-headers"),
  );
  await save.click();
  expect((await other).postDataJSON()).toEqual({ priority: 3 });
  await expect(form).toHaveCount(0);
  expect(await asked()).toBe(`Authorization=${digest(authorization)},X-Title=${digest("pluxy two")}`);

  // A secret typed anew replaces the old one.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await headers.fill(JSON.stringify({ Authorization: "Bearer headers-secret-e2e-new" }, null, 2));
  await save.click();
  await expect(form).toHaveCount(0);
  expect(Object.keys((await shown()) ?? {})).toEqual(["Authorization"]);
  expect(await asked()).toBe(`Authorization=${digest("Bearer headers-secret-e2e-new")}`);

  // Removing every header sets the default: none.
  await detail.getByRole("button", { name: "Edit", exact: true }).click();
  await headers.fill("");
  await save.click();
  await expect(form).toHaveCount(0);
  expect(await shown()).toBeUndefined();
  expect(await asked()).toBe("none");
});
