// The Failed requests tab: the full logs pluxy keeps of its latest failed requests, and a field to
// open the log of a trace ID copied from a tool's error.

import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { type ErrorLogFile, type LogContent, problem } from "../api";
import { Phases } from "../parts";
import { useAccountLabels, usePrivacy } from "../privacy";
import { Button, Detail, Empty, List, MasterDetail, Notice, Time, useKit, useLoad } from "../ui";
import { CopyButton } from "./copyButton";
import { failedRequest, fileSize, TRACE_EXAMPLE, TRACE_SHAPE } from "./logs.helpers";
import { t } from "./logsStrings";

/** A trace ID the address or the field asks to open. n tells the same ID asked again from the first. */
export interface OpenTrace {
  trace: string;
  n: number;
}

/** What the detail shows of the log it was asked for. */
interface View {
  /** The trace ID, or the file's name when it names none. */
  title: string;
  trace?: string;
  state: "loading" | "ready" | "missing" | "tooBig" | "failed";
  content?: LogContent;
  error?: string;
}

/** A log's text. It scrolls, so the keyboard reaches it and it has a name. */
function LogText(props: { content: string; label: string }) {
  return (
    <section
      class="logs-text"
      data-part="log"
      data-wrap=""
      aria-label={props.label}
      // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable region must take focus for the keyboard to scroll it
      tabIndex={0}
    >
      {props.content}
    </section>
  );
}

export function FailedRequests(props: { save: (name: string, content: string) => void; open?: OpenTrace }) {
  const { api, unauthorized } = useKit();
  const privacy = usePrivacy();
  const files = useLoad(api.errorLogs);
  // With every request logged, pluxy names no log as a failed one, so the list stays empty.
  const settings = useLoad(api.coreSettings);
  const everyRequest = settings.data?.observability.logs["request-log"] === true;
  const [field, setField] = useState(props.open?.trace ?? "");
  const [refused, setRefused] = useState(false);
  const [view, setView] = useState<View>();
  const labels = useAccountLabels(view?.content);
  const mask = useMemo(() => privacy.masker(labels), [privacy, labels]);
  const mounted = useRef(true);
  const asked = useRef(0);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  /** Shows what a read of one log said. A newer ask drops the answer of an older one. */
  const show = async (start: View, read: () => ReturnType<typeof api.requestLog>) => {
    const ask = ++asked.current;
    setView(start);
    const r = await read();
    if (!mounted.current || ask !== asked.current) return;
    if (r.status === 401) return unauthorized();
    const failure = problem(r);
    if (failure === undefined && r.body) return setView({ ...start, state: "ready", content: r.body });
    setView({
      ...start,
      state: r.status === 404 ? "missing" : r.status === 413 ? "tooBig" : "failed",
      error: failure,
    });
  };
  const openTrace = (raw: string) => {
    const trace = raw.trim();
    // Checked before any call, as core checks it: a typo never reaches core.
    if (!TRACE_SHAPE.test(trace)) {
      asked.current++;
      setView(undefined);
      return setRefused(true);
    }
    setRefused(false);
    return show({ title: trace, trace, state: "loading" }, () => api.requestLog(trace));
  };
  const openFile = (file: ErrorLogFile) => {
    const { trace } = failedRequest(file.name);
    setRefused(false);
    return show({ title: trace ?? file.name, trace, state: "loading" }, () => api.errorLog(file.name));
  };
  // The address names a trace: the field shows it and its log opens.
  useEffect(() => {
    if (!props.open) return;
    setField(props.open.trace);
    void openTrace(props.open.trace);
  }, [props.open?.n]);

  const selected = view?.trace
    ? files.data?.files.find((f) => failedRequest(f.name).trace === view.trace)?.name
    : undefined;
  const text = view?.content ? mask(view.content.content) : "";
  return (
    <>
      <div class="logs-failed-head">
        <p class="muted">{t("intro")}</p>
        {everyRequest && <p class="muted">{t("everyRequest")}</p>}
        <form
          class="logs-trace-form"
          onSubmit={(e) => {
            e.preventDefault();
            void openTrace(field);
          }}
        >
          <label class="field" data-part="field">
            <span class="label">{t("traceId")}</span>
            <input
              data-slot="input"
              type="text"
              class="mono"
              value={field}
              aria-invalid={refused ? "true" : undefined}
              onInput={(e) => {
                setField(e.currentTarget.value);
                setRefused(false);
              }}
            />
          </label>
          <Button type="submit" disabled={field.trim() === ""}>
            {t("open")}
          </Button>
        </form>
      </div>
      {refused && <Notice tone="danger">{t("badTrace", { example: TRACE_EXAMPLE })}</Notice>}
      <MasterDetail
        list={
          <Phases
            load={files}
            loading={t("loadingFailed")}
            failed={{ title: t("listFailed") }}
            isEmpty={(d) => d.files.length === 0}
            empty={<Empty>{t("none")}</Empty>}
          >
            {(d) => (
              <List<ErrorLogFile>
                label={t("tabFailed")}
                items={d.files}
                id={(f) => f.name}
                selected={selected}
                onSelect={(name) => {
                  const file = d.files.find((f) => f.name === name);
                  if (file) void openFile(file);
                }}
                columns={[
                  { label: t("colRequest"), value: (f) => failedRequest(f.name).request, mono: true },
                  {
                    label: t("colTime"),
                    value: (f) => (
                      <span class="logs-time">
                        <Time value={f.modified} mode="exact" />
                      </span>
                    ),
                  },
                  {
                    label: t("colTrace"),
                    value: (f) => {
                      const { trace } = failedRequest(f.name);
                      return trace ? (
                        <span class="logs-trace" title={trace}>
                          {trace}
                        </span>
                      ) : null;
                    },
                    mono: true,
                  },
                  { label: t("colSize"), value: (f) => fileSize(f.size), numeric: true },
                ]}
              />
            )}
          </Phases>
        }
        detail={
          view && (
            <Detail title={view.title} onClose={() => setView(undefined)}>
              {view.state === "loading" && <Notice tone="muted">{t("opening")}</Notice>}
              {view.state === "failed" && (
                <Notice tone="danger">{t("openFailed", { error: mask(view.error ?? "") })}</Notice>
              )}
              {view.state === "missing" && (
                <Notice tone="warn">
                  {t("missing")} <a href="#/config/logging">{t("settings")}</a>
                </Notice>
              )}
              {view.state === "tooBig" && <Notice tone="warn">{t("tooBig")}</Notice>}
              {view.content && (
                <>
                  <div class="actions">
                    <Button
                      onClick={() => view.content && props.save(view.content.name, view.content.content)}
                    >
                      {t("download")}
                    </Button>
                    {view.trace && <CopyButton value={view.trace} label={t("copyTrace")} />}
                  </div>
                  <LogText content={text} label={t("logOf", { trace: view.title })} />
                </>
              )}
            </Detail>
          )
        }
      />
    </>
  );
}
