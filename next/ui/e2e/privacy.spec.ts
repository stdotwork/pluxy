// Privacy mode: the eye toggle in the navigation masks account emails and identifiers where the
// panel shows them (labels, tooltips and accessible names, log views), keeps the choice in the
// browser across a reload, tells extension pages through the theme event, and shows everything
// again when the person turns it off. Search keeps matching the real values, and keys are never
// treated as privacy fields: the panel shows them as core does in both states.
import { randomUUID } from "node:crypto";
import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import type { ClientKey } from "../kit/api/config";
import { accountRow, goTo, openPanel, pageTitle, signIn, useCore } from "./support";

const core = useCore();

// Hostile identities: `$&` pasted the whole identity back in when a mask used a string
// replacement, and an identity starting with `@` looped forever.
const AMP = "$&abcdefghijk-project-77";
const AT = "@x";
const EMAIL = "alice.smith@example.com";
// An account ID holding the e-mail address in a file-name form.
const EMAIL_ID = "alice_smith_example_com";
// The ID core gives an account imported from today's core's claude-bob.jones+work@example.com.json.
const IMPORTED = "bob.jones+work@example.com";
const IMPORTED_ID = "claude-bob.jones-work-example.com";
const IMPORTED_ID_MASKED = "claude-bo***@e***.com";
// Two accounts whose IDs mask the same.
const TWINS = ["aaaa_example_com", "aabb_example_com"];
const TWIN_MASKED = "a***@e***.com";
// The masks, worked out by hand from the old app's rules (`src/services/privacyMask.ts` in
// router-for-me/EasyCLIProxyAPI, https://github.com/router-for-me/EasyCLIProxyAPI):
// opaque values over 10 characters keep 2 at each end, e-mail addresses keep 2 (local part
// over 4 characters) and 1 (domain) before ***, and values of 4 characters or fewer are ***.
const AMP_MASKED = "$&***77";
const AT_MASKED = "***";
const EMAIL_MASKED = "al***@e***.com";
/** What must never be shown, or be in a tooltip or an accessible name, while masking. */
const RAW = ["abcdefghijk", "alice.smith", "alice_smith", "bob.jones", "example.com", AT];

const keyCanary = `privacy-key-${randomUUID()}`;
let trace = "";

async function addAccount(id: string, label: string, priority?: number) {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    priority,
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
  await addAccount(IMPORTED_ID, IMPORTED);
  for (const id of TWINS) await addAccount(id, id.replace("_example_com", "@example.com"), 900);
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
  // biome-ignore lint/performance/useDomNodeTextContent: the rendered text is what the person sees
  `${await page.locator("body").innerText()}\n${(await names(page)).join("\n")}`;

async function expectMasked(page: Page) {
  const all = await shown(page);
  for (const raw of RAW) expect(all, `"${raw}" is shown while masking`).not.toContain(raw);
}

async function accountRows(page: Page) {
  await goTo(page, "Accounts");
  await expect(page.locator('[data-part="account-row"]').first()).toBeVisible();
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
  await accountRows(page);
  // A row names an account by its label: the ID of an account that has one shows nowhere.
  await expect(accountRow(page, "privacy-amp")).toContainText(AMP);
  await expect(accountRow(page, "privacy-at")).toContainText(AT);
  await expect(accountRow(page, EMAIL_ID)).toContainText(EMAIL);
  const before = await shown(page);
  for (const raw of RAW.filter((r) => r !== "alice_smith")) expect(before).toContain(raw);
  expect((await names(page)).some((n) => n.includes(EMAIL))).toBe(true);

  // On.
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "true");
  await expect(toggle(page)).toHaveAttribute("title", "Turn off privacy mode to show full account details");
  await expect(accountRow(page, "privacy-amp")).toContainText(AMP_MASKED);
  await expect(accountRow(page, "privacy-at")).toContainText(AT_MASKED);
  await expect(
    accountRow(page, EMAIL_ID).getByRole("link", { name: EMAIL_MASKED, exact: true }),
  ).toBeVisible();
  await expectMasked(page);
  // Tooltips and accessible names are masked too: the row's checkbox and the account's own page.
  await page.getByRole("button", { name: "Select", exact: true }).click();
  await expect(page.getByRole("checkbox", { name: `Select ${EMAIL_MASKED}`, exact: true })).toBeVisible();
  await expectMasked(page);
  await page.getByRole("button", { name: "Done", exact: true }).click();
  await accountRow(page, EMAIL_ID).getByRole("link", { name: EMAIL_MASKED, exact: true }).click();
  await expect(pageTitle(page)).toHaveText(EMAIL_MASKED);
  await expectMasked(page);
  // Search still matches the real values.
  await goTo(page, "Accounts");
  await page.getByPlaceholder("Find an account").fill("alice.smith");
  await expect(page.locator('[data-part="account-row"]')).toHaveCount(1);
  await page.getByPlaceholder("Find an account").fill("");

  // Log views: the core log's lines and an error request log.
  await page.goto(`${core.base}/panel/#/logs`);
  const coreLog = page.getByRole("region", { name: "Core logs", exact: true });
  await expect(coreLog).toContainText(`account "${EMAIL_MASKED}" added`);
  const errors = page.getByRole("region", { name: "Error request logs", exact: true });
  await errors
    .locator('[data-part="item"]')
    .filter({ hasText: trace })
    .getByRole("button", { name: "Open", exact: true })
    .click();
  const detail = page.getByRole("complementary", { name: "Details: Error request log details" });
  await expect(detail).toContainText(`ask ${EMAIL_MASKED} about ${AMP_MASKED}`);
  await expectMasked(page);

  // Routing: two accounts whose masked names are the same keep keyboard focus apart.
  await goTo(page, "Routing");
  const twinDown = page.getByRole("button", { name: `Move down a tier: ${TWIN_MASKED}`, exact: true });
  await expect(twinDown).toHaveCount(2);
  await twinDown.nth(1).focus();
  await page.keyboard.press("Enter");
  await expect
    .poll(async () => {
      const all = (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts;
      return all.filter((a) => TWINS.includes(a.id) && (a.priority ?? 0) < 900).length;
    })
    .toBe(1);
  // Focus follows the account that moved, to its new tier, not the first button of that name.
  await expect
    .poll(() =>
      page.evaluate(() => {
        const lists = [...document.querySelectorAll('[data-part="list"]')];
        const active = document.activeElement;
        return {
          tier: lists.indexOf(active?.closest('[data-part="list"]') as Element),
          name: active?.getAttribute("aria-label"),
        };
      }),
    )
    .toEqual({ tier: 1, name: `Move down a tier: ${TWIN_MASKED}` });
  await expectMasked(page);

  // The System page's models list the accounts that serve them.
  await goTo(page, "System");
  await expect(page.getByText(IMPORTED_ID_MASKED).first()).toBeVisible();
  await expectMasked(page);

  // Keys are not privacy fields: the client key list shows core's own masked form, unchanged.
  const keys = (await core.mgmt<{ keys: ClientKey[] }>("GET", "/client-keys")).body.keys;
  await goTo(page, "Config Panel");
  await page.getByRole("tab", { name: "Connectivity", exact: true }).click();
  const clients = page.getByRole("region", { name: "Client keys (access.api-keys)", exact: true });
  for (const k of keys) await expect(clients.getByText(k.masked, { exact: true })).toBeVisible();

  // An extension page is told, through the theme event, and its page library marks its root.
  await goTo(page, "Alpha usage");
  const frame = page.frameLocator('iframe[title="Alpha usage"]');
  await expect(frame.locator("#state")).toHaveText("connected as test/alpha, page usage");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");

  // A reload keeps it on.
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "true");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");
  await accountRows(page);
  await expect(accountRow(page, "privacy-amp")).toContainText(AMP_MASKED);
  await expectMasked(page);

  // The person turns it off: everything shows again, and the extension page hears it.
  await goTo(page, "Alpha usage");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "on");
  await toggle(page).click();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "false");
  await expect(frame.locator("html")).toHaveAttribute("data-privacy", "off");
  await accountRows(page);
  await expect(accountRow(page, "privacy-amp")).toContainText(AMP);
  await expect(accountRow(page, EMAIL_ID)).toContainText(EMAIL);
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-pressed", "false");
  await goTo(page, "System");
  await expect(page.getByText(IMPORTED_ID).first()).toBeVisible();

  // No key reached the page in either state.
  expect(await page.content()).not.toContain(keyCanary);
  expect(await page.content()).not.toContain(core.key);
});

