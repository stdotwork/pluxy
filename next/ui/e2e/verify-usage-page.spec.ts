import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

// Format the rendered time predictably and make the offset case resolve to a different UTC date.
test.use({ locale: "en-US", timezoneId: "UTC" });

const events = [
  {
    seq: 1,
    id: "trace-constructor",
    timestamp: "2026-04-15T03:05:38.856294523-07:00",
    endpoint: "/v1/chat/completions",
    stream: false,
    model: "constructor",
    provider: "test/alpha",
    account: "usage-account",
    failed: false,
    status: 200,
    latency_ms: 12,
    tokens: { total_tokens: 31 },
  },
  {
    seq: 2,
    id: "trace-tostring",
    timestamp: "2026-04-15T10:05:38.987654321Z",
    endpoint: "/v1/chat/completions",
    stream: false,
    model: "toString",
    provider: "test/alpha",
    account: "usage-account",
    failed: false,
    status: 200,
    latency_ms: 13,
    tokens: { total_tokens: 32 },
  },
  {
    seq: 3,
    id: "trace-proto",
    timestamp: "2026-04-16T00:00:00.000000001Z",
    endpoint: "/v1/chat/completions",
    stream: false,
    model: "__proto__",
    provider: "test/alpha",
    account: "usage-account",
    failed: false,
    status: 200,
    latency_ms: 14,
    tokens: { total_tokens: 33 },
  },
];

const at = (i: number): string => events[i]?.timestamp ?? "";

test("usage keeps object-like model names and nanosecond timestamps", async ({ page }) => {
  await page.route("**/v0/management/usage/events?*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ events: [...events].reverse(), next: 3, more: false, lost: false }),
    }),
  );
  await openPanel(page, core);
  await signIn(page, core);
  await nav(page).getByRole("link", { name: "Usage", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Usage", exact: true })).toBeVisible();

  const rows = page.locator('[data-page="usage"] [data-part="item"]');
  await expect(rows).toHaveCount(events.length);
  for (const event of events) await expect(rows.getByText(event.model, { exact: true })).toBeVisible();

  const constructorRow = rows.filter({ has: page.getByText("constructor", { exact: true }) });
  const toStringRow = rows.filter({ has: page.getByText("toString", { exact: true }) });
  const protoRow = rows.filter({ has: page.getByText("__proto__", { exact: true }) });
  await expect(constructorRow.locator("time")).toHaveAttribute("datetime", at(0));
  await expect(constructorRow.locator("time")).toHaveText(/^Apr 15, 10:05:38\sAM$/);
  await expect(toStringRow.locator("time")).toHaveAttribute("datetime", at(1));
  await expect(toStringRow.locator("time")).toHaveText(/^Apr 15, 10:05:38\sAM$/);
  await expect(protoRow.locator("time")).toHaveAttribute("datetime", at(2));
  // The exact local time, with its year, is the title.
  await expect(constructorRow.locator("time")).toHaveAttribute("title", /4\/15\/2026,\s10:05:38\sAM/);
  await expect(protoRow.locator("time")).toHaveText(/^Apr 16, 12:00:00\sAM$/);

  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export", exact: true }).click();
  const csv = await readFile(await (await downloadPromise).path(), "utf8");
  expect(csv.trim().split(/\r?\n/)).toHaveLength(events.length + 1);
  for (const event of events) {
    expect(csv).toContain(event.model);
    expect(csv).toContain(event.id);
  }
});
