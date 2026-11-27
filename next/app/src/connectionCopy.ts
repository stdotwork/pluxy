// Every word the person reads on the first launch screen, on the not-running pane and about where
// pluxy runs, and the rules that choose between them from the app's connection. Nothing here reads
// the native side: it works from the Connection the native side reports, so the words and the cases
// are tested without a window. The settings rows and the close prompts keep their words beside
// their code (startup.tsx, window.tsx).

import type { Connection, Mode } from "./host";

export const FIRST = {
  heading: "Where should pluxy run?",
  lead: "pluxy connects your coding tools to your AI accounts. It needs to run somewhere first.",
  recommended: "Recommended",
  here: {
    title: "On this computer",
    about: "pluxy runs here while this app is open. Nothing to type.",
    note: "Most people pick this. The app starts pluxy and stops it when you quit.",
  },
  there: {
    title: "On another computer",
    about: "Connect to a pluxy that already runs: on a server, or on its own on this computer.",
  },
  later: "You can change this later in Settings, under Where pluxy runs.",
  startingHeading: "Starting pluxy on this computer…",
  startingAbout: "This takes a few seconds the first time.",
  damaged: "The app could not read its saved settings, so it is starting fresh.",
};

export const FORM = {
  address: "Address",
  certificate: "It uses a certificate I made myself",
  certificateFile: "Certificate file",
  certificateHint: "The full path of its .pem file.",
  continue: "Continue",
  connect: "Connect",
  waiting: "Waiting for the key…",
  note: "Next, a small window asks for its management key. On the computer it runs on, pluxy key management --show-key prints it.",
};

export const HINT = {
  empty: "Such as https://pluxy.example, or http://127.0.0.1:8317 on this computer.",
  local: "On this computer. It runs on its own; the app only connects to it.",
  remote: "On another computer, over https.",
  remoteWithCertificate: "Over https, trusting that certificate file.",
  notAnAddress: "Start with https:// or http://, then the computer's name or number.",
  needsHttps:
    "Another computer needs an https:// address, so its key is never sent where others can read it.",
};

export interface AddressKind {
  ok: boolean;
  /** The mode the native side holds this address to; unset when the address is not usable. */
  mode?: Extract<Mode, "local" | "remote">;
  hint: string;
}

/**
 * What kind of address this is, by the rules the native side holds each mode to (conn.rs
 * local_origin, remote_origin): http only on a loopback host; https anywhere, and only the remote
 * mode carries a certificate file, so an https address with one is taken as remote. Anything else
 * is refused here, before any call.
 */
export function addressKind(text: string, certificateFile?: string): AddressKind {
  const value = text.trim();
  if (!value) return { ok: false, hint: HINT.empty };
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return { ok: false, hint: HINT.notAnAddress };
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return { ok: false, hint: HINT.notAnAddress };
  const onThisComputer = /^(localhost|127(\.\d{1,3}){3}|\[::1\])$/.test(url.hostname);
  if (url.protocol === "https:" && certificateFile?.trim())
    return { ok: true, mode: "remote", hint: HINT.remoteWithCertificate };
  if (onThisComputer) return { ok: true, mode: "local", hint: HINT.local };
  if (url.protocol === "https:") return { ok: true, mode: "remote", hint: HINT.remote };
  return { ok: false, hint: HINT.needsHttps };
}

// ---- what the native side says when something went wrong ------------------------------------------

/** The texts check_key (conn.rs) gives when nothing answered at the address. */
const NO_ANSWER: Record<string, string> = {
  "TLS verification or handshake failed; check the certificate authority":
    "Its certificate could not be checked. If it uses one you made yourself, give the app its certificate file.",
  "The connection to pluxy timed out.": "It did not answer in time.",
  "The host name could not be resolved.": "Its name could not be found.",
  "The connection to pluxy failed (I/O).": "The connection to it failed.",
  "The reply from pluxy was not a valid HTTP response.": "Something answered, but not as pluxy does.",
  "Could not reach pluxy, or could not read its reply.": "It could not be reached.",
  "No pluxy answered at this address.": "No pluxy answered at this address.",
};

const REFUSED_KEY = "That pluxy refused the key. Check it and try again.";

/** Whether the native side's text says that nothing answered at the address. */
export const noAnswer = (error?: string) => !!error && Object.hasOwn(NO_ANSWER, error);

/** Whether the native side's text says that pluxy answered and refused the key. */
export const keyRefused = (error?: string) => error === "This management key was not accepted.";

/**
 * The plain reason for one of the native side's own error texts, or undefined for any other text,
 * which the screens then show as it is (under Details on the pane). certificateFile is the file
 * the connection was given: the native side names it before its reason.
 */
export function plainReason(error: string | undefined, certificateFile?: string): string | undefined {
  if (!error) return undefined;
  if (keyRefused(error)) return REFUSED_KEY;
  if (noAnswer(error)) return NO_ANSWER[error];
  if (error === "native key entry is already open") return "The key window is already open.";
  if (error === "the app is quitting") return "The app is quitting.";
  const prefix = "certificate authority ";
  if (error.startsWith(prefix)) {
    const rest = error.slice(prefix.length);
    const at = rest.lastIndexOf(": ");
    if (at > 0) return `The certificate file ${rest.slice(0, at)} could not be read.`;
  }
  if (certificateFile && error.startsWith(`${certificateFile}: `))
    return `The certificate file ${certificateFile} could not be read.`;
  return undefined;
}

