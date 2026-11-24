// Accounts: the list, in the four groups the first screen counts by (need you, waiting, serving,
// turned off), and one account's own page. The list is lines, not a table: a name, its provider,
// what is true of it in words, and the one button that fixes it.

import { createContext } from "preact";
import { useContext, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type Account, type LoginMethod, problem } from "../../api";
import type { Reply } from "../../host";
import { LoginFlow } from "../../loginFlow";
import { AccountEdit, AccountSettings } from "../../pages/accountSettings";
import { AccountTools } from "../../pages/accountTools";
import { configAddress } from "../../pages/configSource.helpers";
import { accountIdentities, usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { accountHref, useCore } from "./core";
import { useRead, useWindows, WINDOWS_AT_ONCE, type Windows } from "./data";
import { FixButton } from "./fix";
import { Icon } from "./icons";
import {
  type Condition,
  clock,
  conditionSentence,
  conditionWord,
  fixFor,
  GROUPS,
  type Group,
  moment,
  num,
  percent,
  plural,
  providerName,
} from "./lib";
import {
  Asking,
  BarList,
  Btn,
  Confirm,
  Note,
  Onward,
  Section,
  StateMark,
  Trouble,
  WindowBrief,
  WindowLines,
} from "./parts";
import { RequestList } from "./requests";

const GROUP_TITLE: Record<Group, string> = {
  needs: "Need you",
  waiting: "Waiting",
  serving: "Serving",
  off: "Turned off",
};

const PAGE = 60;
const WEEK = "since=7d";

/** Case-insensitive search over what the list shows of an account. */
function matches(a: Account, c: Condition, text: string): boolean {
  const q = text.trim().toLowerCase();
  if (!q) return true;
  return [a.id, a.label, a.extension, providerName(a.extension), conditionWord(c), a.kind]
    .filter((s): s is string => !!s)
    .some((s) => s.toLowerCase().includes(q));
}

// The list reads the windows once and its lines share the reading.
const WindowsContext = createContext<{ windows: Map<string, Windows> }>({ windows: new Map() });
const useWindowsShared = () => useContext(WindowsContext);

function AccountLine(props: { a: Account; c: Condition; primary: boolean; requests?: number }) {
  const core = useCore();
  const p = usePrivacy();
  const { a, c } = props;
  const { windows } = useWindowsShared();
  const w = windows.get(a.id);
  const name = core.naming.name(a.id);
  return (
    <li class="db-account" data-group={c.group}>
      <StateMark c={c} label={conditionWord(c)} />
      <div class="db-account-who">
        <a class="db-name" href={accountHref(a.id)} title={name}>
          {name}
        </a>
        <span class="db-account-provider">
          {providerName(a.extension)}
          {a.label ? ` · ${p.id(a.label)}` : ""}
        </span>
      </div>
      <div class="db-account-what">
        {c.group === "serving" && c.resting.length === 0 ? (
          <WindowBrief w={w} now={core.now} account={name} />
        ) : (
          <span>{conditionSentence(a, c, core.now)}</span>
        )}
      </div>
      <div class="db-account-week">{props.requests ? `${num(props.requests)} this week` : ""}</div>
      <div class="db-account-fix">
        <FixButton account={a} c={c} primary={props.primary} quiet={c.group !== "needs"} />
      </div>
    </li>
  );
}

function AccountList() {
  const { api } = useKit();
  const core = useCore();
  const [search, setSearch] = useState("");
  const [more, setMore] = useState<Partial<Record<Group, number>>>({});
  const week = useRead(() => api.usage(WEEK));
  const list = core.list;

  const groups = useMemo(() => {
    const by: Record<Group, Account[]> = { needs: [], waiting: [], serving: [], off: [] };
    for (const a of list ?? []) {
      const c = core.conditionOf(a);
      if (matches(a, c, search)) by[c.group].push(a);
    }
    return by;
    // conditionOf changes with the list and the clock, which core carries.
  }, [list, search, core.conditionOf]);

  // Windows are read for the accounts the list shows first: those that wait, then those that serve.
  const asked = useMemo(
    () => [...groups.waiting, ...groups.serving].slice(0, WINDOWS_AT_ONCE),
    [groups.waiting, groups.serving],
  );
  const read = useWindows(list ? asked : undefined);
  const windows = useMemo(() => ({ windows: read.windows }), [read.windows]);

  const requests = new Map<string, number>();
  for (const g of week.data?.accounts ?? []) requests.set(g.name, g.total_requests);

  if (!list) {
    return (
      <article class="db-page">
        <header class="db-page-head">
          <h1>Accounts</h1>
        </header>
        {core.accounts.error ? (
          <Trouble retry={core.reload}>
            pluxy did not answer, so there is no list to show: {core.naming.mask(core.accounts.error)}.
          </Trouble>
        ) : (
          <Asking lines={6}>Asking pluxy for its accounts…</Asking>
        )}
      </article>
    );
  }

  const n = core.counts;
  const providers = new Set(list.map((a) => a.extension ?? "")).size;
  const shown = GROUPS.reduce((sum, g) => sum + groups[g].length, 0);
  let primaryGiven = false;

  return (
    <article class="db-page">
      <header class="db-page-head">
        <div>
          <h1>Accounts</h1>
          <p class="db-lead">
            {list.length === 0
              ? "pluxy has no accounts yet. An account is a subscription or an API key it sends requests through."
              : `${plural(list.length, "account")} at ${plural(providers, "provider")}. ${[
                  n?.serving ? `${num(n.serving)} serving` : "",
                  n?.needs ? `${num(n.needs)} ${n.needs === 1 ? "needs" : "need"} you` : "",
                  n?.waiting ? `${num(n.waiting)} waiting` : "",
                  n?.off ? `${num(n.off)} turned off` : "",
                ]
                  .filter(Boolean)
                  .join(", ")}.`}
          </p>
        </div>
        <div class="db-page-actions">
          {list.length > 6 && (
            <label class="db-search">
              <Icon name="search" />
              <span class="db-sr">Find an account</span>
              <input
                type="search"
                placeholder="Find an account"
                value={search}
                onInput={(e) => setSearch(e.currentTarget.value)}
              />
            </label>
          )}
          <Btn kind={n?.needs ? "plain" : "primary"} icon="plus" href="#/add-account">
            Add an account
          </Btn>
        </div>
      </header>

      {core.accounts.error && (
        <Trouble retry={core.reload} quiet>
          This is the list as pluxy last gave it. It is not answering now:{" "}
          {core.naming.mask(core.accounts.error)}
        </Trouble>
      )}

      {list.length > 0 && shown === 0 && (
        <p class="db-quiet">
          No account matches “{search}”.{" "}
          <button type="button" class="db-linkish" onClick={() => setSearch("")}>
            Show them all
          </button>
        </p>
      )}

      <WindowsContext.Provider value={windows}>
        {GROUPS.map((g) => {
          const all = groups[g];
          if (all.length === 0) return null;
          const limit = more[g] ?? PAGE;
          const rows = all.slice(0, limit);
          return (
            <Section key={g} title={`${GROUP_TITLE[g]} (${num(all.length)})`} class="db-group">
              <ul class="db-accounts">
                {rows.map((a) => {
                  const c = core.conditionOf(a);
                  const primary = g === "needs" && !primaryGiven && !!fixFor(a, c);
                  if (primary) primaryGiven = true;
                  return (
                    <AccountLine key={a.id} a={a} c={c} primary={primary} requests={requests.get(a.id)} />
                  );
                })}
              </ul>
              {all.length > rows.length && (
                <p class="db-more">
                  Showing {num(rows.length)} of {num(all.length)}.{" "}
                  <button
                    type="button"
                    class="db-linkish"
                    onClick={() => setMore({ ...more, [g]: limit + PAGE * 4 })}
                  >
                    Show {num(Math.min(PAGE * 4, all.length - rows.length))} more
                  </button>
                </p>
              )}
            </Section>
          );
        })}
      </WindowsContext.Provider>
    </article>
  );
}

/** Signing in to an account again: the provider's ways to sign in, then the kit's own sign-in flow. */
function SignIn(props: { account: Account; onDone: () => void }) {
  const { api } = useKit();
  const core = useCore();
  const methods = useRead(api.loginMethods);
  const [chosen, setChosen] = useState<LoginMethod>();
  const [attempt, setAttempt] = useState(0);
  const available =
    methods.data?.extensions.find((e) => e.extension === props.account.extension)?.methods ?? [];
  const only = available.length === 1 ? available[0] : undefined;
  const method = chosen ?? only;
  return (
    <Section title="Sign in again" class="db-signin-again">
      {methods.loading && (
        <Asking lines={1}>Asking how {providerName(props.account.extension)} signs in…</Asking>
      )}
      {methods.error && <Trouble retry={methods.reload}>{core.naming.mask(methods.error)}</Trouble>}
      {methods.data && available.length === 0 && (
        <Note tone="warn">
          {providerName(props.account.extension)} offers no way to sign in from here. Is its provider running?{" "}
          <a href="#/extensions">Open Extensions</a>
        </Note>
      )}
      {!method && available.length > 1 && (
        <div class="db-choices">
          <p>Choose how to sign in:</p>
          {available.map((m) => (
            <Btn key={m.method} onClick={() => setChosen(m)}>
              {m.label || m.method}
            </Btn>
          ))}
        </div>
      )}
      {method && (
        <div class="db-legacy db-legacy-inset">
          <LoginFlow
            key={`${method.method}/${attempt}`}
            extension={props.account.extension ?? ""}
            method={method}
            existingAccountId={props.account.id}
            keptInputs={Object.keys(props.account.settings ?? {})}
            onSuccess={props.onDone}
            onReset={() => {
              setChosen(undefined);
              setAttempt((x) => x + 1);
            }}
          />
        </div>
      )}
    </Section>
  );
}

function AccountPage(props: { id: string; part: string }) {
  const { api, unauthorized } = useKit();
  const core = useCore();
  const p = usePrivacy();
  const a = core.list?.find((x) => x.id === props.id);
  const one = useMemo(() => (a ? [a] : undefined), [a]);
  const { windows, refresh, reading } = useWindows(one);
  const week = useRead(() => api.usage(`${WEEK}&account=${encodeURIComponent(props.id)}`), 0, props.id);
  const recent = useRead(
    () => api.usageEventsPage(`before=0&limit=6&since=7d&account=${encodeURIComponent(props.id)}`),
    0,
    props.id,
  );
  const keys = useRead(api.clientKeys);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [tools, setTools] = useState(false);
  const top = useRef<HTMLHeadingElement>(null);
  // A new page is read from its top: the heading takes the focus the list's link had.
  useEffect(() => {
    top.current?.focus();
  }, [props.id]);

  if (!core.list) {
    return (
      <article class="db-page">
        <p class="db-back">
          <a href="#/accounts">
            <Icon name="arrowLeft" /> Accounts
          </a>
        </p>
        {core.accounts.error ? (
          <Trouble retry={core.reload}>{core.naming.mask(core.accounts.error)}</Trouble>
        ) : (
          <Asking lines={5}>Asking pluxy about this account…</Asking>
        )}
      </article>
    );
  }
  if (!a) {
    return (
      <article class="db-page">
        <p class="db-back">
          <a href="#/accounts">
            <Icon name="arrowLeft" /> Accounts
          </a>
        </p>
        <h1>No such account.</h1>
        <p class="db-lead">pluxy has no account with this name. It may have been removed.</p>
      </article>
    );
  }

  const c = core.conditionOf(a);
  const ids = accountIdentities(a);
  const name = core.naming.name(a.id);
  const w = windows.get(a.id);
  const u = week.data;
  const fix = fixFor(a, c);
  const signingIn = props.part === "sign-in" && a.source === "login";
  const run = async (call: () => Promise<Reply<unknown>>, then?: () => void) => {
    setBusy(true);
    setError(undefined);
    const r = await call();
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why) return setError(p.text(why, ids));
    core.reload();
    then?.();
  };
  const fromConfig = a.source === "config";

  return (
    <article class="db-page db-account-page">
      <p class="db-back">
        <a href="#/accounts">
          <Icon name="arrowLeft" /> Accounts
        </a>
      </p>
      <header class="db-opening">
        <p class="db-dateline">
          {providerName(a.extension)}
          {a.label ? ` · ${p.id(a.label)}` : ""}
        </p>
        <h1 class="db-title" tabIndex={-1} ref={top}>
          {name}
        </h1>
        <p class="db-state-line" data-group={c.group}>
          <StateMark c={c} />
          <span>{conditionSentence(a, c, core.now)}</span>
        </p>
        {c.why && c.group !== "serving" && c.group !== "off" && (
          <p class="db-quote">
            {providerName(a.extension)} or pluxy said: “{p.text(c.why, ids)}”
          </p>
        )}
        {c.kind === "needs_login" && fromConfig && (
          <p class="db-lead">
            Its credential is set in pluxy's config, so the new one goes there.{" "}
            <a href={configAddress("source", a.id)}>Open it in the config</a>
          </p>
        )}
        {c.kind === "broken" && (
          <p class="db-lead">
            Fix or replace the file and restart pluxy, or remove the account, which deletes the file.
          </p>
        )}
        {fix && fix !== "open" && !signingIn && (
          <div class="db-opening-actions">
            <FixButton account={a} c={c} primary />
          </div>
        )}
        {error && <Trouble quiet>{error}</Trouble>}
      </header>

      {signingIn && (
        <SignIn
          account={a}
          onDone={() => {
            core.reload();
            location.hash = accountHref(a.id);
          }}
        />
      )}

      {c.kind !== "broken" && (
        <div class="db-spread">
          <div>
            <Section
              title="Usage windows"
              aside={
                c.group !== "off" &&
                c.kind !== "needs_login" && (
                  <button type="button" class="db-linkish" onClick={refresh} disabled={reading}>
                    {w?.at ? `Read at ${clock(w.at)}. ` : ""}
                    Refresh
                  </button>
                )
              }
            >
              <WindowLines w={w} now={core.now} account={name} />
              {w?.plan && <p class="db-small">Plan: {w.plan}</p>}
            </Section>

            <Section title="About">
              <dl class="db-facts">
                <div>
                  <dt>Provider</dt>
                  <dd>
                    {providerName(a.extension)} <small class="db-code">{a.extension}</small>
                  </dd>
                </div>
                <div>
                  <dt>Signs in with</dt>
                  <dd>
                    {a.kind === "api_key" ? "an API key" : a.kind === "oauth" ? "a sign-in" : (a.kind ?? "—")}
                    , {fromConfig ? "set in pluxy's config" : "added here"}
                  </dd>
                </div>
                {a.label && (
                  <div>
                    <dt>Label</dt>
                    <dd>{p.id(a.label)}</dd>
                  </div>
                )}
                {a.priority !== undefined && (
                  <div>
                    <dt>Priority</dt>
                    <dd>{a.priority}</dd>
                  </div>
                )}
                {a.expires_at ? (
                  <div>
                    <dt>Sign-in expires</dt>
                    <dd>{moment(a.expires_at * 1000, core.now)}</dd>
                  </div>
                ) : null}
                {a.last_error && (
                  <div>
                    <dt>Last error</dt>
                    <dd>
                      {p.text(a.last_error.message, ids)}
                      {a.last_error_at ? ` (${moment(Date.parse(a.last_error_at), core.now)})` : ""}
                    </dd>
                  </div>
                )}
                {a.active_requests ? (
                  <div>
                    <dt>Right now</dt>
                    <dd>{plural(a.active_requests, "request")} under way</dd>
                  </div>
                ) : null}
                {a.created_at && (
                  <div>
                    <dt>Added</dt>
                    <dd>{moment(Date.parse(a.created_at), core.now)}</dd>
                  </div>
                )}
                {a.public_id && (
                  <div>
                    <dt>Known to clients as</dt>
                    <dd class="db-code">{a.public_id}</dd>
                  </div>
                )}
              </dl>
            </Section>
          </div>

          <div>
            <Section
              title="This week"
              aside={
                u && u.total_requests > 0 ? (
                  <Onward href={`#/usage?account=${encodeURIComponent(a.id)}`}>Its usage</Onward>
                ) : undefined
              }
            >
              {!u && week.loading && <Asking lines={3}>Adding up its week…</Asking>}
              {!u && week.error && <Trouble retry={week.reload}>{p.text(week.error, ids)}</Trouble>}
              {u &&
                (u.total_requests === 0 ? (
                  <p class="db-quiet">No requests went to it in the last seven days.</p>
                ) : (
                  <>
                    <p class="db-said">
                      {plural(u.total_requests, "request")}, and{" "}
                      {u.failure_count === 0 ? "all" : percent(u.success_count, u.total_requests)} went
                      through.
                    </p>
                    <BarList
                      label="Its requests by model"
                      max={5}
                      items={u.models
                        .filter((g) => g.total_requests > 0)
                        .sort((x, y) => y.total_requests - x.total_requests)
                        .map((g) => ({
                          id: g.name,
                          name: g.name,
                          value: g.total_requests,
                          failed: g.failure_count,
                        }))}
                    />
                  </>
                ))}
            </Section>

            {(recent.data?.events.length ?? 0) > 0 && (
              <Section title="Latest requests">
                <RequestList
                  own={a.id}
                  events={recent.data?.events ?? []}
                  naming={core.naming}
                  keys={keys.data?.keys}
                  now={core.now}
                />
              </Section>
            )}
          </div>
        </div>
      )}

      <Section title="Change it" class="db-change">
        {fromConfig ? (
          <p class="db-said">
            This account is set in pluxy's config, so it is changed there.{" "}
            <a href={configAddress("source", a.id)}>Open it in the config</a>
          </p>
        ) : (
          <div class="db-change-actions">
            {c.kind !== "broken" && (
              <>
                {c.kind !== "needs_login" && !signingIn && a.state === "reauth_required" && (
                  <Btn href={accountHref(a.id, "sign-in")}>Sign in again</Btn>
                )}
                {c.group !== "off" && c.kind !== "banned" && (
                  <Btn disabled={busy} onClick={() => run(() => api.setDisabled(a.id, true))}>
                    Turn it off
                  </Btn>
                )}
                <span class="db-legacy db-legacy-button">
                  <AccountEdit account={a} onChanged={core.reload} disabled={busy} />
                </span>
              </>
            )}
            <Confirm
              danger
              ask={`Remove ${name} for good?`}
              yes="Remove it"
              disabled={busy}
              onConfirm={() =>
                run(
                  () => api.removeAccount(a.id),
                  () => {
                    location.hash = "#/accounts";
                  },
                )
              }
            >
              Remove
            </Confirm>
          </div>
        )}
        {c.kind !== "broken" && (
          <>
            <button
              type="button"
              class="db-linkish db-disclose"
              aria-expanded={tools}
              onClick={() => setTools(!tools)}
            >
              <Icon name={tools ? "caretDown" : "caretRight"} />
              Settings, models and a test request
            </button>
            {tools && (
              <div class="db-legacy db-legacy-inset">
                <AccountSettings account={a} />
                <AccountTools key={a.id} account={a} onChanged={core.reload} />
              </div>
            )}
          </>
        )}
      </Section>
    </article>
  );
}

/** Accounts: the list at "accounts", one account's page at "accounts/<id>" (and "/sign-in" after it). */
export function Accounts(props: { rest: string }) {
  const [rawId, part = ""] = props.rest.split("/");
  if (!rawId) return <AccountList />;
  let id = rawId;
  try {
    id = decodeURIComponent(rawId);
  } catch {
    // A name that is not an encoded one is taken as written.
  }
  return <AccountPage key={id} id={id} part={part} />;
}
