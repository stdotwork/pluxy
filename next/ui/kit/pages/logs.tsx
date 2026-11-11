import { useEffect, useRef, useState } from "preact/hooks";
import { type ErrorLogFile, type LogContent, problem } from "../api";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import {
  Button,
  Card,
  Detail,
  Empty,
  List,
  MasterDetail,
  Notice,
  PageHead,
  Switch,
  useKit,
  useLoad,
  when,
} from "../ui";

const t = strings("logs", {
  title: "Logs Viewer",
  short: "Logs",
  tip: "Request tracing",
  core: "Core logs",
  content: "Log Content",
  continuous: "Continuous reading",
  refresh: "Refresh Logs",
  search: "Search logs by content or keyword",
  level: "Log level",
  allLevels: "All levels",
  info: "info",
  warn: "warn",
  wrap: "Wrap long lines",
  downloadLogs: "Download Logs",
  clear: "Clear Logs",
  confirmClear: "Yes, clear logs",
  cancel: "Cancel",
  clearConfirm: "Are you sure you want to clear all logs? This action cannot be undone!",
  errors: "Error Request Logs",
  noErrors: "No error request log files found",
  size: "Size",
  bytes: "{size} bytes",
  modified: "Last modified",
  name: "Name",
  actions: "Actions",
  open: "Open",
  download: "Download",
  errorDetail: "Error Request Log Details",
  requestId: "Request ID",
  request: "Request log",
  view: "View log",
  viewRequest: "View log for request {id}",
  loading: "Loading logs...",
  empty: "No Logs Available",
  noMatch: "No matching logs found",
  off: "Logging to file is off. Enable logging to file in the Config Panel's Logging tab, then refresh.",
  config: "Config Panel — Logging",
  retained: "Showing the latest 10,000 loaded lines. Older lines are no longer searched or downloaded here.",
});

function saveLog(name: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  // Allow the browser to start consuming the Blob before releasing it.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function logLevel(line: string) {
  return line.match(/^\[[^\]]+\] \[(info|warn)\s*\]/)?.[1] ?? "";
}

function LogText({ content, wrap }: { content: string; wrap: boolean }) {
  return (
    <pre class="logs-text" data-part="log" data-wrap={wrap ? "" : undefined}>
      {content}
    </pre>
  );
}

