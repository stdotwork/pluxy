// Config: the Settings tab's rows that save the moment they change, the Settings file tab with its
// review before saving, the page's addresses, a settings file that does not parse, and Replace key.
// Against a real core with fake extensions.
//
// With PANEL_SHOTS_DIR set to a folder, the spec also saves a screenshot of each screen and state
// there (Default, Terminal and Paper, light and dark, wide and small), for a person to look at.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, type Route, test } from "@playwright/test";
import type { CoreSettings } from "../kit/api/settings";
import { goToId, openPanel, pageTitle, signIn, topics, useCore } from "./support";

const core = useCore();
// Replace key's replies hold a real management key: never keep traces or failure screenshots.
test.use({ trace: "off", screenshot: "off", video: "off" });

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const SIZES = [
  { width: 1100, height: 760 },
  { width: 640, height: 480 },
] as const;
const SHOTS = process.env.PANEL_SHOTS_DIR;

const settings = async () => (await core.mgmt<CoreSettings>("GET", "/settings")).body;
const tab = (page: Page, name: string) => page.getByRole("tab", { name, exact: true });
const editor = (page: Page) => page.getByRole("textbox", { name: "Settings file", exact: true });
const row = (page: Page, name: string) =>
  page.locator('[data-part="setting-row"]').filter({
    has: page.locator(".setting-name", { hasText: new RegExp(`^${name.replace(/[()]/g, "\\$&")}$`) }),
  });
const leaveQuestion = (page: Page) =>
  page.getByRole("dialog", { name: "You have edits that are not saved. Leave and lose them?", exact: true });

/** The bodies of the PATCH /settings the page sends, in order. */
function patches(page: Page) {
  const sent: unknown[] = [];
  page.on("request", (r) => {
    if (r.method() === "PATCH" && new URL(r.url()).pathname.endsWith("/v0/management/settings"))
      sent.push(r.postDataJSON());
  });
  return sent;
}

/** The settings file as GET /config shows it: a secret's placeholder is keyed by the value it hides. */
const storedProxy = async () =>
  (await core.mgmt<{ content: string }>("GET", "/config")).body.content.match(
    /"proxy_url":\s*"([^"]*)"/,
  )?.[1];

/** Writes the settings file back pretty-printed, one setting a line, so a test can edit one line. */
async function prettyFile() {
  const current = await core.mgmt<{ hash: string; content: string }>("GET", "/config");
  const put = await core.mgmt("PUT", "/config", {
    base_hash: current.body.hash,
    content: `${JSON.stringify(JSON.parse(current.body.content), null, 2)}\n`,
  });
  expect(put.status).toBe(200);
}

const unparsed = (route: Route) =>
  route.request().method() === "GET"
    ? route.fulfill({
        status: 422,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "invalid_config",
            message:
              "the config file does not parse, so it cannot be shown with its secrets hidden: line 3: unexpected }",
          },
        }),
      })
    : route.continue();

test("a Logging switch saves the moment it changes and is still on after a reload", async ({ page }) => {
  await core.mgmt("PATCH", "/settings", { observability: { logs: { "request-log": false } } });
  const sent = patches(page);
  await openPanel(page, core, "/panel/#/config/logging");
  await signIn(page, core);
  const logEvery = page.getByRole("switch", { name: "Log every request", exact: true });
  await expect(logEvery).not.toBeChecked();
  await logEvery.check();
  await expect.poll(async () => (await settings()).observability.logs["request-log"]).toBe(true);
  expect(sent).toEqual([{ observability: { logs: { "request-log": true } } }]);
  await expect(row(page, "Log every request")).not.toContainText("Saving…");
  await page.reload();
  await expect(logEvery).toBeChecked();
  await logEvery.uncheck();
  await expect.poll(async () => (await settings()).observability.logs["request-log"]).toBe(false);
  await expect(page.getByRole("button", { name: "Save changes" })).toHaveCount(0);
});

