// Extension pages in real browsers (Chromium, Firefox and WebKit), served by a real core with
// fake extensions (next/e2e/panelcore): a hostile page that tries to escape, a benign page that
// works through the channel, and the settings form the panel draws from a schema.
// Run it with `bun run test:browser`, which builds the panel first.
import { type ChildProcess, spawn } from "node:child_process";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { expect, type Frame, type Page, test } from "@playwright/test";

let core: ChildProcess | undefined;
let base = "";
let key = "";

// The canary stands for anywhere outside: it records every request that reaches it.
let canary: Server | undefined;
let canaryBase = "";
const canaryHits: string[] = [];

test.describe.configure({ mode: "serial" });

test.beforeAll(async () => {
  canary = createServer((req: IncomingMessage, res) => {
    canaryHits.push(`${req.method} ${req.url}`);
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.end("canary");
  });
  canary.on("upgrade", (req, socket) => {
    canaryHits.push(`UPGRADE ${req.url}`);
    socket.destroy();
  });
  await new Promise<void>((resolve) => canary?.listen(0, "127.0.0.1", resolve));
  canaryBase = `http://127.0.0.1:${(canary.address() as AddressInfo).port}`;

  core = spawn("go", ["run", "./e2e/panelcore"], {
    cwd: join(import.meta.dirname, "..", ".."),
    stdio: ["pipe", "pipe", "inherit"],
  });
  const exited = new Promise<never>((_, reject) => {
    core?.once("exit", (code) => reject(new Error(`panelcore exited with ${code} before core was ready`)));
  });
  const ready = new Promise<{ base: string; key: string }>((resolve) => {
    if (!core?.stdout) throw new Error("no stdout");
    createInterface({ input: core.stdout }).once("line", (line) => resolve(JSON.parse(line)));
  });
  ({ base, key } = await Promise.race([ready, exited]));
});

test.afterAll(async () => {
  canary?.close();
  if (!core || core.exitCode !== null) return;
  const done = new Promise((resolve) => core?.once("exit", resolve));
  core.stdin?.end(); // panelcore stops core and removes its files when its stdin closes
  await done;
});

