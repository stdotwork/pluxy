// One account on its own page (#/accounts/<id>; the header strip says "Accounts / <name>"): its
// header (mark, name, plan, state, the one fix), full-width limits with absolute reset times and
// pace, this week by model, latest requests, facts, and settings, models and a test folded below.
// #/accounts/<id>/sign-in opens its sign-in under the header. The list of all accounts is accounts.tsx.

import { useEffect, useMemo, useState } from "preact/hooks";
import {
  type Account,
  type LoginMethod,
  leftOut,
  problem,
  type QuotaLimit,
  runningExtensions,
  type Status,
  type UsageGroup,
} from "../api";
import type { UsageSummary } from "../api/usage";
import type { UsageEventsReply } from "../api/usageEvents";
import type { Reply } from "../host";
import { Icon } from "../icons";
import { LoginFlow } from "../loginFlow";
import { Meter, meterTone, useThresholds } from "../meter";
import {
  accountCondition,
  accountFix,
  accountHref,
  accountReason,
  type Condition,
  clock,
  conditionUnchecked,
  forgetWindow,
  GroupMark,
  leftPercent,
  Placeholder,
  PlanPill,
  ProviderMark,
  providerName,
  RequestRow,
  StatePane,
  tryAgainWord,
  uncheckedReason,
  useNaming,
  useShownAfter,
  useWindows,
  type Windows,
} from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { fullTime } from "../time";
import { BUSY_SHOWS_AFTER_MS, Button, Facts, Notice, PageHead, useKit, useLoad, when } from "../ui";
import { balanceLabel, isBalanceLimit, limitTitle, paceOf, resetLabel } from "./account.helpers";
import { AccountEdit, AccountSettings } from "./accountSettings";
import { ACCOUNTS_POLL_MS, type Reads, RoundNotice, useRound } from "./accounts";
import { nameProblem, orderWord, signsInWith } from "./accounts.helpers";
import { AccountTools } from "./accountTools";
import { ConfigDialog } from "./configDialog";
import { configAddress } from "./configSource.helpers";

const t = strings("account", {
  title: "Account",
  loading: "Loading accounts…",
  failed: "Could not read the accounts.",
  notFound: "This account is not in pluxy.",
  notFoundText: "It may have been removed.",
  back: "Back to accounts",
  serving: "Serving.",
  said: "“{error}”",
  saidAt: "“{error}”, {when}",
  brokenFix:
    "Remove it and add it again. If you have a copy of its file, put it back and restart pluxy instead.",
  details: "Details",
  configFix: "Change its key in pluxy's settings file.",
  openSettingsFile: "Open the settings file",
  limits: "Limits",
  left: "{percent}% left",
  usedUp: "Used up",
  noLimit: "No limit",
  gauge: "{account}, {name}",
  manualResets: "Manual resets: {count} left",
  subscriptionEnds: "Subscription ends {date}",
  readAt: "Read at {time}",
  refresh: "Refresh",
  notRead: "Limits are read while the account takes requests.",
  reading: "Reading limits…",
  none: "No limits reported",
  notReported: "Not reported by {provider}",
  unavailable: "Limits unavailable",
  about: "About",
  provider: "Provider",
  signsInWith: "Signs in with",
  signIn: "Sign-in",
  renews: "Stays signed in by itself",
  couldNotRenew: "Could not renew: {error}",
  ends: "Ends {when}",
  order: "Order",
  rightNow: "Right now",
  underWay: { one: "{count} request under way", other: "{count} requests under way" },
  added: "Added",
  knownAs: "Known to tools as",
  week: "This week",
  weekLine: {
    one: "{count} request · {percent}% succeeded",
    other: "{count} requests · {percent}% succeeded",
  },
  byModel: "Requests by model",
  seeRequests: "See its requests",
  noWeek: "No requests in the last 7 days.",
  weekFailed: "Could not read this week's requests.",
  latest: "Latest requests",
  latestFailed: "Could not read the latest requests.",
  statusUnread: "Could not check which providers are running.",
  change: "Change it",
  rename: "Rename",
  renameTitle: "Rename {name}",
  name: "Name",
  save: "Save",
  cancel: "Cancel",
  saved: "Saved.",
  turnOff: "Turn off",
  turnOn: "Turn on",
  remove: "Remove…",
  removeNow: "Remove",
  removeTitle: "Remove {name}?",
  removeText: "pluxy deletes its saved sign-in. To use it again, add it again.",
  setInFile: "Set in pluxy's settings file.",
  more: "Settings, models and a test request",
  signInAgain: "Sign in again",
  signedIn: "Signed in again.",
  methods: "How to sign in",
  noMethods: "{provider} offers no way to sign in from here. Is it running?",
  openExtensions: "Open Extensions",
  loadingMethods: "Loading sign-in methods…",
  methodsFailed: "Could not read the sign-in methods.",
  tryAgain: "Try again",
});

