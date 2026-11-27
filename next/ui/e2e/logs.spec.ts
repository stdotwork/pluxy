// Logs: pluxy's log read as it is written, the full logs of failed requests, and a request's log
// opened by its trace ID. Against a real core with fake extensions.
//
// With PANEL_SHOTS_DIR set to a folder, the spec also saves a screenshot of each screen and state
// there (Default, Terminal and Paper, light and dark, wide and small), for a person to look at.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

const THEMES = ["", "pluxy/terminal", "pluxy/paper"] as const; // "" is the default theme
const APPEARANCES = ["light", "dark"] as const;
const SIZES = [
  { width: 1100, height: 760 },
  { width: 640, height: 480 },
] as const;
const SHOTS = process.env.PANEL_SHOTS_DIR;
const UNKNOWN_TRACE = "20200101000000-0123456789abcdef";

let trace: string;
let account = 0;
test.beforeEach(async () => {
  expect(
    (
      await core.mgmt("PATCH", "/settings", {
        observability: { logs: { "logging-to-file": true, "request-log": false } },
      })
    ).status,
  ).toBe(200);
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: `logs-account-${++account}`,
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "logs-test" },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(async () => {
      const r = await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`);
      return r.body.login.state;
    })
    .toBe("done");
  const failed = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      model: "server-error",
      messages: [{ role: "user", content: "log test <b>plain text</b>" }],
    }),
  });
  expect(failed.status).toBe(500);
  trace = failed.headers.get("X-CPA-TRACE-ID") as string;
  expect(trace).toBeTruthy();
  await failed.text();
});

const pluxyLog = (page: Page) => page.getByRole("region", { name: "pluxy's log", exact: true });
const tab = (page: Page, name: string) => page.getByRole("tab", { name, exact: true });

test("pluxy's log follows new lines, filters, downloads and clears", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Logs");
  await expect(tab(page, "pluxy's log")).toHaveAttribute("aria-selected", "true");
  const log = pluxyLog(page);
  await expect(log.locator('[data-part="log-line"]').first()).toBeVisible();
  // The box takes the keyboard, and following and wrapping are on.
  await expect(log).toHaveAttribute("tabindex", "0");
  await log.focus();
  await expect(log).toBeFocused();
  await expect(page.getByRole("switch", { name: "Follow new lines", exact: true })).toBeChecked();
  await expect(page.getByRole("switch", { name: "Wrap lines", exact: true })).toBeChecked();
  await expect(log).toHaveAttribute("data-wrap", "");

  // New lines arrive without a press.
  const before = await log.textContent();
  expect((await core.mgmt("POST", "/reload")).status).toBe(200);
  await expect.poll(() => log.textContent()).not.toBe(before);
  await expect(log).toContainText("config revision");

  // Reading waits while the tab is hidden, and goes on when it shows.
  await page.clock.install();
  await page.evaluate(() =>
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" }),
  );
  const reads: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/management/logs?after=")) reads.push(r.url());
  });
  await page.clock.fastForward(6000);
  expect(reads).toEqual([]);
  await page.evaluate(() =>
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" }),
  );
  await page.clock.fastForward(2000);
  await expect.poll(() => reads.length).toBeGreaterThan(0);
  // Pausing stops the reading.
  await page.getByRole("switch", { name: "Follow new lines", exact: true }).uncheck();
  const paused = reads.length;
  await page.clock.fastForward(6000);
  expect(reads.length).toBe(paused);

  // Show and Find filter the lines that are loaded.
  await expect(page.getByLabel("Show", { exact: true }).locator("option")).toHaveText([
    "All lines",
    "Warnings",
    "Errors",
  ]);
  await page.getByLabel("Find in log").fill("config revision");
  await expect(log.locator('[data-part="log-line"]').first()).toContainText("config revision");
  await page.getByLabel("Show", { exact: true }).selectOption("warn");
  await expect(log.locator('[data-part="log-line"]')).toHaveCount(0);
  await expect(log).toContainText("No lines match.");
  await page.getByLabel("Show", { exact: true }).selectOption("");
  // A line of today shows its time alone and keeps the full date and time as the title: the
  // download has every line in full.
  const expected = (
    await log.locator('[data-part="log-line"]').evaluateAll((lines) =>
      lines.map((line) => {
        const text = line.textContent ?? "";
        const stamp = line.querySelector("time");
        return stamp ? `[${stamp.title}${text.slice(text.indexOf("]"))}` : text;
      }),
    )
  ).join("\n");
  expect(expected).toMatch(/^\[\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d+\] /);
  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download", exact: true }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe("pluxy-log.txt");
  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString()).toBe(expected);
  await page.getByLabel("Find in log").fill("");

  // Clear log asks first, and says what it deletes.
  await page.getByRole("button", { name: "Clear log", exact: true }).click();
  await expect(page.getByText("Deletes pluxy's log. Failed requests stay.", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  expect((await core.mgmt<{ lines: string[] }>("GET", "/logs")).body.lines.length).toBeGreaterThan(0);
  await page.getByRole("button", { name: "Clear log", exact: true }).click();
  await page.getByRole("button", { name: "Yes, clear", exact: true }).click();
  await expect(log.locator('[data-part="log-line"]')).toHaveCount(0);
  expect((await core.mgmt<{ lines: string[] }>("GET", "/logs")).body.lines).toEqual([]);
});

test("a log that is not written to a file leads to the setting, or turns it on", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": false } } })).status,
  ).toBe(200);
  await goTo(page, "Logs");
  await expect(page.getByText("pluxy is not writing its log to a file", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Logging settings", exact: true })).toHaveAttribute(
    "href",
    "#/config/logging",
  );
  // The link opens Config at its Logging section.
  await page.getByRole("link", { name: "Logging settings", exact: true }).click();
  await expect(page.locator("#logging h2")).toBeInViewport();
  await expect(
    page.getByRole("switch", { name: "Write pluxy's log to a file", exact: true }),
  ).not.toBeChecked();
  // One press on the page itself turns it on.
  await goTo(page, "Logs");
  await page.getByRole("button", { name: "Turn on file logging", exact: true }).click();
  await expect(
    page.getByText("Nothing logged yet.").or(pluxyLog(page).locator('[data-part="log-line"]').first()),
  ).toBeVisible();
  const settings = await core.mgmt<{ observability: { logs: { "logging-to-file": boolean } } }>(
    "GET",
    "/settings",
  );
  expect(settings.body.observability.logs["logging-to-file"]).toBe(true);
});

test("failed requests: the list, a request's log, and the log of a trace ID", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Logs");
  await tab(page, "Failed requests").click();
  const row = page.locator('[data-part="item"]').filter({ hasText: trace });
  await expect(row).toContainText("v1-chat-completions");
  await row.getByRole("button", { name: "v1-chat-completions" }).click();
  const detail = page.getByRole("complementary", { name: `Details: ${trace}` });
  await expect(detail).toContainText("=== REQUEST INFO ===");
  await expect(detail).toContainText("log test <b>plain text</b>");
  expect(await detail.locator("b").count()).toBe(0);
  await expect(detail.getByRole("region", { name: `Log of ${trace}` })).toHaveAttribute("tabindex", "0");
  // Download saves the log as it is.
  const text = await detail.locator('[data-part="log"]').textContent();
  const downloaded = page.waitForEvent("download");
  await detail.getByRole("button", { name: "Download", exact: true }).click();
  const stream = await (await downloaded).createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString()).toBe(text);
  // Escape closes the details and gives focus back to the request.
  await detail.focus();
  await page.keyboard.press("Escape");
  await expect(detail).toHaveCount(0);
  await expect(row.getByRole("button", { name: "v1-chat-completions" })).toBeFocused();

  // The field opens a trace ID, and refuses what is not one before asking core.
  await page.getByLabel("Trace ID", { exact: true }).fill("nonsense");
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await expect(page.getByText("That is not a trace ID.", { exact: false })).toBeVisible();
  await page.getByLabel("Trace ID", { exact: true }).fill(trace);
  await page.getByRole("button", { name: "Open", exact: true }).click();
  await expect(detail).toContainText(`Trace ID: ${trace}`);
});

test("a trace ID in the address opens that request's log, with file logging off", async ({ page }) => {
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": false } } })).status,
  ).toBe(200);
  await openPanel(page, core, `/panel/#/logs/${trace}`);
  await signIn(page, core);
  await expect(tab(page, "Failed requests")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByLabel("Trace ID", { exact: true })).toHaveValue(trace);
  await expect(page.getByRole("complementary", { name: `Details: ${trace}` })).toContainText(
    "=== REQUEST INFO ===",
  );
  // From another page of the panel too.
  await goTo(page, "Home");
  await page.evaluate((id) => {
    location.hash = `#/logs/${id}`;
  }, trace);
  await expect(page.getByRole("complementary", { name: `Details: ${trace}` })).toContainText(
    `Trace ID: ${trace}`,
  );
  await expect(page.locator('nav a[href="#/logs"]')).toHaveAttribute("aria-current", "page");
});

test("a trace with no log says what pluxy keeps and where to switch more on", async ({ page }) => {
  await openPanel(page, core, `/panel/#/logs/${UNKNOWN_TRACE}`);
  await signIn(page, core);
  const detail = page.getByRole("complementary", { name: `Details: ${UNKNOWN_TRACE}` });
  await expect(detail).toContainText(
    "No log is kept for this request. pluxy keeps logs of its latest failed requests, and of every request only while Log every request is on.",
  );
  await detail.getByRole("link", { name: "Logging settings", exact: true }).click();
  await expect(page).toHaveURL(/#\/config\/logging$/);
  await expect(page.getByRole("switch", { name: "Log every request", exact: true })).toBeInViewport();
});

test("a request log with long lines scrolls inside its frame", async ({ page }) => {
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "request-log": true } } })).status,
  ).toBe(200);
  const answered = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "fake-model", messages: [{ role: "user", content: "long ".repeat(200) }] }),
  });
  expect(answered.status).toBe(200);
  const id = answered.headers.get("X-CPA-TRACE-ID") as string;
  await answered.text();
  await openPanel(page, core, `/panel/#/logs/${id}`);
  await signIn(page, core);
  await expect(page.getByRole("complementary", { name: `Details: ${id}` })).toContainText(
    "=== REQUEST INFO ===",
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBe(0);
});

