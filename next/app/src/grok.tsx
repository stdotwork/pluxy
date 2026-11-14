// The app's Grok Build page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Grok Build, through the optional curated native tool pluxy/grok-setup
// (next/app-tools/grok-setup) and the launcher. The page is the window's own, not an extension
// page, because Apply takes the client key: the window names a vault secret's ID and the native
// confirmation of the apply names its label; an extension page can pass no secret. The key's value
// never reaches this webview: native key entry keeps it, and the tool's preview reads back only
// whether one is set. Labels follow the old app's Agents page.

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

const t = strings("grok", {
  title: "Grok Build",
  short: "Grok",
  tip: "Set up and start Grok Build with this core",
  intro:
    "Adds this core's models to Grok Build's config.toml, using the core's OpenAI Chat Completions API with a client key, and makes one of them Grok Build's default model. Apply, Clear and Restore Backup each ask in a native confirmation; comments and everything else in the file stay. Restart Grok Build afterward.",
  unavailable:
    "Install and enable the curated Grok Build tool (pluxy/grok-setup) on the Device extensions page to set up Grok Build from here.",
  file: "Configuration file",
  fileDefault: "Grok Build's user config",
  fileSelected: "A selected file",
  fileInRoot: "A file in a selected folder",
  filePath: "File",
  filePathHint:
    "A full path, for example $GROK_HOME/config.toml; a link (as stow makes) is followed and kept",
  root: "Folder",
  rootHint: "A full path; the folder may be a link (as chezmoi can make)",
  inRoot: "File in the folder",
  read: "Read configuration",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  defaultNow: "Default model",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not Configured",
  configured: "Configured",
  needsUpdate: "Configuration Modified",
  legacy:
    "config.toml still has {count} model entries an older app wrote (cpa-gui/…). pluxy leaves them as they are; remove them in Grok Build's config.toml if you no longer use them.",
  models: "Model catalog",
  modelsHint:
    "The core's models Grok Build lists, each with its context window (it drives auto-compaction). Entries pluxy wrote for models left out here are removed on Apply; your own entries stay.",
  refresh: "Refresh models",
  noModels: "No models yet: refresh them from the core.",
  window: "Context window",
  model: "Default model",
  modelHint: "The model Grok Build starts with; one of the selected models.",
  key: "Authentication Key",
  keyHint: "The client API key Grok Build uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "Grok Build client key",
  noKeys: "No keys yet: add one.",
  connect: "Connect in One Click",
  repair: "Repair Connection",
  update: "Update Configuration",
  clear: "Close configuration",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put the file back as it was before the first apply.",
  noCore: "Connect to a core first: its address goes into the configuration.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Agent program path",
  overrideHint: "A full path used instead of ~/.grok/bin/grok or grok found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start Grok Build",
});

const PACKAGE = "pluxy/grok-setup";
/** The context window an entry gets until the person sets another (the old app's default). */
const WINDOW = 200000;

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Entry {
  model: string;
  name: string | null;
  context_window: number | null;
}

interface ReadBack {
  state: "configured" | "not-configured" | "needs-update";
  base_url: string | null;
  key_set: boolean;
  default: string | null;
  model: string | null;
  models: Entry[];
  legacy: number;
}

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  settings: ReadBack;
}

/** One row of the catalog: a model, the name Grok Build shows, its window and whether it is kept. */
interface Row {
  model: string;
  name: string;
  window: number;
  on: boolean;
}

function Text(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  list?: string;
}) {
  const id = useId();
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      <input
        id={id}
        type="text"
        autocomplete="off"
        spellcheck={false}
        list={props.list}
        value={props.value}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function Num(props: { label: string; value: number; onInput: (v: number) => void; disabled?: boolean }) {
  const id = useId();
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      <input
        id={id}
        type="number"
        min={1}
        max={100000000}
        step={1000}
        disabled={props.disabled}
        value={String(props.value)}
        onInput={(e) => props.onInput(Number(e.currentTarget.value))}
      />
    </label>
  );
}

function StartCli(props: { available: boolean }) {
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  const action = useAction(state.reload);
  const recent = state.data?.history[t("title")] ?? [];
  const start = () =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", {});
      if (!recipe.body) return recipe;
      return reply("launcher_launch", {
        launch: {
          ...recipe.body,
          cwd: cwd.trim(),
          terminal,
          ...(override.trim() ? { override: override.trim() } : {}),
        },
      });
    });
  return (
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
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <Button variant="primary" onClick={start} disabled={action.busy || !props.available || !cwd.trim()}>
        {t("launch")}
      </Button>
    </Card>
  );
}

