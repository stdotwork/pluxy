// What the coding-tool pages (Claude Code, Claude Desktop, Codex, Grok Build, Hermes Agent, Oh My
// Pi, OpenCode, Pi) share, so a fix to one is a fix to all. Each page takes its whole frame from
// here: the add-on, the reading of the settings file pinned to the file shown (usePreview), the
// one-press Connect that makes a key named for the tool (keyFor, client_key_capture), the head that
// says how the tool stands and what to press (ToolHead, ToolTop), the model fields (ModelInput,
// DefaultModel, Unserved), the Start section (StartSection), and the collapsed settings file, key
// and backups (FileKeyBackups).

import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef, useState } from "preact/hooks";
import type { SetupClient } from "../../ui/kit/api";
import type { Reply } from "../../ui/kit/host";
import { ToolMark } from "../../ui/kit/pages/tools";
import { SettingRow } from "../../ui/kit/parts/settingRow";
import { toolState } from "../../ui/kit/parts/toolRow";
import { strings } from "../../ui/kit/strings";
import { Button, Choices, Notice, StateBadge, useAction, useKit, useLoad } from "../../ui/kit/ui";
import { LAUNCHER } from "./addonItems";
import { folderWait } from "./clientHelp";
import { type DeviceCatalog, device } from "./device";
import { historyKey } from "./environment";
import { type Environment, PathText, plainPath } from "./environmentBlock";
import { type Connection, connectionGet, reply } from "./host";
import { keptTerminal, launcherProblem, useDeviceChanged } from "./startCli";
import { nativeReply, plainReply, useShown } from "./toolWords";

const t = strings("toolPage", {
  reading: "Reading {tool}'s settings…",
  unread: "Press Read this file to see how {tool} is set up there.",
  notChecked:
    "pluxy cannot see whether {tool} is installed on this computer. You can connect it now: it uses pluxy once installed.",
  notInstalled:
    "{tool} is not installed on this computer. You can connect it now: it uses pluxy once installed.",
  elsewhere: "{tool}'s settings point at {address}, not at this pluxy.",
  partial: "{tool}'s settings are missing part of what pluxy needs.",
  refused: "pluxy will not change {path}, so {tool} stays as it is.",
  why: "Why: {reason}",
  refusedFix: "Fix the file and press Read this file, or put back the copy from before pluxy changed it.",
  addon: "Changing {tool}'s settings needs the {tool} add-on.",
  safeMode: "Safe mode is on, so tool add-ons are paused.",
  readFailed: "Could not read {tool}'s settings. {reason}",
  stale:
    "Could not read {tool}'s settings again, so what this page shows may be out of date. Nothing is written until they are read. {reason}",
  addonsUnread: "Could not check the {tool} add-on, so nothing is read yet. {reason}",
  addonsAgain: "Could not check the {tool} add-on again, so nothing is read or written until it is. {reason}",
  noPluxy: "Connect waits until the app is connected to pluxy.",
  keyKept: "The key made for {tool} is kept: Connect uses it next time.",
  waiting: "Answer the confirmation window to go on.",
  done: "Done. If {tool} is running, start it again so it reads the new settings.",
  keyFailed: "Could not make a key for {tool}. {reason}",
  noBackup: "There is no copy from before pluxy changed this file.",
  unserved: "Your accounts do not offer {model}.",
  refreshSuggestions: "Refresh suggestions",
  suggestionsFailed: "Could not read the models pluxy serves. {reason}",
  connect: "Connect {tool}",
  update: "Update {tool}",
  save: "Save",
  disconnect: "Disconnect",
  readFile: "Read this file",
  restoreBackup: "Restore backup",
  turnOn: "Turn on add-on",
  openAddons: "Open Add-ons",
  tryAgain: "Try again",
  start: "Start {tool}",
  startTitle: "Start",
  folder: "Folder",
  folderNote: "Where {tool} starts.",
  recent: "Recent folders",
  recentPick: "Recent",
  terminal: "Terminal",
  windowsConsole: "Windows console",
  needFolder: "Start stays off until you choose a folder.",
  needLauncher: "Starting {tool} from here needs the Launcher add-on.",
  more: "Settings file, key and backups",
  whichFile: "Which file",
  fileDefault: "Default",
  program: "Program to start",
  programNote: "Leave empty to use {program} from the system.",
  key: "Key",
  keyMade: "Created for {tool}",
  keyEntered: "Entered by you",
  keyNone: "None yet. Connect makes one.",
  keyNote: "The app keeps the key. {tool} gets it only after you confirm.",
  keyPick: "Which key",
  useExisting: "Use an existing key…",
  enteredLabel: "{tool} (your key)",
  changes: "What Connect changes",
  writesTo: "Writes to",
  addressNow: "Address now",
  addressAfter: "Address after",
  keyInFile: "Key in the file",
  nothing: "None",
  yes: "Yes",
  no: "No",
  backups: "Backups",
  restoreNote: "Puts the file back as it was before pluxy first changed it.",
});

// ---- the key ---------------------------------------------------------------------------------

/** A vault secret as vault_secrets lists it: never its value. */
export interface Secret {
  id: string;
  label: string;
  /** When it was kept, in milliseconds. */
  created?: number;
  /** Set for a key the app made or checked: the add-on it may be given to, the pluxy it belongs
   * to (its address as the app reaches it), pluxy's ID of it and its masked form. */
  client_key?: { client: string; core?: { mode?: string; origin: string }; key_id?: string; masked?: string };
}

/** An address's origin (scheme, host and port), or the text itself when it is not a URL. */
export function originOf(address: string | undefined | null): string | undefined {
  if (!address) return undefined;
  try {
    return new URL(address).origin;
  } catch {
    return address.replace(/\/+$/, "");
  }
}

/** The keys a tool's page can use with the pluxy at address: keys bound to add-on pkg and to
 * that pluxy, and plain secrets the person entered. A key bound to another add-on or another
 * pluxy is left out: the app would refuse to give it. */
export function usableKeys(
  pkg: string,
  address: string | undefined,
  secrets: Secret[] | undefined,
): Secret[] {
  const here = originOf(address);
  return (secrets ?? []).filter((s) => {
    const k = s.client_key;
    if (!k) return true;
    return k.client === pkg && here !== undefined && originOf(k.core?.origin) === here;
  });
}

