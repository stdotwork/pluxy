// The tool pages' file preview: a read of the settings file that answers after the person changed
// the path is dropped, so the page's target is always the file its field shows and Connect (Apply,
// on the pages not yet on the shared parts) never writes to another. These drive the real pages
// through Preact's DOM renderer against a fake window command channel (pageHarness.tsx). The pages
// on the shared parts read their default file as they open, and name the other files under
// "Settings file, key and backups"; the others read only when Refresh is pressed.
import { expect, test } from "bun:test";
import type { VNode } from "preact";
import { ClaudeCodePage } from "../src/claudeCode";
import { ClaudeDesktopPage } from "../src/claudeDesktop";
import { CodexPage } from "../src/codex";
import { GrokPage } from "../src/grok";
import { HermesPage } from "../src/hermes";
import { OmpPage } from "../src/omp";
import { OpenCodePage } from "../src/opencode";
import { PiPage } from "../src/pi";
import { type Channel, channel, later, type Mounted, mount, settle } from "./pageHarness";

const MORE = "Settings file, key and backups";

/** A page, the way to make its file a typed path, and the tool's preview of that path. */
interface PageCase {
  name: string;
  page: () => VNode;
  /** On the shared parts: the file choice is under MORE, and the write is Connect. */
  shared: boolean;
  /** The choice that makes the file or folder a typed path, and that field's label. */
  pick: string;
  field: string;
  read: string;
  /** The button that writes the settings. */
  write: string;
  /** The tool's answer to a read of the path typed in the field. */
  body: (path: string) => unknown;
}

const pinned = (path: string) => [{ selected: path, target: `${path}/target.json`, link: null }];
const unconfigured = { state: "not-configured" };
const codexRead = {
  ...unconfigured,
  key_set: false,
  provider: null,
  model: null,
  profile: null,
  remote_compaction: false,
  agents: { enabled: false, max_threads: null, model: "", effort: "", interrupt: false },
  models: [
    {
      slug: "gpt-6.1-sol",
      display_name: "GPT 6.1",
      description: "",
      context_window: null,
      max_context_window: null,
      effective_context_window_percent: null,
      auto_compact_token_limit: null,
      reasoning: [],
      default_reasoning: "",
      input_modalities: ["text"],
      visible: true,
      tool_search: false,
    },
  ],
  approval_model: "",
};

const pages: PageCase[] = [
  {
    name: "Claude Code",
    page: () => <ClaudeCodePage />,
    shared: true,
    pick: "A file",
    field: "File",
    read: "Read this file",
    write: "Connect Claude Code",
    body: (path) => ({
      targets: pinned(path),
      applied: false,
      settings: { ...unconfigured, startup_model: null },
    }),
  },
  {
    name: "Claude Desktop",
    page: () => <ClaudeDesktopPage />,
    shared: true,
    pick: "In a folder",
    field: "Folder",
    read: "Read this file",
    write: "Connect Claude Desktop",
    body: (path) => ({ targets: pinned(path), applied: false, settings: unconfigured }),
  },
  {
    name: "Codex",
    page: () => <CodexPage />,
    shared: true,
    pick: "In a folder",
    field: "Folder",
    read: "Read this file",
    write: "Connect Codex",
    body: (path) => ({
      targets: pinned(path),
      applied: false,
      settings: { ...codexRead, model: "gpt-6.1-sol" },
    }),
  },
  {
    name: "Grok Build",
    page: () => <GrokPage />,
    shared: false,
    pick: "A selected file",
    field: "File",
    read: "Refresh",
    write: "Apply",
    body: (path) => ({ targets: pinned(path), applied: false, settings: unconfigured }),
  },
  {
    name: "Hermes Agent",
    page: () => <HermesPage />,
    shared: false,
    pick: "A selected config file",
    field: "Config file",
    read: "Refresh",
    write: "Apply",
    body: (path) => ({ targets: pinned(path), applied: false, config: unconfigured }),
  },
  {
    name: "Oh My Pi",
    page: () => <OmpPage />,
    shared: false,
    pick: "A selected agent folder",
    field: "Folder",
    read: "Refresh",
    write: "Apply",
    body: (path) => ({ targets: pinned(path), applied: false, config: unconfigured }),
  },
  {
    name: "OpenCode",
    page: () => <OpenCodePage />,
    shared: false,
    pick: "A selected file",
    field: "File",
    read: "Refresh",
    write: "Apply",
    body: (path) => ({ targets: pinned(path), applied: false, config: unconfigured }),
  },
  {
    name: "Pi",
    page: () => <PiPage />,
    shared: false,
    pick: "A selected agent folder",
    field: "Folder",
    read: "Refresh",
    write: "Apply",
    body: (path) => ({
      targets: pinned(path),
      applied: false,
      pi: {
        ...unconfigured,
        adapter: { installed: false, version: null, bundled: "1.0.0", update: false, foreign: false },
      },
    }),
  },
];

