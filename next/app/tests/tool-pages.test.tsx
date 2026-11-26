// The tool pages on the shared parts (clientPage.tsx: Claude Code, Codex, Claude Desktop), driven
// as a person would through the fake window channel (pageHarness.tsx): what the head says and
// offers in each state, the one-press Connect and its key, the questions only the page asks, the
// model lines, and the words no page may use.
import { describe, expect, test } from "bun:test";
import type { VNode } from "preact";
import { act } from "preact/test-utils";
import { ClaudeCodePage } from "../src/claudeCode";
import { ClaudeDesktopPage } from "../src/claudeDesktop";
import { MISSING_LIBRARY } from "../src/claudeDesktopForm";
import { readExtraTools } from "../src/clientPage";
import { CodexPage } from "../src/codex";
import {
  ADDONS,
  all,
  type ChannelOptions,
  CORE,
  catalogOf,
  channel,
  installWindow,
  later,
  mount,
  settle,
} from "./pageHarness";

const SETTINGS = "/home/me/.claude/settings.json";
const pin = (target: string) => [{ selected: target, target, link: null }];

/** Claude Code's reading of its default file, in state, with the person's models. */
const claudeCode = (state: string, more: Record<string, unknown> = {}, applied = false) => ({
  targets: pin(SETTINGS),
  applied,
  settings: {
    state,
    startup_model: null,
    opus: "claude-opus-5-5",
    sonnet: "claude-sonnet-5-5",
    haiku: "claude-haiku-5-5",
    ...more,
  },
});

const codexModel = (slug: string) => ({
  slug,
  display_name: slug,
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
});
const codex = (state: string, applied = false) => ({
  targets: pin("/home/me/.codex/config.toml"),
  applied,
  settings: {
    state,
    key_set: false,
    provider: null,
    model: "gpt-6.1-sol",
    profile: null,
    remote_compaction: false,
    agents: { enabled: true, max_threads: null, model: "", effort: "", interrupt: true },
    models: [codexModel("gpt-6.1-sol")],
    approval_model: "",
  },
});
const desktop = (state: string, applied = false) => ({
  targets: pin("/home/me/.config/Claude-3p/claude_desktop_config.json"),
  applied,
  settings: {
    state,
    key_set: state === "configured",
    models: [{ model: "claude-opus-5-5", alias: "", one_m: false }],
  },
});

interface Tool {
  name: string;
  pkg: string;
  page: () => VNode;
  read: (state: string, applied?: boolean) => unknown;
}

const TOOLS: Tool[] = [
  {
    name: "Claude Code",
    pkg: "pluxy/claude-code",
    page: () => <ClaudeCodePage />,
    read: (s, a) => claudeCode(s, {}, a),
  },
  { name: "Codex", pkg: "pluxy/codex-setup", page: () => <CodexPage />, read: codex },
  { name: "Claude Desktop", pkg: "pluxy/claude-desktop", page: () => <ClaudeDesktopPage />, read: desktop },
];

/** The page open, its default file read as state. */
async function opened(tool: Tool, state: string, o: ChannelOptions = {}, applied = false) {
  const ch = channel(o);
  const page = await mount(tool.page, ch.invoke);
  expect(ch.reads.length).toBe(1);
  ch.reads[0]?.answer(tool.read(state, applied));
  await settle();
  return { ch, page };
}

const applies = (ch: ReturnType<typeof channel>) => ch.writes.filter((w) => w.op === "apply");
const captures = (ch: ReturnType<typeof channel>) => ch.calls.filter((c) => c.cmd === "client_key_capture");

