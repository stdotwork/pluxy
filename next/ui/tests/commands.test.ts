// What the command menu offers and how it finds it (kit/commands.ts), on the pages core offers and
// a few accounts. The expected results are written out by hand.
import { describe, expect, test } from "bun:test";
import type { Account } from "../kit/api";
import {
  accountCommands,
  arrange,
  type Command,
  fold,
  MAX_PER_GROUP,
  MAX_RECENTS,
  marks,
  openKeys,
  opensMenu,
  pageCommands,
  pushRecent,
  rank,
  readRecents,
  step,
} from "../kit/commands";
import { DEST_ICONS, destinationLabel } from "../kit/frame";
import { corePages } from "../kit/pages/core";

const NOW = Date.parse("2026-11-27T12:00:00Z");
const pages = pageCommands(corePages(), DEST_ICONS, destinationLabel);
const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  source: "login",
  state: "active",
  refreshable: false,
  extension: "pluxy/claude",
  ...more,
});
const accounts = accountCommands(
  [
    account("studio-max", { label: "Studio Max" }),
    account("pro-main", { extension: "pluxy/codex", disabled: true }),
    account("work@example.com", { label: "work@example.com" }),
  ],
  // The name pages show: the third is masked, as in privacy mode.
  (a) => (a.id === "work@example.com" ? "wo***@e***.com" : (a.label ?? a.id)),
  NOW,
);
const everything = [...pages, ...accounts];
const labels = (list: Command[]) => list.map((c) => c.label);

describe("what it lists", () => {
  test("every destination opens its first page, and every tab and topic is listed with where it is", () => {
    const own = pages.filter((c) => c.hint === undefined);
    expect(labels(own)).toEqual(["Home", "Accounts", "Activity", "Tools", "Settings"]);
    expect(own.map((c) => c.href)).toEqual(["#/status", "#/accounts", "#/usage", "#/tools", "#/settings"]);
    const tab = pages.find((c) => c.id === "page:routing");
    expect(tab).toMatchObject({
      label: "Order & fallback",
      hint: "Accounts",
      kind: "page",
      href: "#/routing",
    });
  });

  test("a topic of Settings is a settings command, every other tab or topic is a page", () => {
    const settings = pages.filter((c) => c.kind === "setting");
    expect(labels(settings)).toEqual([
      "General",
      "Themes",
      "Config",
      "Extensions",
      "Logs",
      "System",
      "Setup",
    ]);
    expect(settings.every((c) => c.hint === "Settings" && c.icon === "settings")).toBe(true);
    expect(labels(pages.filter((c) => c.kind === "page" && c.hint !== undefined))).toEqual([
      "All accounts",
      "Order & fallback",
      "Requests",
      "Usage",
    ]);
  });

  test("a page reached from another page is not listed: accounts are by name, adding is an action", () => {
    const hrefs = pages.map((c) => c.href);
    expect(hrefs).not.toContain("#/account");
    expect(hrefs).not.toContain("#/add-account");
  });

  test("an account is listed by the name pages show, opens its page, and says its provider and condition", () => {
    expect(accounts.map((c) => [c.label, c.hint, c.href])).toEqual([
      ["Studio Max", "Claude · Serving", "#/accounts/studio-max"],
      ["pro-main", "Codex · Turned off", "#/accounts/pro-main"],
      ["wo***@e***.com", "Claude · Serving", "#/accounts/work%40example.com"],
    ]);
  });
});

describe("how a query finds things", () => {
  test("every word must be found, and the start of the label beats a word in it, which beats the middle", () => {
    const c = (label: string, extra: Partial<Command> = {}): Command => ({
      id: label,
      kind: "page",
      label,
      icon: "home",
      ...extra,
    });
    expect(rank("acc", c("Accounts"))).toBe(0);
    expect(rank("acc", c("Add an account"))).toBe(1);
    expect(rank("ount", c("Accounts"))).toBe(2);
    expect(rank("claude", c("Studio Max", { hint: "Claude · Serving" }))).toBe(3);
    expect(rank("add acc", c("Add an account"))).toBe(1);
    expect(rank("add xyz", c("Add an account"))).toBeUndefined();
    expect(rank("", c("Accounts"))).toBe(0);
  });

  test("accents and capitals do not matter", () => {
    expect(fold("Café")).toBe("cafe");
    expect(rank("CAFE", { id: "a", kind: "page", label: "Café", icon: "home" })).toBe(0);
  });

  test("it marks what was found in a name, and nothing when the folded text has another length", () => {
    expect(marks("Add an account", "add acc")).toEqual([
      [0, 3],
      [7, 10],
    ]);
    // "acc" is at 0-3 and "cou" at 2-5: they run together.
    expect(marks("Accounts", "acc cou")).toEqual([[0, 5]]);
    expect(marks("Accounts", "")).toEqual([]);
    expect(marks("Straße", "stra")).toEqual([[0, 4]]);
    expect(marks("ﬁx", "fi")).toEqual([]);
  });
});

