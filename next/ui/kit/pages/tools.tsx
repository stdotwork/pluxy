// Tools: the coding tools pluxy found and whether each is connected, three steps to set one up with
// a key made for it already in the text, the address for any other program, and the keys tools
// send, named, made and deleted here. The panel and the desktop app show this page; the app adds
// to it through ToolsOptions (a button for a row, rows pluxy does not scan, the names it keeps
// for keys). ToolList is the coding tools alone, for the guided setup.
//
// A key's value is held in this page's memory only: never in prefs, storage or the address. It
// goes when the person closes its notice, deletes the key or leaves the page.

import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import {
  type Account,
  type ClientConnection,
  type ClientKey,
  problem,
  type Setup,
  type SetupClient,
} from "../api";
import { Icon } from "../icons";
import { Phases, refreshWord, StaleNotice, StatePane, ToolRow, tryAgainWord } from "../parts";
import { usePrivacy } from "../privacy";
import type { PageDef } from "../shell";
import { rich, strings } from "../strings";
import {
  Button,
  ConfirmButton,
  type Loaded,
  Notice,
  PageHead,
  Time,
  useAction,
  useCopy,
  useKit,
  useLoad,
  usePoll,
} from "../ui";
import { onThisMachine } from "./setup.helpers";
import {
  groupTools,
  hasPasteText,
  isUnchecked,
  listsAppTools,
  MODEL_PLACEHOLDER,
  requestsByKey,
  type ToolGroups,
  toolKeyName,
  toolName,
  toolsAddress,
  withKey,
} from "./tools.helpers";

const t = strings("tools", {
  title: "Tools",
  tip: "Connect coding tools and manage their keys",
  codingTools: "Coding tools",
  otherTools: "Other tools ({count})",
  looking: "Looking for coding tools…",
  older: "This pluxy cannot look for coding tools. Use the address and a key below in your tool's settings.",
  summary: "{installed} installed · {connected} connected · checked {when}",
  summaryUnread: "{installed} installed · {connected} connected",
  elsewhere: "Detected on the computer pluxy runs on.",
  none: "No coding tools found.",
  appTools: "Claude Desktop, Grok Build and Hermes Agent are set up from the desktop app.",
  localPages: "Set up a tool on this computer:",
  noAccount: "pluxy has no accounts yet, so a coding tool you set up here will get errors.",
  addAccount: "Add an account",
  setUp: "Set up",
  byHand: "Set up by hand",
  hide: "Hide",
  connect: "Connect",
  connectAgain: "Connect again",
  disconnect: "Disconnect",
  connectHint:
    "Connect makes a key for {tool} and writes it into {path} on this computer. pluxy keeps a copy of the file as it was, and Disconnect puts it back.",
  connectHintThere:
    "Connect makes a key for {tool} and writes it into {path} on the computer pluxy runs on. pluxy keeps a copy of the file as it was, and Disconnect puts it back.",
  connected: "Connected. Start {tool} again so it reads the new settings.",
  disconnectedRestored: "Disconnected. {path} is back as it was.",
  disconnectedMerged: "Disconnected. pluxy took its settings out of {path} and kept the changes made since.",
  disconnectedRemoved: "Disconnected. pluxy removed {path}, which it had made.",
  disconnected: "Disconnected.",
  connectFailed: "Could not connect {tool}. {reason}",
  disconnectFailed: "Could not disconnect {tool}. {reason}",
  fromApp: "Set up from the desktop app",
  needsAddOn: "Needs its add-on",

  stepsLoading: "Loading the steps…",
  stepsLabel: "Steps to set up {tool}",
  step1: "Make a key for {tool}.",
  makeKey: "Make key",
  step1Done: "Done. The key is in the text below.",
  step1Again:
    "pluxy already has a key named {name}, but a key is shown only once, when it is made, so its value cannot be shown again. You can make a new one for the text below. The old one keeps working until you delete it in Keys on the Tools page.",
  makeAnother: "Make another key",
  step2: "Put this in {path}:",
  keepFile: "If the file already has settings, add these lines to them instead of replacing the file.",
  makeFirst: "Make the key first, or replace {placeholder} with a key you have.",
  model: "Replace {placeholder} with a model your accounts serve:",
  models: "Models your tools can ask for",
  modelLabel: "Model {tool} starts with",
  modelWhy:
    "{tool} asks for a model of its own unless it is told which one, and your accounts may not serve that one. Pick a model your accounts serve.",
  noModels: "pluxy serves no models yet, so the text below has {placeholder} where a model goes.",
  step3: "Start {tool} again so it reads the new settings.",
  copy: "Copy",
  copied: "Copied",
  copyFailed: "Could not copy. Select the text and copy it yourself.",

  address: "Address for tools",
  thisComputer: "Only programs on this computer can reach it.",
  network: "Other computers on your network can reach it too.",
  otherPrograms: "Another program can use pluxy too: System lists the exact address each kind needs.",
  otherProgramsLink: "Addresses for other programs",

  keys: "Keys",
  keysCount: "Keys ({count})",
  keysHint: "Every tool sends one of these keys with its requests.",
  newKey: "New key",
  name: "Name",
  namePlaceholder: "e.g. Script on my laptop",
  cancel: "Cancel",
  makeFailed: "Could not make the key. {reason}",
  fresh: "Copy this key now. pluxy shows it only this once.",
  close: "Close",
  loadingKeys: "Loading keys…",
  keysFailed: "Could not read the keys.",
  noKeys: "No keys. Until there is one, any program that reaches pluxy can use it.",
  requests: { one: "{count} request this week", other: "{count} requests this week" },
  unused: "Not used this week",
  rename: "Rename",
  save: "Save",
  delete: "Delete",
  askNamed: "{name} stops working until it gets a new key.",
  askUnnamed: "Any tool using this key stops working.",
  lastKey: "Add another key before deleting this one.",
  deleteFailed: "Could not delete the key. {reason}",
  noRename: "This pluxy cannot rename keys.",
  renameFailed: "Could not rename the key. {reason}",
  once: "A key is shown only once, when it is made.",
});