/**
 * The key Connect sends: the one the person chose, while it can still be used here, else the
 * newest key bound to this add-on and this pluxy. A plain secret is used only when chosen.
 * Undefined when there is none: Connect then makes one (client_key_capture).
 */
export function keyFor(
  pkg: string,
  address: string | undefined,
  secrets: Secret[] | undefined,
  chosen: string,
): Secret | undefined {
  const usable = usableKeys(pkg, address, secrets);
  const bound = usable.filter((s) => s.client_key);
  return usable.find((s) => s.id === chosen) ?? bound.sort((a, b) => (b.created ?? 0) - (a.created ?? 0))[0];
}

/** Whether the app made key for the tool called tool (it carries exactly the tool's name), or the
 * person entered it. */
export const keyMadeFor = (key: Secret, tool: string) => !!key.client_key && key.label === tool;

/** Whether a native call's error is the person's cancel of its confirmation, which stops quietly. */
export const cancelled = (e: string | undefined) => !!e && (e === "cancelled" || e.startsWith("cancelled:"));

/** Whether an add-on turn-on or install confirmation was answered No, which stops quietly. */
export const answeredNo = (e: string | undefined) =>
  cancelled(e) || !!e?.startsWith("native enable cancelled;");

/** A native error as the second sentence of a line: its first letter a capital (pluxy keeps its
 * own lower-case name). */
export const sentence = (s: string) => (s.startsWith("pluxy") ? s : s.charAt(0).toUpperCase() + s.slice(1));

/** What a restore refuses with when pluxy never changed the files. */
const NOT_APPLIED = "is not applied to these targets";

// ---- the add-on ------------------------------------------------------------------------------

/**
 * How an add-on (a tool package on this device) stands for a page: on, missing, off (installed
 * but turned off, or not yet allowed for this release), or paused by safe mode. loading until the
 * catalog is read; unread when it could not be read, so nothing about the add-on is known.
 */
export type AddonState = "loading" | "unread" | "on" | "missing" | "off" | "safe";

export function addonState(catalog: DeviceCatalog | undefined, pkg: string): AddonState {
  if (!catalog) return "loading";
  if (catalog.safe_mode) return "safe";
  const p = catalog.packages.find((x) => x.id === pkg);
  if (!p) return "missing";
  if (!p.enabled || (p.tier === "native" && !p.authorized)) return "off";
  return "on";
}

/** This device's catalog, read again when the native side says it changed. unread: its last read
 * failed, so nobody knows whether an add-on is on, even when an older read said it was; again: an
 * older read is what failed to be read again. */
function useCatalog() {
  const catalog = useLoad(() => device.list().then(plainReply));
  useDeviceChanged(catalog.reload);
  const unread = catalog.phase === "failed";
  return { catalog, data: unread ? undefined : catalog.data, unread, again: unread && !!catalog.data };
}

// ---- how the tool stands ---------------------------------------------------------------------

/** What a tool's add-on reads back of its settings file. */
export interface ReadBack {
  state: "configured" | "not-configured" | "needs-update" | "refused";
  reason?: string;
  base_url?: string | null;
  key_set?: boolean;
}

/** A pinned file of a preview, passed back as it is to every write. */
export interface Pin {
  selected: string;
  target: string;
  link: string | null;
  [k: string]: unknown;
}

/** What every add-on's preview gives: the pinned files and whether pluxy changed them. */
export interface ToolPreview {
  targets: Pin[];
  applied: boolean;
}

/**
 * Whether the tool is installed, as far as anyone can tell: settings that are set up or refused
 * mean it is (there are settings to read); otherwise what pluxy's own look at this computer found
 * (found), or undefined when nobody looked.
 */
export function installedOf(
  found: boolean | undefined,
  state: ReadBack["state"] | undefined,
): boolean | undefined {
  if (state === "configured" || state === "needs-update" || state === "refused") return true;
  return found;
}

/** K3's state for a reading: the word the head and the Tools list show (states.ts, "tool"). */
export function readingState(found: boolean | undefined, settings: ReadBack): string {
  const installed = installedOf(found, settings.state);
  return toolState({ installed: installed === true, state: settings.state }, installed === undefined);
}

// ---- the three tools pluxy does not scan -------------------------------------------------------

/** A tool row for a tool whose settings only the app can read, with ToolRow's props. */
export interface ExtraTool {
  client: SetupClient;
  unchecked: boolean;
  reason?: string;
}

const EXTRA = [
  {
    id: "claude-desktop",
    name: "Claude Desktop",
    pkg: "pluxy/claude-desktop",
    config: "~/.config/Claude-3p",
  },
  { id: "grok-build", name: "Grok Build", pkg: "pluxy/grok-setup", config: "~/.grok/config.toml" },
  { id: "hermes", name: "Hermes Agent", pkg: "pluxy/hermes-setup", config: "~/.hermes/config.yaml" },
] as const;

/**
 * Claude Desktop, Grok Build and Hermes Agent as the Tools list shows them in the app. Each
 * add-on that is on reads its tool's default settings (preview); a reading that has settings in
 * it (configured, needs update, refused) also says the tool is installed. With no other look, a
 * reading of nothing set up, or no reading at all (the add-on missing, off or paused, or the read
 * failing), is marked unchecked: nobody can say whether the tool is there. found is pluxy's own
 * look at this computer (GET /setup, only when pluxy runs here): it says whether a tool with
 * nothing set up is installed, and gives its version.
 */
export async function readExtraTools(
  catalog: DeviceCatalog | undefined,
  found?: SetupClient[],
): Promise<ExtraTool[]> {
  const conn = await connectionGet().catch(() => undefined);
  const core = conn?.signed_in ? conn.target : undefined;
  return Promise.all(
    EXTRA.map(async (x): Promise<ExtraTool> => {
      const seen = found?.find((c) => c.id === x.id);
      let settings: ReadBack | undefined;
      let target: string | undefined;
      if (addonState(catalog, x.pkg) === "on") {
        const r = await nativeReply<ToolPreview & { settings?: ReadBack; config?: ReadBack }>(
          "program_call",
          {
            package: x.pkg,
            op: "preview",
            input: core ? { base_url: core } : {},
          },
        );
        settings = r.body?.settings ?? r.body?.config;
        target = r.body?.targets?.[0]?.target;
      }
      const installed = installedOf(seen?.installed, settings?.state);
      const unchecked = !settings || installed === undefined;
      return {
        client: {
          id: x.id,
          name: x.name,
          installed: installed === true,
          ...(seen?.version ? { version: seen.version } : {}),
          config: target ?? seen?.config ?? x.config,
          state: unchecked || !settings ? "unchecked" : settings.state,
        },
        unchecked,
        ...(settings?.state === "refused" && settings.reason ? { reason: settings.reason } : {}),
      };
    }),
  );
}

