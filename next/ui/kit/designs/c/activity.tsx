// Activity: what went through, over a chosen range. Requests over time as bars, the totals by
// provider, model, account and client key as one table whose rows filter the page, and the
// requests themselves, each drawn as the path it took. The filters live in the address.

import { useEffect, useState } from "preact/hooks";
import type { UsageEvent, UsageGroup } from "../../api";
import { useKit } from "../../ui";
import { type Measure, SeriesChart } from "./chart";
import { type Fleet, useAsk, useFleet, useNow, useResource } from "./data";
import { Icon } from "./icons";
import {
  type Address,
  answered,
  compact,
  count,
  type FilterName,
  type Filters,
  filterQuery,
  filtersOf,
  hasFilters,
  hashFor,
  latency,
  RANGES,
  share,
} from "./lib";
import { useRange } from "./overview";
import { Btn, Note, PageHead, ProviderTile, Refresh, Segmented } from "./parts";
import { RequestList } from "./paths";

type Dimension = "provider" | "model" | "account" | "key";

const DIMENSIONS: { id: Dimension; label: string; group: "providers" | "models" | "accounts" | "keys" }[] = [
  { id: "provider", label: "Provider", group: "providers" },
  { id: "model", label: "Model", group: "models" },
  { id: "account", label: "Account", group: "accounts" },
  { id: "key", label: "Client key", group: "keys" },
];

const FILTER_WORDS: Record<FilterName, string> = {
  key: "Client key",
  model: "Model",
  provider: "Provider",
  account: "Account",
  failed: "Failed only",
};

const PAGE = 50;
/** How many rows a breakdown shows before it asks. */
const TOP = 8;

