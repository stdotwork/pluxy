// Mounts one of the app's pages through Preact's DOM renderer against a fake window command
// channel, so every command the page sends is recorded, every answer is chosen by the test, and the
// events the native side sends (listen) can be sent by the test. (connection-page.test.tsx keeps its
// own copy of the part that sends commands.)
import { type ComponentChildren, render } from "preact";
import { act } from "preact/test-utils";
import { type Kit, KitContext } from "../../ui/kit/ui";
import { all, type FakeNode, installDocument, settle } from "./fakeDom";

/** An answer by value, or by a function of the command's arguments; a function may throw (a refused command) or never answer. */
export type Handler = unknown | ((args: Record<string, unknown> | undefined) => unknown);
export type Handlers = Record<string, Handler>;

/** A command that never answers: the page is still waiting for it. */
export const never = () => new Promise<never>(() => {});

export async function mount(tree: ComponentChildren, handlers: Handlers = {}) {
  const document = installDocument();
  Object.defineProperty(globalThis, "location", { configurable: true, value: { hash: "" } });
  const calls: { cmd: string; args: Record<string, unknown> | undefined }[] = [];
  const callbacks = new Map<number, (e: unknown) => void>();
  const listeners: { event: string; cb: (e: unknown) => void }[] = [];
  let next = 0;
  Object.assign((globalThis as unknown as { window: object }).window, {
    __TAURI_EVENT_PLUGIN_INTERNALS__: { unregisterListener: () => {} },
    __TAURI_INTERNALS__: {
      invoke: (cmd: string, args?: Record<string, unknown>) => {
        if (cmd === "plugin:event|listen") {
          const cb = callbacks.get(args?.handler as number);
          if (cb) listeners.push({ event: args?.event as string, cb });
          return Promise.resolve(listeners.length);
        }
        if (cmd.startsWith("plugin:event|")) return Promise.resolve();
        calls.push({ cmd, args });
        if (!(cmd in handlers)) return Promise.reject(`not answered by this test: ${cmd}`);
        const h = handlers[cmd];
        return Promise.resolve().then(() => (typeof h === "function" ? h(args) : h));
      },
      transformCallback: (cb: (e: unknown) => void) => {
        callbacks.set(++next, cb);
        return next;
      },
    },
  });
  const root = document.createElement("main");
  document.body.appendChild(root);
  const kit = {
    host: { features: new Set<string>(), signOut: () => {} },
    themes: { subscribe: () => () => {}, resolved: { layout: { list: "table" } } },
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
    const b = buttons().find((b) => b.textContent === label);
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
    attr,
    show,
    fire,
    text: () => root.textContent,
    labels: () => buttons().map((b) => b.textContent),
    has: (label: string) => buttons().some((b) => b.textContent === label),
    button,
    click: (label: string) => fire(button(label), "click"),
    /** Types into the field named label (its row's field, found by the label's text). */
    input: async (label: string, value: string) => {
      const name = all(root, "label").find((l) => l.textContent === label);
      const input = name?.parentNode && all(name.parentNode, "input")[0];
      if (!input) throw new Error(`no field ${label}`);
      input.value = value;
      await fire(input, "input");
    },
    commands: (cmd: string) => calls.filter((c) => c.cmd === cmd).map((c) => c.args),
    /** The radio buttons of a choice, by their value (a property, not an attribute). */
    radios: () => all(root, "input").filter((i) => attr(i, "type") === "radio"),
    /** Every text field, by its data-part. */
    field: (part: string) => all(root, "input").find((i) => attr(i, "data-part") === part),
    /** The native side sends event with payload. */
    emit: async (event: string, payload: unknown) => {
      await act(async () => {
        for (const l of listeners.filter((l) => l.event === event)) l.cb({ event, id: 1, payload });
      });
      await settle();
    },
  };
}
