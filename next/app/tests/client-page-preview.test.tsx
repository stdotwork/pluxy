// The tool pages' file preview: a read of the settings file that answers after the person changed
// the path is dropped, so the page's target is always the file its field shows and Connect never
// writes to another. These drive the real pages, all eight, through Preact's DOM renderer against a
// fake window command channel (pageHarness.tsx). Each page reads its default file as it opens, and
// names the other files under "Settings file, key and backups", read with Read this file.
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
  /** The choice that makes the file or folder a typed path, and that field's label. */
  pick: string;
  field: string;
  /** The button that writes the settings. */
  write: string;
  /** Every file the tool's preview pins for the path typed in the field, as its add-on gives them. */
  targets: (path: string) => Pin[];
  /** The tool's reading of the settings in its answer to a read, apart from the pins. */
  reading: unknown;
}

interface Pin {
  selected: string;
  target: string;
  link: string | null;
  path?: string;
  root?: string;
}

// Each typed path resolves somewhere else (as a link the add-on follows and keeps), so a target
// on screen or in a write can only have come from the reading, never from the field.
const REAL = "/real";
/** A typed file: one pin, its link followed. */
const fileTarget = (path: string): Pin[] => [
  { selected: path, target: `${REAL}${path}`, link: `${REAL}${path}` },
];
/** A typed folder holding the add-on's files: one pin per file, each a path in the folder. */
const folderTargets =
  (...files: string[]) =>
  (root: string): Pin[] =>
    files.map((path) => ({
      selected: root,
      path,
      root: `${REAL}${root}`,
      target: `${REAL}${root}/${path}`,
      link: null,
    }));
const targetsOf = (c: PageCase, path: string) => c.targets(path).map((t) => t.target);
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
    targets: fileTarget,
    page: () => <ClaudeCodePage />,
    pick: "A file",
    field: "File",
    write: "Connect Claude Code",
    reading: {
      applied: false,
      settings: { ...unconfigured, startup_model: null },
    },
  },
  {
    name: "Claude Desktop",
    targets: folderTargets(
      "claude_desktop_config.json",
      "configLibrary/00000000-0000-4000-8000-000000831700.json",
      "configLibrary/_meta.json",
    ),
    page: () => <ClaudeDesktopPage />,
    pick: "In a folder",
    field: "Folder",
    write: "Connect Claude Desktop",
    reading: { applied: false, settings: unconfigured },
  },
  {
    name: "Codex",
    targets: folderTargets("config.toml", "pluxy-model-catalog.json"),
    page: () => <CodexPage />,
    pick: "In a folder",
    field: "Folder",
    write: "Connect Codex",
    reading: {
      applied: false,
      settings: { ...codexRead, model: "gpt-6.1-sol" },
    },
  },
  {
    name: "Grok Build",
    targets: fileTarget,
    page: () => <GrokPage />,
    pick: "A file",
    field: "File",
    write: "Connect Grok Build",
    reading: {
      applied: false,
      settings: {
        ...unconfigured,
        model: "gpt-6.1-sol",
        models: [{ model: "gpt-6.1-sol", name: null, context_window: null }],
      },
    },
  },
  {
    name: "Hermes Agent",
    targets: fileTarget,
    page: () => <HermesPage />,
    pick: "A file",
    field: "File",
    write: "Connect Hermes Agent",
    reading: {
      applied: false,
      config: { ...unconfigured, model: "gpt-6.1-sol" },
    },
  },
  {
    name: "Oh My Pi",
    targets: folderTargets("models.yml", "config.yml"),
    page: () => <OmpPage />,
    pick: "In a folder",
    field: "Folder",
    write: "Connect Oh My Pi",
    reading: {
      applied: false,
      config: { ...unconfigured, model: "gpt-6.1-sol" },
    },
  },
  {
    name: "OpenCode",
    targets: fileTarget,
    page: () => <OpenCodePage />,
    pick: "A file",
    field: "File",
    write: "Connect OpenCode",
    reading: {
      applied: false,
      config: {
        ...unconfigured,
        model: "gpt-6.1-sol",
        models: [{ id: "gpt-6.1-sol", name: "", attachment: false }],
      },
    },
  },
  {
    name: "Pi",
    targets: folderTargets("cliproxyapi.json", "settings.json", "cliproxyapi-models.json"),
    page: () => <PiPage />,
    pick: "In a folder",
    field: "Folder",
    write: "Connect Pi",
    reading: {
      applied: false,
      pi: {
        ...unconfigured,
        model: "gpt-6.1-sol",
        adapter: { installed: true, version: "1.0.0", bundled: "1.0.0", update: false, foreign: false },
      },
    },
  },
];

