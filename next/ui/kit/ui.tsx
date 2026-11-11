// The kit's components. Each carries the data-part hook themes/README.md promises; class names
// are internal.

import { type ComponentChildren, createContext } from "preact";
import { useCallback, useContext, useEffect, useId, useRef, useState } from "preact/hooks";
import { type Api, problem } from "./api";
import type { Host, Reply } from "./host";
import { strings } from "./strings";
import type { Layout } from "./theme/contract";
import type { ThemeState } from "./theme/state";

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

export interface Loaded<T> {
  data?: T;
  error?: string;
  loading: boolean;
  reload(): void;
}

/** Loads one management API reply, again on reload(). */
export function useLoad<T>(load: () => Promise<Reply<T>>): Loaded<T> {
  const { unauthorized } = useKit();
  const [state, setState] = useState<{ data?: T; error?: string; loading: boolean }>({ loading: true });
  const [n, setN] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    let live = true;
    setState((s) => ({ ...s, loading: true }));
    loadRef.current().then((r) => {
      if (!live) return;
      if (r.status === 401) return unauthorized();
      setState({ data: r.body, error: problem(r), loading: false });
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

export function Button(props: {
  variant?: Variant;
  onClick?: () => void;
  disabled?: boolean;
  type?: "button" | "submit";
  label?: string;
  children: ComponentChildren;
}) {
  return (
    <button
      type={props.type ?? "button"}
      class="btn"
      data-part="button"
      data-variant={props.variant ?? "quiet"}
      onClick={props.onClick}
      disabled={props.disabled}
      aria-label={props.label}
    >
      {props.children}
    </button>
  );
}

/** A button that asks once more before it acts. */
export function ConfirmButton(props: { onConfirm: () => void; disabled?: boolean; children: string }) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <Button variant="danger" disabled={props.disabled} onClick={() => setAsking(true)}>
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
          props.onConfirm();
        }}
      >
        {t("confirm", { action: props.children.toLowerCase() })}
      </Button>
      <Button onClick={() => setAsking(false)}>{t("cancel")}</Button>
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

const TONES: Record<string, Tone> = {
  active: "ok",
  idle: "muted",
  starting: "warn",
  draining: "warn",
  stopped: "muted",
  disabled: "muted",
  crashed: "danger",
  quarantined: "danger",
  failed: "danger",
  reauth_required: "warn",
};

export function StateBadge(props: { state: string }) {
  return <Badge tone={TONES[props.state] ?? "muted"}>{props.state}</Badge>;
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
  useEffect(() => {
    ref.current?.focus();
  }, [props.title]);
  return (
    <aside
      class="detail"
      data-part="detail"
      aria-label={t("details", { title: props.title })}
      tabIndex={-1}
      ref={ref}
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
      <span class="track" aria-hidden="true" />
      <span>
        <span class="label">{props.label}</span>
        {props.hint && <small class="hint">{props.hint}</small>}
      </span>
    </label>
  );
}

export function when(t?: string | number): string | undefined {
  if (t === undefined || t === 0 || t === "") return undefined;
  const d = typeof t === "number" ? new Date(t * 1000) : new Date(t);
  return Number.isNaN(d.getTime()) ? undefined : d.toLocaleString();
}
