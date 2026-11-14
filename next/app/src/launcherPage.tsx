// The app's Launcher page (src-tauri/src/launcher.rs), drawn from host data: the launches the
// person remembered (client, executable, argv, folder and the execution program's release that
// runs them), each started again in one click; a new launch, which the optional execution program
// (pluxy/execution) prepares and a native confirmation shows in full; the managed runtimes it
// started, with Stop and Restart; and whether one-click starts show a launch notice. The window
// only names a remembered launch or the fields of a new one: the native side prepares, confirms and
// spawns.

import { invoke } from "@tauri-apps/api/core";
import { useId, useState } from "preact/hooks";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import {
  Badge,
  Button,
  Card,
  Choices,
  ConfirmButton,
  Empty,
  List,
  Notice,
  PageHead,
  Switch,
  useAction,
  useLoad,
  when,
} from "../../ui/kit/ui";

const t = strings("launcher", {
  title: "Launcher",
  short: "Launch",
  tip: "Start agents and terminals you remembered, from host data",
  intro:
    "Launches you remembered start again in one click, here or from the tray's Start… menu. A new launch, or one whose program, folder or environment changed, asks in a native confirmation first. Detached launches keep running after the app quits.",
  unavailable:
    "Install and enable the curated execution program (pluxy/execution) on the Device extensions page to launch from here.",
  remembered: "Remembered launches",
  none: "No remembered launches. Start a new launch and tick Remember in its confirmation.",
  client: "Client",
  command: "Command",
  folder: "Folder",
  source: "Runs through",
  lastUsed: "Last used",
  start: "Start",
  forget: "Forget",
  stale: "asks again",
  newLaunch: "New launch",
  kind: "Kind",
  terminal: "In a terminal",
  direct: "Directly",
  program: "Program",
  programHint: "A name found on the PATH, or a full path",
  override: "Executable override",
  overrideHint: "A full path used instead of the program found on the PATH (optional)",
  args: "Arguments",
  argsHint: "One per line",
  cwd: "Working folder",
  recent: "Recently used",
  terminalChoice: "Terminal",
  lifetime: "Lifetime",
  detached: "Detached",
  detachedNote: "keeps running after the app quits",
  managed: "Managed",
  managedNote: "the app stops it",
  launch: "Start…",
  runtimes: "Managed runtimes",
  noRuntimes: "No managed runtimes.",
  stop: "Stop",
  restart: "Restart",
  notice: "Show a notice after a one-click start",
  noticeHint: "A native notice says what started, or why it did not.",
});

interface Entry {
  id: string;
  client: string;
  executable: string;
  argv: string[];
  cwd: string;
  program?: string;
  lifetime: string;
  remembered: boolean;
  used: number;
  source: { package: string; version?: string };
}

interface State {
  available: boolean;
  reason?: string;
  package: string;
  notice: boolean;
  entries: Entry[];
  history: Record<string, string[]>;
}

interface Runtime {
  runtime: string;
  state: string;
  pid: number;
  executable: string;
  argv: string[];
}

type Reply<T> = { status: number; body?: T; error?: { code: string; message: string } };

async function reply<T>(cmd: string, args?: Record<string, unknown>): Promise<Reply<T>> {
  try {
    return { status: 200, body: await invoke<T>(cmd, args) };
  } catch (e) {
    return { status: 400, error: { code: "refused", message: String(e) } };
  }
}

const PACKAGE = "pluxy/execution";

const launcher = {
  state: () => reply<State>("launcher_state"),
  terminals: () => reply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  launch: (launch: Record<string, unknown>) => reply<unknown>("launcher_launch", { launch }),
  start: (id: string) => reply<unknown>("launcher_start", { id }),
  forget: (id: string) => reply<State>("launcher_forget", { id }),
  settings: (notice: boolean) => reply<State>("launcher_settings", { notice }),
  runtimes: async (): Promise<Reply<Runtime[]>> => {
    const r = await reply<{ runtimes?: Runtime[] }>("program_status", { package: PACKAGE });
    return r.body ? { status: 200, body: r.body.runtimes ?? [] } : { status: r.status, error: r.error };
  },
  runtime: (op: "stop" | "restart", runtime: string) =>
    reply<unknown>("program_call", { package: PACKAGE, op: "runtime", input: { op, runtime } }),
};