// ---- the page's state and its writes ---------------------------------------------------------

let tickets = 0;
/** A fresh ticket for a write's program_call, so program_cancel can withdraw it. */
const newTicket = () => `tool-page-${Date.now().toString(36)}-${++tickets}`;

/** A call to the page's add-on, in the page's environment. */
export type ToolCall = <T>(op: string, input: Record<string, unknown>) => Promise<Reply<T>>;

/**
 * Everything a tool page knows and does, apart from its own settings: its add-on and the
 * launcher's, the pluxy it is connected to, the reading of the file it shows (read as the page
 * opens, when that is the default file; a reading answering for another file is dropped), the
 * keys, the models pluxy serves, and the writes, each of which opens the add-on's confirmation.
 *
 * pkg is the add-on, name the tool's plain name (the key Connect makes carries it), scan the
 * tool's ID in pluxy's look at this computer. selection is what the page sends to read the file it
 * shows ({} for the default), isDefault whether that is the default file, canRead whether it can
 * be read as typed. settings picks the reading out of a preview; onRead fills the page's form.
 */
export function useToolSetup<P extends ToolPreview>(o: {
  pkg: string;
  name: string;
  scan: string;
  env?: Environment;
  selection: Record<string, unknown>;
  isDefault: boolean;
  canRead: boolean;
  settings: (p: P) => ReadBack;
  onRead: (p: P) => void;
}) {
  const { api } = useKit();
  const { catalog, data, unread, again } = useCatalog();
  const addon: AddonState = unread ? "unread" : addonState(data, o.pkg);
  const launcher: AddonState = unread ? "unread" : addonState(data, LAUNCHER);
  const [conn, setConn] = useState<Connection | null>();
  useEffect(() => {
    connectionGet().then(setConn, () => setConn(null));
  }, []);
  const core = conn?.signed_in ? conn.target : undefined;
  const secrets = useLoad(() => nativeReply<{ secrets?: Secret[] }>("vault_secrets"));
  const [chosen, setChosen] = useState("");
  // The key Connect made last, until the vault's list has it: a second press meanwhile uses it
  // instead of making another.
  const [made, setMade] = useState<Secret>();
  const listed = secrets.data?.secrets;
  const known = made && !listed?.some((k) => k.id === made.id) ? [...(listed ?? []), made] : listed;
  const keys = usableKeys(o.pkg, core, known);
  const key = keyFor(o.pkg, core, known, chosen);
  // The models pluxy's accounts serve, read once as the page opens.
  const served = useLoad(() => api.servedModels());
  // pluxy's own look at this computer: only when pluxy runs here, and for this computer's files.
  const [scan, setScan] = useState<SetupClient>();
  const local = !!conn && conn.mode !== "remote";
  useEffect(() => {
    if (!local) return;
    let live = true;
    api.setup().then(
      (r) => {
        if (live) setScan(r.body?.found.clients.find((c) => c.id === o.scan));
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [local]);
  const found = local && !o.env?.selection ? scan : undefined;

  const environment = o.env ? { environment: o.env.selection } : {};
  const call: ToolCall = (op, input) =>
    nativeReply("program_call", { package: o.pkg, op, input, ...environment });

  // ---- the reading
  const selected = JSON.stringify([o.env?.mode ?? null, o.env?.selection ?? null, o.selection]);
  const { preview, readFor, isCurrent } = usePreview<P>(selected);
  const [readingKey, setReadingKey] = useState<string>();
  const [readError, setReadError] = useState<{ key: string; message: string }>();
  const reads = useRef(0);
  const onRead = useRef(o.onRead);
  onRead.current = o.onRead;
  const load = async () => {
    const key = selected;
    const n = ++reads.current;
    setReadingKey(key);
    const r = await readFor(key, () =>
      call<P>("preview", { ...o.selection, ...(core ? { base_url: core } : {}) }),
    );
    if (n === reads.current) setReadingKey(undefined);
    if (!isCurrent(key)) return;
    setReadError(r.error ? { key, message: r.error.message } : undefined);
    if (r.body) onRead.current(r.body);
  };
  const connKnown = conn !== undefined;
  const pending = !!o.env?.pending;
  // The default file is read once each time it is shown: as the page opens, in another
  // environment, and again when the person comes back to it from another file. A reload of what
  // the environment section knows reads nothing again.
  const autoRead = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!o.isDefault) autoRead.current = undefined;
    if (!connKnown || addon !== "on" || pending || !o.isDefault || autoRead.current === selected) return;
    autoRead.current = selected;
    load();
  }, [connKnown, addon, pending, selected]);
  const reading = readingKey === selected;
  const failed = readError?.key === selected ? readError.message : undefined;
  // A reading kept from before a read of the same file failed: it may be out of date, so writes
  // wait for a good one.
  const stale = !!preview && failed !== undefined;
  const settings = preview ? o.settings(preview) : undefined;
  const state = settings ? readingState(found?.installed, settings) : undefined;

  // ---- the writes: one at a time, each answered in the add-on's confirmation. A write goes to the
  // file the page shows as it is sent; while it waits, the file and environment choices are locked
  // (locked), and leaving the page withdraws a confirmation still open (each write call carries a
  // ticket that program_cancel cancels).
  const live = useRef(true);
  const tickets = useRef(new Set<string>());
  useEffect(() => {
    live.current = true;
    const open = tickets.current;
    return () => {
      live.current = false;
      for (const ticket of open) reply("program_cancel", { ticket });
      open.clear();
    };
  }, []);
  /** A call that writes: cancelled with its confirmation if the page goes away first. */
  const writeCall: ToolCall = async (op, input) => {
    const ticket = newTicket();
    tickets.current.add(ticket);
    try {
      return await nativeReply("program_call", { package: o.pkg, op, input, ticket, ...environment });
    } finally {
      tickets.current.delete(ticket);
    }
  };
  /** Whether the page is still open on the file it showed when a write began at. */
  const still = (at: string) => live.current && isCurrent(at);
  const [working, setWorking] = useState<string>();
  const [outside, setOutside] = useState(false);
  const [error, setError] = useState<string>();
  const [done, setDone] = useState(false);
  const [kept, setKept] = useState(false);
  const busyNow = useRef(false);
  /** Runs one write; step gives the error to show, or nothing (done, or cancelled quietly). */
  const run = async (name: string, step: () => Promise<string | undefined>) => {
    if (busyNow.current) return;
    busyNow.current = true;
    setWorking(name);
    setError(undefined);
    setDone(false);
    setKept(false);
    const out = await step();
    busyNow.current = false;
    if (!live.current) return;
    setWorking(undefined);
    if (out) setError(out);
  };
  /** A reply's failure as the page shows it: undefined when it worked, "" for a cancel. */
  const failure = (r: Reply<unknown>, map?: (m: string) => string): string | undefined => {
    if (!r.error) return undefined;
    const m = r.error.message;
    if (cancelled(m)) return "";
    return map ? map(m) : m;
  };
  /** Connect, Update and Save: a key for the tool when it has none here, then its settings. */
  const connect = (input: Record<string, unknown>) =>
    run("connect", async () => {
      const at = selected;
      const shown = preview;
      if (!shown || stale) return undefined;
      let id = key?.id;
      let fresh = false;
      if (!id) {
        const capture = await nativeReply<Secret>("client_key_capture", { label: o.name, client: o.pkg });
        if (!capture.body) {
          secrets.reload();
          const m = capture.error?.message ?? "";
          return cancelled(m) ? undefined : t("keyFailed", { tool: o.name, reason: sentence(m) });
        }
        id = capture.body.id;
        fresh = true;
        setMade(capture.body);
        setChosen(id);
        secrets.reload();
        // The person may have left the page while the key was being made: the settings shown
        // when Connect was pressed are no longer the ones on screen, so nothing is written.
        if (!still(at)) {
          setKept(true);
          return undefined;
        }
      }
      const r = await writeCall("apply", { targets: shown.targets, base_url: core, key: id, ...input });
      secrets.reload();
      if (!still(at)) return undefined;
      const e = failure(r);
      if (e !== undefined) {
        setKept(fresh);
        return e;
      }
      setDone(true);
      load();
      return undefined;
    });
  const undo = (op: "clear" | "restore") =>
    run(op, async () => {
      const at = selected;
      if (!preview || stale) return undefined;
      const r = await writeCall(op, { targets: preview.targets });
      if (!still(at)) return undefined;
      const e = failure(r, (m) => (m.includes(NOT_APPLIED) ? t("noBackup") : m));
      if (e !== undefined) return e;
      load();
      return undefined;
    });
  /** A write of the page's own (Claude Desktop's folder), made with the call it is given (which
   * carries a ticket); after runs once it worked, while the page still shows the same file. */
  const write = (name: string, step: (call: ToolCall) => Promise<Reply<unknown>>, after: () => void = load) =>
    run(name, async () => {
      const at = selected;
      const r = await step(writeCall);
      if (!still(at)) return undefined;
      const e = failure(r);
      if (e !== undefined) return e;
      after();
      return undefined;
    });
  const turnOn = () =>
    run("addon", async () => {
      const r = plainReply(await device.setEnabled(o.pkg, true));
      catalog.reload();
      return r.error && answeredNo(r.error.message) ? "" : failure(r);
    });
  const enterKey = () =>
    run("key", async () => {
      const r = await nativeReply<Secret>("client_key_enter", {
        label: t("enteredLabel", { tool: o.name }),
        client: o.pkg,
      });
      secrets.reload();
      if (r.body) setChosen(r.body.id);
      return failure(r);
    });
  const busy = !!working || reading || outside;
  // Why Connect, Update or Save cannot be pressed now, apart from the page's own form.
  const blocked = connKnown && !core ? [t("noPluxy")] : [];

  return {
    name: o.name,
    pkg: o.pkg,
    scan: o.scan,
    env: o.env,
    addon,
    launcher,
    retryAddons: catalog.reload,
    addonsError: unread ? catalog.error : undefined,
    /** The add-on list was read before, and reading it again failed. */
    addonsAgain: again,
    turnOn,
    conn,
    core,
    call,
    secrets,
    keys,
    key,
    setChosen,
    enterKey,
    served,
    found,
    preview,
    settings,
    state,
    reading,
    failed,
    stale,
    load,
    canRead: o.canRead && addon === "on" && !pending && connKnown,
    isDefault: o.isDefault,
    working,
    busy,
    /** A write or its confirmation is under way (the page's own, or the backups'): the file and
     * environment choices are locked until it ends. */
    locked: !!working || outside,
    /** The backups tell the page while one of their writes waits. */
    setOutside,
    /** Whether a write can start: the add-on on, a reading of the file shown, not out of date,
     * nothing under way. */
    writable: addon === "on" && !busy && !!preview && !stale && !o.env?.pending,
    blocked,
    error,
    done,
    kept,
    setDone,
    connect,
    disconnect: () => undo("clear"),
    restore: () => undo("restore"),
    write,
  };
}

