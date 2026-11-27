// Usage: what the requests add up to over 24 hours, 7 days or 30 days, split by provider, model,
// account or key. The figures, the chart and the breakdown come from one read of GET /usage in
// hourly steps, added into local days, read again every minute while the tab is on screen.

import { useCallback, useEffect, useId, useRef, useState } from "preact/hooks";
import { problem, type UsageSummary } from "../api";
import { keyName, Phases, providerName, StatePane, timeTaken, useNaming } from "../parts";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Choices, type Loaded, PageHead, TableFrame, useKit, useLoad, usePoll } from "../ui";
import {
  type Bar,
  figuresOf,
  hotspotOf,
  hourBars,
  localDays,
  NO_FILTERS,
  PICKED,
  type Picked,
  queryOf,
  type Range,
  rangeDays,
  shortCount,
  succeededPercent,
  usageSince,
  useAddressQuery,
} from "./activity";
import { FilterChip } from "./activityChips";
import { RecordOff } from "./activityParts";
import { type Metric, UsageChart } from "./usageChart";

const t = strings("usage", {
  title: "Usage",
  tip: "What the requests add up to",
  refresh: "Refresh",
  range: "Range",
  h24: "24 hours",
  d7: "7 days",
  d30: "30 days",
  loading: "Adding up requests…",
  failed: "Could not add up the requests.",
  none: "No requests in the last {range}.",
  starts: "pluxy's record starts {when}.",
  by: "By",
  provider: "Provider",
  model: "Model",
  account: "Account",
  key: "Key",
  requests: "Requests",
  failedCol: "Failed",
  tokens: "Tokens",
  time: "Avg time",
  showAll: "Show all {count}",
  showTop: "Show the top 10",
  seeThem: "See them",
  seeThese: "See these requests",
  remove: "Remove this filter",
  share: "Share",
  chartShows: "What the chart shows",
  tileRequests: "Requests",
  tileThrough: "Went through",
  tileTokens: "Tokens",
  tileTime: "Avg time",
  tileFailed: "{count} failed",
  tileNoneFailed: "None failed",
  tileOf: "{ok} of {total}",
  tileCached: "{tokens} from cache",
  tilePer: "per request",
  breakdown: "Breakdown",
});

const TOP = 10;

type By = "providers" | "models" | "accounts" | "keys";
const BYS: { value: By; filter: Picked }[] = [
  { value: "providers", filter: "provider" },
  { value: "models", filter: "model" },
  { value: "accounts", filter: "account" },
  { value: "keys", filter: "key" },
];

/** A share of the requests, as a person reads it: 62%, or <1% when it rounds to nothing. */
const sharePercent = (part: number, total: number) => {
  const percent = (part / Math.max(1, total)) * 100;
  return percent > 0 && percent < 0.5 ? "<1%" : `${Math.round(percent)}%`;
};

const rangeWord = (range: Range) => (range === "24h" ? t("h24") : range === "7d" ? t("d7") : t("d30"));
const kindWord = (filter: Picked) =>
  filter === "provider"
    ? t("provider")
    : filter === "model"
      ? t("model")
      : filter === "account"
        ? t("account")
        : t("key");

/** GET /usage for a query, read again every minute and when the query changes. */
function useSummary(query: string): Loaded<UsageSummary> {
  const { api, unauthorized } = useKit();
  const [state, setState] = useState<{
    query: string;
    phase: "ready" | "failed";
    data?: UsageSummary;
    error?: string;
  }>();
  const [loading, setLoading] = useState(true);
  const run = useRef(0);
  const queryRef = useRef(query);
  queryRef.current = query;
  const load = useCallback(async () => {
    const mine = ++run.current;
    const asked = queryRef.current;
    setLoading(true);
    const r = await api.usage(asked);
    if (mine !== run.current) return;
    setLoading(false);
    if (r.status === 401) return unauthorized();
    const error = problem(r);
    setState((s) =>
      error === undefined
        ? { query: asked, phase: "ready", data: r.body }
        : { query: asked, phase: "failed", data: s?.query === asked ? s.data : undefined, error },
    );
  }, [api, unauthorized]);
  useEffect(() => {
    void load();
    return () => {
      run.current++;
    };
  }, [query]);
  usePoll(load, 60_000);
  // Figures of another range or filter are not shown while this one is read.
  const mine = state && state.query === query ? state : undefined;
  const reload = useCallback(() => void load(), [load]);
  if (!mine) return { phase: "loading", loading: true, reload };
  if (mine.phase === "failed")
    return { phase: "failed", data: mine.data, error: mine.error ?? "", loading, reload };
  return { phase: "ready", data: mine.data, loading, reload };
}

/**
 * The figures of the range as four tiles. Three of them are also the chart's tabs: the one chosen
 * is underlined and the chart under them shows that figure for each bar. The fourth (the time a
 * request took) has no figure per bar, so it is only read.
 */
