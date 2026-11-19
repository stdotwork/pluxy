// The themes the UI offers and the person's choices, applied to the document whenever either
// changes. Core keeps the themes a person adds and which theme is on, so the panel, the desktop
// app and the `pluxy theme` commands agree. The host's prefs keep what depends on the device
// (appearance, layout, density, the person's own CSS, safe mode) and a copy of the theme last
// applied, so the window paints in it before core answers and while signed out.
//
// A host passes JSON values, not text, so a theme file reaches core as the JSON the person gave,
// written out again: its values are kept, its spacing is not.

import { problem } from "../api";
import { type StoredTheme, themesApi } from "../api/themes";
import type { Host, Reply } from "../host";
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

/** A theme the picker lists. A stored theme has `offered` once its file has been read. */
export interface Listed {
  id: string;
  name: string;
  builtIn: boolean;
  offered?: Offered;
  /** Why the stored file cannot be used. */
  problem?: string;
}

/** What the state needs of the window: the system's settings, and when the person comes back. */
export type ThemeWindow = Pick<Window, "matchMedia" | "addEventListener" | "removeEventListener">;

const DEFAULT_THEME = "pluxy/default";
const PREF_APPLIED = "applied-theme";
const PREF_CHOICES = "choices";
const PREF_SAFE = "safe";
/** Where the chosen theme and the imported themes were kept before core kept them. */
const PREF_OLD_THEME = "theme";
const PREF_OLD_IMPORTED = "themes";

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

const ok = (r: Reply<unknown>) => r.status >= 200 && r.status < 300;

/** A stored theme's file, read: usable, or why not. */
type Read = { file: unknown; offered: Offered } | { problem: string };

const BUILT_IN_ID = (id: string) => `${id} is a built-in theme's ID`;

export class ThemeState {
  private builtIns: Offered[];
  private builtInFiles = new Map<string, unknown>();
  private stored: StoredTheme[] = [];
  private files = new Map<string, Read>();
  /** Core's current theme and its size when this state last followed it. */
  private followed?: string;
  private listeners = new Set<() => void>();
  private applier: ThemeApplier;
  private env: Environment;
  private api: ReturnType<typeof themesApi>;
  /** Calls to core run one after another, so a re-read never crosses a change. */
  private last: Promise<unknown> = Promise.resolve();
  private rereading = false;
  private unauthorized: () => void = () => {};
  themeId: string;
  choices: Choices;
  resolved!: Resolved;
  /** Why the themes could not be read from core, when the last read failed. */
  problem?: string;

