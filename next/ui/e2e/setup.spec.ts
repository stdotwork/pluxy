// The Setup page on a real first run: a core with nothing installed and no accounts, whose
// machine holds Claude Code (panelcore -first-run). The case walks a new install from signing
// in to the Dashboard.
import { expect, test } from "@playwright/test";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

test("a first run opens Setup, installs the suggested provider, adds an account, shows what to paste and finishes", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);

  // Setup opens by itself, in place of the Dashboard.
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
  await expect(page).toHaveURL(/#\/setup$/);
  await expect(page.locator("html")).toHaveAttribute("data-page", "setup");

  // What core found, and the provider it suggests for it, already ticked.
  const found = page.getByRole("region", { name: "Found on this machine" });
  await expect(found.getByText("Claude Code 2.4.1")).toBeVisible();
  const pick = page.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await expect(pick.getByRole("checkbox")).toBeChecked();
  await expect(pick).toContainText("Claude Code is installed on this machine");

  // Start installs it, and says so beside the pick.
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(pick.locator('[data-part="badge"]')).toHaveText("Installed");
  const packages = await core.mgmt<{ packages: { id: string; enabled: boolean }[] }>("GET", "/packages");
  expect(packages.body.packages).toContainEqual(expect.objectContaining({ id: "pluxy/claude", enabled: true }));

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
  await client.getByRole("button", { name: "Make a client key" }).click();
  const value = await client.locator('[data-part="key-value"]').innerText();
  expect(value.length).toBeGreaterThan(20);
  await expect(paste.locator("pre")).toContainText(`"ANTHROPIC_AUTH_TOKEN": "${value}"`);
  const reply = await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${value}` } });
  expect(reply.status).toBe(200);

  // Finish ends the first run and lands on the Dashboard; the panel no longer opens on Setup.
  await page.getByRole("button", { name: "Finish", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Dashboard" })).toBeVisible();
  await openPanel(page, core);
  await expect(nav(page)).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Dashboard" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeHidden();
  // Setup stays in the navigation, now as a status of the same things.
  await nav(page).getByRole("link", { name: "Set up pluxy" }).click();
  await expect(page.getByRole("region", { name: "Providers" })).toContainText("Installed");
  await expect(page.getByRole("button", { name: "Finish", exact: true })).toBeHidden();
});