function UsageTiles(props: { u: UsageSummary; metric: Metric; onMetric: (m: Metric) => void }) {
  const { u } = props;
  const name = useId();
  const tokens = u.tokens?.total_tokens ?? 0;
  const cached = u.tokens?.cached_tokens ?? 0;
  const tile = (metric: Metric, label: string, value: string, sub: string | undefined) => (
    <label class="usage-tile" data-metric={metric}>
      <input
        type="radio"
        name={name}
        value={metric}
        checked={props.metric === metric}
        onChange={() => props.onMetric(metric)}
      />
      <span class="usage-tile-label">{label}</span>
      <span class="usage-tile-value">{value}</span>
      <span class="usage-tile-sub">{sub ?? "\u00a0"}</span>
    </label>
  );
  return (
    <fieldset class="usage-tiles" data-part="usage-tiles">
      <legend class="sr-only">{t("chartShows")}</legend>
      {tile(
        "requests",
        t("tileRequests"),
        u.total_requests.toLocaleString(),
        u.failure_count > 0
          ? t("tileFailed", { count: u.failure_count.toLocaleString() })
          : t("tileNoneFailed"),
      )}
      {tile(
        "through",
        t("tileThrough"),
        succeededPercent(u.success_count, Math.max(1, u.total_requests)),
        t("tileOf", { ok: u.success_count.toLocaleString(), total: u.total_requests.toLocaleString() }),
      )}
      {tile(
        "tokens",
        t("tileTokens"),
        tokens > 0 ? shortCount(tokens) : "—",
        cached > 0 ? t("tileCached", { tokens: shortCount(cached) }) : undefined,
      )}
      <div class="usage-tile" data-static="">
        <span class="usage-tile-label">{t("tileTime")}</span>
        <span class="usage-tile-value">{u.average_latency_ms ? timeTaken(u.average_latency_ms) : "—"}</span>
        <span class="usage-tile-sub">{t("tilePer")}</span>
      </div>
    </fieldset>
  );
}

