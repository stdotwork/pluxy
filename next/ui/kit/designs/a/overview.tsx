// Direction A's first screen: a status board. A strip of the range's figures over a chart, then
// one row per account (state, usage windows with the time to reset, requests in flight, the last
// error and the one action that fixes it) beside a live tail of requests with the path each took.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { UsageEvent, UsageEventsReply, UsageSummary } from "../../api";
import { usePrivacy } from "../../privacy";
import { useKit } from "../../ui";
import { usePoll } from "./data";
import { FixButton, type Fleet, type Row, useFix, useFleet } from "./fleet";
import { Icon } from "./icons";
import { useKeys } from "./keys";
import {
  ago,
  clock,
  compact,
  countBoard,
  fellBack,
  latency,
  meteredWindows,
  NEEDS_YOU,
  pathOf,
  percent,
  RANGES,
  type Range,
  rangeOf,
  rate,
  usageQuery,
} from "./lib";
import { Bars, Blank, Btn, Fault, Kbd, Skeleton, StateTag, WindowBar } from "./parts";

export function RangePicker(props: { range: Range; onPick: (r: Range) => void }) {
  return (
    <fieldset class="da-seg" aria-label="Range">
      {RANGES.map((r) => (
        <button
          key={r.id}
          type="button"
          aria-pressed={r.id === props.range.id}
          onClick={() => props.onPick(r)}
          title={`The last ${r.label}`}
        >
          {r.label}
        </button>
      ))}
    </fieldset>
  );
}

/** The range a person last picked, kept on this device. */
export function useRange(): [Range, (r: Range) => void, (step: number) => void] {
  const { host } = useKit();
  const [range, setRange] = useState(() => rangeOf(host.prefs.get("da.range")));
  const pick = (r: Range) => {
    host.prefs.set("da.range", r.id);
    setRange(r);
  };
  const step = (by: number) => {
    const i = RANGES.findIndex((r) => r.id === range.id);
    const next = RANGES[Math.max(0, Math.min(RANGES.length - 1, i + by))];
    if (next) pick(next);
  };
  return [range, pick, step];
}

function Figure(props: {
  label: string;
  value: string;
  note?: string;
  tone?: "danger" | "warn";
  title?: string;
}) {
  return (
    <div class="da-figure" title={props.title}>
      <dt>{props.label}</dt>
      <dd>
        <span class="da-figure-value da-num" data-tone={props.tone}>
          {props.value}
        </span>
        {props.note && <span class="da-figure-note da-num">{props.note}</span>}
      </dd>
    </div>
  );
}

/** The strip's figures for a range. Shared with the Requests screen. */
export function Figures(props: { usage: UsageSummary; range: Range; inFlight?: number }) {
  const u = props.usage;
  const t = u.tokens;
  return (
    <dl class="da-figures">
      <Figure
        label="requests"
        value={compact(u.total_requests)}
        note={rate(u.total_requests, props.range.seconds)}
        title={`${u.total_requests.toLocaleString("en-US")} requests in the last ${props.range.label}`}
      />
      <Figure
        label="success"
        value={u.total_requests ? percent(u.success_rate) : "–"}
        note={u.failure_count ? `${compact(u.failure_count)} failed` : "none failed"}
        tone={u.failure_count ? (u.success_rate < 0.8 ? "danger" : "warn") : undefined}
      />
      <Figure
        label="tokens"
        value={t ? compact(t.total_tokens) : "–"}
        note={t ? `${compact(t.input_tokens)} in  ${compact(t.output_tokens)} out` : undefined}
        title={
          t
            ? `${t.input_tokens.toLocaleString("en-US")} input (${t.cached_tokens.toLocaleString("en-US")} read from cache), ${t.output_tokens.toLocaleString("en-US")} output (${t.reasoning_tokens.toLocaleString("en-US")} reasoning)`
            : undefined
        }
      />
      {t && t.input_tokens > 0 && (
        <Figure label="cached" value={percent(t.cached_tokens / t.input_tokens)} note="of input" />
      )}
      <Figure
        label="latency"
        value={u.total_requests && u.average_latency_ms !== undefined ? latency(u.average_latency_ms) : "–"}
        note="mean"
      />
      {props.inFlight !== undefined && (
        <Figure
          label="in flight"
          value={String(props.inFlight)}
          note={props.inFlight === 1 ? "request" : "requests"}
        />
      )}
    </dl>
  );
}

