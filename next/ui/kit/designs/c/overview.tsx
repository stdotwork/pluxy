// The first screen: the map of where requests go, what is selected on it (or what needs someone,
// when nothing is), and the latest requests drawn as their paths. Selecting a node filters all
// three; the selection and the range live in the address, so the view can be linked to.

import { useKit } from "../../ui";
import {
  type Fleet,
  type Flow,
  useAsk,
  useConditions,
  useFleet,
  useFlow,
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
  compact,
  count,
  duration,
  type Filters,
  filterQuery,
  hashFor,
  latency,
  millis,
  RANGES,
  type RangeId,
  rangeOf,
  share,
  untilWords,
} from "./lib";
import { FlowMap, MapSkeleton, MapTable, type Pick } from "./map";
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

const PICK_KINDS = ["key", "model", "account"] as const;

function pickOf(params: URLSearchParams): Pick | undefined {
  for (const kind of PICK_KINDS) {
    const ref = params.get(kind);
    if (ref) return { kind, ref };
  }
  return undefined;
}

const RANGE_PREF = "design-c.range";

/** The range in the address, else the one last chosen on this device. */
export function useRange(address: Address) {
  const { host } = useKit();
  return rangeOf(address.params.get("range") ?? host.prefs.get(RANGE_PREF));
}

