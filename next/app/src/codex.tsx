// The app's Codex page: Agent Configuration (Apply, Clear, Restore Backup) of Codex's config.toml
// and its model catalog, the catalog's models (imported from the core on request, then edited),
// and Start CLI, Start App and Restart CLI Service, through the optional curated native tool
// pluxy/codex-setup (next/app-tools/codex) and the launcher. The page is the window's own, not an
// extension page, because Apply takes the client key: the window names a vault secret's ID and the
// native confirmation of the apply names its label; an extension page can pass no secret. The
// key's value never reaches this webview. Labels follow the old app's Agents page.

import { useEffect, useId, useState } from "preact/hooks";
import type { PageDef } from "../../ui/kit/shell";
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
import { Backups, reply } from "./backups";
import { connectionGet } from "./host";

const t = strings("codex", {
  title: "Codex",
  short: "Codex",
  tip: "Set up and start Codex with this core",
  intro:
    "Points Codex's config.toml at this core through the Responses API, with a client key, and writes the model catalog Codex lists. Apply, Clear and Restore Backup each ask in a native confirmation; comments and everything else in the files stay. Restart Codex afterward.",
  unavailable:
    "Install and enable the curated Codex tool (pluxy/codex-setup) on the Device extensions page to set up Codex from here.",
  files: "Configuration files",
  filesDefault: "Codex's folder",
  filesSelected: "Selected files",
  filesInRoot: "Files in a selected folder",
  configPath: "config.toml",
  catalogPath: "Model catalog",
  pathHint: "A full path; a link (as stow makes) is followed and kept",
  root: "Folder",
  rootHint: "A full path, such as your CODEX_HOME; the folder may be a link (as chezmoi can make)",
  read: "Read configuration",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  provider: "Provider now",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not Configured",
  configured: "Configured",
  needsUpdate: "Configuration Modified",
  providerChange:
    "Codex lists past conversations by provider. After Apply, conversations started with {provider} stay hidden from resume until their history is synced; history sync is offered in session management and is never done automatically.",
  profile: "The active Codex profile {profile} may override these settings.",
  model: "Model",
  modelHint: "The model Codex starts with; one of the catalog's models.",
  approval: "Approval model",
  approvalHint:
    "Used for Codex automatic approval requests. Codex default restores built-in selection; a selected model must be callable through the current proxy.",
  approvalDefault: "Codex default",
  approvalMissing:
    "This model is no longer listed. The setting is preserved; verify that the proxy supports it.",
  compaction: "Remote compaction (experimental)",
  compactionHint:
    "Off by default. Compact context on the server; requires a compatible upstream route. Turn off if compaction fails. Restart Codex after saving.",
  subagents: "Codex subagent settings",
  subagentsHint: "Configure the defaults Codex uses when spawning subagents.",
  subEnabled: "Enable subagents",
  subEnabledHint: "Allow Codex to use multi-agent tools to spawn subagents.",
  subThreads: "Maximum concurrent threads per session",
  subThreadsHint: "Empty: use Codex default",
  subModel: "Default subagent model",
  subModelHint: "Empty: inherit parent setting",
  subEffort: "Default reasoning effort",
  subDefault: "Use Codex default",
  subInterrupt: "Include interruption messages",
  subInterruptHint: "Include a model-visible message when a subagent is interrupted.",
  catalog: "Codex model catalog",
  catalogHint:
    "Import the core's models, then customize their parameters; Apply saves them to the Codex catalog. Models without a Codex template are not listed.",
  importModels: "Import models from core",
  noTemplate: "No Codex template for: {models}",
  noModels: "No models yet: import them from the core.",
  displayName: "Display name",
  description: "Description",
  context: "Context window (tokens)",
  maximum: "Maximum context window (tokens)",
  percent: "Effective context (%)",
  compact: "Auto-compaction threshold (tokens)",
  automatic: "Leave blank for automatic",
  reasoning: "Available reasoning levels",
  defaultReasoning: "Default reasoning level",
  clientDefault: "Let the client decide",
  modalities: "Input types",
  text: "Text",
  image: "Image",
  visible: "Show in the Codex model list",
  toolSearch: "Allow Tool Search",
  toolSearchHint: "Requires model and upstream provider support; disable if incompatible.",
  systemPrompt: "System prompt",
  systemPromptHint:
    "Replaces this model's system prompt when you enter one ({chars} characters now); leave empty to keep it.",
  removeModel: "Remove model",
  key: "Authentication Key",
  keyHint: "The client API key Codex uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "Codex client key",
  noKeys: "No keys yet: add one.",
  apply: "Apply Configuration",
  update: "Update Configuration",
  repair: "Repair Connection",
  clear: "Close configuration",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put both files back as they were before the first apply.",
  noCore: "Connect to a core first: its address goes into the configuration.",
  modelMissing: "Choose a model from the catalog.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Agent program path",
  overrideHint: "A full path used instead of codex found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start Codex CLI",
  app: "Start App",
  appPath: "Codex App program",
  appHint: "Codex has no Linux app of its own: the full path of the program to start",
  launchApp: "Start Codex App",
  restartCli: "Restart CLI Service",
  restartCliMessage:
    "Restarting loads the latest configuration and model list. All active CLI sessions, including those in other terminals, will be interrupted. Wait for tasks to finish before restarting.",
});

