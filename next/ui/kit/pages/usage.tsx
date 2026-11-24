import { useEffect, useRef, useState } from "preact/hooks";
import { type Account, problem, type UsageEvent, type UsageFilters } from "../api";
import { csvField } from "../csv";
import { type Masker, useAccountLabels, usePrivacy } from "../privacy";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import {
  Badge,
  Button,
  Detail,
  Empty,
  Facts,
  List,
  MasterDetail,
  Notice,
  PageHead,
  useKit,
  useLoad,
  when,
} from "../ui";

const t = strings("usage", {
  title: "Usage",
  short: "Usage",
  tip: "Recorded requests",
  since: "Since",
  sinceHint: "e.g. 24h or 2026-01-01T00:00:00Z",
  model: "Model",
  provider: "Provider",
  account: "Account",
  key: "Client key",
  failed: "Failed",
  all: "All",
  yes: "Failed",
  no: "Succeeded",
  refresh: "Refresh",
  export: "Export",
  time: "Time",
  status: "Status",
  latency: "Latency",
  tokens: "Tokens",
  trace: "Trace ID",
  empty: "No requests yet. Send traffic through pluxy, or clear your filters.",
  loading: "Loading requests…",
  previous: "Previous",
  next: "Next",
  page: "Page {page}",
  capped: "Export limited to {count} requests.",
  lost: "Core dropped older requests past its retention limit. Export sooner to keep them.",
  milliseconds: "{value} ms",
  missing: "—",
  exportError: "Could not export requests. Refresh the list and try again.",
  exportReal:
    "Privacy mode hides names on this page only. An exported file holds them as core recorded them.",
  fallbackStep: "Fallback step {step}",
  fallbackStepTitle: "Ended on step {step} of this model's fallback chain",
  tries: "{count} tries",
  triesTitle: "pluxy sent this request {count} times. Open the row to see what each try came to.",
  request: "Request {time}",
  triesFact: "Tries",
  tried: "What pluxy tried",
  triedHint: "In order. The last line is the try that ended the request.",
  answered: "answered",
  failedWith: "failed: {result}",
});

const initial: UsageFilters = { since: "", model: "", provider: "", account: "", key_id: "", failed: "" };
const PAGE_SIZE = 50;
const EXPORT_LIMIT = 10000;

/** The step of the model's fallback chain the request ended on, when it was not the first. */
export function fallbackStep(e: UsageEvent) {
  const step = e.chain_step ?? 0;
  if (step < 2) return null;
  return (
    <span class="usage-mark" title={t("fallbackStepTitle", { step })}>
      <Badge tone="muted">{t("fallbackStep", { step })}</Badge>
    </span>
  );
}

/** How many times the request was sent upstream, when it was more than once. */
export function tries(e: UsageEvent) {
  const count = e.attempts ?? 0;
  if (count < 2) return null;
  return (
    <span class="usage-mark" title={t("triesTitle", { count })}>
      <Badge tone="muted">{t("tries", { count })}</Badge>
    </span>
  );
}

/**
 * One recorded request: what the row shows, and each try in order with its account, provider,
 * model and what it came to. name turns an account's public ID into what the page calls it.
 */
