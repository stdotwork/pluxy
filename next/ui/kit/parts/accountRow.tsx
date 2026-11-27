// AccountRow: one account, the same on every page. Its provider's mark with a dot for its condition
// on the corner, its name, and under the name its condition word and why (or, when it simply
// serves, its provider); then its plan, its limits and its one fix.

import { useEffect, useState } from "preact/hooks";
import type { Account } from "../api/accounts";
import type { QuotaLimit } from "../api/quota";
import { Icon, type IconName } from "../icons";
import { Meter, meterTone, useThresholds } from "../meter";
import { accountIdentities, usePrivacy } from "../privacy";
import { strings } from "../strings";
import { shortDuration } from "../time";
import { Button } from "../ui";
import {
  accountCondition,
  accountFix,
  accountHref,
  accountReason,
  accountServer,
  type Condition,
  conditionTall,
  conditionWord,
  type Group,
  leftPercent,
  planTier,
  providerName,
  resetsIn,
  rowWindows,
  uncheckedReason,
  uncheckedWord,
  windowName,
  windowShort,
} from "./account";
import { type Outcome, useNaming, useOutcome, type Windows } from "./hooks";
import { ProviderMark } from "./providerMark";

const t = strings("parts.accountRow", {
  left: "{percent}% left",
  usedUp: "Used up",
  noLimit: "No limit",
  reading: "Reading limits…",
  none: "No limits reported",
  notReported: "Not reported by {provider}",
  unavailable: "Limits unavailable",
  notRead: "Limits not read",
  notReadTitle: "Open the account to see its limits.",
  titleLeft: "{name}: {percent}% left",
  titleUsedUp: "{name}: used up",
  titleNoLimit: "{name}: no limit",
  titleResets: "{title}, resets in {duration}",
  gauge: "{account}, {name}",
  week: "{count} this week",
});

const MARK: Record<Group, IconName> = {
  needs: "needsYou",
  waiting: "waiting",
  serving: "serving",
  off: "off",
};

/** A dot in a state's colour, named by its word for assistive technology. */
function Dot(props: { label: string }) {
  return (
    <svg class="state-dot" viewBox="0 0 10 10" role="img" aria-label={props.label} focusable="false">
      <circle cx="5" cy="5" r="4" />
    </svg>
  );
}

/**
 * A group's mark: its shape, in its state colour, with the condition word as its name. dot draws
 * it as a dot instead, for the corner of a provider's mark (AccountRow), where the word is beside it.
 */
export function GroupMark(props: { c: Condition; unchecked?: boolean; dot?: boolean }) {
  // A condition that waits on a status pluxy could not read: a muted mark, named Not checked.
  if (props.unchecked) {
    return (
      <span class="group-mark" data-group="unchecked" data-dot={props.dot ? "" : undefined}>
        {props.dot ? <Dot label={uncheckedWord()} /> : <Icon name="waiting" label={uncheckedWord()} />}
      </span>
    );
  }
  // An account that serves but cools down on a model is marked as waiting: something is held back.
  const group = props.c.kind === "serving" && props.c.resting.length > 0 ? "waiting" : props.c.group;
  return (
    <span class="group-mark" data-group={group} data-dot={props.dot ? "" : undefined}>
      {props.dot ? (
        <Dot label={conditionWord(props.c)} />
      ) : (
        <Icon name={MARK[group]} label={conditionWord(props.c)} />
      )}
    </span>
  );
}

/** The plan word, by ink weight: data-plan-tier is top, mid or free. */
export function PlanPill(props: { plan: string }) {
  const plan = props.plan.trim();
  if (!plan) return null;
  return (
    <span class="plan-pill" data-part="plan-pill" data-plan-tier={planTier(plan)}>
      {plan}
    </span>
  );
}

/** A clock that moves on every interval, for countdowns. */
function useNow(every = 30_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(timer);
  }, [every]);
  return now;
}

/**
 * An account's limits on one line: each of the two shortest request windows as its short name, a
 * gauge of what is left, the share left and the countdown to its reset ("5h ▬ 58% left 1h 25m").
 * Or, muted, what is known in their place: Limits not read when the page read none for it (a view
 * reads WINDOWS_PER_VIEW accounts' at most). identities are the account's, for masking a failed
 * read's error.
 */