/** Calls core's management API from the test, with the key. */
async function mgmt(method: string, path: string, body?: unknown) {
  const r = await fetch(`${base}/v0/management${path}`, {
    method,
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const json = r.headers.get("content-type")?.startsWith("application/json");
  return { status: r.status, body: json ? await r.json() : await r.text() };
}

async function signIn(page: Page, remember = false) {
  await page.goto(`${base}/panel/`);
  await page.getByLabel("Management key").fill(key);
  if (remember) await page.getByLabel("Remember on this device").check();
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Status" })).toBeVisible();
}

const nav = (page: Page) => page.getByRole("navigation", { name: "Main" });

function frameOf(page: Page, id: string): Frame {
  const f = page.frames().find((fr) => fr.url().includes(`/${id}/`));
  if (!f) throw new Error(`no frame of ${id}: ${page.frames().map((fr) => fr.url())}`);
  return f;
}

interface Attack {
  status: "blocked" | "succeeded" | "attempted";
  detail: string;
}

interface Hostile {
  done: boolean;
  error?: string;
  attacks: Record<string, Attack>;
  received: string[];
  allowed?: { theme: boolean; config: Record<string, unknown> | null };
  welcomeKeys?: string[];
  webrtc?: string;
}

// What the hostile page must see fail itself.
const MUST_BE_BLOCKED = [
  "parentDocument",
  "topDocument",
  "parentLocalStorage",
  "parentSessionStorage",
  "parentCookies",
  "parentLocation",
  "opener",
  "ownCookies",
  "ownLocalStorage",
  "ownSessionStorage",
  "ownIndexedDB",
  "ownCacheStorage",
  "managementFetch_omit",
  "managementFetch_include",
  "managementXHR",
  "topNavigation",
  "popup",
  "exfilFetch",
  "exfilFetchNoCors",
  "exfilXHR",
  "exfilWebSocket",
  "exfilEventSource",
  "exfilImport",
  "exfilWorker",
  "exfilFont",
  "configSetWithoutPermission",
  "unknownMethod",
  "keyMethod",
  "otherExtensionConfig",
  "extraField",
  "wrongVersion",
  "oversized",
  "secondHello",
  "flood",
];

// What only the canary can judge: it must receive nothing.
const MUST_NOT_ARRIVE = [
  "exfilBeacon",
  "exfilImage",
  "exfilStylesheet",
  "exfilPrefetch",
  "exfilCssUrl",
  "exfilFrame",
  "exfilForm",
  "exfilAudio",
];

test("a hostile extension page cannot reach the key, the panel, core or the outside", async ({
  page,
  context,
}) => {
  test.setTimeout(90_000);
  const set = await mgmt("PATCH", "/extensions/test/hostile/config", {
    values: { canary: canaryBase, panel: base },
  });
  expect(set.status).toBe(200);
  const before = await mgmt("GET", "/extensions/test/hostile/config");

  // A cookie on the panel's site: it must never reach the pages site.
  await context.addCookies([{ name: "panel_session", value: "FAKE_PANEL_COOKIE", url: base }]);
  const popups: string[] = [];
  context.on("page", (p) => popups.push(p.url()));
  const seen: Promise<{ url: string; main: boolean; cookie?: string; auth?: string }>[] = [];
  page.on("request", (r) => {
    seen.push(
      r.allHeaders().then((h) => {
        let main = true;
        try {
          main = r.frame() === page.mainFrame();
        } catch {
          main = false;
        }
        return { url: r.url(), main, cookie: h.cookie, auth: h.authorization };
      }),
    );
  });

  await signIn(page, true); // remembered: the key is in the panel's localStorage too
  await nav(page).getByRole("link", { name: "Hostile" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Hostile" })).toBeVisible();
  const hostileUrl = page.url();
  const frame = frameOf(page, "test/hostile");
  await expect(frame.locator("#state")).toHaveText("done", { timeout: 60_000 });
  const r = (await frame.evaluate(() => (window as unknown as { __hostile: unknown }).__hostile)) as Hostile;

  expect(r.error).toBeUndefined();
  expect(r.allowed).toEqual({ theme: true, config: { canary: canaryBase, panel: base } });
  expect(r.welcomeKeys).toEqual(["extension", "page", "pluxy", "theme", "type", "v"]);
  for (const name of MUST_BE_BLOCKED) {
    expect(r.attacks[name]?.status, `${name}: ${r.attacks[name]?.detail}`).toBe("blocked");
  }
  for (const name of MUST_NOT_ARRIVE) {
    expect(r.attacks[name]?.status, `${name}: ${r.attacks[name]?.detail}`).not.toBe("succeeded");
  }
  expect(Object.keys(r.attacks).sort()).toEqual([...MUST_BE_BLOCKED, ...MUST_NOT_ARRIVE].sort());
  for (const m of r.received) expect(m).not.toContain(key);

  // The page navigates its own frame to the outside: the panel's policy refuses the address.
  await frame.evaluate((c) => {
    location.href = `${c}/self-navigation`;
  }, canaryBase);
  await page.waitForTimeout(2000); // beacons, images and the navigation have had time to arrive

  expect(canaryHits).toEqual([]);
  expect(page.url()).toBe(hostileUrl);
  expect(popups).toEqual([]);
  const requests = await Promise.all(seen);
  const pagesSite = requests.filter((q) => !q.main);
  expect(pagesSite.length).toBeGreaterThan(0);
  for (const q of pagesSite) {
    expect(new URL(q.url).hostname, q.url).not.toBe(new URL(base).hostname);
    expect(q.cookie, q.url).toBeUndefined();
    expect(q.auth, q.url).toBeUndefined();
  }
  for (const q of requests.filter((x) => x.main && new URL(x.url).pathname.startsWith("/v0/management"))) {
    expect(q.auth, q.url).toBe(`Bearer ${key}`);
  }
  expect(await mgmt("GET", "/extensions/test/hostile/config")).toEqual(before);
  test.info().annotations.push({ type: "webrtc", description: r.webrtc ?? "not tried" });

  // Control: the canary does record what reaches it, so the empty list above means something.
  await fetch(`${canaryBase}/control`);
  expect(canaryHits).toEqual(["GET /control"]);
  canaryHits.length = 0;
});

test("a benign extension page works through the channel and follows the theme", async ({ page }) => {
  expect((await mgmt("PATCH", "/extensions/test/alpha/config", { values: { greeting: "set by the test" } })).status).toBe(
    200,
  );
  await page.emulateMedia({ colorScheme: "light" });
  await signIn(page);
  await nav(page).getByRole("link", { name: "Alpha usage" }).click();
  const f = page.frameLocator('iframe[title="Alpha usage"]');
  await expect(f.locator("#state")).toHaveText("connected as test/alpha, page usage");
  await expect(f.locator("#greeting")).toHaveText("set by the test");
  await expect(f.locator("#appearance")).toHaveText("light");

  const token = (fr: Page | Frame) =>
    fr.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--px-color-bg").trim());
  const frame = frameOf(page, "test/alpha");
  const lightBg = await token(page);
  expect(await token(frame)).toBe(lightBg);

  // The theme changes while the page shows: the page follows.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(f.locator("#appearance")).toHaveText("dark");
  const darkBg = await token(page);
  expect(darkBg).not.toBe(lightBg);
  await expect.poll(() => token(frame)).toBe(darkBg);

  // The page writes its own settings (it declares config.write).
  await f.getByRole("button", { name: "Set the greeting from this page" }).click();
  await expect(f.locator("#greeting")).toHaveText("hello from the page");
  expect((await mgmt("GET", "/extensions/test/alpha/config")).body.values.greeting).toBe("hello from the page");

  // The page loads another document into its frame: the panel closes the channel.
  await frame.evaluate(() => location.reload());
  await expect(page.getByText("closed its connection")).toBeVisible();
  await page.getByRole("button", { name: "Load the page again" }).click();
  await expect(f.locator("#state")).toHaveText("connected as test/alpha, page usage");
});

test("the panel draws an extension's settings form from its schema, and saves through core", async ({
  page,
}) => {
  await signIn(page);
  await nav(page).getByRole("link", { name: "Extensions" }).click();
  await page.getByRole("row", { name: /test\/alpha/ }).click();
  const form = page.getByRole("form", { name: "Settings of test/alpha" });
  await expect(form).toBeVisible();
  const before = (await mgmt("GET", "/extensions/test/alpha/config")).body;

  // Core checks the values against the schema: refused, nothing changes.
  await form.getByLabel("Greeting").fill("from the form");
  await form.getByLabel("Interval (seconds)").fill("5000");
  await form.getByRole("button", { name: "Save settings" }).click();
  await expect(form.getByText(/interval.*at most 3600/)).toBeVisible();
  expect((await mgmt("GET", "/extensions/test/alpha/config")).body).toEqual(before);

  await form.getByLabel("Interval (seconds)").fill("120");
  await form.getByLabel("Unit").selectOption("requests");
  await form.getByLabel("Detailed view").check();
  await form.getByLabel("Export token").fill("fake-export-token");
  await form.getByRole("button", { name: "Save settings" }).click();
  await expect(form.getByText("Saved.")).toBeVisible();
  const saved = (await mgmt("GET", "/extensions/test/alpha/config")).body;
  expect(saved.values).toEqual({ greeting: "from the form", interval: 120, unit: "requests", detailed: true });
  expect(saved.set).toEqual(["token"]);
  expect(JSON.stringify(saved)).not.toContain("fake-export-token");

  // Read back after a reload: the form shows what core keeps, never the secret.
  await page.reload();
  await page.getByRole("row", { name: /test\/alpha/ }).click();
  await expect(form.getByLabel("Greeting")).toHaveValue("from the form");
  await expect(form.getByLabel("Interval (seconds)")).toHaveValue("120");
  await expect(form.getByLabel("Unit")).toHaveValue("requests");
  await expect(form.getByLabel("Detailed view")).toBeChecked();
  await expect(form.getByLabel("Export token")).toHaveValue("");
  await expect(form.getByText("A value is set.")).toBeVisible();

  // Clearing the secret.
  await form.getByRole("checkbox", { name: "Clear Export token" }).check();
  await form.getByRole("button", { name: "Save settings" }).click();
  await expect(form.getByText("Saved.")).toBeVisible();
  expect((await mgmt("GET", "/extensions/test/alpha/config")).body.set).toEqual([]);
});
