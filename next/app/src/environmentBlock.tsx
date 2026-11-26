// The "Environment / configuration directory" block a client page shows at its top: whether the
// client is set up and started on this computer or in a WSL distribution as one of its users. Only
// when the host says WSL is available (environments_get); elsewhere it renders nothing and the
// page works on this computer. Distributions and their users come from the optional pluxy/wsl
// program; the host checks a picked user (environment_add) and keeps the record. The page's last
// choice is kept per client package in the window's storage; the host validates it on every call.
// Labels follow the old app's environment picker. Logic in environment.ts.

import { useEffect, useId, useRef, useState } from "preact/hooks";
import { strings } from "../../ui/kit/strings";
import { Button, Card, Choices, Notice, useAction, useKit, useLoad } from "../../ui/kit/ui";
import {
  type Distro,
  type EnvRecord,
  encodeSelection,
  linuxBuildMissing,
  linuxPath,
  linuxPlatform,
  parseSelection,
  prefKey,
  recordOf,
  type Selection,
  sameSelection,
  usersInOrder,
  WSL_PACKAGE,
  type WslUser,
} from "./environment";
import { reply } from "./host";

const t = strings("environment", {
  title: "Environment and configuration folder",
  description: "Choose where this client is set up and started.",
  where: "Environment",
  windows: "Windows local",
  wsl: "WSL subsystem",
  distribution: "Distribution",
  user: "User",
  root: "root (administrator of this distribution)",
  default: "default",
  version: "WSL {version}",
  startsDistro: "Reading users starts this distribution.",
  detect: "Detect again",
  detecting: "Detecting available environments…",
  empty: "No WSL distributions found. Install and start WSL, then detect again.",
  pickUser: "Pick a user to set up and start this client in WSL.",
  installLinux: "Install the Linux build for WSL",
  converted: "Converted the WSL path to {path}.",
  outside: "This path is outside the selected distribution; use Windows local for Windows files.",
});

export interface Environment {
  /** Whether the host offers WSL here: the block shows only then. */
  available: boolean;
  /** environments_get's refusal, if any. */
  error?: string;
  /** The host's records of the WSL environments it checked. */
  records: EnvRecord[];
  reload: () => void;
  mode: "local" | "wsl";
  setMode: (m: "local" | "wsl") => void;
  /** What the page's calls pass as `environment`: always null where WSL is not offered. */
  selection: Selection;
  /** The host's record of the selection, when it has one. */
  record?: EnvRecord;
  choose: (s: Selection) => void;
  /** True while the page must not call its package: the host has not said yet whether WSL is
   * offered, or WSL is chosen and no user is picked yet. */
  pending: boolean;
}

/** The environment a client page of package pkg works in; onChange runs when the person picks
 * another one. */
export function useEnvironment(pkg: string, onChange: () => void): Environment {
  const { host } = useKit();
  const known = useLoad(() => reply<{ available: boolean; environments: EnvRecord[] }>("environments_get"));
  const [chosen, setChosen] = useState<Selection>(() => parseSelection(host.prefs.get(prefKey(pkg))));
  const [mode, setModeState] = useState<"local" | "wsl">(chosen ? "wsl" : "local");
  const available = known.data?.available === true;
  const selection = available ? chosen : null;
  const records = known.data?.environments ?? [];
  const choose = (s: Selection) => {
    if (!sameSelection(s, chosen)) onChange();
    setChosen(s);
    host.prefs.set(prefKey(pkg), encodeSelection(s));
  };
  const setMode = (m: "local" | "wsl") => {
    setModeState(m);
    // WSL counts once a user is picked; until then the page waits.
    choose(null);
  };
  return {
    available,
    error: known.error,
    records,
    reload: known.reload,
    mode,
    setMode,
    selection,
    record: recordOf(records, selection),
    choose,
    pending: known.loading || (available && mode === "wsl" && !selection),
  };
}

