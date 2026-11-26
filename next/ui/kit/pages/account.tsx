// One account on its own page (#/accounts/<id>; the header strip says "Accounts / <name>"): what
// state it is in and the one fix, its limits and facts, what it did this week and its latest
// requests, then the ways to change it. #/accounts/<id>/sign-in opens its sign-in under the
// opening. The list of every account is accounts.tsx.

import { useEffect, useMemo, useState } from "preact/hooks";
import {
  type Account,
  type LoginMethod,
  leftOut,
  problem,
  runningExtensions,
  type Status,
  type UsageGroup,
} from "../api";
import type { UsageSummary } from "../api/usage";
import type { UsageEventsReply } from "../api/usageEvents";
import type { Reply } from "../host";
import { LoginFlow } from "../loginFlow";
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
  LimitLine,
  PlanPill,
  providerName,
  RequestRow,
  StatePane,
  tryAgainWord,
  uncheckedReason,
  useNaming,
  useWindows,
  type Windows,
} from "../parts";
import { accountIdentities, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import { Button, Facts, Notice, PageHead, useKit, useLoad, when } from "../ui";
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
  manualResets: "Manual resets: {count} left",
  subscriptionEnds: "Subscription ends {date}",
  readAt: "Read at {time}",
  refresh: "Refresh",
  notRead: "Limits are read while the account takes requests.",
  reading: "Reading limits…",
  none: "No limits reported",
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
    if (accounts.phase === "loading") return <StatePane kind="loading" title={t("loading")} rows={5} />;
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

  return (
    <article class="account-page" data-part="account-page" data-group={c.group}>
      <PageHead title={props.name} />
      <RoundNotice
        reads={[accounts, status, week, latest]}
        unread={unchecked ? [t("statusUnread")] : []}
        ids={ids}
        onRetry={props.run}
      />
      <Opening
        account={a}
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
        <div class="account-columns">
          <div class="account-column">
            <section class="account-section" aria-labelledby="account-limits">
              <div class="account-section-head">
                <h2 id="account-limits">{t("limits")}</h2>
                {w?.at !== undefined && (
                  <span class="account-read muted">
                    {t("readAt", { time: clock(w.at) })}{" "}
                    <Button
                      onClick={() => {
                        forgetWindow(a.id);
                        setRefreshed((n) => n + 1);
                      }}
                    >
                      {t("refresh")}
                    </Button>
                  </span>
                )}
              </div>
              <LimitList w={w} name={props.name} ids={ids} now={now} />
            </section>
            <section class="account-section" aria-labelledby="account-about">
              <h2 id="account-about">{t("about")}</h2>
              <About account={a} all={props.all} ids={ids} />
            </section>
          </div>
          <div class="account-column">
            <section class="account-section" aria-labelledby="account-week">
              <div class="account-section-head">
                <h2 id="account-week">{t("week")}</h2>
                {(week.data?.total_requests ?? 0) > 0 && (
                  <a class="account-onward" href={`#/activity?account=${encodeURIComponent(a.id)}`}>
                    {t("seeRequests")} <span aria-hidden="true">→</span>
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
                <p class="muted">{t("loading")}</p>
              )}
            </section>
            {/* None this week: This week says so. One never read says it could not be read. */}
            {((latest.data?.events.length ?? 0) > 0 || (latest.phase === "failed" && !latest.data)) && (
              <section class="account-section" aria-labelledby="account-latest">
                <h2 id="account-latest">{t("latest")}</h2>
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
            )}
          </div>
        </div>
      )}
      <ChangeIt account={a} c={c} name={props.name} ids={ids} reload={reload} onSaved={setNotice} />
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