export type ToolSetup = ReturnType<typeof useToolSetup>;

// ---- the head --------------------------------------------------------------------------------

/**
 * The top of a tool's page: its name, its state in K3's words, one muted line of facts, the lines
 * that say what is going on, and the actions for the state. Every page fills it the same way
 * (ToolTop), so the same state reads and acts the same on each.
 */
export function ToolHead(props: {
  tool?: string;
  name: string;
  state?: string;
  facts: (string | undefined)[];
  lines?: ComponentChildren;
  actions?: ComponentChildren;
}) {
  const facts = props.facts.filter(Boolean);
  return (
    <header class="tool-head">
      <div class="tool-head-top">
        <div class="tool-head-id">
          {props.tool && <ToolMark id={props.tool} name={props.name} size="large" />}
          <div class="tool-head-who">
            <p class="tool-head-name">
              <strong>{props.name}</strong>
              {props.state && <StateBadge state={props.state} of="tool" />}
            </p>
            {facts.length > 0 && <p class="tool-head-facts">{facts.join(" · ")}</p>}
          </div>
        </div>
        {props.actions && <div class="tool-head-actions">{props.actions}</div>}
      </div>
      {props.lines && (
        <div class="tool-head-lines" role="status">
          {props.lines}
        </div>
      )}
    </header>
  );
}

