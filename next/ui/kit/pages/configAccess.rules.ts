// Config's two settings for reaching pluxy from another computer: remote management and the
// address pluxy listens on. A change to either can shut the person out of the pluxy they are using
// right now, so before it is saved this says so, in plain words. Nothing here calls pluxy.

/** An address and a port, as pluxy's `listen` holds them: 127.0.0.1:8317, [::1]:8317 or :8317. */
export interface Listen {
  /** "" for every address; an IPv6 address has no brackets. */
  host: string;
  port: number;
}

/** Reads a listen address. Undefined for text that is not an address and a port. */
export function parseListen(text: string): Listen | undefined {
  const m = /^(?:\[([^\]]*)\]|([^:[\]]*)):(\d{1,5})$/.exec(text.trim());
  if (!m) return undefined;
  const port = Number(m[3]);
  if (port > 65535) return undefined;
  return { host: (m[1] ?? m[2] ?? "").trim(), port };
}

/** Whether pluxy listening on this host answers the computer it runs on only. */
export function isLoopback(host: string): boolean {
  const h = host.toLowerCase();
  return h === "localhost" || h === "::1" || /^127(\.\d{1,3}){3}$/.test(h);
}

/** Whether the host is no one address: every address of the computer. */
function isEverything(host: string): boolean {
  return host === "" || host === "0.0.0.0" || host === "::";
}

const isIp = (host: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":");

/** The warning before a change, as the dialog shows it: what it is about and what follows. */
export interface Warning {
  title: string;
  /** Sentences, one paragraph each. */
  body: string[];
  /** The button that goes ahead, and the one that does not. */
  yes: string;
  no: string;
}

/** What the page knows about how the person reaches pluxy. */
export interface Reach {
  /** False when the person manages pluxy from another computer. */
  fromThisComputer: boolean;
  /** The address the page was opened at (https://host:port), when the host says. */
  reachedAt?: string;
}

const text = {
  offTitle: "Turn off remote management?",
  offBody:
    "You are using pluxy from another computer. Once this is off, pluxy answers only the computer it runs on: this computer is signed out and cannot sign in again.",
  offBack:
    "To turn it back on, open pluxy's settings file on the computer pluxy runs on and set management_allow_remote to true.",
  offYes: "Turn off",
  offNo: "Keep it on",
  listenTitle: "Change the address pluxy listens on?",
  listenYes: "Save the address",
  listenNo: "Do not change it",
  onlyHere:
    "After pluxy next starts it will answer only on the computer it runs on (127.0.0.1 and localhost are that computer's own address). You are using pluxy from another computer, so you could not reach it from here again until the address is changed back on that computer.",
  otherAddress:
    "You reached pluxy at {reached}. After pluxy next starts it will answer only at {host}, so this computer may no longer reach it.",
  otherPort:
    "This page is open at {reached}. After pluxy next starts it answers at port {port}, so this page stops working until you open it at that port.",
};

const fill = (s: string, values: Record<string, string | number>) =>
  s.replace(/\{(\w+)\}/g, (_, k: string) => String(values[k]));

/** The address a page was opened at, as host and port. Undefined when the host does not say. */
function reached(address?: string): { host: string; port: number } | undefined {
  if (!address) return undefined;
  try {
    const u = new URL(address);
    const host = u.hostname.replace(/^\[|\]$/g, "").toLowerCase();
    const port = u.port ? Number(u.port) : u.protocol === "https:" ? 443 : 80;
    return { host, port };
  } catch {
    return undefined;
  }
}

/** The warning before management_allow_remote is saved as `on`, or undefined when nothing is at risk. */
export function remoteWarning(on: boolean, reach: Reach): Warning | undefined {
  if (on || reach.fromThisComputer) return undefined;
  return {
    title: text.offTitle,
    body: [text.offBody, text.offBack],
    yes: text.offYes,
    no: text.offNo,
  };
}

/**
 * The warning before a new listen address is saved, or undefined when the page keeps working. The
 * address is used the next time pluxy starts. Text that is not an address is not warned about: the
 * row says what is wrong with it.
 */
export function listenWarning(listen: string, reach: Reach): Warning | undefined {
  const next = parseListen(listen);
  if (!next) return undefined;
  const body: string[] = [];
  const now = reached(reach.reachedAt);
  if (!reach.fromThisComputer && isLoopback(next.host)) {
    body.push(text.onlyHere);
  } else if (
    !reach.fromThisComputer &&
    now &&
    isIp(now.host) &&
    isIp(next.host) &&
    !isEverything(next.host) &&
    next.host.toLowerCase() !== now.host
  ) {
    body.push(fill(text.otherAddress, { reached: `${now.host}:${now.port}`, host: next.host }));
  }
  // A port that is not the one in the page's address: a page reached straight ends there. A proxy
  // or a container's port mapping in between makes that untrue, which the person knows and the
  // dialog lets them go on past.
  if (now && next.port !== 0 && next.port !== now.port) {
    body.push(fill(text.otherPort, { reached: `${now.host}:${now.port}`, port: next.port }));
  }
  if (body.length === 0) return undefined;
  return { title: text.listenTitle, body, yes: text.listenYes, no: text.listenNo };
}

/** The warning before the setting at key is saved with value, or undefined. */
export function accessWarning(key: string, value: unknown, reach: Reach): Warning | undefined {
  if (key === "management_allow_remote") return remoteWarning(value === true, reach);
  if (key === "listen" && typeof value === "string") return listenWarning(value, reach);
  return undefined;
}
