// The few parts direction B's screens are made of. They are deliberately plain: text, a rule or
// two, one kind of gauge and one kind of bar. Grouping is done by space and weight, not boxes.

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { QuotaLimit, UsageBucket } from "../../api";
import type { Windows } from "./data";
import { Icon, type IconName } from "./icons";
import { type Condition, duration, type Group, num, usedPercent, windowName } from "./lib";

type Kind = "primary" | "plain" | "quiet" | "danger";

/** A button, or a link that looks like one when href is given. One "primary" per screen. */
export function Btn(props: {
  kind?: Kind;
  onClick?: () => void;
  href?: string;
  disabled?: boolean;
  type?: "button" | "submit";
  icon?: IconName;
  /** The accessible name, when the visible text alone does not say what it acts on. */
  label?: string;
  children: ComponentChildren;
}) {
  const body = (
    <>
      {props.icon && <Icon name={props.icon} />}
      <span>{props.children}</span>
    </>
  );
  if (props.href !== undefined) {
    return (
      <a class="db-btn" data-kind={props.kind ?? "plain"} href={props.href} aria-label={props.label}>
        {body}
      </a>
    );
  }
  return (
    <button
      type={props.type ?? "button"}
      class="db-btn"
      data-kind={props.kind ?? "plain"}
      onClick={props.onClick}
      disabled={props.disabled}
      aria-label={props.label}
    >
      {body}
    </button>
  );
}

/** A link that goes somewhere else in the panel, with an arrow after it. */
export function Onward(props: { href: string; children: ComponentChildren }) {
  return (
    <a class="db-onward" href={props.href}>
      {props.children}
      <Icon name="arrowRight" />
    </a>
  );
}

const GROUP_ICON: Record<Group, IconName> = {
  needs: "needsYou",
  waiting: "waiting",
  serving: "serving",
  off: "off",
};

/** The mark before an account: a shape per group, in the group's state colour. */
export function StateMark(props: { c: Condition; label?: string }) {
  // An account that serves but rests on a model is marked as waiting: something is held back.
  const group = props.c.group === "serving" && props.c.resting.length > 0 ? "waiting" : props.c.group;
  return (
    <span class="db-mark-state" data-group={group}>
      <Icon name={GROUP_ICON[group]} label={props.label} />
    </span>
  );
}

/** A section with a small heading. aside sits on the heading's line, at its end. */
export function Section(props: {
  title: string;
  aside?: ComponentChildren;
  class?: string;
  children: ComponentChildren;
}) {
  return (
    <section class={`db-section ${props.class ?? ""}`} aria-label={props.title}>
      <header class="db-section-head">
        <h2>{props.title}</h2>
        {props.aside && <div class="db-section-aside">{props.aside}</div>}
      </header>
      {props.children}
    </section>
  );
}

/** What a screen says while its first reply is on the way: that it is asking, in its own words. */
export function Asking(props: { children: ComponentChildren; lines?: number }) {
  return (
    <div class="db-asking" role="status">
      <p>{props.children}</p>
      <div class="db-ghost" aria-hidden="true">
        {Array.from({ length: props.lines ?? 3 }, (_, i) => (
          <i key={i} />
        ))}
      </div>
    </div>
  );
}

/** Something went wrong: what, and the one way to try again. */
export function Trouble(props: { children: ComponentChildren; retry?: () => void; quiet?: boolean }) {
  return (
    <div class="db-trouble" data-quiet={props.quiet ? "" : undefined} role="alert">
      <Icon name="needsYou" />
      <p>
        {props.children}
        {props.retry && (
          <>
            {" "}
            <button type="button" class="db-linkish" onClick={props.retry}>
              Try again
            </button>
          </>
        )}
      </p>
    </div>
  );
}

/** A note in the text's own voice: not an error, but worth a line. */
export function Note(props: { tone?: "plain" | "warn"; children: ComponentChildren }) {
  return (
    <p class="db-note" data-tone={props.tone ?? "plain"} role="status">
      {props.children}
    </p>
  );
}

/** An action that asks once more before it acts, in the line it sits in. */
export function Confirm(props: {
  kind?: Kind;
  /** The action cannot be undone: the button that does it is drawn as dangerous. */
  danger?: boolean;
  ask: string;
  yes: string;
  disabled?: boolean;
  onConfirm: () => void;
  label?: string;
  children: ComponentChildren;
}) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <Btn
        kind={props.kind ?? "quiet"}
        disabled={props.disabled}
        onClick={() => setAsking(true)}
        label={props.label}
      >
        {props.children}
      </Btn>
    );
  }
  return (
    <span class="db-confirm">
      <span class="db-confirm-ask">{props.ask}</span>
      <Btn
        kind={props.danger ? "danger" : "plain"}
        disabled={props.disabled}
        onClick={() => {
          setAsking(false);
          props.onConfirm();
        }}
      >
        {props.yes}
      </Btn>
      <Btn kind="quiet" onClick={() => setAsking(false)}>
        Cancel
      </Btn>
    </span>
  );
}

