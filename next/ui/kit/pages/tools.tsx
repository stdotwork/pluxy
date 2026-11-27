// Tools: the coding tools pluxy found and whether each is connected, three steps to set one up with
// a key made for it already in the text, the connection card with the address for any other program,
// and the keys tools send, named, made and deleted here through a row menu. The panel and the desktop
// app show this page; the app adds to it through ToolsOptions (a button for a row, rows pluxy does
// not scan, the names it keeps for keys). ToolList is the coding tools alone, for the guided setup.
//
// A key's value is held in this page's memory only: never in prefs, storage or the address. It
// goes when the person closes its notice, deletes the key or leaves the page.

import type { ComponentChildren } from "preact";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "preact/hooks";
import { type Account, type ClientKey, problem, type Setup, type SetupClient } from "../api";
import { Icon } from "../icons";
import {
  ConnectionCard,
  Phases,
  ProviderMark,
  refreshWord,
  StaleNotice,
  StatePane,
  toolState,
  tryAgainWord,
  useShownAfter,
} from "../parts";
import { usePrivacy } from "../privacy";
import { shownPath } from "../save";
import type { PageDef } from "../shell";
import { stateText, stateTone } from "../states";
import { rich, strings } from "../strings";
import {
  BUSY_SHOWS_AFTER_MS,
  Button,
  type Loaded,
  Notice,
  PageHead,
  Time,
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
  toolMarkKind,
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
  hide: "Hide",
  fromApp: "Set up from the desktop app",
  needsAddOn: "Needs its add-on",
  unchecked: "pluxy cannot see how {tool} is set up.",

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
  keyActions: "Actions for {name}",
  rename: "Rename",
  save: "Save",
  delete: "Delete",
  confirmDelete: "Yes, delete",
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

// ---- tool marks ---------------------------------------------------------------------------------

/** A coding tool's mark in a rounded tile, beside its name. */
export function ToolMark(props: { id: string; name?: string; size?: "medium" | "large" }) {
  const mark = toolMarkKind(props.id, props.name);
  const size = props.size ?? "medium";
  if (mark.kind === "provider") {
    return (
      <span class="tool-mark-tile" data-tool-mark={props.id} data-size={size} aria-hidden="true">
        <ProviderMark extension={mark.extension} size="medium" />
      </span>
    );
  }
  if (mark.kind === "opencode") {
    return (
      <span class="tool-mark-tile" data-tool-mark="opencode" data-size={size} aria-hidden="true">
        <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true">
          <rect
            x="5"
            y="4"
            width="14"
            height="16"
            rx="2.5"
            fill="none"
            stroke="currentColor"
            stroke-width="3"
          />
          <rect x="9.5" y="9" width="5" height="6" rx="0.75" fill="currentColor" opacity="0.28" />
        </svg>
      </span>
    );
  }
  if (mark.kind === "pi") {
    return (
      <span class="tool-mark-tile" data-tool-mark={props.id} data-size={size} aria-hidden="true">
        <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true">
          <path
            d="M5 7h14M9.5 7v10.5M14.5 7v8.5c0 1.2.8 2 2 2"
            fill="none"
            stroke="currentColor"
            stroke-width="2.4"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </span>
    );
  }
  return (
    <span
      class="tool-mark-tile tool-mark-initial"
      data-tool-mark={props.id}
      data-size={size}
      data-series={mark.series}
      aria-hidden="true"
    >
      {mark.letter}
    </span>
  );
}

/** Three horizontal dots for a row menu trigger. */
function EllipsisIcon() {
  return (
    <svg viewBox="0 0 16 16" width={15} height={15} fill="currentColor" aria-hidden="true" focusable="false">
      <circle cx="3.25" cy="8" r="1.25" />
      <circle cx="8" cy="8" r="1.25" />
      <circle cx="12.75" cy="8" r="1.25" />
    </svg>
  );
}

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

export function useToolsState(): ToolsState {
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
  // A pluxy that never scanned scans once as the page opens; while it scans, the page asks again
  // every second (not while hidden), and stops when the scan is over or the page is left.
  const asked = useRef(false);
  const data = setup.data;
  const running = data?.scanning === true;
  useEffect(() => {
    if (!data || data.scanned_at || data.scanning || asked.current) return;
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

/** Skeleton rows in the final shape of the tools list, shown after a brief delay. */
function ToolsSkeleton() {
  const shown = useShownAfter(BUSY_SHOWS_AFTER_MS);
  if (!shown) return null;
  return (
    <div class="tools-skeleton" aria-hidden="true">
      {[0, 1, 2, 3].map((i) => (
        <div key={i} class="tools-skeleton-row">
          <i class="tools-skeleton-mark" />
          <div class="tools-skeleton-lines">
            <i class="tools-skeleton-title" />
            <i class="tools-skeleton-sub" />
          </div>
          <i class="tools-skeleton-btn" />
        </div>
      ))}
    </div>
  );
}

/** The steps to set one tool up, with the text for its settings file. */
function Steps(props: { client: SetupClient; made: MadeKeys; onKeysChanged?: () => void }) {
  const { api, unauthorized } = useKit();
  const p = usePrivacy();
  const tool = toolName(props.client);
  const paste = useLoad(() => api.clientPaste(props.client.id));
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
        <span class="tool-step-num" aria-hidden="true">
          1
        </span>
        <div class="tool-step-body">
          {key ? (
            <p ref={done} tabIndex={-1} class="tool-step-done">
              <Icon name="check" size={14} />
              <span>{t("step1Done")}</span>
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
        </div>
      </li>
      <li>
        <span class="tool-step-num" aria-hidden="true">
          2
        </span>
        <div class="tool-step-body">
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
        </div>
      </li>
      <li>
        <span class="tool-step-num" aria-hidden="true">
          3
        </span>
        <div class="tool-step-body">
          <p>{t("step3", { tool })}</p>
        </div>
      </li>
    </ol>
  );
}

/** One coding tool row: mark, name and version, state badge and quiet path, and one action. */
function ToolRowItem(props: {
  client: SetupClient;
  unchecked?: boolean;
  action?: ComponentChildren;
  children?: ComponentChildren;
}) {
  const p = usePrivacy();
  const c = props.client;
  const state = toolState(c, props.unchecked);
  const title = state === "unchecked" ? t("unchecked", { tool: c.name }) : undefined;
  const path = c.config && shownPath(c.config, p.on);
  return (
    <div class="tool-row" data-part="tool-row" data-state={state} data-open={props.children ? "" : undefined}>
      <div class="tools-row-line">
        <ToolMark id={c.id} name={c.name} />
        <div class="tools-row-main">
          <div class="tools-row-top">
            <span class="tool-name">{c.name}</span>
            {c.version && <span class="tool-version">{c.version}</span>}
          </div>
          <div class="tools-row-sub">
            <span
              class="badge"
              data-part="badge"
              data-tone={stateTone(state, "tool")}
              data-state={state}
              title={title}
            >
              {stateText(state, "tool")}
            </span>
            {path && (
              <span class="tools-row-path mono" title={path}>
                {path}
              </span>
            )}
          </div>
        </div>
        <span class="tool-action">{props.action}</span>
      </div>
      {props.children && <div class="tool-more">{props.children}</div>}
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
    /** Highlight the first tool needing an update as the view's filled action when no form is open. */
    highlightUpdate?: boolean;
  },
) {
  const { host } = useKit();
  const { state } = props;
  const setup = state.setup;
  const [open, setOpen] = useState<string>();
  const remote = props.remote ?? !onThisMachine(host.address);
  const rows = {
    toolAction: props.toolAction,
    extraTools: props.extraTools,
    localPages: props.localPages,
    notes: props.notes,
    remote,
    open,
    setOpen,
    made: props.made,
    onKeysChanged: props.onKeysChanged,
    refresh: props.refresh,
    highlightUpdate: props.highlightUpdate,
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
  const data = setup.data;
  const stale = Boolean(data?.scanned_at) && (setup.phase === "failed" || Boolean(state.scanError));
  const groups = data?.scanned_at ? groupTools(data.found.clients, props.extraTools) : undefined;
  const shown = groups ? [...groups.connected, ...groups.installed] : undefined;
  const summary =
    groups && shown ? (
      <p class="tools-summary">
        <span>
          {data?.scanned_at
            ? rich(t("summary", { installed: shown.length, connected: groups.connected.length }), {
                when: <Time value={data.scanned_at} />,
              })
            : t("summaryUnread", { installed: shown.length, connected: groups.connected.length })}
          {remote && <> {t("elsewhere")}</>}
        </span>
        {props.refresh && <Button onClick={props.refresh}>{refreshWord()}</Button>}
      </p>
    ) : null;

  const body = () => {
    // No list and no way to get one: an older pluxy, or a scan that failed before it gave one.
    if ((setup.phase === "failed" && !data) || (data && !data.scanned_at && state.scanError)) {
      return (
        <div class="tools-card-body">
          <Notice tone="muted">{t("older")}</Notice>
          {!state.older && (
            <div class="actions">
              <Button onClick={data ? state.scan : setup.reload}>{tryAgainWord()}</Button>
            </div>
          )}
          {props.extraTools && <ToolRows {...rows} groups={groupTools([], props.extraTools)} />}
        </div>
      );
    }
    // Nothing is known until the first scan ends: loading, then a scan about to start or running.
    if (!data?.scanned_at || !groups) {
      return (
        <div class="tools-card-body">
          <StatePane kind="loading" title={t("looking")} rows={0} />
          <ToolsSkeleton />
        </div>
      );
    }
    return (
      <>
        {setup.phase === "failed" && (
          <div class="tools-stale-wrap">
            <StaleNotice at={seen} error={setup.error} onRetry={setup.reload} />
          </div>
        )}
        {state.scanError && (
          <div class="tools-stale-wrap">
            <StaleNotice at={Date.parse(data.scanned_at)} error={state.scanError} onRetry={state.scan} />
          </div>
        )}
        <div class="tools-fade" data-stale={stale ? "" : undefined}>
          <ToolRows {...rows} groups={groups} appTools={!props.extraTools && !listsAppTools(groups)} />
        </div>
      </>
    );
  };
  return (
    <div class="tools-list-wrap">
      {noAccount && account}
      <section class="tools-list" aria-labelledby="tools-coding">
        <header class="tools-card-head">
          <div class="tools-card-title">
            <h2 id="tools-coding" class="tools-heading">
              {t("codingTools")}
            </h2>
            {summary}
          </div>
        </header>
        {body()}
      </section>
    </div>
  );
}

/** The rows and the lines under them. */
function ToolRows(props: {
  groups: ToolGroups;
  toolAction?: ToolsOptions["toolAction"];
  extraTools?: SetupClient[];
  localPages?: ToolsOptions["localPages"];
  notes?: ToolNotes;
  remote: boolean;
  open?: string;
  setOpen: (id: string | undefined) => void;
  /** Say the three tools only the app sets up are set up from the app. */
  appTools?: boolean;
  made: MadeKeys;
  onKeysChanged?: () => void;
  highlightUpdate?: boolean;
}) {
  const { groups } = props;
  const shown = [...groups.connected, ...groups.installed];
  const notes = props.notes?.([...shown, ...groups.other]);
  const firstUpdateId = props.highlightUpdate
    ? shown.find((c) => c.state === "needs-update" && hasPasteText(c) && !props.toolAction?.(c))?.id
    : undefined;
  const row = (client: SetupClient) => {
    const custom = props.toolAction?.(client);
    const hasCustom = custom !== undefined && custom !== null && custom !== false && custom !== "";
    const opened = props.open === client.id;
    // A tool with no text to paste is set up from the app; where the app gives rows (extraTools)
    // and its add-on is missing, it says so.
    const action = hasCustom ? (
      custom
    ) : !hasPasteText(client) ? (
      <span class="muted tools-action-note">{props.extraTools ? t("needsAddOn") : t("fromApp")}</span>
    ) : (
      <Button
        variant={!opened && client.id === firstUpdateId ? "primary" : "quiet"}
        onClick={() => props.setOpen(opened ? undefined : client.id)}
      >
        {opened ? t("hide") : t("setUp")}
      </Button>
    );
    return (
      <ToolRowItem key={client.id} client={named(client)} unchecked={isUnchecked(client)} action={action}>
        {!hasCustom && hasPasteText(client) && opened && (
          <Steps client={client} made={props.made} onKeysChanged={props.onKeysChanged} />
        )}
      </ToolRowItem>
    );
  };
  const foot =
    (props.localPages && props.localPages.length > 0) || props.appTools || notes?.below ? (
      <div class="tools-card-foot">
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
      </div>
    ) : null;
  return (
    <>
      {notes?.above && <div class="tools-card-note">{notes.above}</div>}
      {shown.length === 0 && <p class="tools-none">{t("none")}</p>}
      {shown.length > 0 && <div class="tool-rows">{shown.map(row)}</div>}
      {groups.other.length > 0 && (
        <details class="tools-other" open={shown.length === 0}>
          <summary>
            <span class="tools-other-mark">
              <Icon name="chevron" size={14} />
            </span>
            {t("otherTools", { count: groups.other.length })}
          </summary>
          <div class="tool-rows">{groups.other.map(row)}</div>
        </details>
      )}
      {foot}
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

// ---- connection card & address -----------------------------------------------------------------

function ConnectionSection(props: { state: ToolsState; keys?: ClientKey[]; remote: boolean }) {
  const { host } = useKit();
  const found = props.state.setup.data;
  const address = toolsAddress(host.address, found);
  const network = found?.found.core.reachable_from_network;
  return (
    <section class="tools-connection" aria-label={t("address")}>
      <ConnectionCard address={address} keys={props.keys} />
      <div class="tools-connection-foot">
        {!props.remote && network !== undefined && (
          <p class="tools-reach">
            <span class="tools-reach-dot" data-network={network ? "yes" : "no"} aria-hidden="true" />
            <span>{network ? t("network") : t("thisComputer")}</span>
          </p>
        )}
        <p class="muted">
          {t("otherPrograms")} <a href="#/system">{t("otherProgramsLink")}</a>
        </p>
      </div>
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
        <span class="label">{t("name")}</span>
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

/** One key: its name, masked value, use this week, and a row menu for Rename and Delete. */
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
  const [menuOpen, setMenuOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [draft, setDraft] = useState("");
  const [renameError, setRenameError] = useState<string>();
  const [deleteError, setDeleteError] = useState<string>();
  const [lastKey, setLastKey] = useState(false);
  const menuList = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const renameItemRef = useRef<HTMLButtonElement>(null);
  const deleteItemRef = useRef<HTMLButtonElement>(null);
  const renameInputRef = useRef<HTMLInputElement>(null);
  const cancelDeleteRef = useRef<HTMLButtonElement>(null);
  const menuTarget = useRef<"first" | "last">("first");
  const returnFocus = useRef(false);
  const questionId = useId();
  const menuId = useId();
  const lastKeyId = useId();

  const insideMenu = (target: Node | null) =>
    Boolean(triggerRef.current?.contains(target) || menuList.current?.contains(target));

  const enabledMenuItems = (): HTMLButtonElement[] =>
    [renameItemRef.current, deleteItemRef.current].filter((el): el is HTMLButtonElement =>
      Boolean(el && !el.disabled),
    );

  const focusMenuItem = (which: "first" | "last") => {
    const items = enabledMenuItems();
    if (items.length === 0) {
      menuList.current?.focus();
      return;
    }
    (which === "last" ? items[items.length - 1] : items[0])?.focus();
  };

  useLayoutEffect(() => {
    if (!menuOpen) return;
    const onPointerDown = (e: Event) => {
      if (!insideMenu(e.target as Node | null)) setMenuOpen(false);
    };
    const onFocusIn = (e: Event) => {
      if (!insideMenu(e.target as Node | null)) setMenuOpen(false);
    };
    const onKeyDown = (e: Event) => {
      if ((e as KeyboardEvent).key === "Escape") {
        setMenuOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("focusin", onFocusIn);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("focusin", onFocusIn);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [menuOpen]);

  useEffect(() => {
    if (!menuOpen) return;
    focusMenuItem(menuTarget.current);
  }, [menuOpen]);

  useEffect(() => {
    if (confirming) cancelDeleteRef.current?.focus();
  }, [confirming]);

  useEffect(() => {
    if (renaming) renameInputRef.current?.focus();
  }, [renaming]);

  useEffect(() => {
    if (!renaming && !confirming && returnFocus.current) {
      returnFocus.current = false;
      triggerRef.current?.focus();
    }
  }, [renaming, confirming]);

  const rename = async (e: Event) => {
    e.preventDefault();
    setRenameError(undefined);
    const reply = await api.renameClientKey(key.id, draft);
    if (reply.status === 401) return unauthorized();
    if (reply.status === 405) {
      returnFocus.current = true;
      setRenaming(false);
      return props.onNoRename();
    }
    const why = problem(reply);
    if (why) return setRenameError(t("renameFailed", { reason: why }));
    returnFocus.current = true;
    setRenaming(false);
    props.onRenamed();
  };

  const remove = async () => {
    if (deleting) return;
    setDeleteError(undefined);
    setLastKey(false);
    setDeleting(true);
    const reply = await api.deleteClientKey(key.id);
    setDeleting(false);
    returnFocus.current = true;
    setConfirming(false);
    if (reply.status === 401) return unauthorized();
    if (reply.status === 409) return setLastKey(true);
    if (reply.status === 404) return props.onStale();
    const why = problem(reply);
    if (why) return setDeleteError(t("deleteFailed", { reason: why }));
    props.onDeleted(key.id);
  };

  const onMenuKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      setMenuOpen(false);
      triggerRef.current?.focus();
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    const items = enabledMenuItems();
    if (items.length === 0) return;
    e.preventDefault();
    if (e.key === "Home") {
      items[0]?.focus();
      return;
    }
    if (e.key === "End") {
      items[items.length - 1]?.focus();
      return;
    }
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "ArrowDown" ? (at + 1) % items.length : (at <= 0 ? items.length : at) - 1;
    items[next]?.focus();
  };

  const week =
    props.used === undefined
      ? undefined
      : props.used > 0
        ? t("requests", { count: props.used })
        : t("unused");
  const question = name.named ? t("askNamed", { name: name.text }) : t("askUnnamed");

  return (
    <li class="tool-key" data-part="tool-key" data-menu-open={menuOpen ? "" : undefined}>
      <span class="tool-key-icon" aria-hidden="true">
        <Icon name="key" size={14} />
      </span>
      <span class="tool-key-name">
        <span class="tool-key-label">{name.text}</span>
        {name.named && <span class="mono muted tool-key-masked">{key.masked}</span>}
      </span>
      <span class="tool-key-used muted num">{week}</span>
      <div class="tool-key-actions">
        {!renaming && !confirming && (
          <button
            ref={triggerRef}
            type="button"
            class="btn tool-key-menu-btn"
            data-part="button"
            data-slot="button"
            data-variant="ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen ? "true" : "false"}
            aria-controls={menuOpen ? menuId : undefined}
            aria-label={t("keyActions", { name: name.text })}
            onClick={() => {
              menuTarget.current = "first";
              setMenuOpen((o) => !o);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const target = e.key === "ArrowUp" ? "last" : "first";
                menuTarget.current = target;
                if (menuOpen) focusMenuItem(target);
                else setMenuOpen(true);
                return;
              }
              if (menuOpen && e.key === "Escape") {
                e.preventDefault();
                setMenuOpen(false);
              }
            }}
          >
            <EllipsisIcon />
          </button>
        )}
      </div>
      {menuOpen && (
        <div class="tool-key-menu-wrap">
          <div
            ref={menuList}
            id={menuId}
            class="tool-key-menu"
            role="menu"
            tabIndex={-1}
            aria-label={t("keyActions", { name: name.text })}
            onKeyDown={onMenuKeyDown}
          >
            {props.canRename && (
              <button
                ref={renameItemRef}
                type="button"
                role="menuitem"
                class="tool-key-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  setConfirming(false);
                  setDraft(key.label ?? "");
                  setRenaming(true);
                }}
              >
                {t("rename")}
              </button>
            )}
            <button
              ref={deleteItemRef}
              type="button"
              role="menuitem"
              class="tool-key-menu-item"
              data-tone="danger"
              disabled={props.only}
              aria-describedby={props.only || lastKey ? lastKeyId : undefined}
              onClick={() => {
                setMenuOpen(false);
                setRenaming(false);
                setConfirming(true);
              }}
            >
              {t("delete")}
            </button>
          </div>
        </div>
      )}
      {confirming && (
        <div class="tool-key-confirm confirm">
          <span class="confirm-question" id={questionId}>
            {question}
          </span>
          <Button variant="danger" busy={deleting} describedBy={questionId} onClick={remove}>
            {t("confirmDelete")}
          </Button>
          <Button
            buttonRef={cancelDeleteRef}
            describedBy={questionId}
            onClick={() => {
              returnFocus.current = true;
              setConfirming(false);
            }}
          >
            {t("cancel")}
          </Button>
        </div>
      )}
      {renaming && (
        <form class="tool-key-rename" onSubmit={rename}>
          <label>
            <span class="sr-only">{t("name")}</span>
            <input
              ref={renameInputRef}
              type="text"
              value={draft}
              maxLength={64}
              placeholder={t("namePlaceholder")}
              onInput={(e) => setDraft(e.currentTarget.value)}
            />
          </label>
          <Button variant="primary" type="submit">
            {t("save")}
          </Button>
          <Button
            onClick={() => {
              returnFocus.current = true;
              setRenaming(false);
            }}
          >
            {t("cancel")}
          </Button>
        </form>
      )}
      {(props.only || lastKey) && (
        <p id={lastKeyId} class="tool-key-note muted">
          {t("lastKey")}
        </p>
      )}
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
  adding: boolean;
  setAdding: (v: boolean) => void;
  primaryAction: boolean;
  /** Called when a key was made, renamed or deleted: the list and the names are read again. */
  onChanged: () => void;
}) {
  const [noRename, setNoRename] = useState(false);
  const p = usePrivacy();
  const list = props.keys.data?.keys;
  const newKey = (
    <Button
      variant={props.primaryAction && !props.adding ? "primary" : "quiet"}
      onClick={() => props.setAdding(true)}
      disabled={props.adding}
    >
      <Icon name="plus" size={14} />
      {t("newKey")}
    </Button>
  );
  return (
    <section class="tools-section card" aria-labelledby="tools-keys">
      <header class="tools-card-head">
        <div class="tools-card-title">
          <h2 id="tools-keys" class="tools-heading">
            {list ? t("keysCount", { count: list.length }) : t("keys")}
          </h2>
          <p class="tools-subtitle muted">{t("keysHint")}</p>
        </div>
        {list && list.length > 0 && newKey}
      </header>
      {noRename && (
        <div class="tools-keys-banner">
          <Notice tone="muted">{t("noRename")}</Notice>
        </div>
      )}
      {props.made.list.map((made) => {
        const item = list?.find((k) => k.id === made.id);
        const label = item?.label ?? made.label;
        return (
          <div key={made.id} class="tools-keys-banner">
            <FreshKey
              value={made.value}
              name={label ? p.id(label) : undefined}
              onClose={() => props.made.close(made.id)}
            />
          </div>
        );
      })}
      {props.adding && (
        <div class="tools-keys-banner">
          <NewKeyForm
            onCancel={() => props.setAdding(false)}
            onMade={(key) => {
              props.made.add(key);
              props.setAdding(false);
              props.onChanged();
            }}
          />
        </div>
      )}
      <Phases
        load={props.keys}
        loading={t("loadingKeys")}
        failed={{ title: t("keysFailed") }}
        isEmpty={(data) => data.keys.length === 0}
        empty={
          <div class="tools-card-body">
            <StatePane
              kind="empty"
              title={t("noKeys")}
              primary={{ label: t("newKey"), onClick: () => props.setAdding(true) }}
            />
          </div>
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
  const [addingKey, setAddingKey] = useState(false);
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
  // One filled button per view: a tool that needs an update wins when the panel handles it, else New key.
  const clients = state.setup.data?.scanned_at
    ? groupTools(state.setup.data.found.clients, options?.extraTools)
    : undefined;
  const hasPanelUpdate = Boolean(
    clients &&
      [...clients.connected, ...clients.installed].some(
        (c) => c.state === "needs-update" && hasPasteText(c) && !options?.toolAction?.(c),
      ),
  );
  return (
    <>
      <PageHead
        title={t("title")}
        actions={
          <Button onClick={refresh}>
            <Icon name="refresh" size={14} />
            {refreshWord()}
          </Button>
        }
      />
      <div class="tools">
        <div class="tools-cols">
          <div class="tools-main">
            <ToolListView
              toolAction={options?.toolAction}
              extraTools={options?.extraTools}
              localPages={options?.localPages}
              notes={options?.notes}
              remote={remote}
              state={state}
              made={made}
              onKeysChanged={changed}
              highlightUpdate={!addingKey}
            />
            <KeysSection
              keys={keys}
              week={usage.data ? requestsByKey(usage.data.keys) : undefined}
              made={made}
              owners={owners}
              adding={addingKey}
              setAdding={setAddingKey}
              primaryAction={!hasPanelUpdate}
              onChanged={changed}
            />
          </div>
          <aside class="tools-side">
            <ConnectionSection state={state} keys={keys.data?.keys} remote={remote} />
          </aside>
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
