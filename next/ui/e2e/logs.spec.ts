import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

test("logs show failures, trace lookup, continuous reading and clearing", async ({ page }) => {
  const failed = await fetch(core.base + "/v1/chat/completions", {
    method: "POST",
    headers: { Authorization: "Bearer " + core.clientKey, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "server-error", messages: [{ role: "user", content: "log test <b>plain text</b>" }] }),
  });
  expect(failed.status).toBe(500);
  const trace = failed.headers.get("X-CPA-TRACE-ID") as string;
  expect(trace).toBeTruthy();
  await failed.text();
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
  const requestLog = page.getByRole("region", { name: "View log for request " + trace, exact: true });
  await expect(requestLog).toContainText("=== REQUEST INFO ===");
  await expect(requestLog).toContainText("Trace ID: " + trace);
  await page.evaluate((id) => { location.hash = "#/logs/" + id; }, trace);
  await expect(page.getByLabel("Request ID", { exact: true })).toHaveValue(trace);
  await expect(requestLog).toContainText("=== REQUEST INFO ===");
  await goTo(page, "Quota Management");
  await page.evaluate((id) => { location.hash = "#/logs/" + id; }, trace);
  await expect(requestLog).toContainText("Trace ID: " + trace);
  await expect(page.locator('nav a[href="#/logs"]')).toHaveAttribute("aria-current", "page");
  await page.getByRole("switch", { name: "Continuous reading", exact: true }).check();
  const before = await coreLog.textContent();
  expect((await core.mgmt("POST", "/reload")).status).toBe(200);
  await expect.poll(() => coreLog.textContent()).not.toBe(before);
  await expect(coreLog).toContainText("config revision");
  await page.getByRole("switch", { name: "Continuous reading", exact: true }).uncheck();
  await page.getByRole("button", { name: "Clear Logs", exact: true }).click();
  await expect(page.getByText("Are you sure you want to clear all logs? This action cannot be undone!", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Yes, clear logs", exact: true }).click();
  await expect(coreLog.locator('[data-part="log-line"]')).toHaveCount(0);
  expect((await core.mgmt<{ lines: string[] }>("GET", "/logs")).body.lines).toEqual([]);
});
