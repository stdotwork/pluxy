// What the recorded requests add up to (GET /usage; next/README.md, "Usage statistics";
// next/internal/usage/usage.go). The request list (GET /usage/events) has its own module.

import type { Host } from "../host";

/**
 * The tokens of many requests, added up. A request whose upstream reported none of a kind adds 0.
 * Input holds the cached ones and output the reasoning ones; total is input plus output.
 */
export interface UsageTokenTotals {
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  /** Input tokens read from a cache. */
  cached_tokens: number;
  /** Input tokens written to a cache. */
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
  /** The mean, in whole milliseconds; core keeps no percentiles. */
  average_latency_ms?: number;
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
  /** What `by=` in the query asked the requests to be grouped by, and the groups. */
  by?: "model" | "provider" | "account" | "key";
  breakdown?: UsageGroup[];
  /** When the oldest request core still holds arrived: a range that starts before it is not whole. */
  oldest?: string;
  /** Requests core let go of to stay within its limits, and those it could not queue, since it started. */
  evicted?: number;
  dropped?: number;
  /**
   * The requests core could not write to its usage files since it started (a full disk, a file it
   * cannot open). They stay in the record core keeps in memory. Absent from a core that predates it.
   */
  write_errors?: number;
}

export const usageApi = (host: Host) => ({
  /** `query` is the filters and series size, such as `since=200m&bucket=10m&buckets=20`. */
  usage: (query: string) => host.call<UsageSummary>("GET", `/usage?${query}`),
});