const A = "/home/me/A.json";
const B = "/home/me/B.json";
const REFUSAL = "disk exploded";

/** Mounts the page and makes its file the typed path A, reading nothing yet. */
async function typedA(c: PageCase) {
  const ch = channel();
  const page = await mount(c.page, ch.invoke);
  if (c.shared) await page.open(MORE);
  await page.pick(c.pick);
  await page.type(c.field, A);
  return { ch, page };
}

/** Reads path A on the page, then edits the field to B while the read runs. */
async function readThenEdit(c: PageCase) {
  const { ch, page } = await typedA(c);
  const before = ch.reads.length;
  await page.click(c.read);
  expect(ch.reads.length).toBe(before + 1);
  await page.type(c.field, B);
  await settle();
  return { ch, page, read: ch.reads[before] };
}

/** Presses the page's write if it offers one it can press (choosing the vault's key where the page
 * asks for one), and gives every target it wrote. */
async function writeIfOffered(c: PageCase, ch: Channel, page: Mounted) {
  if (!c.shared && page.hasButton(c.write)) await page.pick("My key");
  if (page.canPress(c.write)) await page.click(c.write);
  return ch.writes
    .filter((w) => w.op === "apply")
    .flatMap((w) => w.input.targets?.map((t) => t.target) ?? []);
}

for (const c of pages) {
  test(`${c.name}: a read of the path the field shows is kept`, async () => {
    const { ch, page } = await typedA(c);
    await page.click(c.read);
    ch.pending()?.answer(c.body(A));
    await settle();
    expect(page.text()).toContain(`${A}/target.json`);
    // Connect writes to it (the older pages' Apply needs more than a key; Claude Code's below).
    if (c.shared) expect(await writeIfOffered(c, ch, page)).toEqual([`${A}/target.json`]);
  });

  test(`${c.name}: a failed read of the path the field shows is shown`, async () => {
    const { ch, page } = await typedA(c);
    await page.click(c.read);
    ch.pending()?.fail(REFUSAL);
    await settle();
    // The pages on the shared parts start the native reason with a capital.
    expect(page.text().toLowerCase()).toContain(REFUSAL);
  });

  test(`${c.name}: a read that answers after the path changed is dropped`, async () => {
    const { page, read } = await readThenEdit(c);
    read?.answer(c.body(A));
    await settle();
    expect(page.shown(c.field)).toBe(B);
    expect(page.text()).not.toContain(`${A}/target.json`);
  });

  test(`${c.name}: the write never goes to a file the field no longer shows`, async () => {
    const { ch, page, read } = await readThenEdit(c);
    read?.answer(c.body(A));
    await settle();
    // On the old pages the late answer pinned A: the write was offered, for A, beside a field showing B.
    const sent = await writeIfOffered(c, ch, page);
    expect(sent.filter((target) => target !== `${B}/target.json`)).toEqual([]);
    if (!c.shared) return;
    // On the shared pages, nothing was offered until B is read; then the write goes to B.
    expect(sent).toEqual([]);
    await page.click(c.read);
    ch.pending()?.answer(c.body(B));
    await settle();
    expect(page.canPress(c.write)).toBe(true);
    expect(await writeIfOffered(c, ch, page)).toEqual([`${B}/target.json`]);
  });

  test(`${c.name}: a failed read that answers after the path changed is dropped`, async () => {
    const { page, read } = await readThenEdit(c);
    read?.fail(REFUSAL);
    await settle();
    expect(page.shown(c.field)).toBe(B);
    expect(page.text().toLowerCase()).not.toContain(REFUSAL);
  });
}

for (const c of pages.filter((p) => p.shared)) {
  test(`${c.name}: the default file's read, answering after another file was chosen, is dropped`, async () => {
    const ch = channel();
    const page = await mount(c.page, ch.invoke);
    // The default file is read as the page opens.
    expect(ch.reads.length).toBe(1);
    const first = ch.reads[0];
    await page.open(MORE);
    await page.pick(c.pick);
    first?.answer(c.body("/home/me/default"));
    await settle();
    expect(page.text()).not.toContain("/home/me/default/target.json");
    expect(page.canPress(c.write)).toBe(false);
  });
}