/** What a host adds to the Tools page; the panel adds nothing. */
export interface ToolsOptions {
  /**
   * What goes at the end of a tool's row in place of Set up (the app's own button for the tool);
   * nothing, null or false for none, and the row then offers Set up.
   */
  toolAction?: (client: SetupClient) => ComponentChildren;
  /** Tools the host adds to core's list (the app's, for the tools core does not read). */
  extraTools?: SetupClient[];
  /** The tool each key was made for, by key ID or by masked key, as the host keeps it. */
  keyOwners?: () => Promise<Map<string, string>>;
  /** The tools listed are on another computer than the one showing the page. */
  isRemote?: boolean;
  /** Links to the host's own pages for tools on this computer, shown when pluxy is elsewhere. */
  localPages?: { name: string; page: string }[];
  /** Runs on Refresh, so the host can read its own tools again. */
  onRefresh?: () => void;
  /** Lines the host puts above and under the list of tools, from the tools it lists. */
  notes?: ToolNotes;
}

/** What a host says around the list of tools: above it, and under it. */
export type ToolNotes = (clients: SetupClient[]) => { above?: ComponentChildren; below?: ComponentChildren };

// ---- keys made on this page -------------------------------------------------------------------

/** A key made on this page, whose value is shown until it is closed. */
export interface MadeKey {
  id: string;
  value: string;
  label?: string;
  /** The ID of the tool whose text it fills; none for a key made with New key. */
  tool?: string;
}

/** The keys made on this page, in memory only. */
export interface MadeKeys {
  list: MadeKey[];
  add(key: MadeKey): void;
  /** Forgets a key's value, as closing its notice or deleting the key does. */
  close(id: string): void;
}

export function useMadeKeys(): MadeKeys {
  const [list, setList] = useState<MadeKey[]>([]);
  return {
    list,
    add: (key) => setList((keys) => [...keys.filter((k) => k.id !== key.id), key]),
    close: (id) => setList((keys) => keys.filter((k) => k.id !== id)),
  };
}

// ---- what pluxy found --------------------------------------------------------------------------

/** What the list of tools reads from pluxy, and the scan that fills it. */
export interface ToolsState {
  setup: Loaded<Setup>;
  /** pluxy has no scan to read: the page is for an older pluxy. */
  older: boolean;
  /** A scan asked for from this page failed, with why. */
  scanError?: string;
  /** A scan asked for from this page is running. */
  scanBusy: boolean;
  accounts: Loaded<{ accounts: Account[] }>;
  /** Asks pluxy to scan again, then reads what it found. */
  scan(): Promise<void>;
  /**
   * Asks pluxy to scan again and waits until it has (pluxy scans in the background): gives the
   * setup read once the scan is over, none when asking or reading failed.
   */
  rescan(): Promise<Setup | undefined>;
}

/**
 * What the tools state asks of pluxy as its page opens. A pluxy that never scanned always scans;
 * with lookAgain a pluxy that did is asked to look at its tools again too, for a page that must show
 * what they are now (a tool connected on another page since the kept scan).
 */
