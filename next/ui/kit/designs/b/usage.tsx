// Usage: what the recorded requests add up to over a chosen time, said in a sentence, drawn as
// one plain chart, broken down by provider, model, account or client key, and then the requests
// themselves, each with why it went where it did.

import { useEffect, useState } from "preact/hooks";
import type { UsageEvent, UsageGroup, UsageSummary } from "../../api";
import { useKit } from "../../ui";
import { useCore } from "./core";
import { useRead } from "./data";
import { keyName } from "./home";
import { latency, num, percent, plural, providerName, short } from "./lib";
import { Asking, Note, Section, TimeBars, Trouble, Words } from "./parts";
import { RequestList } from "./requests";

type Range = "24h" | "7d" | "30d";

const RANGES: { value: Range; label: string; said: string; query: string }[] = [
  { value: "24h", label: "24 hours", said: "the last 24 hours", query: "since=24h&bucket=1h&buckets=24" },
  { value: "7d", label: "7 days", said: "the last seven days", query: "since=7d&bucket=24h&buckets=7" },
  { value: "30d", label: "30 days", said: "the last 30 days", query: "since=30d&bucket=24h&buckets=30" },
];

type By = "providers" | "models" | "accounts" | "keys";

const BYS: { value: By; label: string; filter: FilterKey }[] = [
  { value: "providers", label: "Provider", filter: "provider" },
  { value: "models", label: "Model", filter: "model" },
  { value: "accounts", label: "Account", filter: "account" },
  { value: "keys", label: "Client key", filter: "key_id" },
];

type FilterKey = "provider" | "model" | "account" | "key_id";
type Filter = Partial<Record<FilterKey, string>>;

const FILTER_KEYS: FilterKey[] = ["provider", "model", "account", "key_id"];
const PAGE = 50;
const TOP = 10;

/** The filter the address carries after "usage?", such as usage?account=acct-1. */
function filterOf(query: string): Filter {
  const params = new URLSearchParams(query);
  const filter: Filter = {};
  for (const key of FILTER_KEYS) {
    const value = params.get(key);
    if (value) filter[key] = value;
  }
  return filter;
}

const queryOf = (filter: Filter) =>
  FILTER_KEYS.filter((k) => filter[k])
    .map((k) => `&${k}=${encodeURIComponent(filter[k] ?? "")}`)
    .join("");

/** The time's totals as two or three sentences. */
function totalsWords(u: UsageSummary, said: string): string[] {
  if (u.total_requests === 0) return [`No requests in ${said}.`];
  const out = [
    `${plural(u.total_requests, "request")} in ${said}. ${
      u.failure_count === 0
        ? "All of them went through."
        : `${num(u.success_count)} went through (${percent(u.success_count, u.total_requests)}) and ${num(u.failure_count)} failed.`
    }`,
  ];
  const t = u.tokens;
  if (t && t.total_tokens > 0) {
    const cached = t.cached_tokens > 0 ? `, ${short(t.cached_tokens)} of the input read from cache` : "";
    out.push(
      `They used ${short(t.total_tokens)} tokens: ${short(t.input_tokens)} in and ${short(t.output_tokens)} out${cached}.${
        u.average_latency_ms ? ` A reply took ${latency(u.average_latency_ms)} on average.` : ""
      }`,
    );
  }
  return out;
}

