// Direction C's small parts: buttons, the segmented choice, an account's state, a provider's
// tile, a usage window, notes and the page head.

import type { ComponentChildren } from "preact";
import { useId, useState } from "preact/hooks";
import type { Account, QuotaLimit } from "../../api";
import { useAction, useKit } from "../../ui";
import { forgetWindows, useFleet } from "./data";
import { Icon, type IconName } from "./icons";
import {
  ago,
  type Condition,
  duration,
  FIX_WORDS,
  limitElapsed,
  limitName,
  limitPercent,
  type Provider,
  type Tone,
  untilWords,
} from "./lib";

export function Btn(props: {
  kind?: "primary" | "plain" | "danger";
  size?: "small";
  href?: string;
  onClick?: () => void;
  disabled?: boolean;
  icon?: IconName;
  /** The accessible name, when the button shows only an icon or its text is not enough. */
  label?: string;
  title?: string;
  pressed?: boolean;
  children?: ComponentChildren;
}) {
  const inner = (
    <>
      {props.icon && <Icon name={props.icon} size={props.size === "small" ? 15 : 17} />}
      {props.children}
    </>
  );
  const common = {
    class: "dc-btn",
    "data-kind": props.kind,
    "data-size": props.size,
    "data-icon-only": props.children === undefined ? "" : undefined,
    "aria-label": props.label,
    title: props.title ?? (props.children === undefined ? props.label : undefined),
  };
  if (props.href && !props.disabled) {
    return (
      <a {...common} href={props.href}>
        {inner}
      </a>
    );
  }
  return (
    <button
      {...common}
      type="button"
      onClick={props.onClick}
      disabled={props.disabled}
      aria-pressed={props.pressed}
    >
      {inner}
    </button>
  );
}

/** One choice out of a few, as radio buttons drawn as a strip. */
export function Segmented<V extends string>(props: {
  label: string;
  value: V;
  options: { value: V; label: string; icon?: IconName; title?: string }[];
  onChange: (v: V) => void;
}) {
  const name = useId();
  return (
    <div class="dc-seg" role="radiogroup" aria-label={props.label}>
      {props.options.map((o) => (
        <label key={o.value} title={o.title}>
          <input
            type="radio"
            name={name}
            value={o.value}
            checked={props.value === o.value}
            onChange={() => props.onChange(o.value)}
          />
          <span>
            {o.icon && <Icon name={o.icon} size={15} />}
            {o.label}
          </span>
        </label>
      ))}
    </div>
  );
}

/** An account's state: a glyph, its words, and how long is left when core knows. */
export function StateMark(props: { c: Condition; now?: number; bare?: boolean }) {
  const left = props.now ? untilWords(props.c, props.now) : undefined;
  return (
    <span class="dc-state" data-tone={props.c.tone} title={props.c.why}>
      <Icon name={props.c.icon} size={16} />
      {!props.bare && <span class="dc-ellipsis">{props.c.word}</span>}
      {props.bare && <span class="dc-sr">{props.c.word}</span>}
      {left && !props.bare && <span class="dc-muted">· {left}</span>}
    </span>
  );
}

export function ProviderTile(props: { p: Provider }) {
  return (
    <span class="dc-prov" data-slot-p={props.p.slot} title={props.p.name} aria-hidden="true">
      {props.p.letters}
    </span>
  );
}

const windowTone = (percent: number): Tone | undefined =>
  percent >= 100 ? "danger" : percent >= 90 ? "danger" : percent >= 70 ? "warn" : undefined;

/**
 * One usage window: how much of it is used, as a bar, with a tick where the window's time is
 * now. A bar ahead of the tick is being used up faster than the window passes.
 */
export function WindowMeter(props: { limit: QuotaLimit; now: number; plain?: boolean }) {
  const { limit, now } = props;
  const name = limitName(limit);
  const percent = limitPercent(limit);
  const elapsed = limitElapsed(limit, now);
  const left = limit.resets_at ? limit.resets_at - now / 1000 : undefined;
  const reset = left === undefined ? undefined : left > 0 ? `Resets in ${duration(left)}` : "Reset available";
  if (limit.unlimited || percent === undefined) {
    return (
      <div class="dc-win">
        <span class="dc-win-name">{name}</span>
        <span class="dc-win-left">{limit.unlimited ? "Unlimited" : "–"}</span>
        {reset && !props.plain && <span class="dc-win-reset">{reset}</span>}
      </div>
    );
  }
  const used = Math.max(0, Math.min(100, percent));
  const remaining = Math.max(0, Math.round(100 - percent));
  return (
    <div class="dc-win" data-tone={windowTone(used)}>
      <span class="dc-win-name">{name}</span>
      <span class="dc-win-left">{remaining}% remaining</span>
      {/* biome-ignore lint/a11y/useSemanticElements: a drawn bar with a tick on it, which a meter element cannot hold */}
      <div
        class="dc-win-track"
        role="meter"
        aria-label={`${name}: ${remaining}% remaining${reset ? `, ${reset.toLowerCase()}` : ""}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(used)}
        aria-valuetext={`${Math.round(used)}% used`}
      >
        <span class="dc-win-fill" style={{ width: `${used}%` }} />
        {elapsed !== undefined && (
          <span
            class="dc-win-now"
            style={{ left: `${elapsed}%` }}
            title={`${Math.round(elapsed)}% of this window's time has passed`}
          />
        )}
      </div>
      {reset && !props.plain && <span class="dc-win-reset">{reset}</span>}
    </div>
  );
}

