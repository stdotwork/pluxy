// Signing in to a core that answers only its own machine, from another address: the panel says
// what core said and what to change. The page is opened at this machine's own network address, so
// core's real gate sees a caller that is not loopback (core: internal/server/manage.go).
import { networkInterfaces } from "node:os";
import { expect, test } from "@playwright/test";
import { useCore } from "./support";

// Core listens on every address; panelcore asks it through 0.0.0.0, which is this machine.
const core = useCore({ listen: "0.0.0.0:0" });

/** An IPv4 address of this machine that is not loopback, or undefined when it has none. */
function networkAddress(): string | undefined {
  for (const list of Object.values(networkInterfaces())) {
    for (const a of list ?? []) if (a.family === "IPv4" && !a.internal) return a.address;
  }
  return undefined;
}

test("sign-in from another address says that remote management is off and what to change", async ({
  page,
}) => {
  const address = networkAddress();
  if (!address) {
    // A CI runner always has one; a machine with no network does not.
    if (process.env.CI) throw new Error("no non-loopback IPv4 address on this machine");
    test.skip(true, "no non-loopback IPv4 address on this machine");
    return;
  }
  const port = new URL(core.base).port;
  await page.goto(`http://${address}:${port}/panel/`);
  await page.getByLabel("Management key").fill(core.key);
  await page.getByRole("button", { name: "Sign in" }).click();

  const message = page.getByRole("alert");
  await expect(message).toContainText("This pluxy answers only the computer it runs on");
  await expect(message).toContainText('set "management_allow_remote": true in pluxy\'s config.json');
  await expect(message).not.toContainText("The reply from pluxy was 403");
  // Still on the sign-in page: nothing was signed in.
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
});
