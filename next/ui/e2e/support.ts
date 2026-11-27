// What the panel's page specs share: a real core with fake extensions (next/e2e/panelcore, with
// next/e2e/testext), signing in with its management key, moving between pages and calling its
// management API.
import { type ChildProcess, spawn } from "node:child_process";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { expect, type Locator, type Page, test } from "@playwright/test";

/** A running core. Its fields are filled once the spec's `beforeAll` has started it. */
export interface Core {
  /** Core's address, with no trailing slash. */
  base: string;
  /** The management key. */
  key: string;
  /** A client key, for requests to the client API. */
  clientKey: string;
  /** Core's config file. Its folder holds the home folder a firstRun core runs with (`home`). */
  config: string;
  /** Calls the management API (`/v0/management` + path) with the management key. */
  mgmt<T = unknown>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }>;
}

/**
 * Starts a core for the spec file that calls it, before its tests, and stops it after them.
 * Call it once at the top of the file. panelcore builds core from this checkout, so it embeds the
 * panel just built (`bun run test:browser` builds it first).
 *
 * firstRun starts core as a new install: nothing installed, no accounts, a home folder of its own
 * and only a claude program on its PATH (panelcore's -first-run). cpa, with firstRun, puts a
 * CLIProxyAPI setup of made-up accounts in that home folder (panelcore's -cpa).
 */