const PACKAGE = "pluxy/codex-setup";
const EFFORTS = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"] as const;

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Model {
  slug: string;
  upstream?: string;
  non_codex?: boolean;
  display_name: string;
  description: string;
  context_window: number | null;
  max_context_window: number | null;
  effective_context_window_percent: number | null;
  auto_compact_token_limit: number | null;
  reasoning: string[];
  default_reasoning: string;
  input_modalities: string[];
  visible: boolean;
  tool_search: boolean;
  system_prompt_chars?: number;
  system_prompt?: string;
}

interface Agents {
  enabled: boolean;
  max_threads: number | null;
  model: string;
  effort: string;
  interrupt: boolean;
}

interface ReadBack {
  state: "configured" | "not-configured" | "needs-update";
  key_set: boolean;
  provider: string | null;
  model: string | null;
  profile: string | null;
  remote_compaction: boolean;
  agents: Agents;
  models: Model[];
  approval_model: string;
}

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  settings: ReadBack;
}

function Text(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  list?: string;
  area?: boolean;
}) {
  const id = useId();
  const input = (e: { currentTarget: { value: string } }) => props.onInput(e.currentTarget.value);
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      {props.area ? (
        <textarea id={id} rows={4} spellcheck={false} value={props.value} onInput={input} />
      ) : (
        <input
          id={id}
          type="text"
          autocomplete="off"
          spellcheck={false}
          list={props.list}
          value={props.value}
          onInput={input}
        />
      )}
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

/** A whole number, or blank (null). */
function Num(props: {
  label: string;
  hint?: string;
  value: number | null;
  onInput: (v: number | null) => void;
}) {
  const id = useId();
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      <input
        id={id}
        type="number"
        min={1}
        step={1}
        value={props.value === null ? "" : String(props.value)}
        onInput={(e) => {
          const v = e.currentTarget.value.trim();
          props.onInput(v === "" ? null : Number(v));
        }}
      />
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function ModelEditor(props: { m: Model; onChange: (m: Model) => void; onRemove: () => void }) {
  const { m } = props;
  const set = (patch: Partial<Model>) => props.onChange({ ...m, ...patch });
  const toggle = (list: string[], v: string, on: boolean, order: readonly string[]) =>
    order.filter((x) => (x === v ? on : list.includes(x)));
  return (
    <details>
      <summary>
        {m.display_name} <small class="muted">{m.slug}</small>
      </summary>
      <Text label={t("displayName")} value={m.display_name} onInput={(v) => set({ display_name: v })} />
      <Text label={t("description")} value={m.description} onInput={(v) => set({ description: v })} />
      <Num label={t("context")} value={m.context_window} onInput={(v) => set({ context_window: v })} />
      <Num
        label={t("maximum")}
        value={m.max_context_window}
        onInput={(v) => set({ max_context_window: v })}
      />
      <Num
        label={t("percent")}
        value={m.effective_context_window_percent}
        onInput={(v) => set({ effective_context_window_percent: v })}
      />
      <Num
        label={t("compact")}
        hint={t("automatic")}
        value={m.auto_compact_token_limit}
        onInput={(v) => set({ auto_compact_token_limit: v })}
      />
      <span class="label">{t("reasoning")}</span>
      <div class="row">
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
      </div>
      <Choices
        label={t("defaultReasoning")}
        value={m.default_reasoning}
        choices={[
          { value: "", label: t("clientDefault") },
          ...m.reasoning.map((r) => ({ value: r, label: r })),
        ]}
        onChange={(v) => set({ default_reasoning: v })}
      />
      <span class="label">{t("modalities")}</span>
      <div class="row">
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
      </div>
      <Switch label={t("visible")} checked={m.visible} onChange={(on) => set({ visible: on })} />
      <Switch
        label={t("toolSearch")}
        hint={t("toolSearchHint")}
        checked={m.tool_search}
        onChange={(on) => set({ tool_search: on })}
      />
      <Text
        label={t("systemPrompt")}
        hint={t("systemPromptHint", { chars: m.system_prompt_chars ?? 0 })}
        value={m.system_prompt ?? ""}
        area
        onInput={(v) => set({ system_prompt: v })}
      />
      <Button onClick={props.onRemove}>{t("removeModel")}</Button>
    </details>
  );
}

/** Start CLI, Start App and Restart CLI Service, through the launcher. */
function Launch(props: { available: boolean }) {
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const [appPath, setAppPath] = useState("");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  const action = useAction(state.reload);
  const recent = state.data?.history.Codex ?? [];
  const go = (target: "cli" | "app" | "service", extra: Record<string, unknown>, program?: string) =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", {
        target,
        ...(program ? { program } : {}),
      });
      if (!recipe.body) return recipe;
      return reply("launcher_launch", { launch: { ...recipe.body, ...extra } });
    });
  return (
    <>
      <Card title={t("start")}>
        <Text label={t("cwd")} value={cwd} onInput={setCwd} />
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
            value={terminal}
            choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
            onChange={setTerminal}
          />
        )}
        {terminals.error && <Notice tone="warn">{terminals.error}</Notice>}
        <Button
          variant="primary"
          onClick={() =>
            go("cli", {
              cwd: cwd.trim(),
              terminal,
              ...(override.trim() ? { override: override.trim() } : {}),
            })
          }
          disabled={action.busy || !props.available || !cwd.trim()}
        >
          {t("launch")}
        </Button>
        <p class="muted">{t("restartCliMessage")}</p>
        <ConfirmButton disabled={action.busy || !props.available} onConfirm={() => go("service", {})}>
          {t("restartCli")}
        </ConfirmButton>
      </Card>
      <Card title={t("app")}>
        <Text label={t("appPath")} hint={t("appHint")} value={appPath} onInput={setAppPath} />
        <Button
          onClick={() => go("app", { cwd: cwd.trim() }, appPath.trim())}
          disabled={action.busy || !props.available || !cwd.trim() || !appPath.trim()}
        >
          {t("launchApp")}
        </Button>
      </Card>
      {action.error && <Notice tone="danger">{action.error}</Notice>}
    </>
  );
}