const join = (...notes: (string | false | undefined)[]) => notes.filter(Boolean).join(" · ") || undefined;

function Wsl(props: { env: Environment }) {
  const { env } = props;
  const sel = env.selection?.wsl;
  const [distros, setDistros] = useState<{ distros: Distro[]; installed: boolean }>();
  const [distro, setDistro] = useState(sel?.distro);
  const [users, setUsers] = useState<{ distro: string; users: WslUser[] }>();
  const detect = useAction(() => {});
  const read = useAction(() => {});
  const add = useAction(env.reload);
  // The distribution whose users are wanted: a reply for any other (picked before it) is dropped.
  const wanted = useRef(distro);
  const readUsers = (d: string) => {
    // Another distribution has no user picked yet: the page must not keep working in the old one
    // while this one is shown.
    if (sel && sel.distro !== d) env.choose(null);
    wanted.current = d;
    setDistro(d);
    setUsers(undefined);
    read.run(async () => {
      const r = await reply<{ users: WslUser[] }>("program_call", {
        package: WSL_PACKAGE,
        op: "users",
        input: { distro: d },
      });
      if (wanted.current !== d) return { status: 200 };
      if (r.body) setUsers({ distro: d, users: r.body.users });
      return r;
    });
  };
  const listDistros = () =>
    detect.run(async () => {
      const r = await reply<{ distros: Distro[]; installed: boolean }>("program_call", {
        package: WSL_PACKAGE,
        op: "distros",
        input: {},
      });
      if (r.body) setDistros(r.body);
      return r;
    });
  // Listing distributions starts none of them, so it runs each time WSL is chosen (this part mounts
  // then). Detect again also reads the picked distribution's users again.
  useEffect(() => {
    listDistros();
  }, []);
  const detectAll = () => {
    listDistros();
    if (wanted.current) readUsers(wanted.current);
  };

  const listed = distros?.distros ?? [];
  const names = [
    ...new Set([
      ...listed.map((d) => d.name),
      ...env.records.map((r) => r.distro),
      ...(sel ? [sel.distro] : []),
    ]),
  ];
  const got = users && users.distro === distro ? usersInOrder(users.users) : undefined;
  // Before its users are read, a distribution offers the users the host already checked there.
  const knownUsers = env.records.filter((r) => r.distro === distro);
  const offered = got
    ? got.map((u) => ({
        value: u.name,
        label: u.root ? t("root") : u.name,
        note: join(u.default && t("default")),
      }))
    : [
        ...knownUsers
          .filter((r) => r.uid !== 0)
          .map((r) => ({ value: r.user, label: r.user, note: undefined as string | undefined })),
        ...(sel && sel.distro === distro && !knownUsers.some((r) => r.user === sel.user)
          ? [{ value: sel.user, label: sel.user, note: undefined }]
          : []),
        ...knownUsers
          .filter((r) => r.uid === 0)
          .map((r) => ({ value: r.user, label: t("root"), note: undefined })),
      ];
  const pick = (user: string) => {
    if (!distro) return;
    if (!got) return env.choose({ wsl: { distro, user } });
    add.run(async () => {
      const r = await reply<EnvRecord>("environment_add", { distro, user });
      if (r.body) env.choose({ wsl: { distro, user } });
      return r;
    });
  };
  const nothing = distros && !detect.busy && names.length === 0;

  return (
    <>
      {detect.busy && <p class="muted">{t("detecting")}</p>}
      {detect.error && <Notice tone="danger">{detect.error}</Notice>}
      {nothing && <Notice tone="warn">{t("empty")}</Notice>}
      {names.length > 0 && (
        <>
          <Choices
            label={t("distribution")}
            value={distro}
            choices={names.map((name) => {
              const d = listed.find((x) => x.name === name);
              return {
                value: name,
                label: name,
                note: join(
                  d?.default && t("default"),
                  d?.version != null && t("version", { version: d.version }),
                ),
              };
            })}
            onChange={readUsers}
          />
          <small class="hint">{t("startsDistro")}</small>
        </>
      )}
      {read.error && <Notice tone="danger">{read.error}</Notice>}
      {distro && offered.length > 0 && (
        <Choices
          label={t("user")}
          value={sel && sel.distro === distro ? sel.user : undefined}
          choices={offered}
          onChange={pick}
        />
      )}
      {add.error && <Notice tone="danger">{add.error}</Notice>}
      {!sel && !detect.busy && names.length > 0 && <p class="muted">{t("pickUser")}</p>}
      <Button onClick={detectAll} disabled={detect.busy || read.busy || add.busy}>
        {t("detect")}
      </Button>
    </>
  );
}

