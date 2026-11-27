// The Tools page and its list of coding tools: what a person sees and what each press sends, against
// a fake core. The page is driven through Preact's DOM renderer and a small fake DOM.
import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import type { ComponentChildren } from "preact";
import { act } from "preact/test-utils";
import { api, type ClientKey, type Setup, type SetupClient } from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { ToolList, type ToolsOptions, ToolsPage } from "../kit/pages/tools";
import { toolKeyName } from "../kit/pages/tools.helpers";
import { type Kit, KitContext } from "../kit/ui";
import { all, type FakeNode, mountIn, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
});
const unmounts: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const unmount of unmounts.splice(0)) await unmount();
});
beforeEach(() => {
  unmounts.length = 0;
});

const client = (id: string, name: string, more: Partial<SetupClient> = {}): SetupClient => ({
  id,
  name,
  installed: true,
  version: "1.2.3",
  config: `~/.${id}/settings.json`,
  state: "not-configured",
  ...more,
});

const CLAUDE = client("claude-code", "Claude Code");
const CODEX = client("codex", "Codex CLI");
const GONE = client("opencode", "OpenCode", { installed: false });
const DESKTOP = client("claude-desktop", "Claude Desktop", { state: "unchecked" });

const SCANNED = "2026-11-20T10:00:00Z";

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/** A core that keeps its client keys, and answers the calls the Tools page makes. */
function world(
  init: {
    clients?: SetupClient[];
    keys?: ClientKey[];
    accounts?: number;
    setup?: (call: Call) => Reply<unknown> | undefined;
    make?: (call: Call) => Reply<unknown> | undefined;
    rename?: (call: Call) => Reply<unknown> | undefined;
    remove?: (call: Call) => Reply<unknown> | undefined;
    connect?: (call: Call) => Reply<unknown> | undefined;
    used?: Record<string, number>;
    scanned?: boolean;
  } = {},
) {
  const calls: Call[] = [];
  const keys: ClientKey[] = init.keys ?? [{ id: "key-a", masked: "sk-***48" }];
  let made = 0;
  const setup = (scanning: boolean): Setup => ({
    first_run: false,
    scanning,
    scanned_at: init.scanned === false ? undefined : SCANNED,
    found: {
      clients: init.clients ?? [CLAUDE, CODEX, GONE],
      sign_ins: [],
      imports: [],
      core: { listen: "127.0.0.1:8317", reachable_from_network: false },
    },
    picks: [],
  });
  const answer = (call: Call): Reply<unknown> => {
    calls.push(call);
    const { method, path } = call;
    if (method === "GET" && path === "/setup")
      return init.setup?.(call) ?? { status: 200, body: setup(false) };
    if (method === "POST" && path === "/setup/scan") return { status: 200, body: setup(false) };
    if (method === "POST" && /^\/setup\/clients\/[^/]+\/(connect|disconnect)$/.test(path)) {
      return init.connect?.(call) ?? { status: 404, error: { code: "not_found", message: "no such call" } };
    }
    if (method === "GET" && path.startsWith("/setup/clients/")) {
      const id = path.split("/")[3] ?? "";
      if (!["claude-code", "codex", "opencode", "pi", "omp"].includes(id)) {
        return { status: 404, error: { code: "not_found", message: "no settings to paste" } };
      }
      return {
        status: 200,
        body: {
          tool: id,
          name: id,
          base_url: "http://127.0.0.1:8317",
          key: "<client-key>",
          files: [
            {
              path: `~/.${id}/settings.json`,
              content: `{"url": "http://127.0.0.1:8317", "key": "<client-key>", "model": "<model-id>"}`,
            },
          ],
          note: id === "codex" ? "Codex reads this at start." : undefined,
        },
      };
    }
    if (method === "GET" && path === "/accounts") {
      return {
        status: 200,
        body: { accounts: Array.from({ length: init.accounts ?? 1 }, (_, i) => ({ id: `acc-${i}` })) },
      };
    }
    if (method === "GET" && path === "/client-keys") return { status: 200, body: { keys: [...keys] } };
    if (method === "GET" && path.startsWith("/usage")) {
      return {
        status: 200,
        body: {
          keys: Object.entries(init.used ?? {}).map(([name, total_requests]) => ({ name, total_requests })),
        },
      };
    }
    if (method === "POST" && path === "/client-keys") {
      const refused = init.make?.(call);
      if (refused) return refused;
      made++;
      const label = (call.body as { label?: string } | undefined)?.label;
      const key: ClientKey = { id: `key-${made}`, masked: `sk-***${made}${made}`, label };
      keys.push(key);
      return { status: 201, body: { key, value: `sk-secret-${made}` } };
    }
    if (method === "PATCH" && path.startsWith("/client-keys/")) {
      const refused = init.rename?.(call);
      if (refused) return refused;
      const key = keys.find((k) => k.id === path.split("/")[2]);
      if (key) key.label = (call.body as { label: string | null }).label ?? undefined;
      return { status: 200, body: key };
    }
    if (method === "DELETE" && path.startsWith("/client-keys/")) {
      const refused = init.remove?.(call);
      if (refused) return refused;
      const at = keys.findIndex((k) => k.id === path.split("/")[2]);
      if (at >= 0) keys.splice(at, 1);
      return { status: 204 };
    }
    return { status: 404, error: { code: "not_found", message: "no such call" } };
  };
  const prefs: [string, string | null][] = [];
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) =>
      answer({ method, path, body }) as Reply<T>,
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: (name, value) => void prefs.push([name, value]) },
    features: new Set<Feature>(["signOut"]),
    address: "http://127.0.0.1:8317",
  };
  const signedOut = { times: 0 };
  const kit = {
    host,
    api: api(host),
    unauthorized: () => {
      signedOut.times++;
    },
    extensionsChanged: () => {},
  } as Kit;
  const mount = async (tree: ComponentChildren) => {
    const m = await mountIn(<KitContext.Provider value={kit}>{tree}</KitContext.Provider>);
    unmounts.push(m.unmount);
    return m;
  };
  return { calls, keys, prefs, host, signedOut, mount };
}