/** The account's ID and the part after it, from the address: #/accounts/<id>[/sign-in]. */
export function accountAddress(hash: string): { id: string; part: string } {
  const path = hash.replace(/^#\/?/, "").split("?")[0] ?? "";
  const [, raw = "", part = ""] = path.split("/");
  let id = raw;
  try {
    id = decodeURIComponent(raw);
  } catch {
    // An ID that is not encoded is taken as written.
  }
  return { id, part };
}

function useAddress() {
  const [hash, setHash] = useState(() => location.hash);
  useEffect(() => {
    const read = () => setHash(location.hash);
    window.addEventListener("hashchange", read);
    return () => window.removeEventListener("hashchange", read);
  }, []);
  return accountAddress(hash);
}

export function AccountPage() {
  const { id, part } = useAddress();
  return <AccountView key={id} id={id} part={part} />;
}

/** What the page reads in each round: the accounts, the status, and this account's week and latest requests. */
type Round = {
  accounts: { accounts: Account[] };
  status: Status;
  week: UsageSummary;
  latest: UsageEventsReply;
};

/** Final-shape placeholder shown after BUSY_SHOWS_AFTER_MS while the initial read is under way. */
function AccountSkeleton() {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  return (
    <div class="account-skeleton" aria-hidden="true">
      <i class="account-skeleton-hero" />
      <i class="account-skeleton-limits" />
      <div class="account-skeleton-cols">
        <i class="account-skeleton-card" />
        <i class="account-skeleton-card" />
      </div>
    </div>
  );
}

function AccountView(props: { id: string; part: string }) {
  const { api } = useKit();
  const filter = { since: "7d", account: props.id, model: "", provider: "", key_id: "", failed: "" };
  const { reads, run, again } = useRound<Round>(
    {
      accounts: api.accounts,
      status: api.status,
      week: () => api.usage(`since=7d&account=${encodeURIComponent(props.id)}`),
      latest: () => api.usageEventsBefore(filter, 0, 6),
    },
    ACCOUNTS_POLL_MS,
  );
  const accounts = reads.accounts;
  const list = accounts.data?.accounts;
  const a = list?.find((x) => x.id === props.id);
  const naming = useNaming(list);
  if (!a) {
    if (accounts.phase === "loading") {
      return (
        <div class="account-page">
          <StatePane kind="loading" title={t("loading")} rows={0} />
          <AccountSkeleton />
        </div>
      );
    }
    if (!list)
      return (
        <StatePane
          kind="failed"
          title={t("failed")}
          details={accounts.error}
          primary={{ label: t("tryAgain"), onClick: run }}
        />
      );
    return (
      <>
        <PageHead title={t("title")} />
        <StatePane kind="empty" title={t("notFound")} primary={{ label: t("back"), href: "#/accounts" }}>
          <p>{t("notFoundText")}</p>
        </StatePane>
      </>
    );
  }
  return (
    <Loaded
      account={a}
      all={list ?? []}
      part={props.part}
      name={naming.of(a)}
      byId={naming.byId}
      reads={reads}
      run={run}
      again={again}
    />
  );
}

/** Where a fix that the page runs went wrong, or nothing when it worked. */
function useRun(reload: () => void) {
  const { unauthorized } = useKit();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const run = async (call: () => Promise<Reply<unknown>>, then?: () => void) => {
    setBusy(true);
    setError(undefined);
    const r = await call();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why !== undefined) return setError(why);
    reload();
    then?.();
  };
  return { busy, error, run };
}

