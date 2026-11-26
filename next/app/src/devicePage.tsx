// The app's page for add-ons on this computer: pages and coding-tool add-ons installed here, apart
// from pluxy's own extensions. The person installs one from a file they choose in the native file
// chooser or from a source named in device-registries.json, and turns it on or off, goes back to
// the copy before, or removes it. An add-on is Unverified unless pluxy's own publisher key signed
// it; only a verified one may be a coding-tool add-on, which runs as the person and is turned on
// only through a native confirmation. A verified page add-on's use of coding-tool add-ons
// (tool access) is allowed only through a native confirmation too, and can be taken back here. A
// page add-on whose publisher the person approved can be given files and folders it declares; each
// one asks natively.

import type { ComponentChildren } from "preact";
import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { Phases } from "../../ui/kit/parts";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import {
  Badge,
  Button,
  ConfirmButton,
  Empty,
  Facts,
  List,
  type Loaded,
  Notice,
  PageHead,
  useAction,
  useLoad,
  when,
} from "../../ui/kit/ui";
import { Section } from "./clientPage";
import {
  type AccessGrant,
  access,
  type DevicePackage,
  type DeviceRegistry,
  type DeviceRelease,
  device,
} from "./device";
import { askFirst, Intro } from "./pageBits";

const t = strings("addons", {
  title: "Add-ons",
  tip: "Pages and coding-tool add-ons installed on this computer",
  reading: "Reading add-ons…",
  readFailed: "Could not read the add-ons on this computer",
  intro:
    "Add-ons add pages and coding-tool connections to this app. They run on this computer, apart from pluxy's own extensions.",
  refresh: "Refresh",
  installFile: "Install from file…",
  safeMode:
    "Safe mode is on: add-on pages and coding-tool add-ons are paused. Turn off or remove the one that caused trouble, then quit the app and open it again the usual way.",
  installed: "Installed",
  name: "Name",
  version: "Version",
  trust: "Trust",
  state: "State",
  verified: "Verified",
  unverified: "Unverified",
  on: "On",
  off: "Turned off",
  none: "No add-ons installed on this computer.",
  choose: "Choose an add-on to see its details and what you can do with it.",
  close: "Close",
  toolKind: "Coding-tool add-on",
  toolKindAbout:
    "Runs as you, with your permissions. Turning it on opens a confirmation window that shows who published it.",
  pageKind: "Page add-on",
  pageKindAbout: "Its pages get no settings, files or tools unless you allow them.",
  verifiedAbout: "Signed by pluxy's publisher and installed from its source.",
  unverifiedAbout: "Not from a verified source, so the publisher is not checked.",
  fromSource: "From {source}",
  fromFile: "Installed from a file",
  turnOn: "Turn on",
  turnOff: "Turn off",
  allowTools: "Allow tool access…",
  removeTools: "Remove tool access",
  toolsAbout: "Its pages ask to use coding-tool add-ons. pluxy blocks that until you allow it.",
  toolsAllowed: "Its pages may use coding-tool add-ons.",
  toolsBlocked: "Its pages ask to use coding-tool add-ons, but only verified add-ons may.",
  goBack: "Go back to {version}",
  remove: "Remove",
  files: "Files",
  approved: "Publisher approved",
  notApproved: "Publisher not approved",
  approve: "Approve publisher…",
  cannotGive: "Files cannot be given to this add-on: {why}",
  unsigned: "it is not signed.",
  signatureUnusable: "its signature cannot be used for files.",
  chooseFile: "Choose a file…",
  chooseFolder: "Choose a folder…",
  chooseAgain: "Choose again…",
  removeFile: "Remove",
  untilQuit: "Until the app quits",
  changed: "File changed",
  otherVersion: "Given to another version",
  sendWarning: "An add-on's pages can send what they read anywhere they can reach.",
  technical: "Technical details",
  digest: "Digest",
  installedOn: "Installed",
  formats: "Formats",
  formatsValue: "manifest {manifest}, app {app}, pages {ui}; host contract {host}",
  pages: "Pages",
  asksFiles: "Asks for files",
  signer: "Signer",
  signerValue: "{publisher} key {key}",
  signerKey: "Public key",
  signerSource: "Signer's source",
  operations: "Operations",
  imports: "Uses",
  importValue: "{package} (contract {contract}, {required}): {operations}",
  required: "required",
  optional: "optional",
  exports: "Offers",
  exportsValue: "contract {contract}: {operations}",
  lastGood: "Copy kept before",
  available: "Available to install",
  checking: "Checking add-on sources…",
  couldNotRead: "Could not read {source}: {reason}",
  noSources:
    "No add-on sources are set up in this copy of the app. You can still install an add-on file with Install from file….",
  allInstalled: "Everything these sources offer is installed.",
  install: "Install",
  update: "Update to {version}",
  blocked: "Blocked",
  source: "Source",
});