const attribute = (node: FakeNode, name: string) => node.attributes.find((a) => a.name === name)?.value;
const descend = (node: FakeNode): FakeNode[] => [node, ...node.childNodes.flatMap(descend)];
const withPart = (root: FakeNode, part: string) =>
  descend(root).filter((n) => attribute(n, "data-part") === part);
const rowOf = (root: FakeNode, tool: string) => {
  const row = withPart(root, "tool-row").find((r) => r.textContent.includes(tool));
  if (!row) throw new Error(`no row for ${tool}`);
  return row;
};
const keyRows = (root: FakeNode) => withPart(root, "tool-key");
const buttonIn = (root: FakeNode, label: string) => {
  const found = all(root, "button").find((b) => b.textContent === label);
  if (!found) throw new Error(`no button ${label} in ${root.textContent.slice(0, 200)}`);
  return found;
};
const hasButton = (root: FakeNode, label: string) => all(root, "button").some((b) => b.textContent === label);
const press = async (node: FakeNode) => {
  await act(async () => {
    node.dispatchEvent(new Event("click"));
  });
  await settle();
};
const type = async (input: FakeNode, value: string) => {
  input.value = value;
  await act(async () => {
    input.dispatchEvent(new Event("input"));
  });
};
const submit = async (form: FakeNode) => {
  await act(async () => {
    form.dispatchEvent(new Event("submit"));
  });
  await settle();
};
const posts = (calls: Call[]) => calls.filter((c) => c.method === "POST" && c.path === "/client-keys");
const preText = (root: FakeNode) =>
  descend(root)
    .filter((n) => n.localName === "pre")
    .map((n) => n.textContent)
    .join("\n");

// ---- the list ----------------------------------------------------------------------------------

test("the rows are connected tools first, then installed ones; the others wait under Other tools", async () => {
  const w = world({ clients: [GONE, CLAUDE, client("pi", "Pi", { state: "configured" }), CODEX] });
  const m = await w.mount(<ToolsPage />);
  const names = withPart(m.root, "tool-row").map(
    (r) => descend(r).find((n) => attribute(n, "class") === "tool-name")?.textContent,
  );
  expect(names).toEqual(["Pi", "Claude Code", "Codex", "OpenCode"]);
  const other = descend(m.root).find((n) => n.localName === "details");
  expect(other?.textContent).toContain("Other tools (1)");
  expect(other && attribute(other, "open")).toBeUndefined();
  expect(m.text()).toContain("3 installed");
  expect(m.text()).toContain("1 connected");
  expect(m.text()).toContain("checked");
});

test("core's name Codex CLI reads Codex on the row", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain("Codex");
  expect(m.text()).not.toContain("Codex CLI");
});

test("with no tool installed it says so and opens Other tools", async () => {
  const w = world({ clients: [GONE] });
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain("No coding tools found.");
  const other = descend(m.root).find((n) => n.localName === "details");
  expect(other && attribute(other, "open")).not.toBeUndefined();
  expect(other?.textContent).toContain("Other tools (1)");
});

test("a pluxy that has no scan shows one line, and the address and keys still show", async () => {
  const w = world({ setup: () => ({ status: 404, error: { code: "not_found", message: "no such call" } }) });
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain(
    "This pluxy cannot look for coding tools. Use the address and a key below in your tool's settings.",
  );
  expect(m.text()).toContain("Address for tools");
  expect(m.text()).toContain("Keys (1)");
  expect(withPart(m.root, "tool-row")).toHaveLength(0);
  expect(hasButton(m.root, "Try again")).toBe(false);
});

test("the first scan runs once when pluxy never scanned, and a running scan is read again every second", async () => {
  jest.useFakeTimers();
  let reads = 0;
  const w = world({
    scanned: false,
    setup: () => {
      reads++;
      // The first two reads find a scan running; the third finds it over.
      const done = reads > 2;
      return {
        status: 200,
        body: {
          first_run: false,
          scanning: !done,
          scanned_at: done ? SCANNED : undefined,
          found: {
            clients: done ? [CLAUDE] : [],
            sign_ins: [],
            imports: [],
            core: { listen: "127.0.0.1:8317", reachable_from_network: false },
          },
          picks: [],
        },
      };
    },
  });
  const m = await w.mount(<ToolsPage />);
  // Scanning when first read: no scan is asked for, and the pane says it is looking.
  expect(m.text()).toContain("Looking for coding tools…");
  expect(w.calls.filter((c) => c.path === "/setup/scan")).toHaveLength(0);
  const tick = async (ms: number) => {
    await act(async () => {
      jest.advanceTimersByTime(ms);
      for (let i = 0; i < 20; i++) await Promise.resolve();
    });
  };
  await tick(999);
  expect(reads).toBe(1);
  const until = async (count: number) => {
    for (let i = 0; i < 40 && reads < count; i++) await tick(100);
  };
  await until(2);
  expect(reads).toBe(2);
  expect(m.text()).toContain("Looking for coding tools…");
  await until(3);
  expect(reads).toBe(3);
  expect(m.text()).toContain("Claude Code");
  await tick(5000);
  expect(reads).toBe(3);
});

