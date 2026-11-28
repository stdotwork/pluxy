// Setup's coding-tool step on a real core whose computer holds Claude Code (panelcore -first-run):
// a tool connected after pluxy's last look, while the person was on another page, shows as
// connected when Setup is opened again, with no Refresh.
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { expect, test } from "@playwright/test";
import { openPanel, signIn, useCore } from "./support";

const core = useCore({ firstRun: true });

test("a tool connected on another page shows as connected when Setup is opened again", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
  const two = page.locator('[data-part="setup-step"]').nth(1);
  await expect(two).toContainText("No tool connected yet.");

  // The person leaves for Tools, and Claude Code's settings are put in place there.
  await page.evaluate(() => {
    location.hash = "#/tools";
  });
  await expect(page.getByRole("heading", { level: 1, name: "Tools" })).toBeVisible();
  const settings = join(dirname(core.config), "home", ".claude", "settings.json");
  mkdirSync(dirname(settings), { recursive: true });
  writeFileSync(
    settings,
    JSON.stringify({
      env: {
        ANTHROPIC_BASE_URL: core.base,
        ANTHROPIC_AUTH_TOKEN: core.clientKey,
        CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: "1",
        CLAUDE_CODE_AUTO_MODE_SERVER: "0",
      },
    }),
  );

  // Back on Setup (the page opens again, the window never lost focus): it shows the tool connected.
  await page.evaluate(() => {
    location.hash = "#/setup";
  });
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
  await expect(two).toContainText("Claude Code");
  await expect(two).not.toContainText("No tool connected yet.");
});
