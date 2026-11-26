// The app's Claude Code page, on the shared tool-page parts (clientPage.tsx): how Claude Code
// stands and Connect in the head, its models, Start, and the settings file, key and backups,
// through the optional add-on pluxy/claude-code (next/app-tools/claude-code) and the launcher. The
// page is the window's own, not an extension page, because Connect gives the tool a key: the
// window names a vault secret's ID and the add-on's confirmation names its label; the key's value
// never reaches this webview. On Windows with WSL, the section under the head picks where Claude
// Code is set up and started: every call to the add-on and the launch then go to that environment
// (environmentBlock.tsx).

import { useId, useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { PageHead, Switch } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  CLIENT,
  type Form,
  fresh,
  fromFile,
  PACKAGE,
  type ReadBack,
  ROLES,
  type Role,
  ready,
  settingsNote,
  WINDOW_1M,
  WINDOW_DEFAULT,
} from "./claudeCodeForm";
import {
  FileChoice,
  FileKeyBackups,
  fileDefault,
  ModelInput,
  ModelList,
  NumberInput,
  SaveButton,
  Section,
  StartSection,
  servedNames,
  type ToolPreview,
  ToolTop,
  useStart,
  useToolSetup,
} from "./clientPage";
import type { AgentPage } from "./easyMode";
import { inHome } from "./environment";
import { EnvironmentBlock, LinuxBuildOffer, PathText, plainPath, useEnvironment } from "./environmentBlock";
import { useShown } from "./toolWords";

const t = strings("claudeCode", {
  title: "Claude Code",
  tip: "Connect Claude Code to pluxy and start it",
  fileOne: "A file",
  fileRoot: "In a folder",
  filePath: "File",
  filePathHint: "A full path; a link (as stow makes) is followed and kept.",
  root: "Folder",
  rootHint: "A full path; the folder may be a link (as chezmoi can make).",
  inRoot: "File in the folder",
  models: "Models",
  roleNote: "The model Claude Code uses for {role}.",
  fableNote: "Empty: the same as Sonnet.",
  oneM: "1M context",
  startup: "Starts with",
  startupNote: "The model Claude Code starts with: a model, or one of opus, sonnet, haiku and fable.",
  subagent: "Subagents",
  subagentNote: "The model subagents use. Empty: the same as the main model.",
  context: "Context and auto-compaction",
  window: "Auto-compact window",
  windowNote: "Tokens. 200000, or 1000000 with 1M context on; another number you enter is kept.",
  pct: "Compacts at (%)",
  pctNote: "90 unless you change it. A lower number compacts earlier.",
  disable: "Turn off auto-compaction",
  disableNote: "Only /compact, typed by you, is left.",
  ranges:
    "Opus, Sonnet and Haiku each need a model, the auto-compact window a whole number from 100000 to 1000000, and the percentage a whole number from 1 to 100.",
  folderWsl: "A folder in the distribution, such as {example}.",
});

const LABELS: Record<Role, string> = { opus: "Opus", sonnet: "Sonnet", haiku: "Haiku", fable: "Fable" };

interface Preview extends ToolPreview {
  settings: ReadBack;
}

