// The Tools page against a real core (panelcore -first-run: no account, one client key, and Claude
// Code on its machine): the tools listed, the steps under a row, a key made for a tool and put in
// its text, named keys, deleting with a question, and the address.
import { expect, type Locator, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

// panelcore compiles core and the fixture extensions before announcing readiness.
test.setTimeout(300_000);
const core = useCore({ firstRun: true });
// The page shows generated keys; never retain traces or failure screenshots.
test.use({ trace: "off", screenshot: "off", video: "off" });

test.beforeEach(async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Tools");
});

const row = (page: Page, tool: string) => page.locator('[data-part="tool-row"]').filter({ hasText: tool });
const keys = (page: Page) => page.getByRole("region", { name: /^Keys/ });
const keyRow = (page: Page, name: string) => page.locator('[data-part="tool-key"]').filter({ hasText: name });
const openKeyMenu = async (item: Locator) => {
  await item.locator('button[aria-haspopup="menu"]').click();
};

/** Keys this test made are deleted, so the next test starts with the one key core was started with. */
async function forgetMadeKeys() {
  const all = await core.mgmt<{ keys: { id: string }[] }>("GET", "/client-keys");
  for (const key of all.body.keys.slice(1)) await core.mgmt("DELETE", `/client-keys/${key.id}`);
}
test.afterEach(forgetMadeKeys);

test("lists the tools pluxy found, with their state, and warns that there is no account", async ({
  page,
}) => {
  await expect(page.getByRole("heading", { level: 1, name: "Tools" })).toBeVisible();
  const claude = row(page, "Claude Code");
  await expect(claude).toContainText("2.4.1");
  await expect(claude).toContainText("Installed · not connected");
  await expect(claude).toContainText("~/.claude/settings.json");
  await expect(page.getByText("1 installed · 0 connected", { exact: false })).toBeVisible();
  await expect(
    page.getByText("pluxy has no accounts yet, so a coding tool you set up here will get errors."),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "Add an account" })).toHaveAttribute("href", "#/add-account");
  // The tools pluxy did not find wait under Other tools, and the ones only the app sets up say so.
  await page.getByText(/^Other tools \(/).click();
  await expect(row(page, "Claude Desktop")).toContainText("Not checked");
  await expect(row(page, "Claude Desktop")).toContainText("Set up from the desktop app");
  await expect(row(page, "Codex")).toContainText("Not installed");
  await expect(page.getByRole("link", { name: "Tools", exact: true })).toBeVisible();
});

test("Set up opens three steps, one row at a time, and Hide closes them", async ({ page }) => {
  const claude = row(page, "Claude Code");
  await claude.getByRole("button", { name: "Set up", exact: true }).click();
  const steps = claude.locator('[data-part="tool-steps"]');
  await expect(steps).toContainText("Make a key for Claude Code.");
  await expect(steps).toContainText("Put this in ~/.claude/settings.json:");
  await expect(steps.locator("pre")).toContainText("<client-key>");
  await expect(steps).toContainText(
    "If the file already has settings, add these lines to them instead of replacing the file.",
  );
  await expect(steps).toContainText("Start Claude Code again so it reads the new settings.");
  await page.getByText(/^Other tools \(/).click();
  await row(page, "OpenCode").getByRole("button", { name: "Set up", exact: true }).click();
  await expect(page.locator('[data-part="tool-steps"]')).toHaveCount(1);
  await expect(row(page, "OpenCode").locator("pre")).toContainText("<model-id>");
  await expect(
    row(page, "OpenCode").getByRole("link", { name: "Models your tools can ask for" }),
  ).toHaveAttribute("href", "#/system");
  await row(page, "OpenCode").getByRole("button", { name: "Hide", exact: true }).click();
  await expect(page.locator('[data-part="tool-steps"]')).toHaveCount(0);
});

test("Make key puts a real key in the text and names it for the tool; Close takes it out", async ({
  page,
}) => {
  const claude = row(page, "Claude Code");
  await claude.getByRole("button", { name: "Set up", exact: true }).click();
  await claude.getByRole("button", { name: "Make key", exact: true }).click();
  const value = keys(page).locator('[data-part="key-value"]');
  await expect(value).toBeVisible();
  const key = (await value.textContent()) ?? "";
  expect(key.startsWith("sk-") && key.length > 16).toBe(true);
  await expect(claude.locator("pre")).toContainText(`"ANTHROPIC_AUTH_TOKEN": "${key}"`);
  await expect(claude.locator("pre")).not.toContainText("<client-key>");
  await expect(claude).toContainText("Done. The key is in the text below.");
  // Core accepts it, and Keys lists it by the tool's name, with the one-time notice.
  const status = await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${key}` } });
  expect(status.status).toBe(200);
  await expect(keys(page)).toContainText("Keys (2)");
  await expect(keyRow(page, "Claude Code")).toBeVisible();
  await expect(keys(page)).toContainText("Copy this key now. pluxy shows it only this once.");
  // Never stored by the page.
  expect(
    await page.evaluate((secret) => !JSON.stringify([localStorage, sessionStorage]).includes(secret), key),
  ).toBe(true);
  // Copying the notice's key and the text.
  await keys(page).getByRole("button", { name: "Copy", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(key);
  await claude.getByRole("button", { name: "Copy", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(
    `"ANTHROPIC_AUTH_TOKEN": "${key}"`,
  );
  // Closing the notice takes the key out of the text, and step 1 offers a new one.
  await keys(page).getByRole("button", { name: "Close", exact: true }).click();
  await expect(value).toHaveCount(0);
  await expect(claude.locator("pre")).toContainText("<client-key>");
  await expect(claude.locator("pre")).not.toContainText(key);
  await expect(claude.getByRole("button", { name: "Make another key", exact: true })).toBeVisible();
  await expect(claude).toContainText("pluxy already has a key named Claude Code");
  // The key is still a key core accepts: only its value is gone from the page.
  expect(
    (await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${key}` } })).status,
  ).toBe(200);
  // And leaving the page forgets it too.
  await claude.getByRole("button", { name: "Make another key", exact: true }).click();
  await expect(keys(page).locator('[data-part="key-value"]')).toBeVisible();
  await page.goto(`${core.base}/panel/#/status`);
  await goTo(page, "Tools");
  await expect(page.locator('[data-part="key-value"]')).toHaveCount(0);
});