/** The plain reason where there is one, else the text as it is. */
export const plainError = (error: string | undefined, certificateFile?: string) =>
  plainReason(error, certificateFile) ?? error;

/** The host of an address (its name and port), for the words that name it. */
export function hostOf(address?: string): string | undefined {
  try {
    return address ? new URL(address).host : undefined;
  } catch {
    return undefined;
  }
}

// ---- the not-running pane ---------------------------------------------------------------------------

export const PANE = {
  stopped: "pluxy is stopped",
  stoppedFromTray: "It was stopped from the tray.",
  notStarted: "The app is set not to start it when it opens.",
  stoppedWithError: "pluxy stopped with an error",
  restarting: "pluxy stopped with an error. The app is starting it again.",
  paused: "pluxy kept stopping, so the app stopped restarting it",
  missing: "No pluxy is installed",
  missingWithProgram: "There is a pluxy program at {path}. The app can copy it in and run it.",
  missingWithoutProgram: "This copy of the app has no pluxy to start.",
  signedOutHere: "Not signed in to pluxy on this computer",
  signedOut: "Not signed in to pluxy at {host}",
  signedOutAbout: "The app needs its management key.",
  silentHere: "pluxy on this computer is not answering",
  silent: "pluxy at {host} is not answering",
  cannotHere: "Cannot connect to pluxy on this computer",
  cannot: "Cannot connect to pluxy at {host}",
  managedSilent: "pluxy is not answering",
  safeMode: "Safe mode is on: add-ons are paused.",
  openAddOns: "Open Add-ons",
  details: "Details",
};

/** What a button of the pane does; ACTIONS has its words. */
export type Act = "start" | "start-now" | "restart" | "adopt" | "key" | "connect" | "where";

export const ACTIONS: Record<Act, { label: string; busy?: string }> = {
  start: { label: "Start pluxy", busy: "Starting…" },
  "start-now": { label: "Start now", busy: "Starting…" },
  restart: { label: "Restart pluxy", busy: "Restarting…" },
  adopt: { label: "Use this program", busy: "Answer the confirmation window…" },
  key: { label: "Enter management key", busy: "Waiting for the key…" },
  connect: { label: "Connect to a pluxy that is already running" },
  where: { label: "Change where pluxy runs" },
};

/** The pane's colour: neutral for a plain stop, amber for something the person can fix in a moment, red for a fault. */
export type Tone = "neutral" | "warn" | "danger";

export interface Pane {
  /** Which case this is, for tests and the pane's own hooks. */
  key:
    | "stopped"
    | "stopped-with-error"
    | "restarting"
    | "paused"
    | "missing"
    | "not-answering-managed"
    | "signed-out"
    | "not-answering"
    | "cannot-connect";
  tone: Tone;
  heading: string;
  /** Where the app's own pluxy runs, and which version: said under every heading of that pluxy. */
  about?: string;
  /** One or two lines under the heading. */
  lines: string[];
  /** An address or path shown in mono: the address of a pluxy that runs on its own, or a program. */
  mono?: string;
  /** What the native side said, when it said something the lines do not already say. */
  details?: string;
  primary: Act;
  secondary?: Act;
  /** The app is trying again by itself. */
  working?: boolean;
}

const fill = (text: string, values: Record<string, string>) =>
  text.replace(/\{(\w+)\}/g, (m, name: string) =>
    Object.hasOwn(values, name) ? (values[name] as string) : m,
  );

/** On this computer, version 0.1.0. — said under every heading of the app's own pluxy. */
export function whereLine(version?: string): string | undefined {
  return version ? `On this computer, version ${version}.` : undefined;
}

/** The native text MISSING (conn.rs) says nothing the heading does not. */
const NO_PLUXY = "No core is installed yet";

/**
 * The case a connection is in when the window cannot show pluxy's pages, or undefined when it can
 * (signed in, with an address) or when nothing is chosen yet (the first launch screen). The case
 * comes from the connection alone.
 */
export function paneOf(c: Connection): Pane | undefined {
  if (!c.mode) return undefined;
  if (c.target && c.signed_in) return undefined;
  if (c.mode === "managed") return managedPane(c);
  return connectedPane(c);
}

