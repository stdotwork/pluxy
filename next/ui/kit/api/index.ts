// The management API calls the kit's pages use, with the shapes core replies in. Each area's
// calls and reply types live in their own module here; api() puts them together, so a page
// calls useKit().api.<call>.

import type { Host, Reply } from "../host";
import { strings } from "../strings";
import { accountsApi } from "./accounts";
import { configApi } from "./config";
import { extensionsApi } from "./extensions";
import { loginsApi } from "./logins";
import { packagesApi } from "./packages";
import { quotaApi } from "./quota";
import { settingsApi } from "./settings";
import { statusApi } from "./status";
import { usageApi } from "./usage";

export * from "./accounts";
export * from "./config";
export * from "./extensions";
export * from "./logins";
export * from "./packages";
export * from "./quota";
export * from "./settings";
export * from "./status";
export * from "./usage";

export const api = (host: Host) => ({
  ...configApi(host),
  ...statusApi(host),
  ...accountsApi(host),
  ...packagesApi(host),
  ...quotaApi(host),
  ...extensionsApi(host),
  ...loginsApi(host),
  ...settingsApi(host),
  ...usageApi(host),
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