describe("Connect and its key", () => {
  for (const tool of TOOLS) {
    test(`${tool.name}: with no key for it here, Connect makes one named for the tool, then writes with it`, async () => {
      const { ch, page } = await opened(tool, "not-configured");
      await page.click(`Connect ${tool.name}`);
      expect(captures(ch).map((c) => c.args)).toEqual([{ label: tool.name, client: tool.pkg }]);
      const made = ch.secrets.at(-1) as { id: string };
      expect(applies(ch).map((w) => [w.pkg, w.input.key, w.input.base_url])).toEqual([
        [tool.pkg, made.id, CORE],
      ]);
      // The key comes first, and the write goes to the files the page read.
      const order = ch.calls.map((c) => (c.cmd === "program_call" ? c.args.op : c.cmd));
      expect(order.indexOf("client_key_capture")).toBeLessThan(order.indexOf("apply"));
      expect(applies(ch)[0]?.input.targets).toEqual(
        (tool.read("not-configured") as { targets: unknown }).targets,
      );
      expect(page.text()).toContain(
        `Done. If ${tool.name} is running, start it again so it reads the new settings.`,
      );
      // It reads the file again after writing.
      expect(ch.reads.length).toBe(2);
    });
  }

  test("a key bound to this add-on and this pluxy is used again, with no new key", async () => {
    const bound = {
      id: "mine",
      label: "Claude Code",
      client_key: { client: "pluxy/claude-code", core: { mode: "local", origin: CORE } },
    };
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", { secrets: [bound] });
    expect(page.text()).toContain("Created for Claude Code");
    await page.click("Connect Claude Code");
    expect(captures(ch)).toEqual([]);
    expect(applies(ch).map((w) => w.input.key)).toEqual(["mine"]);
  });

  test("a key bound to another pluxy or another add-on is not used: Connect makes one", async () => {
    const secrets = [
      {
        id: "elsewhere",
        label: "Claude Code",
        client_key: { client: "pluxy/claude-code", core: { origin: "http://127.0.0.1:9999" } },
      },
      { id: "codex", label: "Codex", client_key: { client: "pluxy/codex-setup", core: { origin: CORE } } },
    ];
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", { secrets });
    expect(page.text()).toContain("None yet. Connect makes one.");
    await page.click("Connect Claude Code");
    expect(captures(ch).length).toBe(1);
    expect(applies(ch).map((w) => w.input.key)).not.toContain("elsewhere");
    expect(applies(ch).map((w) => w.input.key)).not.toContain("codex");
  });

  test("a cancelled key writes nothing and says nothing", async () => {
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", {
      capture: () => new Error("cancelled"),
    });
    await page.click("Connect Claude Code");
    expect(captures(ch).length).toBe(1);
    expect(applies(ch)).toEqual([]);
    expect(page.text()).not.toContain("Could not make a key");
    expect(page.text()).not.toContain("cancelled");
  });

  test("a key that could not be made writes nothing and says why", async () => {
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", {
      capture: () => new Error("pluxy refused it"),
    });
    await page.click("Connect Claude Code");
    expect(applies(ch)).toEqual([]);
    expect(page.text()).toContain("Could not make a key for Claude Code. pluxy refused it");
  });

  test("Use an existing key… enters it natively, bound to the add-on, and Connect then uses it", async () => {
    const ch = channel();
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    ch.reads[0]?.answer(claudeCode("not-configured"));
    await settle();
    await page.click("Use an existing key…");
    const entered = ch.calls.filter((c) => c.cmd === "client_key_enter");
    expect(entered.map((c) => c.args)).toEqual([
      { label: "Claude Code (your key)", client: "pluxy/claude-code" },
    ]);
    expect(ch.calls.some((c) => c.cmd === "vault_secret_add")).toBe(false);
  });
});

