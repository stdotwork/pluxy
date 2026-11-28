// Logs: pluxy's own log, read as it is written, and the full logs of its latest failed requests.
// Both are topics under Settings; #/logs/<trace ID> opens one request's log on the second tab.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { problem } from "../api";
import { StatePane, tryAgainWord } from "../parts";
import { useAccountLabels, usePrivacy } from "../privacy";
import { useSave } from "../save";
import type { PageDef } from "../shell";
import { Tabs } from "../tabs";
import { Button, ConfirmButton, Empty, Notice, PageHead, Switch, useKit, usePoll } from "../ui";
import { FailedRequests, type OpenTrace } from "./failedRequests";
import { LOG_CAP, logDay, logLevel } from "./logs.helpers";
import { t } from "./logsStrings";

/**
 * A line of pluxy's log. Each starts with its full date and time; a line of today shows the time
 * alone, so the times line up and read at a glance, and keeps the full one as its title.
 */
export function LogLine({ text, today }: { text: string; today: string }) {
  const stamp = text.match(/^\[(\d{4}-\d\d-\d\d) (\d\d:\d\d:\d\d(?:\.\d+)?)\]/);
  if (!stamp || stamp[1] !== today) return <>{text}</>;
  return (
    <>
      [
      <time dateTime={`${stamp[1]}T${stamp[2]}`} title={`${stamp[1]} ${stamp[2]}`}>
        {stamp[2]}
      </time>
      {text.slice(stamp[0].length - 1)}
    </>
  );
}

/** Core's answer to a read of the log while it is not written to a file. */
const isOff = (r: { status: number; error?: { message: string } }) =>
  r.status === 400 && r.error?.message === "logging to file is off";

/** How close to the end the box must be scrolled for new lines to keep it there. */
const AT_END_PX = 32;

function PluxyLog(props: { save: (name: string, content: string) => void }) {
  const { api, unauthorized } = useKit();
  const privacy = usePrivacy();
  const [lines, setLines] = useState<string[]>([]);
  const labels = useAccountLabels(lines);
  const mask = useMemo(() => privacy.masker(labels), [privacy, labels]);
  // loading: no read has worked yet. off: core says it writes no log file.
  const [phase, setPhase] = useState<"loading" | "ready" | "off">("loading");
  const [readError, setReadError] = useState<string>();
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(true);
  const [find, setFind] = useState("");
  const [level, setLevel] = useState("");
  const [capped, setCapped] = useState(false);
  const [clearError, setClearError] = useState<string>();
  const [turnOnError, setTurnOnError] = useState<string>();
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const latest = useRef<number | undefined>(undefined);
  // Counts clears: a read that began before one must not put the lines it read back.
  const epoch = useRef(0);
  const box = useRef<HTMLElement>(null);
  const atEnd = useRef(true);

  const read = async (append: boolean) => {
    if (inFlight.current || !mounted.current) return;
    inFlight.current = true;
    const before = epoch.current;
    const r = await api.logs(append ? latest.current : undefined);
    inFlight.current = false;
    if (!mounted.current || before !== epoch.current) return;
    if (r.status === 401) return unauthorized();
    if (isOff(r)) {
      setPhase("off");
      setReadError(undefined);
      setLines([]);
      latest.current = undefined;
      return;
    }
    const failure = problem(r);
    // One notice for any run of failed reads: each failure replaces it, a read that works removes it.
    if (failure !== undefined) return setReadError(failure);
    if (!r.body) return;
    setReadError(undefined);
    setPhase("ready");
    latest.current = r.body["latest-timestamp"];
    const incoming = r.body.lines;
    setLines((old) => {
      // Nothing new: the same lines, so nothing is masked again.
      if (append && incoming.length === 0) return old;
      const next = append ? [...old, ...incoming] : incoming;
      if (next.length > LOG_CAP) setCapped(true);
      else if (!append) setCapped(false);
      return next.slice(-LOG_CAP);
    });
  };
  useEffect(() => {
    mounted.current = true;
    void read(false);
    return () => {
      mounted.current = false;
    };
  }, []);
  usePoll(() => read(true), 2000, follow && phase !== "off");

  const turnOn = async () => {
    setTurnOnError(undefined);
    const r = await api.changeCoreSettings({ observability: { logs: { "logging-to-file": true } } });
    if (!mounted.current) return;
    if (r.status === 401) return unauthorized();
    const failure = problem(r);
    if (failure !== undefined) return setTurnOnError(t("turnOnFailed", { error: mask(failure) }));
    setPhase("loading");
    await read(false);
  };
  const clear = async () => {
    setClearError(undefined);
    const r = await api.clearLogs();
    if (!mounted.current) return;
    if (r.status === 401) return unauthorized();
    const failure = problem(r);
    if (failure !== undefined) return setClearError(t("clearFailed", { error: mask(failure) }));
    epoch.current++;
    setLines([]);
    setCapped(false);
    latest.current = undefined;
  };

  const today = logDay(new Date());
  // Masked once per change of the lines or the labels, not on each render (a read every 2 seconds
  // while following, each keystroke in Find).
  const masked = useMemo(() => lines.map(mask), [lines, mask]);
  const query = find.toLocaleLowerCase();
  const shownAt = lines.flatMap((line, i) =>
    (!level || logLevel(line) === level) && line.toLocaleLowerCase().includes(query) ? [i] : [],
  );
  const shown = shownAt.map((i) => lines[i] as string);
  // Following keeps the newest line in view, unless the person scrolled up to read.
  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow && atEnd.current) el.scrollTop = el.scrollHeight;
  }, [shown.length, follow, wrap]);

  if (phase === "off") {
    return (
      <>
        <StatePane
          kind="empty"
          title={t("offTitle")}
          primary={{ label: t("turnOn"), onClick: turnOn }}
          secondary={{ label: t("settings"), href: "#/config/logging" }}
        >
          {t("offText")}
        </StatePane>
        {turnOnError && <Notice tone="danger">{turnOnError}</Notice>}
      </>
    );
  }
  if (phase === "loading") {
    return readError === undefined ? (
      <StatePane kind="loading" title={t("loading")} />
    ) : (
      <>
        <Notice tone="danger">{t("readFailed", { error: mask(readError) })}</Notice>
        <div class="actions">
          <Button onClick={() => read(false)}>{tryAgainWord()}</Button>
        </div>
      </>
    );
  }
  const filtered = find !== "" || level !== "";
  return (
    <div class="logs-pane">
      <div class="logs-controls">
        <Switch
          label={t("follow")}
          checked={follow}
          onChange={(on) => {
            atEnd.current = true;
            setFollow(on);
          }}
        />
        <Switch label={t("wrap")} checked={wrap} onChange={setWrap} />
        <label class="field logs-search" data-part="field">
          <span class="label">{t("find")}</span>
          <input
            data-slot="input"
            type="search"
            placeholder={t("find")}
            value={find}
            onInput={(e) => setFind(e.currentTarget.value)}
          />
        </label>
        <label class="field" data-part="field">
          <span class="label">{t("show")}</span>
          <select
            data-slot="select-trigger"
            aria-label={t("show")}
            value={level}
            onChange={(e) => setLevel(e.currentTarget.value)}
          >
            <option value="">{t("allLines")}</option>
            <option value="warn">{t("warnings")}</option>
            <option value="error">{t("errors")}</option>
          </select>
        </label>
        <div class="actions">
          <Button disabled={shown.length === 0} onClick={() => props.save("pluxy-log.txt", shown.join("\n"))}>
            {t("download")}
          </Button>
          <ConfirmButton question={t("clearQuestion")} confirmLabel={t("confirmClear")} onConfirm={clear}>
            {t("clear")}
          </ConfirmButton>
        </div>
      </div>
      {clearError && <Notice tone="danger">{clearError}</Notice>}
      {readError !== undefined && follow && (
        <Notice tone="warn">{t("readNewFailed", { error: mask(readError) })}</Notice>
      )}
      {capped && <Notice tone="muted">{t("capped")}</Notice>}
      <section
        ref={box}
        aria-label={t("log")}
        data-part="log"
        class="logs-text logs-core"
        data-wrap={wrap ? "" : undefined}
        onScroll={() => {
          const el = box.current;
          if (el) atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < AT_END_PX;
        }}
        // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable region must take focus for the keyboard to scroll it
        tabIndex={0}
      >
        {shown.length > 0 ? (
          shownAt.map((i) => (
            <div key={i} data-part="log-line" data-level={logLevel(lines[i] as string)}>
              <LogLine text={masked[i] as string} today={today} />
            </div>
          ))
        ) : filtered ? (
          <Empty
            onClear={() => {
              setFind("");
              setLevel("");
            }}
          >
            {t("noMatch")}
          </Empty>
        ) : (
          <Empty>{t("empty")}</Empty>
        )}
      </section>
    </div>
  );
}