export function useToolsState(opts: { lookAgain?: boolean } = {}): ToolsState {
  const { api, unauthorized } = useKit();
  const status = useRef(0);
  const setup = useLoad(async () => {
    const reply = await api.setup();
    status.current = reply.status;
    return reply;
  });
  const accounts = useLoad(api.accounts);
  const [scanError, setScanError] = useState<string>();
  const [scanBusy, setScanBusy] = useState(false);
  const scanning = useRef(false);
  // Asks for a scan; true when pluxy took the request.
  const start = async (): Promise<boolean> => {
    if (scanning.current) return false;
    scanning.current = true;
    setScanBusy(true);
    setScanError(undefined);
    const reply = await api.scanSetup();
    scanning.current = false;
    setScanBusy(false);
    if (reply.status === 401) {
      unauthorized();
      return false;
    }
    const why = problem(reply);
    setScanError(why);
    setup.reload();
    return why === undefined;
  };
  const scan = async () => {
    await start();
  };
  // Who waits for a scan to be over, given the setup read then (none when the read failed).
  const waiting = useRef<((found: Setup | undefined) => void)[]>([]);
  const rescan = async (): Promise<Setup | undefined> => {
    if (!(await start())) return undefined;
    return new Promise((resolve) => waiting.current.push(resolve));
  };
  // A pluxy that never scanned scans once as the page opens (so does one that did, for lookAgain);
  // while it scans, the page asks again every second (not while hidden), and stops when the scan is
  // over or the page is left. A scan already running as the page opens is the one for it.
  const asked = useRef(false);
  const data = setup.data;
  const running = data?.scanning === true;
  useEffect(() => {
    if (!data || asked.current) return;
    if (data.scanning || (data.scanned_at && !opts.lookAgain)) {
      asked.current = data.scanning === true;
      return;
    }
    asked.current = true;
    void scan();
  }, [data]);
  usePoll(() => !setup.loading && setup.reload(), 1000, running);
  useEffect(() => {
    if (waiting.current.length === 0 || setup.loading) return;
    if (setup.phase !== "failed" && (!data || data.scanning)) return;
    const done = waiting.current;
    waiting.current = [];
    for (const resolve of done) resolve(setup.phase === "failed" ? undefined : data);
  }, [data, setup.loading]);
  return {
    setup,
    older: setup.phase === "failed" && status.current === 404,
    scanError,
    scanBusy,
    accounts,
    scan,
    rescan,
  };
}

/** When the data on screen was last read, for a notice that a refresh failed over it. */
function useSeenAt(data: unknown): number {
  const seen = useRef({ data, at: Date.now() });
  if (seen.current.data !== data) seen.current = { data, at: Date.now() };
  return seen.current.at;
}

/** The tool names the person reads, whatever core calls them. */
const named = (client: SetupClient): SetupClient => ({ ...client, name: toolName(client) });

function CopyButton(props: { value: string }) {
  const { state, copy } = useCopy(props.value);
  return (
    <>
      <Button onClick={copy}>{state === "copied" ? t("copied") : t("copy")}</Button>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </>
  );
}

