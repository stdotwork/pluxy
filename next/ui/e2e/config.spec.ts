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
  await nav(page).getByRole("link", { name: "Dashboard" }).click();
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
      if (tab === "Network")
        await expect(page.getByRole("combobox", { name: "Routing Strategy", exact: true })).toBeVisible();
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
      .poll(() =>
        editor.evaluate((e: HTMLTextAreaElement) => e.value.slice(e.selectionStart, e.selectionEnd)),
      )
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

test("the Network tab sets, keeps and clears the global proxy URL", async ({ page }) => {
  const settings = async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body;
  // The config as GET /config shows it: the proxy URL's secret part is a placeholder keyed by the
  // value, so the same placeholder means the same stored URL.
  const storedProxy = async () =>
    (await core.mgmt<{ content: string }>("GET", "/config")).body.content.match(
      /"proxy_url":\s*"([^"]*)"/,
    )?.[1];
  expect((await settings())["proxy-url"]).toBe("");

  const patches: Record<string, unknown>[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH" && new URL(r.url()).pathname.endsWith("/v0/management/settings"))
      patches.push(r.postDataJSON());
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Network", exact: true }).click();
  const proxy = page.getByRole("textbox", { name: "Proxy URL", exact: true });
  const save = page.getByRole("button", { name: "Save changes", exact: true });
  const saved = page.getByText("Configuration saved successfully", { exact: true });
  await expect(proxy).toHaveValue("");
  await expect(proxy).toHaveAttribute("placeholder", "socks5://user:pass@127.0.0.1:1080/");
  if (process.env.P7_SHOTS) await page.screenshot({ path: `${process.env.P7_SHOTS}/config-proxy-empty.png` });

  // A URL with a user name and password: core holds it and shows it with `redacted` in their place.
  await proxy.fill("socks5://alice:s3cretpass@127.0.0.1:1080/");
  await save.click();
  await expect(saved).toBeVisible();
  expect(patches).toEqual([{ "proxy-url": "socks5://alice:s3cretpass@127.0.0.1:1080/" }]);
  expect((await settings())["proxy-url"]).toBe("socks5://redacted@127.0.0.1:1080");
  await expect(proxy).toHaveValue("socks5://redacted@127.0.0.1:1080");
  const held = await storedProxy();
  expect(held).toMatch(/^pluxy-redacted-/);
  if (process.env.P7_SHOTS) await page.screenshot({ path: `${process.env.P7_SHOTS}/config-proxy-set.png` });

  // Saving another setting sends no proxy, so the password stays: the stored URL is the same one.
  await page.getByRole("switch", { name: "Force Model Prefix", exact: true }).check();
  await save.click();
  await expect(page.getByText("Configuration saved successfully", { exact: true })).toBeVisible();
  expect(patches).toHaveLength(2);
  expect(patches[1]).toEqual({ routing: { "force-model-prefix": true } });
  expect((await settings())["proxy-url"]).toBe("socks5://redacted@127.0.0.1:1080");
  expect(await storedProxy()).toBe(held);

  // A URL core refuses shows as other settings errors do, and changes nothing.
  await proxy.fill("ftp://proxy.example:21");
  await save.click();
  await expect(page.getByRole("alert")).toContainText("proxy-url");
  await expect(saved).toHaveCount(0);
  expect((await settings())["proxy-url"]).toBe("socks5://redacted@127.0.0.1:1080");
  expect(await storedProxy()).toBe(held);

  // Clearing the field removes the proxy.
  await proxy.fill("");
  await save.click();
  await expect(saved).toBeVisible();
  expect(patches.at(-1)).toEqual({ "proxy-url": "" });
  expect((await settings())["proxy-url"]).toBe("");
  expect(await storedProxy()).toBeUndefined();
  await expect(proxy).toHaveValue("");

  // The field is found by search, by its label and its settings name.
  await page.getByPlaceholder("Search settings (label or YAML key)").fill("proxy-url");
  await expect(proxy).toBeVisible();
});