test("a pluxy that never scanned is asked to scan once", async () => {
  jest.useFakeTimers();
  const w = world({
    scanned: false,
    setup: () => ({
      status: 200,
      body: {
        first_run: false,
        scanning: false,
        found: {
          clients: [],
          sign_ins: [],
          imports: [],
          core: { listen: "127.0.0.1:8317", reachable_from_network: false },
        },
        picks: [],
      },
    }),
  });
  await w.mount(<ToolsPage />);
  expect(w.calls.filter((c) => c.path === "/setup/scan")).toHaveLength(1);
});

test("Refresh scans again, then reads the keys and the week's use again, and tells the host", async () => {
  let told = 0;
  const w = world();
  const m = await w.mount(<ToolsPage options={{ onRefresh: () => told++ }} />);
  const before = w.calls.length;
  await press(buttonIn(m.root, "Refresh"));
  const after = w.calls.slice(before).map((c) => `${c.method} ${c.path}`);
  expect(after).toContain("POST /setup/scan");
  expect(after).toContain("GET /client-keys");
  expect(after.some((c) => c.startsWith("GET /usage"))).toBe(true);
  expect(told).toBe(1);
});

test("the summary says the tools were found on the computer pluxy runs on when it runs elsewhere", async () => {
  const w = world();
  const here = await w.mount(<ToolsPage />);
  expect(here.text()).not.toContain("Detected on the computer pluxy runs on.");
  expect(here.text()).toContain("Only programs on this computer can reach it.");
  const away = await w.mount(<ToolsPage options={{ isRemote: true }} />);
  expect(away.text()).toContain("Detected on the computer pluxy runs on.");
  expect(away.text()).not.toContain("Only programs on this computer can reach it.");
});

test("no account: a warning with a link to add one; the steps still open", async () => {
  const w = world({ accounts: 0 });
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain("pluxy has no accounts yet, so a coding tool you set up here will get errors.");
  const link = all(m.root, "a").find((a) => a.textContent === "Add an account");
  expect(link && attribute(link, "href")).toBe("#/add-account");
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  expect(preText(m.root)).toContain("<client-key>");
  const some = await world({ accounts: 2 }).mount(<ToolsPage />);
  expect(some.text()).not.toContain("pluxy has no accounts yet");
});

test("ToolList links to Add an account, and with onAddAccount calls it instead", async () => {
  const plain = await world({ accounts: 0 }).mount(<ToolList />);
  const link = all(plain.root, "a").find((a) => a.textContent === "Add an account");
  expect(link && attribute(link, "href")).toBe("#/add-account");
  let asked = 0;
  const own = await world({ accounts: 0 }).mount(<ToolList onAddAccount={() => asked++} />);
  expect(all(own.root, "a").some((a) => a.textContent === "Add an account")).toBe(false);
  await press(buttonIn(own.root, "Add an account"));
  expect(asked).toBe(1);
  // The list stands without the address and the keys.
  expect(own.text()).not.toContain("Address for tools");
  expect(own.text()).not.toContain("Keys (");
});

// ---- the row's action --------------------------------------------------------------------------

test("a host's toolAction replaces Set up on its row; the other rows keep Set up", async () => {
  const w = world();
  const options: ToolsOptions = {
    toolAction: (c) => (c.id === "codex" ? <a href="#/codex">Open</a> : undefined),
  };
  const m = await w.mount(<ToolsPage options={options} />);
  expect(all(rowOf(m.root, "Codex"), "a").some((a) => a.textContent === "Open")).toBe(true);
  expect(hasButton(rowOf(m.root, "Codex"), "Set up")).toBe(false);
  expect(hasButton(rowOf(m.root, "Claude Code"), "Set up")).toBe(true);
});

test("a tool with no text to paste says it is set up from the desktop app, with no button", async () => {
  const w = world({ clients: [CLAUDE, DESKTOP] });
  const m = await w.mount(<ToolsPage />);
  const row = rowOf(m.root, "Claude Desktop");
  expect(row.textContent).toContain("Set up from the desktop app");
  expect(all(row, "button")).toHaveLength(0);
});

test("in the app (extraTools given) such a row reads Needs its add-on", async () => {
  const w = world({ clients: [CLAUDE] });
  const m = await w.mount(<ToolsPage options={{ extraTools: [DESKTOP] }} />);
  expect(rowOf(m.root, "Claude Desktop").textContent).toContain("Needs its add-on");
  expect(m.text()).not.toContain("are set up from the desktop app.");
});

test("a row marked unchecked reads Not checked, sits under Other tools and is not counted as installed", async () => {
  const w = world({ clients: [CLAUDE] });
  const m = await w.mount(
    <ToolsPage options={{ extraTools: [client("hermes", "Hermes Agent", { state: "unchecked" })] }} />,
  );
  const other = descend(m.root).find((n) => n.localName === "details");
  expect(other?.textContent).toContain("Other tools (1)");
  expect(other?.textContent).toContain("Hermes Agent");
  expect(other?.textContent).toContain("Not checked");
  expect(m.text()).toContain("1 installed");
  // A tool core scans and cannot read is the same.
  const scanned = await world({ clients: [CLAUDE, DESKTOP] }).mount(<ToolsPage />);
  expect(scanned.text()).toContain("1 installed");
  expect(rowOf(scanned.root, "Claude Desktop").textContent).toContain("Not checked");
});