test("the settings file: edit, check, review and save; a save over a newer file keeps the edits", async ({
  page,
}) => {
  await core.mgmt("PATCH", "/settings", { observability: { logs: { "error-logs-max-files": 10 } } });
  await prettyFile();
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "config");
  await tab(page, "Settings file").click();
  await expect(page).toHaveURL(/#\/config\/source$/);
  await expect(editor(page)).not.toHaveValue("");
  await expect(page.getByRole("button", { name: "Review and save", exact: true })).toHaveCount(0);
  const before = await editor(page).inputValue();
  const oldLine = before.split("\n").find((line) => /"error_logs_max_files": 10/.test(line)) ?? "";
  expect(oldLine).not.toBe("");
  const newLine = oldLine.replace("10", "12");
  const edited = before.replace(oldLine, newLine);
  await editor(page).fill(edited);
  await page.getByRole("button", { name: "Check for mistakes", exact: true }).click();
  await expect(page.getByText("No mistakes found.")).toBeVisible();
  await page.getByRole("button", { name: "Review and save", exact: true }).click();
  const review = page.getByRole("dialog", { name: "Review changes" });
  await expect(review).toBeVisible();
  await expect(review).toContainText("Removed");
  await expect(review).toContainText("Added");
  await expect(page.locator('[data-part="diff-line"][data-change="remove"]')).toHaveText([`− ${oldLine}\n`]);
  await expect(page.locator('[data-part="diff-line"][data-change="add"]')).toHaveText([`+ ${newLine}\n`]);
  const revision = (await core.mgmt<{ active: { revision: number } }>("GET", "/status")).body.active.revision;
  await review.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Saved.", { exact: true })).toBeVisible();
  // The saved file is the one core runs now, and the Settings tab shows it.
  expect((await core.mgmt<{ active: { revision: number } }>("GET", "/status")).body.active.revision).toBe(
    revision + 1,
  );
  await tab(page, "Settings").click();
  await expect(page.getByRole("spinbutton", { name: "Failed requests to keep", exact: true })).toHaveValue(
    "12",
  );
  await tab(page, "Settings file").click();
  await expect(editor(page)).toHaveValue(edited);

  // Someone else saves the file meanwhile: this save is refused and the edits stay.
  const draft = edited.replace(newLine, newLine.replace("12", "13"));
  await editor(page).fill(draft);
  await page.getByRole("button", { name: "Review and save", exact: true }).click();
  const current = await core.mgmt<{ hash: string; content: string }>("GET", "/config");
  const put = await core.mgmt("PUT", "/config", {
    base_hash: current.body.hash,
    content: current.body.content.replace(newLine, newLine.replace("12", "14")),
  });
  expect(put.status).toBe(200);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "The file changed while you were editing. Copy your edits, press Refresh, and make them again.",
  );
  await expect(editor(page)).toHaveValue(draft);
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  const refresh = page.getByRole("dialog", { name: "Discard your edits and refresh the file?", exact: true });
  await refresh.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(editor(page)).toHaveValue(draft);
  await page.getByRole("button", { name: "Discard edits", exact: true }).click();
  await expect(editor(page)).toHaveValue(edited);
});

