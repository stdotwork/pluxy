import { useState } from "preact/hooks";
import type { Package } from "../api";
import { SettingsForm } from "../extensions/settings";
import { useAccountLabels, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
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

const t = strings("extensions", {
  title: "Extensions",
  short: "Ext",
  state: "State",
  version: "Version",
  registry: "Registry",
  installed: "Installed",
  digest: "Digest",
  whyNotRunning: "Why it is not running",
  generation: "Generation {generation}",
  process: "Process",
  requestsNow: "Requests now",
  restarts: "Restarts",
  lastError: "Last error",
  isolation:
    "Extensions run as separate programs, so a crash cannot take core down. That is not a sandbox: an extension can reach what its program can reach.",
  disable: "Disable",
  enable: "Enable",
  remove: "Remove",
  available: "Available",
  refresh: "Refresh",
  registryError: "Registry {name}: {error}",
  nothingMore: "Nothing more to install from the registries in core's config.",
  availableExtensions: "Available extensions",
  extension: "Extension",
  install: "Install",
  noInstalled: "No extensions installed.",
  installedExtensions: "Installed extensions",
});

function PackageDetail(props: { p: Package; onClose: () => void; onChanged: () => void }) {
  const { api } = useKit();
  const { p } = props;
  const status = useLoad(api.status);
  const mask = usePrivacy().masker(useAccountLabels(status.data));
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
          [t("state"), <StateBadge state={p.state} />],
          [t("version"), <span class="mono">{p.version}</span>],
          [t("registry"), p.registry],
          [t("installed"), when(p.installed_at)],
          [t("digest"), <span class="mono">{p.sha256.slice(0, 16)}</span>],
          [t("whyNotRunning"), mask(p.error)],
        ]}
      />
      {running.map((g) => (
        <Facts
          key={g.generation}
          rows={[
            [
              t("generation", { generation: g.generation }),
              <StateBadge state={g.current ? g.state : "draining"} />,
            ],
            [t("process"), g.pid],
            [t("requestsNow"), g.active_requests],
            [t("restarts"), g.restarts],
            [t("lastError"), mask(g.last_error)],
          ]}
        />
      ))}
      <p class="muted">{t("isolation")}</p>
      {p.state !== "disabled" && <SettingsForm id={p.id} />}
      {(act.error ?? remove.error) && <Notice tone="danger">{mask(act.error ?? remove.error)}</Notice>}
      <div class="actions">
        <Button
          variant={p.enabled ? "quiet" : "primary"}
          disabled={act.busy}
          onClick={() => act.run(() => api.setEnabled(p.id, !p.enabled))}
        >
          {p.enabled ? t("disable") : t("enable")}
        </Button>
        <ConfirmButton
          disabled={act.busy || remove.busy}
          onConfirm={() => remove.run(() => api.removePackage(p.id))}
        >
          {t("remove")}
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
      title={t("available")}
      actions={
        <Button onClick={registries.reload} disabled={registries.loading}>
          {t("refresh")}
        </Button>
      }
    >
      {registries.error && <Notice tone="danger">{registries.error}</Notice>}
      {broken.map((r) => (
        <Notice key={r.name} tone="warn">
          {t("registryError", { name: r.name, error: r.error ?? "" })}
        </Notice>
      ))}
      {install.error && <Notice tone="danger">{install.error}</Notice>}
      {registries.data && offers.length === 0 ? (
        <Empty>{t("nothingMore")}</Empty>
      ) : (
        <List<Offer>
          label={t("availableExtensions")}
          items={offers}
          id={(o) => `${o.registry}:${o.id}`}
          columns={[
            { label: t("extension"), value: (o) => o.id, mono: true },
            { label: t("version"), value: (o) => o.version, mono: true },
            { label: t("registry"), value: (o) => o.registry },
            {
              label: t("install"),
              value: (o) => (
                <Button
                  disabled={install.busy}
                  onClick={() => install.run(() => api.install(o.id, o.registry))}
                >
                  {t("install")}
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
        title={t("title")}
        actions={
          <Button onClick={packages.reload} disabled={packages.loading}>
            {t("refresh")}
          </Button>
        }
      />
      {packages.error && <Notice tone="danger">{packages.error}</Notice>}
      <div class="stack">
        <MasterDetail
          list={
            packages.data && list.length === 0 ? (
              <Empty>{t("noInstalled")}</Empty>
            ) : (
              <List<Package>
                label={t("installedExtensions")}
                items={list}
                id={(p) => p.id}
                selected={selected}
                onSelect={setSelected}
                columns={[
                  { label: t("extension"), value: (p) => p.id, mono: true },
                  { label: t("version"), value: (p) => p.version, mono: true },
                  { label: t("state"), value: (p) => <StateBadge state={p.state} /> },
                  { label: t("registry"), value: (p) => p.registry },
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

export const extensionsPage = (): PageDef => ({
  id: "extensions",
  group: "control",
  title: t("title"),
  short: t("short"),
  render: () => <ExtensionsPage />,
});
