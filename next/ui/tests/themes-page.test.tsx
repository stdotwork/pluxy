// The Themes page (kit/pages/themes.tsx) over a real theme state and a fake core: its notices say
// one thing at a time, a load that failed has its own Try again, a file over the limit is refused
// before it is read, and turning custom CSS off keeps what the person wrote.
import { afterAll, afterEach, beforeAll, expect, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import { api } from "../kit/api";
import { ThemesPage } from "../kit/pages/themes";
import { PrivacyState } from "../kit/privacy";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";
import { setup, stubStyleSheets } from "./themeHarness";

let restoreSheets = () => {};
beforeAll(() => {
  restoreSheets = stubStyleSheets();
});
afterAll(() => restoreSheets());
afterEach(restoreDom);

const fixture = (name: string) => Bun.file(new URL(`fixtures/${name}`, import.meta.url)).text();
const nightfallText = await fixture("t3-docs-nightfall.json");
const dawnText = await fixture("t3-short-variants-css.json");

function world() {
  const s = setup();
  const themes = s.state();
  const kit = {
    host: s.host,
    api: api(s.host),
    themes,
    privacy: new PrivacyState(false, () => {}),
    unauthorized() {},
    extensionsChanged() {},
  } as Kit;
  const mount = (tree: ComponentChildren) =>
    mountIn(<KitContext.Provider value={kit}>{tree}</KitContext.Provider>);
  return { ...s, themes, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const withClass = (root: FakeNode, name: string) =>
  descend(root).filter((n) => attribute(n, "class")?.split(" ").includes(name));
const buttons = (root: FakeNode) => all(root, "button").map((b) => b.textContent);
const buttonIn = (root: FakeNode, label: string) => {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} among ${buttons(root).join(", ")}`);
  return found;
};
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const fire = async (node: FakeNode, type: string) => {
  await act(async () => {
    node.dispatchEvent(new Event(type));
  });
  await settle();
};
const notices = (root: FakeNode) => withClass(root, "notice").map((n) => n.textContent);
const input = (root: FakeNode, type: string) => {
  const found = all(root, "input").find((i) => attribute(i, "type") === type);
  if (!found) throw new Error(`no input of type ${type}`);
  return found;
};
/** The state's calls to core run one after another, out of sight: wait until check holds. */
async function until(check: () => boolean) {
  for (let i = 0; i < 500 && !check(); i++) await new Promise((r) => setTimeout(r, 1));
  expect(check()).toBe(true);
}
const refuse = (method: string, path: string) => (m: string, p: string) =>
  m === method && p === path
    ? { status: 500, error: { code: "internal", message: "core is down" } }
    : undefined;

test("a themes load that failed says so with its own Try again, which reads once and is then gone", async () => {
  const w = world();
  w.core.instead = refuse("GET", "/themes");
  w.themes.connect();
  await until(() => w.themes.problem !== undefined);
  const m = await w.mount(<ThemesPage />);
  expect(notices(m.root)).toEqual(["Could not load pluxy's themes: core is down. Try again"]);
  expect(buttons(m.root)).not.toContain("Refresh");

  const before = w.core.count("GET /themes");
  w.core.instead = () => undefined;
  // Two presses before the read ends: the button is busy after the first, so core is read once.
  const again = buttonIn(m.root, "Try again");
  await act(async () => {
    again.dispatchEvent(new Event("click"));
    again.dispatchEvent(new Event("click"));
  });
  await settle();
  await until(() => w.themes.problem === undefined);
  await settle();
  expect(w.core.count("GET /themes")).toBe(before + 1);
  expect(notices(m.root)).toEqual([]);
});

test("the page says one thing at a time, the newest first", async () => {
  const w = world();
  w.core.files.set("t3/nightfall", JSON.parse(nightfallText));
  w.themes.connect();
  await until(() => w.themes.listed.some((l) => l.id === "t3/nightfall"));
  const m = await w.mount(<ThemesPage />);
  expect(notices(m.root)).toEqual([]);

  // A re-read that fails: the load notice.
  w.core.instead = refuse("GET", "/themes");
  await act(async () => void w.themes.refresh());
  await until(() => w.themes.problem !== undefined);
  await settle();
  expect(notices(m.root)).toHaveLength(1);
  expect(notices(m.root)[0]).toContain("Could not load pluxy's themes");

  // Then a theme that core will not turn on: that is newer, and the only notice.
  w.core.instead = (method, path) =>
    method === "PUT" && path === "/theme" ? refuse("PUT", "/theme")(method, path) : undefined;
  const radio = all(m.root, "input").find(
    (i) => attribute(i, "type") === "radio" && i.parentNode?.textContent.startsWith("Nightfall"),
  );
  if (!radio) throw new Error("no Nightfall in the picker");
  radio.checked = true;
  await fire(radio, "change");
  expect(notices(m.root)).toEqual(["Could not turn on Nightfall: core is down."]);
});

test("a file over 256 KiB is refused before it is read; one at the limit is read", async () => {
  const w = world();
  const m = await w.mount(<ThemesPage />);
  const picker = input(m.root, "file") as FakeNode & { files?: unknown[] };
  let read = 0;
  const file = (size: number) => ({
    size,
    text: async () => {
      read++;
      return "not json";
    },
  });

  picker.files = [file(257 * 1024)];
  await fire(picker, "change");
  expect(read).toBe(0);
  expect(notices(m.root)).toEqual(["The file is over 256 KiB."]);
  expect(w.core.calls.filter((c) => c.startsWith("PUT"))).toEqual([]);

  picker.files = [file(256 * 1024)];
  await fire(picker, "change");
  expect(read).toBe(1);
  expect(notices(m.root)[0]).toStartWith("Not added: ");
});

test("Custom CSS off turns every custom CSS off and keeps what the person wrote", async () => {
  const w = world();
  expect(await w.themes.addTheme(dawnText)).toEqual({ replaced: false });
  const m = await w.mount(<ThemesPage />);
  const css = all(m.root, "textarea").find((t) => attribute(t, "placeholder")?.startsWith("[data-part"));
  if (!css) throw new Error("no Your CSS box");
  css.value = "h1 { color: red; }";
  await fire(css, "input");
  expect(w.themes.choices.css).toBe("h1 { color: red; }");
  expect(m.text()).toContain("Dawn and Dusk adds its own CSS.");
  expect(m.text()).not.toContain("turned off with custom CSS");

  const on = input(m.root, "checkbox");
  expect(on.checked).toBe(true);
  on.checked = false;
  await fire(on, "change");
  expect(w.themes.choices.safe).toBe(true);
  // What the person wrote stays, in the state and in the box.
  expect(w.themes.choices.css).toBe("h1 { color: red; }");
  expect(css.value).toBe("h1 { color: red; }");
  expect(m.text()).toContain("Dawn and Dusk adds its own CSS, turned off with custom CSS.");

  on.checked = true;
  await fire(on, "change");
  expect(w.themes.choices.safe).toBe(false);
  expect(w.themes.choices.css).toBe("h1 { color: red; }");
});

test("Layout has a row and a sentence for each choice, and Reset layout puts the theme's back", async () => {
  const w = world();
  const m = await w.mount(<ThemesPage />);
  const rows = withClass(m.root, "setting-name").map((n) => n.textContent);
  expect(rows).toEqual(["Frame", "Navigation", "Width", "Density", "Tabs", "Details", "Lists", "Custom CSS"]);
  for (const sentence of [
    "The page on a panel inset from the window, or flush with it.",
    "Where the main pages are listed.",
    "How wide pages may grow.",
    "How much space is between things.",
    "Where a page's tabs sit.",
    "Where an opened item shows.",
    "Tables or cards, for the lists that use them.",
  ]) {
    expect(m.text()).toContain(sentence);
  }
  w.themes.setLayout("nav", "top");
  await settle();
  expect(w.themes.choices.layout).toEqual({ nav: "top" });
  await press(buttonIn(m.root, "Reset layout"));
  expect(w.themes.choices.layout).toEqual({});
});

test("the words of the page are the plain ones", async () => {
  const w = world();
  const m = await w.mount(<ThemesPage />);
  const text = m.text();
  for (const word of ["Export", "Safe mode", "Privacy", "data-part hooks'", "Refresh"]) {
    expect(text).not.toContain(word);
  }
  expect(text).toContain("The theme is kept by pluxy, so every window connected to it shows the same one.");
  expect(text).toContain("A pluxy or T3 Code theme file (.json).");
  expect(text).toContain("Or paste a theme file");
  expect(buttons(m.root)).toContain("Save theme file");
});

test("Remove asks first, names the theme, and a built-in theme has none", async () => {
  const w = world();
  const m = await w.mount(<ThemesPage />);
  expect(buttons(m.root)).not.toContain("Remove");
  expect(await w.themes.addTheme(nightfallText)).toEqual({ replaced: false });
  await settle();
  const remove = buttonIn(m.root, "Remove");
  expect(attribute(remove, "aria-label")).toBe("Remove Nightfall");
  await press(remove);
  expect(w.core.calls.some((c) => c.startsWith("DELETE"))).toBe(false);
  expect(buttons(m.root)).toContain("Yes, remove Nightfall");
  await press(buttonIn(m.root, "Cancel"));
  expect(w.core.calls.some((c) => c.startsWith("DELETE"))).toBe(false);
  await press(buttonIn(m.root, "Remove"));
  await press(buttonIn(m.root, "Yes, remove Nightfall"));
  await until(() => w.core.calls.includes("DELETE /themes/t3/nightfall"));
  expect(w.themes.current.theme.id).toBe("pluxy/default");
});

const cssOf = (node: FakeNode) => (node.style as unknown as { cssText?: string }).cssText ?? "";

test("each theme in the gallery has a miniature of pluxy's frame in its own colours, light and dark when it has both", async () => {
  const w = world();
  // A theme someone added, with both appearances, its navigation on top and a flat frame (as Paper has).
  const topFlat = { theme_version: 0, id: "me/topflat", name: "Top Flat", appearance: "both" };
  expect(
    await w.themes.addTheme(JSON.stringify({ ...topFlat, layout: { nav: "top", frame: "flat" } })),
  ).toEqual({ replaced: false });
  const m = await w.mount(<ThemesPage />);
  await settle();
  const previews = withClass(m.root, "mini-pair");
  // Default has both appearances, Terminal is dark only.
  expect(previews.map((p) => attribute(p, "data-halves"))).toEqual(["2", "1", "2"]);
  const halves = (preview: FakeNode) => withClass(preview, "mini");
  const [def, terminal, flat] = previews as [FakeNode, FakeNode, FakeNode];
  // The miniature's colours are the theme's own tokens, set on the miniature itself: Default's two
  // halves are its light and its dark ground, so a mix of tokens resolves against the theme.
  const [light, dark] = halves(def).map(cssOf);
  expect(light).toContain("--px-color-bg:#fcfcfc;");
  expect(dark).toContain("--px-color-bg:#111113;");
  expect(withClass(def, "mini-half").map((h) => attribute(h, "data-half"))).toEqual(["light", "dark"]);
  expect(withClass(terminal, "mini-half").map((h) => attribute(h, "data-half"))).toEqual(["dark"]);
  // The miniature draws the theme's own layout: a theme with its navigation on top and a flat frame
  // shows that, and Terminal a rail.
  const [flatLight] = halves(flat) as [FakeNode];
  expect([attribute(flatLight, "data-nav"), attribute(flatLight, "data-frame")]).toEqual(["top", "flat"]);
  expect(attribute(halves(terminal)[0] as FakeNode, "data-nav")).toBe("rail");
  // Each card says which appearances the theme offers.
  const cards = withClass(m.root, "theme-card").map((c) => withClass(c, "theme-modes")[0]?.textContent);
  expect(cards).toEqual(["Light and dark", "Dark only", "Light and dark"]);
  // The miniatures are pictures: no text in them, and the cards stay named by their theme.
  for (const p of previews) expect(p.textContent).toBe("");
});

test("a theme file dropped on the drop zone is added like a chosen one, and an oversize one is refused unread", async () => {
  const w = world();
  const m = await w.mount(<ThemesPage />);
  const zone = withClass(m.root, "theme-drop")[0] as FakeNode;
  const drop = async (file: unknown) => {
    const event = new Event("drop") as Event & { dataTransfer?: unknown };
    event.dataTransfer = { files: [file] };
    await act(async () => {
      zone.dispatchEvent(event);
    });
    await settle();
  };
  await drop({ size: 257 * 1024, text: async () => "never read" });
  expect(notices(m.root)).toEqual(["The file is over 256 KiB."]);
  expect(w.core.calls.filter((c) => c.startsWith("PUT"))).toEqual([]);

  await drop({ size: nightfallText.length, text: async () => nightfallText });
  await until(() => w.themes.current.theme.id === "t3/nightfall");
  expect(notices(m.root)).toEqual(["Added and turned on Nightfall."]);
});