describe("the head, by state", () => {
  const scanned = (installed: boolean) => [
    {
      id: "claude-code",
      name: "Claude Code",
      installed,
      version: "2.1.0",
      config: "~/.claude/settings.json",
      state: "not-configured",
    },
  ];
  for (const [what, found, mode, word, line] of [
    ["installed", scanned(true), "local", "Installed · not connected", undefined],
    [
      "not installed",
      scanned(false),
      "local",
      "Not installed",
      "Claude Code is not installed on this computer. You can connect it now: it uses pluxy once installed.",
    ],
    [
      "with pluxy on another computer",
      scanned(true),
      "remote",
      "Not checked",
      "pluxy cannot see whether Claude Code is installed on this computer. You can connect it now: it uses pluxy once installed.",
    ],
  ] as const) {
    test(`not connected, ${what}: ${word}, Connect, nothing to undo`, async () => {
      const ch = channel({ mode });
      const page = await mount(() => <ClaudeCodePage />, ch.invoke, undefined, [...found]);
      ch.reads[0]?.answer(claudeCode("not-configured"));
      await settle();
      expect(page.text()).toContain(word);
      if (line) expect(page.text()).toContain(line);
      if (mode === "local") expect(page.text()).toContain("2.1.0");
      expect(page.canPress("Connect Claude Code")).toBe(true);
      expect(page.hasButton("Disconnect")).toBe(false);
    });
  }

  test("the first open reads the default file at once and says so until it answers", async () => {
    const ch = channel();
    const page = await mount(() => <CodexPage />, ch.invoke);
    expect(ch.reads.length).toBe(1);
    expect(ch.reads[0]?.input.targets).toBeUndefined();
    expect(page.text()).toContain("Reading Codex's settings…");
    expect(page.canPress("Connect Codex")).toBe(false);
  });

  test("a file pluxy will not change shows Read this file and Restore backup, and no Connect", async () => {
    for (const tool of TOOLS) {
      const ch = channel();
      const page = await mount(tool.page, ch.invoke);
      const read = tool.read("refused") as { settings: Record<string, unknown> };
      ch.reads[0]?.answer({ ...read, settings: { state: "refused", reason: "it is not JSON" } });
      await settle();
      expect(page.text()).toContain("Cannot change settings");
      expect(page.text()).toContain(`so ${tool.name} stays as it is.`);
      expect(page.text()).toContain("Why: it is not JSON");
      expect(page.canPress("Read this file")).toBe(true);
      expect(page.canPress("Restore backup")).toBe(true);
      expect(page.hasButton(`Connect ${tool.name}`)).toBe(false);
      expect(page.hasButton(`Update ${tool.name}`)).toBe(false);
      expect(page.hasButton("Disconnect")).toBe(false);
    }
  });

  test("Restore backup with nothing to restore says so in plain words", async () => {
    const ch = channel({
      ops: { restore: () => new Error("refused: claude-code is not applied to these targets") },
    });
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    ch.reads[0]?.answer({ ...claudeCode("refused"), settings: { state: "refused", reason: "bad" } });
    await settle();
    await page.click("Restore backup");
    expect(ch.writes.map((w) => w.op)).toEqual(["restore"]);
    expect(page.text()).toContain("There is no copy from before pluxy changed this file.");
  });

  test("settings that point elsewhere say where, with Update", async () => {
    const ch = channel();
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    ch.reads[0]?.answer(claudeCode("needs-update", { base_url: "http://127.0.0.1:9999" }, true));
    await settle();
    expect(page.text()).toContain(
      "Claude Code's settings point at http://127.0.0.1:9999, not at this pluxy.",
    );
    expect(page.canPress("Update Claude Code")).toBe(true);
    expect(page.canPress("Disconnect")).toBe(true);
  });

  test("Claude Desktop honours applied: Disconnect only when pluxy changed the files", async () => {
    const yes = await opened(TOOLS[2] as Tool, "configured", {}, true);
    expect(yes.page.text()).toContain("Connected");
    expect(yes.page.canPress("Disconnect")).toBe(true);
    await yes.page.click("Disconnect");
    expect(yes.ch.writes.map((w) => w.op)).toEqual(["clear"]);
    // No question of the page's own before the add-on's window (fault #48).
    expect(yes.page.text()).not.toContain("Yes, disconnect");
    const no = await opened(TOOLS[2] as Tool, "configured", {}, false);
    expect(no.page.text()).toContain("Connected");
    expect(no.page.hasButton("Disconnect")).toBe(false);
  });

  test("Claude Desktop with no settings folder offers to create it", async () => {
    const ch = channel();
    const page = await mount(() => <ClaudeDesktopPage />, ch.invoke);
    ch.reads[0]?.fail(`${MISSING_LIBRARY}: /home/me/.config/Claude-3p/configLibrary`);
    await settle();
    expect(page.text()).toContain("Claude Desktop has not made its settings folder yet.");
    expect(page.text()).not.toContain("Could not read");
    await page.click("Create settings folder");
    expect(ch.writes.map((w) => w.op)).toEqual(["folders"]);
    expect(ch.reads.length).toBe(2);
  });

  test("a read that fails says so, with Try again", async () => {
    const ch = channel();
    const page = await mount(() => <CodexPage />, ch.invoke);
    ch.reads[0]?.fail("disk exploded");
    await settle();
    expect(page.text()).toContain("Could not read Codex's settings. Disk exploded");
    await page.click("Try again");
    expect(ch.reads.length).toBe(2);
  });
});

describe("the add-on first", () => {
  test("an add-on that is off says so and offers Turn on add-on, before any read", async () => {
    const catalog = catalogOf(ADDONS.map((id) => ({ id, enabled: id !== "pluxy/claude-code" })));
    const ch = channel({ catalog });
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    expect(ch.reads).toEqual([]);
    expect(page.text()).toContain("Changing Claude Code's settings needs the Claude Code add-on.");
    await page.click("Turn on add-on");
    expect(ch.calls.filter((c) => c.cmd === "device_enable").map((c) => c.args)).toEqual([
      { id: "pluxy/claude-code", enabled: true },
    ]);
  });

  test("a missing add-on links to Add-ons and reads nothing", async () => {
    const ch = channel({
      catalog: catalogOf(ADDONS.filter((id) => id !== "pluxy/codex-setup").map((id) => ({ id }))),
    });
    const page = await mount(() => <CodexPage />, ch.invoke);
    expect(ch.reads).toEqual([]);
    expect(page.text()).toContain("Changing Codex's settings needs the Codex add-on.");
    expect(page.text()).toContain("Open Add-ons");
    expect(page.hasButton("Turn on add-on")).toBe(false);
  });

  test("safe mode says tool add-ons are paused and reads nothing", async () => {
    const ch = channel({ catalog: catalogOf(undefined, true) });
    const page = await mount(() => <ClaudeDesktopPage />, ch.invoke);
    expect(ch.reads).toEqual([]);
    expect(page.text()).toContain("Safe mode is on, so tool add-ons are paused.");
  });

  test("without the Launcher add-on, Start says what it needs", async () => {
    const ch = channel({
      catalog: catalogOf(ADDONS.filter((id) => id !== "pluxy/execution").map((id) => ({ id }))),
    });
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    expect(page.text()).toContain("Starting Claude Code from here needs the Launcher add-on.");
  });
});

