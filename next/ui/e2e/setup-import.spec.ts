// The CLIProxyAPI import line above Setup's steps, against a real first-run core whose home folder
// holds one (panelcore -first-run -cpa): a config.yaml and an auth folder of three made-up
// accounts, two of them Claude's. Then Finish later, Home's card, and Setup reopened from it.
import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true, cpa: true });

interface Accounts {
  accounts: { id: string; extension: string }[];
}

// The refusals come first: once the setup is imported, the line says so.
test("a setup core refuses or can no longer read says why, and changes nothing", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/setup`);
  const line = page.locator('[data-part="setup-import"]');
  const file = join(dirname(core.config), "home", ".cli-proxy-api", "config.yaml");
  const before = (await core.mgmt<Accounts>("GET", "/accounts")).body.accounts;
  const text = readFileSync(file, "utf8");

  // A file core refuses.
  writeFileSync(file, "port: [8317\n");
  await line.getByRole("button", { name: "Preview import", exact: true }).click();
  await expect(line.getByRole("alert")).toContainText("This setup cannot be imported:");
  await expect(line.getByRole("alert")).toContainText("Nothing changed.");
  await expect(line.locator('[data-part="setup-report"]')).toBeHidden();

  // A file that is gone: the path core tried, and Try again.
  rmSync(file);
  await line.getByRole("button", { name: "Preview import", exact: true }).click();
  await expect(line.getByRole("alert")).toContainText("Could not read the CLIProxyAPI setup:");
  await expect(line.getByRole("alert")).toContainText(`there is no file at ${file}`);
  await expect(line.getByRole("button", { name: "Try again", exact: true })).toBeVisible();
  await expect(line).not.toContainText("pluxy import");
  expect((await core.mgmt<Accounts>("GET", "/accounts")).body.accounts).toEqual(before);
  writeFileSync(file, text);
});

test("a found CLIProxyAPI setup is previewed, imported, and the kind its accounts need is ticked", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();

  const line = page.locator('[data-part="setup-import"]');
  await expect(line).toContainText("Found a CLIProxyAPI setup with 3 account files.");
  await expect(line).toContainText("~/.cli-proxy-api/config.yaml");
  // Claude is ticked because Claude Code was found; unticked, only the import can tick it again.
  const kind = page.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await kind.getByRole("checkbox").uncheck();

  // The preview: core's report in its three lists, and nothing written.
  await line.getByRole("button", { name: "Preview import", exact: true }).click();
  const report = line.locator('[data-part="setup-report"]');
  await expect(report).toHaveAttribute("data-state", "preview");
  await expect(report).toContainText(
    "Importing would bring across 4, set 2 differently in pluxy and leave out 2.",
  );
  await report.getByText("Will import (4)").click();
  await expect(report).toContainText("auth file claude-work.json");
  await report.getByText("Set differently in pluxy (2)").click();
  await expect(report).toContainText("listen cannot change while core runs");
  await report.getByText("Left out (2)").click();
  await expect(report).toContainText("qwen-a.json");
  // No key or token of the setup is on the page.
  await expect(page.locator("body")).not.toContainText("fake-c");
  await expect(kind.getByRole("checkbox")).not.toBeChecked();
  expect((await core.mgmt<Accounts>("GET", "/accounts")).body.accounts).toEqual([]);

  // Cancel drops the preview; the import then runs from a new one.
  await line.getByRole("button", { name: "Cancel" }).click();
  await expect(report).toBeHidden();
  await line.getByRole("button", { name: "Preview import", exact: true }).click();
  await line.getByRole("button", { name: "Import now" }).click();
  await expect(report).toHaveAttribute("data-state", "done");
  await expect(line).toContainText("Imported.");
  await expect(line).toContainText("The imported accounts need Claude. They are ticked in step 1.");
  await expect(page.locator("body")).not.toContainText("fake-c");
  const accounts = (await core.mgmt<Accounts>("GET", "/accounts")).body.accounts;
  expect(accounts.map((a) => `${a.extension} ${a.id}`).sort()).toEqual([
    "pluxy/claude claude-home",
    "pluxy/claude claude-work",
  ]);

  // The kind the accounts need is ticked again, and Add adds it; the accounts show.
  await expect(kind.getByRole("checkbox")).toBeChecked();
  await page.getByRole("button", { name: "Add Claude", exact: true }).click();
  const one = page.getByRole("region", { name: "Your AI accounts" });
  await expect(one).toContainText("2 accounts connected.");
  await line.getByRole("button", { name: "Close" }).click();
  await expect(report).toBeHidden();

  // Closed, the line says the setup was imported, also after a reload: core keeps the record.
  // The reload opens the first step not done (the tool), so the person goes back to step 1.
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload();
      await page
        .locator('[data-part="setup-stepper"]')
        .getByRole("button", { name: /Your AI accounts/ })
        .click();
    }
    await expect(line).toHaveAttribute("data-state", "imported");
    await expect(line).toContainText("Imported a CLIProxyAPI setup");
    await expect(line.getByRole("button", { name: "Import again" })).toBeVisible();
  }
});

test("Finish later ends the first run and leaves a card on Home; Setup reopened has every step folded", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/setup`);
  await expect(
    page.getByText("Finish later leaves a reminder on Home. Setup stays under Settings."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Finish later", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(nav(page)).toBeVisible();
  expect((await core.mgmt<{ first_run: boolean }>("GET", "/setup")).body.first_run).toBe(false);
  // Home's card says what is left and leads back to Setup, now among the Settings topics.
  const card = page.locator('[data-part="setup-later"]');
  await expect(card).toContainText("Finish setting up pluxy");
  await expect(card).toContainText("Your coding tool");
  await card.getByRole("link", { name: "Continue setup" }).click();
  await expect(page).toHaveURL(/#\/setup$/);
  await expect(nav(page)).toBeVisible();
  await page.goto(`${core.base}/panel/#/`);
  await card.getByRole("button", { name: "Hide" }).click();
  await expect(card).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: "Home" })).toBeVisible();
  await expect(card).toHaveCount(0);
  await goTo(page, "Setup");
  const steps = page.locator('[data-part="setup-step"]');
  await expect(steps).toHaveCount(3);
  for (let i = 0; i < 3; i++) await expect(steps.nth(i)).not.toHaveAttribute("data-state", "open");
  await expect(page.getByText("Review or change your accounts and coding tools.")).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to Home" })).toHaveAttribute("href", "#/");
  await expect(page.getByRole("button", { name: "Finish later" })).toHaveCount(0);
});
