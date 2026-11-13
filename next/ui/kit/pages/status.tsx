// The Dashboard: how the proxy is doing at a glance (requests, accounts, settings) and the
// config revision and extensions core runs. Its page ID stays `status`: that is its address and
// the theme hook `data-page`.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Account, ExtensionStatus, UsageGroup } from "../api";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import { bucketRows, successPercent, TrafficChart } from "../trafficChart";
import {
  Button,
  Card,
  Empty,
  Facts,
  List,
  Notice,
  PageHead,
  StateBadge,
  type Tone,
  useAction,
  useKit,
  useLoad,
  when,
} from "../ui";

const t = strings("dashboard", {
  title: "Dashboard",
  short: "Dash",
  tip: "Operations overview",
  refresh: "Refresh",
  updated: "Updated {time}",
  reloadConfig: "Reload config",
  reloadRefused: "Reload refused: {error}",
  safeMode: "Core runs in safe mode: no extension runs, so client requests fail.",
  usageOff:
    "Core is not recording requests, so the figures below stay empty. Turn on usage statistics in the Config Panel.",
  verdictGood: "All systems steady",
  verdictWarning: "Minor turbulence",
  verdictCritical: "Needs attention",
  verdictIdle: "Quiet for now",
  verdictOffline: "Waiting to connect",
  requestsHandled: "Requests handled",
  windowMeta: "Rolling {window} window · 10-minute buckets",
  successLabel: "Success",
  failureLabel: "Failure",
  keyMetrics: "Key metrics",
  successRate: "Success rate",
  successHint: "Across {total} requests in the window",
  credentials: "Credentials",
  credentialsHint: "{active} active · {disabled} not serving",
  models: "Models",
  modelsHint: "Served through the proxy endpoint",
  trafficEyebrow: "Live traffic",
  trafficTitle: "Throughput, ten minutes at a time",
  trafficText: "Success and failure counts per 10-minute bucket across the last {window}.",
  fleetEyebrow: "Fleet",
  fleetTitle: "Every provider, side by side",
  fleetEmpty: "No provider traffic recorded yet.",
  healthEyebrow: "Health",
  healthTitle: "Credential status",
  healthActive: "Active",
  healthUnavailable: "Unavailable",
  healthDisabled: "Disabled",
  byProvider: "By provider",
  accounts: "Accounts",
  noAccounts: "No credentials yet.",
  unattributed: "Unattributed",
  openAccounts: "Open auth files",
  runtimeEyebrow: "Runtime",
  runtimeTitle: "Configuration at a glance",
  routing: "Routing strategy",
  retry: "Request retry",
  automatic: "Automatic",
  fileLogging: "File logging",
  requestLog: "Request log",
  modelPrefix: "Model prefix",
  upstreamProxy: "Upstream proxy",
  yes: "Yes",
  no: "No",
  editConfig: "Edit configuration",
  windowHours: "{hours}h",
  windowMinutes: "{minutes}m",
  windowBoth: "{hours}h {minutes}m",
  config: "Config",
  activeRevision: "Active revision",
  activeSince: "Active since",
  hash: "Hash",
  refused: "Revision {revision} was refused: {why}. Revision {active} still serves.",
  noReason: "no reason given",
  runningExtensions: "Running extensions",
  noExtensionRuns: "No extension runs.",
  extension: "Extension",
  version: "Version",
  state: "State",
  requests: "Requests",
  restarts: "Restarts",
  lastError: "Last error",
});

/** The window the page sums up: this many steps of this many minutes, ending now. */
const BUCKET_MINUTES = 10;
const BUCKETS = 20;
const REFRESH_MS = 60_000;

/**
 * The usage call's query. `since` is a duration back from core's own now, so the clocks of
 * the browser and core never have to agree; the series is the same window cut at 10-minute marks.
 * shortcut: totals cover the whole 200 minutes while the series' first step starts at its
 * 10-minute mark, so the oldest step can hold up to 10 minutes less than the totals count;
 * upgrade by asking for `since` at that mark when a call can say it.
 */
export const usageQuery = () =>
  `since=${BUCKET_MINUTES * BUCKETS}m&bucket=${BUCKET_MINUTES}m&buckets=${BUCKETS}`;

export function windowLabel(minutes: number): string {
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (hours === 0) return t("windowMinutes", { minutes: rest });
  return rest === 0 ? t("windowHours", { hours }) : t("windowBoth", { hours, minutes: rest });
}

export type Verdict = "good" | "warning" | "critical" | "idle" | "offline";

/**
 * The page's verdict on the requests in the window: no reply yet is offline, no requests idle,
 * then by the share that succeeded: 95% and above good, 80% and above warning, else critical.
 */
export function verdictOf(requests: { success: number; total: number } | undefined): Verdict {
  if (!requests) return "offline";
  if (requests.total === 0) return "idle";
  if (requests.success * 100 >= requests.total * 95) return "good";
  if (requests.success * 100 >= requests.total * 80) return "warning";
  return "critical";
}

