// The Updates page (src/updates.tsx): what each state of the update source and the updater shows, and
// what each button sends to the native side. These drive the real page against a fake window
// command channel (pageMount.tsx).
import { afterEach, expect, test } from "bun:test";
import type { Connection } from "../src/host";
import { type AppUpdateView, type ReleaseView, UpdatesPage } from "../src/updates";
import { restoreDom } from "./fakeDom";
import { mount, never } from "./pageMount";

afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
});

const SOURCE = "https://updates.example.com/feed.json";
const managed: Connection = {
  mode: "managed",
  target: "http://127.0.0.1:8317",
  signed_in: true,
  remembered: true,
  core: { state: "running", version: "0.1.0" },
};
const local: Connection = { ...managed, mode: "local" };
const remote: Connection = { ...managed, mode: "remote", target: "https://pluxy.home.example:8317" };

const latest = (version: string, over = {}) => ({
  version,
  channel: "stable" as const,
  published: "2026-11-20T10:00:00Z",
  has_notes: false,
  has_notes_url: false,
  newer: true,
  ...over,
});
const view = (over: Partial<ReleaseView> = {}): ReleaseView => ({
  source: null,
  channel: "stable",
  app: { installed: "0.1.0", latest: null },
  core: { installed: "0.1.0", latest: null },
  checked_at: null,
  problem: null,
  changing_source: false,
  ...over,
});
const checked = (over: Partial<ReleaseView> = {}) =>
  view({
    source: SOURCE,
    checked_at: 1_780_000_000,
    app: { installed: "0.1.0", latest: latest("0.2.0") },
    core: { installed: "0.1.0", latest: latest("0.2.0") },
    ...over,
  });
const NOT_CONFIGURED = "The updater is not configured: updater trust not configured";
const updater = (over: Partial<AppUpdateView> = {}): AppUpdateView => ({
  available: false,
  reason: NOT_CONFIGURED,
  version: null,
  task: { phase: "idle", downloaded: 0 },
  last: null,
  rollback: [],
  ...over,
});
const offer = updater({ available: true, reason: null, version: "0.2.0" });

const page = (c: Connection, handlers: Record<string, unknown>) =>
  mount(<UpdatesPage connection={c} />, { release_get: view(), app_update_get: updater(), ...handlers });

test("until both replies are in, the page says it is reading, and draws no section", async () => {
  const m = await page(managed, { release_get: never });
  expect(m.text()).toContain("Reading versions…");
  for (const word of ["This app", "Update source", "App update", "Versions"])
    expect(m.text()).not.toContain(word);
  // The second reply alone is not enough either.
  const n = await page(managed, { app_update_get: never });
  expect(n.text()).toContain("Reading versions…");
  expect(n.text()).not.toContain("This app");
});

test("a read that fails says so, with Try again and the native text under Details", async () => {
  const m = await page(managed, { release_get: () => Promise.reject("the update settings file is damaged") });
  expect(m.text()).toContain("Could not read the update settings");
  expect(m.text()).toContain("the update settings file is damaged");
  expect(m.has("Try again")).toBe(true);
  expect(m.text()).not.toContain("Reading versions…");
  expect(m.text()).not.toContain("This app");
});

test("with no update source, Check is off, one line says why, and App update and Release notes are not drawn", async () => {
  const m = await page(managed, {});
  expect(m.button("Check for updates").disabled).toBe(true);
  expect(m.text()).toContain("This app has no update source yet, so it cannot check for updates.");
  expect(m.text()).toContain("Versions");
  expect(m.text()).toContain("0.1.0");
  expect(m.text()).not.toContain("App update");
  expect(m.text()).not.toContain("Release notes");
  expect(m.text()).not.toContain("Update now");
  // The source can still be set, and Set source… waits for an address.
  expect(m.text()).toContain("None set");
  expect(m.button("Set source…").disabled).toBe(true);
});

test("an app that cannot update itself says so once, under This app, and draws no App update section", async () => {
  const m = await page(managed, { release_get: checked(), app_update_get: updater() });
  expect(m.text()).toContain("This copy of the app cannot update itself.");
  expect(m.text()).not.toContain("App update");
  expect(m.text()).not.toContain("updater trust");
  expect(m.text()).not.toContain("Update now");
});

test("an update on offer shows Update now with the restart line of how pluxy runs", async () => {
  for (const [c, line] of [
    [managed, "The app restarts, and the pluxy it runs stops for a moment."],
    [local, "The app restarts. pluxy keeps running."],
    [remote, "The app restarts. pluxy keeps running."],
  ] as const) {
    const m = await page(c, { release_get: checked(), app_update_get: offer });
    expect(m.text()).toContain("App update");
    expect(m.text()).toContain("Version 0.2.0 is ready to install.");
    expect(m.text()).toContain(line);
    for (const other of [
      "The app restarts, and the pluxy it runs stops for a moment.",
      "The app restarts. pluxy keeps running.",
    ])
      if (other !== line) expect(m.text()).not.toContain(other);
    await m.click("Update now");
    expect(m.commands("app_update_start").length).toBe(1);
  }
});

test("the pluxy row says where its updates come from", async () => {
  expect((await page(managed, {})).text()).toContain("Updated together with this app.");
  expect((await page(local, {})).text()).toContain("Runs on its own; update it where you installed it.");
  expect((await page(remote, {})).text()).toContain("Runs on pluxy.home.example:8317; update it there.");
  const away = await page({ ...managed, signed_in: false, target: undefined }, {});
  expect(away.text()).toContain("Not connected");
});

