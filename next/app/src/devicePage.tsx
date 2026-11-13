// The app's page for extensions on this device: app packages installed on this computer, apart
// from the connected core's Extensions page. The person installs a package from a file they
// choose in the native file chooser or from a registry named in device-registries.json, and
// enables, disables, rolls back or removes it. A package is Unverified unless a curated
// first-party key signed it; only such a package may be a native tool, which has full access to
// the person's user account and is enabled only through a native confirmation.

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import {
  Badge,
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
  useLoad,
  when,
} from "../../ui/kit/ui";
import { type DevicePackage, type DeviceRelease, device } from "./device";

const t = strings("device", {
  title: "Device extensions",
  short: "Device",
  tip: "Extension pages installed on this computer",
  intro:
    "Extensions installed on this computer, apart from the extensions of the core the app is connected to. Their pages run in a webview with no app commands and get no files or settings. Native tools come only from a curated source and stay disabled until you enable them in a native confirmation.",
  safeMode:
    "Safe mode: pages from this device are not loaded and no native tool is enabled. Disable or remove a package here, then start the app without --safe-mode.",
  installFile: "Install from file…",
  refresh: "Refresh",
  extension: "Extension",
  version: "Version",
  trust: "Trust",
  source: "Source",
  state: "State",
  unverified: "Unverified",
  unverifiedWhy:
    "It does not come from a curated source, so the app cannot tell who published it. Its pages still get no app commands, files or settings.",
  curated: "Curated",
  curatedWhy:
    "Signed by a curated first-party key and installed from the curated source that key belongs to.",
  kind: "Kind",
  web: "Pages",
  native: "Native tool",
  fullAccess:
    "This native tool has full access to your user account. Enabling it opens a native confirmation that shows its source, digest and signer.",
  signer: "Signer",
  operations: "Operations",
  local: "File {name}",
  registry: "Registry {name}",
  registryAt: "Registry {name} ({index})",
  digest: "Digest",
  installed: "Installed",
  contracts: "Formats",
  contractsValue: "manifest {manifest}, app {app}, pages {ui}; host contract {host}",
  pages: "Pages",
  lastGood: "Last good release",
  resources: "Asks for files",
  resourcesValue: "{ids} (not available on this device yet)",
  settings: "Settings",
  settingsValue: "declared, not available on this device yet",
  enabled: "enabled",
  disabled: "disabled",
  enable: "Enable",
  disable: "Disable",
  rollBack: "Roll back to {version}",
  remove: "Remove",
  noInstalled: "No extensions installed on this device.",
  installedList: "Extensions on this device",
  available: "Available",
  noRegistries: "No registries are named in device-registries.json in the app's config folder.",
  registryError: "Registry {name}: {error}",
  nothingMore: "Nothing more to install from these registries.",
  availableList: "Available extensions",
  install: "Install",
  update: "Update to {version}",
  reserved: "Refused: reserved publisher",
});

function sourceText(p: DevicePackage): string {
  return p.source.kind === "local"
    ? t("local", { name: p.source.name })
    : p.source.index
      ? t("registryAt", { name: p.source.name, index: p.source.index })
      : t("registry", { name: p.source.name });
}

function trustBadge(p: DevicePackage) {
  return p.trust === "curated" ? (
    <Badge tone="ok">{t("curated")}</Badge>
  ) : (
    <Badge tone="warn">{t("unverified")}</Badge>
  );
}

function releaseFacts(r: DeviceRelease): [string, ComponentChildren][] {
  return [
    [t("version"), <span class="mono">{r.version}</span>],
    [t("digest"), <span class="mono">{r.sha256.slice(0, 16)}</span>],
    [t("installed"), when(r.installed_at)],
    [
      t("contracts"),
      t("contractsValue", {
        manifest: r.manifest_version,
        app: r.app_version,
        ui: r.ui_version ?? "-",
        host: r.host_versions.join(", "),
      }),
    ],
    [t("pages"), r.pages.map((p) => p.title).join(", ") || undefined],
    [t("resources"), r.resources.length ? t("resourcesValue", { ids: r.resources.join(", ") }) : undefined],
    [t("settings"), r.settings ? t("settingsValue") : undefined],
    [t("signer"), r.signer ?? undefined],
    [t("operations"), r.operations.map(([id, what]) => `${id}: ${what}`).join("; ") || undefined],
  ];
}