test("review: fractional duration readbacks do not block unrelated saves or hide refusals", async ({
  page,
}) => {
  const current = (await core.mgmt<{ hash: string; content: string }>("GET", "/config")).body;
  const config = JSON.parse(current.content);
  config.routing = {
    ...config.routing,
    max_wait_ms: 500,
    transient_error_cooldown_ms: 500,
    request_retry: 2,
    force_model_prefix: false,
  };
  expect(
    (await core.mgmt("PUT", "/config", { content: JSON.stringify(config, null, 2), base_hash: current.hash }))
      .status,
  ).toBe(200);
  const readback = (await core.mgmt<CoreSettings>("GET", "/settings")).body;
  expect(readback.routing.retry["max-retry-interval"]).toBe(0.5);
  expect(readback.routing.cooldown["transient-error-cooldown-seconds"]).toBe(0.5);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Network", exact: true }).click();
  await page.getByRole("switch", { name: "Force Model Prefix", exact: true }).check();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByText("Configuration saved successfully", { exact: true })).toBeVisible();
  expect((await core.mgmt<CoreSettings>("GET", "/settings")).body.routing["force-model-prefix"]).toBe(true);
  const retry = page.getByRole("spinbutton", { name: "Request Retry Count", exact: true });
  await retry.fill("");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(retry).toHaveValue("");
  await expect(page.getByText("Configuration saved successfully", { exact: true })).toHaveCount(0);
  expect((await core.mgmt<CoreSettings>("GET", "/settings")).body.routing.retry["request-retry"]).toBe(2);
  await retry.fill("2");
  await page.getByRole("spinbutton", { name: "Max Retry Interval (seconds)", exact: true }).fill("0.75");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("cannot unmarshal number");
  await expect(page.getByText("Configuration saved successfully", { exact: true })).toHaveCount(0);
});

test("review: Source search starts at the first occurrence and repeated Enter keeps the draft", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
  await expect(editor).not.toHaveValue("");
  const draft = '{"first": "needle-one", "second": "needle-two"}';
  await editor.fill(draft);
  const search = page.getByPlaceholder("Search config...");
  const selection = () => editor.evaluate((e: HTMLTextAreaElement) => e.selectionStart);
  await search.fill("needle");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect.poll(selection).toBe(draft.indexOf("needle"));
  await search.fill("");
  await search.fill("needle");
  await search.press("Enter");
  await expect.poll(selection).toBe(draft.indexOf("needle"));
  await expect(search).toBeFocused();
  await page.keyboard.press("Enter");
  await expect.poll(selection).toBe(draft.lastIndexOf("needle"));
  await expect(search).toBeFocused();
  await expect(editor).toHaveValue(draft);
  await page.keyboard.press("Shift+Enter");
  await expect.poll(selection).toBe(draft.indexOf("needle"));
  await expect(page.locator('.config-source-search[data-part="field"]')).toHaveCount(0);
});