test("Claude Code: a failed read for a file no longer shown does not say the add-on is missing", async () => {
  const { page, read } = await readThenEdit(pages[0] as PageCase);
  read?.fail("pluxy/claude-code is not installed on this device");
  await settle();
  expect(page.text()).not.toContain("is not installed on this device");
  expect(page.text()).not.toContain("needs the Claude Code add-on");
  expect(page.text()).not.toContain("Could not read");
});

const UBUNTU = "Ubuntu";
const DEBIAN = "Debian";
const SETTINGS = "/home/me/.claude/settings.json";
const claudeCodeRead = (path: string) => ({
  targets: [{ selected: path, target: path, link: null }],
  applied: false,
  settings: { state: "not-configured", startup_model: null },
});

/** Claude Code on Windows with WSL offered; a read answers with the same settings file in any
 * environment, so a write is told apart only by the environment it is sent to. */
async function wslPage() {
  const ch = channel({ wsl: { distros: [UBUNTU, DEBIAN] } });
  const page = await mount(() => <ClaudeCodePage />, ch.invoke);
  /** Answers the read the page started for its default file, or reads it now. */
  const read = async () => {
    if (!ch.pending()) await page.click("Read this file");
    ch.pending()?.answer(claudeCodeRead(SETTINGS));
    await settle();
  };
  /** Presses Connect if the page offers it. */
  const connect = async () => {
    if (page.canPress("Connect Claude Code")) await page.click("Connect Claude Code");
  };
  return { ch, page, read, connect };
}

test("Claude Code: choosing WSL with no user offers no write to this computer", async () => {
  const { ch, page, read, connect } = await wslPage();
  await read();
  expect(page.text()).toContain("Writes to");
  await page.pick("WSL");
  await connect();
  expect(ch.writes.filter((w) => w.op === "apply")).toEqual([]);
  expect(page.text()).not.toContain("Writes to");
});

test("Claude Code: choosing another distribution with no user offers no write to the old one", async () => {
  const { ch, page, read, connect } = await wslPage();
  const before = ch.reads.length;
  await page.pick("WSL");
  await page.pick(UBUNTU);
  await page.pick("me");
  await read();
  expect(ch.reads.slice(before).map((r) => r.environment)).toEqual([{ wsl: { distro: UBUNTU, user: "me" } }]);
  expect(page.text()).toContain("Writes to");
  await page.pick(DEBIAN);
  await connect();
  expect(ch.writes.filter((w) => w.op === "apply")).toEqual([]);
  expect(page.text()).not.toContain("Writes to");
});

test("Claude Code: a user picked in the distribution shown is written to, and only that one", async () => {
  const { ch, page, read, connect } = await wslPage();
  await page.pick("WSL");
  await page.pick(UBUNTU);
  await page.pick("me");
  await read();
  await page.pick(DEBIAN);
  await page.pick("me");
  await read();
  await connect();
  expect(ch.writes.filter((w) => w.op === "apply").map((w) => w.environment)).toEqual([
    { wsl: { distro: DEBIAN, user: "me" } },
  ]);
});

test("Claude Code: while a write waits, where Claude Code runs cannot be changed", async () => {
  const answer = later<unknown>();
  const ch = channel({ wsl: { distros: [UBUNTU, DEBIAN] }, ops: { apply: () => answer.promise } });
  const page = await mount(() => <ClaudeCodePage />, ch.invoke);
  await page.pick("WSL");
  await page.pick(UBUNTU);
  await page.pick("me");
  if (!ch.pending()) await page.click("Read this file");
  ch.pending()?.answer(claudeCodeRead(SETTINGS));
  await settle();
  await page.click("Connect Claude Code");
  await expect(page.pick("Windows")).rejects.toThrow("is disabled");
  await expect(page.pick(DEBIAN)).rejects.toThrow("is disabled");
  answer.give({});
  await settle();
  expect(ch.writes.filter((w) => w.op === "apply").map((w) => w.environment)).toEqual([
    { wsl: { distro: UBUNTU, user: "me" } },
  ]);
  await page.pick(DEBIAN);
});

test("Claude Code: in privacy mode, WSL users' names are masked", async () => {
  const ch = channel({ wsl: { distros: [UBUNTU], users: ["alice"] } });
  const page = await mount(() => <ClaudeCodePage />, ch.invoke, undefined, [], { privacy: true });
  await page.pick("WSL");
  await page.pick(UBUNTU);
  expect(page.text()).toContain("User");
  expect(page.text()).not.toContain("alice");
});