describe("models", () => {
  test("a model pluxy's accounts do not serve has its line, and Connect still writes it", async () => {
    const ch = channel();
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    ch.reads[0]?.answer(claudeCode("not-configured", { opus: "claude-opus-9" }));
    await settle();
    expect(page.text()).toContain("Your accounts do not offer claude-opus-9.");
    expect(page.text()).not.toContain("Your accounts do not offer claude-sonnet-5-5.");
    // Claude Code's own aliases are not models.
    expect(page.text()).not.toContain("Your accounts do not offer opus.");
    await page.click("Connect Claude Code");
    expect(applies(ch).map((w) => w.input.opus)).toEqual(["claude-opus-9"]);
  });

  test("Codex clears what an import could not bring once its files are read again", async () => {
    const ch = channel({ ops: { import: () => ({ models: [], missing: ["odd-model"] }) } });
    const page = await mount(() => <CodexPage />, ch.invoke);
    ch.reads[0]?.answer(codex("not-configured"));
    await settle();
    await page.click("Import models from pluxy");
    expect(page.text()).toContain("Codex has no template for: odd-model");
    await page.click("Read this file");
    ch.pending()?.answer(codex("not-configured"));
    await settle();
    expect(page.text()).not.toContain("odd-model");
  });

  test("Codex's Connect is held, with its reason, until a default model from the list is chosen", async () => {
    const ch = channel();
    const page = await mount(() => <CodexPage />, ch.invoke);
    const read = codex("not-configured") as { settings: Record<string, unknown> };
    ch.reads[0]?.answer({ ...read, settings: { ...read.settings, models: [], model: null } });
    await settle();
    expect(page.text()).toContain("Import models from pluxy, then choose a default model.");
    expect(page.hasButton("Connect Codex")).toBe(true);
    expect(page.canPress("Connect Codex")).toBe(false);
    // With a default model from the list, the reason goes and Connect can be pressed.
    await page.open("Settings file, key and backups");
    await page.click("Read this file");
    ch.pending()?.answer(codex("not-configured"));
    await settle();
    expect(page.text()).not.toContain("Import models from pluxy, then choose a default model.");
    expect(page.canPress("Connect Codex")).toBe(true);
  });

  for (const [name, page, read] of [
    ["Codex", () => <CodexPage />, codex("not-configured")],
    ["Claude Desktop", () => <ClaudeDesktopPage />, desktop("not-configured")],
  ] as const) {
    test(`${name}: Remove model asks first`, async () => {
      const ch = channel();
      const mounted = await mount(page, ch.invoke);
      ch.reads[0]?.answer(read);
      await settle();
      const before = mounted.text();
      await mounted.click("Remove model");
      expect(mounted.text()).toContain("Yes, remove model");
      expect(mounted.text()).toContain("from the list?");
      await mounted.click("Cancel");
      expect(mounted.text()).toBe(before);
      await mounted.click("Remove model");
      await mounted.click("Yes, remove model");
      expect(mounted.hasButton("Remove model")).toBe(false);
    });
  }
});

/** The words `5 of the tool pages' plan says no page shows. */
const NEVER = [
  /enable add-on/i,
  /this machine/i,
  /\bcore\b/i,
  /\bCLI\b/,
  /client key/i,
  /curated/i,
  /native confirmation/i,
  /upstream model/i,
  /role models/i,
  /agent program path/i,
  /\bvault\b/i,
  /pluxy key add/i,
  /config panel/i,
];

describe("words", () => {
  for (const tool of TOOLS) {
    for (const state of ["not-configured", "configured", "needs-update", "refused"]) {
      test(`${tool.name}, ${state}: no word from the never list`, async () => {
        const { page } = await opened(tool, state, {}, true);
        await page.open("Settings file, key and backups");
        for (const word of NEVER) expect(page.text()).not.toMatch(word);
      });
    }
    test(`${tool.name}, failed read and failed write in the native side's own words: no word from the never list`, async () => {
      const failed = channel();
      const a = await mount(tool.page, failed.invoke);
      failed.reads[0]?.fail("the core at http://127.0.0.1:8317 refused: core's vault is locked");
      await settle();
      expect(a.text()).toContain("Could not read");
      for (const word of NEVER) expect(a.text()).not.toMatch(word);
      const { page } = await opened(tool, "not-configured", {
        ops: { apply: () => new Error("core refused the client key: its native confirmation timed out") },
      });
      await page.click(`Connect ${tool.name}`);
      expect(page.text()).toContain("pluxy refused the key: its confirmation window timed out");
      for (const word of NEVER) expect(page.text()).not.toMatch(word);
    });
    test(`${tool.name}, add-on off: no word from the never list`, async () => {
      const ch = channel({ catalog: catalogOf(ADDONS.map((id) => ({ id, enabled: id !== tool.pkg }))) });
      const page = await mount(tool.page, ch.invoke);
      for (const word of NEVER) expect(page.text()).not.toMatch(word);
    });
  }
});