const VERDICT_TONE: Record<Verdict, Tone> = {
  good: "ok",
  warning: "warn",
  critical: "danger",
  idle: "muted",
  offline: "muted",
};

const verdictText = (v: Verdict) =>
  ({
    good: t("verdictGood"),
    warning: t("verdictWarning"),
    critical: t("verdictCritical"),
    idle: t("verdictIdle"),
    offline: t("verdictOffline"),
  })[v];

/** An account is serving unless it is turned off or needs a new login. */
const isActive = (a: Account) => !a.disabled && a.state === "active";

function Panel(props: { eyebrow: string; title: string; children: ComponentChildren }) {
  return (
    <section class="card" data-part="card" aria-label={props.title}>
      <header class="card-head">
        <div>
          <span class="eyebrow">{props.eyebrow}</span>
          <h2>{props.title}</h2>
        </div>
      </header>
      {props.children}
    </section>
  );
}

function Stat(props: { label: string; value: string; hint?: string; tone?: Tone }) {
  return (
    <section class="stat" data-part="stat" data-tone={props.tone} aria-label={props.label}>
      <span class="stat-label" aria-hidden="true">
        {props.label}
      </span>
      <strong>{props.value}</strong>
      {props.hint && <small>{props.hint}</small>}
    </section>
  );
}

const dash = "—";