function Loaded(props: {
  account: Account;
  all: Account[];
  part: string;
  name: string;
  byId: (id: string) => string;
  reads: Reads<Round>;
  run: () => unknown;
  again: () => unknown;
}) {
  const { api } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const ids = [...accountIdentities(a), a.id];
  const now = Date.now();
  const { accounts, status, week, latest } = props.reads;
  const running = useMemo(() => runningExtensions(status.data), [status.data]);
  const c = accountCondition(a, running, now);
  // A status never read: whether its provider runs is not known, so the page does not say Serving.
  const unchecked =
    status.phase === "failed" && status.data === undefined && conditionUnchecked(a, c, running);
  const [refreshed, setRefreshed] = useState(0);
  const one = useMemo(() => [a], [a]);
  const w = useWindows(one, running, refreshed).get(a.id);
  const keys = useLoad(api.clientKeys);
  const reload = () => void props.again();
  const fix = useRun(reload);
  const [notice, setNotice] = useState<string>();
  const signingIn = props.part === "sign-in" && a.source === "login";
  const broken = c.kind === "broken";
  const stale = [accounts, status, week, latest].some((r) => r.phase === "failed" && r.data !== undefined);
  const hasLatest = (latest.data?.events.length ?? 0) > 0 || (latest.phase === "failed" && !latest.data);

  const aboutCard = (
    <section class="account-section card account-about" aria-labelledby="account-about">
      <h2 id="account-about">{t("about")}</h2>
      <About account={a} all={props.all} ids={ids} />
    </section>
  );

  const weekCard = (
    <section class="account-section card" aria-labelledby="account-week">
      <div class="account-section-head">
        <h2 id="account-week">{t("week")}</h2>
        {(week.data?.total_requests ?? 0) > 0 && (
          <a class="account-onward" href={`#/activity?account=${encodeURIComponent(a.id)}`}>
            {t("seeRequests")}
            <Icon name="chevron" size={12} />
          </a>
        )}
      </div>
      {week.data ? (
        <Week
          total={week.data.total_requests}
          succeeded={week.data.success_count}
          models={week.data.models}
          ids={accountIdentities(a)}
        />
      ) : week.phase === "failed" ? (
        <SectionFailed text={t("weekFailed")} error={week.error} ids={ids} onRetry={props.run} />
      ) : (
        <div role="status" aria-busy="true">
          <Placeholder rows={3} />
        </div>
      )}
    </section>
  );

  return (
    <article
      class="account-page"
      data-part="account-page"
      data-group={c.group}
      data-stale={stale ? "" : undefined}
    >
      <PageHead title={props.name} />
      <RoundNotice
        reads={[accounts, status, week, latest]}
        unread={unchecked ? [t("statusUnread")] : []}
        ids={ids}
        onRetry={props.run}
      />
      <div class="account-fade">
        <Opening
          account={a}
          name={props.name}
          c={c}
          unchecked={unchecked}
          w={w}
          now={now}
          signingIn={signingIn}
          busy={fix.busy}
          onRun={(r) =>
            void fix.run(r === "turnOn" ? () => api.setDisabled(a.id, false) : () => api.resetCooldown(a.id))
          }
        />
        {fix.error && <Notice tone="danger">{p.text(fix.error, ids)}</Notice>}
        {notice && <Notice tone="ok">{notice}</Notice>}
        {signingIn && (
          <SignInAgain
            account={a}
            onDone={() => {
              setNotice(t("signedIn"));
              reload();
              location.hash = accountHref(a.id);
            }}
          />
        )}
        {!broken && (
          <>
            <section class="account-section card account-limits-card" aria-labelledby="account-limits">
              <div class="account-section-head">
                <h2 id="account-limits">{t("limits")}</h2>
                {w?.at !== undefined && (
                  <span class="account-read muted">
                    {t("readAt", { time: clock(w.at) })}
                    <Button
                      onClick={() => {
                        forgetWindow(a.id);
                        setRefreshed((n) => n + 1);
                      }}
                    >
                      <Icon name="refresh" size={14} />
                      {t("refresh")}
                    </Button>
                  </span>
                )}
              </div>
              <LimitList
                w={w}
                name={props.name}
                provider={a.extension ? providerName(a.extension) : undefined}
                ids={ids}
                now={now}
              />
            </section>
            {hasLatest ? (
              <>
                <div class="account-columns">
                  {weekCard}
                  <section class="account-section card" aria-labelledby="account-latest">
                    <div class="account-section-head">
                      <h2 id="account-latest">{t("latest")}</h2>
                      {(latest.data?.events.length ?? 0) > 0 && (
                        <a class="account-onward" href={`#/activity?account=${encodeURIComponent(a.id)}`}>
                          {t("seeRequests")}
                          <Icon name="chevron" size={12} />
                        </a>
                      )}
                    </div>
                    {latest.data ? (
                      <div class="account-requests">
                        {latest.data.events.map((e) => (
                          <RequestRow key={e.seq} event={e} name={props.byId} keys={keys.data?.keys} />
                        ))}
                      </div>
                    ) : (
                      <SectionFailed
                        text={t("latestFailed")}
                        error={latest.error}
                        ids={ids}
                        onRetry={props.run}
                      />
                    )}
                  </section>
                </div>
                {aboutCard}
              </>
            ) : (
              <div class="account-columns" data-even="">
                {weekCard}
                {aboutCard}
              </div>
            )}
          </>
        )}
        <ChangeIt account={a} c={c} name={props.name} ids={ids} reload={reload} onSaved={setNotice} />
      </div>
    </article>
  );
}

