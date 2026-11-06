import type { ExtensionStatus } from "../api";
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

export function StatusPage() {
  const { api } = useKit();
  const status = useLoad(api.status);
  const reload = useAction(status.reload);
  const s = status.data;
  const failed = s && s.desired.revision !== s.active.revision ? s.desired : undefined;

  return (
    <>
      <PageHead
        title="Status"
        actions={
          <>
            <Button onClick={status.reload} disabled={status.loading}>
              Refresh
            </Button>
            <Button variant="primary" disabled={reload.busy} onClick={() => reload.run(api.reload)}>
              Reload config
            </Button>
          </>
        }
      />
      {status.error && <Notice tone="danger">{status.error}</Notice>}
      {reload.error && <Notice tone="danger">Reload refused: {reload.error}</Notice>}
      {s?.safe_mode && (
        <Notice tone="warn">Core runs in safe mode: no extension runs, so client requests fail.</Notice>
      )}
      {s && (
        <div class="stack">
          <Card title="Config">
            <Facts
              rows={[
                ["Active revision", <span class="mono">{s.active.revision}</span>],
                ["Active since", when(s.active.at)],
                ["Hash", s.active.hash && <span class="mono">{s.active.hash.slice(0, 12)}</span>],
              ]}
            />
            {failed && (
              <Notice tone="danger">
                Revision {failed.revision} was refused: {failed.error ?? "no reason given"}. Revision{" "}
                {s.active.revision} still serves.
              </Notice>
            )}
          </Card>
          <Card title="Running extensions">
            {s.extensions.length === 0 ? (
              <Empty>No extension runs.</Empty>
            ) : (
              <List<ExtensionStatus>
                label="Running extensions"
                items={s.extensions}
                id={(e) => `${e.id}#${e.generation}`}
                columns={[
                  { label: "Extension", value: (e) => e.id, mono: true },
                  { label: "Version", value: (e) => e.version, mono: true },
                  { label: "State", value: (e) => <StateBadge state={e.current ? e.state : "draining"} /> },
                  { label: "Requests", value: (e) => e.active_requests },
                  { label: "Restarts", value: (e) => e.restarts },
                  { label: "Last error", value: (e) => e.last_error ?? "" },
                ]}
              />
            )}
          </Card>
        </div>
      )}
    </>
  );
}