function sourceText(p: DevicePackage): string {
  if (p.source.kind === "local") return t("fromFile");
  return t("fromSource", { source: p.source.index ? `${p.source.name} (${p.source.index})` : p.source.name });
}

const trustBadge = (p: DevicePackage) =>
  p.trust === "curated" ? (
    <Badge tone="ok">{t("verified")}</Badge>
  ) : (
    <Badge tone="warn">{t("unverified")}</Badge>
  );

const stateBadge = (p: DevicePackage) =>
  p.enabled ? <Badge tone="ok">{t("on")}</Badge> : <Badge tone="muted">{t("off")}</Badge>;

/** Digest, formats, operations, imports and exports: every fact stays one click away. */
function technicalFacts(r: DeviceRelease, p: DevicePackage): [string, ComponentChildren][] {
  return [
    [t("version"), <span class="mono">{r.version}</span>],
    [t("digest"), <span class="mono">{r.sha256.slice(0, 16)}</span>],
    [t("installedOn"), when(r.installed_at)],
    [
      t("formats"),
      t("formatsValue", {
        manifest: r.manifest_version,
        app: r.app_version,
        ui: r.ui_version ?? "-",
        host: r.host_versions.join(", "),
      }),
    ],
    [t("pages"), r.pages.map((x) => x.title).join(", ") || undefined],
    [t("asksFiles"), r.resources.join(", ") || undefined],
    [
      t("signer"),
      r.signer ? t("signerValue", { publisher: r.signer.publisher, key: r.signer.key_id }) : undefined,
    ],
    [t("signerKey"), r.signer && <span class="mono">{r.signer.public_key}</span>],
    [t("signerSource"), r.signer && <span class="mono">{r.signer.source}</span>],
    [
      t("operations"),
      r.operations.map(([id, what, effect]) => `${id}: ${what} (${effect})`).join("; ") || undefined,
    ],
    [
      t("imports"),
      r.imports
        .map((i) =>
          t("importValue", {
            package: i.package,
            contract: i.contract.join(", "),
            required: t(i.required ? "required" : "optional"),
            operations: i.operations.join(", "),
          }),
        )
        .join("; ") || undefined,
    ],
    [
      t("exports"),
      r.exports
        ? t("exportsValue", { contract: r.exports.contract, operations: r.exports.operations.join(", ") })
        : undefined,
    ],
    [t("lastGood"), p.last_good && <span class="mono">{p.last_good.version}</span>],
  ];
}