/** A section that could not be read: one line, core's error in its title, and the way to read again. */
function SectionFailed(props: {
  text: string;
  error?: string;
  ids: readonly string[];
  onRetry: () => unknown;
}) {
  const p = usePrivacy();
  return (
    <p class="account-failed" role="alert" title={p.text(props.error, props.ids)}>
      <span>{props.text}</span> <Button onClick={props.onRetry}>{tryAgainWord()}</Button>
    </p>
  );
}

/** The opening header: provider mark with state dot, account name, plan pill, state and why, and the one fix. */
function Opening(props: {
  account: Account;
  name: string;
  c: Condition;
  unchecked: boolean;
  w?: Windows;
  now: number;
  signingIn: boolean;
  busy: boolean;
  onRun: (run: "turnOn" | "clearCooldown") => void;
}) {
  const p = usePrivacy();
  const { account: a, c } = props;
  const ids = [...accountIdentities(a), a.id];
  const reason = props.unchecked ? uncheckedReason(a) : (accountReason(a, c, props.now) ?? t("serving"));
  const fix = accountFix(a, c);
  // See what to do leads here: the opening says what to do in its place.
  const sayWhat = fix?.href === accountHref(a.id);
  const failing = c.group === "needs" || c.group === "waiting";
  const error = failing ? a.last_error : undefined;
  const plan = props.w?.plan;
  const provider = a.extension ? providerName(a.extension) : undefined;
  const resting = c.kind === "serving" && c.resting.length > 0;
  const configKeyFix = c.kind === "needs_login" && a.source === "config";
  const showFix = fix && !sayWhat && !(props.signingIn && fix.href);
  // Show the provider beside the state when the reason sentence does not already begin with it.
  const showProvider = !!provider && !reason.startsWith(provider);
  return (
    <header
      class="account-opening"
      data-part="account-opening"
      data-group={c.group}
      data-resting={resting ? "" : undefined}
    >
      <div class="account-hero">
        <div class="account-hero-top">
          <span class="account-hero-mark">
            <ProviderMark extension={a.extension} />
            <GroupMark c={c} unchecked={props.unchecked} dot />
          </span>
          <h2 class="account-hero-name">{props.name}</h2>
          {plan && <PlanPill plan={plan} provider={a.extension} />}
        </div>
        <p
          class="account-state"
          data-group={c.group}
          data-resting={resting ? "" : undefined}
          data-unchecked={props.unchecked ? "" : undefined}
        >
          <span class="account-state-text">{p.text(reason, ids)}</span>
          {showProvider && (
            <>
              <span class="account-meta-sep" aria-hidden="true">
                ·
              </span>
              <span class="muted">{provider}</span>
            </>
          )}
        </p>
        {error && (
          <p class="account-said muted">
            {a.last_error_at
              ? rich(t("saidAt", { error: p.text(error.message, ids) ?? "" }), {
                  when: when(a.last_error_at),
                })
              : t("said", { error: p.text(error.message, ids) ?? "" })}
          </p>
        )}
        {c.kind === "broken" && (
          <>
            <p>{t("brokenFix")}</p>
            {a.health?.why && (
              <details class="account-details">
                <summary>{t("details")}</summary>
                <pre class="mono">{p.text(a.health.why, ids)}</pre>
              </details>
            )}
          </>
        )}
        {configKeyFix && <p>{t("configFix")}</p>}
      </div>
      {(configKeyFix || showFix) && (
        <div class="account-hero-actions">
          {configKeyFix ? (
            <a class="btn" data-part="button" data-variant="primary" href={configAddress("source", a.id)}>
              {t("openSettingsFile")}
            </a>
          ) : fix?.href ? (
            <a class="btn" data-part="button" data-variant={fix.variant} href={fix.href}>
              {fix.label}
            </a>
          ) : fix ? (
            <Button
              variant={fix.variant}
              disabled={props.busy}
              onClick={() => fix.run && props.onRun(fix.run)}
            >
              {fix.label}
            </Button>
          ) : null}
        </div>
      )}
    </header>
  );
}