/** A link that looks like a button: to Add-ons, where the tool's add-on is missing. */
export function LinkButton(props: { href: string; primary?: boolean; children: ComponentChildren }) {
  return (
    <a class="btn" data-part="button" data-variant={props.primary ? "primary" : "quiet"} href={props.href}>
      {props.children}
    </a>
  );
}

/**
 * The head of a page on the shared parts, from its setup s: start is the page's Start, facts the
 * tool's own facts (its version), paths the files the page shows (masked in privacy mode, after
 * the facts and in the lines that name them), held the reasons the page's form holds Connect
 * (each a line), form the settings Connect writes. failedRead gives the page's own words and
 * button for a read that failed in a way it knows (Claude Desktop's missing folder); lines adds
 * the page's own. connectLabel names Connect and Update when the write does more than connect
 * (Pi's Install plugin, which installs the plugin first).
 */
export function ToolTop(props: {
  s: ToolSetup;
  start?: Start;
  facts: (string | undefined)[];
  paths: string[];
  held?: string[];
  form: () => Record<string, unknown>;
  failedRead?: { line: string; action: ComponentChildren };
  lines?: ComponentChildren;
  connectLabel?: string;
}) {
  const { s } = props;
  const shown = useShown();
  const tool = s.name;
  const path = props.paths.map(shown.path).join(", ");
  // Every reason Connect, Update or Save is held, each said where the button is.
  const held = [...s.blocked, ...(props.held ?? [])];
  const busy = s.busy || !!s.env?.pending;
  const canWrite = s.writable && !!s.core;
  const lines: ComponentChildren[] = [];
  let actions: ComponentChildren[] = [];
  let state: string | undefined;
  const readButton = (
    <Button key="read" variant="primary" onClick={s.load} disabled={busy || !s.canRead}>
      {t("readFile")}
    </Button>
  );
  const tryAgain = (
    <Button key="again" variant="primary" onClick={s.load} disabled={busy || !s.canRead}>
      {t("tryAgain")}
    </Button>
  );
  const connectButton = (label: string) => (
    <Button
      key="connect"
      variant="primary"
      busy={s.working === "connect"}
      onClick={() => s.connect(props.form())}
      disabled={!canWrite || held.length > 0}
    >
      {label}
    </Button>
  );
  const disconnect = s.preview?.applied && (
    <Button key="disconnect" busy={s.working === "clear"} onClick={s.disconnect} disabled={!canWrite}>
      {t("disconnect")}
    </Button>
  );
  if (s.addon === "unread") {
    lines.push(
      <Notice key="addons" tone="danger">
        {t(s.addonsAgain ? "addonsAgain" : "addonsUnread", {
          tool,
          reason: sentence(shown.text(s.addonsError) ?? ""),
        })}
      </Notice>,
    );
    actions.push(
      <Button key="addons" variant="primary" onClick={s.retryAddons}>
        {t("tryAgain")}
      </Button>,
    );
  } else if (s.addon !== "on" && s.addon !== "loading") {
    lines.push(<p key="addon">{s.addon === "safe" ? t("safeMode") : t("addon", { tool })}</p>);
    actions.push(
      s.addon === "off" ? (
        <Button
          key="on"
          variant="primary"
          busy={s.working === "addon"}
          onClick={s.turnOn}
          disabled={!!s.working}
        >
          {t("turnOn")}
        </Button>
      ) : (
        <LinkButton key="open" href="#/device" primary>
          {t("openAddons")}
        </LinkButton>
      ),
    );
  } else if (s.failed !== undefined && !s.preview) {
    if (props.failedRead) {
      lines.push(<p key="failed">{props.failedRead.line}</p>);
      actions.push(props.failedRead.action);
    } else {
      lines.push(
        <Notice key="failed" tone="danger">
          {t("readFailed", { tool, reason: sentence(shown.text(s.failed)) })}
        </Notice>,
      );
      actions.push(tryAgain);
    }
  } else if (!s.settings) {
    if (s.reading || s.addon === "loading" || (s.isDefault && !s.env?.pending)) {
      lines.push(<p key="reading">{t("reading", { tool })}</p>);
      actions.push(
        <Button key="connect" variant="primary" disabled>
          {t("connect", { tool })}
        </Button>,
      );
    } else if (!s.env?.pending) {
      lines.push(<p key="unread">{t("unread", { tool })}</p>);
      actions.push(readButton);
    }
  } else {
    const st = s.settings;
    state = s.state;
    if (st.state === "refused") {
      lines.push(<p key="refused">{t("refused", { path, tool })}</p>);
      if (st.reason) {
        lines.push(
          <p key="why" class="muted">
            {t("why", { reason: shown.text(st.reason) })}
          </p>,
        );
      }
      lines.push(<p key="fix">{t("refusedFix")}</p>);
      actions.push(readButton);
      actions.push(
        <Button key="restore" busy={s.working === "restore"} onClick={s.restore} disabled={!s.writable}>
          {t("restoreBackup")}
        </Button>,
      );
    } else if (st.state === "configured") {
      const start = props.start;
      if (start && s.launcher === "on") {
        actions.push(
          <Button
            key="start"
            variant="primary"
            busy={start.busy}
            onClick={start.start}
            disabled={!start.ready}
          >
            {t("start", { tool })}
          </Button>,
        );
      }
      if (disconnect) actions.push(disconnect);
    } else if (st.state === "needs-update") {
      const address = st.base_url && originOf(st.base_url) !== originOf(s.core) ? st.base_url : undefined;
      lines.push(
        <p key="update">
          {address ? t("elsewhere", { tool, address: shown.text(address) }) : t("partial", { tool })}
        </p>,
      );
      actions.push(connectButton(props.connectLabel ?? t("update", { tool })));
      if (disconnect) actions.push(disconnect);
    } else {
      if (state === "unchecked") lines.push(<p key="unchecked">{t("notChecked", { tool })}</p>);
      if (state === "not_installed") lines.push(<p key="missing">{t("notInstalled", { tool })}</p>);
      actions.push(connectButton(props.connectLabel ?? t("connect", { tool })));
    }
    if (s.stale) {
      // The reading shown may be out of date: it says so, and only reading again is offered.
      lines.push(
        <Notice key="stale" tone="danger">
          {t("stale", { tool, reason: sentence(shown.text(s.failed ?? "")) })}
        </Notice>,
      );
      actions = [tryAgain];
    } else if (st.state === "not-configured" || st.state === "needs-update") {
      for (const h of held) {
        lines.push(
          <p key={h} class="tool-held">
            {h}
          </p>,
        );
      }
    }
  }
  if (props.lines) lines.push(props.lines);
  if (s.locked && s.working !== "key") lines.push(<p key="waiting">{t("waiting")}</p>);
  if (s.error) {
    lines.push(
      <Notice key="error" tone="danger">
        {shown.text(s.error)}
      </Notice>,
    );
  }
  if (s.kept && !s.working) lines.push(<p key="kept">{t("keyKept", { tool })}</p>);
  if (s.done && !s.working) {
    lines.push(
      <Notice key="done" tone="ok">
        {t("done", { tool })}
      </Notice>,
    );
  }
  return (
    <ToolHead
      tool={s.scan}
      name={tool}
      state={state}
      facts={[...props.facts, path]}
      lines={lines.length > 0 ? lines : undefined}
      actions={actions.length > 0 ? actions : undefined}
    />
  );
}

