// Direction A's small parts: key caps, buttons, the state tag, the usage window bar, the chart,
// and what a screen shows while it loads, when it is empty and when core cannot be reached.

import type { ComponentChildren, VNode } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { QuotaLimit, UsageBucket } from "../../api";
import { Icon, type IconName } from "./icons";
import { keyCap } from "./keys";
import { type BoardState, compact, paceOf, percent, type Standing, stamp, wait, windowName } from "./lib";

/** Key caps for a binding: "g a" draws two. */
export function Kbd(props: { keys: string }) {
  return (
    <span class="da-kbd" aria-hidden="true">
      {props.keys.split(" ").map((k) => (
        <kbd key={k}>{keyCap(k)}</kbd>
      ))}
    </span>
  );
}

export function Btn(props: {
  variant?: "primary" | "plain" | "danger" | "ghost";
  icon?: IconName;
  /** The key that does the same, shown as a hint. */
  keys?: string;
  onClick?: () => void;
  href?: string;
  disabled?: boolean;
  /** The accessible name, when the button shows only an icon. */
  label?: string;
  title?: string;
  small?: boolean;
  children?: ComponentChildren;
}) {
  const cls = `da-btn${props.small ? " da-btn-small" : ""}${props.children ? "" : " da-btn-icon"}`;
  const body = (
    <>
      {props.icon && <Icon name={props.icon} size={14} />}
      {props.children && <span>{props.children}</span>}
      {props.keys && <Kbd keys={props.keys} />}
    </>
  );
  if (props.href && !props.disabled) {
    return (
      <a
        class={cls}
        data-variant={props.variant ?? "plain"}
        href={props.href}
        aria-label={props.label}
        title={props.title}
      >
        {body}
      </a>
    );
  }
  return (
    <button
      type="button"
      class={cls}
      data-variant={props.variant ?? "plain"}
      onClick={props.onClick}
      disabled={props.disabled}
      aria-label={props.label}
      title={props.title ?? props.label}
    >
      {body}
    </button>
  );
}

/**
 * A button that asks once more in place before it acts: the first press arms it, the second
 * acts, and it disarms by itself, on Escape or when focus leaves.
 */
export function ConfirmBtn(props: {
  variant?: "plain" | "danger";
  icon?: IconName;
  onConfirm: () => void;
  disabled?: boolean;
  small?: boolean;
  /** What the armed button says, such as "Clear now?". */
  ask: string;
  children: ComponentChildren;
}) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const timer = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(timer);
  }, [armed]);
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: the span only watches the button inside it, to disarm it
    <span
      class="da-confirm"
      onKeyDown={(e) => {
        if (e.key === "Escape" && armed) {
          e.stopPropagation();
          setArmed(false);
        }
      }}
      onFocusOut={() => setArmed(false)}
    >
      <Btn
        variant={armed ? "danger" : (props.variant ?? "plain")}
        icon={props.icon}
        small={props.small}
        disabled={props.disabled}
        onClick={() => {
          if (!armed) return setArmed(true);
          setArmed(false);
          props.onConfirm();
        }}
      >
        {armed ? props.ask : props.children}
      </Btn>
      <span class="da-sr" role="status">
        {armed ? "Press again to confirm." : ""}
      </span>
    </span>
  );
}

const STATE_ICON: Record<BoardState, IconName> = {
  serving: "serving",
  cooling: "cooling",
  exhausted: "exhausted",
  "sign-in": "signIn",
  banned: "banned",
  broken: "broken",
  "no-provider": "noProvider",
  off: "off",
};

/** An account's state: a shape, a word and, when it waits on a clock, the time left. Never colour alone. */
export function StateTag(props: { standing: Standing; now: number }) {
  const s = props.standing;
  return (
    <span class="da-state" data-tone={s.tone} data-state={s.state}>
      <Icon name={STATE_ICON[s.state]} size={14} />
      <span class="da-state-label">{s.label}</span>
      {s.until !== undefined && s.until > props.now && (
        <span class="da-state-until da-num" title={`until ${stamp(s.until, props.now)}`}>
          {wait(s.until - props.now)}
        </span>
      )}
    </span>
  );
}

/**
 * One usage window as a thin bar: the fill is what is used, the tick is how much of the window's
 * time has passed. A fill past the tick is being used faster than the clock and turns amber.
 */
export function WindowBar(props: { limit: QuotaLimit; now: number; wide?: boolean }) {
  const pace = paceOf(props.limit, props.now);
  const name = windowName(props.limit);
  if (!pace) return null;
  const used = percent(pace.used).replace(/\.\d%$/, "%");
  const reset = pace.resetsIn !== undefined ? wait(pace.resetsIn) : undefined;
  const label =
    `${props.limit.label ?? name}: ${used} used` +
    (reset ? `, resets in ${reset}` : "") +
    (pace.course === "ahead" ? ", ahead of the clock" : pace.course === "full" ? ", used up" : "");
  return (
    <span class={`da-window${props.wide ? " da-window-wide" : ""}`} data-course={pace.course} title={label}>
      <span class="da-window-name">{name}</span>
      {/* biome-ignore lint/a11y/useSemanticElements: the pace tick and the segmented fill need two layers a <meter> cannot draw */}
      <span
        class="da-window-bar"
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(pace.used * 100)}
      >
        <span class="da-window-fill" style={{ width: `${pace.used * 100}%` }} />
        {pace.elapsed !== undefined && (
          <span class="da-window-tick" style={{ left: `${pace.elapsed * 100}%` }} />
        )}
      </span>
      <span class="da-window-used da-num">{used}</span>
      <span class="da-window-reset da-num">{reset ?? "–"}</span>
    </span>
  );
}

