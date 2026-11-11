import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

/** Chooses files with the Accounts toolbar's Upload File, as a person would. */
async function upload(page: Page, files: { name: string; mimeType: string; buffer: Buffer }[]) {
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Upload File", exact: true }).click();
  await (await chooser).setFiles(files);
}

const ids = async () =>
  (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.map((a) => a.id).sort();

test("Upload File imports an auth file and refuses a file that is not JSON", async ({ page }) => {
  const before = await ids();
  await openPanel(page, core);
  await signIn(page, core);
  await nav(page).locator('a[href="#/accounts"]').click();
  await expect(page.getByRole("heading", { name: /Accounts$/, level: 1 })).toBeVisible();

  // An auth file of today's core, of a type the import takes.
  const file = {
    type: "devin",
    session_token: "fake-devin-upload-token",
    user_name: "upload-user",
    proxy_url: "http://proxy.example:8080",
  };
  await upload(page, [
    { name: "devin-upload.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(file)) },
  ]);
  const results = page.locator('[data-part="upload-results"]');
  // The file's proxy comes along: nothing is reported as left out.
  await expect(results).toContainText(
    "devin-upload.json: File uploaded successfully: account devin-upload (pluxy/devin)",
  );
  await expect(results).not.toContainText("not supported yet");
  await expect(page.getByRole("button", { name: "devin-upload", exact: true })).toBeVisible();
  const added = (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.find(
    (a) => a.id === "devin-upload",
  );
  expect(added).toMatchObject({
    source: "login",
    extension: "pluxy/devin",
    kind: "api_key",
    label: "upload-user",
    proxy_url: "http://proxy.example:8080",
  });
  if (process.env.P7_SHOTS)
    await page.screenshot({ path: `${process.env.P7_SHOTS}/upload-ok.png`, fullPage: true });

  // A file that is not JSON is refused in the panel; one that says .json but is not, by core.
  await upload(page, [
    { name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from("not an auth file") },
    { name: "broken.json", mimeType: "application/json", buffer: Buffer.from("{not json") },
  ]);
  await expect(results).toContainText("notes.txt: Only JSON files are allowed");
  await expect(results).toContainText("broken.json: not a JSON object");
  if (process.env.P7_SHOTS)
    await page.screenshot({ path: `${process.env.P7_SHOTS}/upload-refused.png`, fullPage: true });
  expect(await ids()).toEqual([...before, "devin-upload"].sort());
});
