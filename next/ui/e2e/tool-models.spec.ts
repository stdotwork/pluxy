// A coding tool's first request works, or the page says how to make it work: the steps under a
// tool name a model pluxy serves (with a picker), and a request for the tool's own default model,
// which no account serves, tells the person which models are served and leads to the account's
// alias form with the name already filled in.
import { expect, test } from "@playwright/test";
import { goTo, openPanel, signIn, useCore } from "./support";

// panelcore compiles core and the fixture extensions before announcing readiness.
test.setTimeout(300_000);
const core = useCore();
test.use({ trace: "off", screenshot: "off", video: "off" });

const ASKED = "claude-opus-5-5";
const LOGIN = "tool-models-login";

/** An account added here, whose model aliases the panel can change (the demo's own are in the file). */
async function addLoginAccount() {
  const started = await core.mgmt<{ login: { id: string } }>("POST", "/accounts", {
    id: LOGIN,
    models: ["alpha-model"],
    extension: "test/alpha",
    method: "api_key",
    input: { api_key: "tool-models-key", label: LOGIN },
  });
  expect(started.status).toBe(202);
  await expect
    .poll(
      async () =>
        (await core.mgmt<{ login: { state: string } }>("GET", `/logins/${started.body.login.id}`)).body.login
          .state,
    )
    .toBe("done");
}

test.beforeEach(async ({ page }) => {
  await openPanel(page, core);
  await signIn(page, core);
});

test("the steps under a tool name a model pluxy serves, and picking another changes the text", async ({
  page,
}) => {
  const served = await core.mgmt<{ models: { id: string }[] }>("GET", "/models");
  const names = served.body.models.map((m) => m.id);
  expect(names.length).toBeGreaterThan(1);
  await goTo(page, "Tools");
  const claude = page.locator('[data-part="tool-row"]').filter({ hasText: "Claude Code" });
  await claude.getByRole("button", { name: "Set up", exact: true }).click();
  const steps = claude.locator('[data-part="tool-steps"]');
  const picker = steps.getByLabel("Model Claude Code starts with");
  // The demo serves no Claude model, so the best match is the first chat model it serves.
  const chosen = await picker.inputValue();
  expect(names).toContain(chosen);
  await expect(steps.locator("pre")).toContainText(`"model": "${chosen}"`);
  await expect(steps.locator("pre")).toContainText(`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "${chosen}"`);
  await expect(steps.locator("pre")).not.toContainText("<model-id>");
  const other = names.find((n) => n !== chosen && n !== "");
  if (!other) throw new Error("the demo serves one model");
  await picker.selectOption(other);
  await expect(steps.locator("pre")).toContainText(`"model": "${other}"`);
  await expect(steps.locator("pre")).not.toContainText(`"model": "${chosen}"`);
});

test("a request for a model no account serves says what is served and leads to an alias", async ({
  page,
}) => {
  await addLoginAccount();
  const asked = await fetch(`${core.base}/v1/chat/completions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${core.clientKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: ASKED, messages: [{ role: "user", content: "hi" }] }),
  });
  expect(asked.status).toBe(404);
  const body = (await asked.json()) as { error: { message: string } };
  expect(body.error.message).toContain(`no account serves model "${ASKED}". pluxy serves: `);
  expect(body.error.message).toContain("add a model alias to an account");

  await goTo(page, "Requests");
  const request = page.locator(".activity-request").filter({ hasText: ASKED }).first();
  await expect(request).toContainText("Model not served");
  await request.getByRole("button", { expanded: false }).click();
  // The request opens in the side pane beside the list.
  const pane = page.locator('[data-part="detail"]');
  await expect(pane).toContainText(`No account serves ${ASKED}.`);
  await expect(pane).toContainText("These accounts serve:");
  await expect(pane).toContainText("add a model alias to an account");
  await expect(pane.getByRole("link", { name: "Tools", exact: true })).toHaveAttribute("href", "#/tools");

  // An account set in the settings file has no form to add an alias in: it leads to its page.
  await expect(pane.getByRole("link", { name: "Open alpha-1", exact: true })).toBeVisible();
  // The link leads to the account's form with a new alias row that already holds the name.
  await pane.getByRole("link", { name: `Add an alias on ${LOGIN}`, exact: true }).click();
  const form = page.getByRole("dialog", { name: /^Edit /, exact: false });
  const aliases = form.getByRole("group", { name: "Account model aliases", exact: true });
  await expect(aliases.getByLabel("Alias", { exact: true })).toHaveValue(ASKED);
  await expect(aliases.getByLabel("Model name", { exact: true })).toHaveValue("");
  // The one box left to fill has the focus, so a long form shows it.
  await expect(aliases.getByLabel("Model name", { exact: true })).toBeFocused();
  await expect(aliases).toContainText("A request for the Alias goes to the Model name.");
  // Closing it takes the name out of the address, so a reload does not open it again.
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(form).toHaveCount(0);
  expect(page.url()).not.toContain("alias=");
});
