// The app's OpenCode page: Agent Configuration (Apply, Clear, Restore Backup), Start CLI and Start
// App for OpenCode, through the optional curated native tool pluxy/opencode
// (next/app-tools/opencode) and the launcher. The page is the window's own, not an extension page,
// because Apply takes the client key: the window names a vault secret's ID and the native
// confirmation of the apply names its label; an extension page can pass no secret. The key's value
// never reaches this webview: native key entry keeps it, and the tool's preview reads back only
// whether one is set. Labels follow the old app's Agents page.

import { useEffect, useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import {
  Button,
  Card,
  Choices,
  ConfirmButton,
  Facts,
  Notice,
  PageHead,
  Switch,
  useAction,
  useKit,
  useLoad,
} from "../../ui/kit/ui";
import { Backups } from "./backups";
import { folderWait, NeedsKey, NoKeys, toolMissing, withDeviceLink } from "./clientHelp";
import { keysOf, type Secret, Text, usePreview } from "./clientPage";
import type { AgentPage } from "./easyMode";
import { connectionGet, reply } from "./host";
import {
  attachmentDefault,
  CLIENT,
  type Model,
  PACKAGE,
  type ReadBack,
  ready,
  withModels,
} from "./opencodeForm";
import { keptTerminal, launcherProblem, useDeviceChanged } from "./startCli";

const t = strings("opencode", {
  title: "OpenCode",
  short: "OpenCode",
  tip: "Set up and start OpenCode with this core",
  intro:
    "Adds this core to OpenCode's config as the pluxy provider (OpenAI-compatible), with a client key, the models OpenCode lists and its default model. Apply, Clear and Restore Backup each ask in a native confirmation; comments and everything else in the file stay. Restart OpenCode afterward.",
  unavailable:
    "Install and enable the curated OpenCode tool (pluxy/opencode) on the {device} page to set up OpenCode from here.",
  file: "Configuration file",
  fileDefault: "OpenCode's global config",
  fileDefaultNote: "~/.config/opencode/opencode.jsonc, or opencode.json when only that exists",
  fileSelected: "A selected file",
  fileInRoot: "A file in a selected folder",
  filePath: "File",
  filePathHint:
    "A full path, for example the file OPENCODE_CONFIG names; a link (as stow makes) is followed and kept",
  root: "Folder",
  rootHint: "A full path, for example OPENCODE_CONFIG_DIR; the folder may be a link (as chezmoi can make)",
  inRoot: "File in the folder",
  read: "Read configuration",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  currentModel: "Default model",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not Configured",
  configured: "Configured",
  needsUpdate: "Configuration Modified",
  oldProvider:
    "This file still has the old desktop app's cpa-gui provider. Apply leaves it alone; remove it from the file if you no longer use it.",
  models: "Models",
  modelsHint:
    "The models OpenCode lists under the pluxy provider. Attachments lets OpenCode send images and PDFs to a model.",
  importModels: "Import models from core",
  noModels: "No models yet: import them from the core or add one.",
  modelId: "Model",
  modelName: "Display name",
  attachments: "Attachments",
  remove: "Remove",
  addModel: "Add model",
  add: "Add",
  useModel: "Default model",
  useModelHint: "The model OpenCode starts with; one of the models above.",
  key: "Authentication Key",
  keyHint: "The client API key OpenCode uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "OpenCode client key",
  connect: "Connect in One Click",
  repair: "Repair Connection",
  update: "Update Configuration",
  clear: "Close configuration",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put the file back as it was before the first apply.",
  noCore: "Connect to a core first: its address goes into the config.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Command-line program path",
  overrideHint:
    "A full path used instead of ~/.opencode/bin/opencode or opencode found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start OpenCode",
  app: "Start App",
  appPath: "Desktop program path",
  appHint:
    "A full path used instead of /opt/OpenCode/ai.opencode (or ai.opencode.desktop, from OpenCode 1), where OpenCode's .deb and .rpm install it; for example an AppImage (optional)",
  launchApp: "Start OpenCode Desktop",
  restartNote: "To apply a new configuration to a running OpenCode, quit it and start it again.",
  refused: "OpenCode would not take this file",
  refusedHint:
    "Apply and Close configuration leave it alone until it is fixed; Restore Backup puts back the file as it was before the first apply.",
  v2Provider:
    "This file has providers.pluxy, OpenCode 2's own form of the provider, and OpenCode uses it instead of what Apply writes. Remove it from the file to apply here.",
  invalid:
    "List at least one model (IDs without # or control characters, at most 240 characters) and pick the default.",
});

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Preview {
  /** The pinned file: passed as is to apply, clear and restore. */
  targets: { selected: string; target: string; link: string | null; [k: string]: unknown }[];
  applied: boolean;
  config: ReadBack;
}

/** Start CLI and Start App, through the launcher. */
function Launch(props: { available: boolean }) {
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const [appPath, setAppPath] = useState("");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  useDeviceChanged(terminals.reload);
  const action = useAction(state.reload);
  const recent = state.data?.history[CLIENT] ?? [];
  const shownTerminal = keptTerminal(terminal, terminals.data?.terminals);
  const go = (target: "cli" | "app", extra: Record<string, unknown>) =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", { target });
      if (!recipe.body) return recipe;
      return reply("launcher_launch", { launch: { ...recipe.body, ...extra } });
    });
  return (
    <>
      <Card title={t("start")}>
        <Text label={t("cwd")} hint={folderWait(cwd)} value={cwd} onInput={setCwd} />
        {recent.length > 0 && (
          <Choices
            label={t("recent")}
            value={recent.includes(cwd) ? cwd : undefined}
            choices={recent.map((r) => ({ value: r, label: r }))}
            onChange={setCwd}
          />
        )}
        <Text label={t("override")} hint={t("overrideHint")} value={override} onInput={setOverride} />
        {terminals.data && (
          <Choices
            label={t("terminal")}
            value={shownTerminal}
            choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
            onChange={setTerminal}
          />
        )}
        {terminals.error && <Notice tone="warn">{launcherProblem(terminals.error)}</Notice>}
        <Button
          variant="primary"
          onClick={() =>
            go("cli", {
              cwd: cwd.trim(),
              terminal: shownTerminal,
              ...(override.trim() ? { override: override.trim() } : {}),
            })
          }
          disabled={action.busy || !props.available || !cwd.trim()}
        >
          {t("launch")}
        </Button>
      </Card>
      <Card title={t("app")}>
        <Text label={t("appPath")} hint={t("appHint")} value={appPath} onInput={setAppPath} />
        <Button
          onClick={() => go("app", appPath.trim() ? { override: appPath.trim() } : {})}
          disabled={action.busy || !props.available}
        >
          {t("launchApp")}
        </Button>
        <p class="muted">{t("restartNote")}</p>
      </Card>
      {action.error && <Notice tone="danger">{launcherProblem(action.error)}</Notice>}
    </>
  );
}

