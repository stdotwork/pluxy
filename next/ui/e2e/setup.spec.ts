// The Setup page on a real first run: a core with nothing installed and no accounts, whose
// machine holds Claude Code (panelcore -first-run). The case walks a new install from signing
// in to Home.
import { expect, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

test("with no account, Accounts names the page that adds one", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  const empty = page.locator('main [data-part="state-pane"]');
  await expect(empty).toContainText("No accounts yet.");
  await empty.getByRole("link", { name: "Add an account", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add Account" })).toBeVisible();
});

test("a first run opens Setup, installs the suggested provider, adds an account, shows what to paste and finishes", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);

  // Setup opens by itself, in place of Home.
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
  await expect(page).toHaveURL(/#\/setup$/);
  await expect(page.locator("html")).toHaveAttribute("data-page", "setup");

  // What core found, and the provider it suggests for it, already ticked.
  const found = page.getByRole("region", { name: "Found on this machine" });
  await expect(found.getByText("Claude Code 2.4.1")).toBeVisible();
  const pick = page.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await expect(pick.getByRole("checkbox")).toBeChecked();
  await expect(pick).toContainText("Claude Code is installed on this computer");

  // Start installs it, and says so beside the pick.
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(pick.locator('[data-part="badge"]')).toHaveText("Installed");
  const packages = await core.mgmt<{ packages: { id: string; enabled: boolean }[] }>("GET", "/packages");
  expect(packages.body.packages).toContainEqual(
    expect.objectContaining({ id: "pluxy/claude", enabled: true }),
  );

  // The sign-in of the Add Account page, on this page: an API key adds an account.
  const account = page.getByRole("region", { name: "Connect an account" });
  const tile = account.locator('[data-part="method"]').filter({ hasText: "Test API key" });
  await tile.getByRole("button", { name: "Test API key" }).click();
  await tile.getByLabel("API key", { exact: true }).fill("test-good-key");
  await tile.getByRole("button", { name: "Add account" }).click();
  await expect(account).toContainText("1 account connected");
  const accounts = await core.mgmt<{ accounts: { extension: string }[] }>("GET", "/accounts");
  expect(accounts.body.accounts.map((a) => a.extension)).toEqual(["pluxy/claude"]);

  // The text to paste into the client, and a new client key, shown once and put into the text.
  const client = page.locator('[data-part="setup-client"]').filter({ hasText: "Claude Code" });
  await expect(client).toContainText("Does not point at pluxy yet");
  await client.getByRole("button", { name: "Show what to paste" }).click();
  const paste = client.locator('[data-part="setup-paste"]');
  await expect(paste).toContainText("~/.claude/settings.json");
  await expect(paste.locator("pre")).toContainText(`"ANTHROPIC_BASE_URL": "${core.base}"`);
  await expect(paste.locator("pre")).toContainText("<client-key>");
  await client.getByRole("button", { name: "Create client key" }).click();
  const value = (await client.locator('[data-part="key-value"]').textContent()) ?? "";
  expect(value.length).toBeGreaterThan(20);
  await expect(paste.locator("pre")).toContainText(`"ANTHROPIC_AUTH_TOKEN": "${value}"`);
  const reply = await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${value}` } });
  expect(reply.status).toBe(200);

  // Finish ends the first run and lands on Home; the panel no longer opens on Setup.
  // Finish is at the page's end; in a small window Home still opens at its top.
  await page.setViewportSize({ width: 700, height: 500 });
  await page.getByRole("button", { name: "Finish", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeInViewport();
  expect(await page.locator("main").evaluate((m) => m.scrollTop)).toBe(0);
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPanel(page, core);
  await expect(nav(page)).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeHidden();
  // Setup stays among the Settings topics, now as a status of the same things.
  await goTo(page, "Set up pluxy");
  await expect(page.getByRole("region", { name: "Providers" })).toContainText("Installed");
  await expect(page.getByRole("button", { name: "Finish", exact: true })).toBeHidden();
});

test("the text for a client says what goes where a model's name stands", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/setup`);
  const clients = page.getByRole("region", { name: "Point a client at pluxy", exact: true });
  // Claude Code's text names no model.
  const claude = clients.locator('[data-part="setup-client"]').filter({ hasText: "Claude Code" });
  await claude.getByRole("button", { name: "Show what to paste" }).click();
  await expect(claude.locator("pre")).toContainText("ANTHROPIC_BASE_URL");
  await expect(claude).not.toContainText("Replace <model-id>");
  // OpenCode's does, as <model-id>.
  await clients.getByText(/^Other clients/).click();
  const opencode = clients.locator('[data-part="setup-client"]').filter({ hasText: "OpenCode" });
  await opencode.getByRole("button", { name: "Show what to paste" }).click();
  await expect(opencode.locator("pre")).toContainText("<model-id>");
  await expect(opencode).toContainText("Replace <model-id> with a model your accounts serve.");
});

test("Refresh counts an account added while the page stayed in view", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/setup`);
  const account = page.getByRole("region", { name: "Connect an account" });
  await expect(account).toContainText("1 account connected");
  // A sign-in that finishes elsewhere, as a device code does: the page gets no focus change.
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: "setup-later",
    extension: "pluxy/claude",
    method: "api_key",
    input: { api_key: "test-later-key" },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(async () => (await core.mgmt<{ accounts: unknown[] }>("GET", "/accounts")).body.accounts.length)
    .toBe(2);
  await expect(account).toContainText("1 account connected");
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(account).toContainText("2 accounts connected");
});
