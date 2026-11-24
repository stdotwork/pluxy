// Direction A's keyboard: every action has a key. A screen registers the keys that work on it
// with useKeys; the shell listens once, shows the registered keys in the hint line at the bottom
// and lists them in the keys sheet (?), and the command menu offers the same actions by name.

import { createContext } from "preact";
import { useContext, useEffect, useRef } from "preact/hooks";

export interface KeyBinding {
  /**
   * The key, as KeyboardEvent.key gives it ("r", "/", "Enter", "ArrowDown"), or two keys pressed
   * one after the other with a space between ("g a").
   */
  keys: string;
  /** What it does, in a word or two, for the hint line and the command menu. */
  label: string;
  run: () => void;
  /** Left out of the hint line (still in the keys sheet and the command menu). */
  quiet?: boolean;
  /** Left out of the command menu: it only makes sense on the keyboard (moving a cursor). */
  keyOnly?: boolean;
  /** Its heading in the keys sheet. */
  group?: string;
}

type Listener = () => void;

/** The keys that work now: the shell's own and those of the screen that shows. */
export class KeyRegistry {
  private scopes = new Map<symbol, { bindings: KeyBinding[]; shape: string; base: boolean }>();
  private listeners = new Set<Listener>();
  private prefix: { key: string; at: number } | undefined;

  /**
   * Sets a scope's bindings. Screens set theirs on every render, so what each key does is always
   * current; listeners hear of it only when the keys or their labels changed. base marks the
   * shell's own scope, which every screen's keys come before.
   */
  set(scope: symbol, bindings: KeyBinding[], base = false) {
    const shape = bindings.map((b) => `${b.keys}\u0000${b.label}\u0000${b.quiet ? 1 : 0}`).join("\u0001");
    const changed = this.scopes.get(scope)?.shape !== shape;
    this.scopes.set(scope, { bindings, shape, base });
    if (changed) this.notify();
  }

  remove(scope: symbol) {
    if (this.scopes.delete(scope)) this.notify();
  }

  /** Every binding: the screen's first, the shell's own last. */
  all(): KeyBinding[] {
    const scopes = [...this.scopes.values()];
    return [...scopes.filter((s) => !s.base), ...scopes.filter((s) => s.base)].flatMap((s) => s.bindings);
  }

  /** Runs what a key does now. A hint or a menu entry made earlier calls this, never an old binding. */
  run(keys: string) {
    this.all()
      .find((b) => b.keys === keys)
      ?.run();
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  private notify() {
    for (const fn of this.listeners) fn();
  }

  /**
   * Handles a key press; true when a binding ran or the key began a two-key binding. Keys typed
   * into a field, and keys held with Ctrl, Alt or Meta, are never taken.
   */
  handle(e: KeyboardEvent, now = Date.now()): boolean {
    if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return false;
    if (typing(e.target) && e.key !== "Escape") return false;
    const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    const all = this.all();
    const pending = this.prefix && now - this.prefix.at < 1500 ? this.prefix.key : undefined;
    this.prefix = undefined;
    if (pending) {
      const hit = all.find((b) => b.keys === `${pending} ${key}`);
      if (hit) {
        hit.run();
        return true;
      }
    }
    const hit = all.find((b) => b.keys === key || (e.key === "?" && b.keys === "?"));
    if (hit) {
      hit.run();
      return true;
    }
    if (all.some((b) => b.keys.startsWith(`${key} `))) {
      this.prefix = { key, at: now };
      return true;
    }
    return false;
  }
}

/** Whether a key press is text going into a field. */
function typing(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el?.tagName) return false;
  if (el.isContentEditable) return true;
  if (el.tagName === "TEXTAREA" || el.tagName === "SELECT") return true;
  if (el.tagName !== "INPUT") return false;
  const type = (el as HTMLInputElement).type;
  return !["checkbox", "radio", "button", "submit", "range"].includes(type);
}

export const KeysContext = createContext<KeyRegistry | null>(null);

/** Registers a screen's keys while it shows. Pass a new array when what they do changes. */
export function useKeys(bindings: KeyBinding[]) {
  const registry = useContext(KeysContext);
  const scope = useRef(Symbol("keys"));
  // The bindings close over the screen's state, so the registry always holds the latest ones.
  useEffect(() => {
    registry?.set(scope.current, bindings);
  });
  useEffect(() => {
    const s = scope.current;
    return () => registry?.remove(s);
  }, [registry]);
}

/** A key as people read it: Enter is ↵, arrows are arrows. */
export function keyCap(key: string): string {
  const caps: Record<string, string> = {
    Enter: "↵",
    Escape: "esc",
    ArrowUp: "↑",
    ArrowDown: "↓",
    ArrowLeft: "←",
    ArrowRight: "→",
    " ": "space",
  };
  return caps[key] ?? key;
}
