// Extensions: the programs that connect pluxy to each provider. A person sees which are installed
// and what state each is in, installs more, and opens one to see how it runs, change its settings,
// turn it off or on, or remove it. Rows, the loading, empty and failed panes and the settings form
// come from the shared parts.

import { useState } from "preact/hooks";
import type { Package, Registry, Status } from "../api";
import { SettingsForm } from "../extensions/settings";
import { Phases, providerName, StatePane } from "../parts";
import { useAccountLabels, usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { strings } from "../strings";
import {
  Button,
  ConfirmButton,
  Detail,
  Facts,
  type Loaded,
  MasterDetail,
  Notice,
  PageHead,
  StateBadge,
  useAction,
  useKit,
  useLoad,
  usePoll,
  when,
} from "../ui";
import { SettingGroup, SettingsColumn } from "./settingGroup";

const t = strings("extensions", {
  title: "Extensions",
  tip: "The programs that connect pluxy to each provider",
  intro:
    "Each extension is a program that connects pluxy to one provider. Accounts need their provider's extension.",
  refresh: "Refresh",
  safeMode: "pluxy was started in safe mode, so no extension runs. Start pluxy again normally to leave it.",
  installedHeading: "Installed",
  loading: "Loading extensions…",
  couldNotLoad: "Could not read the installed extensions",
  noneYet: "No extensions yet",
  noneYetHelp: "pluxy needs one for each provider you use.",
  state: "State",
  version: "Version",
  installedFrom: "Installed from",
  installedAt: "Installed",
  checksum: "Checksum",
  whyStopped: "Why it stopped",
  processId: "Process ID",
  requestsNow: "Requests now",
  restarts: "Restarts",
  lastError: "Last error",
  oldCopy: "Finishing requests on the version before",
  trust: "Extensions run as separate programs with your user's permissions. Install only ones you trust.",
  turnOff: "Turn off",
  turnOn: "Turn on",
  turnOffAbout: "Its accounts stop taking requests until you turn it on again.",
  remove: "Remove",
  removeAbout: "Removes the program. Its accounts stay and work again if you install it again.",
  availableHeading: "Available to install",
  loadingList: "Loading what is on offer…",
  couldNotLoadList: "Could not read what is on offer",
  install: "Install",
  installing: "Installing…",
  allInstalled: "Every extension on offer is installed.",
  noList: "This pluxy has no list to install extensions from.",
  noListHelp: "A pluxy built from source has none until one is added to its settings file.",
  openFile: "Open the settings file",
  listFailed: "Could not read the list {name}: {error}",
});

function ExtMark(props: { id: string }) {
  let hash = 0;
  for (const c of props.id) hash = (hash * 31 + c.charCodeAt(0)) % 6;
  return (
    <span class="ext-mark" data-series={hash + 1} aria-hidden="true">
      {providerName(props.id).slice(0, 1).toUpperCase()}
    </span>
  );
}

/** How often the lists are read again while an extension is starting. */
const STARTING_POLL_MS = 1500;

function Copies(props: { id: string; status?: Status }) {
  const mask = usePrivacy().masker(useAccountLabels(props.status));
  const copies = (props.status?.extensions ?? [])
    .filter((e) => e.id === props.id)
    .sort((a, b) => Number(b.current) - Number(a.current));
  return (
    <>
      {copies.map((g) => (
        <div key={g.generation} class="ext-copy">
          {!g.current && <p class="muted">{t("oldCopy")}</p>}
          <Facts
            rows={[
              [t("processId"), g.pid],
              [t("requestsNow"), g.active_requests],
              [t("restarts"), g.restarts],
              [t("lastError"), mask(g.last_error)],
            ]}
          />
        </div>
      ))}
    </>
  );
}

function PackageDetail(props: { p: Package; status?: Status; onClose: () => void; onChanged: () => void }) {
  const { api } = useKit();
  const { p } = props;
  const mask = usePrivacy().masker(useAccountLabels(props.status));
  const power = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  return (
    <Detail title={p.id} onClose={props.onClose}>
      <Facts
        rows={[
          [t("state"), <StateBadge state={p.state} />],
          [t("version"), <span class="mono">{p.version}</span>],
          [t("installedFrom"), p.registry],
          [t("installedAt"), when(p.installed_at)],
          [t("checksum"), <span class="mono">{p.sha256.slice(0, 16)}</span>],
          [t("whyStopped"), mask(p.error)],
        ]}
      />
      <Copies id={p.id} status={props.status} />
      <p class="muted">{t("trust")}</p>
      {p.state !== "disabled" && <SettingsForm id={p.id} />}
      <div class="ext-actions">
        <div class="ext-action">
          <div class="ext-action-line">
            <Button
              variant={p.enabled ? "quiet" : "primary"}
              disabled={power.busy || remove.busy}
              onClick={() => power.run(() => api.setEnabled(p.id, !p.enabled))}
            >
              {p.enabled ? t("turnOff") : t("turnOn")}
            </Button>
            {p.enabled && <span class="muted">{t("turnOffAbout")}</span>}
          </div>
          {power.error && <Notice tone="danger">{mask(power.error)}</Notice>}
        </div>
        <div class="ext-action">
          <div class="ext-action-line">
            <ConfirmButton
              disabled={power.busy || remove.busy}
              onConfirm={() => remove.run(() => api.removePackage(p.id))}
            >
              {t("remove")}
            </ConfirmButton>
            <span class="muted">{t("removeAbout")}</span>
          </div>
          {remove.error && <Notice tone="danger">{mask(remove.error)}</Notice>}
        </div>
      </div>
    </Detail>
  );
}

function Installed(props: {
  packages: Loaded<{ packages: Package[] }>;
  status: Loaded<Status>;
  onChanged: () => void;
}) {
  const [selected, setSelected] = useState<string>();
  return (
    <SettingGroup title={t("installedHeading")} card={false}>
      <Phases
        load={props.packages}
        loading={t("loading")}
        failed={{ title: t("couldNotLoad") }}
        isEmpty={(data) => data.packages.length === 0}
        empty={
          <StatePane kind="empty" title={t("noneYet")}>
            {t("noneYetHelp")}
          </StatePane>
        }
      >
        {(data) => {
          const chosen = data.packages.find((p) => p.id === selected);
          return (
            <MasterDetail
              list={
                <div class="setting-group-card" data-part="setting-card">
                  <ul class="ext-rows">
                    {data.packages.map((p) => (
                      <li key={p.id}>
                        <button
                          type="button"
                          class="ext-row"
                          aria-current={p.id === selected ? "true" : undefined}
                          onClick={() => setSelected(p.id)}
                        >
                          <ExtMark id={p.id} />
                          <span class="ext-who">
                            <span class="ext-name">{providerName(p.id)}</span>
                            <span class="ext-id mono">{p.id}</span>
                          </span>
                          <span class="ext-version mono">{p.version}</span>
                          <span class="ext-state">
                            <StateBadge state={p.state} />
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              }
              detail={
                chosen && (
                  <PackageDetail
                    p={chosen}
                    status={props.status.data}
                    onClose={() => setSelected(undefined)}
                    onChanged={props.onChanged}
                  />
                )
              }
            />
          );
        }}
      </Phases>
    </SettingGroup>
  );
}

interface Offer {
  id: string;
  version: string;
  registry: string;
}

function OfferRow(props: { o: Offer; onInstalled: () => void }) {
  const { api } = useKit();
  const { o } = props;
  const install = useAction(props.onInstalled);
  return (
    <li class="ext-offer">
      <div class="ext-row" data-static="">
        <ExtMark id={o.id} />
        <span class="ext-who">
          <span class="ext-name">{providerName(o.id)}</span>
          <span class="ext-id mono">{o.id}</span>
        </span>
        <span class="ext-version mono">{o.version}</span>
        <span class="ext-state">
          <Button
            variant="quiet"
            disabled={install.busy}
            label={`${t("install")} ${o.id}`}
            onClick={() => install.run(() => api.install(o.id, o.registry))}
          >
            {install.busy ? t("installing") : t("install")}
          </Button>
        </span>
      </div>
      {install.error && <Notice tone="danger">{install.error}</Notice>}
    </li>
  );
}

function offersOf(registries: Registry[], installed: Set<string>): Offer[] {
  const offers: Offer[] = [];
  for (const r of registries) {
    for (const p of r.packages ?? []) {
      const latest = p.releases[0]; // newest first
      if (latest && !installed.has(p.id))
        offers.push({ id: p.id, version: latest.version, registry: r.name });
    }
  }
  return offers;
}

function Available(props: {
  registries: Loaded<{ registries: Registry[] }>;
  installed: Set<string>;
  onInstalled: () => void;
}) {
  return (
    <SettingGroup title={t("availableHeading")} card={false}>
      <Phases
        load={props.registries}
        loading={t("loadingList")}
        failed={{ title: t("couldNotLoadList") }}
        isEmpty={() => false}
        empty={null}
      >
        {(data) => {
          const offers = offersOf(data.registries, props.installed);
          const broken = data.registries.filter((r) => r.error);
          return (
            <>
              {broken.map((r) => (
                <Notice key={r.name} tone="warn">
                  {t("listFailed", { name: r.name, error: r.error ?? "" })}
                </Notice>
              ))}
              {offers.length > 0 && (
                <div class="setting-group-card" data-part="setting-card">
                  <ul class="ext-rows">
                    {offers.map((o) => (
                      <OfferRow key={`${o.registry}:${o.id}`} o={o} onInstalled={props.onInstalled} />
                    ))}
                  </ul>
                </div>
              )}
              {offers.length === 0 && data.registries.length === 0 && (
                <StatePane
                  kind="empty"
                  title={t("noList")}
                  primary={{ label: t("openFile"), href: "#/config/source" }}
                >
                  {t("noListHelp")}
                </StatePane>
              )}
              {offers.length === 0 && data.registries.length > 0 && broken.length === 0 && (
                <p class="muted">{t("allInstalled")}</p>
              )}
            </>
          );
        }}
      </Phases>
    </SettingGroup>
  );
}

export function ExtensionsPage() {
  const { api, extensionsChanged } = useKit();
  const packages = useLoad(api.packages);
  const status = useLoad(api.status);
  const registries = useLoad(api.registries);
  // After a change, the lists and the navigation's extension pages may differ.
  const changed = () => {
    packages.reload();
    status.reload();
    registries.reload();
    extensionsChanged();
  };
  const loading = packages.loading || status.loading || registries.loading;
  // A program that is starting says so only until its handshake ends: read again meanwhile, so
  // Starting does not stay on screen after it runs.
  const starting = (packages.data?.packages ?? []).some((p) => p.state === "starting");
  usePoll(
    () => {
      packages.reload();
      status.reload();
    },
    STARTING_POLL_MS,
    starting,
  );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button onClick={changed} disabled={loading}>
            {t("refresh")}
          </Button>
        }
      />
      <SettingsColumn class="ext">
        <p class="muted ext-intro">{t("intro")}</p>
        {status.data?.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
        <Installed packages={packages} status={status} onChanged={changed} />
        <Available
          registries={registries}
          installed={new Set((packages.data?.packages ?? []).map((p) => p.id))}
          onInstalled={changed}
        />
      </SettingsColumn>
    </>
  );
}

export const extensionsPage = (): PageDef => ({
  id: "extensions",
  dest: "settings",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <ExtensionsPage />,
});
