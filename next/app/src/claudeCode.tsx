// The app's Claude Code page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Claude Code, through the optional curated native tool pluxy/claude-code
// (next/app-tools/claude-code) and the launcher. The page is the window's own, not an extension
// page, because Apply takes the client key: the window names a vault secret's ID and the native
// confirmation of the apply names its label; an extension page can pass no secret. The key's value
// never reaches this webview: native key entry keeps it, and the tool's preview reads back only
// whether one is set. Labels follow the old app's Agents page. On Windows with WSL, the environment
// block at the top picks where Claude Code is set up and started: every call to the tool and the
// launch then go to that environment (environmentBlock.tsx).

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
  useLoad,
} from "../../ui/kit/ui";
import { Backups } from "./backups";
import {
  CLIENT,
  type Form,
  fresh,
  fromFile,
  MODELS,
  PACKAGE,
  type ReadBack,
  ROLES,
  type Role,
  ready,
  settingsNote,
  toolUnavailable,
  WINDOW_1M,
  WINDOW_DEFAULT,
} from "./claudeCodeForm";
import { folderWait, NeedsKey, NoKeys, toolMissing, withDeviceLink } from "./clientHelp";
import { keysOf, type Secret, Text, usePreview } from "./clientPage";
import type { AgentPage } from "./easyMode";
import { historyKey, inHome } from "./environment";
import {
  type Environment,
  EnvironmentBlock,
  LinuxBuildOffer,
  PathText,
  plainPath,
  useEnvironment,
} from "./environmentBlock";
import { connectionGet, reply } from "./host";
import { keptTerminal, launcherProblem, useDeviceChanged } from "./startCli";

const t = strings("claudeCode", {
  title: "Claude Code",
  tip: "Set up and start Claude Code with this core",
  intro:
    "Points Claude Code at this core with a client key and role models. Apply, Clear, and Restore backup ask for confirmation and keep other settings. Restart Claude Code afterward.",
  unavailable:
    "Install and enable the curated Claude Code tool (pluxy/claude-code) on the {device} page to set up Claude Code from here.",
  file: "Settings file",
  fileDefault: "Claude Code's user settings",
  fileSelected: "A selected file",
  fileInRoot: "A file in a selected folder",
  filePath: "File",
  filePathHint: "A full path; a link (as stow makes) is followed and kept",
  root: "Folder",
  rootHint: "A full path; the folder may be a link (as chezmoi can make)",
  inRoot: "File in the folder",
  read: "Refresh",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not configured",
  configured: "Configured",
  needsUpdate: "Configuration modified",
  mapping: "Claude Code model → upstream model",
  mappingHint: "The model each Claude Code role uses through this core. Fable follows Sonnet when empty.",
  context1m: "1M",
  startupModel: "Default model",
  startupHint: "Follows the Opus mapping by default; you can select another model.",
  subagentModel: "Subagent model",
  subagentHint: "Empty: use default inheritance",
  advanced: "Context and auto-compaction (advanced)",
  window: "Auto-compact window",
  windowHint:
    "Follows the 1M toggle between 200000 and 1000000 by default. Any other value you enter (for example 400000) is kept as a custom window.",
  pct: "Auto-compact trigger (%)",
  pctHint: "Defaults to 90%. Lower values compact earlier.",
  disable: "Disable auto-compaction",
  disableHint: "Only manual /compact remains available when enabled.",
  key: "Client key",
  keyHint: "The client key Claude Code uses to reach this core.",
  addKey: "Add key…",
  keyLabel: "Claude Code client key",
  connect: "Apply",
  repair: "Repair",
  update: "Update",
  clear: "Clear",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore backup",
  restoreHint: "Put the file back as it was before the first apply.",
  ranges:
    "Opus, Sonnet and Haiku each need a model (not only [1m]); the auto-compact window must be a whole number from 100000 to 1000000 and the percentage a whole number from 1 to 100.",
  refused: "Claude Code would not take this file",
  refusedHint: "Apply and Clear leave this file unchanged until you fix it; Restore backup reverts it.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working folder",
  recent: "Recently used",
  override: "Agent program path",
  overrideHint: "A full path used instead of claude found on the PATH (optional)",
  terminal: "Terminal",
  terminalWsl: "Terminal: Windows console",
  cwdWsl: "A folder in the distribution, such as {example}",
  launch: "Start Claude Code",
});

const LABELS: Record<Role, string> = { opus: "Opus", sonnet: "Sonnet", haiku: "Haiku", fable: "Fable" };

