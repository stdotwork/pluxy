import type { ExtensionStatus } from "../api";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import {
  Button,
  Card,
  Empty,
  Facts,
  List,
  Notice,
  PageHead,
  StateBadge,
  useAction,
  useKit,
  useLoad,
  when,
} from "../ui";

const t = strings("status", {
  title: "Status",
  short: "Status",
  tip: "Operations overview",
  refresh: "Refresh",
  reloadConfig: "Reload config",
  reloadRefused: "Reload refused: {error}",
  safeMode: "Core runs in safe mode: no extension runs, so client requests fail.",
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

export function StatusPage() {
  const { api } = useKit();
  const status = useLoad(api.status);
  const reload = useAction(status.reload);
  const s = status.data;
  const failed = s && s.desired.revision !== s.active.revision ? s.desired : undefined;

  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <>
            <Button onClick={status.reload} disabled={status.loading}>
              {t("refresh")}
            </Button>
            <Button variant="primary" disabled={reload.busy} onClick={() => reload.run(api.reload)}>
              {t("reloadConfig")}
            </Button>
          </>
        }
      />
      {status.error && <Notice tone="danger">{status.error}</Notice>}
      {reload.error && <Notice tone="danger">{t("reloadRefused", { error: reload.error })}</Notice>}
      {s?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
      {s && (
        <div class="stack">
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
        </div>
      )}
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
