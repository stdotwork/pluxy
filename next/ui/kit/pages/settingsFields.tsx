// The settings the Config page shows as rows: what each is called, what it does, and where it is
// in core's settings (GET and PATCH /settings) and in the settings file. The routing settings are
// Order & fallback's (routing.rules.ts); the token-count estimate is set in the settings file only.

import type { ConfigDocument } from "../api/config";
import { configText as t } from "./configStrings";

export interface Field {
  /** The setting, by its path in core's settings. */
  key: string;
  kind: "switch" | "number" | "text";
  name: string;
  about: string;
  /** Where the setting is in the settings file, by the file's format. */
  file: Record<ConfigDocument["format"], string>;
  placeholder?: string;
  /** A note under the row, after what the setting does. */
  note?: string;
}

/** The rows of the Settings tab's Network, Other computers and Logging sections, in the order they show. */
export const fields = (): { network: Field[]; access: Field[]; logging: Field[] } => ({
  network: [
    {
      key: "proxy-url",
      kind: "text",
      name: t("proxy"),
      about: t("proxyAbout"),
      file: { json: "proxy_url", yaml: "requests.proxy-url" },
      placeholder: t("proxyPlaceholder"),
    },
  ],
  access: [
    {
      key: "management_allow_remote",
      kind: "switch",
      name: t("remote"),
      about: t("remoteAbout"),
      file: { json: "management_allow_remote", yaml: "management.allow-remote" },
    },
    {
      key: "listen",
      kind: "text",
      name: t("listen"),
      about: t("listenAbout"),
      file: { json: "listen", yaml: "server.host and server.port" },
      placeholder: t("listenPlaceholder"),
    },
  ],
  logging: [
    {
      key: "observability.logs.logging-to-file",
      kind: "switch",
      name: t("logToFile"),
      about: t("logToFileAbout"),
      file: { json: "logs.to_file", yaml: "observability.logs.logging-to-file" },
    },
    {
      key: "observability.logs.logs-max-total-size-mb",
      kind: "number",
      name: t("size"),
      about: t("sizeAbout"),
      file: { json: "logs.max_total_size_mb", yaml: "observability.logs.logs-max-total-size-mb" },
    },
    {
      key: "observability.logs.error-logs-max-files",
      kind: "number",
      name: t("kept"),
      about: t("keptAbout"),
      file: { json: "logs.error_logs_max_files", yaml: "observability.logs.error-logs-max-files" },
    },
    {
      key: "observability.usage.usage-statistics-enabled",
      kind: "switch",
      name: t("usage"),
      about: t("usageAbout"),
      file: { json: "usage.enabled", yaml: "observability.usage.usage-statistics-enabled" },
    },
    {
      key: "observability.logs.request-log",
      kind: "switch",
      name: t("requestLog"),
      about: t("requestLogAbout"),
      file: { json: "logs.request_log", yaml: "observability.logs.request-log" },
      note: t("requestLogNote"),
    },
  ],
});

/** Where the management key is in the settings file, by the file's format. */
export const managementKeyFile: Record<ConfigDocument["format"], string> = {
  json: "management_key",
  yaml: "management.secret-key",
};