export function Overview(props: { address: Address }) {
  const { host } = useKit();
  const fleet = useFleet();
  const now = useNow();
  const conditions = useConditions(fleet, now);
  const range = useRange(props.address);
  const pick = pickOf(props.address.params);
  const view = props.address.params.get("view") === "table" ? "table" : "map";
  const filters: Filters = pick ? { [pick.kind]: pick.ref } : {};
  const flow = useFlow(range, {});
  const picked = useFlow(range, filters, !!pick);

  const go = (next: { range?: RangeId; pick?: Pick | null; view?: string }) => {
    const p = next.pick === null ? undefined : (next.pick ?? pick);
    const v = next.view ?? view;
    location.hash = hashFor("map", {
      range: next.range ?? range.id,
      [p?.kind ?? "none"]: p?.ref,
      view: v === "table" ? "table" : undefined,
      none: undefined,
    });
  };

  const accounts = fleet.accounts;
  const summary = flow.data?.summary;
  const serving = accounts?.filter((a) => conditions.get(a.id)?.serving).length ?? 0;
  const busy = fleet.loading || flow.loading || (!!pick && picked.loading);
  const empty = accounts !== undefined && accounts.length === 0;

  let sentence: string;
  if (!accounts && fleet.error) sentence = "The accounts could not be read.";
  else if (!accounts) sentence = "Reading accounts…";
  else if (empty) sentence = "No accounts yet.";
  else {
    const fleetWords =
      serving === accounts.length
        ? `All ${accounts.length} accounts are available.`
        : `${serving} of ${accounts.length} accounts are available.`;
    const flowWords = !summary
      ? flow.error
        ? ""
        : " Reading requests…"
      : summary.total_requests === 0
        ? ` No requests in ${range.words}.`
        : ` ${count(summary.total_requests)} requests in ${range.words}, ${answered(summary.success_count, summary.total_requests)} answered.`;
    sentence = fleetWords + flowWords;
  }

  return (
    <div class="dc-page" data-view="overview">
      <PageHead title="Map" sub={sentence}>
        <Segmented
          label="Range"
          value={range.id}
          options={RANGES.map((r) => ({ value: r.id, label: r.id, title: r.words }))}
          onChange={(id) => {
            host.prefs.set(RANGE_PREF, id);
            go({ range: id });
          }}
        />
        <Segmented
          label="Show the flow as"
          value={view}
          options={[
            { value: "map", label: "Map", icon: "map" },
            { value: "table", label: "Table", icon: "table" },
          ]}
          onChange={(v) => go({ view: v })}
        />
        <Refresh
          at={flow.at ?? fleet.at}
          now={now}
          busy={busy}
          onRefresh={() => {
            fleet.reload();
            flow.reload();
            picked.reload();
          }}
        />
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
          {accounts ? "The accounts could not be read again, so this shows them as they were. " : ""}
          Reading accounts failed: {fleet.error}.
        </Note>
      )}
      {flow.error && accounts && (
        <Note
          tone="warn"
          action={
            <Btn size="small" onClick={flow.reload}>
              Try again
            </Btn>
          }
        >
          The requests could not be read, so the map shows the accounts without their links: {flow.error}.
        </Note>
      )}
      {summary && !summary.enabled && (
        <Note
          action={
            <Btn size="small" href="#/config/logging">
              Open settings
            </Btn>
          }
        >
          Usage recording is off, so pluxy keeps no record of where requests go. Turn it on to see links here.
        </Note>
      )}

      {empty ? (
        <FirstAccount />
      ) : !accounts ? (
        !fleet.error && (
          <div class="dc-overview">
            <section class="dc-card dc-mapcard" aria-label="Request flow">
              <MapSkeleton words="Reading the accounts and where requests went" />
            </section>
          </div>
        )
      ) : (
        <>
          <div class="dc-overview">
            <section class="dc-card dc-mapcard" aria-label="Request flow">
              {pick && (
                <div class="dc-picked">
                  <span class="dc-chip">
                    <span class="dc-muted">
                      {pick.kind === "key" ? "Client key" : pick.kind === "model" ? "Model" : "Account"}
                    </span>
                    <strong class="dc-ellipsis">{pickName(fleet, pick)}</strong>
                    <button type="button" aria-label="Clear the selection" onClick={() => go({ pick: null })}>
                      <Icon name="close" size={13} />
                    </button>
                  </span>
                  <span class="dc-muted">
                    {picked.data
                      ? `${count(picked.data.summary.total_requests)} of ${count(summary?.total_requests ?? 0)} requests go through it.`
                      : picked.error
                        ? `Its requests could not be read: ${picked.error}.`
                        : "Reading its requests…"}
                  </span>
                </div>
              )}
              {view === "table" ? (
                <MapTable
                  fleet={fleet}
                  flow={pick ? (picked.data ?? undefined) : flow.data}
                  pick={pick}
                  onPick={(p) => go({ pick: p ?? null })}
                  conditions={conditions}
                  now={now}
                />
              ) : (
                <FlowMap
                  fleet={fleet}
                  flow={flow.data}
                  picked={pick ? (picked.data ?? undefined) : undefined}
                  pick={pick}
                  onPick={(p) => go({ pick: p ?? null })}
                  conditions={conditions}
                  now={now}
                  rangeWords={range.words}
                />
              )}
              <MapFoot flow={flow.data} rangeWords={range.words} loading={!flow.data && !flow.error} />
            </section>
            <aside class="dc-card dc-inspector" aria-label={pick ? "Selected" : "Needs you"}>
              {pick ? (
                <Selected
                  fleet={fleet}
                  pick={pick}
                  picked={picked.data ?? undefined}
                  total={summary?.total_requests ?? 0}
                  conditions={conditions}
                  now={now}
                  rangeId={range.id}
                />
              ) : (
                <NeedsYou fleet={fleet} conditions={conditions} now={now} flow={flow.data} />
              )}
            </aside>
          </div>
          <Recent fleet={fleet} rangeId={range.id} filters={filters} now={now} />
        </>
      )}
    </div>
  );
}

const pickName = (fleet: Fleet, pick: Pick) =>
  pick.kind === "key" ? fleet.keyName(pick.ref) : pick.kind === "account" ? fleet.mask(pick.ref) : pick.ref;

/** Under the map: how to read it, and what it leaves out. */
function MapFoot(props: { flow: Flow | undefined; rangeWords: string; loading: boolean }) {
  const s = props.flow?.summary;
  return (
    <p class="dc-map-foot">
      <span class="dc-legend">
        <svg width="26" height="10" aria-hidden="true" focusable="false">
          <rect width="26" height="10" rx="2" class="dc-legend-band" />
        </svg>
        Thickness is the share of requests
      </span>
      <span class="dc-legend">
        <svg width="26" height="10" aria-hidden="true" focusable="false">
          <rect width="26" height="10" rx="2" fill="url(#dc-hatch)" />
        </svg>
        Failed
      </span>
      {props.loading && <span class="dc-muted">Reading requests…</span>}
      {s && s.total_requests === 0 && (
        <span class="dc-muted">No requests in {props.rangeWords}: links appear with the first one.</span>
      )}
      {props.flow?.gaps.map((gap) => (
        <span key={gap} class="dc-muted">
          Not whole: {gap}.
        </span>
      ))}
    </p>
  );
}

