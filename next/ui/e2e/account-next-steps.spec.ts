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

test("Home with no account links to Add Account", async ({ page }) => {
  await page.route("**/v0/management/accounts", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { accounts: [] } });
  });
  await openPanel(page, core);
  await signIn(page, core);
  await expect(page.getByRole("heading", { level: 2, name: "pluxy has no accounts yet." })).toBeVisible();
  await page.getByRole("link", { name: "Add an account", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Add Account" })).toBeVisible();
});

test("Home with every account out says so and leads to each fix", async ({ page }) => {
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
  await expect(page.getByRole("heading", { level: 2, name: "2 accounts need you." })).toBeVisible();
  await expect(page.getByRole("region", { name: "Serving", exact: true })).toHaveCount(0);
  // Each account that needs the person has its fix: turning a closed one on, signing the other in again.
  await expect(page.getByRole("button", { name: "Turn on: a", exact: true })).toBeVisible();
  const signInAgain = page.getByRole("link", { name: "Sign in again: b", exact: true });
  await expect(signInAgain).toHaveAttribute("href", "#/accounts/b/sign-in");
  await signInAgain.click();
  await expect(page.getByRole("heading", { level: 1, name: "Accounts" })).toBeVisible();
});