// ---- sections and rows -------------------------------------------------------------------------

export function Section(props: {
  title: string;
  subtitle?: string;
  aside?: ComponentChildren;
  children: ComponentChildren;
}) {
  return (
    <section class="tool-section" aria-label={props.title}>
      <header class="tool-section-head">
        <div class="tool-section-title">
          <h2>{props.title}</h2>
          {props.subtitle && <p class="tool-section-sub muted">{props.subtitle}</p>}
        </div>
        {props.aside && <div class="tool-section-aside">{props.aside}</div>}
      </header>
      {props.children}
    </section>
  );
}

/** Save, in the head of the section whose settings the person edited, while the tool is connected. */
export function SaveButton(props: {
  s: ToolSetup;
  dirty: boolean;
  form: () => Record<string, unknown>;
  held?: string[];
}) {
  const { s } = props;
  if (s.settings?.state !== "configured" || !props.dirty) return null;
  // Every reason Save is held, said beside it (the head says why for Connect and Update).
  const held = s.stale ? [] : [...s.blocked, ...(props.held ?? [])];
  return (
    <span class="tool-save">
      {held.map((h) => (
        <small key={h} class="tool-held">
          {h}
        </small>
      ))}
      <Button
        variant="primary"
        busy={s.working === "connect"}
        onClick={() => s.connect(props.form())}
        disabled={!s.writable || !s.core || held.length > 0}
      >
        {t("save")}
      </Button>
    </span>
  );
}

/** A whole number field, one size on every page; with blank, an empty field is null. */
export function NumberInput(props: {
  id?: string;
  label: string;
  value: number | null;
  onInput: (v: number | null) => void;
  min?: number;
  max?: number;
  step?: number;
  disabled?: boolean;
  blank?: boolean;
}) {
  return (
    <input
      id={props.id}
      class="tool-number"
      type="number"
      aria-label={props.label}
      min={props.min}
      max={props.max}
      step={props.step ?? 1}
      disabled={props.disabled}
      value={props.value === null ? "" : String(props.value)}
      onInput={(e) => {
        const v = e.currentTarget.value.trim();
        props.onInput(v === "" && props.blank ? null : Number(v));
      }}
    />
  );
}

/** The model names pluxy's accounts serve, or undefined until they are read. */
export const servedNames = (s: ToolSetup): Set<string> | undefined =>
  s.served.data?.models ? new Set(s.served.data.models.map((m) => m.id)) : undefined;

/** The models pluxy serves and more, for model fields to suggest (an input's list). */
export function ModelList(props: { id: string; s: ToolSetup; more?: readonly string[] }) {
  const names = [...(props.s.served.data?.models.map((m) => m.id) ?? []), ...(props.more ?? [])];
  return (
    <datalist id={props.id}>
      {[...new Set(names)].map((m) => (
        <option key={m} value={m} />
      ))}
    </datalist>
  );
}

/**
 * A model field: what the person types or picks from what pluxy serves (list), with a line under
 * it when pluxy's accounts do not offer that model. Connect still writes it, for an account added
 * later. known are names that are not models (a tool's own aliases), never said to be missing.
 */
export function ModelInput(props: {
  id?: string;
  label: string;
  value: string;
  onInput: (v: string) => void;
  list: string;
  served: Set<string> | undefined;
  known?: readonly string[];
  placeholder?: string;
}) {
  const model = props.value.trim();
  return (
    <span class="tool-model">
      <input
        id={props.id}
        type="text"
        class="mono"
        aria-label={props.label}
        autocomplete="off"
        spellcheck={false}
        list={props.list}
        placeholder={props.placeholder}
        value={props.value}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
      {!props.known?.includes(model) && <Unserved model={model} served={props.served} />}
    </span>
  );
}

/**
 * A tool's one default model (Oh My Pi, Hermes Agent, Pi): a field that suggests what pluxy serves,
 * and Refresh suggestions, which reads those again; it changes only the suggestions, never the
 * field. note goes under the name. valid says whether the field holds a model ID the tool takes:
 * one it does not take is not said to be missing from pluxy (the head says what is wrong with it).
 */
export function DefaultModel(props: {
  s: ToolSetup;
  name: string;
  note: string;
  value: string;
  onInput: (v: string) => void;
  valid: boolean;
}) {
  const { s } = props;
  const shown = useShown();
  const id = useId();
  return (
    <SettingRow name={props.name} description={props.note}>
      <ModelList id={id} s={s} />
      <span class="tool-inline">
        <ModelInput
          label={props.name}
          value={props.value}
          onInput={props.onInput}
          list={id}
          served={props.valid ? servedNames(s) : undefined}
        />
        <Button busy={s.served.loading} disabled={s.served.loading} onClick={s.served.reload}>
          {t("refreshSuggestions")}
        </Button>
      </span>
      {s.served.error && (
        <small class="tool-unserved">
          {t("suggestionsFailed", { reason: sentence(shown.text(s.served.error)) })}
        </small>
      )}
    </SettingRow>
  );
}

/** The line under a model's field when pluxy's accounts do not offer it (nothing until what they
 * serve is read). Connect still writes the model, for an account added later. */
export function Unserved(props: { model: string; served: Set<string> | undefined }) {
  const shown = useShown();
  const model = props.model.trim();
  if (!props.served || model === "" || props.served.has(model)) return null;
  return <small class="tool-unserved">{t("unserved", { model: shown.text(model) })}</small>;
}

// ---- Start -------------------------------------------------------------------------------------