function FirstAccount() {
  return (
    <section class="dc-card dc-first" aria-label="Add the first account">
      <ol class="dc-first-line">
        <li>
          <span class="dc-stop-dot">
            <Icon name="clients" size={15} />
          </span>
          <strong>A coding tool asks</strong>
          <span class="dc-muted">It sends pluxy a request for a model, with a client key.</span>
        </li>
        <li>
          <span class="dc-stop-dot">
            <Icon name="routing" size={15} />
          </span>
          <strong>pluxy routes it</strong>
          <span class="dc-muted">
            To an account that serves the model, and on to the next one if that fails.
          </span>
        </li>
        <li data-missing="">
          <span class="dc-stop-dot">
            <Icon name="accounts" size={15} />
          </span>
          <strong>An account answers</strong>
          <span class="dc-muted">
            A subscription you sign in to, or an API key. There is none yet, so nothing can be answered.
          </span>
          <Btn kind="primary" icon="plus" href="#/add-account">
            Add account
          </Btn>
        </li>
      </ol>
    </section>
  );
}

/** With nothing selected: the accounts that are not serving, each with the one thing that fixes it. */
function NeedsYou(props: {
  fleet: Fleet;
  conditions: Map<string, Condition>;
  now: number;
  flow: Flow | undefined;
}) {
  const { fleet, conditions } = props;
  const list = (fleet.accounts ?? [])
    .map((a) => ({ a, c: conditions.get(a.id) as Condition }))
    .filter(({ c }) => c && c.key !== "available" && c.key !== "disabled")
    .sort((x, y) => CONDITION_RANK[x.c.key] - CONDITION_RANK[y.c.key]);
  const s = props.flow?.summary;
  return (
    <>
      <div class="dc-insp-head">
        <h2 class="dc-h2">
          {list.length === 0
            ? "Nothing needs you"
            : list.length === 1
              ? "1 account needs you"
              : `${list.length} accounts need you`}
        </h2>
      </div>
      {list.length === 0 ? (
        <p class="dc-muted dc-insp-pad">
          Every account that is turned on is taking requests. Select anything on the map to follow it.
        </p>
      ) : (
        <ul class="dc-needs">
          {list.slice(0, 6).map(({ a, c }) => (
            <li key={a.id}>
              <div class="dc-needs-top">
                <ProviderTile p={fleet.providers.of(a.extension)} />
                <a
                  class="dc-ellipsis dc-needs-name"
                  href={`#/accounts/${encodeURIComponent(a.id)}`}
                  title={fleet.mask(a.id)}
                >
                  {fleet.mask(a.id)}
                </a>
              </div>
              <StateMark c={c} now={props.now} />
              {c.why && <p class="dc-needs-why">{fleet.mask(c.why)}</p>}
              <FixButton account={a} c={c} size="small" />
            </li>
          ))}
          {list.length > 6 && (
            <li>
              <a href="#/accounts?show=attention">{list.length - 6} more on Accounts</a>
            </li>
          )}
        </ul>
      )}
      {s && s.total_requests > 0 && (
        <dl class="dc-figures">
          <div>
            <dt>Requests</dt>
            <dd class="dc-num">{count(s.total_requests)}</dd>
          </div>
          <div>
            <dt>Failed</dt>
            <dd class="dc-num">{count(s.failure_count)}</dd>
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
      )}
    </>
  );
}

function Figures(props: { flow: Flow | undefined; total: number }) {
  const s = props.flow?.summary;
  if (!s) return <p class="dc-muted dc-insp-pad">Reading…</p>;
  return (
    <dl class="dc-figures">
      <div>
        <dt>Requests</dt>
        <dd class="dc-num">
          {count(s.total_requests)} <span class="dc-muted">· {share(s.total_requests, props.total)}</span>
        </dd>
      </div>
      <div>
        <dt>Failed</dt>
        <dd class="dc-num">{count(s.failure_count)}</dd>
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
  );
}

