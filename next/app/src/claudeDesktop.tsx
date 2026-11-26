// The app's Claude Desktop page, on the shared tool-page parts (clientPage.tsx): how Claude Desktop
// stands and Connect in the head, its models (up to 16, each with an alias pluxy can serve), Start,
// and the settings files, key and backups, through the optional add-on pluxy/claude-desktop
// (next/app-tools/claude-desktop) and the launcher. Connect writes Claude Desktop's third-party
// gateway (its Linux beta): three files, together. The key's value never reaches this webview.

import { useId, useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { Button, ConfirmButton, Notice, PageHead, Switch, useAction } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  aliasCancelled,
  aliasState,
  FILES,
  fresh,
  MODELS_MAX,
  missingLibrary,
  PACKAGE,
  type ReadBack,
  type Row,
  ready,
  rowsFrom,
} from "./claudeDesktopForm";
import {
  FileChoice,
  FileKeyBackups,
  fileDefault,
  ModelInput,
  ModelList,
  SaveButton,
  Section,
  StartSection,
  servedNames,
  Text,
  type ToolPreview,
  ToolTop,
  useStart,
  useToolSetup,
} from "./clientPage";
import type { AgentPage } from "./easyMode";
import { reply } from "./host";

const t = strings("claudeDesktop", {
  title: "Claude Desktop",
  tip: "Connect Claude Desktop to pluxy and start it",
  noFolder: "Claude Desktop has not made its settings folder yet.",
  createFolders: "Create settings folder",
  fileRoot: "In a folder",
  fileOther: "Other files",
  root: "Folder",
  rootHint:
    "Claude Desktop's third-party folder (Claude-3p), as a full path; it may be a link (as chezmoi can make).",
  deployment: "claude_desktop_config.json",
  profile: "Gateway profile",
  meta: "configLibrary/_meta.json",
  fileHint: "A full path; a link (as stow makes) is followed and kept.",
  models: "Models",
  modelsNote:
    "The first model is the one Claude Desktop starts with. Claude Desktop takes only Claude model names, so give any other model an alias that looks like one, such as claude-sonnet-5-mine.",
  aliasRule: "An alias must not name another family of models, such as gpt, grok, gemini or deepseek.",
  aliasPluxy:
    "pluxy must serve the alias too. Create alias in pluxy adds it to your signed-in accounts, after you confirm; accounts from pluxy's settings file need it added there.",
  modelN: "Model {n}",
  model: "Model",
  alias: "Alias",
  aliasHint: "Alias (optional)",
  oneM: "1M context",
  createAlias: "Create alias in pluxy",
  aliasServed: "pluxy serves this alias.",
  aliasMade: "pluxy now serves {alias} on {accounts}.",
  aliasAlready: "pluxy already serves {alias} on {accounts}.",
  aliasConfig: "Accounts from pluxy's settings file ({accounts}) do not have it: add it there.",
  addModel: "Add model",
  removeModel: "Remove model",
  removeAsk: "Remove {model} from the list?",
  empty: "No models yet. Add model adds one.",
  rows: "Each model needs a name, and names and aliases have no spaces (1 to 16 models).",
});

/** Claude Desktop's own model families, suggested as aliases. */
const SUGGESTED = [
  "claude-opus-5-5",
  "claude-sonnet-5-5",
  "claude-haiku-5-5",
  "claude-fable-5-2",
  "claude-opus-5",
  "claude-sonnet-5",
  "claude-haiku-4-5",
];

interface Preview extends ToolPreview {
  settings: ReadBack;
}

