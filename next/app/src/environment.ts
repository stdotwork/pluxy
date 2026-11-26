// Where a client page sets up and starts its client: this computer, or a WSL distribution as one
// of its users. The logic of the environment block (environmentBlock.tsx), apart from it so its
// unit tests need no webview. The host keeps a record of each WSL environment it checked and
// validates the page's choice on every call; the page keeps only its last choice, per client
// package, in the window's storage.

/** A page's choice: a WSL distribution and user, or null for this computer ("Windows local").
 * Sent as the `environment` argument of program_call, program_status and launcher_launch. */
export type Selection = { wsl: { distro: string; user: string } } | null;

/** The host's record of a WSL environment, from pluxy/wsl's `check` (environments_get,
 * environment_add). */
export interface EnvRecord {
  distro: string;
  user: string;
  uid: number;
  /** The user's home in the distribution. */
  home: string;
  /** `amd64` or `arm64`: which Linux build runs there. */
  arch: string;
  /** 1 or 2, when known. */
  wsl?: number | null;
  automount?: string | null;
  drives?: unknown[];
  checked_at: number;
}

/** A distribution as pluxy/wsl's `distros` lists it. */
export interface Distro {
  name: string;
  default: boolean;
  version: number | null;
}

/** A user as pluxy/wsl's `users` lists it. */
export interface WslUser {
  name: string;
  uid: number;
  home: string;
  default: boolean;
  root: boolean;
}

/** The program that lists distributions and their users. */
export const WSL_PACKAGE = "pluxy/wsl";

/** The window's storage name of a client package's last choice. */
export const prefKey = (pkg: string) => `environment.${pkg}`;

/** The choice as kept in the window's storage: nothing for this computer. */
export const encodeSelection = (s: Selection): string | null =>
  s ? JSON.stringify({ wsl: { distro: s.wsl.distro, user: s.wsl.user } }) : null;

const named = (v: unknown): v is string => typeof v === "string" && v !== "";

/** The kept choice; anything that is not a WSL distribution and user is this computer. */
export function parseSelection(raw: string | null): Selection {
  if (!raw) return null;
  try {
    const w = JSON.parse(raw)?.wsl;
    if (w && named(w.distro) && named(w.user)) return { wsl: { distro: w.distro, user: w.user } };
  } catch {
    // Not JSON: this computer.
  }
  return null;
}

export const sameSelection = (a: Selection, b: Selection) =>
  a === null || b === null ? a === b : a.wsl.distro === b.wsl.distro && a.wsl.user === b.wsl.user;

/** The host's record of the chosen environment, when it has one. */
export const recordOf = (records: EnvRecord[] | undefined, s: Selection): EnvRecord | undefined =>
  s ? records?.find((r) => r.distro === s.wsl.distro && r.user === s.wsl.user) : undefined;

/** The launcher's key for a client's recent folders: the host keys WSL launches by distribution and
 * user as well. */
export const historyKey = (client: string, s: Selection) =>
  s ? `${client} (WSL ${s.wsl.distro}/${s.wsl.user})` : client;

/** rel inside a Linux home folder. */
export const inHome = (home: string, rel: string) => `${home.replace(/\/+$/, "")}/${rel}`;

/** The platform of the Linux build a record's distribution runs (device_install_wsl). */
export const linuxPlatform = (r: EnvRecord) => `linux/${r.arch}`;

/** A typed path in a WSL environment: a UNC path to the selected distribution's own share
 * (`\\wsl.localhost\<distro>\<path>` or `\\wsl$\<distro>\<path>`, in any case, with `\` or `/`)
 * converted to `/<path>`; any other UNC path refused; anything else as typed. */
export type Checked = { path: string; converted: boolean } | { refused: true };

export function linuxPath(value: string, distro: string): Checked {
  const v = value.trim();
  if (!/^[\\/]{2}/.test(v)) return { path: value, converted: false };
  const [host, share, ...rest] = v.slice(2).split(/[\\/]+/);
  // Windows matches UNC host and share names regardless of case, and wsl.exe takes a distribution's
  // name in any case (its usage text: "not case sensitive"), so no two distributions differ by case.
  const ours =
    (host?.toLowerCase() === "wsl.localhost" || host?.toLowerCase() === "wsl$") &&
    share?.toLowerCase() === distro.toLowerCase();
  if (!ours) return { refused: true };
  return { path: `/${rest.filter(Boolean).join("/")}`, converted: true };
}

/** The host's refusal when the package's Linux build is not installed for WSL. */
export const linuxBuildMissing = (e?: string) =>
  !!e && e.includes("Linux build") && e.includes("is not installed");

/** Users as the block lists them: in pluxy/wsl's order, root last. */
export const usersInOrder = (users: WslUser[]) => [
  ...users.filter((u) => !u.root),
  ...users.filter((u) => u.root),
];