test("without extraTools one line says three tools are set up from the app; localPages replace it", async () => {
  const w = world();
  const plain = await w.mount(<ToolsPage />);
  expect(plain.text()).toContain(
    "Claude Desktop, Grok Build and Hermes Agent are set up from the desktop app.",
  );
  const links = await w.mount(
    <ToolsPage
      options={{
        localPages: [
          { name: "Claude Code", page: "claude-code" },
          { name: "Codex", page: "codex" },
        ],
      }}
    />,
  );
  expect(links.text()).toContain("Set up a tool on this computer:");
  expect(links.text()).not.toContain("are set up from the desktop app.");
  expect(all(links.root, "a").filter((a) => attribute(a, "href") === "#/codex")).toHaveLength(1);
  // When the scan already lists the three, the line would say it twice.
  const listed = await world({
    clients: [
      CLAUDE,
      DESKTOP,
      client("grok-build", "Grok Build", { state: "unchecked" }),
      client("hermes", "Hermes Agent", { state: "unchecked" }),
    ],
  }).mount(<ToolsPage />);
  expect(listed.text()).not.toContain("are set up from the desktop app.");
});

test("a host's notes get the tools the page lists and show above and under the list", async () => {
  const w = world({ clients: [CLAUDE, client("codex", "Codex CLI", { installed: false })] });
  let seen: string[] = [];
  const m = await w.mount(
    <ToolsPage
      options={{
        notes: (clients) => {
          seen = clients.map((c) => c.id);
          return { above: <p>Above the list.</p>, below: <p>Under the list.</p> };
        },
      }}
    />,
  );
  expect(seen.sort()).toEqual(["claude-code", "codex"]);
  const text = m.text();
  expect(text.indexOf("Above the list.")).toBeLessThan(text.indexOf("Claude Code"));
  expect(text.indexOf("Under the list.")).toBeGreaterThan(text.indexOf("Other tools"));
  // Without a host, nothing is added.
  expect((await w.mount(<ToolsPage />)).text()).not.toContain("the list.");
});

// ---- Connect and Disconnect ---------------------------------------------------------------------

const CAN = client("claude-code", "Claude Code", { can_connect: true, config: "~/.claude/settings.json" });

test("a tool pluxy can connect offers Connect and Set up by hand, and says what Connect does", async () => {
  const w = world({
    clients: [
      CAN,
      client("codex", "Codex CLI", { can_connect: true, state: "configured", connected_by_pluxy: true }),
      client("opencode", "OpenCode", { can_connect: true, state: "configured" }),
    ],
  });
  const m = await w.mount(<ToolsPage />);
  const claude = rowOf(m.root, "Claude Code");
  expect(all(claude, "button").map((b) => b.textContent)).toEqual(["Connect", "Set up by hand"]);
  expect(claude.textContent).toContain(
    "Connect makes a key for Claude Code and writes it into ~/.claude/settings.json on this computer. pluxy keeps a copy of the file as it was, and Disconnect puts it back.",
  );
  // Connected by pluxy: Disconnect alone.
  expect(all(rowOf(m.root, "Codex"), "button").map((b) => b.textContent)).toEqual(["Disconnect"]);
  // Connected by hand: its Set up, and no Connect or hint.
  const byHand = rowOf(m.root, "OpenCode");
  expect(all(byHand, "button").map((b) => b.textContent)).toEqual(["Set up"]);
  expect(byHand.textContent).not.toContain("Connect makes a key");
  // A host's own button wins.
  const app = await w.mount(<ToolsPage options={{ toolAction: () => <a href="#/claude-code">Open</a> }} />);
  expect(hasButton(rowOf(app.root, "Claude Code"), "Connect")).toBe(false);
});

test("Connect sends the row's name for the key, reads the tools and keys again, and says what to do next", async () => {
  let connected = false;
  const w = world({
    clients: [CAN],
    connect: () => {
      connected = true;
      return {
        status: 200,
        body: {
          client: { ...CAN, state: "configured" },
          files: [{ path: "~/.claude/settings.json", change: "changed" }],
        },
      };
    },
    setup: () =>
      connected
        ? {
            status: 200,
            body: {
              first_run: false,
              scanning: false,
              scanned_at: SCANNED,
              found: {
                clients: [{ ...CAN, state: "configured", connected_by_pluxy: true }],
                sign_ins: [],
                imports: [],
                core: { listen: "127.0.0.1:8317", reachable_from_network: false },
              },
              picks: [],
            },
          }
        : undefined,
  });
  const m = await w.mount(<ToolsPage />);
  const before = w.calls.length;
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Connect"));
  const sent = w.calls.slice(before);
  expect(sent[0]).toEqual({
    method: "POST",
    path: "/setup/clients/claude-code/connect",
    body: { label: "Claude Code" },
  });
  expect(sent.some((c) => c.method === "GET" && c.path === "/setup")).toBe(true);
  expect(sent.some((c) => c.method === "GET" && c.path === "/client-keys")).toBe(true);
  const row = rowOf(m.root, "Claude Code");
  expect(row.textContent).toContain("Connected. Start Claude Code again so it reads the new settings.");
  expect(all(row, "button").map((b) => b.textContent)).toEqual(["Disconnect"]);
});

