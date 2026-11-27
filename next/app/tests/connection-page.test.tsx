// First launch, the not-running pane and Where pluxy runs (src/firstLaunch.tsx, connection.tsx,
// startup.tsx, window.tsx, connectionCopy.ts): which words and which buttons each state of the
// app's connection gives, and what each button sends to the native side. These drive the real
// components through Preact's DOM renderer against a fake window command channel, so every command
// a screen sends is recorded and every answer is chosen by the test.
import { afterEach, expect, test } from "bun:test";
import { type ComponentChildren, render } from "preact";
import { act } from "preact/test-utils";
import { type Kit, KitContext } from "../../ui/kit/ui";
import { ConnectionPage } from "../src/connection";
import { addressKind, paneOf } from "../src/connectionCopy";
import { FirstLaunch, NotRunningPane } from "../src/firstLaunch";
import type { Connection, KeyEntryResult, StartupView, Versions, WindowPreferences } from "../src/host";
import { all, type FakeNode, installDocument, restoreDom, settle } from "./fakeDom";

afterEach(() => {
  restoreDom();
  Reflect.deleteProperty(globalThis, "location");
});

type Handler = unknown | ((args: Record<string, unknown> | undefined) => unknown);
type Handlers = Record<string, Handler>;

/** A tree mounted on a page of its own, with the window commands it sends answered by handlers. */
async function mount(tree: ComponentChildren, handlers: Handlers = {}) {
  const document = installDocument();
  Object.defineProperty(globalThis, "location", { configurable: true, value: { hash: "" } });
  const calls: { cmd: string; args: Record<string, unknown> | undefined }[] = [];
  Object.assign((globalThis as unknown as { window: object }).window, {
    __TAURI_INTERNALS__: {
      invoke: (cmd: string, args?: Record<string, unknown>) => {
        calls.push({ cmd, args });
        if (!(cmd in handlers)) return Promise.reject(`not answered by this test: ${cmd}`);
        const h = handlers[cmd];
        return Promise.resolve().then(() => (typeof h === "function" ? h(args) : h));
      },
      transformCallback: () => 1,
    },
  });
  const root = document.createElement("main");
  document.body.appendChild(root);
  const kit = {
    host: { features: new Set<string>(), signOut: () => {} },
    unauthorized: () => {},
  } as unknown as Kit;
  const show = async (next: ComponentChildren) => {
    await act(async () => {
      render(<KitContext.Provider value={kit}>{next}</KitContext.Provider>, root as unknown as Element);
    });
    await settle();
  };
  await show(tree);
  const attr = (n: FakeNode, name: string) => n.attributes.find((a) => a.name === name)?.value;
  const fire = async (node: FakeNode, type: string) => {
    await act(async () => {
      node.dispatchEvent(new Event(type, { cancelable: true }));
    });
    await settle();
  };
  /** The controls that act: buttons, and links drawn as buttons. */
  const buttons = () => [
    ...all(root, "button"),
    ...all(root, "a").filter((a) => attr(a, "class")?.includes("btn")),
  ];
  const button = (label: string) => {
    // A choice button holds its title and its line: it is named by its title.
    const b =
      buttons().find((b) => b.textContent === label) ??
      buttons().find((b) => b.textContent.startsWith(label));
    if (!b)
      throw new Error(
        `no button ${label}: ${buttons()
          .map((b) => b.textContent)
          .join(" | ")}`,
      );
    return b;
  };
  return {
    root,
    calls,
    /** Draws the tree again in place, as the window does when the connection changes. */
    show,
    attr,
    text: () => root.textContent,
    labels: () => buttons().map((b) => b.textContent),
    button,
    click: (label: string) => fire(button(label), "click"),
    fire,
    input: async (label: string, value: string) => {
      const name = all(root, "label").find((l) => l.textContent === label);
      const input = name?.parentNode && all(name.parentNode, "input")[0];
      if (!input) throw new Error(`no field ${label}`);
      input.value = value;
      await fire(input, "input");
    },
    submit: () => fire(all(root, "form")[0] as FakeNode, "submit"),
    commands: (cmd: string) => calls.filter((c) => c.cmd === cmd).map((c) => c.args),
  };
}

const view = (over: Partial<Connection> = {}): Connection => ({
  signed_in: false,
  remembered: false,
  ...over,
});
const noop = () => {};
const never = () => new Promise<never>(() => {});

// ---- first launch ------------------------------------------------------------------------------------------

test("the first launch screen is two buttons and nothing else, and no control is there to type in", async () => {
  const m = await mount(<FirstLaunch connection={view()} onChange={noop} />);
  expect(m.labels().length).toBe(2);
  expect(m.labels()[0]).toContain("On this computer");
  expect(m.labels()[1]).toContain("Connect to a pluxy that is already running");
  for (const tag of ["input", "select", "textarea", "details", "summary", "form", "a"])
    expect(all(m.root, tag).length).toBe(0);
  expect(m.text()).toContain("Where should pluxy run?");
  expect(m.text()).toContain(
    "pluxy connects your coding tools to your AI accounts. It needs to run somewhere first.",
  );
  expect(m.text()).toContain("Most people pick this.");
  expect(m.text()).not.toContain("The app could not read its saved settings");
});

