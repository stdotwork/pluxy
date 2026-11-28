// The Setup page on a real first run: a core with nothing installed and no accounts, whose
// computer holds Claude Code (panelcore -first-run). The case walks a new install through the
// three steps to Home.
import { expect, test } from "@playwright/test";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

test("with no account, Accounts names the page that adds one", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts`);
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
  const empty = page.locator('main [data-part="state-pane"]');
  await expect(empty).toContainText("No accounts yet.");
  await empty.getByRole("link", { name: "Add an account", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add an account" })).toBeVisible();
});

test("a first run opens Setup and its three steps lead to Finish setup and Home", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);

  // Setup opens by itself, in place of Home, on step 1.
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
  await expect(page).toHaveURL(/#\/setup$/);
  await expect(page.locator("html")).toHaveAttribute("data-page", "setup");
  const steps = page.locator('[data-part="setup-step"]');
  await expect(steps).toHaveCount(3);
  await expect(steps.nth(0)).toHaveAttribute("data-state", "open");

  // Step 1: the kind core suggests for Claude Code is ticked, with why; Add adds it.
  const one = page.getByRole("region", { name: "Your AI accounts" });
  const kind = one.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await expect(kind.getByRole("checkbox")).toBeChecked();
  await expect(kind).toContainText("Claude Code is installed on this computer");
  await one.getByRole("button", { name: "Add Claude", exact: true }).click();
  await expect(one).toContainText("pluxy can now connect to Claude.");
  const packages = await core.mgmt<{ packages: { id: string; enabled: boolean }[] }>("GET", "/packages");
  expect(packages.body.packages).toContainEqual(
    expect.objectContaining({ id: "pluxy/claude", enabled: true }),
  );

  // Then signing in, with no ID asked for: an API key adds an account.
  const tile = one.locator('[data-part="method"]').filter({ hasText: "Test API key" });
  await tile.getByRole("button", { name: /^Start/ }).click();
  await expect(one.getByLabel("ID", { exact: true })).toHaveCount(0);
  await one.getByLabel("API key", { exact: true }).fill("test-good-key");
  await one.getByRole("button", { name: "Continue", exact: true }).first().click();
  await expect(one).toContainText("1 account connected.");
  await expect(one.getByRole("link", { name: "See them on Accounts" })).toHaveAttribute("href", "#/accounts");
  const accounts = await core.mgmt<{ accounts: { extension: string }[] }>("GET", "/accounts");
  expect(accounts.body.accounts.map((a) => a.extension)).toEqual(["pluxy/claude"]);
  await one.locator(".setup-next").getByRole("button", { name: "Continue", exact: true }).click();

  // Step 2: Connect sets Claude Code up on pluxy's computer, with a key made for it, and the step
  // is done.
  const two = page.getByRole("region", { name: "Your coding tool" });
  await expect(steps.nth(1)).toHaveAttribute("data-state", "open");
  await expect(page.getByRole("heading", { level: 2, name: "Your coding tool" })).toBeFocused();
  await expect(two).toContainText("Detected on this computer.");
  const row = two.locator('[data-part="tool-row"]').filter({ hasText: "Claude Code" });
  await row.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(row).toContainText("Connected. Start Claude Code again so it reads the new settings.");
  const keys = await core.mgmt<{ keys: { id: string; label?: string }[] }>("GET", "/client-keys");
  expect(keys.body.keys.map((k) => k.label)).toContain("Claude Code");
  const found = await core.mgmt<{ found: { clients: { id: string; state: string }[] } }>("GET", "/setup");
  expect(found.body.found.clients.find((c) => c.id === "claude-code")?.state).toBe("configured");
  await two.locator(".setup-next").getByRole("button", { name: "Continue", exact: true }).click();

  // Step 3: one test request through the account passes.
  const three = page.getByRole("region", { name: "Check it works" });
  await expect(three).toContainText("This sends one short test message through");
  await expect(three).toContainText("pluxy does not list it in Activity or Usage.");
  await three.getByRole("button", { name: "Send a test request", exact: true }).click();
  await expect(three.locator('[data-part="setup-test"]')).toContainText("It works:");
  // As step 3 says, pluxy does not list the test in its usage record.
  const events = await core.mgmt<{ events: unknown[] }>("GET", "/usage/events");
  expect(events.body.events).toHaveLength(0);

  // Finish setup ends the first run and lands on Home.
  await three.getByRole("button", { name: "Finish setup", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(page).toHaveURL(/#\/$/);
  const setup = await core.mgmt<{ first_run: boolean }>("GET", "/setup");
  expect(setup.body.first_run).toBe(false);
  await openPanel(page, core);
  await expect(nav(page)).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeHidden();
});