test("each version says how it compares with the latest, in the words of the page", async () => {
  const up = await page(managed, {
    release_get: checked({
      app: { installed: "0.2.0", latest: latest("0.2.0", { newer: false }) },
      core: { installed: "0.1.0", latest: latest("0.2.0") },
    }),
  });
  expect(up.text()).toContain("Up to date");
  expect(up.text()).toContain("Version 0.2.0 available");
  expect(up.text()).toContain("Published");
  const never_ = await page(managed, { release_get: view({ source: SOURCE }) });
  expect(never_.text()).toContain("Not checked");
  const failed = await page(managed, {
    release_get: view({ source: SOURCE, problem: { offline: false, message: "bad json" } }),
  });
  expect(failed.text()).toContain("Check failed");
  expect(failed.text()).toContain("The update source answered with something the app cannot use.");
});

test("an unreachable source is a warning that keeps the last versions and says when they are from", async () => {
  const m = await page(managed, { release_get: checked({ problem: { offline: true, message: "refused" } }) });
  expect(m.text()).toContain("Could not reach the update source. Showing what it said at ");
  expect(m.text()).toContain("Version 0.2.0 available");
});

test("a reason the updater gives is shown in plain words, and a feed's missing download is explained", async () => {
  const m = await page(managed, {
    release_get: checked(),
    app_update_get: updater({
      reason: "Version 0.2.0 has no update for this system (x86_64-unknown-linux-gnu) in the feed.",
    }),
  });
  expect(m.text()).toContain("App update");
  expect(m.text()).toContain("Version 0.2.0 has no download for this computer.");
  expect(m.text()).not.toContain("x86_64");
  const other = await page(managed, {
    release_get: checked(),
    app_update_get: updater({ reason: "this app was installed by a package manager" }),
  });
  expect(other.text()).toContain("this app was installed by a package manager");
});

test("a download shows its phase and bytes, and Cancel download asks the updater to stop", async () => {
  const m = await page(managed, {
    release_get: checked(),
    app_update_get: updater({
      reason: null,
      task: { phase: "downloading", version: "0.2.0", downloaded: 1048576, total: 4194304 },
    }),
    app_update_cancel: null,
  });
  expect(m.text()).toContain("Downloading 1.0 MB of 4.0 MB");
  await m.click("Cancel download");
  expect(m.commands("app_update_cancel").length).toBe(1);
});

test("a copy to go back to is offered with the words of the page, and sends its version and digest", async () => {
  const m = await page(managed, {
    release_get: checked(),
    app_update_get: updater({ reason: null, rollback: [{ version: "0.0.9", sha256: "ab".repeat(32) }] }),
    app_update_rollback: null,
  });
  expect(m.text()).toContain("Versions this app had before. Going back asks you to confirm.");
  await m.click("Go back to 0.0.9…");
  expect(m.commands("app_update_rollback")).toEqual([{ version: "0.0.9", sha256: "ab".repeat(32) }]);
});

test("release notes show only for a release that has notes or an address, and Open full notes sends the product", async () => {
  const none = await page(managed, { release_get: checked() });
  expect(none.text()).not.toContain("Release notes for");
  const m = await page(managed, {
    release_get: checked({
      app: { installed: "0.1.0", latest: latest("0.2.0", { has_notes_url: true }) },
    }),
    release_notes_full: null,
  });
  expect(m.text()).toContain("Release notes for 0.2.0");
  expect(m.has("Hide")).toBe(false);
  await m.click("Open full notes");
  expect(m.commands("release_notes_full")).toEqual([{ product: "app" }]);
});

test("Check for updates sends the check and reads the updater again", async () => {
  const m = await page(managed, {
    release_get: checked(),
    release_check: checked({ checked_at: 1_780_000_100 }),
  });
  const before = m.commands("app_update_get").length;
  await m.click("Check for updates");
  expect(m.commands("release_check").length).toBe(1);
  expect(m.commands("app_update_get").length).toBe(before + 1);
});

test("a channel is set at once, and is off while a source change waits for its confirmation", async () => {
  const m = await page(managed, {
    release_get: checked(),
    release_channel_set: checked({ channel: "preview" }),
  });
  const preview = m.radios().find((i) => i.value === "preview");
  expect(preview).toBeDefined();
  await m.fire(preview as never, "change");
  expect(m.commands("release_channel_set")).toEqual([{ channel: "preview" }]);
  const waiting = await page(managed, { release_get: checked({ changing_source: true }) });
  expect(waiting.text()).toContain("Answer the confirmation window pluxy opened.");
  expect(waiting.button("Change source…").disabled).toBe(true);
  expect(waiting.button("Remove source…").disabled).toBe(true);
  expect(waiting.radios().length).toBe(2);
  expect(waiting.radios().every((i) => i.disabled)).toBe(true);
});

test("Set source… sends the typed address, and one the native side refuses keeps what was typed", async () => {
  const m = await page(managed, {
    release_get: view(),
    release_source_set: null,
  });
  await m.input("Address", "https://example.org/feed.json");
  expect(m.button("Set source…").disabled).toBe(false);
  await m.click("Set source…");
  expect(m.commands("release_source_set")).toEqual([{ source: "https://example.org/feed.json" }]);
  // The confirmation is settled as refused: the page says why and the field still holds the address.
  await m.emit("pluxy-release", { changed: false, error: "the address must start with https://" });
  expect(m.text()).toContain("The update source was not changed: the address must start with https://");
  expect(m.field("release-source-input")?.value).toBe("https://example.org/feed.json");
});

test("a source that is set says so, can be changed or removed, and Remove source… sends none", async () => {
  const m = await page(managed, { release_get: checked(), release_source_set: null });
  expect(m.text()).toContain(SOURCE);
  expect(m.has("Set source…")).toBe(false);
  await m.click("Remove source…");
  expect(m.commands("release_source_set")).toEqual([{ source: null }]);
});