describe("readExtraTools", () => {
  /** readExtraTools with each add-on's preview answered as readings says. */
  async function extra(
    readings: Record<string, unknown>,
    catalog = catalogOf(),
    found?: Parameters<typeof readExtraTools>[1],
  ) {
    const ch = channel();
    installWindow(ch.invoke);
    const out = readExtraTools(catalog, found);
    for (let i = 0; i < 20 && ch.reads.length < Object.keys(readings).length; i++) await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
    for (const r of ch.reads) {
      const body = readings[r.pkg];
      if (body instanceof Error) r.fail(body.message);
      else r.answer(body);
    }
    return { ch, tools: await out };
  }
  const reading = (state: string, key: "settings" | "config" = "settings", reason?: string) => ({
    targets: pin("/home/me/file"),
    applied: false,
    [key]: { state, ...(reason ? { reason } : {}) },
  });

  test("a reading with settings in it says the tool is installed, and how it stands", async () => {
    const { ch, tools } = await extra({
      "pluxy/claude-desktop": reading("configured"),
      "pluxy/grok-setup": reading("needs-update"),
      "pluxy/hermes-setup": reading("refused", "config", "not YAML"),
    });
    expect(ch.reads.every((r) => r.input.base_url === CORE)).toBe(true);
    expect(
      tools.map((t) => [t.client.id, t.client.installed, t.client.state, t.unchecked, t.reason]),
    ).toEqual([
      ["claude-desktop", true, "configured", false, undefined],
      ["grok-build", true, "needs-update", false, undefined],
      ["hermes", true, "refused", false, "not YAML"],
    ]);
  });

  test("nothing set up, or no reading at all, is unchecked", async () => {
    const off = catalogOf(
      ADDONS.filter((id) => id !== "pluxy/hermes-setup").map((id) => ({
        id,
        enabled: id !== "pluxy/grok-setup",
      })),
    );
    const { ch, tools } = await extra({ "pluxy/claude-desktop": reading("not-configured") }, off);
    expect(ch.reads.map((r) => r.pkg)).toEqual(["pluxy/claude-desktop"]);
    expect(tools.map((t) => [t.client.id, t.client.installed, t.client.state, t.unchecked])).toEqual([
      ["claude-desktop", false, "unchecked", true],
      ["grok-build", false, "unchecked", true],
      ["hermes", false, "unchecked", true],
    ]);
  });

  test("a failed reading is unchecked too", async () => {
    const { tools } = await extra({
      "pluxy/claude-desktop": new Error("boom"),
      "pluxy/grok-setup": reading("configured"),
      "pluxy/hermes-setup": reading("configured", "config"),
    });
    expect(tools[0]?.unchecked).toBe(true);
    expect(tools[0]?.client.state).toBe("unchecked");
  });

  test("with pluxy's own look at this computer, nothing set up reads as installed or not", async () => {
    const found = [
      {
        id: "claude-desktop",
        name: "Claude Desktop",
        installed: true,
        version: "1.2",
        config: "~/.config/Claude-3p",
        state: "unchecked" as const,
      },
      {
        id: "grok-build",
        name: "Grok Build",
        installed: false,
        config: "~/.grok/config.toml",
        state: "unchecked" as const,
      },
      {
        id: "hermes",
        name: "Hermes Agent",
        installed: false,
        config: "~/.hermes/config.yaml",
        state: "unchecked" as const,
      },
    ];
    const { tools } = await extra(
      {
        "pluxy/claude-desktop": reading("not-configured"),
        "pluxy/grok-setup": reading("not-configured"),
        "pluxy/hermes-setup": reading("not-configured", "config"),
      },
      catalogOf(),
      found,
    );
    expect(
      tools.map((t) => [t.client.id, t.client.installed, t.client.state, t.unchecked, t.client.version]),
    ).toEqual([
      ["claude-desktop", true, "not-configured", false, "1.2"],
      ["grok-build", false, "not-configured", false, undefined],
      ["hermes", false, "not-configured", false, undefined],
    ]);
  });
});

