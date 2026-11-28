// General (kit/pages/general.tsx): Appearance, Hide names and Sign out; and the header strip's
// Names hidden button, which is there only while names are hidden.
import { afterAll, afterEach, beforeAll, expect, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import { api } from "../kit/api";
import type { Feature } from "../kit/host";
import { type GeneralOptions, GeneralPage } from "../kit/pages/general";
import { PrivacyState } from "../kit/privacy";
import { NamesHiddenButton } from "../kit/shell";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";
import { setup, stubStyleSheets } from "./themeHarness";

let restoreSheets = () => {};
beforeAll(() => {
  restoreSheets = stubStyleSheets();
});
afterAll(() => restoreSheets());
afterEach(restoreDom);

const nightfall = await Bun.file(new URL("fixtures/t3-docs-nightfall.json", import.meta.url)).text();

/** A kit over a fake core, a theme state and a privacy state that starts as given. */
function world(features: Feature[] = ["signOut"], hidden = false) {
  const s = setup({}, features);
  const themes = s.state();
  const privacy = new PrivacyState(hidden, () => {});
  const kit = {
    host: s.host,
    api: api(s.host),
    themes,
    privacy,
    unauthorized() {},
    extensionsChanged() {},
  } as Kit;
  const mount = (tree: ComponentChildren) =>
    mountIn(<KitContext.Provider value={kit}>{tree}</KitContext.Provider>);
  return { ...s, themes, privacy, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const buttons = (root: FakeNode) => all(root, "button").map((b) => b.textContent);
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const switchOf = (root: FakeNode) => {
  const found = all(root, "input").find((i) => attribute(i, "role") === "switch");
  if (!found) throw new Error("no switch");
  return found;
};
const change = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("change"));
  });
  await settle();
};

test("Sign out is a row only where the host can sign out and the build keeps it", async () => {
  const cases: [Feature[], GeneralOptions | undefined, boolean][] = [
    [[], undefined, false],
    [["signOut"], { signOut: false }, false],
    [["signOut"], undefined, true],
    [["signOut"], { signOut: true }, true],
  ];
  for (const [features, options, shown] of cases) {
    const w = world(features);
    const m = await w.mount(<GeneralPage options={options} />);
    expect(buttons(m.root).includes("Sign out")).toBe(shown);
    expect(m.text().includes("This browser forgets the management key")).toBe(shown);
    await m.unmount();
    restoreDom();
  }
});

test("one press of Sign out asks; only Yes, sign out signs out, and Cancel does not", async () => {
  const w = world();
  const m = await w.mount(<GeneralPage />);
  const find = (label: string) => {
    const b = all(m.root, "button").find((n) => n.textContent === label);
    if (!b) throw new Error(`no button ${label} among ${buttons(m.root).join(", ")}`);
    return b;
  };
  await press(find("Sign out"));
  expect(w.signedOut.times).toBe(0);
  expect(buttons(m.root)).toContain("Yes, sign out");
  expect(buttons(m.root)).toContain("Cancel");
  await press(find("Cancel"));
  expect(w.signedOut.times).toBe(0);
  expect(buttons(m.root)).toContain("Sign out");
  await press(find("Sign out"));
  await press(find("Yes, sign out"));
  expect(w.signedOut.times).toBe(1);
});

test("Appearance offers three choices, and a theme with one appearance says so instead", async () => {
  const w = world();
  const m = await w.mount(<GeneralPage />);
  const radios = () => all(m.root, "input").filter((i) => attribute(i, "type") === "radio");
  expect(radios()).toHaveLength(3);
  expect(m.text()).toContain("Light");
  expect(m.text()).toContain("Follow the system");
  expect(m.text()).not.toContain("dark only");

  // Nightfall is a dark theme with no light form.
  expect(await w.themes.addTheme(nightfall)).toEqual({ replaced: false });
  await settle();
  expect(radios()).toHaveLength(0);
  expect(m.text()).toContain("Nightfall is dark only. Pick another theme in Themes.");
  const link = all(m.root, "a").find((a) => attribute(a, "href") === "#/themes");
  expect(link?.textContent).toBe("Themes");
});

test("Hide names is a switch that hides names at once, with nothing to save", async () => {
  const w = world();
  const m = await w.mount(<GeneralPage />);
  expect(m.text()).toContain(
    "Hides account e-mail addresses and names on every page, for sharing your screen.",
  );
  expect(buttons(m.root)).not.toContain("Save");
  const hide = switchOf(m.root);
  expect(hide.checked).toBe(false);
  hide.checked = true;
  await change(hide);
  expect(w.privacy.on).toBe(true);
  hide.checked = false;
  await change(hide);
  expect(w.privacy.on).toBe(false);
});

test("the header strip shows Names hidden only while names are hidden, and pressing it shows them again", async () => {
  const w = world(["signOut"], false);
  const m = await w.mount(<NamesHiddenButton privacy={w.privacy} />);
  expect(all(m.root, "button")).toHaveLength(0);
  await act(async () => w.privacy.set(true));
  await settle();
  const [button] = all(m.root, "button");
  expect(button?.textContent).toBe("Names hidden");
  expect(button && attribute(button, "data-part")).toBe("privacy-toggle");
  expect(button && attribute(button, "aria-pressed")).toBe("true");
  expect(button && attribute(button, "title")).toBe("Show names again");
  if (button) await press(button);
  expect(w.privacy.on).toBe(false);
  expect(all(m.root, "button")).toHaveLength(0);
});

test("a change to Appearance or Hide names says Saved beside it", async () => {
  const w = world();
  const m = await w.mount(<GeneralPage />);
  expect(m.text()).not.toContain("Saved");
  const dark = all(m.root, "input").find(
    (i) => attribute(i, "type") === "radio" && i.parentNode?.textContent === "Dark",
  );
  if (!dark) throw new Error("no Dark choice");
  dark.checked = true;
  await change(dark);
  expect(m.text()).toContain("Saved");
  expect(w.themes.choices.appearance).toBe("dark");
  await m.unmount();

  const hidden = await world().mount(<GeneralPage />);
  expect(hidden.text()).not.toContain("Saved");
  const hide = switchOf(hidden.root);
  hide.checked = true;
  await change(hide);
  expect(hidden.text()).toContain("Saved");
});

test("the search field narrows the rows to what matches, and says when nothing does", async () => {
  const w = world();
  const m = await w.mount(<GeneralPage />);
  const search = all(m.root, "input").find((i) => attribute(i, "type") === "search");
  if (!search) throw new Error("no search field");
  expect(attribute(search, "aria-label")).toBe("Search settings");
  const names = () =>
    all(m.root, "span")
      .filter((s) => attribute(s, "class") === "setting-name")
      .map((s) => s.textContent);
  expect(names()).toEqual(["Appearance", "Hide names", "Sign out"]);
  const type = async (text: string) => {
    search.value = text;
    await act(async () => {
      search.dispatchEvent(new Event("input"));
    });
    await settle();
  };
  await type("names");
  expect(names()).toEqual(["Hide names"]);
  await type("browser forgets");
  expect(names()).toEqual(["Sign out"]);
  await type("zzzz");
  expect(names()).toEqual([]);
  expect(m.text()).toContain("No setting matches that.");
  await type("");
  expect(names()).toEqual(["Appearance", "Hide names", "Sign out"]);
  expect(m.text()).not.toContain("No setting matches that.");
});
