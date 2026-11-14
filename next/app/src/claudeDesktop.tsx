// The app's Claude Desktop page: Agent Configuration (Apply, Clear, Restore Backup) and Start
// Claude Desktop, through the optional curated native tool pluxy/claude-desktop
// (next/app-tools/claude-desktop) and the launcher. Like the Claude Code page it is the window's
// own, not an extension page, because Apply takes the client key: the window names a vault
// secret's ID and the native confirmation of the apply names its label. The key's value never
// reaches this webview. Labels follow the old app's Agents page.

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
  useLoad,
} from "../../ui/kit/ui";
import { Backups, reply } from "./backups";
import { connectionGet } from "./host";

const t = strings("claudeDesktop", {
  title: "Claude Desktop",
  short: "Desk",
  tip: "Set up and start Claude Desktop with this core",
  intro:
    "Uses this core as Claude Desktop's third-party inference gateway (Linux beta): its sign-in choice, a gateway profile with a client key and the models Claude Desktop lists, and that profile applied. Apply, Clear and Restore Backup each ask in a native confirmation; everything else in the files stays. Quit and reopen Claude Desktop afterward: it reads its configuration at launch.",
  unavailable:
    "Install and enable the curated Claude Desktop tool (pluxy/claude-desktop) on the Device extensions page to set up Claude Desktop from here.",
  files: "Configuration files",
  filesDefault: "Claude Desktop's third-party folder",
  filesInRoot: "A selected folder",
  filesSelected: "Selected files",
  root: "Folder",
  rootHint:
    "Claude Desktop's third-party folder (Claude-3p), as a full path; it may be a link (as chezmoi can make)",
  deployment: "claude_desktop_config.json",
  profile: "Gateway profile",
  meta: "configLibrary/_meta.json",
  fileHint: "A full path; a link (as stow makes) is followed and kept",
  read: "Read configuration",
  missing:
    "Claude Desktop's configuration library does not exist yet. Create it here, or save a configuration in Claude Desktop (Developer → Configure Third-Party Inference…).",
  createFolders: "Create configuration folder",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the profile",
  yes: "Yes",
  no: "No",
  applied: "Current integration: pluxy",
  notConfigured: "Current integration: pluxy not configured",
  needsUpdate: "Current integration: pluxy configuration needs repair",
  mapping: "Claude Desktop Models and Aliases",
  mappingHint:
    "Claude Desktop only allows Claude models, so non-Claude models need aliases that follow Claude model naming. If the original model name already belongs to the Claude family, no alias mapping is needed; leave the alias blank. The first model is Claude Desktop's default.",
  aliasCore:
    "Claude Desktop sends the alias to this core: add it to the account or extension serving the original model (its model_alias) so the core serves the model under that name.",
  aliasRule:
    "Aliases can follow patterns such as claude-opus-*, claude-sonnet-*, or claude-haiku-*, but must not contain names of other model families such as gpt, grok, gemini, or deepseek.",
  addModel: "Add model",
  removeModel: "Remove model",
  empty: "Click “Add model” and enter the original model. The alias is optional.",
  model: "Original model",
  alias: "Alias (optional for Claude models)",
  context1m: "1M",
  context1mHint: "Configures a 1M context window. Make sure the selected upstream model supports it.",
  key: "Authentication Key",
  keyHint: "The client API key Claude Desktop uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "Claude Desktop client key",
  noKeys: "No keys yet: add one.",
  connect: "Connect in One Click",
  repair: "Repair Connection",
  update: "Update Configuration",
  clear: "Clear integration",
  clearHint: "Remove pluxy integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put the files back as they were before the first apply.",
  noCore: "Connect to a core first: its address goes into the gateway profile.",
  start: "Run controls",
  override: "Agent program path",
  overrideHint: "A full path used instead of claude-desktop found on the PATH (optional)",
  launch: "Start Claude Desktop",
});

const PACKAGE = "pluxy/claude-desktop";
const PROFILE_ID = "00000000-0000-4000-8000-000000831700";
const FILES = ["claude_desktop_config.json", `configLibrary/${PROFILE_ID}.json`, "configLibrary/_meta.json"];
/** The old app's default rows: Claude Desktop's own model families, as aliases. */
const SUGGESTED = [
  "claude-opus-5-5",
  "claude-sonnet-5-5",
  "claude-haiku-5-5",
  "claude-fable-5-2",
  "claude-opus-5",
  "claude-sonnet-5",
  "claude-haiku-4-5",
];

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Row {
  model: string;
  alias: string;
  one_m: boolean;
}

interface ReadBack {
  state: "configured" | "not-configured" | "needs-update";
  base_url: string | null;
  key_set: boolean;
  models: Row[];
}

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  settings: ReadBack;
}

const fresh = (): Row[] => [
  { model: "claude-opus-5-5", alias: "", one_m: false },
  { model: "claude-sonnet-5-5", alias: "", one_m: false },
  { model: "claude-haiku-5-5", alias: "", one_m: false },
];

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

function StartApp(props: { available: boolean }) {
  const [override, setOverride] = useState("");
  const action = useAction(() => {});
  const start = () =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", {});
      if (!recipe.body) return recipe;
      return reply("launcher_launch", {
        launch: { ...recipe.body, ...(override.trim() ? { override: override.trim() } : {}) },
      });
    });
  return (
    <Card title={t("start")}>
      <Text label={t("override")} hint={t("overrideHint")} value={override} onInput={setOverride} />
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <Button variant="primary" onClick={start} disabled={action.busy || !props.available}>
        {t("launch")}
      </Button>
    </Card>
  );
}