/** The tool's whole answer to a read of path: its reading, with every file its add-on pins. */
const answerOf = (c: PageCase, path: string) => ({ targets: c.targets(path), ...(c.reading as object) });

const A = "/home/me/A.json";
const B = "/home/me/B.json";
const REFUSAL = "disk exploded";

/** Mounts the page and makes its file the typed path A, reading nothing yet. */
async function typedA(c: PageCase) {
  const ch = channel();
  const page = await mount(c.page, ch.invoke);
  await page.open(MORE);
  await page.pick(c.pick);
  await page.type(c.field, A);
  return { ch, page };
}

/** Reads path A on the page, then edits the field to B while the read runs. */
async function readThenEdit(c: PageCase) {
  const { ch, page } = await typedA(c);
  const before = ch.reads.length;
  await page.click("Read this file");
  expect(ch.reads.length).toBe(before + 1);
  await page.type(c.field, B);
  await settle();
  return { ch, page, read: ch.reads[before] };
}

/** Presses the page's write if it offers one it can press, and gives every target it wrote. */
async function writeIfOffered(c: PageCase, ch: Channel, page: Mounted) {
  if (page.canPress(c.write)) await page.click(c.write);
  return ch.writes
    .filter((w) => w.op === "apply")
    .flatMap((w) => w.input.targets?.map((t) => t.target) ?? []);
}

for (const c of pages) {
  test(`${c.name}: a read of the path the field shows is kept`, async () => {
    const { ch, page } = await typedA(c);
    await page.click("Read this file");
    ch.pending()?.answer(answerOf(c, A));
    await settle();
    // Every file the reading pinned is named under "What Connect changes", and Connect writes to
    // all of them.
    for (const target of targetsOf(c, A)) expect(page.text()).toContain(target);
    expect(await writeIfOffered(c, ch, page)).toEqual(targetsOf(c, A));
  });

  test(`${c.name}: a failed read of the path the field shows is shown`, async () => {
    const { ch, page } = await typedA(c);
    await page.click("Read this file");
    ch.pending()?.fail(REFUSAL);
    await settle();
    // The page starts the native reason with a capital.
    expect(page.text().toLowerCase()).toContain(REFUSAL);
  });

  test(`${c.name}: a read that answers after the path changed is dropped`, async () => {
    const { page, read } = await readThenEdit(c);
    read?.answer(answerOf(c, A));
    await settle();
    expect(page.shown(c.field)).toBe(B);
    for (const target of targetsOf(c, A)) expect(page.text()).not.toContain(target);
  });

  test(`${c.name}: the write never goes to a file the field no longer shows`, async () => {
    const { ch, page, read } = await readThenEdit(c);
    read?.answer(answerOf(c, A));
    await settle();
    // Before the pages kept the file they read, the late answer pinned A: the write was offered,
    // for A, beside a field showing B. Now nothing is offered until B is read; then the write goes to B.
    const sent = await writeIfOffered(c, ch, page);
    expect(sent).toEqual([]);
    await page.click("Read this file");
    ch.pending()?.answer(answerOf(c, B));
    await settle();
    expect(page.canPress(c.write)).toBe(true);
    expect(await writeIfOffered(c, ch, page)).toEqual(targetsOf(c, B));
  });

  test(`${c.name}: a failed read that answers after the path changed is dropped`, async () => {
    const { page, read } = await readThenEdit(c);
    read?.fail(REFUSAL);
    await settle();
    expect(page.shown(c.field)).toBe(B);
    expect(page.text().toLowerCase()).not.toContain(REFUSAL);
  });
}

for (const c of pages) {
  test(`${c.name}: the default file's read, answering after another file was chosen, is dropped`, async () => {
    const ch = channel();
    const page = await mount(c.page, ch.invoke);
    // The default file is read as the page opens.
    expect(ch.reads.length).toBe(1);
    const first = ch.reads[0];
    await page.open(MORE);
    await page.pick(c.pick);
    first?.answer(answerOf(c, "/home/me/default"));
    await settle();
    for (const target of targetsOf(c, "/home/me/default")) expect(page.text()).not.toContain(target);
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