/** The steps to set one tool up, with the text for its settings file. */
function Steps(props: { client: SetupClient; made: MadeKeys; onKeysChanged?: () => void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const tool = toolName(props.client);
  // The model the person picked; until they pick, the text names pluxy's best match.
  const [model, setModel] = useState<string>();
  const paste = useLoad(() => api.clientPaste(props.client.id, undefined, model));
  const askedFor = useRef(model);
  useEffect(() => {
    if (askedFor.current === model) return;
    askedFor.current = model;
    paste.reload();
  }, [model, paste.reload]);
  // Keys that already carry the tool's name: made here before, so Make key would add another.
  const keys = useLoad(api.clientKeys);
  const earlier = (keys.data?.keys ?? []).filter((k) => k.label?.trim() === tool).length;
  const key = props.made.list.find((k) => k.tool === props.client.id);
  const [error, setError] = useState<string>();
  const done = useRef<HTMLParagraphElement>(null);
  const justMade = useRef(false);
  useEffect(() => {
    if (key && justMade.current) done.current?.focus();
  }, [key?.id]);
  const make = async () => {
    setError(undefined);
    const reply = await api.addClientKey(tool);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) return setError(t("makeFailed", { reason: why }));
    if (!reply.body) return;
    justMade.current = true;
    props.made.add({
      id: reply.body.key.id,
      value: reply.body.value,
      label: reply.body.key.label ?? tool,
      tool: props.client.id,
    });
    props.onKeysChanged?.();
    keys.reload();
  };
  if (paste.phase === "loading") return <Notice tone="muted">{t("stepsLoading")}</Notice>;
  const data = paste.data;
  if (!data) {
    return (
      <div class="tool-steps-failed">
        <Notice tone="danger">{p.text(paste.error ?? "")}</Notice>
        <Button onClick={paste.reload}>{tryAgainWord()}</Button>
      </div>
    );
  }
  const placeholder = <span class="mono">{data.key}</span>;
  return (
    <ol class="tool-steps" data-part="tool-steps" aria-label={t("stepsLabel", { tool })}>
      <li>
        {key ? (
          <p ref={done} tabIndex={-1}>
            {t("step1Done")}
          </p>
        ) : (
          <>
            <p>{t("step1", { tool })}</p>
            {earlier > 0 && <p class="muted">{t("step1Again", { name: tool })}</p>}
            {error && <Notice tone="danger">{error}</Notice>}
            <div class="actions">
              <Button onClick={make}>{earlier > 0 ? t("makeAnother") : t("makeKey")}</Button>
            </div>
          </>
        )}
      </li>
      <li>
        {(data.models?.length ?? 0) > 0 ? (
          <label class="field" data-part="field">
            <span class="label">{t("modelLabel", { tool })}</span>
            <select
              data-slot="select-trigger"
              value={data.model ?? ""}
              onChange={(e) => setModel(e.currentTarget.value)}
            >
              {data.model && !data.models?.includes(data.model) && (
                <option value={data.model}>{data.model}</option>
              )}
              {data.models?.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
            <span class="muted">{t("modelWhy", { tool })}</span>
          </label>
        ) : (
          // A pluxy that does not send the list (older) is not one that serves nothing.
          data.models && (
            <p class="muted">
              {rich(t("noModels"), { placeholder: <span class="mono">{MODEL_PLACEHOLDER}</span> })}
            </p>
          )
        )}
        {data.files.map((file) => {
          const text = withKey(file.content, data.key, key?.value);
          return (
            <div key={file.path} class="tool-file">
              <div class="tool-file-head">
                <span>{rich(t("step2"), { path: <span class="mono">{file.path}</span> })}</span>
                <CopyButton value={text} />
              </div>
              {/* biome-ignore lint/a11y/noNoninteractiveTabindex: a long line scrolls sideways, and keys must reach it */}
              <pre class="tool-text" tabIndex={0}>
                {text}
              </pre>
            </div>
          );
        })}
        <p class="muted">{t("keepFile")}</p>
        {!key && <p class="muted">{rich(t("makeFirst"), { placeholder })}</p>}
        {data.files.some((file) => file.content.includes(MODEL_PLACEHOLDER)) && (
          <p class="muted">
            {rich(t("model"), {
              placeholder: <span class="mono">{MODEL_PLACEHOLDER}</span>,
            })}{" "}
            <a href="#/system">{t("models")}</a>
          </p>
        )}
        {data.note && <p class="muted">{p.text(data.note)}</p>}
      </li>
      <li>
        <p>{t("step3", { tool })}</p>
      </li>
    </ol>
  );
}

/** What a Connect or Disconnect left to say under a tool's row. */
interface RowNote {
  tone: "ok" | "danger";
  text: string;
}

/** What a finished disconnect says, from what it did to the tool's file. */
function disconnectedText(reply: ClientConnection): string {
  const file = reply.files[0];
  const path = file?.path ?? reply.client.config;
  switch (file?.change) {
    case "restored":
      return t("disconnectedRestored", { path });
    case "merged":
      return t("disconnectedMerged", { path });
    case "removed":
      return t("disconnectedRemoved", { path });
  }
  return t("disconnected");
}

/**
 * Connect, or Disconnect for a tool pluxy connected: pluxy writes the tool's settings on its own
 * computer. Connect again when pluxy connected it and its settings no longer point at pluxy.
 */
function ConnectButtons(props: {
  client: SetupClient;
  onDone: (note: RowNote) => void;
  onChanged: () => void;
}) {
  const { api } = useKit();
  const p = usePrivacy();
  const tool = toolName(props.client);
  const byPluxy = props.client.connected_by_pluxy === true;
  // Which call runs, and its reply: read when it ends, so a ref, not the render's state.
  const verb = useRef<"connect" | "disconnect">("connect");
  const reply = useRef<ClientConnection | undefined>(undefined);
  const [running, setRunning] = useState<"connect" | "disconnect">();
  const action = useAction(() => {
    const r = reply.current;
    props.onDone({
      tone: "ok",
      text: verb.current === "connect" || !r ? t("connected", { tool }) : disconnectedText(r),
    });
    props.onChanged();
  });
  useEffect(() => {
    if (!action.error) return;
    const reason = p.text(action.error);
    props.onDone({
      tone: "danger",
      text:
        verb.current === "connect"
          ? t("connectFailed", { tool, reason })
          : t("disconnectFailed", { tool, reason }),
    });
    // A refused call changes nothing, but what core reads may have changed meanwhile: read again.
    props.onChanged();
  }, [action.error]);
  const run = (which: "connect" | "disconnect") => {
    verb.current = which;
    setRunning(which);
    return action.run(async () => {
      const r =
        which === "connect"
          ? await api.connectClient(props.client.id, tool)
          : await api.disconnectClient(props.client.id);
      reply.current = r.body;
      return r;
    });
  };
  const busy = (which: "connect" | "disconnect") => action.busy && running === which;
  return (
    <>
      {(!byPluxy || props.client.state !== "configured") && (
        <Button busy={busy("connect")} onClick={() => run("connect")}>
          {byPluxy ? t("connectAgain") : t("connect")}
        </Button>
      )}
      {byPluxy && (
        <Button busy={busy("disconnect")} onClick={() => run("disconnect")}>
          {t("disconnect")}
        </Button>
      )}
    </>
  );
}

/** A note under a row, focused when it comes, so a keyboard or screen reader user hears it. */
function NoteLine(props: { note: RowNote }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => ref.current?.focus(), [props.note]);
  return (
    <div ref={ref} tabIndex={-1} class="tool-note" data-part="tool-note">
      <Notice tone={props.note.tone}>{props.note.text}</Notice>
    </div>
  );
}

