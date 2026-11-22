// The Setup page's states from recorded GET /setup bodies, with a fake host, and the shell's gate.
import { expect, test } from "bun:test";
import type { VNode } from "preact";
import render from "preact-render-to-string";
import { api, type ImportReport, type Package, type Setup, type SetupApplyResult } from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import {
  type ImportState,
  importCommand,
  importProblem,
  neededPicks,
  type SetupOptions,
  SetupView,
} from "../kit/pages/setup";
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
  more: {
    choices?: Choices;
    results?: Results;
    accounts?: number;
    remote?: boolean;
    options?: SetupOptions;
    bringing?: ImportState | "offered";
    packages?: Package[];
  } = {},
) {
  const results = more.results ?? {};
  return html(
    <SetupView
      setup={setup}
      options={more.options}
      remote={more.remote ?? false}
      choices={more.choices ?? {}}
      results={results}
      provider={hasProvider(setup, results, more.packages)}
      accounts={more.accounts ?? 0}
      bring={
        more.bringing && {
          state: more.bringing === "offered" ? undefined : more.bringing,
          onBring: nothing,
          onClose: nothing,
        }
      }
      onLook={nothing}
      onTick={nothing}
      onStart={nothing}
      onAdded={nothing}
      onFinish={nothing}
    />,
  );
}

/** The labels of the page's primary buttons: there is one at a time. */
const primary = (page: string) =>
  [...page.matchAll(/data-variant="primary"[^>]*>([^<]*)</g)].map((m) => m[1]);

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
  expect(primary(page)).toEqual(["Start without a provider"]);
  // An account comes after a provider; a client's text can be shown at once.
  expect(page).toContain("tick one above and press Start");
  expect(page).not.toContain("Sign-in methods");
  expect(page).toContain("No client was found on this machine.");
  expect(page).toContain("Other clients (5)");
  expect(page).toContain(">Finish</button>");
});

test("the account part says what leads to an account: Start, a tick first, or the Extensions page", () => {
  const after = /This comes after (Start|Install)/;
  // A provider is ticked: Start installs it, and the account comes after.
  expect(view(found)).toContain("This comes after Start");
  // Providers are offered and none is ticked: the button there ends setup, so the words say to tick.
  const unticked = view(nothingFound);
  expect(primary(unticked)).toEqual(["Start without a provider"]);
  expect(unticked).not.toMatch(after);
  expect(unticked).toContain("tick one above and press Start");
  // No provider is offered: nothing on this page leads to an account, and the words say where to go.
  const none = view({ ...nothingFound, picks: [] });
  expect(none).not.toMatch(after);
  expect(none).not.toContain("tick one above");
  expect(none).toMatch(/Install one on the <a href="#\/extensions">Extensions<\/a> page/);
  // After setup the page has no Install button until a provider is ticked.
  const later = view({ ...nothingFound, first_run: false });
  expect(later).not.toContain(">Install</button>");
  expect(later).not.toMatch(after);
  expect(later).toContain("tick one above and press Install");
  expect(view({ ...nothingFound, first_run: false }, { choices: { "pluxy/codex": true } })).toContain(
    "This comes after Install",
  );
  expect(view({ ...nothingFound, first_run: false, picks: [] })).toMatch(/<a href="#\/extensions">/);
});

test("while the first scan runs, Start waits for what pluxy will suggest", () => {
  const page = view(scanningFirst);
  // Suggestions come with the scan's end: until then nothing is ticked, and that is not a choice.
  expect(page).not.toContain("pluxy will run with no provider");
  expect(page).not.toContain("Start without a provider");
  expect(primary(page)).toEqual([]);
  expect(page).toContain("pluxy is still looking");
  // Finish stays, and a provider the person ticks meanwhile can be started.
  expect(page).toContain(">Finish</button>");
  expect(primary(view(scanningFirst, { choices: { "pluxy/gemini": true } }))).toEqual(["Start"]);
  // A later scan runs over a kept one, whose suggestions stand: Start does not wait for it.
  expect(primary(view({ ...nothingFound, scanning: true }))).toEqual(["Start without a provider"]);
});

