// The page side of pluxy's extension page channel, version 0 (README.md beside this file).
// Copy this file into your package's ui folder and load it as a module:
//
//   import { connect } from "./pluxy-page.js";
//   const pluxy = await connect();
//   const { values } = await pluxy.call("config.get");

const VERSION = 0;

/**
 * Says hello to the panel and resolves once it answers with the channel. Rejects when the page
 * is not inside the panel, the panel refuses, or no answer comes within timeoutMs.
 * @param {{ timeoutMs?: number, applyTheme?: boolean }} [options]
 * @returns {Promise<PluxyPage>}
 */
export function connect(options = {}) {
  const timeoutMs = options.timeoutMs ?? 10000;
  const apply = options.applyTheme ?? true;
  return new Promise((resolve, reject) => {
    if (window.parent === window) {
      reject(new Error("this page is not inside the pluxy panel"));
      return;
    }
    const done = () => {
      clearTimeout(timer);
      window.removeEventListener("message", onMessage);
    };
    const timer = setTimeout(() => {
      done();
      reject(new Error("the pluxy panel did not answer"));
    }, timeoutMs);
    /** @param {MessageEvent} e */
    function onMessage(e) {
      if (e.source !== window.parent) return;
      const m = e.data;
      if (!m || typeof m !== "object" || m.pluxy !== "host") return;
      if (m.type === "refused") {
        done();
        reject(new Error(`the pluxy panel refused the page: ${m.reason}`));
        return;
      }
      if (m.type !== "welcome" || m.v !== VERSION || e.ports.length !== 1) return;
      done();
      resolve(new PluxyPage(e.ports[0], m, apply));
    }
    window.addEventListener("message", onMessage);
    window.parent.postMessage({ pluxy: "page", v: VERSION, type: "hello", versions: [VERSION] }, "*");
  });
}

/** Sets a theme snapshot's tokens and layout on root, as the panel does on its own. */
export function applyTheme(theme, root = document.documentElement) {
  if (!theme || typeof theme !== "object") return;
  for (const [name, value] of Object.entries(theme.tokens ?? {})) {
    root.style.setProperty(`--px-${name.replaceAll(".", "-")}`, String(value));
  }
  if (theme.appearance === "light" || theme.appearance === "dark") {
    root.dataset.appearance = theme.appearance;
    root.style.colorScheme = theme.appearance;
  }
  for (const [option, value] of Object.entries(theme.layout ?? {})) root.dataset[option] = String(value);
}

export class PluxyPage {
  /** @param {MessagePort} port */
  constructor(port, welcome, apply) {
    this.extension = welcome.extension;
    this.page = welcome.page;
    this.theme = welcome.theme;
    this._port = port;
    this._next = 0;
    this._pending = new Map();
    this._listeners = new Map();
    if (apply) {
      applyTheme(this.theme);
      this.on("theme", (t) => applyTheme(t));
    }
    port.onmessage = (e) => this._receive(e.data);
  }

  /**
   * Calls a method of the panel. Resolves with its result; rejects with an Error whose code is
   * the panel's error code.
   */
  call(method, params = null) {
    const id = String(++this._next);
    return new Promise((resolve, reject) => {
      this._pending.set(id, { resolve, reject });
      this._port.postMessage({ v: VERSION, id, method, params });
    });
  }

  /** Listens for an event from the panel ("theme"). Returns a function that stops listening. */
  on(event, fn) {
    if (!this._listeners.has(event)) this._listeners.set(event, new Set());
    this._listeners.get(event).add(fn);
    return () => this._listeners.get(event)?.delete(fn);
  }

  _receive(m) {
    if (!m || typeof m !== "object" || m.v !== VERSION) return;
    if (typeof m.event === "string") {
      if (m.event === "theme") this.theme = m.data;
      for (const fn of this._listeners.get(m.event) ?? []) fn(m.data);
      return;
    }
    const call = this._pending.get(m.id);
    if (!call) return;
    this._pending.delete(m.id);
    if (m.error) {
      const err = new Error(m.error.message);
      err.code = m.error.code;
      call.reject(err);
    } else call.resolve(m.result);
  }
}
