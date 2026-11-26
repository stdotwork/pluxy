// What the page tests share: a small DOM that carries only the nodes and events Preact needs, a
// fake window command channel (what @tauri-apps/api's invoke calls), and a way to mount a page in
// the kit and drive it as a person would, by the words on screen. Reads of a tool's settings wait
// for the test, so it can say which answer comes when; writes and the other native calls are
// recorded. Not a test file itself: the page tests import it.
import { afterEach } from "bun:test";
import { render, type VNode } from "preact";
import { act } from "preact/test-utils";
import { type Kit, KitContext } from "../../ui/kit/ui";

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
  open = false;
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

  getAttribute(name: string) {
    return this.attributes.find((attribute) => attribute.name === name)?.value ?? null;
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

  focus() {}
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

export class FakeDocument {
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

/** A call to a tool's preview that the test answers when it chooses. */
export interface Read {
  /** The add-on asked. */
  pkg: string;
  /** What the page asked to read. */
  input: { targets?: { file?: string; root?: string }[]; base_url?: string };
  /** The environment the page asked it in: null is this computer. */
  environment: unknown;
  answered: boolean;
  answer: (body: unknown) => void;
  /** The tool refuses the read, as a native call that throws. */
  fail: (message: string) => void;
}

/** A write the page sent to its add-on. */
export interface Write {
  pkg: string;
  op: string;
  input: { targets?: { target: string }[]; key?: string; [k: string]: unknown };
  environment: unknown;
}

/** WSL as the host offers it: these distributions, each with the user me. */
export interface Wsl {
  distros: string[];
}

/** The add-ons the page tests install: every tool page's, and the launcher. */
export const ADDONS = [
  "pluxy/claude-code",
  "pluxy/codex-setup",
  "pluxy/claude-desktop",
  "pluxy/opencode-setup",
  "pluxy/omp-setup",
  "pluxy/grok-setup",
  "pluxy/hermes-setup",
  "pluxy/pi-setup",
  "pluxy/execution",
];

/** A device catalog with the given add-ons installed and on (or as each says). */
export function catalogOf(
  packages: { id: string; enabled?: boolean; authorized?: boolean }[] = ADDONS.map((id) => ({ id })),
  safe = false,
) {
  return {
    safe_mode: safe,
    packages: packages.map((p) => ({
      id: p.id,
      enabled: p.enabled ?? true,
      authorized: p.authorized ?? true,
      tier: "native",
      trust: "curated",
      imports_allowed: false,
      source: { kind: "registry", name: "curated" },
      active: { pages: [] },
    })),
  };
}

export const CORE = "http://127.0.0.1:8317";

export interface ChannelOptions {
  wsl?: Wsl;
  /** The vault's secrets; by default one plain secret, "My key". */
  secrets?: unknown[];
  /** This device's add-ons (device_list); by default every one installed and on. */
  catalog?: unknown;
  /** What client_key_capture answers: a secret, or an error to throw. Default: a new key. */
  capture?: (args: { label: string; client: string }) => unknown;
  /** The connection's mode; local by default. */
  mode?: string;
  /** What an add-on answers to an operation other than preview (still recorded as a write). */
  ops?: Record<string, (input: Record<string, unknown>) => unknown>;
}

/**
 * The window's command channel: reads of a tool wait for the test, writes are recorded, and the
 * rest answer as an idle device would. calls records every command with its arguments.
 */
export function channel(o: ChannelOptions = {}) {
  const reads: Read[] = [];
  const writes: Write[] = [];
  const calls: { cmd: string; args: Record<string, unknown> }[] = [];
  const records: unknown[] = [];
  const secrets = [...(o.secrets ?? [{ id: "k1", label: "My key" }])];
  const invoke = (cmd: string, args: Record<string, unknown> = {}): Promise<unknown> => {
    calls.push({ cmd, args });
    switch (cmd) {
      case "environments_get":
        return Promise.resolve({ available: o.wsl !== undefined, environments: records });
      case "environment_add": {
        const { distro, user } = args as { distro: string; user: string };
        const record = { distro, user, uid: 1000, home: `/home/${user}`, arch: "amd64", checked_at: 1 };
        records.push(record);
        return Promise.resolve(record);
      }
      case "device_list":
        return Promise.resolve(o.catalog ?? catalogOf());
      case "vault_secrets":
        return Promise.resolve({ secrets });
      case "client_key_capture": {
        const a = args as { label: string; client: string };
        const made = o.capture
          ? o.capture(a)
          : {
              id: `made-${secrets.length}`,
              label: a.label,
              client_key: { client: a.client, core: { mode: "local", origin: CORE }, masked: "sk-…made" },
            };
        if (made instanceof Error) return Promise.reject(made.message);
        secrets.push(made);
        return Promise.resolve(made);
      }
      case "connection_get":
        return Promise.resolve({ signed_in: true, remembered: false, target: CORE, mode: o.mode ?? "local" });
      case "launcher_state":
        return Promise.resolve({ history: {} });
      case "launcher_terminals":
        return Promise.resolve({ terminals: [{ id: "automatic", label: "Automatic" }] });
      case "plugin:event|listen":
        return Promise.resolve(1);
      case "program_call": {
        const {
          op,
          input,
          package: pkg,
          environment,
        } = args as {
          op: string;
          input: Read["input"];
          package: string;
          environment?: unknown;
        };
        if (pkg === "pluxy/wsl") {
          if (op === "distros")
            return Promise.resolve({
              installed: true,
              distros: (o.wsl?.distros ?? []).map((name) => ({ name, default: false, version: null })),
            });
          return Promise.resolve({
            users: [{ name: "me", uid: 1000, home: "/home/me", default: false, root: false }],
          });
        }
        if (op === "preview")
          return new Promise((resolve, reject) => {
            const read: Read = {
              pkg,
              input,
              environment: environment ?? null,
              answered: false,
              answer: (body) => {
                read.answered = true;
                resolve(body);
              },
              fail: (message) => {
                read.answered = true;
                reject(message);
              },
            };
            reads.push(read);
          });
        if (op === "backup-list") return Promise.resolve({ backups: [], keep: 10 });
        writes.push({ pkg, op, input: input as Write["input"], environment: environment ?? null });
        const answer = o.ops?.[op]?.(input as Record<string, unknown>) ?? {};
        return answer instanceof Error ? Promise.reject(answer.message) : Promise.resolve(answer);
      }
      default:
        return Promise.resolve({});
    }
  };
  /** The newest read not answered yet. */
  const pending = () => [...reads].reverse().find((r) => !r.answered);
  return { reads, writes, calls, invoke, pending, secrets };
}

export type Channel = ReturnType<typeof channel>;

export async function settle() {
  // Each round lets pending answers in and Preact render and run the effects they cause; an
  // effect's own call (a reload after an answer) needs the next round.
  for (let round = 0; round < 6; round++) {
    await act(async () => {
      for (let i = 0; i < 20; i++) await Promise.resolve();
    });
  }
}

export function all(root: FakeNode, tag: string): FakeNode[] {
  return [...(root.localName === tag ? [root] : []), ...root.childNodes.flatMap((child) => all(child, tag))];
}

/** The models pluxy serves in the tests' kit. */
export const SERVED = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "gpt-6.1-sol"];

/** Installs the window (with the channel's invoke) and a document, without mounting anything. */
export function installWindow(invoke: Channel["invoke"]) {
  const document = new FakeDocument();
  Object.defineProperty(globalThis, "document", { configurable: true, value: document });
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      __TAURI_INTERNALS__: { invoke, transformCallback: () => 1 },
      __TAURI_EVENT_PLUGIN_INTERNALS__: { unregisterListener() {} },
      addEventListener() {},
      removeEventListener() {},
    },
  });
  return document;
}

