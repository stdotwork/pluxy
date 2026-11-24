// The app's Claude Desktop page: Agent Configuration (Apply, Clear, Restore Backup) and Start
// Claude Desktop, through the optional curated native tool pluxy/claude-desktop
// (next/app-tools/claude-desktop) and the launcher. Like the Claude Code page it is the window's
// own, not an extension page, because Apply takes the client key: the window names a vault
// secret's ID and the native confirmation of the apply names its label. The key's value never
// reaches this webview. Labels follow the old app's Agents page.

import { invoke } from "@tauri-apps/api/core";
import { useEffect, useId, useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
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
import { NeedsKey, NoKeys, toolMissing, withDeviceLink } from "./clientHelp";
import { keysOf, type Secret, Text, usePreview } from "./clientPage";
import type { AgentPage } from "./easyMode";
import { connectionGet, reply } from "./host";
import { launcherProblem } from "./startCli";

const t = strings("claudeDesktop", {
  title: "Claude Desktop",
  short: "Desk",
  tip: "Set up and start Claude Desktop with this core",
  intro:
    "Configures Claude Desktop's third-party gateway (Linux beta) with a client key and model list. Apply, Clear, and Restore backup ask for confirmation and keep other settings. Quit and reopen Claude Desktop afterward.",
  unavailable:
    "Install and enable the curated Claude Desktop tool (pluxy/claude-desktop) on the {device} page to set up Claude Desktop from here.",
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
  read: "Refresh",
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
  mapping: "Claude Desktop models and aliases",
  mappingHint:
    "Claude Desktop only allows Claude models, so non-Claude models need aliases that follow Claude model naming. If the original model name already belongs to the Claude family, no alias mapping is needed; leave the alias blank. The first model is Claude Desktop's default.",
  aliasCore:
    "Core must also serve the alias. Create alias in core adds it to login accounts after confirmation; add it manually for config accounts. Clear and Restore backup keep core aliases.",
  createAlias: "Create alias in core",
  aliasServed: "Core serves this alias.",
  aliasMade: "Core now serves {alias} on {accounts}.",
  aliasAlready: "Core already serves {alias} on {accounts}.",
  aliasConfig: "Accounts set in core's config ({accounts}) do not have it: add it there.",
  aliasRule:
    "Aliases can follow patterns such as claude-opus-*, claude-sonnet-*, or claude-haiku-*, but must not contain names of other model families such as gpt, grok, gemini, or deepseek.",
  addModel: "Add model",
  removeModel: "Remove model",
  empty: "Press “Add model” and enter a model name. The alias is optional.",
  model: "Original model",
  alias: "Alias (optional for Claude models)",
  context1m: "1M",
  context1mHint: "Configures a 1M context window. Make sure the selected upstream model supports it.",
  key: "Client key",
  keyHint: "The client key Claude Desktop uses to reach this core.",
  addKey: "Add key…",
  keyLabel: "Claude Desktop client key",
  connect: "Apply",
  repair: "Repair",
  update: "Update",
  clear: "Clear",
  clearHint: "Remove pluxy integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore backup",
  restoreHint: "Put the files back as they were before the first apply.",
  refused: "Claude Desktop would not take these files",
  refusedHint: "Apply and Clear leave these files unchanged until you fix them; Restore backup reverts them.",
  rows: "Each row needs an original model, and models and aliases are IDs with no spaces (1 to 16 rows).",
  noCore: "Connect to a core first: its address goes into the gateway profile.",
  start: "Start app",
  override: "Agent program path",
  overrideHint: "A full path used instead of claude-desktop found on the PATH (optional)",
  launch: "Start Claude Desktop",
});

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

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  settings: ReadBack;
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
      {action.error && <Notice tone="danger">{launcherProblem(action.error)}</Notice>}
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
  const [missing, setMissing] = useState(false);
  const [rows, setRows] = useState<Row[]>(fresh);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  const keys = keysOf(PACKAGE, secrets.data);
  const listId = useId();
  // The names core serves, which Claude Desktop's aliases must be among.
  const coreModels = useLoad(() =>
    invoke<Reply<{ models?: { id: string }[] }>>("core_call", {
      method: "GET",
      path: "/models",
      body: null,
    }).catch(
      (e): Reply<{ models?: { id: string }[] }> => ({
        status: 0,
        error: { code: "unavailable", message: String(e) },
      }),
    ),
  );
  const served = coreModels.data?.models ? new Set(coreModels.data.models.map((m) => m.id)) : undefined;
  const aliasAct = useAction(coreModels.reload);
  const [made, setMade] = useState<string>();
  const createAlias = (r: Row) =>
    aliasAct.run(async () => {
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
  // A write acts on the files the last preview pinned (the tool refuses it if one changed since);
  // the preview belongs to the selection it was read for, so a write never pins files unread.
  const selected = JSON.stringify(selection());
  const { preview, readFor, isCurrent } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await readFor(selected, () =>
        tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) }),
      );
      if (isCurrent(selected)) setMissing(missingLibrary(r.error?.message));
      if (r.body) setRows(rowsFrom(r.body.settings));
      return r;
    });
  const write = useAction(load);
  // Choosing other files also drops the offer to create the library folder, which was for the
  // files last read.
  const reselect =
    <T,>(f: (v: T) => void) =>
    (v: T) => {
      f(v);
      setMissing(false);
    };
  const apply = () =>
    write.run(() =>
      tool("apply", {
        targets: preview?.targets,
        base_url: core,
        key: secret,
        models: rows.map((r) => ({ model: r.model.trim(), alias: r.alias.trim(), one_m: r.one_m })),
      }),
    );
  const undo = (op: "clear" | "restore") => write.run(() => tool(op, { targets: preview?.targets }));
  const folders = useAction(load);
  const addKey = useAction(secrets.reload);
  const setRow = (i: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const st = preview?.settings;
  const refused = st?.state === "refused";
  const applyLabel = !preview?.applied
    ? t("connect")
    : st?.state === "configured"
      ? t("update")
      : t("repair");
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy || folders.busy;
  const rowsOk = ready(rows);

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{withDeviceLink(t("unavailable"))}</Notice>}
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
          onChange={reselect(setWhere)}
        />
        {where === "root" && (
          <Text label={t("root")} hint={t("rootHint")} value={root} onInput={reselect(setRoot)} />
        )}
        {where === "files" &&
          [t("deployment"), t("profile"), t("meta")].map((label, i) => (
            <Text
              key={label}
              label={label}
              hint={t("fileHint")}
              value={files[i] ?? ""}
              onInput={reselect((v: string) => setFiles((fs) => fs.map((f, j) => (j === i ? v : f))))}
            />
          ))}
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
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
                refused
                  ? t("refused")
                  : st.state === "configured"
                    ? t("applied")
                    : st.state === "needs-update"
                      ? t("needsUpdate")
                      : t("notConfigured"),
              ],
              ...preview.targets.map((x): [string, string] => [t("target"), x.target]),
              ...(refused ? [] : [[t("keySet"), st.key_set ? t("yes") : t("no")] as [string, string]]),
            ]}
          />
        )}
        {refused && (
          <Notice tone="warn">
            {st?.reason} {t("refusedHint")}
          </Notice>
        )}
      </Card>
      {preview && !refused && (
        <>
          <Card title={t("mapping")}>
            <p class="muted">{t("mappingHint")}</p>
            <p class="muted">{t("aliasRule")}</p>
            <p class="muted">{t("aliasCore")}</p>
            {aliasAct.error && <Notice tone="danger">{aliasAct.error}</Notice>}
            {made && <Notice tone="ok">{made}</Notice>}
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
                {aliasState(r, served) === "missing" && core && (
                  <Button disabled={aliasAct.busy} onClick={() => createAlias(r)}>
                    {t("createAlias")}
                  </Button>
                )}
                {aliasState(r, served) === "served" && <span class="muted">{t("aliasServed")}</span>}
                <Button onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
                  {t("removeModel")}
                </Button>
              </div>
            ))}
            <Button
              disabled={rows.length >= MODELS_MAX}
              onClick={() => setRows((rs) => [...rs, { model: "", alias: "", one_m: false }])}
            >
              {t("addModel")}
            </Button>
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
          {!refused && !rowsOk && <Notice tone="warn">{t("rows")}</Notice>}
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button variant="primary" onClick={apply} disabled={busy || refused || !rowsOk || !secret || !core}>
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
      <StartApp available={!unavailable} />
    </div>
  );
}

export const claudeDesktopPage = (): AgentPage => ({
  id: "claude-desktop",
  package: PACKAGE,
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <ClaudeDesktopPage />,
});
