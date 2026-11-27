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
  clearFilters: "Clear filters",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Not copied",
  copyTrace: "Copy trace ID {id}",
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
  /**
   * The Setup page says whether core is on its first run: the shell shows Setup in the focused
   * window then, and in the frame afterwards. A host without the shell leaves it out.
   */
  setupFirstRun?(first: boolean): void;
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

/**
 * Calls tick every ms while active and the document is visible. Never overlaps a call whose
 * promise is still running, and calls once straight away when the document becomes visible after
 * longer than ms.
 */
export function usePoll(tick: () => unknown, ms: number, active = true): void {
  const tickRef = useRef(tick);
  tickRef.current = tick;
  useEffect(() => {
    if (!active || ms <= 0) return;
    let live = true;
    let running = false;
    let last = Date.now();
    const run = () => {
      if (!live || running || document.visibilityState !== "visible") return;
      last = Date.now();
      running = true;
      const done = () => {
        running = false;
      };
      try {
        const result = tickRef.current();
        if (result && typeof (result as PromiseLike<unknown>).then === "function") {
          (result as PromiseLike<unknown>).then(done, done);
        } else {
          running = false;
        }
      } catch (err) {
        running = false;
        throw err;
      }
    };
    let timer = setInterval(run, ms);
    const shown = () => {
      if (document.visibilityState === "visible" && !running && Date.now() - last >= ms) {
        clearInterval(timer);
        timer = setInterval(run, ms);
        run();
      }
    };
    document.addEventListener("visibilitychange", shown);
    return () => {
      live = false;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", shown);
    };
  }, [ms, active]);
}

/**
 * Runs a management call from a button: busy while it runs, then its error or done(). A second
 * run while one is on its way does nothing. A Button whose onClick returns run()'s promise shows
 * that it is working until the call ends.
 */
export function useAction(done: () => void) {
  const { unauthorized } = useKit();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const running = useRef(false);
  const run = async (call: () => Promise<Reply<unknown>>) => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    setError(undefined);
    const r = await call();
    running.current = false;
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const p = problem(r);
    if (p) setError(p);
    else done();
  };
  return { busy, error, run };
}

/**
 * A button's weight: primary is a view's one main action (filled), quiet the ordinary button
 * (outlined), ghost a button with no border until hovered, danger an outlined button in the
 * danger colour.
 */
type Variant = "primary" | "quiet" | "ghost" | "danger";

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

/** How long a button works before it shows so: a call that answers sooner changes nothing. */
export const BUSY_SHOWS_AFTER_MS = 150;

/**
 * A button. While it works (busy, or until the promise its onClick returned ends) it keeps its
 * label and its width, ignores presses, says so to assistive technology (aria-busy), and after
 * a moment shows a quiet mark (data-busy). A page may disable it meanwhile as before; it is
 * then not dimmed like the buttons that only wait for it.
 */
