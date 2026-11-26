// Bringing a CLIProxyAPI setup across from the Setup page, against a real first-run core whose
// home folder holds one (panelcore -first-run -cpa): a config.yaml and an auth folder of three
// made-up accounts, two of them Claude's.
import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true, cpa: true });

interface Accounts {
  accounts: { id: string; extension: string }[];
}

// The refusals come first: once a setup is brought across, its row offers no button to refuse.
test("a setup core refuses or can no longer read says why, and changes nothing", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/setup`);
  const row = page.locator('[data-part="setup-import"]');
  const file = join(dirname(core.config), "home", ".cli-proxy-api", "config.yaml");
  const before = (await core.mgmt<Accounts>("GET", "/accounts")).body.accounts;
  const text = readFileSync(file, "utf8");

  // A file core refuses: its reason, and what to do.
  writeFileSync(file, "port: [8317\n");
  await row.getByRole("button", { name: "Bring it across", exact: true }).click();
  await expect(row.getByRole("alert")).toContainText("Cannot import this setup:");
  await expect(row.getByRole("alert")).toContainText("Fix it in CLIProxyAPI and try again.");
  await expect(row.locator('[data-part="setup-report"]')).toBeHidden();

  // A file that is gone: the path core tried, and Refresh.
  rmSync(file);
  await row.getByRole("button", { name: "Bring it across", exact: true }).click();
  await expect(row.getByRole("alert")).toContainText("Could not read the CLIProxyAPI setup:");
  await expect(row.getByRole("alert")).toContainText(`there is no file at ${file}`);
  await expect(row.getByRole("alert")).toContainText("then press Refresh");
  expect((await core.mgmt<Accounts>("GET", "/accounts")).body.accounts).toEqual(before);
  writeFileSync(file, text);
});

test("a found CLIProxyAPI setup is shown as a dry run, brought across, and its provider installed", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();

  const row = page.locator('[data-part="setup-import"]');
  await expect(row).toContainText("A CLIProxyAPI setup with 3 accounts");
  await expect(row).toContainText("~/.cli-proxy-api/config.yaml");
  // Claude is ticked because Claude Code was found; unticked, only the import can tick it again.
  const pick = page.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await pick.getByRole("checkbox").uncheck();

  // The dry run: core's report in its three lists, and nothing written.
  await row.getByRole("button", { name: "Bring it across", exact: true }).click();
  const report = row.locator('[data-part="setup-report"]');
  await expect(report).toHaveAttribute("data-state", "dry");
  await expect(report).toContainText("Nothing is written yet.");
  await expect(report).toContainText("Comes across (4)");
  await expect(report).toContainText("pluxy/claude account claude-work");
  await expect(report).toContainText("Different in pluxy (2)");
  await expect(report).toContainText("listen cannot change while core runs");
  await expect(report).toContainText("Does not come across (2)");
  await expect(report).toContainText("qwen-a.json");
  await expect(report).toContainText("Claude is not installed, and 2 accounts need it.");
  // No key or token of the setup is on the page.
  await expect(page.locator("body")).not.toContainText("fake-c");
  await expect(pick.getByRole("checkbox")).not.toBeChecked();
  expect((await core.mgmt<Accounts>("GET", "/accounts")).body.accounts).toEqual([]);

  // Cancel closes the report; the import then runs from a new dry run.
  await row.getByRole("button", { name: "Cancel" }).click();
  await expect(report).toBeHidden();
  await row.getByRole("button", { name: "Bring it across", exact: true }).click();
  await row.getByRole("button", { name: "Bring it across now" }).click();
  await expect(report).toHaveAttribute("data-state", "done");
  await expect(report).toContainText("Brought across (4)");
  await expect(report).toContainText("Not brought across (2)");
  await expect(report).toContainText("It is ticked under Providers: press Start to install it.");
  await expect(page.locator("body")).not.toContainText("fake-c");
  const accounts = (await core.mgmt<Accounts>("GET", "/accounts")).body.accounts;
  expect(accounts.map((a) => `${a.extension} ${a.id}`).sort()).toEqual([
    "pluxy/claude claude-home",
    "pluxy/claude claude-work",
  ]);

  // The provider the accounts need is ticked again, and Start installs it; the accounts show.
  await expect(pick.getByRole("checkbox")).toBeChecked();
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(pick.locator('[data-part="badge"]')).toHaveText("Installed");
  await expect(page.getByRole("region", { name: "Connect an account" })).toContainText(
    "2 accounts connected",
  );
  await row.getByRole("button", { name: "Close" }).click();
  await expect(report).toBeHidden();

  // Closed, the row says the setup was brought across and offers no button, also after a reload:
  // core keeps the record.
  for (const reload of [false, true]) {
    if (reload) await page.reload();
    await expect(row).toHaveAttribute("data-state", "brought");
    await expect(row.locator('[data-part="badge"]')).toHaveText("Brought across");
    await expect(row).toContainText("and was brought across");
    await expect(row).toContainText(
      "Of its settings and auth files: 4 brought across, 2 different in pluxy, 2 not brought across.",
    );
    await expect(row.getByRole("button", { name: "Bring it across" })).toHaveCount(0);
  }
  // The command can still run it again, and says what that does.
  await row.getByText("Bring it across again").click();
  await expect(row).toContainText("Run this where pluxy is installed to overwrite changed settings");
  await expect(row).toContainText("pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir");
});