test("a damaged settings file shows as one quiet line above the two choices", async () => {
  const m = await mount(
    <FirstLaunch connection={view({ error: "expected value at line 1" })} onChange={noop} />,
  );
  expect(m.text()).toContain("The app could not read its saved settings, so it is starting fresh.");
  expect(m.text()).not.toContain("expected value at line 1");
  expect(m.labels().length).toBe(2);
});

test("On this computer asks for nothing else, shows the starting line until pluxy answers, and hands over the reply", async () => {
  let answer: (v: Connection) => void = noop;
  const reply = view({ mode: "managed", target: "http://127.0.0.1:41000", signed_in: true });
  const changed: Connection[] = [];
  const m = await mount(<FirstLaunch connection={view()} onChange={(c) => changed.push(c)} />, {
    connection_set: () => new Promise<Connection>((resolve) => (answer = resolve)),
  });
  await m.click("On this computer");
  expect(m.commands("connection_set")).toEqual([{ change: { mode: "managed" } }]);
  expect(m.text()).toContain("Starting pluxy on this computer…");
  expect(m.text()).toContain("This takes a few seconds the first time.");
  // Nothing can be pressed while it starts.
  expect(m.labels()).toEqual([]);
  expect(changed).toEqual([]);
  await act(async () => answer(reply));
  await settle();
  expect(changed).toEqual([reply]);
});

test("a start the native side refuses goes back to the two choices with the reason in plain words", async () => {
  const m = await mount(<FirstLaunch connection={view()} onChange={noop} />, {
    connection_set: () => Promise.reject("the app is quitting"),
  });
  await m.click("On this computer");
  expect(m.text()).toContain("The app is quitting.");
  expect(m.labels().length).toBe(2);
});

// ---- the address of a pluxy that already runs --------------------------------------------------------------

test("the address decides the mode the native side holds it to, and anything it would refuse is refused first", () => {
  const kind = (text: string, file?: string) => {
    const k = addressKind(text, file);
    return [k.ok, k.mode];
  };
  expect(kind("http://127.0.0.1:8317")).toEqual([true, "local"]);
  expect(kind("http://localhost:8317")).toEqual([true, "local"]);
  expect(kind("http://127.1.2.3:9")).toEqual([true, "local"]);
  expect(kind("http://[::1]:8317")).toEqual([true, "local"]);
  expect(kind("https://127.0.0.1:8317")).toEqual([true, "local"]);
  expect(kind("https://127.0.0.1:8317", "/etc/ca.pem")).toEqual([true, "remote"]);
  expect(kind("https://pluxy.example")).toEqual([true, "remote"]);
  expect(kind("https://pluxy.example", "/etc/ca.pem")).toEqual([true, "remote"]);
  expect(kind("http://pluxy.example")).toEqual([false, undefined]);
  expect(kind("http://10.0.0.5:8317")).toEqual([false, undefined]);
  expect(kind("ftp://pluxy.example")).toEqual([false, undefined]);
  expect(kind("pluxy.example")).toEqual([false, undefined]);
  expect(kind("")).toEqual([false, undefined]);
  expect(addressKind("http://pluxy.example").hint).toBe(
    "Another computer needs an https:// address, so its key is never sent where others can read it.",
  );
});

async function connectStep(handlers: Handlers = {}) {
  const changed: Connection[] = [];
  const m = await mount(<FirstLaunch connection={view()} onChange={(c) => changed.push(c)} />, handlers);
  await m.click("Connect to a pluxy that is already running");
  return { m, changed };
}

test("Connect shows the form under its title, with the hint following what is typed, and Back returns", async () => {
  const { m } = await connectStep();
  expect(m.text()).toContain("Such as https://pluxy.example, or http://127.0.0.1:8317 on this computer.");
  expect((m.button("Continue") as FakeNode).disabled).toBe(true);
  await m.input("Address", "http://127.0.0.1:8317");
  expect(m.text()).toContain("On this computer. It runs on its own; the app only connects to it.");
  expect((m.button("Continue") as FakeNode).disabled).toBe(false);
  // The certificate option is for an https address only.
  expect(m.text()).not.toContain("It uses a certificate I made myself");
  await m.input("Address", "https://pluxy.example");
  expect(m.text()).toContain("On another computer, over https.");
  expect(m.text()).toContain("It uses a certificate I made myself");
  expect(m.text()).toContain("Next, a small window asks for its management key.");
  await m.input("Address", "http://pluxy.example");
  expect(m.text()).toContain("Another computer needs an https:// address");
  expect((m.button("Continue") as FakeNode).disabled).toBe(true);
  await m.click("Back");
  expect(m.text()).toContain("Where should pluxy run?");
  expect(m.labels().length).toBe(2);
});