/** The windows cell: two thin bars, or why there are none. */
export function WindowsCell(props: { row: Row; fleet: Fleet; wide?: boolean }) {
  const { row, fleet } = props;
  const w = fleet.windows.get(row.account.id);
  if (row.account.disabled || row.standing.state === "broken") return <span class="da-quiet">–</span>;
  if (!w) {
    if (fleet.pendingWindows.has(row.account.id))
      return <span class="da-window-wait" role="status" aria-label="Reading usage windows" />;
    return <span class="da-quiet">not read</span>;
  }
  if (w.unsupported)
    return (
      <span class="da-quiet" title="This provider reports no usage windows for the account.">
        no windows
      </span>
    );
  const limits = meteredWindows(w.reply?.quota.limits, fleet.now);
  if (limits.length === 0) {
    return (
      <span class="da-quiet" title={w.error}>
        {w.error ? "unread" : "no windows"}
      </span>
    );
  }
  return (
    <span
      class="da-windows"
      data-stale={w.error ? "" : undefined}
      title={w.error ? `Could not be read again: ${w.error}` : undefined}
    >
      {limits.slice(0, props.wide ? 4 : 2).map((l) => (
        <WindowBar key={l.id} limit={l} now={fleet.now} wide={props.wide} />
      ))}
    </span>
  );
}

