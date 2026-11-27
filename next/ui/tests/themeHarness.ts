// A theme state against a fake core: core keeps the themes and which one is on, prefs keep the
// device's choices. The theme state's tests and the pages that show it share it.
import type { Feature, Host, Method, Reply } from "../kit/host";
import { ThemeState } from "../kit/theme/state";
import defaultTheme from "../themes/default.json";
import terminal from "../themes/terminal.json";

/** Core's themes, as its management API serves them, with the calls it got. */
export class FakeCore {
  current = "";
  files = new Map<string, unknown>();
  calls: string[] = [];
  /** A reply to give instead, for a call this returns one for. */
  instead: (method: Method, path: string) => Reply<unknown> | undefined = () => undefined;

  call(method: Method, path: string, body?: unknown): Reply<unknown> {
    this.calls.push(`${method} ${path}`);
    const forced = this.instead(method, path);
    if (forced) return forced;
    if (method === "GET" && path === "/themes") {
      const themes = [...this.files].map(([id, file]) => ({
        id,
        name: String((file as { name?: unknown }).name ?? ""),
        bytes: JSON.stringify(file).length,
      }));
      return { status: 200, body: { current: this.current, themes } };
    }
    if (method === "PUT" && path === "/theme") {
      this.current = (body as { id: string }).id;
      return { status: 200, body: { id: this.current } };
    }
    const id = path.replace(/^\/themes\//, "");
    if (method === "GET") {
      return this.files.has(id) ? { status: 200, body: this.files.get(id) } : { status: 404 };
    }
    if (method === "PUT") {
      this.files.set(id, body);
      return { status: 200, body: { id, name: "", bytes: JSON.stringify(body).length } };
    }
    if (method === "DELETE") {
      this.files.delete(id);
      if (this.current === id) this.current = "";
      return { status: 204 };
    }
    return { status: 404 };
  }

  count(call: string) {
    return this.calls.filter((c) => c === call).length;
  }
}

export function setup(prefs: Record<string, string> = {}, features: Feature[] = []) {
  /** How many times the host was told to sign out. */
  const signedOut = { times: 0 };
  const core = new FakeCore();
  const kept = new Map(Object.entries(prefs));
  const host: Host = {
    call: async <T>(method: Method, path: string, body?: unknown) =>
      core.call(method, path, body) as Reply<T>,
    signedIn: () => true,
    signIn: async () => undefined,
    signOut: () => {
      signedOut.times++;
    },
    prefs: {
      get: (name) => kept.get(name) ?? null,
      set: (name, value) => {
        if (value === null) kept.delete(name);
        else kept.set(name, value);
      },
    },
    features: new Set(features),
  };
  const doc = Object.assign(new EventTarget(), {
    visibilityState: "visible",
    documentElement: { dataset: {}, classList: { toggle() {} }, style: {} },
    adoptedStyleSheets: [],
  });
  const win = Object.assign(new EventTarget(), {
    matchMedia: () => ({ matches: false, addEventListener() {} }),
  });
  const state = () =>
    new ThemeState(host, doc as unknown as Document, [defaultTheme, terminal], win as unknown as Window);
  return { core, kept, doc, win, host, signedOut, state };
}

/** A stand-in for the browser's constructed stylesheets, which the theme state writes to. */
export function stubStyleSheets(): () => void {
  const g = globalThis as { CSSStyleSheet?: unknown };
  const had = g.CSSStyleSheet;
  g.CSSStyleSheet = class {
    text = "";
    replaceSync(text: string) {
      this.text = text;
    }
  };
  return () => {
    g.CSSStyleSheet = had;
  };
}
