import { expect, test } from "@playwright/test";
import type { CoreSettings } from "../kit/api/settings";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();
test("config settings, exact source diff, revision and concurrent edit protection", async ({ page }) => {
  await core.mgmt("PATCH", "/settings", {
    observability: { logs: { "request-log": false, "error-logs-max-files": 10 } },
  });
  const initial = await core.mgmt<{ hash: string; content: string }>("GET", "/config");
  expect(
    (
      await core.mgmt("PUT", "/config", {
        base_hash: initial.body.hash,
        content: `${JSON.stringify(JSON.parse(initial.body.content), null, 2)}\n`,
      })
    ).status,
  ).toBe(200);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Logging", exact: true }).click();
  await page.getByRole("switch", { name: "Enable request logging" }).check();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect
    .poll(
      async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body.observability.logs["request-log"],
    )
    .toBe(true);
  await page.getByPlaceholder("Search settings (label or YAML key)").fill("error-logs-max-files");
  await expect(page.getByLabel("Retained Error Log Files")).toBeVisible();
  await expect(page.getByRole("switch", { name: "Enable request logging" })).toHaveCount(0);
  await page.getByPlaceholder("Search settings (label or YAML key)").fill("");
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
  await expect(editor).not.toHaveValue("");
  const before = await editor.inputValue();
  const oldLine = before.split("\n").find((line) => /"error_logs_max_files": 10/.test(line));
  expect(oldLine).toBeDefined();
  const newLine = oldLine?.replace("10", "12") ?? "";
  const edited = before.replace(oldLine ?? "", newLine);
  await editor.fill(edited);
  await page.getByRole("button", { name: "Validate", exact: true }).click();
  await expect(page.getByText("Configuration is valid", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Review Changes" })).toBeVisible();
  await expect(page.locator('[data-part="diff-line"][data-change="remove"]')).toHaveText([`− ${oldLine}\n`]);
  await expect(page.locator('[data-part="diff-line"][data-change="add"]')).toHaveText([`+ ${newLine}\n`]);
  const revision = (await core.mgmt<{ active: { revision: number } }>("GET", "/status")).body.active.revision;
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Configuration saved successfully", { exact: true })).toBeVisible();
  await nav(page)
    .getByRole("link", { name: /^(Status|Dashboard)$/ })
    .click();
  await expect(page.locator(".facts > div").filter({ hasText: "Active revision" }).locator("dd")).toHaveText(
    String(revision + 1),
  );
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await expect(editor).toHaveValue(edited);
  const draft = edited.replace(newLine, newLine.replace("12", "13"));
  await editor.fill(draft);
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  const current = await core.mgmt<{ hash: string; content: string }>("GET", "/config");
  expect(
    (
      await core.mgmt("PUT", "/config", {
        base_hash: current.body.hash,
        content: current.body.content.replace(newLine, newLine.replace("12", "14")),
      })
    ).status,
  ).toBe(200);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Saving was blocked");
  await expect(editor).toHaveValue(draft);
  await nav(page).getByRole("link", { name: "Themes" }).click();
  await expect(page.getByRole("dialog", { name: "Unsaved changes" })).toBeVisible();
  await page.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(editor).toHaveValue(draft);
  await page.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(editor).toHaveValue(edited);
});

for (const theme of ["default", "terminal", "paper"]) {
  test(`config tabs, search and draft controls in ${theme}`, async ({ page }) => {
    await page.addInitScript((id) => {
      localStorage.setItem("pluxy.theme", `pluxy/${id}`);
      localStorage.setItem(
        "pluxy.choices",
        JSON.stringify({ layout: { tabs: id === "terminal" ? "side" : "top" } }),
      );
    }, theme);
    await openPanel(page, core);
    await signIn(page, core);
    await nav(page).locator('a[href="#/config"]').click();
    await expect(page.getByRole("heading", { level: 1, name: /Config Panel$/ })).toBeVisible();
    for (const tab of ["Common", "Network", "Logging", "Advanced", "Source"]) {
      await page.getByRole("tab", { name: tab, exact: true }).click();
      await expect(page.getByRole("tabpanel")).toBeVisible();
      if (tab === "Common") await expect(page.getByRole("switch")).toHaveCount(1);
      if (tab === "Network") await expect(page.getByLabel("Routing Strategy", { exact: true })).toBeVisible();
      if (process.env.P7_SHOTS)
        await page.screenshot({
          path: `${process.env.P7_SHOTS}/${theme}-${tab.toLowerCase()}.png`,
          fullPage: true,
        });
    }
    const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
    await expect(editor).not.toHaveValue("");
    const original = await editor.inputValue();
    await page.getByPlaceholder("Search config...").fill("error_logs_max_files");
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await expect
      .poll(() => editor.evaluate((e: HTMLTextAreaElement) => e.value.slice(e.selectionStart, e.selectionEnd)))
      .toBe("error_logs_max_files");
    await page.getByRole("button", { name: "Previous", exact: true }).click();
    await page.getByPlaceholder("Search config...").fill("no-such-config-key");
    await expect(page.getByText("No results", { exact: true })).toBeVisible();
    await page.getByPlaceholder("Search config...").fill("");
    await editor.fill("{");
    await page.getByRole("button", { name: "Validate", exact: true }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    const reloadDialog = page.getByRole("dialog", { name: "Reload", exact: true });
    await expect(reloadDialog).toContainText("Reloading will discard your unsaved changes.");
    await reloadDialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(editor).toHaveValue("{");
    await page.getByRole("button", { name: "Discard changes", exact: true }).click();
    await expect(editor).toHaveValue(original);
    const changed = original.replace(/("error_logs_max_files":\s*)\d+/, "$199");
    expect(changed).not.toBe(original);
    await editor.fill(changed);
    await page.getByRole("button", { name: "Save changes", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Show changes", exact: true })).toBeVisible();
    if (process.env.P7_SHOTS)
      await page.screenshot({ path: `${process.env.P7_SHOTS}/${theme}-diff.png`, fullPage: true });
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await reloadDialog.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(editor).toHaveValue(original);
    await editor.fill(changed);
    await nav(page).locator('a[href="#/themes"]').click();
    await page.getByRole("button", { name: "Leave", exact: true }).click();
    await expect(page.getByRole("heading", { name: /Themes$/, level: 1 })).toBeVisible();
  });
}
