// The Setup page's states from recorded GET /setup bodies, with a fake host, and the shell's gate.
import { expect, test } from "bun:test";
import type { VNode } from "preact";
import render from "preact-render-to-string";
import { api, type Setup, type SetupApplyResult } from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { SetupView } from "../kit/pages/setup";
import {
  type Choices,
  hasProvider,
  isTicked,
  onThisMachine,
  type Results,
  scanState,
  start,
  toInstall,
  withKey,
} from "../kit/pages/setup.helpers";
import { setupGate } from "../kit/shell";
import { type Kit, KitContext } from "../kit/ui";

const core = { listen: "127.0.0.1:8317", reachable_from_network: false };
const noClients = ["claude-code", "codex", "opencode", "pi", "omp"].map((id) => ({
  id,
  name:
    { "claude-code": "Claude Code", codex: "Codex CLI", opencode: "OpenCode", pi: "Pi", omp: "Oh My Pi" }[
      id
    ] ?? id,
  installed: false,
  config: `~/.${id}/settings.json`,
  state: "not-configured" as const,
}));
const pick = (id: string, title: string, more: Partial<Setup["picks"][number]> = {}) => ({
  id,
  kind: "provider",
  title,
  summary: `${title} subscriptions and API keys`,
  installed: false,
  suggested: false,
  ...more,
});

/** A new install whose machine holds nothing pluxy looks for. */
const nothingFound: Setup = {
  first_run: true,
  scanning: false,
  scanned_at: "2026-11-19T21:20:40Z",
  found: { clients: noClients, sign_ins: [], imports: [], core },
  picks: [pick("pluxy/codex", "Codex"), pick("pluxy/claude", "Claude")],
};

/** A new install on a machine with Claude Code, a Codex sign-in and a CLIProxyAPI setup. */
const found: Setup = {
  first_run: true,
  scanning: false,
  scanned_at: "2026-11-19T21:20:40Z",
  found: {
    clients: [
      {
        ...noClients[0],
        id: "claude-code",
        name: "Claude Code",
        installed: true,
        version: "2.4.1",
        config: "~/.claude/settings.json",
        state: "not-configured",
      },
      ...noClients.slice(1),
    ],
    sign_ins: [{ provider: "pluxy/codex", from: "codex", label: "Codex is signed in on this machine" }],
    imports: [
      {
        kind: "cliproxyapi",
        config: "~/.cli-proxy-api/config.yaml",
        auth_dir: "~/.cli-proxy-api",
        accounts: 7,
      },
    ],
    core: { listen: "0.0.0.0:8317", reachable_from_network: true },
  },
  picks: [
    pick("pluxy/codex", "Codex", { suggested: true, because: "Codex is signed in on this machine" }),
    pick("pluxy/claude", "Claude", { suggested: true, because: "Claude Code is installed on this machine" }),
    pick("pluxy/gemini", "Gemini"),
    { ...pick("pluxy/claude-code", "Claude Code settings", { suggested: true }), kind: "client" },
  ],
};

/** The first scan of a new install, still running: nothing is known yet. */
const scanningFirst: Setup = {
  first_run: true,
  scanning: true,
  found: { clients: [], sign_ins: [], imports: [], core },
  picks: found.picks.map((p) => ({ ...p, suggested: false, because: undefined })),
};

/** An install whose setup is over: providers installed, a client pointed at it. */
const allSet: Setup = {
  first_run: false,
  scanning: false,
  scanned_at: "2026-11-19T21:20:40Z",
  found: {
    clients: [
      { ...found.found.clients[0], state: "configured" } as Setup["found"]["clients"][number],
      ...noClients.slice(1),
    ],
    sign_ins: [],
    imports: [],
    core,
  },
  picks: [
    pick("pluxy/codex", "Codex", { installed: true }),
    pick("pluxy/claude", "Claude", { installed: true }),
  ],
};

interface Call {
  method: Method;
  path: string;
  body?: unknown;
}

/** A host that answers from answer and records what it was asked. */
function fakeHost(answer: (call: Call) => Reply<unknown> = () => ({ status: 200, body: {} })) {
  const calls: Call[] = [];
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) => {
      calls.push({ method, path, body });
      return answer({ method, path, body }) as Reply<T>;
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(),
  };
  return { host, calls, api: api(host) };
}

const html = (node: VNode) => {
  const { host } = fakeHost();
  const kit = { host, api: api(host), unauthorized: () => {}, extensionsChanged: () => {} } as Kit;
  return render(<KitContext.Provider value={kit}>{node}</KitContext.Provider>);
};

const nothing = () => {};
function view(
  setup: Setup,
  more: { choices?: Choices; results?: Results; accounts?: number; remote?: boolean } = {},
) {
  const results = more.results ?? {};
  return html(
    <SetupView
      setup={setup}
      remote={more.remote ?? false}
      choices={more.choices ?? {}}
      results={results}
      provider={hasProvider(setup, results)}
      accounts={more.accounts ?? 0}
      onLook={nothing}
      onTick={nothing}
      onStart={nothing}
      onAdded={nothing}
      onFinish={nothing}
    />,
  );
}