export function OpenCodePage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("opencode.jsonc");
  const [models, setModels] = useState<Model[]>([]);
  const [model, setModel] = useState("");
  const [newModel, setNewModel] = useState("");
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  const keys = keysOf(PACKAGE, secrets.data);
  useEffect(() => {
    connectionGet().then(
      (c) => setCore(c.signed_in ? c.target : undefined),
      () => setCore(undefined),
    );
  }, []);
  const selection = () =>
    where === "file"
      ? { targets: [{ file: file.trim() }] }
      : where === "root"
        ? { targets: [{ root: root.trim(), path: inRoot.trim() }] }
        : {};
  // A write acts on the file the last preview pinned (the tool refuses it if the file changed
  // since); the preview belongs to the selection it was read for, so a write never pins a file unread.
  const selected = JSON.stringify(selection());
  const { preview, readFor } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await readFor(selected, () =>
        tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) }),
      );
      if (r.body) {
        const listed = r.body.config.models ?? [];
        setModels(listed);
        setModel(r.body.config.model ?? listed[0]?.id ?? "");
      }
      return r;
    });
  const write = useAction(load);
  const addModels = (found: Model[]) => setModels((have) => withModels(have, found));
  const importModels = useAction(() => {});
  const doImport = () =>
    importModels.run(async () => {
      const served = await api.servedModels();
      if (served.body)
        addModels(
          served.body.models.map((m) => ({
            id: m.id,
            name: m.display_name ?? "",
            attachment: attachmentDefault(m.id),
          })),
        );
      return served;
    });
  const apply = () =>
    write.run(() =>
      tool("apply", {
        targets: preview?.targets,
        base_url: core,
        key: secret,
        model,
        models: models.map((m) => ({ id: m.id.trim(), name: m.name.trim(), attachment: m.attachment })),
      }),
    );
  const undo = (op: "clear" | "restore") => write.run(() => tool(op, { targets: preview?.targets }));
  const addKey = useAction(secrets.reload);
  const st = preview?.config;
  const refused = st?.state === "refused";
  const valid = ready(models, model);
  const applyLabel = !preview?.applied
    ? t("connect")
    : st?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy;
  const setRow = (i: number, patch: Partial<Model>) =>
    setModels(models.map((m, j) => (j === i ? { ...m, ...patch } : m)));

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{withDeviceLink(t("unavailable"))}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <Card title={t("file")}>
        <Choices
          label={t("file")}
          value={where}
          choices={[
            { value: "default", label: t("fileDefault"), note: t("fileDefaultNote") },
            { value: "file", label: t("fileSelected") },
            { value: "root", label: t("fileInRoot") },
          ]}
          onChange={setWhere}
        />
        {where === "file" && (
          <Text label={t("filePath")} hint={t("filePathHint")} value={file} onInput={setFile} />
        )}
        {where === "root" && (
          <>
            <Text label={t("root")} hint={t("rootHint")} value={root} onInput={setRoot} />
            <Text label={t("inRoot")} value={inRoot} onInput={setInRoot} />
          </>
        )}
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && st && (
          <Facts
            rows={[
              [
                t("status"),
                refused
                  ? t("refused")
                  : preview.applied
                    ? t("applied")
                    : st.state === "configured"
                      ? t("configured")
                      : st.state === "needs-update"
                        ? t("needsUpdate")
                        : t("notConfigured"),
              ],
              [t("target"), preview.targets[0]?.target ?? ""],
              ...(refused
                ? []
                : ([
                    [t("keySet"), st.key_set ? t("yes") : t("no")],
                    [t("currentModel"), st.default_model ?? ""],
                  ] as [string, string][])),
            ]}
          />
        )}
        {refused && (
          <Notice tone="warn">
            {st?.reason} {t("refusedHint")}
          </Notice>
        )}
        {st?.old_provider && <Notice tone="warn">{t("oldProvider")}</Notice>}
        {st?.v2_provider && <Notice tone="warn">{t("v2Provider")}</Notice>}
      </Card>
      {preview && !refused && (
        <>
          <Card title={t("models")}>
            <p class="muted">{t("modelsHint")}</p>
            <Button onClick={doImport} disabled={busy || importModels.busy || !core}>
              {t("importModels")}
            </Button>
            {importModels.error && <Notice tone="danger">{importModels.error}</Notice>}
            {models.length === 0 && <p class="muted">{t("noModels")}</p>}
            {models.map((m, i) => (
              <div key={m.id} class="row">
                <span class="label">{m.id}</span>
                <Text label={t("modelName")} value={m.name} onInput={(v) => setRow(i, { name: v })} />
                <Switch
                  label={t("attachments")}
                  checked={m.attachment}
                  onChange={(on) => setRow(i, { attachment: on })}
                />
                <Button onClick={() => setModels(models.filter((_, j) => j !== i))}>{t("remove")}</Button>
              </div>
            ))}
            <div class="row">
              <Text label={t("addModel")} value={newModel} onInput={setNewModel} />
              <Button
                disabled={!newModel.trim()}
                onClick={() => {
                  const id = newModel.trim();
                  addModels([{ id, name: "", attachment: attachmentDefault(id) }]);
                  setNewModel("");
                }}
              >
                {t("add")}
              </Button>
            </div>
            {models.length > 0 && (
              <Choices
                label={t("useModel")}
                value={models.some((m) => m.id === model) ? model : undefined}
                choices={models.map((m) => ({ value: m.id, label: m.name.trim() || m.id }))}
                onChange={setModel}
              />
            )}
            <small class="hint">{t("useModelHint")}</small>
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
      {preview && (
        <Card>
          {!refused && !valid && <Notice tone="warn">{t("invalid")}</Notice>}
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button
            variant="primary"
            onClick={apply}
            disabled={busy || refused || st?.v2_provider || !valid || !secret || !core}
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
      <Launch available={!unavailable} />
    </div>
  );
}

export const opencodePage = (): AgentPage => ({
  id: "opencode",
  package: PACKAGE,
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <OpenCodePage />,
});