test("a refused Connect says why under the row, and Disconnect says what it did to the file", async () => {
  const byPluxy = { ...CAN, state: "configured" as const, connected_by_pluxy: true };
  const refused = world({
    clients: [CAN],
    connect: () => ({ status: 422, error: { code: "refused", message: "~/.claude is a link." } }),
  });
  const m = await refused.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Connect"));
  expect(rowOf(m.root, "Claude Code").textContent).toContain(
    "Could not connect Claude Code. ~/.claude is a link.",
  );
  expect(hasButton(rowOf(m.root, "Claude Code"), "Set up by hand")).toBe(true);

  for (const [change, says] of [
    ["restored", "Disconnected. ~/.claude/settings.json is back as it was."],
    [
      "merged",
      "Disconnected. pluxy took its settings out of ~/.claude/settings.json and kept the changes made since.",
    ],
    ["removed", "Disconnected. pluxy removed ~/.claude/settings.json, which it had made."],
  ] as const) {
    const w = world({
      clients: [byPluxy],
      connect: (call) => ({
        status: 200,
        body: {
          client: CAN,
          files: [{ path: "~/.claude/settings.json", change }],
          key_removed: call.path.endsWith("/disconnect"),
        },
      }),
    });
    const d = await w.mount(<ToolsPage />);
    await press(buttonIn(rowOf(d.root, "Claude Code"), "Disconnect"));
    expect(
      w.calls.some((c) => c.method === "POST" && c.path === "/setup/clients/claude-code/disconnect"),
    ).toBe(true);
    expect(rowOf(d.root, "Claude Code").textContent).toContain(says);
  }
});

// ---- the steps and the key made for a tool -----------------------------------------------------

test("Set up opens the steps under one row at a time", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  expect(withPart(m.root, "tool-steps")).toHaveLength(1);
  expect(rowOf(m.root, "Claude Code").textContent).toContain("Put this in ~/.claude-code/settings.json:");
  expect(m.text()).toContain("Make a key for Claude Code.");
  expect(m.text()).toContain("Start Claude Code again so it reads the new settings.");
  expect(m.text()).toContain(
    "If the file already has settings, add these lines to them instead of replacing the file.",
  );
  expect(m.text()).toContain("Make the key first, or replace <client-key> with a key you have.");
  expect(m.text()).toContain("Replace <model-id> with a model your accounts serve:");
  const models = all(m.root, "a").find((a) => a.textContent === "Models your tools can ask for");
  expect(models && attribute(models, "href")).toBe("#/system");
  await press(buttonIn(rowOf(m.root, "Codex"), "Set up"));
  expect(withPart(m.root, "tool-steps")).toHaveLength(1);
  expect(withPart(rowOf(m.root, "Claude Code"), "tool-steps")).toHaveLength(0);
  expect(rowOf(m.root, "Codex").textContent).toContain("Codex reads this at start.");
  await press(buttonIn(rowOf(m.root, "Codex"), "Hide"));
  expect(withPart(m.root, "tool-steps")).toHaveLength(0);
});

test("Make key sends the tool's name, puts the key in that row's text only, and names it in Keys", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  expect(posts(w.calls).map((c) => c.body)).toEqual([{ label: "Claude Code" }]);
  const steps = rowOf(m.root, "Claude Code");
  expect(preText(steps)).toContain("sk-secret-1");
  expect(preText(steps)).not.toContain("<client-key>");
  expect(steps.textContent).toContain("Done. The key is in the text below.");
  expect(hasButton(steps, "Make key")).toBe(false);
  // Keys lists it by its name, and shows the one-time notice with the value.
  expect(
    keyRows(m.root)
      .map((r) => r.textContent)
      .some((t) => t.includes("Claude Code")),
  ).toBe(true);
  expect(m.text()).toContain("Copy this key now. pluxy shows it only this once.");
  expect(withPart(m.root, "key-value").map((n) => n.textContent)).toEqual(["sk-secret-1"]);
  // Another tool's text does not get it, and its own key is named for it.
  await press(buttonIn(rowOf(m.root, "Codex"), "Set up"));
  expect(preText(rowOf(m.root, "Codex"))).toContain("<client-key>");
  expect(preText(rowOf(m.root, "Codex"))).not.toContain("sk-secret-1");
  await press(buttonIn(rowOf(m.root, "Codex"), "Make key"));
  expect(posts(w.calls).map((c) => c.body)).toEqual([{ label: "Claude Code" }, { label: "Codex" }]);
  expect(preText(rowOf(m.root, "Codex"))).toContain("sk-secret-2");
  expect(preText(rowOf(m.root, "Codex"))).not.toContain("sk-secret-1");
  // The first row still holds its own key when it opens again.
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  expect(preText(rowOf(m.root, "Claude Code"))).toContain("sk-secret-1");
  expect(preText(rowOf(m.root, "Claude Code"))).not.toContain("sk-secret-2");
});

test("a key that could not be made says why, and step 1 stays", async () => {
  const w = world({
    make: () => ({ status: 500, error: { code: "internal", message: "the settings file is read-only" } }),
  });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  expect(m.text()).toContain("Could not make the key. the settings file is read-only");
  expect(hasButton(rowOf(m.root, "Claude Code"), "Make key")).toBe(true);
  expect(preText(m.root)).toContain("<client-key>");
});