const freshAgents = (): Agents => ({
  enabled: true,
  max_threads: null,
  model: "",
  effort: "",
  interrupt: true,
});

export function CodexPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "files" | "root">("default");
  const [configPath, setConfigPath] = useState("");
  const [catalogPath, setCatalogPath] = useState("");
  const [root, setRoot] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [model, setModel] = useState("");
  const [approval, setApproval] = useState("");
  const [compaction, setCompaction] = useState(false);
  const [agents, setAgents] = useState<Agents>(freshAgents);
  const [models, setModels] = useState<Model[]>([]);
  const [missing, setMissing] = useState<string[]>([]);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ id: string; label: string }[]>("vault_secrets"));
  const listId = useId();
  useEffect(() => {
    connectionGet().then(
      (c) => setCore(c.signed_in ? c.target : undefined),
      () => setCore(undefined),
    );
  }, []);
  const selection = () =>
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
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) });
      if (r.body) {
        const st = r.body.settings;
        setPreview(r.body);
        setModel(st.model ?? "");
        setApproval(st.approval_model);
        setCompaction(st.remote_compaction);
        setAgents(st.agents);
        setModels(st.models);
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the files the last preview pinned; a new preview first, so the pins are fresh.
  const pins = async () => (await tool<Preview>("preview", selection())).body?.targets;
  const importModels = useAction(() => {});
  const doImport = () =>
    importModels.run(async () => {
      const served = await api.servedModels();
      if (!served.body) return served;
      const r = await tool<{ models: Model[]; missing: string[] }>("import", {
        models: served.body.models.map((m) => ({
          slug: m.id,
          upstream: m.upstream || m.id,
          display_name: m.display_name ?? "",
          non_codex: m.extension !== "pluxy/codex",
        })),
      });
      if (r.body) {
        const { models: found, missing: none } = r.body;
        setModels((have) => [...have, ...found.filter((f) => !have.some((h) => h.slug === f.slug))]);
        setMissing(none);
      }
      return r;
    });
  const apply = () =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return {
          status: 400,
          error: { code: "refused", message: "the configuration files could not be read" },
        };
      return tool("apply", {
        targets,
        base_url: core,
        key: secret,
        model: model.trim(),
        approval_model: approval,
        remote_compaction: compaction,
        agents,
        models: models.map(({ system_prompt_chars: _, system_prompt, ...m }) => ({
          ...m,
          ...(system_prompt?.trim() ? { system_prompt: system_prompt.trim() } : {}),
        })),
      });
    });
  const undo = (op: "clear" | "restore") =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return {
          status: 400,
          error: { code: "refused", message: "the configuration files could not be read" },
        };
      return tool(op, { targets });
    });
  const addKey = useAction(secrets.reload);
  const st = preview?.settings;
  const applyLabel = !preview?.applied ? t("apply") : st?.state === "configured" ? t("update") : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || importModels.busy;
  const modelOk = models.some((m) => m.slug === model.trim());
  const slugs = models.map((m) => m.slug);

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{t("unavailable")}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <datalist id={listId}>
        {slugs.map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
      <Card title={t("files")}>
        <Choices
          label={t("files")}
          value={where}
          choices={[
            { value: "default", label: t("filesDefault"), note: "~/.codex" },
            { value: "files", label: t("filesSelected") },
            { value: "root", label: t("filesInRoot") },
          ]}
          onChange={setWhere}
        />
        {where === "files" && (
          <>
            <Text label={t("configPath")} hint={t("pathHint")} value={configPath} onInput={setConfigPath} />
            <Text
              label={t("catalogPath")}
              hint={t("pathHint")}
              value={catalogPath}
              onInput={setCatalogPath}
            />
          </>
        )}
        {where === "root" && <Text label={t("root")} hint={t("rootHint")} value={root} onInput={setRoot} />}
        {read.error && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && st && (
          <Facts
            rows={[
              [
                t("status"),
                preview.applied
                  ? t("applied")
                  : st.state === "configured"
                    ? t("configured")
                    : st.state === "needs-update"
                      ? t("needsUpdate")
                      : t("notConfigured"),
              ],
              [t("target"), preview.targets.map((x) => x.target).join(", ")],
              [t("keySet"), st.key_set ? t("yes") : t("no")],
              [t("provider"), st.provider ?? "openai"],
            ]}
          />
        )}
        {st && st.provider !== "pluxy" && (
          <Notice tone="warn">{t("providerChange", { provider: st.provider ?? "openai" })}</Notice>
        )}
        {st?.profile && <Notice tone="warn">{t("profile", { profile: st.profile })}</Notice>}
      </Card>
      {preview && (
        <>
          <Card title={t("model")}>
            <Text label={t("model")} hint={t("modelHint")} value={model} list={listId} onInput={setModel} />
            <Choices
              label={t("approval")}
              value={approval}
              choices={[
                { value: "", label: t("approvalDefault") },
                ...[...slugs, ...(approval && !slugs.includes(approval) ? [approval] : [])].map((s) => ({
                  value: s,
                  label: s,
                })),
              ]}
              onChange={setApproval}
            />
            <small class="hint">{t("approvalHint")}</small>
            {approval && !slugs.includes(approval) && <Notice tone="warn">{t("approvalMissing")}</Notice>}
            <Switch
              label={t("compaction")}
              hint={t("compactionHint")}
              checked={compaction}
              onChange={setCompaction}
            />
            <details>
              <summary>{t("subagents")}</summary>
              <p class="muted">{t("subagentsHint")}</p>
              <Switch
                label={t("subEnabled")}
                hint={t("subEnabledHint")}
                checked={agents.enabled}
                onChange={(on) => setAgents({ ...agents, enabled: on })}
              />
              <Num
                label={t("subThreads")}
                hint={t("subThreadsHint")}
                value={agents.max_threads}
                onInput={(v) => setAgents({ ...agents, max_threads: v })}
              />
              <Text
                label={t("subModel")}
                hint={t("subModelHint")}
                value={agents.model}
                list={listId}
                onInput={(v) => setAgents({ ...agents, model: v })}
              />
              <Choices
                label={t("subEffort")}
                value={agents.effort}
                choices={[
                  { value: "", label: t("subDefault") },
                  ...EFFORTS.map((e) => ({ value: e, label: e })),
                ]}
                onChange={(v) => setAgents({ ...agents, effort: v })}
              />
              <Switch
                label={t("subInterrupt")}
                hint={t("subInterruptHint")}
                checked={agents.interrupt}
                onChange={(on) => setAgents({ ...agents, interrupt: on })}
              />
            </details>
          </Card>
          <Card title={t("catalog")}>
            <p class="muted">{t("catalogHint")}</p>
            <Button onClick={doImport} disabled={busy || !core}>
              {t("importModels")}
            </Button>
            {importModels.error && <Notice tone="danger">{importModels.error}</Notice>}
            {missing.length > 0 && (
              <Notice tone="warn">{t("noTemplate", { models: missing.join(", ") })}</Notice>
            )}
            {models.length === 0 && <p class="muted">{t("noModels")}</p>}
            {models.map((m, i) => (
              <ModelEditor
                key={m.slug}
                m={m}
                onChange={(n) => setModels(models.map((x, j) => (j === i ? n : x)))}
                onRemove={() => setModels(models.filter((_, j) => j !== i))}
              />
            ))}
          </Card>
          <Card title={t("key")}>
            <p class="muted">{t("keyHint")}</p>
            {secrets.data && secrets.data.length > 0 ? (
              <Choices
                label={t("key")}
                value={secret || undefined}
                choices={secrets.data.map((s) => ({ value: s.id, label: s.label }))}
                onChange={setSecret}
              />
            ) : (
              <p class="muted">{t("noKeys")}</p>
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
          <Card>
            {!modelOk && <Notice tone="warn">{t("modelMissing")}</Notice>}
            {write.error && <Notice tone="danger">{write.error}</Notice>}
            <Button variant="primary" onClick={apply} disabled={busy || !secret || !core || !modelOk}>
              {applyLabel}
            </Button>
            <ConfirmButton disabled={busy || !preview.applied} onConfirm={() => undo("clear")}>
              {t("clear")}
            </ConfirmButton>
            <small class="hint">{t("clearHint")}</small>
            <ConfirmButton disabled={busy} onConfirm={() => undo("restore")}>
              {t("restore")}
            </ConfirmButton>
            <small class="hint">{t("restoreHint")}</small>
          </Card>
          <Backups pkg={PACKAGE} pins={pins} disabled={busy} onChange={load} />
        </>
      )}
      <Launch available={!unavailable} />
    </div>
  );
}

export const codexPage = (): PageDef => ({
  id: "codex",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <CodexPage />,
});