/** A call to the tool in the page's environment. */
const toolIn =
  (env: Environment) =>
  <T,>(op: string, input: Record<string, unknown>) =>
    reply<T>("program_call", { package: PACKAGE, op, input, environment: env.selection });

interface Preview {
  /** The pinned file: passed as is to apply, clear and restore. */
  targets: { selected: string; target: string; link: string | null; [k: string]: unknown }[];
  applied: boolean;
  settings: ReadBack;
}

function Num(props: {
  label: string;
  hint?: string;
  value: number;
  min: number;
  max: number;
  step: number;
  onInput: (v: number) => void;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      <input
        id={id}
        type="number"
        min={props.min}
        max={props.max}
        step={props.step}
        disabled={props.disabled}
        value={String(props.value)}
        onInput={(e) => props.onInput(Number(e.currentTarget.value))}
      />
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function StartCli(props: { available: boolean; env: Environment }) {
  const { env } = props;
  const tool = toolIn(env);
  const sel = env.selection;
  const distro = sel?.wsl.distro;
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  useDeviceChanged(terminals.reload);
  const action = useAction(state.reload);
  const recent = state.data?.history[historyKey(CLIENT, sel)] ?? [];
  const shownTerminal = keptTerminal(terminal, terminals.data?.terminals);
  const start = () =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", {});
      if (!recipe.body) return recipe;
      // In WSL the launch opens in a Windows console: no terminal is chosen.
      return reply("launcher_launch", {
        launch: {
          ...recipe.body,
          cwd: cwd.trim(),
          ...(sel ? {} : { terminal: shownTerminal }),
          ...(override.trim() ? { override: override.trim() } : {}),
        },
        environment: sel,
      });
    });
  return (
    <Card title={t("start")}>
      <PathText
        label={t("cwd")}
        hint={
          [
            sel ? t("cwdWsl", { example: inHome(env.record?.home ?? "~", "project") }) : undefined,
            folderWait(cwd),
          ]
            .filter(Boolean)
            .join(" ") || undefined
        }
        value={cwd}
        onInput={setCwd}
        distro={distro}
      />
      {recent.length > 0 && (
        <Choices
          label={t("recent")}
          value={recent.includes(cwd) ? cwd : undefined}
          choices={recent.map((r) => ({ value: r, label: r }))}
          onChange={setCwd}
        />
      )}
      <PathText
        label={t("override")}
        hint={t("overrideHint")}
        value={override}
        onInput={setOverride}
        distro={distro}
      />
      {sel && <p>{t("terminalWsl")}</p>}
      {!sel && terminals.data && (
        <Choices
          label={t("terminal")}
          value={shownTerminal}
          choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
          onChange={setTerminal}
        />
      )}
      {!sel && terminals.error && <Notice tone="warn">{launcherProblem(terminals.error)}</Notice>}
      {action.error && <Notice tone="danger">{launcherProblem(action.error)}</Notice>}
      <LinuxBuildOffer error={action.error} pkg={PACKAGE} record={env.record} />
      <Button
        variant="primary"
        onClick={start}
        disabled={
          action.busy ||
          !props.available ||
          env.pending ||
          !cwd.trim() ||
          !plainPath(cwd, distro) ||
          !plainPath(override, distro)
        }
      >
        {t("launch")}
      </Button>
    </Card>
  );
}