export function Limits(props: {
  windows?: Windows;
  account: string;
  identities?: readonly string[];
  now?: number;
  /** The provider's name, for "Not reported by Claude" when it reports no limits. */
  provider?: string;
}) {
  const p = usePrivacy();
  const w = props.windows;
  const now = props.now ?? Date.now();
  if (!w) {
    return (
      <span class="limits" data-part="limits" data-state="unread" title={t("notReadTitle")}>
        {t("notRead")}
      </span>
    );
  }
  if (w.state !== "read" || rowWindows(w.limits).length === 0) {
    const word =
      w.state === "reading"
        ? t("reading")
        : w.state === "failed"
          ? t("unavailable")
          : props.provider
            ? t("notReported", { provider: props.provider })
            : t("none");
    return (
      <span
        class="limits"
        data-part="limits"
        data-state={w.state === "read" ? "none" : w.state}
        title={p.text(w.error, props.identities)}
      >
        {word}
      </span>
    );
  }
  return (
    <span class="limits" data-part="limits" data-state="read">
      {rowWindows(w.limits).map((l) => (
        <LimitLine key={l.id} limit={l} account={props.account} now={now} />
      ))}
    </span>
  );
}

/**
 * One limit: its name (short, "5h", or long, "5-hour limit"), a gauge of what is left, the share
 * left and the countdown to its reset. account names the gauge for assistive technology.
 */
export function LimitLine(props: { limit: QuotaLimit; account: string; now?: number; long?: boolean }) {
  const l = props.limit;
  const now = props.now ?? Date.now();
  const thresholds = useThresholds();
  const name = windowName(l);
  const left = leftPercent(l);
  const reset = resetsIn(l, now);
  const countdown = reset === undefined ? undefined : shortDuration(reset / 1000);
  const said = l.unlimited
    ? t("titleNoLimit", { name })
    : left === undefined
      ? name
      : left === 0
        ? t("titleUsedUp", { name })
        : t("titleLeft", { name, percent: left });
  const title = countdown ? t("titleResets", { title: said, duration: countdown }) : said;
  const word = l.unlimited
    ? t("noLimit")
    : left === undefined
      ? ""
      : left === 0
        ? t("usedUp")
        : t("left", { percent: left });
  return (
    // The share left stays in text ink until its gauge turns amber or red (data-tone).
    <span
      class="limit"
      title={title}
      data-tone={left === undefined ? undefined : meterTone(left, thresholds)}
    >
      <span class="limit-name">{props.long ? windowName(l) : windowShort(l)}</span>
      {left !== undefined ? (
        <Meter
          mode="left"
          percent={left}
          label={t("gauge", { account: props.account, name })}
          valueText={word}
        />
      ) : (
        <span class="meter" data-mode="left" data-tone="none" aria-hidden="true" />
      )}
      <span class="limit-left">{word}</span>
      {countdown && <span class="limit-reset">{countdown}</span>}
    </span>
  );
}

/** The fix's button, and why it failed under it, in one line; it can be pressed again. */
function FixButton(props: {
  account: Account;
  c: Condition;
  name: string;
  identities: readonly string[];
  /** Draw a filled fix outlined: a view has one filled button, and another row has it. */
  quiet?: boolean;
  onTurnOn: (id: string) => Promise<Outcome> | Outcome;
  onClearCooldown: (id: string) => Promise<Outcome> | Outcome;
}) {
  const p = usePrivacy();
  const outcome = useOutcome();
  const [error, setError] = useState<string>();
  const fix = accountFix(props.account, props.c);
  if (!fix) return null;
  const variant = props.quiet && fix.variant === "primary" ? "quiet" : fix.variant;
  const label = `${fix.label}: ${props.name}`;
  const button = fix.href ? (
    <a class="btn" data-part="button" data-variant={variant} href={fix.href} aria-label={label}>
      {fix.label}
    </a>
  ) : (
    <Button
      variant={variant}
      label={label}
      onClick={async () => {
        setError(undefined);
        const run = fix.run === "turnOn" ? props.onTurnOn : props.onClearCooldown;
        try {
          setError(outcome(await run(props.account.id)));
        } catch (err) {
          setError(err instanceof Error ? err.message : String(err));
        }
      }}
    >
      {fix.label}
    </Button>
  );
  return (
    <span class="fix">
      {button}
      {error && (
        <span class="fix-error" role="alert" title={p.text(error, props.identities)}>
          {p.text(error, props.identities)}
        </span>
      )}
    </span>
  );
}