/** What the list of tools needs from its page. */
export interface ToolListProps {
  toolAction?: ToolsOptions["toolAction"];
  extraTools?: SetupClient[];
  localPages?: ToolsOptions["localPages"];
  /** Runs on Refresh, so the host can read its own tools again. */
  onRefresh?: () => void;
  notes?: ToolNotes;
  /** The tools are on another computer than the one showing the page. */
  remote?: boolean;
  /** Opens the host's own way to add an account, in place of the link to Add an account. */
  onAddAccount?: () => void;
}

/** The list of tools, from what a page read (ToolsState), with the keys it holds. */
export function ToolListView(
  props: ToolListProps & {
    state: ToolsState;
    made: MadeKeys;
    onKeysChanged?: () => void;
    /** Show Refresh with the summary: for a list without a page header of its own. */
    refresh?: () => unknown;
  },
) {
  const { host } = useKit();
  const { state } = props;
  const setup = state.setup;
  const [open, setOpen] = useState<string>();
  const [rowNotes, setRowNotes] = useState<Record<string, RowNote | undefined>>({});
  const remote = props.remote ?? !onThisMachine(host.address);
  const rows = {
    toolAction: props.toolAction,
    extraTools: props.extraTools,
    localPages: props.localPages,
    notes: props.notes,
    remote,
    open,
    setOpen,
    rowNotes,
    setRowNote: (id: string, note?: RowNote) => setRowNotes((n) => ({ ...n, [id]: note })),
    // A connect or disconnect changed the tool's settings and the keys: both are read again.
    onConnected: () => {
      setup.reload();
      props.onKeysChanged?.();
    },
    made: props.made,
    onKeysChanged: props.onKeysChanged,
    refresh: props.refresh,
  };
  const noAccount = state.accounts.phase === "ready" && state.accounts.data?.accounts.length === 0;
  const account = (
    <Notice tone="warn">
      {t("noAccount")}{" "}
      {props.onAddAccount ? (
        <Button onClick={props.onAddAccount}>{t("addAccount")}</Button>
      ) : (
        <a href="#/add-account">{t("addAccount")}</a>
      )}
    </Notice>
  );
  const seen = useSeenAt(setup.data);
  const body = () => {
    const data = setup.data;
    // No list and no way to get one: an older pluxy, or a scan that failed before it gave one.
    if ((setup.phase === "failed" && !data) || (data && !data.scanned_at && state.scanError)) {
      return (
        <>
          <Notice tone="muted">{t("older")}</Notice>
          {!state.older && (
            <div class="actions">
              <Button onClick={data ? state.scan : setup.reload}>{tryAgainWord()}</Button>
            </div>
          )}
          {props.extraTools && (
            <ToolRows {...rows} groups={groupTools([], props.extraTools)} summary={false} />
          )}
        </>
      );
    }
    // Nothing is known until the first scan ends: loading, then a scan about to start or running.
    if (!data?.scanned_at) return <StatePane kind="loading" title={t("looking")} />;
    const groups = groupTools(data.found.clients, props.extraTools);
    return (
      <>
        {setup.phase === "failed" && <StaleNotice at={seen} error={setup.error} onRetry={setup.reload} />}
        {state.scanError && (
          <StaleNotice at={Date.parse(data.scanned_at)} error={state.scanError} onRetry={state.scan} />
        )}
        <ToolRows
          {...rows}
          groups={groups}
          scannedAt={data.scanned_at}
          appTools={!props.extraTools && !listsAppTools(groups)}
        />
      </>
    );
  };
  return (
    <section class="tools-list" aria-labelledby="tools-coding">
      {noAccount && account}
      <h2 id="tools-coding" class="tools-heading">
        {t("codingTools")}
      </h2>
      {body()}
    </section>
  );
}