export function ClaudeCodePage() {
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("settings.json");
  const [form, setForm] = useState<Form>(fresh);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  // Another environment is another file: its preview and form start over.
  const env = useEnvironment(PACKAGE, () => setForm(fresh()));
  const tool = toolIn(env);
  const distro = env.selection?.wsl.distro;
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
  // since); the preview belongs to the file and environment it was read for, so a write never pins
  // a file unread. WSL with no user picked yet is an environment of its own: it is not this computer.
  const selected = JSON.stringify([env.mode, env.selection, selection()]);
  const { preview, readFor } = usePreview<Preview>(selected);
  const read = useAction(() => {});
  const load = () =>
    read.run(async () => {
      const r = await readFor(selected, () =>
        tool<Preview>("preview", { ...selection(), ...(core ? { base_url: core } : {}) }),
      );
      if (r.body) setForm(fromFile(r.body.settings));
      return r;
    });
  const write = useAction(load);
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  const setOneM = (role: Role, on: boolean) =>
    setForm((f) => {
      const oneM = { ...f.oneM, [role]: on };
      const any = Object.values(oneM).some(Boolean);
      // The window follows the 1M switches while it holds one of their defaults.
      const window =
        f.window === WINDOW_DEFAULT && any
          ? WINDOW_1M
          : f.window === WINDOW_1M && !any
            ? WINDOW_DEFAULT
            : f.window;
      return { ...f, oneM, window };
    });
  const valid = ready(form);
  const apply = () =>
    write.run(() =>
      tool("apply", {
        targets: preview?.targets,
        base_url: core,
        key: secret,
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
  const unavailable = toolUnavailable(read.error);
  const busy = read.busy || write.busy;
  const typed = where === "file" ? file : where === "root" ? root : "";

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      <EnvironmentBlock env={env} />
      {unavailable && <Notice tone="warn">{withDeviceLink(t("unavailable"))}</Notice>}
      {!core && <Notice tone="warn">{t("noCore")}</Notice>}
      <datalist id={listId}>
        {[...ROLES, ...MODELS].map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
      <Card title={t("file")}>
        <Choices
          label={t("file")}
          value={where}
          choices={[
            { value: "default", label: t("fileDefault"), note: settingsNote(env.record?.home) },
            { value: "file", label: t("fileSelected") },
            { value: "root", label: t("fileInRoot") },
          ]}
          onChange={setWhere}
        />
        {where === "file" && (
          <PathText
            label={t("filePath")}
            hint={t("filePathHint")}
            value={file}
            onInput={setFile}
            distro={distro}
          />
        )}
        {where === "root" && (
          <>
            <PathText label={t("root")} hint={t("rootHint")} value={root} onInput={setRoot} distro={distro} />
            <Text label={t("inRoot")} value={inRoot} onInput={setInRoot} />
          </>
        )}
        {read.error && !toolMissing(read.error, PACKAGE) && <Notice tone="danger">{read.error}</Notice>}
        <LinuxBuildOffer error={read.error} pkg={PACKAGE} record={env.record} onInstalled={load} />
        <Button onClick={load} disabled={busy || env.pending || !plainPath(typed, distro)}>
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
            {(["sonnet", "opus", "fable", "haiku"] as const).map((role) => (
              <div key={role} class="row">
                <Text
                  label={LABELS[role]}
                  value={form.models[role]}
                  list={listId}
                  onInput={(v) => set({ models: { ...form.models, [role]: v } })}
                />
                <Switch
                  label={`${t("context1m")}`}
                  checked={form.oneM[role]}
                  onChange={(on) => setOneM(role, on)}
                />
              </div>
            ))}
            <Text
              label={t("startupModel")}
              hint={t("startupHint")}
              value={form.startup}
              list={listId}
              onInput={(v) => set({ startup: v })}
            />
            <Text
              label={t("subagentModel")}
              hint={t("subagentHint")}
              value={form.subagent}
              list={listId}
              onInput={(v) => set({ subagent: v })}
            />
            <details>
              <summary>{t("advanced")}</summary>
              <Num
                label={t("window")}
                hint={t("windowHint")}
                min={100000}
                max={1000000}
                step={1000}
                value={form.window}
                onInput={(v) => set({ window: v })}
              />
              <Num
                label={t("pct")}
                hint={t("pctHint")}
                min={1}
                max={100}
                step={1}
                disabled={form.disable}
                value={form.pct}
                onInput={(v) => set({ pct: v })}
              />
              <Switch
                label={t("disable")}
                hint={t("disableHint")}
                checked={form.disable}
                onChange={(on) => set({ disable: on })}
              />
            </details>
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
          {!refused && !valid && <Notice tone="warn">{t("ranges")}</Notice>}
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button
            variant="primary"
            onClick={apply}
            disabled={busy || env.pending || refused || !valid || !secret || !core}
          >
            {applyLabel}
          </Button>
          <NeedsKey missing={!secret} />
          <ConfirmButton
            disabled={busy || env.pending || refused || !preview.applied}
            onConfirm={() => undo("clear")}
          >
            {t("clear")}
          </ConfirmButton>
          <small class="hint">{t("clearHint")}</small>
          <ConfirmButton disabled={busy || env.pending} onConfirm={() => undo("restore")}>
            {t("restore")}
          </ConfirmButton>
          <small class="hint">{t("restoreHint")}</small>
        </Card>
      )}
      {preview && (
        <Backups
          pkg={PACKAGE}
          pins={async () => preview.targets}
          disabled={busy || env.pending}
          onChange={load}
          environment={env.selection}
        />
      )}
      <StartCli key={JSON.stringify(env.selection)} available={!unavailable} env={env} />
    </div>
  );
}

export const claudeCodePage = (): AgentPage => ({
  id: "claude-code",
  package: PACKAGE,
  dest: "tools",
  kind: "topic",
  title: t("title"),
  tip: t("tip"),
  render: () => <ClaudeCodePage />,
});