export function ClaudeCodePage() {
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("settings.json");
  const [form, setForm] = useState<Form>(fresh);
  const [read, setRead] = useState<Form>(fresh);
  // Another environment is another file: its form starts over.
  const env = useEnvironment(PACKAGE, () => setForm(fresh()));
  const distro = env.selection?.wsl.distro;
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
    scan: "claude-code",
    env,
    selection,
    isDefault: where === "default",
    canRead: (where === "default" || typed.trim() !== "") && plainPath(typed, distro),
    settings: (p) => p.settings,
    onRead: (p) => {
      const f = fromFile(p.settings);
      setForm(f);
      setRead(f);
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true });
  const shown = useShown();
  const listId = useId();
  const served = servedNames(s);
  const edit = (patch: Partial<Form>) => {
    s.setDone(false);
    setForm((f) => ({ ...f, ...patch }));
  };
  const setOneM = (role: Role, on: boolean) =>
    edit({
      oneM: { ...form.oneM, [role]: on },
      // The window follows the 1M switches while it holds one of their defaults.
      window: ((any) =>
        form.window === WINDOW_DEFAULT && any
          ? WINDOW_1M
          : form.window === WINDOW_1M && !any
            ? WINDOW_DEFAULT
            : form.window)(Object.values({ ...form.oneM, [role]: on }).some(Boolean)),
    });
  const input = () => ({
    ...Object.fromEntries(
      ROLES.flatMap((r) => [
        [r, form.models[r].trim()],
        [`${r}_1m`, form.oneM[r]],
      ]),
    ),
    startup_model: form.startup.trim(),
    subagent_model: form.subagent.trim(),
    max_context_tokens: form.window,
    auto_compact_pct: form.pct,
    disable_auto_compact: form.disable,
  });
  const held = ready(form) ? [] : [t("ranges")];
  const dirty = JSON.stringify(form) !== JSON.stringify(read);
  const home = env.record?.home;
  const target = s.preview?.targets[0]?.target;
  const path =
    where === "default" ? (s.found?.config ?? target ?? settingsNote(home)) : (target ?? typed.trim());
  const refused = s.settings?.state === "refused";

  return (
    <div class="tool-page" data-tool="claude-code">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={[path]}
        held={held}
        form={input}
        lines={<LinuxBuildOffer error={s.failed} pkg={PACKAGE} record={env.record} onInstalled={s.load} />}
      />
      <EnvironmentBlock env={env} tool={t("title")} disabled={s.locked} />
      {s.addon === "on" && s.settings && !refused && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <ModelList id={listId} s={s} />
          {(["opus", "sonnet", "haiku", "fable"] as const).map((role) => (
            <SettingRow
              key={role}
              name={LABELS[role]}
              description={role === "fable" ? t("fableNote") : t("roleNote", { role: LABELS[role] })}
            >
              <span class="tool-inline">
                <ModelInput
                  label={LABELS[role]}
                  value={form.models[role]}
                  list={listId}
                  served={served}
                  onInput={(v) => edit({ models: { ...form.models, [role]: v } })}
                />
                <Switch label={t("oneM")} checked={form.oneM[role]} onChange={(on) => setOneM(role, on)} />
              </span>
            </SettingRow>
          ))}
          <SettingRow name={t("startup")} description={t("startupNote")}>
            <ModelInput
              label={t("startup")}
              value={form.startup}
              list={listId}
              served={served}
              known={ROLES}
              onInput={(v) => edit({ startup: v })}
            />
          </SettingRow>
          <SettingRow name={t("subagent")} description={t("subagentNote")}>
            <ModelInput
              label={t("subagent")}
              value={form.subagent}
              list={listId}
              served={served}
              known={ROLES}
              onInput={(v) => edit({ subagent: v })}
            />
          </SettingRow>
          <details class="tool-more tool-more-inner">
            <summary>{t("context")}</summary>
            <SettingRow name={t("window")} description={t("windowNote")}>
              <NumberInput
                label={t("window")}
                min={100000}
                max={1000000}
                step={1000}
                value={form.window}
                onInput={(v) => edit({ window: v ?? 0 })}
              />
            </SettingRow>
            <SettingRow name={t("pct")} description={t("pctNote")}>
              <NumberInput
                label={t("pct")}
                min={1}
                max={100}
                disabled={form.disable}
                value={form.pct}
                onInput={(v) => edit({ pct: v ?? 0 })}
              />
            </SettingRow>
            <SettingRow name={t("disable")} description={t("disableNote")}>
              <Switch label={t("disable")} checked={form.disable} onChange={(on) => edit({ disable: on })} />
            </SettingRow>
          </details>
        </Section>
      )}
      {s.addon === "on" && (
        <StartSection
          key={JSON.stringify(env.selection)}
          s={s}
          start={start}
          folderNote={
            env.selection
              ? t("folderWsl", { example: shown.path(inHome(home ?? "~", "project")) })
              : undefined
          }
          extra={<LinuxBuildOffer error={start.error} pkg={PACKAGE} record={env.record} />}
        />
      )}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="claude"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: settingsNote(home) },
                { value: "file", label: t("fileOne") },
                { value: "root", label: t("fileRoot") },
              ]}
            >
              {where === "file" && (
                <PathText
                  label={t("filePath")}
                  hint={t("filePathHint")}
                  value={file}
                  onInput={setFile}
                  distro={distro}
                  disabled={s.locked}
                />
              )}
              {where === "root" && (
                <>
                  <PathText
                    label={t("root")}
                    hint={t("rootHint")}
                    value={root}
                    onInput={setRoot}
                    distro={distro}
                    disabled={s.locked}
                  />
                  <PathText label={t("inRoot")} value={inRoot} onInput={setInRoot} disabled={s.locked} />
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
                environment={env.selection}
              />
            )
          }
        />
      )}
    </div>
  );
}

export const claudeCodePage = (): AgentPage => ({
  id: "claude-code",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <ClaudeCodePage />,
});