test("the panel's other tabs follow privacy mode", async ({ page, context }) => {
  await openPanel(page, core);
  await signIn(page, core);
  const other = await context.newPage();
  await openPanel(other, core);
  await signIn(other, core);
  await expect(toggle(other)).toHaveAttribute("aria-pressed", "false");
  await toggle(page).click();
  await expect(toggle(other)).toHaveAttribute("aria-pressed", "true");
  await goTo(other, "Accounts");
  await expect(other.getByRole("link", { name: EMAIL_MASKED, exact: true })).toBeVisible();
  await toggle(page).click();
  await expect(toggle(other)).toHaveAttribute("aria-pressed", "false");
  await expect(other.getByRole("link", { name: EMAIL, exact: true })).toBeVisible();
});

test("the Dashboard hides the upstream proxy's login while privacy mode is on", async ({ page }) => {
  const line = page
    .getByRole("region", { name: "Configuration at a glance" })
    .locator(".facts > div")
    .filter({ hasText: "Upstream proxy" })
    .locator("dd");
  try {
    // Core shows the proxy with "redacted" in place of its login; privacy mode masks that too.
    const set = await core.mgmt<{ "proxy-url": string }>("PATCH", "/settings", {
      "proxy-url": "http://carol:privpass99@127.0.0.1:3128",
    });
    expect(set.status).toBe(200);
    expect(set.body["proxy-url"]).toBe("http://redacted@127.0.0.1:3128");
    await openPanel(page, core);
    await signIn(page, core);
    await goTo(page, "Dashboard");
    await expect(line).toHaveText("http://redacted@127.0.0.1:3128");
    await toggle(page).click();
    await expect(toggle(page)).toHaveAttribute("aria-pressed", "true");
    await expect(line).toHaveText("http://***@127.0.0.1:3128");
    await toggle(page).click();
    await expect(line).toHaveText("http://redacted@127.0.0.1:3128");
  } finally {
    await core.mgmt("PATCH", "/settings", { "proxy-url": "" });
  }
});

test("with privacy mode on, Requests and Logs say that a saved file holds the real text", async ({
  page,
}) => {
  await openPanel(page, core);
  await signIn(page, core);
  const requests = page.getByText("Names are hidden on screen only. The saved file has them.");
  const logs = page.getByText("A downloaded log holds the text as core wrote it.");
  await page.goto(`${core.base}/panel/#/usage`);
  const save = page.getByRole("button", { name: "Save as spreadsheet", exact: true });
  await expect(save).toBeVisible();
  await expect(requests).toHaveCount(0);
  await toggle(page).click();
  // It is said once a file has been saved, not before.
  await expect(requests).toHaveCount(0);
  const download = page.waitForEvent("download");
  await save.click();
  await download;
  await expect(requests).toBeVisible();
  await page.goto(`${core.base}/panel/#/logs`);
  await expect(logs).toBeVisible();
  await toggle(page).click();
  await expect(logs).toHaveCount(0);
});