export function useCore(options: { firstRun?: boolean; cpa?: boolean } = {}): Core {
  let proc: ChildProcess | undefined;
  const core: Core = {
    base: "",
    key: "",
    clientKey: "",
    config: "",
    async mgmt<T>(method: string, path: string, body?: unknown) {
      const r = await fetch(`${core.base}/v0/management${path}`, {
        method,
        headers: { Authorization: `Bearer ${core.key}`, "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const json = r.headers.get("content-type")?.startsWith("application/json");
      return { status: r.status, body: (json ? await r.json() : await r.text()) as T };
    },
  };

  test.beforeAll(async () => {
    const flags = [...(options.firstRun ? ["-first-run"] : []), ...(options.cpa ? ["-cpa"] : [])];
    const p = spawn("go", ["run", "./e2e/panelcore", ...flags], {
      cwd: join(import.meta.dirname, "..", ".."),
      stdio: ["pipe", "pipe", "inherit"],
    });
    proc = p;
    const exited = new Promise<never>((_, reject) => {
      p.once("exit", (code) => reject(new Error(`panelcore exited with ${code} before core was ready`)));
    });
    const ready = new Promise<{ base: string; key: string; client_key: string; config: string }>(
      (resolve) => {
        if (!p.stdout) throw new Error("no stdout");
        createInterface({ input: p.stdout }).once("line", (line) => resolve(JSON.parse(line)));
      },
    );
    const r = await Promise.race([ready, exited]);
    core.base = r.base;
    core.key = r.key;
    core.clientKey = r.client_key;
    core.config = r.config;
  });

  // Core keeps which theme is on, so one test's choice would show in the next test's new browser:
  // each test starts in the default theme.
  test.beforeEach(async () => {
    await core.mgmt("PUT", "/theme", { id: "" });
  });

  test.afterAll(async () => {
    const p = proc;
    if (!p || p.exitCode !== null) return;
    const done = new Promise((resolve) => p.once("exit", resolve));
    p.stdin?.end(); // panelcore stops core and removes its files when its stdin closes
    await done;
  });

  return core;
}

/** The panel's main navigation. */
export const nav = (page: Page) => page.getByRole("navigation", { name: "Main" });

/** Opens the panel's address path (the sign-in page, or the page the person was on). */
export async function openPanel(page: Page, core: Core, path = "/panel/") {
  await page.goto(core.base + path);
}

/** Signs in on the sign-in page with the management key, and waits for the shell. */
export async function signIn(page: Page, core: Core) {
  await page.getByLabel("Management key").fill(core.key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(nav(page)).toBeVisible();
}

/** The tabs a destination shows in the header strip. */
export const headerTabs = (page: Page) => page.locator('[data-part="header-tabs"]');

/** The topics listed beside a Settings page. */
export const topics = (page: Page) => page.locator('[data-part="topics"]');

/** The page's heading, in the header strip. */
export const pageTitle = (page: Page) => page.locator('[data-part="page-title"]');

/**
 * Where the pages that are not a Settings topic sit: the destination to choose, and the tab or
 * link to choose after it. A name that is not listed is a topic under Settings.
 */
const PLACES: Record<string, { dest: string; tab?: string; link?: string }> = {
  Home: { dest: "Home" },
  Accounts: { dest: "Accounts" },
  "Add an account": { dest: "Accounts", link: "Add an account" },
  "Order & fallback": { dest: "Accounts", tab: "Order & fallback" },
  Requests: { dest: "Activity" },
  Usage: { dest: "Activity", tab: "Usage" },
  Tools: { dest: "Tools" },
};

/**
 * Goes to a page by its title, the way a person does: its destination in the navigation, then
 * its tab, its topic or the link that leads to it. Waits for its heading.
 */
export async function goTo(page: Page, name: string) {
  const place = PLACES[name] ?? { dest: "Settings" };
  const destination = nav(page).getByRole("link", { name: place.dest, exact: true });
  if (place.tab) {
    // Already among the destination's tabs: the tab alone, as a person would.
    const tab = headerTabs(page).getByRole("link", { name: place.tab, exact: true });
    if (!(await tab.isVisible())) await destination.click();
    await tab.click();
  } else if (place.link) {
    await destination.click();
    await page.locator('[data-part="header-actions"]').getByRole("link", { name: place.link }).click();
  } else if (place.dest === "Settings") {
    const topic = topics(page).getByRole("link", { name, exact: true });
    // In a narrow window the topics are a select above the page.
    const select = page.locator('[data-part="topics-select"] select');
    if (!(await topic.isVisible()) && !(await select.isVisible())) {
      await destination.click();
      // The topics draw after the destination opens: wait for the list or the select.
      await expect(topic.or(select)).toBeVisible();
    }
    if (await select.isVisible()) await select.selectOption({ label: name });
    else await topic.click();
  } else await destination.click();
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
}

/** The titles of core's pages, by page ID. */
const TITLES: Record<string, string> = {
  status: "Home",
  accounts: "Accounts",
  "add-account": "Add an account",
  routing: "Order & fallback",
  usage: "Requests",
  "activity/usage": "Usage",
  settings: "General",
  setup: "Set up pluxy",
  config: "Config",
  system: "System",
  extensions: "Extensions",
  themes: "Themes",
  logs: "Logs",
  tools: "Tools",
};

/** Goes to one of core's pages by its ID, as goTo does by title. */
export async function goToId(page: Page, id: string) {
  const title = TITLES[id];
  if (!title) throw new Error(`goToId: no core page has the ID ${id}`);
  await goTo(page, title);
}

/**
 * An account's row on All accounts (the shared AccountRow), by the account's ID: its name's link
 * leads to #/accounts/<id>. A row shows the account's name, which is its label when it has one.
 */
export function accountRow(scope: Page | Locator, id: string) {
  const page = "frames" in scope ? scope : scope.page();
  return scope
    .locator('[data-part="account-row"]')
    .filter({ has: page.locator(`a.account-name[href="#/accounts/${encodeURIComponent(id)}"]`) });
}

/** Opens an account's own page from All accounts, the way a person does: by its name's link. */
export async function openAccount(page: Page, id: string) {
  await goTo(page, "Accounts");
  await accountRow(page, id).locator("a.account-name").click();
  await expect(page).toHaveURL(new RegExp(`#/accounts/${encodeURIComponent(id)}$`));
  await expect(page.locator('[data-part="account-page"]')).toBeVisible();
}
