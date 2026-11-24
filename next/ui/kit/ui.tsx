// The kit's components. Each carries the data-part hook themes/README.md promises; class names
// are internal.

import { type ComponentChildren, createContext, type Ref } from "preact";
import { useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Api, problem } from "./api";
import type { Host, Reply } from "./host";
import type { PrivacyState } from "./privacy";
import { type StateOf, stateText, stateTone } from "./states";
import { strings } from "./strings";
import type { Layout } from "./theme/contract";
import type { ThemeState } from "./theme/state";

export { stateText } from "./states";
export { shortDuration, Time, when, whenText } from "./time";

const t = strings("kit", {
  confirm: "Yes, {action}",
  cancel: "Cancel",
  details: "Details: {title}",
  closeDetails: "Close details",
});

export interface Kit {
  host: Host;
  api: Api;
  themes: ThemeState;
  /** Privacy mode: pages mask with usePrivacy; extension pages are told through the theme event. */
  privacy: PrivacyState;
  /** Called when core refuses the key: the shell goes back to signing in. */
  unauthorized(): void;
  /** Called after an extension changed (installed, enabled, removed): its pages may differ. */
  extensionsChanged(): void;
}

export const KitContext = createContext<Kit | null>(null);

export function useKit(): Kit {
  const kit = useContext(KitContext);
  if (!kit) throw new Error("useKit outside the kit's shell");
  return kit;
}

/** Re-renders when the theme or the person's choices change. */
export function useThemeState(): ThemeState {
  const { themes } = useKit();
  const [, bump] = useState(0);
  useEffect(() => themes.subscribe(() => bump((n) => n + 1)), [themes]);
  return themes;
}

export function useLayout(): Layout {
  return useThemeState().resolved.layout;
}

/**
 * What useLoad knows. phase tells the cases apart: "loading" is no reply yet (never loaded);
 * "ready" is the last reply worked, and data is what it carried, which may be empty; "failed" is
 * the last reply did not work, with its error, and data is whatever the last good reply carried
 * (none when the first reply failed). loading is true while a request is on its way, also on a
 * reload, when data and error are still the last reply's.
 */
export type Loaded<T> = LoadState<T> & { reload(): void };

type LoadState<T> = { loading: boolean } & (
  | { phase: "loading"; data?: undefined; error?: undefined }
  | { phase: "ready"; data?: T; error?: undefined }
  | { phase: "failed"; data?: T; error: string }
);

/** Loads one management API reply, again on reload(). A failed reload keeps the last good data. */
export function useLoad<T>(load: () => Promise<Reply<T>>): Loaded<T> {
  const { unauthorized } = useKit();
  const [state, setState] = useState<LoadState<T>>({ phase: "loading", loading: true });
  const [n, setN] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    let live = true;
    setState((s) => ({ ...s, loading: true }));
    loadRef.current().then((r) => {
      if (!live) return;
      if (r.status === 401) return unauthorized();
      const error = problem(r);
      setState((s) =>
        error === undefined
          ? { phase: "ready", data: r.body, loading: false }
          : { phase: "failed", data: s.data, error, loading: false },
      );
    });
    return () => {
      live = false;
    };
  }, [n, unauthorized]);
  return { ...state, reload: useCallback(() => setN((x) => x + 1), []) };
}

/** Runs a management call from a button: busy while it runs, then its error or done(). */
export function useAction(done: () => void) {
  const { unauthorized } = useKit();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const run = async (call: () => Promise<Reply<unknown>>) => {
    setBusy(true);
    setError(undefined);
    const r = await call();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const p = problem(r);
    if (p) setError(p);
    else done();
  };
  return { busy, error, run };
}

type Variant = "primary" | "quiet" | "danger";

/** How long a Copy button says it copied. */
const COPIED_MS = 2000;

/**
 * Copies value for a Copy button. state is "copied" for about two seconds after a copy, so the
 * next press shows again; a press within that time copies again and restarts the wait. It is
 * "failed" when the browser refused, until the next press or another value.
 */
export function useCopy(value: string): { state?: "copied" | "failed"; copy: () => Promise<void> } {
  const [state, setState] = useState<"copied" | "failed">();
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  useEffect(() => {
    setState(undefined);
    return () => clearTimeout(timer.current);
  }, [value]);
  const copy = useCallback(async () => {
    let next: "copied" | "failed" = "copied";
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      next = "failed";
    }
    // The button may be gone by the time the browser answers: no timer is left behind it.
    if (!live.current) return;
    clearTimeout(timer.current);
    setState(next);
    if (next === "copied") timer.current = setTimeout(() => setState(undefined), COPIED_MS);
  }, [value]);
  return { state, copy };
}