test("found clients, a sign-in and a CLIProxyAPI setup are named, and what they suggest is ticked", () => {
  const page = view(found, { remote: true });
  expect(page).toContain("Found on the machine pluxy runs on");
  expect(page).toContain("<strong>Claude Code 2.4.1</strong>");
  expect(page).toContain("Does not point at pluxy yet");
  expect(page).toContain("<strong>Codex is signed in on this machine</strong>");
  expect(page).toContain("<strong>A CLIProxyAPI setup with 7 accounts</strong>");
  expect(page).toContain("pluxy import cpa ~/.cli-proxy-api/config.yaml");
  expect(page).toContain("Other machines on the network can reach pluxy at this address.");
  // The two suggested providers are ticked, with why; the client pick is no checkbox at all.
  expect(ticked(page)).toBe(2);
  expect(page).toContain("Claude Code is installed on this machine");
  expect(page).not.toContain("Claude Code settings");
  expect(toInstall(found, {}, {})).toEqual(["pluxy/codex", "pluxy/claude"]);
  expect(page).toContain("Start installs: Codex, Claude.");
  expect(primary(page)).toEqual(["Start"]);
  expect(page).toContain("Other clients (4)");
  // With its provider installed and no account, the sign-in found is named above the sign-in.
  const named = "Codex is signed in on this machine. You can sign in to the same account here.";
  expect(page).not.toContain(named);
  expect(view(found, { results: { "pluxy/claude": { status: "done" } } })).not.toContain(named);
  expect(view(found, { results: { "pluxy/codex": { status: "done" } } })).toContain(named);
  // With an account, the part is one line.
  const connected = view(found, { results: { "pluxy/codex": { status: "done" } }, accounts: 2 });
  expect(connected).toContain("2 accounts connected.");
  expect(connected).not.toContain(named);
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
  expect(primary(view(found, { choices, results: second.results }))).toEqual(["Finish"]);

  // A call that fails as a whole changes no result.
  const down = fakeHost(() => ({ status: 0 }));
  const failed = await start(down.api, found, choices, first.results);
  expect(failed.reply.status).toBe(0);
  expect(failed.results).toBe(first.results);
});

/** An installed package as GET /packages shows it: enabled and active unless said otherwise. */
const pkg = (more: Partial<Package> = {}): Package => ({
  id: "pluxy/gemini",
  version: "1.0.0",
  registry: "bundled",
  url: "https://packages.example.test/gemini",
  sha256: "0".repeat(64),
  enabled: true,
  installed_at: "2026-11-20T10:00:00Z",
  state: "active",
  ...more,
});

test("a pick core kept after a failed start says it is installed and not running", () => {
  // Core keeps a package whose program does not start: the next /setup says it is installed.
  const kept: Setup = {
    ...found,
    picks: found.picks.map((p) => (p.id === "pluxy/claude" ? { ...p, installed: true } : p)),
  };
  const results: Results = { "pluxy/claude": { status: "error", error: "the program exited at once" } };
  const page = view(kept, { results });

  // The row says it is not running, and names the error and where to look, instead of "Installed".
  expect(page).toContain('data-tone="warn">Installed, not running<');
  expect(page).not.toContain('data-tone="ok">Installed<');
  expect(page).toContain(
    "Claude was installed but did not start: the program exited at once<br/>Look at it on the Extensions page.",
  );
  // It is not the notice for a pick that was never installed, and Start does not send it again.
  expect(page).not.toContain("Could not install");
  expect(toInstall(kept, {}, results)).not.toContain("pluxy/claude");
  // Its checkbox stays as an installed pick's: ticked, and disabled.
  expect(page).toContain('<input type="checkbox" checked disabled');

  // It is no running provider: Finish does not take the page's primary button.
  expect(hasProvider(kept, results)).toBe(false);

  // With nothing left to install but that failed pick, Finish no longer takes the primary button:
  // the Start part offers to start without a provider instead.
  const onlyFailed: Setup = {
    ...nothingFound,
    picks: [pick("pluxy/claude", "Claude", { installed: true })],
  };
  const noProvider = view(onlyFailed, { results });
  expect(noProvider).toContain("pluxy will run with no provider");
  expect(primary(noProvider)).toEqual(["Start without a provider"]);
  expect(noProvider).toContain('data-variant="quiet">Finish</button>');
});

