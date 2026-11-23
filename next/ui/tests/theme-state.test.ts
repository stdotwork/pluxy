// The theme state against a fake host: core keeps the themes and which one is on, prefs keep the
// device's choices and a copy of the theme last applied.
import { afterAll, beforeAll, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Host, Method, Reply } from "../kit/host";
import { ThemeState } from "../kit/theme/state";
import defaultTheme from "../themes/default.json";
import terminal from "../themes/terminal.json";

const fixture = (name: string) => readFileSync(join(import.meta.dir, "fixtures", name), "utf8");
const nightfallText = fixture("t3-docs-nightfall.json");
const nightfall = JSON.parse(nightfallText) as Record<string, unknown>;
const dawn = JSON.parse(fixture("t3-short-variants-css.json")) as Record<string, unknown>;
const CANVAS = "--app-theme-canvas";

const g = globalThis as { CSSStyleSheet?: unknown };
const had = g.CSSStyleSheet;
beforeAll(() => {
  g.CSSStyleSheet = class {
    text = "";
    replaceSync(text: string) {
      this.text = text;
    }
  };
});
afterAll(() => {
  g.CSSStyleSheet = had;
});

/** Core's themes, as its management API serves them, with the calls it got. */
class FakeCore {
  current = "";
  files = new Map<string, unknown>();
  calls: string[] = [];
  /** A reply to give instead, for a call this returns one for. */
  instead: (method: Method, path: string) => Reply<unknown> | undefined = () => undefined;

