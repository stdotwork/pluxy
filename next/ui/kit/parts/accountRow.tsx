// AccountRow: one account on one line, the same on every page. Its group's mark and condition
// word, its name, its provider and plan, then its limits; a row that needs the person, waits, or
// cools down on a model says why under the name and has its one fix at the end.

import { useEffect, useState } from "preact/hooks";
import type { Account } from "../api/accounts";
import type { QuotaLimit } from "../api/quota";
import { Icon, type IconName } from "../icons";
import { Meter } from "../meter";
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
  uncheckedWord,
  windowName,
  windowShort,
} from "./account";
import { type Outcome, useNaming, useOutcome, type Windows } from "./hooks";

const t = strings("parts.accountRow", {
  left: "{percent}% left",
  usedUp: "Used up",
  noLimit: "No limit",
  reading: "Reading limits…",
  none: "No limits reported",
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

/** A group's mark: its shape, in its state colour, with the condition word as its name. */
export function GroupMark(props: { c: Condition; unchecked?: boolean }) {
  // A condition that waits on a status pluxy could not read: a muted mark, named Not checked.
  if (props.unchecked) {
    return (
      <span class="group-mark" data-group="unchecked">
        <Icon name="waiting" label={uncheckedWord()} />
      </span>
    );
  }
  // An account that serves but cools down on a model is marked as waiting: something is held back.
  const group = props.c.kind === "serving" && props.c.resting.length > 0 ? "waiting" : props.c.group;
  return (
    <span class="group-mark" data-group={group}>
      <Icon name={MARK[group]} label={conditionWord(props.c)} />
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
    const word = w.state === "reading" ? t("reading") : w.state === "failed" ? t("unavailable") : t("none");
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
    <span class="limit" title={title}>
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
  onTurnOn: (id: string) => Promise<Outcome> | Outcome;
  onClearCooldown: (id: string) => Promise<Outcome> | Outcome;
}) {
  const p = usePrivacy();
  const outcome = useOutcome();
  const [error, setError] = useState<string>();
  const fix = accountFix(props.account, props.c);
  if (!fix) return null;
  const label = `${fix.label}: ${props.name}`;
  const button = fix.href ? (
    <a class="btn" data-part="button" data-variant={fix.variant} href={fix.href} aria-label={label}>
      {fix.label}
    </a>
  ) : (
    <Button
      variant={fix.variant}
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
 */
export function AccountRow(props: {
  account: Account;
  windows?: Windows;
  wide?: boolean;
  requests?: number;
  running?: ReadonlySet<string>;
  unchecked?: boolean;
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
  const reason = tall ? accountReason(a, c, now) : undefined;
  // An account that needs the person or is turned off takes no requests: its limits are not read.
  const limited = c.group === "serving" || c.group === "waiting";
  const fix = (
    <FixButton
      account={a}
      c={c}
      name={name}
      identities={ids}
      onTurnOn={props.onTurnOn}
      onClearCooldown={props.onClearCooldown}
    />
  );
  const plan = limited ? props.windows?.plan : undefined;
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
        <GroupMark c={c} unchecked={props.unchecked} />
        <span class="account-who">
          <span class="account-line">
            <a class="account-name" href={href} title={name}>
              {name}
            </a>
            <span class="account-meta">
              {/* An account pluxy cannot read names no provider: nothing to say here. */}
              {a.extension && <span class="account-provider">{providerName(a.extension)}</span>}
              {address && (
                <span class="account-address" title={address}>
                  {address}
                </span>
              )}
              {plan && <PlanPill plan={plan} />}
            </span>
          </span>
          {reason && (
            <span class="account-reason" title={p.text(reason, ids)}>
              {p.text(reason, ids)}
            </span>
          )}
        </span>
        <span class="account-side">
          {limited ? (
            <Limits windows={props.windows} account={name} identities={ids} now={now} />
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