/** One full-width limit row: name, % left, absolute + relative reset time, bar with pace tick, and pace line. */
function AccountLimit(props: { limit: QuotaLimit; account: string; now: number }) {
  const l = props.limit;
  const thresholds = useThresholds();
  if (isBalanceLimit(l)) {
    const b = balanceLabel(l);
    return (
      <div class="account-balance" data-part="account-limit">
        <span class="account-limit-name">{b.title}</span>
        <span class="account-balance-value">{b.value}</span>
      </div>
    );
  }
  const title = limitTitle(l);
  const left = leftPercent(l);
  const tone = left === undefined ? undefined : meterTone(left, thresholds);
  const word = l.unlimited
    ? t("noLimit")
    : left === undefined
      ? ""
      : left === 0
        ? t("usedUp")
        : t("left", { percent: left });
  const reset = resetLabel(l, props.now);
  const pace = paceOf(l, props.now);
  return (
    <div class="account-limit" data-part="account-limit" data-tone={tone}>
      <div class="account-limit-head">
        <span class="account-limit-name">{title}</span>
        <span class="account-limit-right">
          {word && <span class="account-limit-value">{word}</span>}
          {reset &&
            (reset.at ? (
              <time class="account-limit-reset" dateTime={reset.at.toISOString()} title={fullTime(reset.at)}>
                {reset.text}
              </time>
            ) : (
              <span class="account-limit-reset">{reset.text}</span>
            ))}
        </span>
      </div>
      <div class="account-limit-bar">
        {left !== undefined ? (
          <Meter
            mode="left"
            percent={left}
            label={t("gauge", { account: props.account, name: title })}
            valueText={word}
          />
        ) : (
          <span class="meter" data-mode="left" data-tone="none" aria-hidden="true" />
        )}
        {pace?.showTick && (
          <span
            class="account-pace-tick"
            aria-hidden="true"
            style={{ insetInlineStart: `${pace.evenLeft}%` }}
          />
        )}
      </div>
      {pace?.text && (
        <p class="account-limit-pace" data-pace={pace.kind}>
          {pace.text}
        </p>
      )}
    </div>
  );
}