  constructor(
    private readonly host: Host,
    private readonly doc: Document,
    builtIn: unknown[],
    private readonly win: ThemeWindow = window,
  ) {
    this.applier = new ThemeApplier(doc);
    this.api = themesApi(host);
    this.builtIns = builtIn.flatMap((raw) => {
      const r = parseTheme(raw);
      if (!r.ok) return [];
      this.builtInFiles.set(r.theme.id, raw);
      return [{ theme: r.theme, builtIn: true, warnings: r.warnings }];
    });
    this.choices = {
      ...readChoices(this.readJSON<unknown>(PREF_CHOICES, {})),
      safe: host.prefs.get(PREF_SAFE) === "1",
    };

    // The theme last applied here, until core says which one is on.
    const applied = this.readJSON<{ id?: unknown; file?: unknown }>(PREF_APPLIED, {});
    if (typeof applied.id === "string") {
      this.themeId = applied.id;
      if (applied.file !== undefined) this.keep(applied.file, applied.id);
    } else {
      this.themeId = host.prefs.get(PREF_OLD_THEME) ?? DEFAULT_THEME;
      for (const text of this.oldImported()) {
        try {
          this.keep(JSON.parse(text));
        } catch {
          // Not a theme file: left out.
        }
      }
    }

    const dark = win.matchMedia("(prefers-color-scheme: dark)");
    const still = win.matchMedia("(prefers-reduced-motion: reduce)");
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
      return v ? ((JSON.parse(v) as T) ?? fallback) : fallback;
    } catch {
      return fallback;
    }
  }

  private oldImported(): string[] {
    const texts = this.readJSON<unknown>(PREF_OLD_IMPORTED, []);
    return Array.isArray(texts) ? texts.filter((t) => typeof t === "string") : [];
  }

  /**
   * Reads a stored theme's file and lists it under id (core's ID for it, which a theme added with
   * `pluxy theme add --id` has apart from its file's), else under the file's own ID. Returns why
   * the reader refuses it, or undefined.
   */
  private keep(file: unknown, id?: string): string | undefined {
    const r = parseTheme(file);
    const as = id ?? (r.ok ? r.theme.id : undefined);
    if (as === undefined) return r.ok ? undefined : r.error;
    if (this.builtInFiles.has(as)) return BUILT_IN_ID(as);
    if (!r.ok) {
      this.files.set(as, { problem: r.error });
      return r.error;
    }
    const theme = { ...r.theme, id: as };
    this.files.set(as, { file, offered: { theme, builtIn: false, warnings: r.warnings } });
    if (!this.stored.some((s) => s.id === as)) {
      this.stored = [...this.stored, { id: as, name: theme.name, bytes: 0 }];
    }
    return undefined;
  }

  private usable(id: string): { file: unknown; offered: Offered } | undefined {
    const read = this.files.get(id);
    return read && "offered" in read ? read : undefined;
  }

  /** The themes to pick from: the built-in ones, then the ones core keeps. */
  get listed(): Listed[] {
    return [
      ...this.builtIns.map((o) => ({ id: o.theme.id, name: o.theme.name, builtIn: true, offered: o })),
      ...this.stored
        .filter((s) => !this.builtInFiles.has(s.id))
        .map((s) => {
          const read = this.files.get(s.id);
          return {
            id: s.id,
            name: s.name || s.id,
            builtIn: false,
            offered: read && "offered" in read ? read.offered : undefined,
            problem: read && "problem" in read ? read.problem : undefined,
          };
        }),
    ];
  }

  /** The theme that is applied. */
  get current(): Offered {
    return (
      this.builtIns.find((o) => o.theme.id === this.themeId) ??
      this.usable(this.themeId)?.offered ??
      // biome-ignore lint/style/noNonNullAssertion: the built-in themes are never empty
      this.builtIns[0]!
    );
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

  /** Applies a theme this state holds, and keeps a copy for the next first paint. */
  private apply(id: string) {
    this.themeId = id;
    const read = this.usable(id);
    this.host.prefs.set(PREF_APPLIED, JSON.stringify(read ? { id, file: read.file } : { id }));
    this.update();
  }

  private saveChoices() {
    const { safe: _safe, ...rest } = this.choices;
    this.host.prefs.set(PREF_CHOICES, JSON.stringify(rest));
  }

  private queue<T>(work: () => Promise<T>): Promise<T> {
    const run = this.last.then(work, work);
    this.last = run.catch(() => {});
    return run;
  }

  /** A failed reply as one line; a refused key also sends the person back to signing in. */
  private failure(r: Reply<unknown>): string | undefined {
    if (r.status === 401) this.unauthorized();
    return problem(r);
  }

  /**
   * Follows core's themes from now on: reads them, moves themes kept in prefs by an earlier
   * version to core, and reads them again whenever the window gets focus or shows again, which
   * is how a change made in another window or with `pluxy theme` arrives. Call it once the host
   * is signed in, with what to do when core refuses the key; the function it returns stops it.
   */
  connect(unauthorized: () => void = () => {}): () => void {
    this.unauthorized = unauthorized;
    const reread = () => {
      if (this.doc.visibilityState !== "hidden") void this.refresh();
    };
    this.win.addEventListener("focus", reread);
    this.doc.addEventListener("visibilitychange", reread);
    void this.queue(async () => {
      await this.moveOld();
      await this.read(true);
    });
    return () => {
      this.unauthorized = () => {};
      this.win.removeEventListener("focus", reread);
      this.doc.removeEventListener("visibilitychange", reread);
    };
  }

  /** Reads core's themes again and follows a change. One re-read waits at a time. */
  refresh(): Promise<void> {
    if (this.rereading) return this.last.then(() => {});
    this.rereading = true;
    return this.queue(() => {
      this.rereading = false;
      return this.read(false);
    });
  }

  private mark(current: string): string {
    const id = current || DEFAULT_THEME;
    return `${id} ${this.stored.find((s) => s.id === id)?.bytes ?? ""}`;
  }

  private async read(first: boolean) {
    const r = await this.api.storedThemes();
    if (!ok(r) || !r.body) {
      this.problem = this.failure(r);
      for (const fn of this.listeners) fn();
      return;
    }
    this.problem = undefined;
    // A file read before is kept only while core still lists it at the same size.
    const before = new Map(this.stored.map((s) => [s.id, s.bytes]));
    this.stored = r.body.themes ?? [];
    for (const id of [...this.files.keys()]) {
      const now = this.stored.find((s) => s.id === id);
      if (!now || (before.get(id) !== now.bytes && id !== this.themeId)) this.files.delete(id);
    }
    const id = r.body.current || DEFAULT_THEME;
    const mark = this.mark(r.body.current);
    if (first || mark !== this.followed) {
      if (this.builtInFiles.has(id)) {
        this.followed = mark;
        this.apply(id);
        return;
      }
      if (this.stored.some((s) => s.id === id)) {
        const why = await this.fetchFile(id);
        if (why) this.problem = `${id}: ${why}`;
        else {
          this.followed = mark;
          this.apply(id);
          return;
        }
      } else {
        // Core names a theme nobody holds: the default shows, and core's choice is left alone.
        this.followed = mark;
        this.apply(DEFAULT_THEME);
        return;
      }
    }
    if (!this.usable(this.themeId) && !this.builtInFiles.has(this.themeId)) this.apply(DEFAULT_THEME);
    else for (const fn of this.listeners) fn();
  }

  /** Reads one stored theme's file from core. Returns why it cannot be used, or undefined. */
  private async fetchFile(id: string): Promise<string | undefined> {
    const r = await this.api.themeFile(id);
    if (!ok(r)) return this.failure(r);
    return this.keep(r.body, id);
  }

  /** Themes an earlier version kept in prefs go to core once; one core refuses stays out. */
  private async moveOld() {
    const prefs = this.host.prefs;
    const oldTheme = prefs.get(PREF_OLD_THEME);
    if (oldTheme === null && prefs.get(PREF_OLD_IMPORTED) === null) return;
    const list = await this.api.storedThemes();
    // Core did not answer: the prefs stay, for the next time.
    if (!ok(list) || !list.body) return;
    const held = new Set((list.body.themes ?? []).map((s) => s.id));
    for (const text of this.oldImported()) {
      const r = parseThemeText(text);
      if (!r.ok || this.builtInFiles.has(r.theme.id) || held.has(r.theme.id)) continue;
      const put = await this.api.storeTheme(r.theme.id, JSON.parse(text));
      if (put.status === 0 || put.status === 401) return;
      if (ok(put)) held.add(r.theme.id);
    }
    if (!list.body.current && oldTheme && (this.builtInFiles.has(oldTheme) || held.has(oldTheme))) {
      const set = await this.api.selectTheme(oldTheme);
      if (set.status === 0 || set.status === 401) return;
    }
    prefs.set(PREF_OLD_IMPORTED, null);
    prefs.set(PREF_OLD_THEME, null);
  }

  /** Reads the files of the stored themes not read yet, for the picker's previews. */
  loadFiles(): Promise<void> {
    return this.queue(async () => {
      const missing = this.stored.filter((s) => !this.files.has(s.id) && !this.builtInFiles.has(s.id));
      if (missing.length === 0) return;
      for (const s of missing) await this.fetchFile(s.id);
      for (const fn of this.listeners) fn();
    });
  }

  /**
   * Turns a theme on: it applies at once and core is told. Returns what failed, or undefined; after
   * a failure the theme that was on before is on again.
   */
  select(id: string): Promise<string | undefined> {
    return this.queue(async () => {
      if (!this.builtInFiles.has(id) && !this.usable(id)) {
        const why = await this.fetchFile(id);
        if (why) return why;
      }
      return this.turnOn(id);
    });
  }

  private async turnOn(id: string): Promise<string | undefined> {
    const before = this.themeId;
    this.apply(id);
    const r = await this.api.selectTheme(id);
    if (!ok(r)) {
      this.apply(before);
      return this.failure(r);
    }
    this.followed = this.mark(id);
    return undefined;
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

  /**
   * Adds a theme from a theme file's text: the reader checks it first, so a file it refuses never
   * reaches core; then core stores it and it is turned on. Returns why it was not added, or
   * undefined.
   */
  async addTheme(text: string): Promise<string | undefined> {
    const r = parseThemeText(text);
    if (!r.ok) return r.error;
    const id = r.theme.id;
    if (this.builtInFiles.has(id)) return BUILT_IN_ID(id);
    return this.queue(async () => {
      const file: unknown = JSON.parse(text);
      const put = await this.api.storeTheme(id, file);
      if (!ok(put)) return this.failure(put);
      const summary = put.body ?? { id, name: r.theme.name, bytes: 0 };
      this.stored = [...this.stored.filter((s) => s.id !== id), summary].sort((a, b) =>
        a.id < b.id ? -1 : a.id > b.id ? 1 : 0,
      );
      this.files.set(id, { file, offered: { theme: r.theme, builtIn: false, warnings: r.warnings } });
      const why = await this.turnOn(id);
      return why === undefined ? undefined : `it was added, but could not be turned on: ${why}`;
    });
  }

  /** Removes a stored theme from core. Returns what failed, or undefined. */
  removeTheme(id: string): Promise<string | undefined> {
    return this.queue(async () => {
      const r = await this.api.removeStoredTheme(id);
      if (!ok(r)) return this.failure(r);
      this.stored = this.stored.filter((s) => s.id !== id);
      this.files.delete(id);
      // Core goes back to the default when the theme that was on is removed.
      if (this.themeId === id) {
        this.followed = this.mark("");
        this.apply(DEFAULT_THEME);
      } else this.update();
      return undefined;
    });
  }

  /** The current theme's file, to save: its name and its JSON. */
  exportFile(): { name: string; text: string } {
    const id = this.current.theme.id;
    const file = this.builtInFiles.get(id) ?? this.usable(id)?.file ?? {};
    return { name: `${id.split("/").pop()}.json`, text: `${JSON.stringify(file, null, 2)}\n` };
  }
}
