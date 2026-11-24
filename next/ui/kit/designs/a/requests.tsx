// Direction A's Requests: what was asked of pluxy and where it went. Totals (a chart and the
// range broken down by provider, model, account and client key) and the list (every request with
// the path it took and why) are two views of one place and share one filter; the log is the third.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { UsageEvent, UsageEventsReply, UsageGroup, UsageSummary } from "../../api";
import { problem } from "../../api";
import { usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { useNow, usePoll } from "./data";
import { useAccountNames } from "./fleet";
import { Icon } from "./icons";
import { useKeys } from "./keys";
import { clock, compact, fellBack, latency, matchScore, pathOf, percent, type Range, stamp, usageQuery, verdict } from "./lib";
import { Figures, RangePicker, stepLabel, useRange } from "./overview";
import { Bars, Blank, Btn, Fault, Kbd, ShareBar, Skeleton } from "./parts";

const FILTERS = ["provider", "model", "account", "key", "failed"] as const;
type FilterKey = (typeof FILTERS)[number];
export type Filters = Partial<Record<FilterKey, string>>;

export interface Place {
  view: "totals" | "list";
  /** The request the list has open, by its seq. */
  seq?: number;
  filters: Filters;
}

/** Reads the address after "usage": "list/42?model=x" is the list with request 42 open, filtered to model x. */
export function parsePlace(rest: string): Place {
  const [path = "", query = ""] = rest.split("?");
  const parts = path.split("/").filter(Boolean);
  const params = new URLSearchParams(query);
  const filters: Filters = {};
  for (const k of FILTERS) {
    const v = params.get(k);
    if (v) filters[k] = v;
  }
  const seq = Number(parts[1]);
  return {
    view: parts[0] === "list" ? "list" : "totals",
    seq: parts[0] === "list" && Number.isInteger(seq) && seq > 0 ? seq : undefined,
    filters,
  };
}

export function placeAddress(place: Place): string {
  const params = new URLSearchParams();
  for (const k of FILTERS) {
    const v = place.filters[k];
    if (v) params.set(k, v);
  }
  const query = params.toString();
  const path = place.view === "list" ? `/list${place.seq ? `/${place.seq}` : ""}` : "";
  return `#/usage${path}${query ? `?${query}` : ""}`;
}

const filterQuery = (f: Filters) =>
  FILTERS.map((k) => (f[k] ? `&${k === "key" ? "key_id" : k}=${encodeURIComponent(f[k] as string)}` : "")).join("");

export function Requests(props: { rest: string }) {
  const place = useMemo(() => parsePlace(props.rest), [props.rest]);
  const [range, setRange, stepRange] = useRange();
  const names = useAccountNames();
  const go = (next: Place) => {
    location.hash = placeAddress(next);
  };
  const setFilter = (key: FilterKey, value?: string) =>
    go({ ...place, seq: undefined, filters: { ...place.filters, [key]: place.filters[key] === value ? undefined : value } });
  const active = FILTERS.filter((k) => place.filters[k]);
  const shown = (k: FilterKey, v: string) => (k === "account" ? names.nameOf(v) : k === "failed" ? (v === "true" ? "failed only" : "served only") : v);
  return (
    <div class="da-requests" data-view={place.view}>
      <header class="da-pane-head da-toolbar da-requests-bar">
        <RangePicker range={range} onPick={setRange} />
        <span class="da-quiet da-strip-keys">
          <Kbd keys="[" />
          <Kbd keys="]" />
        </span>
        {active.length > 0 && (
          <ul class="da-chips" aria-label="Filters">
            {active.map((k) => (
              <li key={k}>
                <button type="button" class="da-chip" onClick={() => setFilter(k, place.filters[k])} title="Remove this filter">
                  <span class="da-quiet">{k === "failed" ? "" : `${k} `}</span>
                  <span class="da-clip">{shown(k, place.filters[k] as string)}</span>
                  <Icon name="close" size={12} />
                  <span class="da-sr">Remove filter</span>
                </button>
              </li>
            ))}
            {active.length > 1 && (
              <li>
                <button type="button" class="da-chip da-chip-clear" onClick={() => go({ ...place, seq: undefined, filters: {} })}>
                  clear all
                </button>
              </li>
            )}
          </ul>
        )}
      </header>
      {place.view === "totals" ? (
        <Totals range={range} stepRange={stepRange} place={place} setFilter={setFilter} names={names} />
      ) : (
        <List range={range} stepRange={stepRange} place={place} go={go} setFilter={setFilter} names={names} />
      )}
    </div>
  );
}

type Names = ReturnType<typeof useAccountNames>;

function Totals(props: {
  range: Range;
  stepRange: (by: number) => void;
  place: Place;
  setFilter: (k: FilterKey, v?: string) => void;
  names: Names;
}) {
  const { api } = useKit();
  const { range, place } = props;
  const now = useNow(30_000);
  const query = usageQuery(range, filterQuery(place.filters));
  const usage = usePoll<UsageSummary>(() => api.usage(query), 30_000, query);
  useKeys([
    { keys: "r", label: "Refresh", run: usage.reload, group: "Requests" },
    { keys: "[", label: "Shorter range", run: () => props.stepRange(-1), quiet: true, group: "Requests" },
    { keys: "]", label: "Longer range", run: () => props.stepRange(1), quiet: true, group: "Requests" },
    { keys: "l", label: "Show these as a list", run: () => (location.hash = placeAddress({ ...place, view: "list" })), group: "Requests" },
  ]);
  const u = usage.data;
  if (usage.loading) {
    return (
      <div class="da-totals">
        <section class="da-strip">
          <Skeleton rows={3} label="Reading usage" />
        </section>
        <section class="da-pane">
          <Skeleton rows={10} label="Reading the breakdown" />
        </section>
      </div>
    );
  }
  if (!u) {
    return (
      <div class="da-totals">
        <Fault what="Usage could not be read." error={usage.error ?? ""} retry={usage.reload} />
      </div>
    );
  }
  if (u.enabled === false) {
    return (
      <Blank title="Usage recording is off" action={<Btn href="#/config/logging">Open logging settings</Btn>}>
        Core keeps no record of requests while it is off, so there are no totals and no list.
      </Blank>
    );
  }
  const whole = !u.oldest || Date.parse(u.oldest) <= now - range.seconds * 1000 || u.total_requests === 0;
  const filtered = Object.keys(place.filters).length > 0;
  return (
    <div class="da-totals">
      {usage.error && <Fault what="Usage is not updating." error={usage.error} retry={usage.reload} stale />}
      <section class="da-strip da-strip-tall" aria-label={`The last ${range.label}`}>
        <Figures usage={u} range={range} />
        <Bars
          series={u.series}
          bucketSeconds={u.bucket_seconds}
          now={now}
          height={96}
          label={`Requests over the last ${range.label}: ${u.total_requests} in all, ${u.failure_count} failed`}
          summary={u.total_requests ? `requests per ${stepLabel(u.bucket_seconds)}, failed in red` : "no requests in this range"}
        />
        {(!whole || (u.write_errors ?? 0) > 0) && (
          <p class="da-strip-note">
            <Icon name="alert" size={14} />
            {!whole && u.oldest && `Core's record starts ${stamp(u.oldest, now)}, so this range is not whole. `}
            {(u.write_errors ?? 0) > 0 && `${u.write_errors} requests could not be written to the usage files and will be lost on restart.`}
          </p>
        )}
      </section>
      {u.total_requests === 0 ? (
        <Blank
          title={filtered ? "No requests match" : `No requests in the last ${range.label}`}
          action={filtered ? <Btn href="#/usage">Clear the filters</Btn> : <Btn onClick={() => props.stepRange(1)}>Longer range</Btn>}
        >
          {filtered
            ? "Nothing in this range has all of the filters above."
            : "Requests show here once a coding tool sends them through pluxy."}
        </Blank>
      ) : (
        <div class="da-breakdowns">
          <Breakdown title="provider" k="provider" groups={u.providers} total={u.total_requests} place={place} setFilter={props.setFilter} />
          <Breakdown title="model" k="model" groups={u.models} total={u.total_requests} place={place} setFilter={props.setFilter} />
          <Breakdown
            title="account"
            k="account"
            groups={u.accounts}
            total={u.total_requests}
            place={place}
            setFilter={props.setFilter}
            name={props.names.nameOf}
          />
          <Breakdown
            title="client key"
            k="key"
            groups={u.keys}
            total={u.total_requests}
            place={place}
            setFilter={props.setFilter}
            name={(n) => n || "no key"}
          />
        </div>
      )}
    </div>
  );
}

/** How many rows a breakdown shows before "show all". */
const TOP = 8;

function Breakdown(props: {
  title: string;
  k: FilterKey;
  groups: UsageGroup[];
  total: number;
  place: Place;
  setFilter: (k: FilterKey, v?: string) => void;
  name?: (n: string) => string;
}) {
  const [all, setAll] = useState(false);
  const groups = useMemo(
    () => [...props.groups].sort((a, b) => b.total_requests - a.total_requests || a.name.localeCompare(b.name)),
    [props.groups],
  );
  const shown = all ? groups : groups.slice(0, TOP);
  const picked = props.place.filters[props.k];
  return (
    <section class="da-pane da-breakdown" aria-label={`By ${props.title}`}>
      <table class="da-table">
        <thead>
          <tr>
            <th scope="col">
              by {props.title} <span class="da-quiet da-num">{groups.length}</span>
            </th>
            <th scope="col" class="da-right">
              requests
            </th>
            <th scope="col" class="da-col-share">
              <span class="da-sr">Share</span>
            </th>
            <th scope="col" class="da-right">
              ok
            </th>
            <th scope="col" class="da-right da-col-latency">
              mean
            </th>
            <th scope="col" class="da-right da-col-tokens">
              tokens
            </th>
          </tr>
        </thead>
        <tbody>
          {shown.map((g) => {
            const label = props.name ? props.name(g.name) : g.name || "unknown";
            return (
              <tr key={g.name} aria-current={picked === g.name ? "true" : undefined}>
                <th scope="row">
                  <button
                    type="button"
                    class="da-rowlink da-clip"
                    aria-pressed={picked === g.name}
                    title={picked === g.name ? "Remove this filter" : `Show only ${label}`}
                    onClick={() => props.setFilter(props.k, g.name)}
                  >
                    {label}
                  </button>
                </th>
                <td class="da-num da-right">{compact(g.total_requests)}</td>
                <td class="da-col-share">
                  <ShareBar
                    share={props.total ? g.total_requests / props.total : 0}
                    failed={g.total_requests ? g.failure_count / g.total_requests : 0}
                  />
                </td>
                <td class="da-num da-right" data-tone={g.failure_count ? (g.success_rate < 0.8 ? "danger" : "warn") : undefined}>
                  {percent(g.success_rate)}
                </td>
                <td class="da-num da-right da-col-latency da-quiet">
                  {g.average_latency_ms !== undefined ? latency(g.average_latency_ms) : "–"}
                </td>
                <td class="da-num da-right da-col-tokens">{g.tokens ? compact(g.tokens.total_tokens) : "–"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {groups.length > TOP && (
        <button type="button" class="da-linkbtn" onClick={() => setAll((v) => !v)}>
          {all ? `Show the top ${TOP}` : `Show all ${groups.length}`}
        </button>
      )}
    </section>
  );
}

const PAGE = 200;

function List(props: {
  range: Range;
  stepRange: (by: number) => void;
  place: Place;
  go: (p: Place) => void;
  setFilter: (k: FilterKey, v?: string) => void;
  names: Names;
}) {
  const { api, unauthorized } = useKit();
  const { range, place, names } = props;
  const now = useNow(30_000);
  const query = `since=${range.since}${filterQuery(place.filters)}`;
  const first = usePoll<UsageEventsReply>(() => api.usageEventsPage(`before=0&limit=${PAGE}&${query}`), 0, query);
  // Older pages, loaded on request, belong to one query and one first page.
  const [older, setOlder] = useState<{ query: string; events: UsageEvent[]; next: number; more: boolean }>();
  const [olderBusy, setOlderBusy] = useState(false);
  const [olderError, setOlderError] = useState<string>();
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new first page starts the older ones over
  useEffect(() => setOlder(undefined), [query, first.at]);
  const [typed, setTyped] = useState("");
  const [onlyBack, setOnlyBack] = useState(false);
  const filter = useRef<HTMLInputElement>(null);
  const table = useRef<HTMLDivElement>(null);
  const loaded = useMemo(
    () => [...(first.data?.events ?? []), ...(older?.query === query ? older.events : [])],
    [first.data, older, query],
  );
  const more = older?.query === query ? older.more : (first.data?.more ?? false);
  const next = older?.query === query ? older.next : (first.data?.next ?? 0);
  const rows = useMemo(
    () =>
      loaded.filter(
        (e) =>
          (!onlyBack || fellBack(e)) &&
          matchScore(`${e.model} ${names.nameOf(e.account)} ${e.provider ?? ""} ${e.status ?? ""} ${e.error_code ?? ""} ${e.id ?? ""} ${e.key_id ?? ""}`, typed) > 0,
      ),
    [loaded, onlyBack, typed, names],
  );
  const loadOlder = async () => {
    setOlderBusy(true);
    setOlderError(undefined);
    const r = await api.usageEventsPage(`before=${next}&limit=${PAGE}&${query}`);
    setOlderBusy(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why || !r.body) return setOlderError(why ?? "no reply");
    const body = r.body;
    setOlder((old) => ({
      query,
      events: [...(old?.query === query ? old.events : []), ...body.events],
      next: body.next,
      more: body.more,
    }));
  };
  const selected = place.seq !== undefined ? loaded.find((e) => e.seq === place.seq) : undefined;
  const open = (seq?: number) => props.go({ ...place, seq });
  const move = (by: number) => {
    if (rows.length === 0) return;
    const at = rows.findIndex((e) => e.seq === place.seq);
    const to = rows[Math.max(0, Math.min(rows.length - 1, at < 0 ? 0 : at + by))];
    if (to) open(to.seq);
  };
  // biome-ignore lint/correctness/useExhaustiveDependencies: the open request is what moved
  useEffect(() => {
    table.current?.querySelector('[aria-current="true"]')?.scrollIntoView({ block: "nearest" });
  }, [place.seq]);
  useKeys([
    { keys: "j", label: "Next request", run: () => move(1), keyOnly: true, group: "Requests" },
    { keys: "k", label: "Previous request", run: () => move(-1), keyOnly: true, group: "Requests" },
    { keys: "/", label: "Filter requests", run: () => filter.current?.focus(), group: "Requests" },
    { keys: "r", label: "Refresh", run: first.reload, group: "Requests" },
    { keys: "f", label: place.filters.failed === "true" ? "Show served too" : "Failed only", run: () => props.setFilter("failed", "true"), group: "Requests" },
    { keys: "b", label: onlyBack ? "Show all paths" : "Fell back only", run: () => setOnlyBack((v) => !v), group: "Requests" },
    { keys: "[", label: "Shorter range", run: () => props.stepRange(-1), quiet: true, group: "Requests" },
    { keys: "]", label: "Longer range", run: () => props.stepRange(1), quiet: true, group: "Requests" },
    ...(place.seq !== undefined
      ? [{ keys: "Escape", label: "Close the request", run: () => open(), keyOnly: true, group: "Requests" }]
      : []),
  ]);
  const data = first.data;
  return (
    <div class="da-list" data-open={place.seq !== undefined ? "" : undefined}>
      <section class="da-pane da-list-pane" aria-label="Requests">
        <header class="da-pane-head da-toolbar">
          <label class="da-filter">
            <Icon name="search" size={14} />
            <input
              ref={filter}
              type="search"
              placeholder="Filter: model, account, status, trace"
              aria-label="Filter the loaded requests"
              value={typed}
              onInput={(e) => setTyped(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setTyped("");
                  e.currentTarget.blur();
                }
              }}
            />
            <Kbd keys="/" />
          </label>
          <fieldset class="da-seg" aria-label="Show">
            <button type="button" aria-pressed={place.filters.failed === "true"} onClick={() => props.setFilter("failed", "true")}>
              failed <Kbd keys="f" />
            </button>
            <button type="button" aria-pressed={onlyBack} onClick={() => setOnlyBack((v) => !v)}>
              fell back <Kbd keys="b" />
            </button>
          </fieldset>
          <span class="da-grow" />
          {data && (
            <span class="da-quiet da-num">
              {rows.length === loaded.length ? compact(loaded.length) : `${compact(rows.length)} of ${compact(loaded.length)}`}
              {more ? "+" : ""} requests
            </span>
          )}
        </header>
        {first.error && <Fault what="Requests could not be read." error={first.error} retry={first.reload} stale={!!data} />}
        {data?.lost && (
          <p class="da-strip-note">
            <Icon name="alert" size={14} />
            Core let go of older requests in this range to stay within its limit, so the list starts later than the range.
          </p>
        )}
        {first.loading ? (
          <Skeleton rows={16} label="Reading requests" />
        ) : data?.enabled === false ? (
          <Blank title="Usage recording is off" action={<Btn href="#/config/logging">Open logging settings</Btn>}>
            Core keeps no record of requests while it is off.
          </Blank>
        ) : data && loaded.length === 0 ? (
          <Blank
            title={Object.keys(place.filters).length ? "No requests match" : `No requests in the last ${range.label}`}
            action={
              Object.keys(place.filters).length ? (
                <Btn href="#/usage/list">Clear the filters</Btn>
              ) : (
                <Btn onClick={() => props.stepRange(1)}>Longer range</Btn>
              )
            }
          >
            Each request pluxy handles gets a line here with the account that served it and the path it took.
          </Blank>
        ) : data && rows.length === 0 ? (
          <Blank
            title="Nothing loaded matches"
            action={
              <Btn
                onClick={() => {
                  setTyped("");
                  setOnlyBack(false);
                }}
              >
                Clear the filter
              </Btn>
            }
          >
            {onlyBack ? "No loaded request fell back" : `Nothing loaded matches “${typed}”`}
            {more ? ". Older requests are not loaded yet." : "."}
          </Blank>
        ) : (
          <div class="da-table-wrap" ref={table}>
            <table class="da-table da-requests-table" aria-label="Requests, newest first">
              <thead>
                <tr>
                  <th scope="col">time</th>
                  <th scope="col">status</th>
                  <th scope="col">model</th>
                  <th scope="col">served by</th>
                  <th scope="col" class="da-col-path">
                    path
                  </th>
                  <th scope="col" class="da-right">
                    took
                  </th>
                  <th scope="col" class="da-right da-col-tokens">
                    tokens
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <RequestRow key={e.seq} e={e} names={names} now={now} selected={e.seq === place.seq} open={open} />
                ))}
              </tbody>
            </table>
            <p class="da-table-foot">
              {more ? (
                <Btn small onClick={loadOlder} disabled={olderBusy}>
                  {olderBusy ? "Loading" : `Load ${PAGE} older`}
                </Btn>
              ) : (
                <span class="da-quiet">That is every request core holds for this range.</span>
              )}
              {olderError && <span data-tone="danger"> {olderError}</span>}
            </p>
          </div>
        )}
      </section>
      {place.seq !== undefined && (
        <RequestPane e={selected} seq={place.seq} loading={first.loading} names={names} now={now} onClose={() => open()} />
      )}
    </div>
  );
}

/** The path in a few characters: one mark per try, a cross for each that failed, then who served. */
function PathMarks(props: { e: UsageEvent }) {
  const hops = pathOf(props.e);
  if (hops.length === 0) return <span class="da-quiet">no account</span>;
  if (hops.length === 1 && hops[0]?.ok) return <span class="da-quiet">direct</span>;
  return (
    <span class="da-marks" title={hops.map((h) => h.result).join(" → ")}>
      {hops.map((h, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a request can try one account twice
        <span key={i} class="da-mark-hop" data-ok={h.ok ? "" : undefined}>
          {h.ok ? "ok" : h.result.replace(/_/g, " ")}
        </span>
      ))}
    </span>
  );
}

function RequestRow(props: { e: UsageEvent; names: Names; now: number; selected: boolean; open: (seq: number) => void }) {
  const { e } = props;
  return (
    <tr
      aria-current={props.selected ? "true" : undefined}
      data-failed={e.failed ? "" : undefined}
      onClick={(ev) => {
        if ((ev.target as HTMLElement).closest("a,button")) return;
        props.open(e.seq);
      }}
    >
      <th scope="row">
        <button type="button" class="da-rowlink da-num" onClick={() => props.open(e.seq)} title={new Date(e.timestamp).toLocaleString()}>
          {new Date(e.timestamp).toDateString() === new Date(props.now).toDateString() ? clock(e.timestamp) : stamp(e.timestamp, props.now)}
        </button>
      </th>
      <td class="da-num">
        <span class="da-status" data-tone={e.failed ? "danger" : undefined}>
          {e.failed && <Icon name="failed" size={12} />}
          {e.status ?? (e.failed ? "err" : "ok")}
          {e.failed && e.error_code && <span class="da-status-code"> {e.error_code.replace(/_/g, " ")}</span>}
        </span>
      </td>
      <td>
        <span class="da-clip" title={e.model}>
          {e.model}
        </span>
      </td>
      <td>
        <span class="da-clip">{e.account ? props.names.nameOf(e.account) : <span class="da-quiet">–</span>}</span>
      </td>
      <td class="da-col-path">
        <PathMarks e={e} />
      </td>
      <td class="da-num da-right">{latency(e.latency_ms)}</td>
      <td class="da-num da-right da-col-tokens">
        {e.tokens.total_tokens !== undefined ? compact(e.tokens.total_tokens) : <span class="da-quiet">–</span>}
      </td>
    </tr>
  );
}

function RequestPane(props: {
  e?: UsageEvent;
  seq: number;
  loading: boolean;
  names: Names;
  now: number;
  onClose: () => void;
}) {
  const { e, names } = props;
  const p = usePrivacy();
  const pane = useRef<HTMLElement>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: focus moves when another request opens
  useEffect(() => {
    pane.current?.focus();
  }, [props.seq]);
  const hops = e ? pathOf(e) : [];
  const t = e?.tokens;
  const num = (n?: number) => (n === undefined ? "–" : n.toLocaleString("en-US"));
  return (
    <aside class="da-pane da-detail" aria-label="Request" tabIndex={-1} ref={pane}>
      <header class="da-pane-head da-detail-head">
        <a class="da-back" href="#/usage/list" aria-label="All requests">
          <Icon name="chevronRight" size={14} />
        </a>
        <div class="da-detail-title">
          <h2 class="da-clip">{e ? e.model : `request ${props.seq}`}</h2>
          {e && (
            <span class="da-sub da-num">
              {stamp(e.timestamp, props.now)}:{String(new Date(e.timestamp).getSeconds()).padStart(2, "0")}
              {e.endpoint ? ` · ${e.endpoint}` : ""}
              {e.stream ? " · stream" : ""}
            </span>
          )}
        </div>
        <Btn small variant="ghost" icon="close" label="Close" keys="Escape" onClick={props.onClose} />
      </header>
      {!e ? (
        props.loading ? (
          <Skeleton rows={8} label="Reading the request" />
        ) : (
          <Blank title="Not among the loaded requests" action={<Btn href="#/usage/list">All requests</Btn>}>
            This request is older than the loaded ones, outside the range or hidden by a filter.
          </Blank>
        )
      ) : (
        <div class="da-detail-body">
          <section class="da-verdict" data-tone={e.failed ? "danger" : fellBack(e) ? "warn" : "ok"} aria-label="What happened">
            <div class="da-verdict-line">
              <span class="da-state" data-tone={e.failed ? "danger" : "ok"}>
                <Icon name={e.failed ? "failed" : "serving"} size={14} />
                <span class="da-state-label">
                  {e.failed ? "failed" : "served"} <span class="da-num">{e.status ?? ""}</span>
                </span>
              </span>
              <span class="da-grow" />
              <span class="da-num da-quiet">{latency(e.latency_ms)}</span>
            </div>
            <p class="da-verdict-why">{verdict(e, names.nameOf)}</p>
          </section>

          <section aria-label="Path">
            <h3>
              path <span class="da-quiet da-num">{hops.length} {hops.length === 1 ? "try" : "tries"}</span>
            </h3>
            {hops.length === 0 ? (
              <p class="da-quiet">No account was tried: none serves this model, or all were left out.</p>
            ) : (
              <ol class="da-path">
                {hops.map((h, i) => {
                  const id = names.idOf(h.account);
                  return (
                    // biome-ignore lint/suspicious/noArrayIndexKey: a request can try one account twice
                    <li key={i} data-ok={h.ok ? "" : undefined}>
                      <span class="da-path-n da-num">{i + 1}</span>
                      <span class="da-path-who">
                        {id ? <a href={`#/accounts/${encodeURIComponent(id)}`}>{names.nameOf(h.account)}</a> : names.nameOf(h.account)}
                        <span class="da-sub">
                          {h.provider}
                          {h.model && h.model !== e.model ? ` · ${h.model}` : ""}
                        </span>
                      </span>
                      <span class="da-path-result" data-tone={h.ok ? "ok" : e.failed && i === hops.length - 1 ? "danger" : "warn"}>
                        <Icon name={h.ok ? "check" : "close"} size={12} />
                        {h.ok ? "ok" : h.result.replace(/_/g, " ")}
                      </span>
                    </li>
                  );
                })}
              </ol>
            )}
            {e.chain_step !== undefined && (
              <p class="da-quiet">
                The last try was step {e.chain_step} of this model's fallback chain. <a href="#/routing">Routing</a>
              </p>
            )}
          </section>

          <section aria-label="Tokens">
            <h3>tokens</h3>
            <dl class="da-facts da-facts-row">
              <div class="da-fact">
                <dt>input</dt>
                <dd class="da-num">{num(t?.input_tokens)}</dd>
              </div>
              <div class="da-fact">
                <dt>cached</dt>
                <dd class="da-num">{num(t?.cached_tokens)}</dd>
              </div>
              <div class="da-fact">
                <dt>output</dt>
                <dd class="da-num">{num(t?.output_tokens)}</dd>
              </div>
              <div class="da-fact">
                <dt>reasoning</dt>
                <dd class="da-num">{num(t?.reasoning_tokens)}</dd>
              </div>
            </dl>
            {t?.total_tokens === undefined && <p class="da-quiet">The provider reported no token counts for this request.</p>}
          </section>

          <section aria-label="Request">
            <h3>request</h3>
            <dl class="da-facts">
              <div class="da-fact">
                <dt>model</dt>
                <dd>
                  {e.model}
                  {e.upstream_model && e.upstream_model !== e.model && <span class="da-quiet"> → {e.upstream_model}</span>}
                  {e.served_model && e.served_model !== (e.upstream_model ?? e.model) && (
                    <span class="da-quiet"> (answered as {e.served_model})</span>
                  )}
                </dd>
              </div>
              <div class="da-fact">
                <dt>client key</dt>
                <dd class="da-num">{e.key_id || "–"}</dd>
              </div>
              {e.client_ip && (
                <div class="da-fact">
                  <dt>from</dt>
                  <dd class="da-num">{p.id(e.client_ip)}</dd>
                </div>
              )}
              {e.error_code && (
                <div class="da-fact">
                  <dt>error</dt>
                  <dd>{e.error_code.replace(/_/g, " ")}</dd>
                </div>
              )}
              <div class="da-fact">
                <dt>trace</dt>
                <dd class="da-num da-break">{e.id ?? "–"}</dd>
              </div>
            </dl>
            {e.id && (
              <div class="da-actions">
                <Btn small icon="log" href={`#/logs/${encodeURIComponent(e.id)}`}>
                  Open its log
                </Btn>
              </div>
            )}
          </section>
        </div>
      )}
    </aside>
  );
}