test("a key made by name can be renamed and deleted from its row menu; the last key cannot be deleted", async ({
  page,
}) => {
  const only = keys(page).locator('[data-part="tool-key"]');
  await expect(only).toHaveCount(1);
  await openKeyMenu(only);
  await expect(only.getByRole("menuitem", { name: "Delete", exact: true })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(only.getByRole("menu")).toHaveCount(0);
  await expect(only).toContainText("Add another key before deleting this one.");
  await keys(page).getByRole("button", { name: "New key", exact: true }).click();
  await keys(page).getByLabel("Name", { exact: true }).fill("Script on my laptop");
  await keys(page).getByRole("button", { name: "Make key", exact: true }).click();
  const made = keyRow(page, "Script on my laptop");
  await expect(made).toBeVisible();
  await expect(made).toContainText("Not used this week");
  const value = (await keys(page).locator('[data-part="key-value"]').textContent()) ?? "";
  expect(
    (await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${value}` } })).status,
  ).toBe(200);
  // New key puts nothing in any tool's text.
  await row(page, "Claude Code").getByRole("button", { name: "Set up", exact: true }).click();
  await expect(row(page, "Claude Code").locator("pre")).toContainText("<client-key>");
  // Rename from the row menu.
  await openKeyMenu(made);
  await made.getByRole("menuitem", { name: "Rename", exact: true }).click();
  await made.getByLabel("Name", { exact: true }).fill("Nightly job");
  await made.getByRole("button", { name: "Save", exact: true }).click();
  const renamed = keyRow(page, "Nightly job");
  await expect(renamed).toBeVisible();
  const listed = await core.mgmt<{ keys: { label?: string }[] }>("GET", "/client-keys");
  expect(listed.body.keys.map((k) => k.label)).toContain("Nightly job");
  // Delete from the row menu asks first, naming the key; Cancel leaves it.
  await openKeyMenu(renamed);
  await renamed.getByRole("menuitem", { name: "Delete", exact: true }).click();
  await expect(renamed).toContainText("Nightly job stops working until it gets a new key.");
  await renamed.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(renamed).toBeVisible();
  await openKeyMenu(renamed);
  await renamed.getByRole("menuitem", { name: "Delete", exact: true }).click();
  await renamed.getByRole("button", { name: "Yes, delete", exact: true }).click();
  await expect(keys(page).locator('[data-part="tool-key"]')).toHaveCount(1);
  expect(
    (await fetch(`${core.base}/v1/models`, { headers: { Authorization: `Bearer ${value}` } })).status,
  ).toBe(401);
  await openKeyMenu(keys(page).locator('[data-part="tool-key"]'));
  await expect(
    keys(page).locator('[data-part="tool-key"]').getByRole("menuitem", { name: "Delete", exact: true }),
  ).toBeDisabled();
});

test("a refused delete of the last key shows the same line", async ({ page }) => {
  // A second tab made another key, and deleted the first: this page still lists two.
  await keys(page).getByRole("button", { name: "New key", exact: true }).click();
  await keys(page).getByRole("button", { name: "Make key", exact: true }).click();
  await expect(keys(page).locator('[data-part="tool-key"]')).toHaveCount(2);
  const all = await core.mgmt<{ keys: { id: string }[] }>("GET", "/client-keys");
  const first = all.body.keys[0];
  await core.mgmt("DELETE", `/client-keys/${first?.id}`);
  const asked = page.waitForResponse(
    (r) => r.request().method() === "DELETE" && r.url().includes("/client-keys/"),
  );
  const last = keys(page).locator('[data-part="tool-key"]').nth(1);
  await openKeyMenu(last);
  await last.getByRole("menuitem", { name: "Delete", exact: true }).click();
  await last.getByRole("button", { name: "Yes, delete", exact: true }).click();
  expect((await asked).status()).toBe(409);
  await expect(last).toContainText("Add another key before deleting this one.");
});

test("the address uses ConnectionCard with Copy and says who can reach it", async ({ page }) => {
  const address = page.getByRole("region", { name: "Address for tools" });
  await expect(address.locator('[data-part="connection-card"]')).toBeVisible();
  await expect(address).toContainText(`${core.base}/v1`);
  await expect(address).toContainText("Only programs on this computer can reach it.");
  await address.getByRole("button", { name: "Copy", exact: true }).click();
  await expect(address.getByRole("button", { name: "Copied", exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${core.base}/v1`);
  await address.getByRole("radio", { name: "Claude" }).click();
  await expect(address).toContainText(core.base);
  await address.getByRole("button", { name: "Copy", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(core.base);
  await expect(address.getByRole("link", { name: "Addresses for other programs" })).toHaveAttribute(
    "href",
    "#/system",
  );
});

test("Refresh scans again", async ({ page }) => {
  const scan = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/setup/scan"),
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  expect((await scan).status()).toBe(200);
  await expect(row(page, "Claude Code")).toBeVisible();
});
