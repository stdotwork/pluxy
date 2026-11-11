// What the recorded requests add up to (GET /usage; next/README.md, "Usage statistics";
// next/internal/usage/usage.go). The request list (GET /usage/events) has its own module.

import type { Host } from "../host";

export interface UsageGroup {
  /** The model, extension, account or client key ID the requests share. */
  name: string;
  /** An account's extension (in the accounts breakdown only). */
  provider?: string;
  total_requests: number;
  success_count: number;
  failure_count: number;
  /** 0 to 1. */
  success_rate: number;
}

/** The requests that arrived within one step of the series. */
export interface UsageBucket {
  start: string;
  requests: number;
  failure_count: number;
  total_tokens: number;
}

export interface UsageSummary extends Omit<UsageGroup, "name" | "provider"> {
  enabled: boolean;
  models: UsageGroup[];
  providers: UsageGroup[];
  accounts: UsageGroup[];
  keys: UsageGroup[];
  bucket_seconds: number;
  /** Oldest first, ending with the step that holds now; steps with no requests are included. */
  series: UsageBucket[];
}

export const usageApi = (host: Host) => ({
  /** `query` is the filters and series size, such as `since=200m&bucket=10m&buckets=20`. */
  usage: (query: string) => host.call<UsageSummary>("GET", `/usage?${query}`),
});