/** How many checkboxes the page shows ticked. */
const ticked = (page: string) => page.match(/<input type="checkbox" checked/g)?.length ?? 0;

test("a first run with nothing found offers every provider unticked, and may start without one", () => {
  const page = view(nothingFound);
  expect(page).toContain("No client, sign-in or CLIProxyAPI setup was found.");
  expect(page).toContain("pluxy listens on ");
  expect(page).not.toContain("Other machines on the network");
  expect(page).toContain("<strong>Codex</strong>");
  expect(ticked(page)).toBe(0);
  expect(toInstall(nothingFound, {}, {})).toEqual([]);
  expect(page).toContain("pluxy will run with no provider");
  expect(page).toContain(">Start without a provider</button>");
  // An account comes after a provider; a client's text can be shown at once.
  expect(page).toContain("This comes after Start");
  expect(page).not.toContain("Sign-in methods");
  expect(page).toContain("No client was found on this machine.");
  expect(page).toContain("Other clients (5)");
  expect(page).toContain(">Finish</button>");
});

test("found clients, a sign-in and a CLIProxyAPI setup are named, and what they suggest is ticked", () => {
  const page = view(found, { remote: true });
  expect(page).toContain("Found on the machine pluxy runs on");
  expect(page).toContain("<strong>Claude Code 2.4.1</strong>");
  expect(page).toContain("Does not point at pluxy yet");
  expect(page).toContain("<strong>Codex is signed in on this machine</strong>");
  expect(page).toContain("A CLIProxyAPI setup with 7 accounts is at ");
  expect(page).toContain("pluxy import cpa ~/.cli-proxy-api/config.yaml");
  expect(page).toContain("Other machines on the network can reach pluxy at this address.");
  // The two suggested providers are ticked, with why; the client pick is no checkbox at all.
  expect(ticked(page)).toBe(2);
  expect(page).toContain("Claude Code is installed on this machine");
  expect(page).not.toContain("Claude Code settings");
  expect(toInstall(found, {}, {})).toEqual(["pluxy/codex", "pluxy/claude"]);
  expect(page).toContain("Start installs: Codex, Claude.");
  expect(page).toContain("Other clients (4)");
  // With a provider installed and no account, the sign-in found is named above the sign-in.
  const after = view(found, { results: { "pluxy/codex": { status: "done" } } });
  expect(after).toContain("Codex is signed in on this machine. You can sign in to the same account here.");
  // With an account, the part is one line.
  const connected = view(found, { results: { "pluxy/codex": { status: "done" } }, accounts: 2 });
  expect(connected).toContain("2 accounts connected.");
  expect(connected).not.toContain("You can sign in to the same account here.");
});

test("a client's state is said in words, and a refused file says why that is fine", () => {
  const withState = (state: Setup["found"]["clients"][number]["state"]) =>
    view({
      ...found,
      found: {
        ...found.found,
        clients: [{ ...found.found.clients[0], state } as Setup["found"]["clients"][number]],
      },
    });
  expect(withState("configured")).toContain("Points at this pluxy");
  expect(withState("needs-update")).toContain("Its settings need an update to work with this pluxy");
  expect(withState("refused")).toContain("pluxy would not change ~/.claude/settings.json");
  expect(withState("refused")).toContain("you paste the text yourself");
});

test("the first scan says pluxy is looking, and the person's ticks survive the replies that follow", () => {
  expect(scanState(scanningFirst)).toBe("first");
  const page = view(scanningFirst);
  expect(page).toContain("pluxy is looking at this machine.");
  expect(page).not.toContain("was found");
  expect(page).not.toContain("No client was found on this machine.");
  expect(page).toContain(">Looking…</button>");

  // While it runs, the person ticks Gemini and, once Codex is suggested, unticks it.
  const choices: Choices = { "pluxy/gemini": true, "pluxy/codex": false };
  expect(toInstall(scanningFirst, choices, {})).toEqual(["pluxy/gemini"]);
  expect(scanState(found)).toBe("done");
  // The scan's suggestion (Claude) arrives ticked; the person's own two choices stand.
  expect(toInstall(found, choices, {})).toEqual(["pluxy/claude", "pluxy/gemini"]);
  expect(found.picks.filter((p) => isTicked(p, choices, {})).map((p) => p.id)).not.toContain("pluxy/codex");
  expect(ticked(view(found, { choices }))).toBe(2);
  expect(scanState({ ...found, scanning: true })).toBe("again");
  expect(view({ ...found, scanning: true })).toContain("<strong>Claude Code 2.4.1</strong>");
  expect(scanState({ ...nothingFound, scanned_at: undefined })).toBe("never");
});

