import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

test("Add Account with no provider says a provider comes first and links to Set up pluxy", async ({
  page,
}) => {
  await page.route("**/v0/management/login-methods", (route) => route.fulfill({ json: { extensions: [] } }));
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Add Account");
  await expect(page.getByText("No running extension offers a sign-in method.")).toBeVisible();
  await expect(page.getByText("Install an extension first")).toBeVisible();
  await page.getByRole("main").getByRole("link", { name: "Set up pluxy", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Set up pluxy" })).toBeVisible();
});

test("the Dashboard with no account links to Add Account", async ({ page }) => {
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { accounts: [] } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  const health = page.getByRole("region", { name: "Credential status", exact: true });
  await expect(health.getByText("No credentials yet.")).toBeVisible();
  await health.getByRole("link", { name: "Add Account", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add Account" })).toBeVisible();
});

test("the Dashboard's not-serving count opens Accounts, also when every account is out", async ({ page }) => {
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const out = { source: "login", refreshable: false, extension: "test/alpha" };
    await route.fulfill({
      json: {
        accounts: [
          { ...out, id: "a", state: "active", health: { state: "banned" } },
          { ...out, id: "b", state: "reauth_required" },
        ],
      },
    });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await page.getByRole("link", { name: "2 not serving", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
});