export function Note(props: {
  tone?: "warn" | "danger";
  icon?: IconName;
  action?: ComponentChildren;
  children: ComponentChildren;
}) {
  return (
    <div class="dc-note" data-tone={props.tone} role={props.tone === "danger" ? "alert" : "status"}>
      <Icon name={props.icon ?? (props.tone ? "warn" : "info")} size={18} />
      <div class="dc-note-body">{props.children}</div>
      {props.action}
    </div>
  );
}

export function PageHead(props: {
  title: string;
  /** One sentence beside the title: what the page says before anything is read. */
  sub?: ComponentChildren;
  crumb?: { href: string; label: string };
  children?: ComponentChildren;
}) {
  return (
    <header class="dc-head">
      {props.crumb && (
        <a class="dc-crumb" href={props.crumb.href}>
          <Icon name="back" size={16} />
          {props.crumb.label}
        </a>
      )}
      <h1 data-part="page-title">{props.title}</h1>
      {props.sub && <p class="dc-head-sub">{props.sub}</p>}
      {props.children && <div class="dc-head-tools">{props.children}</div>}
    </header>
  );
}

/** When the page's data was read, with the button that reads it again. */
export function Refresh(props: { at?: number; now: number; busy: boolean; onRefresh: () => void }) {
  return (
    <>
      {props.at && (
        <span class="dc-updated" title={new Date(props.at).toLocaleString()}>
          {props.busy ? "Reading…" : `Read ${ago(new Date(props.at).toISOString(), props.now)}`}
        </span>
      )}
      <Btn icon="refresh" label="Refresh" kind="plain" onClick={props.onRefresh} disabled={props.busy} />
    </>
  );
}

/**
 * The one action that fixes an account's condition, beside the account. Clearing a cooldown asks
 * once more, in place: it lets requests go to an account its provider may still be limiting.
 */
export function FixButton(props: { account: Account; c: Condition; size?: "small"; primary?: boolean }) {
  const { api } = useKit();
  const fleet = useFleet();
  const [asking, setAsking] = useState(false);
  const act = useAction(() => {
    setAsking(false);
    forgetWindows(props.account.id);
    fleet.reload();
  });
  const fix = props.c.fix;
  if (!fix || fix === "remove") return null;
  const kind = props.primary ? "primary" : undefined;
  const id = props.account.id;
  if (fix === "signin") {
    return (
      <Btn
        kind={kind}
        size={props.size}
        href={`#/accounts/${encodeURIComponent(id)}?do=signin`}
        icon="signIn"
      >
        {FIX_WORDS.signin}
      </Btn>
    );
  }
  if (fix === "extensions") {
    return (
      <Btn kind={kind} size={props.size} href="#/extensions">
        {FIX_WORDS.extensions}
      </Btn>
    );
  }
  if (fix === "enable") {
    return (
      <span class="dc-fix">
        <Btn
          kind={kind}
          size={props.size}
          disabled={act.busy}
          onClick={() => act.run(() => api.setDisabled(id, false))}
        >
          {FIX_WORDS.enable}
        </Btn>
        {act.error && (
          <span class="dc-fix-error" role="alert">
            {fleet.mask(act.error)}
          </span>
        )}
      </span>
    );
  }
  return (
    <span class="dc-fix">
      {asking ? (
        <>
          <Btn
            kind="primary"
            size={props.size}
            disabled={act.busy}
            onClick={() => act.run(() => api.resetCooldown(id))}
            title="Requests can go to this account again at once. This does not restore the provider's limit."
          >
            Yes, clear
          </Btn>
          <Btn kind="plain" size={props.size} onClick={() => setAsking(false)}>
            Cancel
          </Btn>
        </>
      ) : (
        <Btn kind={kind} size={props.size} onClick={() => setAsking(true)}>
          {FIX_WORDS.clear}
        </Btn>
      )}
      {act.error && (
        <span class="dc-fix-error" role="alert">
          {fleet.mask(act.error)}
        </span>
      )}
    </span>
  );
}
