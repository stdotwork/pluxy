// The panel in a real browser, served by a real core with fake extensions (next/e2e/panelcore).
// Run it with `bun run test:browser`, which builds the panel first.
import { mkdirSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import { goTo, nav, openPanel, signIn, useCore } from "./support";

const core = useCore();

interface Seen {
  url: string;
  auth?: string;
}

/** Opens the panel, records every request the page makes and every CSP report or page error. */
async function open(page: Page, path = "/panel/") {
  const requests: Seen[] = [];
  const problems: string[] = [];
  page.on("request", (r) => {
    requests.push({ url: r.url(), auth: r.headers().authorization });
  });
  page.on("pageerror", (e) => {
    problems.push(`page error: ${e.message}`);
  });
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  await openPanel(page, core, path);
  return { requests, problems };
}

const root = (page: Page) => page.locator("html");

test("panel shows core, switches layout and theme live, and recovers from custom CSS", async ({ page }) => {
  const { requests, problems } = await open(page);
  await signIn(page, core);

  // Shared pages appear in their navigation groups.
  await expect(
    nav(page).getByRole("group", { name: "Operate" }).getByRole("link", { name: "Dashboard" }),
  ).toBeVisible();
  await expect(
    nav(page).getByRole("group", { name: "Control" }).getByRole("link", { name: "Themes" }),
  ).toBeVisible();

  // Dashboard: the extensions core runs, from its status.
  const running = page.getByRole("table", { name: "Running extensions" });
  await expect(running.getByText("test/local", { exact: true })).toBeVisible();
  await expect(running.getByText("test/alpha", { exact: true })).toBeVisible();

  // Accounts: config accounts of both extensions.
  await goTo(page, "Accounts");
  const accounts = page.locator('[data-part="list"]');
  for (const id of ["local-1", "alpha-1", "alpha-2"]) {
    await expect(accounts.locator('[data-part="item"]', { hasText: id })).toBeVisible();
  }

  // Extensions: the installed packages, one enabled and running, one disabled.
  await goTo(page, "Extensions");
  const packages = page.locator('[data-part="list"][aria-label="Installed extensions"]');
  const alpha = packages.locator('[data-part="item"]', { hasText: "test/alpha" });
  const beta = packages.locator('[data-part="item"]', { hasText: "test/beta" });
  await expect(alpha).toBeVisible();
  await expect(alpha.locator('[data-part="badge"]')).toHaveText("active");
  await expect(beta.locator('[data-part="badge"]')).toHaveText("disabled");

  // Themes: switching a layout option applies at once, without reloading the page.
  await goTo(page, "Themes");
  await page.evaluate(() => {
    (window as unknown as { marker: number }).marker = 1;
  });
  await expect(root(page)).toHaveAttribute("data-nav", "sidebar");
  const sideBox = await nav(page).boundingBox();
  expect(sideBox && sideBox.height > sideBox.width).toBe(true);

  await page.getByRole("radiogroup", { name: "Navigation" }).getByRole("radio", { name: "Top" }).check();
  await expect(root(page)).toHaveAttribute("data-nav", "top");
  const topBox = await nav(page).boundingBox();
  expect(topBox && topBox.width > topBox.height && topBox.y < 10).toBe(true);
  expect(await page.evaluate(() => (window as unknown as { marker?: number }).marker)).toBe(1);

  await page.getByRole("radiogroup", { name: "Lists" }).getByRole("radio", { name: "Cards" }).check();
  await expect(root(page)).toHaveAttribute("data-list", "cards");

  // Another theme changes the tokens; the person's layout choice stays.
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const before = await bg();
  await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Terminal" }).check();
  await expect.poll(bg).not.toBe(before);
  await expect(root(page)).toHaveAttribute("data-nav", "top");

  // Custom CSS applies live; safe mode turns it off and on again.
  const title = page.locator('[data-part="page-title"]');
  const titleColor = () => title.evaluate((e) => getComputedStyle(e).color);
  await page.getByLabel("Your CSS").fill('[data-part="page-title"] { color: rgb(255, 0, 0); }');
  await expect.poll(titleColor).toBe("rgb(255, 0, 0)");
  const safe = page.getByRole("switch", { name: "Safe mode" });
  await safe.check();
  await expect.poll(titleColor).not.toBe("rgb(255, 0, 0)");
  await safe.uncheck();
  await expect.poll(titleColor).toBe("rgb(255, 0, 0)");

  // Custom CSS cannot load anything from elsewhere: the CSP refuses it, so nothing is sent.
  // A canary server on another origin counts what reaches it.
  let reached = 0;
  const canary = createServer((_, res) => {
    reached++;
    res.end();
  });
  await new Promise<void>((resolve) => canary.listen(0, "127.0.0.1", resolve));
  const outside = `http://127.0.0.1:${(canary.address() as AddressInfo).port}/leak.png`;
  await page.getByLabel("Your CSS").fill(`[data-part="main"] { background-image: url(${outside}); }`);
  await expect
    .poll(() => problems.some((p) => p.includes("Content Security Policy") && p.includes(outside)))
    .toBe(true);
  const refused = problems.filter((p) => p.includes(outside));
  problems.splice(0, problems.length, ...problems.filter((p) => !p.includes(outside)));
  expect(refused.length).toBeGreaterThan(0);
  expect(reached).toBe(0);
  canary.close();

  // CSS that hides the navigation: ?safe in the address brings it back, and safe mode stays on.
  await page.getByLabel("Your CSS").fill('[data-part="nav"] { display: none; }');
  await expect(nav(page)).toBeHidden();
  await openPanel(page, core, "/panel/?safe");
  await expect(nav(page)).toBeVisible();
  await page.reload();
  await expect(nav(page)).toBeVisible();
  await goTo(page, "Themes");
  await expect(page.getByRole("switch", { name: "Safe mode" })).toBeChecked();

  // The management key went to core's own management API and nowhere else, and the page
  // reached no other origin.
  const keyed = requests.filter((r) => r.auth !== undefined);
  expect(keyed.length).toBeGreaterThan(0);
  for (const r of keyed) {
    expect(r.url.startsWith(`${core.base}/v0/management/`), r.url).toBe(true);
    expect(r.auth).toBe(`Bearer ${core.key}`);
  }
  for (const r of requests) {
    if (r.url !== outside) expect(new URL(r.url).origin, r.url).toBe(core.base);
  }
  expect(requests.some((r) => r.url.includes(core.key))).toBe(false);
  expect(problems).toEqual([]);
});

test("themes are kept by core: added from a file and from pasted JSON, followed, exported, removed", async ({
  page,
}) => {
  const fixtures = join(import.meta.dirname, "..", "tests", "fixtures");
  const nightfall = join(fixtures, "t3-docs-nightfall.json");
  const dawn = readFileSync(join(fixtures, "t3-short-variants-css.json"), "utf8");
  interface Themes {
    current: string;
    themes: { id: string; name: string }[];
  }
  const stored = async () => (await core.mgmt<Themes>("GET", "/themes")).body;
  const variable = (name: string) =>
    page.evaluate((n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim(), name);
  const picker = page.getByRole("radiogroup", { name: "Theme" });

  const { problems } = await open(page);
  await signIn(page, core);
  await goTo(page, "Themes");
  await expect(picker.getByRole("radio", { name: "Default" })).toBeChecked();
  const defaultBg = await variable("--px-color-bg");
  expect(await variable("--app-theme-canvas")).toBe("");

  // A T3 Code theme file, chosen as a file: it applies at once and core keeps it.
  await page.getByLabel("Theme file", { exact: true }).setInputFiles(nightfall);
  await expect(page.getByText("Added and applied Nightfall.")).toBeVisible();
  await expect(picker.getByRole("radio", { name: "Nightfall" })).toBeChecked();
  await expect.poll(() => variable("--px-color-bg")).not.toBe(defaultBg);
  const nightfallBg = await variable("--px-color-bg");
  expect(await variable("--app-theme-canvas")).toBe(nightfallBg);
  expect(await stored()).toEqual({
    current: "t3/nightfall",
    themes: [expect.objectContaining({ id: "t3/nightfall", name: "Nightfall" })],
  });

  // Pasted JSON goes the same way.
  const paste = page.getByLabel("Or paste a theme file's JSON");
  const add = page.getByRole("button", { name: "Add", exact: true });
  await expect(add).toBeDisabled();
  await paste.fill(dawn);
  await add.click();
  await expect(picker.getByRole("radio", { name: "Dawn and Dusk" })).toBeChecked();
  await expect(paste).toHaveValue("");
  await expect.poll(() => variable("--px-color-bg")).not.toBe(nightfallBg);
  const dawnBg = await variable("--px-color-bg");
  expect((await stored()).current).toBe("t3/dawn-and-dusk");

  // JSON the reader refuses says why in place, stays in the box, and never reaches core.
  await paste.fill('{"name": "No colours", "appearance": "dark"}');
  await add.click();
  await expect(page.getByRole("alert").filter({ hasText: "Not added:" })).toBeVisible();
  await expect(paste).toHaveValue(/No colours/);
  expect((await stored()).themes.map((t) => t.id)).toEqual(["t3/dawn-and-dusk", "t3/nightfall"]);
  expect(await variable("--px-color-bg")).toBe(dawnBg);

  // After a reload the theme is still on.
  await page.reload();
  await expect(nav(page)).toBeVisible();
  await expect.poll(() => variable("--px-color-bg")).toBe(dawnBg);
  await goTo(page, "Themes");
  await expect(picker.getByRole("radio", { name: "Dawn and Dusk" })).toBeChecked();

  // A change made elsewhere (another window, `pluxy theme set`) is followed when the window gets focus.
  await core.mgmt("PUT", "/theme", { id: "t3/nightfall" });
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(picker.getByRole("radio", { name: "Nightfall" })).toBeChecked();
  await expect.poll(() => variable("--px-color-bg")).toBe(nightfallBg);

  // Export saves the theme's file as it was given.
  const saving = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export Nightfall as a theme file" }).click();
  const saved = await saving;
  expect(saved.suggestedFilename()).toBe("nightfall.json");
  expect(JSON.parse(readFileSync(await saved.path(), "utf8"))).toEqual(
    JSON.parse(readFileSync(nightfall, "utf8")),
  );

  // Removing the theme that is on asks first, then goes back to the default.
  await page.getByRole("button", { name: "Remove Nightfall" }).click();
  await page.getByRole("button", { name: "Yes, remove Nightfall", exact: true }).click();
  await expect(picker.getByRole("radio", { name: "Nightfall" })).toHaveCount(0);
  await expect(picker.getByRole("radio", { name: "Default" })).toBeChecked();
  await expect.poll(() => variable("--px-color-bg")).toBe(defaultBg);
  expect(await stored()).toEqual({
    current: "",
    themes: [expect.objectContaining({ id: "t3/dawn-and-dusk" })],
  });

  await picker.getByRole("radio", { name: "Dawn and Dusk" }).check();
  await page.getByRole("button", { name: "Remove Dawn and Dusk" }).click();
  await page.getByRole("button", { name: "Yes, remove Dawn and Dusk", exact: true }).click();
  await expect(picker.getByRole("radio")).toHaveCount(3);
  expect((await stored()).themes).toEqual([]);
  expect(problems).toEqual([]);
});

test("the Themes page says what is still true, and what to do about a file it cannot use", async ({
  page,
}) => {
  const fixtures = join(import.meta.dirname, "..", "tests", "fixtures");
  const picker = page.getByRole("radiogroup", { name: "Theme" });
  const paste = page.getByLabel("Or paste a theme file's JSON");
  const add = page.getByRole("button", { name: "Add", exact: true });
  const alerts = page.getByRole("alert");
  const ids = async () =>
    (await core.mgmt<{ themes: { id: string }[] }>("GET", "/themes")).body.themes.map((t) => t.id);

  const { problems } = await open(page);
  await signIn(page, core);
  await goTo(page, "Themes");

  // Something that is not a theme: the reason says what a theme file has.
  await paste.fill('{"foo": 1}');
  await add.click();
  await expect(alerts).toHaveText(
    /^Not added: the file is not a theme file\. A pluxy theme file has “theme_version”/,
  );
  // The notice goes when the box is edited, and when a theme is picked.
  await paste.pressSequentially(" ");
  await expect(alerts).toHaveCount(0);
  await add.click();
  await expect(alerts).toHaveCount(1);
  await picker.getByRole("radio", { name: "Terminal" }).check();
  await expect(alerts).toHaveCount(0);

  // A built-in theme's own file, as Export saves it: the reason says which field to change.
  await paste.fill(readFileSync(join(import.meta.dirname, "..", "themes", "default.json"), "utf8"));
  await add.click();
  await expect(alerts).toHaveText(
    'Not added: "id" is pluxy/default, a built-in theme\'s ID. Change "id" in the file to your own, such as my/default, and add it again',
  );

  // "Added" goes too once it is no longer the theme that is on.
  await page
    .getByLabel("Theme file", { exact: true })
    .setInputFiles(join(fixtures, "t3-docs-nightfall.json"));
  const added = page.getByText("Added and applied Nightfall.");
  await expect(added).toBeVisible();
  await expect(alerts).toHaveCount(0);
  await picker.getByRole("radio", { name: "Default" }).check();
  await expect(added).toHaveCount(0);

  // A second file with the same name and a different description: it gets the same ID, so it takes
  // the first one's place, and the notice says so.
  const nightfallFile = JSON.parse(readFileSync(join(fixtures, "t3-docs-nightfall.json"), "utf8"));
  const changed = JSON.stringify({ ...nightfallFile, description: "A second file with this name" });
  const secondFile = { name: "nightfall.json", mimeType: "application/json", buffer: Buffer.from(changed) };
  await page.getByLabel("Theme file", { exact: true }).setInputFiles(secondFile);
  await expect(page.getByText("Added and applied Nightfall, replacing the existing theme.")).toBeVisible();
  // The very same file again takes no theme's place.
  await page.getByLabel("Theme file", { exact: true }).setInputFiles(secondFile);
  await expect(page.getByText("Added and applied Nightfall.", { exact: true })).toBeVisible();

  // A theme removed somewhere else since this page read the list: removing it here just works.
  await picker.getByRole("radio", { name: "Nightfall" }).check();
  await core.mgmt("DELETE", "/themes/t3/nightfall");
  await page.getByRole("button", { name: "Remove Nightfall" }).click();
  await page.getByRole("button", { name: "Yes, remove Nightfall", exact: true }).click();
  await expect(picker.getByRole("radio", { name: "Nightfall" })).toHaveCount(0);
  await expect(picker.getByRole("radio", { name: "Default" })).toBeChecked();
  await expect(alerts).toHaveCount(0);

  // A stored file the page cannot use (`pluxy theme add` stores what it is given), turned on from
  // elsewhere: the page says the file is what is wrong, keeps its theme, and offers to remove it.
  expect((await core.mgmt("PUT", "/themes/me/broken", { name: "Broken Thing" })).status).toBe(200);
  await core.mgmt("PUT", "/theme", { id: "me/broken" });
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(alerts).toHaveText(
    /^me\/broken is turned on, but this page cannot use it: the file is not a theme file\..* Remove it, or pick another theme\.$/,
  );
  const broken = picker.locator('[data-part="card"][data-unusable]');
  await expect(broken).toContainText("Broken Thing");
  await expect(broken).toContainText("Cannot be used: the file is not a theme file.");
  await expect(picker.getByRole("radio", { name: "Default" })).toBeChecked();
  await broken.getByRole("button", { name: "Remove Broken Thing" }).click();
  // The question keeps the name as written.
  await broken.getByRole("button", { name: "Yes, remove Broken Thing", exact: true }).click();
  await expect(broken).toHaveCount(0);
  await expect(alerts).toHaveCount(0);
  expect(await ids()).toEqual([]);
  expect((await core.mgmt<{ current: string }>("GET", "/themes")).body.current).toBe("");
  // The browser's own line for core's 404 to removing the theme that was already gone.
  expect(problems).toEqual([
    "console: Failed to load resource: the server responded with a status of 404 (Not Found)",
  ]);
});

test("in a window shorter than the side navigation, the navigation scrolls to its last entry", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1100, height: 320 });
  await open(page);
  await signIn(page, core);
  const bar = nav(page);
  const last = bar.getByRole("link").last();
  // The navigation is taller than the window: its last entry starts out of view.
  expect(await bar.evaluate((n) => n.scrollHeight > n.clientHeight)).toBe(true);
  await expect(last).not.toBeInViewport();
  // The wheel over the navigation reaches it, on a page too short to scroll itself.
  await bar.hover({ position: { x: 20, y: 20 } });
  await page.mouse.wheel(0, 4000);
  await expect(last).toBeInViewport({ ratio: 0.95 });
});