/** The choice under Settings file, key and backups that makes each page's file a typed path. */
const TYPED: Record<string, string> = {
  "Claude Code": "A file",
  Codex: "In a folder",
  "Claude Desktop": "In a folder",
};
const MORE = "Settings file, key and backups";
const bound = (pkg: string) => ({
  id: "mine",
  label: "Mine",
  client_key: { client: pkg, core: { mode: "local", origin: CORE } },
});
const op = (ch: ReturnType<typeof channel>, name: string) =>
  ch.calls.find((c) => c.cmd === "program_call" && c.args.op === name);
const cancels = (ch: ReturnType<typeof channel>) =>
  ch.calls.filter((c) => c.cmd === "program_cancel").map((c) => c.args.ticket);

describe("a write goes only to the file the page shows", () => {
  for (const tool of TOOLS) {
    test(`${tool.name}: while Connect's key window is open, the file choice is locked and the write goes to the file read`, async () => {
      const key = later<unknown>();
      const { ch, page } = await opened(tool, "not-configured", { capture: () => key.promise });
      await page.open(MORE);
      await page.click(`Connect ${tool.name}`);
      await expect(page.pick(TYPED[tool.name] as string)).rejects.toThrow("is disabled");
      key.give({ id: "made", label: tool.name, client_key: { client: tool.pkg, core: { origin: CORE } } });
      await settle();
      expect(applies(ch).map((w) => w.input.targets)).toEqual([
        (tool.read("not-configured") as { targets: unknown }).targets,
      ]);
      // Once the write is over, the choice is free again.
      await page.pick(TYPED[tool.name] as string);
    });

    test(`${tool.name}: leaving the page while the key window is open writes nothing`, async () => {
      const key = later<unknown>();
      const { ch, page } = await opened(tool, "not-configured", { capture: () => key.promise });
      await page.click(`Connect ${tool.name}`);
      await page.leave();
      key.give({ id: "made", label: tool.name, client_key: { client: tool.pkg, core: { origin: CORE } } });
      await settle();
      expect(captures(ch).length).toBe(1);
      expect(applies(ch)).toEqual([]);
    });

    test(`${tool.name}: leaving the page while the write's window is open withdraws it`, async () => {
      const answer = later<unknown>();
      const { ch, page } = await opened(tool, "not-configured", {
        secrets: [bound(tool.pkg)],
        ops: { apply: () => answer.promise },
      });
      await page.click(`Connect ${tool.name}`);
      const ticket = op(ch, "apply")?.args.ticket;
      expect(typeof ticket).toBe("string");
      expect(cancels(ch)).toEqual([]);
      await page.leave();
      expect(cancels(ch)).toEqual([ticket]);
      answer.give(new Error("the call was cancelled while its native confirmation was open"));
      await settle();
    });

    test(`${tool.name}: a backup's write locks the file choice, and leaving the page withdraws it`, async () => {
      const answer = later<unknown>();
      const { ch, page } = await opened(tool, "not-configured", {
        ops: { "backup-create": () => answer.promise },
      });
      await page.open(MORE);
      await page.click("Create backup");
      await expect(page.pick(TYPED[tool.name] as string)).rejects.toThrow("is disabled");
      expect(page.canPress(`Connect ${tool.name}`)).toBe(false);
      const ticket = op(ch, "backup-create")?.args.ticket;
      expect(typeof ticket).toBe("string");
      await page.leave();
      expect(cancels(ch)).toEqual([ticket]);
    });
  }

  test("Disconnect, Restore backup and the settings folder carry a ticket too", async () => {
    const yes = await opened(TOOLS[2] as Tool, "configured", {}, true);
    await yes.page.click("Disconnect");
    expect(typeof op(yes.ch, "clear")?.args.ticket).toBe("string");
    const ch = channel();
    const page = await mount(() => <ClaudeDesktopPage />, ch.invoke);
    ch.reads[0]?.fail(`${MISSING_LIBRARY}: /home/me/.config/Claude-3p/configLibrary`);
    await settle();
    await page.click("Create settings folder");
    expect(typeof op(ch, "folders")?.args.ticket).toBe("string");
  });
});

describe("the add-on list could not be read", () => {
  test("nothing is read or offered until it is, and Try again reads it", async () => {
    let n = 0;
    const ch = channel({
      catalog: () => (n++ === 0 ? new Error("the device catalog is locked") : catalogOf()),
    });
    const page = await mount(() => <ClaudeCodePage />, ch.invoke);
    expect(ch.reads).toEqual([]);
    expect(page.text()).toContain(
      "Could not check the Claude Code add-on, so nothing is read yet. The device catalog is locked",
    );
    expect(page.hasButton("Connect Claude Code")).toBe(false);
    expect(page.text()).not.toContain(MORE);
    await page.click("Try again");
    expect(ch.reads.length).toBe(1);
    ch.reads[0]?.answer(claudeCode("not-configured"));
    await settle();
    expect(page.canPress("Connect Claude Code")).toBe(true);
  });
});

