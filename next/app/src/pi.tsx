// The app's Pi page: Agent Configuration (Install, Update, Uninstall, Clear, Restore Backup) and
// Start CLI for Pi, through the optional curated native tool pluxy/pi-setup
// (next/app-tools/pi-setup) and the launcher. The page is the window's own, not an extension page,
// because Install and Apply take the client key: the window names a vault secret's ID and the
// native confirmation of the apply names its label; an extension page can pass no secret. The
// key's value never reaches this webview: native key entry keeps it, and the tool's preview reads
// back only whether one is set. Labels follow the old app's Agents page.

import { useEffect, useId, useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import {
  Badge,
  Button,
  Card,
  Choices,
  ConfirmButton,
  Facts,
  Notice,
  PageHead,
  useAction,
  useKit,
  useLoad,
} from "../../ui/kit/ui";
import { Backups } from "./backups";
import { NeedsKey, NoKeys, toolMissing, withDeviceLink } from "./clientHelp";
import { keysOf, type Secret, StartCli, Text, usePreview } from "./clientPage";
import type { AgentPage } from "./easyMode";
import { connectionGet, reply } from "./host";
import { selectionFor, type Where } from "./piForm";

const t = strings("pi", {
  title: "Pi",
  tip: "Set up and start Pi with this core",
  intro:
    "Installs pluxy's Pi plugin, writes core's address and a client key to cliproxyapi.json, and sets Pi's default model. All write actions ask for confirmation and keep other settings. Restart Pi afterward.",
  unavailable:
    "Install and enable the curated Pi tool (pluxy/pi-setup) on the {device} page to set up Pi from here.",
  files: "Configuration files",
  fileDefault: "Pi's agent folder",
  fileDefaultNote: "~/.pi/agent",
  fileFolder: "A selected agent folder",
  fileLinks: "Linked files in a folder",
  linksHint:
    "A full path to the folder holding cliproxyapi.json and settings.json as links (as stow makes); each link is followed and kept",
  folder: "Folder",
  folderHint:
    "A full path, such as the folder PI_CODING_AGENT_DIR names; the folder may be a link (as chezmoi can make)",
  read: "Refresh",
  status: "Status",
  applied: "Applied",
  notConfigured: "Not configured",
  configured: "Configured",
  needsUpdate: "Configuration modified",
  installation: "Installation status",
  pluginInstalled: "Pi plugin installed",
  providerNotInstalled: "Pi plugin not installed",
  pluginVersion: "Plugin version",
  notAvailable: "Not available",
  updateAvailable: "Update available",
  updateTitle: "Version {bundled} is available",
  configFile: "cliproxyapi.json",
  settingsFile: "settings.json",
  cacheFile: "cliproxyapi-models.json",
  keySet: "Key in the file",
  yes: "Yes",
  no: "No",
  defaultProvider: "Default provider",
  defaultModel: "Default model",
  none: "None",
  legacy:
    "settings.json lists the CLIProxyAPI provider an older app installed from npm. Apply replaces it with pluxy's reviewed copy; Clear and Restore backup put it back.",
  foreign:
    "The agent folder has a pluxy-cliproxyapi-provider folder pluxy did not install: move it away to install pluxy's Pi plugin.",
  model: "Model",
  modelHint:
    "The model Pi starts with, through this core. The plugin lists the core's Codex catalog models in Pi itself.",
  modelLabel: "Model",
  refresh: "Refresh models",
  invalidModel: "Enter one model ID, without spaces, of at most 240 characters.",
  key: "Client key",
  keyHint: "The client key Pi uses to reach this core.",
  addKey: "Add key…",
  keyLabel: "Pi client key",
  install: "Install plugin",
  connect: "Apply",
  repair: "Repair",
  update: "Update",
  clear: "Clear",
  clearHint:
    "Remove pluxy's integration settings and the plugin, and keep other settings. Restart Pi afterward.",
  restore: "Restore backup",
  refused: "Pi would not take these files",
  refusedHint:
    "Apply, Clear, and Uninstall plugin leave these files unchanged until you fix them; Restore backup reverts them.",
  restoreHint: "Put the three files back as they were before the first apply.",
  plugin: "Plugin management",
  installedVersion: "Installed plugin version: {version}",
  pluginNotInstalled: "The Pi plugin is not installed. Choose a model above and press Install plugin.",
  updatePlugin: "Update plugin",
  updated: "The Pi plugin is updated.",
  uninstallPlugin: "Uninstall plugin",
  uninstalled: "The Pi plugin is uninstalled.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working folder",
  recent: "Recently used",
  override: "Agent program path",
  overrideHint: "A full path used instead of pi (optional)",
  terminal: "Terminal",
  launch: "Start Pi",
});

const PACKAGE = "pluxy/pi-setup";

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface PiAdapter {
  installed: boolean;
  version: string | null;
  bundled: string;
  update: boolean;
  foreign: boolean;
}

interface PiConfig {
  /** refused: Pi would not take a file (reason says why); the other fields are then absent. */
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
  provider?: string;
  default_provider?: string | null;
  model?: string | null;
  registered?: boolean;
  legacy?: boolean;
  adapter: PiAdapter;
}

interface Preview {
  targets: { selected: string; path?: string | null; target: string; link: string | null }[];
  applied: boolean;
  pi: PiConfig;
}

/** A model ID the plugin takes: non-empty, without whitespace, at most 240 characters. */
const validModel = (m: string) => m.length > 0 && !/\s/.test(m) && [...m].length <= 240;

const refused = {
  status: 400,
  error: { code: "refused", message: "the configuration files could not be read" },
};

export function PiPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<Where>("default");
  const [folder, setFolder] = useState("");
  const [model, setModel] = useState("");
  const [served, setServed] = useState<string[]>([]);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  // Keys the person entered, and client keys made for this tool; never another tool's client key.
  const keys = keysOf(PACKAGE, secrets.data);
  const listId = useId();
  useEffect(() => {
    connectionGet().then(
      (c) => setCore(c.signed_in ? c.target : undefined),
      () => setCore(undefined),
    );
  }, []);
  const selection = () => selectionFor(where, folder);
  const selected = JSON.stringify(selection());
  const { preview, readFor } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await readFor(selected, () =>
        tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) }),
      );
      if (r.body) {
        setModel(r.body.pi.model ?? "");
      }
      return r;
    });
  const write = useAction(load);
  // The plugin's own actions: their errors show in its card, not beside the apply buttons.
  const plugin = useAction(load);
  // A write acts on the files of the preview the person read: its pins carry the preview-to-write
  // check. Changing where the files are drops that preview, so it must be read again.
  const pins = () => preview?.targets;
  // The agent folder the last preview found: a selected folder as given, else the folder of the
  // default files.
  const agentFolder = (): string | undefined => {
    const first = preview?.targets[0];
    if (!first) return undefined;
    return first.path ? first.selected : first.selected.slice(0, first.selected.lastIndexOf("/"));
  };
  const fetchModels = useAction(() => {});
  const refresh = () =>
    fetchModels.run(async () => {
      const r = await api.servedModels();
      if (r.body) setServed(r.body.models.map((m) => m.id));
      return r;
    });
  const apply = () =>
    write.run(async () => {
      setNotice(undefined);
      const targets = pins();
      if (!targets) return refused;
      return tool("apply", { targets, base_url: core, key: secret, model: model.trim() });
    });
  const undo = (op: "clear" | "restore") =>
    write.run(async () => {
      setNotice(undefined);
      const targets = pins();
      if (!targets) return refused;
      return tool(op, { targets });
    });
  const updatePlugin = () =>
    plugin.run(async () => {
      setNotice(undefined);
      const dir = agentFolder();
      if (!dir) return refused;
      const r = await tool("update", { folder: dir });
      if (r.body) setNotice(t("updated"));
      return r;
    });
  const uninstallPlugin = () =>
    plugin.run(async () => {
      setNotice(undefined);
      const targets = pins();
      if (!targets) return refused;
      const r = await tool("uninstall", { targets });
      if (r.body) setNotice(t("uninstalled"));
      return r;
    });
  const addKey = useAction(secrets.reload);
  const cfg = preview?.pi;
  const adapter = cfg?.adapter;
  const refusedFiles = cfg?.state === "refused";
  const applyLabel = !adapter?.installed
    ? t("install")
    : !preview?.applied
      ? t("connect")
      : cfg?.state === "configured"
        ? t("update")
        : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || plugin.busy;

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{withDeviceLink(t("unavailable"))}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <Card title={t("files")}>
        <Choices
          label={t("files")}
          value={where}
          choices={[
            { value: "default", label: t("fileDefault"), note: t("fileDefaultNote") },
            { value: "folder", label: t("fileFolder") },
            { value: "links", label: t("fileLinks") },
          ]}
          onChange={setWhere}
        />
        {where !== "default" && (
          <Text
            label={t("folder")}
            hint={where === "links" ? t("linksHint") : t("folderHint")}
            value={folder}
            onInput={setFolder}
          />
        )}
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && cfg && adapter && (
          <Facts
            rows={[
              [t("installation"), adapter.installed ? t("pluginInstalled") : t("providerNotInstalled")],
              [
                t("pluginVersion"),
                <>
                  {adapter.version ?? t("notAvailable")}
                  {adapter.update && (
                    <>
                      {" "}
                      <span title={t("updateTitle", { bundled: adapter.bundled })}>
                        <Badge tone="warn">{t("updateAvailable")}</Badge>
                      </span>
                    </>
                  )}
                </>,
              ],
              [
                t("status"),
                refusedFiles
                  ? t("refused")
                  : preview.applied
                    ? t("applied")
                    : cfg.state === "configured"
                      ? t("configured")
                      : cfg.state === "needs-update"
                        ? t("needsUpdate")
                        : t("notConfigured"),
              ],
              [t("configFile"), preview.targets[0]?.target ?? ""],
              [t("settingsFile"), preview.targets[1]?.target ?? ""],
              [t("cacheFile"), preview.targets[2]?.target ?? ""],
              ...(refusedFiles
                ? []
                : ([
                    [t("keySet"), cfg.key_set ? t("yes") : t("no")],
                    [t("defaultProvider"), cfg.default_provider ?? t("none")],
                    [t("defaultModel"), cfg.model ?? t("none")],
                  ] as [string, string][])),
            ]}
          />
        )}
        {refusedFiles && (
          <Notice tone="warn">
            {cfg?.reason} {t("refusedHint")}
          </Notice>
        )}
        {cfg?.legacy && <Notice tone="warn">{t("legacy")}</Notice>}
      </Card>
      {preview && cfg && adapter && (
        <>
          {!refusedFiles && (
            <>
              <Card title={t("model")}>
                <p class="muted">{t("modelHint")}</p>
                <datalist id={listId}>
                  {served.map((m) => (
                    <option key={m} value={m} />
                  ))}
                </datalist>
                <Text label={t("modelLabel")} value={model} list={listId} onInput={setModel} />
                {!validModel(model.trim()) && <Notice tone="warn">{t("invalidModel")}</Notice>}
                {fetchModels.error && <Notice tone="danger">{fetchModels.error}</Notice>}
                <Button onClick={refresh} disabled={fetchModels.busy || busy}>
                  {t("refresh")}
                </Button>
              </Card>
              <Card title={t("key")}>
                <p class="muted">{t("keyHint")}</p>
                {secrets.error && <Notice tone="danger">{secrets.error}</Notice>}
                {keys.length > 0 ? (
                  <Choices
                    label={t("key")}
                    value={secret || undefined}
                    choices={keys.map((s) => ({ value: s.id, label: s.label }))}
                    onChange={setSecret}
                  />
                ) : (
                  <NoKeys secrets={secrets} />
                )}
                {addKey.error && <Notice tone="danger">{addKey.error}</Notice>}
                <Button
                  disabled={addKey.busy}
                  onClick={() =>
                    addKey.run(async () => {
                      const r = await reply<{ id: string }>("vault_secret_add", { label: t("keyLabel") });
                      if (r.body) setSecret(r.body.id);
                      return r;
                    })
                  }
                >
                  {t("addKey")}
                </Button>
              </Card>
            </>
          )}
          <Card>
            {write.error && <Notice tone="danger">{write.error}</Notice>}
            <Button
              variant="primary"
              onClick={apply}
              disabled={busy || refusedFiles || !validModel(model.trim()) || !secret || !core}
            >
              {applyLabel}
            </Button>
            <NeedsKey missing={!secret} />
            <ConfirmButton
              disabled={busy || refusedFiles || !preview.applied}
              onConfirm={() => undo("clear")}
            >
              {t("clear")}
            </ConfirmButton>
            <small class="hint">{t("clearHint")}</small>
            <ConfirmButton disabled={busy} onConfirm={() => undo("restore")}>
              {t("restore")}
            </ConfirmButton>
            <small class="hint">{t("restoreHint")}</small>
          </Card>
          <Card title={t("plugin")}>
            <p class="muted">
              {adapter.installed
                ? t("installedVersion", { version: adapter.version ?? t("notAvailable") })
                : t("pluginNotInstalled")}
            </p>
            {adapter.foreign && <Notice tone="warn">{t("foreign")}</Notice>}
            {notice && <Notice tone="ok">{notice}</Notice>}
            {plugin.error && <Notice tone="danger">{plugin.error}</Notice>}
            <Button onClick={updatePlugin} disabled={busy || !adapter.installed}>
              {t("updatePlugin")}
            </Button>
            <ConfirmButton
              disabled={busy || refusedFiles || !(adapter.installed || cfg.registered)}
              onConfirm={uninstallPlugin}
            >
              {t("uninstallPlugin")}
            </ConfirmButton>
          </Card>
          <Backups pkg={PACKAGE} pins={async () => preview.targets} disabled={busy} onChange={load} />
        </>
      )}
      <StartCli
        available={!unavailable}
        client={t("title")}
        recipe={() => tool<Record<string, unknown>>("recipe", {})}
        t={t}
      />
    </div>
  );
}

export const piPage = (): AgentPage => ({
  id: "pi",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <PiPage />,
});