/**
 * One account, the same on every page. account is the account as GET /accounts gives it;
 * windows its limits (useWindows); wide (Accounts) adds the week's requests and a column for the
 * fix. onTurnOn and onClearCooldown run the two fixes a page carries out (PATCH /accounts/{id}
 * { disabled: false } and POST /accounts/{id}/cooldown/reset): each gives back the reply, or an
 * error to show, or nothing; the page reads the accounts again after one that worked. The row
 * builds its own links, so every page sends a fix to the same place. running is the extensions
 * core runs, for Provider stopped. unchecked marks a row whose condition waits on a status the
 * page could not read (conditionUnchecked). requests is the week's count once read: 0 shows.
 * grouped leaves the provider's name out of the line under the name, for a list already headed by
 * its provider. quietFix draws the fix outlined even when it repairs something (another row on the
 * view has the filled one).
 */
export function AccountRow(props: {
  account: Account;
  windows?: Windows;
  wide?: boolean;
  requests?: number;
  running?: ReadonlySet<string>;
  unchecked?: boolean;
  grouped?: boolean;
  quietFix?: boolean;
  onTurnOn: (id: string) => Promise<Outcome> | Outcome;
  onClearCooldown: (id: string) => Promise<Outcome> | Outcome;
}) {
  const p = usePrivacy();
  const naming = useNaming();
  const now = useNow();
  const a = props.account;
  const c = accountCondition(a, props.running, now);
  const name = naming.of(a);
  const address = accountServer(a);
  // What names the account in text about it (a reason, an error): its label and its ID.
  const ids = [...accountIdentities(a), a.id];
  const href = accountHref(a.id);
  const tall = conditionTall(c);
  const reason = props.unchecked ? uncheckedReason(a) : tall ? accountReason(a, c, now) : undefined;
  // An account that needs the person or is turned off takes no requests: its limits are not read.
  const limited = c.group === "serving" || c.group === "waiting";
  const fix = (
    <FixButton
      account={a}
      c={c}
      name={name}
      identities={ids}
      quiet={props.quietFix}
      onTurnOn={props.onTurnOn}
      onClearCooldown={props.onClearCooldown}
    />
  );
  const plan = limited ? props.windows?.plan : undefined;
  const provider = a.extension ? providerName(a.extension) : undefined;
  // The condition in words beside its dot, so the colour never says it alone. The dot's name says
  // it to assistive technology, so the word is hidden from it.
  const word = props.unchecked ? uncheckedWord() : conditionWord(c);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: a pointer shortcut only; the name's link is the way to the page by keyboard
    // biome-ignore lint/a11y/noStaticElementInteractions: the same shortcut; the row is not a control of its own
    <div
      class="account-row"
      data-part="account-row"
      data-group={c.group}
      data-condition={props.unchecked ? "unchecked" : c.kind}
      data-tall={tall ? "" : undefined}
      data-wide={props.wide ? "" : undefined}
      onClick={(e) => {
        // The row is a large target for the name's link; its own controls keep their clicks.
        if ((e.target as Element).closest("a, button, input")) return;
        location.hash = href;
      }}
    >
      <div class="account-grid">
        <span class="account-mark">
          {/* Named when the line under the name does not say the provider. */}
          <ProviderMark extension={a.extension} named={!!reason && !props.grouped} />
          <GroupMark c={c} unchecked={props.unchecked} dot />
        </span>
        <span class="account-who">
          <span class="account-line">
            <a class="account-name" href={href} title={name}>
              {name}
            </a>
          </span>
          <span class="account-sub">
            <span class="account-word" aria-hidden="true">
              {word}
            </span>
            {reason ? (
              <span class="account-reason" title={p.text(reason, ids)}>
                {p.text(reason, ids)}
              </span>
            ) : (
              <span class="account-meta">
                {/* An account pluxy cannot read names no provider: nothing to say here. */}
                {provider && !props.grouped && <span class="account-provider">{provider}</span>}
                {address && (
                  <span class="account-address" title={address}>
                    {address}
                  </span>
                )}
              </span>
            )}
          </span>
        </span>
        <span class="account-plan">{plan && <PlanPill plan={plan} />}</span>
        <span class="account-side">
          {limited ? (
            <Limits windows={props.windows} account={name} identities={ids} now={now} provider={provider} />
          ) : (
            !props.wide && fix
          )}
        </span>
        {props.wide && (
          <span class="account-week">
            {props.requests === undefined ? "" : t("week", { count: props.requests.toLocaleString() })}
          </span>
        )}
        {(props.wide || limited) && <span class="account-fix">{fix}</span>}
      </div>
    </div>
  );
}