export function Button(props: {
  variant?: Variant;
  onClick?: () => void;
  disabled?: boolean;
  type?: "button" | "submit";
  label?: string;
  /** Names the control for restoring focus after a re-render, when its label may not be unique. */
  focusKey?: string;
  /** For a component that moves focus to this button. */
  buttonRef?: Ref<HTMLButtonElement>;
  children: ComponentChildren;
}) {
  return (
    <button
      ref={props.buttonRef}
      type={props.type ?? "button"}
      class="btn"
      data-part="button"
      data-slot="button"
      data-variant={props.variant ?? "quiet"}
      data-focus-key={props.focusKey}
      onClick={props.onClick}
      disabled={props.disabled}
      aria-label={props.label}
    >
      {props.children}
    </button>
  );
}

/** Lower-cases a button label for confirmation text while keeping all-capital words (such as CLI). */
export function confirmActionLabel(label: string): string {
  return label.replace(/\b[A-Za-z]+\b/g, (word) =>
    word.length > 1 && word === word.toUpperCase() ? word : word.toLowerCase(),
  );
}

/** A button that asks once more before it acts. */
export function ConfirmButton(props: { onConfirm: () => void; disabled?: boolean; children: string }) {
  const [asking, setAsking] = useState(false);
  const ask = useRef<HTMLButtonElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  // The button in focus is replaced when the question opens or closes: focus goes to the button
  // that takes its place (Cancel, the safe answer, or the button that asked), not to the page.
  const moved = useRef(false);
  useEffect(() => {
    if (!moved.current) return;
    (asking ? cancel : ask).current?.focus();
  }, [asking]);
  if (!asking) {
    return (
      <Button
        variant="danger"
        disabled={props.disabled}
        buttonRef={ask}
        onClick={() => {
          moved.current = true;
          setAsking(true);
        }}
      >
        {props.children}
      </Button>
    );
  }
  return (
    <span class="confirm">
      <Button
        variant="danger"
        disabled={props.disabled}
        onClick={() => {
          setAsking(false);
          moved.current = false;
          props.onConfirm();
        }}
      >
        {t("confirm", { action: confirmActionLabel(props.children) })}
      </Button>
      <Button buttonRef={cancel} onClick={() => setAsking(false)}>
        {t("cancel")}
      </Button>
    </span>
  );
}

export type Tone = "ok" | "warn" | "danger" | "muted";

export function Badge(props: { tone: Tone; children: ComponentChildren }) {
  return (
    <span class="badge" data-part="badge" data-tone={props.tone}>
      {props.children}
    </span>
  );
}

/**
 * A state from core as a badge, in plain words (states.ts). The value core sent stays on the
 * badge, as its title and data-state, for support. `of` says what the state belongs to.
 */
export function StateBadge(props: { state: string; of?: StateOf }) {
  return (
    <span
      class="badge"
      data-part="badge"
      data-tone={stateTone(props.state, props.of)}
      data-state={props.state}
      title={props.state}
    >
      {stateText(props.state, props.of)}
    </span>
  );
}

export function Card(props: { title?: string; actions?: ComponentChildren; children: ComponentChildren }) {
  return (
    <section class="card" data-part="card" aria-label={props.title}>
      {(props.title || props.actions) && (
        <header class="card-head">
          {props.title && <h2>{props.title}</h2>}
          {props.actions && <div class="actions">{props.actions}</div>}
        </header>
      )}
      {props.children}
    </section>
  );
}

export function PageHead(props: { title: string; actions?: ComponentChildren }) {
  return (
    <header class="page-head">
      <h1 data-part="page-title">{props.title}</h1>
      {props.actions && <div class="actions">{props.actions}</div>}
    </header>
  );
}

export function Notice(props: { tone: Tone; children: ComponentChildren }) {
  return (
    <p class="notice" data-tone={props.tone} role={props.tone === "danger" ? "alert" : "status"}>
      {props.children}
    </p>
  );
}

export function Empty(props: { children: ComponentChildren }) {
  return <p class="empty">{props.children}</p>;
}

