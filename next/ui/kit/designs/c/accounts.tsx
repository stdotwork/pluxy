// Accounts: the end of the line. The list is one lane per provider, each account a row with its
// state in words, how much of each usage window is left and when it resets, its share of the
// requests, and the one action that fixes it. One account's page says the same in full, shows
// what reaches it, and keeps today's settings and tools.

import { useEffect, useRef, useState } from "preact/hooks";
import { type Account, leftOut } from "../../api";
import { AccountEdit, AccountSettings } from "../../pages/accountSettings";
import { AccountTools } from "../../pages/accountTools";
import { ConfirmButton, Notice, useAction, useKit } from "../../ui";
import { SeriesChart } from "./chart";
import {
  type Fleet,
  forgetWindows,
  useAsk,
  useConditions,
  useFleet,
  useNow,
  useResource,
  useWindows,
} from "./data";
import { Icon } from "./icons";
import {
  type Address,
  answered,
  CONDITION_RANK,
  type Condition,
  clockExact,
  compact,
  count,
  hashFor,
  latency,
  RANGES,
  share,
} from "./lib";
import { useRange } from "./overview";
import {
  Btn,
  FixButton,
  Note,
  PageHead,
  ProviderTile,
  Refresh,
  Segmented,
  StateMark,
  WindowMeter,
} from "./parts";
import { RequestList } from "./paths";

type Show = "all" | "attention" | "available" | "disabled";

const needsSomeone = (c: Condition | undefined) => !!c && c.key !== "available" && c.key !== "disabled";

function matches(a: Account, c: Condition | undefined, providerName: string, text: string): boolean {
  const terms = text.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const hay = [
    a.id,
    a.label,
    a.kind,
    a.extension,
    providerName,
    c?.word,
    a.last_error?.code,
    a.last_error?.message,
  ]
    .filter(Boolean)
    .join("\n")
    .toLowerCase();
  return terms.every((t) => hay.includes(t));
}

/** How many rows a lane shows before it asks. */
const LANE_ROWS = 24;

export function Accounts(props: { address: Address }) {
  const id = props.address.parts[1];
  return id ? <AccountPage id={id} address={props.address} /> : <AccountList address={props.address} />;
}