  call(method: Method, path: string, body?: unknown): Reply<unknown> {
    this.calls.push(`${method} ${path}`);
    const forced = this.instead(method, path);
    if (forced) return forced;
    if (method === "GET" && path === "/themes") {
      const themes = [...this.files].map(([id, file]) => ({
        id,
        name: String((file as { name?: unknown }).name ?? ""),
        bytes: JSON.stringify(file).length,
      }));
      return { status: 200, body: { current: this.current, themes } };
    }
    if (method === "PUT" && path === "/theme") {
      this.current = (body as { id: string }).id;
      return { status: 200, body: { id: this.current } };
    }
    const id = path.replace(/^\/themes\//, "");
    if (method === "GET") {
      return this.files.has(id) ? { status: 200, body: this.files.get(id) } : { status: 404 };
    }
    if (method === "PUT") {
      this.files.set(id, body);
      return { status: 200, body: { id, name: "", bytes: JSON.stringify(body).length } };
    }
    if (method === "DELETE") {
      this.files.delete(id);
      if (this.current === id) this.current = "";
      return { status: 204 };
    }
    return { status: 404 };
  }

  count(call: string) {
    return this.calls.filter((c) => c === call).length;
  }
}

function setup(prefs: Record<string, string> = {}) {
  const core = new FakeCore();
  const kept = new Map(Object.entries(prefs));
  const host: Host = {
    call: async <T>(method: Method, path: string, body?: unknown) =>
      core.call(method, path, body) as Reply<T>,
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: {
      get: (name) => kept.get(name) ?? null,
      set: (name, value) => {
        if (value === null) kept.delete(name);
        else kept.set(name, value);
      },
    },
    features: new Set(),
  };
  const doc = Object.assign(new EventTarget(), {
    visibilityState: "visible",
    documentElement: { dataset: {}, classList: { toggle() {} }, style: {} },
    adoptedStyleSheets: [],
  });
  const win = Object.assign(new EventTarget(), {
    matchMedia: () => ({ matches: false, addEventListener() {} }),
  });
  const state = () =>
    new ThemeState(host, doc as unknown as Document, [defaultTheme, terminal], win as unknown as Window);
  return { core, kept, doc, win, state };
}

/** Waits until check holds: the state's calls to core run one after another, out of sight. */
async function until(check: () => boolean) {
  for (let i = 0; i < 500 && !check(); i++) await new Promise((r) => setTimeout(r, 1));
  expect(check()).toBe(true);
}

const applied = (kept: Map<string, string>) =>
  JSON.parse(kept.get("applied-theme") ?? "{}") as { id?: string };

test("starts in the theme last applied, then follows core's", async () => {
  const s = setup({ "applied-theme": JSON.stringify({ id: "t3/nightfall", file: nightfall }) });
  s.core.current = "pluxy/terminal";
  const themes = s.state();
  // Before core has answered: the cached theme, from its cached file.
  expect(themes.current.theme.id).toBe("t3/nightfall");
  expect(themes.resolved.variables[CANVAS]).toBeString();
  expect(s.core.calls).toEqual([]);

  themes.connect();
  await until(() => themes.current.theme.id === "pluxy/terminal");
  expect(themes.resolved.variables[CANVAS]).toBeUndefined();
  expect(applied(s.kept).id).toBe("pluxy/terminal");
  // Core's choice was read, not written.
  expect(s.core.count("PUT /theme")).toBe(0);
});

test("follows core to a stored theme by reading its file, and to the default when core holds none", async () => {
  const s = setup();
  s.core.files.set("t3/dawn-and-dusk", dawn);
  s.core.current = "t3/dawn-and-dusk";
  const themes = s.state();
  expect(themes.current.theme.id).toBe("pluxy/default");
  themes.connect();
  await until(() => themes.current.theme.id === "t3/dawn-and-dusk");
  expect(s.core.count("GET /themes/t3/dawn-and-dusk")).toBe(1);
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal", "t3/dawn-and-dusk"]);

  // A theme core names but does not hold: the default shows, and core's choice is left alone.
  s.core.current = "t3/gone";
  await themes.refresh();
  expect(themes.current.theme.id).toBe("pluxy/default");
  expect(s.core.current).toBe("t3/gone");
});

test("a file the reader refuses never reaches core", async () => {
  const s = setup();
  const themes = s.state();
  themes.connect();
  await until(() => s.core.count("GET /themes") === 1);
  expect(await themes.addTheme("{ not json")).toEqual({ error: "the file is not JSON" });
  expect(await themes.addTheme(JSON.stringify({ name: "No colours", appearance: "dark" }))).toEqual({
    error: expect.any(String),
  });
  // A built-in theme's exported file: the reason names the field to change.
  expect(await themes.addTheme(JSON.stringify({ ...defaultTheme }))).toEqual({
    error:
      '"id" is pluxy/default, a built-in theme\'s ID. Change "id" in the file to your own, such as my/default, and add it again',
  });
  expect(s.core.calls.filter((c) => c.startsWith("PUT"))).toEqual([]);
  expect(themes.current.theme.id).toBe("pluxy/default");
});

test("adds a theme as it was given, turns it on, exports it and removes it", async () => {
  const s = setup();
  const themes = s.state();
  themes.connect();
  expect(await themes.addTheme(nightfallText)).toEqual({ replaced: false });
  expect(s.core.files.get("t3/nightfall")).toEqual(nightfall);
  expect(s.core.current).toBe("t3/nightfall");
  expect(themes.current.theme.id).toBe("t3/nightfall");
  expect(themes.resolved.variables[CANVAS]).toBeString();
  expect(applied(s.kept).id).toBe("t3/nightfall");

  const file = themes.exportFile();
  expect(file.name).toBe("nightfall.json");
  expect(JSON.parse(file.text)).toEqual(nightfall);

  expect(await themes.select("pluxy/terminal")).toBeUndefined();
  expect(s.core.current).toBe("pluxy/terminal");
  expect(JSON.parse(themes.exportFile().text)).toEqual(terminal);

  expect(await themes.select("t3/nightfall")).toBeUndefined();
  expect(await themes.removeTheme("t3/nightfall")).toBeUndefined();
  expect(s.core.files.size).toBe(0);
  expect(themes.current.theme.id).toBe("pluxy/default");
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal"]);
});

test("adding a different file under a stored theme's ID says it replaced that theme", async () => {
  const s = setup();
  const themes = s.state();
  themes.connect();
  // The first file under this name: there is no stored theme for it to take the place of.
  expect(await themes.addTheme(nightfallText)).toEqual({ replaced: false });
  // A second, different file with the same name gets the same ID, and replaces the first.
  const other = { ...nightfall, description: "a different file" };
  expect(await themes.addTheme(JSON.stringify(other))).toEqual({ replaced: true });
  expect(s.core.files.get("t3/nightfall")).toEqual(other);
  // The very same file again takes no theme's place.
  expect(await themes.addTheme(JSON.stringify(other))).toEqual({ replaced: false });
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal", "t3/nightfall"]);
});

test("a theme core refuses is not added, and the theme that was on stays", async () => {
  const s = setup();
  const themes = s.state();
  themes.connect();
  expect(await themes.select("pluxy/terminal")).toBeUndefined();
  s.core.instead = (method, path) =>
    method === "PUT" && path.startsWith("/themes/")
      ? { status: 422, error: { code: "refused", message: "at most 64 themes can be stored" } }
      : undefined;
  expect(await themes.addTheme(nightfallText)).toEqual({ error: "at most 64 themes can be stored" });
  expect(themes.current.theme.id).toBe("pluxy/terminal");
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal"]);
  expect(applied(s.kept).id).toBe("pluxy/terminal");
});

test("a theme core cannot turn on goes back to the one before, and so does a failed removal", async () => {
  const s = setup();
  s.core.files.set("t3/nightfall", nightfall);
  const themes = s.state();
  themes.connect();
  await until(() => themes.listed.length === 3);
  s.core.instead = (method, path) => (method === "PUT" && path === "/theme" ? { status: 0 } : undefined);
  expect(await themes.select("t3/nightfall")).toBe(
    "core could not be reached: pluxy is not running or the address is wrong, so start it, then try again",
  );
  expect(themes.current.theme.id).toBe("pluxy/default");
  expect(applied(s.kept).id).toBe("pluxy/default");

  s.core.instead = () => undefined;
  expect(await themes.select("t3/nightfall")).toBeUndefined();
  s.core.instead = (method) => (method === "DELETE" ? { status: 500 } : undefined);
  expect(await themes.removeTheme("t3/nightfall")).toBe("core answered 500");
  expect(themes.current.theme.id).toBe("t3/nightfall");
});

test("themes kept in prefs by an earlier version go to core once", async () => {
  const s = setup({
    theme: "t3/nightfall",
    themes: JSON.stringify([nightfallText, "{ not a theme"]),
    choices: JSON.stringify({ appearance: "dark" }),
  });
  const themes = s.state();
  // Before sign-in the old choice still paints.
  expect(themes.current.theme.id).toBe("t3/nightfall");

  themes.connect();
  await until(() => !s.kept.has("themes"));
  await themes.refresh();
  expect([...s.core.files.keys()]).toEqual(["t3/nightfall"]);
  expect(s.core.current).toBe("t3/nightfall");
  expect(s.kept.has("theme")).toBe(false);
  expect(themes.current.theme.id).toBe("t3/nightfall");
  expect(themes.problem).toBeUndefined();
  // The device's own choices stay in prefs.
  expect(themes.choices.appearance).toBe("dark");
  expect(s.kept.has("choices")).toBe(true);

  // The next start sends nothing again.
  const puts = s.core.calls.filter((c) => c.startsWith("PUT")).length;
  s.state().connect();
  await until(() => s.core.count("GET /themes") >= 3);
  expect(s.core.calls.filter((c) => c.startsWith("PUT")).length).toBe(puts);
});

test("old themes core refuses stay in prefs, and the next start stores them", async () => {
  const refused = { ...dawn, name: "Refused By Core" };
  const imported = JSON.stringify([nightfallText, JSON.stringify(refused)]);
  const s = setup({ theme: "t3/refused-by-core", themes: imported });
  s.core.instead = (method, path) =>
    method === "PUT" && path === "/themes/t3/refused-by-core"
      ? { status: 422, error: { code: "refused", message: "at most 64 themes can be stored" } }
      : undefined;
  const themes = s.state();
  themes.connect();
  await until(() => s.core.count("GET /themes") === 2);

  // Core took the theme it could; the refused one is only in prefs, so both prefs stay.
  expect([...s.core.files.keys()]).toEqual(["t3/nightfall"]);
  expect(s.core.count("PUT /themes/t3/refused-by-core")).toBe(1);
  expect(s.kept.get("themes")).toBe(imported);
  expect(s.kept.get("theme")).toBe("t3/refused-by-core");
  expect(themes.problem).toBeUndefined();

  // The next start: core answers this time, so the theme goes to core and the prefs go.
  s.core.instead = () => undefined;
  s.state().connect();
  await until(() => !s.kept.has("themes"));
  expect([...s.core.files.keys()].sort()).toEqual(["t3/nightfall", "t3/refused-by-core"]);
  // The theme core already held was not sent again.
  expect(s.core.count("PUT /themes/t3/nightfall")).toBe(1);
  expect(s.kept.has("theme")).toBe(false);
  expect(s.core.current).toBe("t3/refused-by-core");
});

test("old prefs stay when core cannot be reached, for the next time", async () => {
  const s = setup({ theme: "pluxy/terminal", themes: JSON.stringify([nightfallText]) });
  s.core.instead = () => ({ status: 0 });
  const themes = s.state();
  themes.connect();
  await until(() => themes.problem !== undefined);
  expect(themes.problem).toBe(
    "core could not be reached: pluxy is not running or the address is wrong, so start it, then try again",
  );
  expect(s.kept.has("themes")).toBe(true);
  expect(themes.current.theme.id).toBe("pluxy/terminal");
});

test("re-reads core's themes when the window gets focus or shows again, and stops when told", async () => {
  const s = setup();
  const themes = s.state();
  const stop = themes.connect();
  await until(() => s.core.count("GET /themes") === 1);
  expect(themes.current.theme.id).toBe("pluxy/default");

  // Another window, or `pluxy theme add` and `pluxy theme set`, changed core's themes.
  s.core.files.set("t3/nightfall", nightfall);
  s.core.current = "t3/nightfall";
  s.win.dispatchEvent(new Event("focus"));
  await until(() => themes.current.theme.id === "t3/nightfall");
  expect(themes.listed.map((l) => l.id)).toContain("t3/nightfall");

  // The list alone changed: the theme that is on stays, the list follows.
  s.core.files.set("t3/dawn-and-dusk", dawn);
  s.doc.dispatchEvent(new Event("visibilitychange"));
  await until(() => themes.listed.length === 4);
  expect(themes.current.theme.id).toBe("t3/nightfall");
  await themes.loadFiles();
  expect(themes.listed.find((l) => l.id === "t3/dawn-and-dusk")?.offered?.theme.appearance).toBe("both");

  // A hidden window does not ask.
  const reads = s.core.count("GET /themes");
  s.doc.visibilityState = "hidden";
  s.doc.dispatchEvent(new Event("visibilitychange"));
  await new Promise((r) => setTimeout(r, 5));
  expect(s.core.count("GET /themes")).toBe(reads);

  s.doc.visibilityState = "visible";
  stop();
  s.win.dispatchEvent(new Event("focus"));
  await new Promise((r) => setTimeout(r, 5));
  expect(s.core.count("GET /themes")).toBe(reads);
});

test("follows the theme that is on when its file is replaced by one of the same length", async () => {
  const s = setup();
  s.core.files.set("t3/nightfall", nightfall);
  s.core.current = "t3/nightfall";
  const themes = s.state();
  themes.connect();
  await until(() => themes.current.theme.id === "t3/nightfall");
  const before = themes.resolved.variables[CANVAS];
  expect(before).toBeString();

  // `pluxy theme add` replaced the file with one of the same byte length (#1a1b26 to #1a1b27).
  const swapped = { ...nightfall, canvas: "#1a1b27" };
  expect(JSON.stringify(swapped).length).toBe(JSON.stringify(nightfall).length);
  s.core.files.set("t3/nightfall", swapped);
  s.win.dispatchEvent(new Event("focus"));
  await themes.refresh();
  expect(themes.resolved.variables[CANVAS]).not.toBe(before);
  expect((JSON.parse(s.kept.get("applied-theme") ?? "{}") as { file: { canvas: string } }).file.canvas).toBe(
    "#1a1b27",
  );
});

test("reads the file of the theme that is on when the window gets focus, and repaints nothing when it is the same", async () => {
  const s = setup();
  s.core.files.set("t3/nightfall", nightfall);
  s.core.current = "t3/nightfall";
  const themes = s.state();
  themes.connect();
  await until(() => themes.current.theme.id === "t3/nightfall");
  const painted = themes.resolved;
  const reads = s.core.count("GET /themes/t3/nightfall");

  s.win.dispatchEvent(new Event("focus"));
  await themes.refresh();
  expect(s.core.count("GET /themes/t3/nightfall")).toBe(reads + 1);
  expect(themes.resolved).toBe(painted);
});

test("a stored file the reader refuses is listed with the reason and cannot be turned on", async () => {
  const s = setup();
  s.core.files.set("me/broken", { name: "Broken" });
  const themes = s.state();
  themes.connect();
  await until(() => themes.listed.length === 3);
  await themes.loadFiles();
  const broken = themes.listed.find((l) => l.id === "me/broken");
  expect(broken?.problem).toBeString();
  expect(await themes.select("me/broken")).toBe(broken?.problem);
  expect(themes.current.theme.id).toBe("pluxy/default");
  expect(s.core.count("PUT /theme")).toBe(0);
});

test("a stored file the reader refuses that core has on is named apart from a failed read, and can be removed", async () => {
  const s = setup();
  s.core.files.set("pluxy/terminal-copy", terminal);
  s.core.files.set("me/broken", { name: "Broken" });
  s.core.current = "me/broken";
  const themes = s.state();
  themes.connect();
  await until(() => themes.unusable !== undefined);
  // Core answered: what is wrong is the file.
  expect(themes.problem).toBeUndefined();
  expect(themes.unusable).toEqual({
    id: "me/broken",
    problem: themes.listed.find((l) => l.id === "me/broken")?.problem ?? "",
  });
  expect(themes.unusable?.problem).toStartWith("the file is not a theme file.");
  expect(themes.current.theme.id).toBe("pluxy/default");

  // It stays named after another read, and goes when the theme is removed.
  await themes.refresh();
  expect(themes.unusable?.id).toBe("me/broken");
  expect(await themes.removeTheme("me/broken")).toBeUndefined();
  expect(themes.unusable).toBeUndefined();
  expect(s.core.files.has("me/broken")).toBe(false);
  expect(s.core.current).toBe("");
  expect(themes.listed.map((l) => l.id)).not.toContain("me/broken");

  // A read that fails is still core's fault, in `problem`.
  s.core.current = "pluxy/terminal-copy";
  s.core.instead = (method, path) =>
    method === "GET" && path === "/themes/pluxy/terminal-copy" ? { status: 0 } : undefined;
  await themes.refresh();
  expect(themes.unusable).toBeUndefined();
  expect(themes.problem).toBeString();
});

test("a theme core no longer stores says so when picked, leaves the list, and counts as removed", async () => {
  const s = setup();
  s.core.files.set("t3/nightfall", nightfall);
  s.core.files.set("t3/dawn", dawn);
  const themes = s.state();
  themes.connect();
  await until(() => themes.listed.length === 4);

  // Removed somewhere else since this window read the list.
  s.core.files.clear();
  expect(await themes.select("t3/nightfall")).toBe("core does not store this theme any more");
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal", "t3/dawn"]);
  expect(themes.current.theme.id).toBe("pluxy/default");
  expect(s.core.count("PUT /theme")).toBe(0);

  // Removing one that is already gone is what the person asked for.
  expect(await themes.removeTheme("t3/dawn")).toBeUndefined();
  expect(themes.listed.map((l) => l.id)).toEqual(["pluxy/default", "pluxy/terminal"]);
});

test("a key core refuses signs the person out when they act, not when the state reads by itself", async () => {
  const s = setup();
  s.core.files.set("t3/nightfall", nightfall);
  const themes = s.state();
  let signedOut = 0;
  themes.connect(() => signedOut++);
  await until(() => themes.listed.length === 3);

  // The window gets focus while core refuses the key (a sign-in may be under way): no sign-out.
  s.core.instead = () => ({ status: 401 });
  s.win.dispatchEvent(new Event("focus"));
  await until(() => themes.problem !== undefined);
  await themes.loadFiles();
  expect(signedOut).toBe(0);
  expect(themes.current.theme.id).toBe("pluxy/default");

  // The person picks a theme and core refuses the key: back to signing in.
  expect(await themes.select("pluxy/terminal")).toBeDefined();
  expect(signedOut).toBe(1);
});