test("leaving with edits in the file asks on a link, Back, a typed address and the other tab", async ({
  page,
}) => {
  await openPanel(page, core, "/panel/#/status");
  await signIn(page, core);
  await goToId(page, "config");
  // Settings rows have no draft: moving between the tabs asks nothing.
  await tab(page, "Settings file").click();
  await tab(page, "Settings").click();
  await tab(page, "Settings file").click();
  await expect(leaveQuestion(page)).toHaveCount(0);
  await expect(editor(page)).not.toHaveValue("");
  const original = await editor(page).inputValue();
  await editor(page).fill(`${original} `);

  await topics(page).getByRole("link", { name: "Themes", exact: true }).click();
  await expect(leaveQuestion(page)).toBeVisible();
  await page.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(editor(page)).toHaveValue(`${original} `);

  await page.goBack();
  await expect(leaveQuestion(page)).toBeVisible();
  await expect(page).toHaveURL(/#\/config\/source$/);
  await page.getByRole("button", { name: "Stay", exact: true }).click();

  await tab(page, "Settings").click();
  await expect(leaveQuestion(page)).toBeVisible();
  await page.getByRole("button", { name: "Stay", exact: true }).click();
  await expect(tab(page, "Settings file")).toHaveAttribute("aria-selected", "true");

  await page.evaluate(() => {
    location.hash = "#/themes";
  });
  await expect(leaveQuestion(page)).toBeVisible();
  await page.getByRole("button", { name: "Leave", exact: true }).click();
  await expect(pageTitle(page)).toHaveText("Themes");
  await expect(page).toHaveURL(/#\/themes$/);
});

test("the old tabs' addresses open the Settings tab at their section", async ({ page }) => {
  await page.setViewportSize({ width: 640, height: 480 });
  await openPanel(page, core, "/panel/#/config/connectivity");
  await signIn(page, core);
  await expect(tab(page, "Settings")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("button", { name: "Replace key", exact: true })).toBeInViewport();
  await page.evaluate(() => {
    location.hash = "#/config/logging";
  });
  await expect(page.locator("#logging h2")).toBeInViewport();
  await page.evaluate(() => {
    location.hash = "#/config/source";
  });
  await expect(tab(page, "Settings file")).toHaveAttribute("aria-selected", "true");
  await expect(editor(page)).not.toHaveValue("");
  for (const old of ["common", "network", "advanced", "nope"]) {
    await page.evaluate((t) => {
      location.hash = `#/config/${t}`;
    }, old);
    await expect(tab(page, "Settings")).toHaveAttribute("aria-selected", "true");
  }
  await tab(page, "Settings file").click();
  await expect(page).toHaveURL(/#\/config\/source$/);
  await tab(page, "Settings").click();
  await expect(page).toHaveURL(/#\/config$/);
});

test("the outbound proxy saves on Enter, keeps its password unless a new one is typed, and clears", async ({
  page,
}) => {
  await core.mgmt("PATCH", "/settings", { observability: { logs: { "error-logs-max-files": 10 } } });
  await prettyFile();
  expect((await settings())["proxy-url"]).toBe("");
  const sent = patches(page);
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "config");
  const proxy = page.getByRole("textbox", { name: "Outbound proxy", exact: true });
  await expect(proxy).toHaveValue("");
  await expect(proxy).toHaveAttribute("placeholder", "socks5://user:pass@host:1080");

  // A URL with a user name and password: core holds it and shows it with `redacted` in their place.
  await proxy.fill("socks5://alice:s3cretpass@127.0.0.1:1080/");
  await proxy.press("Enter");
  await expect(proxy).toHaveValue("socks5://redacted@127.0.0.1:1080");
  expect(sent).toEqual([{ "proxy-url": "socks5://alice:s3cretpass@127.0.0.1:1080/" }]);
  const held = await storedProxy();
  expect(held).toMatch(/^pluxy-redacted-/);

  // Sent back as shown, it is not sent at all; another setting saves alone; the password stays.
  await proxy.press("Enter");
  await proxy.blur();
  const usage = page.getByRole("switch", { name: "Keep a record of requests", exact: true });
  const usageOn = await usage.isChecked();
  await usage.setChecked(!usageOn);
  await expect
    .poll(async () => (await settings()).observability.usage["usage-statistics-enabled"])
    .toBe(!usageOn);
  expect(sent).toEqual([
    { "proxy-url": "socks5://alice:s3cretpass@127.0.0.1:1080/" },
    { observability: { usage: { "usage-statistics-enabled": !usageOn } } },
  ]);
  expect(await storedProxy()).toBe(held);
  await usage.setChecked(usageOn);
  await expect
    .poll(async () => (await settings()).observability.usage["usage-statistics-enabled"])
    .toBe(usageOn);

  // The file saved with the placeholder left as it is keeps the password too.
  await tab(page, "Settings file").click();
  const text = await editor(page).inputValue();
  expect(text).toContain(held);
  await editor(page).fill(text.replace(/("error_logs_max_files":\s*)\d+/, "$17"));
  await page.getByRole("button", { name: "Review and save", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Saved.", { exact: true })).toBeVisible();
  expect(await storedProxy()).toBe(held);
  await tab(page, "Settings").click();

  // A URL core refuses goes back, with core's reason under the row.
  await proxy.fill("ftp://proxy.example:21");
  await proxy.press("Enter");
  await expect(row(page, "Outbound proxy").getByRole("alert")).toContainText("proxy");
  await expect(proxy).toHaveValue("socks5://redacted@127.0.0.1:1080");
  expect(await storedProxy()).toBe(held);

  // Emptied, the proxy goes.
  await proxy.fill("");
  await proxy.blur();
  await expect.poll(async () => (await settings())["proxy-url"]).toBe("");
  expect(sent.at(-1)).toEqual({ "proxy-url": "" });
  expect(await storedProxy()).toBeUndefined();
});

test("a settings file that does not parse turns every setting off, says so on both tabs, and Try again reads it again", async ({
  page,
}) => {
  await page.route("**/v0/management/config", unparsed);
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "config");
  const message = page.getByText(
    "pluxy's settings file has a mistake, so it cannot be shown or changed here: line 3: unexpected }. pluxy keeps working with the settings it read before.",
  );
  await expect(message).toBeVisible();
  await expect(page.getByRole("switch", { name: "Log every request", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Replace key", exact: true })).toBeDisabled();
  await expect(page.getByText("Fix the settings file first.", { exact: true })).toHaveCount(7);
  await tab(page, "Settings file").click();
  await expect(message).toBeVisible();
  await expect(editor(page)).toHaveCount(0);
  await page.unroute("**/v0/management/config");
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(editor(page)).not.toHaveValue("");
  await expect(message).toHaveCount(0);
  await tab(page, "Settings").click();
  await expect(page.getByRole("switch", { name: "Log every request", exact: true })).toBeEnabled();
});

test("a newer settings file that pluxy refused is said on the page", async ({ page }) => {
  await page.route("**/v0/management/status", async (route) => {
    const response = await route.fetch();
    const status = await response.json();
    status.desired = { ...status.active, revision: status.active.revision + 1, error: "listen: port in use" };
    await route.fulfill({ response, json: status });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goToId(page, "config");
  await expect(
    page.getByText(
      "pluxy is not using the latest version of its settings file: listen: port in use. It keeps working with the version before.",
    ),
  ).toBeVisible();
});

test("a typed Settings file address with an account scrolls a long file to the account", async ({ page }) => {
  await prettyFile();
  await openPanel(page, core, "/panel/#/config/source?account=alpha-2");
  await signIn(page, core);
  await expect(editor(page)).not.toHaveValue("");
  const view = () =>
    editor(page).evaluate((e: HTMLTextAreaElement) => {
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

test.describe("Replace key", () => {
  // Each test replaces core's key: the next test signs in with the new one.
  for (const remember of [false, true]) {
    test(`asks first, then shows the new key once and keeps the panel signed in (${remember ? "remembered" : "this tab"})`, async ({
      page,
      context,
    }) => {
      await context.grantPermissions(["clipboard-read", "clipboard-write"]);
      await openPanel(page, core);
      if (remember) await page.getByLabel("Remember on this device").check();
      await signIn(page, core);
      await goToId(page, "config");
      let calls = 0;
      page.on("request", (r) => {
        if (r.url().endsWith("/management-key/rotate")) calls++;
      });
      const keys = page.getByRole("region", { name: "Keys", exact: true });
      await keys.getByRole("button", { name: "Replace key", exact: true }).click();
      await expect(keys.getByRole("button", { name: "Yes, replace key", exact: true })).toBeVisible();
      await keys.getByRole("button", { name: "Cancel", exact: true }).click();
      expect(calls).toBe(0);
      await keys.getByRole("button", { name: "Replace key", exact: true }).click();
      await keys.getByRole("button", { name: "Yes, replace key", exact: true }).click();
      const notice = keys.locator('[data-part="key-notice"]');
      await expect(notice).toBeVisible();
      await expect(notice).toContainText("New management key");
      await expect(notice).toContainText("This browser already uses it. Copy it now: it is not shown again.");
      await expect(notice).toBeFocused();
      const oldKey = core.key;
      const newKey = (await notice.locator('[data-part="key-value"]').textContent()) ?? "";
      expect(newKey.length > 16 && newKey !== oldKey).toBe(true);
      core.key = newKey;
      expect(calls).toBe(1);
      expect(
        await page.evaluate(
          ({ secret, remembered }) => {
            const kept = remembered ? localStorage : sessionStorage;
            const other = remembered ? sessionStorage : localStorage;
            return kept.getItem("pluxy.key") === secret && other.getItem("pluxy.key") === null;
          },
          { secret: newKey, remembered: remember },
        ),
      ).toBe(true);
      const old = await fetch(`${core.base}/v0/management/status`, {
        headers: { Authorization: `Bearer ${oldKey}` },
      });
      expect(old.status).toBe(401);
      await notice.getByRole("button", { name: "Copy", exact: true }).click();
      expect((await page.evaluate(() => navigator.clipboard.readText())) === newKey).toBe(true);
      // Leaving the tab takes it away; it is not shown again.
      await tab(page, "Settings file").click();
      await tab(page, "Settings").click();
      await expect(page.locator('[data-part="key-value"]')).toHaveCount(0);
      await goToId(page, "status");
      await page.reload();
      await expect(pageTitle(page)).toHaveText("Home");
    });
  }

  test("a reply that comes after leaving the tab is shown on coming back", async ({ page }) => {
    await openPanel(page, core);
    await signIn(page, core);
    await goToId(page, "config");
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let received!: () => void;
    const responseReady = new Promise<void>((resolve) => {
      received = resolve;
    });
    await page.route("**/management-key/rotate", async (route) => {
      const response = await route.fetch();
      core.key = (await response.json()).value;
      received();
      await gate;
      await route.fulfill({ response });
    });
    await page.getByRole("button", { name: "Replace key", exact: true }).click();
    await page.getByRole("button", { name: "Yes, replace key", exact: true }).click();
    await responseReady;
    await tab(page, "Settings file").click();
    release();
    await tab(page, "Settings").click();
    const notice = page.locator('[data-part="key-notice"]');
    await expect(notice).toBeVisible();
    expect((await notice.locator('[data-part="key-value"]').textContent()) === core.key).toBe(true);
  });

  test("a lost reply says how to find the key, and Replace key stays off", async ({ page }) => {
    await openPanel(page, core);
    await signIn(page, core);
    await goToId(page, "config");
    await page.route("**/management-key/rotate", async (route) => {
      const response = await route.fetch();
      core.key = (await response.json()).value;
      await route.abort("failed");
    });
    await page.getByRole("button", { name: "Replace key", exact: true }).click();
    await page.getByRole("button", { name: "Yes, replace key", exact: true }).click();
    await expect(page.getByText(/^pluxy did not answer, so the key may have been replaced\./)).toContainText(
      "run pluxy key management --show-key on the computer pluxy runs on",
    );
    await expect(page.getByRole("button", { name: "Replace key", exact: true })).toBeDisabled();
  });
});

/** Holds every call to path until the test ends, so the page shows it waiting. */
const hold = (page: Page, path: string, method = "GET") =>
  page.route(`**/v0/management${path}`, (route) =>
    route.request().method() === method ? new Promise(() => {}) : route.continue(),
  );

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    for (const size of SIZES) {
      const where = `${theme || "pluxy/default"}, ${appearance}, ${size.width}x${size.height}`.replace(
        "pluxy/",
        "",
      );
      const open = async (page: Page, path = "/panel/#/config") => {
        await core.mgmt("PUT", "/theme", { id: theme });
        await page.setViewportSize(size);
        await page.emulateMedia({ colorScheme: appearance });
        await openPanel(page, core, path);
        await signIn(page, core);
        await expect(pageTitle(page)).toHaveText("Config");
      };
      const shot = async (page: Page, file: string) => {
        if (!SHOTS) return;
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({
          path: join(
            SHOTS,
            `${file}-${(theme || "default").replace("pluxy/", "")}-${appearance}-${size.width}.png`,
          ),
          animations: "disabled",
        });
      };
      /** Nothing is wider than the window, and every button is as high as the theme's controls. */
      const fits = async (page: Page) => {
        expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
        const wrong = await page.evaluate(() => {
          const control = Number.parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue("--px-size-control"),
          );
          const main = document.querySelector("main");
          return [
            ...document.querySelectorAll<HTMLElement>("main .btn, main select, main input, dialog .btn"),
          ]
            .filter((b) => b.getBoundingClientRect().height > 0)
            .filter((b) => b.getBoundingClientRect().height < control - 0.5)
            .filter((b) => !(b instanceof HTMLInputElement && ["checkbox", "radio"].includes(b.type)))
            .map(
              (b) =>
                `${b.tagName}.${b.className} ${b.textContent}: ${b.getBoundingClientRect().height} < ${control}`,
            )
            .concat(main && main.scrollWidth > main.clientWidth ? ["the page area scrolls sideways"] : []);
        });
        expect(wrong).toEqual([]);
      };

      test(`Config's Settings tab and its states fit: ${where}`, async ({ page }) => {
        await open(page);
        await expect(page.getByRole("switch", { name: "Log every request", exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "config-settings");
        await page.locator("#keys").scrollIntoViewIfNeeded();
        await shot(page, "config-settings-keys");
        // A save on its way, then refused: Saving…, then the row goes back with the reason.
        let refuse!: () => void;
        const refused = new Promise<void>((resolve) => {
          refuse = resolve;
        });
        await page.route("**/v0/management/settings", async (route) => {
          if (route.request().method() !== "PATCH") return route.continue();
          await refused;
          await route.fulfill({
            status: 400,
            json: { error: { code: "invalid_request", message: "the settings file is locked" } },
          });
        });
        const logToFile = page.getByRole("switch", { name: "Write pluxy's log to a file", exact: true });
        const was = await logToFile.isChecked();
        await logToFile.setChecked(!was);
        await expect(row(page, "Write pluxy's log to a file")).toContainText("Saving…");
        await shot(page, "config-saving");
        refuse();
        await expect(row(page, "Write pluxy's log to a file").getByRole("alert")).toContainText(
          "the settings file is locked",
        );
        await expect(logToFile).toBeChecked({ checked: was });
        await fits(page);
        await shot(page, "config-refused");
        // Replace key: the question, a new key (core's key is replaced: the panel signs in with the
        // new one by itself), then the app's outcome and no answer, made up so core's key stays.
        const keys = page.getByRole("region", { name: "Keys", exact: true });
        await keys.getByRole("button", { name: "Replace key", exact: true }).click();
        await fits(page);
        await shot(page, "config-replace-ask");
        await keys.getByRole("button", { name: "Yes, replace key", exact: true }).click();
        const notice = keys.locator('[data-part="key-notice"]');
        await expect(notice).toBeVisible();
        core.key = (await notice.locator('[data-part="key-value"]').textContent()) ?? "";
        await fits(page);
        await shot(page, "config-new-key");
        await keys.getByRole("button", { name: "Close", exact: true }).click();
        let reply: { status: number; json?: unknown } = { status: 200, json: { kept: "device" } };
        await page.route("**/management-key/rotate", (route) =>
          reply.status === 0 ? route.abort("failed") : route.fulfill(reply),
        );
        await keys.getByRole("button", { name: "Replace key", exact: true }).click();
        await keys.getByRole("button", { name: "Yes, replace key", exact: true }).click();
        await expect(keys).toContainText("Replaced. The app uses the new key");
        await shot(page, "config-replaced-in-app");
        reply = { status: 0 };
        await keys.getByRole("button", { name: "Replace key", exact: true }).click();
        await keys.getByRole("button", { name: "Yes, replace key", exact: true }).click();
        await expect(keys).toContainText("pluxy did not answer");
        await fits(page);
        await shot(page, "config-no-answer");
      });

      test(`Config's Settings file tab and its states fit: ${where}`, async ({ page }) => {
        await open(page, "/panel/#/config/source");
        await expect(editor(page)).not.toHaveValue("");
        await fits(page);
        await shot(page, "config-file");
        const text = await editor(page).inputValue();
        await editor(page).fill(`${text}\n`);
        await page.getByRole("searchbox", { name: "Find in file" }).fill("logs");
        await fits(page);
        await shot(page, "config-file-edited");
        await page.getByRole("button", { name: "Review and save", exact: true }).click();
        await expect(page.getByRole("dialog", { name: "Review changes" })).toBeVisible();
        await fits(page);
        await shot(page, "config-review");
        await page.getByRole("button", { name: "Cancel", exact: true }).click();
        await page.getByRole("button", { name: "Refresh", exact: true }).click();
        await shot(page, "config-refresh-ask");
        await page.getByRole("button", { name: "Cancel", exact: true }).click();
        await tab(page, "Settings").click();
        await expect(leaveQuestion(page)).toBeVisible();
        await shot(page, "config-leave-ask");
        await page.getByRole("button", { name: "Stay", exact: true }).click();
        await page.getByRole("button", { name: "Discard edits", exact: true }).click();
        await editor(page).fill("{");
        await page.getByRole("button", { name: "Check for mistakes", exact: true }).click();
        await expect(page.getByRole("alert")).toContainText("There is a mistake");
        await fits(page);
        await shot(page, "config-mistake");
      });

      test(`Config's loading and failed states fit: ${where}`, async ({ page }) => {
        // A file that does not parse, and a newer one pluxy refused.
        await page.route("**/v0/management/config", unparsed);
        await page.route("**/v0/management/status", async (route) => {
          const response = await route.fetch();
          const status = await response.json();
          status.desired = {
            ...status.active,
            revision: status.active.revision + 1,
            error: "listen: port in use",
          };
          await route.fulfill({ response, json: status });
        });
        await open(page);
        await expect(page.getByText("Fix the settings file first.").first()).toBeVisible();
        await fits(page);
        await shot(page, "config-broken-settings");
        await tab(page, "Settings file").click();
        await fits(page);
        await shot(page, "config-broken-file");
        await page.unroute("**/v0/management/config");
        await page.unroute("**/v0/management/status");
        // pluxy does not answer GET /settings, then answers late.
        await page.route("**/v0/management/settings", (route) =>
          route.request().method() === "GET" ? route.abort("failed") : route.continue(),
        );
        await page.reload();
        await expect(pageTitle(page)).toHaveText("Config");
        await tab(page, "Settings").click();
        await expect(page.getByText("Could not read pluxy's settings")).toBeVisible();
        await fits(page);
        await shot(page, "config-settings-failed");
        await page.unroute("**/v0/management/settings");
        await hold(page, "/settings");
        await hold(page, "/config");
        await page.reload();
        await expect(page.getByText("Loading settings…")).toBeVisible();
        await page.waitForTimeout(300); // the placeholder lines show after 150 ms
        await shot(page, "config-loading");
        await tab(page, "Settings file").click();
        await expect(page.getByText("Loading the settings file…")).toBeVisible();
        await page.waitForTimeout(300);
        await shot(page, "config-file-loading");
      });
    }
  }
}