export function UsagePage() {
  const { api } = useKit();
  const [address, setAddress] = useAddressQuery();
  const [range, setRange] = useState<Range>("7d");
  const [by, setBy] = useState<By>("providers");
  const [all, setAll] = useState(false);
  const [metric, setMetric] = useState<Metric>("requests");
  const accounts = useLoad(api.accounts);
  const keys = useLoad(api.clientKeys);
  const naming = useNaming(accounts.data?.accounts);
  // The tab holds one filter: the first its address names.
  const picked = PICKED.find((k) => address.get(k));
  const value = picked ? (address.get(picked) ?? "") : "";
  const filterQuery = picked ? `&${picked}=${encodeURIComponent(value)}` : "";
  const summary = useSummary(`since=${encodeURIComponent(usageSince(range))}&bucket=1h${filterQuery}`);
  const u = summary.data;

  const nameOf = (filter: Picked, name: string) =>
    filter === "provider"
      ? providerName(name)
      : filter === "account"
        ? naming.byId(name)
        : filter === "key"
          ? (keyName(name, keys.data?.keys) ?? name)
          : name;
  const refresh = () => {
    summary.reload();
    accounts.reload();
    keys.reload();
  };
  const filterTo = (filter: Picked, name: string) => setAddress(queryOf({ ...NO_FILTERS, [filter]: name }));

  const bars: Bar[] = u ? (range === "24h" ? hourBars(u.series) : localDays(u.series, rangeDays(range))) : [];
  const figures = u?.enabled ? figuresOf(u, timeTaken) : "";
  const oldest = u?.oldest ? new Date(u.oldest) : undefined;
  const starts = new Date(range === "24h" ? Date.now() - 24 * 3_600_000 : usageSince(range));
  const record =
    oldest && !Number.isNaN(oldest.getTime()) && oldest.getTime() > starts.getTime()
      ? oldest.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" })
      : undefined;
  const hotspot = u ? hotspotOf(u) : undefined;

  const group = BYS.find((b) => b.value === by) ?? (BYS[0] as (typeof BYS)[number]);
  const groups = u
    ? [...u[by]].filter((g) => g.total_requests > 0).sort((a, b) => b.total_requests - a.total_requests)
    : [];
  const rows = all ? groups : groups.slice(0, TOP);
  const total = Math.max(1, u?.total_requests ?? 0);

  if (u && !u.enabled) {
    return (
      <div class="activity" data-part="activity">
        <PageHead
          title={t("title")}
          actions={
            <Button onClick={refresh} disabled={summary.loading}>
              {t("refresh")}
            </Button>
          }
        />
        <RecordOff />
      </div>
    );
  }
  return (
    <div class="activity" data-part="activity">
      <PageHead
        title={t("title")}
        actions={
          <Button onClick={refresh} disabled={summary.loading}>
            {t("refresh")}
          </Button>
        }
      />
      <div class="usage-top">
        <div class="usage-filter">
          {picked && (
            <>
              <FilterChip
                kind={kindWord(picked)}
                name={nameOf(picked, value)}
                onRemove={() => setAddress("")}
              />
              <a href={`#/activity?${queryOf({ ...NO_FILTERS, [picked]: value })}`}>{t("seeThese")}</a>
            </>
          )}
          {record && <p class="usage-note">{t("starts", { when: record })}</p>}
        </div>
        <div class="usage-range">
          <Choices
            label={t("range")}
            value={range}
            choices={[
              { value: "24h", label: t("h24") },
              { value: "7d", label: t("d7") },
              { value: "30d", label: t("d30") },
            ]}
            onChange={(v) => setRange(v)}
          />
        </div>
      </div>
      <p class="usage-figures sr-only" aria-live="polite">
        {figures}
      </p>
      <Phases
        load={summary}
        loading={t("loading")}
        failed={{ title: t("failed") }}
        isEmpty={(d) => d.total_requests === 0}
        empty={<StatePane kind="empty" title={t("none", { range: rangeWord(range) })} />}
      >
        {() => (
          <>
            <section class="usage-panel" data-part="usage-panel">
              {u && <UsageTiles u={u} metric={metric} onMetric={setMetric} />}
              <UsageChart
                bars={bars}
                unit={range === "24h" ? "hour" : "day"}
                range={rangeWord(range)}
                metric={metric}
              />
            </section>
            {hotspot && (
              <p class="usage-hotspot">
                {hotspot.text}{" "}
                <a href={`#/activity?${queryOf({ ...NO_FILTERS, failed: true, model: hotspot.model })}`}>
                  {t("seeThem")}
                </a>
              </p>
            )}
            <section class="usage-by" aria-label={t("breakdown")}>
              <div class="usage-by-head">
                <Choices
                  label={t("by")}
                  value={by}
                  choices={[
                    { value: "providers", label: t("provider") },
                    { value: "models", label: t("model") },
                    { value: "accounts", label: t("account") },
                    { value: "keys", label: t("key") },
                  ]}
                  onChange={(v) => {
                    setBy(v);
                    setAll(false);
                  }}
                />
              </div>
              <div class="usage-breakdown">
                <TableFrame>
                  <table class="table" data-part="usage-breakdown">
                    <thead>
                      <tr>
                        <th scope="col">{kindWord(group.filter)}</th>
                        <th scope="col" class="num">
                          {t("requests")}
                        </th>
                        <th scope="col" class="usage-bar-col">
                          {t("share")}
                        </th>
                        <th scope="col" class="num">
                          {t("failedCol")}
                        </th>
                        <th scope="col" class="num">
                          {t("tokens")}
                        </th>
                        <th scope="col" class="num">
                          {t("time")}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((g) => (
                        <tr key={g.name}>
                          <th scope="row">
                            <button
                              type="button"
                              class="usage-name"
                              title={nameOf(group.filter, g.name)}
                              onClick={() => filterTo(group.filter, g.name)}
                            >
                              {nameOf(group.filter, g.name)}
                            </button>
                          </th>
                          <td class="num">{g.total_requests.toLocaleString()}</td>
                          <td class="usage-bar-col">
                            <span class="usage-share">
                              <span class="usage-bar" aria-hidden="true">
                                {g.success_count > 0 && (
                                  <i
                                    data-outcome="success"
                                    style={{ width: `${(g.success_count / total) * 100}%` }}
                                  />
                                )}
                                {g.failure_count > 0 && (
                                  <i
                                    data-outcome="failure"
                                    style={{ width: `${(g.failure_count / total) * 100}%` }}
                                  />
                                )}
                              </span>
                              <span class="usage-percent">{sharePercent(g.total_requests, total)}</span>
                            </span>
                          </td>
                          <td class="num" data-failed={g.failure_count > 0 ? "" : undefined}>
                            {g.failure_count.toLocaleString()}
                          </td>
                          <td class="num">
                            {g.tokens?.total_tokens ? shortCount(g.tokens.total_tokens) : "—"}
                          </td>
                          <td class="num">{g.average_latency_ms ? timeTaken(g.average_latency_ms) : "—"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </TableFrame>
              </div>
              {groups.length > TOP && (
                <Button variant="ghost" onClick={() => setAll(!all)}>
                  {all ? t("showTop") : t("showAll", { count: groups.length })}
                </Button>
              )}
            </section>
          </>
        )}
      </Phases>
    </div>
  );
}

export const usageTabPage = (): PageDef => ({
  id: "activity/usage",
  dest: "activity",
  kind: "tab",
  title: t("title"),
  label: t("title"),
  tip: t("tip"),
  render: () => <UsagePage />,
});
