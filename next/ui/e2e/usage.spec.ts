import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

test("filters recorded requests and exports the matching rows", async ({ page }) => {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: "usage-account",
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "test-usage" },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", "/logins/" + started.body.login.id)).body.login
          .state,
    )
    .toBe("done");
  const traces: string[] = [];
  for (const model of ["fake-model", "server-error"]) {
    const response = await fetch(core.base + "/v1/chat/completions", {
      method: "POST",
      headers: { Authorization: "Bearer " + core.clientKey, "Content-Type": "application/json" },
      body: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }),
    });
    expect(response.status).toBe(model === "fake-model" ? 200 : 500);
    const trace = response.headers.get("X-CPA-TRACE-ID");
    expect(trace).toBeTruthy();
    if (!trace) throw new Error("missing trace ID");
    traces.push(trace);
    await response.text();
  }
  await openPanel(page, core);
  await signIn(page, core);
  await nav(page).getByRole("link", { name: "Usage", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Usage", exact: true })).toBeVisible();
  const requests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/usage/events?")) requests.push(request.url());
  });
  await expect(page.getByLabel("Since", { exact: true })).toHaveAttribute("placeholder", "e.g. 24h or 2026-01-01T00:00:00Z");
  await page.getByLabel("Since", { exact: true }).fill("invalid");
  await expect(page.getByRole("alert")).toContainText("since");
  await page.getByLabel("Since", { exact: true }).fill("");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.locator('[data-page="usage"] [data-part="item"]')).toHaveCount(2);
  requests.length = 0;
  await page.getByLabel("Model", { exact: true }).pressSequentially("abc", { delay: 50 });
  await expect.poll(() => requests.length).toBe(1);
  expect(new URL(requests[0] ?? "http://invalid").searchParams.get("model")).toBe("abc");
  await page.getByLabel("Model", { exact: true }).fill("fake-model");
  const rows = page.locator('[data-page="usage"] [data-part="item"]');
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("fake-model");
  await expect(rows.first().getByRole("link", { name: traces[0] })).toHaveAttribute(
    "href",
    "#/logs/" + traces[0],
  );
  if (process.env.USAGE_SHOTS) {
    for (const theme of ["default", "terminal"]) {
      await nav(page).getByRole("link", { name: "Themes", exact: true }).click();
      await page.getByRole("radio", { name: new RegExp("^" + theme, "i") }).check();
      await page.locator('nav a[href="#/usage"]').click();
      await expect(rows).toHaveCount(2);
      await page.screenshot({ path: process.env.USAGE_SHOTS + "/" + theme + "-rows.png", fullPage: true });
      await page.getByLabel("Model", { exact: true }).fill("fake-model");
      await expect(rows).toHaveCount(1);
      await page.screenshot({ path: process.env.USAGE_SHOTS + "/" + theme + "-filter.png", fullPage: true });
    }
  }
  await page.getByLabel("Model", { exact: true }).fill("");
  await page.getByLabel("Failed", { exact: true }).selectOption("true");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("server-error");
  await expect(rows.first()).toContainText(traces[1] ?? "missing trace");
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export", exact: true }).click();
  const csv = await readFile(await (await downloadPromise).path(), "utf8");
  expect(csv).toContain("server-error");
  expect(csv).toContain(traces[1]);
  expect(csv).not.toContain("fake-model");
  expect(csv.trim().split(/\r?\n/)).toHaveLength(2);
});