/** Every limit the provider reports (but its manual resets), then the resets left and the end of the plan. */
function LimitList(props: {
  w?: Windows;
  name: string;
  provider?: string;
  ids: readonly string[];
  now: number;
}) {
  const p = usePrivacy();
  const w = props.w;
  if (!w) return <p class="muted">{t("notRead")}</p>;
  const noneText = props.provider ? t("notReported", { provider: props.provider }) : t("none");
  const hasFoot = w.resets !== undefined || w.endsAt !== undefined;
  return (
    <div class="account-limits" data-part="limits" data-state={w.state}>
      {w.state === "reading" && <p class="muted">{t("reading")}</p>}
      {w.state === "none" && <p class="muted">{noneText}</p>}
      {w.state === "failed" && (
        <p class="muted" title={p.text(w.error, props.ids)}>
          {t("unavailable")}
        </p>
      )}
      {w.limits.map((l) => (
        <AccountLimit key={l.id} limit={l} account={props.name} now={props.now} />
      ))}
      {hasFoot && (
        <div class="account-limits-foot">
          {w.resets !== undefined && <span>{t("manualResets", { count: w.resets.toLocaleString() })}</span>}
          {w.endsAt !== undefined && <span>{rich(t("subscriptionEnds"), { date: when(w.endsAt) })}</span>}
        </div>
      )}
    </div>
  );
}

function About(props: { account: Account; all: Account[]; ids: readonly string[] }) {
  const p = usePrivacy();
  const a = props.account;
  // A sign-in that no longer works is the opening's to say: it is not "Stays signed in".
  const signedOut = a.state === "reauth_required" || leftOut(a) === "needs_login";
  const sign =
    a.kind === "api_key"
      ? undefined
      : a.renewal_error
        ? t("couldNotRenew", { error: p.text(a.renewal_error.message, props.ids) ?? "" })
        : signedOut
          ? undefined
          : a.refreshable
            ? t("renews")
            : a.expires_at
              ? rich(t("ends"), { when: when(a.expires_at) })
              : undefined;
  return (
    <Facts
      rows={[
        [t("provider"), providerName(a.extension)],
        [t("signsInWith"), signsInWith(a) || undefined],
        [t("signIn"), sign],
        [t("order"), a.extension && <a href="#/routing">{orderWord(a, props.all)}</a>],
        [t("rightNow"), a.active_requests ? t("underWay", { count: a.active_requests }) : undefined],
        [t("added"), when(a.created_at)],
        [t("knownAs"), a.public_id && a.public_id !== a.id && <span class="mono">{a.public_id}</span>],
      ]}
    />
  );
}