export function GrokPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("config.toml");
  const [preview, setPreview] = useState<Preview>();
  const [rows, setRows] = useState<Row[]>([]);
  const [model, setModel] = useState("");
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
    where === "file"
      ? { targets: [{ file: file.trim() }] }
      : where === "root"
        ? { targets: [{ root: root.trim(), path: inRoot.trim() }] }
        : {};
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) });
      if (r.body) {
        const st = r.body.settings;
        setPreview(r.body);
        setRows(
          st.models.map((m) => ({
            model: m.model,
            name: m.name ?? m.model,
            window: m.context_window ?? WINDOW,
            on: true,
          })),
        );
        setModel(st.model ?? "");
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the file the last preview pinned; a new preview first, so the pins are fresh.
  const pins = async () => {
    const r = await tool<Preview>("preview", selection());
    return r.body?.targets;
  };
  const fetchModels = useAction(() => {});
  // The core's models join the catalog (selected, as the old app wrote them all); rows already
  // there keep their window and choice.
  const refresh = () =>
    fetchModels.run(async () => {
      const r = await api.servedModels();
      if (r.body) {
        const served = r.body.models;
        setRows((have) => [
          ...have,
          ...served
            .filter((m) => !have.some((h) => h.model === m.id))
            .map((m) => ({ model: m.id, name: m.display_name || m.id, window: WINDOW, on: true })),
        ]);
      }
      return r;
    });
  const chosen = rows.filter((r) => r.on);
  const windowsOk = chosen.every((r) => Number.isInteger(r.window) && r.window >= 1 && r.window <= 100000000);
  const apply = () =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return {
          status: 400,
          error: { code: "refused", message: "the configuration file could not be read" },
        };
      return tool("apply", {
        targets,
        base_url: core,
        key: secret,
        model: model.trim(),
        models: chosen.map((r) => ({
          model: r.model,
          name: r.name === r.model ? "" : r.name,
          context_window: r.window,
        })),
      });
    });
  const undo = (op: "clear" | "restore") =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return {
          status: 400,
          error: { code: "refused", message: "the configuration file could not be read" },
        };
      return tool(op, { targets });
    });
  const addKey = useAction(secrets.reload);
  const st = preview?.settings;
  const applyLabel = !preview?.applied
    ? t("connect")
    : st?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || fetchModels.busy;
  const modelOk = chosen.some((r) => r.model === model.trim());

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{t("unavailable")}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <datalist id={listId}>
        {chosen.map((r) => (
          <option key={r.model} value={r.model} />
        ))}
      </datalist>
      <Card title={t("file")}>
        <Choices
          label={t("file")}
          value={where}
          choices={[
            { value: "default", label: t("fileDefault"), note: "~/.grok/config.toml" },
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
              [t("target"), preview.targets[0]?.target ?? ""],
              [t("defaultNow"), st.default ?? ""],
              [t("keySet"), st.key_set ? t("yes") : t("no")],
            ]}
          />
        )}
        {st && st.legacy > 0 && <Notice tone="warn">{t("legacy", { count: String(st.legacy) })}</Notice>}
      </Card>
      {preview && (
        <>
          <Card title={t("models")}>
            <p class="muted">{t("modelsHint")}</p>
            {fetchModels.error && <Notice tone="danger">{fetchModels.error}</Notice>}
            <Button onClick={refresh} disabled={busy || !core}>
              {t("refresh")}
            </Button>
            {rows.length === 0 && <p class="muted">{t("noModels")}</p>}
            {rows.map((r, i) => (
              <div key={r.model} class="row">
                <Switch
                  label={r.name === r.model ? r.model : `${r.name} (${r.model})`}
                  checked={r.on}
                  onChange={(on) => setRows(rows.map((x, j) => (j === i ? { ...x, on } : x)))}
                />
                <Num
                  label={t("window")}
                  value={r.window}
                  disabled={!r.on}
                  onInput={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, window: v } : x)))}
                />
              </div>
            ))}
            <Text label={t("model")} hint={t("modelHint")} value={model} list={listId} onInput={setModel} />
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
            {write.error && <Notice tone="danger">{write.error}</Notice>}
            <Button
              variant="primary"
              onClick={apply}
              disabled={busy || !secret || !core || !modelOk || !windowsOk}
            >
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
      <StartCli available={!unavailable} />
    </div>
  );
}

export const grokPage = (): PageDef => ({
  id: "grok-build",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <GrokPage />,
});
