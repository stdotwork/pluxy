// The app's Grok Build page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Grok Build, through the optional curated native tool pluxy/grok-setup
// (next/app-tools/grok-setup) and the launcher. The page is the window's own, not an extension
// page, because Apply takes the client key: the window names a vault secret's ID and the native
// confirmation of the apply names its label; an extension page can pass no secret. The key's value
// never reaches this webview: native key entry keeps it, and the tool's preview reads back only
// whether one is set. Labels follow the old app's Agents page.

import { useEffect, useId, useState } from "preact/hooks";
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
import { Backups } from "./backups";
import { NeedsKey, NoKeys, toolMissing, withDeviceLink } from "./clientHelp";
import { keysOf, type Secret, StartCli, Text, usePreview } from "./clientPage";
import type { AgentPage } from "./easyMode";
import {
  blocked,
  CLIENT,
  MODELS_MAX,
  modelOf,
  PACKAGE,
  type ReadBack,
  type Row,
  rowsOf,
  withServed,
} from "./grokForm";
import { connectionGet, reply } from "./host";

const t = strings("grok", {
  title: "Grok Build",
  tip: "Set up and start Grok Build with this core",
  intro:
    "Adds this core's models and a client key to Grok Build's config.toml and sets a default model. Apply, Clear, and Restore backup ask for confirmation and keep other settings. Restart Grok Build afterward.",
  unavailable:
    "Install and enable the curated Grok Build tool (pluxy/grok-setup) on the {device} page to set up Grok Build from here.",
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
  read: "Refresh",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  defaultNow: "Default model",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not configured",
  configured: "Configured",
  needsUpdate: "Configuration modified",
  legacy:
    "config.toml still has {count} model entries an older app wrote (cpa-gui/…). pluxy leaves them as they are; remove them in Grok Build's config.toml if you no longer use them.",
  models: "Model catalog",
  modelsHint:
    "The core's models Grok Build lists, each with its context window (it drives auto-compaction). Entries pluxy wrote for models left out here are removed on Apply; your own entries stay.",
  refresh: "Refresh models",
  noModels: "No models yet: refresh them from the core.",
  tooMany: "Grok Build's catalog takes at most {max} models from here: leave some out.",
  window: "Context window",
  model: "Default model",
  modelHint: "The model Grok Build starts with; one of the selected models.",
  refused: "Grok Build would not take this file",
  refusedHint: "Apply and Clear leave this file unchanged until you fix it; Restore backup reverts it.",
  key: "Client key",
  keyHint: "The client key Grok Build uses to reach this core.",
  addKey: "Add key…",
  keyLabel: "Grok Build client key",
  connect: "Apply",
  repair: "Repair",
  update: "Update",
  clear: "Clear",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore backup",
  restoreHint: "Put the file back as it was before the first apply.",
  noCore: "Connect to a core first: its address goes into the configuration.",
  start: "Start CLI",
  cwd: "Working folder",
  recent: "Recently used",
  override: "Agent program path",
  overrideHint: "A full path used instead of ~/.grok/bin/grok or grok found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start Grok Build",
});

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Preview {
  /** The pinned file: passed as it is to apply, clear and restore. */
  targets: { selected: string; target: string; link: string | null; [k: string]: unknown }[];
  applied: boolean;
  settings: ReadBack;
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

export function GrokPage() {
  const { api } = useKit();
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("config.toml");
  const [rows, setRows] = useState<Row[]>([]);
  const [model, setModel] = useState("");
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  const keys = keysOf(PACKAGE, secrets.data);
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
  // A write acts on the file the last preview pinned (the tool refuses it if the file changed
  // since); the preview belongs to the selection it was read for, so a write never pins a file unread.
  const selected = JSON.stringify(selection());
  const { preview, readFor } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await readFor(selected, () =>
        tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) }),
      );
      if (r.body) {
        setRows(rowsOf(r.body.settings));
        setModel(modelOf(r.body.settings));
      }
      return r;
    });
  const write = useAction(load);
  const fetchModels = useAction(() => {});
  // The core's models join the catalog (selected, as the old app wrote them all); rows already
  // there keep their window and choice.
  const refresh = () =>
    fetchModels.run(async () => {
      const r = await api.servedModels();
      const served = r.body?.models;
      if (served) setRows((have) => withServed(have, served));
      return r;
    });
  const chosen = rows.filter((r) => r.on);
  const apply = () =>
    write.run(() =>
      tool("apply", {
        targets: preview?.targets,
        base_url: core,
        key: secret,
        model: model.trim(),
        models: chosen.map((r) => ({
          model: r.model,
          name: r.name === r.model ? "" : r.name,
          context_window: r.window,
        })),
      }),
    );
  const undo = (op: "clear" | "restore") => write.run(() => tool(op, { targets: preview?.targets }));
  const addKey = useAction(secrets.reload);
  const st = preview?.settings;
  const refused = st?.state === "refused";
  const applyLabel = !preview?.applied
    ? t("connect")
    : st?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || fetchModels.busy;
  const held = blocked(rows, model);

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{withDeviceLink(t("unavailable"))}</Notice>}
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
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && st && (
          <Facts
            rows={[
              [
                t("status"),
                refused
                  ? t("refused")
                  : preview.applied
                    ? t("applied")
                    : st.state === "configured"
                      ? t("configured")
                      : st.state === "needs-update"
                        ? t("needsUpdate")
                        : t("notConfigured"),
              ],
              [t("target"), preview.targets[0]?.target ?? ""],
              ...(refused
                ? []
                : ([
                    [t("defaultNow"), st.default ?? ""],
                    [t("keySet"), st.key_set ? t("yes") : t("no")],
                  ] as [string, string][])),
            ]}
          />
        )}
        {refused && (
          <Notice tone="warn">
            {st?.reason} {t("refusedHint")}
          </Notice>
        )}
        {st && (st.legacy ?? 0) > 0 && (
          <Notice tone="warn">{t("legacy", { count: String(st.legacy) })}</Notice>
        )}
      </Card>
      {preview && !refused && (
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
            {held === "too-many" && <Notice tone="warn">{t("tooMany", { max: String(MODELS_MAX) })}</Notice>}
            <Text label={t("model")} hint={t("modelHint")} value={model} list={listId} onInput={setModel} />
          </Card>
          <Card title={t("key")}>
            <p class="muted">{t("keyHint")}</p>
            {secrets.error && <Notice tone="danger">{secrets.error}</Notice>}
            {keys.length > 0 ? (
              <Choices
                label={t("key")}
                value={secret || undefined}
                choices={keys.map((s) => ({ value: s.id, label: s.label }))}
                onChange={setSecret}
              />
            ) : (
              <NoKeys secrets={secrets} />
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
        </>
      )}
      {preview && (
        <Card>
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button
            variant="primary"
            onClick={apply}
            disabled={busy || refused || !secret || !core || held !== undefined}
          >
            {applyLabel}
          </Button>
          <NeedsKey missing={!secret} />
          <ConfirmButton disabled={busy || refused || !preview.applied} onConfirm={() => undo("clear")}>
            {t("clear")}
          </ConfirmButton>
          <small class="hint">{t("clearHint")}</small>
          <ConfirmButton disabled={busy} onConfirm={() => undo("restore")}>
            {t("restore")}
          </ConfirmButton>
          <small class="hint">{t("restoreHint")}</small>
        </Card>
      )}
      {preview && (
        <Backups pkg={PACKAGE} pins={async () => preview.targets} disabled={busy} onChange={load} />
      )}
      <StartCli
        available={!unavailable}
        client={CLIENT}
        recipe={() => tool<Record<string, unknown>>("recipe", {})}
        t={t}
      />
    </div>
  );
}

export const grokPage = (): AgentPage => ({
  id: "grok-build",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <GrokPage />,
});