/** The week's requests, how many succeeded, and the five models it served most. */
function Week(props: { total: number; succeeded: number; models: UsageGroup[]; ids: readonly string[] }) {
  const p = usePrivacy();
  if (props.total === 0) return <p class="muted">{t("noWeek")}</p>;
  const percent = Math.floor((props.succeeded / props.total) * 100);
  const top = props.models
    .filter((m) => m.total_requests > 0)
    .sort((x, y) => y.total_requests - x.total_requests)
    .slice(0, 5);
  const most = top[0]?.total_requests ?? 1;
  return (
    <>
      <p class="account-week-summary">
        <strong>{t("weekLine", { count: props.total, percent })}</strong>
      </p>
      <ul class="account-bars" aria-label={t("byModel")}>
        {top.map((m) => (
          <li key={m.name} class="account-bar">
            <span class="account-bar-name mono" title={p.text(m.name, props.ids)}>
              {p.text(m.name, props.ids)}
            </span>
            <span class="account-bar-count">{m.total_requests.toLocaleString()}</span>
            <span class="account-bar-track" aria-hidden="true">
              <span class="account-bar-fill" style={{ width: `${(m.total_requests / most) * 100}%` }} />
            </span>
          </li>
        ))}
      </ul>
    </>
  );
}

/** Signing in to this account again: its provider's ways to sign in, then the sign-in itself. */
function SignInAgain(props: { account: Account; onDone: () => void }) {
  const { api } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const methods = useLoad(api.loginMethods);
  const [chosen, setChosen] = useState<LoginMethod>();
  const [attempt, setAttempt] = useState(0);
  const available = methods.data?.extensions.find((e) => e.extension === a.extension)?.methods ?? [];
  const method = chosen ?? (available.length === 1 ? available[0] : undefined);
  const provider = providerName(a.extension);
  return (
    <section
      class="account-section card account-sign-in"
      data-part="account-sign-in"
      aria-labelledby="account-sign-in"
    >
      <h2 id="account-sign-in">{t("signInAgain")}</h2>
      {methods.phase === "loading" && <p class="muted">{t("loadingMethods")}</p>}
      {methods.phase === "failed" && !methods.data && (
        <StatePane
          kind="failed"
          title={t("methodsFailed")}
          details={methods.error}
          primary={{ label: t("tryAgain"), onClick: methods.reload }}
        />
      )}
      {methods.data && available.length === 0 && (
        <p>
          {t("noMethods", { provider })} <a href="#/extensions">{t("openExtensions")}</a>
        </p>
      )}
      {!method && available.length > 1 && (
        <ul class="account-methods" aria-label={t("methods")}>
          {available.map((m) => (
            <li key={m.method}>
              <Button onClick={() => setChosen(m)}>{m.label || m.method}</Button>
            </li>
          ))}
        </ul>
      )}
      {method && (
        <LoginFlow
          key={`${method.method}/${attempt}`}
          extension={a.extension ?? ""}
          method={method}
          existingAccountId={a.id}
          identities={accountIdentities(a)}
          keptInputs={Object.keys(a.settings ?? {})}
          onSuccess={props.onDone}
          onReset={() => {
            // Back to the methods when there are several; with one there is nothing to choose, so the sign-in is left.
            if (available.length > 1) {
              setChosen(undefined);
              setAttempt((n) => n + 1);
            } else location.hash = accountHref(a.id);
          }}
        />
      )}
      {methods.error && methods.data && <Notice tone="warn">{p.text(methods.error)}</Notice>}
    </section>
  );
}