/** The opening: provider and plan, the condition and why, core's last error, and the one fix. */
function Opening(props: {
  account: Account;
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
  return (
    <header class="account-opening" data-part="account-opening">
      {/* An account pluxy cannot read has no provider it knows of: nothing to say here. */}
      {a.extension && (
        <p class="account-dateline muted">
          {providerName(a.extension)}
          {plan && (
            <>
              {" · "}
              <PlanPill plan={plan} />
            </>
          )}
        </p>
      )}
      <p class="account-state" data-group={c.group}>
        <GroupMark c={c} unchecked={props.unchecked} />
        <span>{p.text(reason, ids)}</span>
      </p>
      {error && (
        <p class="account-said muted">
          {a.last_error_at
            ? rich(t("saidAt", { error: p.text(error.message, ids) ?? "" }), { when: when(a.last_error_at) })
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
      {c.kind === "needs_login" && a.source === "config" && (
        <p>
          {t("configFix")}{" "}
          <a class="btn" data-part="button" data-variant="primary" href={configAddress("source", a.id)}>
            {t("openSettingsFile")}
          </a>
        </p>
      )}
      {fix && !sayWhat && !(props.signingIn && fix.href) && (
        <div class="actions">
          {fix.href ? (
            <a class="btn" data-part="button" data-variant={fix.variant} href={fix.href}>
              {fix.label}
            </a>
          ) : (
            <Button
              variant={fix.variant}
              disabled={props.busy}
              onClick={() => fix.run && props.onRun(fix.run)}
            >
              {fix.label}
            </Button>
          )}
        </div>
      )}
    </header>
  );
}

/** Every limit the provider reports (but its manual resets), then the resets left and the end of the plan. */
function LimitList(props: { w?: Windows; name: string; ids: readonly string[]; now: number }) {
  const p = usePrivacy();
  const w = props.w;
  if (!w) return <p class="muted">{t("notRead")}</p>;
  return (
    <div class="account-limits" data-part="limits" data-state={w.state}>
      {w.state === "reading" && <p class="muted">{t("reading")}</p>}
      {w.state === "none" && <p class="muted">{t("none")}</p>}
      {w.state === "failed" && (
        <p class="muted" title={p.text(w.error, props.ids)}>
          {t("unavailable")}
        </p>
      )}
      {w.limits.map((l) => (
        <LimitLine key={l.id} limit={l} account={props.name} now={props.now} long />
      ))}
      {w.resets !== undefined && <p>{t("manualResets", { count: w.resets.toLocaleString() })}</p>}
      {w.endsAt !== undefined && <p>{rich(t("subscriptionEnds"), { date: when(w.endsAt) })}</p>}
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
      <p>{t("weekLine", { count: props.total, percent })}</p>
      <ul class="account-bars" aria-label={t("byModel")}>
        {top.map((m) => (
          <li key={m.name} class="account-bar">
            <span class="account-bar-name mono" title={p.text(m.name, props.ids)}>
              {p.text(m.name, props.ids)}
            </span>
            <span class="account-bar-count">{m.total_requests.toLocaleString()}</span>
            <span
              class="account-bar-fill"
              aria-hidden="true"
              style={{ width: `${(m.total_requests / most) * 100}%` }}
            />
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
      class="account-section account-sign-in"
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
          keptInputs={Object.keys(a.settings ?? {})}
          onSuccess={props.onDone}
          onReset={() => {
            setChosen(undefined);
            setAttempt((n) => n + 1);
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
    <section class="account-section account-change" aria-labelledby="account-change">
      <h2 id="account-change">{t("change")}</h2>
      {a.source === "config" ? (
        <p>
          {t("setInFile")} <a href={configAddress("source", a.id)}>{t("openSettingsFile")}</a>
        </p>
      ) : (
        <div class="actions">
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
              <AccountEdit account={a} name={props.name} onChanged={props.reload} disabled={act.busy} />
            </>
          )}
          <Button variant="danger" disabled={act.busy} onClick={() => setDialog("remove")}>
            {t("remove")}
          </Button>
        </div>
      )}
      {act.error && <Notice tone="danger">{p.text(act.error, props.ids)}</Notice>}
      {!broken && (
        <details class="account-more" data-part="account-more">
          <summary>{t("more")}</summary>
          <AccountSettings account={a} />
          <AccountTools account={a} onChanged={props.reload} />
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
    </section>
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