test("only a provider that is enabled and has not failed counts as running", () => {
  // A pick installed by a Start that worked is a running provider.
  expect(hasProvider(allSet, {})).toBe(true);
  expect(hasProvider(found, { "pluxy/claude": { status: "done" } })).toBe(true);
  // One whose Start failed is not, whatever core says about the install.
  const failedPick: Setup = {
    ...nothingFound,
    picks: [pick("pluxy/claude", "Claude", { installed: true })],
  };
  expect(hasProvider(failedPick, { "pluxy/claude": { status: "error", error: "no start" } })).toBe(false);
  // A package counts only when it is enabled and has not failed.
  const noPicks: Setup = { ...nothingFound, picks: [] };
  expect(hasProvider(noPicks, {}, [pkg()])).toBe(true);
  expect(hasProvider(noPicks, {}, [pkg({ enabled: false, state: "disabled" })])).toBe(false);
  expect(hasProvider(noPicks, {}, [pkg({ state: "failed" })])).toBe(false);
  expect(hasProvider(noPicks, {}, [pkg({ state: "crashed" })])).toBe(false);
  expect(hasProvider(noPicks, {}, [pkg({ state: "quarantined" })])).toBe(false);
  // An installed pick whose package is disabled or has failed is not running, whatever the pick says.
  const installed: Setup = {
    ...nothingFound,
    picks: [pick("pluxy/claude", "Claude", { installed: true })],
  };
  const claude = (more: Partial<Package> = {}) => pkg({ id: "pluxy/claude", ...more });
  expect(hasProvider(installed, {}, [claude()])).toBe(true);
  expect(hasProvider(installed, {}, [claude({ enabled: false, state: "disabled" })])).toBe(false);
  expect(hasProvider(installed, {}, [claude({ state: "failed" })])).toBe(false);
  expect(hasProvider(installed, {}, [claude({ state: "crashed" })])).toBe(false);
  expect(hasProvider(installed, {}, [claude({ state: "quarantined" })])).toBe(false);
  // Another provider that runs still counts.
  expect(hasProvider(installed, {}, [claude({ state: "crashed" }), pkg()])).toBe(true);
  // A client pick's package is a client, not a provider.
  const clientPick: Setup = {
    ...nothingFound,
    picks: [{ ...pick("pluxy/claude-code", "Claude Code settings"), kind: "client" }],
  };
  expect(hasProvider(clientPick, {}, [pkg({ id: "pluxy/claude-code" })])).toBe(false);
});

test("with the only installed provider disabled, the page offers no sign-in and Finish is not primary", () => {
  const installed: Setup = {
    ...nothingFound,
    picks: [pick("pluxy/claude", "Claude", { installed: true })],
  };
  const running = view(installed, { packages: [pkg({ id: "pluxy/claude" })] });
  expect(running).toContain('class="add-account stack"');
  expect(primary(running)).toEqual(["Finish"]);

  const disabled = view(installed, {
    packages: [pkg({ id: "pluxy/claude", enabled: false, state: "disabled" })],
  });
  expect(disabled).not.toContain('class="add-account stack"');
  expect(disabled).toContain("This comes after a provider");
  expect(disabled).toContain("pluxy will run with no provider");
  expect(primary(disabled)).toEqual(["Start without a provider"]);
  expect(disabled).toContain('data-variant="quiet">Finish</button>');
});

test("a pick that was installed and started keeps its Installed badge and offers Finish", () => {
  const done = { ...found, picks: found.picks.map((p) => ({ ...p, installed: true })) };
  const results: Results = { "pluxy/claude": { status: "done" } };
  const page = view(done, { results, accounts: 2 });
  expect(page).toContain('data-tone="ok">Installed<');
  expect(page).not.toContain("Installed, not running");
  expect(page).not.toContain("did not start");
  expect(hasProvider(done, results)).toBe(true);
  expect(primary(page)).toEqual(["Finish"]);
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
  expect(view(more, { accounts: 2 })).not.toContain(">Install</button>");
  expect(view(more, { accounts: 2 })).toContain("Tick the ones you have an account for.");
  expect(view(more, { accounts: 2, choices: { "pluxy/gemini": true } })).toContain(">Install</button>");
});