/** The block itself: nothing unless the host offers WSL here. */
export function EnvironmentBlock(props: { env: Environment }) {
  const { env } = props;
  if (env.error) return <Notice tone="danger">{env.error}</Notice>;
  if (!env.available) return null;
  return (
    <Card title={t("title")}>
      <p class="muted">{t("description")}</p>
      <Choices
        label={t("where")}
        value={env.mode}
        choices={[
          { value: "local", label: t("windows") },
          { value: "wsl", label: t("wsl") },
        ]}
        onChange={env.setMode}
      />
      {env.mode === "wsl" && <Wsl env={env} />}
    </Card>
  );
}

/** The button a page shows under a refusal saying its package's Linux build is not installed for
 * WSL: it installs it (in a native confirmation), then onInstalled runs (the page reads again). */
export function LinuxBuildOffer(props: {
  error?: string;
  pkg: string;
  record?: EnvRecord;
  onInstalled?: () => void;
}) {
  const install = useAction(props.onInstalled ?? (() => {}));
  const { record } = props;
  if (!record || !linuxBuildMissing(props.error)) return null;
  return (
    <>
      {install.error && <Notice tone="danger">{install.error}</Notice>}
      <Button
        disabled={install.busy}
        onClick={() =>
          install.run(() =>
            reply("device_install_wsl", { package: props.pkg, platform: linuxPlatform(record) }),
          )
        }
      >
        {t("installLinux")}
      </Button>
    </>
  );
}

/** Whether a path field's value can be sent in the given distribution (null: this computer): not a
 * UNC path the field has not converted yet, nor one outside the distribution. */
export const plainPath = (value: string, distro: string | undefined) => {
  if (!distro) return true;
  const c = linuxPath(value, distro);
  return "path" in c && !c.converted;
};

/** A typed path field. In a WSL distribution (distro set), a UNC path of that distribution pasted
 * or dropped in, or left typed in when the field loses focus, becomes its Linux path with a notice;
 * any other UNC path is refused under the field (plainPath tells the page not to send it). */
export function PathText(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  distro?: string;
  list?: string;
}) {
  const id = useId();
  const [converted, setConverted] = useState<string>();
  const { distro } = props;
  const convert = (v: string) => {
    if (!distro) return false;
    const c = linuxPath(v, distro);
    if (!("path" in c) || !c.converted) return false;
    setConverted(c.path);
    props.onInput(c.path);
    return true;
  };
  const refused = distro && "refused" in linuxPath(props.value, distro);
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
        onInput={(e) => {
          const v = e.currentTarget.value;
          const pasted = (e as InputEvent).inputType?.startsWith("insertFrom");
          if (pasted && convert(v)) return;
          setConverted(undefined);
          props.onInput(v);
        }}
        onBlur={(e) => convert(e.currentTarget.value)}
      />
      {props.hint && <small class="hint">{props.hint}</small>}
      {refused && <Notice tone="danger">{t("outside")}</Notice>}
      {!refused && converted !== undefined && converted === props.value && (
        <Notice tone="ok">{t("converted", { path: converted })}</Notice>
      )}
    </label>
  );
}
