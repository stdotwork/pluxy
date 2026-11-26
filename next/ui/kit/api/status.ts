// Core's status and reload (next/internal/core/management.go).

import type { Host } from "../host";

export interface Revision {
  revision: number;
  hash?: string;
  at: string;
  error?: string;
}

export interface ExtensionStatus {
  id: string;
  version: string;
  generation: number;
  state: string;
  pid?: number;
  restarts: number;
  crashes_in_a_row: number;
  last_error?: string;
  active_requests: number;
  current: boolean;
}

export interface Status {
  safe_mode: boolean;
  desired: Revision;
  active: Revision;
  extensions: ExtensionStatus[];
  /** The build core runs: its version, commit and build date ("dev" and "unknown" when not known). */
  version: string;
  commit: string;
  built: string;
}

export const statusApi = (host: Host) => ({
  status: () => host.call<Status>("GET", "/status"),
  reload: () => host.call<Status>("POST", "/reload"),
});