test("leaving with unsaved settings asks on Back and on a typed address, and Stay keeps the edit", async ({
  page,
}) => {
  await openPanel(page, core, "/panel/#/status");
  await signIn(page, core);
  await goTo(page, "Config Panel");
  const logToFile = page.getByRole("switch", { name: "Log to File", exact: true });
  const before = await logToFile.isChecked();
  await logToFile.setChecked(!before);
  const question = page.getByRole("dialog", { name: "Unsaved changes", exact: true });

  // Back is a change of the address's hash, like a typed #/… address.
  await page.goBack();
  await expect(question).toBeVisible();
  await expect(page).toHaveURL(/#\/config$/);
  await question.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(question).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Config Panel" })).toBeVisible();
  await expect(logToFile).toBeChecked({ checked: !before });

  await page.evaluate(() => {
    location.hash = "#/themes";
  });
  await expect(question).toBeVisible();
  await expect(page).toHaveURL(/#\/config$/);
  await question.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(logToFile).toBeChecked({ checked: !before });

  // Leave follows the address the person asked for.
  await page.evaluate(() => {
    location.hash = "#/themes";
  });
  await question.getByRole("button", { name: "Leave", exact: true }).click();
  await expect(page.getByRole("heading", { name: /Themes$/, level: 1 })).toBeVisible();
  await expect(page).toHaveURL(/#\/themes$/);

  // With nothing unsaved, Back leaves without asking.
  await goTo(page, "Config Panel");
  await expect(logToFile).toBeChecked({ checked: before });
  await page.goBack();
  await expect(page.getByRole("heading", { name: /Themes$/, level: 1 })).toBeVisible();
  await expect(question).toHaveCount(0);
});

test("review: unavailable config source leaves settings usable and shows its error only in Source", async ({
  page,
}) => {
  // Core's real 422 shape: it cannot safely redact a file that does not parse, so carries no content.
  await page.route("**/v0/management/config", (route) =>
    route.fulfill({
      status: 422,
      contentType: "application/json",
      body: JSON.stringify({
        error: {
          code: "invalid_config",
          message: "the config file does not parse, so it cannot be shown with its secrets hidden",
        },
      }),
    }),
  );
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Config Panel");
  await expect(page.getByRole("switch", { name: "Log to File", exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.getByRole("tab", { name: "Network", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Routing Strategy", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("cannot be shown with its secrets hidden");
  await expect(page.getByText("Loading configuration...", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "Configuration source", exact: true })).toHaveCount(0);
});

test("the Config Panel's tab is in its address: opened, typed, clicked and Back", async ({ page }) => {
  await openPanel(page, core, "/panel/#/config/source");
  await signIn(page, core);
  const tab = (name: string) => page.getByRole("tab", { name, exact: true });
  const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
  await expect(tab("Source")).toHaveAttribute("aria-selected", "true");
  await expect(editor).not.toHaveValue("");

  await tab("Network").click();
  await expect(page).toHaveURL(/#\/config\/network$/);
  await tab("Common").click();
  await expect(page).toHaveURL(/#\/config$/);

  await page.evaluate(() => {
    location.hash = "#/config/logging";
  });
  await expect(tab("Logging")).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/#\/config\/logging$/);
  await page.evaluate(() => {
    location.hash = "#/config/source";
  });
  await expect(tab("Source")).toHaveAttribute("aria-selected", "true");
  await expect(editor).not.toHaveValue("");

  await page.goBack();
  await expect(tab("Logging")).toHaveAttribute("aria-selected", "true");
  await page.goBack();
  await expect(tab("Common")).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/#\/config$/);

  // A tab the page does not have opens the first.
  await page.evaluate(() => {
    location.hash = "#/config/nope";
  });
  await expect(tab("Network")).toHaveAttribute("aria-selected", "false");
  await expect(tab("Common")).toHaveAttribute("aria-selected", "true");
});

test("moving between the page's tabs does not ask about unsaved settings, except to and from Source", async ({
  page,
}) => {
  await openPanel(page, core, "/panel/#/config");
  await signIn(page, core);
  const logToFile = page.getByRole("switch", { name: "Log to File", exact: true });
  const before = await logToFile.isChecked();
  await logToFile.setChecked(!before);
  const question = page.getByRole("dialog", { name: "Unsaved changes", exact: true });
  const tab = (name: string) => page.getByRole("tab", { name, exact: true });

  await tab("Network").click();
  await page.evaluate(() => {
    location.hash = "#/config/logging";
  });
  await expect(tab("Logging")).toHaveAttribute("aria-selected", "true");
  await expect(question).toHaveCount(0);
  await tab("Common").click();
  await expect(logToFile).toBeChecked({ checked: !before });

  // Source is its own text, so a move to it asks, as its tab does; Stay keeps the tab and address.
  await page.evaluate(() => {
    location.hash = "#/config/source";
  });
  await expect(question).toBeVisible();
  await expect(page).toHaveURL(/#\/config$/);
  await question.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(tab("Common")).toHaveAttribute("aria-selected", "true");
  await expect(logToFile).toBeChecked({ checked: !before });
  await page.evaluate(() => {
    location.hash = "#/config/source";
  });
  await question.getByRole("button", { name: "Leave", exact: true }).click();
  await expect(tab("Source")).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/#\/config\/source$/);
  await expect(page.getByRole("textbox", { name: "Configuration source", exact: true })).not.toHaveValue("");
  await tab("Common").click();
  await expect(logToFile).toBeChecked({ checked: before });
});

test("a typed Source address with an account scrolls a long file to the account", async ({ page }) => {
  const current = await core.mgmt<{ hash: string; content: string }>("GET", "/config");
  expect(
    (
      await core.mgmt("PUT", "/config", {
        base_hash: current.body.hash,
        content: `${JSON.stringify(JSON.parse(current.body.content), null, 2)}\n`,
      })
    ).status,
  ).toBe(200);
  await openPanel(page, core, "/panel/#/config/source?account=alpha-2");
  await signIn(page, core);
  const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
  await expect(editor).not.toHaveValue("");
  const view = () =>
    editor.evaluate((e: HTMLTextAreaElement) => {
      const lineHeight = Number.parseFloat(getComputedStyle(e).lineHeight);
      const row = e.value.slice(0, e.selectionStart).split("\n").length - 1;
      return {
        text: e.value.slice(e.selectionStart, e.selectionEnd),
        row,
        shown: row * lineHeight >= e.scrollTop && (row + 1) * lineHeight <= e.scrollTop + e.clientHeight,
        scrolled: e.scrollTop > 0,
      };
    });
  await expect.poll(view).toMatchObject({ text: "alpha-2", shown: true, scrolled: true });
  expect((await view()).row).toBeGreaterThan(20); // below the first screen, so the scroll was needed
});