export function UsageDetail(props: {
  event: UsageEvent;
  name: (account: string) => string;
  mask: Masker;
  onClose: () => void;
}) {
  const { event: e, mask } = props;
  return (
    <Detail title={t("request", { time: when(e.timestamp) ?? e.timestamp })} onClose={props.onClose}>
      <Facts
        rows={[
          [t("model"), e.model],
          [t("account"), mask(e.account)],
          [
            t("status"),
            <>
              {(e.status ? String(e.status) : t("missing")) + (e.failed ? ` · ${t("yes")}` : "")}{" "}
              {fallbackStep(e)}
            </>,
          ],
          [t("triesFact"), e.attempts ? String(e.attempts) : undefined],
          [t("latency"), t("milliseconds", { value: e.latency_ms })],
          [t("tokens"), e.tokens.total_tokens?.toLocaleString()],
          [t("trace"), e.id && <a href={`#/logs/${encodeURIComponent(e.id)}`}>{e.id}</a>],
        ]}
      />
      {e.tried && e.tried.length > 0 && (
        <section aria-label={t("tried")}>
          <h3>{t("tried")}</h3>
          <small class="hint">{t("triedHint")}</small>
          <ol class="usage-tried">
            {e.tried.map((a, i) => (
              <li key={i}>
                <span class="mono">{mask(props.name(a.account))}</span>
                <small class="muted">
                  {" "}
                  {a.provider}
                  {a.model ? ` · ${a.model}` : ""}
                </small>
                <br />
                {a.result === "ok" ? t("answered") : t("failedWith", { result: a.result })}
              </li>
            ))}
          </ol>
        </section>
      )}
    </Detail>
  );
}

/** Turns an account's public ID into its ID, when core lists the account; else the public ID stays. */
export function accountNames(accounts: Account[] | undefined): (account: string) => string {
  const byPublic = new Map((accounts ?? []).flatMap((a) => (a.public_id ? [[a.public_id, a.id]] : [])));
  return (account) => byPublic.get(account) ?? account;
}