/** Words that pick one of a few views, as a row of links-in-waiting: the chosen one is underlined. */
export function Words<V extends string>(props: {
  label: string;
  value: V;
  options: { value: V; label: string }[];
  onChange: (v: V) => void;
}) {
  return (
    <fieldset class="db-words">
      <legend class="db-sr">{props.label}</legend>
      {props.options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === props.value}
          onClick={() => props.onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </fieldset>
  );
}

// ---- usage windows ----------------------------------------------------------------------------

/** How much of a window is left, as a tone: plenty, nearly used up, used up. */
export function leftTone(used: number): "plenty" | "low" | "out" {
  if (used >= 100) return "out";
  return used >= 90 ? "low" : "plenty";
}

/** A gauge of what is left of a window: full is plenty, empty is used up. */
export function Gauge(props: { used: number; label: string }) {
  const left = Math.round(100 - props.used);
  return (
    // biome-ignore lint/a11y/useSemanticElements: a styled fill reads the same in every browser the panel runs in
    <div
      class="db-gauge"
      data-left={leftTone(props.used)}
      role="meter"
      aria-label={props.label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={left}
      aria-valuetext={`${left}% remaining`}
    >
      <span style={{ width: `${Math.max(0, 100 - props.used)}%` }} />
    </div>
  );
}

/** When a window starts again, in the UI terms' words. */
export function resets(limit: QuotaLimit, now: number): string | undefined {
  if (!limit.resets_at) return undefined;
  const ms = limit.resets_at * 1000 - now;
  if (ms <= 0) return "Reset available";
  // A reset years away is a provider's placeholder, not a time worth counting down to.
  if (ms > 366 * 86_400_000) return undefined;
  return `Resets in ${duration(ms)}`;
}

/** One usage window on a line: its name, the gauge, what is left and when it resets. */
export function WindowLine(props: { limit: QuotaLimit; now: number; account: string }) {
  const { limit } = props;
  const name = windowName(limit);
  const used = usedPercent(limit);
  const left = used === undefined ? undefined : Math.round(100 - used);
  const amount = (n: number) => `${num(n)}${limit.unit ? ` ${limit.unit}` : ""}`;
  return (
    <div class="db-window" data-left={used === undefined ? undefined : leftTone(used)}>
      <span class="db-window-name">{name}</span>
      {used !== undefined ? (
        <Gauge used={used} label={`${props.account}, ${name}`} />
      ) : (
        <span class="db-gauge" data-left="none" aria-hidden="true" />
      )}
      <span class="db-window-left">
        {limit.unlimited
          ? "Unlimited"
          : left !== undefined
            ? left <= 0
              ? "Used up"
              : `${left}% remaining`
            : limit.used !== undefined && limit.limit !== undefined
              ? `Used ${amount(limit.used)} / ${amount(limit.limit)}`
              : limit.remaining !== undefined
                ? `${amount(limit.remaining)} remaining`
                : "Not reported"}
      </span>
      <span class="db-window-resets">{resets(limit, props.now) ?? ""}</span>
    </div>
  );
}

/** An account's windows as lines, or what is known in their place. */
export function WindowLines(props: { w: Windows | undefined; now: number; account: string; max?: number }) {
  const w = props.w;
  if (!w) return <p class="db-window-none">Not read: the account is not taking requests.</p>;
  if (w.state === "reading") return <p class="db-window-none">Asking the provider…</p>;
  if (w.state === "failed")
    return <p class="db-window-none">The provider did not say{w.error ? `: ${w.error}` : "."}</p>;
  if (w.state === "none") return <p class="db-window-none">No limits reported.</p>;
  const limits = props.max ? w.limits.slice(0, props.max) : w.limits;
  return (
    <div class="db-windows">
      {limits.map((l) => (
        <WindowLine key={l.id} limit={l} now={props.now} account={props.account} />
      ))}
    </div>
  );
}

/**
 * An account's windows in one line: the window it is closest to using up, as a gauge and words,
 * or what is known in its place.
 */
export function WindowBrief(props: { w: Windows | undefined; now: number; account: string }) {
  const w = props.w;
  const tight = tightest(w);
  const used = tight ? usedPercent(tight) : undefined;
  if (!tight || used === undefined) {
    return (
      <span class="db-brief db-brief-none">
        {!w
          ? ""
          : w.state === "reading"
            ? "Asking the provider…"
            : w.state === "failed"
              ? "The provider did not say"
              : "No limits reported"}
      </span>
    );
  }
  const name = windowName(tight);
  const reset = resets(tight, props.now);
  return (
    <span class="db-brief" data-left={leftTone(used)}>
      <Gauge used={used} label={`${props.account}, ${name}`} />
      <span class="db-brief-left">
        {name}: {used >= 100 ? "used up" : `${Math.round(100 - used)}% remaining`}
      </span>
      <span class="db-brief-resets">{reset ?? ""}</span>
    </span>
  );
}

/** The window an account is closest to using up, for a one-line summary. */
export function tightest(w: Windows | undefined): QuotaLimit | undefined {
  if (w?.state !== "read") return undefined;
  let best: QuotaLimit | undefined;
  let most = -1;
  for (const l of w.limits) {
    const used = usedPercent(l);
    if (used !== undefined && used > most) {
      most = used;
      best = l;
    }
  }
  return best;
}

// ---- bars -------------------------------------------------------------------------------------

export interface BarItem {
  id: string;
  name: string;
  /** Said under the name, smaller. */
  note?: string;
  value: number;
  failed: number;
  /** Shown at the row's end in place of the count. */
  figure?: string;
  href?: string;
}

/**
 * A ranked list with a thin bar under each name: the bar's length is the share of the largest.
 * The part of a bar that failed is drawn apart, in the failure colour, and said in words.
 */
export function BarList(props: {
  label: string;
  items: BarItem[];
  max?: number;
  more?: string;
  /** Leaves the words about failures out where the names need the room: the bar still shows them. */
  bare?: boolean;
}) {
  const shown = props.max ? props.items.slice(0, props.max) : props.items;
  const rest = props.items.length - shown.length;
  const top = Math.max(1, ...props.items.map((i) => i.value));
  return (
    <ol class="db-bars" aria-label={props.label}>
      {shown.map((i) => {
        const ok = ((i.value - i.failed) / top) * 100;
        const bad = (i.failed / top) * 100;
        return (
          <li key={i.id}>
            <span class="db-bars-name" title={i.name}>
              {i.href ? <a href={i.href}>{i.name}</a> : i.name}
            </span>
            <span class="db-bars-figure">
              {!props.bare && (i.note || i.failed > 0) && (
                <small>
                  {i.note}
                  {i.note && i.failed > 0 ? ", " : ""}
                  {i.failed > 0 && `${num(i.failed)} failed`}
                </small>
              )}
              {i.figure ?? num(i.value)}
            </span>
            <span class="db-bars-track" aria-hidden="true">
              {ok > 0 && <i style={{ width: `${ok}%` }} />}
              {bad > 0 && <i data-failed="" style={{ width: `${bad}%` }} />}
            </span>
          </li>
        );
      })}
      {rest > 0 && <li class="db-bars-more">{props.more ?? `and ${num(rest)} more`}</li>}
    </ol>
  );
}

/**
 * Requests over time as columns, oldest first: what went through in ink, what failed above it in
 * the failure colour. One rule marks the tallest column with its count; each column says its own
 * numbers when pointed at, and the same numbers are a table for those who do not point.
 */
export function TimeBars(props: {
  buckets: UsageBucket[];
  /** The text under a column, or nothing for a column that carries no label. */
  tick: (start: Date, index: number) => string | undefined;
  /** How a column names its span in its tip and in the table. */
  span: (start: Date) => string;
  small?: boolean;
  label: string;
}) {
  const peak = Math.max(0, ...props.buckets.map((b) => b.requests));
  const [table, setTable] = useState(false);
  if (peak === 0) return <p class="db-quiet">No requests in this time.</p>;
  return (
    <figure class="db-time" data-small={props.small ? "" : undefined}>
      <div class="db-time-plot" role="img" aria-label={props.label}>
        <span class="db-time-peak" aria-hidden="true">
          {num(peak)}
        </span>
        {props.buckets.map((b, i) => {
          const start = new Date(b.start);
          const ok = b.requests - b.failure_count;
          return (
            <div class="db-time-col" key={b.start} data-empty={b.requests === 0 ? "" : undefined}>
              <div class="db-time-stack" style={{ height: `${(b.requests / peak) * 100}%` }}>
                {b.failure_count > 0 && <i data-failed="" style={{ flexGrow: b.failure_count }} />}
                {ok > 0 && <i style={{ flexGrow: ok }} />}
              </div>
              <span class="db-time-tip" aria-hidden="true">
                <b>{props.span(start)}</b>
                {num(b.requests)} {b.requests === 1 ? "request" : "requests"}
                {b.failure_count > 0 && <span class="db-failed-text">, {num(b.failure_count)} failed</span>}
              </span>
              <span class="db-time-tick" aria-hidden="true">
                {props.tick(start, i) ?? ""}
              </span>
            </div>
          );
        })}
      </div>
      {!props.small && (
        <figcaption>
          <span class="db-legend">
            <i /> went through
            <i data-failed="" /> failed
          </span>
          <button type="button" class="db-linkish" aria-expanded={table} onClick={() => setTable(!table)}>
            {table ? "Hide the numbers" : "Show the numbers"}
          </button>
        </figcaption>
      )}
      {table && (
        <div class="db-scroll">
          <table class="db-table db-time-table">
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col" class="db-n">
                  Requests
                </th>
                <th scope="col" class="db-n">
                  Failed
                </th>
                <th scope="col" class="db-n">
                  Tokens
                </th>
              </tr>
            </thead>
            <tbody>
              {props.buckets.map((b) => (
                <tr key={b.start}>
                  <th scope="row">{props.span(new Date(b.start))}</th>
                  <td class="db-n">{num(b.requests)}</td>
                  <td class="db-n">{num(b.failure_count)}</td>
                  <td class="db-n">{num(b.total_tokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </figure>
  );
}
