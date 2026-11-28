// Requests: every request pluxy still keeps, newest first, under the day each came on. A row opens
// to why it went where it did, what each attempt came to, and its facts. The list is read on open,
// on Refresh and when a filter changes, never on a timer, so a row being read does not move.

import { useEffect, useRef, useState } from "preact/hooks";
import { type ClientKey, problem, type UsageEvent } from "../api";
import { Icon } from "../icons";
import { keyName, providerName, StaleNotice, StatePane, timeTaken, tryAgainWord, useNaming } from "../parts";
import { usePrivacy } from "../privacy";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { failureWord } from "../states";
import { rich, strings } from "../strings";
import { Button, Choices, Empty, MasterDetail, Notice, PageHead, TableFrame, useKit, useLoad } from "../ui";
import {
  accountValue,
  byDay,
  coreFilters,
  csvName,
  csvOf,
  detourOf,
  type Filters,
  filtersOf,
  hasFilters,
  NO_FILTERS,
  type Picked,
  queryOf,
  readDownTo,
  readNewest,
  SAVE_LIMIT,
  traceMode,
  useAddressQuery,
  whyOf,
} from "./activity";
import { FilterChip, FilterMenu, WordChip } from "./activityChips";
import { RecordOff, recordOffReason } from "./activityParts";
import { RequestPane } from "./requestPane";

const t = strings("requests", {
  title: "Requests",
  tip: "Every request pluxy still keeps",
  refresh: "Refresh",
  save: "Save as spreadsheet",
  showOlder: "Show older",
  clearFilters: "Clear filters",
  which: "Which requests",
  all: "All",
  failed: "Failed",
  everyProvider: "Every provider",
  everyAccount: "Every account",
  everyModel: "Every model",
  everyKey: "Every key",
  loading: "Reading requests…",
  failedTitle: "Could not read the requests.",
  noRecent: "No recent requests.",
  noRecentText: "Requests from your coding tools show here once a tool uses pluxy.",
  connect: "Connect a tool",
  olderCounted: "pluxy keeps the details of recent requests only. {usage} counts older ones.",
  usage: "Usage",
  noMatch: "No requests match.",
  end: "That is every request pluxy keeps, back to {when}.",
  endToday: "{time} today",
  endYesterday: "yesterday {time}",
  endUsage: "Usage counts older ones.",
  gone: "That request is no longer kept.",
  lost: {
    one: "pluxy could not save {count} request to disk since it started. They show here until pluxy restarts.",
    other:
      "pluxy could not save {count} requests to disk since it started. They show here until pluxy restarts.",
  },
  logs: "Logs",
  hidden: "Names are hidden on screen only. The saved file has them.",
  savedCapped: "Saved the newest {count} matching requests. pluxy keeps more.",
  saveFailed: "Could not read the requests to save. Try again.",
  list: "Requests",
  colTime: "Time",
  colModel: "Model",
  colAccount: "Account",
  colKey: "Key",
  colTokens: "Tokens",
  colTaken: "Time taken",
  colResult: "Result",
  succeeded: "Succeeded",
  provider: "Provider",
  account: "Account",
  model: "Model",
  keyKind: "Key",
});

const SHOW = 50;

/**
 * The time of a request as the end of the list says it, so that it never names today's weekday
 * for another day: "14:05 today", "yesterday 14:05", "Wednesday 14:05" within the last week, and
 * "Nov 3, 14:05" before that.
 */