/** Rename, Turn off, Edit settings and Remove for an account added here; the settings file's link for one set there. */
function ChangeIt(props: {
  account: Account;
  c: Condition;
  name: string;
  ids: readonly string[];
  reload: () => void;
  onSaved: (notice: string) => void;
}) {
  const { api } = useKit();
  const p = usePrivacy();
  const a = props.account;
  const act = useRun(props.reload);
  const [dialog, setDialog] = useState<"rename" | "remove">();
  const broken = props.c.kind === "broken";
  // Turning on is the opening's fix for an account that is off or closed: it is not offered twice.
  const on = !a.disabled && leftOut(a) !== "banned";
  return (
    <>
      <section class="account-section card account-change" aria-labelledby="account-change">
        <h2 id="account-change">{t("change")}</h2>
        {a.source === "config" ? (
          <p class="muted">
            {t("setInFile")} <a href={configAddress("source", a.id)}>{t("openSettingsFile")}</a>
          </p>
        ) : (
          <div class="account-change-actions">
            {/* Core refuses every change to an account it cannot read but its removal. */}
            {!broken && (
              <>
                <Button disabled={act.busy} onClick={() => setDialog("rename")}>
                  {t("rename")}
                </Button>
                {on && (
                  <Button disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, true))}>
                    {t("turnOff")}
                  </Button>
                )}
                <AccountEdit
                  account={a}
                  name={props.name}
                  onChanged={() => {
                    props.onSaved(t("saved"));
                    props.reload();
                  }}
                  disabled={act.busy}
                />
              </>
            )}
            <span class="account-change-remove">
              <Button variant="danger" disabled={act.busy} onClick={() => setDialog("remove")}>
                {t("remove")}
              </Button>
            </span>
          </div>
        )}
        {act.error && <Notice tone="danger">{p.text(act.error, props.ids)}</Notice>}
      </section>
      {!broken && (
        <details class="account-more card" data-part="account-more">
          <summary>
            <span class="account-more-chevron" aria-hidden="true">
              <Icon name="chevron" size={14} />
            </span>
            <span>{t("more")}</span>
          </summary>
          <div class="account-more-body">
            <AccountSettings account={a} />
            <AccountTools account={a} onChanged={props.reload} />
          </div>
        </details>
      )}
      {dialog === "rename" && (
        <RenameDialog
          account={a}
          name={props.name}
          close={() => setDialog(undefined)}
          saved={() => {
            setDialog(undefined);
            props.onSaved(t("saved"));
            props.reload();
          }}
        />
      )}
      {dialog === "remove" && (
        <ConfigDialog title={t("removeTitle", { name: props.name })} close={() => setDialog(undefined)}>
          <p>{t("removeText")}</p>
          {act.error && <Notice tone="danger">{p.text(act.error, props.ids)}</Notice>}
          <div class="actions">
            <Button
              variant="danger"
              disabled={act.busy}
              onClick={() =>
                act.run(
                  () => api.removeAccount(a.id),
                  () => {
                    location.hash = "#/accounts";
                  },
                )
              }
            >
              {t("removeNow")}
            </Button>
            <Button onClick={() => setDialog(undefined)}>{t("cancel")}</Button>
          </div>
        </ConfigDialog>
      )}
    </>
  );
}

/** The account's name as a person gives it: PATCH { label }; an empty name takes it away. */
function RenameDialog(props: { account: Account; name: string; close: () => void; saved: () => void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const [value, setValue] = useState(props.account.label ?? "");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const save = async () => {
    const why = nameProblem(value);
    if (why) return setError(why);
    setBusy(true);
    setError(undefined);
    const r = await api.setAccount(props.account.id, { label: value.trim() });
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const failed = problem(r);
    if (failed !== undefined) return setError(p.text(failed, accountIdentities(props.account)));
    props.saved();
  };
  return (
    <ConfigDialog title={t("renameTitle", { name: props.name })} close={props.close}>
      <form
        class="stack"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <label class="field" data-part="field">
          <span class="label">{t("name")}</span>
          <input
            data-slot="input"
            type="text"
            autocomplete="off"
            value={value}
            aria-invalid={error ? "true" : undefined}
            onInput={(e) => {
              setValue(e.currentTarget.value);
              setError(undefined);
            }}
          />
        </label>
        {error && <Notice tone="danger">{error}</Notice>}
        <div class="actions">
          <Button type="submit" variant="primary" disabled={busy}>
            {t("save")}
          </Button>
          <Button onClick={props.close}>{t("cancel")}</Button>
        </div>
      </form>
    </ConfigDialog>
  );
}

export const accountPage = (): PageDef => ({
  id: "account",
  dest: "accounts",
  under: "accounts",
  title: t("title"),
  // #/accounts/<id> and #/accounts/<id>/sign-in: the links every account's row and the other pages use.
  addresses: ["accounts/"],
  render: () => <AccountPage />,
});