test("Start sends only ticked providers; a failed one says why and is sent again alone", async () => {
  const replies: SetupApplyResult[][] = [
    [
      { id: "pluxy/codex", status: "done" },
      { id: "pluxy/claude", status: "error", error: "the archive's digest does not match" },
    ],
    [{ id: "pluxy/claude", status: "done" }],
  ];
  const fake = fakeHost(() => ({ status: 200, body: { results: replies.shift() } }));
  // A ticked client pick is never sent.
  const choices: Choices = { "pluxy/claude-code": true };

  const first = await start(fake.api, found, choices, {});
  expect(fake.calls).toEqual([
    { method: "POST", path: "/setup/apply", body: { picks: ["pluxy/codex", "pluxy/claude"] } },
  ]);
  expect(first.results).toEqual({
    "pluxy/codex": { status: "done", error: undefined },
    "pluxy/claude": { status: "error", error: "the archive's digest does not match" },
  });
  const page = view(found, { choices, results: first.results });
  expect(page).toContain('data-part="setup-pick" data-state="installed"');
  expect(page).toContain('data-part="setup-pick" data-state="failed"');
  expect(page).toContain(
    "Could not install Claude: the archive's digest does not match<br/>Press Start to try again.",
  );
  expect(page).toContain("Start installs: Claude.");

  const second = await start(fake.api, found, choices, first.results);
  expect(fake.calls[1]).toEqual({ method: "POST", path: "/setup/apply", body: { picks: ["pluxy/claude"] } });
  expect(second.results["pluxy/codex"]?.status).toBe("done");
  expect(second.results["pluxy/claude"]?.status).toBe("done");
  expect(view(found, { choices, results: second.results })).not.toContain("Could not install");
  expect(toInstall(found, choices, second.results)).toEqual([]);

  // A call that fails as a whole changes no result.
  const down = fakeHost(() => ({ status: 0 }));
  const failed = await start(down.api, found, choices, first.results);
  expect(failed.reply.status).toBe(0);
  expect(failed.results).toBe(first.results);
});

test("opened again after setup, the page is a status and offers no Finish", () => {
  const page = view(allSet, { accounts: 2 });
  expect(page).toContain("Setup is finished.");
  expect(page).toContain("Every provider on the list is installed.");
  expect(page).toContain("2 accounts connected.");
  expect(page).toContain("Points at this pluxy");
  expect(page).not.toContain(">Finish</button>");
  expect(page).not.toContain(">Start</button>");
  expect(page).not.toContain("Start without a provider");
  expect(ticked(page)).toBe(2); // installed: shown ticked, and cannot be changed
  expect(page.match(/<input type="checkbox" checked disabled/g)?.length).toBe(2);
  // One more provider can still be installed from here.
  const more = { ...allSet, picks: [...allSet.picks, pick("pluxy/gemini", "Gemini")] };
  expect(view(more, { accounts: 2, choices: { "pluxy/gemini": true } })).toContain(">Install</button>");
});

test("the paste text takes a new client key where its placeholder stood", () => {
  const content = '{"env": {"ANTHROPIC_AUTH_TOKEN": "<client-key>"}}';
  expect(withKey(content, "<client-key>")).toBe(content);
  expect(withKey(content, "<client-key>", "sk-new")).toBe('{"env": {"ANTHROPIC_AUTH_TOKEN": "sk-new"}}');
});

test("the paste call names the client, and a key when asked", async () => {
  const fake = fakeHost();
  await fake.api.clientPaste("pluxy/pi-setup");
  await fake.api.clientPaste("claude-code", "key 1");
  expect(fake.calls.map((c) => c.path)).toEqual([
    "/setup/clients/pluxy%2Fpi-setup/paste",
    "/setup/clients/claude-code/paste?key_id=key%201",
  ]);
});

test("core on another machine than the browser is said to be so", () => {
  expect(onThisMachine(undefined)).toBe(true);
  expect(onThisMachine("http://127.0.0.1:8317")).toBe(true);
  expect(onThisMachine("http://localhost:8317")).toBe(true);
  expect(onThisMachine("http://[::1]:8317")).toBe(true);
  expect(onThisMachine("https://pluxy.example:8317")).toBe(false);
});

test("the gate opens Setup on a first run, unless the address names a page or core lacks the call", () => {
  const firstRun: Reply<{ first_run: boolean }> = { status: 200, body: { first_run: true } };
  expect(setupGate(firstRun, "")).toEqual({ offered: true, open: true });
  expect(setupGate(firstRun, "#/")).toEqual({ offered: true, open: true });
  // A named page wins.
  expect(setupGate(firstRun, "#/accounts")).toEqual({ offered: true, open: false });
  expect(setupGate({ status: 200, body: { first_run: false } }, "")).toEqual({ offered: true, open: false });
  // A core without the call: no page and no gate.
  expect(setupGate({ status: 404, error: { code: "not_found", message: "no such call" } }, "")).toEqual({
    offered: false,
    open: false,
  });
  // A core that could not be asked keeps the page, which then says what failed, and opens nothing.
  expect(setupGate({ status: 0 }, "")).toEqual({ offered: true, open: false });
});