export function ClaudeDesktopPage() {
  const [where, setWhere] = useState<"default" | "root" | "files">("default");
  const [root, setRoot] = useState("");
  const [files, setFiles] = useState<string[]>(["", "", ""]);
  const [preview, setPreview] = useState<Preview>();
  const [missing, setMissing] = useState(false);
  const [rows, setRows] = useState<Row[]>(fresh);
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
    where === "root"
      ? { targets: FILES.map((path) => ({ root: root.trim(), path })) }
      : where === "files"
        ? { targets: files.map((f) => ({ file: f.trim() })) }
        : {};
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) });
      setMissing(where === "default" && !!r.error?.message.includes("configLibrary"));
      if (r.body) {
        setPreview(r.body);
        if (r.body.settings.models.length > 0) setRows(r.body.settings.models);
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the files the last preview pinned; a new preview first, so the pins are fresh.
  const pins = async () => {
    const r = await tool<Preview>("preview", selection());
    return r.body?.targets;
  };
  const refused = { status: 400, error: { code: "refused", message: "the files could not be read" } };
  const apply = () =>
    write.run(async () => {
      const targets = await pins();
      if (!targets) return refused;
      return tool("apply", {
        targets,
        base_url: core,
        key: secret,
        models: rows.map((r) => ({ model: r.model.trim(), alias: r.alias.trim(), one_m: r.one_m })),
      });
    });
  const undo = (op: "clear" | "restore") =>
    write.run(async () => {
      const targets = await pins();
      if (!targets) return refused;
      return tool(op, { targets });
    });
  const folders = useAction(load);
  const addKey = useAction(secrets.reload);
  const setRow = (i: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const st = preview?.settings;
  const applyLabel = !preview?.applied
    ? t("connect")
    : st?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || folders.busy;
  const rowsOk = rows.length > 0 && rows.every((r) => r.model.trim());

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{t("unavailable")}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <datalist id={listId}>
        {SUGGESTED.map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
      <Card title={t("files")}>
        <Choices
          label={t("files")}
          value={where}
          choices={[
            { value: "default", label: t("filesDefault"), note: "~/.config/Claude-3p" },
            { value: "root", label: t("filesInRoot") },
            { value: "files", label: t("filesSelected") },
          ]}
          onChange={setWhere}
        />
        {where === "root" && <Text label={t("root")} hint={t("rootHint")} value={root} onInput={setRoot} />}
        {where === "files" &&
          [t("deployment"), t("profile"), t("meta")].map((label, i) => (
            <Text
              key={label}
              label={label}
              hint={t("fileHint")}
              value={files[i] ?? ""}
              onInput={(v) => setFiles((fs) => fs.map((f, j) => (j === i ? v : f)))}
            />
          ))}
        {read.error && <Notice tone="danger">{read.error}</Notice>}
        {missing && (
          <>
            <Notice tone="warn">{t("missing")}</Notice>
            <Button disabled={busy} onClick={() => folders.run(() => tool("folders", {}))}>
              {t("createFolders")}
            </Button>
            {folders.error && <Notice tone="danger">{folders.error}</Notice>}
          </>
        )}
        <Button onClick={load} disabled={busy}>
          {t("read")}
        </Button>
        {preview && st && (
          <Facts
            rows={[
              [
                t("status"),
                st.state === "configured"
                  ? t("applied")
                  : st.state === "needs-update"
                    ? t("needsUpdate")
                    : t("notConfigured"),
              ],
              ...preview.targets.map((x): [string, string] => [t("target"), x.target]),
              [t("keySet"), st.key_set ? t("yes") : t("no")],
            ]}
          />
        )}
      </Card>
      {preview && (
        <>
          <Card title={t("mapping")}>
            <p class="muted">{t("mappingHint")}</p>
            <p class="muted">{t("aliasRule")}</p>
            <p class="muted">{t("aliasCore")}</p>
            {rows.length === 0 && <p class="muted">{t("empty")}</p>}
            {rows.map((r, i) => (
              <div key={i} class="row">
                <Text
                  label={t("model")}
                  value={r.model}
                  list={listId}
                  onInput={(v) => setRow(i, { model: v })}
                />
                <Text
                  label={t("alias")}
                  value={r.alias}
                  list={listId}
                  onInput={(v) => setRow(i, { alias: v })}
                />
                <Switch
                  label={t("context1m")}
                  hint={t("context1mHint")}
                  checked={r.one_m}
                  onChange={(on) => setRow(i, { one_m: on })}
                />
                <Button onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
                  {t("removeModel")}
                </Button>
              </div>
            ))}
            <Button
              disabled={rows.length >= 16}
              onClick={() => setRows((rs) => [...rs, { model: "", alias: "", one_m: false }])}
            >
              {t("addModel")}
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
            <Button variant="primary" onClick={apply} disabled={busy || !rowsOk || !secret || !core}>
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
      <StartApp available={!unavailable} />
    </div>
  );
}

export const claudeDesktopPage = (): PageDef => ({
  id: "claude-desktop",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <ClaudeDesktopPage />,
});