/** The summary line, the rows and the lines under them. */
function ToolRows(props: {
  groups: ToolGroups;
  toolAction?: ToolsOptions["toolAction"];
  extraTools?: SetupClient[];
  localPages?: ToolsOptions["localPages"];
  notes?: ToolNotes;
  remote: boolean;
  open?: string;
  setOpen: (id: string | undefined) => void;
  /** What a Connect or Disconnect said under each row, by tool ID. */
  rowNotes: Record<string, RowNote | undefined>;
  setRowNote: (id: string, note?: RowNote) => void;
  onConnected: () => void;
  /** When pluxy last scanned; without it the summary has no time. */
  scannedAt?: string;
  /** Show the summary line (the rows may come with no scan to count). */
  summary?: boolean;
  /** Say the three tools only the app sets up are set up from the app. */
  appTools?: boolean;
  made: MadeKeys;
  onKeysChanged?: () => void;
  refresh?: () => unknown;
}) {
  const { groups } = props;
  const shown = [...groups.connected, ...groups.installed];
  const connected = groups.connected.length;
  const installed = shown.length;
  const remote = props.remote;
  const notes = props.notes?.([...shown, ...groups.other]);
  const row = (client: SetupClient) => {
    const custom = props.toolAction?.(client);
    const hasCustom = custom !== undefined && custom !== null && custom !== false && custom !== "";
    const opened = props.open === client.id;
    // pluxy writes the settings of the tools it can connect itself; the steps to paste them stay,
    // as Set up by hand, until pluxy has connected the tool. A tool already connected by hand keeps
    // its Set up alone.
    const byPluxy = !hasCustom && client.connected_by_pluxy === true;
    const connect =
      !hasCustom &&
      (byPluxy || (client.can_connect === true && client.installed && client.state !== "configured"));
    const note = props.rowNotes[client.id];
    // Next to Connect, the steps by hand are the lesser way: a ghost button.
    const toggle = (
      <Button
        variant={connect ? "ghost" : undefined}
        onClick={() => props.setOpen(opened ? undefined : client.id)}
      >
        {opened ? t("hide") : connect ? t("byHand") : t("setUp")}
      </Button>
    );
    // A tool with no text to paste is set up from the app; where the app gives rows (extraTools)
    // and its add-on is missing, it says so.
    const action = hasCustom ? (
      custom
    ) : connect ? (
      <>
        <ConnectButtons
          client={client}
          onDone={(n) => {
            props.setRowNote(client.id, n);
            if (n.tone === "ok") props.setOpen(undefined);
          }}
          onChanged={props.onConnected}
        />
        {!byPluxy && hasPasteText(client) && toggle}
      </>
    ) : !hasPasteText(client) ? (
      <span class="muted">{props.extraTools ? t("needsAddOn") : t("fromApp")}</span>
    ) : (
      toggle
    );
    const steps = !hasCustom && hasPasteText(client) && opened;
    const hint = connect && !byPluxy && !note && !steps;
    const path = <span class="mono">{client.config}</span>;
    return (
      <ToolRow key={client.id} client={named(client)} unchecked={isUnchecked(client)} action={action}>
        {(note || hint || steps) && (
          <>
            {note && <NoteLine note={note} />}
            {hint && (
              <p class="muted tool-hint">
                {rich(remote ? t("connectHintThere") : t("connectHint"), { tool: toolName(client), path })}
              </p>
            )}
            {steps && <Steps client={client} made={props.made} onKeysChanged={props.onKeysChanged} />}
          </>
        )}
      </ToolRow>
    );
  };
  return (
    <>
      {notes?.above}
      {props.summary !== false && (
        <p class="tools-summary">
          <span>
            {props.scannedAt
              ? rich(t("summary", { installed, connected }), { when: <Time value={props.scannedAt} /> })
              : t("summaryUnread", { installed, connected })}
            {remote && <> {t("elsewhere")}</>}
          </span>
          {props.refresh && <Button onClick={props.refresh}>{refreshWord()}</Button>}
        </p>
      )}
      {shown.length === 0 && <p class="tools-none">{t("none")}</p>}
      {shown.length > 0 && <div class="tool-rows">{shown.map(row)}</div>}
      {groups.other.length > 0 && (
        <details class="tools-other" open={shown.length === 0}>
          <summary>
            <span class="tools-other-mark">
              <Icon name="chevron" />
            </span>
            {t("otherTools", { count: groups.other.length })}
          </summary>
          <div class="tool-rows">{groups.other.map(row)}</div>
        </details>
      )}
      {props.localPages && props.localPages.length > 0 ? (
        <p class="muted tools-more">
          {t("localPages")}{" "}
          {props.localPages.map((page, i) => (
            <span key={page.page}>
              {i > 0 && ", "}
              <a href={`#/${page.page}`}>{page.name}</a>
            </span>
          ))}
        </p>
      ) : (
        props.appTools && <p class="muted tools-more">{t("appTools")}</p>
      )}
      {notes?.below}
    </>
  );
}

/** The coding tools on their own, for a page that has no Address and Keys beside them. */
export function ToolList(props: ToolListProps) {
  const state = useToolsState();
  const made = useMadeKeys();
  const refresh = async () => {
    await state.scan();
    props.onRefresh?.();
  };
  return <ToolListView {...props} state={state} made={made} refresh={refresh} />;
}

// ---- address ------------------------------------------------------------------------------------

function AddressSection(props: { state: ToolsState; remote: boolean }) {
  const { host } = useKit();
  const found = props.state.setup.data;
  const address = toolsAddress(host.address, found);
  const { state, copy } = useCopy(address ?? "");
  const network = found?.found.core.reachable_from_network;
  return (
    <section class="tools-section" aria-labelledby="tools-address">
      <h2 id="tools-address" class="tools-heading">
        {t("address")}
      </h2>
      {address && (
        <div class="tools-address">
          <code class="mono">{address}</code>
          <Button onClick={copy}>{state === "copied" ? t("copied") : t("copy")}</Button>
        </div>
      )}
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
      {!props.remote && network !== undefined && (
        <p class="muted">{network ? t("network") : t("thisComputer")}</p>
      )}
      <p class="muted">
        {t("otherPrograms")} <a href="#/system">{t("otherProgramsLink")}</a>
      </p>
    </section>
  );
}