test("an address the native side would refuse is refused before any call is made", async () => {
  const { m } = await connectStep({ key_entry: never });
  await m.input("Address", "http://pluxy.example");
  await m.submit();
  await m.input("Address", "not an address");
  await m.submit();
  expect(m.commands("key_entry")).toEqual([]);
});

test("Continue sends only the mode, the address and the certificate file; no key is ever typed here", async () => {
  const { m } = await connectStep({
    key_entry: (): KeyEntryResult => ({ status: "cancelled", connection: view() }),
  });
  await m.input("Address", " https://pluxy.example ");
  await m.input("Certificate file", "/etc/pluxy/ca.pem");
  await m.submit();
  expect(m.commands("key_entry")).toEqual([
    { change: { mode: "remote", url: "https://pluxy.example", ca_file: "/etc/pluxy/ca.pem" } },
  ]);
  expect(all(m.root, "input").some((i) => m.attr(i, "type") === "password")).toBe(false);
  expect(all(m.root, "input").map((i) => m.attr(i, "type"))).toEqual(["text", "text"]);
  await m.input("Address", "http://127.0.0.1:8317");
  await m.submit();
  expect(m.commands("key_entry")[1]).toEqual({
    change: { mode: "local", url: "http://127.0.0.1:8317", ca_file: undefined },
  });
});

test("a refused key or an unreachable address says why above the buttons and keeps what was typed", async () => {
  let result: KeyEntryResult = { status: "wrong_key", connection: view() };
  const { m, changed } = await connectStep({ key_entry: () => result });
  await m.input("Address", "https://pluxy.example");
  await m.input("Certificate file", "/etc/pluxy/ca.pem");
  await m.submit();
  expect(m.text()).toContain("That pluxy refused the key. Check it and try again.");
  result = { status: "error", connection: view({ error: "the connection to core timed out" }) };
  await m.submit();
  expect(m.text()).toContain("It did not answer in time.");
  expect(m.text()).not.toContain("That pluxy refused the key");
  result = {
    status: "error",
    connection: view({
      error: "certificate authority /etc/pluxy/ca.pem: No such file or directory (os error 2)",
    }),
  };
  await m.submit();
  expect(m.text()).toContain("The certificate file /etc/pluxy/ca.pem could not be read.");
  // A key window that was closed says nothing, and nothing the person typed is gone.
  result = { status: "cancelled", connection: view() };
  await m.submit();
  expect(m.text()).not.toContain("could not be read");
  expect(all(m.root, "input").map((i) => i.value)).toEqual(["https://pluxy.example", "/etc/pluxy/ca.pem"]);
  expect(changed).toEqual([]);
});

test("an accepted key hands the new connection over; while the key window is open the button says so", async () => {
  let answer: (r: KeyEntryResult) => void = noop;
  const { m, changed } = await connectStep({
    key_entry: () => new Promise<KeyEntryResult>((resolve) => (answer = resolve)),
  });
  await m.input("Address", "https://pluxy.example");
  await m.submit();
  expect(m.labels()).toContain("Waiting for the key…");
  expect((m.button("Waiting for the key…") as FakeNode).disabled).toBe(true);
  const connection = view({ mode: "remote", target: "https://pluxy.example", signed_in: true });
  await act(async () => answer({ status: "signed_in", connection }));
  await settle();
  expect(changed).toEqual([connection]);
});

// ---- not running ------------------------------------------------------------------------------------------

const MISSING = "No core is installed yet: install one from the core source, or adopt a core program.";
const managed = (core: NonNullable<Connection["core"]>, over: Partial<Connection> = {}) =>
  view({ mode: "managed", core, ...over });

interface Case {
  name: string;
  connection: Connection;
  heading: string;
  buttons: string[];
  /** Lines the pane says. */
  says?: string[];
  details?: string;
  /** What must not be on the pane. */
  not?: string[];
}

