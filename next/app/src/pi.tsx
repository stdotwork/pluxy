// The app's Pi page, on the shared tool-page parts (clientPage.tsx): how Pi stands and Connect in the
// head (Install plugin while pluxy's Pi plugin is not there: the write installs it first), its
// default model and the plugin, Start, and the settings files, key and backups, through the
// optional add-on pluxy/pi-setup (next/app-tools/pi-setup) and the launcher. Connect writes
// cliproxyapi.json, settings.json and the plugin's model list together. The key's value never
// reaches this webview.

import { useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { Button, ConfirmButton, Notice, PageHead } from "../../ui/kit/ui";
import type { AgentPage } from "./addonItems";
import { Backups } from "./backups";
import {
  DefaultModel,
  FileChoice,
  FileKeyBackups,
  fileDefault,
  type ReadBack,
  SaveButton,
  Section,
  StartSection,
  Text,
  type ToolPreview,
  ToolTop,
  useStart,
  useToolSetup,
} from "./clientPage";
import { CLIENT, PACKAGE, pluginNewer, selectionFor, validModel, type Where } from "./piForm";

const t = strings("pi", {
  title: "Pi",
  tip: "Connect Pi to pluxy and start it",
  fileRoot: "In a folder",
  fileLinks: "Linked files",
  root: "Folder",
  rootHint:
    "A full path, such as the folder PI_CODING_AGENT_DIR names; the folder may be a link (as chezmoi can make).",
  linksHint:
    "A full path to the folder holding cliproxyapi.json and settings.json as links (as stow makes); each link is followed and kept.",
  legacy:
    "settings.json lists a copy of the plugin an older app installed from npm. pluxy puts its own reviewed copy in its place; Disconnect and Restore backup put the old one back.",
  foreign:
    "Pi's agent folder already has a folder named pluxy-cliproxyapi-provider that pluxy did not put there. Move it out of the agent folder, then press Read this file.",
  heldForeign: "Install plugin waits until that folder is moved.",
  models: "Models",
  model: "Default model",
  modelNote: "The model Pi starts with. The plugin lists pluxy's Codex models in Pi itself.",
  invalid: "Enter one model ID of at most 240 characters, with no spaces or control characters.",
  install: "Install plugin",
  plugin: "Pi plugin",
  pluginIn: "Version {version} is installed. It lets Pi reach pluxy.",
  pluginNewer: "Version {version} is installed. Update plugin installs version {bundled}.",
  pluginOut: "Not installed. Install plugin installs it and connects Pi.",
  unknown: "unknown",
  updatePlugin: "Update plugin",
  updated: "The Pi plugin is updated.",
  uninstallPlugin: "Uninstall plugin",
  uninstallAsk: "Uninstall the Pi plugin? Pi's settings for pluxy stay.",
  uninstalled: "The Pi plugin is uninstalled.",
});

interface Adapter {
  installed: boolean;
  version: string | null;
  bundled: string;
  /** The add-on's word: true for any version but its own, older too (the page offers Update plugin
   * only for a newer copy, pluginNewer). */
  update: boolean;
  foreign: boolean;
  /** The pin of pluxy's copy of the plugin, which Update plugin and Uninstall plugin carry back so
   * the add-on refuses a plugin or agent folder that changed since; null when there is none. */
  pin: ToolPreview["targets"][number] | null;
}

/** What the add-on reads back of Pi's files; refused (with reason) for files Pi would not take. */
interface PiConfig extends ReadBack {
  provider?: string;
  default_provider?: string | null;
  model?: string | null;
  registered?: boolean;
  legacy?: boolean;
  adapter: Adapter;
}

interface Preview extends ToolPreview {
  targets: (ToolPreview["targets"][number] & { path?: string | null })[];
  pi: PiConfig;
}

export function PiPage() {
  const [where, setWhere] = useState<Where>("default");
  const [folder, setFolder] = useState("");
  const [model, setModel] = useState("");
  const [read, setRead] = useState("");
  const [notice, setNotice] = useState<string>();
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "pi",
    selection: selectionFor(where, folder),
    isDefault: where === "default",
    canRead: where === "default" || folder.trim() !== "",
    settings: (p) => p.pi,
    onRead: (p) => {
      const m = p.pi.state === "refused" ? "" : (p.pi.model ?? "");
      setModel(m);
      setRead(m);
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true });
  const edit = (v: string) => {
    s.setDone(false);
    setModel(v);
  };
  const cfg = s.preview?.pi;
  const adapter = cfg?.adapter;
  const st = cfg?.state === "refused" ? undefined : cfg;
  // Update plugin only when the add-on carries a newer copy than the one installed.
  const newer = !!adapter?.installed && pluginNewer(adapter.version, adapter.bundled);
  const input = () => ({ model: model.trim() });
  const held = [
    ...(adapter?.foreign && !adapter.installed ? [t("heldForeign")] : []),
    ...(validModel(model.trim()) ? [] : [t("invalid")]),
  ];
  const dirty = model !== read;
  // The agent folder the reading found: a folder as given, else the folder of the default files.
  const first = s.preview?.targets[0];
  const agent =
    first && (first.path ? first.selected : first.selected.slice(0, first.selected.lastIndexOf("/")));
  const path =
    (where === "default" ? s.found?.config : undefined) ??
    agent ??
    (where === "default" ? "~/.pi/agent" : folder.trim());
  /** One of the plugin's own writes (Update plugin, Uninstall plugin), which says what it did. Each
   * sends the reading's pins of the three files and of the plugin's copy: the add-on refuses one
   * that changed since, as every write on these pages does. */
  const plugin = (op: "update" | "uninstall", said: string) => {
    setNotice(undefined);
    s.write(
      op,
      (call) => call(op, { targets: s.preview?.targets, plugin: s.preview?.pi.adapter.pin ?? null }),
      () => {
        setNotice(said);
        s.load();
      },
    );
  };
  const lines = st && (
    <>
      {adapter?.foreign && !adapter.installed && <Notice tone="warn">{t("foreign")}</Notice>}
      {st.legacy && <p class="muted">{t("legacy")}</p>}
    </>
  );

  return (
    <div class="tool-page" data-tool="pi">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[path]}
        held={held}
        form={input}
        lines={lines}
        connectLabel={adapter && !adapter.installed ? t("install") : undefined}
      />
      {s.addon === "on" && st && adapter && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <DefaultModel
            s={s}
            name={t("model")}
            note={t("modelNote")}
            value={model}
            onInput={edit}
            valid={validModel(model.trim())}
          />
          <SettingRow
            name={t("plugin")}
            description={
              !adapter.installed
                ? t("pluginOut")
                : newer
                  ? t("pluginNewer", { version: adapter.version ?? t("unknown"), bundled: adapter.bundled })
                  : t("pluginIn", { version: adapter.version ?? t("unknown") })
            }
          >
            {(adapter.installed || st.registered) && (
              <span class="tool-inline">
                {newer && (
                  <Button
                    busy={s.working === "update"}
                    onClick={() => plugin("update", t("updated"))}
                    disabled={!s.writable || !adapter.pin}
                  >
                    {t("updatePlugin")}
                  </Button>
                )}
                <ConfirmButton
                  question={t("uninstallAsk")}
                  disabled={!s.writable}
                  onConfirm={() => plugin("uninstall", t("uninstalled"))}
                >
                  {t("uninstallPlugin")}
                </ConfirmButton>
              </span>
            )}
            {notice && !s.working && <small class="muted">{notice}</small>}
          </SettingRow>
        </Section>
      )}
      {s.addon === "on" && <StartSection s={s} start={start} />}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="pi"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={(w) => {
                setNotice(undefined);
                setWhere(w);
              }}
              options={[
                { value: "default", label: fileDefault(), note: "~/.pi/agent" },
                { value: "folder", label: t("fileRoot") },
                { value: "links", label: t("fileLinks") },
              ]}
            >
              {where !== "default" && (
                <Text
                  label={t("root")}
                  hint={where === "links" ? t("linksHint") : t("rootHint")}
                  value={folder}
                  onInput={setFolder}
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

export const piPage = (): AgentPage => ({
  id: "pi",
  package: PACKAGE,
  dest: "tools",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <PiPage />,
});