test("closing the key's notice takes it out of the text and offers a new key, saying the old one stays", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  expect(preText(m.root)).toContain("sk-secret-1");
  await press(buttonIn(withPart(m.root, "key-notice")[0] as FakeNode, "Close"));
  expect(withPart(m.root, "key-value")).toHaveLength(0);
  expect(preText(m.root)).not.toContain("sk-secret-1");
  expect(preText(m.root)).toContain("<client-key>");
  expect(m.text()).not.toContain("sk-secret-1");
  // The key is made and its value gone: Make key would add a second one, and says so.
  expect(hasButton(rowOf(m.root, "Claude Code"), "Make another key")).toBe(true);
  expect(rowOf(m.root, "Claude Code").textContent).toContain("pluxy already has a key named Claude Code");
});

test("deleting the key a row's text holds takes it out of the text", async () => {
  const w = world({ keys: [{ id: "key-a", masked: "sk-***48" }] });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  expect(preText(m.root)).toContain("sk-secret-1");
  const made = keyRows(m.root).find((r) => r.textContent.includes("Claude Code")) as FakeNode;
  await press(buttonIn(made, "Delete"));
  await press(buttonIn(made, "Yes, delete"));
  expect(w.calls.some((c) => c.method === "DELETE" && c.path === "/client-keys/key-1")).toBe(true);
  expect(m.text()).not.toContain("sk-secret-1");
  expect(preText(m.root)).toContain("<client-key>");
});

test("the key's value is never given to the host's prefs", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  await press(buttonIn(buttonScope(m.root), "New key"));
  const name = all(m.root, "input")[0] as FakeNode;
  await type(name, "Laptop");
  await submit(all(m.root, "form")[0] as FakeNode);
  expect(m.text()).toContain("sk-secret-2");
  expect(JSON.stringify(w.prefs)).not.toContain("sk-secret");
  expect(w.prefs).toEqual([]);
});

/** The Keys section, to press its own buttons: the rows have Make key and Set up of their own. */
const buttonScope = (root: FakeNode) => {
  const section = descend(root).find((n) => attribute(n, "aria-labelledby") === "tools-keys");
  if (!section) throw new Error("no Keys section");
  return section;
};

// ---- the keys ----------------------------------------------------------------------------------

test("New key asks for a name, sends it, and fills no row's text", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(buttonScope(m.root), "New key"));
  const input = all(m.root, "input")[0] as FakeNode;
  expect(attribute(input, "placeholder")).toBe("e.g. Script on my laptop");
  await type(input, "Script on my laptop");
  await submit(all(m.root, "form")[0] as FakeNode);
  expect(posts(w.calls).map((c) => c.body)).toEqual([{ label: "Script on my laptop" }]);
  expect(withPart(m.root, "key-value").map((n) => n.textContent)).toEqual(["sk-secret-1"]);
  expect(
    keyRows(m.root)
      .map((r) => r.textContent)
      .some((t) => t.includes("Script on my laptop")),
  ).toBe(true);
  expect(preText(m.root)).toContain("<client-key>");
  expect(preText(m.root)).not.toContain("sk-secret-1");
  // The form is closed, and the row still offers its own Make key.
  expect(all(m.root, "form")).toHaveLength(0);
  expect(hasButton(rowOf(m.root, "Claude Code"), "Make key")).toBe(true);
});

test("a New key that fails keeps the name in the form and says why", async () => {
  const w = world({
    make: () => ({ status: 400, error: { code: "invalid_request", message: "label must be one line" } }),
  });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(buttonScope(m.root), "New key"));
  await type(all(m.root, "input")[0] as FakeNode, "Two\nlines");
  await submit(all(m.root, "form")[0] as FakeNode);
  expect(m.text()).toContain("Could not make the key. label must be one line");
  expect((all(m.root, "input")[0] as FakeNode).value).toBe("Two\nlines");
  expect(withPart(m.root, "key-value")).toHaveLength(0);
  await press(buttonIn(buttonScope(m.root), "Cancel"));
  expect(all(m.root, "form")).toHaveLength(0);
});

test("a key's name is its label, then in the app the tool it was made for, then Key sk…48", () => {
  const owners = new Map([["owned", "Codex"]]);
  expect(toolKeyName({ id: "owned", masked: "sk-***48", label: "Script" }, owners)).toEqual({
    text: "Script",
    named: true,
  });
  expect(toolKeyName({ id: "owned", masked: "sk-***48" }, owners)).toEqual({ text: "Codex", named: true });
  expect(toolKeyName({ id: "other", masked: "sk-***48" }, new Map([["sk-***48", "Pi"]]))).toEqual({
    text: "Pi",
    named: true,
  });
  const bare = toolKeyName({ id: "other", masked: "sk-***48" }, owners);
  expect(bare.named).toBe(false);
  expect(bare.text).toMatch(/^Key sk.*48$/);
  expect(bare.text).not.toContain("*");
});

test("the rows show each key by that name, the masked key beside a name, and the week's use", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11", label: "Script" },
      { id: "k2", masked: "sk-***22" },
      { id: "k3", masked: "sk-***33" },
    ],
    used: { k1: 1, k2: 12 },
  });
  const m = await w.mount(<ToolsPage options={{ keyOwners: async () => new Map([["k2", "Codex"]]) }} />);
  const [first, second, third] = keyRows(m.root);
  expect(first?.textContent).toContain("Script");
  expect(first?.textContent).toContain("sk-***11");
  expect(first?.textContent).toContain("1 request this week");
  expect(second?.textContent).toContain("Codex");
  expect(second?.textContent).toContain("sk-***22");
  expect(second?.textContent).toContain("12 requests this week");
  expect(third?.textContent).toMatch(/Key sk.*33/);
  expect(third?.textContent).toContain("Not used this week");
  expect(m.text()).toContain("Keys (3)");
  expect(m.text()).toContain("Every tool sends one of these keys with its requests.");
  expect(m.text()).toContain("A key is shown only once, when it is made.");
});

