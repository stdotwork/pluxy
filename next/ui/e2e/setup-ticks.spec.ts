// Setup's step 1 on a real core whose computer holds Claude Code: the Claude kind is ticked when
// Claude Code is signed in, and not when it is only installed (a CLI does not say the person has a
// Claude subscription).
import { rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

test("an installed Claude Code ticks the Claude kind only when it is signed in", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  const one = page.getByRole("region", { name: "Your AI accounts" });
  const kind = one.locator('[data-part="setup-pick"]').filter({ hasText: "Claude" });
  await expect(kind.getByRole("checkbox")).toBeChecked();
  await expect(kind).toContainText("Claude Code is installed and signed in on this computer");

  // The sign-in goes; Claude Code stays installed. pluxy looks again, and the page is opened again.
  rmSync(join(dirname(core.config), "home", ".claude", ".credentials.json"));
  await core.mgmt("POST", "/setup/scan");
  await expect
    .poll(async () => (await core.mgmt<{ scanning: boolean }>("GET", "/setup")).body.scanning)
    .toBe(false);
  await page.reload();
  const again = page
    .getByRole("region", { name: "Your AI accounts" })
    .locator('[data-part="setup-pick"]')
    .filter({ hasText: "Claude" });
  await expect(again.getByRole("checkbox")).not.toBeChecked();
  await expect(again).not.toContainText("installed");
  await expect(page.getByRole("button", { name: "Add Claude", exact: true })).toHaveCount(0);
});
