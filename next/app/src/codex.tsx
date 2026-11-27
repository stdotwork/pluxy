// The app's Codex page, on the shared tool-page parts (clientPage.tsx): how Codex stands and
// Connect in the head, its models (the default, the approval model, remote compaction, subagents
// and the model list, imported from pluxy on request and then edited), Start (the command line,
// the app, and a restart of Codex's service), and the settings files, key and backups, through the
// optional add-on pluxy/codex-setup (next/app-tools/codex) and the launcher. Connect writes
// config.toml and the model list file together. The key's value never reaches this webview.

import { useState } from "preact/hooks";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { strings } from "../../ui/kit/strings";
import { Button, ConfirmButton, Notice, PageHead, Switch, useAction, useKit } from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  FileChoice,
  FileKeyBackups,
  fileDefault,
  NumberInput,
  SaveButton,
  Section,
  StartSection,
  Text,
  type ToolPreview,
  ToolTop,
  useStart,
  useToolSetup,
} from "./clientPage";
import {
  type Agents,
  CLIENT,
  type Form,
  fresh,
  fromFile,
  type Model,
  PACKAGE,
  type ReadBack,
} from "./codexForm";
import type { AgentPage } from "./easyMode";
import { PathText } from "./environmentBlock";
import { plainReply, useShown } from "./toolWords";

const t = strings("codex", {
  title: "Codex",
  tip: "Connect Codex to pluxy and start it",
  fileOther: "Other files",
  fileRoot: "In a folder",
  configPath: "config.toml",
  catalogPath: "Model list file",
  pathHint: "A full path; a link (as stow makes) is followed and kept.",
  root: "Folder",
  rootHint: "A full path, such as your CODEX_HOME; the folder may be a link (as chezmoi can make).",
  providerChange:
    "Codex lists past conversations by provider. After Connect, conversations started with {provider} stay hidden from resume. The app does not change past conversations.",
  profile: "The Codex profile {profile} is in use and may override these settings.",
  models: "Models",
  model: "Default model",
  modelNote: "The model Codex starts with, one from the model list.",
  importFirst: "Import models first",
  approval: "Approval model",
  approvalNote: "Answers Codex's automatic approval requests.",
  approvalDefault: "Codex's default",
  approvalMissing: "This model is no longer in the model list. pluxy keeps the setting.",
  compaction: "Remote compaction",
  compactionNote: "Off unless you turn it on. Compacts on the server; turn it off if compacting fails.",
  subagents: "Subagents",
  subEnabled: "Use subagents",
  subEnabledNote: "Lets Codex start subagents with its multi-agent tools.",
  subThreads: "Most at once",
  subThreadsNote: "Subagents per session. Empty: Codex's default.",
  subModel: "Subagent model",
  subModelNote: "Empty: the same as the main model.",
  subEffort: "Reasoning effort",
  subDefault: "Codex's default",
  subInterrupt: "Tell the model about interruptions",
  subInterruptNote: "Adds a message the model sees when a subagent is interrupted.",
  catalog: "Model list",
  catalogNote: "The models Codex lists. Only models Codex has a template for are imported.",
  count: { one: "{count} model", other: "{count} models" },
  importModels: "Import models from pluxy",
  noTemplate: "Codex has no template for: {models}",
  displayName: "Display name",
  description: "Description",
  context: "Context window",
  contextNote: "Tokens.",
  maximum: "Most context",
  percent: "Context used (%)",
  compact: "Compacts at",
  automatic: "Tokens. Empty: automatic.",
  reasoning: "Reasoning levels",
  defaultReasoning: "Default reasoning level",
  clientDefault: "Codex decides",
  modalities: "Takes",
  text: "Text",
  image: "Images",
  visible: "Show in Codex's model list",
  toolSearch: "Tool search",
  toolSearchNote: "Needs support from the model and its provider; turn it off if it fails.",
  systemPrompt: "System prompt",
  systemPromptNote: "Replaces this model's system prompt ({chars} characters now). Empty: keeps it.",
  removeModel: "Remove model",
  removeAsk: "Remove {model} from the list?",
  heldNoModels: "Import models from pluxy, then choose a default model.",
  heldModel: "Choose a default model from the model list.",
  restartService: "Restart Codex service",
  restartNote: "Codex's sessions read the new settings and models.",
  restartAsk: "Restarting stops every Codex session running now, in every terminal.",
  app: "Codex app",
  appNote: "Codex has no Linux app of its own: the full path of the program to start.",
  launchApp: "Start Codex App",
  appCwd: "Choose a folder above first: the app starts there.",
});

const EFFORTS = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"] as const;

interface Preview extends ToolPreview {
  settings: ReadBack;
}