export function ClaudeDesktopPage() {
  const [where, setWhere] = useState<"default" | "root" | "files">("default");
  const [root, setRoot] = useState("");
  const [files, setFiles] = useState<string[]>(["", "", ""]);
  const [rows, setRows] = useState<Row[]>(fresh);
  const [read, setRead] = useState<Row[]>(fresh);
  const selection =
    where === "root"
      ? { targets: FILES.map((path) => ({ root: root.trim(), path })) }
      : where === "files"
        ? { targets: files.map((f) => ({ file: f.trim() })) }
        : {};
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "claude-desktop",
    selection,
    isDefault: where === "default",
    canRead:
      where === "default" || (where === "root" ? root.trim() !== "" : files.every((f) => f.trim() !== "")),
    settings: (p) => p.settings,
    onRead: (p) => {
      const r = rowsFrom(p.settings);
      setRows(r);
      setRead(r);
    },
  });
  const start = useStart({ s, client: t("title"), folder: false });
  const listId = useId();
  const aliasList = useId();
  const served = servedNames(s);
  const edit = (next: Row[]) => {
    s.setDone(false);
    setRows(next);
  };
  const setRow = (i: number, patch: Partial<Row>) =>
    edit(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const alias = useAction(s.served.reload);
  const [made, setMade] = useState<string>();
  const createAlias = (r: Row) =>
    alias.run(async () => {
      setMade(undefined);
      const res = await reply<{
        alias: string;
        accounts: string[];
        config_accounts: string[];
        already: boolean;
      }>("core_alias_create", { model: r.model.trim(), alias: r.alias.trim() });
      if (aliasCancelled(res.error?.message)) return { status: 200 };
      const b = res.body;
      if (b) {
        const said = t(b.already ? "aliasAlready" : "aliasMade", {
          alias: b.alias,
          accounts: b.accounts.join(", "),
        });
        setMade(
          b.config_accounts.length > 0
            ? `${said} ${t("aliasConfig", { accounts: b.config_accounts.join(", ") })}`
            : said,
        );
      }
      return res;
    });
  const input = () => ({
    models: rows.map((r) => ({ model: r.model.trim(), alias: r.alias.trim(), one_m: r.one_m })),
  });
  const held = ready(rows) ? [] : [t("rows")];
  const dirty = JSON.stringify(rows) !== JSON.stringify(read);
  // The default folder as a person reads it (from ~), else the first file read.
  const path =
    (where === "default" ? s.found?.config : undefined) ??
    s.preview?.targets[0]?.target ??
    (where === "root" ? root.trim() : "~/.config/Claude-3p");
  const noFolder = where === "default" && missingLibrary(s.failed);

  return (
    <div class="tool-page" data-tool="claude-desktop">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version, path]}
        path={path}
        held={held}
        form={input}
        failedRead={
          noFolder
            ? {
                line: t("noFolder"),
                action: (
                  <Button
                    key="folders"
                    variant="primary"
                    busy={s.working === "folders"}
                    disabled={s.busy}
                    onClick={() => s.write("folders", () => s.call("folders", {}))}
                  >
                    {t("createFolders")}
                  </Button>
                ),
              }
            : undefined
        }
      />
      {s.addon === "on" && s.settings && s.settings.state !== "refused" && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <ModelList id={listId} s={s} />
          <datalist id={aliasList}>
            {SUGGESTED.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
          <div class="tool-say">
            <p class="muted">{t("modelsNote")}</p>
            <p class="muted">{t("aliasRule")}</p>
            <p class="muted">{t("aliasPluxy")}</p>
          </div>
          {alias.error && <Notice tone="danger">{alias.error}</Notice>}
          {made && <Notice tone="ok">{made}</Notice>}
          {rows.length === 0 && <p class="muted">{t("empty")}</p>}
          {rows.map((r, i) => {
            const state = aliasState(r, served);
            return (
              <SettingRow key={i} name={t("modelN", { n: i + 1 })}>
                <span class="tool-desktop-model">
                  <ModelInput
                    label={t("model")}
                    value={r.model}
                    list={listId}
                    served={served}
                    onInput={(v) => setRow(i, { model: v })}
                  />
                  <input
                    type="text"
                    class="mono"
                    aria-label={t("alias")}
                    placeholder={t("aliasHint")}
                    autocomplete="off"
                    spellcheck={false}
                    list={aliasList}
                    value={r.alias}
                    onInput={(e) => setRow(i, { alias: e.currentTarget.value })}
                  />
                  <span class="tool-inline">
                    <Switch label={t("oneM")} checked={r.one_m} onChange={(on) => setRow(i, { one_m: on })} />
                    {state === "missing" && s.core && (
                      <Button busy={alias.busy} disabled={alias.busy} onClick={() => createAlias(r)}>
                        {t("createAlias")}
                      </Button>
                    )}
                    {state === "served" && <span class="muted">{t("aliasServed")}</span>}
                    <ConfirmButton
                      question={t("removeAsk", {
                        model: r.alias.trim() || r.model.trim() || t("modelN", { n: i + 1 }),
                      })}
                      onConfirm={() => edit(rows.filter((_, j) => j !== i))}
                    >
                      {t("removeModel")}
                    </ConfirmButton>
                  </span>
                </span>
              </SettingRow>
            );
          })}
          <div class="tool-say">
            <Button
              disabled={rows.length >= MODELS_MAX}
              onClick={() => edit([...rows, { model: "", alias: "", one_m: false }])}
            >
              {t("addModel")}
            </Button>
          </div>
        </Section>
      )}
      {s.addon === "on" && <StartSection s={s} start={start} />}
      {s.addon === "on" && (
        <FileKeyBackups
          s={s}
          start={start}
          program="claude-desktop"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: "~/.config/Claude-3p" },
                { value: "root", label: t("fileRoot") },
                { value: "files", label: t("fileOther") },
              ]}
            >
              {where === "root" && (
                <Text label={t("root")} hint={t("rootHint")} value={root} onInput={setRoot} />
              )}
              {where === "files" &&
                [t("deployment"), t("profile"), t("meta")].map((label, i) => (
                  <Text
                    key={label}
                    label={label}
                    hint={t("fileHint")}
                    value={files[i] ?? ""}
                    onInput={(v: string) => setFiles((fs) => fs.map((f, j) => (j === i ? v : f)))}
                  />
                ))}
            </FileChoice>
          }
          backups={
            s.preview && (
              <Backups
                bare
                pkg={PACKAGE}
                pins={async () => s.preview?.targets}
                disabled={s.busy}
                onChange={s.load}
              />
            )
          }
        />
      )}
    </div>
  );
}

export const claudeDesktopPage = (): AgentPage => ({
  id: "claude-desktop",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <ClaudeDesktopPage />,
});