export function LogsPage() {
  const { api, unauthorized } = useKit();
  const files = useLoad(api.errorLogs);
  const [lines, setLines] = useState<string[]>([]);
  const [error, setError] = useState<string>();
  const [off, setOff] = useState(false);
  const [busy, setBusy] = useState(false);
  const [continuous, setContinuous] = useState(false);
  const [wrap, setWrap] = useState(false);
  const [search, setSearch] = useState("");
  const [level, setLevel] = useState("");
  const [asking, setAsking] = useState(false);
  const [trimmed, setTrimmed] = useState(false);
  const [selected, setSelected] = useState<string>();
  const [detail, setDetail] = useState<LogContent>();
  const [detailError, setDetailError] = useState<string>();
  const [detailBusy, setDetailBusy] = useState(false);
  const [id, setId] = useState("");
  const [requested, setRequested] = useState("");
  const [request, setRequest] = useState<LogContent>();
  const [requestError, setRequestError] = useState<string>();
  const [requestBusy, setRequestBusy] = useState(false);
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const latest = useRef<number | undefined>(undefined);
  const detailVersion = useRef(0);
  const requestVersion = useRef(0);

  const read = async (append = false) => {
    if (inFlight.current || !mounted.current) return;
    inFlight.current = true;
    setBusy(true);
    const r = await api.logs(append ? latest.current : undefined);
    inFlight.current = false;
    if (!mounted.current) return;
    setBusy(false);
    if (r.status === 401) return unauthorized();
    const isOff = r.status === 400 && r.error?.message === "logging to file is off";
    setOff(isOff);
    setError(isOff ? undefined : problem(r));
    if (isOff) {
      setLines([]);
      latest.current = undefined;
    }
    if (!problem(r) && r.body) {
      latest.current = r.body["latest-timestamp"];
      const incoming = r.body.lines;
      setLines((old) => {
        const next = append ? [...old, ...incoming] : incoming;
        if (next.length > 10000) setTrimmed(true);
        else if (!append) setTrimmed(false);
        return next.slice(-10000);
      });
    }
  };
  const viewRequest = async (trace: string) => {
    const version = ++requestVersion.current;
    setRequested(trace);
    setRequest(undefined);
    setRequestError(undefined);
    setRequestBusy(!!trace);
    if (!trace) return;
    const r = await api.requestLog(trace);
    if (!mounted.current || requestVersion.current !== version) return;
    setRequestBusy(false);
    if (r.status === 401) return unauthorized();
    setRequestError(problem(r));
    if (!problem(r)) setRequest(r.body);
  };
  useEffect(() => {
    mounted.current = true;
    void read();
    const fromAddress = () => {
      const raw = location.hash.startsWith("#/logs/") ? location.hash.slice(7) : undefined;
      if (!raw) return;
      let trace = raw;
      try {
        trace = decodeURIComponent(raw);
      } catch {
        /* Keep malformed input as text. */
      }
      setId(trace);
      void viewRequest(trace);
    };
    fromAddress();
    window.addEventListener("hashchange", fromAddress);
    return () => {
      mounted.current = false;
      window.removeEventListener("hashchange", fromAddress);
    };
  }, []);
  useEffect(() => {
    if (!continuous || off) return;
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") void read(true);
    }, 2000);
    return () => clearInterval(timer);
  }, [continuous, off]);
  const clear = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    const r = await api.clearLogs();
    inFlight.current = false;
    if (!mounted.current) return;
    setBusy(false);
    if (r.status === 401) return unauthorized();
    setError(problem(r));
    if (!problem(r)) {
      setLines([]);
      setTrimmed(false);
      latest.current = undefined;
      setAsking(false);
    }
  };
  const open = async (name: string, download = false) => {
    const version = ++detailVersion.current;
    setSelected(name);
    setDetail(undefined);
    setDetailError(undefined);
    setDetailBusy(true);
    const r = await api.errorLog(name);
    if (!mounted.current || version !== detailVersion.current) return;
    setDetailBusy(false);
    if (r.status === 401) return unauthorized();
    setDetailError(problem(r));
    if (!problem(r) && r.body) {
      setDetail(r.body);
      if (download) saveLog(r.body.name, r.body.content);
    }
  };
  const query = search.toLocaleLowerCase();
  const shown = lines.filter(
    (line) => (!level || logLevel(line) === level) && line.toLocaleLowerCase().includes(query),
  );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button
            onClick={() => {
              void read();
              files.reload();
            }}
            disabled={busy}
          >
            {t("refresh")}
          </Button>
        }
      />
      <Card title={t("core")}>
        {error && <Notice tone="danger">{error}</Notice>}
        {off && (
          <Notice tone="warn">
            {t("off")}{" "}
            <a href="#/config" data-part="button">
              {t("config")}
            </a>
          </Notice>
        )}
        <div class="logs-controls">
          <Switch label={t("continuous")} checked={continuous} onChange={setContinuous} />
          <Switch label={t("wrap")} checked={wrap} onChange={setWrap} />
          <label class="field logs-search" data-part="field">
            <span class="label">{t("search")}</span>
            <input type="search" value={search} onInput={(e) => setSearch(e.currentTarget.value)} />
          </label>
          <label class="field" data-part="field">
            <span class="label">{t("level")}</span>
            <select value={level} onChange={(e) => setLevel(e.currentTarget.value)}>
              <option value="">{t("allLevels")}</option>
              <option value="info">{t("info")}</option>
              <option value="warn">{t("warn")}</option>
            </select>
          </label>
        </div>
        {trimmed && <Notice tone="muted">{t("retained")}</Notice>}
        <section
          aria-label={t("core")}
          data-part="log"
          class="logs-text logs-core"
          data-wrap={wrap ? "" : undefined}
        >
          {shown.length ? (
            shown.map((line, i) => (
              <div key={i} data-part="log-line" data-level={logLevel(line)}>
                {line}
              </div>
            ))
          ) : (
            <Empty>{t(search || level ? "noMatch" : "empty")}</Empty>
          )}
        </section>
        <div class="actions">
          <Button disabled={off || shown.length === 0} onClick={() => saveLog("logs.txt", shown.join("\n"))}>
            {t("downloadLogs")}
          </Button>
          <Button variant="danger" disabled={busy || off} onClick={() => setAsking(true)}>
            {t("clear")}
          </Button>
        </div>
        {asking && (
          <section data-part="card" class="logs-confirm">
            <p>{t("clearConfirm")}</p>
            <div class="actions">
              <Button variant="danger" disabled={busy} onClick={() => void clear()}>
                {t("confirmClear")}
              </Button>
              <Button onClick={() => setAsking(false)}>{t("cancel")}</Button>
            </div>
          </section>
        )}
      </Card>
      <Card title={t("errors")}>
        {files.error && <Notice tone="danger">{files.error}</Notice>}
        <MasterDetail
          list={
            files.loading ? (
              <Notice tone="muted">{t("loading")}</Notice>
            ) : !files.data?.files.length ? (
              <Empty>{t("noErrors")}</Empty>
            ) : (
              <List<ErrorLogFile>
                label={t("errors")}
                items={files.data.files}
                id={(file) => file.name}
                selected={selected}
                columns={[
                  { label: t("name"), value: (file) => file.name, mono: true },
                  { label: t("size"), value: (file) => t("bytes", { size: file.size.toLocaleString() }) },
                  { label: t("modified"), value: (file) => when(file.modified) },
                  {
                    label: t("actions"),
                    value: (file) => (
                      <div class="actions">
                        <Button onClick={() => void open(file.name)}>{t("open")}</Button>
                        <Button
                          onClick={() => {
                            if (detail?.name === file.name) saveLog(detail.name, detail.content);
                            else void open(file.name, true);
                          }}
                        >
                          {t("download")}
                        </Button>
                      </div>
                    ),
                  },
                ]}
              />
            )
          }
          detail={
            selected && (
              <Detail
                title={t("errorDetail")}
                onClose={() => {
                  ++detailVersion.current;
                  setSelected(undefined);
                }}
              >
                <p class="mono">{selected}</p>
                {detailBusy && <Notice tone="muted">{t("loading")}</Notice>}
                {detailError && <Notice tone="danger">{detailError}</Notice>}
                {detail && (
                  <>
                    <Button onClick={() => saveLog(detail.name, detail.content)}>{t("download")}</Button>
                    <LogText content={detail.content} wrap={wrap} />
                  </>
                )}
              </Detail>
            )
          }
        />
      </Card>
      <Card title={t("request")}>
        <form
          class="logs-controls"
          onSubmit={(e) => {
            e.preventDefault();
            void viewRequest(id.trim());
          }}
        >
          <label class="field logs-search" data-part="field">
            <span class="label">{t("requestId")}</span>
            <input value={id} onInput={(e) => setId(e.currentTarget.value)} />
          </label>
          <Button type="submit" disabled={!id.trim() || requestBusy}>
            {t("view")}
          </Button>
        </form>
        {requested && (
          <section data-part="card" aria-label={t("viewRequest", { id: requested })}>
            <h3>{t("viewRequest", { id: requested })}</h3>
            {requestBusy && <Notice tone="muted">{t("loading")}</Notice>}
            {requestError && <Notice tone="danger">{requestError}</Notice>}
            {request && (
              <>
                <Button onClick={() => saveLog(request.name, request.content)}>{t("download")}</Button>
                <LogText content={request.content} wrap={wrap} />
              </>
            )}
          </section>
        )}
      </Card>
    </>
  );
}

export const logsPage = (): PageDef => ({
  id: "logs",
  group: "observe",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <LogsPage />,
});
