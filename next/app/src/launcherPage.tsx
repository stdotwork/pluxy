// The app's Launcher page (src-tauri/src/launcher.rs), drawn from host data: the launches the person
// remembered, each started again in one click; a new launch, which the optional execution program
// (pluxy/execution) prepares and a native confirmation shows in full; what it started and still
// runs, with Restart and Stop (each asks first, because it ends what the tool was doing); and
// whether one-click starts show a notice. The window only names a remembered launch or the fields of
// a new one: the native side prepares, confirms and spawns.

import { useState } from "preact/hooks";
import type { Reply } from "../../ui/kit/host";
import { Phases, SettingRow, useSetting } from "../../ui/kit/parts";
import type { PageDef } from "../../ui/kit/shell";
import { strings } from "../../ui/kit/strings";
import {
  Button,
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
import { Section } from "./clientPage";
import { reply } from "./host";
import { lifetimeFor } from "./launcherForm";
import { askFirst, Details, Intro, TextField } from "./pageBits";

const t = strings("launcher", {
  title: "Launcher",
  tip: "Start coding tools in one click",
  reading: "Reading launches…",
  readFailed: "Could not read the launcher",
  intro: "Start a coding tool in a terminal with one click, from here or from the tray.",
  unavailable: "Starting tools from here needs the Launcher add-on.",
  openAddons: "Open Add-ons",
  remembered: "Remembered launches",
  none: "No remembered launches yet. Start one below and tick Remember in the confirmation window.",
  tool: "Tool",
  command: "Command",
  folder: "Folder",
  lastUsed: "Last used",
  start: "Start",
  forget: "Forget",
  newLaunch: "New launch",
  toolName: "Tool name",
  toolNameAbout: "Shown here and in the tray. Leave empty to use the program's name.",
  howToRun: "How to run",
  terminal: "In a terminal",
  direct: "Directly",
  program: "Program",
  programAbout: "A program's name, such as claude, or its full path.",
  programFile: "Program file",
  programFileAbout: "A full path to use instead of the program found by name.",
  args: "Arguments",
  argsAbout: "One per line.",
  folderName: "Folder",
  folderAbout: "Where it starts.",
  recent: "Recent",
  terminalName: "Terminal",
  quits: "When the app quits",
  keep: "Keep it running",
  stop: "Stop it",
  terminalNote: "A terminal keeps running after the app quits.",
  launch: "Start…",
  launchAbout: "A confirmation window shows exactly what will run.",
  running: "Running now",
  process: "Process",
  restart: "Restart",
  stopNow: "Stop",
  none2: "Nothing started from here is running.",
  notifications: "Notifications",
  notice: "Show a notification after a one-click start",
  noticeAbout: "Says what started, or why it did not.",
});

interface Entry {
  id: string;
  client: string;
  executable: string;
  argv: string[];
  cwd: string;
  used: number;
}

interface State {
  available: boolean;
  reason?: string;
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
    reply<unknown>("program_call", { package: PACKAGE, op: "control", input: { op, runtime } }),
};

/** A program's name from a path of either kind, for a launch with no name of its own. */
const baseName = (path: string): string => path.trim().split(/[\\/]/).pop() ?? "";

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
  const name = client.trim() || baseName(program);
  const recent = props.state.history[name] ?? [];
  const submit = () =>
    action.run(
      askFirst(() =>
        launcher.launch({
          client: name,
          kind,
          program: program.trim(),
          ...(override.trim() ? { override: override.trim() } : {}),
          args: args.split("\n").filter((a) => a !== ""),
          cwd: cwd.trim(),
          ...(kind === "terminal" ? { terminal } : {}),
          lifetime: lifetimeFor(kind, lifetime),
        }),
      ),
    );
  return (
    <Section title={t("newLaunch")}>
      <SettingRow name={t("toolName")} description={t("toolNameAbout")}>
        <TextField label={t("toolName")} value={client} onInput={setClient} />
      </SettingRow>
      <SettingRow name={t("howToRun")}>
        <Choices
          label={t("howToRun")}
          value={kind}
          choices={[
            { value: "terminal", label: t("terminal") },
            { value: "direct", label: t("direct") },
          ]}
          onChange={setKind}
        />
      </SettingRow>
      <SettingRow name={t("program")} description={t("programAbout")}>
        <TextField label={t("program")} value={program} onInput={setProgram} />
      </SettingRow>
      <SettingRow name={t("programFile")} description={t("programFileAbout")}>
        <TextField label={t("programFile")} value={override} onInput={setOverride} />
      </SettingRow>
      <SettingRow name={t("args")} description={t("argsAbout")}>
        <TextField label={t("args")} value={args} onInput={setArgs} lines />
      </SettingRow>
      <SettingRow name={t("folderName")} description={t("folderAbout")}>
        <div class="launch-folder">
          <TextField label={t("folderName")} value={cwd} onInput={setCwd} />
          {recent.length > 0 && (
            <select
              aria-label={t("recent")}
              value={recent.includes(cwd) ? cwd : ""}
              onChange={(e) => e.currentTarget.value && setCwd(e.currentTarget.value)}
            >
              <option value="">{t("recent")}</option>
              {recent.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          )}
        </div>
      </SettingRow>
      {kind === "terminal" && terminals.data && (
        <SettingRow name={t("terminalName")}>
          <Choices
            label={t("terminalName")}
            value={terminal}
            choices={terminals.data.terminals.map((x) => ({ value: x.id, label: x.label }))}
            onChange={setTerminal}
          />
        </SettingRow>
      )}
      <SettingRow name={t("quits")}>
        {kind === "direct" ? (
          <Choices
            label={t("quits")}
            value={lifetime}
            choices={[
              { value: "detached", label: t("keep") },
              { value: "managed", label: t("stop") },
            ]}
            onChange={setLifetime}
          />
        ) : (
          <span class="muted">{t("terminalNote")}</span>
        )}
      </SettingRow>
      <div class="launch-start">
        {action.error && <Notice tone="danger">{action.error}</Notice>}
        <div class="actions">
          <Button variant="primary" onClick={submit} disabled={action.busy || !program.trim() || !cwd.trim()}>
            {t("launch")}
          </Button>
          <span class="muted">{t("launchAbout")}</span>
        </div>
      </div>
    </Section>
  );
}

/** What the page started and still runs: each can be restarted or stopped, after asking. */
function Running(props: { runtimes: ReturnType<typeof useLoad<Runtime[]>> }) {
  const list = props.runtimes;
  const action = useAction(list.reload);
  const live = (list.data ?? []).filter((r) => r.state === "running" || r.state === "stopping");
  return (
    <Section title={t("running")}>
      <div class="addon-section-body">
        {list.phase === "loading" && (
          <p class="muted" role="status">
            {t("reading")}
          </p>
        )}
        {list.error && <Notice tone="danger">{list.error}</Notice>}
        {action.error && <Notice tone="danger">{action.error}</Notice>}
        {live.length === 0 ? (
          list.data && <Empty>{t("none2")}</Empty>
        ) : (
          <List
            label={t("running")}
            items={live}
            id={(r) => r.runtime}
            columns={[
              { label: t("command"), value: (r) => [r.executable, ...r.argv].join(" "), mono: true },
              { label: t("process"), value: (r) => String(r.pid) },
              {
                label: "",
                value: (r) => (
                  <span class="actions">
                    <ConfirmButton
                      variant="quiet"
                      disabled={action.busy}
                      onConfirm={() => action.run(() => launcher.runtime("restart", r.runtime))}
                    >
                      {t("restart")}
                    </ConfirmButton>
                    <ConfirmButton
                      disabled={action.busy}
                      onConfirm={() => action.run(() => launcher.runtime("stop", r.runtime))}
                    >
                      {t("stopNow")}
                    </ConfirmButton>
                  </span>
                ),
              },
            ]}
          />
        )}
      </div>
    </Section>
  );
}

/** One-click starts show a notice, or not; it saves the moment the switch moves. */
function NoticeSetting(props: { s: State; reload: () => void }) {
  const notice = useSetting(props.s.notice, async (on) => {
    const r = await launcher.settings(on);
    if (r.status === 200) props.reload();
    return r;
  });
  return (
    <Section title={t("notifications")}>
      <SettingRow
        name={t("notice")}
        description={t("noticeAbout")}
        saving={notice.saving}
        error={notice.error}
      >
        <Switch label={t("notice")} checked={notice.value} onChange={(on) => notice.set(on)} />
      </SettingRow>
    </Section>
  );
}

export function LauncherPage() {
  const state = useLoad(launcher.state);
  const runtimes = useLoad(launcher.runtimes);
  const action = useAction(state.reload);
  const started = () => {
    state.reload();
    runtimes.reload();
  };
  return (
    <>
      <PageHead title={t("title")} />
      <Intro>{t("intro")}</Intro>
      <Phases
        load={state}
        loading={t("reading")}
        failed={{ title: t("readFailed") }}
        isEmpty={() => false}
        empty={null}
      >
        {(s) => (
          <div class="launcher">
            {!s.available && (
              <Notice tone="warn">
                {t("unavailable")}{" "}
                <a class="btn" data-part="button" data-variant="quiet" href="#/device">
                  {t("openAddons")}
                </a>
                {s.reason && <Details text={s.reason} />}
              </Notice>
            )}
            {action.error && <Notice tone="danger">{action.error}</Notice>}
            <Section title={t("remembered")}>
              <div class="addon-section-body">
                {s.entries.length === 0 ? (
                  <Empty>{t("none")}</Empty>
                ) : (
                  <List
                    label={t("remembered")}
                    items={s.entries}
                    id={(e) => e.id}
                    columns={[
                      { label: t("tool"), value: (e) => e.client },
                      { label: t("command"), value: (e) => [e.executable, ...e.argv].join(" "), mono: true },
                      { label: t("folder"), value: (e) => e.cwd, mono: true },
                      {
                        label: t("lastUsed"),
                        value: (e) => (e.used ? when(new Date(e.used).toISOString()) : ""),
                      },
                      {
                        label: "",
                        value: (e) => (
                          <span class="actions">
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
                          </span>
                        ),
                      },
                    ]}
                  />
                )}
              </div>
            </Section>
            {s.available && <NewLaunch state={s} done={started} />}
            {s.available && <Running runtimes={runtimes} />}
            <NoticeSetting s={s} reload={state.reload} />
          </div>
        )}
      </Phases>
    </>
  );
}

export const launcherPage = (): PageDef => ({
  id: "launcher",
  dest: "settings",
  kind: "topic",
  section: "device",
  title: t("title"),
  tip: t("tip"),
  render: () => <LauncherPage />,
});