export function UsagePage() {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const [filters, setFilters] = useState<UsageFilters>(initial);
  const [events, setEvents] = useState<UsageEvent[]>([]);
  const mask = p.masker(useAccountLabels(events));
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<string>();
  // The tries of a request name accounts by public ID: the accounts say which is which.
  const accounts = useLoad(api.accounts);
  const chosen = events.find((e) => String(e.seq) === selected);
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
  const saved = useSave();
  const [error, setError] = useState<string>();
  const [lost, setLost] = useState(false);
  const [capped, setCapped] = useState(false);
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const changeFilters = (next: UsageFilters) => {
    generation.current++;
    setLoading(true);
    setLost(false);
    setFilters(next);
  };

  useEffect(() => {
    const current = ++generation.current;
    setLoading(true);
    setError(undefined);
    setCapped(false);
    setPage(0);
    const load = async () => {
      let after = 0;
      let all: UsageEvent[] = [];
      let missing = false;
      for (;;) {
        const r = await api.usageEvents(filters, after);
        if (current !== generation.current) return;
        if (r.status === 401) {
          unauthorized();
          return;
        }
        const issue = problem(r);
        if (issue || !r.body) {
          setError(issue ?? t("exportError"));
          setEvents([]);
          setLoading(false);
          return;
        }
        missing ||= r.body.lost;
        all = all.concat(r.body.events);
        if (!r.body.more) break;
        after = r.body.next;
      }
      setEvents(all.reverse());
      setLost(missing);
      setLoading(false);
    };
    const timer = setTimeout(() => void load(), 300);
    return () => {
      generation.current++;
      clearTimeout(timer);
    };
  }, [filters, revision]);

  const exportCSV = async () => {
    setExporting(true);
    const selected = events.slice(0, EXPORT_LIMIT);
    setCapped(events.length > EXPORT_LIMIT);
    const rows: (string | number)[][] = [
      [
        t("time"),
        t("model"),
        t("provider"),
        t("account"),
        t("key"),
        t("failed"),
        t("status"),
        t("latency"),
        t("tokens"),
        t("trace"),
      ],
      ...selected.map((e) => [
        e.timestamp,
        e.model,
        e.provider ?? "",
        e.account ?? "",
        e.key_id ?? "",
        String(e.failed),
        e.status ?? "",
        e.latency_ms,
        e.tokens.total_tokens ?? "",
        e.id ?? "",
      ]),
    ];
    await saved.save(
      "usage.csv",
      `${rows.map((row) => row.map(csvField).join(",")).join("\r\n")}\r\n`,
      "text/csv;charset=utf-8",
    );
    setExporting(false);
  };
  const field = (name: keyof UsageFilters, label: string) => (
    <label class="field" data-part="field">
      <span class="label">{label}</span>
      <input
        data-slot="input"
        type="text"
        placeholder={name === "since" ? t("sinceHint") : undefined}
        value={filters[name]}
        onInput={(e) => changeFilters({ ...filters, [name]: e.currentTarget.value })}
      />
    </label>
  );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <>
            <Button onClick={() => setRevision(revision + 1)} disabled={loading}>
              {t("refresh")}
            </Button>
            <Button onClick={exportCSV} disabled={loading || exporting || !!error}>
              {t("export")}
            </Button>
          </>
        }
      />
      {p.on && <p class="hint">{t("exportReal")}</p>}
      <div class="usage-controls">
        {field("since", t("since"))}
        {field("model", t("model"))}
        {field("provider", t("provider"))}
        {field("account", t("account"))}
        {field("key_id", t("key"))}
        <label class="field" data-part="field">
          <span class="label">{t("failed")}</span>
          <select
            data-slot="select-trigger"
            aria-label={t("failed")}
            value={filters.failed}
            onChange={(e) => changeFilters({ ...filters, failed: e.currentTarget.value })}
          >
            <option value="">{t("all")}</option>
            <option value="true">{t("yes")}</option>
            <option value="false">{t("no")}</option>
          </select>
        </label>
      </div>
      {saved.notice}
      {error && <Notice tone="danger">{mask(error)}</Notice>}
      {lost && <Notice tone="muted">{t("lost")}</Notice>}
      {capped && <Notice tone="muted">{t("capped", { count: EXPORT_LIMIT })}</Notice>}
      {loading ? (
        <Notice tone="muted">{t("loading")}</Notice>
      ) : events.length === 0 ? (
        <Empty>{t("empty")}</Empty>
      ) : (
        <>
          <MasterDetail
            detail={
              chosen && (
                <UsageDetail
                  event={chosen}
                  name={accountNames(accounts.data?.accounts)}
                  mask={mask}
                  onClose={() => setSelected(undefined)}
                />
              )
            }
            list={
              <List<UsageEvent>
                label={t("title")}
                items={events.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)}
                id={(e) => String(e.seq)}
                selected={selected}
                onSelect={setSelected}
                columns={[
                  { label: t("time"), value: (e) => <time dateTime={e.timestamp}>{when(e.timestamp)}</time> },
                  { label: t("model"), value: (e) => e.model },
                  { label: t("account"), value: (e) => mask(e.account) || t("missing") },
                  {
                    label: t("status"),
                    value: (e) => (
                      <>
                        {(e.status ? String(e.status) : t("missing")) + (e.failed ? ` · ${t("yes")}` : "")}{" "}
                        {fallbackStep(e)} {tries(e)}
                      </>
                    ),
                  },
                  { label: t("latency"), value: (e) => t("milliseconds", { value: e.latency_ms }) },
                  {
                    label: t("tokens"),
                    value: (e) => e.tokens.total_tokens?.toLocaleString() ?? t("missing"),
                  },
                  {
                    label: t("trace"),
                    value: (e) =>
                      e.id ? <a href={`#/logs/${encodeURIComponent(e.id)}`}>{e.id}</a> : t("missing"),
                  },
                ]}
              />
            }
          />
          <div class="usage-paging">
            <Button onClick={() => setPage(page - 1)} disabled={page === 0}>
              {t("previous")}
            </Button>
            <span>{t("page", { page: page + 1 })}</span>
            <Button onClick={() => setPage(page + 1)} disabled={(page + 1) * PAGE_SIZE >= events.length}>
              {t("next")}
            </Button>
          </div>
        </>
      )}
    </>
  );
}

export const usagePage = (): PageDef => ({
  id: "usage",
  group: "observe",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <UsagePage />,
});
