// The app's OpenCode page, on the shared tool-page parts (clientPage.tsx): how OpenCode stands and
// Connect in the head, its models (the list OpenCode shows under the pluxy provider, imported from
// pluxy on request and then edited, and the default model), Start (the command line and OpenCode
// Desktop), and the settings file, key and backups, through the optional add-on pluxy/opencode
// (next/app-tools/opencode) and the launcher. The key's value never reaches this webview.

import { useId, useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { Button, ConfirmButton, Notice, PageHead, Switch, useAction, useKit } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  FileChoice,
  FileKeyBackups,
  fileDefault,
  ModelList,
  SaveButton,
  Section,
  StartSection,
  servedNames,
  Text,
  type ToolPreview,
  ToolTop,
  Unserved,
  useStart,
  useToolSetup,
} from "./clientPage";
import type { AgentPage } from "./easyMode";
import { PathText } from "./environmentBlock";
import {
  attachmentDefault,
  CLIENT,
  type Held,
  heldOf,
  MODELS_MAX,
  type Model,
  PACKAGE,
  type ReadBack,
  withModels,
} from "./opencodeForm";
import { plainReply, useShown } from "./toolWords";

const t = strings("opencode", {
  title: "OpenCode",
  tip: "Connect OpenCode to pluxy and start it",
  fileOne: "A file",
  fileRoot: "In a folder",
  filePath: "File",
  filePathHint:
    "A full path, such as the file OPENCODE_CONFIG names; a link (as stow makes) is followed and kept.",
  root: "Folder",
  rootHint: "A full path, such as OPENCODE_CONFIG_DIR; the folder may be a link (as chezmoi can make).",
  inRoot: "File in the folder",
  oldProvider:
    "This file still has the old desktop app's cpa-gui provider. pluxy leaves it as it is; remove it from the file if you no longer use it.",
  v2Provider:
    "This file has providers.pluxy, the form OpenCode 2 writes itself, and OpenCode uses it instead of what pluxy writes. Remove it from the file, then press Read this file.",
  models: "Models",
  list: "Model list",
  listNote:
    "The models OpenCode lists under pluxy. Attachments lets OpenCode send images and PDFs to a model.",
  count: { one: "{count} model", other: "{count} models" },
  importModels: "Import models from pluxy",
  empty: "No models yet. Import models from pluxy, or add one below.",
  displayName: "Display name",
  attachments: "Attachments",
  removeModel: "Remove model",
  removeAsk: "Remove {model} from the list?",
  addModel: "Add model",
  addModelNote: "A model ID, as pluxy serves it.",
  modelId: "Model ID",
  add: "Add",
  model: "Default model",
  modelNote: "The model OpenCode starts with, one from the list.",
  chooseModel: "Choose a model",
  heldNone: "Import models from pluxy or add one, then choose a default model.",
  heldTooMany: "OpenCode takes at most {max} models from here: remove some.",
  heldText:
    "Model IDs cannot be empty or hold # or control characters, names cannot hold control characters, and IDs and names are at most 240 characters.",
  heldTwice: "A model is listed twice: remove one.",
  heldModel: "Choose a default model from the list.",
  heldV2: "Connect waits until providers.pluxy is out of the file.",
  app: "OpenCode Desktop",
  appNote:
    "Empty: /opt/OpenCode/ai.opencode, where OpenCode's .deb and .rpm put it. Or the full path of another, such as an AppImage.",
  launchApp: "Start OpenCode Desktop",
});

interface Preview extends ToolPreview {
  config: ReadBack;
}

const HELD: Record<Held, () => string> = {
  none: () => t("heldNone"),
  "too-many": () => t("heldTooMany", { max: MODELS_MAX }),
  text: () => t("heldText"),
  twice: () => t("heldTwice"),
  model: () => t("heldModel"),
};

/** The form Connect writes: the model list and the default model. */
interface Form {
  models: Model[];
  model: string;
}

const fromFile = (r: ReadBack): Form => {
  const models = r.state === "refused" ? [] : (r.models ?? []);
  return { models, model: r.state === "refused" ? "" : (r.model ?? models[0]?.id ?? "") };
};