/** Skeleton rows for a first load: the shape of what is coming, never zeros. */
export function Skeleton(props: { rows: number; label: string }) {
  return (
    <div class="da-skeleton" role="status" aria-label={props.label}>
      {Array.from({ length: props.rows }, (_, i) => (
        <span key={i} class="da-skeleton-row" style={{ width: `${92 - ((i * 17) % 40)}%` }} />
      ))}
    </div>
  );
}

/** A load that failed: what, why, and the way to try again. With stale data it is one line above it. */
export function Fault(props: { what: string; error: string; retry?: () => void; stale?: boolean }) {
  return (
    <div class="da-fault" role="alert" data-stale={props.stale ? "" : undefined}>
      <Icon name="warning" size={14} />
      <span class="da-fault-text">
        <strong>{props.what}</strong> {props.error}
        {props.stale && " Showing the last reading."}
      </span>
      {props.retry && (
        <Btn small onClick={props.retry} icon="refresh">
          Retry
        </Btn>
      )}
    </div>
  );
}

export function Blank(props: { title: string; children?: ComponentChildren; action?: VNode }) {
  return (
    <div class="da-blank">
      <p class="da-blank-title">{props.title}</p>
      {props.children && <p class="da-blank-text">{props.children}</p>}
      {props.action}
    </div>
  );
}

/** A thin inline bar for a share of a total, beside its number in a table. */
export function ShareBar(props: { share: number; failed?: number }) {
  return (
    <span class="da-share" aria-hidden="true">
      <span class="da-share-fill" style={{ width: `${Math.max(0, Math.min(1, props.share)) * 100}%` }}>
        {props.failed !== undefined && props.failed > 0 && (
          <span class="da-share-failed" style={{ width: `${Math.min(1, props.failed) * 100}%` }} />
        )}
      </span>
    </span>
  );
}

/**
 * Requests over time as thin bars: what succeeded in the text's ink, what failed stacked on top
 * in red, so the eye goes to where the red is. Moving over it, or the arrow keys while it has
 * focus, reads one step out in the line above.
 */
export function Bars(props: {
  series: UsageBucket[];
  bucketSeconds: number;
  height?: number;
  now: number;
  /** A line shown in place of the reading while no step is picked. */
  summary?: string;
  label: string;
}) {
  const { series } = props;
  const [at, setAt] = useState<number>();
  const ref = useRef<HTMLDivElement>(null);
  const peak = Math.max(1, ...series.map((b) => b.requests));
  const picked = at !== undefined ? series[at] : undefined;
  const pick = (clientX: number) => {
    const box = ref.current?.getBoundingClientRect();
    if (!box || series.length === 0) return;
    const i = Math.floor(((clientX - box.left) / box.width) * series.length);
    setAt(Math.max(0, Math.min(series.length - 1, i)));
  };
  const first = series[0];
  const last = series[series.length - 1];
  const reading = picked
    ? `${stamp(picked.start, props.now)}  ${compact(picked.requests)} req` +
      (picked.failure_count ? `  ${compact(picked.failure_count)} failed` : "") +
      (picked.total_tokens ? `  ${compact(picked.total_tokens)} tok` : "")
    : (props.summary ?? "");
  return (
    <div class="da-bars" data-picked={picked ? "" : undefined}>
      <div class="da-bars-head">
        <span class="da-bars-reading da-num" aria-live="off">
          {reading}
        </span>
        <span class="da-bars-peak da-num">peak {compact(peak)}</span>
      </div>
      <div
        class="da-bars-plot"
        ref={ref}
        role="img"
        // biome-ignore lint/a11y/noNoninteractiveTabindex: the arrow keys read the chart step by step
        tabIndex={0}
        aria-label={`${props.label}. Left and right arrows read one step.`}
        style={{ height: `${props.height ?? 56}px` }}
        onPointerMove={(e) => pick(e.clientX)}
        onPointerLeave={() => setAt(undefined)}
        onBlur={() => setAt(undefined)}
        onKeyDown={(e) => {
          if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
          e.preventDefault();
          const from = at ?? (e.key === "ArrowLeft" ? series.length : -1);
          setAt(Math.max(0, Math.min(series.length - 1, from + (e.key === "ArrowLeft" ? -1 : 1))));
        }}
      >
        {series.map((b, i) => {
          const ok = b.requests - b.failure_count;
          return (
            <span
              key={b.start}
              class="da-bar"
              data-on={i === at ? "" : undefined}
              data-empty={b.requests ? undefined : ""}
            >
              {b.failure_count > 0 && (
                <span
                  class="da-bar-failed"
                  style={{ height: `${Math.max(2, (b.failure_count / peak) * 100)}%` }}
                />
              )}
              {ok > 0 && <span class="da-bar-ok" style={{ height: `${Math.max(2, (ok / peak) * 100)}%` }} />}
            </span>
          );
        })}
      </div>
      <div class="da-bars-axis da-num" aria-hidden="true">
        <span>{first ? stamp(first.start, props.now) : ""}</span>
        <span>{last ? `${stamp(last.start, props.now)} →` : ""}</span>
      </div>
      <span class="da-sr" role="status">
        {picked ? reading : ""}
      </span>
    </div>
  );
}