const cases: Case[] = [
  {
    name: "stopped from the tray",
    connection: managed({ state: "stopped", version: "0.1.0", stop_reason: "stopped_by_user" }),
    heading: "pluxy is stopped",
    buttons: ["Start pluxy", "Change where pluxy runs"],
    says: ["On this computer, version 0.1.0.", "It was stopped from the tray."],
  },
  {
    name: "set not to start with the app",
    connection: managed({ state: "stopped", version: "0.1.0", stop_reason: "not_started" }),
    heading: "pluxy is stopped",
    buttons: ["Start pluxy", "Change where pluxy runs"],
    says: ["The app is set not to start it when it opens."],
    not: ["stopped from the tray"],
  },
  {
    name: "stopped with no reason known",
    connection: managed({ state: "stopped" }),
    heading: "pluxy is stopped",
    buttons: ["Start pluxy", "Change where pluxy runs"],
    not: ["stopped from the tray", "set not to start", "version"],
  },
  {
    name: "stopped with an error",
    connection: managed({ state: "stopped", version: "0.1.0", detail: "exited with status 1: port in use" }),
    heading: "pluxy stopped with an error",
    buttons: ["Start pluxy", "Change where pluxy runs"],
    details: "exited with status 1: port in use",
  },
  {
    name: "restarting after an error",
    connection: managed({ state: "recovering", version: "0.1.0", detail: "exited with status 2" }),
    heading: "pluxy stopped with an error. The app is starting it again.",
    buttons: ["Start now", "Change where pluxy runs"],
    details: "exited with status 2",
  },
  {
    name: "stopped restarting",
    connection: managed({ state: "paused", version: "0.1.0", detail: "exited with status 2" }),
    heading: "pluxy kept stopping, so the app stopped restarting it",
    buttons: ["Start pluxy", "Change where pluxy runs"],
    details: "exited with status 2",
  },
  {
    name: "not installed, with a program to adopt",
    connection: managed({ state: "missing", adopt: "/opt/pluxy/pluxy", detail: MISSING }),
    heading: "No pluxy is installed",
    buttons: ["Use this program", "Change where pluxy runs"],
    says: ["There is a pluxy program at /opt/pluxy/pluxy. The app can copy it in and run it."],
    not: ["No core is installed yet"],
  },
  {
    name: "not installed, nothing to adopt",
    connection: managed({ state: "missing", detail: MISSING }),
    heading: "No pluxy is installed",
    buttons: ["Connect to a pluxy that is already running"],
    says: ["This copy of the app has no pluxy to start."],
    not: ["No core is installed yet", "Details"],
  },
  {
    name: "not installed, for another reason",
    connection: managed({ state: "missing", detail: "the core program is damaged" }),
    heading: "No pluxy is installed",
    buttons: ["Connect to a pluxy that is already running"],
    details: "the core program is damaged",
  },
  {
    name: "remote, not signed in",
    connection: view({ mode: "remote", url: "https://pluxy.example", target: "https://pluxy.example" }),
    heading: "Not signed in to pluxy at pluxy.example",
    buttons: ["Enter management key", "Change where pluxy runs"],
    says: ["https://pluxy.example", "The app needs its management key."],
  },
  {
    name: "this computer, not signed in",
    connection: view({ mode: "local", url: "http://127.0.0.1:8317", target: "http://127.0.0.1:8317" }),
    heading: "Not signed in to pluxy on this computer",
    buttons: ["Enter management key", "Change where pluxy runs"],
  },
  {
    name: "its key was refused",
    connection: view({
      mode: "remote",
      url: "https://pluxy.example",
      target: "https://pluxy.example",
      error: "core refused this key",
    }),
    heading: "Not signed in to pluxy at pluxy.example",
    buttons: ["Enter management key", "Change where pluxy runs"],
    says: ["That pluxy refused the key. Check it and try again."],
    not: ["core refused this key"],
  },
  {
    name: "remote, not answering",
    connection: view({
      mode: "remote",
      url: "https://pluxy.example",
      target: "https://pluxy.example",
      error: "the connection to core timed out",
    }),
    heading: "pluxy at pluxy.example is not answering",
    buttons: ["Enter management key", "Change where pluxy runs"],
    says: ["https://pluxy.example", "It did not answer in time."],
    not: ["the connection to core timed out"],
  },
  {
    name: "this computer, not answering",
    connection: view({
      mode: "local",
      url: "http://127.0.0.1:8317",
      target: "http://127.0.0.1:8317",
      error: "core could not be reached or its reply could not be read",
    }),
    heading: "pluxy on this computer is not answering",
    buttons: ["Enter management key", "Change where pluxy runs"],
    says: ["It could not be reached."],
  },
  {
    name: "cannot connect",
    connection: view({
      mode: "remote",
      url: "https://pluxy.example",
      error: "core's host name could not be resolved",
    }),
    heading: "Cannot connect to pluxy at pluxy.example",
    buttons: ["Change where pluxy runs"],
    says: ["https://pluxy.example", "Its name could not be found."],
  },
  {
    name: "cannot connect, on this computer, with a text the app does not know",
    connection: view({ mode: "local", url: "http://127.0.0.1:8317", error: "something new went wrong" }),
    heading: "Cannot connect to pluxy on this computer",
    buttons: ["Change where pluxy runs"],
    details: "something new went wrong",
  },
];

for (const c of cases) {
  test(`not running, ${c.name}: its heading, its one main button, and never the developer text`, async () => {
    const m = await mount(<NotRunningPane connection={c.connection} onChange={noop} />);
    const heading = all(m.root, "h2")[0]?.textContent;
    expect(heading).toBe(c.heading);
    expect(m.labels().filter((l) => l !== "Details")).toEqual(c.buttons);
    // The first button is the filled one; a second is the quiet one; a link to Where pluxy runs is a link.
    expect(m.attr(m.button(c.buttons[0] as string), "data-variant")).toBe("primary");
    if (c.buttons[1]) expect(m.attr(m.button(c.buttons[1]), "data-variant")).toBe("quiet");
    for (const line of c.says ?? []) expect(m.text()).toContain(line);
    const details = all(m.root, "pre")[0]?.textContent;
    expect(details).toBe(c.details);
    expect(m.text()).not.toContain("No core is installed yet");
    expect(m.text()).not.toMatch(/\bcore\b/i.test(c.details ?? "") ? /^$/ : /\bcore\b/i);
    for (const word of c.not ?? []) expect(m.text()).not.toContain(word);
    expect(paneOf(c.connection)?.heading).toBe(c.heading);
  });
}