function Text(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  lines?: boolean;
}) {
  const id = useId();
  const onInput = (e: { currentTarget: HTMLInputElement | HTMLTextAreaElement }) =>
    props.onInput(e.currentTarget.value);
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      {props.lines ? (
        <textarea id={id} rows={3} spellcheck={false} value={props.value} onInput={onInput} />
      ) : (
        <input
          id={id}
          type="text"
          autocomplete="off"
          spellcheck={false}
          value={props.value}
          onInput={onInput}
        />
      )}
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

function NewLaunch(props: { state: State; done: () => void }) {
  const [client, setClient] = useState("");
  const [kind, setKind] = useState<"terminal" | "direct">("terminal");
  const [program, setProgram] = useState("");
  const [override, setOverride] = useState("");
  const [args, setArgs] = useState("");
  const [cwd, setCwd] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const [lifetime, setLifetime] = useState<"detached" | "managed">("detached");
  const terminals = useLoad(launcher.terminals);
  const action = useAction(props.done);
  const name = client.trim() || program.trim().split("/").pop() || "";
  const recent = props.state.history[name] ?? [];
  const submit = () =>
    action.run(() =>
      launcher.launch({
        client: name,
        kind,
        program: program.trim(),
        ...(override.trim() ? { override: override.trim() } : {}),
        args: args.split("\n").filter((a) => a !== ""),
        cwd: cwd.trim(),
        ...(kind === "terminal" ? { terminal } : {}),
        lifetime,
      }),
    );
  return (
    <Card title={t("newLaunch")}>
      <Text label={t("client")} value={client} onInput={setClient} />
      <Choices
        label={t("kind")}
        value={kind}
        choices={[
          { value: "terminal", label: t("terminal") },
          { value: "direct", label: t("direct") },
        ]}
        onChange={setKind}
      />
      <Text label={t("program")} hint={t("programHint")} value={program} onInput={setProgram} />
      <Text label={t("override")} hint={t("overrideHint")} value={override} onInput={setOverride} />
      <Text label={t("args")} hint={t("argsHint")} value={args} onInput={setArgs} lines />
      <Text label={t("cwd")} value={cwd} onInput={setCwd} />
      {recent.length > 0 && (
        <Choices
          label={t("recent")}
          value={recent.includes(cwd) ? cwd : undefined}
          choices={recent.map((r) => ({ value: r, label: r }))}
          onChange={setCwd}
        />
      )}
      {kind === "terminal" && terminals.data && (
        <Choices
          label={t("terminalChoice")}
          value={terminal}
          choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
          onChange={setTerminal}
        />
      )}
      <Choices
        label={t("lifetime")}
        value={lifetime}
        choices={[
          { value: "detached", label: t("detached"), note: t("detachedNote") },
          { value: "managed", label: t("managed"), note: t("managedNote") },
        ]}
        onChange={setLifetime}
      />
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <Button variant="primary" onClick={submit} disabled={action.busy || !program.trim() || !cwd.trim()}>
        {t("launch")}
      </Button>
    </Card>
  );
}

function Runtimes() {
  const list = useLoad(launcher.runtimes);
  const action = useAction(list.reload);
  const live = (list.data ?? []).filter((r) => r.state === "running" || r.state === "stopping");
  return (
    <Card title={t("runtimes")}>
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      {live.length === 0 ? (
        <Empty>{t("noRuntimes")}</Empty>
      ) : (
        <List
          label={t("runtimes")}
          items={live}
          id={(r) => r.runtime}
          columns={[
            { label: t("command"), value: (r) => [r.executable, ...r.argv].join(" "), mono: true },
            { label: "PID", value: (r) => String(r.pid) },
            {
              label: "",
              value: (r) => (
                <>
                  <Button
                    disabled={action.busy}
                    onClick={() => action.run(() => launcher.runtime("restart", r.runtime))}
                  >
                    {t("restart")}
                  </Button>
                  <Button
                    disabled={action.busy}
                    onClick={() => action.run(() => launcher.runtime("stop", r.runtime))}
                  >
                    {t("stop")}
                  </Button>
                </>
              ),
            },
          ]}
        />
      )}
    </Card>
  );
}

export function LauncherPage() {
  const state = useLoad(launcher.state);
  const action = useAction(state.reload);
  const s = state.data;
  return (
    <div class="stack">
      <PageHead title={t("title")} />
      <p class="muted">{t("intro")}</p>
      {state.error && <Notice tone="danger">{state.error}</Notice>}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      {s && !s.available && <Notice tone="warn">{t("unavailable")}</Notice>}
      {s && (
        <Card title={t("remembered")}>
          {s.entries.length === 0 ? (
            <Empty>{t("none")}</Empty>
          ) : (
            <List
              label={t("remembered")}
              items={s.entries}
              id={(e) => e.id}
              columns={[
                { label: t("client"), value: (e) => e.client },
                { label: t("command"), value: (e) => [e.executable, ...e.argv].join(" "), mono: true },
                { label: t("folder"), value: (e) => e.cwd, mono: true },
                {
                  label: t("source"),
                  value: (e) => `${e.source.package}${e.source.version ? ` ${e.source.version}` : ""}`,
                },
                { label: t("lastUsed"), value: (e) => when(new Date(e.used).toISOString()) ?? "" },
                {
                  label: "",
                  value: (e) => (
                    <>
                      {!e.remembered && <Badge tone="warn">{t("stale")}</Badge>}
                      <Button
                        variant="primary"
                        disabled={action.busy || !s.available}
                        onClick={() => action.run(() => launcher.start(e.id))}
                      >
                        {t("start")}
                      </Button>
                      <ConfirmButton
                        disabled={action.busy}
                        onConfirm={() => action.run(() => launcher.forget(e.id))}
                      >
                        {t("forget")}
                      </ConfirmButton>
                    </>
                  ),
                },
              ]}
            />
          )}
        </Card>
      )}
      {s?.available && <NewLaunch state={s} done={state.reload} />}
      {s?.available && <Runtimes />}
      {s && (
        <Card>
          <Switch
            label={t("notice")}
            hint={t("noticeHint")}
            checked={s.notice}
            onChange={(on) => action.run(() => launcher.settings(on))}
          />
        </Card>
      )}
    </div>
  );
}

export const launcherPage = (): PageDef => ({
  id: "launcher",
  group: "control",
  title: t("title"),
  short: t("short"),
  tip: t("tip"),
  render: () => <LauncherPage />,
});
