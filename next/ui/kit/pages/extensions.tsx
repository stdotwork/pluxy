import { useState } from "preact/hooks";
import type { Package } from "../api";
import { SettingsForm } from "../extensions/settings";
import {
  Button,
  Card,
  ConfirmButton,
  Detail,
  Empty,
  Facts,
  List,
  MasterDetail,
  Notice,
  PageHead,
  StateBadge,
  useAction,
  useKit,
  useLoad,
  when,
} from "../ui";

function PackageDetail(props: { p: Package; onClose: () => void; onChanged: () => void }) {
  const { api } = useKit();
  const { p } = props;
  const status = useLoad(api.status);
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  const running = status.data?.extensions.filter((e) => e.id === p.id) ?? [];
  return (
    <Detail title={p.id} onClose={props.onClose}>
      <Facts
        rows={[
          ["State", <StateBadge state={p.state} />],
          ["Version", <span class="mono">{p.version}</span>],
          ["Registry", p.registry],
          ["Installed", when(p.installed_at)],
          ["Digest", <span class="mono">{p.sha256.slice(0, 16)}</span>],
          ["Why it is not running", p.error],
        ]}
      />
      {running.map((g) => (
        <Facts
          key={g.generation}
          rows={[
            [`Generation ${g.generation}`, <StateBadge state={g.current ? g.state : "draining"} />],
            ["Process", g.pid],
            ["Requests now", g.active_requests],
            ["Restarts", g.restarts],
            ["Last error", g.last_error],
          ]}
        />
      ))}
      <p class="muted">
        Extensions run as separate programs, so a crash cannot take core down. That is not a sandbox: an
        extension can reach what its program can reach.
      </p>
      {p.state !== "disabled" && <SettingsForm id={p.id} />}
      {(act.error ?? remove.error) && <Notice tone="danger">{act.error ?? remove.error}</Notice>}
      <div class="actions">
        <Button
          variant={p.enabled ? "quiet" : "primary"}
          disabled={act.busy}
          onClick={() => act.run(() => api.setEnabled(p.id, !p.enabled))}
        >
          {p.enabled ? "Disable" : "Enable"}
        </Button>
        <ConfirmButton
          disabled={act.busy || remove.busy}
          onConfirm={() => remove.run(() => api.removePackage(p.id))}
        >
          Remove
        </ConfirmButton>
      </div>
    </Detail>
  );
}

interface Offer {
  id: string;
  version: string;
  registry: string;
}

function Available(props: { installed: Set<string>; onInstalled: () => void }) {
  const { api } = useKit();
  const registries = useLoad(api.registries);
  const install = useAction(props.onInstalled);
  const offers: Offer[] = [];
  for (const r of registries.data?.registries ?? []) {
    for (const p of r.packages ?? []) {
      const latest = p.releases[0]; // newest first
      if (latest && !props.installed.has(p.id))
        offers.push({ id: p.id, version: latest.version, registry: r.name });
    }
  }
  const broken = (registries.data?.registries ?? []).filter((r) => r.error);
  return (
    <Card
      title="Available"
      actions={
        <Button onClick={registries.reload} disabled={registries.loading}>
          Refresh
        </Button>
      }
    >
      {registries.error && <Notice tone="danger">{registries.error}</Notice>}
      {broken.map((r) => (
        <Notice key={r.name} tone="warn">
          Registry {r.name}: {r.error}
        </Notice>
      ))}
      {install.error && <Notice tone="danger">{install.error}</Notice>}
      {registries.data && offers.length === 0 ? (
        <Empty>Nothing more to install from the registries in core's config.</Empty>
      ) : (
        <List<Offer>
          label="Available extensions"
          items={offers}
          id={(o) => `${o.registry}:${o.id}`}
          columns={[
            { label: "Extension", value: (o) => o.id, mono: true },
            { label: "Version", value: (o) => o.version, mono: true },
            { label: "Registry", value: (o) => o.registry },
            {
              label: "Install",
              value: (o) => (
                <Button
                  disabled={install.busy}
                  onClick={() => install.run(() => api.install(o.id, o.registry))}
                >
                  Install
                </Button>
              ),
            },
          ]}
        />
      )}
    </Card>
  );
}

export function ExtensionsPage() {
  const { api, extensionsChanged } = useKit();
  const loaded = useLoad(api.packages);
  // After a change, the navigation's extension pages may differ too.
  const packages = {
    ...loaded,
    reload: () => {
      loaded.reload();
      extensionsChanged();
    },
  };
  const [selected, setSelected] = useState<string>();
  const list = packages.data?.packages ?? [];
  const chosen = list.find((p) => p.id === selected);

  return (
    <>
      <PageHead
        title="Extensions"
        actions={
          <Button onClick={packages.reload} disabled={packages.loading}>
            Refresh
          </Button>
        }
      />
      {packages.error && <Notice tone="danger">{packages.error}</Notice>}
      <div class="stack">
        <MasterDetail
          list={
            packages.data && list.length === 0 ? (
              <Empty>No extensions installed.</Empty>
            ) : (
              <List<Package>
                label="Installed extensions"
                items={list}
                id={(p) => p.id}
                selected={selected}
                onSelect={setSelected}
                columns={[
                  { label: "Extension", value: (p) => p.id, mono: true },
                  { label: "Version", value: (p) => p.version, mono: true },
                  { label: "State", value: (p) => <StateBadge state={p.state} /> },
                  { label: "Registry", value: (p) => p.registry },
                ]}
              />
            )
          }
          detail={
            chosen && (
              // Keyed by state, so the running generations shown reload when the package changes.
              <PackageDetail
                key={`${chosen.id} ${chosen.state}`}
                p={chosen}
                onClose={() => setSelected(undefined)}
                onChanged={packages.reload}
              />
            )
          }
        />
        <Available installed={new Set(list.map((p) => p.id))} onInstalled={packages.reload} />
      </div>
    </>
  );
}
