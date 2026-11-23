// The client setup pages' file preview: a read of the settings file that answers after the person
// changed the path is dropped, so the page's target is always the file its field shows and Apply
// never writes to another. These drive the real pages through Preact's DOM renderer against a fake
// window command channel, using a small DOM that carries only the nodes and events Preact needs.
import { afterEach, expect, test } from "bun:test";
import { render, type VNode } from "preact";
import { act } from "preact/test-utils";
import { type Kit, KitContext } from "../../ui/kit/ui";
import { ClaudeCodePage } from "../src/claudeCode";
import { PiPage } from "../src/pi";

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

  get firstChild(): FakeNode | null {
    return this.childNodes[0] ?? null;
  }

  get nextSibling(): FakeNode | null {
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

const original = {
  document: Object.getOwnPropertyDescriptor(globalThis, "document"),
  window: Object.getOwnPropertyDescriptor(globalThis, "window"),
};

afterEach(() => {
  for (const name of ["document", "window"] as const) {
    const was = original[name];
    if (was) Object.defineProperty(globalThis, name, was);
    else Reflect.deleteProperty(globalThis, name);
  }
});

/** A call to a tool's `preview` that the test answers when it chooses. */
interface Read {
  /** What the page asked to read. */
  input: { targets?: { file?: string }[] };
  answer: (body: unknown) => void;
}

/**
 * The window's command channel (what @tauri-apps/api's invoke calls): reads of the tool wait for the
 * test, so it can say which answer comes when; writes are recorded; the rest answer as an idle
 * device would, with one client key in the vault.
 */
function channel() {
  const reads: Read[] = [];
  const writes: { op: string; input: { targets?: { target: string }[] } }[] = [];
  const invoke = (cmd: string, args?: Record<string, unknown>): Promise<unknown> => {
    switch (cmd) {
      case "environments_get":
        return Promise.resolve({ available: false, environments: [] });
      case "vault_secrets":
        return Promise.resolve({ secrets: [{ id: "k1", label: "My key" }] });
      case "connection_get":
        return Promise.resolve({ signed_in: true, remembered: false, target: "http://127.0.0.1:8317" });
      case "launcher_state":
        return Promise.resolve({ history: {} });
      case "launcher_terminals":
        return Promise.resolve({ terminals: [] });
      case "plugin:event|listen":
        return Promise.resolve(1);
      case "program_call": {
        const { op, input } = args as { op: string; input: Read["input"] };
        if (op === "preview") return new Promise((answer) => reads.push({ input, answer }));
        writes.push({ op, input });
        return Promise.resolve({});
      }
      default:
        return Promise.resolve({});
    }
  };
  return { reads, writes, invoke };
}

async function settle() {
  await act(async () => {
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

function all(root: FakeNode, tag: string): FakeNode[] {
  return [...(root.localName === tag ? [root] : []), ...root.childNodes.flatMap((child) => all(child, tag))];
}

async function mount(page: () => VNode, invoke: ReturnType<typeof channel>["invoke"]) {
  const document = new FakeDocument();
  Object.defineProperty(globalThis, "document", { configurable: true, value: document });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      __TAURI_INTERNALS__: { invoke, transformCallback: () => 1 },
      addEventListener() {},
      removeEventListener() {},
    },
  });
  const kit = {
    host: { prefs: { get: () => null, set: () => {} } },
    api: {},
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const root = document.createElement("main");
  document.body.appendChild(root);
  await act(async () => {
    render(<KitContext.Provider value={kit}>{page()}</KitContext.Provider>, root);
  });
  await settle();

  /** The text input a label names. */
  const field = (label: string) => {
    const found = all(root, "label")
      .filter((l) => l.childNodes.some((c) => c.localName === "span" && c.textContent === label))
      .flatMap((l) => all(l, "input"))[0];
    if (!found) throw new Error(`no field labelled ${label}`);
    return found;
  };
  const button = (label: string) => all(root, "button").find((b) => b.textContent === label);
  return {
    text: () => root.textContent,
    /** The path a field shows. */
    shown: (label: string) => field(label).value,
    pick: async (label: string) => {
      const radio = all(root, "label")
        .filter((l) => l.childNodes.some((c) => c.localName === "span" && c.textContent === label))
        .flatMap((l) => all(l, "input"))[0];
      if (!radio) throw new Error(`no choice labelled ${label}`);
      await act(async () => {
        radio.dispatchEvent(new Event("change"));
      });
    },
    type: async (label: string, value: string) => {
      const input = field(label);
      input.value = value;
      await act(async () => {
        input.dispatchEvent(new Event("input"));
      });
    },
    hasButton: (label: string) => button(label) !== undefined,
    click: async (label: string) => {
      const b = button(label);
      if (!b) throw new Error(`no button labelled ${label}`);
      if (b.disabled) throw new Error(`button ${label} is disabled`);
      await act(async () => {
        b.dispatchEvent(new Event("click"));
      });
      await settle();
    },
  };
}

const claudeCodeRead = (path: string) => ({
  targets: [{ selected: path, target: path, link: null }],
  applied: false,
  settings: { state: "not-configured", startup_model: null },
});

const piRead = (folder: string) => ({
  targets: [{ selected: folder, path: null, target: `${folder}/cliproxyapi.json`, link: null }],
  applied: false,
  pi: {
    state: "not-configured",
    adapter: { installed: false, version: null, bundled: "1.0.0", update: false, foreign: false },
  },
});

const A = "/home/me/A.json";
const B = "/home/me/B.json";

/** Presses Read settings on A, edits the field to B while the read runs, then lets A's answer in. */
async function lateReplyForA() {
  const c = channel();
  const page = await mount(() => <ClaudeCodePage />, c.invoke);
  await page.pick("A selected file");
  await page.type("File", A);
  await page.click("Read settings");
  expect(c.reads.map((r) => r.input.targets?.[0]?.file)).toEqual([A]);
  await page.type("File", B);
  c.reads[0]?.answer(claudeCodeRead(A));
  await settle();
  return { c, page };
}

test("Claude Code: a read that answers after the path changed is dropped", async () => {
  const { page } = await lateReplyForA();
  expect(page.shown("File")).toBe(B);
  expect(page.text()).not.toContain("Writes to");
  expect(page.text()).not.toContain(A);
  expect(page.hasButton("Connect in One Click")).toBe(false);
});

test("Claude Code: Apply never writes to a file other than the one the field shows", async () => {
  const { c, page } = await lateReplyForA();
  // On the old page the late answer pinned A: Apply was offered, for A, beside a field showing B.
  if (page.hasButton("Connect in One Click")) {
    await page.pick("My key");
    await page.click("Connect in One Click");
  }
  const sent = c.writes
    .filter((w) => w.op === "apply")
    .flatMap((w) => w.input.targets?.map((t) => t.target) ?? []);
  expect(sent.filter((target) => target !== B)).toEqual([]);
});

test("Claude Code: a read of the path the field shows is kept, and Apply writes to it", async () => {
  const c = channel();
  const page = await mount(() => <ClaudeCodePage />, c.invoke);
  await page.pick("A selected file");
  await page.type("File", A);
  await page.click("Read settings");
  c.reads[0]?.answer(claudeCodeRead(A));
  await settle();
  expect(page.text()).toContain("Writes to");
  expect(page.text()).toContain(A);
  await page.pick("My key");
  await page.click("Connect in One Click");
  const applies = c.writes.filter((w) => w.op === "apply");
  expect(applies.map((w) => w.input.targets?.[0]?.target)).toEqual([A]);
});

test("Pi: a read that answers after the folder changed is dropped", async () => {
  const c = channel();
  const page = await mount(() => <PiPage />, c.invoke);
  await page.pick("A selected agent folder");
  await page.type("Folder", "/home/me/a");
  await page.click("Read settings");
  await page.type("Folder", "/home/me/b");
  c.reads[0]?.answer(piRead("/home/me/a"));
  await settle();
  expect(page.shown("Folder")).toBe("/home/me/b");
  expect(page.text()).not.toContain("Installation Status");
  expect(page.text()).not.toContain("/home/me/a");
});

test("Pi: a read of the folder the field shows is kept", async () => {
  const c = channel();
  const page = await mount(() => <PiPage />, c.invoke);
  await page.pick("A selected agent folder");
  await page.type("Folder", "/home/me/a");
  await page.click("Read settings");
  c.reads[0]?.answer(piRead("/home/me/a"));
  await settle();
  expect(page.text()).toContain("Installation Status");
  expect(page.text()).toContain("/home/me/a/cliproxyapi.json");
});