test("a pane has nothing to say for a connection that works, or one that is not chosen yet", () => {
  expect(paneOf(view())).toBeUndefined();
  expect(paneOf(view({ mode: "remote", target: "https://pluxy.example", signed_in: true }))).toBeUndefined();
  expect(
    paneOf(
      managed({ state: "running", version: "0.1.0" }, { target: "http://127.0.0.1:1", signed_in: true }),
    ),
  ).toBeUndefined();
});

test("the pane's buttons send what they say, show their failure in the pane and wait while they work", async () => {
  let answer: (c: Connection) => void = noop;
  const changed: Connection[] = [];
  const stopped = managed({ state: "stopped", version: "0.1.0" });
  const m = await mount(<NotRunningPane connection={stopped} onChange={(c) => changed.push(c)} />, {
    core_start: () => new Promise<Connection>((resolve) => (answer = resolve)),
  });
  await m.click("Start pluxy");
  expect(m.commands("core_start")).toEqual([{}]);
  expect(m.labels()).toContain("Starting…");
  expect((m.button("Starting…") as FakeNode).disabled).toBe(true);
  const failed = managed(
    { state: "stopped", version: "0.1.0", detail: "exited with status 1" },
    { error: "exited with status 1" },
  );
  await act(async () => answer(failed));
  await settle();
  expect(changed).toEqual([failed]);
  expect(m.labels()).toContain("Start pluxy");
  expect(m.text()).toContain("exited with status 1");
});

test("Enter management key opens the native key window and sends no change; a refused key says so in the pane", async () => {
  const signedOut = view({ mode: "remote", url: "https://pluxy.example", target: "https://pluxy.example" });
  const m = await mount(<NotRunningPane connection={signedOut} onChange={noop} />, {
    key_entry: (): KeyEntryResult => ({ status: "wrong_key", connection: signedOut }),
  });
  await m.click("Enter management key");
  expect(m.commands("key_entry")).toEqual([{ change: null }]);
  expect(m.text()).toContain("That pluxy refused the key. Check it and try again.");
});

test("Use this program asks the native side, then waits for its confirmation window to end", async () => {
  const missing = managed({ state: "missing", adopt: "/opt/pluxy/pluxy", detail: MISSING });
  const m = await mount(<NotRunningPane connection={missing} onChange={noop} />, { core_adopt: null });
  await m.click("Use this program");
  expect(m.commands("core_adopt")).toEqual([{}]);
  expect(m.labels()).toContain("Answer the confirmation window…");
  expect((m.button("Answer the confirmation window…") as FakeNode).disabled).toBe(true);
});

test("Change where pluxy runs is a link to Where pluxy runs", async () => {
  const m = await mount(<NotRunningPane connection={managed({ state: "stopped" })} onChange={noop} />);
  const link = all(m.root, "a").find((a) => a.textContent === "Change where pluxy runs");
  expect(link && m.attr(link, "href")).toBe("#/connection");
});

test("in safe mode the pane links to Add-ons; otherwise it does not offer them", async () => {
  const stopped = managed({ state: "stopped" });
  const safe = await mount(<NotRunningPane connection={stopped} onChange={noop} safeMode />);
  expect(safe.text()).toContain("Safe mode is on: add-ons are paused.");
  const link = all(safe.root, "a").find((a) => a.textContent === "Open Add-ons");
  expect(link && safe.attr(link, "href")).toBe("#/device");
  const plain = await mount(<NotRunningPane connection={stopped} onChange={noop} />);
  expect(plain.text()).not.toContain("Add-ons");
});

// ---- Where pluxy runs -------------------------------------------------------------------------------------

const startup = (over: Partial<StartupView> = {}): StartupView => ({
  autostart_enabled: false,
  silent_start_enabled: false,
  start_core_on_launch: true,
  registration_owned: false,
  registration_conflict: false,
  sign_in: true,
  ...over,
});
const windowPrefs = (over: Partial<WindowPreferences> = {}): WindowPreferences => ({
  close_behavior: "minimize-to-tray",
  tray: true,
  ask_without_tray: true,
  ...over,
});
const versions = (over: Partial<Versions> = {}): Versions => ({
  available: [],
  installed: [{ id: "0.1.0", kind: "bundled", version: "0.1.0" }],
  ...over,
});
const running = managed(
  { state: "running", version: "0.1.0", pid: 4242 },
  { target: "http://127.0.0.1:41000", signed_in: true },
);
const remote = view({
  mode: "remote",
  url: "https://pluxy.example",
  target: "https://pluxy.example",
  signed_in: true,
  remembered: true,
});
const page = (c: Connection) => <ConnectionPage connection={c} onChange={noop} />;
const settings = (over: Handlers = {}): Handlers => ({
  startup_get: startup(),
  window_get: windowPrefs(),
  core_versions: versions(),
  ...over,
});

