// Core's basic and routing settings, by today's core's names (GET and PATCH /settings;
// next/README.md, the settings table; next/internal/core/manageconfig.go). Several pages show
// the same settings, so they all read and change them here.

import type { Host } from "../host";

export interface CoreSettings {
  routing: {
    strategy: "round-robin" | "fill-first" | "weighted-round-robin" | "least-active";
    "session-affinity": boolean;
    /** A duration such as "1h" or "30m". */
    "session-affinity-ttl": string;
    "force-model-prefix": boolean;
    retry: {
      /** null while the config leaves rounds to this core's own rules. */
      "request-retry": number | null;
      "max-retry-credentials": number;
      /** Seconds. */
      "max-retry-interval": number;
    };
    cooldown: {
      "disable-cooling": boolean;
      /** Seconds; 0 is this core's backoff, -1 none. */
      "transient-error-cooldown-seconds": number;
      "quota-response-mode": "retryable" | "usage-limit";
    };
  };
  observability: {
    logs: {
      "logging-to-file": boolean;
      "logs-max-total-size-mb": number;
      "request-log": boolean;
      "error-logs-max-files": number;
    };
    usage: { "usage-statistics-enabled": boolean };
  };
  /** This core's own setting. */
  token_count_estimate: boolean;
}

type Change<T> = { [K in keyof T]?: T[K] extends Record<string, unknown> ? Change<T[K]> : T[K] };

/** The settings a PATCH names; the rest stay as they are. */
export type SettingsChange = Change<CoreSettings>;

export const settingsApi = (host: Host) => ({
  coreSettings: () => host.call<CoreSettings>("GET", "/settings"),
  /** Changes the settings change names and replies with them all; 400 for a name or value core does not take. */
  changeCoreSettings: (change: SettingsChange) => host.call<CoreSettings>("PATCH", "/settings", change),
});
