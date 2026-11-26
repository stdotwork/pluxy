// Where a page sits in the frame (kit/destinations.ts), on the pages core offers and a few a host
// adds. The expected tabs, topics and ways back are written out by hand.
import { expect, test } from "bun:test";
import { h } from "preact";
import { DESTINATIONS, destinationHome, type PageDef, place } from "../kit/destinations";
import { corePages } from "../kit/pages/core";

const render = () => h("div", null);
const ids = (pages: PageDef[]) => pages.map((p) => p.id);

test("each of core's pages sits under the destination a person looks for it in", () => {
  const pages = corePages();
  const byDest = Object.fromEntries(
    DESTINATIONS.map((dest) => [dest, ids(pages.filter((p) => p.dest === dest))]),
  );
  expect(byDest).toEqual({
    home: ["status"],
    accounts: ["accounts", "add-account", "routing", "quota"],
    activity: ["usage", "activity/usage"],
    tools: [],
    settings: ["setup", "config", "system", "extensions", "themes", "logs"],
  });
});

test("a destination opens its first page that sits under no other; one with no page has none", () => {
  const pages = corePages();
  expect(destinationHome(pages, "home")?.id).toBe("status");
  expect(destinationHome(pages, "accounts")?.id).toBe("accounts");
  expect(destinationHome(pages, "settings")?.id).toBe("setup");
  // The panel has no Tools pages, so its navigation shows no Tools.
  expect(destinationHome(pages, "tools")).toBeUndefined();
  const child: PageDef = { id: "child", title: "Child", dest: "tools", under: "parent", render };
  const parent: PageDef = { id: "parent", title: "Parent", dest: "tools", render };
  expect(destinationHome([child, parent], "tools")?.id).toBe("parent");
});

test("Accounts has three tabs, named for what a person does there", () => {
  const placed = place(corePages(), "routing");
  expect(placed?.dest).toBe("accounts");
  expect(placed?.tabs.map((p) => p.label ?? p.title)).toEqual(["Accounts", "Order & fallback", "Limits"]);
  expect(placed?.current).toBe("routing");
  expect(placed?.above).toEqual([]);
  expect(placed?.topics).toEqual([]);
});

test("Activity has two tabs, Requests first, so the destination opens on it", () => {
  const pages = corePages();
  expect(destinationHome(pages, "activity")?.id).toBe("usage");
  const placed = place(pages, "activity/usage");
  expect(placed?.tabs.map((p) => p.label ?? p.title)).toEqual(["Requests", "Usage"]);
  expect(placed?.current).toBe("activity/usage");
});

test("a page under another shows the way back and no tabs", () => {
  const placed = place(corePages(), "add-account");
  expect(ids(placed?.above ?? [])).toEqual(["accounts"]);
  expect(placed?.tabs).toEqual([]);
  // The tab it sits under stays the current one, for a list drawn beside the page.
  expect(placed?.current).toBe("accounts");
});

test("Settings lists its topics by section, the extensions' pages last", () => {
  const pages: PageDef[] = [
    ...corePages(),
    {
      id: "ext/a/b/c",
      title: "From an extension",
      dest: "settings",
      kind: "topic",
      section: "extensions",
      render,
    },
    { id: "connection", title: "Connection", dest: "settings", kind: "topic", section: "device", render },
  ];
  const placed = place(pages, "themes");
  expect(placed?.topics.map((g) => [g.section, ids(g.pages)])).toEqual([
    ["core", ["setup", "config", "system", "extensions", "themes", "logs"]],
    ["device", ["connection"]],
    ["extensions", ["ext/a/b/c"]],
  ]);
  expect(placed?.current).toBe("themes");
  expect(placed?.tabs).toEqual([]);
  // The panel alone: one section, so the frame draws no section label.
  expect(place(corePages(), "themes")?.topics.map((g) => g.section)).toEqual(["core"]);
});

test("a destination with one tab shows no tabs, and a page with no listing has neither", () => {
  const one: PageDef = { id: "one", title: "One", dest: "tools", kind: "tab", render };
  expect(place([one], "one")?.tabs).toEqual([]);
  expect(place(corePages(), "status")).toEqual({
    dest: "home",
    above: [],
    tabs: [],
    topics: [],
    current: undefined,
  });
  expect(place(corePages(), "nowhere")).toBeUndefined();
});

test("a page that names a missing parent, or pages that name each other, still place", () => {
  const orphan: PageDef = { id: "orphan", title: "Orphan", dest: "tools", under: "gone", render };
  expect(place([orphan], "orphan")?.above).toEqual([]);
  const a: PageDef = { id: "a", title: "A", dest: "tools", under: "b", render };
  const b: PageDef = { id: "b", title: "B", dest: "tools", under: "a", render };
  expect(ids(place([a, b], "a")?.above ?? [])).toEqual(["b"]);
});