// ---- keys ---------------------------------------------------------------------------------------

/** A key just made: its value, shown once, with Copy and Close. */
function FreshKey(props: { value: string; name?: string; onClose: () => void }) {
  const { state, copy } = useCopy(props.value);
  return (
    <div class="tools-fresh" data-part="key-notice">
      <Notice tone="ok">
        <span class="tools-fresh-text">
          {props.name && <strong>{props.name}. </strong>}
          {t("fresh")}
        </span>
        <span class="tools-fresh-key">
          <code class="mono" data-part="key-value">
            {props.value}
          </code>
          <Button onClick={copy}>{state === "copied" ? t("copied") : t("copy")}</Button>
          <Button onClick={props.onClose}>{t("close")}</Button>
        </span>
      </Notice>
      {state === "failed" && <Notice tone="danger">{t("copyFailed")}</Notice>}
    </div>
  );
}

/** The form for a key made by hand: its name, and Make key. */
function NewKeyForm(props: { onMade: (key: MadeKey) => void; onCancel: () => void }) {
  const { api, unauthorized } = useKit();
  const [name, setName] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.focus(), []);
  const submit = async (e: Event) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(undefined);
    const reply = await api.addClientKey(name.trim());
    setBusy(false);
    if (reply.status === 401) return unauthorized();
    const why = problem(reply);
    if (why) return setError(t("makeFailed", { reason: why }));
    if (reply.body)
      props.onMade({ id: reply.body.key.id, value: reply.body.value, label: reply.body.key.label });
  };
  return (
    <form class="tools-new" onSubmit={submit}>
      <label class="tools-new-name">
        <span>{t("name")}</span>
        <input
          ref={input}
          type="text"
          value={name}
          maxLength={64}
          placeholder={t("namePlaceholder")}
          onInput={(e) => setName(e.currentTarget.value)}
        />
      </label>
      <div class="actions">
        <Button variant="primary" type="submit" busy={busy}>
          {t("makeKey")}
        </Button>
        <Button onClick={props.onCancel}>{t("cancel")}</Button>
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
    </form>
  );
}

/** One key: its name, its use this week, Rename and Delete. */
function KeyRow(props: {
  item: ClientKey;
  owners?: ReadonlyMap<string, string>;
  used?: number;
  only: boolean;
  canRename: boolean;
  onRenamed: () => void;
  onNoRename: () => void;
  onDeleted: (id: string) => void;
  onStale: () => void;
}) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const key = props.item;
  const name = toolKeyName(key, props.owners, p.id);
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState("");
  const [renameError, setRenameError] = useState<string>();
  const [deleteError, setDeleteError] = useState<string>();
  const [lastKey, setLastKey] = useState(false);
  const rename = async (e: Event) => {
    e.preventDefault();
    setRenameError(undefined);
    const reply = await api.renameClientKey(key.id, draft);
    if (reply.status === 401) return unauthorized();
    if (reply.status === 405) {
      setRenaming(false);
      return props.onNoRename();
    }
    const why = problem(reply);
    if (why) return setRenameError(t("renameFailed", { reason: why }));
    setRenaming(false);
    props.onRenamed();
  };
  const remove = async () => {
    setDeleteError(undefined);
    setLastKey(false);
    const reply = await api.deleteClientKey(key.id);
    if (reply.status === 401) return unauthorized();
    if (reply.status === 409) return setLastKey(true);
    if (reply.status === 404) return props.onStale();
    const why = problem(reply);
    if (why) return setDeleteError(t("deleteFailed", { reason: why }));
    props.onDeleted(key.id);
  };
  const week =
    props.used === undefined
      ? undefined
      : props.used > 0
        ? t("requests", { count: props.used })
        : t("unused");
  return (
    <li class="tool-key" data-part="tool-key">
      <span class="tool-key-name">
        <span class="tool-key-label">{name.text}</span>
        {name.named && <span class="mono muted">{key.masked}</span>}
      </span>
      <span class="tool-key-used muted">{week}</span>
      <span class="tool-key-actions">
        {props.canRename && !renaming && (
          <Button
            onClick={() => {
              setDraft(key.label ?? "");
              setRenaming(true);
            }}
          >
            {t("rename")}
          </Button>
        )}
        <ConfirmButton
          disabled={props.only}
          question={name.named ? t("askNamed", { name: name.text }) : t("askUnnamed")}
          onConfirm={remove}
        >
          {t("delete")}
        </ConfirmButton>
      </span>
      {renaming && (
        <form class="tool-key-rename" onSubmit={rename}>
          <label>
            <span class="sr-only">{t("name")}</span>
            <input
              type="text"
              value={draft}
              maxLength={64}
              placeholder={t("namePlaceholder")}
              onInput={(e) => setDraft(e.currentTarget.value)}
              // biome-ignore lint/a11y/noAutofocus: the field opens because the person pressed Rename
              autoFocus
            />
          </label>
          <Button variant="primary" type="submit">
            {t("save")}
          </Button>
          <Button onClick={() => setRenaming(false)}>{t("cancel")}</Button>
        </form>
      )}
      {(props.only || lastKey) && <p class="tool-key-note muted">{t("lastKey")}</p>}
      {renameError && <Notice tone="danger">{renameError}</Notice>}
      {deleteError && <Notice tone="danger">{deleteError}</Notice>}
    </li>
  );
}