export function Activity(props: { address: Address }) {
  const { api, host } = useKit();
  const ask = useAsk();
  const fleet = useFleet();
  const now = useNow();
  const range = useRange(props.address);
  const filters = filtersOf(props.address.params);
  const q = filterQuery(filters);
  const by = (DIMENSIONS.find((d) => d.id === props.address.params.get("by")) ??
    DIMENSIONS[0]) as (typeof DIMENSIONS)[number];
  const [measure, setMeasure] = useState<Measure>("requests");
  const [all, setAll] = useState(false);

  const go = (next: { range?: string; filters?: Filters; by?: Dimension }) => {
    location.hash = hashFor("activity", {
      range: next.range ?? range.id,
      by: (next.by ?? by.id) === "provider" ? undefined : (next.by ?? by.id),
      ...(next.filters ?? filters),
    });
  };

  const usage = useResource(
    `activity:${range.id}${q}`,
    () => ask(api.usage(`since=${range.id}&${range.series}${q}`)),
    60_000,
  );
  const s = usage.data;

  // The requests, newest first, a page at a time.
  const listKey = `${range.id}${q}`;
  const [list, setList] = useState<{
    key: string;
    events: UsageEvent[];
    next: number;
    more: boolean;
    lost: boolean;
    error?: string;
    busy: boolean;
  }>({
    key: "",
    events: [],
    next: 0,
    more: false,
    lost: false,
    busy: true,
  });
  const [turn, setTurn] = useState(0);
  useEffect(() => {
    let live = true;
    setList((l) => ({ ...l, busy: true }));
    ask(api.usageEventsPage(`before=0&limit=${PAGE}&since=${range.id}${q}`)).then(
      (r) =>
        live &&
        setList({ key: listKey, events: r.events, next: r.next, more: r.more, lost: r.lost, busy: false }),
      (e: Error) => live && setList((l) => ({ ...l, key: listKey, error: e.message, busy: false })),
    );
    return () => {
      live = false;
    };
  }, [listKey, turn]);
  const older = () => {
    setList((l) => ({ ...l, busy: true }));
    ask(api.usageEventsPage(`before=${list.next}&limit=${PAGE}&since=${range.id}${q}`)).then(
      (r) =>
        setList((l) =>
          l.key === listKey
            ? {
                ...l,
                events: [...l.events, ...r.events],
                next: r.next,
                more: r.more,
                lost: l.lost || r.lost,
                busy: false,
              }
            : l,
        ),
      (e: Error) => setList((l) => ({ ...l, error: e.message, busy: false })),
    );
  };

  const groups = s ? s[by.group] : [];
  const top = Math.max(1, ...groups.map((g) => g.total_requests));
  const rows = all ? groups : groups.slice(0, TOP);
  const nameOf = (g: UsageGroup) =>
    by.id === "key"
      ? g.name
        ? fleet.keyName(g.name)
        : "No client key"
      : by.id === "account"
        ? fleet.mask(g.name)
        : by.id === "provider"
          ? fleet.providers.of(g.name).name
          : g.name;

  const sentence = !s
    ? usage.error
      ? "The requests could not be read."
      : "Reading requests…"
    : s.total_requests === 0
      ? `No requests in ${range.words}${hasFilters(filters) ? " match these filters" : ""}.`
      : `${count(s.total_requests)} requests in ${range.words}, ${answered(s.success_count, s.total_requests)} answered, ${latency(s.average_latency_ms)} on average.`;

  return (
    <div class="dc-page" data-view="activity">
      <PageHead title="Activity" sub={sentence}>
        <Segmented
          label="Range"
          value={range.id}
          options={RANGES.map((r) => ({ value: r.id, label: r.id, title: r.words }))}
          onChange={(id) => {
            host.prefs.set("design-c.range", id);
            go({ range: id });
          }}
        />
        <Refresh
          at={usage.at}
          now={now}
          busy={usage.loading || list.busy}
          onRefresh={() => {
            usage.reload();
            setTurn((n) => n + 1);
          }}
        />
      </PageHead>

      <div class="dc-toolbar">
        {(Object.keys(FILTER_WORDS) as FilterName[])
          .filter((name) => name !== "failed" && filters[name])
          .map((name) => (
            <span key={name} class="dc-chip">
              <span class="dc-muted">{FILTER_WORDS[name]}</span>
              <strong class="dc-ellipsis">{filterValue(fleet, name, filters[name] as string)}</strong>
              <button
                type="button"
                aria-label={`Remove the ${FILTER_WORDS[name].toLowerCase()} filter`}
                onClick={() => go({ filters: { ...filters, [name]: undefined } })}
              >
                <Icon name="close" size={13} />
              </button>
            </span>
          ))}
        <Btn
          size="small"
          icon={filters.failed ? "check" : "filter"}
          pressed={!!filters.failed}
          onClick={() => go({ filters: { ...filters, failed: filters.failed ? undefined : "true" } })}
        >
          Failed only
        </Btn>
        {hasFilters(filters) && (
          <Btn size="small" kind="plain" onClick={() => go({ filters: {} })}>
            Clear filters
          </Btn>
        )}
        {!hasFilters(filters) && (
          <span class="dc-muted">Press a row below, or a node on the map, to filter.</span>
        )}
      </div>

      {usage.error && (
        <Note
          tone="danger"
          action={
            <Btn size="small" onClick={usage.reload}>
              Try again
            </Btn>
          }
        >
          {s ? "This shows the totals as they were. " : ""}Reading the totals failed: {usage.error}.
        </Note>
      )}
      {s && !s.enabled && (
        <Note
          action={
            <Btn size="small" href="#/config/logging">
              Open settings
            </Btn>
          }
        >
          Usage recording is off, so new requests are not counted here.
        </Note>
      )}
      {s && (s.write_errors ?? 0) > 0 && (
        <Note tone="warn">
          {count(s.write_errors ?? 0)} requests could not be written to the usage files. They are counted here
          until pluxy restarts.
        </Note>
      )}

      {!s && !usage.error && (
        <div class="dc-activity-top" role="status">
          <span class="dc-sr">Reading requests</span>
          <section class="dc-card dc-card-pad dc-stack" aria-hidden="true">
            <span class="dc-skel dc-skel-word" />
            <span class="dc-skel dc-skel-chart" />
          </section>
          <section class="dc-card dc-card-pad dc-stack" aria-hidden="true">
            <span class="dc-skel dc-skel-word" />
            <span class="dc-skel dc-skel-line" />
            <span class="dc-skel dc-skel-line" />
            <span class="dc-skel dc-skel-line" />
          </section>
        </div>
      )}

      {s && (
        <div class="dc-activity-top">
          <section class="dc-card" aria-label="Over time">
            <div class="dc-card-head">
              <h2>Over time</h2>
              <div class="dc-head-tools">
                <Segmented
                  label="Measure"
                  value={measure}
                  options={[
                    { value: "requests", label: "Requests" },
                    { value: "tokens", label: "Tokens" },
                  ]}
                  onChange={setMeasure}
                />
              </div>
            </div>
            <div class="dc-card-pad dc-stack">
              {s.total_requests === 0 ? (
                <p class="dc-muted dc-chart-none">
                  Nothing to draw: no request {hasFilters(filters) ? "matches" : `in ${range.words}`}.
                </p>
              ) : (
                <SeriesChart
                  series={s.series}
                  bucketSeconds={s.bucket_seconds}
                  measure={measure}
                  label={`${measure === "requests" ? "Requests" : "Tokens"} in ${range.words}`}
                />
              )}
              <Tokens s={s} />
            </div>
          </section>

          <section class="dc-card" aria-label={`By ${by.label.toLowerCase()}`}>
            <div class="dc-card-head">
              <h2>By</h2>
              <Segmented
                label="Group the requests by"
                value={by.id}
                options={DIMENSIONS.map((d) => ({ value: d.id, label: d.label }))}
                onChange={(id) => {
                  setAll(false);
                  go({ by: id });
                }}
              />
            </div>
            {groups.length === 0 ? (
              <p class="dc-muted dc-card-pad">Nothing in {range.words}.</p>
            ) : (
              <div class="dc-tablewrap dc-by">
                <table class="dc-table">
                  <thead>
                    <tr>
                      <th scope="col">{by.label}</th>
                      <th scope="col" class="dc-r">
                        Requests
                      </th>
                      <th scope="col" class="dc-r">
                        Answered
                      </th>
                      <th scope="col" class="dc-r">
                        Latency
                      </th>
                      <th scope="col" class="dc-r">
                        Tokens
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((g) => {
                      const provider =
                        by.id === "provider"
                          ? g.name
                          : by.id === "account"
                            ? (g.provider ?? fleet.account(g.name)?.extension)
                            : undefined;
                      const p = provider !== undefined ? fleet.providers.of(provider) : undefined;
                      const on = filters[by.id] === g.name;
                      return (
                        <tr key={g.name} data-slot-p={p?.slot}>
                          <th scope="row">
                            <span class="dc-by-name">
                              {p && <ProviderTile p={p} />}
                              {g.name ? (
                                <button
                                  type="button"
                                  class="dc-rowpick"
                                  aria-pressed={on}
                                  title={nameOf(g)}
                                  onClick={() =>
                                    go({ filters: { ...filters, [by.id]: on ? undefined : g.name } })
                                  }
                                >
                                  {nameOf(g)}
                                </button>
                              ) : (
                                nameOf(g)
                              )}
                            </span>
                            <span
                              class="dc-bar"
                              title={`${share(g.total_requests, s.total_requests)} of the requests`}
                            >
                              <i
                                style={{ width: `${((g.total_requests - g.failure_count) / top) * 100}%` }}
                              />
                              {g.failure_count > 0 && (
                                <i data-failed="" style={{ width: `${(g.failure_count / top) * 100}%` }} />
                              )}
                            </span>
                          </th>
                          <td class="dc-r dc-num">{count(g.total_requests)}</td>
                          <td
                            class="dc-r dc-num"
                            data-low={g.total_requests > 0 && g.success_rate < 0.9 ? "" : undefined}
                          >
                            {answered(g.success_count, g.total_requests)}
                          </td>
                          <td class="dc-r dc-num">{latency(g.average_latency_ms)}</td>
                          <td class="dc-r dc-num">{compact(g.tokens?.total_tokens ?? 0)}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
                {groups.length > TOP && (
                  <button type="button" class="dc-lane-more" onClick={() => setAll(!all)}>
                    {all ? "Show the busiest only" : `Show all ${groups.length}`}
                  </button>
                )}
              </div>
            )}
          </section>
        </div>
      )}

      <section class="dc-card" aria-label="Requests">
        <div class="dc-card-head">
          <h2>Requests</h2>
          <span class="dc-muted">
            {list.key === listKey && !list.error ? `${count(list.events.length)} shown, newest first` : ""}
          </span>
        </div>
        {list.error ? (
          <div class="dc-card-pad">
            <Note
              tone="danger"
              action={
                <Btn size="small" onClick={() => setTurn((n) => n + 1)}>
                  Try again
                </Btn>
              }
            >
              The requests could not be read: {list.error}.
            </Note>
          </div>
        ) : list.key !== listKey ? (
          <p class="dc-muted dc-card-pad" role="status">
            Reading requests…
          </p>
        ) : list.events.length === 0 ? (
          <p class="dc-muted dc-card-pad">
            No request {hasFilters(filters) ? "matches these filters" : `in ${range.words}`}.
          </p>
        ) : (
          <>
            <RequestList events={list.events} fleet={fleet} now={now} label="Requests, newest first" />
            <div class="dc-list-foot">
              {list.more ? (
                <Btn onClick={older} disabled={list.busy}>
                  {list.busy ? "Reading…" : `Show ${PAGE} older`}
                </Btn>
              ) : (
                <span class="dc-muted">
                  {s && s.total_requests > list.events.length
                    ? "Older requests are counted in the totals; pluxy keeps the list for the last two days."
                    : "That is all of them."}
                </span>
              )}
              {list.lost && <span class="dc-muted">Some older requests are no longer available.</span>}
            </div>
          </>
        )}
      </section>
    </div>
  );
}

function filterValue(fleet: Fleet, name: FilterName, value: string): string {
  if (name === "key") return fleet.keyName(value);
  if (name === "account") return fleet.mask(value);
  if (name === "provider") return fleet.providers.of(value).name;
  return value;
}

/** The tokens of the range by kind: input with what was read from cache, output with the reasoning part. */
function Tokens(props: {
  s: {
    tokens?: {
      input_tokens: number;
      output_tokens: number;
      reasoning_tokens: number;
      cached_tokens: number;
      cache_creation_tokens: number;
      total_tokens: number;
    };
  };
}) {
  const t = props.s.tokens;
  if (!t || t.total_tokens === 0) return null;
  const total = Math.max(1, t.input_tokens + t.output_tokens);
  const pct = (n: number) => `${(n / total) * 100}%`;
  return (
    <div class="dc-tokens">
      <div
        class="dc-tokens-bar"
        role="img"
        aria-label={`${count(t.input_tokens)} input tokens and ${count(t.output_tokens)} output tokens`}
      >
        <i data-kind="cached" style={{ width: pct(t.cached_tokens) }} />
        <i data-kind="input" style={{ width: pct(Math.max(0, t.input_tokens - t.cached_tokens)) }} />
        <i data-kind="output" style={{ width: pct(Math.max(0, t.output_tokens - t.reasoning_tokens)) }} />
        <i data-kind="reasoning" style={{ width: pct(t.reasoning_tokens) }} />
      </div>
      <dl class="dc-tokens-key">
        <div data-kind="input">
          <dt>Input</dt>
          <dd class="dc-num">{compact(t.input_tokens)}</dd>
        </div>
        <div data-kind="cached">
          <dt>of it read from cache</dt>
          <dd class="dc-num">{compact(t.cached_tokens)}</dd>
        </div>
        <div data-kind="output">
          <dt>Output</dt>
          <dd class="dc-num">{compact(t.output_tokens)}</dd>
        </div>
        <div data-kind="reasoning">
          <dt>of it reasoning</dt>
          <dd class="dc-num">{compact(t.reasoning_tokens)}</dd>
        </div>
      </dl>
    </div>
  );
}