export function Button(props: {
  variant?: Variant;
  /** A promise it returns keeps the button busy until it ends. */
  onClick?: () => unknown;
  /** The button's action is running. */
  busy?: boolean;
  disabled?: boolean;
  type?: "button" | "submit";
  label?: string;
  /** Names the control for restoring focus after a re-render, when its label may not be unique. */
  focusKey?: string;
  /** For a component that moves focus to this button. */
  buttonRef?: Ref<HTMLButtonElement>;
  /** The id of what describes the button, such as the question it answers. */
  describedBy?: string;
  children: ComponentChildren;
}) {
  const [running, setRunning] = useState(false);
  const [marked, setMarked] = useState(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const busy = !!props.busy || running;
  useEffect(() => {
    if (!busy) return setMarked(false);
    const timer = setTimeout(() => setMarked(true), BUSY_SHOWS_AFTER_MS);
    return () => clearTimeout(timer);
  }, [busy]);
  const press = (e: Event) => {
    // A busy submit button must not send its form again either.
    if (busy) return e.preventDefault();
    const result = props.onClick?.();
    if (!(result instanceof Promise)) return;
    setRunning(true);
    const ended = () => {
      if (live.current) setRunning(false);
    };
    result.then(ended, ended);
  };
  return (
    <button
      ref={props.buttonRef}
      type={props.type ?? "button"}
      class="btn"
      data-part="button"
      data-slot="button"
      data-variant={props.variant ?? "quiet"}
      data-focus-key={props.focusKey}
      onClick={press}
      disabled={props.disabled}
      aria-busy={busy ? "true" : undefined}
      data-busy={busy && marked ? "" : undefined}
      aria-label={props.label}
      aria-describedby={props.describedBy}
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

/**
 * A button that asks once more before it acts. question, when given, is shown beside the
 * answers while it asks ("Delete the key Claude Code? Tools using it stop working."), and
 * describes both, so a screen reader says it with the answer in focus. The button that asks is
 * danger unless variant says otherwise (signing out is not a loss, so it asks quietly); the
 * answer that goes ahead stays danger. confirmLabel is that answer's text when "Yes, {action}"
 * does not say enough (it names what is removed), and label the asking button's accessible name
 * when several buttons on a page read the same.
 */
export function ConfirmButton(props: {
  /** A promise it returns keeps the button busy until it ends. */
  onConfirm: () => unknown;
  disabled?: boolean;
  question?: string;
  variant?: Variant;
  confirmLabel?: string;
  label?: string;
  children: string;
}) {
  const [asking, setAsking] = useState(false);
  const [working, setWorking] = useState(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const ask = useRef<HTMLButtonElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const questionId = useId();
  const describedBy = props.question ? questionId : undefined;
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
        variant={props.variant ?? "danger"}
        disabled={props.disabled}
        busy={working}
        label={props.label}
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
      {props.question && (
        <span class="confirm-question" id={questionId}>
          {props.question}
        </span>
      )}
      <Button
        variant="danger"
        disabled={props.disabled}
        describedBy={describedBy}
        onClick={() => {
          setAsking(false);
          // The asking button comes back (working, for a promise): focus goes to it. A confirm
          // that removes the button leaves nothing to focus.
          const result = props.onConfirm();
          if (!(result instanceof Promise)) return;
          setWorking(true);
          const ended = () => {
            if (live.current) setWorking(false);
          };
          result.then(ended, ended);
        }}
      >
        {props.confirmLabel ?? t("confirm", { action: confirmActionLabel(props.children) })}
      </Button>
      <Button buttonRef={cancel} describedBy={describedBy} onClick={() => setAsking(false)}>
        {t("cancel")}
      </Button>
    </span>
  );
}

/** A meaning: ok, warn and danger are states; info is a neutral notice; update says an update is ready. */
export type Tone = "ok" | "warn" | "danger" | "muted" | "info" | "update";

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
/**
 * A state in plain words. sent is the state core sent, kept in data-state and the title for
 * support, when the words come from another state (an account's condition); else state.
 */
export function StateBadge(props: { state: string; of?: StateOf; sent?: string }) {
  const sent = props.sent ?? props.state;
  return (
    <span
      class="badge"
      data-part="badge"
      data-tone={stateTone(props.state, props.of)}
      data-state={sent}
      title={sent}
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

/**
 * Where a page's head goes when the page is shown in the frame (frame.tsx): the frame's header
 * strip draws the title and the actions. undefined takes either back.
 */
export interface HeadSlot {
  setTitle(title: string | undefined): void;
  setActions(actions: ComponentChildren): void;
}

export const HeadContext = createContext<HeadSlot | undefined>(undefined);

/**
 * A page's title and its main actions. In the frame they are drawn in the header strip, which
 * holds the page's one h1; a page shown outside a frame draws them itself, in place.
 */
export function PageHead(props: { title: string; actions?: ComponentChildren }) {
  const slot = useContext(HeadContext);
  useLayoutEffect(() => {
    if (!slot) return;
    slot.setTitle(props.title);
    return () => slot.setTitle(undefined);
  }, [slot, props.title]);
  // After every render: the actions are drawn elsewhere, with what this render gave them.
  useLayoutEffect(() => slot?.setActions(props.actions));
  useLayoutEffect(() => () => slot?.setActions(undefined), [slot]);
  if (slot) return null;
  return (
    <header class="page-head">
      <h1 data-part="page-title">{props.title}</h1>
      {props.actions && <div class="actions">{props.actions}</div>}
    </header>
  );
}

/** Draws the heads inside it in place, not in the frame's header strip. */
export function InlineHeads(props: { children: ComponentChildren }) {
  return <HeadContext.Provider value={undefined}>{props.children}</HeadContext.Provider>;
}

export function Notice(props: { tone: Tone; children: ComponentChildren }) {
  return (
    <p class="notice" data-tone={props.tone} role={props.tone === "danger" ? "alert" : "status"}>
      {props.children}
    </p>
  );
}

/**
 * What a list shows when it has nothing: what would appear here, and the way to get it there.
 * action is that way, a link or a button. onClear makes it the "nothing matches this filter"
 * kind, with a button that clears the filters. A page shows it only for a list it has read: not
 * while loading, and not beside the notice of a load that failed.
 */
export function Empty(props: {
  children: ComponentChildren;
  action?: ComponentChildren;
  onClear?: () => void;
  /** The clear button's label, when "Clear filters" does not fit. */
  clearLabel?: string;
}) {
  return (
    <div class="empty" data-part="empty" data-variant={props.onClear ? "filter" : "none"}>
      <p>{props.children}</p>
      {(props.action || props.onClear) && (
        <div class="actions">
          {props.action}
          {props.onClear && <Button onClick={props.onClear}>{props.clearLabel ?? t("clearFilters")}</Button>}
        </div>
      )}
    </div>
  );
}

/** A small button that copies a trace ID. A press does not select the row it is in. */
export function CopyTrace(props: { id: string }) {
  const { state, copy } = useCopy(props.id);
  return (
    <button
      type="button"
      class="btn"
      data-part="button"
      data-slot="button"
      data-variant="ghost"
      data-size="small"
      aria-label={t("copyTrace", { id: props.id })}
      onClick={(e) => {
        e.stopPropagation();
        void copy();
      }}
    >
      <span aria-live="polite">
        {state === "copied" ? t("copied") : state === "failed" ? t("copyFailed") : t("copy")}
      </span>
    </button>
  );
}

/** A request's trace ID with its copy button; with href, the ID links there. */
export function TraceId(props: { id: string; href?: string }) {
  return (
    <span class="trace-id" data-part="trace-id">
      {props.href ? <a href={props.href}>{props.id}</a> : <span class="mono">{props.id}</span>}
      <CopyTrace id={props.id} />
    </span>
  );
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
  /** A column of numbers: a table sets them against the end edge, so their digits line up. */
  numeric?: boolean;
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
  const cellClass = (c: Column<T>) =>
    [c.mono && "mono", c.numeric && "num"].filter(Boolean).join(" ") || undefined;

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
    <TableFrame>
      <table class="table" data-part="list" aria-label={props.label}>
        <thead>
          <tr>
            {props.columns.map((c) => (
              <th key={c.label} scope="col" class={c.numeric ? "num" : undefined}>
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
                <td key={c.label} class={cellClass(c)}>
                  {c.value(item)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </TableFrame>
  );
}

/** Which sides of a box that scrolls sideways have more to see. */
export function overflowSides(box: {
  scrollLeft: number;
  scrollWidth: number;
  clientWidth: number;
}): "start" | "end" | "both" | undefined {
  // In a right-to-left page scrollLeft runs from 0 down to a negative number.
  const before = Math.abs(box.scrollLeft) > 1;
  const after = box.scrollWidth - box.clientWidth - Math.abs(box.scrollLeft) > 1;
  return before && after ? "both" : before ? "start" : after ? "end" : undefined;
}

/**
 * The box a table scrolls in. When the table is wider than the box, data-more on the frame
 * names the side (or sides) with columns out of view, and the frame fades that side out.
 */
export function TableFrame(props: { children: ComponentChildren }) {
  const wrap = useRef<HTMLDivElement>(null);
  const [more, setMore] = useState<ReturnType<typeof overflowSides>>();
  useLayoutEffect(() => {
    const box = wrap.current;
    if (!box) return;
    const update = () => setMore(overflowSides(box));
    update();
    box.addEventListener("scroll", update, { passive: true });
    const seen = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(update);
    seen?.observe(box);
    if (box.firstElementChild) seen?.observe(box.firstElementChild);
    return () => {
      box.removeEventListener("scroll", update);
      seen?.disconnect();
    };
  }, []);
  return (
    <div class="table-frame" data-part="list-frame" data-more={more}>
      <div class="table-wrap" ref={wrap}>
        {props.children}
      </div>
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
        <Button variant="ghost" label={t("closeDetails")} onClick={props.onClose}>
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
  /** Every choice shown but none can be picked, such as while what it chooses is in use. */
  disabled?: boolean;
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
              disabled={props.disabled}
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