export function OpenCodePage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("opencode.jsonc");
  const [form, setForm] = useState<Form>({ models: [], model: "" });
  const [read, setRead] = useState<Form>({ models: [], model: "" });
  const [newModel, setNewModel] = useState("");
  const [appPath, setAppPath] = useState("");
  const typed = where === "file" ? file : where === "root" ? root : "";
  const selection =
    where === "file"
      ? { targets: [{ file: file.trim() }] }
      : where === "root"
        ? { targets: [{ root: root.trim(), path: inRoot.trim() }] }
        : {};
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "opencode",
    selection,
    isDefault: where === "default",
    canRead: where === "default" || typed.trim() !== "",
    settings: (p) => p.config,
    onRead: (p) => {
      const f = fromFile(p.config);
      setForm(f);
      setRead(f);
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true, recipe: { target: "cli" } });
  const shown = useShown();
  const listId = useId();
  const served = servedNames(s);
  const edit = (patch: Partial<Form>) => {
    s.setDone(false);
    setForm((f) => ({ ...f, ...patch }));
  };
  const setRow = (i: number, patch: Partial<Model>) =>
    edit({ models: form.models.map((m, j) => (j === i ? { ...m, ...patch } : m)) });
  const imports = useAction(() => {});
  const doImport = () =>
    imports.run(async () => {
      const r = plainReply(await api.servedModels());
      if (r.body) {
        const found = r.body.models.map((m) => ({
          id: m.id,
          name: m.display_name ?? "",
          attachment: attachmentDefault(m.id),
        }));
        edit({ models: withModels(form.models, found) });
      }
      return r;
    });
  const add = () => {
    const id = newModel.trim();
    edit({ models: withModels(form.models, [{ id, name: "", attachment: attachmentDefault(id) }]) });
    setNewModel("");
  };
  const input = () => ({
    model: form.model,
    models: form.models.map((m) => ({ id: m.id.trim(), name: m.name.trim(), attachment: m.attachment })),
  });
  const st = s.preview?.config.state === "refused" ? undefined : s.preview?.config;
  const held = [
    ...(st?.v2_provider ? [t("heldV2")] : []),
    ...heldOf(form.models, form.model).map((h) => HELD[h]()),
  ];
  const dirty = JSON.stringify(form) !== JSON.stringify(read);
  const target = s.preview?.targets[0]?.target;
  const path =
    where === "default"
      ? (s.found?.config ?? target ?? "~/.config/opencode/opencode.jsonc")
      : (target ?? typed.trim());
  const ids = form.models.map((m) => m.id);
  const lines = st && (
    <>
      {st.v2_provider && <Notice tone="warn">{t("v2Provider")}</Notice>}
      {st.old_provider && <p class="muted">{t("oldProvider")}</p>}
    </>
  );

  return (
    <div class="tool-page" data-tool="opencode">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[path]}
        held={held}
        form={input}
        lines={lines}
      />
      {s.addon === "on" && st && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <ModelList id={listId} s={s} />
          <SettingRow name={t("list")} description={t("listNote")}>
            <span class="tool-inline">
              <span class="muted">{t("count", { count: form.models.length })}</span>
              <Button busy={imports.busy} onClick={doImport} disabled={s.busy || imports.busy || !s.core}>
                {t("importModels")}
              </Button>
            </span>
          </SettingRow>
          {imports.error && <Notice tone="danger">{shown.text(imports.error)}</Notice>}
          {form.models.length === 0 && (
            <div class="tool-say">
              <p class="muted">{t("empty")}</p>
            </div>
          )}
          {form.models.map((m, i) => (
            <SettingRow key={m.id} name={shown.text(m.id)}>
              <span class="tool-inline">
                <input
                  type="text"
                  aria-label={t("displayName")}
                  placeholder={t("displayName")}
                  autocomplete="off"
                  spellcheck={false}
                  value={m.name}
                  onInput={(e) => setRow(i, { name: e.currentTarget.value })}
                />
                <Switch
                  label={t("attachments")}
                  checked={m.attachment}
                  onChange={(on) => setRow(i, { attachment: on })}
                />
                <ConfirmButton
                  question={t("removeAsk", { model: shown.text(m.name.trim() || m.id) })}
                  onConfirm={() => edit({ models: form.models.filter((_, j) => j !== i) })}
                >
                  {t("removeModel")}
                </ConfirmButton>
              </span>
              <Unserved model={m.id} served={served} />
            </SettingRow>
          ))}
          <SettingRow name={t("addModel")} description={t("addModelNote")}>
            <span class="tool-inline">
              <input
                type="text"
                class="mono"
                aria-label={t("modelId")}
                autocomplete="off"
                spellcheck={false}
                list={listId}
                value={newModel}
                onInput={(e) => setNewModel(e.currentTarget.value)}
              />
              <Button onClick={add} disabled={!newModel.trim() || ids.includes(newModel.trim())}>
                {t("add")}
              </Button>
            </span>
          </SettingRow>
          <SettingRow name={t("model")} description={t("modelNote")}>
            <select
              aria-label={t("model")}
              value={form.model}
              onChange={(e) => edit({ model: e.currentTarget.value })}
            >
              {!ids.includes(form.model) && (
                <option value={form.model}>{form.model ? shown.text(form.model) : t("chooseModel")}</option>
              )}
              {form.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {shown.text(m.name.trim() || m.id)}
                </option>
              ))}
            </select>
          </SettingRow>
        </Section>
      )}
      {s.addon === "on" && (
        <StartSection s={s} start={start}>
          <SettingRow name={t("app")} description={t("appNote")}>
            <span class="tool-inline">
              <PathText label={t("app")} value={appPath} onInput={setAppPath} bare />
              <Button
                busy={start.busy}
                onClick={() =>
                  start.go({ target: "app" }, appPath.trim() ? { override: appPath.trim() } : {})
                }
                disabled={start.busy || !start.on}
              >
                {t("launchApp")}
              </Button>
            </span>
          </SettingRow>
        </StartSection>
      )}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="opencode"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: "~/.config/opencode/opencode.jsonc" },
                { value: "file", label: t("fileOne") },
                { value: "root", label: t("fileRoot") },
              ]}
            >
              {where === "file" && (
                <Text
                  label={t("filePath")}
                  hint={t("filePathHint")}
                  value={file}
                  onInput={setFile}
                  disabled={s.locked}
                />
              )}
              {where === "root" && (
                <>
                  <Text
                    label={t("root")}
                    hint={t("rootHint")}
                    value={root}
                    onInput={setRoot}
                    disabled={s.locked}
                  />
                  <Text label={t("inRoot")} value={inRoot} onInput={setInRoot} disabled={s.locked} />
                </>
              )}
            </FileChoice>
          }
          backups={
            s.preview && (
              <Backups
                pkg={PACKAGE}
                pins={async () => s.preview?.targets}
                disabled={!s.writable}
                onBusy={s.setOutside}
                onChange={s.load}
              />
            )
          }
        />
      )}
    </div>
  );
}

export const opencodePage = (): AgentPage => ({
  id: "opencode",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <OpenCodePage />,
});