describe("what the menu shows", () => {
  const act = (id: string, label: string, more: Partial<Command> = {}): Command => ({
    id: `action:${id}`,
    kind: "action",
    label,
    icon: "plus",
    suggested: true,
    ...more,
  });
  const all = [
    ...everything,
    act("add-account", "Add an account"),
    act("appearance", "Switch to dark appearance"),
  ];

  test("with nothing typed: the destinations and the actions, not the tabs, the settings or the accounts", () => {
    const groups = arrange("", all, []);
    expect(groups.map((g) => g.id)).toEqual(["pages", "actions"]);
    expect(labels(groups[0]?.items ?? [])).toEqual(["Home", "Accounts", "Activity", "Tools", "Settings"]);
    expect(labels(groups[1]?.items ?? [])).toEqual(["Add an account", "Switch to dark appearance"]);
    expect(arrange("   ", all, [])).toEqual(groups);
  });

  test("with nothing typed, the recents come first, once, in the order they were last used", () => {
    const groups = arrange("", all, ["account:studio-max", "page:routing", "page:gone", "page:status:home"]);
    expect(groups.map((g) => g.id)).toEqual(["recent", "pages", "actions"]);
    expect(labels(groups[0]?.items ?? [])).toEqual(["Studio Max", "Order & fallback", "Home"]);
    expect(labels(groups[1]?.items ?? [])).toEqual(["Accounts", "Activity", "Tools", "Settings"]);
  });

  test("a query shows each group's matches, and the group with the best match comes first", () => {
    const groups = arrange("acc", all, []);
    expect(groups.map((g) => g.id)).toEqual(["pages", "actions"]);
    expect(labels(groups[0]?.items ?? [])).toEqual(["Accounts", "All accounts", "Order & fallback"]);
    expect(labels(groups[1]?.items ?? [])).toEqual(["Add an account"]);
    // An account whose name starts with the query is as good as a page that does: pages go first on a tie.
    const both = [...all, ...accountCommands([account("accra")], (a) => a.id, NOW)];
    expect(arrange("acc", both, []).map((g) => g.id)).toEqual(["pages", "accounts", "actions"]);
    // A better match in a later group takes the lead, so Enter opens what was most likely meant.
    const lead = [
      { id: "page:a", kind: "page" as const, label: "Open the dark room", icon: "home" as const },
      act("dark-mode", "Dark mode"),
    ];
    expect(arrange("dark", lead, []).map((g) => g.id)).toEqual(["actions", "pages"]);
    // "dark" is the start of a word of an action, and nothing else's label has it.
    expect(arrange("dark", all, []).map((g) => g.id)).toEqual(["actions"]);
  });

  test("an account is found by the name it shows, and by the real name behind a masked one", () => {
    expect(labels(arrange("studio", all, []).flatMap((g) => g.items))).toEqual(["Studio Max"]);
    expect(labels(arrange("work@example", all, []).flatMap((g) => g.items))).toEqual(["wo***@e***.com"]);
    expect(labels(arrange("codex", all, []).flatMap((g) => g.items))).toEqual(["pro-main"]);
  });

  test("a group shows at most a few matches and says how many more there are", () => {
    const many = Array.from({ length: 12 }, (_, i) => ({
      ...accounts[0],
      id: `account:team-${i}`,
      label: `team-${i}`,
    })) as Command[];
    const [group] = arrange("team", many, []);
    expect(group?.items).toHaveLength(MAX_PER_GROUP);
    expect(group?.more).toBe(12 - MAX_PER_GROUP);
  });

  test("nothing matching is no groups", () => {
    expect(arrange("zzzq", all, [])).toEqual([]);
  });
});

describe("moving, and the recents kept", () => {
  test("arrow keys go round at the ends, and an empty list stays at the first", () => {
    expect(step(0, 1, 3)).toBe(1);
    expect(step(2, 1, 3)).toBe(0);
    expect(step(0, -1, 3)).toBe(2);
    expect(step(0, 1, 0)).toBe(0);
  });

  test("the recents are the last few used, newest first, each once", () => {
    let list: string[] = [];
    for (const id of ["a", "b", "c", "a", "d", "e", "f"]) list = pushRecent(list, id);
    expect(list).toEqual(["f", "e", "d", "a", "c"]);
    expect(list).toHaveLength(MAX_RECENTS);
  });

  test("what was stored is read back, and anything that is not a list of texts is none", () => {
    expect(readRecents(JSON.stringify(["a", "b"]))).toEqual(["a", "b"]);
    expect(readRecents(JSON.stringify(["a", 3, null, "b"]))).toEqual(["a", "b"]);
    expect(readRecents(null)).toEqual([]);
    expect(readRecents("not json")).toEqual([]);
    expect(readRecents('{"a":1}')).toEqual([]);
  });
});

describe("the key that opens it", () => {
  const press = (
    key: string,
    mods: Partial<Record<"ctrlKey" | "metaKey" | "altKey" | "shiftKey", boolean>>,
  ) => ({
    key,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...mods,
  });

  test("Command K on an Apple computer, Control K elsewhere, and no other chord", () => {
    expect(opensMenu(press("k", { metaKey: true }), true)).toBe(true);
    expect(opensMenu(press("K", { metaKey: true }), true)).toBe(true);
    expect(opensMenu(press("k", { ctrlKey: true }), true)).toBe(false);
    expect(opensMenu(press("k", { ctrlKey: true }), false)).toBe(true);
    expect(opensMenu(press("k", { metaKey: true }), false)).toBe(false);
    expect(opensMenu(press("k", {}), false)).toBe(false);
    expect(opensMenu(press("k", { ctrlKey: true, shiftKey: true }), false)).toBe(false);
    expect(opensMenu(press("j", { ctrlKey: true }), false)).toBe(false);
  });

  test("the Search field shows the keys of this computer", () => {
    expect(openKeys(true)).toEqual(["⌘", "K"]);
    expect(openKeys(false)).toEqual(["Ctrl", "K"]);
  });
});
