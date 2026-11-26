// One account's own page (#/accounts/<id>): its opening, the models it serves and a test request,
// Clear cooldown, renewing and signing in again, and the way to the settings file.
import { expect, type Locator, type Page, test } from "@playwright/test";
import { accountRow, goTo, openAccount, openPanel, pageTitle, signIn, useCore } from "./support";

const core = useCore();

async function addAccount(id: string, method = "api_key") {
  const r = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id,
    extension: "test/alpha",
    method,
    // Named after its ID: the test provider calls every signed-in account "Test account".
    input: { api_key: "ak-Rm5Tq8Wz3Lc6Vj9", label: id },
  });
  expect(r.status).toBe(202);
  if (method === "browser") {
    // The provider's device sign-in finishes on a fixed 3-second timer, which a 5-second poll
    // can miss on a slow machine. A browser sign-in finishes when it is given the address.
    const sent = await core.mgmt("POST", `/logins/${r.body.login.id}/input`, {
      input: { redirect_url: "http://localhost/callback?code=ok" },
    });
    expect(sent.status).toBe(204);
  }
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${r.body.login.id}`)).body.login.state,
    )
    .toBe("done");
}

async function noSplitWords(part: Locator) {
  const split = await part.evaluate((root) => {
    const words: string[] = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      for (const match of (node.textContent ?? "").matchAll(/\S+/g)) {
        const range = document.createRange();
        range.setStart(node, match.index);
        range.setEnd(node, match.index + match[0].length);
        if (range.getClientRects().length > 1) words.push(match[0]);
      }
    }
    return words;
  });
  expect(split).toEqual([]);
}

const opening = (page: Page) => page.locator('[data-part="account-opening"]');
const more = async (page: Page) => {
  await page.getByText("Settings, models and a test request", { exact: true }).click();
  return page.locator('[data-part="account-more"]');
};

test("an account's page: a model cooling down, the models it serves, a test request and Clear cooldown", async ({
  page,
}) => {
  await addAccount("tools-key");
  const request = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "rate-limited", messages: [{ role: "user", content: "Hi" }] }),
  });
  expect(request.status).toBe(429);
  await openPanel(page, core);
  await signIn(page, core);
  await openAccount(page, "tools-key");
  await expect(page.locator('[data-part="header"]')).toContainText("Accounts/tools-key");
  await expect(opening(page)).toContainText("Cooling down on rate-limited, back in");
  // The facts: how it signs in, its place in the order, its public ID.
  const about = page.getByRole("region", { name: "About" });
  await expect(about).toContainText("an API key");
  await expect(
    about.getByRole("link", { name: /^(First among Alpha accounts|Backup \d+)$/ }),
  ).toHaveAttribute("href", "#/routing");
  await expect(about).toContainText(/Known to tools as\s*acc_[0-9a-f]{8}/);
  const tools = await more(page);
  const models = tools.locator('[data-part="account-models"]');
  for (const id of ["fake-model", "rate-limited", "server-error"]) await expect(models).toContainText(id);
  await noSplitWords(models);
  await expect(tools).toContainText("Uses one request.");
  await tools.getByRole("button", { name: "Send a test request", exact: true }).click();
  const result = tools.locator('[data-part="account-test-result"]');
  await expect(result).toContainText("Succeeded");
  await expect(result).toContainText("v=0.2.0 pid=");
  await expect(result).toContainText(/[0-9]+ ms/);
  // Clear cooldown asks nothing: nothing is lost.
  await opening(page).getByRole("button", { name: "Clear cooldown", exact: true }).click();
  await expect(opening(page)).toContainText("Serving.");
  const r = await core.mgmt<{ accounts: { id: string; cooldowns?: unknown[] }[] }>("GET", "/accounts");
  expect(r.body.accounts.find((a) => a.id === "tools-key")?.cooldowns ?? []).toEqual([]);
  await tools.getByLabel("Test with").selectOption("server-error");
  await tools.getByRole("button", { name: "Send a test request", exact: true }).click();
  await expect(result).toContainText("Provider error");
  await expect(result).toContainText("the test provider failed this request");
});

test("limits show on an account's row and on its page", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Accounts");
  const row = accountRow(page, "alpha-1");
  await expect(row.locator('[data-part="limits"]')).toHaveAttribute("data-state", /^(read|none)$/);
  await row.getByRole("link", { name: "alpha-1", exact: true }).click();
  const limits = page.getByRole("region", { name: "Limits" });
  await expect(limits.locator('[data-part="limits"]')).toHaveAttribute("data-state", /^(read|none)$/);
  await expect(limits).toContainText(/Read at \d\d:\d\d/);
  // Refresh reads the limits from core again, not from what the page read before.
  const read = page.waitForRequest(
    (r) => r.method() === "GET" && new URL(r.url()).pathname.endsWith("/accounts/alpha-1/quota"),
  );
  await limits.getByRole("button", { name: "Refresh", exact: true }).click();
  await read;
  await expect(limits).toContainText(/Read at \d\d:\d\d/);
});

test("an account from the settings file leads to it; a signed-in one renews and signs in again", async ({
  page,
}) => {
  await addAccount("tools-oauth", "browser");
  await openPanel(page, core);
  await signIn(page, core);
  await openAccount(page, "alpha-1");
  await expect(page.getByRole("main")).toContainText("Set in pluxy's settings file.");
  await expect(page.getByRole("link", { name: "Open the settings file" })).toHaveAttribute(
    "href",
    "#/config/source?account=alpha-1",
  );
  for (const label of [
    "Rename",
    "Turn off",
    "Remove…",
    "Edit settings",
    "Renew sign-in now",
    "Sign in again",
  ])
    await expect(page.getByRole("button", { name: label, exact: true })).toHaveCount(0);
  const alpha = await more(page);
  await expect(alpha.locator('[data-part="account-models"]')).toContainText("alpha-model");

  await openAccount(page, "tools-oauth");
  const tools = await more(page);
  await tools.getByRole("button", { name: "Renew sign-in now", exact: true }).click();
  await expect(tools.getByRole("status")).toContainText("Signed in again.");
  await expect(page.getByRole("link", { name: "Sign in again", exact: true })).toHaveCount(0);
  // The fake has no auth-failure model. Supply core's reauth state at the HTTP edge;
  // sign-in still goes through the real core and provider.
  await page.route("**/v0/management/accounts", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    for (const account of body.accounts) if (account.id === "tools-oauth") account.state = "reauth_required";
    await route.fulfill({ response, json: body });
  });
  await page.reload();
  await expect(opening(page)).toContainText("Alpha no longer accepts its sign-in.");
  // One Sign in again, in the opening.
  await expect(page.getByRole("link", { name: "Sign in again", exact: true })).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Sign in again", exact: true })).toHaveCount(0);
  await opening(page).getByRole("link", { name: "Sign in again", exact: true }).click();
  await expect(page).toHaveURL(/#\/accounts\/tools-oauth\/sign-in$/);
  await expect(pageTitle(page)).toHaveText("Test account");
  const signInPart = page.locator('[data-part="account-sign-in"]');
  await signInPart.getByRole("button", { name: "Test browser", exact: true }).click();
  await signInPart.getByLabel("Address the browser ended on").fill("http://localhost/callback?code=ok");
  await signInPart.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("main").getByText("Signed in again.", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/#\/accounts\/tools-oauth$/);
});

test("Open the settings file opens the Source tab with the account's ID selected", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await openAccount(page, "alpha-2");
  await page.getByRole("link", { name: "Open the settings file" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Config Panel" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Source", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page).toHaveURL(/#\/config\/source\?account=alpha-2$/);
  const editor = page.getByRole("textbox", { name: "Configuration source", exact: true });
  await expect(editor).not.toHaveValue("");
  const value = await editor.inputValue();
  const start = value.indexOf('"alpha-2"') + 1;
  expect(start).toBeGreaterThan(0);
  const cursor = () =>
    editor.evaluate((e: HTMLTextAreaElement) => ({
      start: e.selectionStart,
      end: e.selectionEnd,
      scrollLeft: e.scrollLeft,
    }));
  await expect.poll(cursor).toMatchObject({ start, end: start + "alpha-2".length });
  await expect(editor).toBeFocused();
  // Core writes its config on one line, so the ID is far to the right: the editor shows it.
  expect((await cursor()).scrollLeft).toBeGreaterThan(0);
  await expect(page.getByText(/No entry matches/)).toHaveCount(0);
});

test("Open the settings file still opens Source when the file does not hold the account's ID", async ({
  page,
}) => {
  // An account of a provider key section (claude-api-key, ...) has an ID made from its place.
  await page.route("**/v0/management/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.content = body.content.replaceAll("alpha-2", "renamed");
    await route.fulfill({ response, json: body });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await openAccount(page, "alpha-2");
  await page.getByRole("link", { name: "Open the settings file" }).click();
  await expect(page.getByRole("tab", { name: "Source", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByRole("textbox", { name: "Configuration source", exact: true })).not.toHaveValue("");
  await expect(page.getByText(/No entry matches alpha-2/)).toBeVisible();
});

test("an address of an account pluxy does not have says so and leads back", async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
  await page.goto(`${core.base}/panel/#/accounts/acc_00000000`);
  await expect(page.getByText("This account is not in pluxy.")).toBeVisible();
  await page.getByRole("link", { name: "Back to accounts", exact: true }).click();
  await expect(page).toHaveURL(/#\/accounts$/);
});
