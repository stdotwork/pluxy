// The app's Oh My Pi page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Oh My Pi, through the optional curated native tool pluxy/omp-setup (next/app-tools/omp-setup)
// and the launcher. The page is the window's own, not an extension page, because Apply takes the
// client key: the window names a vault secret's ID and the native confirmation of the apply names
// its label; an extension page can pass no secret. The key's value never reaches this webview:
// native key entry keeps it, and the tool's preview reads back only whether one is set. Labels
// follow the old app's Agents page.

import { useEffect, useId, useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import {
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
import { CLIENT, modelOf, PACKAGE, type ReadBack, validModel } from "./ompForm";

const t = strings("omp", {
  title: "Oh My Pi",
  short: "Oh My Pi",
  tip: "Set up and start Oh My Pi with this core",
  intro:
    "Adds this core to Oh My Pi's models.yml with a client key and default model. Apply, Clear, and Restore backup ask for confirmation and keep other settings. Restart Oh My Pi afterward.",
  unavailable:
    "Install and enable the curated Oh My Pi tool (pluxy/omp-setup) on the {device} page to set up Oh My Pi from here.",
  files: "Configuration files",
  fileDefault: "Oh My Pi's agent folder",
  fileDefaultNote: "~/.omp/agent",
  fileFolder: "A selected agent folder",
  folder: "Folder",
  folderHint:
    "A full path, such as a profile's ~/.omp/profiles/<name>/agent; the folder may be a link (as chezmoi can make)",
  fileSelected: "Selected files",
  modelsFile: "Models file",
  settingsFile: "Settings file",
  filesHint: "A full path; a link (as stow makes) is followed and kept",
  read: "Refresh",
  status: "Status",
  applied: "Applied",
  notConfigured: "Not configured",
  configured: "Configured",
  needsUpdate: "Configuration modified",
  keySet: "Key in the file",
  yes: "Yes",
  no: "No",
  defaultRole: "Default model role",
  none: "None",
  legacy:
    "models.yml still has the easy-cliproxyapi provider an older app wrote. pluxy leaves it as it is; remove it in Oh My Pi's models.yml if you no longer use it.",
  model: "Model",
  modelHint:
    "The default model Oh My Pi starts with, through this core. Oh My Pi lists the core's other models itself.",
  modelLabel: "Default model",
  refresh: "Refresh models",
  invalidModel: "Enter one model ID, without spaces or commas.",
  refused: "Oh My Pi would not take these files",
  refusedHint: "Apply and Clear leave these files unchanged until you fix them; Restore backup reverts them.",
  key: "Client key",
  keyHint: "The client key Oh My Pi uses to reach this core.",
  addKey: "Add key…",
  keyLabel: "Oh My Pi client key",
  connect: "Apply",
  repair: "Repair",
  update: "Update",
  clear: "Clear",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart Oh My Pi afterward.",
  restore: "Restore backup",
  restoreHint: "Put both files back as they were before the first apply.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working folder",
  recent: "Recently used",
  override: "Agent program path",
  overrideHint: "A full path used instead of omp (optional)",
  terminal: "Terminal",
  launch: "Start Oh My Pi",
});

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Preview {
  /** The pinned files: passed as they are to apply, clear and restore. */
  targets: { selected: string; target: string; link: string | null; [k: string]: unknown }[];
  applied: boolean;
  config: ReadBack;
}

export function OmpPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "folder" | "files">("default");
  const [folder, setFolder] = useState("");
  const [modelsFile, setModelsFile] = useState("");
  const [settingsFile, setSettingsFile] = useState("");
  const [model, setModel] = useState("");
  const [served, setServed] = useState<string[]>([]);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  const keys = keysOf(PACKAGE, secrets.data);
  const listId = useId();
  useEffect(() => {
    connectionGet().then(
      (c) => setCore(c.signed_in ? c.target : undefined),
      () => setCore(undefined),
    );
  }, []);
  const selection = () =>
    where === "folder"
      ? { folder: folder.trim() }
      : where === "files"
        ? { targets: [{ file: modelsFile.trim() }, { file: settingsFile.trim() }] }
        : {};
  // A write acts on the files the last preview pinned (the tool refuses them if one changed
  // since); the preview belongs to the selection it was read for, so a write never pins files unread.
  const selected = JSON.stringify(selection());
  const { preview, accept } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) });
      if (r.body && accept(selected, r.body)) {
        setModel(modelOf(r.body.config));
      }
      return r;
    });
  const write = useAction(load);
  const fetchModels = useAction(() => {});
  const refresh = () =>
    fetchModels.run(async () => {
      const r = await api.servedModels();
      if (r.body) setServed(r.body.models.map((m) => m.id));
      return r;
    });
  const apply = () =>
    write.run(() =>
      tool("apply", { targets: preview?.targets, base_url: core, key: secret, model: model.trim() }),
    );
  const undo = (op: "clear" | "restore") => write.run(() => tool(op, { targets: preview?.targets }));
  const addKey = useAction(secrets.reload);
  const cfg = preview?.config;
  const refused = cfg?.state === "refused";
  const applyLabel = !preview?.applied
    ? t("connect")
    : cfg?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy;

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
            { value: "files", label: t("fileSelected") },
          ]}
          onChange={setWhere}
        />
        {where === "folder" && (
          <Text label={t("folder")} hint={t("folderHint")} value={folder} onInput={setFolder} />
        )}
        {where === "files" && (
          <>
            <Text label={t("modelsFile")} hint={t("filesHint")} value={modelsFile} onInput={setModelsFile} />
            <Text
              label={t("settingsFile")}
              hint={t("filesHint")}
              value={settingsFile}
              onInput={setSettingsFile}
            />
          </>
        )}
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && cfg && (
          <Facts
            rows={[
              [
                t("status"),
                refused
                  ? t("refused")
                  : preview.applied
                    ? t("applied")
                    : cfg.state === "configured"
                      ? t("configured")
                      : cfg.state === "needs-update"
                        ? t("needsUpdate")
                        : t("notConfigured"),
              ],
              [t("modelsFile"), preview.targets[0]?.target ?? ""],
              [t("settingsFile"), preview.targets[1]?.target ?? ""],
              ...(refused
                ? []
                : ([
                    [t("keySet"), cfg.key_set ? t("yes") : t("no")],
                    [t("defaultRole"), cfg.default_role ?? t("none")],
                  ] as [string, string][])),
            ]}
          />
        )}
        {refused && (
          <Notice tone="warn">
            {cfg?.reason} {t("refusedHint")}
          </Notice>
        )}
        {cfg?.legacy && <Notice tone="warn">{t("legacy")}</Notice>}
      </Card>
      {preview && !refused && (
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
              <NoKeys />
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
      {preview && (
        <Card>
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button
            variant="primary"
            onClick={apply}
            disabled={busy || refused || !validModel(model.trim()) || !secret || !core}
          >
            {applyLabel}
          </Button>
          <NeedsKey missing={!secret} />
          <ConfirmButton disabled={busy || refused || !preview.applied} onConfirm={() => undo("clear")}>
            {t("clear")}
          </ConfirmButton>
          <small class="hint">{t("clearHint")}</small>
          <ConfirmButton disabled={busy} onConfirm={() => undo("restore")}>
            {t("restore")}
          </ConfirmButton>
          <small class="hint">{t("restoreHint")}</small>
        </Card>
      )}
      {preview && (
        <Backups pkg={PACKAGE} pins={async () => preview.targets} disabled={busy} onChange={load} />
      )}
      <StartCli
        available={!unavailable}
        client={CLIENT}
        recipe={() => tool<Record<string, unknown>>("recipe", {})}
        t={t}
      />
    </div>
  );
}

export const ompPage = (): AgentPage => ({
  id: "omp",
  package: PACKAGE,
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <OmpPage />,
});
