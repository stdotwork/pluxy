// The app's Claude Code page: Agent Configuration (Apply, Clear, Restore Backup) and Start CLI for
// Claude Code, through the optional curated native tool pluxy/claude-code
// (next/app-tools/claude-code) and the launcher. The page is the window's own, not an extension
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
  useLoad,
} from "../../ui/kit/ui";
import { Backups, reply } from "./backups";
import { connectionGet } from "./host";

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
  ranges: "Maximum context must be 100000–1000000 and the auto-compact percentage must be 1–100.",
  noCore: "Connect to a core first: its address goes into the settings.",
  start: "Start CLI",
  cwd: "Working directory",
  recent: "Recently Used",
  override: "Agent program path",
  overrideHint: "A full path used instead of claude found on the PATH (optional)",
  terminal: "Terminal",
  launch: "Start Claude Code",
});

/** Claude Code's own models (its documentation's alias table), suggested until the model picker
 * lists the core's. */
const MODELS = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"] as const;
const ROLES = ["opus", "sonnet", "haiku", "fable"] as const;
type Role = (typeof ROLES)[number];
const LABELS: Record<Role, string> = { opus: "Opus", sonnet: "Sonnet", haiku: "Haiku", fable: "Fable" };
const PACKAGE = "pluxy/claude-code";
const SET_DEFAULT = 200000;
const SET_1M = 1000000;

const tool = <T,>(op: string, input: Record<string, unknown>) =>
  reply<T>("program_call", { package: PACKAGE, op, input });

interface ReadBack {
  state: "configured" | "not-configured" | "needs-update";
  base_url: string | null;
  key_set: boolean;
  startup_model: string | null;
  subagent_model: string;
  max_context_tokens: number | null;
  auto_compact_pct: number | null;
  disable_auto_compact: boolean;
  [k: string]: unknown;
}

interface Preview {
  targets: { selected: string; target: string; link: string | null }[];
  applied: boolean;
  settings: ReadBack;
}

interface Form {
  models: Record<Role, string>;
  oneM: Record<Role, boolean>;
  startup: string;
  subagent: string;
  window: number;
  pct: number;
  disable: boolean;
}

const fresh = (): Form => ({
  models: { opus: MODELS[0], sonnet: MODELS[1], haiku: MODELS[2], fable: "" },
  oneM: { opus: false, sonnet: false, haiku: false, fable: false },
  startup: "opus",
  subagent: "",
  window: SET_DEFAULT,
  pct: 90,
  disable: false,
});

/** The form from what the file holds, where it holds anything. */
function fromFile(r: ReadBack): Form {
  const f = fresh();
  for (const role of ROLES) {
    const m = r[role];
    if (typeof m === "string" && m) f.models[role] = m;
    f.oneM[role] = r[`${role}_1m`] === true;
  }
  if (r.startup_model) f.startup = r.startup_model;
  f.subagent = r.subagent_model;
  if (r.max_context_tokens) f.window = r.max_context_tokens;
  if (r.auto_compact_pct) f.pct = r.auto_compact_pct;
  f.disable = r.disable_auto_compact;
  return f;
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

export function ClaudeCodePage() {
  const [where, setWhere] = useState<"default" | "file" | "root">("default");
  const [file, setFile] = useState("");
  const [root, setRoot] = useState("");
  const [inRoot, setInRoot] = useState("settings.json");
  const [preview, setPreview] = useState<Preview>();
  const [form, setForm] = useState<Form>(fresh);
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
        setPreview(r.body);
        setForm(fromFile(r.body.settings));
      }
      return r;
    });
  const write = useAction(load);
  // A write acts on the file the last preview pinned; a new preview first, so the pins are fresh.
  const pins = async () => {
    const r = await tool<Preview>("preview", selection());
    return r.body?.targets;
  };
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  const setOneM = (role: Role, on: boolean) =>
    setForm((f) => {
      const oneM = { ...f.oneM, [role]: on };
      const any = Object.values(oneM).some(Boolean);
      // The window follows the 1M switches while it holds one of their defaults.
      const window =
        f.window === SET_DEFAULT && any ? SET_1M : f.window === SET_1M && !any ? SET_DEFAULT : f.window;
      return { ...f, oneM, window };
    });
  const ranges = form.window >= 100000 && form.window <= 1000000 && form.pct >= 1 && form.pct <= 100;
  const apply = () =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return { status: 400, error: { code: "refused", message: "the settings file could not be read" } };
      return tool("apply", {
        targets,
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
      });
    });
  const undo = (op: "clear" | "restore") =>
    write.run(async () => {
      const targets = await pins();
      if (!targets)
        return { status: 400, error: { code: "refused", message: "the settings file could not be read" } };
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
              [t("keySet"), st.key_set ? t("yes") : t("no")],
            ]}
          />
        )}
      </Card>
      {preview && (
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
            {!ranges && <Notice tone="warn">{t("ranges")}</Notice>}
            {write.error && <Notice tone="danger">{write.error}</Notice>}
            <Button
              variant="primary"
              onClick={apply}
              disabled={busy || !ranges || !secret || !core || !form.models.opus.trim()}
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

export const claudeCodePage = (): PageDef => ({
  id: "claude-code",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <ClaudeCodePage />,
});
