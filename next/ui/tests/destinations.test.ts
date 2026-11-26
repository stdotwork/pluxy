// Where a page sits in the frame (kit/destinations.ts), on the pages core offers and a few a host
// adds. The expected tabs, topics and ways back are written out by hand.
import { describe, expect, test } from "bun:test";
import { h } from "preact";
import { DESTINATIONS, destinationHome, type PageDef, place } from "../kit/destinations";
import { corePages } from "../kit/pages/core";
import { currentId } from "../kit/shell";

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
    tools: ["tools"],
    settings: ["settings", "themes", "config", "extensions", "logs", "system", "setup"],
  });
});

test("a destination opens its first page that sits under no other; one with no page has none", () => {
  const pages = corePages();
  expect(destinationHome(pages, "home")?.id).toBe("status");
  expect(destinationHome(pages, "accounts")?.id).toBe("accounts");
  expect(destinationHome(pages, "settings")?.id).toBe("settings");
  expect(destinationHome(pages, "tools")?.id).toBe("tools");
  // A destination with no page has none, so its navigation entry is not shown.
  expect(
    destinationHome(
      pages.filter((p) => p.dest !== "tools"),
      "tools",
    ),
  ).toBeUndefined();
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
    ["device", ["settings", "themes", "connection"]],
    ["core", ["config", "extensions", "logs", "system", "setup"]],
    ["extensions", ["ext/a/b/c"]],
  ]);
  expect(placed?.current).toBe("themes");
  expect(placed?.tabs).toEqual([]);
  // The panel alone: this device's two topics, then pluxy's own.
  expect(place(corePages(), "themes")?.topics.map((g) => g.section)).toEqual(["device", "core"]);
});

test("Settings opens on General, with This device listed first, in the panel and in the app", () => {
  const app: PageDef[] = [
    ...corePages({ general: { signOut: false } }),
    {
      id: "connection",
      title: "Where pluxy runs",
      dest: "settings",
      kind: "topic",
      section: "device",
      render,
    },
    { id: "versions", title: "Updates", dest: "settings", kind: "topic", section: "device", render },
    { id: "device", title: "Add-ons", dest: "settings", kind: "topic", section: "device", render },
    { id: "launcher", title: "Launcher", dest: "settings", kind: "topic", section: "device", render },
  ];
  for (const pages of [corePages(), app]) {
    expect(destinationHome(pages, "settings")?.id).toBe("settings");
    const sections = place(pages, "settings")?.topics ?? [];
    expect(sections.map((g) => g.section)).toEqual(["device", "core"]);
    expect(place(pages, "settings")?.current).toBe("settings");
  }
  // This device's topics are General and Themes, then the app's own; every topic has a tip.
  expect(ids(place(app, "settings")?.topics[0]?.pages ?? [])).toEqual([
    "settings",
    "themes",
    "connection",
    "versions",
    "device",
    "launcher",
  ]);
  const settingsTopics = corePages().filter((p) => p.dest === "settings" && p.kind === "topic");
  expect(settingsTopics.filter((p) => p.tip === undefined).map((p) => p.id)).toEqual([]);
  expect(currentId(app, "#/settings")).toBe("settings");
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

describe("the address that opens a page", () => {
  const pages = corePages();

  test("a page's ID, with or without more after it, opens that page", () => {
    expect(currentId(pages, "#/accounts")).toBe("accounts");
    expect(currentId(pages, "#/logs/abc123")).toBe("logs");
    expect(currentId(pages, "#/config/logging")).toBe("config");
  });

  test("what follows a question mark is ignored in matching", () => {
    expect(currentId(pages, "#/status?x=1")).toBe("status");
    expect(currentId(pages, "#/accounts?search=a/b")).toBe("accounts");
    expect(currentId(pages, "#/config/source?account=x")).toBe("config");
  });

  test("an address that names a destination with no page of that ID opens the destination's first page", () => {
    // Requests (the page `usage`) is the first page of Activity, and Usage its other tab.
    expect(currentId(pages, "#/activity")).toBe("usage");
    expect(currentId(pages, "#/activity?account=x")).toBe("usage");
    expect(currentId(pages, "#/activity/usage")).toBe("activity/usage");
    expect(currentId(pages, "#/activity?trace=t1")).toBe("usage");
    // The first of Activity's pages changes when pages are added; the rule does not.
    const later: PageDef[] = [{ id: "requests", title: "Requests", dest: "activity", render }, ...pages];
    expect(currentId(later, "#/activity?failed=1")).toBe("requests");
  });

  test("an unknown address, or a destination with no page, still opens the first page", () => {
    expect(currentId(pages, "")).toBe("status");
    expect(currentId(pages, "#/nowhere")).toBe("status");
    expect(currentId(pages, "#/nowhere?x=1")).toBe("status");
    // A destination with no page in the list opens the first page.
    expect(
      currentId(
        pages.filter((p) => p.dest !== "tools"),
        "#/tools",
      ),
    ).toBe("status");
    expect(currentId(pages, "#/tools")).toBe("tools");
    expect(currentId([], "#/activity")).toBe("");
  });
});
