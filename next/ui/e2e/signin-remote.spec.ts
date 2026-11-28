// Signing in to a core that answers only its own machine, as a caller it cannot take as this
// machine: the panel says what core said and what to change. Core counts a call from loopback, or
// from the very address it arrived at, as this machine, so the page cannot get here by opening
// core at this machine's own address. Instead every request the page sends names another client in
// X-Forwarded-For, as a proxy forwarding a caller from elsewhere does, and core's real gate
// refuses it (core: internal/server/manage.go, internal/server/clientip.go).
import { expect, test } from "@playwright/test";
import { useCore } from "./support";

const core = useCore();

test.use({ extraHTTPHeaders: { "X-Forwarded-For": "203.0.113.9" } });

test("sign-in from another address says that remote management is off and what to change", async ({
  page,
}) => {
  await page.goto(`${core.base}/panel/`);
  await page.getByLabel("Management key").fill(core.key);
  await page.getByRole("button", { name: "Sign in" }).click();

  const message = page.getByRole("alert");
  await expect(message).toContainText("This pluxy answers only the computer it runs on");
  await expect(message).toContainText('set "management_allow_remote": true in pluxy\'s config.json');
  await expect(message).not.toContainText("The reply from pluxy was 403");
  // Still on the sign-in page: nothing was signed in.
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});