describe("a read that fails after a good one", () => {
  for (const tool of TOOLS) {
    test(`${tool.name}: the reading shown says it may be out of date, and every write waits`, async () => {
      const { ch, page } = await opened(tool, "not-configured");
      await page.open(MORE);
      expect(page.canPress("Create backup")).toBe(true);
      await page.click("Read this file");
      ch.pending()?.fail("disk exploded");
      await settle();
      expect(page.text()).toContain(
        `Could not read ${tool.name}'s settings again, so what this page shows may be out of date. Nothing is written until they are read. Disk exploded`,
      );
      expect(page.canPress(`Connect ${tool.name}`)).toBe(false);
      expect(page.canPress("Restore backup")).toBe(false);
      expect(page.canPress("Create backup")).toBe(false);
      await page.click("Try again");
      ch.pending()?.answer(tool.read("not-configured"));
      await settle();
      expect(page.text()).not.toContain("may be out of date");
      expect(page.canPress(`Connect ${tool.name}`)).toBe(true);
    });
  }
});

describe("privacy mode", () => {
  const alice = <T,>(v: T): T => JSON.parse(JSON.stringify(v).replaceAll("/home/me", "/home/alice"));
  const privately = async (tool: Tool, o: ChannelOptions = {}) => {
    const ch = channel(o);
    const page = await mount(tool.page, ch.invoke, undefined, [], { privacy: true });
    return { ch, page };
  };

  for (const tool of TOOLS) {
    test(`${tool.name}: no path shows the user's name, in the head or under ${MORE}`, async () => {
      const { ch, page } = await privately(tool);
      ch.reads[0]?.answer(alice(tool.read("not-configured")));
      await settle();
      await page.open(MORE);
      expect(page.text()).toContain("…/");
      expect(page.text()).not.toContain("alice");
    });

    test(`${tool.name}: a refused file's path and reason are masked`, async () => {
      const { ch, page } = await privately(tool);
      const read = alice(tool.read("refused")) as { settings: Record<string, unknown> };
      ch.reads[0]?.answer({
        ...read,
        settings: { state: "refused", reason: "/home/alice/settings is not JSON (alice@example.com)" },
      });
      await settle();
      expect(page.text()).toContain("Cannot change settings");
      expect(page.text()).toContain("pluxy will not change …/");
      expect(page.text()).not.toContain("alice");
    });

    test(`${tool.name}: a failed read's path is masked`, async () => {
      const { ch, page } = await privately(tool);
      ch.reads[0]?.fail("cannot open /home/alice/.config/tool/settings.json");
      await settle();
      expect(page.text()).toContain("Could not read");
      expect(page.text()).toContain("…/tool/settings.json");
      expect(page.text()).not.toContain("alice");
    });

    test(`${tool.name}: a backup's files and errors are masked`, async () => {
      const target = "/home/alice/.config/tool/settings.json";
      const { ch, page } = await privately(tool, {
        ops: {
          "backup-list": () => ({
            keep: 10,
            backups: [
              {
                id: "b1",
                created: 1_790_000_000_000,
                files: [{ selected: target, path: null, target, exists: true, size: 9 }],
              },
            ],
          }),
          "backup-diff": () => ({
            same: false,
            files: [
              {
                target,
                exists: true,
                now_exists: true,
                same: false,
                readable: true,
                changes: [{ path: ["env", "EXAMPLE"], restore: "changes" }],
                more: 0,
              },
            ],
          }),
          "backup-create": () => new Error(`could not copy ${target}`),
        },
      });
      ch.reads[0]?.answer(tool.read("not-configured"));
      await settle();
      await page.open(MORE);
      const copy = all(page.root, "input").find(
        (i) => i.getAttribute("type") === "radio" && i.value === "b1",
      );
      if (!copy) throw new Error("no saved copy listed");
      await act(async () => {
        copy.dispatchEvent(new Event("change"));
      });
      await settle();
      await page.click("Create backup");
      const backups = all(page.root, "div").find((d) => d.getAttribute("class") === "backups");
      expect(backups?.textContent).toContain("…/tool/settings.json");
      expect(backups?.textContent).toContain("could not copy …/tool/settings.json");
      expect(backups?.textContent).not.toContain("alice");
    });
  }

  test("Start's recent folders are masked", async () => {
    const { ch, page } = await privately(TOOLS[0] as Tool, {
      commands: { launcher_state: () => ({ history: { "Claude Code": ["/home/alice/project/app"] } }) },
    });
    ch.reads[0]?.answer(claudeCode("not-configured"));
    await settle();
    expect(page.text()).toContain("…/project/app");
    expect(page.text()).not.toContain("alice");
  });

  test("Claude Desktop's alias names its accounts by masked IDs", async () => {
    const read = desktop("not-configured") as { settings: Record<string, unknown> };
    const { ch, page } = await privately(TOOLS[2] as Tool, {
      commands: {
        core_alias_create: () => ({
          alias: "claude-sonnet-5-mine",
          accounts: ["acc_alice01"],
          config_accounts: ["alice-example-com"],
          already: false,
        }),
      },
    });
    ch.reads[0]?.answer({
      ...read,
      settings: {
        ...read.settings,
        models: [{ model: "gpt-6.1-sol", alias: "claude-sonnet-5-mine", one_m: false }],
      },
    });
    await settle();
    await page.click("Create alias in pluxy");
    expect(page.text()).toContain("pluxy now serves claude-sonnet-5-mine on");
    expect(page.text()).not.toContain("alice");
  });
});