/** What a page add-on may be given: who published it, and each file or folder it asks for. */
function Files(props: { p: DevicePackage }) {
  const { p } = props;
  const grants = useLoad(access.list);
  const act = useAction(grants.reload);
  // Only a signature Access may use is offered for approval and grants (src-tauri/src/device.rs).
  const signer = p.active.signer?.access ? p.active.signer : null;
  if (p.active.declared.length === 0) return null;
  const mine = (grants.data?.grants ?? []).filter((g) => g.package === p.id);
  const rows = p.active.declared
    .map((r) => ({ r, g: mine.find((x) => x.resource === r.id) }))
    .filter(({ g }) => signer?.approved || g);
  return (
    <div class="addon-block">
      <h4>{t("files")}</h4>
      {signer ? (
        <p class="addon-line">
          <Badge tone={signer.approved ? "ok" : "warn"}>
            {signer.approved ? t("approved") : t("notApproved")}
          </Badge>
          <span class="muted">{t("signerValue", { publisher: signer.publisher, key: signer.key_id })}</span>
          {!signer.approved && (
            <Button disabled={act.busy} onClick={() => act.run(askFirst(() => access.approveSigner(p.id)))}>
              {t("approve")}
            </Button>
          )}
        </p>
      ) : (
        <p class="muted">{t("cannotGive", { why: t(p.active.signer ? "signatureUnusable" : "unsigned") })}</p>
      )}
      {rows.length > 0 && (
        <ul class="addon-grants">
          {rows.map(({ r, g }) => (
            <li key={r.id}>
              <span class="mono">{r.id}</span>
              {g && (
                <span class="mono addon-path">
                  {g.selected === g.target ? g.selected : `${g.selected} → ${g.target}`}
                </span>
              )}
              {g?.session_only && <Badge tone="warn">{t("untilQuit")}</Badge>}
              {g?.changed && <Badge tone="danger">{t("changed")}</Badge>}
              {g && g.sha256 !== p.active.sha256 && <Badge tone="warn">{t("otherVersion")}</Badge>}
              <span class="actions">
                {signer?.approved && (
                  <Button
                    disabled={act.busy}
                    onClick={() => act.run(askFirst(() => access.grant(p.id, r.id)))}
                  >
                    {g ? t("chooseAgain") : t(r.kind === "file" ? "chooseFile" : "chooseFolder")}
                  </Button>
                )}
                {g && (
                  <Button
                    variant="quiet"
                    disabled={act.busy}
                    label={`${t("removeFile")} ${r.id}`}
                    onClick={() => act.run(() => access.revoke((g as AccessGrant).id))}
                  >
                    {t("removeFile")}
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
      {signer?.approved && <p class="muted">{t("sendWarning")}</p>}
      {(grants.error ?? act.error) && <Notice tone="danger">{grants.error ?? act.error}</Notice>}
      {grants.data?.warning && <Notice tone="warn">{grants.data.warning}</Notice>}
    </div>
  );
}

/** The chosen add-on, under the list: what it is, what it may do, and what the person can do with it. */
function AddonDetail(props: { p: DevicePackage; onClose: () => void; onChanged: () => void }) {
  const { p } = props;
  const ref = useRef<HTMLElement>(null);
  useLayoutEffect(() => ref.current?.focus(), [p.id]);
  const tool = p.tier === "native";
  const usesTools = p.tier === "web" && p.active.imports.length > 0;
  const act = useAction(props.onChanged);
  const remove = useAction(() => {
    props.onClose();
    props.onChanged();
  });
  return (
    <section
      class="addon-detail"
      aria-label={p.id}
      tabIndex={-1}
      ref={ref}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !e.defaultPrevented) props.onClose();
      }}
    >
      <header class="addon-detail-head">
        <h3 class="mono">{p.id}</h3>
        <Button variant="ghost" label={t("close")} onClick={props.onClose}>
          ✕
        </Button>
      </header>
      <div class="addon-block">
        <p>
          <strong>{t(tool ? "toolKind" : "pageKind")}</strong>
        </p>
        <p class="muted">{t(tool ? "toolKindAbout" : "pageKindAbout")}</p>
        <p class="addon-line">
          {trustBadge(p)}
          <span class="muted">{t(p.trust === "curated" ? "verifiedAbout" : "unverifiedAbout")}</span>
        </p>
        <p class="muted">{sourceText(p)}</p>
        {usesTools && (
          <p class="muted">
            {t(p.trust !== "curated" ? "toolsBlocked" : p.imports_allowed ? "toolsAllowed" : "toolsAbout")}
          </p>
        )}
      </div>
      {(act.error ?? remove.error) && <Notice tone="danger">{act.error ?? remove.error}</Notice>}
      <div class="actions">
        <Button
          variant={p.enabled ? "quiet" : "primary"}
          disabled={act.busy}
          onClick={() => act.run(askFirst(() => device.setEnabled(p.id, !p.enabled)))}
        >
          {p.enabled ? t("turnOff") : t("turnOn")}
        </Button>
        {usesTools && p.trust === "curated" && (
          <Button
            disabled={act.busy}
            onClick={() => act.run(askFirst(() => device.setImports(p.id, !p.imports_allowed)))}
          >
            {p.imports_allowed ? t("removeTools") : t("allowTools")}
          </Button>
        )}
        {p.last_good && (
          <Button disabled={act.busy} onClick={() => act.run(() => device.rollback(p.id))}>
            {t("goBack", { version: p.last_good.version })}
          </Button>
        )}
        <ConfirmButton
          disabled={act.busy || remove.busy}
          onConfirm={() => remove.run(() => device.remove(p.id))}
        >
          {t("remove")}
        </ConfirmButton>
      </div>
      {p.tier === "web" && <Files p={p} />}
      <details class="page-details">
        <summary>{t("technical")}</summary>
        <Facts rows={technicalFacts(p.active, p)} />
      </details>
    </section>
  );
}

interface Offer {
  id: string;
  version: string;
  registry: string;
  refused?: string;
  installed?: DevicePackage;
}

/** What the sources offer that is not installed, or is installed from there and has a newer version. */
function Available(props: {
  registries: Loaded<DeviceRegistry[]>;
  installed: DevicePackage[];
  onInstalled: () => void;
}) {
  const { registries } = props;
  const install = useAction(props.onInstalled);
  const offers: Offer[] = [];
  for (const r of registries.data ?? []) {
    for (const p of r.packages) {
      const latest = p.versions[0]; // the one an install without a version takes
      const installed = props.installed.find((i) => i.id === p.id);
      const current = installed?.source.kind === "registry" && installed.source.name === r.name;
      if (!latest || (installed && (!current || installed.active.version === latest))) continue;
      offers.push({ id: p.id, version: latest, registry: r.name, refused: p.refused, installed });
    }
  }
  const broken = (registries.data ?? []).filter((r) => r.error);
  return (
    <Section title={t("available")}>
      <div class="addon-section-body">
        {registries.phase === "loading" && (
          <p class="muted" role="status">
            {t("checking")}
          </p>
        )}
        {registries.error && <Notice tone="danger">{registries.error}</Notice>}
        {broken.map((r) => (
          <Notice key={r.name} tone="warn">
            {t("couldNotRead", { source: r.name, reason: r.error ?? "" })}
          </Notice>
        ))}
        {install.error && <Notice tone="danger">{install.error}</Notice>}
        {registries.data?.length === 0 ? (
          <p class="muted">{t("noSources")}</p>
        ) : registries.data && offers.length === 0 ? (
          broken.length === 0 && <p class="muted">{t("allInstalled")}</p>
        ) : (
          offers.length > 0 && (
            <List<Offer>
              label={t("available")}
              items={offers}
              id={(o) => `${o.registry}:${o.id}`}
              columns={[
                { label: t("name"), value: (o) => o.id, mono: true },
                { label: t("version"), value: (o) => o.version, mono: true },
                { label: t("source"), value: (o) => o.registry },
                {
                  label: "",
                  value: (o) =>
                    o.refused ? (
                      <span class="addon-blocked">
                        <strong>{t("blocked")}</strong>
                        <span class="muted">{o.refused}</span>
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
          )
        )}
      </div>
    </Section>
  );
}

export function DevicePage(props: { onChanged: () => void }) {
  const catalog = useLoad(device.list);
  const registries = useLoad(device.registries);
  const [selected, setSelected] = useState<string>();
  const changed = () => {
    catalog.reload();
    registries.reload();
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
            <Button
              onClick={() => {
                catalog.reload();
                registries.reload();
              }}
              disabled={catalog.loading || registries.loading}
            >
              {t("refresh")}
            </Button>
            <Button
              variant="primary"
              disabled={file.busy}
              onClick={() => file.run(askFirst(device.installFile))}
            >
              {t("installFile")}
            </Button>
          </>
        }
      />
      <Intro>{t("intro")}</Intro>
      <Phases
        load={catalog}
        loading={t("reading")}
        failed={{ title: t("readFailed") }}
        isEmpty={() => false}
        empty={null}
      >
        {(c) => (
          <div class="addons">
            {c.safe_mode && <Notice tone="warn">{t("safeMode")}</Notice>}
            {c.warning && <Notice tone="warn">{c.warning}</Notice>}
            {file.error && <Notice tone="danger">{file.error}</Notice>}
            <Section title={t("installed")}>
              <div class="addon-section-body">
                {list.length === 0 ? (
                  <Empty>{t("none")}</Empty>
                ) : (
                  <>
                    <p class="muted">{t("choose")}</p>
                    <List<DevicePackage>
                      label={t("installed")}
                      items={list}
                      id={(p) => p.id}
                      selected={selected}
                      onSelect={setSelected}
                      columns={[
                        { label: t("name"), value: (p) => p.id, mono: true },
                        { label: t("version"), value: (p) => p.active.version, mono: true },
                        { label: t("trust"), value: trustBadge },
                        { label: t("state"), value: stateBadge },
                      ]}
                    />
                  </>
                )}
                {chosen && (
                  <AddonDetail
                    key={`${chosen.id} ${chosen.active.sha256} ${chosen.enabled} ${chosen.imports_allowed} ${chosen.active.signer?.approved}`}
                    p={chosen}
                    onClose={() => setSelected(undefined)}
                    onChanged={changed}
                  />
                )}
              </div>
            </Section>
            <Available registries={registries} installed={list} onInstalled={changed} />
          </div>
        )}
      </Phases>
    </>
  );
}

export const devicePage = (onChanged: () => void): PageDef => ({
  id: "device",
  dest: "settings",
  kind: "topic",
  section: "device",
  title: t("title"),
  tip: t("tip"),
  render: () => <DevicePage onChanged={onChanged} />,
});
