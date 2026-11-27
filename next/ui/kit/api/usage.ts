// What the recorded requests add up to (GET /usage; next/README.md, "Usage statistics";
// next/internal/usage/usage.go). The request list (GET /usage/events) has its own module.

import type { Host } from "../host";

/** Tokens the requests reported, added up; a request that reported none adds nothing. */
export interface UsageTokenTotals {
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
}

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
  /** The mean time a request took, in milliseconds; 0 with no requests. Absent from an older core. */
  average_latency_ms?: number;
  /** Absent from an older core. */
  tokens?: UsageTokenTotals;
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
  /**
   * The requests core could not write to its usage files since it started (a full disk, a file it
   * cannot open). They stay in the record core keeps in memory. Absent from a core that predates it.
   */
  write_errors?: number;
  /**
   * The requests core dropped since it started because its disk writer's queue was full. They stay
   * in the record core keeps in memory. Absent from a core that predates it.
   */
  dropped?: number;
  /**
   * When the oldest request core keeps arrived, matching the filter or not, the day files
   * included; absent when it keeps none.
   */
  oldest?: string;
}

export const usageApi = (host: Host) => ({
  /** `query` is the filters and series size, such as `since=200m&bucket=10m&buckets=20`. */
  usage: (query: string) => host.call<UsageSummary>("GET", `/usage?${query}`),
});
