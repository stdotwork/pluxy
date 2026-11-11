import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

let trace: string;
let account = 0;
test.beforeEach(async () => {
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": true } } })).status,
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

test("logs show failures, trace lookup, continuous reading and clearing", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Logs Viewer");
  const coreLog = page.getByRole("region", { name: "Core logs", exact: true });
  await expect(coreLog.locator('[data-part="log-line"]').first()).toBeVisible();
  const errors = page.getByRole("region", { name: "Error Request Logs", exact: true });
  const row = errors.locator('[data-part="item"]').filter({ hasText: trace });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const detail = page.getByRole("complementary", { name: "Details: Error Request Log Details" });
  await expect(detail).toContainText("=== REQUEST INFO ===");
  await expect(detail).toContainText("log test <b>plain text</b>");
  expect(await detail.locator("b").count()).toBe(0);
  await page.getByLabel("Request ID", { exact: true }).fill(trace);
  await page.getByRole("button", { name: "View log", exact: true }).click();
  const requestLog = page.getByRole("region", { name: `View log for request ${trace}`, exact: true });
  await expect(requestLog).toContainText("=== REQUEST INFO ===");
  await expect(requestLog).toContainText(`Trace ID: ${trace}`);
  await page.evaluate((id) => {
    location.hash = `#/logs/${id}`;
  }, trace);
  await expect(page.getByLabel("Request ID", { exact: true })).toHaveValue(trace);
  await expect(requestLog).toContainText("=== REQUEST INFO ===");
  await goTo(page, "Quota Management");
  await page.evaluate((id) => {
    location.hash = `#/logs/${id}`;
  }, trace);
  await expect(requestLog).toContainText(`Trace ID: ${trace}`);
  await expect(page.locator('nav a[href="#/logs"]')).toHaveAttribute("aria-current", "page");
  await page.getByRole("switch", { name: "Continuous reading", exact: true }).check();
  const before = await coreLog.textContent();
  expect((await core.mgmt("POST", "/reload")).status).toBe(200);
  await expect.poll(() => coreLog.textContent()).not.toBe(before);
  await expect(coreLog).toContainText("config revision");
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
  await page.getByRole("switch", { name: "Continuous reading", exact: true }).uncheck();
  await page.getByRole("button", { name: "Clear Logs", exact: true }).click();
  await expect(
    page.getByText("Are you sure you want to clear all logs? This action cannot be undone!", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Yes, clear logs", exact: true }).click();
  await expect(coreLog.locator('[data-part="log-line"]')).toHaveCount(0);
  expect((await core.mgmt<{ lines: string[] }>("GET", "/logs")).body.lines).toEqual([]);
});

test("filters, downloads and logging-off guidance follow the selected theme", async ({ page }) => {
  expect((await core.mgmt("POST", "/reload")).status).toBe(200);
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Logs Viewer");
  const log = page.getByRole("region", { name: "Core logs", exact: true });
  await page.getByLabel("Search logs by content or keyword").fill("config revision");
  await expect(log.locator('[data-part="log-line"]').first()).toContainText("config revision");
  await page.getByLabel("Log level", { exact: true }).selectOption("warn");
  await expect(log.locator('[data-part="log-line"]')).toHaveCount(0);
  await page.getByLabel("Log level", { exact: true }).selectOption("info");
  await expect(log.locator('[data-part="log-line"]').first()).toHaveAttribute("data-level", "info");
  await page.getByRole("switch", { name: "Wrap long lines", exact: true }).check();
  await expect(log).toHaveAttribute("data-wrap", "");
  const expected = (await log.locator('[data-part="log-line"]').allTextContents()).join("\n");
  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download Logs", exact: true }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe("logs.txt");
  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString()).toBe(expected);
  await page.getByLabel("Search logs by content or keyword").fill("");

  for (const theme of ["default", "terminal"]) {
    expect(
      (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": true } } }))
        .status,
    ).toBe(200);
    await goTo(page, "Themes");
    await page.getByRole("radio", { name: new RegExp(`^${theme}`, "i") }).check();
    await page.locator('nav a[href="#/logs"]').click();
    await expect(page.getByRole("heading", { level: 1, name: "Logs Viewer" })).toBeVisible();
    await expect(log.locator('[data-part="log-line"]').first()).toBeVisible();
    if (process.env.LOGS_SHOTS)
      await page.screenshot({ path: `${process.env.LOGS_SHOTS}/${theme}-lines.png`, fullPage: true });
    const errors = page.getByRole("region", { name: "Error Request Logs", exact: true });
    await errors.getByRole("button", { name: "Open", exact: true }).first().click();
    const detail = page.getByRole("complementary", { name: "Details: Error Request Log Details" });
    await expect(detail).toContainText("=== REQUEST INFO ===");
    if (process.env.LOGS_SHOTS)
      await page.screenshot({ path: `${process.env.LOGS_SHOTS}/${theme}-detail.png`, fullPage: true });
    const fileContent = await detail.locator('[data-part="log"]').textContent();
    const fileDownload = page.waitForEvent("download");
    await detail.getByRole("button", { name: "Download", exact: true }).click();
    const fileStream = await (await fileDownload).createReadStream();
    const fileChunks: Buffer[] = [];
    for await (const chunk of fileStream) fileChunks.push(chunk);
    expect(Buffer.concat(fileChunks).toString()).toBe(fileContent);
    await detail.getByRole("button", { name: "Close details" }).click();
    expect(
      (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": false } } }))
        .status,
    ).toBe(200);
    await page.getByRole("button", { name: "Refresh Logs", exact: true }).click();
    await expect(page.getByText(/Logging to file is off/)).toBeVisible();
    await expect(page.getByRole("link", { name: "Config Panel — Logging", exact: true })).toHaveAttribute(
      "href",
      "#/config",
    );
    if (process.env.LOGS_SHOTS)
      await page.screenshot({ path: `${process.env.LOGS_SHOTS}/${theme}-off.png`, fullPage: true });
    await page.getByRole("link", { name: "Config Panel — Logging", exact: true }).click();
    await page.getByRole("tab", { name: "Logging", exact: true }).click();
    await expect(page.getByRole("switch", { name: "Log to File", exact: true })).not.toBeChecked();
  }
});
