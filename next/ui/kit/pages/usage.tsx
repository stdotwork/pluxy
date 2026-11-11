import { useEffect, useRef, useState } from "preact/hooks";
import { problem, type UsageEvent, type UsageFilters } from "../api";
import { csvField } from "../csv";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { Button, Empty, List, Notice, PageHead, useKit, when } from "../ui";

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
  empty: "No requests",
  loading: "Loading requests...",
  previous: "Previous",
  next: "Next",
  page: "Page {page}",
  capped: "Export limited to {count} requests.",
  lost: "Some older requests are no longer available.",
  milliseconds: "{value} ms",
  missing: "—",
  exportError: "Could not export requests.",
});

const initial: UsageFilters = { since: "", model: "", provider: "", account: "", key_id: "", failed: "" };
const PAGE_SIZE = 50;
const EXPORT_LIMIT = 10000;

export function UsagePage() {
  const { api, unauthorized } = useKit();
  const [filters, setFilters] = useState<UsageFilters>(initial);
  const [events, setEvents] = useState<UsageEvent[]>([]);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
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

  const exportCSV = () => {
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
    const url = URL.createObjectURL(
      new Blob([rows.map((row) => row.map(csvField).join(",")).join("\r\n") + "\r\n"], {
        type: "text/csv;charset=utf-8",
      }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = "usage.csv";
    link.click();
    URL.revokeObjectURL(url);
    setExporting(false);
  };
  const field = (name: keyof UsageFilters, label: string) => (
    <label class="field" data-part="field">
      <span class="label">{label}</span>
      <input
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
      <div class="usage-controls">
        {field("since", t("since"))}
        {field("model", t("model"))}
        {field("provider", t("provider"))}
        {field("account", t("account"))}
        {field("key_id", t("key"))}
        <label class="field" data-part="field">
          <span class="label">{t("failed")}</span>
          <select
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
      {error && <Notice tone="danger">{error}</Notice>}
      {lost && <Notice tone="muted">{t("lost")}</Notice>}
      {capped && <Notice tone="muted">{t("capped", { count: EXPORT_LIMIT })}</Notice>}
      {loading ? (
        <Notice tone="muted">{t("loading")}</Notice>
      ) : events.length === 0 ? (
        <Empty>{t("empty")}</Empty>
      ) : (
        <>
          <List<UsageEvent>
            label={t("title")}
            items={events.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)}
            id={(e) => String(e.seq)}
            columns={[
              { label: t("time"), value: (e) => <time dateTime={e.timestamp}>{when(e.timestamp)}</time> },
              { label: t("model"), value: (e) => e.model },
              { label: t("account"), value: (e) => e.account || t("missing") },
              {
                label: t("status"),
                value: (e) =>
                  (e.status ? String(e.status) : t("missing")) + (e.failed ? " · " + t("yes") : ""),
              },
              { label: t("latency"), value: (e) => t("milliseconds", { value: e.latency_ms }) },
              { label: t("tokens"), value: (e) => e.tokens.total_tokens?.toLocaleString() ?? t("missing") },
              {
                label: t("trace"),
                value: (e) =>
                  e.id ? <a href={"#/logs/" + encodeURIComponent(e.id)}>{e.id}</a> : t("missing"),
              },
            ]}
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