/** list with v in or out, in the order order gives. */
const toggle = (list: string[], v: string, on: boolean, order: readonly string[]) =>
  order.filter((x) => (x === v ? on : list.includes(x)));

/** One model of the list: a row that opens to its settings, and Remove model, which asks first. */
function ModelEditor(props: { m: Model; onChange: (m: Model) => void; onRemove: () => void }) {
  const { m } = props;
  const set = (patch: Partial<Model>) => props.onChange({ ...m, ...patch });
  return (
    <details class="tool-model-row">
      <summary>
        <span>{m.display_name}</span> <small class="mono muted">{m.slug}</small>
      </summary>
      <SettingRow name={t("displayName")}>
        <input
          type="text"
          aria-label={t("displayName")}
          value={m.display_name}
          onInput={(e) => set({ display_name: e.currentTarget.value })}
        />
      </SettingRow>
      <SettingRow name={t("description")}>
        <input
          type="text"
          aria-label={t("description")}
          value={m.description}
          onInput={(e) => set({ description: e.currentTarget.value })}
        />
      </SettingRow>
      <SettingRow name={t("context")} description={t("contextNote")}>
        <NumberInput
          label={t("context")}
          min={1}
          blank
          value={m.context_window}
          onInput={(v) => set({ context_window: v })}
        />
      </SettingRow>
      <SettingRow name={t("maximum")} description={t("contextNote")}>
        <NumberInput
          label={t("maximum")}
          min={1}
          blank
          value={m.max_context_window}
          onInput={(v) => set({ max_context_window: v })}
        />
      </SettingRow>
      <SettingRow name={t("percent")}>
        <NumberInput
          label={t("percent")}
          min={1}
          blank
          value={m.effective_context_window_percent}
          onInput={(v) => set({ effective_context_window_percent: v })}
        />
      </SettingRow>
      <SettingRow name={t("compact")} description={t("automatic")}>
        <NumberInput
          label={t("compact")}
          min={1}
          blank
          value={m.auto_compact_token_limit}
          onInput={(v) => set({ auto_compact_token_limit: v })}
        />
      </SettingRow>
      <SettingRow name={t("reasoning")}>
        <span class="tool-inline">
          {EFFORTS.map((e) => (
            <Switch
              key={e}
              label={e}
              checked={m.reasoning.includes(e)}
              onChange={(on) => {
                const reasoning = toggle(m.reasoning, e, on, EFFORTS);
                set({
                  reasoning,
                  default_reasoning: reasoning.includes(m.default_reasoning) ? m.default_reasoning : "",
                });
              }}
            />
          ))}
        </span>
      </SettingRow>
      <SettingRow name={t("defaultReasoning")}>
        <select
          aria-label={t("defaultReasoning")}
          value={m.default_reasoning}
          onChange={(e) => set({ default_reasoning: e.currentTarget.value })}
        >
          <option value="">{t("clientDefault")}</option>
          {m.reasoning.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      </SettingRow>
      <SettingRow name={t("modalities")}>
        <span class="tool-inline">
          {(["text", "image"] as const).map((x) => (
            <Switch
              key={x}
              label={t(x)}
              checked={m.input_modalities.includes(x)}
              onChange={(on) =>
                set({ input_modalities: toggle(m.input_modalities, x, on, ["text", "image", "audio"]) })
              }
            />
          ))}
        </span>
      </SettingRow>
      <SettingRow name={t("visible")}>
        <Switch label={t("visible")} checked={m.visible} onChange={(on) => set({ visible: on })} />
      </SettingRow>
      <SettingRow name={t("toolSearch")} description={t("toolSearchNote")}>
        <Switch label={t("toolSearch")} checked={m.tool_search} onChange={(on) => set({ tool_search: on })} />
      </SettingRow>
      <SettingRow
        name={t("systemPrompt")}
        description={t("systemPromptNote", { chars: m.system_prompt_chars ?? 0 })}
      >
        <textarea
          aria-label={t("systemPrompt")}
          rows={4}
          spellcheck={false}
          value={m.system_prompt ?? ""}
          onInput={(e) => set({ system_prompt: e.currentTarget.value })}
        />
      </SettingRow>
      <div class="tool-model-remove">
        <ConfirmButton
          question={t("removeAsk", { model: m.display_name || m.slug })}
          onConfirm={props.onRemove}
        >
          {t("removeModel")}
        </ConfirmButton>
      </div>
    </details>
  );
}

export function CodexPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "files" | "root">("default");
  const [configPath, setConfigPath] = useState("");
  const [catalogPath, setCatalogPath] = useState("");
  const [root, setRoot] = useState("");
  const [form, setForm] = useState<Form>(fresh);
  const [read, setRead] = useState<Form>(fresh);
  const [missing, setMissing] = useState<string[]>([]);
  const [appPath, setAppPath] = useState("");
  const selection =
    where === "files"
      ? { targets: [{ file: configPath.trim() }, { file: catalogPath.trim() }] }
      : where === "root"
        ? {
            targets: [
              { root: root.trim(), path: "config.toml" },
              { root: root.trim(), path: "pluxy-model-catalog.json" },
            ],
          }
        : {};
  const s = useToolSetup<Preview>({
    pkg: PACKAGE,
    name: t("title"),
    scan: "codex",
    selection,
    isDefault: where === "default",
    canRead:
      where === "default" ||
      (where === "files" ? configPath.trim() !== "" && catalogPath.trim() !== "" : root.trim() !== ""),
    settings: (p) => p.settings,
    onRead: (p) => {
      const f = fromFile(p.settings);
      setForm(f);
      setRead(f);
      // What an earlier import could not bring belongs to the list that was read before.
      setMissing([]);
    },
  });
  const start = useStart({ s, client: CLIENT, folder: true, recipe: { target: "cli" } });
  const shown = useShown();
  const edit = (patch: Partial<Form>) => {
    s.setDone(false);
    setForm((f) => ({ ...f, ...patch }));
  };
  const setAgents = (patch: Partial<Agents>) => edit({ agents: { ...form.agents, ...patch } });
  const imports = useAction(() => {});
  const doImport = () =>
    imports.run(async () => {
      const served = plainReply(await api.servedModels());
      if (!served.body) return served;
      const r = await s.call<{ models: Model[]; missing: string[] }>("import", {
        models: served.body.models.map((m) => ({
          slug: m.id,
          upstream: m.upstream || m.id,
          display_name: m.display_name ?? "",
          non_codex: m.extension !== "pluxy/codex",
        })),
      });
      if (r.body) {
        const { models: found, missing: none } = r.body;
        edit({
          models: [...form.models, ...found.filter((f) => !form.models.some((h) => h.slug === f.slug))],
        });
        setMissing(none);
      }
      return r;
    });
  const input = () => ({
    model: form.model.trim(),
    approval_model: form.approval,
    remote_compaction: form.compaction,
    agents: form.agents,
    models: form.models.map(({ system_prompt_chars: _, system_prompt, ...m }) => ({
      ...m,
      ...(system_prompt?.trim() ? { system_prompt: system_prompt.trim() } : {}),
    })),
  });
  const slugs = form.models.map((m) => m.slug);
  const held =
    form.models.length === 0
      ? [t("heldNoModels")]
      : slugs.includes(form.model.trim())
        ? []
        : [t("heldModel")];
  const dirty = JSON.stringify(form) !== JSON.stringify(read);
  const st =
    s.settings?.state === "refused"
      ? undefined
      : (s.settings as Exclude<ReadBack, { state: "refused" }> | undefined);
  // The default files as a person reads them (from ~), else the files read.
  const pinned = s.preview?.targets.map((x) => x.target) ?? [];
  const paths = [
    (where === "default" ? s.found?.config : undefined) ??
      (pinned.length > 0 ? pinned : ["~/.codex/config.toml"]),
  ].flat();
  const lines = st && (
    <>
      {st.provider !== "pluxy" && st.state !== "configured" && (
        <p class="muted">{t("providerChange", { provider: st.provider ?? "openai" })}</p>
      )}
      {st.profile && <Notice tone="warn">{t("profile", { profile: st.profile })}</Notice>}
    </>
  );

  return (
    <div class="tool-page" data-tool="codex">
      <PageHead title={t("title")} />
      <ToolTop
        s={s}
        start={start}
        facts={[s.found?.version]}
        paths={paths}
        held={held}
        form={input}
        lines={lines}
      />
      {s.addon === "on" && s.settings && s.settings.state !== "refused" && (
        <Section title={t("models")} aside={<SaveButton s={s} dirty={dirty} form={input} held={held} />}>
          <SettingRow name={t("model")} description={t("modelNote")}>
            <select
              aria-label={t("model")}
              value={form.model}
              onChange={(e) => edit({ model: e.currentTarget.value })}
            >
              {!slugs.includes(form.model) && (
                <option value={form.model}>{form.model || t("importFirst")}</option>
              )}
              {slugs.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </SettingRow>
          <SettingRow name={t("approval")} description={t("approvalNote")}>
            <select
              aria-label={t("approval")}
              value={form.approval}
              onChange={(e) => edit({ approval: e.currentTarget.value })}
            >
              <option value="">{t("approvalDefault")}</option>
              {[...slugs, ...(form.approval && !slugs.includes(form.approval) ? [form.approval] : [])].map(
                (m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ),
              )}
            </select>
            {form.approval && !slugs.includes(form.approval) && (
              <small class="tool-unserved">{t("approvalMissing")}</small>
            )}
          </SettingRow>
          <SettingRow name={t("compaction")} description={t("compactionNote")}>
            <Switch
              label={t("compaction")}
              checked={form.compaction}
              onChange={(on) => edit({ compaction: on })}
            />
          </SettingRow>
          <details class="tool-more tool-more-inner">
            <summary>{t("subagents")}</summary>
            <SettingRow name={t("subEnabled")} description={t("subEnabledNote")}>
              <Switch
                label={t("subEnabled")}
                checked={form.agents.enabled}
                onChange={(on) => setAgents({ enabled: on })}
              />
            </SettingRow>
            <SettingRow name={t("subThreads")} description={t("subThreadsNote")}>
              <NumberInput
                label={t("subThreads")}
                min={1}
                blank
                value={form.agents.max_threads}
                onInput={(v) => setAgents({ max_threads: v })}
              />
            </SettingRow>
            <SettingRow name={t("subModel")} description={t("subModelNote")}>
              <select
                aria-label={t("subModel")}
                value={form.agents.model}
                onChange={(e) => setAgents({ model: e.currentTarget.value })}
              >
                <option value="">{t("subDefault")}</option>
                {[
                  ...slugs,
                  ...(form.agents.model && !slugs.includes(form.agents.model) ? [form.agents.model] : []),
                ].map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </SettingRow>
            <SettingRow name={t("subEffort")}>
              <select
                aria-label={t("subEffort")}
                value={form.agents.effort}
                onChange={(e) => setAgents({ effort: e.currentTarget.value })}
              >
                <option value="">{t("subDefault")}</option>
                {EFFORTS.map((e) => (
                  <option key={e} value={e}>
                    {e}
                  </option>
                ))}
              </select>
            </SettingRow>
            <SettingRow name={t("subInterrupt")} description={t("subInterruptNote")}>
              <Switch
                label={t("subInterrupt")}
                checked={form.agents.interrupt}
                onChange={(on) => setAgents({ interrupt: on })}
              />
            </SettingRow>
          </details>
          <SettingRow name={t("catalog")} description={t("catalogNote")}>
            <span class="tool-inline">
              <span class="muted">{t("count", { count: form.models.length })}</span>
              <Button busy={imports.busy} onClick={doImport} disabled={s.busy || imports.busy || !s.core}>
                {t("importModels")}
              </Button>
            </span>
          </SettingRow>
          {imports.error && <Notice tone="danger">{shown.text(imports.error)}</Notice>}
          {missing.length > 0 && (
            <Notice tone="warn">{t("noTemplate", { models: missing.join(", ") })}</Notice>
          )}
          <div class="tool-models">
            {form.models.map((m, i) => (
              <ModelEditor
                key={m.slug}
                m={m}
                onChange={(n) => edit({ models: form.models.map((x, j) => (j === i ? n : x)) })}
                onRemove={() => edit({ models: form.models.filter((_, j) => j !== i) })}
              />
            ))}
          </div>
        </Section>
      )}
      {s.addon === "on" && (
        <StartSection s={s} start={start}>
          <SettingRow name={t("restartService")} description={t("restartNote")}>
            <ConfirmButton
              question={t("restartAsk")}
              disabled={start.busy || !start.on}
              onConfirm={() => start.go({ target: "service" }, {})}
            >
              {t("restartService")}
            </ConfirmButton>
          </SettingRow>
          <SettingRow name={t("app")} description={start.cwd.trim() ? t("appNote") : t("appCwd")}>
            <span class="tool-inline">
              <PathText label={t("app")} value={appPath} onInput={setAppPath} bare />
              <Button
                busy={start.busy}
                onClick={() =>
                  start.go({ target: "app", program: appPath.trim() }, { cwd: start.cwd.trim() })
                }
                disabled={start.busy || !start.cwd.trim() || !appPath.trim()}
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
          program="codex"
          files={
            <FileChoice
              s={s}
              value={where}
              onChange={setWhere}
              options={[
                { value: "default", label: fileDefault(), note: "~/.codex" },
                { value: "files", label: t("fileOther") },
                { value: "root", label: t("fileRoot") },
              ]}
            >
              {where === "files" && (
                <>
                  <Text
                    label={t("configPath")}
                    hint={t("pathHint")}
                    value={configPath}
                    onInput={setConfigPath}
                    disabled={s.locked}
                  />
                  <Text
                    label={t("catalogPath")}
                    hint={t("pathHint")}
                    value={catalogPath}
                    onInput={setCatalogPath}
                    disabled={s.locked}
                  />
                </>
              )}
              {where === "root" && (
                <Text
                  label={t("root")}
                  hint={t("rootHint")}
                  value={root}
                  onInput={setRoot}
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

export const codexPage = (): AgentPage => ({
  id: "codex",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  under: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <CodexPage />,
});
