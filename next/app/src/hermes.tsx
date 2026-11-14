// The app's Hermes Agent page: Agent Configuration (Connect, Clear, Restore Backup) and Start CLI for
// Hermes Agent, through the optional curated native tool pluxy/hermes-setup (next/app-tools/
// hermes-setup) and the launcher. The page is the window's own, not an extension page, because
// Apply takes the client key: the window names a vault secret's ID and the native confirmation of
// the apply names its label; an extension page can pass no secret. The key's value never reaches
// this webview: native key entry keeps it, and the tool's preview reads back only whether one is
// set. Labels follow the old app's Agents page.

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
  useAction,
  useKit,
  useLoad,
} from "../../ui/kit/ui";
import { Backups, reply } from "./backups";
import { connectionGet } from "./host";

const t = strings("hermes", {
  title: "Hermes Agent",
  short: "Hermes",
  tip: "Set up and start Hermes Agent with this core",
  intro:
    "Uses pluxy's core as a custom provider: adds it to Hermes Agent's config.yaml with a client key, and makes a model through it Hermes Agent's default model. Connect in One Click, Clear and Restore Backup each ask in a native confirmation; comments and everything else in the file stay. Restart Hermes Agent afterward.",
  unavailable:
    "Install and enable the curated Hermes Agent tool (pluxy/hermes-setup) on the Device extensions page to set up Hermes Agent from here.",
  files: "Configuration file",
  fileDefault: "Hermes Agent's default home",
  fileDefaultNote: "~/.hermes (or its active profile)",
  fileFolder: "Hermes home folder (HERMES_HOME)",
  folder: "Folder",
  folderHint:
    "A full path, such as a profile's ~/.hermes/profiles/<name>; the folder may be a link (as chezmoi can make)",
  fileSelected: "A selected config file",
  configFile: "Config file",
  filesHint: "A full path; a link (as stow makes) is followed and kept",
  read: "Read settings",
  status: "Status",
  applied: "Applied",
  notConfigured: "Not Configured",
  configured: "Configured",
  needsUpdate: "Configuration Modified",
  address: "Address",
  keySet: "Key in the file",
  provider: "Provider",
  yes: "Yes",
  no: "No",
  none: "None",
  legacy:
    "config.yaml still has the cpa-gui provider the old desktop app wrote. pluxy leaves it as it is; remove it in Hermes Agent's config.yaml if you no longer use it.",
  model: "Model",
  modelHint: "The default model Hermes Agent starts with, through this core.",
  modelLabel: "Default model",
  refresh: "Refresh models",
  invalidModel: "Enter one model ID, without spaces or commas.",
  key: "Authentication Key",
  keyHint: "The client API key Hermes Agent uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "Hermes Agent client key",
  noKeys: "No keys yet: add one.",
  connect: "Connect in One Click",
  repair: "Repair Connection",
  update: "Update Configuration",
  clear: "Clear integration",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart Hermes Agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put the file back as it was before the first apply.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Agent program path",
  overrideHint: "A full path used instead of hermes (optional)",
  terminal: "Terminal",
  launch: "Start Hermes Agent",
});

const PACKAGE = "pluxy/hermes-setup";

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface HermesConfig {
  state: "configured" | "not-configured" | "needs-update";
  base_url: string | null;
  key_set: boolean;
  provider: string | null;
  model: string | null;
  legacy: boolean;
  [k: string]: unknown;
}

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  config: HermesConfig;
}

/** A model ID the provider can take: one word, not starting with @ or *. */
const validModel = (m: string) => m.length > 0 && !/[\s,]/.test(m) && !/^[@*]/.test(m);

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

export function HermesPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "folder" | "file">("default");
  const [folder, setFolder] = useState("");
  const [configFile, setConfigFile] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [model, setModel] = useState("");
  const [served, setServed] = useState<string[]>([]);
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
    where === "folder"
      ? { folder: folder.trim() }
      : where === "file"
        ? { targets: [{ file: configFile.trim() }] }
        : {};
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) });
      if (r.body) {
        setPreview(r.body);
        setModel(r.body.config.model ?? "");
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the file the last preview pinned; a new preview first, so the pin is fresh.
  const pins = async () => {
    const r = await tool<Preview>("preview", selection());
    return r.body?.targets;
  };
  const fetchModels = useAction(() => {});
  const refresh = () =>
    fetchModels.run(async () => {
      const r = await api.servedModels();
      if (r.body) setServed(r.body.models.map((m) => m.id));
      return r;
    });
  const apply = () =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return {
          status: 400,
          error: { code: "refused", message: "the configuration file could not be read" },
        };
      return tool("apply", { targets, base_url: core, key: secret, model: model.trim() });
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
  const cfg = preview?.config;
  const applyLabel = !preview?.applied
    ? t("connect")
    : cfg?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy;

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{t("unavailable")}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <Card title={t("files")}>
        <Choices
          label={t("files")}
          value={where}
          choices={[
            { value: "default", label: t("fileDefault"), note: t("fileDefaultNote") },
            { value: "folder", label: t("fileFolder") },
            { value: "file", label: t("fileSelected") },
          ]}
          onChange={setWhere}
        />
        {where === "folder" && (
          <Text label={t("folder")} hint={t("folderHint")} value={folder} onInput={setFolder} />
        )}
        {where === "file" && (
          <Text label={t("configFile")} hint={t("filesHint")} value={configFile} onInput={setConfigFile} />
        )}
        {read.error && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && cfg && (
          <Facts
            rows={[
              [
                t("status"),
                preview.applied
                  ? t("applied")
                  : cfg.state === "configured"
                    ? t("configured")
                    : cfg.state === "needs-update"
                      ? t("needsUpdate")
                      : t("notConfigured"),
              ],
              [t("configFile"), preview.targets[0]?.target ?? ""],
              [t("address"), cfg.base_url ?? t("none")],
              [t("keySet"), cfg.key_set ? t("yes") : t("no")],
              [t("provider"), cfg.provider ?? t("none")],
            ]}
          />
        )}
        {cfg?.legacy && <Notice tone="warn">{t("legacy")}</Notice>}
      </Card>
      {preview && (
        <>
          <Card title={t("model")}>
            <p class="muted">{t("modelHint")}</p>
            <datalist id={listId}>
              {served.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
            <Text label={t("modelLabel")} value={model} list={listId} onInput={setModel} />
            {!validModel(model.trim()) && <Notice tone="warn">{t("invalidModel")}</Notice>}
            {fetchModels.error && <Notice tone="danger">{fetchModels.error}</Notice>}
            <Button onClick={refresh} disabled={fetchModels.busy || busy}>
              {t("refresh")}
            </Button>
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
              disabled={busy || !validModel(model.trim()) || !secret || !core}
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

export const hermesPage = (): PageDef => ({
  id: "hermes",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <HermesPage />,
});