function AccountList(props: { address: Address }) {
  const { api, host } = useKit();
  const ask = useAsk();
  const fleet = useFleet();
  const now = useNow();
  const conditions = useConditions(fleet, now);
  const range = useRange(props.address);
  const [search, setSearch] = useState("");
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const show = (props.address.params.get("show") ?? "all") as Show;
  const usage = useResource(
    `accounts-usage:${range.id}`,
    () => ask(api.usage(`since=${range.id}&buckets=1`)),
    60_000,
  );

  const accounts = fleet.accounts;
  const all = accounts ?? [];
  const counts = {
    all: all.length,
    attention: all.filter((a) => needsSomeone(conditions.get(a.id))).length,
    available: all.filter((a) => conditions.get(a.id)?.serving).length,
    disabled: all.filter((a) => conditions.get(a.id)?.key === "disabled").length,
  };
  const visible = all.filter((a) => {
    const c = conditions.get(a.id);
    if (show === "attention" && !needsSomeone(c)) return false;
    if (show === "available" && !c?.serving) return false;
    if (show === "disabled" && c?.key !== "disabled") return false;
    return matches(a, c, fleet.providers.of(a.extension).name, search);
  });
  const requestsOf = new Map((usage.data?.accounts ?? []).map((g) => [g.name, g]));
  const total = usage.data?.total_requests ?? 0;
  const most = Math.max(1, ...(usage.data?.accounts ?? []).map((g) => g.total_requests));

  const lanes = fleet.providers.list
    .map((p) => ({ p, list: visible.filter((a) => (a.extension ?? "") === p.id) }))
    .filter((lane) => lane.list.length > 0);
  const stray = visible.filter((a) => !a.extension);
  if (stray.length > 0) lanes.push({ p: fleet.providers.of(undefined), list: stray });
  for (const lane of lanes) {
    lane.list.sort((a, b) => {
      const ca = conditions.get(a.id);
      const cb = conditions.get(b.id);
      return (
        (ca ? CONDITION_RANK[ca.key] : 9) - (cb ? CONDITION_RANK[cb.key] : 9) ||
        (requestsOf.get(b.id)?.total_requests ?? 0) - (requestsOf.get(a.id)?.total_requests ?? 0) ||
        a.id.localeCompare(b.id)
      );
    });
  }
  const shownRows = lanes.flatMap((lane) =>
    open.has(lane.p.id) ? lane.list : lane.list.slice(0, LANE_ROWS),
  );
  const windows = useWindows(
    shownRows
      .filter(
        (a) =>
          !["needs_login", "broken", "no_provider", "disabled"].includes(conditions.get(a.id)?.key ?? ""),
      )
      .map((a) => a.id),
  );

  const sentence = !accounts
    ? fleet.error
      ? "The accounts could not be read."
      : "Reading accounts…"
    : all.length === 0
      ? "No accounts yet."
      : `${counts.available} of ${all.length} available` +
        (counts.attention ? `, ${counts.attention} ${counts.attention === 1 ? "needs" : "need"} you.` : ".");

  return (
    <div class="dc-page" data-view="accounts">
      <PageHead title="Accounts" sub={sentence}>
        <Refresh
          at={fleet.at}
          now={now}
          busy={fleet.loading}
          onRefresh={() => {
            forgetWindows();
            fleet.reload();
            usage.reload();
          }}
        />
        <Btn kind="primary" icon="plus" href="#/add-account">
          Add account
        </Btn>
      </PageHead>

      {fleet.error && (
        <Note
          tone="danger"
          action={
            <Btn size="small" onClick={fleet.reload}>
              Try again
            </Btn>
          }
        >
          {accounts ? "This shows the accounts as they were. " : ""}Reading accounts failed: {fleet.error}.
        </Note>
      )}

      {!accounts ? (
        !fleet.error && <LaneSkeleton />
      ) : all.length === 0 ? (
        <section class="dc-card dc-empty">
          <h2>No accounts yet</h2>
          <p>
            An account is a subscription you sign in to, or an API key. pluxy sends each request to one of
            them, and falls back to the next when one cannot take it.
          </p>
          <Btn kind="primary" icon="plus" href="#/add-account">
            Add account
          </Btn>
        </section>
      ) : (
        <>
          <div class="dc-toolbar">
            <Segmented
              label="Show"
              value={show}
              options={[
                { value: "all", label: `All ${counts.all}` },
                { value: "attention", label: `Need you ${counts.attention}` },
                { value: "available", label: `Available ${counts.available}` },
                { value: "disabled", label: `Disabled ${counts.disabled}` },
              ]}
              onChange={(v) => {
                location.hash = hashFor("accounts", {
                  show: v === "all" ? undefined : v,
                  range: props.address.params.get("range") ?? undefined,
                });
              }}
            />
            <label class="dc-field dc-search">
              <Icon name="search" size={16} />
              <span class="dc-sr">Find an account</span>
              <input
                type="search"
                placeholder="Find by name, provider or state"
                value={search}
                onInput={(e) => setSearch(e.currentTarget.value)}
              />
            </label>
            <span class="dc-toolbar-end">
              <span class="dc-muted">Requests in</span>
              <Segmented
                label="Requests in"
                value={range.id}
                options={RANGES.map((r) => ({ value: r.id, label: r.id, title: r.words }))}
                onChange={(v) => {
                  host.prefs.set("design-c.range", v);
                  location.hash = hashFor("accounts", { show: show === "all" ? undefined : show, range: v });
                }}
              />
            </span>
          </div>
          {usage.error && (
            <Note tone="warn">The requests could not be read, so the shares are missing: {usage.error}.</Note>
          )}
          {lanes.length === 0 && (
            <section class="dc-card dc-empty">
              <h2>No account matches</h2>
              <p>Nothing fits {search ? `“${search}”` : "this filter"}.</p>
              <Btn
                onClick={() => {
                  setSearch("");
                  location.hash = hashFor("accounts");
                }}
              >
                Show all accounts
              </Btn>
            </section>
          )}
          {lanes.map(({ p, list }) => {
            const laneRequests = list.reduce((n, a) => n + (requestsOf.get(a.id)?.total_requests ?? 0), 0);
            const rows = open.has(p.id) ? list : list.slice(0, LANE_ROWS);
            return (
              <section
                key={p.id}
                class="dc-card dc-lane"
                data-slot-p={p.slot}
                aria-label={`${p.name} accounts`}
              >
                <header class="dc-lane-head">
                  <ProviderTile p={p} />
                  <h2 class="dc-h2">{p.name}</h2>
                  <span class="dc-muted">
                    {list.length} {list.length === 1 ? "account" : "accounts"}
                    {usage.data
                      ? ` · ${count(laneRequests)} requests, ${share(laneRequests, total)} of all`
                      : ""}
                  </span>
                </header>
                <ul class="dc-rows">
                  {rows.map((a) => {
                    const c = conditions.get(a.id) as Condition;
                    const g = requestsOf.get(a.id);
                    const w = windows.get(a.id);
                    const limits =
                      w?.state === "ok" ? w.reply.quota.limits.filter((l) => !l.feature).slice(0, 2) : [];
                    const note = c.key === "available" ? undefined : c.why;
                    return (
                      <li key={a.id} class="dc-row" data-tone={c.tone}>
                        <div class="dc-row-who">
                          <a
                            class="dc-row-name dc-ellipsis"
                            href={`#/accounts/${encodeURIComponent(a.id)}`}
                            title={fleet.mask(a.id)}
                          >
                            {fleet.mask(a.id)}
                          </a>
                          <span class="dc-row-sub dc-ellipsis">
                            {[
                              a.label ? fleet.mask(a.label) : undefined,
                              a.source === "config" ? "set in config" : "signed in here",
                            ]
                              .filter(Boolean)
                              .join(" · ")}
                          </span>
                        </div>
                        <div class="dc-row-state">
                          <StateMark c={c} now={now} />
                          {note && (
                            <span class="dc-row-sub dc-ellipsis" title={fleet.mask(note)}>
                              {fleet.mask(note)}
                            </span>
                          )}
                        </div>
                        <div class="dc-row-wins">
                          {w?.state === "loading" && (
                            <span class="dc-skel dc-row-skel" role="status">
                              <span class="dc-sr">Reading usage windows</span>
                            </span>
                          )}
                          {limits.map((l) => (
                            <WindowMeter key={l.id} limit={l} now={now} />
                          ))}
                          {w && w.state !== "loading" && limits.length === 0 && (
                            <span class="dc-row-sub">
                              {w.state === "none" && w.why ? "Windows not read" : "No usage windows"}
                            </span>
                          )}
                        </div>
                        <div
                          class="dc-row-use"
                          title={
                            g
                              ? `${count(g.total_requests)} requests, ${count(g.failure_count)} failed`
                              : undefined
                          }
                        >
                          <span class="dc-num">{g ? count(g.total_requests) : usage.data ? "0" : "–"}</span>
                          {g && g.failure_count > 0 && (
                            <span class="dc-row-failed dc-num">{count(g.failure_count)} failed</span>
                          )}
                          {g && (
                            <span class="dc-bar">
                              <i
                                style={{ width: `${((g.total_requests - g.failure_count) / most) * 100}%` }}
                              />
                              {g.failure_count > 0 && (
                                <i data-failed="" style={{ width: `${(g.failure_count / most) * 100}%` }} />
                              )}
                            </span>
                          )}
                        </div>
                        <div class="dc-row-act">
                          <FixButton account={a} c={c} size="small" />
                        </div>
                      </li>
                    );
                  })}
                </ul>
                {list.length > rows.length && (
                  <button
                    type="button"
                    class="dc-lane-more"
                    onClick={() => setOpen(new Set([...open, p.id]))}
                  >
                    Show {list.length - rows.length} more
                  </button>
                )}
              </section>
            );
          })}
        </>
      )}
    </div>
  );
}