test("once Start has installed what was ticked, its part goes and the first line stops promising it", () => {
  const before = view(found);
  expect(before).toContain(
    "Nothing is installed until you press Start, and nothing on this page is required.",
  );
  expect(before).toContain("<h2>Start</h2>");
  expect(before).toContain("Start installs: Codex, Claude.");

  const done: Results = { "pluxy/codex": { status: "done" }, "pluxy/claude": { status: "done" } };
  const after = view(found, { results: done });
  expect(toInstall(found, {}, done)).toEqual([]);
  expect(after).not.toContain("<h2>Start</h2>");
  expect(after).not.toContain("Nothing more to install.");
  expect(after).not.toContain("until you press Start");
  expect(after).toContain(
    "pluxy looks at this machine and offers what fits. Nothing on this page is required.",
  );
  expect(primary(after)).toEqual(["Finish"]);

  // With no provider to offer, Start installs nothing either.
  expect(view({ ...nothingFound, picks: [] })).not.toContain("until you press Start");
});

test("after setup, what Install will do is said in plain words", () => {
  const more = { ...allSet, picks: [...allSet.picks, pick("pluxy/gemini", "Gemini")] };
  const page = view(more, { accounts: 2, choices: { "pluxy/gemini": true } });
  expect(page).toContain("This installs Gemini.");
  expect(page).not.toContain("Install installs");
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

test("a host's client action adds its button and its sentence to the clients it names, beside the paste text", () => {
  const hint = "Set up opens the client's own page.";
  const options: SetupOptions = {
    clientAction: (client) =>
      client === "claude-code" ? { label: "Set up", page: "claude-code" } : undefined,
    clientActionHint: hint,
  };
  const row = (page: string) =>
    page.match(/<li data-part="setup-client" data-state="not-configured">.*?<\/li>/s)?.[0] ?? "";
  const buttons = (part: string) => [...part.matchAll(/<button[^>]*>([^<]*)</g)].map((m) => m[1]);

  // Without a host action the row has the paste button alone, and no sentence.
  const plain = view(found);
  expect(buttons(row(plain))).toEqual(["Show what to paste"]);
  expect(plain).not.toContain(hint);

  // With one, Claude Code (found installed) gets the button; the page still has one primary button.
  const page = view(found, { options });
  expect(buttons(row(page))).toEqual(["Set up", "Show what to paste"]);
  expect(page).toContain(hint);
  expect(page.match(/>Set up</g)?.length).toBe(1);
  expect(primary(page)).toEqual(["Start"]);

  // A client that was not found gets none, and with no client to act on the sentence is left out.
  const none = view(nothingFound, { options });
  expect(none).not.toContain(">Set up<");
  expect(none).not.toContain(hint);
  const other: SetupOptions = { ...options, clientAction: () => undefined };
  expect(view(found, { options: other })).not.toContain(hint);
});

/** A dry run's report as core writes one: its three lists, and a provider the accounts need. */
const dryReport: ImportReport = {
  dry_run: true,
  brought: [
    { what: "api-keys", as: "1 client key" },
    { what: "auth file", name: "gemini-work.json", as: "pluxy/gemini account gemini-work" },
  ],
  changed: [
    {
      what: "port",
      from: "0.0.0.0:8317",
      to: "127.0.0.1:8317",
      why: "core keeps listening on 127.0.0.1:8317: listen cannot change while core runs",
      keep_with: '"listen": "0.0.0.0:8317" in pluxy\'s config file, then restart core',
    },
  ],
  not_brought: [
    { what: "debug", why: "this core writes no debug-level log lines" },
    { what: "management.secret-key", why: "pluxy keeps its own management key" },
    { what: "auth file", name: "claude-a.json", why: "core already has an account claude-a, which it keeps" },
    { what: "auth file", name: "kiro-a.json", why: "pluxy has no provider for it" },
  ],
  needs: [
    { pick: "pluxy/gemini", why: "1 account needs it" },
    { pick: "example/other", why: "2 accounts need it" },
  ],
};
const config = "~/.cli-proxy-api/config.yaml";

test("a found CLIProxyAPI setup offers to bring it across, with the command beside it", () => {
  const page = view(found, { bringing: "offered" });
  expect(page).toContain(">Bring it across</button>");
  expect(page).toContain("writes nothing until you confirm");
  // The command does what the button does: it names the folder the accounts were counted in.
  expect(page).toContain("<summary>Or use the command</summary>");
  expect(importCommand(found.found.imports[0] as Setup["found"]["imports"][number])).toBe(
    "pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/.cli-proxy-api",
  );
  expect(importCommand({ kind: "cliproxyapi", config, auth_dir: "~/.cli-proxy-api", accounts: 0 })).toBe(
    "pluxy import cpa ~/.cli-proxy-api/config.yaml",
  );
  // The offer does not take the page's one primary button.
  expect(primary(page)).toEqual(["Start"]);
  expect(view(found, { bringing: { config, busy: "checking" } })).toContain(">Checking…</button>");
});

test("the dry run's report shows core's three lists with counts, what is needed, and asks before importing", () => {
  const page = view(found, { bringing: { config, report: dryReport } });
  expect(page).toContain("Nothing is written yet.");
  expect(page).toContain("<summary>Comes across (2)</summary>");
  expect(page).toContain("<summary>Different in pluxy (1)</summary>");
  expect(page).toContain("<summary>Does not come across (4)</summary>");
  expect(page).toContain('<strong class="mono">auth file gemini-work.json</strong>');
  expect(page).toContain("pluxy/gemini account gemini-work");
  expect(page).toContain("core already has an account claude-a, which it keeps");
  expect(page).toContain("pluxy has no provider for it");
  // A row that stays behind says why, the management key included.
  expect(page).toContain("pluxy keeps its own management key");
  expect(page).toContain("&quot;listen&quot;: &quot;0.0.0.0:8317&quot; in pluxy's config file");
  expect(page).toContain("Gemini is not installed, and 1 account needs it. You can install it once");
  expect(page).toContain("example/other is not installed, and 2 accounts need it.");
  expect(page).toContain(">Bring it across now</button>");
  expect(page).toContain(">Cancel</button>");
  // The row's own button and the command make way for the report.
  expect(page).not.toContain(">Bring it across</button>");
  expect(page).not.toContain("Or use the command");
});

test("after the import the report says what was brought, and where to install what the accounts need", () => {
  const report: ImportReport = { ...dryReport, dry_run: false };
  // Gemini is on the page's list, so it is ticked; a package the page does not offer is not.
  expect(neededPicks(found, report)).toEqual({ "pluxy/gemini": true });

  const page = view(found, { bringing: { config, report, done: true } });
  expect(page).toContain("Done. This is what was brought across:");
  expect(page).toContain("<summary>Brought across (2)</summary>");
  expect(page).toContain("<summary>Not brought across (4)</summary>");
  expect(page).toContain("It is ticked under Providers: press Start to install it.");
  expect(page).toContain(
    "example/other is not installed, and 2 accounts need it. Install it on the Extensions page.",
  );
  expect(page).toContain(">Close</button>");
  expect(page).not.toContain("Bring it across now");
  // The import keeps pluxy's management key, so there is no new key and no sign-in to send to.
  expect(page).not.toContain("Sign in again");
  expect(page).not.toContain("New management key");
});

/** The same machine once core has a record of the setup's import. */
function brought(last: { brought: number; changed: number; not_brought: number }): Setup {
  const at = "2026-11-20T08:30:05Z";
  return {
    ...found,
    found: {
      ...found.found,
      imports: found.found.imports.map((row) => ({ ...row, last_import: { at, ...last } })),
    },
  };
}

test("a setup core has brought across says so, when and with what counts, and offers only the command", () => {
  const last = { brought: 4, changed: 2, not_brought: 3 };
  const at = new Date("2026-11-20T08:30:05Z").toLocaleString();
  for (const bringing of ["offered", undefined] as const) {
    const page = view(brought(last), { bringing });
    expect(page).toContain('data-part="setup-import" data-state="brought"');
    expect(page).toContain("<strong>A CLIProxyAPI setup with 7 accounts</strong>");
    expect(page).toContain(
      `<span class="mono">~/.cli-proxy-api/config.yaml</span>, and was brought across on ${at}.`,
    );
    // The numbers count rows of the report, settings and auth files alike: never accounts.
    expect(page).toContain(
      "Of its settings and auth files: 4 brought across, 2 different in pluxy, 3 not brought across.",
    );
    expect(page).toContain(">Brought across</span>");
    // No button offers it again; the command still can, and says what a second import does.
    expect(page).not.toContain(">Bring it across</button>");
    expect(page).not.toContain("writes nothing until you confirm");
    expect(page).not.toContain("Or use the command");
    expect(page).toContain("<summary>Bring it across again</summary>");
    expect(page).toContain("It writes the file's settings over any you changed in pluxy since");
    expect(page).toContain(
      "<pre>pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/.cli-proxy-api</pre>",
    );
  }
  // A list that held no row is left out.
  const clean = view(brought({ brought: 5, changed: 0, not_brought: 0 }), { bringing: "offered" });
  expect(clean).toContain("Of its settings and auth files: 5 brought across.");
  expect(clean).not.toContain("not brought across");
  expect(clean).not.toContain("different in pluxy");
});

test("the report of an import just made stays until it is closed, whatever core has recorded", () => {
  const report: ImportReport = { ...dryReport, dry_run: false };
  const page = view(brought({ brought: 2, changed: 1, not_brought: 4 }), {
    bringing: { config, report, done: true },
  });
  expect(page).toContain('data-part="setup-import" data-state="done"');
  expect(page).toContain("Done. This is what was brought across:");
  expect(page).toContain(">Close</button>");
  expect(page).not.toContain("was brought across on");
  expect(page).not.toContain("Bring it across again");
});

test("each refusal is said in words a person can act on", () => {
  const refused = (status: number, message: string, dryRun = true) =>
    importProblem({ status, error: { code: "x", message } }, dryRun);
  expect(importProblem({ status: 200, body: dryReport }, true)).toEqual({});
  expect(refused(400, "there is no file at /home/a/.cli-proxy-api/config.yaml").problem).toBe(
    "pluxy could not read the CLIProxyAPI setup: there is no file at /home/a/.cli-proxy-api/config.yaml. Check that the file is still there and that pluxy may read it, then press Look again. Nothing was changed.",
  );
  expect(refused(422, "config.yaml: port must be a number").problem).toBe(
    "pluxy will not bring this setup across: config.yaml: port must be a number. Change that in the CLIProxyAPI setup, then try again. Nothing was changed.",
  );
  expect(refused(500, "disk full.").problem).toBe("pluxy could not bring the setup across: disk full.");
  expect(importProblem({ status: 0 }, true).problem).toContain("nothing was read or changed");
  expect(importProblem({ status: 0 }, false).problem).toContain("cannot tell whether the import ran");

  // A core on a config.yaml takes no import: the page says how to get to one that does.
  const yaml = refused(409, "core runs on a config.yaml", false);
  expect(yaml.show).toBe("init");
  const onYaml = view(found, { bringing: { config, ...yaml } });
  expect(onYaml).toContain("This pluxy runs on a config.yaml");
  expect(onYaml).toContain("<pre>pluxy init</pre>");
  expect(onYaml).toContain(">Bring it across</button>");

  // An older core has no such call: the row names the command, as it did before, and no button.
  const old = refused(404, "not found");
  expect(old.show).toBe("command");
  const onOld = view(found, { bringing: { config, ...old } });
  expect(onOld).toContain("This pluxy is an older one");
  expect(onOld).toContain("run this where pluxy is installed:");
  expect(onOld).toContain(
    "<pre>pluxy import cpa ~/.cli-proxy-api/config.yaml --auth-dir ~/.cli-proxy-api</pre>",
  );
  expect(onOld).not.toContain(">Bring it across</button>");
  expect(onOld).not.toContain("Or use the command");
});

test("when core has no extension registry, Setup says so and names what to do next", () => {
  const noReg = view({ ...nothingFound, picks: [], registries: 0 });
  expect(noReg).toContain("This build has no extension registry");
  expect(noReg).toContain("go run ./cmd/pluxy-pack -out");
  expect(noReg).toContain("-bundled");
  expect(noReg).not.toContain("The Extensions page lists what can be installed.");
  expect(noReg).not.toContain("#/extensions");

  // With a registry configured (even if no setup picks match), the no-registry message does not appear.
  const hasReg = view({ ...nothingFound, picks: [], registries: 1 });
  expect(hasReg).not.toContain("This build has no extension registry");
  expect(hasReg).toContain("The Extensions page lists what can be installed.");
});