function managedPane(c: Connection): Pane {
  const core = c.core;
  const version = core?.version;
  const detail = core?.detail ?? c.error;
  if (!core || core.state === "missing") {
    const program = core?.adopt;
    const other = detail && !detail.startsWith(NO_PLUXY) ? detail : undefined;
    return program
      ? {
          key: "missing",
          tone: "neutral",
          heading: PANE.missing,
          lines: [PANE.missingWithProgram],
          mono: program,
          details: other,
          primary: "adopt",
          secondary: "where",
        }
      : {
          key: "missing",
          tone: "neutral",
          heading: PANE.missing,
          lines: [PANE.missingWithoutProgram],
          details: other,
          primary: "connect",
        };
  }
  const about = whereLine(version);
  if (core.state === "recovering")
    return {
      key: "restarting",
      tone: "warn",
      heading: PANE.restarting,
      about,
      lines: [],
      details: detail,
      primary: "start-now",
      secondary: "where",
      working: true,
    };
  if (core.state === "paused")
    return {
      key: "paused",
      tone: "danger",
      heading: PANE.paused,
      about,
      lines: [],
      details: detail,
      primary: "start",
      secondary: "where",
    };
  if (core.state === "stopped") {
    if (detail)
      return {
        key: "stopped-with-error",
        tone: "danger",
        heading: PANE.stoppedWithError,
        about,
        lines: [],
        details: detail,
        primary: "start",
        secondary: "where",
      };
    const why =
      core.stop_reason === "stopped_by_user"
        ? [PANE.stoppedFromTray]
        : core.stop_reason === "not_started"
          ? [PANE.notStarted]
          : [];
    return {
      key: "stopped",
      tone: "neutral",
      heading: PANE.stopped,
      about,
      lines: why,
      primary: "start",
      secondary: "where",
    };
  }
  // The app's own pluxy runs, yet the app holds no address or key for it: a moment while it signs
  // in. The native side names no case for it, so the pane says only that nothing answers, and the
  // one thing that can help is starting it again.
  return {
    key: "not-answering-managed",
    tone: "danger",
    heading: PANE.managedSilent,
    about,
    lines: [],
    details: detail,
    primary: "restart",
    secondary: "where",
  };
}

function connectedPane(c: Connection): Pane {
  const here = c.mode === "local";
  const address = c.target ?? c.url;
  const host = hostOf(address);
  const named = (onThisComputer: string, atHost: string, bare: string) =>
    here ? onThisComputer : host ? fill(atHost, { host }) : bare;
  const reason = plainReason(c.error, c.ca_file);
  // Text the native side gave that the plain reason does not stand in for.
  const raw = c.error && reason === undefined ? c.error : undefined;
  if (!c.target) {
    return {
      key: "cannot-connect",
      tone: "danger",
      heading: named(PANE.cannotHere, PANE.cannot, "Cannot connect to pluxy"),
      lines: reason ? [reason] : [],
      mono: address,
      details: raw,
      primary: "where",
    };
  }
  if (noAnswer(c.error))
    return {
      key: "not-answering",
      tone: "danger",
      heading: named(PANE.silentHere, PANE.silent, "pluxy is not answering"),
      lines: reason ? [reason] : [],
      mono: address,
      primary: "key",
      secondary: "where",
    };
  return {
    key: "signed-out",
    tone: "warn",
    heading: named(PANE.signedOutHere, PANE.signedOut, "Not signed in to pluxy"),
    lines: [reason ?? PANE.signedOutAbout],
    mono: address,
    details: raw,
    primary: "key",
    secondary: "where",
  };
}

// ---- Where pluxy runs -------------------------------------------------------------------------------

export const WHERE = {
  title: "Where pluxy runs",
  now: "Now",
  where: "Where",
  thereTitle: "Connect to a pluxy that is already running",
  thisApp: "This app",
  advanced: "Versions and advanced",
  runningHere: "Running on this computer",
  connectedTo: "Connected to {host}",
  connectedHere: "Connected to pluxy on this computer",
  copy: "Copy",
  copied: "Copied",
  runsManaged: "This app runs pluxy and stops it when you quit the app.",
  runsLocal: "It runs on its own; the app only connects to it.",
  runsRemote: "Over https.",
  runsRemoteWithCertificate: "Over https, trusting your certificate file.",
  signOut: "Sign out",
  switchHere: "Switch to this computer",
  switching: "Switching…",
  leaves: "Switching stops using {host}. Tools set up for it stop getting answers until you switch back.",
  leavesManaged: "Switching stops the pluxy this app runs.",
};

/** The state in a few words, for the Now section. */
export function stateHeading(c: Connection): string {
  const pane = paneOf(c);
  if (pane) return pane.heading;
  if (c.mode === "managed") return WHERE.runningHere;
  if (c.mode === "local") return WHERE.connectedHere;
  return fill(WHERE.connectedTo, { host: hostOf(c.target ?? c.url) ?? "another computer" });
}

/** The sentence on who runs the pluxy. */
export function whoRuns(c: Connection): string | undefined {
  if (c.mode === "managed") return WHERE.runsManaged;
  if (c.mode === "local") return WHERE.runsLocal;
  if (c.mode === "remote") return c.ca_file ? WHERE.runsRemoteWithCertificate : WHERE.runsRemote;
  return undefined;
}

/** What switching away from the way the app reaches pluxy now stops, said before the person switches. */
export function leaves(c: Connection): string | undefined {
  if (c.mode === "managed") return WHERE.leavesManaged;
  const host = hostOf(c.target ?? c.url);
  return host ? fill(WHERE.leaves, { host }) : undefined;
}

export { fill };