test("with the navigation on top in a narrow window, the current page's entry is in view and the bar shows there is more", async ({
  page,
}) => {
  await page.setViewportSize({ width: 700, height: 800 });
  await open(page);
  await signIn(page, core);
  const bar = nav(page);
  const themes = bar.getByRole("link", { name: "Themes", exact: true });
  // The bar is wider than the window: its last entries start out of view.
  expect(await bar.evaluate((n) => n.scrollWidth > n.clientWidth)).toBe(true);
  await expect(themes).not.toBeInViewport();
  // Opening the page by its address, not by a click on the entry (which would scroll to it).
  await page.evaluate(() => {
    location.hash = "#/themes";
  });
  await expect(page.getByRole("heading", { name: "Themes", level: 1 })).toBeVisible();
  await expect(themes).toBeInViewport({ ratio: 0.95 });
  // The cue at the edges: shadows that stay put while the entries scroll under them.
  await expect(bar).toHaveCSS("background-attachment", /local.*scroll/);
  await bar.getByRole("link", { name: "Dashboard", exact: true }).click();
  await expect(bar.getByRole("link", { name: "Dashboard", exact: true })).toBeInViewport({ ratio: 0.95 });
});

test("a change of page opens the new page at its top; a change inside a page keeps the place", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 250 });
  await open(page);
  await signIn(page, core);
  const scrolled = () => page.evaluate(() => window.scrollY);
  const scrollDown = async () => {
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    await expect.poll(scrolled).toBeGreaterThan(100);
  };

  // A tab of the Config page is in the address, and is the same page: the window stays put.
  await page.evaluate(() => {
    location.hash = "#/config";
  });
  await expect(page.getByRole("tab", { name: "Common", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await scrollDown();
  await page.evaluate(() => {
    location.hash = "#/config/network";
  });
  await expect(page.getByRole("tab", { name: "Network", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  // The Network tab is shorter, so the window may move up to its end, but not to its top.
  expect(await scrolled()).toBeGreaterThan(100);

  // Another page, by a navigation entry and by the address: each opens at its top.
  await scrollDown();
  await nav(page).getByRole("link", { name: "Accounts", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Accounts", level: 1 })).toBeInViewport();
  expect(await scrolled()).toBe(0);
  await page.evaluate(() => {
    location.hash = "#/themes";
  });
  await expect(page.getByRole("heading", { name: "Themes", level: 1 })).toBeVisible();
  await scrollDown();
  await page.evaluate(() => {
    location.hash = "#/system";
  });
  await expect(page.getByRole("heading", { name: "System", level: 1 })).toBeInViewport();
  expect(await scrolled()).toBe(0);
});

test("the sign-in page fills the window, says how to open the panel without custom CSS, and the rail's sign-out has its name", async ({
  page,
}) => {
  await open(page);
  // The card is in the middle of the window at its own width, not in the navigation's column.
  const card = page.locator('[data-part="card"]');
  const box = await card.boundingBox();
  if (!box) throw new Error("no sign-in card");
  expect(box.width).toBeGreaterThan(300);
  expect(Math.abs(box.x + box.width / 2 - 640)).toBeLessThan(2);
  await expect(card).toContainText("add ?safe to the URL to disable it.");

  // The rail shows short labels: the sign-out control still has its whole name.
  await signIn(page, core);
  await goTo(page, "Themes");
  await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Terminal" }).check();
  await expect(root(page)).toHaveAttribute("data-nav", "rail");
  const out = nav(page).locator(".signout");
  await expect(out).toHaveAccessibleName("Sign out");
  await expect(out).toHaveAttribute("title", "Sign out");
  await out.click();
  // Signed out in a theme with the navigation on top or in a rail, the card is in the middle too.
  const again = await card.boundingBox();
  if (!again) throw new Error("no sign-in card");
  expect(Math.abs(again.x + again.width / 2 - 640)).toBeLessThan(2);
  expect(Math.abs(again.y + again.height / 2 - 400)).toBeLessThan(2);
});

test("what an add came to is brought into view, and a theme from a file leaves no pasted text", async ({
  page,
}) => {
  const nightfall = join(import.meta.dirname, "..", "tests", "fixtures", "t3-docs-nightfall.json");
  const paste = page.getByLabel("Or paste a theme file's JSON");
  const add = page.getByRole("button", { name: "Add", exact: true });
  await page.setViewportSize({ width: 1280, height: 560 });
  await open(page);
  await signIn(page, core);
  await goTo(page, "Themes");

  // The Add button at the window's lower edge, as after scrolling down to it: the reason shows
  // under it, outside the window unless the page brings it in.
  await paste.fill('{"foo": 1}');
  await add.evaluate((b) => window.scrollBy(0, b.getBoundingClientRect().bottom - window.innerHeight + 4));
  await add.click();
  await expect(page.getByRole("alert")).toContainText("Not added:");
  await expect(page.getByRole("alert")).toBeInViewport({ ratio: 0.95 });

  // A theme then added from a file: the refused text does not stay in the box.
  await page.getByLabel("Theme file", { exact: true }).setInputFiles(nightfall);
  await expect(page.getByText("Added and applied Nightfall.")).toBeInViewport({ ratio: 0.95 });
  await expect(paste).toHaveValue("");
  await page.getByRole("button", { name: "Remove Nightfall", exact: true }).click();
  await page.getByRole("button", { name: "Yes, remove Nightfall", exact: true }).click();
  await expect(
    page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Default" }),
  ).toBeChecked();
});

test("screenshots in two themes", async ({ page }) => {
  const dir = process.env.PANEL_SHOTS_DIR;
  test.skip(!dir, "set PANEL_SHOTS_DIR to take screenshots");
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await open(page);
  await signIn(page, core);
  for (const [theme, file] of [
    ["Default", "panel-default.png"],
    ["Terminal", "panel-terminal.png"],
  ] as const) {
    await goTo(page, "Themes");
    await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: theme }).check();
    await goTo(page, "Accounts");
    await page.locator('[data-part="item"]', { hasText: "alpha-1" }).click();
    await expect(page.locator('[data-part="detail"]')).toBeVisible();
    await page.screenshot({ path: join(dir, file), fullPage: true, animations: "disabled" });
  }
  await goTo(page, "Themes");
  await page.screenshot({ path: join(dir, "panel-themes.png"), fullPage: true, animations: "disabled" });
});
