// The app's Hermes Agent page, on the shared tool-page parts (clientPage.tsx): how Hermes Agent
// stands and Connect in the head, its default model, Start, and the settings file, key and
// backups, through the optional add-on pluxy/hermes-setup (next/app-tools/hermes-setup) and the
// launcher. The key's value never reaches this webview.

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
import { CLIENT, modelOf, PACKAGE, plainModel, type ReadBack, validModel } from "./hermesForm";

const t = strings("hermes", {
  title: "Hermes Agent",
  tip: "Connect Hermes Agent to pluxy and start it",
  defaultNote: "~/.hermes (or its active profile)",
  fileRoot: "In a folder",
  fileOne: "A file",
  root: "Folder",
  rootHint:
    "A full path, such as the folder HERMES_HOME names or a profile's ~/.hermes/profiles/<name>; the folder may be a link (as chezmoi can make).",
  filePath: "File",
  filePathHint: "A full path; a link (as stow makes) is followed and kept.",
  legacy:
    "config.yaml still has the cpa-gui provider the old desktop app wrote. pluxy leaves it as it is; remove it from config.yaml if you no longer use it.",
  models: "Models",
  model: "Default model",
  modelNote: "The model Hermes Agent starts with.",
  invalid: 'Enter one model ID, with no spaces and no "${".',
  notText: "Hermes Agent would read that as a number, a date or yes/no, not as a model ID.",
});

interface Preview extends ToolPreview {
  config: ReadBack;
}

export function HermesPage() {
  const [where, setWhere] = useState<"default" | "root" | "file">("default");
  const [root, setRoot] = useState("");
  const [file, setFile] = useState("");
  const [model, setModel] = useState("");
  const [read, setRead] = useState("");
  const typed = where === "root" ? root : where === "file" ? file : "";
  const selection =
    where === "root" ? { folder: root.trim() } : where === "file" ? { targets: [{ file: file.trim() }] } : {};
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "hermes",
    selection,
    isDefault: where === "default",
    canRead: where === "default" || typed.trim() !== "",
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
  const held = validModel(model.trim()) ? [] : [plainModel(model.trim()) ? t("notText") : t("invalid")];
  const dirty = model !== read;
  const st = s.preview?.config.state === "refused" ? undefined : s.preview?.config;
  const target = s.preview?.targets[0]?.target;
  const path =
    where === "default" ? (s.found?.config ?? target ?? "~/.hermes/config.yaml") : (target ?? typed.trim());

  return (
    <div class="tool-page" data-tool="hermes">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[path]}
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
          program="hermes"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: t("defaultNote") },
                { value: "root", label: t("fileRoot") },
                { value: "file", label: t("fileOne") },
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
              {where === "file" && (
                <Text
                  label={t("filePath")}
                  hint={t("filePathHint")}
                  value={file}
                  onInput={setFile}
                  disabled={s.locked}
                />
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

export const hermesPage = (): AgentPage => ({
  id: "hermes",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <HermesPage />,
});