/** Mounts page in the kit. served are the models pluxy serves; found the tools pluxy's own look at
 * this computer found (GET /setup). */
export async function mount(
  page: () => VNode,
  invoke: Channel["invoke"],
  served: string[] = SERVED,
  found: unknown[] = [],
) {
  const document = installWindow(invoke);
  const kit = {
    host: { prefs: { get: () => null, set: () => {} } },
    api: {
      servedModels: async () => ({ status: 200, body: { models: served.map((id) => ({ id })) } }),
      setup: async () => ({ status: 200, body: { found: { clients: found } } }),
    },
    unauthorized: () => {},
    extensionsChanged: () => {},
  } as unknown as Kit;
  const root = document.createElement("main");
  document.body.appendChild(root);
  await act(async () => {
    render(<KitContext.Provider value={kit}>{page()}</KitContext.Provider>, root);
  });
  await settle();

  /** The labels whose own text (a span in them) is label. */
  const labelled = (label: string) =>
    all(root, "label").filter((l) =>
      l.childNodes.some((c) => c.localName === "span" && c.textContent === label),
    );
  /** The text input a label names, or the input whose accessible name is label. */
  const field = (label: string) => {
    const found =
      labelled(label).flatMap((l) => all(l, "input"))[0] ??
      all(root, "input").find((i) => i.getAttribute("aria-label") === label);
    if (!found) throw new Error(`no field labelled ${label}`);
    return found;
  };
  const buttons = (label: string) => all(root, "button").filter((b) => b.textContent === label);
  const button = (label: string) => buttons(label).find((b) => !b.disabled) ?? buttons(label)[0];
  return {
    root,
    text: () => root.textContent,
    /** The path a field shows. */
    shown: (label: string) => field(label).value,
    pick: async (label: string) => {
      const radio = labelled(label).flatMap((l) => all(l, "input"))[0];
      if (!radio) throw new Error(`no choice labelled ${label}`);
      await act(async () => {
        radio.dispatchEvent(new Event("change"));
      });
      await settle();
    },
    type: async (label: string, value: string) => {
      const input = field(label);
      input.value = value;
      await act(async () => {
        input.dispatchEvent(new Event("input"));
      });
    },
    /** Opens the collapsed part whose summary says label. */
    open: async (label: string) => {
      const summary = all(root, "summary").find((s) => s.textContent === label);
      const details = summary?.parentNode;
      if (!details) throw new Error(`no collapsed part ${label}`);
      details.open = true;
      await act(async () => {
        details.dispatchEvent(new Event("toggle"));
      });
      await settle();
    },
    hasButton: (label: string) => buttons(label).length > 0,
    /** Whether a button labelled label can be pressed. */
    canPress: (label: string) => buttons(label).some((b) => !b.disabled),
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

export type Mounted = Awaited<ReturnType<typeof mount>>;
