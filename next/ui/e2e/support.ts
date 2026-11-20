// What the panel's page specs share: a real core with fake extensions (next/e2e/panelcore, with
// next/e2e/testext), signing in with its management key, moving between pages and calling its
// management API.
import { type ChildProcess, spawn } from "node:child_process";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { expect, type Page, test } from "@playwright/test";

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

/** Goes to a page by its navigation entry and waits for its heading. */
export async function goTo(page: Page, name: string) {
  await nav(page).getByRole("link", { name }).click();
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
}