test("Delete asks first, naming the key when it has a name", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11", label: "Script" },
      { id: "k2", masked: "sk-***22" },
    ],
  });
  const m = await w.mount(<ToolsPage />);
  const [named, unnamed] = keyRows(m.root) as [FakeNode, FakeNode];
  await press(buttonIn(named, "Delete"));
  expect(named.textContent).toContain("Script stops working until it gets a new key.");
  expect(w.calls.some((c) => c.method === "DELETE")).toBe(false);
  await press(buttonIn(named, "Cancel"));
  await press(buttonIn(unnamed, "Delete"));
  expect(unnamed.textContent).toContain("Any tool using this key stops working.");
  await press(buttonIn(unnamed, "Yes, delete"));
  expect(w.calls.filter((c) => c.method === "DELETE").map((c) => c.path)).toEqual(["/client-keys/k2"]);
  expect(keyRows(m.root)).toHaveLength(1);
});

test("with one key Delete is disabled and says to add another first", async () => {
  const w = world({ keys: [{ id: "k1", masked: "sk-***11" }] });
  const m = await w.mount(<ToolsPage />);
  const only = keyRows(m.root)[0] as FakeNode;
  expect(buttonIn(only, "Delete").disabled).toBe(true);
  expect(only.textContent).toContain("Add another key before deleting this one.");
});

test("a refused delete of the last key (another tab) shows the same line, and 404 reads the list again", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11" },
      { id: "k2", masked: "sk-***22" },
    ],
    remove: (call) =>
      call.path.endsWith("k1")
        ? { status: 409, error: { code: "conflict", message: "this is the last client key" } }
        : { status: 404, error: { code: "not_found", message: "no key has this ID" } },
  });
  const m = await w.mount(<ToolsPage />);
  const [first, second] = keyRows(m.root) as [FakeNode, FakeNode];
  expect(first.textContent).not.toContain("Add another key before deleting this one.");
  await press(buttonIn(first, "Delete"));
  await press(buttonIn(first, "Yes, delete"));
  expect(first.textContent).toContain("Add another key before deleting this one.");
  expect(first.textContent).not.toContain("this is the last client key");
  const reads = () => w.calls.filter((c) => c.method === "GET" && c.path === "/client-keys").length;
  const before = reads();
  await press(buttonIn(second, "Delete"));
  await press(buttonIn(second, "Yes, delete"));
  expect(reads()).toBe(before + 1);
});

test("a failed delete says why", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11" },
      { id: "k2", masked: "sk-***22" },
    ],
    remove: () => ({ status: 500, error: { code: "internal", message: "the settings file is read-only" } }),
  });
  const m = await w.mount(<ToolsPage />);
  const first = keyRows(m.root)[0] as FakeNode;
  await press(buttonIn(first, "Delete"));
  await press(buttonIn(first, "Yes, delete"));
  expect(first.textContent).toContain("Could not delete the key. the settings file is read-only");
});

test("Rename saves the name on Save, and an empty name clears it", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11", label: "Script" },
      { id: "k2", masked: "sk-***22" },
    ],
  });
  const m = await w.mount(<ToolsPage />);
  const first = keyRows(m.root)[0] as FakeNode;
  await press(buttonIn(first, "Rename"));
  const input = all(first, "input")[0] as FakeNode;
  expect(input.value).toBe("Script");
  await type(input, "Nightly job");
  await submit(all(first, "form")[0] as FakeNode);
  expect(w.calls.find((c) => c.method === "PATCH")).toEqual({
    method: "PATCH",
    path: "/client-keys/k1",
    body: { label: "Nightly job" },
  });
  expect(keyRows(m.root)[0]?.textContent).toContain("Nightly job");
  await press(buttonIn(keyRows(m.root)[0] as FakeNode, "Rename"));
  await type(all(keyRows(m.root)[0] as FakeNode, "input")[0] as FakeNode, "  ");
  await submit(all(keyRows(m.root)[0] as FakeNode, "form")[0] as FakeNode);
  expect(w.calls.filter((c) => c.method === "PATCH")[1]?.body).toEqual({ label: null });
  expect(keyRows(m.root)[0]?.textContent).toMatch(/Key sk.*11/);
});

test("a failed rename says why and keeps the field", async () => {
  const w = world({
    rename: () => ({ status: 400, error: { code: "invalid_request", message: "label must be one line" } }),
  });
  const m = await w.mount(<ToolsPage />);
  const first = keyRows(m.root)[0] as FakeNode;
  await press(buttonIn(first, "Rename"));
  await type(all(first, "input")[0] as FakeNode, "x");
  await submit(all(first, "form")[0] as FakeNode);
  expect(first.textContent).toContain("Could not rename the key. label must be one line");
  expect(all(first, "input")).toHaveLength(1);
});

test("a pluxy that cannot rename (405) loses Rename on every key and says so once", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11" },
      { id: "k2", masked: "sk-***22" },
    ],
    rename: () => ({ status: 405, error: { code: "method_not_allowed", message: "method not allowed" } }),
  });
  const m = await w.mount(<ToolsPage />);
  expect(keyRows(m.root).every((r) => hasButton(r, "Rename"))).toBe(true);
  const first = keyRows(m.root)[0] as FakeNode;
  await press(buttonIn(first, "Rename"));
  await type(all(first, "input")[0] as FakeNode, "x");
  await submit(all(first, "form")[0] as FakeNode);
  expect(keyRows(m.root).some((r) => hasButton(r, "Rename"))).toBe(false);
  expect(m.text().split("This pluxy cannot rename keys.")).toHaveLength(2);
  expect(all(m.root, "form")).toHaveLength(0);
});