function Board(props: { fleet: Fleet; fix: ReturnType<typeof useFix>; cursor: number }) {
  const { fleet } = props;
  const p = usePrivacy();
  const groups = useMemo(() => {
    const by = new Map<string, Row[]>();
    for (const r of fleet.rows) {
      const key = r.account.extension || "unknown";
      by.set(key, [...(by.get(key) ?? []), r]);
    }
    for (const rows of by.values()) {
      rows.sort(
        (a, b) =>
          (b.account.priority ?? 0) - (a.account.priority ?? 0) || a.account.id.localeCompare(b.account.id),
      );
    }
    return [...by.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [fleet.rows]);
  let index = -1;
  // A table for what it says; its rows are laid out as grids so they can fold to two lines on a phone.
  return (
    <table class="da-board" aria-label="Accounts">
      <thead>
        <tr class="da-board-head">
          <th scope="col">state</th>
          <th scope="col">account</th>
          <th
            scope="col"
            title="What is used of each usage window, with a tick where the clock is, and the time to its reset"
          >
            windows <span class="da-quiet">used · resets in</span>
          </th>
          <th scope="col" class="da-right" title="Requests in flight on the account">
            now
          </th>
          <th scope="col">last error</th>
        </tr>
      </thead>
      {groups.map(([provider, rows]) => {
        const counts = countBoard(rows.map((r) => r.standing));
        return (
          <tbody key={provider} class="da-board-group">
            <tr class="da-board-provider">
              <th scope="rowgroup" colSpan={5}>
                <span class="da-board-provider-name">{provider}</span>
                <span class="da-quiet da-num">
                  {counts.serving}/{counts.total} serving
                </span>
              </th>
            </tr>
            {rows.map((row) => {
              index++;
              const a = row.account;
              const s = row.standing;
              const ids = [a.label, a.id];
              const err = a.last_error;
              const why = s.state !== "serving" && s.state !== "off" ? s.why : undefined;
              return (
                <tr
                  key={a.id}
                  class="da-board-row"
                  data-tone={s.tone}
                  data-cursor={index === props.cursor ? "" : undefined}
                >
                  <td class="da-cell-state">
                    <StateTag standing={s} now={fleet.now} />
                  </td>
                  <th scope="row" class="da-cell-account">
                    <a href={`#/accounts/${encodeURIComponent(a.id)}`} class="da-rowlink" title={row.name}>
                      {row.name}
                    </a>
                    {s.coolingModels > 0 && s.state === "serving" && (
                      <span
                        class="da-note"
                        data-tone="warn"
                        title="Models held back while the account serves the others"
                      >
                        {s.coolingModels} model{s.coolingModels === 1 ? "" : "s"} cooling down
                      </span>
                    )}
                  </th>
                  <td class="da-cell-windows">
                    <WindowsCell row={row} fleet={fleet} />
                  </td>
                  <td class="da-cell-now da-num da-right" data-zero={a.active_requests ? undefined : ""}>
                    {a.active_requests ?? 0}
                    {a.max_concurrency ? <span class="da-quiet">/{a.max_concurrency}</span> : null}
                  </td>
                  <td class="da-cell-error">
                    {why ? (
                      <span class="da-clip" title={p.text(why, ids)}>
                        {p.text(why, ids)}
                      </span>
                    ) : err ? (
                      <span class="da-clip da-quiet" title={p.text(err.message, ids)}>
                        <span class="da-num">{err.status ?? err.code}</span> {p.text(err.message, ids)}
                        {a.last_error_at && (
                          <span class="da-num"> · {ago(fleet.now - Date.parse(a.last_error_at))} ago</span>
                        )}
                      </span>
                    ) : (
                      <span class="da-quiet">–</span>
                    )}
                    <FixButton row={row} fix={props.fix} small />
                  </td>
                </tr>
              );
            })}
          </tbody>
        );
      })}
    </table>
  );
}

/** One request in the tail: when, how it ended, what was asked, who served it, and its path when it fell back. */
function TailRow(props: { e: UsageEvent; fleet: Fleet; fresh: boolean }) {
  const { e, fleet } = props;
  const hops = pathOf(e);
  const back = fellBack(e);
  return (
    <li class="da-tail-row" data-failed={e.failed ? "" : undefined} data-fresh={props.fresh ? "" : undefined}>
      <a href={`#/usage/list/${e.seq}`} class="da-tail-main" title="Open this request">
        <time class="da-num da-quiet" dateTime={e.timestamp}>
          {clock(e.timestamp)}
        </time>
        <span class="da-tail-status da-num" data-tone={e.failed ? "danger" : undefined}>
          {e.failed && <Icon name="failed" size={12} />}
          {e.status ?? (e.failed ? "err" : "ok")}
        </span>
        <span class="da-clip da-tail-model">{e.model}</span>
        <span class="da-clip da-tail-account">
          {e.failed && hops.length === 0 ? "no account" : fleet.nameOf(e.account)}
        </span>
        <span class="da-num da-right da-quiet">{latency(e.latency_ms)}</span>
      </a>
      {(back || (e.failed && hops.length > 0)) && (
        <span class="da-tail-path" title="The accounts tried, in order">
          <Icon name="fallback" size={12} />
          {hops.map((h, i) => (
            <span key={i} class="da-hop" data-ok={h.ok ? "" : undefined}>
              {i > 0 && <span class="da-hop-then">→</span>}
              {fleet.nameOf(h.account)}
              <span class="da-hop-result">{h.ok ? "ok" : h.result.replace(/_/g, " ")}</span>
            </span>
          ))}
        </span>
      )}
    </li>
  );
}

function Tail(props: {
  tail: ReturnType<typeof usePoll<UsageEventsReply>>;
  fleet: Fleet;
  paused: boolean;
  onPause: () => void;
}) {
  const { tail } = props;
  const events = tail.data?.events ?? [];
  // Rows that arrived since the last reading come in marked, so the eye catches them once.
  const seen = useRef<number | undefined>(undefined);
  const newest = events[0]?.seq;
  const before = seen.current;
  useEffect(() => {
    seen.current = newest;
  }, [newest]);
  const back = events.filter(fellBack).length;
  const failed = events.filter((e) => e.failed).length;
  return (
    <section class="da-pane da-tail" aria-label="Recent requests">
      <header class="da-pane-head">
        <h2>tail</h2>
        <span class="da-live" data-paused={props.paused ? "" : undefined}>
          <span class="da-live-dot" />
          {props.paused ? "paused" : "live"}
        </span>
        {events.length > 0 && (
          <span class="da-quiet da-num da-clip">
            last {events.length}: {failed} failed, {back} fell back
          </span>
        )}
        <span class="da-grow" />
        <Btn small variant="ghost" keys="space" onClick={props.onPause}>
          {props.paused ? "Resume" : "Pause"}
        </Btn>
        <Btn small variant="ghost" href="#/usage/list" keys="g r">
          All
        </Btn>
      </header>
      {tail.error && (
        <Fault what="The tail stopped." error={tail.error} retry={tail.reload} stale={events.length > 0} />
      )}
      {tail.loading ? (
        <Skeleton rows={14} label="Reading recent requests" />
      ) : tail.data?.enabled === false ? (
        <Blank
          title="Usage recording is off"
          action={<Btn href="#/config/logging">Open logging settings</Btn>}
        >
          Core is not keeping a record of requests, so there is nothing to tail.
        </Blank>
      ) : events.length === 0 && !tail.error ? (
        <Blank title="No requests yet">
          Point a coding tool at this address with a client key and its requests show here as they arrive.
        </Blank>
      ) : (
        <ol class="da-tail-list">
          {events.map((e) => (
            <TailRow key={e.seq} e={e} fleet={props.fleet} fresh={before !== undefined && e.seq > before} />
          ))}
        </ol>
      )}
    </section>
  );
}

export function Overview() {
  const { api } = useKit();
  const [range, setRange, stepRange] = useRange();
  const fleet = useFleet();
  const usage = usePoll(() => api.usage(usageQuery(range)), 15_000, range.id);
  const [paused, setPaused] = useState(false);
  const tail = usePoll(() => api.usageEventsPage("before=0&limit=60"), paused ? 0 : 4000);
  const fix = useFix(fleet.reload);
  const [cursor, setCursor] = useState(-1);
  const boardRef = useRef<HTMLDivElement>(null);
  const counts = countBoard(fleet.rows.map((r) => r.standing));
  const needy = fleet.rows.filter((r) => NEEDS_YOU.includes(r.standing.state));
  const inFlight = fleet.rows.reduce((n, r) => n + (r.account.active_requests ?? 0), 0);
  const refresh = () => {
    fleet.reload();
    fleet.refreshWindows();
    usage.reload();
    tail.reload();
  };
  const move = (by: number) => {
    const links = boardRef.current?.querySelectorAll<HTMLAnchorElement>(".da-rowlink");
    if (!links?.length) return;
    const next = Math.max(0, Math.min(links.length - 1, cursor + by));
    setCursor(next);
    links[next]?.focus();
  };
  useKeys([
    { keys: "j", label: "Next account", run: () => move(1), keyOnly: true, quiet: true, group: "Overview" },
    {
      keys: "k",
      label: "Previous account",
      run: () => move(-1),
      keyOnly: true,
      quiet: true,
      group: "Overview",
    },
    { keys: "r", label: "Refresh", run: refresh, group: "Overview" },
    { keys: "[", label: "Shorter range", run: () => stepRange(-1), quiet: true, group: "Overview" },
    { keys: "]", label: "Longer range", run: () => stepRange(1), quiet: true, group: "Overview" },
    {
      keys: " ",
      label: paused ? "Resume the tail" : "Pause the tail",
      run: () => setPaused((v) => !v),
      group: "Overview",
    },
  ]);
  const u = usage.data;
  return (
    <div class="da-overview">
      <section class="da-strip" aria-label={`The last ${range.label}`}>
        <div class="da-strip-head">
          <RangePicker range={range} onPick={setRange} />
          <span class="da-quiet da-strip-keys">
            <Kbd keys="[" />
            <Kbd keys="]" />
          </span>
        </div>
        {usage.loading ? (
          <Skeleton rows={2} label="Reading usage" />
        ) : !u ? (
          <Fault what="Usage could not be read." error={usage.error ?? ""} retry={usage.reload} />
        ) : u.enabled === false ? (
          <p class="da-quiet da-strip-off">
            Usage recording is off, so there are no figures. <a href="#/config/logging">Turn it on</a>.
          </p>
        ) : (
          <>
            <Figures usage={u} range={range} inFlight={fleet.accounts.data ? inFlight : undefined} />
            <Bars
              series={u.series}
              bucketSeconds={u.bucket_seconds}
              now={fleet.now}
              label={`Requests over the last ${range.label}: ${u.total_requests} in all, ${u.failure_count} failed`}
              summary={
                u.total_requests ? `requests per ${stepLabel(u.bucket_seconds)}` : "no requests in this range"
              }
            />
          </>
        )}
        {usage.error && u && (
          <Fault what="Usage is not updating." error={usage.error} retry={usage.reload} stale />
        )}
      </section>

      <section class="da-pane da-accounts-pane" aria-label="Accounts" ref={boardRef}>
        <header class="da-pane-head">
          <h2>accounts</h2>
          {fleet.accounts.data && (
            <span class="da-counts da-num">
              <span>{counts.serving} serving</span>
              {counts.waiting > 0 && <span data-tone="warn">{counts.waiting} waiting</span>}
              {counts.needYou > 0 && <span data-tone="danger">{counts.needYou} need you</span>}
              {counts.off > 0 && <span class="da-quiet">{counts.off} off</span>}
            </span>
          )}
          <span class="da-grow" />
          <Btn small variant="ghost" icon="plus" href="#/add-account" keys="a">
            Add account
          </Btn>
        </header>
        {needy.length > 0 && (
          <p class="da-needy" role="status">
            <Icon name="alert" size={14} />
            <span>
              {needy.length === 1 ? "One account needs you" : `${needy.length} accounts need you`}:{" "}
              {needy.slice(0, 4).map((r, i) => (
                <span key={r.account.id}>
                  {i > 0 && ", "}
                  <a href={`#/accounts/${encodeURIComponent(r.account.id)}`}>{r.name}</a> ({r.standing.label})
                </span>
              ))}
              {needy.length > 4 && `, and ${needy.length - 4} more`}
            </span>
          </p>
        )}
        {fix.error && <Fault what="" error={fix.error} retry={fix.clearError} />}
        {fleet.accounts.error && (
          <Fault
            what="Accounts could not be read."
            error={fleet.accounts.error}
            retry={fleet.reload}
            stale={!!fleet.accounts.data}
          />
        )}
        {fleet.accounts.loading ? (
          <Skeleton rows={9} label="Reading accounts" />
        ) : fleet.accounts.data && fleet.rows.length === 0 ? (
          <Blank
            title="No accounts yet"
            action={
              <Btn variant="primary" icon="plus" href="#/add-account" keys="a">
                Add account
              </Btn>
            }
          >
            pluxy sends requests through the subscriptions and API keys you add. Add the first one and it
            shows here with its state and usage windows.
          </Blank>
        ) : (
          <Board fleet={fleet} fix={fix} cursor={cursor} />
        )}
      </section>

      <Tail tail={tail} fleet={fleet} paused={paused} onPause={() => setPaused((v) => !v)} />
    </div>
  );
}

/** A series step in words: 60 reads "minute", 7200 "2 h". */
export function stepLabel(seconds: number): string {
  if (seconds === 60) return "minute";
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  if (seconds === 3600) return "hour";
  if (seconds < 86_400) return `${Math.round(seconds / 3600)} h`;
  return seconds === 86_400 ? "day" : `${Math.round(seconds / 86_400)} d`;
}