function KeysSection(props: {
  keys: Loaded<{ keys: ClientKey[] }>;
  week?: Map<string, number>;
  made: MadeKeys;
  owners?: ReadonlyMap<string, string>;
  /** Called when a key was made, renamed or deleted: the list and the names are read again. */
  onChanged: () => void;
}) {
  const [adding, setAdding] = useState(false);
  const [noRename, setNoRename] = useState(false);
  const p = usePrivacy();
  const list = props.keys.data?.keys;
  const newKey = (
    <Button onClick={() => setAdding(true)} disabled={adding}>
      {t("newKey")}
    </Button>
  );
  return (
    <section class="tools-section" aria-labelledby="tools-keys">
      <div class="tools-section-head">
        <h2 id="tools-keys" class="tools-heading">
          {list ? t("keysCount", { count: list.length }) : t("keys")}
        </h2>
        {list && list.length > 0 && newKey}
      </div>
      <p class="muted">{t("keysHint")}</p>
      {noRename && <Notice tone="muted">{t("noRename")}</Notice>}
      {props.made.list.map((made) => {
        const item = list?.find((k) => k.id === made.id);
        const label = item?.label ?? made.label;
        return (
          <FreshKey
            key={made.id}
            value={made.value}
            name={label ? p.id(label) : undefined}
            onClose={() => props.made.close(made.id)}
          />
        );
      })}
      {adding && (
        <NewKeyForm
          onCancel={() => setAdding(false)}
          onMade={(key) => {
            props.made.add(key);
            setAdding(false);
            props.onChanged();
          }}
        />
      )}
      <Phases
        load={props.keys}
        loading={t("loadingKeys")}
        failed={{ title: t("keysFailed") }}
        isEmpty={(data) => data.keys.length === 0}
        empty={
          <StatePane
            kind="empty"
            title={t("noKeys")}
            primary={{ label: t("newKey"), onClick: () => setAdding(true) }}
          />
        }
      >
        {(data) => (
          <>
            <ul class="tool-keys">
              {data.keys.map((item) => (
                <KeyRow
                  key={item.id}
                  item={item}
                  owners={props.owners}
                  used={props.week && (props.week.get(item.id) ?? 0)}
                  only={data.keys.length === 1}
                  canRename={!noRename}
                  onRenamed={props.onChanged}
                  onNoRename={() => setNoRename(true)}
                  onDeleted={(id) => {
                    props.made.close(id);
                    props.onChanged();
                  }}
                  onStale={props.onChanged}
                />
              ))}
            </ul>
            <p class="muted tools-once">{t("once")}</p>
          </>
        )}
      </Phases>
    </section>
  );
}

// ---- the page -----------------------------------------------------------------------------------

/** What the app has recorded about each key: read when the keys are, and when they change. */
function useOwners(read: ToolsOptions["keyOwners"], stamp: unknown) {
  const [owners, setOwners] = useState<Map<string, string>>();
  const readRef = useRef(read);
  readRef.current = read;
  useEffect(() => {
    if (!readRef.current) return;
    let live = true;
    readRef.current().then(
      (found) => live && setOwners(found),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [stamp]);
  return owners;
}

export function ToolsPage(props: { options?: ToolsOptions }) {
  const { api, host } = useKit();
  const options = props.options;
  const state = useToolsState();
  const keys = useLoad(api.clientKeys);
  const usage = useLoad(() => api.usage("since=7d"));
  const made = useMadeKeys();
  const owners = useOwners(options?.keyOwners, keys.data);
  const remote = options?.isRemote === true || !onThisMachine(host.address);
  const refresh = async () => {
    keys.reload();
    usage.reload();
    await state.scan();
    options?.onRefresh?.();
  };
  const changed = () => {
    keys.reload();
    usage.reload();
  };
  return (
    <>
      <PageHead title={t("title")} actions={<Button onClick={refresh}>{refreshWord()}</Button>} />
      <div class="tools stack">
        <ToolListView
          toolAction={options?.toolAction}
          extraTools={options?.extraTools}
          localPages={options?.localPages}
          notes={options?.notes}
          remote={remote}
          state={state}
          made={made}
          onKeysChanged={changed}
        />
        <div class="tools-secondary">
          <AddressSection state={state} remote={remote} />
          <KeysSection
            keys={keys}
            week={usage.data ? requestsByKey(usage.data.keys) : undefined}
            made={made}
            owners={owners}
            onChanged={changed}
          />
        </div>
      </div>
    </>
  );
}

export const toolsPage = (options?: ToolsOptions): PageDef => ({
  id: "tools",
  dest: "tools",
  title: t("title"),
  tip: t("tip"),
  render: () => <ToolsPage options={options} />,
});
