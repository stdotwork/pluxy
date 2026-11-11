// The management API calls the kit's pages use, with the shapes core replies in. Each area's
// calls and reply types live in their own module here; api() puts them together, so a page
// calls useKit().api.<call>.

import type { Host, Reply } from "../host";
import { strings } from "../strings";
import { accountsApi } from "./accounts";
import { extensionsApi } from "./extensions";
import { packagesApi } from "./packages";
import { settingsApi } from "./settings";
import { statusApi } from "./status";

export * from "./accounts";
export * from "./extensions";
export * from "./packages";
export * from "./settings";
export * from "./status";

export const api = (host: Host) => ({
  ...statusApi(host),
  ...accountsApi(host),
  ...packagesApi(host),
  ...extensionsApi(host),
  ...settingsApi(host),
});

export type Api = ReturnType<typeof api>;

const t = strings("api", {
  unreachable: "core could not be reached",
  answered: "core answered {status}",
});

/** A reply's error as one line for people. */
export function problem(r: Reply<unknown>): string | undefined {
  if (r.status >= 200 && r.status < 300) return undefined;
  if (r.status === 0) return t("unreachable");
  return r.error?.message ?? t("answered", { status: r.status });
}