export function StatusPage() {
  const { api } = useKit();
  const status = useLoad(api.status);
  const usage = useLoad(() => api.usage(usageQuery()));
  const accounts = useLoad(api.accounts);
  const settings = useLoad(api.coreSettings);
  const served = useLoad(api.servedModels);
  const reload = useAction(status.reload);
  const [updatedAt, setUpdatedAt] = useState<Date>();

  const last = useRef(Date.now());
  const refresh = () => {
    last.current = Date.now();
    for (const reloadOne of [status.reload, usage.reload, accounts.reload, settings.reload, served.reload])
      reloadOne();
  };
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  // Reads again every minute and when the page is shown after a longer time, never while hidden.
  useEffect(() => {
    const visible = () => document.visibilityState === "visible";
    const timer = setInterval(() => visible() && refreshRef.current(), REFRESH_MS);
    const shown = () => {
      if (visible() && Date.now() - last.current >= REFRESH_MS) refreshRef.current();
    };
    document.addEventListener("visibilitychange", shown);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", shown);
    };
  }, []);

  const loading = usage.loading;
  useEffect(() => {
    if (!loading) setUpdatedAt(new Date());
  }, [loading]);

  const s = status.data;
  const failed = s && s.desired.revision !== s.active.revision ? s.desired : undefined;
  const u = usage.data;
  const list = accounts.data?.accounts;
  const settingsData = settings.data;
  const modelList = served.data?.models;

  const requests = u && { success: u.success_count, total: u.total_requests };
  const verdict = verdictOf(requests);
  const tone = VERDICT_TONE[verdict];
  const span = windowLabel(BUCKET_MINUTES * BUCKETS);
  const percent = u && successPercent(u.success_count, u.total_requests);

  const active = list?.filter(isActive).length ?? 0;
  const providers = new Map<string, number>();
  for (const a of list ?? []) providers.set(a.extension ?? "", (providers.get(a.extension ?? "") ?? 0) + 1);
  const byProvider = [...providers].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  const yesNo = (on: boolean) => (on ? t("yes") : t("no"));

  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <>
            {updatedAt && (
              <small class="hint">{t("updated", { time: updatedAt.toLocaleTimeString() })}</small>
            )}
            <Button onClick={refresh} disabled={status.loading || usage.loading}>
              {t("refresh")}
            </Button>
            <Button variant="primary" disabled={reload.busy} onClick={() => reload.run(api.reload)}>
              {t("reloadConfig")}
            </Button>
          </>
        }
      />
      {status.error && <Notice tone="danger">{status.error}</Notice>}
      {usage.error && <Notice tone="danger">{usage.error}</Notice>}
      {served.error && <Notice tone="danger">{served.error}</Notice>}
      {reload.error && <Notice tone="danger">{t("reloadRefused", { error: reload.error })}</Notice>}
      {s?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
      {u && !u.enabled && <Notice tone="warn">{t("usageOff")}</Notice>}
      <div class="stack">
        <section class="card hero" data-part="card" data-tone={tone} aria-label={verdictText(verdict)}>
          <h2 class="hero-verdict">{verdictText(verdict)}</h2>
          <Stat
            label={t("requestsHandled")}
            value={u ? u.total_requests.toLocaleString() : dash}
            hint={t("windowMeta", { window: span })}
          />
          {u && u.total_requests > 0 && (
            <p class="chart-legend">
              <span>
                <i class="chart-swatch" data-outcome="success" aria-hidden="true" />
                {t("successLabel")} <b>{u.success_count.toLocaleString()}</b>
              </span>
              <span>
                <i class="chart-swatch" data-outcome="failure" aria-hidden="true" />
                {t("failureLabel")} <b>{u.failure_count.toLocaleString()}</b>
              </span>
            </p>
          )}
        </section>

        <section class="stats" aria-label={t("keyMetrics")}>
          <Stat
            label={t("successRate")}
            value={percent ?? dash}
            hint={t("successHint", { total: (u?.total_requests ?? 0).toLocaleString() })}
            tone={percent ? VERDICT_TONE[verdict] : undefined}
          />
          <Stat
            label={t("credentials")}
            value={list ? list.length.toLocaleString() : dash}
            hint={list && t("credentialsHint", { active, disabled: list.length - active })}
          />
          <Stat
            label={t("models")}
            value={modelList ? modelList.length.toLocaleString() : dash}
            hint={t("modelsHint")}
          />
        </section>

        <Panel eyebrow={t("trafficEyebrow")} title={t("trafficTitle")}>
          <p class="hint">{t("trafficText", { window: span })}</p>
          {u && <TrafficChart rows={bucketRows(u.series)} />}
        </Panel>

        <Panel eyebrow={t("fleetEyebrow")} title={t("fleetTitle")}>
          {u && u.providers.length === 0 ? (
            <Empty>{t("fleetEmpty")}</Empty>
          ) : (
            <List<UsageGroup>
              label={t("fleetTitle")}
              items={u?.providers ?? []}
              id={(g) => g.name}
              columns={[
                { label: t("extension"), value: (g) => g.name, mono: true },
                { label: t("requests"), value: (g) => g.total_requests.toLocaleString() },
                {
                  label: t("successRate"),
                  value: (g) => successPercent(g.success_count, g.total_requests) ?? dash,
                },
              ]}
            />
          )}
        </Panel>

        <div class="dash-pair">
          <Panel eyebrow={t("healthEyebrow")} title={t("healthTitle")}>
            {list &&
              (list.length === 0 ? (
                <Empty>{t("noAccounts")}</Empty>
              ) : (
                <>
                  <Facts
                    rows={[
                      [t("healthActive"), active.toLocaleString()],
                      [
                        t("healthUnavailable"),
                        (
                          list?.filter((a) => !a.disabled && a.state !== "active").length ?? 0
                        ).toLocaleString(),
                      ],
                      [t("healthDisabled"), (list?.filter((a) => a.disabled).length ?? 0).toLocaleString()],
                    ]}
                  />
                  <List<[string, number]>
                    label={t("byProvider")}
                    items={byProvider}
                    id={([name]) => name}
                    columns={[
                      { label: t("byProvider"), value: ([name]) => name || t("unattributed"), mono: true },
                      { label: t("accounts"), value: ([, n]) => n.toLocaleString() },
                    ]}
                  />
                </>
              ))}
            <a href="#/accounts">{t("openAccounts")}</a>
          </Panel>
          <Panel eyebrow={t("runtimeEyebrow")} title={t("runtimeTitle")}>
            {settingsData && (
              <Facts
                rows={[
                  [t("routing"), <span class="mono">{settingsData.routing.strategy}</span>],
                  [t("retry"), settingsData.routing.retry["request-retry"]?.toString() ?? t("automatic")],
                  [t("fileLogging"), yesNo(settingsData.observability.logs["logging-to-file"])],
                  [t("requestLog"), yesNo(settingsData.observability.logs["request-log"])],
                  [t("modelPrefix"), yesNo(settingsData.routing["force-model-prefix"])],
                  [t("upstreamProxy"), <span class="mono">{settingsData["proxy-url"].trim() || dash}</span>],
                ]}
              />
            )}
            <a href="#/config">{t("editConfig")}</a>
          </Panel>
        </div>

        {s && (
          <>
            <Card title={t("config")}>
              <Facts
                rows={[
                  [t("activeRevision"), <span class="mono">{s.active.revision}</span>],
                  [t("activeSince"), when(s.active.at)],
                  [t("hash"), s.active.hash && <span class="mono">{s.active.hash.slice(0, 12)}</span>],
                ]}
              />
              {failed && (
                <Notice tone="danger">
                  {t("refused", {
                    revision: failed.revision,
                    why: failed.error ?? t("noReason"),
                    active: s.active.revision,
                  })}
                </Notice>
              )}
            </Card>
            <Card title={t("runningExtensions")}>
              {s.extensions.length === 0 ? (
                <Empty>{t("noExtensionRuns")}</Empty>
              ) : (
                <List<ExtensionStatus>
                  label={t("runningExtensions")}
                  items={s.extensions}
                  id={(e) => `${e.id}#${e.generation}`}
                  columns={[
                    { label: t("extension"), value: (e) => e.id, mono: true },
                    { label: t("version"), value: (e) => e.version, mono: true },
                    {
                      label: t("state"),
                      value: (e) => <StateBadge state={e.current ? e.state : "draining"} />,
                    },
                    { label: t("requests"), value: (e) => e.active_requests },
                    { label: t("restarts"), value: (e) => e.restarts },
                    { label: t("lastError"), value: (e) => e.last_error ?? "" },
                  ]}
                />
              )}
            </Card>
          </>
        )}
      </div>
    </>
  );
}

export const statusPage = (): PageDef => ({
  id: "status",
  group: "operate",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <StatusPage />,
});
