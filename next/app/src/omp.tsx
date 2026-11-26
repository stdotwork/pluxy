// The app's Oh My Pi page, on the shared tool-page parts (clientPage.tsx): how Oh My Pi stands and
// Connect in the head, its default model, Start, and the settings files, key and backups, through
// the optional add-on pluxy/omp-setup (next/app-tools/omp-setup) and the launcher. Connect writes
// models.yml and config.yml together. The key's value never reaches this webview.

import { useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import { PageHead } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  DefaultModel,
  FileChoice,
  FileKeyBackups,
  fileDefault,
  SaveButton,
  Section,
  StartSection,
  Text,
  type ToolPreview,
  ToolTop,
  useStart,
  useToolSetup,
} from "./clientPage";
import type { AgentPage } from "./easyMode";
import { CLIENT, modelOf, PACKAGE, type ReadBack, validModel } from "./ompForm";

const t = strings("omp", {
  title: "Oh My Pi",
  tip: "Connect Oh My Pi to pluxy and start it",
  fileRoot: "In a folder",
  fileOther: "Other files",
  root: "Folder",
  rootHint:
    "A full path, such as a profile's ~/.omp/profiles/<name>/agent; the folder may be a link (as chezmoi can make).",
  modelsFile: "Models file",
  settingsFile: "Settings file",
  pathHint: "A full path; a link (as stow makes) is followed and kept.",
  legacy:
    "models.yml still has the easy-cliproxyapi provider an older app wrote. pluxy leaves it as it is; remove it from models.yml if you no longer use it.",
  models: "Models",
  model: "Default model",
  modelNote: "The model Oh My Pi starts with. Oh My Pi lists pluxy's other models itself.",
  invalid: "Enter one model ID, with no spaces or commas, not starting with @ or *.",
});

interface Preview extends ToolPreview {
  config: ReadBack;
}

export function OmpPage() {
  const [where, setWhere] = useState<"default" | "root" | "files">("default");
  const [root, setRoot] = useState("");
  const [modelsFile, setModelsFile] = useState("");
  const [settingsFile, setSettingsFile] = useState("");
  const [model, setModel] = useState("");
  const [read, setRead] = useState("");
  const selection =
    where === "root"
      ? { folder: root.trim() }
      : where === "files"
        ? { targets: [{ file: modelsFile.trim() }, { file: settingsFile.trim() }] }
        : {};
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "omp",
    selection,
    isDefault: where === "default",
    canRead:
      where === "default" ||
      (where === "root" ? root.trim() !== "" : modelsFile.trim() !== "" && settingsFile.trim() !== ""),
    settings: (p) => p.config,
    onRead: (p) => {
      setModel(modelOf(p.config));
      setRead(modelOf(p.config));
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true });
  const edit = (v: string) => {
    s.setDone(false);
    setModel(v);
  };
  const input = () => ({ model: model.trim() });
  const held = validModel(model.trim()) ? [] : [t("invalid")];
  const dirty = model !== read;
  const st = s.preview?.config.state === "refused" ? undefined : s.preview?.config;
  const pinned = s.preview?.targets.map((x) => x.target) ?? [];
  const paths =
    (where === "default" ? s.found?.config : undefined) ??
    (pinned.length > 0 ? pinned : [where === "root" ? root.trim() : "~/.omp/agent/models.yml"]);

  return (
    <div class="tool-page" data-tool="omp">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[paths].flat()}
        held={held}
        form={input}
        lines={st?.legacy && <p class="muted">{t("legacy")}</p>}
      />
      {s.addon === "on" && st && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <DefaultModel
            s={s}
            name={t("model")}
            note={t("modelNote")}
            value={model}
            onInput={edit}
            valid={validModel(model.trim())}
          />
        </Section>
      )}
      {s.addon === "on" && <StartSection s={s} start={start} />}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="omp"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: "~/.omp/agent" },
                { value: "root", label: t("fileRoot") },
                { value: "files", label: t("fileOther") },
              ]}
            >
              {where === "root" && (
                <Text
                  label={t("root")}
                  hint={t("rootHint")}
                  value={root}
                  onInput={setRoot}
                  disabled={s.locked}
                />
              )}
              {where === "files" && (
                <>
                  <Text
                    label={t("modelsFile")}
                    hint={t("pathHint")}
                    value={modelsFile}
                    onInput={setModelsFile}
                    disabled={s.locked}
                  />
                  <Text
                    label={t("settingsFile")}
                    hint={t("pathHint")}
                    value={settingsFile}
                    onInput={setSettingsFile}
                    disabled={s.locked}
                  />
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

export const ompPage = (): AgentPage => ({
  id: "omp",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <OmpPage />,
});
