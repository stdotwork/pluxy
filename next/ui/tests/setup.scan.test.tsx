// The Setup page while core scans: its re-reads, leaving, looking again, and a failed re-read.
// These drive the real page through Preact's DOM renderer against a fake host, using a small DOM
// that carries only the nodes and events Preact needs.
import { afterEach, expect, jest, test } from "bun:test";
import { render } from "preact";
import { act } from "preact/test-utils";
import { api, type Setup } from "../kit/api";
import type { Feature, Host, Method, Reply } from "../kit/host";
import { SetupPage } from "../kit/pages/setup";
import { type Kit, KitContext } from "../kit/ui";

interface Call {
  method: Method;
  path: string;
  body?: unknown;
  at: number;
}

const core = { listen: "127.0.0.1:8317", reachable_from_network: false };
const pick = (id: string, title: string, more: Partial<Setup["picks"][number]> = {}) => ({
  id,
  kind: "provider",
  title,
  summary: `${title} subscriptions and API keys`,
  installed: false,
  suggested: false,
  ...more,
});

function setup(scanning: boolean, scanned_at?: string, client = false): Setup {
  return {
    first_run: true,
    scanning,
    scanned_at,
    found: {
      clients: client
        ? [
            {
              id: "claude-code",
              name: "Claude Code",
              installed: true,
              version: "2.4.1",
              config: "~/.claude/settings.json",
              state: "not-configured",
            },
          ]
        : [],
      sign_ins: [],
      imports: [],
      core,
    },
    picks: [
      pick("pluxy/codex", "Codex", { suggested: true }),
      pick("pluxy/claude", "Claude", { suggested: true }),
    ],
  };
}

class FakeNode {
  nodeType = 1;
  nodeName: string;
  localName: string;
  namespaceURI: string;
  ownerDocument: FakeDocument;
  parentNode: FakeNode | null = null;
  childNodes: FakeNode[] = [];
  attributes: { name: string; value: string }[] = [];
  style: Record<string, string> = {};
  checked = false;
  disabled = false;
  value = "";
  private listeners = new Map<string, ((event: Event) => void)[]>();
  private ownText = "";

  constructor(document: FakeDocument, tag: string, namespaceURI = "http://www.w3.org/1999/xhtml") {
    this.ownerDocument = document;
    this.nodeName = tag.toUpperCase();
    this.localName = tag;
    this.namespaceURI = namespaceURI;
  }

  get firstChild() {
    return this.childNodes[0] ?? null;
  }

  get nextSibling() {
    if (!this.parentNode) return null;
    const at = this.parentNode.childNodes.indexOf(this);
    return this.parentNode.childNodes[at + 1] ?? null;
  }

  get textContent(): string {
    return this.ownText + this.childNodes.map((child) => child.textContent).join("");
  }

  set textContent(value: string) {
    this.ownText = value;
    this.childNodes = [];
  }

  set innerHTML(value: string) {
    this.textContent = value;
  }

  appendChild(node: FakeNode) {
    return this.insertBefore(node, null);
  }

  insertBefore(node: FakeNode, before: FakeNode | null) {
    if (node.parentNode) node.parentNode.removeChild(node);
    const at = before ? this.childNodes.indexOf(before) : -1;
    if (at === -1) this.childNodes.push(node);
    else this.childNodes.splice(at, 0, node);
    node.parentNode = this;
    return node;
  }

  removeChild(node: FakeNode) {
    const at = this.childNodes.indexOf(node);
    if (at === -1) throw new Error("node is not a child");
    this.childNodes.splice(at, 1);
    node.parentNode = null;
    return node;
  }

  remove() {
    this.parentNode?.removeChild(this);
  }

  setAttribute(name: string, value: string) {
    const found = this.attributes.find((attribute) => attribute.name === name);
    if (found) found.value = String(value);
    else this.attributes.push({ name, value: String(value) });
  }

  removeAttribute(name: string) {
    this.attributes = this.attributes.filter((attribute) => attribute.name !== name);
  }

