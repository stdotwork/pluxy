// The app's Claude Code page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Claude Code, through the optional curated native tool pluxy/claude-code
// (next/app-tools/claude-code) and the launcher. The page is the window's own, not an extension
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
  useLoad,
} from "../../ui/kit/ui";
import { Backups, reply } from "./backups";
import {
  CLIENT,
  type Form,
  fresh,
  fromFile,
  keysOf,
  MODELS,
  PACKAGE,
  type ReadBack,
  ROLES,
  type Role,
  ready,
  type Secret,
  WINDOW_1M,
  WINDOW_DEFAULT,
} from "./claudeCodeForm";
import type { AgentPage } from "./easyMode";
import { connectionGet } from "./host";
import { keptTerminal, useDeviceChanged } from "./startCli";

const t = strings("claudeCode", {
  title: "Claude Code",
  short: "Claude",
  tip: "Set up and start Claude Code with this core",
  intro:
    "Points Claude Code's settings at this core, with a client key and the models each role uses. Apply, Clear and Restore Backup each ask in a native confirmation; everything else in the file stays. Restart Claude Code afterward.",
  unavailable:
    "Install and enable the curated Claude Code tool (pluxy/claude-code) on the Device extensions page to set up Claude Code from here.",
  file: "Settings file",
  fileDefault: "Claude Code's user settings",
  fileSelected: "A selected file",
  fileInRoot: "A file in a selected folder",
  filePath: "File",
  filePathHint: "A full path; a link (as stow makes) is followed and kept",
  root: "Folder",
  rootHint: "A full path; the folder may be a link (as chezmoi can make)",
  inRoot: "File in the folder",
  read: "Read settings",
  status: "Status",
  target: "Writes to",
  keySet: "Key in the file",
  yes: "Yes",
  no: "No",
  applied: "Applied",
  notConfigured: "Not Configured",
  configured: "Configured",
  needsUpdate: "Configuration Modified",
  mapping: "Claude Code Model → Upstream Model",
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
  key: "Authentication Key",
  keyHint: "The client API key Claude Code uses to connect to this core, kept by native key entry.",
  addKey: "Add key…",
  keyLabel: "Claude Code client key",
  noKeys: "No keys yet: add one.",
  connect: "Connect in One Click",
  repair: "Repair Connection",
  update: "Update Configuration",
  clear: "Close configuration",
  clearHint: "Remove pluxy's integration settings and keep other settings. Restart the agent afterward.",
  restore: "Restore Backup",
  restoreHint: "Put the file back as it was before the first apply.",
  ranges:
    "Opus, Sonnet and Haiku each need a model (not only [1m]); the auto-compact window must be a whole number from 100000 to 1000000 and the percentage a whole number from 1 to 100.",
  refused: "Claude Code would not take this file",
  refusedHint:
    "Apply and Close configuration leave it alone until it is fixed; Restore Backup puts back the file as it was before the first apply.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Agent program path",
  overrideHint: "A full path used instead of claude found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start Claude Code",
});

const LABELS: Record<Role, string> = { opus: "Opus", sonnet: "Sonnet", haiku: "Haiku", fable: "Fable" };

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface Preview {
  /** The pinned file: passed as is to apply, clear and restore. */
  targets: { selected: string; target: string; link: string | null; [k: string]: unknown }[];
  applied: boolean;
  settings: ReadBack;
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

function StartCli(props: { available: boolean }) {
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const state = useLoad(() => reply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  useDeviceChanged(terminals.reload);
  const action = useAction(state.reload);
  const recent = state.data?.history[CLIENT] ?? [];
  const shownTerminal = keptTerminal(terminal, terminals.data?.terminals);
  const start = () =>
    action.run(async () => {
      const recipe = await tool<Record<string, unknown>>("recipe", {});
      if (!recipe.body) return recipe;
      return reply("launcher_launch", {
        launch: {
          ...recipe.body,
          cwd: cwd.trim(),
          terminal: shownTerminal,
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
          value={shownTerminal}
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

export function ClaudeCodePage() {
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("settings.json");
  const [preview, setPreview] = useState<Preview>();
  const [form, setForm] = useState<Form>(fresh);
  const [secret, setSecret] = useState("");
  const [core, setCore] = useState<string>();
  const secrets = useLoad(() => reply<{ secrets: Secret[] }>("vault_secrets"));
  const keys = keysOf(secrets.data);
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
        setPreview(r.body);
        setForm(fromFile(r.body.settings));
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the file the last preview pinned (the tool refuses it if the file changed
  // since); choosing another file drops that preview, so a write never pins a file unread.
  const reselect =
    <T,>(f: (v: T) => void) =>
    (v: T) => {
      f(v);
      setPreview(undefined);
    };
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
  const unavailable = read.error?.includes(PACKAGE) || read.error?.includes("not installed");
  const busy = read.busy || write.busy;

  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {unavailable && <Notice tone="warn">{t("unavailable")}</Notice>}
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
            { value: "default", label: t("fileDefault"), note: "~/.claude/settings.json" },
            { value: "file", label: t("fileSelected") },
            { value: "root", label: t("fileInRoot") },
          ]}
          onChange={reselect(setWhere)}
        />
        {where === "file" && (
          <Text label={t("filePath")} hint={t("filePathHint")} value={file} onInput={reselect(setFile)} />
        )}
        {where === "root" && (
          <>
            <Text label={t("root")} hint={t("rootHint")} value={root} onInput={reselect(setRoot)} />
            <Text label={t("inRoot")} value={inRoot} onInput={reselect(setInRoot)} />
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
        </>
      )}
      {preview && (
        <Card>
          {!refused && !valid && <Notice tone="warn">{t("ranges")}</Notice>}
          {write.error && <Notice tone="danger">{write.error}</Notice>}
          <Button variant="primary" onClick={apply} disabled={busy || refused || !valid || !secret || !core}>
            {applyLabel}
          </Button>
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
      <StartCli available={!unavailable} />
    </div>
  );
}

export const claudeCodePage = (): AgentPage => ({
  id: "claude-code",
  package: PACKAGE,
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <ClaudeCodePage />,
});
