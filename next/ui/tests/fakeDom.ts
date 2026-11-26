// A small DOM that carries only the nodes and events Preact needs, so a test can mount a real page
// or hook through Preact's DOM renderer and read its text back. Call restoreDom in afterEach.
import { type ComponentChildren, render } from "preact";
import { act } from "preact/test-utils";

export class FakeNode {
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

  /** Finds nothing: a page that looks for an element to focus finds none. */
  querySelector() {
    return null;
  }

  /** A dialog's showModal and close: a fake dialog is open by being in the tree. */
  showModal() {}
  close() {}

  /** Takes focus: the document's activeElement is then this node. */
  focus() {
    this.ownerDocument.activeElement = this;
  }

  dispatchEvent(event: Event) {
    Object.defineProperty(event, "target", { configurable: true, value: this });
    Object.defineProperty(event, "currentTarget", { configurable: true, value: this });
    for (const listener of this.listeners.get(event.type) ?? []) listener.call(this, event);
    return true;
  }
}

export class FakeText extends FakeNode {
  private data: string;

  constructor(document: FakeDocument, data: string) {
    super(document, "#text", "http://www.w3.org/1999/xhtml");
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

export class FakeDocument {
  nodeType = 9;
  visibilityState: DocumentVisibilityState = "visible";
  body = new FakeNode(this, "body");
  /** The node that last took focus (FakeNode.focus), else the body. */
  activeElement: FakeNode = this.body;
  private listeners = new Map<string, Set<() => void>>();

  get hidden() {
    return this.visibilityState !== "visible";
  }

  createElement(tag: string) {
    return new FakeNode(this, tag);
  }

  createElementNS(namespaceURI: string, tag: string) {
    return new FakeNode(this, tag, namespaceURI);
  }

  createTextNode(data: string) {
    return new FakeText(this, data);
  }

  addEventListener(name: string, listener: () => void) {
    const set = this.listeners.get(name) ?? new Set();
    set.add(listener);
    this.listeners.set(name, set);
  }

  removeEventListener(name: string, listener: () => void) {
    this.listeners.get(name)?.delete(listener);
  }

  count(name: string) {
    return this.listeners.get(name)?.size ?? 0;
  }

  dispatch(name: string) {
    for (const listener of [...(this.listeners.get(name) ?? [])]) listener();
  }
}

/** The window's listeners, so a test can say the window got focus. */
export class FakeWindow {
  private listeners = new Map<string, Set<() => void>>();

  addEventListener(name: string, listener: () => void) {
    const set = this.listeners.get(name) ?? new Set();
    set.add(listener);
    this.listeners.set(name, set);
  }

  removeEventListener(name: string, listener: () => void) {
    this.listeners.get(name)?.delete(listener);
  }

  count(name: string) {
    return this.listeners.get(name)?.size ?? 0;
  }

  dispatch(name: string) {
    for (const listener of [...(this.listeners.get(name) ?? [])]) listener();
  }
}

const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
const originalHTMLElement = Object.getOwnPropertyDescriptor(globalThis, "HTMLElement");

/** The roots mountIn rendered into, unmounted by restoreDom. */
const mounted: FakeNode[] = [];
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");

/**
 * Unmounts every tree mountIn rendered, then puts back the document, window and navigator the test
 * run started with. Unmounting first stops the pages' timers and listeners while their document is
 * still there: a page left mounted would tick on after it, against no document.
 */
export function restoreDom() {
  // Inside act: Preact runs an unmounted tree's effect cleanups after the next paint, and act runs
  // them now.
  for (const root of mounted.splice(0))
    act(() => {
      render(null, root as unknown as Element);
    });
  if (originalHTMLElement) Object.defineProperty(globalThis, "HTMLElement", originalHTMLElement);
  else Reflect.deleteProperty(globalThis, "HTMLElement");
  if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
  else Reflect.deleteProperty(globalThis, "window");
  if (originalNavigator) Object.defineProperty(globalThis, "navigator", originalNavigator);
  else Reflect.deleteProperty(globalThis, "navigator");
  if (originalDocument) Object.defineProperty(globalThis, "document", originalDocument);
  else Reflect.deleteProperty(globalThis, "document");
}

export function installDocument() {
  const document = new FakeDocument();
  Object.defineProperty(globalThis, "document", { configurable: true, value: document });
  Object.defineProperty(globalThis, "window", { configurable: true, value: new FakeWindow() });
  // What a dialog checks the control that opened it against.
  if (!originalHTMLElement)
    Object.defineProperty(globalThis, "HTMLElement", { configurable: true, value: FakeNode });
  return document;
}

export const fakeWindow = () => (globalThis as unknown as { window: FakeWindow }).window;

/** Let the host promises settle and Preact run the renders they cause. */
export async function settle() {
  await act(async () => {
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

export function all(root: FakeNode, tag: string): FakeNode[] {
  return [...(root.localName === tag ? [root] : []), ...root.childNodes.flatMap((child) => all(child, tag))];
}

export function button(root: FakeNode, label: string) {
  const found = all(root, "button").find((node) => node.textContent === label);
  if (!found) throw new Error(`no button labelled ${label}`);
  return found;
}

/** Mounts a tree in a fresh fake document and lets its first reads settle. */
export async function mountIn(tree: ComponentChildren) {
  const document = installDocument();
  const root = document.createElement("main");
  document.body.appendChild(root);
  mounted.push(root);
  await act(async () => {
    render(tree, root as unknown as Element);
  });
  await settle();
  return {
    document,
    root,
    text: () => root.textContent,
    unmount: async () => {
      await act(async () => render(null, root as unknown as Element));
      const at = mounted.indexOf(root);
      if (at >= 0) mounted.splice(at, 1);
    },
  };
}