/**
 * What starting a tool needs: the folder (when it starts in one), the program to start instead of
 * the tool's own (in Settings file, key and backups), the terminal, and the launch itself. client
 * is the name the launcher keeps recent folders under (the tool's CLIENT); recipe is what the
 * add-on is asked for its plain start (Codex's names its target).
 */
export function useStart(o: {
  s: ToolSetup;
  client: string;
  folder: boolean;
  recipe?: Record<string, unknown>;
}) {
  const { s } = o;
  const sel = s.env?.selection ?? null;
  const [cwd, setCwd] = useState("");
  const [override, setOverride] = useState("");
  const [terminal, setTerminal] = useState("automatic");
  const on = s.launcher === "on";
  const state = useLoad(() => nativeReply<{ history: Record<string, string[]> }>("launcher_state"));
  const terminals = useLoad(() =>
    nativeReply<{ terminals: { id: string; label: string }[] }>("launcher_terminals"),
  );
  useDeviceChanged(terminals.reload);
  const action = useAction(state.reload);
  const recent = state.data?.history[historyKey(o.client, sel)] ?? [];
  const shownTerminal = keptTerminal(terminal, terminals.data?.terminals);
  const distro = sel?.wsl.distro;
  /** Asks the add-on for the launch (recipe) and has the launcher start it with launch added. */
  const go = (recipe: Record<string, unknown>, launch: Record<string, unknown>) =>
    action.run(async () => {
      const r = await s.call<Record<string, unknown>>("recipe", recipe);
      if (!r.body) return r;
      return nativeReply("launcher_launch", {
        launch: { ...r.body, ...launch },
        ...(s.env ? { environment: sel } : {}),
      });
    });
  const program = override.trim() ? { override: override.trim() } : {};
  const start = () =>
    go(
      o.recipe ?? {},
      o.folder ? { cwd: cwd.trim(), ...(sel ? {} : { terminal: shownTerminal }), ...program } : program,
    );
  const ready =
    on &&
    !action.busy &&
    !s.env?.pending &&
    plainPath(override, distro) &&
    (!o.folder || (cwd.trim() !== "" && plainPath(cwd, distro)));
  return {
    on,
    cwd,
    setCwd,
    override,
    setOverride,
    terminal: shownTerminal,
    setTerminal,
    terminals,
    recent,
    folder: o.folder,
    distro,
    wsl: !!sel,
    go,
    start,
    ready,
    busy: action.busy,
    error: action.error,
  };
}

export type Start = ReturnType<typeof useStart>;

/**
 * The Start section, one for every tool: the folder (with the recent ones), the terminal, and
 * Start; children adds a tool's own ways to start (Codex's app and service), extra what goes
 * under (the WSL build offer). Without the Launcher add-on it says so, with the way to Add-ons.
 */
export function StartSection(props: {
  s: ToolSetup;
  start: Start;
  folderNote?: string;
  children?: ComponentChildren;
  extra?: ComponentChildren;
}) {
  const { s, start } = props;
  const shown = useShown();
  const tool = s.name;
  const ids = { folder: useId(), terminal: useId() };
  if (!start.on) {
    return (
      <Section title={t("startTitle")}>
        <div class="tool-say">
          <p>{t("needLauncher", { tool })}</p>
          <LinkButton href="#/device">{t("openAddons")}</LinkButton>
        </div>
      </Section>
    );
  }
  const waits = start.folder && folderWait(start.cwd) !== undefined;
  return (
    <Section title={t("startTitle")}>
      {start.folder && (
        <SettingRow name={t("folder")} description={props.folderNote ?? t("folderNote", { tool })}>
          <span class="tool-inline">
            <PathText
              id={ids.folder}
              label={t("folder")}
              value={start.cwd}
              onInput={start.setCwd}
              distro={start.distro}
              bare
            />
            {start.recent.length > 0 && (
              <select
                class="tool-recent"
                aria-label={t("recent")}
                value=""
                onChange={(e) => {
                  if (e.currentTarget.value) start.setCwd(e.currentTarget.value);
                }}
              >
                <option value="">{t("recentPick")}</option>
                {start.recent.map((r) => (
                  <option key={r} value={r}>
                    {shown.path(r)}
                  </option>
                ))}
              </select>
            )}
          </span>
        </SettingRow>
      )}
      {start.folder && (
        <SettingRow name={t("terminal")}>
          {start.wsl ? (
            <span>{t("windowsConsole")}</span>
          ) : (
            start.terminals.data && (
              <select
                id={ids.terminal}
                aria-label={t("terminal")}
                value={start.terminal}
                onChange={(e) => start.setTerminal(e.currentTarget.value)}
              >
                {start.terminals.data.terminals.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.label}
                  </option>
                ))}
              </select>
            )
          )}
        </SettingRow>
      )}
      <div class="tool-say">
        <Button busy={start.busy} onClick={start.start} disabled={!start.ready}>
          {t("start", { tool })}
        </Button>
        {waits && <p class="muted">{t("needFolder")}</p>}
      </div>
      {props.children}
      {!start.wsl && start.terminals.error && (
        <Notice tone="warn">{shown.text(launcherProblem(start.terminals.error))}</Notice>
      )}
      {start.error && !cancelled(start.error) && (
        <Notice tone="danger">{shown.text(launcherProblem(start.error))}</Notice>
      )}
      {props.extra}
    </Section>
  );
}

// ---- Settings file, key and backups ------------------------------------------------------------

/** A path that a narrow column wraps after a slash rather than inside a name. */
function BreakAtSlashes(props: { path: string }) {
  const parts = props.path.split(/(?<=[\\/])/);
  return (
    <>
      {parts.map((p, i) => (
        <span key={`${i}:${p}`}>
          {p}
          {i < parts.length - 1 && <wbr />}
        </span>
      ))}
    </>
  );
}

/** A choice of where the tool's settings file is, as FileChoice shows it. */
export interface FileOption<V extends string> {
  value: V;
  label: string;
  note?: string;
}

/** Which file: the default (with its path), or files the person names (the fields, children),
 * and Read this file. The choice is locked while a write waits (s.locked); the page locks its
 * fields the same way. */