function PackageDetail(props: { p: DevicePackage; onClose: () => void; onChanged: () => void }) {
  const { p } = props;
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  return (
    <Detail title={p.id} onClose={props.onClose}>
      <Facts
        rows={[
          [t("trust"), trustBadge(p)],
          [t("kind"), t(p.tier === "native" ? "native" : "web")],
          [t("source"), sourceText(p)],
          [t("state"), <StateBadge state={p.enabled ? "active" : "disabled"} />],
          ...releaseFacts(p.active),
          [t("lastGood"), p.last_good && <span class="mono">{p.last_good.version}</span>],
        ]}
      />
      <p class="muted">{t(p.trust === "curated" ? "curatedWhy" : "unverifiedWhy")}</p>
      {p.tier === "native" && <Notice tone="warn">{t("fullAccess")}</Notice>}
      {(act.error ?? remove.error) && <Notice tone="danger">{act.error ?? remove.error}</Notice>}
      <div class="actions">
        <Button
          variant={p.enabled ? "quiet" : "primary"}
          disabled={act.busy}
          onClick={() => act.run(() => device.setEnabled(p.id, !p.enabled))}
        >
          {p.enabled ? t("disable") : t("enable")}
        </Button>
        {p.last_good && (
          <Button disabled={act.busy} onClick={() => act.run(() => device.rollback(p.id))}>
            {t("rollBack", { version: p.last_good.version })}
          </Button>
        )}
        <ConfirmButton
          disabled={act.busy || remove.busy}
          onConfirm={() => remove.run(() => device.remove(p.id))}
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
  refused?: string;
  installed?: DevicePackage;
}

function Available(props: { installed: DevicePackage[]; onInstalled: () => void }) {
  const registries = useLoad(device.registries);
  const install = useAction(props.onInstalled);
  const offers: Offer[] = [];
  for (const r of registries.data ?? []) {
    for (const p of r.packages) {
      const latest = p.versions[0]; // newest first
      const installed = props.installed.find((i) => i.id === p.id);
      const current = installed?.source.kind === "registry" && installed.source.name === r.name;
      if (!latest || (installed && (!current || installed.active.version === latest))) continue;
      offers.push({ id: p.id, version: latest, registry: r.name, refused: p.refused, installed });
    }
  }
  const broken = (registries.data ?? []).filter((r) => r.error);
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
      {registries.data?.length === 0 ? (
        <Empty>{t("noRegistries")}</Empty>
      ) : registries.data && offers.length === 0 ? (
        <Empty>{t("nothingMore")}</Empty>
      ) : (
        <List<Offer>
          label={t("availableList")}
          items={offers}
          id={(o) => `${o.registry}:${o.id}`}
          columns={[
            { label: t("extension"), value: (o) => o.id, mono: true },
            { label: t("version"), value: (o) => o.version, mono: true },
            { label: t("source"), value: (o) => t("registry", { name: o.registry }) },
            {
              label: t("install"),
              value: (o) =>
                o.refused ? (
                  <span class="muted" title={o.refused}>
                    {t("reserved")}
                  </span>
                ) : (
                  <Button
                    disabled={install.busy}
                    onClick={() => install.run(() => device.install(o.registry, o.id, o.version))}
                  >
                    {o.installed ? t("update", { version: o.version }) : t("install")}
                  </Button>
                ),
            },
          ]}
        />
      )}
    </Card>
  );
}

export function DevicePage(props: { onChanged: () => void }) {
  const catalog = useLoad(device.list);
  const [selected, setSelected] = useState<string>();
  const changed = () => {
    catalog.reload();
    props.onChanged();
  };
  const file = useAction(changed);
  const list = catalog.data?.packages ?? [];
  const chosen = list.find((p) => p.id === selected);
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <>
            <Button onClick={catalog.reload} disabled={catalog.loading}>
              {t("refresh")}
            </Button>
            <Button variant="primary" disabled={file.busy} onClick={() => file.run(device.installFile)}>
              {t("installFile")}
            </Button>
          </>
        }
      />
      <p class="muted">{t("intro")}</p>
      {catalog.data?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
      {catalog.data?.warning && <Notice tone="warn">{catalog.data.warning}</Notice>}
      {(catalog.error ?? file.error) && <Notice tone="danger">{catalog.error ?? file.error}</Notice>}
      <div class="stack">
        <MasterDetail
          list={
            catalog.data && list.length === 0 ? (
              <Empty>{t("noInstalled")}</Empty>
            ) : (
              <List<DevicePackage>
                label={t("installedList")}
                items={list}
                id={(p) => p.id}
                selected={selected}
                onSelect={setSelected}
                columns={[
                  { label: t("extension"), value: (p) => p.id, mono: true },
                  { label: t("version"), value: (p) => p.active.version, mono: true },
                  { label: t("trust"), value: trustBadge },
                  { label: t("source"), value: sourceText },
                  {
                    label: t("state"),
                    value: (p) => <StateBadge state={p.enabled ? "active" : "disabled"} />,
                  },
                ]}
              />
            )
          }
          detail={
            chosen && (
              <PackageDetail
                key={`${chosen.id} ${chosen.active.sha256} ${chosen.enabled}`}
                p={chosen}
                onClose={() => setSelected(undefined)}
                onChanged={changed}
              />
            )
          }
        />
        <Available installed={list} onInstalled={changed} />
      </div>
    </>
  );
}

export const devicePage = (onChanged: () => void): PageDef => ({
  id: "device",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <DevicePage onChanged={onChanged} />,
});