/** One row of the settings, by its name. */
const row = (m: Awaited<ReturnType<typeof mount>>, name: string) =>
  need(
    rows(m).find((r) => r.name === name),
    `no row ${name}`,
  );

/** The two radios of the Where section. */
function whereRadios(m: Awaited<ReturnType<typeof mount>>) {
  return all(m.root, "label")
    .filter((l) => m.attr(l, "class") === "where-choice-head")
    .map((l) => all(l, "input")[0] as FakeNode);
}

const need = <T,>(value: T | undefined | null, what: string): T => {
  if (value === undefined || value === null) throw new Error(what);
  return value;
};

function rows(m: Awaited<ReturnType<typeof mount>>) {
  return all(m.root, "div")
    .filter((d) => m.attr(d, "data-part") === "setting-row")
    .map((d) => ({
      name: all(d, "span").find((s) => m.attr(s, "class") === "setting-name")?.textContent as string,
      node: d,
    }));
}

test("Where pluxy runs for the app's own pluxy: its state, who runs it, one action, and the settings that apply", async () => {
  const m = await mount(page(running), settings());
  expect(m.text()).toContain("Running on this computer");
  expect(m.text()).toContain("http://127.0.0.1:41000");
  expect(m.text()).toContain("0.1.0");
  expect(m.text()).toContain("This app runs pluxy and stops it when you quit the app.");
  expect(m.labels()).toContain("Restart pluxy");
  expect(rows(m).map((r) => r.name)).toEqual([
    "Open at sign-in",
    "Start in the tray",
    "Start pluxy with the app",
    "When you close the window",
  ]);
  for (const word of ["Core", "Silent launch", "Launch at startup", "Minimize to tray", "Exit app"])
    expect(m.text()).not.toContain(word);
  await m.click("Restart pluxy");
  expect(m.commands("core_restart")).toEqual([{}]);
});

test("for a pluxy the app only connects to, the rows that do nothing are not drawn, and its action is Sign out", async () => {
  const m = await mount(page(remote), settings());
  expect(rows(m).map((r) => r.name)).toEqual(["Open at sign-in", "When you close the window"]);
  expect(m.text()).toContain("Connected to pluxy.example");
  expect(m.text()).toContain("Over https.");
  expect(m.text()).toContain(
    "pluxy keeps running either way; this only decides whether the app stays in the tray.",
  );
  expect(m.labels()).not.toContain("Restart pluxy");
  const local = await mount(
    page(
      view({ mode: "local", url: "http://127.0.0.1:8317", target: "http://127.0.0.1:8317", signed_in: true }),
    ),
    settings(),
  );
  expect(local.text()).toContain("Connected to pluxy on this computer");
  expect(local.text()).toContain("It runs on its own; the app only connects to it.");
});

test("a failed action's message sits beside it and the page keeps its fields", async () => {
  const stopped = managed({ state: "stopped", version: "0.1.0" });
  const m = await mount(
    page(stopped),
    settings({ core_start: () => Promise.reject("could not start: no space left") }),
  );
  await m.click("Start pluxy");
  expect(m.text()).toContain("could not start: no space left");
  expect(m.labels()).toContain("Start pluxy");
});

test("Where: picking the other choice shows its controls only, and switching says what it stops", async () => {
  const m = await mount(page(remote), settings({ connection_set: () => remote }));
  // The choice in use shows its controls: the form, with the address as it is now.
  expect(m.labels()).not.toContain("Switch to this computer");
  expect(m.labels()).toContain("Connect");
  expect(all(m.root, "input").map((i) => i.value)).toContain("https://pluxy.example");
  const radios = whereRadios(m);
  expect(radios.length).toBe(2);
  need(radios[0], "no radio").checked = true;
  await m.fire(radios[0] as FakeNode, "change");
  expect(m.text()).toContain(
    "Switching stops using pluxy.example. Tools set up for it stop getting answers until you switch back.",
  );
  expect(m.labels()).toContain("Switch to this computer");
  expect(m.labels()).not.toContain("Connect");
  await m.click("Switch to this computer");
  expect(m.commands("connection_set")).toEqual([{ change: { mode: "managed" } }]);
});

test("switching away from the app's own pluxy says it stops, and the form there is the first launch form", async () => {
  const m = await mount(page(running), settings());
  const radios = whereRadios(m);
  need(radios[1], "no radio").checked = true;
  await m.fire(radios[1] as FakeNode, "change");
  expect(m.text()).toContain("Switching stops the pluxy this app runs.");
  expect(m.labels()).toContain("Connect");
  expect(m.text()).toContain("Next, a small window asks for its management key.");
});

test("Where pluxy runs keeps showing the native side's own message when signed in with an error", async () => {
  const m = await mount(page({ ...running, error: "the key could not be kept on this device" }), settings());
  expect(m.text()).toContain("the key could not be kept on this device");
});