export function FileChoice<V extends string>(props: {
  s: ToolSetup;
  value: V;
  options: FileOption<V>[];
  onChange: (v: V) => void;
  children?: ComponentChildren;
}) {
  const { s } = props;
  const shown = useShown();
  return (
    <div class="tool-files">
      <Choices
        label={t("whichFile")}
        value={props.value}
        choices={props.options.map((o) => ({ ...o, note: o.note && shown.path(o.note) }))}
        onChange={props.onChange}
        disabled={s.locked}
      />
      {props.children}
      <div>
        <Button onClick={s.load} disabled={s.busy || !s.canRead}>
          {t("readFile")}
        </Button>
      </div>
    </div>
  );
}

/** The Default choice's label. */
export const fileDefault = () => t("fileDefault");

/**
 * The collapsed part of a tool's page: which file (files), the program to start (when start is
 * given), the key, what Connect changes, Restore backup and the backups. It opens itself when the
 * file is one pluxy will not change. program is the tool's own program, which an empty field uses.
 */
export function FileKeyBackups(props: {
  s: ToolSetup;
  start?: Start;
  program?: string;
  files: ComponentChildren;
  backups: ComponentChildren;
}) {
  const { s, start } = props;
  const shown = useShown();
  const tool = s.name;
  const refused = s.settings?.state === "refused";
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (refused) setOpen(true);
  }, [refused]);
  const ids = { program: useId() };
  const key = s.key;
  const masked = (k: Secret) => (k.client_key?.masked ? ` · ${shown.text(k.client_key.masked)}` : "");
  return (
    <details class="tool-more" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>{t("more")}</summary>
      {props.files}
      {start && (
        <SettingRow name={t("program")} description={t("programNote", { program: props.program ?? "" })}>
          <PathText
            id={ids.program}
            label={t("program")}
            value={start.override}
            onInput={start.setOverride}
            distro={start.distro}
            placeholder={props.program}
            bare
          />
        </SettingRow>
      )}
      <SettingRow name={t("key")} description={t("keyNote", { tool })}>
        <span class="tool-key">
          <span class="tool-key-is">
            {key
              ? `${keyMadeFor(key, tool) ? t("keyMade", { tool }) : t("keyEntered")}${masked(key)}`
              : s.secrets.phase === "loading"
                ? ""
                : t("keyNone")}
          </span>
          {(s.keys.length > 1 || (s.keys.length === 1 && !key)) && (
            <Choices
              label={t("keyPick")}
              value={key?.id}
              choices={s.keys.map((k) => ({
                value: k.id,
                label: shown.text(k.label),
                note: k.client_key?.masked ? shown.text(k.client_key.masked) : undefined,
              }))}
              onChange={s.setChosen}
            />
          )}
          <Button busy={s.working === "key"} onClick={s.enterKey} disabled={!!s.working || !s.core}>
            {t("useExisting")}
          </Button>
        </span>
        {s.secrets.error && <Notice tone="danger">{shown.text(s.secrets.error)}</Notice>}
      </SettingRow>
      {s.preview && s.settings && !refused && (
        <SettingRow name={t("changes")}>
          <dl class="tool-facts">
            <dt>{t("writesTo")}</dt>
            <dd class="mono">
              {s.preview.targets.map((x) => (
                <span key={x.target}>
                  <BreakAtSlashes path={shown.path(x.target)} />
                </span>
              ))}
            </dd>
            <dt>{t("addressNow")}</dt>
            <dd class="mono">{s.settings.base_url ? shown.text(s.settings.base_url) : t("nothing")}</dd>
            <dt>{t("addressAfter")}</dt>
            <dd class="mono">{s.core ? shown.text(s.core) : t("nothing")}</dd>
            <dt>{t("keyInFile")}</dt>
            <dd>{s.settings.key_set ? t("yes") : t("no")}</dd>
          </dl>
        </SettingRow>
      )}
      <SettingRow name={t("restoreBackup")} description={t("restoreNote")}>
        <Button busy={s.working === "restore"} onClick={s.restore} disabled={!s.writable}>
          {t("restoreBackup")}
        </Button>
      </SettingRow>
      {s.preview && (
        <div class="tool-backups">
          <h3>{t("backups")}</h3>
          {props.backups}
        </div>
      )}
    </details>
  );
}

// ---- fields and the reading ---------------------------------------------------------------------

/** A labelled text field: one line, or with area a few lines; list names a datalist. */
export function Text(props: {
  label: string;
  hint?: string;
  value: string;
  onInput: (v: string) => void;
  list?: string;
  area?: boolean;
  disabled?: boolean;
}) {
  const id = useId();
  const input = (e: { currentTarget: { value: string } }) => props.onInput(e.currentTarget.value);
  return (
    <label class="field" data-part="field" for={id}>
      <span class="label">{props.label}</span>
      {props.area ? (
        <textarea
          id={id}
          rows={4}
          spellcheck={false}
          value={props.value}
          onInput={input}
          disabled={props.disabled}
        />
      ) : (
        <input
          id={id}
          type="text"
          autocomplete="off"
          spellcheck={false}
          list={props.list}
          value={props.value}
          onInput={input}
          disabled={props.disabled}
        />
      )}
      {props.hint && <small class="hint">{props.hint}</small>}
    </label>
  );
}

/**
 * The file preview of a client page, tied to the files its fields select. key names that selection
 * (the request the page sends to read it, with anything else that makes another file: the
 * environment); the page passes it on every render. The preview is the last one readFor took for
 * the current key, and is gone as soon as the key changes, so the page never offers a write for
 * files its fields no longer show.
 *
 * readFor(readKey, call) runs a read: readKey is the key the read was started with, and call
 * makes the request. An answer, successful or failed, for a selection the person has since left is
 * no result (an empty success), so the page shows neither its preview nor its error. isCurrent
 * says the same for a page that acts on the answer itself.
 */
export function usePreview<P>(key: string) {
  const [held, setHeld] = useState<{ key: string; preview: P }>();
  const latest = useRef(key);
  latest.current = key;
  // The preview shown is always the current key's; a changed key also drops what was held, so
  // returning to the old selection does not bring the old preview back.
  useEffect(() => setHeld((h) => (h && h.key !== key ? undefined : h)), [key]);
  const isCurrent = (readKey: string) => readKey === latest.current;
  const readFor = async (readKey: string, call: () => Promise<Reply<P>>): Promise<Reply<P>> => {
    const r = await call();
    if (!isCurrent(readKey)) return { status: 200 };
    if (r.body) setHeld({ key: readKey, preview: r.body });
    return r;
  };
  return { preview: held?.key === key ? held.preview : undefined, readFor, isCurrent };
}
