// The close prompt the native side shows through window.__pluxyClosePrompt (src/window.tsx): its
// two kinds, their controls and what each sends back through window_close. These drive the real
// component through Preact's DOM renderer against a fake window command channel, using a small DOM
// that carries only the nodes and events Preact and the dialog need.
import { afterEach, expect, test } from "bun:test";
import { render } from "preact";
import { act } from "preact/test-utils";
import { ClosePrompt } from "../src/window";

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
  /** A dialog's open state, as showModal and close set it. */
  open = false;
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

  getAttribute(name: string) {
    return this.attributes.find((attribute) => attribute.name === name)?.value ?? null;
  }

  setAttribute(name: string, value: string) {
    const found = this.attributes.find((attribute) => attribute.name === name);
    if (found) found.value = String(value);
    else this.attributes.push({ name, value: String(value) });
  }

  removeAttribute(name: string) {
    this.attributes = this.attributes.filter((attribute) => attribute.name !== name);
  }

  showModal() {
    this.open = true;
  }

  close() {
    this.open = false;
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
    super(document, "#text");
    this.nodeType = 3;
    this.nodeName = "#text";
    this.data = String(data);
  }

  override get textContent() {
    return this.data;
  }

  override set textContent(value: string) {
    this.data = value;
  }
}

class FakeDocument {
  nodeType = 9;
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

function all(root: FakeNode, tag: string): FakeNode[] {
  return [...(root.localName === tag ? [root] : []), ...root.childNodes.flatMap((child) => all(child, tag))];
}

async function settle() {
  for (let round = 0; round < 4; round++) {
    await act(async () => {
      for (let i = 0; i < 20; i++) await Promise.resolve();
    });
  }
}

/** The prompt on a page of its own; calls records every window command it sends. */
async function mount() {
  const calls: { cmd: string; args: unknown }[] = [];
  const document = new FakeDocument();
  const win: { __TAURI_INTERNALS__: unknown; __pluxyClosePrompt?: (kind?: "no-tray") => boolean } = {
    __TAURI_INTERNALS__: {
      invoke: (cmd: string, args?: unknown) => {
        calls.push({ cmd, args });
        return Promise.resolve(null);
      },
      transformCallback: () => 1,
    },
  };
  Object.defineProperty(globalThis, "document", { configurable: true, value: document });
  Object.defineProperty(globalThis, "window", { configurable: true, value: win });
  const root = document.createElement("main");
  document.body.appendChild(root);
  await act(async () => {
    render(<ClosePrompt />, root as unknown as Element);
  });
  await settle();
  const dialog = () => {
    const d = all(root, "dialog")[0];
    if (!d) throw new Error("no close prompt");
    return d;
  };
  const buttons = () => all(dialog(), "button");
  const button = (label: string) => {
    const b = buttons().find((b) => b.textContent === label);
    if (!b) throw new Error(`no button ${label}: ${buttons().map((b) => b.textContent)}`);
    return b;
  };
  const fire = async (node: FakeNode, type: string) => {
    await act(async () => {
      node.dispatchEvent(new Event(type, { cancelable: true }));
    });
    await settle();
  };
  return {
    calls,
    dialog,
    /** The native side asks, as lib.rs ask_on_close does; what the page answers. */
    ask: async (kind?: "no-tray") => {
      let answer: boolean | undefined;
      await act(async () => {
        answer = kind ? win.__pluxyClosePrompt?.(kind) : win.__pluxyClosePrompt?.();
      });
      await settle();
      return answer;
    },
    title: () => all(dialog(), "h2")[0]?.textContent,
    text: () => dialog().textContent,
    labels: () => buttons().map((b) => b.textContent),
    variant: (label: string) => button(label).getAttribute("data-variant"),
    switchLabel: () => all(dialog(), "label")[0]?.textContent,
    toggle: async () => {
      const input = all(dialog(), "input")[0];
      if (!input) throw new Error("no switch");
      input.checked = !input.checked;
      await fire(input, "change");
    },
    click: (label: string) => fire(button(label), "click"),
    escape: () => fire(dialog(), "cancel"),
  };
}

test("with no tray, the prompt asks to close and stop pluxy, and only that sends anything", async () => {
  const p = await mount();
  expect(await p.ask("no-tray")).toBe(true);
  expect(p.dialog().open).toBe(true);
  expect(p.title()).toBe("Close and stop pluxy?");
  expect(p.text()).toContain(
    "This desktop has no tray, so closing the window stops pluxy, and your coding tools stop getting answers.",
  );
  expect(p.switchLabel()).toBe("Don't ask again");
  // No Minimize to tray: there is no tray to keep the app in.
  expect(p.labels()).toEqual(["Keep open", "Close and stop pluxy"]);
  expect(p.variant("Keep open")).toBe("primary");
  expect(p.variant("Close and stop pluxy")).toBe("danger");

  // Keep open keeps the window and sends nothing; so does Escape.
  await p.click("Keep open");
  expect(p.dialog().open).toBe(false);
  await p.ask("no-tray");
  await p.escape();
  expect(p.dialog().open).toBe(false);
  expect(p.calls.filter((c) => c.cmd === "window_close")).toEqual([]);

  // Close and stop pluxy with Don't ask again: quits, remembered as the no-tray prompt's answer.
  await p.ask("no-tray");
  await p.toggle();
  await p.click("Close and stop pluxy");
  expect(p.calls.filter((c) => c.cmd === "window_close")).toEqual([
    { cmd: "window_close", args: { action: "exit", remember: true, kind: "no-tray" } },
  ]);
  expect(p.dialog().open).toBe(false);
});

test("the Ask prompt keeps its choices, and sends its own kind after a no-tray prompt", async () => {
  const p = await mount();
  await p.ask("no-tray");
  await p.click("Keep open");
  expect(await p.ask()).toBe(true);
  expect(p.title()).toBe("What would you like to do?");
  expect(p.switchLabel()).toBe("Remember this choice");
  expect(p.labels()).toEqual(["Minimize to tray", "Exit app", "Cancel"]);
  await p.toggle();
  await p.click("Minimize to tray");
  await p.ask();
  await p.click("Exit app");
  await p.ask();
  await p.click("Cancel");
  await p.ask();
  await p.escape();
  expect(p.calls.filter((c) => c.cmd === "window_close").map((c) => c.args)).toEqual([
    { action: "minimize-to-tray", remember: true, kind: "ask" },
    { action: "exit", remember: false, kind: "ask" },
  ]);
});