// ---- This app ---------------------------------------------------------------------------------------------

test("each row is disabled until the native side has answered, and none shows a value nobody read", async () => {
  const m = await mount(page(running), {
    startup_get: never,
    window_get: never,
    core_versions: versions(),
  });
  const shown = rows(m);
  expect(shown.map((r) => r.name)).toEqual([
    "Open at sign-in",
    "Start in the tray",
    "Start pluxy with the app",
    "When you close the window",
  ]);
  for (const r of shown) {
    expect(all(r.node, "input")).toEqual([]);
    expect(r.node.textContent).toContain("Reading…");
  }
});

test("a setting that cannot be read says so on its row, with a way to try again", async () => {
  const m = await mount(page(running), settings({ startup_get: () => Promise.reject("no such file") }));
  expect(m.text()).toContain("The app could not read this setting. no such file");
  expect(m.labels()).toContain("Try again");
});

test("Start in the tray is off until Open at sign-in is on, and the row says why", async () => {
  const m = await mount(page(running), settings());
  const tray = row(m, "Start in the tray");
  expect(tray.node.textContent).toContain("Turn on Open at sign-in first.");
  expect(tray.node.textContent).toContain("At sign-in, starts in the tray instead of opening this window.");
  expect(all(tray.node, "fieldset").some((f) => f.disabled)).toBe(true);
  const on = await mount(page(running), settings({ startup_get: startup({ autostart_enabled: true }) }));
  const enabled = row(on, "Start in the tray");
  expect(enabled.node.textContent).not.toContain("Turn on Open at sign-in first.");
  expect(all(enabled.node, "fieldset").some((f) => f.disabled)).toBe(false);
});

test("with no tray, Start in the tray and the close choice say why they are off", async () => {
  const m = await mount(
    page(running),
    settings({ startup_get: startup({ autostart_enabled: true }), window_get: windowPrefs({ tray: false }) }),
  );
  const tray = row(m, "Start in the tray");
  expect(tray.node.textContent).toContain("This desktop has no tray.");
  const close = row(m, "When you close the window");
  expect(close.node.textContent).toContain(
    "This desktop has no tray, so closing the window quits the app. It asks first, because that stops pluxy.",
  );
  expect(all(close.node, "fieldset").some((f) => f.disabled)).toBe(true);
  const other = await mount(page(remote), settings({ window_get: windowPrefs({ tray: false }) }));
  const closeOther = row(other, "When you close the window");
  expect(closeOther.node.textContent).toContain(
    "This desktop has no tray, so closing the window quits the app.",
  );
  expect(closeOther.node.textContent).not.toContain("It asks first");
});

test("a setting saves when it is changed, with no Save button, and turning Open at sign-in off turns the tray choice off", async () => {
  let saved = startup({ autostart_enabled: true, silent_start_enabled: true });
  const m = await mount(
    page(running),
    settings({
      startup_get: () => saved,
      startup_set: (args: Record<string, unknown> | undefined) => {
        saved = { ...saved, ...(args?.preferences as object) };
        return saved;
      },
    }),
  );
  for (const r of rows(m)) expect(all(r.node, "button").map((b) => b.textContent)).not.toContain("Save");
  const signIn = row(m, "Open at sign-in");
  const box = all(signIn.node, "input")[0] as FakeNode;
  box.checked = false;
  await m.fire(box, "change");
  expect(m.commands("startup_set")).toEqual([
    {
      preferences: { autostart_enabled: false, silent_start_enabled: false, start_core_on_launch: true },
    },
  ]);
});

test("a failed startup save puts the control back and shows the reason under the row", async () => {
  const m = await mount(
    page(running),
    settings({ startup_set: () => Promise.reject("the sign-in entry could not be written") }),
  );
  const withApp = row(m, "Start pluxy with the app");
  const box = all(withApp.node, "input")[0] as FakeNode;
  expect(box.checked).toBe(true);
  box.checked = false;
  await m.fire(box, "change");
  const after = row(m, "Start pluxy with the app");
  expect(after.node.textContent).toContain("the sign-in entry could not be written");
  expect((all(after.node, "input")[0] as FakeNode).checked).toBe(true);
  expect(after.node.textContent).not.toContain("Saving…");
});

test("the close choice saves on change, and a failed save goes back with the reason", async () => {
  let fail = false;
  const m = await mount(
    page(running),
    settings({
      window_set: (args: Record<string, unknown> | undefined) => {
        if (fail) return Promise.reject("window settings could not be written");
        return windowPrefs({ close_behavior: args?.closeBehavior as WindowPreferences["close_behavior"] });
      },
    }),
  );
  const choose = async (value: string) => {
    const close = row(m, "When you close the window");
    const radio = all(close.node, "input").find((i) => i.value === value) as FakeNode;
    radio.checked = true;
    await m.fire(radio, "change");
  };
  const checked = () => all(row(m, "When you close the window").node, "input").find((i) => i.checked)?.value;
  expect(checked()).toBe("minimize-to-tray");
  await choose("exit");
  expect(m.commands("window_set")).toEqual([{ closeBehavior: "exit" }]);
  expect(checked()).toBe("exit");
  fail = true;
  await choose("ask");
  expect(checked()).toBe("exit");
  expect(m.text()).toContain("window settings could not be written");
  expect(all(row(m, "When you close the window").node, "label").map((l) => l.textContent)).toEqual(
    expect.arrayContaining(["Keep in the tray", "Quit", "Ask"]),
  );
});