/** Facts as a definition list. */
export function Facts(props: { rows: [string, ComponentChildren][] }) {
  return (
    <dl class="facts">
      {props.rows
        .filter(([, v]) => v !== undefined && v !== null && v !== "")
        .map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
    </dl>
  );
}

export interface Column<T> {
  label: string;
  value: (item: T) => ComponentChildren;
  mono?: boolean;
}

/**
 * A list of items, as table rows or cards by the `list` layout option. The first column names
 * the item; selecting it opens its details.
 */
export function List<T>(props: {
  label: string;
  items: T[];
  id: (item: T) => string;
  columns: Column<T>[];
  selected?: string;
  onSelect?: (id: string) => void;
}) {
  const { list } = useLayout();
  const [first, ...rest] = props.columns;
  if (!first) return null;
  const name = (item: T) =>
    props.onSelect ? (
      <button type="button" class="item-name" onClick={() => props.onSelect?.(props.id(item))}>
        {first.value(item)}
      </button>
    ) : (
      <span class="item-name">{first.value(item)}</span>
    );
  const select = (item: T) => (props.onSelect ? () => props.onSelect?.(props.id(item)) : undefined);
  const current = (item: T) => (props.selected === props.id(item) ? "true" : undefined);

  if (list === "cards") {
    return (
      <ul class="cards" data-part="list" aria-label={props.label}>
        {props.items.map((item) => (
          // biome-ignore lint/a11y/useKeyWithClickEvents: the item's name is a button; a click elsewhere on the card is a shortcut for pointers
          <li
            key={props.id(item)}
            class="item"
            data-part="item"
            aria-current={current(item)}
            onClick={select(item)}
            data-clickable={props.onSelect ? "" : undefined}
          >
            <div class={first.mono ? "mono" : undefined}>{name(item)}</div>
            <dl class="facts">
              {rest.map((c) => (
                <div key={c.label}>
                  <dt>{c.label}</dt>
                  <dd class={c.mono ? "mono" : undefined}>{c.value(item)}</dd>
                </div>
              ))}
            </dl>
          </li>
        ))}
      </ul>
    );
  }
  return (
    <div class="table-wrap">
      <table class="table" data-part="list" aria-label={props.label}>
        <thead>
          <tr>
            {props.columns.map((c) => (
              <th key={c.label} scope="col">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {props.items.map((item) => (
            <tr
              key={props.id(item)}
              class="item"
              data-part="item"
              aria-current={current(item)}
              onClick={select(item)}
              data-clickable={props.onSelect ? "" : undefined}
            >
              <th scope="row" class={first.mono ? "mono" : undefined}>
                {name(item)}
              </th>
              {rest.map((c) => (
                <td key={c.label} class={c.mono ? "mono" : undefined}>
                  {c.value(item)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** A list with the selected item's details beside or below it, by the `detail` layout option. */
export function MasterDetail(props: { list: ComponentChildren; detail?: ComponentChildren }) {
  return (
    <div class="master" data-open={props.detail ? "" : undefined}>
      <div class="master-list">{props.list}</div>
      {props.detail}
    </div>
  );
}

export function Detail(props: { title: string; onClose: () => void; children: ComponentChildren }) {
  const ref = useRef<HTMLElement>(null);
  // The control that opened the detail (or switched it to another item), for focus to return to
  // when the detail closes.
  const opener = useRef<HTMLElement | null>(null);
  // A layout effect: it runs in the same task as the click that opened the detail, before the
  // person can move focus on.
  useLayoutEffect(() => {
    const from = document.activeElement;
    if (from instanceof HTMLElement && from !== document.body && !ref.current?.contains(from)) {
      opener.current = from;
    }
    ref.current?.focus();
  }, [props.title]);
  // A layout effect too: a passive one has not run yet when the detail closes straight away, and
  // then its cleanup is never set.
  useLayoutEffect(
    () => () => {
      // Closing with focus inside the detail (or lost) puts it back on the opener, when that
      // is still on the page; focus the person moved elsewhere stays there.
      const at = document.activeElement;
      const here = at === document.body || at === null || ref.current?.contains(at);
      if (here && opener.current?.isConnected) opener.current.focus();
    },
    [],
  );
  return (
    <aside
      class="detail"
      data-part="detail"
      aria-label={t("details", { title: props.title })}
      tabIndex={-1}
      ref={ref}
      onKeyDown={(e) => {
        // Escape closes the detail, unless it belongs to a dialog or a field inside it.
        if (e.key !== "Escape" || e.defaultPrevented) return;
        if (e.target instanceof Element && e.target.closest("dialog")) return;
        props.onClose();
      }}
    >
      <header class="card-head">
        <h2 class="mono">{props.title}</h2>
        <Button label={t("closeDetails")} onClick={props.onClose}>
          ✕
        </Button>
      </header>
      {props.children}
    </aside>
  );
}

export interface Choice<V extends string> {
  value: V;
  label: string;
  note?: string;
}

/** One choice out of a few, as a row of radio buttons. */
export function Choices<V extends string>(props: {
  label: string;
  value: V | undefined;
  choices: Choice<V>[];
  onChange: (v: V) => void;
}) {
  const id = useId();
  return (
    <div class="field choices" data-part="field" role="radiogroup" aria-labelledby={`${id}-l`}>
      <span class="label" id={`${id}-l`}>
        {props.label}
      </span>
      <div class="segments">
        {props.choices.map((c) => (
          <label key={c.value} class="segment">
            <input
              type="radio"
              name={id}
              value={c.value}
              checked={props.value === c.value}
              onChange={() => props.onChange(c.value)}
            />
            <span>
              {c.label}
              {c.note && <small> {c.note}</small>}
            </span>
          </label>
        ))}
      </div>
    </div>
  );
}

export function Switch(props: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label class="field switch" data-part="field">
      <input
        type="checkbox"
        role="switch"
        aria-checked={props.checked}
        checked={props.checked}
        onChange={(e) => props.onChange(e.currentTarget.checked)}
      />
      <span class="track" data-slot="switch" aria-hidden="true" />
      <span>
        <span class="label">{props.label}</span>
        {props.hint && <small class="hint">{props.hint}</small>}
      </span>
    </label>
  );
}