export function endWhen(time: string, now: number = Date.now()): string {
  const d = new Date(time);
  if (Number.isNaN(d.getTime())) return time;
  const clock = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const daysBack = (n: number) => {
    const today = new Date(now);
    return new Date(today.getFullYear(), today.getMonth(), today.getDate() - n).getTime();
  };
  if (d.getTime() >= daysBack(0)) return t("endToday", { time: clock });
  if (d.getTime() >= daysBack(1)) return t("endYesterday", { time: clock });
  if (d.getTime() >= daysBack(6)) {
    return `${d.toLocaleDateString(undefined, { weekday: "long" })} ${clock}`;
  }
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })}, ${clock}`;
}

interface List {
  /** The filters and trace the rows were read for. */
  key: string;
  rows: UsageEvent[];
  more: boolean;
  /** When they were read. */
  at: number;
  /** A trace was asked for: whether it was among them. */
  found?: boolean;
}

export function RequestsPage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const [query, setQuery] = useAddressQuery();
  const filters = filtersOf(query);
  const trace = query.get("trace") ?? "";
  const key = queryOf(filters, trace);
  const accounts = useLoad(api.accounts);
  const keys = useLoad(api.clientKeys);
  const settings = useLoad(api.coreSettings);
  const summary = useLoad(() => api.usage("since=30d&bucket=24h"));
  const naming = useNaming(accounts.data?.accounts);
  const requestLog = settings.data?.observability.logs["request-log"];
  const [list, setList] = useState<List>();
  const [failure, setFailure] = useState<string>();
  const [reading, setReading] = useState(true);
  const [older, setOlder] = useState(false);
  const [off, setOff] = useState(false);
  const [open, setOpen] = useState<number>();
  const [revision, setRevision] = useState(0);
  const [stamp, setStamp] = useState(0);
  const run = useRef(0);
  const root = useRef<HTMLDivElement>(null);
  const saved = useSave();
  const [saving, setSaving] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const [capped, setCapped] = useState(false);
  const [wasSaved, setWasSaved] = useState(false);

  const coreFilter = () => coreFilters(filters, accounts.data?.accounts);

  // Read the list: on open, on Refresh and when a filter or the trace changes.
  useEffect(() => {
    if (accounts.phase === "loading") return;
    const mine = ++run.current;
    setReading(true);
    setFailure(undefined);
    const read = (before: number, limit: number) => api.usageEventsBefore(coreFilter(), before, limit);
    void (async () => {
      let rows: UsageEvent[];
      let more: boolean;
      let found: boolean | undefined;
      let enabled: boolean | undefined;
      if (trace) {
        const r = await readDownTo(trace, SHOW, async (before, limit) => {
          const reply = await read(before, limit);
          if (reply.body?.enabled === false) enabled = false;
          return reply;
        });
        if (mine !== run.current) return;
        if ("unauthorized" in r) return unauthorized();
        if ("error" in r) {
          setFailure(r.error);
          setReading(false);
          return;
        }
        ({ rows, more, found } = r);
      } else {
        const r = await read(0, SHOW);
        if (mine !== run.current) return;
        if (r.status === 401) return unauthorized();
        const why = problem(r);
        if (why !== undefined || !r.body) {
          setFailure(why ?? "");
          setReading(false);
          return;
        }
        enabled = r.body.enabled;
        rows = r.body.events;
        more = r.body.more;
      }
      setOff(enabled === false);
      setList({ key, rows, more, at: Date.now(), found });
      setOpen(found ? rows[rows.length - 1]?.seq : undefined);
      setStamp((n) => n + 1);
      setReading(false);
    })();
    return () => {
      run.current++;
    };
  }, [key, revision, accounts.phase]);

  // A request opened from a link: scrolled into view, with focus on it.
  useEffect(() => {
    if (!list?.found || !trace) return;
    const line = root.current?.querySelector<HTMLElement>(`[data-trace="${CSS.escape(trace)}"] .req-open`);
    line?.scrollIntoView?.({ block: "center" });
    line?.focus();
  }, [stamp]);

  const shown = list && list.key === key ? list : undefined;
  const setFilters = (next: Filters) => setQuery(queryOf(next));
  const refresh = () => {
    setRevision((n) => n + 1);
    accounts.reload();
    keys.reload();
    settings.reload();
    summary.reload();
  };

  const showOlder = async () => {
    if (!shown) return;
    const last = shown.rows[shown.rows.length - 1];
    if (!last) return;
    const mine = run.current;
    setOlder(true);
    const r = await api.usageEventsBefore(coreFilter(), last.seq, SHOW);
    if (mine !== run.current) return;
    setOlder(false);
    if (r.status === 401) return unauthorized();
    const why = problem(r);
    if (why !== undefined || !r.body) {
      setFailure(why ?? "");
      return;
    }
    setFailure(undefined);
    const body = r.body;
    setList((l) =>
      l && l.key === key ? { ...l, rows: [...l.rows, ...body.events], more: body.more, at: Date.now() } : l,
    );
  };

  const save = async () => {
    setSaving(true);
    setSaveFailed(false);
    setCapped(false);
    setWasSaved(false);
    saved.clear();
    const r = await readNewest((before, limit) => api.usageEventsBefore(coreFilter(), before, limit));
    if ("unauthorized" in r) {
      setSaving(false);
      return unauthorized();
    }
    if ("error" in r) {
      setSaveFailed(true);
      setSaving(false);
      return;
    }
    await saved.save(csvName(), csvOf(r.events), "text/csv;charset=utf-8");
    setCapped(r.capped);
    setWasSaved(true);
    setSaving(false);
  };

  const recordOff = off || summary.data?.enabled === false;
  const providers = [
    ...new Set((accounts.data?.accounts ?? []).flatMap((a) => (a.extension ? [a.extension] : []))),
  ]
    .map((value) => ({ value, label: providerName(value) }))
    .sort((a, b) => a.label.localeCompare(b.label));
  const accountOptions = (accounts.data?.accounts ?? [])
    .map((a) => ({ value: a.id, label: naming.of(a) }))
    .sort((a, b) => a.label.localeCompare(b.label));
  const models = [...new Set((summary.data?.models ?? []).map((m) => m.name))]
    .sort((a, b) => a.localeCompare(b))
    .map((value) => ({ value, label: value }));
  const keyOptions = (keys.data?.keys ?? [])
    .map((k) => ({ value: k.id, label: keyName(k.id, keys.data?.keys, undefined, p.id) ?? k.id }))
    .sort((a, b) => a.label.localeCompare(b.label));
  const selects: {
    filter: Picked;
    kind: string;
    every: string;
    options: { value: string; label: string }[];
  }[] = [
    { filter: "provider", kind: t("provider"), every: t("everyProvider"), options: providers },
    { filter: "account", kind: t("account"), every: t("everyAccount"), options: accountOptions },
    { filter: "model", kind: t("model"), every: t("everyModel"), options: models },
    { filter: "key", kind: t("keyKind"), every: t("everyKey"), options: keyOptions },
  ];
  const valueFor = (filter: Picked) =>
    filter === "account" ? accountValue(filters.account, accounts.data?.accounts) : filters[filter];
  const offered = selects
    .filter((s) => s.options.length >= 2 || filters[s.filter] !== "")
    .map((s) => ({ ...s, value: valueFor(s.filter) }));
  const applied = offered.filter((s) => s.value !== "");
  const nameOf = (s: (typeof offered)[number]) =>
    s.options.find((o) => o.value === s.value)?.label ??
    (s.filter === "provider" ? providerName(s.value) : s.value);
  const lost = (summary.data?.dropped ?? 0) + (summary.data?.write_errors ?? 0);
  const idOf = (account: string) =>
    accounts.data?.accounts.find((a) => a.id === account || a.public_id === account)?.id ?? account;
  // An account set in the settings file has its model aliases there, not in the account's form.
  const editable = (account: string) =>
    accounts.data?.accounts.find((a) => a.id === account || a.public_id === account)?.source !== "config";
  const last = shown?.rows[shown.rows.length - 1];
  const oldest = summary.data?.oldest ? Date.parse(summary.data.oldest) : Number.NaN;
  const usageOlder = !!last && !Number.isNaN(oldest) && oldest < Date.parse(last.timestamp);
  const opened = shown?.rows.find((e) => e.seq === open);

  const table = (rows: UsageEvent[]) => (
    <TableFrame>
      <table class="table requests-table" data-part="requests-table" aria-label={t("list")}>
        <thead>
          <tr>
            <th scope="col">{t("colTime")}</th>
            <th scope="col">{t("colModel")}</th>
            <th scope="col">{t("colAccount")}</th>
            <th scope="col" class="req-key">
              {t("colKey")}
            </th>
            <th scope="col" class="num req-tokens">
              {t("colTokens")}
            </th>
            <th scope="col" class="num req-taken">
              {t("colTaken")}
            </th>
            <th scope="col">{t("colResult")}</th>
          </tr>
        </thead>
        {byDay(rows).flatMap((d) => [
          <tbody class="activity-day" key={`day-${d.day}`}>
            <tr>
              <th scope="colgroup" colSpan={7}>
                <h2 class="activity-day-heading">{d.day}</h2>
              </th>
            </tr>
          </tbody>,
          ...d.events.map((e) => (
            <RequestLine
              key={e.seq}
              event={e}
              name={naming.byId}
              keys={keys.data?.keys}
              open={open === e.seq}
              onToggle={(now) => setOpen(now ? e.seq : undefined)}
            />
          )),
        ])}
      </table>
    </TableFrame>
  );

  const body = () => {
    if (recordOff) return <RecordOff />;
    if (!shown && failure === undefined) return <StatePane kind="loading" title={t("loading")} />;
    if (!shown) {
      return (
        <StatePane
          kind="failed"
          title={t("failedTitle")}
          details={failure}
          primary={{ label: tryAgainWord(), onClick: refresh }}
        />
      );
    }
    const stale = failure !== undefined && (
      <StaleNotice
        at={shown.at}
        error={failure}
        onRetry={older || shown.rows.length === 0 ? refresh : showOlder}
      />
    );
    if (shown.rows.length === 0) {
      if (hasFilters(filters))
        return (
          <>
            {stale}
            <Empty onClear={() => setFilters(NO_FILTERS)}>{t("noMatch")}</Empty>
          </>
        );
      const counted = (summary.data?.total_requests ?? 0) > 0;
      return (
        <>
          {stale}
          <StatePane
            kind="empty"
            title={t("noRecent")}
            secondary={counted ? undefined : { label: t("connect"), href: "#/tools" }}
          >
            {counted
              ? rich(t("olderCounted"), { usage: <a href="#/activity/usage">{t("usage")}</a> })
              : t("noRecentText")}
          </StatePane>
        </>
      );
    }
    return (
      <>
        {stale}
        {!shown.found && trace && <Notice tone="muted">{t("gone")}</Notice>}
        <MasterDetail
          list={
            <div class="activity-list">
              {table(shown.rows)}
              <p class="activity-end">
                {shown.more ? (
                  <Button onClick={showOlder} busy={older}>
                    {t("showOlder")}
                  </Button>
                ) : (
                  !hasFilters(filters) && (
                    <>
                      {t("end", { when: last ? endWhen(last.timestamp) : "" })}
                      {usageOlder && (
                        <>
                          {" "}
                          <a href="#/activity/usage">{t("endUsage")}</a>
                        </>
                      )}
                    </>
                  )
                )}
              </p>
            </div>
          }
          detail={
            opened && (
              <RequestPane
                key={opened.seq}
                event={opened}
                name={naming.byId}
                idOf={idOf}
                editable={editable}
                keys={keys.data?.keys}
                trace={traceMode(opened, requestLog)}
                onClose={() => setOpen(undefined)}
              />
            )
          }
        />
      </>
    );
  };

  return (
    <div ref={root} class="activity" data-part="activity">
      <PageHead
        title={t("title")}
        actions={
          <>
            <Button onClick={refresh} disabled={reading}>
              {t("refresh")}
            </Button>
            <span title={recordOff ? recordOffReason() : undefined}>
              <Button onClick={save} disabled={recordOff || saving || (reading && !shown)} busy={saving}>
                {t("save")}
              </Button>
            </span>
          </>
        }
      />
      {!recordOff && lost > 0 && (
        <Notice tone="warn">
          {t("lost", { count: lost })} <a href="#/logs">{t("logs")}</a>
        </Notice>
      )}
      {saved.notice}
      {saveFailed && <Notice tone="danger">{t("saveFailed")}</Notice>}
      {capped && <Notice tone="muted">{t("savedCapped", { count: SAVE_LIMIT.toLocaleString() })}</Notice>}
      {wasSaved && p.on && <Notice tone="muted">{t("hidden")}</Notice>}
      {!recordOff && (
        <div class="activity-filters" data-part="activity-filters">
          <Choices
            label={t("which")}
            value={filters.failed ? "failed" : "all"}
            choices={[
              { value: "all", label: t("all") },
              { value: "failed", label: t("failed") },
            ]}
            onChange={(v) => setFilters({ ...filters, failed: v === "failed" })}
          />
          <FilterMenu
            choices={offered}
            onPick={(filter, value) => setFilters({ ...filters, [filter]: value })}
          />
          {applied.map((s) => (
            <FilterChip
              key={s.filter}
              kind={s.kind}
              name={nameOf(s)}
              onRemove={() => setFilters({ ...filters, [s.filter]: "" })}
            />
          ))}
          {hasFilters(filters) && !(shown && shown.rows.length === 0) && (
            <Button variant="ghost" onClick={() => setFilters(NO_FILTERS)}>
              {t("clearFilters")}
            </Button>
          )}
        </div>
      )}
      {body()}
    </div>
  );
}

function RequestLine(props: {
  event: UsageEvent;
  name: (account: string) => string;
  keys?: ClientKey[];
  open: boolean;
  onToggle: (open: boolean) => void;
}) {
  const p = usePrivacy();
  const e = props.event;
  const account = e.account ? props.name(e.account) : undefined;
  const model = p.text(e.model);
  const key = keyName(e.key_id, props.keys, e.label, p.id);
  const detour = detourOf(e);
  const note = whyOf(e, props.name).note;
  const when = new Date(e.timestamp);
  const full = Number.isNaN(when.getTime()) ? e.timestamp : when.toLocaleString();
  const clock = Number.isNaN(when.getTime())
    ? e.timestamp
    : when.toLocaleTimeString(undefined, {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hourCycle: "h23",
      });
  const total = e.tokens?.total_tokens;
  return (
    <tbody class="activity-request" data-trace={e.id}>
      <tr
        class="req-row"
        data-part="request-row"
        data-failed={e.failed ? "" : undefined}
        data-open={props.open ? "" : undefined}
        onClick={(ev) => {
          if (ev.target instanceof Element && ev.target.closest("a, button")) return;
          props.onToggle(!props.open);
        }}
      >
        <td class="req-time-cell">
          <button
            type="button"
            class="req-open"
            aria-expanded={props.open}
            onClick={() => props.onToggle(!props.open)}
          >
            <time class="req-time" dateTime={e.timestamp} title={full}>
              {clock}
            </time>
          </button>
        </td>
        <td class="req-model" title={model}>
          {model}
        </td>
        <td class="req-account" title={account}>
          <div class="req-account-box">
            <span class="req-account-name">{account}</span>
            {detour && <WordChip title={note}>{detour}</WordChip>}
          </div>
        </td>
        <td class="req-key" title={key}>
          {key}
        </td>
        <td class="num req-tokens">{total === undefined ? "" : total.toLocaleString()}</td>
        <td class="num req-taken">{timeTaken(e.latency_ms)}</td>
        <td class="req-result-cell">
          {e.failed ? (
            <span class="req-result" data-result="failed">
              <WordChip tone="danger">{failureWord(e)}</WordChip>
            </span>
          ) : (
            <span class="req-result" data-result="ok">
              <Icon name="check" label={t("succeeded")} />
            </span>
          )}
        </td>
      </tr>
    </tbody>
  );
}

export const requestsPage = (): PageDef => ({
  id: "usage",
  dest: "activity",
  kind: "tab",
  title: t("title"),
  label: t("title"),
  tip: t("tip"),
  render: () => <RequestsPage />,
});