test("a local build offers no Open at sign-in: one line says it opens with its start, and Start in the tray is not drawn", async () => {
  const m = await mount(page(running), settings({ startup_get: startup({ sign_in: false }) }));
  expect(rows(m).map((r) => r.name)).toEqual([
    "Open at sign-in",
    "Start pluxy with the app",
    "When you close the window",
  ]);
  const signIn = row(m, "Open at sign-in");
  expect(signIn.node.textContent).toContain(
    "This local build opens only with its start command, not at sign-in.",
  );
  expect(signIn.node.textContent).not.toContain("Opens this app when you sign in to the computer.");
  expect(all(signIn.node, "input")).toEqual([]);
  expect(all(signIn.node, "button")).toEqual([]);
});

test("another program owning the sign-in entry shows as a warning under Open at sign-in", async () => {
  const m = await mount(page(running), settings({ startup_get: startup({ registration_conflict: true }) }));
  expect(m.text()).toContain("Another program owns the sign-in entry, so the app leaves it alone.");
});

// ---- Versions and advanced ---------------------------------------------------------------------------------

test("the versions list puts the program to adopt first, and a change disables every button until its outcome arrives", async () => {
  let current = versions({
    available: [{ version: "0.2.0" }, { version: "0.0.9" }],
    installed: [
      { id: "0.1.0", kind: "bundled", version: "0.1.0" },
      { id: "0.0.9", kind: "signed", version: "0.0.9" },
    ],
    previous: "0.0.9",
    source: "/srv/releases.json",
    outcome: { seq: 1, action: "install", version: "0.0.9", result: "done" },
  });
  const m = await mount(
    page({ ...running, core: { ...need(running.core, "no core"), adopt: "/opt/pluxy/pluxy" } }),
    settings({ core_versions: () => current, core_rollback: null }),
  );
  expect(m.text()).toContain("Versions of pluxy on this computer");
  expect(m.text()).toContain("Came with the app");
  expect(m.text()).toContain("Running");
  expect(m.text()).toContain("Previous");
  expect(m.text()).toContain("Process 4242");
  expect(m.text()).toContain("Done");
  expect(m.labels().filter((l) => /version|program|Install/.test(l))).toEqual([
    "Use this program",
    "Switch to this version",
    "Install again",
    "Install 0.2.0",
  ]);
  await m.click("Switch to this version");
  expect(m.commands("core_rollback")).toEqual([{ version: "0.0.9" }]);
  expect(m.text()).toContain("Answer the confirmation window pluxy opened.");
  for (const l of ["Use this program", "Switch to this version", "Install again", "Install 0.2.0"])
    expect((m.button(l) as FakeNode).disabled).toBe(true);
  expect(m.text()).not.toContain("Done");
  // The confirmation ends (the native side tells the window, which reads the versions again): the
  // buttons work again, and the outcome is told.
  current = { ...current, outcome: { seq: 2, action: "rollback", version: "0.0.9", result: "cancelled" } };
  await m.show(
    page({
      ...running,
      core: { ...need(running.core, "no core"), adopt: "/opt/pluxy/pluxy", detail: undefined },
    }),
  );
  expect(m.text()).not.toContain("Answer the confirmation window pluxy opened.");
  expect(m.text()).toContain("Cancelled: nothing changed");
  for (const l of ["Use this program", "Switch to this version", "Install again", "Install 0.2.0"])
    expect((m.button(l) as FakeNode).disabled).toBe(false);
});

test("Where pluxy runs for a pluxy the app connects to says how its key is kept and offers to enter it again", async () => {
  const m = await mount(page({ ...remote, ca_file: "/etc/pluxy/ca.pem" }), settings());
  expect(m.text()).toContain("Kept on this computer, so the app signs in by itself.");
  expect(m.text()).toContain("/etc/pluxy/ca.pem");
  expect(m.labels()).toContain("Enter it again");
  const session = await mount(page({ ...remote, remembered: false }), settings());
  expect(session.text()).toContain("Kept until the app quits.");
});

test("no setting row or button says core, the old names, or Save", async () => {
  const m = await mount(page(running), settings());
  for (const old of [
    "Run core here",
    "Core source",
    "Core program",
    "Roll back to",
    "Restart core",
    "Start core",
  ])
    expect(m.text()).not.toContain(old);
  expect(m.text()).toContain("Release list");
  expect(m.text()).toContain("Your own pluxy program");
});