describe("the native side's words", () => {
  test("a key that could not be made says pluxy, not core", async () => {
    const { page } = await opened(TOOLS[0] as Tool, "not-configured", {
      capture: () =>
        new Error("core did not make a client key: the core at http://127.0.0.1:8317 answered 500"),
    });
    await page.click("Connect Claude Code");
    expect(page.text()).toContain(
      "Could not make a key for Claude Code. pluxy did not make a key: pluxy at http://127.0.0.1:8317 answered 500",
    );
    expect(page.text()).not.toMatch(/\bcore\b/i);
  });

  test("Claude Desktop's alias refusal says pluxy, not core", async () => {
    const read = desktop("not-configured") as { settings: Record<string, unknown> };
    const { ch, page } = await (async () => {
      const ch = channel({
        commands: { core_alias_create: () => new Error("core serves no model named gpt-6.1-sol") },
      });
      const page = await mount(() => <ClaudeDesktopPage />, ch.invoke);
      return { ch, page };
    })();
    ch.reads[0]?.answer({
      ...read,
      settings: {
        ...read.settings,
        models: [{ model: "gpt-6.1-sol", alias: "claude-sonnet-5-mine", one_m: false }],
      },
    });
    await settle();
    await page.click("Create alias in pluxy");
    expect(page.text()).toContain("pluxy serves no model named gpt-6.1-sol");
    expect(page.text()).not.toMatch(/\bcore\b/i);
  });
});

describe("why Connect or Save is held", () => {
  test("with the app not connected to pluxy, the head says Connect waits for it", async () => {
    const { page } = await opened(TOOLS[0] as Tool, "not-configured", { signedIn: false });
    expect(page.hasButton("Connect Claude Code")).toBe(true);
    expect(page.canPress("Connect Claude Code")).toBe(false);
    expect(page.text()).toContain("Connect waits until the app is connected to pluxy.");
  });

  test("Save held by the form says why beside it", async () => {
    const { page } = await opened(TOOLS[0] as Tool, "configured", {}, true);
    await page.type("Opus", "");
    expect(page.hasButton("Save")).toBe(true);
    expect(page.canPress("Save")).toBe(false);
    expect(page.text()).toContain("Opus, Sonnet and Haiku each need a model");
  });
});

describe("back to Default", () => {
  test("choosing Default again reads the default file again", async () => {
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured");
    await page.open(MORE);
    await page.pick("A file");
    await page.pick("Default ~/.claude/settings.json");
    expect(ch.reads.length).toBe(2);
    expect(ch.reads[1]?.input.targets).toBeUndefined();
    ch.reads[1]?.answer(claudeCode("not-configured"));
    await settle();
    expect(page.text()).not.toContain("Reading Claude Code's settings…");
    expect(page.canPress("Connect Claude Code")).toBe(true);
  });
});

describe("the key Connect made", () => {
  test("a write cancelled after the key was made says the key is kept", async () => {
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", {
      ops: { apply: () => new Error("cancelled") },
    });
    await page.click("Connect Claude Code");
    expect(captures(ch).length).toBe(1);
    expect(page.text()).toContain("The key made for Claude Code is kept: Connect uses it next time.");
  });

  test("pressed again before the key list is read again, Connect uses that key and makes no second", async () => {
    let n = 0;
    const { ch, page } = await opened(TOOLS[0] as Tool, "not-configured", {
      holdSecrets: true,
      ops: { apply: () => (n++ === 0 ? new Error("cancelled") : {}) },
    });
    await page.click("Connect Claude Code");
    await page.click("Connect Claude Code");
    expect(captures(ch).length).toBe(1);
    const made = ch.secrets.at(-1) as { id: string };
    expect(applies(ch).map((w) => w.input.key)).toEqual([made.id, made.id]);
    ch.release();
    await settle();
  });
});
