// StatePane: what a whole pane shows while it loads, when it has nothing, or when it could not be
// read, so a slow or failed load never looks like an empty list. Phases puts the rule in one
// place: the pane while loading, the failure with no data, the rows with a notice when a refresh
// failed over good data, and the empty pane only for a list that was read and is empty.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { Icon, type IconName } from "../icons";
import { usePrivacy } from "../privacy";
import { strings } from "../strings";
import { BUSY_SHOWS_AFTER_MS, Button, type Loaded, Notice } from "../ui";
import { clock } from "./account";

const t = strings("parts.statePane", {
  tryAgain: "Try again",
  refresh: "Refresh",
  details: "Details",
  stale: "Could not refresh. Showing what pluxy said at {time}.",
});

/** The words for reading again: Try again after a failure, Refresh to read again. */
export const tryAgainWord = () => t("tryAgain");
export const refreshWord = () => t("refresh");

/** A button of the pane: a label and what it does, or where it leads. */
export interface PaneAction {
  label: string;
  onClick?: () => unknown;
  href?: string;
  /** The button waits, for a button that must not be pressed again until something outside ends. */
  disabled?: boolean;
}

function PaneButton(props: { action: PaneAction; variant: "primary" | "quiet" }) {
  const a = props.action;
  if (a.href !== undefined) {
    return (
      <a class="btn" data-part="button" data-variant={props.variant} href={a.href}>
        {a.label}
      </a>
    );
  }
  return (
    <Button variant={props.variant} onClick={a.onClick} disabled={a.disabled}>
      {a.label}
    </Button>
  );
}

/** True once ms have passed since the part showed, so a fast load never flashes a placeholder. */
export function useShownAfter(ms: number) {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setShown(true), ms);
    return () => clearTimeout(timer);
  }, [ms]);
  return shown;
}

/** Placeholder bars, one row high each, shown only after BUSY_SHOWS_AFTER_MS. */
export function Placeholder(props: { rows?: number }) {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  return (
    <div class="placeholder" aria-hidden="true">
      {Array.from({ length: props.rows ?? 3 }, (_, i) => (
        <i key={i} />
      ))}
    </div>
  );
}

/**
 * A pane that is loading, empty or failed. title says what happened (the page's own words for
 * loading, "Loading accounts…"); children, one or two lines of what to do. details is core's own
 * error text, masked, under a Details disclosure. primary and secondary are its one main and one
 * quiet button. mark overrides the default mark (failed: needs you; loading and empty: none).
 */
export function StatePane(props: {
  kind: "loading" | "empty" | "failed";
  title: string;
  children?: ComponentChildren;
  details?: string;
  primary?: PaneAction;
  secondary?: PaneAction;
  mark?: IconName;
  rows?: number;
}) {
  const p = usePrivacy();
  const mark = props.mark ?? (props.kind === "failed" ? "needsYou" : undefined);
  return (
    <section
      class="state-pane"
      data-part="state-pane"
      data-kind={props.kind}
      role={props.kind === "loading" ? "status" : props.kind === "failed" ? "alert" : undefined}
      aria-busy={props.kind === "loading" ? "true" : undefined}
    >
      <div class="state-pane-head">
        {mark && (
          <span class="state-pane-mark">
            <Icon name={mark} />
          </span>
        )}
        <h2 class="state-pane-title">{props.title}</h2>
      </div>
      {props.children && <div class="state-pane-text">{props.children}</div>}
      {props.details && (
        <details class="state-pane-details">
          <summary>{t("details")}</summary>
          <pre class="mono">{p.text(props.details)}</pre>
        </details>
      )}
      {(props.primary || props.secondary) && (
        <div class="state-pane-actions">
          {props.primary && <PaneButton action={props.primary} variant="primary" />}
          {props.secondary && <PaneButton action={props.secondary} variant="quiet" />}
        </div>
      )}
      {props.kind === "loading" && <Placeholder rows={props.rows} />}
    </section>
  );
}

/** The stale notice's words: "Could not refresh. Showing what pluxy said at {14:05}." */
export const staleWords = (at?: number) => t("stale", { time: at === undefined ? "?" : clock(at) });

/** Over rows from an earlier read, when a refresh failed: what is shown, and Try again. */
export function StaleNotice(props: { at?: number; error?: string; onRetry: () => unknown }) {
  const p = usePrivacy();
  return (
    <div class="stale-notice" title={p.text(props.error)}>
      <Notice tone="warn">
        {staleWords(props.at)} <Button onClick={props.onRetry}>{t("tryAgain")}</Button>
      </Notice>
    </div>
  );
}

/**
 * A pane by useLoad's phase. loading: StatePane loading with the loading title. failed with no
 * data, or over a list that was empty: StatePane failed with core's error as its details and Try
 * again (whether the list is still empty is not known). failed over rows: the rows, under a
 * StaleNotice. ready: empty when isEmpty says so, else the rows.
 */
export function Phases<T>(props: {
  load: Loaded<T>;
  loading: string;
  failed: { title: string; help?: ComponentChildren };
  isEmpty: (data: T) => boolean;
  empty: ComponentChildren;
  rows?: number;
  children: (data: T) => ComponentChildren;
}) {
  const l = props.load;
  // When the data on screen was read: the last reply that worked.
  const seen = useRef<{ data?: T; at?: number }>({});
  if (l.phase === "ready" && seen.current.data !== l.data) seen.current = { data: l.data, at: Date.now() };
  if (l.phase === "loading") return <StatePane kind="loading" title={props.loading} rows={props.rows} />;
  if (l.phase === "failed" && (l.data === undefined || props.isEmpty(l.data))) {
    return (
      <StatePane
        kind="failed"
        title={props.failed.title}
        details={l.error}
        primary={{ label: t("tryAgain"), onClick: l.reload }}
      >
        {props.failed.help}
      </StatePane>
    );
  }
  if (l.data === undefined) return <>{props.empty}</>;
  return (
    <>
      {l.phase === "failed" && <StaleNotice at={seen.current.at} error={l.error} onRetry={l.reload} />}
      {props.isEmpty(l.data) ? props.empty : props.children(l.data)}
    </>
  );
}
