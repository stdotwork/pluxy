// The themes the UI offers and the person's choices, kept in the host's prefs, applied to the
// document whenever either changes.

import type { Host } from "../host";
import { isLayoutValue, LAYOUT_OPTIONS, type Layout, type Theme } from "./contract";
import {
  type Choices,
  DEFAULT_CHOICES,
  type Environment,
  type Resolved,
  resolve,
  ThemeApplier,
} from "./engine";
import { parseTheme, parseThemeText } from "./validate";

export interface Offered {
  theme: Theme;
  builtIn: boolean;
  warnings: string[];
}

const PREF_THEME = "theme";
const PREF_CHOICES = "choices";
const PREF_SAFE = "safe";
const PREF_IMPORTED = "themes";

/** The saved choices, keeping only values the UI can use: prefs may hold anything. */
function readChoices(raw: unknown): Choices {
  const c: Choices = { ...DEFAULT_CHOICES, layout: {} };
  if (typeof raw !== "object" || raw === null) return c;
  const r = raw as Record<string, unknown>;
  if (r.appearance === "light" || r.appearance === "dark" || r.appearance === "system")
    c.appearance = r.appearance;
  if (typeof r.density === "string") c.density = r.density;
  if (typeof r.css === "string") c.css = r.css;
  if (typeof r.layout === "object" && r.layout !== null) {
    for (const option of LAYOUT_OPTIONS) {
      const v = (r.layout as Record<string, unknown>)[option];
      if (isLayoutValue(option, v)) (c.layout as Record<string, string>)[option] = v;
    }
  }
  return c;
}

export class ThemeState {
  private builtIns: Offered[];
  private imported: { text: string; offered: Offered }[] = [];
  private listeners = new Set<() => void>();
  private applier: ThemeApplier;
  private env: Environment;
  themeId: string;
  choices: Choices;
  resolved!: Resolved;

  constructor(
    private readonly host: Host,
    doc: Document,
    builtIn: unknown[],
  ) {
    this.applier = new ThemeApplier(doc);
    this.builtIns = builtIn.flatMap((raw) => {
      const r = parseTheme(raw);
      return r.ok ? [{ theme: r.theme, builtIn: true, warnings: r.warnings }] : [];
    });
    for (const text of this.readJSON<string[]>(PREF_IMPORTED, [])) {
      const r = parseThemeText(text);
      if (r.ok)
        this.imported.push({ text, offered: { theme: r.theme, builtIn: false, warnings: r.warnings } });
    }
    this.choices = {
      ...readChoices(this.readJSON<unknown>(PREF_CHOICES, {})),
      safe: host.prefs.get(PREF_SAFE) === "1",
    };
    this.themeId = host.prefs.get(PREF_THEME) ?? "pluxy/default";

    const dark = window.matchMedia("(prefers-color-scheme: dark)");
    const still = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.env = { systemDark: dark.matches, reducedMotion: still.matches };
    dark.addEventListener("change", (e) => {
      this.env = { ...this.env, systemDark: e.matches };
      this.update();
    });
    still.addEventListener("change", (e) => {
      this.env = { ...this.env, reducedMotion: e.matches };
      this.update();
    });
    this.update();
  }

  private readJSON<T>(name: string, fallback: T): T {
    try {
      const v = this.host.prefs.get(name);
      return v ? (JSON.parse(v) as T) : fallback;
    } catch {
      return fallback;
    }
  }

  get offered(): Offered[] {
    const builtInIds = new Set(this.builtIns.map((o) => o.theme.id));
    return [
      ...this.builtIns,
      ...this.imported.map((i) => i.offered).filter((o) => !builtInIds.has(o.theme.id)),
    ];
  }

  get current(): Offered {
    const all = this.offered;
    // biome-ignore lint/style/noNonNullAssertion: the built-in themes are never empty
    return all.find((o) => o.theme.id === this.themeId) ?? all[0]!;
  }

  subscribe(fn: () => void) {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  private update() {
    this.resolved = resolve(this.current.theme, this.choices, this.env);
    this.applier.apply(this.resolved);
    for (const fn of this.listeners) fn();
  }

  private saveChoices() {
    const { safe: _safe, ...rest } = this.choices;
    this.host.prefs.set(PREF_CHOICES, JSON.stringify(rest));
  }

  select(id: string) {
    this.themeId = id;
    this.host.prefs.set(PREF_THEME, id);
    this.update();
  }

  setLayout<K extends keyof Layout>(option: K, value: Layout[K] | undefined) {
    const layout = { ...this.choices.layout };
    if (value === undefined) delete layout[option];
    else layout[option] = value;
    this.choices = { ...this.choices, layout };
    this.saveChoices();
    this.update();
  }

  set(change: Partial<Pick<Choices, "appearance" | "density" | "css">>) {
    this.choices = { ...this.choices, ...change };
    if ("density" in change && change.density === undefined) delete this.choices.density;
    this.saveChoices();
    this.update();
  }

  /** Clears the person's layout and density choices, so the theme's apply again. */
  resetLayout() {
    this.choices = { ...this.choices, layout: {} };
    delete this.choices.density;
    this.saveChoices();
    this.update();
  }

  setSafe(safe: boolean) {
    this.choices = { ...this.choices, safe };
    this.host.prefs.set(PREF_SAFE, safe ? "1" : null);
    this.update();
  }

  /** Adds a theme from a file's text. Returns why it was refused, or undefined. */
  importTheme(text: string): string | undefined {
    const r = parseThemeText(text);
    if (!r.ok) return r.error;
    if (this.builtIns.some((o) => o.theme.id === r.theme.id)) return `${r.theme.id} is a built-in theme's ID`;
    this.imported = this.imported.filter((i) => i.offered.theme.id !== r.theme.id);
    this.imported.push({ text, offered: { theme: r.theme, builtIn: false, warnings: r.warnings } });
    this.saveImported();
    this.select(r.theme.id);
    return undefined;
  }

  removeTheme(id: string) {
    this.imported = this.imported.filter((i) => i.offered.theme.id !== id);
    this.saveImported();
    if (this.themeId === id) this.select("pluxy/default");
    else this.update();
  }

  private saveImported() {
    this.host.prefs.set(PREF_IMPORTED, JSON.stringify(this.imported.map((i) => i.text)));
  }
}