function LaneSkeleton() {
  return (
    <section class="dc-card dc-lane" role="status">
      <span class="dc-sr">Reading accounts</span>
      <header class="dc-lane-head">
        <span class="dc-skel dc-skel-tile" />
        <span class="dc-skel dc-skel-word" />
      </header>
      <ul class="dc-rows" aria-hidden="true">
        {[0, 1, 2, 3].map((i) => (
          <li key={i} class="dc-row">
            <span class="dc-skel dc-skel-line" />
            <span class="dc-skel dc-skel-word" />
            <span class="dc-skel dc-skel-line" />
            <span class="dc-skel dc-skel-word" />
          </li>
        ))}
      </ul>
    </section>
  );
}

// ---- one account ----------------------------------------------------------------------------

function AccountPage(props: { id: string; address: Address }) {
  const { api, host } = useKit();
  const ask = useAsk();
  const fleet = useFleet();
  const now = useNow();
  const conditions = useConditions(fleet, now);
  const range = useRange(props.address);
  const a = fleet.account(props.id);
  const q = `account=${encodeURIComponent(props.id)}`;
  const usage = useResource(
    `account-usage:${props.id}:${range.id}`,
    () => ask(api.usage(`since=${range.id}&${range.series}&${q}`)),
    60_000,
  );
  const recent = useResource(`account-recent:${props.id}:${range.id}`, () =>
    ask(api.usageEventsPage(`before=0&limit=6&since=${range.id}&${q}`)),
  );

  // Arriving to sign in again: the tools that do it are brought into view.
  const tools = useRef<HTMLElement>(null);
  const wantsSignIn = props.address.params.get("do") === "signin";
  useEffect(() => {
    if (wantsSignIn && a) tools.current?.scrollIntoView({ block: "start" });
  }, [wantsSignIn, a]);

  if (!fleet.accounts) {
    return (
      <div class="dc-page">
        <PageHead
          title={props.id}
          crumb={{ href: "#/accounts", label: "Accounts" }}
          sub={fleet.error ? undefined : "Reading the account…"}
        />
        {fleet.error ? (
          <Note
            tone="danger"
            action={
              <Btn size="small" onClick={fleet.reload}>
                Try again
              </Btn>
            }
          >
            Reading accounts failed: {fleet.error}.
          </Note>
        ) : (
          <LaneSkeleton />
        )}
      </div>
    );
  }
  if (!a) {
    return (
      <div class="dc-page">
        <PageHead title="No such account" crumb={{ href: "#/accounts", label: "Accounts" }} />
        <section class="dc-card dc-empty">
          <h2>Core has no account with this ID</h2>
          <p>It may have been removed. Its requests stay in the record: Activity still shows them.</p>
          <Btn href={hashFor("activity", { account: props.id })}>See its requests</Btn>
        </section>
      </div>
    );
  }
  const c = conditions.get(a.id) as Condition;
  const p = fleet.providers.of(a.extension);
  const s = usage.data;
  const models = (a.models ?? []).map((m) => (typeof m === "string" ? m : (m.alias ?? m.name)));
  const most = Math.max(1, ...(s?.models ?? []).map((m) => m.total_requests));
  return (
    <div class="dc-page" data-view="account">
      <PageHead title={fleet.mask(a.id)} crumb={{ href: "#/accounts", label: "Accounts" }}>
        <Refresh
          at={fleet.at}
          now={now}
          busy={fleet.loading}
          onRefresh={() => {
            forgetWindows(a.id);
            fleet.reload();
            usage.reload();
            recent.reload();
          }}
        />
      </PageHead>

      <section class="dc-card dc-acct-hero" data-slot-p={p.slot} data-tone={c.tone} aria-label="State">
        <div class="dc-acct-id">
          <ProviderTile p={p} />
          <span>
            {p.name}
            {a.label ? <span class="dc-muted"> · {fleet.mask(a.label)}</span> : null}
            <span class="dc-muted">
              {" "}
              · {a.source === "config" ? "set in core's config" : "signed in here"}
            </span>
          </span>
        </div>
        <div class="dc-acct-state">
          <StateMark c={c} now={now} />
          <p>
            {c.key === "available"
              ? "Requests for its models can go to this account."
              : fleet.mask(c.why ?? "")}
          </p>
          <FixButton account={a} c={c} primary />
        </div>
        {/* Where the account sits on the line: what reaches it. */}
        <div class="dc-acct-line">
          <span class="dc-h3">Serves</span>
          <ul class="dc-tags">
            {models.length === 0 && <li class="dc-muted">Every model its provider offers</li>}
            {models.slice(0, 8).map((m) => (
              <li key={m}>
                <a
                  class="dc-tag dc-ellipsis"
                  href={hashFor("routing", { model: m })}
                  title={`Where ${m} would go now`}
                >
                  {m}
                </a>
              </li>
            ))}
            {models.length > 8 && <li class="dc-muted">and {models.length - 8} more</li>}
          </ul>
        </div>
      </section>

      <div class="dc-acct-grid">
        <WindowsCard account={a} c={c} now={now} />
        <section class="dc-card" aria-label={`Requests in ${range.words}`}>
          <div class="dc-card-head">
            <h2>Requests</h2>
            <div class="dc-head-tools">
              <Segmented
                label="Range"
                value={range.id}
                options={RANGES.map((r) => ({ value: r.id, label: r.id, title: r.words }))}
                onChange={(v) => {
                  host.prefs.set("design-c.range", v);
                  location.hash = hashFor(`accounts/${encodeURIComponent(a.id)}`, { range: v });
                }}
              />
            </div>
          </div>
          {usage.error && !s ? (
            <p class="dc-muted dc-card-pad">The requests could not be read: {usage.error}.</p>
          ) : !s ? (
            <p class="dc-muted dc-card-pad" role="status">
              Reading requests…
            </p>
          ) : s.total_requests === 0 ? (
            <p class="dc-muted dc-card-pad">No request went to this account in {range.words}.</p>
          ) : (
            <div class="dc-card-pad dc-stack">
              <dl class="dc-inline-figures">
                <div>
                  <dt>Requests</dt>
                  <dd class="dc-num">{count(s.total_requests)}</dd>
                </div>
                <div>
                  <dt>Answered</dt>
                  <dd class="dc-num">{answered(s.success_count, s.total_requests)}</dd>
                </div>
                <div>
                  <dt>Mean latency</dt>
                  <dd class="dc-num">{latency(s.average_latency_ms)}</dd>
                </div>
                <div>
                  <dt>Tokens</dt>
                  <dd class="dc-num">{compact(s.tokens?.total_tokens ?? 0)}</dd>
                </div>
              </dl>
              <SeriesChart
                series={s.series}
                bucketSeconds={s.bucket_seconds}
                measure="requests"
                height={132}
                label={`Requests to this account in ${range.words}`}
              />
              <ul class="dc-ranks">
                {s.models.slice(0, 5).map((m) => (
                  <li key={m.name}>
                    <span class="dc-ellipsis" title={m.name}>
                      {m.name}
                    </span>
                    <span class="dc-num">{count(m.total_requests)}</span>
                    <span class="dc-bar">
                      <i style={{ width: `${((m.total_requests - m.failure_count) / most) * 100}%` }} />
                      {m.failure_count > 0 && (
                        <i data-failed="" style={{ width: `${(m.failure_count / most) * 100}%` }} />
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </section>
      </div>

      <section class="dc-card" aria-label="Latest requests to this account">
        <div class="dc-card-head">
          <h2>Latest requests</h2>
          <div class="dc-head-tools">
            <a href={hashFor("activity", { range: range.id, account: a.id })}>All its activity</a>
          </div>
        </div>
        {!recent.data ? (
          <p class="dc-muted dc-card-pad">
            {recent.error ? `The requests could not be read: ${recent.error}.` : "Reading requests…"}
          </p>
        ) : recent.data.events.length === 0 ? (
          <p class="dc-muted dc-card-pad">None in {range.words}.</p>
        ) : (
          <RequestList
            events={recent.data.events}
            fleet={fleet}
            now={now}
            label="Latest requests to this account"
          />
        )}
      </section>

      <section class="dc-card dc-card-pad dc-acct-manage" aria-label="Settings and tools" ref={tools}>
        <div class="dc-acct-manage-head">
          <h2 class="dc-h2">Settings and tools</h2>
          <Manage account={a} fleet={fleet} />
        </div>
        <dl class="dc-facts">
          <div>
            <dt>Credential</dt>
            <dd>{a.kind ?? "–"}</dd>
          </div>
          <div>
            <dt>Extension</dt>
            <dd class="dc-mono">{a.extension ?? "–"}</dd>
          </div>
          {a.created_at && (
            <div>
              <dt>Added</dt>
              <dd>{clockExact(a.created_at)}</dd>
            </div>
          )}
          {a.expires_at ? (
            <div>
              <dt>Sign-in expires</dt>
              <dd>{clockExact(a.expires_at)}</dd>
            </div>
          ) : null}
        </dl>
        {leftOut(a) !== "broken" && (
          <div class="dc-kit">
            <AccountSettings account={a} />
            <AccountTools key={a.id} account={a} onChanged={fleet.reload} />
          </div>
        )}
      </section>
    </div>
  );
}

function WindowsCard(props: { account: Account; c: Condition; now: number }) {
  const { account: a, c } = props;
  const readable = !["needs_login", "broken", "no_provider", "disabled"].includes(c.key);
  const w = useWindows(readable ? [a.id] : []).get(a.id);
  const limits = w?.state === "ok" ? w.reply.quota.limits : [];
  return (
    <section class="dc-card" aria-label="Usage windows">
      <div class="dc-card-head">
        <h2>Usage windows</h2>
        <div class="dc-head-tools">
          {w?.state === "ok" && (
            <span class="dc-updated">Read {new Date(w.reply.fetched_at).toLocaleTimeString()}</span>
          )}
          {readable && (
            <Btn
              kind="plain"
              icon="refresh"
              label="Read the usage windows again"
              onClick={() => forgetWindows(a.id)}
            />
          )}
        </div>
      </div>
      <div class="dc-card-pad dc-stack">
        {!readable ? (
          <p class="dc-muted">
            {c.key === "disabled"
              ? "The windows are not read while the account is disabled."
              : "The windows cannot be read while the account is not signed in."}
          </p>
        ) : !w || w.state === "loading" ? (
          <div class="dc-wins" role="status">
            <span class="dc-sr">Reading usage windows</span>
            <span class="dc-skel" />
            <span class="dc-skel" />
          </div>
        ) : w.state === "none" ? (
          <p class="dc-muted">
            {w.why
              ? `The windows could not be read: ${w.why}.`
              : "This provider reports no usage windows for the account."}
          </p>
        ) : limits.length === 0 ? (
          <p class="dc-muted">This provider reports no usage windows for the account.</p>
        ) : (
          <>
            <div class="dc-wins dc-wins-big">
              {limits.map((l) => (
                <WindowMeter key={l.id} limit={l} now={props.now} />
              ))}
            </div>
            <p class="dc-win-key">
              <span class="dc-win-now dc-win-now-key" aria-hidden="true" /> The tick is where the window's
              time is now: a bar past it is being used up faster than the window passes.
            </p>
          </>
        )}
        {w?.state === "ok" && w.reply.quota.plan && (
          <p class="dc-muted">
            Plan: {w.reply.quota.plan}
            {w.reply.quota.plan_ends_at
              ? ` · Subscription ends ${clockExact(w.reply.quota.plan_ends_at)}`
              : ""}
          </p>
        )}
      </div>
    </section>
  );
}

/** Edit, turn off or on, and remove: today's actions, beside the settings they change. */
function Manage(props: { account: Account; fleet: Fleet }) {
  const { api } = useKit();
  const a = props.account;
  const act = useAction(props.fleet.reload);
  const remove = useAction(() => {
    location.hash = "#/accounts";
    props.fleet.reload();
  });
  const out = leftOut(a);
  const off = a.disabled || out === "banned";
  if (a.source === "config") {
    return <span class="dc-muted">Set in core's config: change it there.</span>;
  }
  return (
    <div class="dc-manage">
      {out !== "broken" && (
        <>
          <AccountEdit account={a} onChanged={props.fleet.reload} disabled={act.busy || remove.busy} />
          {!off && (
            <Btn disabled={act.busy} onClick={() => act.run(() => api.setDisabled(a.id, true))}>
              Disable
            </Btn>
          )}
        </>
      )}
      <ConfirmButton
        disabled={act.busy || remove.busy}
        onConfirm={() => remove.run(() => api.removeAccount(a.id))}
      >
        Remove
      </ConfirmButton>
      {(act.error ?? remove.error) && (
        <Notice tone="danger">{props.fleet.mask(act.error ?? remove.error)}</Notice>
      )}
    </div>
  );
}
