// Privacy mode: the eye toggle in the navigation masks account emails and identifiers where the
// panel shows them (labels, tooltips and accessible names, log views), keeps the choice in the
// browser across a reload, tells extension pages through the theme event, and shows everything
// again when the person turns it off. Search keeps matching the real values, and keys are never
// treated as privacy fields: the panel shows them as core does in both states.
import { randomUUID } from "node:crypto";
import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import type { ClientKey } from "../kit/api/config";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

// Hostile identities: `$&` pasted the whole identity back in when a mask used a string
// replacement, and an identity starting with `@` looped forever.
const AMP = "$&abcdefghijk-project-77";
const AT = "@x";
const EMAIL = "alice.smith@example.com";
// An account ID holding the e-mail address in a file-name form.
const EMAIL_ID = "alice_smith_example_com";
// The masks, worked out by hand from the old app's rules (extensions/app privacyMask.ts):
// opaque values over 10 characters keep 2 at each end, e-mail addresses keep 2 (local part
// over 4 characters) and 1 (domain) before ***, and values of 4 characters or fewer are ***.
const AMP_MASKED = "$&***77";
const AT_MASKED = "***";
const EMAIL_MASKED = "al***@e***.com";
/** What must never be shown, or be in a tooltip or an accessible name, while masking. */
const RAW = ["abcdefghijk", "alice.smith", "alice_smith", "example.com", AT];

const keyCanary = `privacy-key-${randomUUID()}`;
let trace = "";

async function addAccount(id: string, label: string) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/local",
    method: "api_key",
    input: { api_key: keyCanary, label },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(async () => {
      const r = await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`);
      return r.body.login.state;
    })
    .toBe("done");
}

test.beforeAll(async () => {
  expect(
    (await core.mgmt("PATCH", "/settings", { observability: { logs: { "logging-to-file": true } } })).status,
  ).toBe(200);
  await addAccount("privacy-amp", AMP);
  await addAccount("privacy-at", AT);
  await addAccount(EMAIL_ID, EMAIL);
  const accounts = (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts;
  expect(accounts.find((a) => a.id === "privacy-amp")?.label).toBe(AMP);
  expect(accounts.find((a) => a.id === "privacy-at")?.label).toBe(AT);
  expect(accounts.find((a) => a.id === EMAIL_ID)?.label).toBe(EMAIL);
  // A failed request whose error log holds the identities.
  const failed = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      model: "server-error",
      messages: [{ role: "user", content: `ask ${EMAIL} about ${AMP}` }],
    }),
  });
  expect(failed.status).toBe(500);
  trace = failed.headers.get("X-CPA-TRACE-ID") as string;
  expect(trace).toBeTruthy();
  await failed.text();
});

const toggle = (page: Page) => page.getByRole("button", { name: "Privacy mode", exact: true });

/** Every tooltip and accessible name on the page. */
const names = (page: Page) =>
  page.evaluate(() =>
    [...document.querySelectorAll("[title], [aria-label]")].flatMap((el) =>
      ["title", "aria-label"].map((a) => el.getAttribute(a) ?? ""),
    ),
  );

const shown = async (page: Page) =>
  `${await page.locator("body").innerText()}\n${(await names(page)).join("\n")}`;

async function expectMasked(page: Page) {
  const all = await shown(page);
  for (const raw of RAW) expect(all, `"${raw}" is shown while masking`).not.toContain(raw);
}

async function accountRows(page: Page) {
  await goTo(page, "Accounts");
  const list = page.locator('[data-part="list"]');
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-amp" })).toBeVisible();
  return list;
}

test("privacy mode masks identities, survives a reload, reaches extension pages and reveals again", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);

  // Off: everything shows as it is (the positive control for every check below).
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "false");
  await expect(toggle(page)).toHaveAttribute(
    "title",
    "Turn on privacy mode to hide account emails and identifiers",
  );
  let list = await accountRows(page);
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-amp" })).toContainText(AMP);
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-at" })).toContainText(AT);
  await expect(list.locator('[data-part="item"]').filter({ hasText: EMAIL_ID })).toContainText(EMAIL);
  const before = await shown(page);
  for (const raw of RAW) expect(before).toContain(raw);
  expect((await names(page)).some((n) => n.includes(EMAIL_ID))).toBe(true);

  // On.
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "true");
  await expect(toggle(page)).toHaveAttribute("title", "Turn off privacy mode to show full account details");
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-amp" })).toContainText(
    AMP_MASKED,
  );
  const at = list.locator('[data-part="item"]').filter({ hasText: "privacy-at" });
  await expect(at).toContainText(AT_MASKED);
  await expect(list.locator('[data-part="item"]').filter({ hasText: EMAIL_MASKED })).toHaveCount(1);
  await expectMasked(page);
  // Tooltips and accessible names are masked too: the row's checkbox and its details.
  await expect(page.getByRole("checkbox", { name: `Select ${EMAIL_MASKED}`, exact: true })).toBeVisible();
  await list.getByRole("button", { name: EMAIL_MASKED, exact: true }).click();
  await expect(
    page.getByRole("complementary", { name: `Details: ${EMAIL_MASKED}`, exact: true }),
  ).toBeVisible();
  await expectMasked(page);
  // Search still matches the real values.
  await page.getByPlaceholder(/^Search name, account/).fill("alice.smith");
  await expect(list.locator('[data-part="item"]')).toHaveCount(1);
  await page.getByPlaceholder(/^Search name, account/).fill("");

  // Log views: the core log's lines and an error request log.
  await goTo(page, "Logs Viewer");
  const coreLog = page.getByRole("region", { name: "Core logs", exact: true });
  await expect(coreLog).toContainText(`account "${EMAIL_MASKED}" added`);
  const errors = page.getByRole("region", { name: "Error Request Logs", exact: true });
  await errors
    .locator('[data-part="item"]')
    .filter({ hasText: trace })
    .getByRole("button", { name: "Open", exact: true })
    .click();
  const detail = page.getByRole("complementary", { name: "Details: Error Request Log Details" });
  await expect(detail).toContainText(`ask ${EMAIL_MASKED} about ${AMP_MASKED}`);
  await expectMasked(page);

  // Keys are not privacy fields: the client key list shows core's own masked form, unchanged.
  const keys = (await core.mgmt<{ keys: ClientKey[] }>("GET", "/client-keys")).body.keys;
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
  const clients = page.getByRole("region", { name: "Client API Keys (access.api-keys)", exact: true });
  for (const k of keys) await expect(clients.getByText(k.masked, { exact: true })).toBeVisible();

  // An extension page is told, through the theme event, and its page library marks its root.
  await nav(page).getByRole("link", { name: "Alpha usage" }).click();
  const frame = page.frameLocator('iframe[title="Alpha usage"]');
  await expect(frame.locator("#state")).toHaveText("connected as test/alpha, page usage");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");

  // A reload keeps it on.
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "true");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");
  list = await accountRows(page);
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-amp" })).toContainText(
    AMP_MASKED,
  );
  await expectMasked(page);

  // The person turns it off: everything shows again, and the extension page hears it.
  await nav(page).getByRole("link", { name: "Alpha usage" }).click();
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "false");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "off");
  list = await accountRows(page);
  await expect(list.locator('[data-part="item"]').filter({ hasText: "privacy-amp" })).toContainText(AMP);
  await expect(list.locator('[data-part="item"]').filter({ hasText: EMAIL_ID })).toContainText(EMAIL);
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "false");

  // No key reached the page in either state.
  expect(await page.content()).not.toContain(keyCanary);
  expect(await page.content()).not.toContain(core.key);
});