/** What is selected on the map, in full. */
function Selected(props: {
  fleet: Fleet;
  pick: Pick;
  picked: Flow | undefined;
  total: number;
  conditions: Map<string, Condition>;
  now: number;
  rangeId: RangeId;
}) {
  const { fleet, pick } = props;
  const activity = hashFor("activity", { range: props.rangeId, [pick.kind]: pick.ref });
  if (pick.kind === "account") {
    const a = fleet.account(pick.ref);
    const c = a ? props.conditions.get(a.id) : undefined;
    return (
      <>
        <div class="dc-insp-head">
          {a && <ProviderTile p={fleet.providers.of(a.extension)} />}
          <h2 class="dc-h2 dc-ellipsis" title={fleet.mask(pick.ref)}>
            {fleet.mask(pick.ref)}
          </h2>
        </div>
        {a && c ? (
          <div class="dc-insp-pad dc-insp-stack">
            <StateMark c={c} now={props.now} />
            {c.why && <p class="dc-needs-why">{fleet.mask(c.why)}</p>}
            <FixButton account={a} c={c} size="small" primary />
            <AccountWindows
              id={a.id}
              serving={c.key !== "needs_login" && c.key !== "broken"}
              now={props.now}
            />
          </div>
        ) : (
          <p class="dc-muted dc-insp-pad">
            Core no longer has this account; its requests are still in the record.
          </p>
        )}
        <Figures flow={props.picked} total={props.total} />
        <div class="dc-insp-links">
          {a && <a href={`#/accounts/${encodeURIComponent(a.id)}`}>Open the account</a>}
          <a href={activity}>See its requests</a>
        </div>
      </>
    );
  }
  if (pick.kind === "model") {
    const chain = fleet.chains?.find((c) => c.name === pick.ref);
    return (
      <>
        <div class="dc-insp-head">
          <Icon name={chain ? "chain" : "model"} size={18} />
          <h2 class="dc-h2 dc-ellipsis" title={pick.ref}>
            {pick.ref}
          </h2>
        </div>
        <RouteNow model={pick.ref} fleet={fleet} now={props.now} />
        <Figures flow={props.picked} total={props.total} />
        <div class="dc-insp-links">
          <a href={hashFor("routing", { model: pick.ref })}>Open in Routing</a>
          <a href={activity}>See its requests</a>
        </div>
      </>
    );
  }
  const models = props.picked?.summary.models.slice(0, 5) ?? [];
  const most = models[0]?.total_requests ?? 1;
  return (
    <>
      <div class="dc-insp-head">
        <Icon name="key" size={18} />
        <h2 class="dc-h2 dc-mono dc-ellipsis">{fleet.keyName(pick.ref)}</h2>
      </div>
      <Figures flow={props.picked} total={props.total} />
      {models.length > 0 && (
        <div class="dc-insp-pad dc-insp-stack">
          <h3 class="dc-h3">Asks most for</h3>
          <ul class="dc-ranks">
            {models.map((m) => (
              <li key={m.name}>
                <span class="dc-ellipsis" title={m.name}>
                  {m.name}
                </span>
                <span class="dc-num">{count(m.total_requests)}</span>
                <span class="dc-bar">
                  <i style={{ width: `${(m.total_requests / most) * 100}%` }} />
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div class="dc-insp-links">
        <a href="#/clients">Open Clients</a>
        <a href={activity}>See its requests</a>
      </div>
    </>
  );
}

/** An account's usage windows, read when it is looked at. */
export function AccountWindows(props: { id: string; serving: boolean; now: number; plain?: boolean }) {
  const windows = useWindows(props.serving ? [props.id] : []).get(props.id);
  if (!props.serving || !windows) return null;
  if (windows.state === "loading") {
    return (
      <div class="dc-wins" role="status">
        <span class="dc-sr">Reading usage windows</span>
        <span class="dc-skel" />
        <span class="dc-skel" />
      </div>
    );
  }
  if (windows.state === "none" || windows.reply.quota.limits.length === 0) {
    return (
      <p class="dc-muted dc-wins-none">
        {windows.state === "none" && windows.why
          ? `Usage windows could not be read: ${windows.why}.`
          : "This provider reports no usage windows."}
      </p>
    );
  }
  return (
    <div class="dc-wins">
      {windows.reply.quota.limits.map((l) => (
        <WindowMeter key={l.id} limit={l} now={props.now} plain={props.plain} />
      ))}
    </div>
  );
}

/** Where a request for the model would go right now, as core plans it (nothing is sent). */
export function RouteNow(props: { model: string; fleet: Fleet; now: number }) {
  const { api } = useKit();
  const ask = useAsk();
  const { fleet } = props;
  const plan = useResource(`route:${props.model}`, () => ask(api.routePlan(props.model)));
  if (plan.loading && !plan.data) return <p class="dc-muted dc-insp-pad">Asking core where it would go…</p>;
  if (plan.error || !plan.data) {
    return <p class="dc-muted dc-insp-pad">Core could not say where it would go: {plan.error}.</p>;
  }
  const { order, left_out, chain } = plan.data;
  return (
    <div class="dc-insp-pad dc-insp-stack">
      <h3 class="dc-h3">Right now it would go to</h3>
      {order.length === 0 ? (
        <p class="dc-state" data-tone="danger">
          <Icon name="failed" size={16} /> No account can take it
        </p>
      ) : (
        <ol class="dc-plan">
          {order.map((s, i) => (
            <li key={`${s.id}:${s.step ?? 0}`} data-slot-p={fleet.providers.of(s.provider).slot}>
              <span class="dc-plan-n dc-num">{i + 1}</span>
              <a class="dc-ellipsis" href={`#/accounts/${encodeURIComponent(s.id)}`} title={fleet.mask(s.id)}>
                {fleet.mask(s.id)}
              </a>
              <span class="dc-muted dc-ellipsis">
                {chain && s.step ? `step ${s.step} · ` : ""}
                {i === 0 ? "first" : "then"}
              </span>
            </li>
          ))}
        </ol>
      )}
      {left_out.length > 0 && (
        <>
          <h3 class="dc-h3">Left out</h3>
          <ul class="dc-plan dc-plan-out">
            {left_out.map((s) => {
              const until = millis(s.until);
              return (
                <li key={`${s.id}:${s.step ?? 0}`}>
                  <Icon name="close" size={12} />
                  <a
                    class="dc-ellipsis"
                    href={`#/accounts/${encodeURIComponent(s.id)}`}
                    title={fleet.mask(s.id)}
                  >
                    {fleet.mask(s.id)}
                  </a>
                  <span class="dc-muted dc-ellipsis">
                    {leftOutWords(s.state)}
                    {until && until > props.now ? ` · ${duration((until - props.now) / 1000)}` : ""}
                  </span>
                </li>
              );
            })}
          </ul>
        </>
      )}
    </div>
  );
}

export const leftOutWords = (state: string) =>
  ({
    cooling: "cooling down",
    exhausted: "limit used up",
    needs_login: "needs sign-in",
    banned: "closed by provider",
    disabled: "disabled",
    busy: "at its concurrency limit",
    skipped: "past the request's limits",
    broken: "unreadable auth file",
  })[state] ?? state.replace(/_/g, " ");

/** The latest requests, each drawn as its path. */
function Recent(props: { fleet: Fleet; rangeId: RangeId; filters: Filters; now: number }) {
  const { api } = useKit();
  const ask = useAsk();
  const q = filterQuery(props.filters);
  const res = useResource(
    `recent:${props.rangeId}${q}`,
    () => ask(api.usageEventsPage(`before=0&limit=8&since=${props.rangeId}${q}`)),
    60_000,
  );
  const events = res.data?.events ?? [];
  return (
    <section class="dc-card" aria-label="Latest requests">
      <div class="dc-card-head">
        <h2>Latest requests</h2>
        <div class="dc-head-tools">
          <a href={hashFor("activity", { range: props.rangeId, ...props.filters })}>All activity</a>
        </div>
      </div>
      {res.error && !res.data ? (
        <p class="dc-muted dc-card-pad">The requests could not be read: {res.error}.</p>
      ) : !res.data ? (
        <p class="dc-muted dc-card-pad" role="status">
          Reading requests…
        </p>
      ) : events.length === 0 ? (
        <p class="dc-muted dc-card-pad">
          No requests{hasPick(props.filters) ? " through the selection" : ""} in this range.
        </p>
      ) : (
        <RequestList events={events} fleet={props.fleet} now={props.now} label="Latest requests" />
      )}
    </section>
  );
}

const hasPick = (filters: Filters) => Object.values(filters).some(Boolean);

/** For pages that say how long an account is still blocked. */
export const untilOf = (c: Condition, now: number) => untilWords(c, now);