/** The trace ID in #/logs/<trace ID>, when the address has one. */
function traceInAddress(): string | undefined {
  const raw = location.hash.startsWith("#/logs/") ? location.hash.slice("#/logs/".length) : "";
  if (!raw) return undefined;
  try {
    return decodeURIComponent(raw);
  } catch {
    // Kept as typed: the field says it is not a trace ID.
    return raw;
  }
}

export function LogsPage() {
  const privacy = usePrivacy();
  const saved = useSave();
  const save = (name: string, content: string) => saved.save(name, content, "text/plain;charset=utf-8");
  const [open, setOpen] = useState<OpenTrace | undefined>(() => {
    const trace = traceInAddress();
    return trace === undefined ? undefined : { trace, n: 0 };
  });
  const [tab, setTab] = useState<"log" | "failed">(open ? "failed" : "log");
  // An address with a trace opens the second tab on that request, also while the page is shown.
  useEffect(() => {
    const fromAddress = () => {
      const trace = traceInAddress();
      if (trace === undefined) return;
      setOpen((old) => ({ trace, n: (old?.n ?? 0) + 1 }));
      setTab("failed");
    };
    window.addEventListener("hashchange", fromAddress);
    return () => window.removeEventListener("hashchange", fromAddress);
  }, []);
  return (
    <>
      <PageHead title={t("title")} />
      {saved.notice}
      {privacy.on && <Notice tone="muted">{t("hidden")}</Notice>}
      <Tabs
        label={t("tabs")}
        tabs={[
          { id: "log", label: t("tabLog") },
          { id: "failed", label: t("tabFailed") },
        ]}
        active={tab}
        onChange={(id) => setTab(id === "failed" ? "failed" : "log")}
      >
        {tab === "log" ? <PluxyLog save={save} /> : <FailedRequests save={save} open={open} />}
      </Tabs>
    </>
  );
}

export const logsPage = (): PageDef => ({
  id: "logs",
  dest: "settings",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <LogsPage />,
});