export function Usage(props: { query: string }) {
  const { api, unauthorized } = useKit();
  const core = useCore();
  const [range, setRange] = useState<Range>("7d");
  const [by, setBy] = useState<By>("providers");
  const [filter, setFilter] = useState<Filter>(() => filterOf(props.query));
  const [failedOnly, setFailedOnly] = useState(false);
  const [all, setAll] = useState(false);
  // The address may name a filter after the page is open (a link from an account's page).
  useEffect(() => setFilter(filterOf(props.query)), [props.query]);

  const r = RANGES.find((x) => x.value === range) ?? RANGES[1];
  const filterQuery = queryOf(filter);
  const summary = useRead(() => api.usage(`${r?.query}${filterQuery}`), 60_000, `${range}${filterQuery}`);
  const keys = useRead(api.clientKeys);

  // The requests, newest first, a page at a time; a new time or filter starts the list again.
  const eventsKey = `${range}${filterQuery}${failedOnly}`;
  const [events, setEvents] = useState<{ key: string; list: UsageEvent[]; next: number; more: boolean }>();
  const [eventsError, setEventsError] = useState<string>();
  const [loadingMore, setLoadingMore] = useState(false);
  const load = async (before: number, old: UsageEvent[]) => {
    setLoadingMore(true);
    const reply = await api.usageEventsPage(
      `before=${before}&limit=${PAGE}&since=${range}${filterQuery}${failedOnly ? "&failed=true" : ""}`,
    );
    setLoadingMore(false);
    if (reply.status === 401) return unauthorized();
    if (!reply.body || reply.status !== 200) {
      setEventsError(reply.error?.message ?? "pluxy did not answer");
      return;
    }
    setEventsError(undefined);
    setEvents({
      key: eventsKey,
      list: [...old, ...reply.body.events],
      next: reply.body.next,
      more: reply.body.more,
    });
  };
  useEffect(() => {
    void load(0, []);
  }, [eventsKey]);
  const shownEvents = events?.key === eventsKey ? events : undefined;

  const u = summary.data;
  const { naming, now } = core;
  const nameOf = (key: FilterKey, value: string) =>
    key === "provider"
      ? providerName(value)
      : key === "account"
        ? naming.name(value)
        : key === "key_id"
          ? keyName(value, keys.data?.keys)
          : value;
  const active = FILTER_KEYS.filter((k) => filter[k]);
  const dim = BYS.find((b) => b.value === by) ?? BYS[0];
  const groups: UsageGroup[] = u
    ? [...u[by]].filter((g) => g.total_requests > 0).sort((a, b) => b.total_requests - a.total_requests)
    : [];
  const top = Math.max(1, ...groups.map((g) => g.total_requests));
  const rows = all ? groups : groups.slice(0, TOP);

  return (
    <article class="db-page db-usage">
      <header class="db-page-head">
        <div>
          <h1>Usage</h1>
          {u?.enabled !== false &&
            (u ? (
              totalsWords(u, r?.said ?? "").map((s) => (
                <p class="db-lead" key={s}>
                  {s}
                </p>
              ))
            ) : summary.error ? null : (
              <p class="db-lead db-lead-asking" role="status">
                Adding up {r?.said}…
              </p>
            ))}
        </div>
        <div class="db-page-actions">
          <Words label="Time" value={range} options={RANGES} onChange={setRange} />
        </div>
      </header>

      {active.length > 0 && (
        <p class="db-filters">
          Only{" "}
          {active.map((k, i) => (
            <span key={k}>
              {i > 0 && " and "}
              <b>{nameOf(k, filter[k] ?? "")}</b>{" "}
              <button
                type="button"
                class="db-linkish"
                onClick={() => {
                  const next = { ...filter };
                  delete next[k];
                  setFilter(next);
                }}
                aria-label={`Stop filtering by ${nameOf(k, filter[k] ?? "")}`}
              >
                show all
              </button>
            </span>
          ))}
        </p>
      )}

      {summary.error && !u && (
        <Trouble retry={summary.reload}>pluxy could not add this up: {naming.mask(summary.error)}.</Trouble>
      )}
      {summary.error && u && (
        <Trouble retry={summary.reload} quiet>
          These are the last figures pluxy gave. It is not answering now: {naming.mask(summary.error)}
        </Trouble>
      )}
      {u && !u.enabled && (
        <Note tone="warn">
          pluxy is not recording requests, so there is nothing to add up.{" "}
          <a href="#/config/logging">Turn on usage statistics</a>
        </Note>
      )}
      {u && (u.write_errors ?? 0) > 0 && (
        <Note tone="warn">
          pluxy could not save {plural(u.write_errors ?? 0, "request")} to its usage files since it started.
          They are counted here until it restarts; its log says why.
        </Note>
      )}
      {!u && summary.loading && (
        <div class="db-ghost" aria-hidden="true">
          <i />
          <i />
          <i />
        </div>
      )}

      {u?.enabled && u.total_requests > 0 && (
        <>
          <TimeBars
            label={`Requests over ${r?.said}`}
            buckets={u.series}
            tick={(d, i) =>
              range === "24h"
                ? d.getHours() % 6 === 0
                  ? `${String(d.getHours()).padStart(2, "0")}:00`
                  : undefined
                : range === "7d"
                  ? d.toLocaleDateString("en-GB", { weekday: "short", timeZone: "UTC" })
                  : i % 7 === 2
                    ? d.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" })
                    : undefined
            }
            span={(d) =>
              range === "24h"
                ? `${d.toLocaleDateString("en-GB", { weekday: "short" })} ${String(d.getHours()).padStart(2, "0")}:00`
                : d.toLocaleDateString("en-GB", {
                    weekday: "long",
                    day: "numeric",
                    month: "long",
                    timeZone: "UTC",
                  })
            }
          />

          <Section
            title="Where it went"
            aside={
              <Words
                label="Break down by"
                value={by}
                options={BYS}
                onChange={(v) => {
                  setBy(v);
                  setAll(false);
                }}
              />
            }
          >
            <div class="db-scroll">
              <table class="db-table db-breakdown">
                <thead>
                  <tr>
                    <th scope="col">{dim?.label}</th>
                    <th scope="col" class="db-n">
                      Requests
                    </th>
                    <th scope="col">
                      <span class="db-sr">Share of the largest</span>
                    </th>
                    <th scope="col" class="db-n">
                      Failed
                    </th>
                    <th scope="col" class="db-n">
                      Tokens
                    </th>
                    <th scope="col" class="db-n">
                      Average reply
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((g) => {
                    const key = dim?.filter ?? "provider";
                    const name = nameOf(key, g.name);
                    return (
                      <tr key={g.name}>
                        <th scope="row">
                          <button
                            type="button"
                            class="db-linkish db-breakdown-name"
                            title={`${name}: show only this`}
                            onClick={() => setFilter({ ...filter, [key]: g.name })}
                          >
                            {name}
                          </button>
                          {by === "accounts" && g.provider && <small> {providerName(g.provider)}</small>}
                        </th>
                        <td class="db-n">{num(g.total_requests)}</td>
                        <td class="db-breakdown-bar" aria-hidden="true">
                          <span class="db-bars-track">
                            {g.success_count > 0 && (
                              <i style={{ width: `${(g.success_count / top) * 100}%` }} />
                            )}
                            {g.failure_count > 0 && (
                              <i data-failed="" style={{ width: `${(g.failure_count / top) * 100}%` }} />
                            )}
                          </span>
                        </td>
                        <td class="db-n">
                          {g.failure_count > 0 ? num(g.failure_count) : <span class="db-faint">none</span>}
                        </td>
                        <td class="db-n">{g.tokens ? short(g.tokens.total_tokens) : ""}</td>
                        <td class="db-n">{g.average_latency_ms ? latency(g.average_latency_ms) : ""}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {groups.length > TOP && (
              <p class="db-more">
                <button type="button" class="db-linkish" onClick={() => setAll(!all)}>
                  {all ? `Show the top ${TOP}` : `Show all ${num(groups.length)}`}
                </button>
              </p>
            )}
          </Section>
        </>
      )}

      {u?.enabled !== false && (
        <Section
          title="Requests"
          aside={
            <Words
              label="Which requests"
              value={failedOnly ? "failed" : "all"}
              options={[
                { value: "all", label: "All" },
                { value: "failed", label: "Failed" },
              ]}
              onChange={(v) => setFailedOnly(v === "failed")}
            />
          }
        >
          {eventsError && (
            <Trouble retry={() => load(0, [])}>
              pluxy did not give the requests: {naming.mask(eventsError)}.
            </Trouble>
          )}
          {!shownEvents && !eventsError && <Asking lines={6}>Asking pluxy for the requests…</Asking>}
          {shownEvents &&
            (shownEvents.list.length === 0 ? (
              <p class="db-quiet">
                {failedOnly ? `Nothing failed in ${r?.said}.` : `No requests in ${r?.said}.`}
              </p>
            ) : (
              <>
                <RequestList events={shownEvents.list} naming={naming} keys={keys.data?.keys} now={now} />
                <p class="db-more">
                  {shownEvents.more ? (
                    <>
                      Showing the newest {num(shownEvents.list.length)}.{" "}
                      <button
                        type="button"
                        class="db-linkish"
                        disabled={loadingMore}
                        onClick={() => load(shownEvents.next, shownEvents.list)}
                      >
                        {loadingMore ? "Asking…" : `Show ${PAGE} older`}
                      </button>
                    </>
                  ) : (
                    `That is all ${num(shownEvents.list.length)}.`
                  )}
                </p>
              </>
            ))}
        </Section>
      )}
    </article>
  );
}