for (const theme of THEMES) {
  for (const appearance of APPEARANCES) {
    for (const size of SIZES) {
      const where = `${theme || "pluxy/default"}, ${appearance}, ${size.width}x${size.height}`.replace(
        "pluxy/",
        "",
      );
      const open = async (page: Page, path = "/panel/") => {
        await core.mgmt("PUT", "/theme", { id: theme });
        await page.setViewportSize(size);
        await page.emulateMedia({ colorScheme: appearance });
        await openPanel(page, core, path);
        await signIn(page, core);
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
            ...document.querySelectorAll<HTMLElement>("main .btn, header .btn, main select, main input"),
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

      test(`Logs, following, and failed requests fit: ${where}`, async ({ page }) => {
        await open(page);
        await goTo(page, "Logs");
        await expect(pluxyLog(page).locator('[data-part="log-line"]').first()).toBeVisible();
        await fits(page);
        await shot(page, "logs-following");
        await page.getByRole("button", { name: "Clear log", exact: true }).click();
        await fits(page);
        await shot(page, "logs-clearing");
        await page.getByRole("button", { name: "Cancel", exact: true }).click();
        await tab(page, "Failed requests").click();
        await expect(page.locator('[data-part="item"]').filter({ hasText: trace })).toBeVisible();
        await fits(page);
        await shot(page, "logs-failed");
        await page.locator('[data-part="item"]').filter({ hasText: trace }).getByRole("button").click();
        await expect(page.getByRole("complementary", { name: `Details: ${trace}` })).toContainText(
          "=== REQUEST INFO ===",
        );
        await fits(page);
        await shot(page, "logs-failed-open");
        await page.getByLabel("Trace ID", { exact: true }).fill("nonsense");
        await page.getByRole("button", { name: "Open", exact: true }).click();
        await fits(page);
        await shot(page, "logs-bad-trace");
        await page.getByLabel("Trace ID", { exact: true }).fill(UNKNOWN_TRACE);
        await page.getByRole("button", { name: "Open", exact: true }).click();
        await expect(page.getByText("No log is kept for this request.", { exact: false })).toBeVisible();
        await fits(page);
        await shot(page, "logs-no-log");
        // With names hidden the page says so, on both tabs.
        await goTo(page, "General");
        await page.getByRole("switch", { name: "Hide names" }).check();
        await goTo(page, "Logs");
        await expect(page.getByText("Names are hidden on screen only.", { exact: false })).toBeVisible();
        await fits(page);
        await shot(page, "logs-hidden");
      });

      test(`Logs, file logging off, fits: ${where}`, async ({ page }) => {
        expect(
          (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": false } } }))
            .status,
        ).toBe(200);
        await open(page);
        await goTo(page, "Logs");
        await expect(page.getByRole("button", { name: "Turn on file logging", exact: true })).toBeVisible();
        await fits(page);
        await shot(page, "logs-off");
      });
    }
  }
}
