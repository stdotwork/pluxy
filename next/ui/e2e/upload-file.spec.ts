// Moving from CLIProxyAPI: its auth files, imported at the end of Add an account.
import { expect, type Page, test } from "@playwright/test";
import type { Account } from "../kit/api/accounts";
import { accountRow, goTo, openPanel, signIn, useCore } from "./support";

const core = useCore();

/** Chooses files with Import files…, as a person would. */
async function upload(page: Page, files: { name: string; mimeType: string; buffer: Buffer }[]) {
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Import files…", exact: true }).click();
  await (await chooser).setFiles(files);
}

const ids = async () =>
  (await core.mgmt<{ accounts: Account[] }>("GET", "/accounts")).body.accounts.map((a) => a.id).sort();

test("Import files… adds an auth file, and refuses one that is not JSON or is too large before sending", async ({
  page,
}) => {
  const before = await ids();
  const imports: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/v0/management/accounts/import"))
      imports.push(r.postDataJSON().name);
  });
  await openPanel(page, core);
  await signIn(page, core);
  await goTo(page, "Add Account");
  await expect(page.getByText("Have auth files from CLIProxyAPI?")).toBeVisible();

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
  await expect(results).toContainText("Added devin-upload (Devin)");
  await expect(results).not.toContainText("not supported yet");
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

  // A file that is not JSON, and one over 1 MB, are refused in the page; one that says .json but
  // is not, by core.
  await upload(page, [
    { name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from("not an auth file") },
    { name: "huge.json", mimeType: "application/json", buffer: Buffer.alloc(2 << 20, 32) },
    { name: "broken.json", mimeType: "application/json", buffer: Buffer.from("{not json") },
  ]);
  await expect(results).toContainText("Choose .json files.");
  await expect(results).toContainText("huge.json is larger than 1 MB.");
  await expect(results).toContainText("Not added: not a JSON object");
  expect(imports).toEqual(["devin-upload.json", "broken.json"]);
  expect(await ids()).toEqual([...before, "devin-upload"].sort());

  // The account is on the list under the name it came with.
  await goTo(page, "Accounts");
  await expect(accountRow(page, "devin-upload")).toContainText("upload-user");
});