  addEventListener(name: string, listener: (event: Event) => void) {
    const listeners = this.listeners.get(name) ?? [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  removeEventListener(name: string, listener: (event: Event) => void) {
    this.listeners.set(
      name,
      (this.listeners.get(name) ?? []).filter((current) => current !== listener),
    );
  }

  dispatchEvent(event: Event) {
    Object.defineProperty(event, "target", { configurable: true, value: this });
    Object.defineProperty(event, "currentTarget", { configurable: true, value: this });
    for (const listener of this.listeners.get(event.type) ?? []) listener.call(this, event);
    return true;
  }
}

class FakeText extends FakeNode {
  private data: string;

  constructor(document: FakeDocument, data: string) {
    super(document, "#text", "http://www.w3.org/1999/xhtml");
    this.nodeType = 3;
    this.nodeName = "#text";
    this.data = String(data);
  }

  get textContent() {
    return this.data;
  }

  set textContent(value: string) {
    this.data = value;
  }
}

class FakeDocument {
  nodeType = 9;
  visibilityState: DocumentVisibilityState = "visible";
  body = new FakeNode(this, "body");

  createElement(tag: string) {
    return new FakeNode(this, tag);
  }

  createElementNS(namespaceURI: string, tag: string) {
    return new FakeNode(this, tag, namespaceURI);
  }

  createTextNode(data: string) {
    return new FakeText(this, data);
  }

  addEventListener() {}

  removeEventListener() {}
}

const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
afterEach(() => {
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
  if (originalDocument) Object.defineProperty(globalThis, "document", originalDocument);
  else Reflect.deleteProperty(globalThis, "document");
});

function installDocument() {
  const document = new FakeDocument();
  Object.defineProperty(globalThis, "document", { configurable: true, value: document });
  return document;
}

function fakeHost(answer: (call: Call) => Reply<unknown>) {
  const calls: Call[] = [];
  const host: Host = {
    call: async <T,>(method: Method, path: string, body?: unknown) => {
      calls.push({ method, path, body, at: jest.now() });
      return answer({ method, path, body }) as Reply<T>;
    },
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {},
    prefs: { get: () => null, set: () => {} },
    features: new Set<Feature>(),
    address: "http://127.0.0.1:8317",
  };
  return { host, calls, api: api(host) };
}

const quiet = (call: Call): Reply<unknown> => {
  if (call.method === "GET" && call.path === "/accounts") return { status: 200, body: { accounts: [] } };
  if (call.method === "GET" && call.path === "/packages") return { status: 200, body: { packages: [] } };
  if (call.method === "POST" && call.path === "/setup/scan") {
    return { status: 200, body: setup(false, "2026-11-20T00:00:00Z") };
  }
  return { status: 200, body: {} };
};

/** Let the host promises settle and Preact run the renders they cause. */
async function settle() {
  await act(async () => {
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

async function mount(answer: (call: Call) => Reply<unknown>) {
  const document = installDocument();
  const fake = fakeHost(answer);
  const kit = {
    host: fake.host,
    api: fake.api,
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as Kit;
  const root = document.createElement("main");
  document.body.appendChild(root);
  await act(async () => {
    render(
      <KitContext.Provider value={kit}>
        <SetupPage />
      </KitContext.Provider>,
      root,
    );
  });
  await settle();
  return {
    ...fake,
    root,
    text: () => root.textContent,
    unmount: async () => {
      await act(async () => render(null, root));
    },
  };
}

function all(root: FakeNode, tag: string): FakeNode[] {
  return [
    ...(root.localName === tag ? [root] : []),
    ...root.childNodes.flatMap((child) => all(child, tag)),
  ];
}

function button(root: FakeNode, label: string) {
  const found = all(root, "button").find((node) => node.textContent === label);
  if (!found) throw new Error(`no button labelled ${label}`);
  return found;
}

function at<T>(items: T[], index: number): T {
  const item = items[index];
  if (item === undefined) throw new Error(`no item at ${index}`);
  return item;
}

function nextSetup(scans: Setup[]): Setup {
  const body = scans.shift();
  if (!body) throw new Error("no setup reply left for this request");
  return body;
}

async function tick(milliseconds: number) {
  await act(async () => {
    jest.advanceTimersByTime(milliseconds);
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

async function tickUntil(check: () => boolean, limit = 5000) {
  for (let elapsed = 0; elapsed < limit; elapsed += 100) {
    if (check()) return elapsed;
    await tick(100);
  }
  if (check()) return limit;
  throw new Error(`condition did not become true within ${limit} ms`);
}

test("while scanning, the page reads about once a second, stops, and shows what arrived", async () => {
  jest.useFakeTimers();
  const scans = [setup(true), setup(true), setup(false, "2026-11-20T00:00:00Z", true)];
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: nextSetup(scans) };
    return quiet(call);
  });
  const setupReads = () => fake.calls.filter((call) => call.method === "GET" && call.path === "/setup");

  expect(setupReads()).toHaveLength(1);
  expect(jest.getTimerCount()).toBe(1);
  expect(fake.text()).toContain("pluxy is looking at this machine.");
  expect(fake.text()).not.toContain("Claude Code 2.4.1");

  await tick(999);
  expect(setupReads()).toHaveLength(1);
  await tickUntil(() => setupReads().length === 2);
  expect(at(setupReads(), 1).at - at(setupReads(), 0).at).toBeGreaterThanOrEqual(999);
  expect(at(setupReads(), 1).at - at(setupReads(), 0).at).toBeLessThan(1300);
  expect(fake.text()).toContain("pluxy is looking at this machine.");

  await tickUntil(() => setupReads().length === 3);
  expect(at(setupReads(), 2).at - at(setupReads(), 1).at).toBeGreaterThanOrEqual(999);
  expect(at(setupReads(), 2).at - at(setupReads(), 1).at).toBeLessThan(1300);
  expect(fake.text()).toContain("Claude Code 2.4.1");
  expect(fake.text()).not.toContain("pluxy is looking at this machine.");
  expect(jest.getTimerCount()).toBe(0);

  await tick(5000);
  expect(setupReads()).toHaveLength(3);
});

test("leaving while core scans stops the re-reads", async () => {
  jest.useFakeTimers();
  const scans = [setup(true), setup(true)];
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") return { status: 200, body: nextSetup(scans) };
    return quiet(call);
  });
  const setupCalls = () =>
    fake.calls.filter((call) => call.method === "GET" && call.path === "/setup").length;

  await tickUntil(() => setupCalls() === 2);
  expect(setupCalls()).toBe(2);
  expect(jest.getTimerCount()).toBe(1);
  await fake.unmount();
  expect(jest.getTimerCount()).toBe(0);
  await tick(5000);
  expect(setupCalls()).toBe(2);
});

test("Look again asks core to scan once and then reads its answer again", async () => {
  jest.useFakeTimers();
  const fake = await mount((call) => {
    if (call.method === "GET" && call.path === "/setup") {
      return { status: 200, body: setup(false, "2026-11-20T00:00:00Z") };
    }
    return quiet(call);
  });
  const setupCalls = () =>
    fake.calls.filter((call) => call.method === "GET" && call.path === "/setup").length;
  const scans = () =>
    fake.calls.filter((call) => call.method === "POST" && call.path === "/setup/scan").length;
  expect(setupCalls()).toBe(1);

  button(fake.root, "Look again").dispatchEvent(new Event("click"));
  await settle();
  expect(scans()).toBe(1);
  expect(setupCalls()).toBe(2);

  await tick(5000);
  expect(scans()).toBe(1);
  expect(setupCalls()).toBe(2);
});

for (const failure of [
  { status: 0, shown: "core could not be reached" },
  { status: 500, shown: "core answered 500" },
] as const) {
  test(`a ${failure.status} re-read keeps the page and shows ${failure.shown}`, async () => {
    jest.useFakeTimers();
    let setupReads = 0;
    const fake = await mount((call) => {
      if (call.method === "GET" && call.path === "/setup") {
        setupReads++;
        return setupReads === 1
          ? { status: 200, body: setup(false, "2026-11-20T00:00:00Z", true) }
          : { status: failure.status };
      }
      return quiet(call);
    });
    expect(fake.text()).toContain("Claude Code 2.4.1");

    button(fake.root, "Look again").dispatchEvent(new Event("click"));
    await settle();
    expect(fake.text()).toContain(failure.shown);
    expect(fake.text()).toContain("Claude Code 2.4.1");
    expect(fake.text()).toContain("Codex");
    expect(fake.text()).toContain("Claude");
  });
}
