// The models core serves, with who serves them (next/internal/core/management.go).

import type { Host } from "../host";

/** A model of GET /v1/models under the name clients use, and who serves it. */
export interface ServedModel {
  /** The name clients ask for. */
  id: string;
  owned_by?: string;
  display_name?: string;
  description?: string;
  /** Its name upstream, which differs from `id` for an alias or a prefixed name. */
  upstream: string;
  /** The extension of the account whose entry the list shows. */
  extension: string;
  /** The IDs of every account that serves the name. */
  accounts: string[];
}

export const systemApi = (host: Host) => ({
  servedModels: () => host.call<{ models: ServedModel[] }>("GET", "/models"),
});