test("with no keys it says any program can use pluxy, and offers New key", async () => {
  const w = world({ keys: [] });
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain("No keys. Until there is one, any program that reaches pluxy can use it.");
  expect(m.text()).toContain("Keys (0)");
  await press(buttonIn(buttonScope(m.root), "New key"));
  expect(all(m.root, "form")).toHaveLength(1);
});

test("while the keys are read, and when they cannot be, the section says so and never says there are none", async () => {
  let reply: Reply<unknown> = { status: 500, error: { code: "internal", message: "the keys are locked" } };
  const w = world({});
  const real = w.host.call;
  w.host.call = (async (method: Method, path: string, body?: unknown) =>
    method === "GET" && path === "/client-keys" ? reply : real(method, path, body)) as Host["call"];
  const m = await w.mount(<ToolsPage />);
  expect(m.text()).toContain("Could not read the keys.");
  expect(m.text()).not.toContain("No keys.");
  reply = { status: 200, body: { keys: [] } };
  await press(buttonIn(buttonScope(m.root), "Try again"));
  expect(m.text()).toContain("No keys.");
});

test("the address is the host's, with Copy, and says who can reach it", async () => {
  const w = world();
  const m = await w.mount(<ToolsPage />);
  const section = descend(m.root).find(
    (n) => attribute(n, "aria-labelledby") === "tools-address",
  ) as FakeNode;
  expect(section.textContent).toContain("http://127.0.0.1:8317");
  expect(section.textContent).toContain("Only programs on this computer can reach it.");
  expect(section.textContent).toContain(
    "Another program can use pluxy too: System lists the exact address each kind needs.",
  );
  expect(hasButton(section, "Copy")).toBe(true);
  const system = all(section, "a").find((a) => a.textContent === "Addresses for other programs");
  expect(system && attribute(system, "href")).toBe("#/system");
});

test("a 401 anywhere signs the person out", async () => {
  const w = world({ make: () => ({ status: 401, error: { code: "unauthorized", message: "no" } }) });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  expect(w.signedOut.times).toBeGreaterThan(0);
  expect(m.text()).not.toContain("Could not make the key.");
});

// ---- words -------------------------------------------------------------------------------------

test("no word on the page is one of the words it must not use", async () => {
  const w = world({
    keys: [
      { id: "k1", masked: "sk-***11", label: "Script" },
      { id: "k2", masked: "sk-***22" },
    ],
    accounts: 0,
  });
  const m = await w.mount(
    <ToolsPage options={{ localPages: [{ name: "Claude Code", page: "claude-code" }] }} />,
  );
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Make key"));
  await press(buttonIn(keyRows(m.root)[0] as FakeNode, "Delete"));
  await press(buttonIn(buttonScope(m.root), "New key"));
  // Text nodes apart, so words in neighbouring elements stay apart.
  const leaves = (node: FakeNode): string[] =>
    node.nodeType === 3 ? [node.textContent] : node.childNodes.flatMap(leaves);
  const text = leaves(m.root).join(" ");
  // What core and the tools name themselves (a settings path, the text for the file) is not the page's own words.
  const own = text
    .replaceAll(/~\/[\w./-]+/g, "")
    .replaceAll(leaves(descend(m.root).find((n) => n.localName === "pre") as FakeNode).join(" "), "");
  for (const banned of [
    /\bconfig\b/i,
    /\bcore\b/i,
    /\bCLI\b/,
    /client key/i,
    /API key/,
    /pluxy (model|key|account|serve|setup)\b/,
    /Show config/,
    /Look again/,
    /Make a key/,
  ]) {
    expect(own).not.toMatch(banned);
  }
});

test("Make key says what it does when pluxy already has a key named for the tool: another one, the old one stays", async () => {
  const w = world({ keys: [{ id: "key-old", masked: "sk-***48", label: "Claude Code" }] });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  const row = rowOf(m.root, "Claude Code");
  expect(row.textContent).toContain(
    "pluxy already has a key named Claude Code, but a key is shown only once, when it is made, so its value cannot be shown again.",
  );
  expect(row.textContent).toContain(
    "The old one keeps working until you delete it in Keys on the Tools page.",
  );
  expect(hasButton(row, "Make key")).toBe(false);
  // Nothing was made by opening the steps; the button makes one more key.
  expect(posts(w.calls)).toHaveLength(0);
  await press(buttonIn(row, "Make another key"));
  expect(posts(w.calls).map((c) => c.body)).toEqual([{ label: "Claude Code" }]);
  expect(preText(rowOf(m.root, "Claude Code"))).toContain("sk-secret-1");
});

test("a tool with no key of its name still says Make key, with no warning", async () => {
  const w = world({ keys: [{ id: "key-a", masked: "sk-***48", label: "Codex" }] });
  const m = await w.mount(<ToolsPage />);
  await press(buttonIn(rowOf(m.root, "Claude Code"), "Set up"));
  const row = rowOf(m.root, "Claude Code");
  expect(hasButton(row, "Make key")).toBe(true);
  expect(row.textContent).not.toContain("already has a key");
  expect(hasButton(row, "Make another key")).toBe(false);
});
