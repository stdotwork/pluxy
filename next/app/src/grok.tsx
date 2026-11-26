// The app's Grok Build page, on the shared tool-page parts (clientPage.tsx): how Grok Build stands
// and Connect in the head, its models (the catalog pluxy's entries list, imported from pluxy on
// request, each with its context window, and the default model), Start, and the settings file, key
// and backups, through the optional add-on pluxy/grok-setup (next/app-tools/grok-setup) and the
// launcher. The key's value never reaches this webview.

import { useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { Button, Notice, PageHead, Switch, useAction, useKit } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  FileChoice,
  FileKeyBackups,
  fileDefault,
  NumberInput,
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
import {
  CLIENT,
  type Held,
  heldOf,
  MODELS_MAX,
  modelOf,
  PACKAGE,
  type ReadBack,
  type Row,
  rowsOf,
  WINDOW_MAX,
  withServed,
} from "./grokForm";
import { plainReply, useShown } from "./toolWords";

const t = strings("grok", {
  title: "Grok Build",
  tip: "Connect Grok Build to pluxy and start it",
  fileOne: "A file",
  fileRoot: "In a folder",
  filePath: "File",
  filePathHint: "A full path, such as $GROK_HOME/config.toml; a link (as stow makes) is followed and kept.",
  root: "Folder",
  rootHint: "A full path; the folder may be a link (as chezmoi can make).",
  inRoot: "File in the folder",
  legacy: {
    one: "config.toml still has {count} model entry an older app wrote (cpa-gui/…). pluxy leaves it as it is; remove it from config.toml if you no longer use it.",
    other:
      "config.toml still has {count} model entries an older app wrote (cpa-gui/…). pluxy leaves them as they are; remove them from config.toml if you no longer use them.",
  },
  models: "Models",
  catalog: "Model list",
  catalogNote:
    "The models Grok Build lists from pluxy, each with its context window in tokens, which Grok Build compacts by. Turning one off takes it out of the list when you connect; your own entries stay.",
  count: { one: "{count} of {total} models listed", other: "{count} of {total} models listed" },
  importModels: "Import models from pluxy",
  empty: "No models yet. Import models from pluxy.",
  listed: "List in Grok Build",
  window: "Context window",
  badWindow: "A whole number from 1 to {max}.",
  model: "Default model",
  modelNote: "The model Grok Build starts with, one of the models listed.",
  chooseModel: "Choose a model",
  heldNone: "Import models from pluxy and list at least one.",
  heldTooMany: "Grok Build takes at most {max} models from here: turn some off.",
  heldWindow: "Each context window must be a whole number from 1 to {max}.",
  heldModel: "Choose a default model from the models listed.",
  heldText:
    "Each model listed needs an ID of 1 to 240 characters with no spaces or control characters, and a name of at most 240 characters with no control characters.",
  heldTwice: "A model is listed twice: turn one off.",
});

interface Preview extends ToolPreview {
  settings: ReadBack;
}

const HELD: Record<Held, () => string> = {
  none: () => t("heldNone"),
  "too-many": () => t("heldTooMany", { max: MODELS_MAX }),
  window: () => t("heldWindow", { max: WINDOW_MAX }),
  text: () => t("heldText"),
  twice: () => t("heldTwice"),
  model: () => t("heldModel"),
};

interface Form {
  rows: Row[];
  model: string;
}

export function GrokPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("config.toml");
  const [form, setForm] = useState<Form>({ rows: [], model: "" });
  const [read, setRead] = useState<Form>({ rows: [], model: "" });
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
    scan: "grok-build",
    selection,
    isDefault: where === "default",
    canRead: where === "default" || typed.trim() !== "",
    settings: (p) => p.settings,
    onRead: (p) => {
      const f = { rows: rowsOf(p.settings), model: modelOf(p.settings) };
      setForm(f);
      setRead(f);
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true });
  const shown = useShown();
  const served = servedNames(s);
  const edit = (patch: Partial<Form>) => {
    s.setDone(false);
    setForm((f) => ({ ...f, ...patch }));
  };
  const setRow = (i: number, patch: Partial<Row>) =>
    edit({ rows: form.rows.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  const imports = useAction(() => {});
  const doImport = () =>
    imports.run(async () => {
      const r = plainReply(await api.servedModels());
      if (r.body) edit({ rows: withServed(form.rows, r.body.models) });
      return r;
    });
  const chosen = form.rows.filter((r) => r.on);
  const input = () => ({
    model: form.model.trim(),
    models: chosen.map((r) => ({
      model: r.model,
      name: r.name === r.model ? "" : r.name,
      context_window: r.window,
    })),
  });
  const held = heldOf(form.rows, form.model).map((h) => HELD[h]());
  const dirty = JSON.stringify(form) !== JSON.stringify(read);
  const st = s.preview?.settings.state === "refused" ? undefined : s.preview?.settings;
  const target = s.preview?.targets[0]?.target;
  const path =
    where === "default" ? (s.found?.config ?? target ?? "~/.grok/config.toml") : (target ?? typed.trim());
  const names = chosen.map((r) => r.model);

  return (
    <div class="tool-page" data-tool="grok-build">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[path]}
        held={held}
        form={input}
        lines={(st?.legacy ?? 0) > 0 && <p class="muted">{t("legacy", { count: st?.legacy ?? 0 })}</p>}
      />
      {s.addon === "on" && st && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <SettingRow name={t("catalog")} description={t("catalogNote")}>
            <span class="tool-inline">
              <span class="muted">{t("count", { count: chosen.length, total: form.rows.length })}</span>
              <Button busy={imports.busy} onClick={doImport} disabled={s.busy || imports.busy || !s.core}>
                {t("importModels")}
              </Button>
            </span>
          </SettingRow>
          {imports.error && <Notice tone="danger">{shown.text(imports.error)}</Notice>}
          {form.rows.length === 0 && (
            <div class="tool-say">
              <p class="muted">{t("empty")}</p>
            </div>
          )}
          {form.rows.map((r, i) => (
            <SettingRow
              key={r.model}
              name={shown.text(r.name)}
              description={r.name === r.model ? undefined : shown.text(r.model)}
            >
              <span class="tool-inline">
                <Switch label={t("listed")} checked={r.on} onChange={(on) => setRow(i, { on })} />
                <span class="muted">{t("window")}</span>
                <NumberInput
                  label={t("window")}
                  min={1}
                  max={WINDOW_MAX}
                  step={1000}
                  disabled={!r.on}
                  value={r.window}
                  onInput={(v) => setRow(i, { window: v ?? 0 })}
                />
              </span>
              {r.on && !(Number.isInteger(r.window) && r.window >= 1 && r.window <= WINDOW_MAX) && (
                <small class="tool-unserved">{t("badWindow", { max: WINDOW_MAX })}</small>
              )}
              <Unserved model={r.model} served={served} />
            </SettingRow>
          ))}
          <SettingRow name={t("model")} description={t("modelNote")}>
            <select
              aria-label={t("model")}
              value={form.model}
              onChange={(e) => edit({ model: e.currentTarget.value })}
            >
              {!names.includes(form.model) && (
                <option value={form.model}>{form.model ? shown.text(form.model) : t("chooseModel")}</option>
              )}
              {chosen.map((r) => (
                <option key={r.model} value={r.model}>
                  {shown.text(r.name)}
                </option>
              ))}
            </select>
          </SettingRow>
        </Section>
      )}
      {s.addon === "on" && <StartSection s={s} start={start} />}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="grok"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: "~/.grok/config.toml" },
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

export const grokPage = (): AgentPage => ({
  id: "grok-build",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <GrokPage />,
});
